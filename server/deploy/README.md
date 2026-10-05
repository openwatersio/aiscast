# Deploy

One box, one binary: the aiscast server. The WebSocket/HTTP side is `ais.openwaters.io`, which Cloudflare can proxy. UDP goes straight to the box under its own name, `udp.ais.openwaters.io`, because a proxied name drops UDP.

Everything the box needs lives in this directory, and changing any of it is a pull request:

- [`rootfs/`](rootfs/) mirrors the managed files on the box, copied to `/` verbatim: the systemd unit, [Caddyfile](rootfs/etc/caddy/Caddyfile), the [Alloy config](rootfs/etc/alloy/config.alloy), sshd hardening, and the fail2ban jail. Add a file here (a new unit, a timer) and merge; the next deploy installs it.
- [`apply.sh`](apply.sh) converges a box and restarts the service: installs packages (alloy, caddy, and clickhouse from their vendors' apt repositories, since Ubuntu's caddy is too old for the Caddyfile, plus curl, fail2ban, jq for reading the logs, unattended-upgrades), creates the `aiscast` user and its directories, copies `rootfs/`, validates sshd and the Caddyfile before reloading them, installs the bundled binary, and restarts aiscast exactly once. Binary and config always ship together, so they cannot get out of step. It is idempotent and safe to rerun; it seeds `/etc/aiscast.env` and `/etc/alloy.env` only when missing and never overwrites them.
- [`deploy.sh`](deploy.sh) is the whole deploy: build (or take a prebuilt binary), send the bundle over ssh, run `apply.sh`. The same command sets up a fresh box and updates the live one: `server/deploy/deploy.sh root@<ip>`.
- [`aiscast.env.example`](aiscast.env.example) lists every variable `/etc/aiscast.env` holds, and [`alloy.env.example`](alloy.env.example) the Grafana Cloud credentials in `/etc/alloy.env`. The real files stay on the box; secrets never enter the repo.
- ClickHouse runs beside aiscast and listens on loopback only. [Its config](rootfs/etc/clickhouse-server/config.d/aiscast.xml) caps it at 4 GB and exports metrics for Alloy, and [a systemd drop-in](rootfs/etc/systemd/system/clickhouse-server.service.d/10-aiscast.conf) gives aiscast the CPU first and a hard memory ceiling to ClickHouse. aiscast writes to it when `CLICKHOUSE_URL` is set. Once `/etc/clickhouse-server/r2.env` holds an R2 token ([`clickhouse-r2.env.example`](clickhouse-r2.env.example)), `apply.sh` installs [`clickhouse-r2.xml`](clickhouse-r2.xml): `receptions` moves its parts to the `ais-clickhouse` bucket 30 days past their rows' time, behind an 8 GB local read cache, and [a nightly job](rootfs/opt/aiscast/clickhouse-backup.sh) at 04:00 UTC backs up the database to `ais-clickhouse-backup`, a full backup the first night of each month and an incremental one against it every other night. That bucket deletes objects after 70 days, which leaves the current and previous month's backups whole. The backup is the only copy of history off the box: Hetzner's images of the box are not enough, since a cold part's metadata on the box can point at objects a merge has since deleted. See [Restoring ClickHouse](#restoring-clickhouse).
- [`grafana/`](grafana/) holds the alert rules and the capacity dashboard, applied to Grafana Cloud by [`grafana/push.sh`](grafana/push.sh).

## What exists

- Hetzner Cloud server `ais-server-1`: `cx43` (8 shared vCPU, 16 GB, 160 GB NVMe, 20 TB/mo traffic, €18.49/mo) in `hel1` (Helsinki), Ubuntu 24.04, IPv4 `2.29.0.215`, IPv6 `2a01:4f9:c015:e7ca::/64`, label `project=ais`. Resize when the CPU alerts fire, and move to a dedicated-CPU type (`ccx23`, €101.49/mo) when the steal alerts do.
- Firewall `ais-server`: in 22/tcp, 80/tcp, 443/tcp, 10110/udp, ICMP.
- SSH: root logs in with `bkeepers-ed25519` (the 1Password agent key) and the CI deploy key (`DEPLOY_SSH_KEY` secret). sshd is key-only with the settings in [rootfs](rootfs/etc/ssh/sshd_config.d/10-hardening.conf) plus fail2ban (3 tries / 10 min → 1 h ban): the box gets continuous root-password brute force, and with sshd defaults those attempts fill the pre-auth slots and randomly drop real connections, including CI deploys.
- On the box: user `aiscast` runs `/opt/aiscast/aiscast` with state under `/var/lib/aiscast/{archive,aiscast.db,vessels-usage.json}` and config in `/etc/aiscast.env` (0600). The issuer *seed* for minting tokens lives only in the repo's untracked `.env` as `ISSUER_SEED`/`ISSUER_KID`.
- Public: `https://ais.openwaters.io` serves `/v0/stream`, `/v1/stream`, `/v1/vessels`, `/v1/receive`, and `/health`. The request path is a DNS-only A record → Caddy → aiscast on `127.0.0.1:8080`. The Caddyfile sets the Let's Encrypt cert, `zstd`/`gzip` response compression, a block on `/metrics`, which stays reachable only on the box, per-request metrics on Caddy's admin endpoint at `localhost:2019`, and the access log described under [Access logs](#access-logs). Cloudflare proxying is off for the beta, and `TRUST_CF_HEADERS=1` re-enables it if the box needs DDoS cover.
- UDP ingest at `udp.ais.openwaters.io:10110`, a DNS-only name for the same box. It is separate from the API name so that name can be proxied: a proxied name drops UDP, and a forwarder resolves its target once and keeps sending to that address until restarted, so feeders still using the API name stop silently at their next restart once it is proxied. `UDP_ADDR` can bind one labelled listener per address, counted apart in `/metrics`, which becomes useful if the UDP name ever gets its own address.
- Upstreams: Kystverket, BarentsWatch, Digitraffic, aisstream.io. Credentials go in `/etc/aiscast.env`.
- Archive hours upload to R2 `ais-archive` over the S3 API on rotation. The bucket is the archive and the box is only staging. An hour closes and uploads once receptions are five minutes past its end, since sources interleave two hours near a boundary. Rotation uploads but never deletes, because a reception that arrives later still reopens that hour and appends to it; a file deleted at rotation would come back as a stub and overwrite the complete object. An hourly sweep does the deleting. It skips any file the writer still holds open, since deleting one would strand the gzip footer. It deletes a file the bucket already holds at the same size, uploads one that is missing or short, and leaves alone any object larger than its local file, which means a stub is sitting over a good upload and needs a human. A file touched in the last two hours is uploaded the same way but never deleted. Shutdown closes the open hours without uploading them, because the largest take most of a minute to send and a restart would wait on them. The sweep at boot uploads them instead, within a minute of the new process starting. Steady-state disk is a few hours of traffic, well under 1 GB.
- The journal is capped at 500 MB ([rootfs](rootfs/etc/systemd/journald.conf.d/10-cap.conf)). The default is 10% of the filesystem capped at 4 GB, so 4 GB here.

## Continuous deployment

[`ci.yml`](../../.github/workflows/ci.yml): every push and PR runs gofmt/vet/test and builds the linux/amd64 binary. A push to `main` then runs `deploy.sh` with that tested artifact against the `production` environment (add approvals or branch rules there). Every merge re-converges the whole box, so config drift gets corrected on every deploy. The run fails if `/health` does not answer after the restart.

Deploys ssh in as root: `apply.sh` writes to `/etc` and manages systemd, so a service-level account would not be meaningfully weaker — anyone who can push to `main` controls the files it installs. Repository secrets: `DEPLOY_HOST` (the box IP), `DEPLOY_KNOWN_HOSTS` (`ssh-keyscan -t ed25519 <ip>`), `DEPLOY_SSH_KEY` (the CI ssh key; its public half is in root's `authorized_keys`).

Manual deploy: `server/deploy/deploy.sh root@2.29.0.215`. Logs: `ssh root@2.29.0.215 journalctl -u aiscast -f`. `systemctl stop aiscast` closes the open archive hours and writes the vessel record before exit. The hours upload when the server next starts, so on a box whose server will not start again, upload them by hand: `systemd-run --wait -p User=aiscast -p EnvironmentFile=/etc/aiscast.env -p WorkingDirectory=/var/lib/aiscast /opt/aiscast/aiscast sweep`.

## Replacing the server

`/var/lib/aiscast` is disposable: the archive is in R2 and everything else rebuilds from live traffic. `aiscast.db`, the vessel record, rebuilds too, but only from the vessels heard after the move, so the rest lose their last known position unless it is copied. Copying it also brings the live map across, which otherwise refills over a few minutes. ClickHouse's `/var/lib/clickhouse` does not rebuild: it holds every track, or the local metadata of the parts on R2, so it is copied too. If the old box is gone, restore from the backup instead ([Restoring ClickHouse](#restoring-clickhouse)). So a replacement is:

1. Create the server with the Hetzner API (`HCLOUD_TOKEN` from the repo's `.env`; no `hcloud` CLI needed), reusing the existing firewall and ssh keys — include the CI deploy public key in `ssh_keys`:

   ```sh
   set -a; . ./.env; set +a; H="Authorization: Bearer $HCLOUD_TOKEN"; A=https://api.hetzner.cloud/v1
   python3 -c 'import json;print(json.dumps({"name":"ais-server-2","server_type":"cx43","location":"hel1","image":"ubuntu-24.04","ssh_keys":["bkeepers-ed25519","aiscast-deploy"],"firewalls":[{"firewall":<firewall id>}],"labels":{"project":"ais"}}))' > /tmp/create.json
   curl -H "$H" -H "Content-Type: application/json" -X POST $A/servers -d @/tmp/create.json
   ```

   (First-time setup of ssh keys and the firewall uses `POST $A/ssh_keys` and `POST $A/firewalls` with the rules above.)

2. Update the `DEPLOY_HOST` and `DEPLOY_KNOWN_HOSTS` secrets for the new IP.
3. Deploy: rerun the CI deploy job, or `server/deploy/deploy.sh root@<ip>`.
4. Copy the vessel record and the history: `systemctl stop aiscast clickhouse-server` on both boxes, then copy `/var/lib/aiscast/aiscast.db` to the new one, owned by `aiscast`, and `/var/lib/clickhouse` with `rsync -a`, owned by `clickhouse`. Start `clickhouse-server` on the new box before aiscast, which otherwise answers tracks with 503 until it connects. Run the `aiscast sweep` command from [Continuous deployment](#continuous-deployment) on the old box, so the hours it closed reach the bucket.
5. Fill `/etc/aiscast.env` and `/etc/alloy.env`: copy them from the old box, or refill the seeded templates from `aiscast.env.example` and `alloy.env.example`, then `systemctl restart aiscast` and `systemctl enable --now alloy`.
6. Move the `ais.openwaters.io` and `udp.ais.openwaters.io` records to the new IP. Stream clients follow the name, so keep its TTL low. UDP feeders resolve once, so they keep sending to the old box until they restart; a floating IP would let them follow at once. Retire the old box once traffic drains.

## Restoring ClickHouse

The backup bucket alone rebuilds the database, cold parts included. On a box that has been deployed, with the R2 keys in `/etc/clickhouse-server/r2.env` and ClickHouse running, but before aiscast has written:

1. `systemctl stop aiscast`, so the live writer writes nothing meanwhile. If it has already created an empty database, `clickhouse-client -q "DROP DATABASE aiscast SYNC"`.
2. List the backups: `clickhouse-client -q "SELECT name, end_time FROM system.backup_log WHERE status = 'BACKUP_CREATED' ORDER BY end_time"` on the old box, or the top-level prefixes of the `ais-clickhouse-backup` bucket. Take the newest `incr-` backup of the current month, or its `full-` backup if there is none yet.
3. `clickhouse-client -q "RESTORE DATABASE aiscast FROM Disk('backups', '<name>')"`. A restored part lands on local disk, and parts past 30 days move back to R2 in the background, so the disk needs room for the cold data until they do. To restore one month at a time instead, oldest first: `RESTORE TABLE aiscast.receptions PARTITIONS '<YYYYMM>' FROM Disk('backups', '<name>') SETTINGS allow_non_empty_tables = true` for each month, after restoring the other tables whole.
4. `systemctl start aiscast`. ClickHouse lacks what arrived between the backup and the restore; the raw archive keeps it.

## Monitoring

[status.openwaters.io](https://status.openwaters.io) ([openwatersio/status](https://github.com/openwatersio/status), Upptime on GitHub Actions) is the outside-in check. It checks `/health`, `/v1/stream`, `/v1/vessels` and the viewer every 5 minutes, and opens an assigned issue, which emails, when something is down. `/health` is the end-to-end signal. It fails when the server's own loopback `/v1/stream` subscriber has received nothing for two minutes, meaning the stream is delivering no events; a single silent upstream is not an outage.

Grafana Alloy on the box ships metrics to Grafana Cloud, where they outlive the box. [`config.alloy`](rootfs/etc/alloy/config.alloy) scrapes three targets every 60 s: aiscast's `/metrics` on `127.0.0.1:8080`, Caddy's admin endpoint on `127.0.0.1:2019`, and Alloy's built-in unix exporter for CPU (steal included), memory, network, disk, and file descriptors. The exporter also reads systemd's automatic-restart counter for aiscast, Caddy, and Alloy. That counter moves only when systemd restarts a process that exited on its own, so the `ServiceCrashed` alert and the dashboard's crash markers never fire for a deploy or a `systemctl restart`. Deploys show as a new `revision` on `aiscast_build_info`. Every series carries a `host` label. None of it is reachable from outside: Caddy answers `/metrics` with 404, and the firewall admits only the ports above.

The push URL and token live in `/etc/alloy.env` (0600, root), which a systemd drop-in hands to the alloy unit. `apply.sh` seeds the file from [`alloy.env.example`](alloy.env.example) and keeps Alloy stopped until `GRAFANA_CLOUD_PROM_URL` is set. To connect a Grafana Cloud stack:

1. In the stack's Prometheus details, copy the remote write URL and the numeric user. The remote write URL ends in `/api/prom/push`; the query URL beside it ends in `/api/prom` and answers pushes with 404. Create an access policy token with the `metrics:write` scope.
2. Fill in `/etc/alloy.env` on the box, then rerun the deploy or run `systemctl enable --now alloy`.

The alert rules are [`grafana/rules.yaml`](grafana/rules.yaml), in Prometheus rule format, and the dashboard is [`grafana/capacity.json`](grafana/capacity.json). [`grafana/push.sh`](grafana/push.sh) applies both. It imports the rules as Grafana-managed rules in the `aiscast` folder, routes them to an `aiscast email` contact point, deletes rules that left the file, and uploads the dashboard. Rerun it after changing either file. Grafana Cloud emails only members of its organization, so `ALERT_EMAIL` must belong to one. It needs `curl`, `jq`, `mimirtool`, and a service account token with the Admin role:

```sh
GRAFANA_URL=https://<stack>.grafana.net GRAFANA_TOKEN=<token> ALERT_EMAIL=<address> server/deploy/grafana/push.sh
```

`grep -v '^namespace:' server/deploy/grafana/rules.yaml | promtool check rules` checks the rules locally.

## Access logs

Two logs record every request, and the request id joins them.

- aiscast's access log, in its own private R2 bucket (`ACCESS_BUCKET`, `ais-access`) under `access/v1/YYYY/MM/DD/HH.gz`, kept 90 days. It must never share a bucket with the archive, which is meant to become public. Lines are personal data, since the hash key can recover an address from its /24, and the bucket keeps them 90 days. The bucket and its rule were made with wrangler, logged in to the Open Waters account:

```sh
wrangler r2 bucket create ais-access
wrangler r2 bucket lifecycle add ais-access access-90-days access/ --expire-days 90 --force
wrangler r2 bucket lifecycle list ais-access   # the 90-day rule beside R2's default multipart abort rule
```

Public access stays off: no r2.dev URL and no custom domain. The server's R2 token needs read and write on `ais-access`, and `/etc/aiscast.env` needs `ACCESS_BUCKET=ais-access`. Until both are set, the hours stay under `ACCESS_DIR`, the sweep deletes them at 90 days, and it uploads the rest once the bucket is set. It names a client by its /24 or /48 and a keyed hash of its address, and a token by its subject. It is the one to analyze. [server/README.md](../README.md#access-log) lists its fields.
- Caddy's access log, `/var/log/caddy/access.log` on the box, in Caddy's JSON format, with the full client address, the URI, status, timing, and sizes, and the request id. No request header is written, since a token can arrive under any header name. The URI loses the usual token parameters (`key`, `token`, `api_key`, `access_token`, any case), any token in it in the usual encodings, and the `around` and `bbox` parameters, so no search position or view sits beside a full address; aiscast's log keeps those coordinates rounded and the user agent and origin with tokens redacted. Tile paths stay whole: Caddy's filters cannot coarsen them. It rolls daily and at 100 MiB, gzips what it rolls, and deletes rolled files after 12 days, so no address stays two weeks. It lives only on the box and goes with it.

To find the address behind load, take the busiest clients for the window from the R2 log:

```sql
SELECT client, net, sub, any_value(ua) AS ua, count(*) AS n, sum(ms) / 1000 AS busy_s, any_value(id) AS id
FROM read_json_auto('access/v1/2026/10/02/*.gz') GROUP BY ALL ORDER BY busy_s DESC LIMIT 10;
```

Then look one of its request ids up on the box:

```sh
ssh root@2.29.0.215 "zcat -f /var/log/caddy/access*.log* | grep -F '\"request_id\":\"<id>\"' | jq -r .request.client_ip"
```

While it is still happening, the Caddy log answers alone:

```sh
ssh root@2.29.0.215 "tail -n 200000 /var/log/caddy/access.log | jq -r .request.client_ip | sort | uniq -c | sort -rn | head"
```

## Profiling

aiscast serves `net/http/pprof` on `127.0.0.1:6060`, set by `PPROF_ADDR`. Reach it over an ssh tunnel:

```sh
ssh -N -L 6060:127.0.0.1:6060 root@2.29.0.215 &
go tool pprof -http=: 'http://localhost:6060/debug/pprof/profile?seconds=30'
```

`/debug/pprof/heap` and `/debug/pprof/goroutine` work the same way.

`perf` and `tcpdump` are installed on the box for what pprof cannot see: Caddy, the kernel, or an aiscast that no longer answers HTTP. This samples aiscast for 30 s at 199 Hz with call graphs, then prints each function's inclusive share of CPU:

```sh
perf record -F 199 -g -p $(pgrep -x aiscast) -o /tmp/x.data -- sleep 30
perf report -i /tmp/x.data --stdio --children --sort symbol -g none
```

`perf` comes from `linux-tools-$(uname -r)`, which follows the running kernel, so a new box needs it installed by hand.

## Still to do

1. Mint feeder tokens for the first volunteer stations: `go run ./cmd/aiscast-key new -sub <station> -role feeder -exp 8760h` (seed/kid from `.env`). A fleet customer gets `-role partner -area -1 -mmsis <n> -conns 10`: MMSI subscriptions only, no bbox.
