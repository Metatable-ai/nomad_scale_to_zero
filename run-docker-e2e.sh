#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -e

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091
. "$ROOT_DIR/e2e/scripts/load-profile.sh"
load_e2e_profile "${E2E_PROFILE:-certification}"

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
: "${E2E_TARGET_NOMAD_SERVERS:=1}"
: "${E2E_TARGET_NOMAD_CLIENTS:=1}"
: "${E2E_TARGET_CONSUL_SERVERS:=1}"
: "${E2E_TARGET_TRAEFIK_REPLICAS:=1}"
: "${E2E_TARGET_REDIS_NODES:=1}"
: "${E2E_TARGET_IDLE_SCALER_PLACEMENT:=docker-compose-service}"
: "${E2E_TARGET_IDLE_SCALER_ISOLATION_MODE:=disabled}"
: "${E2E_NOMAD_CLIENTS:=$E2E_TARGET_NOMAD_CLIENTS}"
: "${E2E_TRAFFIC_SCENARIO:=storm}"
: "${E2E_WARMUP_VUS:=5}"
: "${E2E_WARMUP_DURATION:=10s}"
: "${E2E_BURST_VUS:=50}"
: "${E2E_BURST_DURATION:=20s}"
: "${E2E_STORM_VUS:=100}"
: "${E2E_STORM_DURATION:=30s}"
: "${E2E_STORM_RATE:=250}"
: "${E2E_STORM_PREALLOCATED_VUS:=150}"
: "${E2E_RUN_ID:=e2e-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
: "${E2E_ARTIFACTS_DIR:=.e2e-artifacts/$E2E_RUN_ID}"

case "$E2E_ARTIFACTS_DIR" in
	/*)
		;;
	*)
		E2E_ARTIFACTS_DIR="$ROOT_DIR/$E2E_ARTIFACTS_DIR"
		;;
esac

export E2E_PROFILE
export E2E_JOB_COUNT
export E2E_IDLE_TIMEOUT
export E2E_IDLE_CHECK_INTERVAL
export E2E_REQUEST_TIMEOUT
export E2E_SOAK_CYCLES
export E2E_SCENARIO_SET
export E2E_STARTUP_READY_TIMEOUT
export E2E_CONSUL_SERVICES_TIMEOUT
export E2E_NOMAD_WAKE_TIMEOUT
export E2E_CONSUL_CHECKS_TIMEOUT
export E2E_K6_TARGET_MODE
export E2E_JOB_CPU
export E2E_JOB_MEMORY
export E2E_JOB_CHECK_INTERVAL
export E2E_JOB_CHECK_TIMEOUT
export E2E_TARGET_NOMAD_SERVERS
export E2E_TARGET_NOMAD_CLIENTS
export E2E_TARGET_CONSUL_SERVERS
export E2E_TARGET_TRAEFIK_REPLICAS
export E2E_TARGET_REDIS_NODES
export E2E_TARGET_IDLE_SCALER_PLACEMENT
export E2E_TARGET_IDLE_SCALER_ISOLATION_MODE
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
export E2E_RUN_ID
export E2E_ARTIFACTS_DIR

COMPOSE_FILE="docker-compose.e2e.yml"
WAIT_TIMEOUT_SECONDS="${E2E_DOCKER_WAIT_TIMEOUT:-900}"
HOST_ARTIFACTS_DIR="$E2E_ARTIFACTS_DIR/host"

stack_stopped=0
topology_services=""
monitored_services=""
host_artifacts_collected=0
RUN_EXIT_CODE=1

require_range() {
	name="$1"
	value="$2"
	minimum="$3"
	maximum="$4"

	if [ "$value" -lt "$minimum" ] || [ "$value" -gt "$maximum" ]; then
		echo "$name must be between $minimum and $maximum (got $value)" >&2
		exit 1
	fi
}

append_service() {
	service_name="$1"

	if [ -z "$topology_services" ]; then
		topology_services="$service_name"
	else
		topology_services="$topology_services $service_name"
	fi
}

append_monitored_service() {
	service_name="$1"

	append_service "$service_name"

	if [ -z "$monitored_services" ]; then
		monitored_services="$service_name"
	else
		monitored_services="$monitored_services $service_name"
	fi
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
	summary_exit_code="$(resolve_runner_exit_code "$RUN_EXIT_CODE")"

	{
		printf 'run_id=%s\n' "$E2E_RUN_ID"
		printf 'artifacts_dir=%s\n' "$E2E_ARTIFACTS_DIR"
		printf 'profile=%s\n' "$E2E_PROFILE"
		printf 'exit_code=%s\n' "$summary_exit_code"
		printf 'wrapper_exit_code=%s\n' "$RUN_EXIT_CODE"
		printf 'captured_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$HOST_ARTIFACTS_DIR/run-summary.env"

	docker compose -f "$COMPOSE_FILE" ps >"$HOST_ARTIFACTS_DIR/compose-ps.txt" 2>&1 || true
	docker compose -f "$COMPOSE_FILE" logs --no-color >"$HOST_ARTIFACTS_DIR/compose.log" 2>&1 || true

	for service in $topology_services; do
		collect_service_host_artifacts "$service"
	done

	host_artifacts_collected=1
}

cleanup() {
	collect_host_artifacts

	if [ "$stack_stopped" -eq 0 ]; then
		docker compose -f "$COMPOSE_FILE" stop >/dev/null 2>&1 || true
		stack_stopped=1
	fi
}

service_container_id() {
	docker compose -f "$COMPOSE_FILE" ps -a -q "$1" 2>/dev/null || true
}

service_state() {
	docker inspect -f '{{.State.Status}}' "$1"
}

service_exit_code() {
	docker inspect -f '{{.State.ExitCode}}' "$1"
}

resolve_runner_exit_code() {
	fallback_exit_code="$1"
	runner_id="$(service_container_id e2e-runner)"
	if [ -n "$runner_id" ]; then
		runner_state="$(service_state "$runner_id" 2>/dev/null || true)"
		case "$runner_state" in
			exited|dead)
				service_exit_code "$runner_id" 2>/dev/null || printf '%s\n' "$fallback_exit_code"
				return 0
				;;
		esac
	fi

	printf '%s\n' "$fallback_exit_code"
}

trap cleanup EXIT INT TERM

require_range "E2E_TARGET_NOMAD_SERVERS" "$E2E_TARGET_NOMAD_SERVERS" 1 3
require_range "E2E_TARGET_NOMAD_CLIENTS" "$E2E_TARGET_NOMAD_CLIENTS" 1 3
require_range "E2E_TARGET_CONSUL_SERVERS" "$E2E_TARGET_CONSUL_SERVERS" 1 3
require_range "E2E_TARGET_TRAEFIK_REPLICAS" "$E2E_TARGET_TRAEFIK_REPLICAS" 1 2

if [ "$E2E_TARGET_REDIS_NODES" -ne 1 ]; then
	echo "Only E2E_TARGET_REDIS_NODES=1 is currently supported by docker-compose.e2e.yml" >&2
	exit 1
fi

append_monitored_service redis
append_monitored_service consul
if [ "$E2E_TARGET_CONSUL_SERVERS" -ge 2 ]; then
	append_monitored_service consul-server-2
fi
if [ "$E2E_TARGET_CONSUL_SERVERS" -ge 3 ]; then
	append_monitored_service consul-server-3
fi
append_monitored_service consul-acl-bootstrap
append_monitored_service nomad-server
if [ "$E2E_TARGET_NOMAD_SERVERS" -ge 2 ]; then
	append_monitored_service nomad-server-2
fi
if [ "$E2E_TARGET_NOMAD_SERVERS" -ge 3 ]; then
	append_monitored_service nomad-server-3
fi
append_monitored_service nomad-acl-bootstrap
append_monitored_service nomad-client
if [ "$E2E_TARGET_NOMAD_CLIENTS" -ge 2 ]; then
	append_monitored_service nomad-client-2
fi
if [ "$E2E_TARGET_NOMAD_CLIENTS" -ge 3 ]; then
	append_monitored_service nomad-client-3
fi
append_monitored_service traefik
if [ "$E2E_TARGET_TRAEFIK_REPLICAS" -ge 2 ]; then
	append_monitored_service traefik-2
fi
case "$E2E_TARGET_IDLE_SCALER_PLACEMENT" in
	docker-compose-service)
		append_monitored_service idle-scaler
		;;
	nomad-system-job)
		;;
	*)
		echo "Unsupported E2E_TARGET_IDLE_SCALER_PLACEMENT: $E2E_TARGET_IDLE_SCALER_PLACEMENT" >&2
		exit 1
		;;
esac
append_service e2e-runner

printf 'Running Docker e2e soak with:\n'
printf '  E2E_PROFILE=%s\n' "$E2E_PROFILE"
printf '  E2E_AUTOMATION_JOB_NAME=%s\n' "${E2E_AUTOMATION_JOB_NAME:-unset}"
printf '  E2E_WORKLOAD_MIX_LABELS=%s\n' "${E2E_WORKLOAD_MIX_LABELS:-unset}"
printf '  E2E_TRAFFIC_SHAPE=%s\n' "${E2E_TRAFFIC_SHAPE:-unset}"
printf '  E2E_JOB_COUNT=%s\n' "$E2E_JOB_COUNT"
printf '  E2E_IDLE_TIMEOUT=%s\n' "$E2E_IDLE_TIMEOUT"
printf '  E2E_IDLE_CHECK_INTERVAL=%s\n' "$E2E_IDLE_CHECK_INTERVAL"
printf '  E2E_SOAK_CYCLES=%s\n' "$E2E_SOAK_CYCLES"
printf '  E2E_SCENARIO_SET=%s\n' "${E2E_SCENARIO_SET:-unset}"
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
printf '  E2E_TARGET_NOMAD_SERVERS=%s\n' "$E2E_TARGET_NOMAD_SERVERS"
printf '  E2E_TARGET_NOMAD_CLIENTS=%s\n' "$E2E_TARGET_NOMAD_CLIENTS"
printf '  E2E_TARGET_CONSUL_SERVERS=%s\n' "$E2E_TARGET_CONSUL_SERVERS"
printf '  E2E_TARGET_TRAEFIK_REPLICAS=%s\n' "$E2E_TARGET_TRAEFIK_REPLICAS"
printf '  E2E_TARGET_REDIS_NODES=%s\n' "$E2E_TARGET_REDIS_NODES"
printf '  E2E_TARGET_IDLE_SCALER_PLACEMENT=%s\n' "$E2E_TARGET_IDLE_SCALER_PLACEMENT"
printf '  E2E_TARGET_IDLE_SCALER_ISOLATION_MODE=%s\n' "$E2E_TARGET_IDLE_SCALER_ISOLATION_MODE"
printf '  E2E_NOMAD_CLIENTS=%s\n' "$E2E_NOMAD_CLIENTS"
printf '  E2E_K6_TARGET_MODE=%s\n' "${E2E_K6_TARGET_MODE:-random}"
printf '  E2E_STORM_VUS=%s\n' "$E2E_STORM_VUS"
printf '  E2E_STORM_DURATION=%s\n' "$E2E_STORM_DURATION"
printf '  E2E_STORM_RATE=%s\n' "$E2E_STORM_RATE"
printf '  E2E_RUN_ID=%s\n' "$E2E_RUN_ID"
printf '  E2E_ARTIFACTS_DIR=%s\n' "$E2E_ARTIFACTS_DIR"

mkdir -p "$E2E_ARTIFACTS_DIR" "$HOST_ARTIFACTS_DIR"
printf 'Artifacts will be written to %s\n' "$E2E_ARTIFACTS_DIR"

printf 'Cleaning previous Docker e2e stack state...\n'
docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true

docker compose -f "$COMPOSE_FILE" up -d --build --force-recreate $topology_services

runner_id=""
started_at="$(date +%s)"
exit_code="1"

while true; do
	runner_id="$(service_container_id e2e-runner)"
	if [ -n "$runner_id" ]; then
		runner_state="$(service_state "$runner_id" 2>/dev/null || true)"
		case "$runner_state" in
			exited|dead)
				exit_code="$(service_exit_code "$runner_id")"
				break
				;;
		esac
	fi

	for service in $monitored_services; do
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

RUN_EXIT_CODE="$(resolve_runner_exit_code "$exit_code")"
cleanup
trap - EXIT INT TERM

exit "$RUN_EXIT_CODE"
