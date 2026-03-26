//go:build !integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package traefik_plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Unit tests — no infrastructure required
// ---------------------------------------------------------------------------

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"first non-empty wins", []string{"a", "b"}, "a"},
		{"skip empty", []string{"", "b", "c"}, "b"},
		{"skip whitespace", []string{"  ", "b"}, "b"},
		{"all empty", []string{"", "", ""}, ""},
		{"single value", []string{"x"}, "x"},
		{"no values", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coalesce(tt.values...)
			if got != tt.want {
				t.Errorf("coalesce(%v) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}

func TestWrapJobRegister(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, result []byte)
	}{
		{
			name:  "unwrapped job with Stop=true",
			input: `{"ID":"test","Name":"test","Stop":true}`,
			check: func(t *testing.T, result []byte) {
				var w struct {
					Job map[string]interface{} `json:"Job"`
				}
				if err := json.Unmarshal(result, &w); err != nil {
					t.Fatal(err)
				}
				if w.Job == nil {
					t.Fatal("expected Job wrapper")
				}
				if w.Job["Stop"] != false {
					t.Errorf("Stop = %v, want false", w.Job["Stop"])
				}
			},
		},
		{
			name:  "already wrapped",
			input: `{"Job":{"ID":"test","Stop":true}}`,
			check: func(t *testing.T, result []byte) {
				var w struct {
					Job map[string]interface{} `json:"Job"`
				}
				if err := json.Unmarshal(result, &w); err != nil {
					t.Fatal(err)
				}
				if w.Job["Stop"] != false {
					t.Errorf("Stop = %v, want false", w.Job["Stop"])
				}
			},
		},
		{
			name:  "unwrapped without Stop field",
			input: `{"ID":"test","Name":"test"}`,
			check: func(t *testing.T, result []byte) {
				var w struct {
					Job map[string]interface{} `json:"Job"`
				}
				if err := json.Unmarshal(result, &w); err != nil {
					t.Fatal(err)
				}
				if w.Job["Stop"] != false {
					t.Errorf("Stop = %v, want false", w.Job["Stop"])
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   ``,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := wrapJobRegister([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name    string
		sw      *ScaleWaker
		host    string
		wantSvc string
		wantJob string
		wantGrp string
	}{
		{
			name:    "explicit config",
			sw:      &ScaleWaker{service: "mysvc", jobName: "myjob", group: "mygrp"},
			host:    "whatever.localhost",
			wantSvc: "mysvc", wantJob: "myjob", wantGrp: "mygrp",
		},
		{
			name:    "derived from Host header",
			sw:      &ScaleWaker{},
			host:    "echo-s2z.localhost",
			wantSvc: "echo-s2z", wantJob: "echo-s2z", wantGrp: "main",
		},
		{
			name:    "host with port",
			sw:      &ScaleWaker{},
			host:    "echo-s2z.localhost:8080",
			wantSvc: "echo-s2z", wantJob: "echo-s2z", wantGrp: "main",
		},
		{
			name:    "service only in config",
			sw:      &ScaleWaker{service: "mysvc"},
			host:    "whatever",
			wantSvc: "mysvc", wantJob: "mysvc", wantGrp: "main",
		},
		{
			name:    "job defaults to service",
			sw:      &ScaleWaker{},
			host:    "app.localhost",
			wantSvc: "app", wantJob: "app", wantGrp: "main",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Host = tt.host
			svc, job, grp := tt.sw.resolveTarget(req)
			if svc != tt.wantSvc {
				t.Errorf("service = %q, want %q", svc, tt.wantSvc)
			}
			if job != tt.wantJob {
				t.Errorf("job = %q, want %q", job, tt.wantJob)
			}
			if grp != tt.wantGrp {
				t.Errorf("group = %q, want %q", grp, tt.wantGrp)
			}
		})
	}
}

func TestJobSpecKey(t *testing.T) {
	tests := []struct {
		name      string
		configKey string
		job       string
		want      string
	}{
		{"default key", "", "echo-s2z", "scale-to-zero/jobs/echo-s2z"},
		{"custom key", "custom/path/job", "echo-s2z", "custom/path/job"},
		{"leading slash stripped", "", "/echo-s2z", "scale-to-zero/jobs/echo-s2z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw := &ScaleWaker{config: &Config{JobSpecKey: tt.configKey}}
			got := sw.jobSpecKey(tt.job)
			if got != tt.want {
				t.Errorf("jobSpecKey(%q) = %q, want %q", tt.job, got, tt.want)
			}
		})
	}
}

func TestIsEndpointReachable(t *testing.T) {
	sw := &ScaleWaker{}

	// nil URL
	if sw.isEndpointReachable(nil) {
		t.Error("nil URL should not be reachable")
	}

	// real listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	u, _ := url.Parse("http://" + ln.Addr().String())
	if !sw.isEndpointReachable(u) {
		t.Errorf("expected %s to be reachable", u)
	}

	// port that is almost certainly not listening
	u2, _ := url.Parse("http://127.0.0.1:1")
	if sw.isEndpointReachable(u2) {
		t.Error("port 1 should not be reachable")
	}
}

// ---------------------------------------------------------------------------
// Component tests — Nomad/Consul mocked via httptest
// ---------------------------------------------------------------------------

// backendInfo holds a running httptest backend and its parsed address.
type backendInfo struct {
	server  *httptest.Server
	host    string
	port    int
	entries []consulServiceEntry
}

func startBackend(t *testing.T, body string) *backendInfo {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	u, _ := url.Parse(srv.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	entries := []consulServiceEntry{{}}
	entries[0].Node.Address = host
	entries[0].Service.Address = host
	entries[0].Service.Port = port
	return &backendInfo{server: srv, host: host, port: port, entries: entries}
}

func TestServeHTTP_HealthyService(t *testing.T) {
	be := startBackend(t, "backend OK")
	defer be.server.Close()

	activityRecorded := false

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode(be.entries)
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				activityRecorded = true
				w.Write([]byte("true"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next called") }),
		config:        &Config{ServiceName: "test-svc", JobName: "test-job", GroupName: "main"},
		service:       "test-svc",
		jobName:       "test-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     "http://unused",
		activityStore: "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       30 * time.Second,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/hello", nil)
	req.Host = "test-svc.localhost"
	sw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "backend OK" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "backend OK")
	}
	if !activityRecorded {
		t.Error("activity was not recorded")
	}
}

func TestServeHTTP_WakeUpFromZero(t *testing.T) {
	be := startBackend(t, "woke up")
	defer be.server.Close()

	var scaled int32 // flips to 1 after scaleUp
	scaleUpCalled := false
	jobRegistered := false

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			if atomic.LoadInt32(&scaled) == 1 {
				json.NewEncoder(w).Encode(be.entries)
			} else {
				json.NewEncoder(w).Encode([]consulServiceEntry{})
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				w.Write([]byte("true"))
				return
			}
			// GET — return job spec
			w.Write([]byte(`{"ID":"test-job","Name":"test-job","Type":"service"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			scaleUpCalled = true
			atomic.StoreInt32(&scaled, 1)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			jobRegistered = true
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{Status: "dead"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next called") }),
		config:        &Config{ServiceName: "test-svc", JobName: "test-job", GroupName: "main"},
		service:       "test-svc",
		jobName:       "test-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		activityStore: "consul",
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       10 * time.Second,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "test-svc.localhost"
	sw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !jobRegistered {
		t.Error("job was not registered via POST /v1/jobs")
	}
	if !scaleUpCalled {
		t.Error("scale-up was not called")
	}
}

func TestServeHTTP_ConcurrentWakeupDedup(t *testing.T) {
	be := startBackend(t, "OK")
	defer be.server.Close()

	var scaled int32
	var scaleCount int32

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			if atomic.LoadInt32(&scaled) == 1 {
				json.NewEncoder(w).Encode(be.entries)
			} else {
				json.NewEncoder(w).Encode([]consulServiceEntry{})
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				w.Write([]byte("true"))
				return
			}
			w.Write([]byte(`{"ID":"test-job","Name":"test-job"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			atomic.AddInt32(&scaleCount, 1)
			atomic.StoreInt32(&scaled, 1)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{Status: "dead"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		config:        &Config{ServiceName: "test-svc", JobName: "test-job", GroupName: "main"},
		service:       "test-svc",
		jobName:       "test-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		activityStore: "consul",
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       10 * time.Second,
	}

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "test-svc.localhost"
			sw.ServeHTTP(rec, r)
		}()
	}
	wg.Wait()

	c := atomic.LoadInt32(&scaleCount)
	if c != 1 {
		t.Errorf("scale-up called %d times, want exactly 1 (dedup via mutex)", c)
	}
}

func TestServeHTTP_MissingServiceMapping(t *testing.T) {
	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		config:        &Config{},
		consulAddr:    "http://unused",
		nomadAddr:     "http://unused",
		activityStore: "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       30 * time.Second,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "" // empty host → empty service
	sw.ServeHTTP(rec, req)

	// resolveTarget with empty host yields service="" → 503
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestServeHTTP_Timeout(t *testing.T) {
	// Consul never returns healthy entries → should time out.
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			// Simulate blocking query delay to prevent tight loop
			if r.URL.Query().Get("wait") != "" {
				time.Sleep(200 * time.Millisecond)
			}
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode([]consulServiceEntry{})
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodGet {
				w.Write([]byte(`{"ID":"timeout-job","Name":"timeout-job"}`))
				return
			}
			w.Write([]byte("true"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{Status: "dead"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		config:        &Config{ServiceName: "to-svc", JobName: "to-job", GroupName: "main"},
		service:       "to-svc",
		jobName:       "to-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		activityStore: "consul",
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       1 * time.Second,
	}

	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "to-svc.localhost"
	sw.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v, expected ~1 s", elapsed)
	}
}

func TestServeHTTP_StaleConsulEntry(t *testing.T) {
	// Consul reports healthy but the endpoint is unreachable.
	// ScaleWaker should detect stale entry and trigger wake-up.
	be := startBackend(t, "fresh")
	defer be.server.Close()

	var wakeupDone int32

	// Stale entry pointing to unreachable port
	staleEntries := []consulServiceEntry{{}}
	staleEntries[0].Node.Address = "127.0.0.1"
	staleEntries[0].Service.Address = "127.0.0.1"
	staleEntries[0].Service.Port = 1 // unreachable

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			if atomic.LoadInt32(&wakeupDone) == 1 {
				json.NewEncoder(w).Encode(be.entries)
			} else {
				// Return stale entry (unreachable endpoint)
				json.NewEncoder(w).Encode(staleEntries)
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				w.Write([]byte("true"))
				return
			}
			w.Write([]byte(`{"ID":"stale-job","Name":"stale-job"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			atomic.StoreInt32(&wakeupDone, 1)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{Status: "running"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		config:        &Config{ServiceName: "stale-svc", JobName: "stale-job", GroupName: "main"},
		service:       "stale-svc",
		jobName:       "stale-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		activityStore: "consul",
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       10 * time.Second,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "stale-svc.localhost"
	sw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNew_Defaults(t *testing.T) {
	cfg := CreateConfig()
	h, err := New(context.Background(), http.NotFoundHandler(), cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sw := h.(*ScaleWaker)

	if sw.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", sw.timeout)
	}
	if sw.activityStore != "consul" {
		t.Errorf("activityStore = %q, want consul", sw.activityStore)
	}
	if sw.jobSpecStore != "consul" {
		t.Errorf("jobSpecStore = %q, want consul", sw.jobSpecStore)
	}
}

func TestNew_InvalidTimeout(t *testing.T) {
	cfg := &Config{Timeout: "notaduration"}
	_, err := New(context.Background(), http.NotFoundHandler(), cfg, "test")
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

// ---------------------------------------------------------------------------
// Stress tests
// ---------------------------------------------------------------------------

func TestStress_ServeHTTP_Burst(t *testing.T) {
	be := startBackend(t, "OK")
	defer be.server.Close()

	var scaled int32

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			if atomic.LoadInt32(&scaled) == 1 {
				json.NewEncoder(w).Encode(be.entries)
			} else {
				json.NewEncoder(w).Encode([]consulServiceEntry{})
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				w.Write([]byte("true"))
				return
			}
			w.Write([]byte(`{"ID":"burst-job","Name":"burst-job"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	var scaleCount atomic.Int32

	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scale") && r.Method == http.MethodPost:
			scaleCount.Add(1)
			atomic.StoreInt32(&scaled, 1)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/job/"):
			json.NewEncoder(w).Encode(nomadJobInfo{Status: "dead"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer nomad.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		config:        &Config{ServiceName: "burst-svc", JobName: "burst-job", GroupName: "main"},
		service:       "burst-svc",
		jobName:       "burst-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     nomad.URL,
		activityStore: "consul",
		jobSpecStore:  "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       10 * time.Second,
	}

	// Burst: 100 concurrent requests
	const n = 100
	var wg sync.WaitGroup
	var okCount, errCount atomic.Int32

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "burst-svc.localhost"
			sw.ServeHTTP(rec, r)
			if rec.Code == http.StatusOK {
				okCount.Add(1)
			} else {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()

	t.Logf("burst: ok=%d err=%d scaleUps=%d", okCount.Load(), errCount.Load(), scaleCount.Load())
	if sc := scaleCount.Load(); sc != 1 {
		t.Errorf("scale-up called %d times, want exactly 1", sc)
	}
	if oc := okCount.Load(); oc < int32(n/2) {
		t.Errorf("fewer than 50%% requests succeeded: %d / %d", oc, n)
	}
}

func TestStress_ServeHTTP_ActivityNonBlocking(t *testing.T) {
	// Activity store returns errors but requests should still succeed
	be := startBackend(t, "still works")
	defer be.server.Close()

	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
			w.Header().Set("X-Consul-Index", "1")
			json.NewEncoder(w).Encode(be.entries)
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			if r.Method == http.MethodPut {
				// Simulate activity store failure
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("store error"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer consul.Close()

	sw := &ScaleWaker{
		next:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next called") }),
		config:        &Config{ServiceName: "nb-svc", JobName: "nb-job", GroupName: "main"},
		service:       "nb-svc",
		jobName:       "nb-job",
		group:         "main",
		consulAddr:    consul.URL,
		nomadAddr:     "http://unused",
		activityStore: "consul",
		client:        &http.Client{Timeout: 5 * time.Second},
		timeout:       30 * time.Second,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/hello", nil)
	req.Host = "nb-svc.localhost"
	sw.ServeHTTP(rec, req)

	// Should still succeed despite activity store error
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (activity recording is non-blocking); body: %s",
			rec.Code, rec.Body.String())
	}
}
