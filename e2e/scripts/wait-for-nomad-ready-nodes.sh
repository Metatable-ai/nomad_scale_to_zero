#!/bin/sh
# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

set -eu

expected_ready_nodes="${1:?expected ready node count is required}"
timeout_seconds="${2:-120}"
nomad_addr="${E2E_NOMAD_ADDR:-http://nomad-server:4646}"
nomad_token="${NOMAD_MGMT_TOKEN:-${NOMAD_TOKEN:-}}"

start="$(date +%s)"

while true; do
  ready_nodes="$(curl -fsS -H "X-Nomad-Token: $nomad_token" "$nomad_addr/v1/nodes" | jq '[.[] | select(.Status == "ready")] | length')" || ready_nodes=0
  if [ "$ready_nodes" -ge "$expected_ready_nodes" ]; then
    exit 0
  fi

  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_seconds" ]; then
    echo "Timed out waiting for $expected_ready_nodes ready Nomad node(s) at $nomad_addr" >&2
    exit 1
  fi

  sleep 2
done
