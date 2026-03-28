// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestScaleDownControllerScalesIdleJob(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	jobCount := 1
	scaledDown := false

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-test":
			_, _ = io.WriteString(w, `{"Status":"running","TaskGroups":[{"Name":"main","Count":`+strconv.Itoa(jobCount)+`}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/job/echo-s2z-test/scale":
			scaledDown = true
			jobCount = 0
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Logf("unexpected nomad request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer nomad.Close()

	store := &fakeStateStore{
		workloads: map[string]WorkloadRegistration{
			"echo.test.local": {
				HostName:    "echo.test.local",
				ServiceName: "echo-svc",
				JobName:     "echo-s2z-test",
				GroupName:   "main",
			},
		},
		activityTimes: map[string]time.Time{
			"echo-svc": time.Now().UTC().Add(-10 * time.Minute), // idle for 10min
		},
	}

	controller := &ScaleDownController{
		logger:          testLogger(),
		store:           store,
		nomadAddr:       nomad.URL,
		client:          nomad.Client(),
		idleTimeout:     5 * time.Minute,
		scanInterval:    1 * time.Second,
		minScaleDownAge: 1 * time.Minute,
		ownerID:         "test-owner",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	controller.scanAndScaleDown(ctx)

	mu.Lock()
	defer mu.Unlock()
	if !scaledDown {
		t.Fatal("expected job to be scaled down")
	}
}

func TestScaleDownControllerSkipsRecentActivity(t *testing.T) {
	t.Parallel()

	scaledDown := false
	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-test":
			_, _ = io.WriteString(w, `{"Status":"running","TaskGroups":[{"Name":"main","Count":1}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/job/echo-s2z-test/scale":
			scaledDown = true
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer nomad.Close()

	store := &fakeStateStore{
		workloads: map[string]WorkloadRegistration{
			"echo.test.local": {
				HostName:    "echo.test.local",
				ServiceName: "echo-svc",
				JobName:     "echo-s2z-test",
				GroupName:   "main",
			},
		},
		activityTimes: map[string]time.Time{
			"echo-svc": time.Now().UTC().Add(-1 * time.Minute), // active 1min ago
		},
	}

	controller := &ScaleDownController{
		logger:          testLogger(),
		store:           store,
		nomadAddr:       nomad.URL,
		client:          nomad.Client(),
		idleTimeout:     5 * time.Minute,
		scanInterval:    1 * time.Second,
		minScaleDownAge: 1 * time.Minute,
		ownerID:         "test-owner",
	}

	ctx := context.Background()
	controller.scanAndScaleDown(ctx)

	if scaledDown {
		t.Fatal("expected job NOT to be scaled down (recent activity)")
	}
}

func TestScaleDownControllerSkipsPendingActivation(t *testing.T) {
	t.Parallel()

	scaledDown := false
	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/job/echo-s2z-test":
			_, _ = io.WriteString(w, `{"Status":"running","TaskGroups":[{"Name":"main","Count":1}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/job/echo-s2z-test/scale":
			scaledDown = true
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer nomad.Close()

	store := &fakeStateStore{
		workloads: map[string]WorkloadRegistration{
			"echo.test.local": {
				HostName:    "echo.test.local",
				ServiceName: "echo-svc",
				JobName:     "echo-s2z-test",
				GroupName:   "main",
			},
		},
		activityTimes: map[string]time.Time{
			"echo-svc": time.Now().UTC().Add(-10 * time.Minute),
		},
		activationStates: map[string]ActivationState{
			"echo.test.local": {Status: activationStatusPending},
		},
	}

	controller := &ScaleDownController{
		logger:          testLogger(),
		store:           store,
		nomadAddr:       nomad.URL,
		client:          nomad.Client(),
		idleTimeout:     5 * time.Minute,
		scanInterval:    1 * time.Second,
		minScaleDownAge: 1 * time.Minute,
		ownerID:         "test-owner",
	}

	ctx := context.Background()
	controller.scanAndScaleDown(ctx)

	if scaledDown {
		t.Fatal("expected job NOT to be scaled down (activation pending)")
	}
}
