//go:build integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package activitystore

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestRedisJobStore(t *testing.T) *RedisJobSpecStore {
	t.Helper()
	addr := redisTestAddr(t) // reuses helper from redis_test.go
	cfg := RedisConfig{
		Addr:     addr,
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
		DB:       0,
	}
	store, err := NewRedisJobSpecStore(cfg, "test-ns")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIntegration_RedisJobSpecStore_RoundTrip(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("test-job-%d", time.Now().UnixNano())
	spec := []byte(`{"ID":"` + jobID + `","Name":"test","Type":"service"}`)

	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatalf("SetJobSpec: %v", err)
	}

	got, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatalf("GetJobSpec: %v", err)
	}
	if !ok {
		t.Fatal("expected job spec to be found")
	}
	if string(got) != string(spec) {
		t.Errorf("spec mismatch:\n  got:  %s\n  want: %s", got, spec)
	}
}

func TestIntegration_RedisJobSpecStore_NotFound(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	_, ok, err := store.GetJobSpec(fmt.Sprintf("nonexistent-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected not found")
	}
}

func TestIntegration_RedisJobSpecStore_SetIfChanged(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("test-change-%d", time.Now().UnixNano())
	spec1 := []byte(`{"ID":"` + jobID + `","Version":1}`)
	spec2 := []byte(`{"ID":"` + jobID + `","Version":2}`)

	// First write — always changed
	changed, err := store.SetJobSpecIfChanged(jobID, spec1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first write should report changed=true")
	}

	// Same spec — should be no-op
	changed, err = store.SetJobSpecIfChanged(jobID, spec1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("identical spec should report changed=false")
	}

	// Different spec — changed again
	changed, err = store.SetJobSpecIfChanged(jobID, spec2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("different spec should report changed=true")
	}

	// Verify latest version stored
	got, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected job spec")
	}
	if string(got) != string(spec2) {
		t.Errorf("got %s, want %s", got, spec2)
	}
}

func TestIntegration_RedisJobSpecStore_Delete(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("test-delete-%d", time.Now().UnixNano())
	spec := []byte(`{"ID":"` + jobID + `"}`)

	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJobSpec(jobID); err != nil {
		t.Fatal(err)
	}

	_, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected deleted spec to not be found")
	}
}

func TestIntegration_RedisJobSpecStore_InvalidJSON(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	err := store.SetJobSpec("invalid-json", []byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestIntegration_RedisJobSpecStore_LargeSpec(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("test-lg-%d", time.Now().UnixNano())
	// Build ~30 KB spec
	obj := map[string]interface{}{"ID": jobID, "Meta": map[string]string{}}
	for i := 0; i < 300; i++ {
		obj["Meta"].(map[string]string)[fmt.Sprintf("k%04d", i)] = fmt.Sprintf("v%0200d", i)
	}
	spec, _ := json.Marshal(obj)
	t.Logf("spec size: %d bytes", len(spec))

	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected found")
	}
	if len(got) != len(spec) {
		t.Errorf("length mismatch: got %d, want %d", len(got), len(spec))
	}
}

// ---------------------------------------------------------------------------
// Stress tests
// ---------------------------------------------------------------------------

func TestIntegration_RedisJobSpecStore_StressConcurrentWrites(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	const goroutines = 50
	const writesPerGoroutine = 10
	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				jobID := fmt.Sprintf("stress-%d-%d-%d", time.Now().UnixNano(), id, i)
				spec := []byte(fmt.Sprintf(`{"ID":"%s","V":%d}`, jobID, i))
				if err := store.SetJobSpec(jobID, spec); err != nil {
					errCount.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	if ec := errCount.Load(); ec > 0 {
		t.Errorf("%d / %d writes failed", ec, goroutines*writesPerGoroutine)
	}
}

func TestIntegration_RedisJobSpecStore_StressConcurrentReadWrite(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("rw-stress-%d", time.Now().UnixNano())
	spec := []byte(fmt.Sprintf(`{"ID":"%s","V":0}`, jobID))
	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatal(err)
	}

	const goroutines = 30
	var readErrs, writeErrs atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines * 2)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				newSpec := []byte(fmt.Sprintf(`{"ID":"%s","V":%d}`, jobID, id*100+i))
				if err := store.SetJobSpec(jobID, newSpec); err != nil {
					writeErrs.Add(1)
				}
			}
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, _, err := store.GetJobSpec(jobID)
				if err != nil {
					readErrs.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if re := readErrs.Load(); re > 0 {
		t.Errorf("%d reads failed", re)
	}
	if we := writeErrs.Load(); we > 0 {
		t.Errorf("%d writes failed", we)
	}
}

func TestIntegration_RedisJobSpecStore_StressSetIfChanged(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("sic-stress-%d", time.Now().UnixNano())

	const goroutines = 20
	const iterations = 30
	var changedCount, unchangedCount atomic.Int64
	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Alternate between two specs to test dedup
				v := i % 3
				spec := []byte(fmt.Sprintf(`{"ID":"%s","V":%d}`, jobID, v))
				changed, err := store.SetJobSpecIfChanged(jobID, spec)
				if err != nil {
					errCount.Add(1)
					continue
				}
				if changed {
					changedCount.Add(1)
				} else {
					unchangedCount.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	t.Logf("changed=%d unchanged=%d errors=%d",
		changedCount.Load(), unchangedCount.Load(), errCount.Load())

	if ec := errCount.Load(); ec > 0 {
		t.Errorf("%d errors", ec)
	}
}

func TestIntegration_RedisJobSpecStore_Stress1MBSpec(t *testing.T) {
	store := newTestRedisJobStore(t)
	defer store.Close()

	jobID := fmt.Sprintf("1mb-spec-%d", time.Now().UnixNano())
	obj := map[string]interface{}{
		"ID":   jobID,
		"Meta": map[string]string{},
	}
	// ~1MB of meta
	for i := 0; i < 2000; i++ {
		obj["Meta"].(map[string]string)[fmt.Sprintf("k%05d", i)] = strings.Repeat("v", 500)
	}
	spec, _ := json.Marshal(obj)
	t.Logf("spec size: %d bytes (%.1f MB)", len(spec), float64(len(spec))/(1024*1024))

	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatalf("set 1MB spec: %v", err)
	}

	got, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatalf("get 1MB spec: %v", err)
	}
	if !ok {
		t.Fatal("expected found")
	}
	if len(got) != len(spec) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(spec))
	}
}
