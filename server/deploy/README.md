# Deploy

One box, one binary: the aiscast server. Cloudflare goes in front of the WebSocket/HTTP side once DNS exists. UDP goes straight to the box.

Everything the box needs lives in this directory, and changing any of it is a pull request:

- [`rootfs/`](rootfs/) mirrors the managed files on the box, copied to `/` verbatim: the systemd unit, [Caddyfile](rootfs/etc/caddy/Caddyfile), sshd hardening, and the fail2ban jail. Add a file here (a new unit, a timer) and merge; the next deploy installs it.
- [`apply.sh`](apply.sh) converges a box and restarts the service: installs packages (caddy, curl, fail2ban, unattended-upgrades), creates the `aiscast` user and its directories, copies `rootfs/`, validates sshd and the Caddyfile before reloading them, installs the bundled binary, and restarts aiscast exactly once. Binary and config always ship together, so they cannot get out of step. It is idempotent and safe to rerun; it seeds `/etc/aiscast.env` only when missing and never overwrites it.
- [`deploy.sh`](deploy.sh) is the whole deploy: build (or take a prebuilt binary), send the bundle over ssh, run `apply.sh`. The same command sets up a fresh box and updates the live one: `server/deploy/deploy.sh root@<ip>`.
- [`aiscast.env.example`](aiscast.env.example) lists every variable `/etc/aiscast.env` holds. The real file stays on the box; secrets never enter the repo.

## What exists

- Hetzner Cloud server `ais-server-1`: `cx43` (8 shared vCPU, 16 GB, 160 GB NVMe, 20 TB/mo traffic, €18.49/mo) in `hel1` (Helsinki), Ubuntu 24.04, IPv4 `2.29.0.215`, IPv6 `2a01:4f9:c015:e7ca::/64`, label `project=ais`. Resize to a dedicated-CPU type (`ccx23`, €101.49/mo) only if fan-out CPU appears in `/metrics`. The load test ran 1,000 clients at ~12% of one core.
- Firewall `ais-server`: in 22/tcp, 80/tcp, 443/tcp, 10110/udp, ICMP.
- SSH: root logs in with `bkeepers-ed25519` (the 1Password agent key) and the CI deploy key (`DEPLOY_SSH_KEY` secret). sshd is key-only with the settings in [rootfs](rootfs/etc/ssh/sshd_config.d/10-hardening.conf) plus fail2ban (3 tries / 10 min → 1 h ban): the box gets continuous root-password brute force, and with sshd defaults those attempts fill the pre-auth slots and randomly drop real connections, including CI deploys.
- On the box: user `aiscast` runs `/opt/aiscast/aiscast` with state under `/var/lib/aiscast/{archive,vessels.json}` and config in `/etc/aiscast.env` (0600). The issuer *seed* for minting tokens lives only in the repo's untracked `.env` as `ISSUER_SEED`/`ISSUER_KID`.
- Public: `https://ais.openwaters.io` serves `/v0/stream`, `/v1/stream`, `/v1/vessels`, `/v1/receive`, and `/health`. The request path is a DNS-only A record → Caddy → aiscast on `127.0.0.1:8080`. The Caddyfile sets the Let's Encrypt cert, `zstd`/`gzip` response compression, and a block on `/metrics`, which stays reachable only on the box. Cloudflare proxying is off for the beta, and `TRUST_CF_HEADERS=1` re-enables it if the box needs DDoS cover.
- UDP ingest at `ais.openwaters.io:10110`, the same name, which resolves straight to the box.
- Upstreams: Kystverket, BarentsWatch, Digitraffic, aisstream.io. Credentials go in `/etc/aiscast.env`.
- Archive hours upload to R2 `ais-archive` over the S3 API on rotation and on shutdown.

## Continuous deployment

[`ci.yml`](../../.github/workflows/ci.yml): every push and PR runs gofmt/vet/test and builds the linux/amd64 binary. A push to `main` then runs `deploy.sh` with that tested artifact against the `production` environment (add approvals or branch rules there). Every merge re-converges the whole box, so config drift gets corrected on every deploy. The run fails if `/health` does not answer after the restart.

Deploys ssh in as root: `apply.sh` writes to `/etc` and manages systemd, so a service-level account would not be meaningfully weaker — anyone who can push to `main` controls the files it installs. Repository secrets: `DEPLOY_HOST` (the box IP), `DEPLOY_KNOWN_HOSTS` (`ssh-keyscan -t ed25519 <ip>`), `DEPLOY_SSH_KEY` (the CI ssh key; its public half is in root's `authorized_keys`).

Manual deploy: `server/deploy/deploy.sh root@2.29.0.215`. Logs: `ssh root@2.29.0.215 journalctl -u aiscast -f`. `systemctl stop aiscast` snapshots the vessel cache and flushes the open archive hour before exit.

## Replacing the server

`/var/lib/aiscast` is disposable: the archive is in R2 and the vessel cache rebuilds from live traffic. So a replacement is:

1. Create the server with the Hetzner API (`HCLOUD_TOKEN` from the repo's `.env`; no `hcloud` CLI needed), reusing the existing firewall and ssh keys — include the CI deploy public key in `ssh_keys`:

   ```sh
   set -a; . ./.env; set +a; H="Authorization: Bearer $HCLOUD_TOKEN"; A=https://api.hetzner.cloud/v1
   python3 -c 'import json;print(json.dumps({"name":"ais-server-2","server_type":"cx43","location":"hel1","image":"ubuntu-24.04","ssh_keys":["bkeepers-ed25519","aiscast-deploy"],"firewalls":[{"firewall":<firewall id>}],"labels":{"project":"ais"}}))' > /tmp/create.json
   curl -H "$H" -H "Content-Type: application/json" -X POST $A/servers -d @/tmp/create.json
   ```

   (First-time setup of ssh keys and the firewall uses `POST $A/ssh_keys` and `POST $A/firewalls` with the rules above.)

2. Update the `DEPLOY_HOST` and `DEPLOY_KNOWN_HOSTS` secrets for the new IP.
3. Deploy: rerun the CI deploy job, or `server/deploy/deploy.sh root@<ip>`.
4. Fill `/etc/aiscast.env`: copy it from the old box, or refill the seeded template from `aiscast.env.example`, then `systemctl restart aiscast`.
5. Move the `ais.openwaters.io` A record to the new IP. UDP feeders and stream clients follow the name; keep its TTL low. Retire the old box once traffic drains.

## Still to do

1. Mint feeder tokens for the first volunteer stations: `go run ./cmd/aiscast-key new -sub <station> -role feeder -exp 8760h` (seed/kid from `.env`). A fleet customer gets `-role partner -area -1 -mmsis <n> -conns 10`: MMSI subscriptions only, no bbox.
2. Monitoring: [status.openwaters.io](https://status.openwaters.io) ([openwatersio/status](https://github.com/openwatersio/status), Upptime on GitHub Actions) checks `/health`, `/v1/stream`, `/v1/vessels` and the viewer every 5 minutes. It opens an issue when something is down, and the issue is assigned, so it emails. `/health` is the end-to-end signal. It fails when the server's own loopback `/v1/stream` subscriber has received nothing for two minutes, meaning the stream is delivering no events; a single silent upstream is not an outage. `/metrics` stays box-local. Scrape it with a Prometheus/Grafana Cloud agent if capacity questions arise.
