# Every public client counts as one address

Behind Caddy the server sees every public connection as coming from `127.0.0.1`, so all the per-address limits are shared by everyone: 8 concurrent WebSocket streams, 20 WebSocket connects per minute, 120 HTTP requests per minute, and 3 token mints per minute, for the whole public service. Once eight viewers and feeders are connected, the ninth client is refused with `concurrent connections per key exceeded` whatever its token, and reconnect attempts see `429 rate limited`.

## Evidence

- 2026-08-22 21:05 UTC: a freshly minted admin token (unlimited `conns`, a subject that had never opened a stream) was refused on its first `/v1/stream` connection with `{"type":"error","error":"concurrent connections per key exceeded"}`; the next attempt got `429 rate limited` on the upgrade. `lsof` on the client machine showed no other connection to the server, so the eight streams charged to its "address" belonged to other people.
- 2026-08-21: the Signal K plugin on a laptop (egress `153.66.157.205`, no other process connected to the server) minted a personal token and was refused on its first connection with the same message; for ten minutes a plain `GET /v1/stream` from that laptop alternated between `429 rate limited` and `426` as other clients consumed the connect budget. The plugin's backoff grew to minutes and it never connected.
- The only address the server can be observing in both cases is the proxy's.

## Mechanism

- `clientIP` ([server/ops.go](../server/ops.go)) returns the TCP peer unless `TRUST_CF_HEADERS=1`, in which case it returns `CF-Connecting-IP`. It never reads `X-Forwarded-For`.
- The shipped [Caddyfile](../server/deploy/Caddyfile) does `reverse_proxy localhost:8080`, so the peer is always `127.0.0.1`. Caddy sets `X-Forwarded-For` on every proxied request; nothing uses it.
- Everything keyed by `clientIP` therefore shares one bucket: `addrMaxStreams` (8, [server/tiers.go](../server/tiers.go), taken in `acquireStream` before the per-token slot, which is why the refusal names the key), `wsConnectLimit` (20/min across `/v0/stream`, `/v1/stream`, `/v1/nmea`), `httpLimit` (120/min across every HTTP GET), `keysLimit` (3/min on `/v1/keys`), and the anonymous identity `anon:<ip>` (so every anonymous viewer shares `anonConns` 2 and one rate budget).
- Unaffected: UDP ingest (`udpLimit` keys on the real datagram source), `/v1/receive` (keyed by feeder id), `publishLimit` (keyed by token sub).

## Change

In `clientIP`, when the peer is a loopback address and the request carries `X-Forwarded-For`, use the last hop of that header (the address Caddy appended) as the client address. A loopback peer is only ever Caddy in production and the developer's own browser locally, so this needs no new flag. Keep `CF-Connecting-IP` behind `TRUST_CF_HEADERS` for when Cloudflare proxying is turned back on, and keep the raw peer for any non-loopback connection so a public client cannot spoof its address by sending the header itself.

With real addresses in place, the remaining per-address limits are right for a single household but tight for a shared egress (carrier-grade NAT on cellular and Starlink, marina wifi, a VPN), where a dozen plotters can share one IPv4. Once the proxy fix is live:

- Raise `addrMaxStreams` to 32 so a dozen plotters behind one address all fit while minting many tokens stays pointless, and name the cap in the refusal: `concurrent streams per address exceeded` for the address ceiling and for anonymous streams (their key is the address), `concurrent connections per key exceeded` for a token's own `Conns`. `/v0/stream` keeps aisstream's `concurrent connections per user exceeded` for client compatibility.
- Key `wsConnectLimit` by token sub when a valid token is presented on `/v1/stream` and `/v1/nmea`, by address otherwise (`/v0/stream` receives its token after the handshake), so one neighbour cannot lock a token out of reconnecting.
- Raise `keysPerMinute` to 10 so a first install behind a shared address is not refused because a neighbour minted a token a minute ago; the plugin caches its token so it mints once.
- Update [docs/limits.md](../docs/limits.md) to match.

## Checks

- Unit test for `clientIP`: peer `127.0.0.1:1234` with `X-Forwarded-For: 203.0.113.9` returns `203.0.113.9`; with `X-Forwarded-For: 10.0.0.1, 203.0.113.9` returns `203.0.113.9`; peer `198.51.100.7:1234` with the same header returns `198.51.100.7`; loopback peer without the header returns `127.0.0.1`.
- After deploy: more than eight concurrent streams from distinct addresses all connect; a ninth stream from one address is refused; `/v1/stations` keeps answering while many viewers are open.
- Unit tests for the second part: 9 distinct personal tokens from one address all acquire; a 3rd stream from one token is refused with the per-key message; 3 anonymous streams from one address: the 3rd is refused with the per-address message; a token's reconnects are not counted against another client's address budget.
