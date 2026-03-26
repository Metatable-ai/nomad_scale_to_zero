//go:build integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package activitystore

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func consulTestAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_CONSUL_ADDR")
	if addr == "" {
		t.Skip("TEST_CONSUL_ADDR not set")
	}
	return addr
}

func newTestConsulStore(t *testing.T) *ConsulStore {
	t.Helper()
	addr := consulTestAddr(t)
	store, err := NewConsulStore(addr, "test-ns")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIntegration_ConsulStore_RoundTrip(t *testing.T) {
	store := newTestConsulStore(t)

	svc := fmt.Sprintf("consul-rt-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Nanosecond)

	if err := store.SetActivity(svc, now); err != nil {
		t.Fatalf("SetActivity: %v", err)
	}

	got, ok, err := store.LastActivity(svc)
	if err != nil {
		t.Fatalf("LastActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity to be found")
	}
	if !got.Equal(now) {
		t.Errorf("time mismatch: got %v, want %v", got, now)
	}
}

func TestIntegration_ConsulStore_NotFound(t *testing.T) {
	store := newTestConsulStore(t)

	_, ok, err := store.LastActivity(fmt.Sprintf("nonexistent-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected activity not found")
	}
}

func TestIntegration_ConsulStore_Overwrite(t *testing.T) {
	store := newTestConsulStore(t)

	svc := fmt.Sprintf("consul-ow-%d", time.Now().UnixNano())
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)

	if err := store.SetActivity(svc, t1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActivity(svc, t2); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.LastActivity(svc)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected activity")
	}
	if !got.Equal(t2) {
		t.Errorf("expected latest time %v, got %v", t2, got)
	}
}

// ---------------------------------------------------------------------------
// Stress tests
// ---------------------------------------------------------------------------

func TestIntegration_ConsulStore_StressConcurrentWrites(t *testing.T) {
	store := newTestConsulStore(t)

	const goroutines = 50
	const writesPerGoroutine = 20

	svc := fmt.Sprintf("consul-stress-%d", time.Now().UnixNano())
	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				ts := time.Now().UTC().Add(time.Duration(id*1000+i) * time.Millisecond)
				if err := store.SetActivity(svc, ts); err != nil {
					errCount.Add(1)
					t.Logf("goroutine %d write %d: %v", id, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	if ec := errCount.Load(); ec > 0 {
		t.Errorf("%d / %d writes failed", ec, goroutines*writesPerGoroutine)
	}

	// Verify we can still read successfully
	_, ok, err := store.LastActivity(svc)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !ok {
		t.Fatal("expected activity after stress writes")
	}
}

func TestIntegration_ConsulStore_StressConcurrentReadWrite(t *testing.T) {
	store := newTestConsulStore(t)

	svc := fmt.Sprintf("consul-rw-stress-%d", time.Now().UnixNano())

	// Seed initial value
	if err := store.SetActivity(svc, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	const goroutines = 30
	var readErrs, writeErrs atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines * 2)
	// Writers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := store.SetActivity(svc, time.Now().UTC()); err != nil {
					writeErrs.Add(1)
				}
			}
		}(g)
	}
	// Readers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, _, err := store.LastActivity(svc)
				if err != nil {
					readErrs.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	if re := readErrs.Load(); re > 0 {
		t.Errorf("%d reads failed", re)
	}
	if we := writeErrs.Load(); we > 0 {
		t.Errorf("%d writes failed", we)
	}
}

func TestIntegration_ConsulStore_StressManySvcs(t *testing.T) {
	store := newTestConsulStore(t)

	const numServices = 100
	base := fmt.Sprintf("consul-many-%d", time.Now().UnixNano())

	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(numServices)
	for i := 0; i < numServices; i++ {
		go func(idx int) {
			defer wg.Done()
			svc := fmt.Sprintf("%s-%04d", base, idx)
			ts := time.Now().UTC()
			if err := store.SetActivity(svc, ts); err != nil {
				errCount.Add(1)
				return
			}
			got, ok, err := store.LastActivity(svc)
			if err != nil || !ok {
				errCount.Add(1)
				return
			}
			if !got.Equal(ts) {
				errCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if ec := errCount.Load(); ec > 0 {
		t.Errorf("%d / %d services had errors", ec, numServices)
	}
}
