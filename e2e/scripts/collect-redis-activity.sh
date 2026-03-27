#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

label="${1:-snapshot}"
format="${2:-json}"
namespace="${E2E_ACTIVITY_STORE_NAMESPACE:-scale-to-zero}"
redis_addr="${E2E_REDIS_ADDR:-redis:6379}"
redis_host="${redis_addr%:*}"
redis_port="${redis_addr#*:}"
activity_prefix="${namespace}/activity/"

redis_cli() {
  if [ -n "${E2E_REDIS_PASSWORD:-}" ]; then
    redis-cli -h "$redis_host" -p "$redis_port" -a "$E2E_REDIS_PASSWORD" "$@"
  else
    redis-cli -h "$redis_host" -p "$redis_port" "$@"
  fi
}

activity_keys="$(redis_cli --scan --pattern "${activity_prefix}*" | sort)"
records=""

for key in $activity_keys; do
  value="$(redis_cli --raw GET "$key" 2>/dev/null || true)"
  service="${key#${activity_prefix}}"
  record="$(jq -nc \
    --arg key "$key" \
    --arg service "$service" \
    --arg last_activity "$value" \
    '{
      key: $key,
      service: $service,
      last_activity: (if $last_activity == "" then null else $last_activity end)
    }')"
  records="${records}${records:+
}${record}"
done

if [ -n "$records" ]; then
  activities_json="$(printf '%s\n' "$records" | jq -s '.')"
else
  activities_json='[]'
fi

case "$format" in
  text)
    echo "=== Redis activity ($label) ==="
    printf '%s\n' "$activities_json" | jq -r '.[] | "\(.service)\t\(.last_activity // "missing")"'
    ;;
  json)
    jq -n \
      --arg snapshot_label "$label" \
      --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg namespace "$namespace" \
      --argjson activities "$activities_json" \
      '{
        label: $snapshot_label,
        captured_at: $captured_at,
        namespace: $namespace,
        activity_count: ($activities | length),
        activities: $activities
      }'
    ;;
  *)
    echo "Unsupported redis activity format: $format" >&2
    exit 1
    ;;
esac
