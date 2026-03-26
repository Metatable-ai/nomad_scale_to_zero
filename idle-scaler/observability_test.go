//go:build !integration

// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"
	"time"
)

func TestScalerObservabilityObserveRun(t *testing.T) {
	tests := []struct {
		name                string
		runErr              error
		wantSuccessRecorded bool
	}{
		{
			name:                "successful run records success timestamp",
			wantSuccessRecorded: true,
		},
		{
			name:   "failed run does not record success timestamp",
			runErr: errors.New("boom"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := newScalerObservability("idle-scaler-test")

			if got := obs.lastRunUnix.Load(); got != 0 {
				t.Fatalf("initial lastRunUnix = %d, want 0", got)
			}
			if got := obs.lastSuccessUnix.Load(); got != 0 {
				t.Fatalf("initial lastSuccessUnix = %d, want 0", got)
			}

			obs.observeRun(25*time.Millisecond, tt.runErr)

			if got := obs.lastRunUnix.Load(); got == 0 {
				t.Fatal("lastRunUnix was not recorded")
			}

			successRecorded := obs.lastSuccessUnix.Load() != 0
			if successRecorded != tt.wantSuccessRecorded {
				t.Fatalf("success recorded = %t, want %t", successRecorded, tt.wantSuccessRecorded)
			}
		})
	}
}
