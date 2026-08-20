# Deploy

One box, one binary. Cloudflare goes in front of the WebSocket/HTTP side once DNS exists; UDP goes straight to the box.

## What exists

- Hetzner Cloud server `ais-hub-1`: `cx43` (8 shared vCPU, 16 GB, 160 GB NVMe, 20 TB/mo traffic, €18.49/mo) in `hel1` (Helsinki), Ubuntu 24.04, IPv4 `2.29.0.215`, IPv6 `2a01:4f9:c015:e7ca::/64`, label `project=ais`. Resize to a dedicated-CPU type (`ccx23`, €101.49/mo) only if fan-out CPU shows up in `/metrics`; the load test ran 1,000 clients at ~12% of one core.
- Firewall `ais-hub`: in 22/tcp, 80/tcp, 443/tcp, 10110/udp, ICMP.
- SSH key `bkeepers-ed25519` (the 1Password agent key); root login with it.
- On the box (from [cloud-init.yaml](cloud-init.yaml)): user `hub`, `/opt/hub/hub`, `/var/lib/hub/{archive,vessels.json}`, `/etc/hub.env` (0600; holds `V0_API_KEYS`, also in the repo's untracked `.env` as `V0_API_KEY_PROD`), `/etc/systemd/system/hub.service` enabled.
- Public: `https://ais.openwaters.io` (DNS-only A record → Caddy with a Let's Encrypt cert → hub on `127.0.0.1:8080`; Cloudflare proxying is off for the beta and can be re-enabled with `TRUST_CF_HEADERS=1` if the box needs DDoS cover): `/v0/stream`, `/v1/stream`, `/v1/vessels`, `/v1/receive`, `/health`; `/metrics` is only reachable on the box. UDP ingest at `ais.openwaters.io:10110` (same name; it resolves straight to the box). Upstreams: Kystverket, Digitraffic, aisstream.io (best effort, not in the health gate). Archive hours upload to R2 `ais-archive` over the S3 API on rotation and on shutdown.

## How it was created

Hetzner Cloud API with `HCLOUD_TOKEN` from the repo's `.env` (no `hcloud` CLI needed):

```sh
set -a; . ./.env; set +a; H="Authorization: Bearer $HCLOUD_TOKEN"; A=https://api.hetzner.cloud/v1
curl -H "$H" -H "Content-Type: application/json" -X POST $A/ssh_keys -d '{"name":"bkeepers-ed25519","public_key":"ssh-ed25519 AAAA..."}'
curl -H "$H" -H "Content-Type: application/json" -X POST $A/firewalls -d '{"name":"ais-hub","rules":[{"direction":"in","protocol":"tcp","port":"22","source_ips":["0.0.0.0/0","::/0"]}, ...80, 443, 8080 tcp; 10110 udp; icmp...]}'
sed "s/__V0_API_KEY__/$(openssl rand -hex 16)/" hub/deploy/cloud-init.yaml > /tmp/ci.yaml
python3 -c 'import json;print(json.dumps({"name":"ais-hub-1","server_type":"cx43","location":"hel1","image":"ubuntu-24.04","ssh_keys":["bkeepers-ed25519"],"firewalls":[{"firewall":<firewall id>}],"user_data":open("/tmp/ci.yaml").read(),"labels":{"project":"ais"}}))' > /tmp/create.json
curl -H "$H" -H "Content-Type: application/json" -X POST $A/servers -d @/tmp/create.json
ssh root@<ip> cloud-init status --wait
hub/deploy/deploy.sh root@<ip>     # builds linux/amd64, scp, systemctl restart, /health
```

## Continuous deployment

[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml): every push and PR runs gofmt/vet/test and builds the linux/amd64 binary; a push to `main` then copies it to the box as the `deploy` user and restarts the service, failing the run if `/health` doesn't answer. The `deploy` user has its own ed25519 key (`/home/deploy/.ssh/authorized_keys`), owns `/opt/hub`, and can run only `sudo systemctl restart hub` / `is-active hub` (`/etc/sudoers.d/deploy`). Repository secrets: `DEPLOY_SSH_KEY` (that private key), `DEPLOY_HOST` (the box IP), `DEPLOY_KNOWN_HOSTS` (`ssh-keyscan -t ed25519 <ip>`). The job uses the `production` environment, so approvals or branch rules can be added there.

Manual redeploy after a code change: `hub/deploy/deploy.sh root@2.29.0.215`. Logs: `ssh root@2.29.0.215 journalctl -u hub -f`. `systemctl stop hub` snapshots the vessel cache and flushes the open archive hour before exit.

## Still to do

1. `FEEDER_KEYS` for the first volunteer stations.
2. Uptime monitor on `wss://ais.openwaters.io/v0/stream` with a real subscribe, not just a TCP check.
