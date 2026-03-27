#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

BOOTSTRAP_DIR="${E2E_BOOTSTRAP_DIR:-/bootstrap}"
CONFIG_TEMPLATE_FILE="/app/e2e/nomad/nomad.d/client.hcl"
CONFIG_FILE="/tmp/nomad-client.hcl"
NOMAD_NODE_NAME="${NOMAD_NODE_NAME:-$HOSTNAME}"
NOMAD_EXPECT_SERVERS="${E2E_TARGET_NOMAD_SERVERS:-1}"
IDLE_SCALER_NODE_NAME="${E2E_IDLE_SCALER_NODE_NAME:-nomad-client}"

. /app/e2e/scripts/bootstrap-env.sh
load_bootstrap_env "$BOOTSTRAP_DIR/consul.env"

nomad_server_name() {
  index="$1"
  if [ "$index" -eq 1 ]; then
    echo "nomad-server"
    return 0
  fi
  echo "nomad-server-$index"
}

build_retry_join() {
  expected_servers="$1"
  index=1
  join_list=""

  while [ "$index" -le "$expected_servers" ]; do
    node_name="$(nomad_server_name "$index")"
    if [ -n "$join_list" ]; then
      join_list="$join_list, "
    fi
    join_list="${join_list}\"${node_name}\""
    index=$((index + 1))
  done

  printf '%s' "$join_list"
}

NOMAD_SERVER_JOIN="$(build_retry_join "$NOMAD_EXPECT_SERVERS")"

NOMAD_IDLE_SCALER_SINGLETON="false"
if [ "$NOMAD_NODE_NAME" = "$IDLE_SCALER_NODE_NAME" ]; then
  NOMAD_IDLE_SCALER_SINGLETON="true"
fi

export CONSUL_NOMAD_AGENT_TOKEN
export NOMAD_NODE_NAME
export NOMAD_SERVER_JOIN
export NOMAD_IDLE_SCALER_SINGLETON

envsubst '${CONSUL_NOMAD_AGENT_TOKEN} ${NOMAD_NODE_NAME} ${NOMAD_SERVER_JOIN} ${NOMAD_IDLE_SCALER_SINGLETON}' \
  < "$CONFIG_TEMPLATE_FILE" \
  > "$CONFIG_FILE"

exec /usr/local/bin/nomad agent -config="$CONFIG_FILE"
