# Copyright 2026 Metatable Inc.
# SPDX-License-Identifier: Apache-2.0

job "idle-scaler-e2e" {
  datacenters = ["dc1"]
  type        = "system"

  group "main" {
    constraint {
      attribute = "${meta.idle_scaler_singleton}"
      value     = "true"
    }

    network {
      mode = "host"

      port "metrics" {
        static = 9108
      }
    }

    task "idle-scaler" {
      driver = "raw_exec"

      config {
        command = "/app/bin/idle-scaler"
      }

      env {
        NOMAD_ADDR           = "http://nomad-server:4646"
        CONSUL_ADDR          = "http://consul:8500"
        NOMAD_TOKEN          = "${E2E_RENDER_NOMAD_TOKEN}"
        CONSUL_TOKEN         = "${E2E_RENDER_CONSUL_TOKEN}"
        REDIS_ADDR           = "redis:6379"
        REDIS_PASSWORD       = ""
        STORE_TYPE           = "${E2E_STORE_TYPE}"
        IDLE_CHECK_INTERVAL  = "${E2E_IDLE_CHECK_INTERVAL}"
        DEFAULT_IDLE_TIMEOUT = "${E2E_IDLE_TIMEOUT}"
        MIN_SCALE_DOWN_AGE   = "${E2E_MIN_SCALE_DOWN_AGE}"
        PURGE_ON_SCALEDOWN   = "false"
        METRICS_ADDR         = ":${NOMAD_PORT_metrics}"
        LOG_LEVEL            = "info"
      }

      resources {
        cpu    = 100
        memory = 128
      }

      service {
        name         = "idle-scaler"
        provider     = "consul"
        port         = "metrics"
        address_mode = "host"

        check {
          type     = "http"
          path     = "/healthz"
          interval = "5s"
          timeout  = "3s"
        }
      }
    }
  }
}
