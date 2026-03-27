#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)"
generated_dir="${E2E_GENERATED_DIR:-$ROOT_DIR/.e2e-generated}"
output_file="${generated_dir}/idle-scaler.nomad"
render_vars='${E2E_STORE_TYPE} ${E2E_IDLE_CHECK_INTERVAL} ${E2E_IDLE_TIMEOUT} ${E2E_MIN_SCALE_DOWN_AGE} ${E2E_RENDER_NOMAD_TOKEN} ${E2E_RENDER_CONSUL_TOKEN}'

export E2E_STORE_TYPE="${E2E_STORE_TYPE:-redis}"
export E2E_IDLE_CHECK_INTERVAL="${E2E_IDLE_CHECK_INTERVAL:-3s}"
export E2E_IDLE_TIMEOUT="${E2E_IDLE_TIMEOUT:-10s}"
export E2E_MIN_SCALE_DOWN_AGE="${E2E_MIN_SCALE_DOWN_AGE:-1m}"
export E2E_RENDER_NOMAD_TOKEN="${NOMAD_TOKEN:-}"
export E2E_RENDER_CONSUL_TOKEN="${CONSUL_TOKEN:-}"

mkdir -p "$generated_dir"
envsubst "$render_vars" < "$ROOT_DIR"/e2e/nomad/jobs/idle-scaler.nomad.tpl > "$output_file"

echo "Rendered idle-scaler job into ${output_file}"
