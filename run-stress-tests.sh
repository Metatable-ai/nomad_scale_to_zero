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

run_suite() {
  name="$1"
  dir="$2"
  shift 2
  printf "\n${CYAN}=== %s ===${NC}\n" "$name"
  if (cd "$dir" && go test -v -count=1 -timeout="${STRESS_GO_TEST_TIMEOUT:-20m}" "$@" ./...); then
    printf "${GREEN}✓ %s passed${NC}\n" "$name"
    pass=$((pass + 1))
  else
    printf "${RED}✗ %s FAILED${NC}\n" "$name"
    fail=$((fail + 1))
  fi
}

printf "${CYAN}Redis stress configuration${NC}\n"
printf "  STRESS_WORKERS=%s\n" "${STRESS_WORKERS:-200}"
printf "  STRESS_DURATION=%s\n" "${STRESS_DURATION:-20s}"
printf "  STRESS_SAMPLE_INTERVAL=%s\n" "${STRESS_SAMPLE_INTERVAL:-250ms}"
printf "  STRESS_SERVICE_KEYS=%s\n" "${STRESS_SERVICE_KEYS:-2000}"
printf "  STRESS_JOB_KEYS=%s\n" "${STRESS_JOB_KEYS:-1000}"
printf "  STRESS_JOB_SPEC_SIZE=%s\n" "${STRESS_JOB_SPEC_SIZE:-32768}"

run_suite "traefik-plugin (redis RESP stress)" /app/traefik-plugin -tags=integration -run '^TestIntegration_Stress_RESP_'
run_suite "activity-store (redis workload highload)" /app/activity-store -tags=stress -run '^TestStress_RedisHighLoadWorkload$'

printf "\n${CYAN}=== Stress Summary ===${NC}\n"
printf "  passed: %d\n" "$pass"
printf "  failed: %d\n" "$fail"

if [ "$fail" -gt 0 ]; then
  printf "${RED}STRESS SUITES FAILED${NC}\n"
  exit 1
fi

printf "${GREEN}ALL STRESS SUITES PASSED${NC}\n"