# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

name      = "${NOMAD_NODE_NAME}"
data_dir  = "/tmp/nomad"
bind_addr = "0.0.0.0"

advertise {
  http = "${NOMAD_NODE_NAME}"
  rpc  = "${NOMAD_NODE_NAME}"
  serf = "${NOMAD_NODE_NAME}"
}

server {
  enabled          = true
  bootstrap_expect = ${E2E_TARGET_NOMAD_SERVERS}
}

server_join {
  retry_join     = [${NOMAD_SERVER_JOIN}]
  retry_max      = 0
  retry_interval = "5s"
}

acl {
  enabled = true
}

consul {
  address              = "consul:8500"
  auto_advertise       = true
  checks_use_advertise = true
  token                = "${CONSUL_NOMAD_AGENT_TOKEN}"
}

ports {
  http = 4646
  rpc  = 4647
  serf = 4648
}
