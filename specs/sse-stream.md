# Server-Sent Events on `/v1/stream`

`/v1/stream` serves Server-Sent Events to `GET` requests that are not WebSocket upgrades. Same URL, same tokens, same tier limits, same JSON payloads as the WebSocket transport — a client parses `.type` identically on either. Only the transport differs, and with it the loss of the client-to-server channel: SSE subscribers cannot publish, register, or resubscribe.

The audience is browsers and HTTP clients that want the feed without a WebSocket library, and networks where a long-lived `GET` survives middleboxes that break upgrades.

## Dispatch

`serveV1` in [server/stream.go](../server/stream.go) branches at its first line:

- `Upgrade: websocket` (case-insensitive) → the existing WebSocket handler. This test only routes; `websocket.Accept` performs the real handshake validation and answers a malformed one with 400.
- `GET` without that header → `serveV1SSE`.
- Any other method → 405.

No new route, no change to [server/main.go](../server/main.go).

Requiring `GET` is what keeps a `POST /v1/stream` from opening an unbounded stream. Browser navigation to the URL is a `GET` and does open one; that is accepted rather than gated on `Accept: text/event-stream`, because requiring the header would break `curl -N` as a debugging tool. The cost is bounded by the concurrent-connection caps, which apply to SSE exactly as to sockets.

## Request

The subscription is fixed at connect time from the query string, since there is no channel to change it on:

```
GET /v1/stream?bbox=41.2,-71.2,42.0,-70.0&bbox=58.5,9.5,60.5,11.5&mmsi=368168720&snapshot=1
```

| Parameter | Meaning |
|---|---|
| `bbox` | `minLat,minLon,maxLat,maxLon`, repeatable; boxes are ORed. Same form as `/v1/nmea`. |
| `mmsi` | comma-separated vessels to follow wherever they are, ORed with `bbox`. Same form as `/v1/vessels`. |
| `snapshot` | `1` replays the last known events for vessels already matched, interleaved with live traffic. |
| `key` | token, as elsewhere; `Authorization: Bearer` also works. |

Neither `bbox` nor `mmsi` means everything, permitted only for tokens without an `area` cap. The parsed parameters build a `v1Sub` and reuse its `match` method unchanged.

A malformed `bbox` or `mmsi` is a 400. This diverges from `/v1/vessels`, which silently drops MMSIs it cannot parse: that is tolerable for a one-shot response the caller can eyeball, and wrong for a connection held open for hours, where a typo would present as a subscription that simply never delivers. Failing at connect time is the difference between a visible error and an afternoon of debugging silence.

## Auth and limits

Identical to the WebSocket path, using the same helpers so the two transports cannot drift apart:

- `p.socketClaims(r)` for the token, `anonymousClaims(clientIP(r))` when there is none.
- `p.limited(w, wsConnectLimit, connectKey(cl, r))` for the connect rate.
- `acquireStreamSlots(cl, ip)` for the concurrent-connection caps; an SSE stream occupies a slot exactly as a socket does.
- `cl.allowsBox`, `cl.allowsArea`, `cl.allowsMMSIs` against the requested subscription.
- `pacer{n: cl.Rate}` for the per-connection message rate; thinned events increment `p.stats.thinned` as on the socket.
- `p.subscribe()` / `p.unsubscribe(sub)` for fan-out, which already counts the stream in `/v1/stats`. `countRequests` already excludes `/v1/stream` from the API request counter.

## Response

```
Content-Type: text/event-stream
Cache-Control: no-cache
Access-Control-Allow-Origin: *
```

The body is one `data:` block per message, no `event:` name, so a browser's `EventSource.onmessage` fires for every frame and the payload's own `type` field distinguishes them:

```
data: {"type":"welcome","sub":"anon","role":"anonymous","limits":{...}}

data: {"type":"event","id":"15f3d254...","time":"2026-08-20T15:25:54.342871Z",...}

```

Payloads are the existing `v1Welcome` and `renderV1(ev)` structures marshalled unchanged. The welcome frame comes first, as on the socket. `snapshot=1` then replays `p.snapshotEvents(s)` unpaced, with the same guarantee the socket gives: a vessel may appear in both the replay and the live stream, but nothing falls in the gap between them.

Live events take priority over the replay, and the two interleave. The fan-out queue holds events at the global rate rather than at this subscription's, so a replay written as one uninterrupted burst overflows a queue nobody is draining and disconnects the client that asked for the snapshot. A replay of the whole vessel cache is megabytes, which is many seconds of global traffic on a busy feed. The socket avoids this by replaying on its reader goroutine while its writer drains; SSE writes from a single goroutine, so the replay yields to the queue between frames instead.

A slow client whose `sub.overflow` is set gets a final `{"type":"error","error":"client too slow"}` and the handler returns, mirroring the socket's close 1008.

## Write deadlines and flushing

Every write — welcome, snapshot item, event, keepalive — is preceded by `rc.SetWriteDeadline(time.Now().Add(10 * time.Second))` and followed by `rc.Flush()`, where `rc` is `http.NewResponseController(w)`.

The deadline is load-bearing, not defensive garnish. A client that stops reading fills its socket buffer, and an undeadlined write to it blocks forever: the handler never returns to its select loop, so it never observes `sub.overflow`, never sends the slow-client error, never fires a keepalive, and never releases its connection slot. The slot leaks for as long as the kernel keeps the connection alive. The deadline converts that into a write error, which ends the handler and runs its deferred releases.

Flushing is equally non-optional and does not belong to the proxy: nothing downstream can forward bytes the handler has not released.

A 30-second ticker writes an SSE comment (`:\n\n`) when otherwise idle. There is no SSE counterpart to `pingLoop` — the deadlined write is the liveness check. The resulting worst case for a vanished client is the 30-second keepalive interval plus the 10-second deadline, matching the socket's `pingEvery` plus `pingTimeout`.

## Compression

Caddy's `encode zstd gzip` in [server/deploy/Caddyfile](../server/deploy/Caddyfile) compresses the stream, as it does every other JSON endpoint. The handler sets no `Content-Encoding` of its own. A compressed HTTP response is a single stream, so the deflate window carries across events and the repeated structure of AIS JSON — field names, `"type":"event"`, source and timestamp prefixes — compresses against earlier events rather than each event standing alone.

Two properties to measure once the endpoint is live, because both fail silently:

```
# Latency: events should trickle out individually, not in bursts.
curl -N --compressed 'https://ais.openwaters.io/v1/stream?bbox=41.2,-71.2,42.0,-70.0'

# Ratio: bytes over the same window, compressed against not.
timeout 30 curl -sN 'https://ais.openwaters.io/v1/stream?bbox=41.2,-71.2,42.0,-70.0' | wc -c
timeout 30 curl -sN -H 'Accept-Encoding: gzip' 'https://ais.openwaters.io/v1/stream?bbox=41.2,-71.2,42.0,-70.0' | wc -c
```

The first catches buffering: `encode` holds back the first `minimum_length` bytes (512 by default) while deciding whether compressing is worthwhile, and an early flush needs to break it out of that. The second catches a dictionary reset: a full flush per event instead of a sync flush would drop a 500-byte event from roughly 8x to roughly 1.5x. Both are expected to pass. If the ratio disappoints, the fix is a `gzip.Writer` in the handler whose `Flush` runs before the response controller's, which makes the behavior deterministic regardless of what sits in front of the server.

## Errors

Failures before the stream opens are HTTP status codes, not SSE frames, matching `/v1/nmea`:

| Status | Cause |
|---|---|
| 400 | malformed `bbox` or `mmsi`; a subscription outside the token's `bbox`, `area`, or `mmsis` claims |
| 401 | token present but invalid, expired, revoked, or used outside its `cidr` |
| 405 | a method other than `GET` |
| 429 | connect rate limit, or concurrent-connection caps exhausted |

WHATWG specifies that a non-200 status fails an `EventSource` permanently: fire `error`, set `readyState` to `CLOSED`, no reconnect. Nothing here depends on that. Client reconnect behavior varies across non-browser `EventSource` implementations, and a client is free to reconnect in a loop whatever the status says, so the protection that matters is the per-address connect limit (`wsConnectLimit`) that every rejection already passes through. Rejections are cheap by construction: token verification and bbox parsing happen before any stream slot or fan-out subscription is acquired.

Clients wanting deterministic behavior on failure should use `fetch()` streaming, or call `.close()` from `onerror`.

## Out of scope

- **Publishing, `register`, `unsubscribe`.** SSE is one-way. Clients needing them use the WebSocket, which remains the full-featured transport.
- **`Last-Event-ID` resumption.** No replay buffer exists to resume from; `snapshot=1` covers reconnect by rebuilding current state. Revisit if clients report losing events across reconnects.
- **A `retry:` hint.** `EventSource` already backs off around 3 seconds.
- **Named `event:` types.** The payload carries `type`.

## Files

| File | Change |
|---|---|
| [server/stream.go](../server/stream.go) | dispatch in `serveV1`; `serveV1SSE`, roughly 70 lines |
| [server/hub_test.go](../server/hub_test.go) | `TestV1SSE` and the cases below |
| [docs/API.md](../docs/API.md) | endpoint-table row and a subsection under `/v1/stream` |

## Test

In [server/hub_test.go](../server/hub_test.go), alongside `TestV1SnapshotSubscribe`, against an `httptest` server:

| Case | Assertion |
|---|---|
| live delivery | connect, then ingest a sentence; a `welcome` arrives, then an `event` carrying the same JSON the WebSocket test asserts |
| snapshot replay | ingest a sentence **before** connecting, then `GET` with `snapshot=1`; the vessel is replayed after the `welcome` |
| replay under load | step the handler frame by frame through a gated writer, broadcasting one event per replay frame; the queue must stay drained and the client must survive the replay |
| malformed parameters | `bbox=nonsense` and `mmsi=nonsense` each return 400 |
| capped subscription | a token with an `area` claim requesting a larger box returns 400; one with `mmsis` exceeded returns 400 |
| disconnect cleanup | closing the response body releases the stream slot, so a second connect within the token's `conns` succeeds |
| stalled reader | a client that never reads causes the write deadline to fire; the handler returns and releases its slot rather than blocking indefinitely |

The snapshot ordering is the case worth writing first: ingesting after the `GET` exercises only the live path and would pass whether or not replay works at all.
