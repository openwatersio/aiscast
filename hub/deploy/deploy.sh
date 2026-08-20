#!/bin/sh
# Build for the box and install. Usage: deploy/deploy.sh root@ais.example.org
# First run on the box: useradd -r -s /usr/sbin/nologin hub; mkdir -p /opt/hub /var/lib/hub/archive; chown -R hub:hub /var/lib/hub;
# write /etc/hub.env; cp hub.service to /etc/systemd/system; systemctl enable hub.
set -eu
host=${1:?host}
cd "$(dirname "$0")/.."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/hub-linux .
scp /tmp/hub-linux "$host:/opt/hub/hub.new"
ssh "$host" 'mv /opt/hub/hub.new /opt/hub/hub && systemctl restart hub && sleep 2 && systemctl is-active hub && curl -fsS localhost:8080/health'
