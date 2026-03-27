#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
job_name="$1"
expected_running="$2"
timeout_seconds="${3:-120}"
nomad_addr="${E2E_NOMAD_ADDR:-http://nomad-server:4646}"

start="$(date +%s)"
while true; do
  if [ -n "${NOMAD_TOKEN:-}" ]; then
    job_allocations_json="$(curl -fsS -H "X-Nomad-Token: $NOMAD_TOKEN" "$nomad_addr/v1/job/$job_name/allocations" 2>/dev/null || true)"
  else
    job_allocations_json="$(curl -fsS "$nomad_addr/v1/job/$job_name/allocations" 2>/dev/null || true)"
  fi
  if [ -n "$job_allocations_json" ]; then
    running="$(printf '%s' "$job_allocations_json" | jq -r '[.[] | select(.ClientStatus == "running")] | length')"
  else
    running=0
  fi

  if [ "$running" = "$expected_running" ]; then
    echo "Job $job_name reached running=$running"
    exit 0
  fi

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    failure_message="Timed out waiting for $job_name to reach running=$expected_running (last running=$running)"
    echo "$failure_message" >&2
    if [ -n "${NOMAD_TOKEN:-}" ]; then
      curl -fsS -H "X-Nomad-Token: $NOMAD_TOKEN" "$nomad_addr/v1/job/$job_name/allocations" || true
    else
      curl -fsS "$nomad_addr/v1/job/$job_name/allocations" || true
    fi
    failure_context_json="$(jq -nc --arg failure_kind "timeout" --arg job_name "$job_name" --argjson expected_running "$expected_running" --argjson last_running "$running" '{failure_kind: $failure_kind, job_name: $job_name, expected_running: $expected_running, last_running: $last_running}')"
    E2E_FAILURE_MESSAGE="$failure_message" \
    E2E_FAILURE_CONTEXT_JSON="$failure_context_json" \
    E2E_FAILURE_NOMAD_ALLOCATIONS_JSON="${job_allocations_json:-}" \
      "$SCRIPT_DIR"/capture-service-failure-bundle.sh "$job_name" "$job_name" "nomad-readiness-failed" || true
    exit 1
  fi

  sleep 2
done
