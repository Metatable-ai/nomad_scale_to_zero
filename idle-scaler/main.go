// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	activitystore "nomad_scale_to_zero/activity-store"

	consul "github.com/hashicorp/consul/api"
	nomad "github.com/hashicorp/nomad/api"
)

const (
	metaEnabled     = "scale-to-zero.enabled"
	metaIdleTimeout = "scale-to-zero.idle-timeout"
	metaJobSpecKey  = "scale-to-zero.job-spec-kv"

	idleScalerJobNamePrefix = "idle-scaler"
	nomadSystemJobType      = "system"
)

func main() {
	var (
		nomadAddr       = flag.String("nomad-addr", envOrDefault("NOMAD_ADDR", "http://nomad.service.consul:4646"), "Nomad API address")
		consulAddr      = flag.String("consul-addr", envOrDefault("CONSUL_ADDR", "http://consul.service.consul:8500"), "Consul address")
		nomadToken      = flag.String("nomad-token", envOrDefault("NOMAD_TOKEN", ""), "Nomad ACL token")
		consulToken     = flag.String("consul-token", envOrDefault("CONSUL_TOKEN", ""), "Consul ACL token")
		redisAddr       = flag.String("redis-addr", envOrDefault("REDIS_ADDR", ""), "Redis address (optional, uses Consul if empty)")
		redisPass       = flag.String("redis-password", envOrDefault("REDIS_PASSWORD", ""), "Redis password")
		redisDB         = flag.Int("redis-db", envOrDefaultInt("REDIS_DB", 0), "Redis database number")
		storeType       = flag.String("store-type", envOrDefault("STORE_TYPE", "consul"), "Store type: consul or redis")
		interval        = flag.Duration("interval", envOrDefaultDuration("IDLE_CHECK_INTERVAL", 30*time.Second), "Idle check interval")
		defaultTO       = flag.Duration("default-idle-timeout", envOrDefaultDuration("DEFAULT_IDLE_TIMEOUT", 5*time.Minute), "Default idle timeout")
		minScaleDownAge = flag.Duration("min-scale-down-age", envOrDefaultDuration("MIN_SCALE_DOWN_AGE", time.Minute), "Minimum age of an active allocation before scale down is allowed")
		purgeOnSD       = flag.Bool("purge-on-scaledown", envOrDefaultBool("PURGE_ON_SCALEDOWN", false), "Purge job on scale down when idle")
		metricsAddr     = flag.String("metrics-addr", envOrDefault("METRICS_ADDR", ""), "Observability listen address")
	)
	flag.Parse()

	obs := newScalerObservability("idle-scaler")
	logger := obs.logger
	if err := obs.start(*metricsAddr); err != nil {
		logger.Error("observability startup failed", "error", err)
		os.Exit(1)
	}

	nomadClient, err := nomad.NewClient(&nomad.Config{Address: *nomadAddr, SecretID: *nomadToken})
	if err != nil {
		logger.Error("creating nomad client failed", "error", err, "nomad_addr", *nomadAddr)
		os.Exit(1)
	}

	if _, err := nomadClient.Status().Leader(); err != nil {
		logger.Error("checking nomad leader failed", "error", err, "nomad_addr", *nomadAddr)
		os.Exit(1)
	}

	var store activitystore.Store
	var jobSpecStore activitystore.JobSpecStore

	switch *storeType {
	case "redis":
		if *redisAddr == "" {
			logger.Error("redis address is required when store type is redis")
			os.Exit(1)
		}
		redisCfg := activitystore.RedisConfig{
			Addr:     *redisAddr,
			Password: *redisPass,
			DB:       *redisDB,
		}
		redisStore, err := activitystore.NewRedisStore(redisCfg, activitystore.DefaultNamespace)
		if err != nil {
			logger.Error("creating redis activity store failed", "error", err, "redis_addr", *redisAddr)
			os.Exit(1)
		}
		store = redisStore

		redisJobStore, err := activitystore.NewRedisJobSpecStore(redisCfg, activitystore.DefaultNamespace)
		if err != nil {
			logger.Error("creating redis job spec store failed", "error", err, "redis_addr", *redisAddr)
			os.Exit(1)
		}
		jobSpecStore = redisJobStore
		logger.Info("using Redis backend", "store_type", *storeType, "redis_addr", *redisAddr)

	default: // consul
		consulStore, err := activitystore.NewConsulStoreWithToken(*consulAddr, *consulToken, activitystore.DefaultNamespace)
		if err != nil {
			logger.Error("creating consul activity store failed", "error", err, "consul_addr", *consulAddr)
			os.Exit(1)
		}
		store = consulStore

		consulConfig := consul.DefaultConfig()
		consulConfig.Address = *consulAddr
		consulConfig.Token = *consulToken
		consulClient, err := consul.NewClient(consulConfig)
		if err != nil {
			logger.Error("creating consul client failed", "error", err, "consul_addr", *consulAddr)
			os.Exit(1)
		}
		jobSpecStore = &ConsulJobSpecStore{client: consulClient}
		logger.Info("using Consul backend", "store_type", *storeType, "consul_addr", *consulAddr)
	}

	scaler := &IdleScaler{
		nomadClient:     nomadClient,
		store:           store,
		jobSpecStore:    jobSpecStore,
		interval:        *interval,
		defaultTO:       *defaultTO,
		minScaleDownAge: *minScaleDownAge,
		purgeOnSD:       *purgeOnSD,
		obs:             obs,
		logger:          logger,
		now:             time.Now,
	}

	logger.Info("idle-scaler started",
		"interval", interval.String(),
		"default_idle_timeout", defaultTO.String(),
		"min_scale_down_age", minScaleDownAge.String(),
		"purge_on_scaledown", *purgeOnSD,
		"metrics_addr", *metricsAddr,
		"store_type", *storeType,
	)
	ctx := context.Background()
	for {
		startedAt := time.Now()
		summary, err := scaler.RunOnce(ctx)
		obs.observeRun(time.Since(startedAt), summary, err)
		time.Sleep(*interval)
	}
}

type IdleScaler struct {
	nomadClient      *nomad.Client
	store            activitystore.Store
	jobSpecStore     activitystore.JobSpecStore
	interval         time.Duration
	defaultTO        time.Duration
	minScaleDownAge  time.Duration
	purgeOnSD        bool
	obs              *scalerObservability
	logger           *slog.Logger
	now              func() time.Time
	scaleGroupFn     func(jobID, group string, count int64) error
	purgeJobFn       func(jobID string) error
	jobAllocationsFn func(jobID string) ([]*nomad.AllocationListStub, error)
}

func (s *IdleScaler) RunOnce(ctx context.Context) (scalerRunSummary, error) {
	var summary scalerRunSummary

	jobs, _, err := s.nomadClient.Jobs().List(nil)
	if err != nil {
		return summary, fmt.Errorf("list jobs: %w", err)
	}

	for _, job := range jobs {
		recordDecision(s.obs, &summary, decisionScopeJob, decisionScanned)

		jobInfo, _, err := s.nomadClient.Jobs().Info(job.ID, nil)
		if err != nil {
			return summary, fmt.Errorf("job info %s: %w", job.ID, err)
		}

		jobID, jobName := jobIdentity(jobInfo)
		jobLogger := s.log().With("job", jobID, "job_name", jobName)

		decision := scaleToZeroJobDecision(jobInfo)
		recordDecision(s.obs, &summary, decisionScopeJob, decision)
		if decision != decisionManaged {
			jobLogger.DebugContext(ctx, "skipping job", "decision", decision)
			continue
		}

		if err := s.storeJobSpec(jobInfo); err != nil {
			jobLogger.WarnContext(ctx, "storing job spec failed", "error", err)
		}

		timeout := s.idleTimeoutForJob(ctx, jobInfo)

		if err := s.maybeScaleToZero(ctx, jobInfo, timeout, &summary); err != nil {
			jobLogger.ErrorContext(ctx, "scale-to-zero check failed", "error", err, "idle_timeout", timeout.String())
			continue
		}
	}

	return summary, nil
}

func recordDecision(obs *scalerObservability, summary *scalerRunSummary, scope, decision string) {
	if summary != nil {
		summary.record(scope, decision)
	}
	if obs != nil {
		obs.observeDecision(scope, decision)
	}
}

func shouldManageScaleToZeroJob(job *nomad.Job) bool {
	return scaleToZeroJobDecision(job) == decisionManaged
}

func scaleToZeroJobDecision(job *nomad.Job) string {
	if job == nil || job.Meta == nil {
		return decisionSkippedDisabled
	}

	if !strings.EqualFold(strings.TrimSpace(job.Meta[metaEnabled]), "true") {
		return decisionSkippedDisabled
	}

	if isProtectedScaleToZeroJob(job) {
		return decisionSkippedProtected
	}

	return decisionManaged
}

func isProtectedScaleToZeroJob(job *nomad.Job) bool {
	if job == nil {
		return false
	}

	if job.Type != nil && strings.EqualFold(*job.Type, nomadSystemJobType) {
		return true
	}

	if hasIdleScalerJobName(job.ID) || hasIdleScalerJobName(job.Name) {
		return true
	}

	return false
}

func hasIdleScalerJobName(value *string) bool {
	if value == nil {
		return false
	}

	return strings.HasPrefix(
		strings.ToLower(strings.TrimSpace(*value)),
		idleScalerJobNamePrefix,
	)
}

func (s *IdleScaler) storeJobSpec(job *nomad.Job) error {
	if s.jobSpecStore == nil || job == nil {
		return nil
	}

	jobID := ""
	if job.ID != nil {
		jobID = *job.ID
	}
	if jobID == "" {
		return nil
	}

	// Clone the job to avoid modifying the original
	jobCopy := *job
	// Ensure Stop is false so the job can be revived
	stopFalse := false
	jobCopy.Stop = &stopFalse

	payload, err := json.Marshal(jobCopy)
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", jobID, err)
	}

	// Use SetJobSpecIfChanged to avoid unnecessary writes
	changed, err := s.jobSpecStore.SetJobSpecIfChanged(jobID, payload)
	if err != nil {
		return fmt.Errorf("store job spec %s: %w", jobID, err)
	}
	if changed {
		s.log().Info("job spec updated", "job", jobID)
	}

	return nil
}

func (s *IdleScaler) maybeScaleToZero(ctx context.Context, job *nomad.Job, timeout time.Duration, summary *scalerRunSummary) error {
	jobID, jobName := jobIdentity(job)
	if jobID == "" || job.TaskGroups == nil {
		return nil
	}

	for _, group := range job.TaskGroups {
		recordDecision(s.obs, summary, decisionScopeGroup, decisionScanned)

		groupName := ""
		if group.Name != nil {
			groupName = *group.Name
		}
		jobLogger := s.log().With("job", jobID, "job_name", jobName)
		if groupName == "" {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionSkippedMissingName)
			jobLogger.DebugContext(ctx, "skipping group", "decision", decisionSkippedMissingName)
			continue
		}

		count := 0
		if group.Count != nil {
			count = *group.Count
		}
		groupLogger := jobLogger.With("group", groupName, "desired_count", count)
		if count == 0 {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionSkippedZeroCount)
			groupLogger.DebugContext(ctx, "skipping group", "decision", decisionSkippedZeroCount)
			continue
		}

		serviceName := s.resolveServiceName(job, group)
		if serviceName == "" {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionSkippedMissingService)
			groupLogger.DebugContext(ctx, "skipping group", "decision", decisionSkippedMissingService)
			continue
		}

		recordDecision(s.obs, summary, decisionScopeGroup, decisionManaged)
		groupLogger = groupLogger.With("service", serviceName, "idle_timeout", timeout.String())

		last, ok, err := s.store.LastActivity(serviceName)
		if err != nil {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionActivityLookupFailure)
			return fmt.Errorf("lookup activity for service %s: %w", serviceName, err)
		}

		now := s.currentTime()
		if !ok {
			// No activity recorded; initialize to now to avoid immediate scale-down
			if err := s.store.SetActivity(serviceName, now); err != nil {
				recordDecision(s.obs, summary, decisionScopeGroup, decisionActivityInitializationFailure)
				return fmt.Errorf("initialize activity for service %s: %w", serviceName, err)
			}
			recordDecision(s.obs, summary, decisionScopeGroup, decisionActivityInitialized)
			groupLogger.InfoContext(ctx, "initialized missing activity timestamp", "decision", decisionActivityInitialized)
			continue
		}

		idleFor := now.Sub(last)
		if idleFor < timeout {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionSkippedRecentActivity)
			groupLogger.DebugContext(ctx, "skipping group with recent activity",
				"decision", decisionSkippedRecentActivity,
				"idle_for", idleFor.String(),
				"time_until_scale", (timeout - idleFor).String(),
			)
			continue
		}

		if s.minScaleDownAge > 0 {
			activeFor, ok, err := s.youngestActiveAllocationAge(jobID, groupName, now)
			if err != nil {
				return fmt.Errorf("list allocations for job %s group %s: %w", jobID, groupName, err)
			}
			if ok && activeFor < s.minScaleDownAge {
				recordDecision(s.obs, summary, decisionScopeGroup, decisionSkippedMinScaleDownAge)
				groupLogger.DebugContext(ctx, "skipping group within scale-down guard window",
					"decision", decisionSkippedMinScaleDownAge,
					"active_for", activeFor.String(),
					"min_scale_down_age", s.minScaleDownAge.String(),
					"time_until_scale_eligible", (s.minScaleDownAge - activeFor).String(),
				)
				continue
			}
		}

		if s.purgeOnSD {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionPurgeAttempt)
			groupLogger.InfoContext(ctx, "purging idle job",
				"decision", decisionPurgeAttempt,
				"idle_for", idleFor.String(),
			)
			if err := s.purgeJob(jobID); err != nil {
				recordDecision(s.obs, summary, decisionScopeGroup, decisionPurgeFailure)
				return err
			}
			recordDecision(s.obs, summary, decisionScopeGroup, decisionPurgeSuccess)
			groupLogger.InfoContext(ctx, "purged idle job",
				"decision", decisionPurgeSuccess,
				"idle_for", idleFor.String(),
			)
			return nil
		}

		recordDecision(s.obs, summary, decisionScopeGroup, decisionScaleAttempt)
		groupLogger.InfoContext(ctx, "scaling idle group",
			"decision", decisionScaleAttempt,
			"idle_for", idleFor.String(),
			"target_count", 0,
		)
		if err := s.scaleGroup(jobID, groupName, 0); err != nil {
			recordDecision(s.obs, summary, decisionScopeGroup, decisionScaleFailure)
			return err
		}
		recordDecision(s.obs, summary, decisionScopeGroup, decisionScaleSuccess)
		groupLogger.InfoContext(ctx, "scaled idle group",
			"decision", decisionScaleSuccess,
			"idle_for", idleFor.String(),
			"target_count", 0,
		)
	}

	return nil
}

func (s *IdleScaler) resolveServiceName(job *nomad.Job, group *nomad.TaskGroup) string {
	if group.Services != nil {
		for _, svc := range group.Services {
			if svc != nil && svc.Name != "" {
				return svc.Name
			}
		}
	}

	// fallback: use job name
	if job.Name != nil {
		return *job.Name
	}

	return ""
}

func (s *IdleScaler) youngestActiveAllocationAge(jobID, group string, now time.Time) (time.Duration, bool, error) {
	allocations, err := s.jobAllocations(jobID)
	if err != nil {
		return 0, false, err
	}

	var youngest time.Duration
	found := false
	for _, alloc := range allocations {
		if alloc == nil || alloc.TaskGroup != group {
			continue
		}
		if alloc.DesiredStatus != "run" {
			continue
		}
		if alloc.ClientStatus != "pending" && alloc.ClientStatus != "running" {
			continue
		}

		createdAt := time.Unix(0, alloc.CreateTime)
		age := now.Sub(createdAt)
		if age < 0 {
			age = 0
		}
		if !found || age < youngest {
			youngest = age
			found = true
		}
	}

	return youngest, found, nil
}

func (s *IdleScaler) jobAllocations(jobID string) ([]*nomad.AllocationListStub, error) {
	if s != nil && s.jobAllocationsFn != nil {
		return s.jobAllocationsFn(jobID)
	}

	allocations, _, err := s.nomadClient.Jobs().Allocations(jobID, true, nil)
	return allocations, err
}

func (s *IdleScaler) scaleGroup(jobID, group string, count int64) error {
	if s != nil && s.scaleGroupFn != nil {
		return s.scaleGroupFn(jobID, group, count)
	}

	countValue := int(count)
	_, _, err := s.nomadClient.Jobs().Scale(jobID, group, &countValue, "scale-to-zero idle", false, nil, nil)
	if err != nil {
		return fmt.Errorf("scale job %s group %s: %w", jobID, group, err)
	}

	return nil
}

func (s *IdleScaler) purgeJob(jobID string) error {
	if s != nil && s.purgeJobFn != nil {
		return s.purgeJobFn(jobID)
	}

	_, _, err := s.nomadClient.Jobs().Deregister(jobID, true, nil)
	if err != nil {
		return fmt.Errorf("purge job %s: %w", jobID, err)
	}
	return nil
}

func (s *IdleScaler) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}

	return time.Now()
}

func (s *IdleScaler) log() *slog.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}

	return newJSONLogger("idle-scaler")
}

func (s *IdleScaler) idleTimeoutForJob(ctx context.Context, job *nomad.Job) time.Duration {
	timeout := s.defaultTO
	if job == nil || job.Meta == nil {
		return timeout
	}

	raw := strings.TrimSpace(job.Meta[metaIdleTimeout])
	if raw == "" {
		return timeout
	}

	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}

	jobID, jobName := jobIdentity(job)
	s.log().WarnContext(ctx, "invalid idle timeout metadata, using default",
		"job", jobID,
		"job_name", jobName,
		"raw_timeout", raw,
		"default_timeout", timeout.String(),
	)

	return timeout
}

func jobIdentity(job *nomad.Job) (string, string) {
	if job == nil {
		return "", ""
	}

	return strings.TrimSpace(stringValue(job.ID)), strings.TrimSpace(stringValue(job.Name))
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envOrDefaultDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			return parsed
		}
		if seconds, err := strconv.Atoi(v); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	return defaultVal
}

func envOrDefaultInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func envOrDefaultBool(key string, defaultVal bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return defaultVal
}

// ConsulJobSpecStore implements JobSpecStore using Consul KV
type ConsulJobSpecStore struct {
	client     *consul.Client
	specHashes map[string]string // in-memory cache to avoid unnecessary writes
}

func (s *ConsulJobSpecStore) key(jobID string) string {
	return "scale-to-zero/jobs/" + strings.TrimPrefix(jobID, "/")
}

func (s *ConsulJobSpecStore) GetJobSpec(jobID string) ([]byte, bool, error) {
	key := s.key(jobID)
	pair, _, err := s.client.KV().Get(key, nil)
	if err != nil {
		return nil, false, fmt.Errorf("get job spec %s: %w", key, err)
	}
	if pair == nil || len(pair.Value) == 0 {
		return nil, false, nil
	}

	// Validate JSON
	if !json.Valid(pair.Value) {
		return nil, false, fmt.Errorf("invalid JSON in job spec %s", key)
	}

	return pair.Value, true, nil
}

func (s *ConsulJobSpecStore) SetJobSpec(jobID string, spec []byte) error {
	// Validate JSON before storing
	if !json.Valid(spec) {
		return fmt.Errorf("invalid JSON for job spec %s", jobID)
	}

	key := s.key(jobID)
	_, err := s.client.KV().Put(&consul.KVPair{Key: key, Value: spec}, nil)
	if err != nil {
		return fmt.Errorf("store job spec %s: %w", key, err)
	}

	return nil
}

func (s *ConsulJobSpecStore) SetJobSpecIfChanged(jobID string, spec []byte) (bool, error) {
	// Validate JSON before storing
	if !json.Valid(spec) {
		return false, fmt.Errorf("invalid JSON for job spec %s", jobID)
	}

	// Initialize hash cache if needed
	if s.specHashes == nil {
		s.specHashes = make(map[string]string)
	}

	// Compute hash
	newHash := computeHash(spec)

	// Check if hash matches cached value
	if s.specHashes[jobID] == newHash {
		return false, nil
	}

	// Write to Consul
	key := s.key(jobID)
	_, err := s.client.KV().Put(&consul.KVPair{Key: key, Value: spec}, nil)
	if err != nil {
		return false, fmt.Errorf("store job spec %s: %w", key, err)
	}

	// Update cache
	s.specHashes[jobID] = newHash
	return true, nil
}

func (s *ConsulJobSpecStore) DeleteJobSpec(jobID string) error {
	key := s.key(jobID)
	_, err := s.client.KV().Delete(key, nil)
	if err != nil {
		return fmt.Errorf("delete job spec %s: %w", key, err)
	}
	if s.specHashes != nil {
		delete(s.specHashes, jobID)
	}
	return nil
}

func computeHash(data []byte) string {
	// Use FNV-1a for speed (not cryptographic, just for change detection)
	var hash uint64 = 14695981039346656037
	for _, b := range data {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return fmt.Sprintf("%016x", hash)
}
