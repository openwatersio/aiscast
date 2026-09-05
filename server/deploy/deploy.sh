#!/bin/sh
# Build for the box and install the binary. Usage: deploy/deploy.sh root@ais.example.org
# Box setup and config changes go through provision.sh; see README.md.
set -eu
host=${1:?host}
cd "$(dirname "$0")/.."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/aiscast-linux .
scp /tmp/aiscast-linux "$host:/opt/aiscast/aiscast.new"
ssh "$host" 'chmod 755 /opt/aiscast/aiscast.new && mv /opt/aiscast/aiscast.new /opt/aiscast/aiscast && systemctl restart aiscast && sleep 2 && systemctl is-active aiscast caddy && curl -fsS localhost:8080/health'
