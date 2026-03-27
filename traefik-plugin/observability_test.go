//go:build !integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package traefik_plugin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWakeUpService_ObservabilitySuccess(t *testing.T) {
	be := startBackend(t, "woke up")
	defer be.server.Close()

	var scaled atomic.Int32

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			if scaled.Load() == 1 {
				json.NewEncoder(w).Encode(be.entries)
				return
			}
			json.NewEncoder(w).Encode([]consulServiceEntry{})
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			w.Write([]byte(`{"ID":"test-job","Name":"test-job","Type":"service"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			scaled.Store(1)
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/allocations") && r.Method == http.MethodGet:
			if scaled.Load() == 1 {
				json.NewEncoder(w).Encode(makeAllocations("main", be.host, be.port))
			} else {
				json.NewEncoder(w).Encode([]nomadAllocation{})
			}
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			count := 0
			if scaled.Load() == 1 {
				count = 1
			}
			json.NewEncoder(w).Encode(nomadJobInfo{
				Status:     "dead",
				TaskGroups: []nomadJobTaskGroup{{Name: "main", Count: count}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	obs := newWakeObservability(slog.Default())
	sw := &ScaleWaker{
		config:        &Config{ServiceName: "test-svc", JobName: "test-job", GroupName: "main"},
		service:       "test-svc",
		jobName:       "test-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       2 * time.Second,
		observability: obs,
	}

	if _, err := sw.wakeUpService(context.Background(), "test-svc", "test-job", "main"); err != nil {
		t.Fatalf("wakeUpService: %v", err)
	}

	snap := obs.Snapshot()
	if snap["attempts"] != 1 {
		t.Fatalf("wake attempts = %v, want 1", snap["attempts"])
	}
	if snap[wakeResultSuccess] != 1 {
		t.Fatalf("wake success outcomes = %v, want 1", snap[wakeResultSuccess])
	}
}

func TestWakeUpService_ObservabilityAlreadyHealthy(t *testing.T) {
	be := startBackend(t, "already healthy")
	defer be.server.Close()

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode(be.entries)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	obs := newWakeObservability(slog.Default())
	sw := &ScaleWaker{
		config:        &Config{ServiceName: "healthy-svc", JobName: "healthy-job", GroupName: "main"},
		service:       "healthy-svc",
		jobName:       "healthy-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     "http://unused",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       2 * time.Second,
		observability: obs,
	}

	endpoint, err := sw.wakeUpService(context.Background(), "healthy-svc", "healthy-job", "main")
	if err != nil {
		t.Fatalf("wakeUpService: %v", err)
	}
	if endpoint == nil {
		t.Fatal("wakeUpService returned nil endpoint")
	}

	snap := obs.Snapshot()
	if snap["attempts"] != 1 {
		t.Fatalf("wake attempts = %v, want 1", snap["attempts"])
	}
	if snap[wakeResultAlreadyHealthy] != 1 {
		t.Fatalf("wake already healthy outcomes = %v, want 1", snap[wakeResultAlreadyHealthy])
	}
}

func TestWakeUpService_ObservabilityTimeout(t *testing.T) {
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			if r.URL.Query().Get("wait") != "" {
				time.Sleep(50 * time.Millisecond)
			}
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode([]consulServiceEntry{})
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			w.Write([]byte(`{"ID":"timeout-job","Name":"timeout-job","Type":"service"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/allocations") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]nomadAllocation{})
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{
				Status:     "dead",
				TaskGroups: []nomadJobTaskGroup{{Name: "main", Count: 0}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	obs := newWakeObservability(slog.Default())
	sw := &ScaleWaker{
		config:        &Config{ServiceName: "timeout-svc", JobName: "timeout-job", GroupName: "main"},
		service:       "timeout-svc",
		jobName:       "timeout-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       120 * time.Millisecond,
		observability: obs,
	}

	_, err := sw.wakeUpService(context.Background(), "timeout-svc", "timeout-job", "main")
	if err == nil {
		t.Fatal("expected wakeUpService to time out")
	}
	if !errors.Is(err, errWaitForHealthyTimeout) {
		t.Fatalf("wakeUpService error = %v, want wait timeout", err)
	}

	snap := obs.Snapshot()
	if snap["attempts"] != 1 {
		t.Fatalf("wake attempts = %v, want 1", snap["attempts"])
	}
	if snap[wakeResultWaitTimeout] != 1 {
		t.Fatalf("wake timeout outcomes = %v, want 1", snap[wakeResultWaitTimeout])
	}
}

func TestWakeUpService_ObservabilityContextCanceled(t *testing.T) {
	var waitStarted sync.Once
	waitStartedCh := make(chan struct{})

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode([]consulServiceEntry{})
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			w.Write([]byte(`{"ID":"cancel-job","Name":"cancel-job","Type":"service"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/allocations") && r.Method == http.MethodGet:
			waitStarted.Do(func() { close(waitStartedCh) })
			json.NewEncoder(w).Encode([]nomadAllocation{})
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{
				Status:     "dead",
				TaskGroups: []nomadJobTaskGroup{{Name: "main", Count: 0}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	obs := newWakeObservability(slog.Default())
	sw := &ScaleWaker{
		config:        &Config{ServiceName: "cancel-svc", JobName: "cancel-job", GroupName: "main"},
		service:       "cancel-svc",
		jobName:       "cancel-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       5 * time.Second,
		observability: obs,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := sw.wakeUpService(ctx, "cancel-svc", "cancel-job", "main")
		errCh <- err
	}()

	select {
	case <-waitStartedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("wakeUpService never started polling allocations")
	}
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected wakeUpService to return context cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wakeUpService error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wakeUpService did not return after context cancellation")
	}

	snap := obs.Snapshot()
	if snap["attempts"] != 1 {
		t.Fatalf("wake attempts = %v, want 1", snap["attempts"])
	}
	if snap[wakeResultContextCanceled] != 1 {
		t.Fatalf("wake canceled outcomes = %v, want 1", snap[wakeResultContextCanceled])
	}
}
