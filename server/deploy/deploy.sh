#!/bin/sh
# Build for the box and install. Usage: deploy/deploy.sh root@ais.example.org
# First run on the box: useradd -r -s /usr/sbin/nologin aiscast; mkdir -p /opt/aiscast /var/lib/aiscast/archive; chown -R aiscast:aiscast /var/lib/aiscast;
# write /etc/aiscast.env; cp aiscast.service to /etc/systemd/system; systemctl enable aiscast.
set -eu
host=${1:?host}
cd "$(dirname "$0")/.."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/aiscast-linux .
scp /tmp/aiscast-linux "$host:/opt/aiscast/aiscast.new"
ssh "$host" 'mv /opt/aiscast/aiscast.new /opt/aiscast/aiscast && systemctl restart aiscast && sleep 2 && systemctl is-active aiscast && curl -fsS localhost:8080/health'
