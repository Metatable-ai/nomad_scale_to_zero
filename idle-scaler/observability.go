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

type scalerObservability struct {
	logger          *slog.Logger
	registry        *prometheus.Registry
	runDuration     prometheus.Histogram
	runTotal        *prometheus.CounterVec
	runErrors       prometheus.Counter
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

	registry.MustRegister(runDuration, runTotal, runErrors)

	return &scalerObservability{
		logger:      newJSONLogger(service),
		registry:    registry,
		runDuration: runDuration,
		runTotal:    runTotal,
		runErrors:   runErrors,
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

func (o *scalerObservability) observeRun(duration time.Duration, err error) {
	o.lastRunUnix.Store(time.Now().Unix())
	o.runDuration.Observe(duration.Seconds())
	if err != nil {
		o.runErrors.Inc()
		o.runTotal.WithLabelValues("error").Inc()
		return
	}

	o.lastSuccessUnix.Store(time.Now().Unix())
	o.runTotal.WithLabelValues("success").Inc()
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
