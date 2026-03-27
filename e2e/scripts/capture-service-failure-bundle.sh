#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

service_name="$1"
job_name="${2:-$service_name}"
failure_reason="${3:-service-failure}"

artifacts_root="${E2E_FAILURE_ARTIFACTS_DIR:-}"
if [ -z "$artifacts_root" ]; then
	artifacts_dir="${E2E_ARTIFACTS_DIR:-}"
	if [ -n "$artifacts_dir" ]; then
		artifacts_root="$artifacts_dir/failures"
	fi
fi

[ -n "$artifacts_root" ] || exit 0

nomad_addr="${E2E_NOMAD_ADDR:-http://nomad-server:4646}"
consul_addr="${E2E_CONSUL_ADDR:-http://consul:8500}"
manifest_file="${E2E_WORKLOAD_MANIFEST_FILE:-}"
failure_message="${E2E_FAILURE_MESSAGE:-}"
failure_context_json="${E2E_FAILURE_CONTEXT_JSON:-{}}"
metadata_response="${E2E_FAILURE_METADATA_RESPONSE:-}"
request_output="${E2E_FAILURE_REQUEST_OUTPUT:-}"
provided_agent_checks_json="${E2E_FAILURE_CONSUL_AGENT_CHECKS_JSON:-}"
provided_nomad_allocations_json="${E2E_FAILURE_NOMAD_ALLOCATIONS_JSON:-}"
nomad_log_bytes="${E2E_FAILURE_NOMAD_LOG_BYTES:-16384}"

timestamp_utc() {
	date -u '+%Y-%m-%dT%H:%M:%SZ'
}

slugify() {
	value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9._-' '-')"
	value="${value#-}"
	value="${value%-}"
	if [ -z "$value" ]; then
		value="artifact"
	fi
	printf '%s' "$value"
}

nomad_get() {
	path="$1"
	if [ -n "${NOMAD_TOKEN:-}" ]; then
		curl -fsS -H "X-Nomad-Token: $NOMAD_TOKEN" "$nomad_addr$path"
	else
		curl -fsS "$nomad_addr$path"
	fi
}

consul_get() {
	path="$1"
	if [ -n "${CONSUL_HTTP_TOKEN:-}" ]; then
		curl -fsS -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" "$consul_addr$path"
	else
		curl -fsS "$consul_addr$path"
	fi
}

capture_remote_output() {
	getter="$1"
	path="$2"
	output_file="$3"
	error_file="$4"

	if output="$($getter "$path" 2>&1)"; then
		printf '%s' "$output" > "$output_file"
	else
		printf '%s\n' "$output" > "$error_file"
	fi
}

write_optional_payload() {
	payload="$1"
	output_prefix="$2"

	[ -n "$payload" ] || return 0

	if printf '%s' "$payload" | jq -e . >/dev/null 2>&1; then
		printf '%s' "$payload" > "${output_prefix}.json"
	else
		printf '%s\n' "$payload" > "${output_prefix}.txt"
	fi
}

capture_nomad_task_log() {
	alloc_id="$1"
	task_name="$2"
	log_type="$3"
	output_file="$4"

	if log_output="$(nomad_get "/v1/client/fs/logs/$alloc_id?task=$task_name&type=$log_type&origin=end&offset=$nomad_log_bytes&plain=true" 2>&1)"; then
		printf '%s' "$log_output" > "$output_file"
	else
		printf '%s\n' "$log_output" > "${output_file}.error.txt"
	fi
}

capture_host_log_slice() {
	source_file="$1"
	output_file="$2"

	if [ -f "$source_file" ]; then
		tail -n 200 "$source_file" > "$output_file" 2>/dev/null || true
	fi
}

bundle_dir="$artifacts_root/$(slugify "$service_name")/$(slugify "$failure_reason")"
bundle_relpath="failures/$(slugify "$service_name")/$(slugify "$failure_reason")"

mkdir -p \
	"$bundle_dir" \
	"$bundle_dir/consul" \
	"$bundle_dir/logs/host" \
	"$bundle_dir/logs/nomad" \
	"$bundle_dir/nomad" \
	"$bundle_dir/nomad/allocations" \
	"$bundle_dir/request" \
	"$bundle_dir/workload"

jq -n \
	--arg service_name "$service_name" \
	--arg job_name "$job_name" \
	--arg failure_reason "$failure_reason" \
	--arg captured_at "$(timestamp_utc)" \
	--arg bundle_dir "$bundle_relpath" \
	--arg failure_message "$failure_message" \
	--arg failure_context_json "$failure_context_json" \
	'($failure_context_json | fromjson?) as $failure_context
	| {
		service_name: $service_name,
		job_name: $job_name,
		failure_reason: $failure_reason,
		captured_at: $captured_at,
		bundle_dir: $bundle_dir,
		context: ($failure_context // {})
	}
	+ (if $failure_message == "" then {} else {failure_message: $failure_message} end)
	+ (if $failure_context == null and $failure_context_json != "" and $failure_context_json != "{}" then {context_raw: $failure_context_json} else {} end)' > "$bundle_dir/metadata.json"

if [ -n "$failure_message" ]; then
	printf '%s\n' "$failure_message" > "$bundle_dir/failure-message.txt"
fi

write_optional_payload "$metadata_response" "$bundle_dir/request/metadata-response"
write_optional_payload "$request_output" "$bundle_dir/request/request-output"

if [ -n "$provided_agent_checks_json" ]; then
	printf '%s' "$provided_agent_checks_json" > "$bundle_dir/consul/agent-checks-timeout.json"
fi

if [ -n "$provided_nomad_allocations_json" ]; then
	printf '%s' "$provided_nomad_allocations_json" > "$bundle_dir/nomad/allocations-timeout.json"
fi

if [ -n "$manifest_file" ] && [ -f "$manifest_file" ]; then
	manifest_entry="$(awk -F'|' -v service_name="$service_name" -v job_name="$job_name" '$1 == job_name || $2 == service_name { print; exit }' "$manifest_file")"
	if [ -n "$manifest_entry" ]; then
		printf '%s\n' "$manifest_entry" > "$bundle_dir/workload/manifest-entry.tsv"
		IFS='|' read -r manifest_job_name manifest_service_name manifest_class manifest_ordinal manifest_job_spec_key <<EOF
$manifest_entry
EOF
		jq -n \
			--arg job_name "$manifest_job_name" \
			--arg service_name "$manifest_service_name" \
			--arg workload_class "$manifest_class" \
			--arg workload_ordinal "$manifest_ordinal" \
			--arg job_spec_key "$manifest_job_spec_key" \
			'{job_name: $job_name, service_name: $service_name, workload_class: $workload_class, workload_ordinal: ($workload_ordinal | tonumber?), job_spec_key: $job_spec_key}' > "$bundle_dir/workload/manifest-entry.json"
	fi
fi

capture_remote_output nomad_get "/v1/job/$job_name" "$bundle_dir/nomad/job.json" "$bundle_dir/nomad/job.error.txt"
capture_remote_output nomad_get "/v1/job/$job_name/summary" "$bundle_dir/nomad/job-summary.json" "$bundle_dir/nomad/job-summary.error.txt"
capture_remote_output nomad_get "/v1/job/$job_name/allocations" "$bundle_dir/nomad/allocations.json" "$bundle_dir/nomad/allocations.error.txt"

if [ ! -s "$bundle_dir/nomad/allocations.json" ] && [ -n "$provided_nomad_allocations_json" ]; then
	printf '%s' "$provided_nomad_allocations_json" > "$bundle_dir/nomad/allocations.json"
fi

if [ -s "$bundle_dir/nomad/allocations.json" ]; then
	jq -c '[.[] | {id: .ID, job_id: .JobID, desired_status: .DesiredStatus, client_status: .ClientStatus, task_group: .TaskGroup, node_id: .NodeID, node_name: (.NodeName // ""), create_time: .CreateTime, modify_time: .ModifyTime}]' \
		"$bundle_dir/nomad/allocations.json" > "$bundle_dir/nomad/allocations-summary.json" 2>/dev/null || true

	jq -r '.[].ID // empty' "$bundle_dir/nomad/allocations.json" 2>/dev/null | while IFS= read -r alloc_id; do
		[ -n "$alloc_id" ] || continue
		alloc_dir="$bundle_dir/nomad/allocations/$alloc_id"
		alloc_log_dir="$bundle_dir/logs/nomad/$alloc_id"
		mkdir -p "$alloc_dir" "$alloc_log_dir"
		capture_remote_output nomad_get "/v1/allocation/$alloc_id" "$alloc_dir/allocation.json" "$alloc_dir/allocation.error.txt"
		if [ -s "$alloc_dir/allocation.json" ]; then
			jq -r '(.TaskStates // {}) | keys[]' "$alloc_dir/allocation.json" 2>/dev/null | while IFS= read -r task_name; do
				[ -n "$task_name" ] || continue
				capture_nomad_task_log "$alloc_id" "$task_name" stdout "$alloc_log_dir/${task_name}.stdout.log"
				capture_nomad_task_log "$alloc_id" "$task_name" stderr "$alloc_log_dir/${task_name}.stderr.log"
			done || true
		fi
	done || true
fi

capture_remote_output consul_get "/v1/catalog/service/$service_name" "$bundle_dir/consul/catalog-service.json" "$bundle_dir/consul/catalog-service.error.txt"
capture_remote_output consul_get "/v1/health/service/$service_name?passing=false" "$bundle_dir/consul/health-service.json" "$bundle_dir/consul/health-service.error.txt"
capture_remote_output consul_get "/v1/health/checks/$service_name" "$bundle_dir/consul/health-checks.json" "$bundle_dir/consul/health-checks.error.txt"
capture_remote_output consul_get "/v1/agent/checks" "$bundle_dir/consul/agent-checks.raw.json" "$bundle_dir/consul/agent-checks.raw.error.txt"

if [ -s "$bundle_dir/consul/agent-checks.raw.json" ]; then
	jq -c --arg service_name "$service_name" '[to_entries[] | .value | select((.ServiceName // "") == $service_name)]' \
		"$bundle_dir/consul/agent-checks.raw.json" > "$bundle_dir/consul/agent-checks.filtered.json" 2>/dev/null || true
fi

for host_component in \
	consul \
	consul-server-2 \
	consul-server-3 \
	idle-scaler \
	nomad-client \
	nomad-client-2 \
	nomad-client-3 \
	nomad-server \
	nomad-server-2 \
	nomad-server-3 \
	traefik \
	traefik-2
do
	host_log_file="${E2E_ARTIFACTS_DIR:-}/host/logs/${host_component}.log"
	capture_host_log_slice "$host_log_file" "$bundle_dir/logs/host/${host_component}.tail.log"
done

echo "Captured service failure bundle: $bundle_relpath" >&2
