#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

label="${1:-snapshot}"
format="${2:-${E2E_REDIS_INFO_FORMAT:-text}}"
redis_addr="${E2E_REDIS_ADDR:-redis:6379}"
redis_host="${redis_addr%:*}"
redis_port="${redis_addr#*:}"
redis_auth_args=""

if [ -n "${E2E_REDIS_PASSWORD:-}" ]; then
  redis_auth_args="-a ${E2E_REDIS_PASSWORD}"
fi

# shellcheck disable=SC2086
redis_info="$(redis-cli -h "$redis_host" -p "$redis_port" $redis_auth_args INFO memory cpu stats clients)"

field_value() {
  key="$1"
  printf '%s\n' "$redis_info" | awk -F: -v key="$key" '$1 == key { sub(/\r$/, "", $2); print $2; exit }'
}

used_memory_human="$(field_value used_memory_human)"
used_memory_peak_human="$(field_value used_memory_peak_human)"
used_cpu_sys="$(field_value used_cpu_sys)"
used_cpu_user="$(field_value used_cpu_user)"
instantaneous_ops_per_sec="$(field_value instantaneous_ops_per_sec)"
connected_clients="$(field_value connected_clients)"
expired_keys="$(field_value expired_keys)"

case "$format" in
  text)
    echo "=== Redis info ($label) ==="
    printf '%s\n' "$redis_info" | grep -E '^(used_memory_human|used_memory_peak_human|used_cpu_sys|used_cpu_user|instantaneous_ops_per_sec|connected_clients|expired_keys):' || true
    ;;
  json)
    jq -n \
      --arg snapshot_label "$label" \
      --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg used_memory_human "$used_memory_human" \
      --arg used_memory_peak_human "$used_memory_peak_human" \
      --arg used_cpu_sys "$used_cpu_sys" \
      --arg used_cpu_user "$used_cpu_user" \
      --arg instantaneous_ops_per_sec "$instantaneous_ops_per_sec" \
      --arg connected_clients "$connected_clients" \
      --arg expired_keys "$expired_keys" \
      '
      def num_or_null:
        if . == "" then null else (try tonumber catch null) end;

      {
        "label": $snapshot_label,
        captured_at: $captured_at,
        metrics: {
          used_memory_human: $used_memory_human,
          used_memory_peak_human: $used_memory_peak_human,
          used_cpu_sys: ($used_cpu_sys | num_or_null),
          used_cpu_user: ($used_cpu_user | num_or_null),
          instantaneous_ops_per_sec: ($instantaneous_ops_per_sec | num_or_null),
          connected_clients: ($connected_clients | num_or_null),
          expired_keys: ($expired_keys | num_or_null)
        }
      }
      '
    ;;
  *)
    echo "Unsupported redis info format: $format" >&2
    exit 1
    ;;
esac
