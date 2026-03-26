#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -e

# Export defaults so docker compose receives them even when users invoke this
# script without pre-exporting variables in their shell.
: "${STRESS_WORKERS:=400}"
: "${STRESS_DURATION:=60s}"
: "${STRESS_JOB_SPEC_SIZE:=65536}"
: "${STRESS_SERVICE_KEYS:=4000}"
: "${STRESS_JOB_KEYS:=2000}"
: "${STRESS_SAMPLE_INTERVAL:=250ms}"
: "${STRESS_GO_TEST_TIMEOUT:=20m}"

export STRESS_WORKERS
export STRESS_DURATION
export STRESS_JOB_SPEC_SIZE
export STRESS_SERVICE_KEYS
export STRESS_JOB_KEYS
export STRESS_SAMPLE_INTERVAL
export STRESS_GO_TEST_TIMEOUT

printf 'Running Docker stress test with:\n'
printf '  STRESS_WORKERS=%s\n' "$STRESS_WORKERS"
printf '  STRESS_DURATION=%s\n' "$STRESS_DURATION"
printf '  STRESS_JOB_SPEC_SIZE=%s\n' "$STRESS_JOB_SPEC_SIZE"
printf '  STRESS_SERVICE_KEYS=%s\n' "$STRESS_SERVICE_KEYS"
printf '  STRESS_JOB_KEYS=%s\n' "$STRESS_JOB_KEYS"
printf '  STRESS_SAMPLE_INTERVAL=%s\n' "$STRESS_SAMPLE_INTERVAL"
printf '  STRESS_GO_TEST_TIMEOUT=%s\n' "$STRESS_GO_TEST_TIMEOUT"

exec docker compose -f docker-compose.stress.yml up --build --abort-on-container-exit --exit-code-from stress