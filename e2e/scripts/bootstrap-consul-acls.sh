#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

ROOT_DIR="/app"
BOOTSTRAP_DIR="${E2E_BOOTSTRAP_DIR:-/bootstrap}"
CONSUL_ADDR="${CONSUL_ADDR:-http://consul:8500}"

CONSUL_POLICY_FILE="$ROOT_DIR/local-test/nomad/scale-to-zero-consul-policy.hcl"
CONSUL_CATALOG_POLICY_FILE="$ROOT_DIR/local-test/nomad/consul-catalog-read-policy.hcl"
NOMAD_AGENT_CONSUL_POLICY_FILE="$ROOT_DIR/local-test/nomad/nomad-agent-consul-policy.hcl"
NOMAD_SERVER_TEMPLATE_FILE="$ROOT_DIR/e2e/nomad/nomad.d/server.hcl"
NOMAD_CLIENT_TEMPLATE_FILE="$ROOT_DIR/e2e/nomad/nomad.d/client.hcl"
TRAEFIK_TEMPLATE_FILE="$ROOT_DIR/e2e/traefik/traefik.yml"

mkdir -p "$BOOTSTRAP_DIR"

until curl -fsS "$CONSUL_ADDR/v1/status/leader" >/dev/null 2>&1; do
  sleep 1
done

CONSUL_MGMT_TOKEN="$(consul acl bootstrap -http-addr="$CONSUL_ADDR" -format=json | jq -r '.SecretID')"
export CONSUL_HTTP_TOKEN="$CONSUL_MGMT_TOKEN"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "scale-to-zero" -rules @"$CONSUL_POLICY_FILE" >/dev/null
CONSUL_S2Z_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "scale-to-zero" -policy-name "scale-to-zero" -format=json | jq -r '.SecretID')"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "catalog-read" -rules @"$CONSUL_CATALOG_POLICY_FILE" >/dev/null
CONSUL_CATALOG_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "catalog-read" -policy-name "catalog-read" -format=json | jq -r '.SecretID')"

consul acl policy create -http-addr="$CONSUL_ADDR" -name "nomad-agent" -rules @"$NOMAD_AGENT_CONSUL_POLICY_FILE" >/dev/null
CONSUL_NOMAD_AGENT_TOKEN="$(consul acl token create -http-addr="$CONSUL_ADDR" -description "nomad-agent" -policy-name "nomad-agent" -format=json | jq -r '.SecretID')"

consul acl set-agent-token -http-addr="$CONSUL_ADDR" default "$CONSUL_NOMAD_AGENT_TOKEN" >/dev/null

export CONSUL_NOMAD_AGENT_TOKEN CONSUL_CATALOG_TOKEN
envsubst '${CONSUL_NOMAD_AGENT_TOKEN}' < "$NOMAD_SERVER_TEMPLATE_FILE" > "$BOOTSTRAP_DIR/server.hcl"
envsubst '${CONSUL_NOMAD_AGENT_TOKEN}' < "$NOMAD_CLIENT_TEMPLATE_FILE" > "$BOOTSTRAP_DIR/client.hcl"
envsubst '${CONSUL_CATALOG_TOKEN}' < "$TRAEFIK_TEMPLATE_FILE" > "$BOOTSTRAP_DIR/traefik.yml"

cat > "$BOOTSTRAP_DIR/consul.env" <<EOF
CONSUL_MGMT_TOKEN=$CONSUL_MGMT_TOKEN
CONSUL_S2Z_TOKEN=$CONSUL_S2Z_TOKEN
CONSUL_CATALOG_TOKEN=$CONSUL_CATALOG_TOKEN
CONSUL_NOMAD_AGENT_TOKEN=$CONSUL_NOMAD_AGENT_TOKEN
EOF

echo "Bootstrapped Consul ACLs and rendered shared configs into $BOOTSTRAP_DIR"