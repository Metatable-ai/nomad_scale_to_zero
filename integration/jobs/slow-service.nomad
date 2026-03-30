job "slow-service" {
  datacenters = ["dc1"]
  type        = "service"

  group "main" {
    count = 1

    network {
      mode = "host"
      port "http" {}
    }

    task "setup" {
      driver = "raw_exec"
      lifecycle {
        hook    = "prestart"
        sidecar = false
      }
      config {
        command = "/bin/sh"
        args    = ["-c", <<EOT
mkdir -p /tmp/slow-service/cgi-bin
echo 'Hello from slow-service!' > /tmp/slow-service/index.html
cat > /tmp/slow-service/cgi-bin/slow << 'CGI'
#!/bin/sh
DELAY=$(echo "$QUERY_STRING" | sed -n 's/.*delay=\([0-9]*\).*/\1/p')
[ -z "$DELAY" ] && DELAY=0
[ "$DELAY" -gt 0 ] 2>/dev/null && sleep "$DELAY"
printf "Content-Type: text/plain\r\n\r\nDone after %ss delay\n" "$DELAY"
CGI
chmod +x /tmp/slow-service/cgi-bin/slow
EOT
        ]
      }
      resources {
        cpu    = 1
        memory = 10
      }
    }

    task "server" {
      driver = "raw_exec"

      config {
        command = "/bin/busybox"
        args    = ["httpd", "-f", "-p", "${NOMAD_PORT_http}", "-h", "/tmp/slow-service"]
      }

      resources {
        cpu    = 10
        memory = 32
      }

      service {
        name         = "slow-service"
        provider     = "consul"
        port         = "http"
        address_mode = "host"

        tags = [
          "traefik.enable=true",
          "traefik.http.routers.slow-service.rule=Host(`slow-service.localhost`)",
          "traefik.http.routers.slow-service.entryPoints=http",
          "traefik.http.routers.slow-service.service=s2z-nscale@file",
        ]

        check {
          type     = "http"
          path     = "/"
          interval = "2s"
          timeout  = "1s"
        }
      }
    }
  }
}
