// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package traefik_plugin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	wakeResultAlreadyHealthy         = "already_healthy"
	wakeResultSuccess                = "success"
	wakeResultSuccessAfterScaleError = "success_after_scale_error"
	wakeResultEnsureJobError         = "ensure_job_error"
	wakeResultScaleUpError           = "scale_up_error"
	wakeResultWaitTimeout            = "wait_timeout"
	wakeResultContextCanceled        = "context_canceled"
	wakeResultContextDeadline        = "context_deadline_exceeded"
	wakeResultWaitError              = "wait_error"
)

// wakeObservability tracks wake metrics using atomic counters and structured
// logging. Yaegi-safe: no external dependencies.
type wakeObservability struct {
	attempts atomic.Int64
	outcomes sync.Map // result string → *atomic.Int64
	logger   *slog.Logger
}

var (
	defaultWakeMetrics     *wakeObservability
	defaultWakeMetricsOnce sync.Once
)

func defaultWakeObservabilityInstance() *wakeObservability {
	defaultWakeMetricsOnce.Do(func() {
		defaultWakeMetrics = newWakeObservability(nil)
	})
	return defaultWakeMetrics
}

func newWakeObservability(logger *slog.Logger) *wakeObservability {
	if logger == nil {
		logger = slog.Default()
	}
	return &wakeObservability{logger: logger}
}

func (s *ScaleWaker) observabilityMetrics() *wakeObservability {
	if s != nil && s.observability != nil {
		return s.observability
	}
	return defaultWakeObservabilityInstance()
}

func (o *wakeObservability) observeAttempt() {
	if o == nil {
		return
	}
	o.attempts.Add(1)
}

func (o *wakeObservability) outcomeCounter(result string) *atomic.Int64 {
	if v, ok := o.outcomes.Load(result); ok {
		return v.(*atomic.Int64)
	}
	c := &atomic.Int64{}
	actual, _ := o.outcomes.LoadOrStore(result, c)
	return actual.(*atomic.Int64)
}

func (o *wakeObservability) observeOutcome(result string, duration time.Duration) {
	if o == nil {
		return
	}
	o.outcomeCounter(result).Add(1)
	o.logger.Info("wake_outcome",
		"result", result,
		"duration_ms", duration.Milliseconds(),
		"attempts_total", o.attempts.Load(),
	)
}

// Snapshot returns current counter values for testing.
func (o *wakeObservability) Snapshot() map[string]int64 {
	m := map[string]int64{"attempts": o.attempts.Load()}
	o.outcomes.Range(func(key, value any) bool {
		m[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	return m
}

func wakeResultForWaitError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return wakeResultContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return wakeResultContextDeadline
	case errors.Is(err, errWaitForHealthyTimeout):
		return wakeResultWaitTimeout
	default:
		return wakeResultWaitError
	}
}
