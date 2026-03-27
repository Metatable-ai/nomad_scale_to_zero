#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
check_prefix="${1:-_nomad-check-}"
minimum_checks="${2:-1}"
timeout_seconds="${3:-120}"
service_name_filter="${4:-}"
consul_addr="${E2E_CONSUL_ADDR:-http://consul:8500}"

start="$(date +%s)"
while true; do
  if [ -n "${CONSUL_HTTP_TOKEN:-}" ]; then
    checks_json="$(curl -fsS -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" "$consul_addr/v1/agent/checks")"
  else
    checks_json="$(curl -fsS "$consul_addr/v1/agent/checks")"
  fi
  if [ -n "$service_name_filter" ]; then
    matching_checks="$(printf '%s' "$checks_json" | jq -c --arg prefix "$check_prefix" --arg service "$service_name_filter" '[to_entries[] | select(.key | startswith($prefix)) | .value | select((.ServiceName // "") == $service)]')"
  else
    matching_checks="$(printf '%s' "$checks_json" | jq -c --arg prefix "$check_prefix" '[to_entries[] | select(.key | startswith($prefix)) | .value]')"
  fi
  total="$(printf '%s' "$matching_checks" | jq 'length')"
  passing="$(printf '%s' "$matching_checks" | jq '[.[] | select(.Status == "passing")] | length')"
  critical="$(printf '%s' "$matching_checks" | jq '[.[] | select(.Status == "critical")] | length')"

  if [ "$total" -ge "$minimum_checks" ] && [ "$passing" -ge "$minimum_checks" ] && [ "$critical" -eq 0 ]; then
    echo "Consul checks ${check_prefix}* are healthy (passing=${passing}, total=${total})"
    exit 0
  fi

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    if [ -n "$service_name_filter" ]; then
      failure_message="Timed out waiting for Consul checks ${check_prefix}* for service ${service_name_filter} to become healthy (passing=${passing}, critical=${critical}, total=${total})"
    else
      failure_message="Timed out waiting for Consul checks ${check_prefix}* to become healthy (passing=${passing}, critical=${critical}, total=${total})"
    fi
    echo "$failure_message" >&2
    printf '%s' "$matching_checks" | jq '[.[] | {CheckID, Name, Status, ServiceID, ServiceName, Output}]' >&2 || true
    if [ -n "$service_name_filter" ]; then
      failure_context_json="$(jq -nc --arg failure_kind "timeout" --arg check_prefix "$check_prefix" --arg service_name "$service_name_filter" --argjson minimum_checks "$minimum_checks" --argjson passing "$passing" --argjson critical "$critical" --argjson total "$total" '{failure_kind: $failure_kind, check_prefix: $check_prefix, service_name: $service_name, minimum_checks: $minimum_checks, passing: $passing, critical: $critical, total: $total}')"
      E2E_FAILURE_MESSAGE="$failure_message" \
      E2E_FAILURE_CONTEXT_JSON="$failure_context_json" \
      E2E_FAILURE_CONSUL_AGENT_CHECKS_JSON="$matching_checks" \
        "$SCRIPT_DIR"/capture-service-failure-bundle.sh "$service_name_filter" "$service_name_filter" "consul-readiness-failed" || true
    fi
    exit 1
  fi

  sleep 2
done
