//go:build integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package traefik_plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testRedisAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("redis unreachable at %s: %v", addr, err)
	}
	conn.Close()
	return addr
}

func newTestScaleWaker(redisAddr string) *ScaleWaker {
	return &ScaleWaker{
		redisAddr: redisAddr,
		redisPass: os.Getenv("TEST_REDIS_PASSWORD"),
	}
}

func TestIntegration_RESPParser_RoundTrip(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:rt:%d", time.Now().UnixNano())
	value := `{"ID":"test-job","Name":"test-job","Type":"service","Meta":{"scale-to-zero.enabled":"true"}}`

	if err := sw.setRedisValue(ctx, key, value); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != value {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", string(got), value)
	}
}

func TestIntegration_RESPParser_LargePayload(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:large:%d", time.Now().UnixNano())
	obj := map[string]interface{}{"ID": "large-job", "Meta": map[string]string{}}
	for i := 0; i < 500; i++ {
		obj["Meta"].(map[string]string)[fmt.Sprintf("k%04d", i)] = strings.Repeat("v", 80)
	}
	payload, _ := json.Marshal(obj)
	t.Logf("payload size: %d bytes", len(payload))

	if err := sw.setRedisValue(ctx, key, string(payload)); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !json.Valid(got) {
		t.Fatalf("returned invalid JSON (%d bytes)", len(got))
	}
	if len(got) != len(payload) {
		t.Fatalf("payload truncated: got %d / %d bytes", len(got), len(payload))
	}
	t.Log("large payload round-trip OK")
}

func TestIntegration_RESPParser_100KB(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:100kb:%d", time.Now().UnixNano())
	big := strings.Repeat("X", 100*1024)

	if err := sw.setRedisValue(ctx, key, big); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get 100KB: %v", err)
	}
	if string(got) != big {
		t.Fatalf("100KB mismatch: got %d bytes, want %d", len(got), len(big))
	}
}

func TestIntegration_RESPParser_1MB(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:1mb:%d", time.Now().UnixNano())
	big := strings.Repeat("A", 1*1024*1024)

	if err := sw.setRedisValue(ctx, key, big); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get 1MB: %v", err)
	}
	if len(got) != len(big) {
		t.Fatalf("1MB mismatch: got %d bytes, want %d", len(got), len(big))
	}
	if string(got) != big {
		t.Fatal("1MB content mismatch")
	}
}

func TestIntegration_RESPParser_5MB(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:5mb:%d", time.Now().UnixNano())
	big := strings.Repeat("B", 5*1024*1024)

	if err := sw.setRedisValue(ctx, key, big); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get 5MB: %v", err)
	}
	if len(got) != len(big) {
		t.Fatalf("5MB mismatch: got %d bytes, want %d", len(got), len(big))
	}
}

func TestIntegration_RESPParser_2MB_JSON(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:2mbjson:%d", time.Now().UnixNano())
	obj := map[string]interface{}{"ID": "huge-job", "Meta": map[string]string{}}
	for i := 0; i < 5000; i++ {
		obj["Meta"].(map[string]string)[fmt.Sprintf("key_%05d", i)] = strings.Repeat("v", 400)
	}
	payload, _ := json.Marshal(obj)
	t.Logf("JSON payload size: %d bytes (%.1f MB)", len(payload), float64(len(payload))/(1024*1024))

	if err := sw.setRedisValue(ctx, key, string(payload)); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := sw.getRedisValue(ctx, key)
	if err != nil {
		t.Fatalf("get 2MB JSON: %v", err)
	}
	if !json.Valid(got) {
		t.Fatalf("returned invalid JSON (%d bytes)", len(got))
	}
	if len(got) != len(payload) {
		t.Fatalf("JSON payload truncated: got %d / %d bytes", len(got), len(payload))
	}
}

func TestIntegration_RESPParser_NotFound(t *testing.T) {
	addr := testRedisAddr(t)
	sw := newTestScaleWaker(addr)
	ctx := context.Background()

	key := fmt.Sprintf("test:resp:missing:%d", time.Now().UnixNano())
	_, err := sw.getRedisValue(ctx, key)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestIntegration_Stress_RESP_ConcurrentReads(t *testing.T) {
	addr := testRedisAddr(t)
	ctx := context.Background()

	sw := newTestScaleWaker(addr)
	prefix := fmt.Sprintf("test:stress:cr:%d", time.Now().UnixNano())
	sizes := []int{100, 1024, 10 * 1024, 50 * 1024, 100 * 1024}
	keys := make([]string, len(sizes))
	for i, sz := range sizes {
		keys[i] = fmt.Sprintf("%s:%d", prefix, i)
		val := strings.Repeat("D", sz)
		if err := sw.setRedisValue(ctx, keys[i], val); err != nil {
			t.Fatalf("seed key %d (%d bytes): %v", i, sz, err)
		}
	}

	const goroutines = 50
	const readsPerGoroutine = 20
	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			sw := newTestScaleWaker(addr)
			for i := 0; i < readsPerGoroutine; i++ {
				idx := i % len(keys)
				got, err := sw.getRedisValue(ctx, keys[idx])
				if err != nil {
					errCount.Add(1)
					continue
				}
				if len(got) != sizes[idx] {
					errCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if ec := errCount.Load(); ec > 0 {
		t.Errorf("%d / %d reads failed or truncated", ec, goroutines*readsPerGoroutine)
	}
}

func TestIntegration_Stress_RESP_ConcurrentWrites(t *testing.T) {
	addr := testRedisAddr(t)
	ctx := context.Background()

	const goroutines = 50
	const writesPerGoroutine = 20
	prefix := fmt.Sprintf("test:stress:cw:%d", time.Now().UnixNano())
	var errCount atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			sw := newTestScaleWaker(addr)
			for i := 0; i < writesPerGoroutine; i++ {
				key := fmt.Sprintf("%s:%d:%d", prefix, id, i)
				val := strings.Repeat("W", 1024*(1+(i%10)))
				if err := sw.setRedisValue(ctx, key, val); err != nil {
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

func TestIntegration_Stress_RESP_ConcurrentReadWrite(t *testing.T) {
	addr := testRedisAddr(t)
	ctx := context.Background()

	key := fmt.Sprintf("test:stress:rw:%d", time.Now().UnixNano())
	val := strings.Repeat("S", 50*1024)

	sw := newTestScaleWaker(addr)
	if err := sw.setRedisValue(ctx, key, val); err != nil {
		t.Fatal(err)
	}

	const goroutines = 30
	var readErrs, writeErrs atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines * 2)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			sw := newTestScaleWaker(addr)
			for i := 0; i < 20; i++ {
				newVal := strings.Repeat("S", 50*1024+i)
				if err := sw.setRedisValue(ctx, key, newVal); err != nil {
					writeErrs.Add(1)
				}
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			sw := newTestScaleWaker(addr)
			for i := 0; i < 20; i++ {
				got, err := sw.getRedisValue(ctx, key)
				if err != nil {
					readErrs.Add(1)
					continue
				}
				if len(got) < 50*1024 {
					readErrs.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if re := readErrs.Load(); re > 0 {
		t.Errorf("%d reads failed or truncated", re)
	}
	if we := writeErrs.Load(); we > 0 {
		t.Errorf("%d writes failed", we)
	}
}

func TestIntegration_Stress_RESP_LargePayloadSequential(t *testing.T) {
	addr := testRedisAddr(t)
	ctx := context.Background()

	sizes := []int{
		1 * 1024,
		10 * 1024,
		100 * 1024,
		512 * 1024,
		1024 * 1024,
		2 * 1024 * 1024,
	}

	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dKB", sz/1024), func(t *testing.T) {
			sw := newTestScaleWaker(addr)
			key := fmt.Sprintf("test:stress:seq:%d:%d", sz, time.Now().UnixNano())
			val := strings.Repeat("P", sz)

			if err := sw.setRedisValue(ctx, key, val); err != nil {
				t.Fatalf("set %d bytes: %v", sz, err)
			}

			got, err := sw.getRedisValue(ctx, key)
			if err != nil {
				t.Fatalf("get %d bytes: %v", sz, err)
			}
			if len(got) != sz {
				t.Fatalf("size mismatch: got %d, want %d", len(got), sz)
			}
		})
	}
}
