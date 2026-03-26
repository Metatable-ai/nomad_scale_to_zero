#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -e

: "${E2E_JOB_COUNT:=10}"
: "${E2E_IDLE_TIMEOUT:=10s}"
: "${E2E_IDLE_CHECK_INTERVAL:=3s}"
: "${E2E_REQUEST_TIMEOUT:=45s}"
: "${E2E_SOAK_CYCLES:=3}"
: "${E2E_STARTUP_READY_TIMEOUT:=}"
: "${E2E_CONSUL_SERVICES_TIMEOUT:=}"
: "${E2E_NOMAD_WAKE_TIMEOUT:=}"
: "${E2E_CONSUL_CHECKS_TIMEOUT:=}"
: "${E2E_JOB_CPU:=50}"
: "${E2E_JOB_MEMORY:=64}"
: "${E2E_JOB_CHECK_INTERVAL:=2s}"
: "${E2E_JOB_CHECK_TIMEOUT:=2s}"
: "${E2E_NOMAD_CLIENTS:=3}"
: "${E2E_TRAFFIC_SCENARIO:=storm}"
: "${E2E_WARMUP_VUS:=5}"
: "${E2E_WARMUP_DURATION:=10s}"
: "${E2E_BURST_VUS:=50}"
: "${E2E_BURST_DURATION:=20s}"
: "${E2E_STORM_VUS:=100}"
: "${E2E_STORM_DURATION:=30s}"
: "${E2E_STORM_RATE:=250}"
: "${E2E_STORM_PREALLOCATED_VUS:=150}"

export E2E_JOB_COUNT
export E2E_IDLE_TIMEOUT
export E2E_IDLE_CHECK_INTERVAL
export E2E_REQUEST_TIMEOUT
export E2E_SOAK_CYCLES
export E2E_STARTUP_READY_TIMEOUT
export E2E_CONSUL_SERVICES_TIMEOUT
export E2E_NOMAD_WAKE_TIMEOUT
export E2E_CONSUL_CHECKS_TIMEOUT
export E2E_JOB_CPU
export E2E_JOB_MEMORY
export E2E_JOB_CHECK_INTERVAL
export E2E_JOB_CHECK_TIMEOUT
export E2E_NOMAD_CLIENTS
export E2E_TRAFFIC_SCENARIO
export E2E_WARMUP_VUS
export E2E_WARMUP_DURATION
export E2E_BURST_VUS
export E2E_BURST_DURATION
export E2E_STORM_VUS
export E2E_STORM_DURATION
export E2E_STORM_RATE
export E2E_STORM_PREALLOCATED_VUS

COMPOSE_FILE="docker-compose.e2e.yml"
WAIT_TIMEOUT_SECONDS="${E2E_DOCKER_WAIT_TIMEOUT:-900}"

log_pid=""
stack_stopped=0

cleanup() {
	if [ -n "$log_pid" ]; then
		kill "$log_pid" >/dev/null 2>&1 || true
		wait "$log_pid" 2>/dev/null || true
		log_pid=""
	fi

	if [ "$stack_stopped" -eq 0 ]; then
		docker compose -f "$COMPOSE_FILE" stop >/dev/null 2>&1 || true
		stack_stopped=1
	fi
}

service_container_id() {
	docker compose -f "$COMPOSE_FILE" ps -q "$1" 2>/dev/null || true
}

service_state() {
	docker inspect -f '{{.State.Status}}' "$1"
}

service_exit_code() {
	docker inspect -f '{{.State.ExitCode}}' "$1"
}

trap cleanup EXIT INT TERM

printf 'Running Docker e2e soak with:\n'
printf '  E2E_JOB_COUNT=%s\n' "$E2E_JOB_COUNT"
printf '  E2E_IDLE_TIMEOUT=%s\n' "$E2E_IDLE_TIMEOUT"
printf '  E2E_IDLE_CHECK_INTERVAL=%s\n' "$E2E_IDLE_CHECK_INTERVAL"
printf '  E2E_SOAK_CYCLES=%s\n' "$E2E_SOAK_CYCLES"
if [ -n "$E2E_STARTUP_READY_TIMEOUT" ]; then
	printf '  E2E_STARTUP_READY_TIMEOUT=%s\n' "$E2E_STARTUP_READY_TIMEOUT"
fi
if [ -n "$E2E_CONSUL_SERVICES_TIMEOUT" ]; then
	printf '  E2E_CONSUL_SERVICES_TIMEOUT=%s\n' "$E2E_CONSUL_SERVICES_TIMEOUT"
fi
if [ -n "$E2E_NOMAD_WAKE_TIMEOUT" ]; then
	printf '  E2E_NOMAD_WAKE_TIMEOUT=%s\n' "$E2E_NOMAD_WAKE_TIMEOUT"
fi
if [ -n "$E2E_CONSUL_CHECKS_TIMEOUT" ]; then
	printf '  E2E_CONSUL_CHECKS_TIMEOUT=%s\n' "$E2E_CONSUL_CHECKS_TIMEOUT"
fi
printf '  E2E_TRAFFIC_SCENARIO=%s\n' "$E2E_TRAFFIC_SCENARIO"
printf '  E2E_JOB_CPU=%s\n' "$E2E_JOB_CPU"
printf '  E2E_JOB_MEMORY=%s\n' "$E2E_JOB_MEMORY"
printf '  E2E_JOB_CHECK_INTERVAL=%s\n' "$E2E_JOB_CHECK_INTERVAL"
printf '  E2E_JOB_CHECK_TIMEOUT=%s\n' "$E2E_JOB_CHECK_TIMEOUT"
printf '  E2E_NOMAD_CLIENTS=%s\n' "$E2E_NOMAD_CLIENTS"
printf '  E2E_STORM_VUS=%s\n' "$E2E_STORM_VUS"
printf '  E2E_STORM_DURATION=%s\n' "$E2E_STORM_DURATION"
printf '  E2E_STORM_RATE=%s\n' "$E2E_STORM_RATE"

printf 'Cleaning previous Docker e2e stack state...\n'
docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true

docker compose -f "$COMPOSE_FILE" up -d --build --force-recreate
docker compose -f "$COMPOSE_FILE" logs -f --no-color &
log_pid=$!

runner_id=""
started_at="$(date +%s)"
exit_code="1"

while true; do
	if [ -z "$runner_id" ]; then
		runner_id="$(service_container_id e2e-runner)"
	fi

	if [ -n "$runner_id" ]; then
		runner_state="$(service_state "$runner_id")"
		case "$runner_state" in
			exited|dead)
				exit_code="$(service_exit_code "$runner_id")"
				break
				;;
		esac
	fi

	for service in consul consul-acl-bootstrap redis nomad-server nomad-acl-bootstrap traefik idle-scaler; do
		container_id="$(service_container_id "$service")"
		if [ -z "$container_id" ]; then
			continue
		fi

		container_state="$(service_state "$container_id")"
		case "$container_state" in
			exited|dead)
				container_exit_code="$(service_exit_code "$container_id")"
				if [ "$container_exit_code" -ne 0 ]; then
					echo "Service $service exited before e2e-runner completed (status=$container_exit_code)" >&2
					exit_code="$container_exit_code"
					break 2
				fi
				;;
		esac
	done

	now="$(date +%s)"
	if [ $((now - started_at)) -ge "$WAIT_TIMEOUT_SECONDS" ]; then
		echo "Timed out waiting for e2e-runner to finish after ${WAIT_TIMEOUT_SECONDS}s" >&2
		exit_code=1
		break
	fi

	sleep 2
done

cleanup
trap - EXIT INT TERM

exit "$exit_code"