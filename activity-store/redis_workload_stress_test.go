//go:build stress

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package activitystore

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type redisStressConfig struct {
	Workers        int
	Duration       time.Duration
	SampleInterval time.Duration
	ServiceKeys    int
	JobKeys        int
	JobSpecSize    int
}

type redisInfoSnapshot struct {
	UsedMemory             int64
	UsedMemoryRSS          int64
	UsedMemoryPeak         int64
	InstantaneousOpsPerSec int64
	TotalCommandsProcessed int64
	KeyspaceHits           int64
	KeyspaceMisses         int64
	ConnectedClients       int64
	UsedCPUUser            float64
	UsedCPUSys             float64
}

type redisSampleSummary struct {
	Samples                 int64
	MaxUsedMemory           int64
	MaxUsedMemoryRSS        int64
	MaxInstantaneousOpsPerS int64
	Err                     error
}

func stressRedisAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("redis unreachable at %s: %v", addr, err)
	}
	_ = conn.Close()
	return addr
}

func mustStressIntEnv(t *testing.T, key string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		t.Fatalf("%s must be a positive integer, got %q", key, value)
	}
	return n
}

func mustStressDurationEnv(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		t.Fatalf("%s must be a positive duration, got %q", key, value)
	}
	return d
}

func redisStressCfg(t *testing.T) redisStressConfig {
	t.Helper()
	return redisStressConfig{
		Workers:        mustStressIntEnv(t, "STRESS_WORKERS", 200),
		Duration:       mustStressDurationEnv(t, "STRESS_DURATION", 20*time.Second),
		SampleInterval: mustStressDurationEnv(t, "STRESS_SAMPLE_INTERVAL", 250*time.Millisecond),
		ServiceKeys:    mustStressIntEnv(t, "STRESS_SERVICE_KEYS", 2000),
		JobKeys:        mustStressIntEnv(t, "STRESS_JOB_KEYS", 1000),
		JobSpecSize:    mustStressIntEnv(t, "STRESS_JOB_SPEC_SIZE", 32*1024),
	}
}

func newStressRedisClient(t *testing.T, cfg RedisConfig) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("redis ping: %v", err)
	}

	return client
}

func parseRedisInfoSnapshot(info string) (redisInfoSnapshot, error) {
	var snap redisInfoSnapshot

	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		switch key {
		case "used_memory":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse used_memory: %w", err)
			}
			snap.UsedMemory = n
		case "used_memory_rss":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse used_memory_rss: %w", err)
			}
			snap.UsedMemoryRSS = n
		case "used_memory_peak":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse used_memory_peak: %w", err)
			}
			snap.UsedMemoryPeak = n
		case "instantaneous_ops_per_sec":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse instantaneous_ops_per_sec: %w", err)
			}
			snap.InstantaneousOpsPerSec = n
		case "total_commands_processed":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse total_commands_processed: %w", err)
			}
			snap.TotalCommandsProcessed = n
		case "keyspace_hits":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse keyspace_hits: %w", err)
			}
			snap.KeyspaceHits = n
		case "keyspace_misses":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse keyspace_misses: %w", err)
			}
			snap.KeyspaceMisses = n
		case "connected_clients":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snap, fmt.Errorf("parse connected_clients: %w", err)
			}
			snap.ConnectedClients = n
		case "used_cpu_user":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return snap, fmt.Errorf("parse used_cpu_user: %w", err)
			}
			snap.UsedCPUUser = n
		case "used_cpu_sys":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return snap, fmt.Errorf("parse used_cpu_sys: %w", err)
			}
			snap.UsedCPUSys = n
		}
	}

	return snap, nil
}

func fetchRedisInfo(ctx context.Context, client *redis.Client) (redisInfoSnapshot, error) {
	info, err := client.Info(ctx, "memory", "cpu", "stats", "clients").Result()
	if err != nil {
		return redisInfoSnapshot{}, err
	}
	return parseRedisInfoSnapshot(info)
}

func startRedisSampler(client *redis.Client, interval time.Duration) (func(), <-chan redisSampleSummary) {
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan redisSampleSummary, 1)

	go func() {
		defer close(resultCh)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		var summary redisSampleSummary
		for {
			select {
			case <-ctx.Done():
				resultCh <- summary
				return
			case <-ticker.C:
				snapshot, err := fetchRedisInfo(context.Background(), client)
				if err != nil {
					if summary.Err == nil {
						summary.Err = err
					}
					continue
				}

				summary.Samples++
				if snapshot.UsedMemory > summary.MaxUsedMemory {
					summary.MaxUsedMemory = snapshot.UsedMemory
				}
				if snapshot.UsedMemoryRSS > summary.MaxUsedMemoryRSS {
					summary.MaxUsedMemoryRSS = snapshot.UsedMemoryRSS
				}
				if snapshot.InstantaneousOpsPerSec > summary.MaxInstantaneousOpsPerS {
					summary.MaxInstantaneousOpsPerS = snapshot.InstantaneousOpsPerSec
				}
			}
		}
	}()

	return cancel, resultCh
}

func makeStressJSONPayload(targetSize int, version int) []byte {
	if targetSize < 128 {
		targetSize = 128
	}

	prefix := fmt.Sprintf(`{"ID":"stress-workload","Version":%d,"Payload":"`, version)
	suffix := `"}`
	padLen := targetSize - len(prefix) - len(suffix)
	if padLen < 0 {
		padLen = 0
	}

	return []byte(prefix + strings.Repeat("x", padLen) + suffix)
}

func bytesToMiB(bytes int64) float64 {
	return float64(bytes) / (1024 * 1024)
}

func TestStress_RedisHighLoadWorkload(t *testing.T) {
	addr := stressRedisAddr(t)
	stressCfg := redisStressCfg(t)
	redisCfg := RedisConfig{
		Addr:     addr,
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
		DB:       0,
	}
	namespace := fmt.Sprintf("stress-ns-%d", time.Now().UnixNano())

	activityStore, err := NewRedisStore(redisCfg, namespace)
	if err != nil {
		t.Fatalf("new redis activity store: %v", err)
	}
	defer activityStore.Close()

	jobStore, err := NewRedisJobSpecStore(redisCfg, namespace)
	if err != nil {
		t.Fatalf("new redis job store: %v", err)
	}
	defer jobStore.Close()

	adminClient := newStressRedisClient(t, redisCfg)
	defer adminClient.Close()

	serviceKeys := make([]string, stressCfg.ServiceKeys)
	for i := range serviceKeys {
		serviceKeys[i] = fmt.Sprintf("svc-%06d", i)
	}

	jobKeys := make([]string, stressCfg.JobKeys)
	for i := range jobKeys {
		jobKeys[i] = fmt.Sprintf("job-%06d", i)
	}

	payloadA := makeStressJSONPayload(stressCfg.JobSpecSize, 1)
	payloadB := makeStressJSONPayload(stressCfg.JobSpecSize, 2)

	seedAt := time.Now().UTC()
	for _, service := range serviceKeys {
		if err := activityStore.SetActivity(service, seedAt); err != nil {
			t.Fatalf("seed activity %s: %v", service, err)
		}
	}
	for _, jobID := range jobKeys {
		if err := jobStore.SetJobSpec(jobID, payloadA); err != nil {
			t.Fatalf("seed job spec %s: %v", jobID, err)
		}
	}

	if err := adminClient.Do(context.Background(), "CONFIG", "RESETSTAT").Err(); err != nil {
		t.Logf("redis CONFIG RESETSTAT unavailable: %v", err)
	}

	startMetrics, err := fetchRedisInfo(context.Background(), adminClient)
	if err != nil {
		t.Fatalf("fetch start redis info: %v", err)
	}

	stopSampler, samplesCh := startRedisSampler(adminClient, stressCfg.SampleInterval)

	var totalOps atomic.Int64
	var setActivityOps atomic.Int64
	var getActivityOps atomic.Int64
	var setJobSpecOps atomic.Int64
	var getJobSpecOps atomic.Int64
	var errCount atomic.Int64

	workloadCtx, cancelWorkload := context.WithTimeout(context.Background(), stressCfg.Duration)
	defer cancelWorkload()

	startTime := time.Now()
	var wg sync.WaitGroup
	wg.Add(stressCfg.Workers)
	for workerID := 0; workerID < stressCfg.Workers; workerID++ {
		go func(workerID int) {
			defer wg.Done()
			iteration := 0
			for {
				select {
				case <-workloadCtx.Done():
					return
				default:
				}

				service := serviceKeys[(workerID*131+iteration)%len(serviceKeys)]
				jobID := jobKeys[(workerID*197+iteration)%len(jobKeys)]

				var opErr error
				switch (workerID + iteration) % 10 {
				case 0, 1, 2, 3:
					opErr = activityStore.SetActivity(service, time.Now().UTC())
					setActivityOps.Add(1)
				case 4, 5, 6:
					_, _, opErr = activityStore.LastActivity(service)
					getActivityOps.Add(1)
				case 7, 8:
					payload := payloadA
					if iteration%2 == 1 {
						payload = payloadB
					}
					_, opErr = jobStore.SetJobSpecIfChanged(jobID, payload)
					setJobSpecOps.Add(1)
				default:
					_, _, opErr = jobStore.GetJobSpec(jobID)
					getJobSpecOps.Add(1)
				}

				if opErr != nil {
					errCount.Add(1)
				}
				totalOps.Add(1)
				iteration++
			}
		}(workerID)
	}

	wg.Wait()
	wallTime := time.Since(startTime)
	stopSampler()
	sampleSummary := <-samplesCh

	endMetrics, err := fetchRedisInfo(context.Background(), adminClient)
	if err != nil {
		t.Fatalf("fetch final redis info: %v", err)
	}
	if sampleSummary.Err != nil {
		t.Fatalf("sample redis info: %v", sampleSummary.Err)
	}

	commandDelta := endMetrics.TotalCommandsProcessed - startMetrics.TotalCommandsProcessed
	userCPUDelta := endMetrics.UsedCPUUser - startMetrics.UsedCPUUser
	sysCPUDelta := endMetrics.UsedCPUSys - startMetrics.UsedCPUSys
	totalCPUDelta := userCPUDelta + sysCPUDelta
	avgCPUCores := 0.0
	if wallTime > 0 {
		avgCPUCores = totalCPUDelta / wallTime.Seconds()
	}

	t.Logf(
		"redis high-load summary:\n  namespace=%s\n  duration=%s\n  workers=%d\n  total_ops=%d\n  redis_commands=%d\n  ops_per_sec=%.2f\n  set_activity=%d get_activity=%d set_job_spec=%d get_job_spec=%d\n  errors=%d\n  redis_memory_start=%.2f MiB redis_memory_end=%.2f MiB sampled_peak=%.2f MiB info_peak=%.2f MiB\n  redis_rss_start=%.2f MiB redis_rss_end=%.2f MiB sampled_rss_peak=%.2f MiB\n  redis_cpu_user_delta=%.3fs redis_cpu_sys_delta=%.3fs redis_cpu_total_delta=%.3fs avg_cpu_cores=%.3f\n  peak_instantaneous_ops_per_sec=%d\n  connected_clients=%d keyspace_hits=%d keyspace_misses=%d samples=%d",
		namespace,
		wallTime,
		stressCfg.Workers,
		totalOps.Load(),
		commandDelta,
		float64(totalOps.Load())/wallTime.Seconds(),
		setActivityOps.Load(),
		getActivityOps.Load(),
		setJobSpecOps.Load(),
		getJobSpecOps.Load(),
		errCount.Load(),
		bytesToMiB(startMetrics.UsedMemory),
		bytesToMiB(endMetrics.UsedMemory),
		bytesToMiB(sampleSummary.MaxUsedMemory),
		bytesToMiB(endMetrics.UsedMemoryPeak),
		bytesToMiB(startMetrics.UsedMemoryRSS),
		bytesToMiB(endMetrics.UsedMemoryRSS),
		bytesToMiB(sampleSummary.MaxUsedMemoryRSS),
		userCPUDelta,
		sysCPUDelta,
		totalCPUDelta,
		avgCPUCores,
		sampleSummary.MaxInstantaneousOpsPerS,
		endMetrics.ConnectedClients,
		endMetrics.KeyspaceHits-startMetrics.KeyspaceHits,
		endMetrics.KeyspaceMisses-startMetrics.KeyspaceMisses,
		sampleSummary.Samples,
	)

	if totalOps.Load() == 0 {
		t.Fatal("stress workload executed zero operations")
	}
	if errCount.Load() > 0 {
		t.Fatalf("stress workload completed with %d redis errors", errCount.Load())
	}
	if commandDelta <= 0 {
		t.Fatalf("expected redis command delta to be positive, got %d", commandDelta)
	}
	if sampleSummary.MaxUsedMemory < startMetrics.UsedMemory {
		t.Fatalf("expected sampled peak memory to be at least start memory: peak=%d start=%d", sampleSummary.MaxUsedMemory, startMetrics.UsedMemory)
	}
}
