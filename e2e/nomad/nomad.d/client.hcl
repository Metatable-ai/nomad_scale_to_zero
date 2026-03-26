# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

data_dir  = "/tmp/nomad"
bind_addr = "0.0.0.0"

server {
  enabled = false
}

acl {
  enabled = true
}

client {
  enabled           = true
  servers           = ["nomad-server:4647"]
  network_interface = "eth0"
  cpu_total_compute = 4000
  memory_total_mb   = 4096
  disk_total_mb     = 524288
  disk_free_mb      = 262144
  options = {
    "driver.allowlist" = "raw_exec"
  }
}

plugin "raw_exec" {
  config {
    enabled = true
  }
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
