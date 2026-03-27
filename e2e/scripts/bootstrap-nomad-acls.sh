#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

ROOT_DIR="/app"
BOOTSTRAP_DIR="${E2E_BOOTSTRAP_DIR:-/bootstrap}"
NOMAD_ADDR="${NOMAD_ADDR:-http://nomad-server:4646}"
NOMAD_EXPECT_SERVERS="${E2E_TARGET_NOMAD_SERVERS:-1}"
POLICY_FILE="$ROOT_DIR/local-test/nomad/scale-to-zero-policy.hcl"

wait_for_nomad_cluster() {
  expected_servers="$1"

  while true; do
    leader="$(curl -fsS "$NOMAD_ADDR/v1/status/leader" | jq -r '.')"
    if [ -n "$leader" ]; then
      break
    fi
    sleep 1
  done

  while true; do
    peer_count="$(curl -fsS "$NOMAD_ADDR/v1/status/peers" | jq 'length')"
    if [ "$peer_count" -ge "$expected_servers" ]; then
      break
    fi
    sleep 1
  done
}

mkdir -p "$BOOTSTRAP_DIR"

wait_for_nomad_cluster "$NOMAD_EXPECT_SERVERS"

NOMAD_MGMT_TOKEN="$(nomad acl bootstrap -address="$NOMAD_ADDR" -json | jq -r '.SecretID')"
export NOMAD_ADDR
export NOMAD_TOKEN="$NOMAD_MGMT_TOKEN"

nomad acl policy apply -description "scale-to-zero" scale-to-zero "$POLICY_FILE" >/dev/null
NOMAD_S2Z_TOKEN="$(nomad acl token create -name "scale-to-zero" -policy scale-to-zero -json | jq -r '.SecretID')"

cat > "$BOOTSTRAP_DIR/nomad.env" <<EOF
NOMAD_MGMT_TOKEN=$NOMAD_MGMT_TOKEN
NOMAD_S2Z_TOKEN=$NOMAD_S2Z_TOKEN
EOF

echo "Bootstrapped Nomad ACLs for $NOMAD_EXPECT_SERVERS server(s) into $BOOTSTRAP_DIR"
