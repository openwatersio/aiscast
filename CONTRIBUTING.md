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

The server and viewer have no release step; a merge to `main` deploys both.

The Signal K plugin publishes to npm from `release.yml` when a GitHub release is created with a `signalk-plugin-v*` tag. It uses trusted publishing over OIDC with provenance, so there is no npm token and the workflow needs `id-token: write`. Node 24 in that workflow is deliberate, because trusted publishing needs npm >= 11.5.1.

Before cutting a release, work the [Open Waters release preparation checklist](https://github.com/openwatersio/.github/blob/main/docs/agents/releases.md#release-preparation-checklist): review specs and plans from this cycle, move lasting guidance into the docs above and user-facing changes into the release notes, delete completed ones, and have a human review those deletions in the release PR.

To release the plugin:

1. Rename the `## Unreleased` heading in `signalk-plugin/CHANGELOG.md` to the version, and merge that with the change it describes.
2. Create a GitHub release targeting `main`, tagged `signalk-plugin-v<version>`, with the notes from that changelog section. The tag sets the version: the workflow derives `package.json` from it, so leave `package.json` alone until step 4.
3. Watch the run, then confirm the version on the registry.
4. Land the version bump as a pull request.

Two things about that run look like failures and are not.

**The run ends red after the publish succeeded.** Its last step commits the version bump and pushes it to `main`. A ruleset on `main` declines that push, so the job exits non-zero with `GH013: Repository rule violations found`. Read the `npm publish` step before reacting to the red X. Never re-run the workflow to clear it, because the version is already on npm and publishing it a second time fails.

**The new version does not reach npm right away.** npm accepts a publish and then takes minutes to serve it. Until it does, `npm view signalk-aiscast version` and the registry both answer with the previous version. Treat `+ signalk-aiscast@<version>` at the end of the `npm publish` step as the authority, and poll for the rest:

```sh
until curl -s https://registry.npmjs.org/signalk-aiscast | grep -q '"<version>"'; do sleep 15; done
```

The bump the workflow could not push is step 4. It is two lines: `version` in `signalk-plugin/package.json`, and the `signalk-plugin` entry near the end of the root `package-lock.json`. It belongs to the release rather than to the change being released, and until it lands npm and the repository disagree about the current version.
