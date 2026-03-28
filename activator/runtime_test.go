// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
