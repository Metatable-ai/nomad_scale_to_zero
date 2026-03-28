#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

manifest_file="${1:-${E2E_WORKLOAD_MANIFEST_FILE:-}}"
activator_url="${2:-${E2E_ACTIVATOR_BASE_URL:-http://activator:8090}}"

if [ -z "$manifest_file" ]; then
	echo "Usage: $0 <workload-manifest.tsv> [activator-url]" >&2
	exit 1
fi

if [ ! -s "$manifest_file" ]; then
	echo "Expected workload manifest at $manifest_file" >&2
	exit 1
fi

	jq -Rn --rawfile manifest "$manifest_file" '
		{
			workloads: (
				$manifest
				| split("\n")
				| map(select(length > 0))
				| map(split("|"))
				| map({
					job_name: .[0],
					service_name: .[1],
					host_name: (.[1] + ".localhost"),
					workload_class: .[2],
					workload_ordinal: (.[3] | tonumber),
					job_spec_key: .[4]
				})
			)
		}
	' | curl -fsS \
		-X POST \
		-H "Content-Type: application/json" \
		--data-binary @- \
		"$activator_url/admin/registry/sync"
