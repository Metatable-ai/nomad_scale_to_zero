//go:build integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	consul "github.com/hashicorp/consul/api"
)

func testConsulAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_CONSUL_ADDR")
	if addr == "" {
		t.Skip("TEST_CONSUL_ADDR not set")
	}
	return addr
}

func newTestConsulJobSpecStore(t *testing.T) *ConsulJobSpecStore {
	t.Helper()
	addr := testConsulAddr(t)
	cfg := consul.DefaultConfig()
	cfg.Address = addr
	client, err := consul.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &ConsulJobSpecStore{client: client}
}

func TestIntegration_ConsulJobSpecStore_RoundTrip(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("test-job-%d", time.Now().UnixNano())
	spec := []byte(`{"ID":"` + jobID + `","Name":"test","Type":"service"}`)

	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, ok, err := store.GetJobSpec(jobID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected found")
	}
	if string(got) != string(spec) {
		t.Errorf("spec mismatch:\n  got:  %s\n  want: %s", got, spec)
	}

	_ = store.DeleteJobSpec(jobID)
}

func TestIntegration_ConsulJobSpecStore_NotFound(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	_, ok, err := store.GetJobSpec(fmt.Sprintf("nonexistent-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected not found")
	}
}

func TestIntegration_ConsulJobSpecStore_SetIfChanged(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("test-change-%d", time.Now().UnixNano())
	spec1 := []byte(`{"ID":"` + jobID + `","V":1}`)
	spec2 := []byte(`{"ID":"` + jobID + `","V":2}`)

	changed, err := store.SetJobSpecIfChanged(jobID, spec1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first write: expected changed=true")
	}

	changed, err = store.SetJobSpecIfChanged(jobID, spec1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("same data: expected changed=false")
	}

	changed, err = store.SetJobSpecIfChanged(jobID, spec2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("different data: expected changed=true")
	}

	_ = store.DeleteJobSpec(jobID)
}

func TestIntegration_ConsulJobSpecStore_InvalidJSON(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	err := store.SetJobSpec("bad", []byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestIntegration_ConsulJobSpecStore_Delete(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("test-del-%d", time.Now().UnixNano())
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
		t.Error("expected deleted")
	}
}

func TestIntegration_ConsulJobSpecStore_LargeSpec(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("test-lg-consul-%d", time.Now().UnixNano())
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

	_ = store.DeleteJobSpec(jobID)
}

func TestIntegration_ConsulJobSpecStore_StressConcurrentWrites(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	const goroutines = 30
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

func TestIntegration_ConsulJobSpecStore_StressConcurrentReadWrite(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("consul-rw-stress-%d", time.Now().UnixNano())
	spec := []byte(fmt.Sprintf(`{"ID":"%s","V":0}`, jobID))
	if err := store.SetJobSpec(jobID, spec); err != nil {
		t.Fatal(err)
	}
	defer store.DeleteJobSpec(jobID)

	const goroutines = 20
	var readErrs, writeErrs atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines * 2)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
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
			for i := 0; i < 15; i++ {
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

func TestIntegration_ConsulJobSpecStore_StressSetIfChanged(t *testing.T) {
	store := newTestConsulJobSpecStore(t)

	jobID := fmt.Sprintf("consul-sic-stress-%d", time.Now().UnixNano())
	defer store.DeleteJobSpec(jobID)

	const iterations = 50
	var changedCount, unchangedCount int

	for i := 0; i < iterations; i++ {
		v := i % 3
		spec := []byte(fmt.Sprintf(`{"ID":"%s","V":%d}`, jobID, v))
		changed, err := store.SetJobSpecIfChanged(jobID, spec)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if changed {
			changedCount++
		} else {
			unchangedCount++
		}
	}

	t.Logf("changed=%d unchanged=%d (out of %d)", changedCount, unchangedCount, iterations)
	if changedCount < 3 {
		t.Errorf("expected at least 3 changed writes, got %d", changedCount)
	}
}
