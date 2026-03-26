#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

BOOTSTRAP_DIR="${E2E_BOOTSTRAP_DIR:-/bootstrap}"

. /app/e2e/scripts/bootstrap-env.sh
load_bootstrap_env "$BOOTSTRAP_DIR/consul.env"
load_bootstrap_env "$BOOTSTRAP_DIR/nomad.env"

export CONSUL_HTTP_TOKEN="${CONSUL_MGMT_TOKEN:-${CONSUL_HTTP_TOKEN:-}}"
export CONSUL_TOKEN="${CONSUL_S2Z_TOKEN:-${CONSUL_TOKEN:-}}"
export NOMAD_TOKEN="${NOMAD_S2Z_TOKEN:-${NOMAD_TOKEN:-}}"
export S2Z_CONSUL_TOKEN="${CONSUL_S2Z_TOKEN:-${S2Z_CONSUL_TOKEN:-}}"
export S2Z_NOMAD_TOKEN="${NOMAD_S2Z_TOKEN:-${S2Z_NOMAD_TOKEN:-}}"

exec /app/run-e2e-tests.sh