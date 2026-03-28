// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func hasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

func TestNomadRuntimeActivateUsesSharedReadyState(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	store := &fakeStateStore{
		activationStates: map[string]ActivationState{
			"echo-s2z-0001.localhost": {
				Status:   activationStatusReady,
				Endpoint: backend.URL,
			},
		},
	}
	runtime := &nomadRuntime{
		logger:         testLogger(),
		store:          store,
		client:         backend.Client(),
		requestTimeout: 45 * time.Second,
		activationTTL:  90 * time.Second,
		probePath:      "/healthz",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	endpoint, err := runtime.Activate(ctx, testWorkload())
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if endpoint == nil || endpoint.String() != backend.URL {
		t.Fatalf("Activate() endpoint = %v, want %s", endpoint, backend.URL)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	cached := store.readyEndpoints["echo-s2z-0001.localhost"]
	if cached == nil || cached.String() != backend.URL {
		t.Fatalf("ready endpoint cache = %v, want %s", cached, backend.URL)
	}
}

func TestNomadRuntimeActivateWaitsForPendingActivationState(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	store := &fakeStateStore{
		activationStates: map[string]ActivationState{
			"echo-s2z-0001.localhost": {
				Status: activationStatusPending,
				Owner:  "winner",
			},
		},
	}
	runtime := &nomadRuntime{
		logger:         testLogger(),
		store:          store,
		client:         backend.Client(),
		requestTimeout: 45 * time.Second,
		activationTTL:  90 * time.Second,
		probePath:      "/healthz",
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = store.SetActivationState(context.Background(), "echo-s2z-0001.localhost", ActivationState{
			Status:   activationStatusReady,
			Owner:    "winner",
			Endpoint: backend.URL,
		}, time.Second)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	endpoint, err := runtime.Activate(ctx, testWorkload())
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if endpoint == nil || endpoint.String() != backend.URL {
		t.Fatalf("Activate() endpoint = %v, want %s", endpoint, backend.URL)
	}
}

func TestNomadRuntimePerformWakeSkipsRegisterForDeadJob(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	backendURL := mustURL(t, backend.URL)
	backendPort, err := strconv.Atoi(backendURL.Port())
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}

	var mu sync.Mutex
	jobCount := 0
	scaleCalls := 0

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && hasPrefix(r.URL.Path, "/v1/event/stream") {
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}

		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-0001":
			_, _ = io.WriteString(w, `{"Status":"dead","TaskGroups":[{"Name":"main","Count":`+strconv.Itoa(jobCount)+`}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/job/echo-s2z-0001/scale":
			scaleCalls++
			jobCount = 1
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-0001/allocations":
			if jobCount == 0 {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			payload := `[{
				"ID":"alloc-1",
				"TaskGroup":"main",
				"ClientStatus":"running",
				"TaskStates":{"echo":{"State":"running"}},
				"Resources":{"Networks":[{"IP":"` + backendURL.Hostname() + `","DynamicPorts":[{"Label":"http","Value":` + strconv.Itoa(backendPort) + `}]}]}
			}]`
			_, _ = io.WriteString(w, payload)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			t.Fatalf("unexpected job register request for existing dead job")
		default:
			t.Fatalf("unexpected nomad request %s %s", r.Method, r.URL.String())
		}
	}))
	defer nomad.Close()

	runtime := &nomadRuntime{
		logger:         testLogger(),
		store:          &fakeStateStore{},
		client:         nomad.Client(),
		nomadAddr:      nomad.URL,
		requestTimeout: 45 * time.Second,
		activationTTL:  90 * time.Second,
		probePath:      "/healthz",
	}

	endpoint, err := runtime.performWake(context.Background(), testWorkload(), testLogger())
	if err != nil {
		t.Fatalf("performWake() error = %v", err)
	}
	if endpoint == nil || endpoint.String() != backend.URL {
		t.Fatalf("performWake() endpoint = %v, want %s", endpoint, backend.URL)
	}

	mu.Lock()
	defer mu.Unlock()
	if scaleCalls != 1 {
		t.Fatalf("scaleCalls = %d, want 1", scaleCalls)
	}
}

func TestNomadRuntimeWaitForNomadAllocationReassertsScaleUp(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	backendURL := mustURL(t, backend.URL)
	backendPort, err := strconv.Atoi(backendURL.Port())
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}

	var mu sync.Mutex
	jobCount := 0
	scaleCalls := 0

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Event stream must not hold the mutex — it blocks until context cancels.
		if r.Method == http.MethodGet && hasPrefix(r.URL.Path, "/v1/event/stream") {
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}

		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-0001":
			_, _ = io.WriteString(w, `{"Status":"dead","TaskGroups":[{"Name":"main","Count":`+strconv.Itoa(jobCount)+`}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/job/echo-s2z-0001/scale":
			scaleCalls++
			jobCount = 1
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-0001/allocations":
			if jobCount == 0 {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			payload := `[{
				"ID":"alloc-1",
				"TaskGroup":"main",
				"ClientStatus":"running",
				"TaskStates":{"echo":{"State":"running"}},
				"Resources":{"Networks":[{"IP":"` + backendURL.Hostname() + `","DynamicPorts":[{"Label":"http","Value":` + strconv.Itoa(backendPort) + `}]}]}
			}]`
			_, _ = io.WriteString(w, payload)
		default:
			t.Fatalf("unexpected nomad request %s %s", r.Method, r.URL.String())
		}
	}))
	defer nomad.Close()

	runtime := &nomadRuntime{
		logger:         testLogger(),
		store:          &fakeStateStore{},
		client:         nomad.Client(),
		nomadAddr:      nomad.URL,
		requestTimeout: 45 * time.Second,
		activationTTL:  90 * time.Second,
		probePath:      "/healthz",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	endpoint, err := runtime.waitForNomadAllocation(ctx, "echo-s2z-0001", "main")
	if err != nil {
		t.Fatalf("waitForNomadAllocation() error = %v", err)
	}
	if endpoint == nil || endpoint.String() != backend.URL {
		t.Fatalf("waitForNomadAllocation() endpoint = %v, want %s", endpoint, backend.URL)
	}

	mu.Lock()
	defer mu.Unlock()
	if scaleCalls == 0 {
		t.Fatalf("scaleCalls = %d, want at least 1 reasserted scale-up", scaleCalls)
	}
}

func TestNomadRuntimeWaitForAllocationUsesEventStream(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	backendURL := mustURL(t, backend.URL)
	backendPort, err := strconv.Atoi(backendURL.Port())
	if err != nil {
		t.Fatalf("parse backend port: %v", err)
	}

	var mu sync.Mutex
	allocReady := false
	allocPollCount := 0

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && hasPrefix(r.URL.Path, "/v1/event/stream") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Wait a bit then send an AllocationUpdated event with running status.
			time.Sleep(100 * time.Millisecond)

			mu.Lock()
			allocReady = true
			mu.Unlock()

			event := `{"Index":1,"Events":[{"Topic":"Allocation","Type":"AllocationUpdated","Key":"alloc-evt-1","Payload":{"Allocation":{"ID":"alloc-evt-1","JobID":"echo-s2z-0001","TaskGroup":"main","ClientStatus":"running","TaskStates":{"echo":{"State":"running"}}}}}]}` + "\n"
			_, _ = io.WriteString(w, event)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}

		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-0001/allocations":
			allocPollCount++
			if !allocReady {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			payload := `[{
				"ID":"alloc-evt-1",
				"TaskGroup":"main",
				"ClientStatus":"running",
				"TaskStates":{"echo":{"State":"running"}},
				"Resources":{"Networks":[{"IP":"` + backendURL.Hostname() + `","DynamicPorts":[{"Label":"http","Value":` + strconv.Itoa(backendPort) + `}]}]}
			}]`
			_, _ = io.WriteString(w, payload)
		default:
			t.Logf("nomad request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer nomad.Close()

	runtime := &nomadRuntime{
		logger:         testLogger(),
		store:          &fakeStateStore{},
		client:         nomad.Client(),
		nomadAddr:      nomad.URL,
		requestTimeout: 45 * time.Second,
		activationTTL:  90 * time.Second,
		probePath:      "/healthz",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	endpoint, err := runtime.waitForNomadAllocation(ctx, "echo-s2z-0001", "main")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("waitForNomadAllocation() error = %v", err)
	}
	if endpoint == nil || endpoint.String() != backend.URL {
		t.Fatalf("waitForNomadAllocation() endpoint = %v, want %s", endpoint, backend.URL)
	}

	// The event stream sends the event after 100ms, so we should complete
	// reasonably quickly via the event-driven path.
	if elapsed > 2*time.Second {
		t.Fatalf("waitForNomadAllocation() took %s, want < 2s (event-driven)", elapsed)
	}

	mu.Lock()
	polls := allocPollCount
	mu.Unlock()

	t.Logf("completed in %s with %d alloc polls (event-driven)", elapsed.Round(time.Millisecond), polls)
}
