# Deploy

One box, one binary: the aiscast server. Cloudflare goes in front of the WebSocket/HTTP side once DNS exists. UDP goes straight to the box.

## What exists

- Hetzner Cloud server `ais-server-1`: `cx43` (8 shared vCPU, 16 GB, 160 GB NVMe, 20 TB/mo traffic, €18.49/mo) in `hel1` (Helsinki), Ubuntu 24.04, IPv4 `2.29.0.215`, IPv6 `2a01:4f9:c015:e7ca::/64`, label `project=ais`. Resize to a dedicated-CPU type (`ccx23`, €101.49/mo) only if fan-out CPU appears in `/metrics`. The load test ran 1,000 clients at ~12% of one core.
- Firewall `ais-server`: in 22/tcp, 80/tcp, 443/tcp, 10110/udp, ICMP.
- SSH key `bkeepers-ed25519` (the 1Password agent key). Root login uses it. sshd is key-only with `MaxStartups 50:30:200`, `PerSourceMaxStartups 3`, and `LoginGraceTime 20` (`/etc/ssh/sshd_config.d/10-hardening.conf`), plus fail2ban (`/etc/fail2ban/jail.local`: 3 tries / 10 min → 1 h ban). The box gets continuous root-password brute force, and with the sshd defaults those attempts filled the pre-auth slots and randomly dropped real connections, including CI deploys.
- On the box (from [cloud-init.yaml](cloud-init.yaml)): user `aiscast`, `/opt/aiscast/aiscast`, `/var/lib/aiscast/{archive,vessels.json}`, `/etc/aiscast.env` (0600), and `/etc/systemd/system/aiscast.service` enabled. `/etc/aiscast.env` holds `ISSUER_PUBKEYS`, `PERSONAL_ISSUER_KEY`, and the R2 and AISHub/aisstream settings. The issuer *seed* for minting tokens lives only in the repo's untracked `.env` as `ISSUER_SEED`/`ISSUER_KID`.
- Public: `https://ais.openwaters.io` serves `/v0/stream`, `/v1/stream`, `/v1/vessels`, `/v1/receive`, and `/health`. The request path is a DNS-only A record → Caddy → aiscast on `127.0.0.1:8080`. [Caddyfile](Caddyfile) sets the Let's Encrypt cert, `zstd`/`gzip` response compression, and a block on `/metrics`, which stays reachable only on the box. Cloudflare proxying is off for the beta, and `TRUST_CF_HEADERS=1` re-enables it if the box needs DDoS cover.
- UDP ingest at `ais.openwaters.io:10110`, the same name, which resolves straight to the box.
- Upstreams: Kystverket, BarentsWatch, Digitraffic, aisstream.io (best effort, outside the health gate). BarentsWatch credentials go in `/etc/aiscast.env` (`BARENTSWATCH_CLIENT_ID`, `BARENTSWATCH_CLIENT_SECRET`).
- Archive hours upload to R2 `ais-archive` over the S3 API on rotation and on shutdown.

## How it was created

Hetzner Cloud API with `HCLOUD_TOKEN` from the repo's `.env` (no `hcloud` CLI needed):

```sh
set -a; . ./.env; set +a; H="Authorization: Bearer $HCLOUD_TOKEN"; A=https://api.hetzner.cloud/v1
curl -H "$H" -H "Content-Type: application/json" -X POST $A/ssh_keys -d '{"name":"bkeepers-ed25519","public_key":"ssh-ed25519 AAAA..."}'
curl -H "$H" -H "Content-Type: application/json" -X POST $A/firewalls -d '{"name":"ais-server","rules":[{"direction":"in","protocol":"tcp","port":"22","source_ips":["0.0.0.0/0","::/0"]}, ...80, 443, 8080 tcp; 10110 udp; icmp...]}'
sed "s/__V0_API_KEY__/$(openssl rand -hex 16)/" server/deploy/cloud-init.yaml > /tmp/ci.yaml
python3 -c 'import json;print(json.dumps({"name":"ais-server-1","server_type":"cx43","location":"hel1","image":"ubuntu-24.04","ssh_keys":["bkeepers-ed25519"],"firewalls":[{"firewall":<firewall id>}],"user_data":open("/tmp/ci.yaml").read(),"labels":{"project":"ais"}}))' > /tmp/create.json
curl -H "$H" -H "Content-Type: application/json" -X POST $A/servers -d @/tmp/create.json
ssh root@<ip> cloud-init status --wait
server/deploy/deploy.sh root@<ip>     # builds linux/amd64, scp, systemctl restart, /health
```

## Continuous deployment

[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml): every push and PR runs gofmt/vet/test and builds the linux/amd64 binary. A push to `main` then copies the binary and [Caddyfile](Caddyfile) to the box as the `deploy` user, restarts the service, and reloads Caddy. The reload is graceful: open connections survive, and Caddy rejects a bad Caddyfile and keeps the old config. The run fails if `/health` does not answer.

The `deploy` user has its own ed25519 key (`/home/deploy/.ssh/authorized_keys`) and owns `/opt/aiscast` and `/etc/caddy/Caddyfile`. It can run only `sudo systemctl restart aiscast` / `is-active aiscast` / `reload caddy` / `is-active caddy` (`/etc/sudoers.d/deploy`). Repository secrets: `DEPLOY_SSH_KEY` (that private key), `DEPLOY_HOST` (the box IP), `DEPLOY_KNOWN_HOSTS` (`ssh-keyscan -t ed25519 <ip>`). The job uses the `production` environment, so you can add approvals or branch rules there.

Manual redeploy: `server/deploy/deploy.sh root@2.29.0.215` (binary and Caddyfile, same as CI). Logs: `ssh root@2.29.0.215 journalctl -u aiscast -f`. `systemctl stop aiscast` snapshots the vessel cache and flushes the open archive hour before exit.

## Still to do

1. Mint feeder tokens for the first volunteer stations: `go run ./cmd/aiscast-key new -sub <station> -role feeder -exp 8760h` (seed/kid from `.env`). A fleet customer gets `-role partner -area -1 -mmsis <n> -conns 10`: MMSI subscriptions only, no bbox.
2. Monitoring: [status.openwaters.io](https://status.openwaters.io) ([openwatersio/status](https://github.com/openwatersio/status), Upptime on GitHub Actions) checks `/health`, `/v1/stream`, `/v1/vessels` and the viewer every 5 minutes. It opens an issue when something is down, and the issue is assigned, so it emails. `/health` is the end-to-end signal. It fails when an open-feed upstream is silent, or when the server's own loopback `/v1/stream` subscriber has received nothing for two minutes. `/metrics` stays box-local. Scrape it with a Prometheus/Grafana Cloud agent if capacity questions arise.
