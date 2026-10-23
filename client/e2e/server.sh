#!/bin/sh
# The aiscast server the browser tests render against, on the ADDR and UDP_ADDR playwright.config.ts
# picks (e2e/ports.ts), fed by Digitraffic and by a volunteer receiver the tests play over UDP
# (e2e/data.ts). Its state lives in e2e/.run, so a local rerun restores the vessels the last one
# heard; CI starts empty. AISCAST_BIN is a prebuilt server, as CI's test job uploads; without it
# this builds one.
set -e
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$here/.run"
# One run per worktree: runs here share client/build/, .dev.vars.e2e, and .run. The lock holds this
# shell's pid, which exec hands to the server, so it lasts the run; a crashed run's pid is gone.
# Playwright starts this before the app's build, so a second run stops before touching either.
lock="$here/.run/lock"
if ! (set -C; echo $$ > "$lock") 2>/dev/null; then
  if kill -0 "$(cat "$lock")" 2>/dev/null; then
    echo "another e2e run (pid $(cat "$lock")) is using this worktree; wait for it or stop it, or delete $lock if that pid is not one" >&2
    exit 1
  fi
  echo $$ > "$lock"
fi
bin=${AISCAST_BIN:-}
# A path is relative to where this was started, not to .run, where it runs from.
case "$bin" in
  */*) bin=$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin") ;;
esac
if [ -z "$bin" ]; then
  (cd "$here/../../server" && go build -o "$here/.run/aiscast" .)
  bin="$here/.run/aiscast"
fi
cd "$here/.run"
# Kystverket allows one connection per address, which a developer's own server may hold.
# ISSUER_PUBKEYS comes from playwright.config.ts, which mints the tests' token. A fixed
# STATION_SALT keeps the volunteer one station across reruns rather than a new one beside the last.
ADDR=${ADDR:?set by playwright.config.ts} UDP_ADDR=${UDP_ADDR:?set by playwright.config.ts} STATION_SALT=e2e PPROF_ADDR=off KYSTVERKET=0 WS_CONNECTS_PER_MIN=10000 exec "$bin"
