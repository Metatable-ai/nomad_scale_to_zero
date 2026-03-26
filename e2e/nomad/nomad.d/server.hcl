# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

data_dir  = "/tmp/nomad"
bind_addr = "0.0.0.0"

advertise {
  http = "nomad-server"
  rpc  = "nomad-server"
  serf = "nomad-server"
}

server {
  enabled          = true
  bootstrap_expect = 1
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
