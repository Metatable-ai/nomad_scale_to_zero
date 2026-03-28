#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Run all tests. Intended to execute inside the Docker test container.
#
# Usage (standalone):
#   docker compose -f docker-compose.test.yml up --build --abort-on-container-exit --exit-code-from tests
#
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

pass=0
fail=0

run_suite() {
  name="$1"
  dir="$2"
  shift 2
  printf "\n${CYAN}=== %s ===${NC}\n" "$name"
  if (cd "$dir" && go test -v -count=1 -timeout=300s "$@" ./...); then
    printf "${GREEN}✓ %s passed${NC}\n" "$name"
    pass=$((pass + 1))
  else
    printf "${RED}✗ %s FAILED${NC}\n" "$name"
    fail=$((fail + 1))
  fi
}

run_suite "activator (unit)"              /app/activator
run_suite "traefik-plugin (unit)"         /app/traefik-plugin
run_suite "traefik-plugin (integration)"  /app/traefik-plugin -tags=integration -run '^TestIntegration_'
run_suite "activity-store (unit)"         /app/activity-store
run_suite "activity-store (integration)"  /app/activity-store -tags=integration -run '^TestIntegration_'
run_suite "idle-scaler (unit)"            /app/idle-scaler
run_suite "idle-scaler (integration)"     /app/idle-scaler -tags=integration -run '^TestIntegration_'

printf "\n${CYAN}=== Summary ===${NC}\n"
printf "  passed: %d\n" "$pass"
printf "  failed: %d\n" "$fail"

if [ "$fail" -gt 0 ]; then
  printf "${RED}SOME TESTS FAILED${NC}\n"
  exit 1
fi

printf "${GREEN}ALL TESTS PASSED${NC}\n"
