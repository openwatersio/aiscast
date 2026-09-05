#!/bin/sh
# Converge a box to match this directory: packages, users, and everything under rootfs/.
# Usage: deploy/provision.sh root@ais.example.org
# CI runs this on pushes to main that touch server/deploy (.github/workflows/provision.yml).
set -eu
host=${1:?usage: provision.sh root@host}
cd "$(dirname "$0")"
tar cz apply.sh aiscast.env.example deploy_authorized_keys rootfs |
	ssh "$host" 'rm -rf aiscast-provision && mkdir aiscast-provision && tar xz -C aiscast-provision && sh aiscast-provision/apply.sh'
