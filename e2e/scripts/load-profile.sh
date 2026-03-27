#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

e2e_profile_managed_vars='
E2E_PROFILE_DESCRIPTION
E2E_AUTOMATION_SMOKE_JOB
E2E_AUTOMATION_CERTIFICATION_JOB
E2E_AUTOMATION_JOB_NAME
E2E_TARGET_NOMAD_SERVERS
E2E_TARGET_NOMAD_CLIENTS
E2E_TARGET_CONSUL_SERVERS
E2E_TARGET_TRAEFIK_REPLICAS
E2E_TARGET_REDIS_NODES
E2E_TARGET_IDLE_SCALER_PLACEMENT
E2E_TARGET_IDLE_SCALER_ISOLATION_MODE
E2E_WORKLOAD_MIX_LABELS
E2E_WORKLOAD_FAST_API_COUNT
E2E_WORKLOAD_SLOW_START_COUNT
E2E_WORKLOAD_DEPENDENCY_SENSITIVE_COUNT
E2E_JOB_COUNT
E2E_NOMAD_CLIENTS
E2E_IDLE_TIMEOUT
E2E_IDLE_CHECK_INTERVAL
E2E_MIN_SCALE_DOWN_AGE
E2E_SOAK_CYCLES
E2E_SCENARIO_SET
E2E_TRAFFIC_SHAPE
E2E_TRAFFIC_SCENARIO
E2E_K6_TARGET_MODE
E2E_WARMUP_VUS
E2E_WARMUP_DURATION
E2E_BURST_VUS
E2E_BURST_DURATION
E2E_STORM_START_RATE
E2E_STORM_RATE
E2E_STORM_DURATION
E2E_STORM_PREALLOCATED_VUS
E2E_STORM_MAX_VUS
E2E_GATE_TRAFFIC_SUCCESS_RATE
E2E_GATE_WAKE_P95_MS
E2E_GATE_WAKE_P99_MS
E2E_GATE_SCALE_TO_ZERO_MAX_SECONDS
E2E_GATE_DEPENDENCY_READY_MAX_SECONDS
'

capture_e2e_profile_overrides() {
  if [ "${E2E_PROFILE_OVERRIDES_CAPTURED:-0}" = "1" ]; then
    return 0
  fi

  for var_name in $e2e_profile_managed_vars; do
    eval "var_is_set=\${${var_name}+x}"
    if [ "${var_is_set:-}" = "x" ]; then
      eval "var_value=\${$var_name}"
      if [ -z "$var_value" ]; then
        continue
      fi
      eval "E2E_PROFILE_OVERRIDE_SET_${var_name}=1"
      eval "E2E_PROFILE_OVERRIDE_VALUE_${var_name}=\$var_value"
      export "E2E_PROFILE_OVERRIDE_SET_${var_name}=1"
      export "E2E_PROFILE_OVERRIDE_VALUE_${var_name}=$var_value"
    fi
  done

  export E2E_PROFILE_OVERRIDES_CAPTURED=1
}

clear_e2e_profile_values() {
  for var_name in $e2e_profile_managed_vars; do
    unset "$var_name"
  done
}

restore_e2e_profile_overrides() {
  for var_name in $e2e_profile_managed_vars; do
    eval "override_is_set=\${E2E_PROFILE_OVERRIDE_SET_${var_name}+x}"
    if [ "${override_is_set:-}" = "x" ]; then
      eval "override_value=\${E2E_PROFILE_OVERRIDE_VALUE_${var_name}}"
      export "$var_name=$override_value"
    fi
  done
}

e2e_profile_workload_var_name() {
  label="$1"
  suffix="$2"
  normalized_label="$(printf '%s' "$label" | tr -d '[:space:]' | tr '[:lower:]-' '[:upper:]_' | tr -cd 'A-Z0-9_')"
  printf 'E2E_WORKLOAD_%s_%s' "$normalized_label" "$suffix"
}

derive_e2e_job_count() {
  labels="${E2E_WORKLOAD_MIX_LABELS:-}"

  if [ -z "$labels" ]; then
    printf '%s' "${E2E_JOB_COUNT:-0}"
    return 0
  fi

  total=0
  for raw_label in $(printf '%s' "$labels" | tr ',' ' '); do
    label="$(printf '%s' "$raw_label" | tr -d '[:space:]')"
    [ -n "$label" ] || continue

    count_var_name="$(e2e_profile_workload_var_name "$label" COUNT)"
    eval "count_value=\${$count_var_name:-0}"
    case "$count_value" in
      ''|*[!0-9]*)
        echo "Invalid ${count_var_name}: ${count_value}" >&2
        return 1
        ;;
    esac

    total=$((total + count_value))
  done

  printf '%s' "$total"
}

load_e2e_profile() {
  if [ -z "${ROOT_DIR:-}" ]; then
    echo "ROOT_DIR must be set before calling load_e2e_profile" >&2
    return 1
  fi

  profile_name="${1:-${E2E_PROFILE:-certification}}"
  profile_file="$ROOT_DIR/e2e/profiles/${profile_name}.sh"

  if [ ! -f "$profile_file" ]; then
    echo "Unknown E2E profile: $profile_name" >&2
    return 1
  fi

  capture_e2e_profile_overrides
  clear_e2e_profile_values

  # shellcheck disable=SC1090
  . "$profile_file"

  restore_e2e_profile_overrides

  if [ "${E2E_PROFILE_OVERRIDE_SET_E2E_JOB_COUNT:-0}" != "1" ]; then
    derived_job_count="$(derive_e2e_job_count)" || return 1
    export E2E_JOB_COUNT="$derived_job_count"
  fi

  export E2E_PROFILE="$profile_name"
  export E2E_PROFILE_FILE="$profile_file"
}
