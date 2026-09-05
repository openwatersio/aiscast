# Contributing

## Layout

- [server/](server/): the server, one Go binary: ingest → reassemble → dedupe → decode → bbox fan-out, hourly archive to R2. [server/README.md](server/README.md) documents endpoints, environment, access tokens, and sources; [server/deploy/](server/deploy/) the production box.
- [viewer/](viewer/): static MapLibre page, deployed to GitHub Pages from `main`.
- [signalk-plugin/](signalk-plugin/): `signalk-aiscast`, the Signal K plugin (TypeScript, vitest). `npm install && npm test` runs it against a fake aiscast; `npm run build` emits `dist/`. Published to npm by `release.yml` on a `signalk-plugin-v*` release tag.
- [docs/](docs/): [architecture.md](docs/architecture.md) is how data flows and why; read it before proposing a change to that. [policy.md](docs/policy.md) covers per-source licensing, privacy, and funding; [limits.md](docs/limits.md) the access tiers.
- [research/](research/): the research behind every claim in the docs.

## Running locally

```sh
cd server
ALLOW_ANON=1 go run .   # Kystverket + Digitraffic in, WebSocket on :8080, UDP NMEA on :10110, archive/ in cwd
```

`ALLOW_ANON=1` disables tokens; never set it on a public host. Kystverket allows one TCP connection per source IP, so if another server is already running on your network set `KYSTVERKET=0`. Then `cd viewer && python3 -m http.server 8089` and open http://localhost:8089/?server=localhost:8080, or point any aisstream.io client at `ws://localhost:8080/v0/stream` with any non-empty `APIKey`.

Go 1.24 and Node 24 (`mise.toml`, derived from CI; `server/go.mod` is the authoritative Go version). `go test ./...` runs unit tests, the aisstream golden envelope, and the GPSD/libais public fixtures in `server/testdata/`. `go run ./cmd/loadtest -clients 1000 -duration 30s` against a server started with `WS_CONNECTS_PER_MIN=100000` measures fan-out. `go run ./cmd/aiscast-key` mints and inspects access tokens.

## Changes

- Open a pull request against `main`. CI runs gofmt, vet, tests, and a linux build on every push and PR; a merge to `main` deploys aiscast to `ais-server-1` and the viewer to GitHub Pages.
- `/v0/stream` is frozen to aisstream.io's wire format; anything new goes under `/v1`. Additive changes to `/v1` are fine; breaking ones need a note in [docs/architecture.md](docs/architecture.md#the-v0-compatibility-contract).
- Every source gets its own `source` value, license tag in the archive path, and env flag, and stays out of the health gate unless it is an open-licensed feed we commit to.
- Comments explain why, not what; durable docs describe the current state, not history.

## Production

`server/deploy/README.md` has the box, firewall, the managed files under `rootfs/`, the one-step deploy, and the secrets layout. Secrets live in the untracked `.env` at the repo root (issuer seed for tokens, Hetzner/R2/AISHub credentials) and in `/etc/aiscast.env` on the box.

## Releases

The Signal K plugin publishes to npm from `release.yml` when a GitHub release is created with a `signalk-plugin-v*` tag, using trusted publishing over OIDC with provenance. There is no npm token; the workflow needs `id-token: write`. Set the release tag to the version you want published — the workflow derives `package.json` from it and commits the bump only after a successful publish. Node 24 in that workflow is deliberate: trusted publishing needs npm >= 11.5.1. The server and viewer have no release step; a merge to `main` deploys both.

Before cutting a release, work the [Open Waters release preparation checklist](https://github.com/openwatersio/.github/blob/main/docs/agents/releases.md#release-preparation-checklist): review specs and plans from this cycle, move lasting guidance into the docs above and user-facing changes into the release notes, delete completed ones, and have a human review those deletions in the release PR.
