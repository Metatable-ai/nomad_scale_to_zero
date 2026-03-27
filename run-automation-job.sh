#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

timestamp_run_id() {
  date -u '+%Y%m%dT%H%M%SZ'
}

slugify() {
  value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9._-' '-')"
  value="${value#-}"
  value="${value%-}"
  if [ -z "$value" ]; then
    value="automation"
  fi
  printf '%s' "$value"
}

set_default() {
  var_name="$1"
  default_value="$2"

  eval "current_value=\${$var_name-}"
  if [ -z "$current_value" ]; then
    export "$var_name=$default_value"
  fi
}

print_usage() {
  cat <<'EOF' >&2
Usage: ./run-automation-job.sh <job>

Available jobs:
  smoke
  prod-profile
  certification
  soak
  resilience
  store-pressure
  release-gate

Set AUTOMATION_PRESET_ONLY=1 to print the resolved preset without starting Docker.
EOF
}

if [ "$#" -ne 1 ]; then
  print_usage
  exit 64
fi

job_name="$1"
job_kind=""
job_purpose=""
runner=""

case "$job_name" in
  smoke)
    job_kind="e2e"
    job_purpose="Minimal smoke coverage across wake, idle, and revival paths"
    set_default E2E_PROFILE smoke
    set_default E2E_AUTOMATION_JOB_NAME smoke
    set_default E2E_AUTOMATION_SMOKE_JOB smoke
    set_default E2E_AUTOMATION_CERTIFICATION_JOB prod-profile
    ;;
  prod-profile|certification|certification-profile)
    job_kind="e2e"
    job_purpose="Production-shaped certification profile for readiness evidence"
    set_default E2E_PROFILE certification
    set_default E2E_AUTOMATION_JOB_NAME prod-profile
    set_default E2E_AUTOMATION_SMOKE_JOB smoke
    set_default E2E_AUTOMATION_CERTIFICATION_JOB prod-profile
    ;;
  soak)
    job_kind="e2e"
    job_purpose="Extended mixed-traffic soak on the certification topology"
    set_default E2E_PROFILE certification
    set_default E2E_AUTOMATION_JOB_NAME soak
    set_default E2E_AUTOMATION_SMOKE_JOB smoke
    set_default E2E_AUTOMATION_CERTIFICATION_JOB prod-profile
    set_default E2E_SCENARIO_SET mixed-traffic
    set_default E2E_TRAFFIC_SHAPE warmup,rolling,storm
    set_default E2E_TRAFFIC_SCENARIO storm
    set_default E2E_SOAK_CYCLES 12
    ;;
  resilience)
    job_kind="e2e"
    job_purpose="Recovery and restart resilience across dead-job and idle-scaler faults"
    set_default E2E_PROFILE certification
    set_default E2E_AUTOMATION_JOB_NAME resilience
    set_default E2E_AUTOMATION_SMOKE_JOB smoke
    set_default E2E_AUTOMATION_CERTIFICATION_JOB prod-profile
    set_default E2E_SCENARIO_SET dead-job-revival,idle-scaler-restart
    set_default E2E_TRAFFIC_SHAPE rolling,storm
    set_default E2E_TRAFFIC_SCENARIO storm
    set_default E2E_SOAK_CYCLES 2
    ;;
  release-gate)
    job_kind="e2e"
    job_purpose="Certification-profile release gate with full gate evaluation"
    set_default E2E_PROFILE certification
    set_default E2E_AUTOMATION_JOB_NAME release-gate
    set_default E2E_AUTOMATION_SMOKE_JOB smoke
    set_default E2E_AUTOMATION_CERTIFICATION_JOB prod-profile
    set_default E2E_SCENARIO_SET mixed-traffic,dead-job-revival,idle-scaler-restart
    set_default E2E_TRAFFIC_SHAPE warmup,rolling,storm
    set_default E2E_TRAFFIC_SCENARIO storm
    ;;
  store-pressure)
    job_kind="stress"
    job_purpose="Redis and Consul pressure stress suites with persisted artifacts"
    set_default STRESS_AUTOMATION_JOB_NAME store-pressure
    set_default STRESS_WORKERS 400
    set_default STRESS_DURATION 60s
    set_default STRESS_JOB_SPEC_SIZE 65536
    set_default STRESS_SERVICE_KEYS 4000
    set_default STRESS_JOB_KEYS 2000
    set_default STRESS_SAMPLE_INTERVAL 250ms
    set_default STRESS_GO_TEST_TIMEOUT 20m
    ;;
  *)
    print_usage
    exit 64
    ;;
esac

case "$job_kind" in
  e2e)
    # shellcheck disable=SC1091
    . "$ROOT_DIR/e2e/scripts/load-profile.sh"
    load_e2e_profile "${E2E_PROFILE:-certification}"

    job_slug="$(slugify "${E2E_AUTOMATION_JOB_NAME:-$job_name}")"
    if [ -z "${E2E_RUN_ID:-}" ]; then
      E2E_RUN_ID="${job_slug}-$(timestamp_run_id)-$$"
      export E2E_RUN_ID
    fi
    if [ -z "${E2E_ARTIFACTS_DIR:-}" ]; then
      E2E_ARTIFACTS_DIR=".e2e-artifacts/$job_slug/$E2E_RUN_ID"
      export E2E_ARTIFACTS_DIR
    fi

    runner="$ROOT_DIR/run-docker-e2e.sh"
    printf 'Automation job preset:\n'
    printf '  job=%s\n' "${E2E_AUTOMATION_JOB_NAME:-$job_slug}"
    printf '  purpose=%s\n' "$job_purpose"
    printf '  runner=%s\n' "$(basename "$runner")"
    printf '  E2E_PROFILE=%s\n' "${E2E_PROFILE:-unset}"
    printf '  E2E_PROFILE_DESCRIPTION=%s\n' "${E2E_PROFILE_DESCRIPTION:-unset}"
    printf '  E2E_SCENARIO_SET=%s\n' "${E2E_SCENARIO_SET:-unset}"
    printf '  E2E_TARGET_IDLE_SCALER_ISOLATION_MODE=%s\n' "${E2E_TARGET_IDLE_SCALER_ISOLATION_MODE:-unset}"
    printf '  E2E_TRAFFIC_SHAPE=%s\n' "${E2E_TRAFFIC_SHAPE:-unset}"
    printf '  E2E_TRAFFIC_SCENARIO=%s\n' "${E2E_TRAFFIC_SCENARIO:-unset}"
    printf '  E2E_SOAK_CYCLES=%s\n' "${E2E_SOAK_CYCLES:-unset}"
    printf '  E2E_RUN_ID=%s\n' "$E2E_RUN_ID"
    printf '  E2E_ARTIFACTS_DIR=%s\n' "$E2E_ARTIFACTS_DIR"
    ;;
  stress)
    job_slug="$(slugify "${STRESS_AUTOMATION_JOB_NAME:-$job_name}")"
    if [ -z "${STRESS_RUN_ID:-}" ]; then
      STRESS_RUN_ID="${job_slug}-$(timestamp_run_id)-$$"
      export STRESS_RUN_ID
    fi
    if [ -z "${STRESS_HOST_ARTIFACTS_DIR:-}" ]; then
      STRESS_HOST_ARTIFACTS_DIR=".e2e-artifacts/$job_slug/$STRESS_RUN_ID"
      export STRESS_HOST_ARTIFACTS_DIR
    fi
    if [ -z "${STRESS_CONTAINER_ARTIFACTS_DIR:-}" ]; then
      STRESS_CONTAINER_ARTIFACTS_DIR="/artifacts"
      export STRESS_CONTAINER_ARTIFACTS_DIR
    fi

    runner="$ROOT_DIR/run-docker-stress.sh"
    printf 'Automation job preset:\n'
    printf '  job=%s\n' "${STRESS_AUTOMATION_JOB_NAME:-$job_slug}"
    printf '  purpose=%s\n' "$job_purpose"
    printf '  runner=%s\n' "$(basename "$runner")"
    printf '  STRESS_WORKERS=%s\n' "${STRESS_WORKERS:-unset}"
    printf '  STRESS_DURATION=%s\n' "${STRESS_DURATION:-unset}"
    printf '  STRESS_JOB_SPEC_SIZE=%s\n' "${STRESS_JOB_SPEC_SIZE:-unset}"
    printf '  STRESS_SERVICE_KEYS=%s\n' "${STRESS_SERVICE_KEYS:-unset}"
    printf '  STRESS_JOB_KEYS=%s\n' "${STRESS_JOB_KEYS:-unset}"
    printf '  STRESS_RUN_ID=%s\n' "$STRESS_RUN_ID"
    printf '  STRESS_HOST_ARTIFACTS_DIR=%s\n' "$STRESS_HOST_ARTIFACTS_DIR"
    ;;
esac

if [ "${AUTOMATION_PRESET_ONLY:-0}" = "1" ]; then
  exit 0
fi

exec "$runner"
