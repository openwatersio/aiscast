# Contributing

## Layout

- [server/](server/): the server, one Go binary: ingest → reassemble → dedupe → decode → bbox fan-out, hourly archive to R2. [server/README.md](server/README.md) documents endpoints, environment, access tokens, and sources; [server/deploy/](server/deploy/) the production box.
- [viewer/](viewer/): static MapLibre page, deployed to GitHub Pages from `main`.
- [PLAN.md](PLAN.md): scope, architecture, staged plan, licensing, sustainability. Read it before proposing a change to how data flows.
- [research/](research/): the research behind every claim in the plan.

## Running locally

```sh
cd server
ALLOW_ANON=1 go run .   # Kystverket + Digitraffic in, WebSocket on :8080, UDP NMEA on :10110, archive/ in cwd
```

`ALLOW_ANON=1` disables tokens; never set it on a public host. Kystverket allows one TCP connection per source IP, so if another server is already running on your network set `KYSTVERKET=0`. Then `cd viewer && python3 -m http.server 8089` and open http://localhost:8089/?server=localhost:8080, or point any aisstream.io client at `ws://localhost:8080/v0/stream` with any non-empty `APIKey`.

Go 1.24 (`server/mise.toml`). `go test ./...` runs unit tests, the aisstream golden envelope, and the GPSD/libais public fixtures in `server/testdata/`. `go run ./cmd/loadtest -clients 1000 -duration 30s` against a server started with `WS_CONNECTS_PER_MIN=100000` measures fan-out. `go run ./cmd/aiscast-key` mints and inspects access tokens.

## Changes

- Open a pull request against `main`. CI runs gofmt, vet, tests, and a linux build on every push and PR; a merge to `main` deploys aiscast to `ais-server-1` and the viewer to GitHub Pages.
- `/v0/stream` is frozen to aisstream.io's wire format; anything new goes under `/v1`. Additive changes to `/v1` are fine; breaking ones need a note in PLAN.md.
- Every source gets its own `source` value, license tag in the archive path, and env flag, and stays out of the health gate unless it is an open-licensed feed we commit to.
- Comments explain why, not what; durable docs describe the current state, not history.

## Production

`server/deploy/README.md` has the box, firewall, systemd unit, cloud-init template, secrets layout, and the CI deploy. Secrets live in the untracked `.env` at the repo root (issuer seed for tokens, Hetzner/R2/AISaiscast credentials) and in `/etc/server.env` on the box.
