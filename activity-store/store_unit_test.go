//go:build !integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package activitystore

import "testing"

func TestRedisStore_KeyFormat(t *testing.T) {
	store := &RedisStore{prefix: "scale-to-zero/activity/"}
	tests := []struct {
		service string
		want    string
	}{
		{"echo-s2z", "scale-to-zero/activity/echo-s2z"},
		{"/leading-slash", "scale-to-zero/activity/leading-slash"},
		{"simple", "scale-to-zero/activity/simple"},
	}
	for _, tt := range tests {
		got := store.key(tt.service)
		if got != tt.want {
			t.Errorf("key(%q) = %q, want %q", tt.service, got, tt.want)
		}
	}
}

func TestConsulStore_KeyFormat(t *testing.T) {
	store := &ConsulStore{prefix: "my-ns/activity/"}
	key := store.key("my-service")
	want := "my-ns/activity/my-service"
	if key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	data := []byte(`{"ID":"test","Version":1}`)
	h1 := computeHash(data)
	h2 := computeHash(data)
	if h1 != h2 {
		t.Errorf("non-deterministic: %s != %s", h1, h2)
	}
}

func TestComputeHash_DifferentInputs(t *testing.T) {
	h1 := computeHash([]byte(`{"Version":1}`))
	h2 := computeHash([]byte(`{"Version":2}`))
	if h1 == h2 {
		t.Errorf("different inputs produced same hash: %s", h1)
	}
}

func TestComputeHash_Empty(t *testing.T) {
	h := computeHash(nil)
	if h == "" {
		t.Error("hash of nil should not be empty string")
	}
}

func TestRedisJobSpecStore_KeyFormat(t *testing.T) {
	store := &RedisJobSpecStore{prefix: "scale-to-zero/jobs/"}
	tests := []struct {
		jobID string
		want  string
	}{
		{"echo-s2z", "scale-to-zero/jobs/echo-s2z"},
		{"/leading-slash", "scale-to-zero/jobs/leading-slash"},
	}
	for _, tt := range tests {
		got := store.key(tt.jobID)
		if got != tt.want {
			t.Errorf("key(%q) = %q, want %q", tt.jobID, got, tt.want)
		}
	}
}
