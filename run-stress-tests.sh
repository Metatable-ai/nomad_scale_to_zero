#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

pass=0
fail=0

timestamp_utc() {
  date -u '+%Y-%m-%dT%H:%M:%SZ'
}

: "${STRESS_RUN_ID:=stress-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
: "${STRESS_AUTOMATION_JOB_NAME:=store-pressure}"
: "${STRESS_ARTIFACTS_DIR:=}"

RUN_STARTED_AT="$(timestamp_utc)"
SUITE_RESULTS_FILE=""

if [ -n "$STRESS_ARTIFACTS_DIR" ]; then
  mkdir -p "$STRESS_ARTIFACTS_DIR"
  SUITE_RESULTS_FILE="$STRESS_ARTIFACTS_DIR/suite-results.tsv"
  printf 'suite\tstatus\tdirectory\n' > "$SUITE_RESULTS_FILE"

  {
    printf 'run_id=%s\n' "$STRESS_RUN_ID"
    printf 'automation_job_name=%s\n' "$STRESS_AUTOMATION_JOB_NAME"
    printf 'started_at=%s\n' "$RUN_STARTED_AT"
    printf 'workers=%s\n' "${STRESS_WORKERS:-200}"
    printf 'duration=%s\n' "${STRESS_DURATION:-20s}"
    printf 'sample_interval=%s\n' "${STRESS_SAMPLE_INTERVAL:-250ms}"
    printf 'service_keys=%s\n' "${STRESS_SERVICE_KEYS:-2000}"
    printf 'job_keys=%s\n' "${STRESS_JOB_KEYS:-1000}"
    printf 'job_spec_size=%s\n' "${STRESS_JOB_SPEC_SIZE:-32768}"
    printf 'go_test_timeout=%s\n' "${STRESS_GO_TEST_TIMEOUT:-20m}"
  } > "$STRESS_ARTIFACTS_DIR/run-metadata.env"
fi

record_suite_result() {
  suite_name="$1"
  suite_status="$2"
  suite_dir="$3"

  if [ -n "$SUITE_RESULTS_FILE" ]; then
    printf '%s\t%s\t%s\n' "$suite_name" "$suite_status" "$suite_dir" >> "$SUITE_RESULTS_FILE"
  fi
}

write_summary_artifacts() {
  summary_status="$1"

  if [ -z "$STRESS_ARTIFACTS_DIR" ]; then
    return 0
  fi

  {
    printf 'run_id=%s\n' "$STRESS_RUN_ID"
    printf 'automation_job_name=%s\n' "$STRESS_AUTOMATION_JOB_NAME"
    printf 'started_at=%s\n' "$RUN_STARTED_AT"
    printf 'finished_at=%s\n' "$(timestamp_utc)"
    printf 'passed=%d\n' "$pass"
    printf 'failed=%d\n' "$fail"
    printf 'status=%s\n' "$summary_status"
  } > "$STRESS_ARTIFACTS_DIR/summary.env"
}

run_suite() {
  name="$1"
  dir="$2"
  shift 2
  printf "\n${CYAN}=== %s ===${NC}\n" "$name"
  if (cd "$dir" && go test -v -count=1 -timeout="${STRESS_GO_TEST_TIMEOUT:-20m}" "$@" ./...); then
    printf "${GREEN}✓ %s passed${NC}\n" "$name"
    pass=$((pass + 1))
    suite_status=passed
  else
    printf "${RED}✗ %s FAILED${NC}\n" "$name"
    fail=$((fail + 1))
    suite_status=failed
  fi

  record_suite_result "$name" "$suite_status" "$dir"
}

printf "${CYAN}Redis stress configuration${NC}\n"
printf "  STRESS_RUN_ID=%s\n" "$STRESS_RUN_ID"
printf "  STRESS_AUTOMATION_JOB_NAME=%s\n" "$STRESS_AUTOMATION_JOB_NAME"
printf "  STRESS_WORKERS=%s\n" "${STRESS_WORKERS:-200}"
printf "  STRESS_DURATION=%s\n" "${STRESS_DURATION:-20s}"
printf "  STRESS_SAMPLE_INTERVAL=%s\n" "${STRESS_SAMPLE_INTERVAL:-250ms}"
printf "  STRESS_SERVICE_KEYS=%s\n" "${STRESS_SERVICE_KEYS:-2000}"
printf "  STRESS_JOB_KEYS=%s\n" "${STRESS_JOB_KEYS:-1000}"
printf "  STRESS_JOB_SPEC_SIZE=%s\n" "${STRESS_JOB_SPEC_SIZE:-32768}"
if [ -n "$STRESS_ARTIFACTS_DIR" ]; then
  printf "  STRESS_ARTIFACTS_DIR=%s\n" "$STRESS_ARTIFACTS_DIR"
fi

run_suite "traefik-plugin (redis RESP stress)" /app/traefik-plugin -tags=integration -run '^TestIntegration_Stress_RESP_'
run_suite "activity-store (redis workload highload)" /app/activity-store -tags=stress -run '^TestStress_RedisHighLoadWorkload$'

printf "\n${CYAN}=== Stress Summary ===${NC}\n"
printf "  passed: %d\n" "$pass"
printf "  failed: %d\n" "$fail"

if [ "$fail" -gt 0 ]; then
  write_summary_artifacts failed
  printf "${RED}STRESS SUITES FAILED${NC}\n"
  exit 1
fi

write_summary_artifacts passed
printf "${GREEN}ALL STRESS SUITES PASSED${NC}\n"
