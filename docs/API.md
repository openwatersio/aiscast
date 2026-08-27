
JSON responses are compressed (`zstd` or `gzip`) when the request sends `Accept-Encoding`; the full `/v1/vessels` snapshot is ~16 MB plain and ~2.5 MB gzipped, so send it.
# aiscast API

Base: `https://ais.openwaters.io` (WebSocket: `wss://`). All responses are JSON; CORS is open (`Access-Control-Allow-Origin: *`) on the HTTP endpoints. Times are UTC. Bounding boxes in `/v1` are `[minLat, minLon, maxLat, maxLon]`; in `/v0` they follow aisstream.io's `[[lat, lon], [lat, lon]]` corner pairs.

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /v0/stream` (WebSocket) | token as `APIKey` | aisstream.io-compatible stream |
| `GET /v1/stream` (WebSocket) | none to subscribe (anonymous tier); any token to publish | native event stream, both directions |
| `GET /v1/stream` (SSE) | none to subscribe (anonymous tier) | the same events over Server-Sent Events, subscribe only |
| `GET /v1/vessels` | none | current positions as GeoJSON |
| `GET /v1/stations`, `GET /v1/stations/{id}` | none | stations being heard, with per-station statistics |
| `GET /v1/stats` | none | usage summary: stations, vessels per source, event rate, streams and API requests |
| `GET /v1/nmea` (WebSocket) | feeder tier (earned or minted), peer, partner, admin | deduplicated raw NMEA back to feeders |
| `POST /v1/keys` | none | mint a personal token for a device key |
| `POST /v1/receive` | personal, feeder, peer, or admin token | AIS-catcher style HTTP ingest |
| UDP `:10110` | none | raw NMEA ingest |
| `GET /health` | none | 503 when an open-feed upstream is silent or nothing is reaching subscribers |

## Tokens

Credentials are Ed25519-signed claim tokens, `ak1.<base64url claims>.<base64url signature>`, about 200 characters. Claims:

| Claim | Meaning |
|---|---|
| `sub` | who: a station name, partner name, or `ed25519:<pubkey>` / `mmsi:<n>` for a device |
| `role` | `personal` (subscribe + publish as the device key, small limits), `feeder` (publish), `peer` (publish + subscribe), `partner` (subscribe), `admin` (everything) |
| `exp`, `iat` | unix seconds; `exp` is optional and absent on personal tokens (they never expire; misuse is revoked) |
| `bbox` | optional list of `[minLat,minLon,maxLat,maxLon]`; every subscription box must fit inside one of them |
| `cidr` | optional list of source CIDRs/IPs the token may be used from |
| `conns` | optional cap on concurrent WebSockets for this `sub` (on top of 8 per network address for everyone) |
| `rate` | optional cap on messages per second per connection; excess events are thinned (skipped), the connection stays up |
| `area` | optional cap on the total subscribed bounding-box area in square degrees (a 20°×20° box is 400); does not apply to MMSI-filtered subscriptions. A negative value (`-1`) means no bounding-box subscriptions at all: the token can only follow vessels by MMSI (`/v0` with `FiltersShipMMSI`, `/v1` with `mmsi` and no `bbox`), and `/v1/nmea` is refused |
| `mmsis` | optional cap on vessels followed by MMSI per subscription |

Where to put it: aisstream `APIKey` field, `Authorization: Bearer <token>`, HTTP Basic password (`anything:<token>`, which is what AIS-catcher's `USERPWD` sends), or `?key=<token>` on the URL. A token that is present but invalid, expired, revoked, used outside its `cidr`, or lacking the role for the action is refused; it never degrades to anonymous access.

**Personal tokens** are self-serve:

```
POST /v1/keys
{"pubkey": "<base64url-encoded Ed25519 public key, 32 bytes>", "bind_ip": false}

200 {"token": "ak1....", "claims": {"kid": "...", "sub": "ed25519:<pubkey>", "role": "personal", "iat": ..., "conns": 2, "rate": 50, "area": 400}}
```

No expiry. Two concurrent connections, 50 messages/s per connection, 400 square degrees of subscribed area; publishing allowed (receptions are credited to `v1:ed25519:<pubkey>` / `http:ed25519:<pubkey>`, an identity label with no trust attached; the Signal K plugin contributes this way). `bind_ip: true` adds the requester's address as a `cidr` claim so a UDP station at that address counts as this token's (the token then only works from that address). 10 requests per minute per address: one token is all a client needs. `501` if aiscast has no personal issuer configured. The token is a bearer token naming your key; there is no proof-of-possession handshake yet. Clients that cannot POST (a sandboxed plugin with GET-only network access) can mint the same token in-band on `/v1/stream` with a `register` frame. Feeder, peer, partner, and admin tokens are minted offline by the operator.

**Tiers.** Limits come from the token's claims; the tiers in [limits.md](limits.md) are the defaults. A personal token whose stations (`v1:<sub>`, `http:<sub>`, and any bound UDP address) delivered at least 1,000 events in the last 24 hours is treated as **feeder** on each connection it makes: 5 connections, 200 messages/s, no area cap, and access to `/v1/nmea`. Nothing to request; it lapses when the station stops and returns when it resumes. Without a token, `/v1/stream` and `/v1/nmea`'s HTTP siblings use the **anonymous** tier keyed by address: 2 connections, 20 messages/s, 100 square degrees, subscribe only.

## `/v0/stream`: aisstream.io compatibility

Wire-compatible with aisstream.io: change the hostname and use an aiscast token as the API key. Frozen; nothing here deviates from aisstream.io.

Connect to `wss://ais.openwaters.io/v0/stream` and send one subscribe message within 3 seconds:

```json
{"APIKey": "ak1....", "BoundingBoxes": [[[58.5, 9.5], [60.5, 11.5]]], "FiltersShipMMSI": ["257090090"], "FilterMessageTypes": ["PositionReport", "ShipStaticData"]}
```

- Keys are case-insensitive (`APIKey`, `Apikey`, `apikey`). `APIKey` and `BoundingBoxes` are required.
- Each box is two `[lat, lon]` corners in any order; several boxes are OR-ed. Latitude −90…90, longitude −180…180.
- `FiltersShipMMSI`: up to 50 nine-digit strings (more if the token's `mmsis` claim allows); when present, the traffic is bounded by the list, so the token's `area` cap does not apply to the boxes (a world box plus an MMSI list is fine on a personal token); the tier's MMSI cap does (`Too Many MMSI Filters For This Key`). `FilterMessageTypes`: any of the 24 aisstream type names; duplicates are rejected.
- Sending another subscribe message replaces the subscription.

Each frame is one decoded message:

```json
{"Message": {"PositionReport": {"Cog": 36.7, "Latitude": 49.47557666666667, "Longitude": 0.13138, "MessageID": 1, "NavigationalStatus": 0, "Sog": 0, "TrueHeading": 511, "UserID": 227006760, "Valid": true, "...": "..."}},
 "MessageType": "PositionReport",
 "MetaData": {"MMSI": 227006760, "MMSI_String": 227006760, "ShipName": "", "latitude": 49.47557666666667, "longitude": 0.13138, "time_utc": "2026-08-20 15:21:32.794168 +0000 UTC"}}
```

`Message` is keyed by the type name and holds the decoded fields with go-ais naming (`UserID` is the MMSI, `Valid`, sentinel values such as `TrueHeading: 511`, `Cog: 360`, `Sog: 102.3`). `MetaData.ShipName` is the cached, untrimmed name; `MetaData.latitude/longitude` come from the last known position, which is how positionless messages (static data, types 5 and 24) are routed to your bounding box. `time_utc` is Go's `time.Time.String()` format. Messages with no known position are not delivered.

Error frames close the connection: `{"error": "Api Key Is Not Valid"}`, `{"error": "Subscription Object Is Malformed"}`, `{"error": "Bounding Box Not Allowed For This Key"}` (outside the token's `bbox`), `{"error": "concurrent connections per user exceeded"}` (token's `conns`). A client that cannot keep up is disconnected with close code 1008 "client too slow". Connects are limited to 20 per minute per address.

## `/v1/stream`: native stream, both directions

Connect to `wss://ais.openwaters.io/v1/stream`, optionally with `?key=<token>` or `Authorization: Bearer`. Frames are JSON text.

Client → server:

```json
{"type": "subscribe", "bbox": [[41.2, -71.2, 42.0, -70.0], [58.5, 9.5, 60.5, 11.5]]}
{"type": "subscribe", "mmsi": [368168720, 257090090]}
{"type": "subscribe", "bbox": [[41.2, -71.2, 42.0, -70.0]], "mmsi": [368168720]}
{"type": "subscribe", "bbox": [[41.2, -71.2, 42.0, -70.0]], "snapshot": true}
{"type": "publish", "nmea": ["!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23", "\\s:st1,c:1787234980*03\\!AIVDM,..."]}
{"type": "publish", "replay": true, "nmea": ["\\c:1787234980123*2A\\!AIVDM,..."]}
{"type": "unsubscribe"}
{"type": "register", "pubkey": "<base64url-encoded Ed25519 public key, 32 bytes>", "bind_ip": false}
```

- `subscribe`: `bbox` is a list of boxes, `mmsi` a list of vessels to follow wherever they are; give either or both (an event matches if it is inside a box OR from a listed MMSI; MMSI matches include positionless messages such as static data). Neither means everything (only for tokens without an `area` cap); a token with a negative `area` must send `mmsi` and no `bbox`. The `area` cap applies to the boxes, the tier's MMSI cap to the list (anonymous 10, personal 50, feeder 200; `{"type":"error","error":"too many mmsi for this key"}` otherwise). Re-sending replaces the subscription. No events are sent until the first subscribe. Without a token the socket has the anonymous tier: 2 concurrent connections per address, 20 messages/s, 100 square degrees, subscribe only. If the token carries `bbox`, every requested box must fit inside, and the total area must fit `area`; otherwise `{"type":"error","error":"bbox not allowed for this key"}` and the subscription is unchanged.
- `snapshot`: with `true`, the subscription starts by replaying the last known messages for every vessel it already matches (positions held up to 30 minutes, and a static message when a name or ship type is known). Replayed frames carry their original `time`; live events can arrive interleaved with the replay, and a vessel can appear in both, but nothing falls in the gap between them. Most replayed frames are the messages as received; when the original is no longer held (the vessel survived a server restart, or its static data arrived as type 24 halves) the frame is a reconstruction from the vessel cache, marked `synthesized: true` with no `nmea` and no `id`. The replay is not counted against the connection's messages/s rate.
- `unsubscribe`: stop receiving events; the socket stays open for publishing.
- `publish`: needs a token (`personal`, `feeder`, `peer`, or `admin`). Sentences are ingested exactly like UDP input (TAG blocks honoured, multipart reassembled per sender, deduplicated) with `source: v1:<sub>`, and every frame is answered in order with `{"type":"ack","n":<sentences accepted>}`. `replay: true` marks an offline backlog: sentences whose TAG `c:` time is more than 60 s old are then archived and counted, not emitted live and not folded into the vessel cache. At most 1000 sentences per frame and 6000 per minute per key; the rest are dropped (and not counted in `n`). Without a token: `{"type":"error","error":"publish requires a token"}`.
- `register`: the in-band twin of `POST /v1/keys`, for clients that cannot POST. On an anonymous socket only (`"already registered"` otherwise), it mints a personal token with the same semantics, limits, and `bind_ip` behaviour as the HTTP endpoint, answers `{"type":"key","token":"ak1....","claims":{...}}`, and upgrades this connection to the token's tier in place — no reconnect — confirmed by a fresh `welcome` with the new limits. A live subscription is kept. Errors are non-fatal error frames: `"personal tokens not enabled"`, `"rate limited"` (shares the `/v1/keys` per-address limit), `"pubkey must be a base64url ed25519 public key"`, or a concurrent-connection message when the token's slots are already taken (the socket then stays anonymous).

aiscast → client. The first frame on every accepted socket is a `welcome` advertising the limits in effect for this connection, so a client can size its bounding boxes, pace itself, and back off reconnects without discovering the limits by error (a refused socket — bad token, connection caps — gets a single `error` frame instead, never a welcome):

```json
{"type": "welcome", "sub": "ed25519:abc...", "role": "personal", "feeder": false,
 "limits": {"conns": 2, "rate": 50, "area": 400, "mmsis": 50,
            "publish": true, "publish_per_min": 6000, "publish_frame": 1000, "connects_per_min": 20}}
```

`limits` uses the claim semantics: an absent value is unlimited, a negative `area` means MMSI-only subscriptions, and `bbox` (when present) lists the boxes subscriptions must fit inside. `publish_per_min`/`publish_frame` appear only when the socket may publish. On an anonymous socket, `conns` and `connects_per_min` are shared by every client behind the same network address, not per client. `feeder: true` marks a personal token currently earning the feeder tier; the limits reflect the tier at connect time (or at the latest in-band `register`), so a tier earned mid-connection shows up on the next connect. Then one frame per decoded message after deduplication:

```json
{"type": "event",
 "id": "15f3d25469c1de49dbcb36baea34eed6",
 "time": "2026-08-20T15:25:54.342871Z",
 "source": "kystverket", "station": "kystverket/2573010", "channel": "A",
 "nmea": ["\\s:2573010,c:1787234980*03\\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43"],
 "mmsi": 257090090, "msg_type": "PositionReport",
 "lat": 59.88693333333333, "lon": 10.749376666666667,
 "message": {"MessageID": 1, "RepeatIndicator": 0, "UserID": 257090090, "Valid": true, "NavigationalStatus": 5, "...": "..."},
 "synthesized": false}
```

- `id`: content id, not an event id: hex of the first 16 bytes of SHA-256 over the decoded payload bits (one byte per bit, fill bits dropped) followed by the channel letter. Identical payloads share an id, whether that is the same transmission heard late by a second station or a static message (Type 5/24) rebroadcast unchanged every few minutes. Use `(id, time)` as the event key; dedupe on `id` alone drops the rebroadcasts. Absent on snapshot reconstructions.
- `time`: canonical time: the source's timestamp when it is within 30 s of our receive time, else our receive time.
- `source`: `kystverket`, `digitraffic`, `aishub`, `aisstream`, `http:<station>`, `udp:<hash>`, `mmsi:<n>` (a UDP sender identified by its own AIVDO), `v1:<sub>`. `station` refines it (Kystverket base station id). `channel` is `A`/`B`, or empty for events rebuilt from a non-NMEA source.
- `nmea`: the sentences as received, or a re-encoded `!AIVDM` for events rebuilt from a non-NMEA source (self-reported `s:self` events keep their as-received `!AIVDO`). Absent on snapshot reconstructions, which never had sentences.
- `lat`/`lon`: the vessel's last known position from the cache (present for static messages too); absent until a position has been heard.
- `msg_type`: aisstream type name; `message`: go-ais decoded struct.
- `synthesized`: `true` when the message was not heard over VHF: rebuilt from a non-NMEA source (Digitraffic JSON, AISHub rows, aisstream envelopes), an own-ship report a vessel built from its GPS (`signalk-aiscast` with TAG `s:self`, station `v1:<sub>/self`), or a snapshot reconstruction from the vessel cache. Never fed to AISHub.

Other frames: `{"type":"error","error":"invalid token"}` followed by close 1008 for a bad token; `{"type":"error","error":"bad frame"}` / `"unknown type"` for malformed input; `"concurrent connections per key exceeded"` then close. Frames in are limited to 256 KB. Slow clients are closed with 1008 "client too slow".

### Server-Sent Events

A `GET` to the same URL without a WebSocket upgrade returns `text/event-stream` instead: the same events, the same tokens, the same tier limits, for clients that would rather not carry a WebSocket library. It is subscribe-only — there is no channel to publish or `register` on, so `limits.publish` is always `false` however capable the token is.

```
curl -N 'https://ais.openwaters.io/v1/stream?bbox=41.2,-71.2,42.0,-70.0&mmsi=368168720&snapshot=1'
```

The subscription is fixed at connect time from the query string: `bbox=minLat,minLon,maxLat,maxLon` (repeatable, ORed), `mmsi=<mmsi>,<mmsi>,...`, `snapshot=1`, and `key=<token>` (or `Authorization: Bearer`). Neither `bbox` nor `mmsi` means everything, for tokens without an `area` cap. The claim checks are the ones the `subscribe` frame applies, and `snapshot=1` behaves as it does on the socket: live events interleave with the replay, a vessel can appear in both, and nothing falls in the gap between them.

Each frame is one `data:` line carrying the same JSON the socket sends — a `welcome` first, then `event` frames, distinguished by their `type` field rather than an SSE event name, so a browser's `EventSource.onmessage` sees all of them:

```
data: {"type":"welcome","sub":"anon:203.0.113.4","role":"anonymous","limits":{"conns":2,"rate":20,"area":100,"mmsis":10,"publish":false,"connects_per_min":20}}

data: {"type":"event","id":"15f3d254...","time":"2026-08-20T15:25:54.342871Z","mmsi":257090090,"...":"..."}

```

A `:` comment arrives every 30 seconds while the stream is idle, so a proxy does not mistake a quiet subscription for a dead one. A client that cannot keep up receives `{"type":"error","error":"client too slow"}` and the stream ends, as close 1008 does on the socket.

Anything wrong with the request is an HTTP status, not a frame: `400` for a malformed `bbox` or `mmsi` or a subscription outside the token's claims (unlike `/v1/vessels`, an unparseable MMSI is refused rather than dropped — on a long-lived stream a typo would be indistinguishable from a subscription that never matches), `401` for a bad token, `405` for a method other than `GET`, `429` for the connect limit or the concurrent-connection caps.

There is no `Last-Event-ID` resumption; `snapshot=1` rebuilds current state on reconnect.

## `GET /v1/vessels`

`GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon` and/or `?mmsi=368168720,257090090` (either, both ORed, or neither for all). GeoJSON `FeatureCollection`, one `Point` per vessel with a known position, seen within the last 30 minutes:

```json
{"type": "Feature", "id": 368168720, "geometry": {"type": "Point", "coordinates": [-70.63165, 41.680075]},
 "properties": {"mmsi": 368168720, "kind": "vessel", "name": "CERULEAN", "type": 36, "cog": 237.4, "sog": 0, "heading": 237, "nav_status": 0,
                "seen": "2026-08-20T19:12:31Z", "source": "udp:84a377dcf41b", "station": "udp:84a377dcf41b", "msg_type": "StaticDataReport"}}
```

`kind` is `vessel`, `aton`, `base`, or `sar`; `type` is the AIS ship/cargo code (or AtoN type); `cog`, `sog`, `heading`, `nav_status`, `name`, `type` are present only when known (AIS "not available" sentinels are omitted rather than reported as 360/102.3/511/15). `seen`, `source`, `station`, `msg_type` describe the last message that updated the vessel.

## `GET /v1/stations`

Every station heard since the server started:

```json
[{"station": "kystverket/2573010", "source": "kystverket", "events": {"last_24h": 41812, "last_7d": 280112}, "duplicates": 40, "vessels": 61, "positions": 1500,
  "first_seen": "2026-08-20T16:57:47Z", "last_seen": "2026-08-21T03:10:32Z", "last_age_s": 2,
  "bbox": [59.41, 10.31, 59.93, 10.78]},
 {"station": "udp:84a377dcf41b", "source": "udp:84a377dcf41b", "events": {"last_24h": 1064, "last_7d": 6210}, "duplicates": 2, "vessels": 53, "positions": 60, "...": "..."}]
```

`events` counts decoded messages credited to the station (first to deliver them) over the last 24 hours and 7 days, in hourly buckets that survive restarts; `duplicates` and `positions` count since the station was first heard after the last restart; `vessels` is distinct MMSIs heard in the last 30 minutes; `bbox` is the extent of positions heard (a rough coverage footprint). Volunteer UDP stations appear as a keyed hash, never an address.

`GET /v1/stations/{id}` returns `{"station": {...same row...}, "vessels": <GeoJSON FeatureCollection of the vessels this station last updated>}`; `404` for an unknown id. The viewer shows it at `?station=<id>`.

## `GET /v1/stats`

A one-shot usage summary, for status pages and tracking growth. Counts are rolling windows over the last 24 hours and 7 days (hourly buckets, kept across restarts), never since-start totals:

```json
{"time": "2026-08-21T13:40:12Z",
 "stations": {"total": 14, "active": 11, "by_source": {"kystverket": 1, "digitraffic": 1, "udp": 7, "http": 3, "v1": 2}},
 "vessels": {"total": 4812, "with_position": 4790, "by_kind": {"vessel": 4701, "aton": 88, "base": 19, "sar": 4}},
 "events": {"per_second": 212.4, "last_24h": 18230411, "last_7d": 121004312, "duplicates": {"last_24h": 2210560, "last_7d": 15320011}},
 "clients": {"streams": 9, "streams_opened": {"last_24h": 410, "last_7d": 2822}, "requests": {"last_24h": 28310, "last_7d": 190412}},
 "sources": {"kystverket": {"events": {"last_24h": 9120033, "last_7d": 61233190}, "last_age_s": 0, "vessels": 2411, "vessels_exclusive": 180},
             "v1": {"events": {"last_24h": 40211, "last_7d": 281002}, "last_age_s": 3, "vessels": 61, "vessels_exclusive": 2}, "...": {}}}
```

`stations.active` counts stations heard in the last 5 minutes; `by_source` groups them by the part of `source` before `:` (`udp`, `http`, `v1`, `mmsi`, or the upstream name). `vessels` covers the 30-minute cache. `events.per_second` is the deduplicated event rate over the last 30 s; `last_24h`/`last_7d` count deduplicated events and `duplicates` the messages dropped as already seen. `clients.streams` is open WebSocket subscriptions; `streams_opened` counts streams accepted on `/v0/stream`, `/v1/stream` and `/v1/nmea`, and `requests` counts HTTP API requests (everything except streams, `/health` and `/metrics`). `sources` is keyed by source kind: each upstream by name, and all API publishers as `v1`, all UDP senders as `udp`, all HTTP feeders as `http`, UDP senders identified by their own `!AIVDO` as `mmsi`. Per kind: events over the same windows, seconds since any member last produced an event, `vessels` (distinct MMSIs its stations heard in the last 30 minutes, counting messages another kind delivered first) and `vessels_exclusive` (those no other kind heard in that window).

## `GET /v1/nmea`: raw sentences back to feeders

WebSocket. Token with role `feeder`, `peer`, `partner`, or `admin`, or a personal token that has earned the feeder tier by contributing (`?key=` or `Authorization: Bearer`); anonymous is refused. Low-trust (UDP) events that no trusted source has corroborated are not included. Optional `?bbox=minLat,minLon,maxLat,maxLon` (repeatable) filters by the vessel's last known position; the token's `bbox`, `area`, `conns`, and `rate` claims apply.

One text frame per deduplicated message, sentences joined by CRLF, each carrying a NMEA 4.10 TAG block with the station (`s:`, truncated to 15 characters), the canonical time (`c:`, unix seconds), and the source's license tag (`t:`):

```
\s:2573010,c:1787234980,t:NLOD-2.0*2E\!BSVDM,1,1,,B,13noH:00000H@P@RSPEakGK@0D33,0*43
```

Synthesized messages (Digitraffic, AISHub, aisstream) are delivered as re-encoded `!AIVDM` with `t:` naming their terms; filter on `t:` if you only want receiver data.

## `POST /v1/receive`

AIS-catcher's HTTP output: `AIS-catcher -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`. Body is either AIS-catcher's `jsonaiscatcher` envelope (`{"msgs":[{"nmea":["!AIVDM,..."],"rxtime":"20260820111900","channel":"A"}, ...]}`) or plain newline-separated NMEA; `Content-Encoding: gzip` is accepted; 1 MB limit before and after decompression. Needs a token that may publish (`personal`, `feeder`, `peer`, `admin`); the token's `sub` becomes the station (`source: http:<sub>`). `rxtime` is used as the message time when within 30 s of arrival. 600 posts per minute per station. Responses: `200` empty, `401` with a reason, `413`, `429`.

## UDP `ais.openwaters.io:10110`

Datagrams of newline-separated NMEA (`!AIVDM`/`!AIVDO`, `!BSVDM`…, TAG blocks allowed, lines ≤ 4 KB). No authentication, 500 sentences per second per source address. The sender is identified as `udp:<keyed hash of address>`; if it sends `!AIVDO`, as `mmsi:<own MMSI>` from then on; if a personal token was minted with `bind_ip` from the same address, the station counts toward that token's feeder tier.

UDP is the lowest-trust path: events from it are shown on the map and in `/v1/stream` (with their `source`), but they never raise a vessel's trust, a position that implies more than 120 knots from the vessel's last known position is dropped (archived, not emitted), and they are forwarded to AISHub and `/v1/nmea` only while a trusted source (an open feed, an authenticated station) has heard the same vessel within the last hour. Anything received is archived with its source so a bad source can be purged.

## Tolerances and dedupe

Accepted on every input: NMEA 4.10 TAG blocks (`s:` station, `c:` time in s/ms/µs, `g:` grouping), any talker (`AI`, `BS`, `AB`…), channels `A`/`B`/`1`/`2`, fixed-length messages up to 5 bits over length, USCG trailing fields after the checksum (`…*5B,s36310,d-081`). Multipart messages are reassembled per sender. Duplicates are dropped when the same payload on the same channel arrives within 10 s of canonical time, whichever station heard it first wins. Out-of-skew source timestamps fall back to receive time.

## Limits

Defaults by tier (a minted token's claims override them); see [limits.md](limits.md) for the reasoning.

| | Anonymous | Personal | Feeder (earned or minted) | Partner / admin |
|---|---|---|---|---|
| Concurrent streams | 2 per address | 2 | 5 | as minted |
| Messages/s per stream (excess thinned) | 20 | 50 | 200 | as minted |
| Subscribed area (sq °) | 100 | 400 | unlimited | as minted |
| Vessels followed by MMSI per stream | 10 | 50 | 200 | as minted |
| `/v1/nmea` | no | no | yes | yes |
| Publish | no | 6,000 sentences/min, 1,000/frame, `/v1/receive` 600 posts/min and 1 MB | same | same |

Everyone: at most 8 concurrent streams per network address across all tokens; 20 WebSocket connects per minute per address (`/v0/stream`, `/v1/stream`, `/v1/nmea`); 120 requests per minute per address on `/v1/vessels`, `/v1/stations`, `/v1/stats`; 10 per minute per address on `/v1/keys` and in-band `register`; 500 UDP sentences/s per source address; 3 s subscribe deadline on `/v0`; a 1,024-event send queue, after which the client is disconnected.

## Health

`GET /health` returns `ok` or `503` with the reasons: open-feed upstreams (Kystverket, Digitraffic) silent for more than two minutes, or the server's own loopback `/v1/stream` subscriber receiving nothing for two minutes (ingest without delivery). AISHub and aisstream are best-effort and never affect it. Uptime history is on [status.openwaters.io](https://status.openwaters.io).
