#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

expected_count="$1"
consul_addr="${E2E_CONSUL_ADDR:-http://consul:8500}"
timeout_seconds="${2:-120}"

start="$(date +%s)"
while true; do
  count=0
  missing_services=""
  i=1
  while [ "$i" -le "$expected_count" ]; do
    service_name="$(printf 'echo-s2z-%04d' "$i")"
    if [ -n "${CONSUL_HTTP_TOKEN:-}" ]; then
      service_json="$(curl -fsS -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" "$consul_addr/v1/health/service/$service_name" 2>/dev/null || printf '[]')"
    else
      service_json="$(curl -fsS "$consul_addr/v1/health/service/$service_name" 2>/dev/null || printf '[]')"
    fi

    service_instances="$(printf '%s' "$service_json" | jq 'length')"
    if [ "$service_instances" -gt 0 ]; then
      count=$((count + 1))
    else
      if [ -z "$missing_services" ]; then
        missing_services="$service_name"
      else
        missing_services="$missing_services\n$service_name"
      fi
    fi

    i=$((i + 1))
  done

  if [ "$count" -ge "$expected_count" ]; then
    echo "Consul has $count echo-s2z services visible via health lookups"
    exit 0
  fi

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    echo "Timed out waiting for $expected_count echo-s2z services in Consul health lookups (last count=$count)" >&2
    printf '%b\n' "$missing_services" | head -n 40 >&2 || true
    exit 1
  fi

  sleep 2
done