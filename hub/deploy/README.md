# Deploy

One box, one binary, Cloudflare in front of the WebSocket/HTTP side, UDP direct.

1. Build: `GOOS=linux GOARCH=amd64 go build -o hub .` (CCX23 is x86). Copy to `/opt/hub/hub`.
2. `useradd -r -s /usr/sbin/nologin hub; mkdir -p /var/lib/hub/archive; chown -R hub:hub /var/lib/hub`.
3. `/etc/hub.env`: `ADDR=127.0.0.1:8080` (behind the TLS terminator) or `:443` with the Origin CA cert if the hub terminates TLS itself; `UDP_ADDR=:10110`; `ARCHIVE_DIR=/var/lib/hub/archive`; `SNAPSHOT=/var/lib/hub/vessels.json`; `R2_BUCKET=ais-archive`; `V0_API_KEYS=...`; `FEEDER_KEYS=id:secret,...`.
4. `cp deploy/hub.service /etc/systemd/system/ && systemctl enable --now hub`.
5. Cloudflare DNS: `ais.<domain>` proxied (A record → box) for `/v0/stream`, `/v1/*`, `/health`; `ingest.<domain>` unproxied (A record → box) for UDP. WebSockets on in the zone; `permessage-deflate` is negotiated end to end.
6. Firewall: 443 (or the TLS terminator's port), 10110/udp, ssh. `/metrics` stays off the public hostname (bind a second listener or scrape over ssh/WireGuard).
7. Uptime monitor on `wss://ais.<domain>/v0/stream` with a real subscribe, not just a TCP check: the aisstream failure mode was a healthy-looking empty service.
