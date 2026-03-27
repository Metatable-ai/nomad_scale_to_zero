#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

ROOT_DIR="/app"
BOOTSTRAP_DIR="${E2E_BOOTSTRAP_DIR:-/bootstrap}"
CONSUL_ADDR="${CONSUL_ADDR:-http://consul:8500}"
CONSUL_EXPECT_SERVERS="${E2E_TARGET_CONSUL_SERVERS:-1}"

CONSUL_POLICY_FILE="$ROOT_DIR/local-test/nomad/scale-to-zero-consul-policy.hcl"
CONSUL_CATALOG_POLICY_FILE="$ROOT_DIR/local-test/nomad/consul-catalog-read-policy.hcl"
NOMAD_AGENT_CONSUL_POLICY_FILE="$ROOT_DIR/local-test/nomad/nomad-agent-consul-policy.hcl"
TRAEFIK_TEMPLATE_FILE="$ROOT_DIR/e2e/traefik/traefik.yml"

consul_server_name() {
  index="$1"
  if [ "$index" -eq 1 ]; then
    echo "consul"
    return 0
  fi
  echo "consul-server-$index"
}

wait_for_consul_cluster() {
  expected_servers="$1"

  while true; do
    leader="$(curl -fsS "$CONSUL_ADDR/v1/status/leader" | jq -r '.')"
    if [ -n "$leader" ]; then
      break
    fi
    sleep 1
  done

  while true; do
    server_count="$(curl -fsS "$CONSUL_ADDR/v1/status/peers" | jq 'length')" || server_count=0
    if [ "$server_count" -ge "$expected_servers" ]; then
      break
    fi
    sleep 1
  done
}

mkdir -p "$BOOTSTRAP_DIR"

wait_for_consul_cluster "$CONSUL_EXPECT_SERVERS"

CONSUL_MGMT_TOKEN="$(consul acl bootstrap -http-addr="$CONSUL_ADDR" -format=json | jq -r '.SecretID')"
export CONSUL_HTTP_TOKEN="$CONSUL_MGMT_TOKEN"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "scale-to-zero" -rules @"$CONSUL_POLICY_FILE" >/dev/null
CONSUL_S2Z_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "scale-to-zero" -policy-name "scale-to-zero" -format=json | jq -r '.SecretID')"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "catalog-read" -rules @"$CONSUL_CATALOG_POLICY_FILE" >/dev/null
CONSUL_CATALOG_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "catalog-read" -policy-name "catalog-read" -format=json | jq -r '.SecretID')"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "nomad-agent" -rules @"$NOMAD_AGENT_CONSUL_POLICY_FILE" >/dev/null
CONSUL_NOMAD_AGENT_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "nomad-agent" -policy-name "nomad-agent" -format=json | jq -r '.SecretID')"

server_index=1
while [ "$server_index" -le "$CONSUL_EXPECT_SERVERS" ]; do
  consul acl set-agent-token -http-addr="http://$(consul_server_name "$server_index"):8500" default "$CONSUL_NOMAD_AGENT_TOKEN" >/dev/null
  server_index=$((server_index + 1))
done

export CONSUL_CATALOG_TOKEN
envsubst '${CONSUL_CATALOG_TOKEN}' < "$TRAEFIK_TEMPLATE_FILE" > "$BOOTSTRAP_DIR/traefik.yml"

cat > "$BOOTSTRAP_DIR/consul.env" <<EOF
CONSUL_MGMT_TOKEN=$CONSUL_MGMT_TOKEN
CONSUL_S2Z_TOKEN=$CONSUL_S2Z_TOKEN
CONSUL_CATALOG_TOKEN=$CONSUL_CATALOG_TOKEN
CONSUL_NOMAD_AGENT_TOKEN=$CONSUL_NOMAD_AGENT_TOKEN
EOF

echo "Bootstrapped Consul ACLs for $CONSUL_EXPECT_SERVERS server(s) into $BOOTSTRAP_DIR"
