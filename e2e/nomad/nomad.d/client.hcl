# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

name      = "${NOMAD_NODE_NAME}"
data_dir  = "/tmp/nomad"
bind_addr = "0.0.0.0"

server {
  enabled = false
}

server_join {
  retry_join     = [${NOMAD_SERVER_JOIN}]
  retry_max      = 0
  retry_interval = "5s"
}

acl {
  enabled = true
}

client {
  enabled           = true
  network_interface = "eth0"
  cpu_total_compute = 4000
  memory_total_mb   = 4096
  disk_total_mb     = 524288
  disk_free_mb      = 262144
  meta = {
    "idle_scaler_singleton" = "${NOMAD_IDLE_SCALER_SINGLETON}"
  }
  options = {
    "driver.allowlist"          = "raw_exec"
    "driver.raw_exec.enable"    = "1"
    "driver.raw_exec.no_cgroups" = "1"
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
