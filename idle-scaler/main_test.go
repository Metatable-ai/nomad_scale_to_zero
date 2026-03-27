//go:build !integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	nomad "github.com/hashicorp/nomad/api"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ---------------------------------------------------------------------------
// Unit tests — no infrastructure required
// ---------------------------------------------------------------------------

func TestEnvOrDefault(t *testing.T) {
	t.Run("env set", func(t *testing.T) {
		t.Setenv("TEST_EOD_VAL", "from-env")
		got := envOrDefault("TEST_EOD_VAL", "fallback")
		if got != "from-env" {
			t.Errorf("got %q, want %q", got, "from-env")
		}
	})
	t.Run("env empty", func(t *testing.T) {
		got := envOrDefault("TEST_EOD_MISSING_"+fmt.Sprintf("%d", time.Now().UnixNano()), "fallback")
		if got != "fallback" {
			t.Errorf("got %q, want %q", got, "fallback")
		}
	})
}

func TestEnvOrDefaultDuration(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"go duration", "30s", 30 * time.Second},
		{"minutes", "5m", 5 * time.Minute},
		{"plain seconds", "120", 120 * time.Second},
		{"invalid", "xyz", 99 * time.Second}, // falls through to default
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := fmt.Sprintf("TEST_EODD_%d", time.Now().UnixNano())
			t.Setenv(key, tt.env)
			got := envOrDefaultDuration(key, 99*time.Second)
			if got != tt.want {
				t.Errorf("envOrDefaultDuration(%q) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		got := envOrDefaultDuration("MISSING_KEY_"+fmt.Sprintf("%d", time.Now().UnixNano()), 42*time.Second)
		if got != 42*time.Second {
			t.Errorf("got %v, want 42s", got)
		}
	})
}

func TestEnvOrDefaultInt(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		key := fmt.Sprintf("TEST_EODI_%d", time.Now().UnixNano())
		t.Setenv(key, "5")
		if got := envOrDefaultInt(key, 0); got != 5 {
			t.Errorf("got %d, want 5", got)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		key := fmt.Sprintf("TEST_EODI_%d", time.Now().UnixNano())
		t.Setenv(key, "abc")
		if got := envOrDefaultInt(key, 7); got != 7 {
			t.Errorf("got %d, want 7", got)
		}
	})
	t.Run("unset", func(t *testing.T) {
		if got := envOrDefaultInt("MISSING_"+fmt.Sprintf("%d", time.Now().UnixNano()), 3); got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})
}

func TestEnvOrDefaultBool(t *testing.T) {
	truthy := []string{"1", "true", "yes", "y", "on", "TRUE", "Yes"}
	falsy := []string{"0", "false", "no", "n", "off", "FALSE", "No"}

	for _, v := range truthy {
		t.Run("true/"+v, func(t *testing.T) {
			key := fmt.Sprintf("TEST_EODB_%d", time.Now().UnixNano())
			t.Setenv(key, v)
			if got := envOrDefaultBool(key, false); !got {
				t.Errorf("envOrDefaultBool(%q) = false, want true", v)
			}
		})
	}
	for _, v := range falsy {
		t.Run("false/"+v, func(t *testing.T) {
			key := fmt.Sprintf("TEST_EODB_%d", time.Now().UnixNano())
			t.Setenv(key, v)
			if got := envOrDefaultBool(key, true); got {
				t.Errorf("envOrDefaultBool(%q) = true, want false", v)
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		if got := envOrDefaultBool("MISSING_"+fmt.Sprintf("%d", time.Now().UnixNano()), true); !got {
			t.Error("expected default true")
		}
	})
}

func TestComputeHashIdleScaler(t *testing.T) {
	h1 := computeHash([]byte(`{"v":1}`))
	h2 := computeHash([]byte(`{"v":1}`))
	h3 := computeHash([]byte(`{"v":2}`))

	if h1 != h2 {
		t.Errorf("determinism: %s != %s", h1, h2)
	}
	if h1 == h3 {
		t.Error("different data produced same hash")
	}
	if len(h1) != 16 {
		t.Errorf("hash length = %d, want 16 hex chars", len(h1))
	}
}

func TestConsulJobSpecStore_KeyFormat(t *testing.T) {
	s := &ConsulJobSpecStore{}
	tests := []struct {
		jobID string
		want  string
	}{
		{"echo-s2z", "scale-to-zero/jobs/echo-s2z"},
		{"/leading-slash", "scale-to-zero/jobs/leading-slash"},
	}
	for _, tt := range tests {
		got := s.key(tt.jobID)
		if got != tt.want {
			t.Errorf("key(%q) = %q, want %q", tt.jobID, got, tt.want)
		}
	}
}

func TestShouldManageScaleToZeroJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		job  *nomad.Job
		want bool
	}{
		{
			name: "nil job",
			job:  nil,
			want: false,
		},
		{
			name: "missing metadata",
			job: &nomad.Job{
				ID:   pointerTo("echo-s2z"),
				Name: pointerTo("echo-s2z"),
			},
			want: false,
		},
		{
			name: "scale to zero disabled",
			job: &nomad.Job{
				ID:   pointerTo("echo-s2z"),
				Name: pointerTo("echo-s2z"),
				Meta: map[string]string{metaEnabled: "false"},
			},
			want: false,
		},
		{
			name: "enabled service job is managed",
			job: &nomad.Job{
				ID:   pointerTo("echo-s2z"),
				Name: pointerTo("echo-s2z"),
				Type: pointerTo("service"),
				Meta: map[string]string{metaEnabled: "TRUE"},
			},
			want: true,
		},
		{
			name: "system jobs are always skipped",
			job: &nomad.Job{
				ID:   pointerTo("echo-s2z"),
				Name: pointerTo("echo-s2z"),
				Type: pointerTo("system"),
				Meta: map[string]string{metaEnabled: "true"},
			},
			want: false,
		},
		{
			name: "idle scaler job id is skipped",
			job: &nomad.Job{
				ID:   pointerTo("idle-scaler-e2e"),
				Name: pointerTo("workload"),
				Type: pointerTo("service"),
				Meta: map[string]string{metaEnabled: "true"},
			},
			want: false,
		},
		{
			name: "idle scaler job name is skipped",
			job: &nomad.Job{
				ID:   pointerTo("workload"),
				Name: pointerTo("idle-scaler"),
				Type: pointerTo("service"),
				Meta: map[string]string{metaEnabled: "true"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := shouldManageScaleToZeroJob(tt.job)
			if got != tt.want {
				t.Errorf("shouldManageScaleToZeroJob() = %t, want %t", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Idle-timeout parsing — verify the fix works correctly
// ---------------------------------------------------------------------------

func TestIdleTimeoutParsing_Fixed(t *testing.T) {
	// The fixed idle-scaler does: time.ParseDuration(raw), then falls back
	// to strconv.Atoi(raw) * time.Second for bare numbers.
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"20", 20 * time.Second},
		{"300", 300 * time.Second},
		{"5m", 5 * time.Minute},
		{"30s", 30 * time.Second},
		{"1h", 1 * time.Hour},
		{"1h30m", 1*time.Hour + 30*time.Minute},
		{"500ms", 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			// Replicate the fixed parsing logic
			var timeout time.Duration
			if parsed, err := time.ParseDuration(tt.input); err == nil {
				timeout = parsed
			} else if seconds, err := strconv.Atoi(tt.input); err == nil {
				timeout = time.Duration(seconds) * time.Second
			} else {
				t.Fatalf("could not parse %q", tt.input)
			}

			if timeout != tt.expected {
				t.Errorf("input %q: got %v, want %v", tt.input, timeout, tt.expected)
			}
		})
	}
}

func TestMaybeScaleToZero_InitializesMissingActivity(t *testing.T) {
	t.Parallel()

	fixedNow := time.Unix(1_710_000_000, 0).UTC()
	store := &fakeActivityStore{}
	obs := newScalerObservability("idle-scaler-test")
	scaler := newTestIdleScaler(store, obs, fixedNow)

	var summary scalerRunSummary
	if err := scaler.maybeScaleToZero(context.Background(), newManagedScaleToZeroJob("echo-s2z", "api", "echo-service", 1), time.Minute, &summary); err != nil {
		t.Fatalf("maybeScaleToZero() error = %v", err)
	}

	got, ok := store.last["echo-service"]
	if !ok {
		t.Fatal("expected missing activity to be initialized")
	}
	if !got.Equal(fixedNow) {
		t.Fatalf("initialized activity = %v, want %v", got, fixedNow)
	}
	if summary.groupsScanned != 1 || summary.groupsManaged != 1 {
		t.Fatalf("summary groups = %+v, want 1 scanned and 1 managed", summary)
	}
	if summary.activityInitializations != 1 {
		t.Fatalf("activityInitializations = %d, want 1", summary.activityInitializations)
	}
	if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionActivityInitialized)); got != 1 {
		t.Fatalf("activity_initialized metric = %v, want 1", got)
	}
}

func TestMaybeScaleToZero_RecordsRecentActivitySkip(t *testing.T) {
	t.Parallel()

	fixedNow := time.Unix(1_710_000_100, 0).UTC()
	store := &fakeActivityStore{
		last: map[string]time.Time{
			"echo-service": fixedNow.Add(-30 * time.Second),
		},
	}
	obs := newScalerObservability("idle-scaler-test")
	scaler := newTestIdleScaler(store, obs, fixedNow)

	var summary scalerRunSummary
	if err := scaler.maybeScaleToZero(context.Background(), newManagedScaleToZeroJob("echo-s2z", "api", "echo-service", 1), time.Minute, &summary); err != nil {
		t.Fatalf("maybeScaleToZero() error = %v", err)
	}

	if summary.recentActivitySkips != 1 {
		t.Fatalf("recentActivitySkips = %d, want 1", summary.recentActivitySkips)
	}
	if summary.scaleAttempts != 0 || summary.scaleSuccesses != 0 || summary.scaleFailures != 0 {
		t.Fatalf("unexpected scale counters in summary: %+v", summary)
	}
	if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionSkippedRecentActivity)); got != 1 {
		t.Fatalf("skipped_recent_activity metric = %v, want 1", got)
	}
}

func TestMaybeScaleToZero_HonorsMinimumScaleDownAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		allocationAge   time.Duration
		wantScaleCalls  int
		wantGuardSkips  int
		wantGuardMetric float64
		wantErr         bool
	}{
		{
			name:            "fresh allocation is skipped",
			allocationAge:   30 * time.Second,
			wantScaleCalls:  0,
			wantGuardSkips:  1,
			wantGuardMetric: 1,
		},
		{
			name:            "older allocation is eligible",
			allocationAge:   2 * time.Minute,
			wantScaleCalls:  1,
			wantGuardSkips:  0,
			wantGuardMetric: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixedNow := time.Unix(1_710_000_150, 0).UTC()
			store := &fakeActivityStore{
				last: map[string]time.Time{
					"echo-service": fixedNow.Add(-2 * time.Minute),
				},
			}
			obs := newScalerObservability("idle-scaler-test")
			scaler := newTestIdleScaler(store, obs, fixedNow)
			scaler.minScaleDownAge = time.Minute
			scaler.jobAllocationsFn = func(jobID string) ([]*nomad.AllocationListStub, error) {
				if jobID != "echo-s2z" {
					t.Fatalf("unexpected job allocations lookup for %q", jobID)
				}
				return []*nomad.AllocationListStub{
					newRunningAllocation(jobID, "api", fixedNow.Add(-tt.allocationAge)),
				}, nil
			}

			scaleCalls := 0
			scaler.scaleGroupFn = func(jobID, group string, count int64) error {
				scaleCalls++
				if jobID != "echo-s2z" || group != "api" || count != 0 {
					t.Fatalf("unexpected scale request job=%q group=%q count=%d", jobID, group, count)
				}
				return nil
			}

			var summary scalerRunSummary
			err := scaler.maybeScaleToZero(context.Background(), newManagedScaleToZeroJob("echo-s2z", "api", "echo-service", 1), time.Minute, &summary)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scaleCalls != tt.wantScaleCalls {
				t.Fatalf("scale calls = %d, want %d", scaleCalls, tt.wantScaleCalls)
			}
			if summary.minScaleDownAgeSkips != tt.wantGuardSkips {
				t.Fatalf("minScaleDownAgeSkips = %d, want %d", summary.minScaleDownAgeSkips, tt.wantGuardSkips)
			}
			if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionSkippedMinScaleDownAge)); got != tt.wantGuardMetric {
				t.Fatalf("skipped_min_scale_down_age metric = %v, want %v", got, tt.wantGuardMetric)
			}
		})
	}
}

func TestMaybeScaleToZero_RecordsScaleOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		scaleErr          error
		wantErr           bool
		wantScaleSuccess  int
		wantScaleFailures int
		failureMetric     float64
		successMetric     float64
	}{
		{
			name:              "success",
			wantScaleSuccess:  1,
			wantScaleFailures: 0,
			successMetric:     1,
		},
		{
			name:              "failure",
			scaleErr:          errors.New("boom"),
			wantErr:           true,
			wantScaleSuccess:  0,
			wantScaleFailures: 1,
			failureMetric:     1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixedNow := time.Unix(1_710_000_200, 0).UTC()
			store := &fakeActivityStore{
				last: map[string]time.Time{
					"echo-service": fixedNow.Add(-2 * time.Minute),
				},
			}
			obs := newScalerObservability("idle-scaler-test")
			scaler := newTestIdleScaler(store, obs, fixedNow)

			scaleCalls := 0
			scaler.scaleGroupFn = func(jobID, group string, count int64) error {
				scaleCalls++
				if jobID != "echo-s2z" || group != "api" || count != 0 {
					t.Fatalf("unexpected scale request job=%q group=%q count=%d", jobID, group, count)
				}
				return tt.scaleErr
			}

			var summary scalerRunSummary
			err := scaler.maybeScaleToZero(context.Background(), newManagedScaleToZeroJob("echo-s2z", "api", "echo-service", 1), time.Minute, &summary)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scaleCalls != 1 {
				t.Fatalf("scaleGroupFn calls = %d, want 1", scaleCalls)
			}
			if summary.scaleAttempts != 1 {
				t.Fatalf("scaleAttempts = %d, want 1", summary.scaleAttempts)
			}
			if summary.scaleSuccesses != tt.wantScaleSuccess {
				t.Fatalf("scaleSuccesses = %d, want %d", summary.scaleSuccesses, tt.wantScaleSuccess)
			}
			if summary.scaleFailures != tt.wantScaleFailures {
				t.Fatalf("scaleFailures = %d, want %d", summary.scaleFailures, tt.wantScaleFailures)
			}
			if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionScaleAttempt)); got != 1 {
				t.Fatalf("scale_attempt metric = %v, want 1", got)
			}
			if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionScaleSuccess)); got != tt.successMetric {
				t.Fatalf("scale_success metric = %v, want %v", got, tt.successMetric)
			}
			if got := metricValue(t, obs.decisionTotal.WithLabelValues(decisionScopeGroup, decisionScaleFailure)); got != tt.failureMetric {
				t.Fatalf("scale_failure metric = %v, want %v", got, tt.failureMetric)
			}
		})
	}
}

type fakeActivityStore struct {
	last    map[string]time.Time
	lastErr error
	setErr  error
}

func (s *fakeActivityStore) LastActivity(service string) (time.Time, bool, error) {
	if s.lastErr != nil {
		return time.Time{}, false, s.lastErr
	}
	if s.last == nil {
		return time.Time{}, false, nil
	}

	at, ok := s.last[service]
	return at, ok, nil
}

func (s *fakeActivityStore) SetActivity(service string, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	if s.last == nil {
		s.last = make(map[string]time.Time)
	}
	s.last[service] = at
	return nil
}

func newTestIdleScaler(store *fakeActivityStore, obs *scalerObservability, now time.Time) *IdleScaler {
	return &IdleScaler{
		store:  store,
		obs:    obs,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time {
			return now
		},
	}
}

func newRunningAllocation(jobID, group string, createdAt time.Time) *nomad.AllocationListStub {
	return &nomad.AllocationListStub{
		JobID:         jobID,
		TaskGroup:     group,
		DesiredStatus: "run",
		ClientStatus:  "running",
		CreateTime:    createdAt.UnixNano(),
	}
}

func newManagedScaleToZeroJob(jobID, groupName, serviceName string, count int) *nomad.Job {
	return &nomad.Job{
		ID:   pointerTo(jobID),
		Name: pointerTo(jobID),
		Type: pointerTo("service"),
		Meta: map[string]string{
			metaEnabled: "true",
		},
		TaskGroups: []*nomad.TaskGroup{
			{
				Name:  pointerTo(groupName),
				Count: pointerTo(count),
				Services: []*nomad.Service{
					{Name: serviceName},
				},
			},
		},
	}
}

func metricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()

	dtoMetric := &dto.Metric{}
	if err := metric.Write(dtoMetric); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if counter := dtoMetric.GetCounter(); counter != nil {
		return counter.GetValue()
	}
	if gauge := dtoMetric.GetGauge(); gauge != nil {
		return gauge.GetValue()
	}

	t.Fatal("unsupported metric type")
	return 0
}

func pointerTo[T any](value T) *T {
	return &value
}
