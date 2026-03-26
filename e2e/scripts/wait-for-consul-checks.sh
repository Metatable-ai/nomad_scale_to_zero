#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

check_prefix="${1:-_nomad-check-}"
minimum_checks="${2:-1}"
timeout_seconds="${3:-120}"
consul_addr="${E2E_CONSUL_ADDR:-http://consul:8500}"

start="$(date +%s)"
while true; do
  if [ -n "${CONSUL_HTTP_TOKEN:-}" ]; then
    checks_json="$(curl -fsS -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" "$consul_addr/v1/agent/checks")"
  else
    checks_json="$(curl -fsS "$consul_addr/v1/agent/checks")"
  fi
  matching_checks="$(printf '%s' "$checks_json" | jq -c --arg prefix "$check_prefix" '[to_entries[] | select(.key | startswith($prefix)) | .value]')"
  total="$(printf '%s' "$matching_checks" | jq 'length')"
  passing="$(printf '%s' "$matching_checks" | jq '[.[] | select(.Status == "passing")] | length')"
  critical="$(printf '%s' "$matching_checks" | jq '[.[] | select(.Status == "critical")] | length')"

  if [ "$total" -ge "$minimum_checks" ] && [ "$passing" -ge "$minimum_checks" ] && [ "$critical" -eq 0 ]; then
    echo "Consul checks ${check_prefix}* are healthy (passing=${passing}, total=${total})"
    exit 0
  fi

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    echo "Timed out waiting for Consul checks ${check_prefix}* to become healthy (passing=${passing}, critical=${critical}, total=${total})" >&2
    printf '%s' "$matching_checks" | jq '[.[] | {CheckID, Name, Status, ServiceID, ServiceName, Output}]' >&2 || true
    exit 1
  fi

  sleep 2
done