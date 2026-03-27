#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)"
scenario="${E2E_TRAFFIC_SCENARIO:-storm}"
target_mode="${E2E_K6_TARGET_MODE:-random}"

case "$scenario" in
  coldstart|warmup|storm|rolling)
    script_file="$ROOT_DIR/e2e/k6/${scenario}.js"
    ;;
  *)
    echo "Unknown E2E_TRAFFIC_SCENARIO: $scenario" >&2
    exit 1
    ;;
esac

if [ "$target_mode" = "fixed" ]; then
  echo "Running k6 scenario $scenario against ${E2E_K6_SERVICE_NAME:-echo-s2z-0001}"
else
  echo "Running k6 scenario $scenario with ${target_mode} job selection across ${E2E_JOB_COUNT:-10} jobs"
fi

round_robin_width="${E2E_K6_ROUND_ROBIN_WIDTH:-}"
if [ -z "$round_robin_width" ]; then
  case "$scenario" in
    warmup)
      round_robin_width="${E2E_WARMUP_VUS:-5}"
      ;;
    rolling)
      round_robin_width="${E2E_BURST_VUS:-50}"
      ;;
    storm)
      round_robin_width="${E2E_STORM_MAX_VUS:-}"
      if [ -z "$round_robin_width" ]; then
        round_robin_width="${E2E_STORM_PREALLOCATED_VUS:-150}"
      fi
      ;;
    coldstart)
      if [ "$target_mode" = "fixed" ]; then
        round_robin_width=1
      else
        round_robin_width="${E2E_JOB_COUNT:-10}"
      fi
      ;;
  esac
fi
export E2E_K6_ROUND_ROBIN_WIDTH="$round_robin_width"

if [ -n "${E2E_K6_SUMMARY_FILE:-}" ]; then
  mkdir -p "$(dirname "$E2E_K6_SUMMARY_FILE")"
fi

if [ -n "${E2E_K6_RESULTS_FILE:-}" ]; then
  mkdir -p "$(dirname "$E2E_K6_RESULTS_FILE")"
fi

export K6_SUMMARY_TREND_STATS="${E2E_K6_SUMMARY_TREND_STATS:-avg,min,med,max,p(90),p(95),p(99)}"

set -- run
if [ -n "${E2E_K6_SUMMARY_FILE:-}" ]; then
  set -- "$@" --summary-export "$E2E_K6_SUMMARY_FILE"
fi
if [ -n "${E2E_K6_RESULTS_FILE:-}" ]; then
  set -- "$@" --out "json=${E2E_K6_RESULTS_FILE}"
fi
set -- "$@" "$script_file"

k6 "$@"
