// Copyright 2026 Metatable Inc.
// SPDX-License-Identifier: Apache-2.0

datacenter     = "dc1"
data_dir       = "/consul/data"
bind_addr      = "{{ GetInterfaceIP \"eth0\" }}"
advertise_addr = "{{ GetInterfaceIP \"eth0\" }}"
client_addr    = "0.0.0.0"
node_name      = "${CONSUL_NODE_NAME}"
server         = true

bootstrap_expect = ${E2E_TARGET_CONSUL_SERVERS}
retry_join       = [${CONSUL_RETRY_JOIN}]

acl {
  enabled                  = true
  default_policy           = "deny"
  enable_token_persistence = true
}

performance {
  leave_drain_time = "5s"
}
