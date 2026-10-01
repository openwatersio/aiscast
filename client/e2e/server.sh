#!/bin/sh
# The aiscast server the browser tests render against, on the address wrangler.jsonc's e2e
# environment names, fed by Digitraffic alone. Its state lives in e2e/.run, so a local rerun
# restores the vessels the last one heard; CI starts empty. AISCAST_BIN is a prebuilt server,
# as CI's test job uploads; without it this builds one.
set -e
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$here/.run"
bin=${AISCAST_BIN:-}
if [ -z "$bin" ]; then
  (cd "$here/../../server" && go build -o "$here/.run/aiscast" .)
  bin="$here/.run/aiscast"
fi
cd "$here/.run"
# Kystverket allows one connection per address, which a developer's own server may hold.
# ISSUER_PUBKEYS comes from playwright.config.ts, which mints the tests' token.
ADDR=127.0.0.1:8787 UDP_ADDR=127.0.0.1:0 PPROF_ADDR=off KYSTVERKET=0 WS_CONNECTS_PER_MIN=10000 exec "$bin"
