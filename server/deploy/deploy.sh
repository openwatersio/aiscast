#!/bin/sh
# One-step deploy: ship the binary and the config bundle, converge the box, restart once.
# Usage: server/deploy/deploy.sh root@ais.example.org [linux-amd64-binary]
# Without a binary argument it cross-compiles first. Works the same on a fresh Ubuntu box
# and the live one; CI runs it on every push to main with the tested build artifact.
set -eu
host=${1:?usage: deploy.sh root@host [linux-amd64-binary]}
bin=${2:-}
if [ -n "$bin" ]; then
	bin=$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")
fi
cd "$(dirname "$0")"
if [ -z "$bin" ]; then
	(cd .. && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/aiscast-linux .)
	bin=/tmp/aiscast-linux
fi
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cp "$bin" "$stage/aiscast-linux"
tar czf - apply.sh aiscast.env.example rootfs -C "$stage" aiscast-linux |
	ssh "$host" 'rm -rf aiscast-deploy && mkdir aiscast-deploy && tar xzf - -C aiscast-deploy && sh aiscast-deploy/apply.sh'
