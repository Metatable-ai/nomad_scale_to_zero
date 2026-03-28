// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	registryNamespacePrefix = "scale-to-zero/registry"
	registryHostsSetKey     = registryNamespacePrefix + "/hosts"
	registryHostKeyPrefix   = registryNamespacePrefix + "/hosts/"
	readyEndpointKeyPrefix  = registryNamespacePrefix + "/ready-endpoints/"
	wakeLockKeyPrefix       = registryNamespacePrefix + "/wake-locks/"
	activityKeyPrefix       = "scale-to-zero/activity/"
)

var releaseWakeLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

type RegistrySyncResult struct {
	SyncedCount  int
	RemovedCount int
}

type stateStore interface {
	Ping(ctx context.Context) error
	LookupWorkload(ctx context.Context, host string) (WorkloadRegistration, bool, error)
	SyncWorkloads(ctx context.Context, workloads []WorkloadRegistration) (RegistrySyncResult, error)
	LookupReadyEndpoint(ctx context.Context, host string) (*url.URL, bool, error)
	SetReadyEndpoint(ctx context.Context, host string, endpoint *url.URL, ttl time.Duration) error
	ClearReadyEndpoint(ctx context.Context, host string) error
	AcquireWakeLock(ctx context.Context, host, owner string, ttl time.Duration) (bool, error)
	ReleaseWakeLock(ctx context.Context, host, owner string) error
	GetJobSpec(ctx context.Context, key string) ([]byte, bool, error)
	SetActivity(ctx context.Context, service string, at time.Time) error
}

type redisStateStore struct {
	client *redis.Client
}

func newRedisStateStore(cfg Config) *redisStateStore {
	return &redisStateStore{
		client: redis.NewClient(&redis.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
		}),
	}
}

func (s *redisStateStore) Ping(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is not configured")
	}

	return s.client.Ping(ctx).Err()
}

func (s *redisStateStore) LookupWorkload(ctx context.Context, host string) (WorkloadRegistration, bool, error) {
	if s == nil || s.client == nil {
		return WorkloadRegistration{}, false, errors.New("redis client is not configured")
	}

	host = normalizeHost(host)
	if host == "" {
		return WorkloadRegistration{}, false, nil
	}

	raw, err := s.client.Get(ctx, registryHostKey(host)).Result()
	switch {
	case err == nil:
	case errors.Is(err, redis.Nil):
		return WorkloadRegistration{}, false, nil
	default:
		return WorkloadRegistration{}, false, fmt.Errorf("lookup workload %s: %w", host, err)
	}

	var record WorkloadRegistration
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return WorkloadRegistration{}, false, fmt.Errorf("decode workload %s: %w", host, err)
	}

	record = normalizeWorkloadRegistration(record)
	return record, true, nil
}

func (s *redisStateStore) SyncWorkloads(ctx context.Context, workloads []WorkloadRegistration) (RegistrySyncResult, error) {
	if s == nil || s.client == nil {
		return RegistrySyncResult{}, errors.New("redis client is not configured")
	}

	currentHosts, err := s.client.SMembers(ctx, registryHostsSetKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return RegistrySyncResult{}, fmt.Errorf("list registry hosts: %w", err)
	}

	desiredHosts := make(map[string]WorkloadRegistration, len(workloads))
	for _, workload := range workloads {
		desiredHosts[workload.HostName] = workload
	}

	pipe := s.client.TxPipeline()
	for _, workload := range workloads {
		payload, err := json.Marshal(workload)
		if err != nil {
			return RegistrySyncResult{}, fmt.Errorf("encode workload %s: %w", workload.HostName, err)
		}
		pipe.Set(ctx, registryHostKey(workload.HostName), payload, 0)
		pipe.SAdd(ctx, registryHostsSetKey, workload.HostName)
	}

	removedCount := 0
	for _, currentHost := range currentHosts {
		currentHost = normalizeHost(currentHost)
		if _, ok := desiredHosts[currentHost]; ok {
			continue
		}

		pipe.Del(ctx, registryHostKey(currentHost))
		pipe.Del(ctx, readyEndpointKey(currentHost))
		pipe.Del(ctx, wakeLockKey(currentHost))
		pipe.SRem(ctx, registryHostsSetKey, currentHost)
		removedCount++
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return RegistrySyncResult{}, fmt.Errorf("sync registry workloads: %w", err)
	}

	return RegistrySyncResult{
		SyncedCount:  len(workloads),
		RemovedCount: removedCount,
	}, nil
}

func (s *redisStateStore) LookupReadyEndpoint(ctx context.Context, host string) (*url.URL, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, errors.New("redis client is not configured")
	}

	raw, err := s.client.Get(ctx, readyEndpointKey(host)).Result()
	switch {
	case err == nil:
	case errors.Is(err, redis.Nil):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("lookup ready endpoint %s: %w", host, err)
	}

	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("parse ready endpoint %s: %w", host, err)
	}

	return endpoint, true, nil
}

func (s *redisStateStore) SetReadyEndpoint(ctx context.Context, host string, endpoint *url.URL, ttl time.Duration) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is not configured")
	}
	if endpoint == nil {
		return errors.New("ready endpoint is required")
	}
	if ttl <= 0 {
		return errors.New("ready endpoint ttl must be greater than zero")
	}

	return s.client.Set(ctx, readyEndpointKey(host), endpoint.String(), ttl).Err()
}

func (s *redisStateStore) ClearReadyEndpoint(ctx context.Context, host string) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is not configured")
	}

	return s.client.Del(ctx, readyEndpointKey(host)).Err()
}

func (s *redisStateStore) AcquireWakeLock(ctx context.Context, host, owner string, ttl time.Duration) (bool, error) {
	if s == nil || s.client == nil {
		return false, errors.New("redis client is not configured")
	}
	if owner == "" {
		return false, errors.New("wake lock owner is required")
	}
	if ttl <= 0 {
		return false, errors.New("wake lock ttl must be greater than zero")
	}

	acquired, err := s.client.SetNX(ctx, wakeLockKey(host), owner, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("acquire wake lock %s: %w", host, err)
	}

	return acquired, nil
}

func (s *redisStateStore) ReleaseWakeLock(ctx context.Context, host, owner string) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is not configured")
	}
	if owner == "" {
		return errors.New("wake lock owner is required")
	}

	if err := releaseWakeLockScript.Run(ctx, s.client, []string{wakeLockKey(host)}, owner).Err(); err != nil {
		return fmt.Errorf("release wake lock %s: %w", host, err)
	}

	return nil
}

func (s *redisStateStore) GetJobSpec(ctx context.Context, key string) ([]byte, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, errors.New("redis client is not configured")
	}

	raw, err := s.client.Get(ctx, key).Bytes()
	switch {
	case err == nil:
		return raw, true, nil
	case errors.Is(err, redis.Nil):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("get job spec %s: %w", key, err)
	}
}

func (s *redisStateStore) SetActivity(ctx context.Context, service string, at time.Time) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is not configured")
	}
	service = normalizeHost(service)
	if service == "" {
		return errors.New("service is required")
	}

	return s.client.Set(ctx, activityKey(service), at.UTC().Format(time.RFC3339Nano), 0).Err()
}

func registryHostKey(host string) string {
	return registryHostKeyPrefix + normalizeHost(host)
}

func readyEndpointKey(host string) string {
	return readyEndpointKeyPrefix + normalizeHost(host)
}

func wakeLockKey(host string) string {
	return wakeLockKeyPrefix + normalizeHost(host)
}

func activityKey(service string) string {
	return activityKeyPrefix + normalizeHost(service)
}
