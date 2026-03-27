// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	decisionScopeJob   = "job"
	decisionScopeGroup = "group"

	decisionScanned                       = "scanned"
	decisionManaged                       = "managed"
	decisionSkippedDisabled               = "skipped_disabled"
	decisionSkippedProtected              = "skipped_protected"
	decisionSkippedMissingName            = "skipped_missing_name"
	decisionSkippedZeroCount              = "skipped_zero_count"
	decisionSkippedMissingService         = "skipped_missing_service"
	decisionActivityInitialized           = "activity_initialized"
	decisionActivityLookupFailure         = "activity_lookup_failure"
	decisionActivityInitializationFailure = "activity_initialization_failure"
	decisionSkippedRecentActivity         = "skipped_recent_activity"
	decisionSkippedMinScaleDownAge        = "skipped_min_scale_down_age"
	decisionScaleAttempt                  = "scale_attempt"
	decisionScaleSuccess                  = "scale_success"
	decisionScaleFailure                  = "scale_failure"
	decisionPurgeAttempt                  = "purge_attempt"
	decisionPurgeSuccess                  = "purge_success"
	decisionPurgeFailure                  = "purge_failure"
)

type scalerRunSummary struct {
	jobsScanned                    int
	jobsManaged                    int
	jobsSkippedDisabled            int
	jobsSkippedProtected           int
	groupsScanned                  int
	groupsManaged                  int
	groupsSkippedMissingName       int
	groupsSkippedZeroCount         int
	groupsSkippedMissingService    int
	activityInitializations        int
	activityLookupFailures         int
	activityInitializationFailures int
	recentActivitySkips            int
	minScaleDownAgeSkips           int
	scaleAttempts                  int
	scaleSuccesses                 int
	scaleFailures                  int
	purgeAttempts                  int
	purgeSuccesses                 int
	purgeFailures                  int
}

func (s *scalerRunSummary) record(scope, decision string) {
	if s == nil {
		return
	}

	switch scope {
	case decisionScopeJob:
		switch decision {
		case decisionScanned:
			s.jobsScanned++
		case decisionManaged:
			s.jobsManaged++
		case decisionSkippedDisabled:
			s.jobsSkippedDisabled++
		case decisionSkippedProtected:
			s.jobsSkippedProtected++
		}
	case decisionScopeGroup:
		switch decision {
		case decisionScanned:
			s.groupsScanned++
		case decisionManaged:
			s.groupsManaged++
		case decisionSkippedMissingName:
			s.groupsSkippedMissingName++
		case decisionSkippedZeroCount:
			s.groupsSkippedZeroCount++
		case decisionSkippedMissingService:
			s.groupsSkippedMissingService++
		case decisionActivityInitialized:
			s.activityInitializations++
		case decisionActivityLookupFailure:
			s.activityLookupFailures++
		case decisionActivityInitializationFailure:
			s.activityInitializationFailures++
		case decisionSkippedRecentActivity:
			s.recentActivitySkips++
		case decisionSkippedMinScaleDownAge:
			s.minScaleDownAgeSkips++
		case decisionScaleAttempt:
			s.scaleAttempts++
		case decisionScaleSuccess:
			s.scaleSuccesses++
		case decisionScaleFailure:
			s.scaleFailures++
		case decisionPurgeAttempt:
			s.purgeAttempts++
		case decisionPurgeSuccess:
			s.purgeSuccesses++
		case decisionPurgeFailure:
			s.purgeFailures++
		}
	}
}

type scalerObservability struct {
	logger          *slog.Logger
	registry        *prometheus.Registry
	runDuration     prometheus.Histogram
	runTotal        *prometheus.CounterVec
	runErrors       prometheus.Counter
	decisionTotal   *prometheus.CounterVec
	lastRunItems    *prometheus.GaugeVec
	lastRunUnix     atomic.Int64
	lastSuccessUnix atomic.Int64
}

func newScalerObservability(service string) *scalerObservability {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	runDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "s2z",
		Subsystem: "idle_scaler",
		Name:      "run_duration_seconds",
		Help:      "Duration of a single idle-scaler scan loop.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	})
	runTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "s2z",
		Subsystem: "idle_scaler",
		Name:      "runs_total",
		Help:      "Total number of idle-scaler runs partitioned by outcome.",
	}, []string{"result"})
	runErrors := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "s2z",
		Subsystem: "idle_scaler",
		Name:      "run_errors_total",
		Help:      "Total number of idle-scaler runs that returned an error.",
	})
	decisionTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "s2z",
		Subsystem: "idle_scaler",
		Name:      "decision_total",
		Help:      "Total number of idle-scaler job and group decisions partitioned by scope and decision.",
	}, []string{"scope", "decision"})
	lastRunItems := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "s2z",
		Subsystem: "idle_scaler",
		Name:      "last_run_items",
		Help:      "Number of jobs and groups processed during the most recent idle-scaler run.",
	}, []string{"scope", "state"})

	registry.MustRegister(runDuration, runTotal, runErrors, decisionTotal, lastRunItems)

	return &scalerObservability{
		logger:        newJSONLogger(service),
		registry:      registry,
		runDuration:   runDuration,
		runTotal:      runTotal,
		runErrors:     runErrors,
		decisionTotal: decisionTotal,
		lastRunItems:  lastRunItems,
	}
}

func (o *scalerObservability) start(addr string) error {
	if strings.TrimSpace(addr) == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if o.lastSuccessUnix.Load() == 0 {
			http.Error(w, "idle-scaler has not completed a successful run yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			o.logger.Error("observability server stopped", "error", err, "addr", addr)
		}
	}()

	o.logger.Info("observability server listening", "addr", addr)
	return nil
}

func (o *scalerObservability) observeRun(duration time.Duration, summary scalerRunSummary, err error) {
	now := time.Now().Unix()
	o.lastRunUnix.Store(now)
	o.runDuration.Observe(duration.Seconds())
	o.lastRunItems.WithLabelValues(decisionScopeJob, decisionScanned).Set(float64(summary.jobsScanned))
	o.lastRunItems.WithLabelValues(decisionScopeJob, decisionManaged).Set(float64(summary.jobsManaged))
	o.lastRunItems.WithLabelValues(decisionScopeGroup, decisionScanned).Set(float64(summary.groupsScanned))
	o.lastRunItems.WithLabelValues(decisionScopeGroup, decisionManaged).Set(float64(summary.groupsManaged))

	runAttrs := []any{
		"duration", duration.String(),
		"jobs_scanned", summary.jobsScanned,
		"jobs_managed", summary.jobsManaged,
		"jobs_skipped_disabled", summary.jobsSkippedDisabled,
		"jobs_skipped_protected", summary.jobsSkippedProtected,
		"groups_scanned", summary.groupsScanned,
		"groups_managed", summary.groupsManaged,
		"groups_skipped_missing_name", summary.groupsSkippedMissingName,
		"groups_skipped_zero_count", summary.groupsSkippedZeroCount,
		"groups_skipped_missing_service", summary.groupsSkippedMissingService,
		"activity_initializations", summary.activityInitializations,
		"activity_lookup_failures", summary.activityLookupFailures,
		"activity_initialization_failures", summary.activityInitializationFailures,
		"recent_activity_skips", summary.recentActivitySkips,
		"min_scale_down_age_skips", summary.minScaleDownAgeSkips,
		"scale_attempts", summary.scaleAttempts,
		"scale_successes", summary.scaleSuccesses,
		"scale_failures", summary.scaleFailures,
		"purge_attempts", summary.purgeAttempts,
		"purge_successes", summary.purgeSuccesses,
		"purge_failures", summary.purgeFailures,
	}

	if err != nil {
		o.runErrors.Inc()
		o.runTotal.WithLabelValues("error").Inc()
		runAttrs = append(runAttrs, "error", err)
		o.logger.Error("idle-scaler run failed", runAttrs...)
		return
	}

	o.lastSuccessUnix.Store(now)
	o.runTotal.WithLabelValues("success").Inc()
	o.logger.Info("idle-scaler run completed", runAttrs...)
}

func (o *scalerObservability) observeDecision(scope, decision string) {
	if o == nil {
		return
	}

	o.decisionTotal.WithLabelValues(scope, decision).Inc()
}

func newJSONLogger(service string) *slog.Logger {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With(
		"service", service,
	)
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
