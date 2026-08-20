# aiscast API

Base: `https://ais.openwaters.io` (WebSocket: `wss://`). All responses are JSON; CORS is open (`Access-Control-Allow-Origin: *`) on the HTTP endpoints. Times are UTC. Bounding boxes in `/v1` are `[minLat, minLon, maxLat, maxLon]`; in `/v0` they follow aisstream.io's `[[lat, lon], [lat, lon]]` corner pairs.

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /v0/stream` (WebSocket) | token as `APIKey` | aisstream.io-compatible stream |
| `GET /v1/stream` (WebSocket) | none to subscribe; feeder/peer token to publish | native event stream, both directions |
| `GET /v1/vessels` | none | current positions as GeoJSON |
| `GET /v1/stations` | none | sources being heard |
| `POST /v1/keys` | none | mint a personal token for a device key |
| `POST /v1/receive` | feeder token | AIS-catcher style HTTP ingest |
| UDP `:10110` | none | raw NMEA ingest |
| `GET /health` | none | 503 when an open-feed upstream is silent |

## Tokens

Credentials are Ed25519-signed claim tokens, `ak1.<base64url claims>.<base64url signature>`, about 200 characters. Claims:

| Claim | Meaning |
|---|---|
| `sub` | who: a station name, partner name, or `ed25519:<pubkey>` / `mmsi:<n>` for a device |
| `role` | `personal` (subscribe, small limits), `feeder` (publish), `peer` (publish + subscribe), `partner` (subscribe), `admin` (everything) |
| `exp`, `iat` | unix seconds |
| `bbox` | optional list of `[minLat,minLon,maxLat,maxLon]`; every subscription box must fit inside one of them |
| `cidr` | optional list of source CIDRs/IPs the token may be used from |
| `conns` | optional cap on concurrent WebSockets for this `sub` |

Where to put it: aisstream `APIKey` field, `Authorization: Bearer <token>`, HTTP Basic password (`anything:<token>`, which is what AIS-catcher's `USERPWD` sends), or `?key=<token>` on the URL. A token that is present but invalid, expired, revoked, used outside its `cidr`, or lacking the role for the action is refused; it never degrades to anonymous access.

**Personal tokens** are self-serve:

```
POST /v1/keys
{"pubkey": "<base64url-encoded Ed25519 public key, 32 bytes>"}

200 {"token": "ak1....", "claims": {"kid": "...", "sub": "ed25519:<pubkey>", "role": "personal", "exp": ..., "iat": ..., "conns": 2}}
```

30 days, two concurrent connections, no bbox limit; request a new one before it expires. 10 requests per minute per IP. `501` if aiscast has no personal issuer configured. The token is a bearer token naming your key; there is no proof-of-possession handshake yet. Feeder, peer, partner, and admin tokens are minted offline by the operator.

## `/v0/stream`: aisstream.io compatibility

Wire-compatible with aisstream.io: change the hostname and use an aiscast token as the API key. Frozen; nothing here deviates from aisstream.io.

Connect to `wss://ais.openwaters.io/v0/stream` and send one subscribe message within 3 seconds:

```json
{"APIKey": "ak1....", "BoundingBoxes": [[[58.5, 9.5], [60.5, 11.5]]], "FiltersShipMMSI": ["257090090"], "FilterMessageTypes": ["PositionReport", "ShipStaticData"]}
```

- Keys are case-insensitive (`APIKey`, `Apikey`, `apikey`). `APIKey` and `BoundingBoxes` are required.
- Each box is two `[lat, lon]` corners in any order; several boxes are OR-ed. Latitude −90…90, longitude −180…180.
- `FiltersShipMMSI`: up to 50 nine-digit strings. `FilterMessageTypes`: any of the 24 aisstream type names; duplicates are rejected.
- Sending another subscribe message replaces the subscription.

Each frame is one decoded message:

```json
{"Message": {"PositionReport": {"Cog": 36.7, "Latitude": 49.47557666666667, "Longitude": 0.13138, "MessageID": 1, "NavigationalStatus": 0, "Sog": 0, "TrueHeading": 511, "UserID": 227006760, "Valid": true, "...": "..."}},
 "MessageType": "PositionReport",
 "MetaData": {"MMSI": 227006760, "MMSI_String": 227006760, "ShipName": "", "latitude": 49.47557666666667, "longitude": 0.13138, "time_utc": "2026-08-20 15:21:32.794168 +0000 UTC"}}
```

`Message` is keyed by the type name and holds the decoded fields with go-ais naming (`UserID` is the MMSI, `Valid`, sentinel values such as `TrueHeading: 511`, `Cog: 360`, `Sog: 102.3`). `MetaData.ShipName` is the cached, untrimmed name; `MetaData.latitude/longitude` come from the last known position, which is how positionless messages (static data, types 5 and 24) are routed to your bounding box. `time_utc` is Go's `time.Time.String()` format. Messages with no known position are not delivered.

Error frames close the connection: `{"error": "Api Key Is Not Valid"}`, `{"error": "Subscription Object Is Malformed"}`, `{"error": "Bounding Box Not Allowed For This Key"}` (outside the token's `bbox`), `{"error": "concurrent connections per user exceeded"}` (token's `conns`). A client that cannot keep up is disconnected with close code 1008 "client too slow". Connects are limited to 60 per minute per IP.

## `/v1/stream`: native stream, both directions

Connect to `wss://ais.openwaters.io/v1/stream`, optionally with `?key=<token>` or `Authorization: Bearer`. Frames are JSON text.

Client → server:

```json
{"type": "subscribe", "bbox": [[41.2, -71.2, 42.0, -70.0], [58.5, 9.5, 60.5, 11.5]]}
{"type": "publish", "nmea": ["!AIVDM,1,1,,A,13HOI:0P0000VOHLCnHQKwvL05Ip,0*23", "\\s:st1,c:1787234980*03\\!AIVDM,..."]}
```

- `subscribe`: `bbox` is a list of boxes; an empty list or no `bbox` means everything. Re-sending replaces the subscription. Nothing is sent until the first subscribe. If the token carries `bbox`, every requested box must fit inside; otherwise `{"type":"error","error":"bbox not allowed for this key"}` and the subscription is unchanged.
- `publish`: needs a `feeder`, `peer`, or `admin` token. Sentences are ingested exactly like UDP input (TAG blocks honoured, multipart reassembled per sender, deduplicated) with `source: v1:<sub>`. Without a publish role: `{"type":"error","error":"publish requires a feeder or peer token"}`.

aiscast → client, one frame per decoded message after deduplication:

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

- `id`: hash of the payload and channel; the same transmission heard by several stations produces one event.
- `time`: canonical time: the source's timestamp when it is within 30 s of our receive time, else our receive time.
- `source`: `kystverket`, `digitraffic`, `aishub`, `aisstream`, `http:<station>`, `udp:<hash>`, `mmsi:<n>` (a UDP sender identified by its own AIVDO), `v1:<sub>`. `station` refines it (Kystverket base station id). `channel` is `A`/`B`, or empty for synthesized events.
- `nmea`: the sentences as received, or a re-encoded `!AIVDM` for synthesized events.
- `lat`/`lon`: the vessel's last known position from the cache (present for static messages too); absent until a position has been heard.
- `msg_type`: aisstream type name; `message`: go-ais decoded struct.
- `synthesized`: `true` when the message was rebuilt from a non-NMEA source (Digitraffic JSON, AISaiscast rows, aisstream envelopes).

Other frames: `{"type":"error","error":"invalid token"}` followed by close 1008 for a bad token; `{"type":"error","error":"bad frame"}` / `"unknown type"` for malformed input; `"concurrent connections per key exceeded"` then close. Frames in are limited to 256 KB. Slow clients are closed with 1008 "client too slow".

## `GET /v1/vessels`

`GET /v1/vessels?bbox=minLat,minLon,maxLat,maxLon` (no `bbox` = all). GeoJSON `FeatureCollection`, one `Point` per vessel with a known position, seen within the last 30 minutes:

```json
{"type": "Feature", "id": 368168720, "geometry": {"type": "Point", "coordinates": [-70.63165, 41.680075]},
 "properties": {"mmsi": 368168720, "kind": "vessel", "name": "CERULEAN", "type": 36, "cog": 237.4, "sog": 0, "heading": 237, "nav_status": 0,
                "seen": "2026-08-20T19:12:31Z", "source": "udp:84a377dcf41b", "station": "udp:84a377dcf41b", "msg_type": "StaticDataReport"}}
```

`kind` is `vessel`, `aton`, `base`, or `sar`; `type` is the AIS ship/cargo code (or AtoN type); `cog`, `sog`, `heading`, `nav_status`, `name`, `type` are present only when known (AIS "not available" sentinels are omitted rather than reported as 360/102.3/511/15). `seen`, `source`, `station`, `msg_type` describe the last message that updated the vessel.

## `GET /v1/stations`

Every source seen since aiscast started:

```json
[{"source": "digitraffic", "events": 334, "last_age_s": 0},
 {"source": "kystverket", "events": 220, "last_age_s": 0},
 {"source": "udp:84a377dcf41b", "events": 64, "last_age_s": 1}]
```

`events` counts decoded, deduplicated messages credited to the source; `last_age_s` is seconds since the last one. Volunteer UDP stations appear as a keyed hash, never an address.

## `POST /v1/receive`

AIS-catcher's HTTP output: `AIS-catcher -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`. Body is either AIS-catcher's `jsonaiscatcher` envelope (`{"msgs":[{"nmea":["!AIVDM,..."],"rxtime":"20260820111900","channel":"A"}, ...]}`) or plain newline-separated NMEA; `Content-Encoding: gzip` is accepted; 1 MB limit before and after decompression. Needs a `feeder` (or `peer`/`admin`) token; the token's `sub` becomes the station (`source: http:<sub>`). `rxtime` is used as the message time when within 30 s of arrival. 600 posts per minute per station. Responses: `200` empty, `401` with a reason, `413`, `429`.

## UDP `ais.openwaters.io:10110`

Datagrams of newline-separated NMEA (`!AIVDM`/`!AIVDO`, `!BSVDM`…, TAG blocks allowed, lines ≤ 4 KB). No authentication. The sender is identified as `udp:<keyed hash of address>`; if it sends `!AIVDO`, as `mmsi:<own MMSI>` from then on. Anything received is forwarded to AISaiscast under aiscast's reciprocal feed.

## Tolerances and dedupe

Accepted on every input: NMEA 4.10 TAG blocks (`s:` station, `c:` time in s/ms/µs, `g:` grouping), any talker (`AI`, `BS`, `AB`…), channels `A`/`B`/`1`/`2`, fixed-length messages up to 5 bits over length, USCG trailing fields after the checksum (`…*5B,s36310,d-081`). Multipart messages are reassembled per sender. Duplicates are dropped when the same payload on the same channel arrives within 10 s of canonical time, whichever station heard it first wins. Out-of-skew source timestamps fall back to receive time.

## Limits

| What | Limit |
|---|---|
| WebSocket connects | 60 per minute per IP (`/v0` and `/v1`) |
| Concurrent WebSockets per token | the token's `conns` claim (2 for personal tokens) |
| Subscribe deadline on `/v0` | 3 s |
| `/v1/receive` | 600 posts/min per station, 1 MB per post |
| `/v1/keys` | 10 per minute per IP |
| Per-client send queue | 1024 events; overflow disconnects the client |

## Health

`GET /health` returns `ok` or `503` with the names of open-feed upstreams (Kystverket, Digitraffic) silent for more than two minutes. AISaiscast and aisstream are best-effort and never affect it.
