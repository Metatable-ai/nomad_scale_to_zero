#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

ROOT_DIR="/app"
CONFIG_TEMPLATE_FILE="$ROOT_DIR/e2e/consul/consul.hcl"
CONFIG_DIR="/consul/config-runtime"
CONFIG_FILE="$CONFIG_DIR/consul.hcl"
CONSUL_NODE_NAME="${CONSUL_NODE_NAME:-$HOSTNAME}"
CONSUL_EXPECT_SERVERS="${E2E_TARGET_CONSUL_SERVERS:-1}"

consul_server_name() {
  index="$1"
  if [ "$index" -eq 1 ]; then
    echo "consul"
    return 0
  fi
  echo "consul-server-$index"
}

build_retry_join() {
  expected_servers="$1"
  current_node="$2"
  index=1
  join_list=""

  while [ "$index" -le "$expected_servers" ]; do
    node_name="$(consul_server_name "$index")"
    if [ "$node_name" != "$current_node" ]; then
      if [ -n "$join_list" ]; then
        join_list="$join_list, "
      fi
      join_list="${join_list}\"${node_name}\""
    fi
    index=$((index + 1))
  done

  printf '%s' "$join_list"
}

mkdir -p "$CONFIG_DIR"

CONSUL_RETRY_JOIN="$(build_retry_join "$CONSUL_EXPECT_SERVERS" "$CONSUL_NODE_NAME")"

export CONSUL_NODE_NAME
export E2E_TARGET_CONSUL_SERVERS="$CONSUL_EXPECT_SERVERS"
export CONSUL_RETRY_JOIN

envsubst '${CONSUL_NODE_NAME} ${E2E_TARGET_CONSUL_SERVERS} ${CONSUL_RETRY_JOIN}' \
  < "$CONFIG_TEMPLATE_FILE" \
  > "$CONFIG_FILE"

exec /usr/local/bin/consul agent -config-file="$CONFIG_FILE"
