#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

job_prefix="$1"
expected_running="$2"
match_mode="${3:-exact}"
timeout_seconds="${4:-120}"
nomad_addr="${E2E_NOMAD_ADDR:-http://nomad-server:4646}"

start="$(date +%s)"
while true; do
  if [ -n "${NOMAD_TOKEN:-}" ]; then
    allocations_json="$(curl -fsS -H "X-Nomad-Token: $NOMAD_TOKEN" "$nomad_addr/v1/allocations")"
  else
    allocations_json="$(curl -fsS "$nomad_addr/v1/allocations")"
  fi
  total_running="$(printf '%s' "$allocations_json" | jq -r --arg prefix "$job_prefix" '[.[] | select(.JobID != null and (.JobID | startswith($prefix)) and .ClientStatus == "running")] | length')"

  case "$match_mode" in
    at-least)
      if [ "$total_running" -ge "$expected_running" ]; then
        echo "Jobs ${job_prefix}* reached running=${total_running} (threshold >= ${expected_running})"
        exit 0
      fi
      ;;
    exact)
      if [ "$total_running" -eq "$expected_running" ]; then
        echo "Jobs ${job_prefix}* reached running=${total_running}"
        exit 0
      fi
      ;;
    *)
      echo "Unknown match mode: $match_mode" >&2
      exit 1
      ;;
  esac

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    echo "Timed out waiting for jobs ${job_prefix}* to reach running=${expected_running} (last running=${total_running}, mode=${match_mode})" >&2
    printf '%s' "$allocations_json" | jq -r --arg prefix "$job_prefix" '[.[] | select(.JobID != null and (.JobID | startswith($prefix))) | {job: .JobID, desired: .DesiredStatus, client: .ClientStatus}] | sort_by(.job)' >&2 || true
    exit 1
  fi

  sleep 2
done