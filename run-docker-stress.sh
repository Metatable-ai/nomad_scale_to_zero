#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -e

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

# Export defaults so docker compose receives them even when users invoke this
# script without pre-exporting variables in their shell.
: "${STRESS_AUTOMATION_JOB_NAME:=manual}"
: "${STRESS_WORKERS:=400}"
: "${STRESS_DURATION:=60s}"
: "${STRESS_JOB_SPEC_SIZE:=65536}"
: "${STRESS_SERVICE_KEYS:=4000}"
: "${STRESS_JOB_KEYS:=2000}"
: "${STRESS_SAMPLE_INTERVAL:=250ms}"
: "${STRESS_GO_TEST_TIMEOUT:=20m}"
: "${STRESS_RUN_ID:=stress-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
: "${STRESS_HOST_ARTIFACTS_DIR:=.e2e-artifacts/store-pressure/$STRESS_RUN_ID}"
: "${STRESS_CONTAINER_ARTIFACTS_DIR:=/artifacts}"

case "$STRESS_HOST_ARTIFACTS_DIR" in
	/*)
		;;
	*)
		STRESS_HOST_ARTIFACTS_DIR="$ROOT_DIR/$STRESS_HOST_ARTIFACTS_DIR"
		;;
esac

export STRESS_AUTOMATION_JOB_NAME
export STRESS_WORKERS
export STRESS_DURATION
export STRESS_JOB_SPEC_SIZE
export STRESS_SERVICE_KEYS
export STRESS_JOB_KEYS
export STRESS_SAMPLE_INTERVAL
export STRESS_GO_TEST_TIMEOUT
export STRESS_RUN_ID
export STRESS_HOST_ARTIFACTS_DIR
export STRESS_CONTAINER_ARTIFACTS_DIR

COMPOSE_FILE="docker-compose.stress.yml"
HOST_ARTIFACTS_DIR="$STRESS_HOST_ARTIFACTS_DIR/host"
RUN_EXIT_CODE=1
host_artifacts_collected=0
stack_down=0

service_container_id() {
	docker compose -f "$COMPOSE_FILE" ps -a -q "$1" 2>/dev/null || true
}

collect_service_host_artifacts() {
	service_name="$1"
	log_file="$HOST_ARTIFACTS_DIR/logs/${service_name}.log"
	inspect_file="$HOST_ARTIFACTS_DIR/inspect/${service_name}.json"

	docker compose -f "$COMPOSE_FILE" logs --no-color "$service_name" >"$log_file" 2>&1 || true

	container_id="$(service_container_id "$service_name")"
	if [ -n "$container_id" ]; then
		docker inspect "$container_id" >"$inspect_file" 2>&1 || true
	fi
}

collect_host_artifacts() {
	if [ "$host_artifacts_collected" -eq 1 ]; then
		return 0
	fi

	mkdir -p "$HOST_ARTIFACTS_DIR" "$HOST_ARTIFACTS_DIR/logs" "$HOST_ARTIFACTS_DIR/inspect"

	{
		printf 'run_id=%s\n' "$STRESS_RUN_ID"
		printf 'automation_job_name=%s\n' "$STRESS_AUTOMATION_JOB_NAME"
		printf 'artifacts_dir=%s\n' "$STRESS_HOST_ARTIFACTS_DIR"
		printf 'exit_code=%s\n' "$RUN_EXIT_CODE"
		printf 'captured_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$HOST_ARTIFACTS_DIR/run-summary.env"

	docker compose -f "$COMPOSE_FILE" ps >"$HOST_ARTIFACTS_DIR/compose-ps.txt" 2>&1 || true
	docker compose -f "$COMPOSE_FILE" logs --no-color >"$HOST_ARTIFACTS_DIR/compose.log" 2>&1 || true

	for service in redis consul stress; do
		collect_service_host_artifacts "$service"
	done

	host_artifacts_collected=1
}

cleanup() {
	collect_host_artifacts

	if [ "$stack_down" -eq 0 ]; then
		docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
		stack_down=1
	fi
}

trap cleanup EXIT INT TERM

printf 'Running Docker stress test with:\n'
printf '  STRESS_AUTOMATION_JOB_NAME=%s\n' "$STRESS_AUTOMATION_JOB_NAME"
printf '  STRESS_WORKERS=%s\n' "$STRESS_WORKERS"
printf '  STRESS_DURATION=%s\n' "$STRESS_DURATION"
printf '  STRESS_JOB_SPEC_SIZE=%s\n' "$STRESS_JOB_SPEC_SIZE"
printf '  STRESS_SERVICE_KEYS=%s\n' "$STRESS_SERVICE_KEYS"
printf '  STRESS_JOB_KEYS=%s\n' "$STRESS_JOB_KEYS"
printf '  STRESS_SAMPLE_INTERVAL=%s\n' "$STRESS_SAMPLE_INTERVAL"
printf '  STRESS_GO_TEST_TIMEOUT=%s\n' "$STRESS_GO_TEST_TIMEOUT"
printf '  STRESS_RUN_ID=%s\n' "$STRESS_RUN_ID"
printf '  STRESS_HOST_ARTIFACTS_DIR=%s\n' "$STRESS_HOST_ARTIFACTS_DIR"

mkdir -p "$STRESS_HOST_ARTIFACTS_DIR"
printf 'Artifacts will be written to %s\n' "$STRESS_HOST_ARTIFACTS_DIR"

printf 'Cleaning previous Docker stress stack state...\n'
docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true

if docker compose -f "$COMPOSE_FILE" up --build --abort-on-container-exit --exit-code-from stress; then
	RUN_EXIT_CODE=0
else
	RUN_EXIT_CODE=$?
fi

cleanup
trap - EXIT INT TERM

exit "$RUN_EXIT_CODE"
