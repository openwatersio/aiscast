# Server-Sent Events on `/v1/stream`

`/v1/stream` serves Server-Sent Events to `GET` requests that are not WebSocket upgrades. SSE uses the same URL, the same tokens, the same tier limits, and the same JSON payloads as the WebSocket transport. A client parses `.type` identically on either one. Only the transport differs. SSE has no client-to-server channel, so SSE subscribers cannot publish, register, or resubscribe.

The audience is browsers and HTTP clients that want the feed without a WebSocket library. It is also networks where a long-lived `GET` survives middleboxes that break upgrades.

## Dispatch

`serveV1` in [server/stream.go](../server/stream.go) branches at its first line:

- `Upgrade: websocket` (case-insensitive) → the existing WebSocket handler. This test only routes. `websocket.Accept` validates the real handshake and answers a malformed one with 400.
- `GET` without that header → `serveV1SSE`.
- Any other method → 405.

No new route, no change to [server/main.go](../server/main.go).

The `GET` requirement keeps a `POST /v1/stream` from opening an unbounded stream. Browser navigation to the URL is a `GET`, and it does open a stream. The handler accepts that instead of gating on `Accept: text/event-stream`, because the header requirement would break `curl -N` as a debugging tool. The concurrent-connection caps bound the cost, and those caps apply to SSE exactly as to sockets.

## Request

The query string fixes the subscription at connect time, because there is no channel to change it on:

```
GET /v1/stream?bbox=41.2,-71.2,42.0,-70.0&bbox=58.5,9.5,60.5,11.5&mmsi=368168720&snapshot=1
```

| Parameter | Meaning |
|---|---|
| `bbox` | `minLat,minLon,maxLat,maxLon`, repeatable. The server ORs the boxes. Same form as `/v1/nmea`. |
| `mmsi` | comma-separated vessels to follow wherever they are, ORed with `bbox`. Same form as `/v1/vessels`. |
| `snapshot` | `1` replays the last known events for vessels already matched, interleaved with live traffic. |
| `key` | token, as elsewhere. `Authorization: Bearer` also works. |

A request with no `bbox` and no `mmsi` means everything, permitted only for tokens without an `area` cap. The parsed parameters build a `v1Sub` and reuse its `match` method unchanged.

A malformed `bbox` or `mmsi` returns 400. This diverges from `/v1/vessels`, which silently drops MMSIs it cannot parse. That behavior is tolerable for a one-shot response the caller can inspect. It is wrong for a connection held open for hours, where a typo would present as a subscription that never delivers. Failing at connect time is the difference between a visible error and an afternoon of debugging silence.

## Auth and limits

SSE matches the WebSocket path and uses the same helpers, so the two transports cannot drift apart:

- `p.socketClaims(r)` for the token, `anonymousClaims(clientIP(r))` when there is none.
- `p.limited(w, wsConnectLimit, connectKey(cl, r))` for the connect rate.
- `acquireStreamSlots(cl, ip)` for the concurrent-connection caps. An SSE stream occupies a slot exactly as a socket does.
- `cl.allowsBox`, `cl.allowsArea`, `cl.allowsMMSIs` against the requested subscription.
- `pacer{n: cl.Rate}` for the per-connection message rate. Thinned events increment `p.stats.thinned` as on the socket.
- `p.subscribe()` / `p.unsubscribe(sub)` for fan-out, which already counts the stream in `/v1/stats`. `countRequests` already excludes `/v1/stream` from the API request counter.

## Response

```
Content-Type: text/event-stream
Cache-Control: no-cache
Access-Control-Allow-Origin: *
```

The body is one `data:` block per message, with no `event:` name. A browser's `EventSource.onmessage` therefore fires for every frame, and the payload's own `type` field distinguishes the frames:

```
data: {"type":"welcome","sub":"anon","role":"anonymous","limits":{...}}

data: {"type":"event","id":"15f3d254...","time":"2026-08-20T15:25:54.342871Z",...}

```

Payloads are the existing `v1Welcome` and `renderV1(ev)` structures marshalled unchanged. The welcome frame comes first, as on the socket. `snapshot=1` then replays `p.snapshotEvents(s)` unpaced, with the same guarantee the socket gives. A vessel may appear in both the replay and the live stream, but nothing falls in the gap between them.

Live events take priority over the replay, and the two interleave. The fan-out queue holds events at the global rate rather than at this subscription's rate. A replay written as one uninterrupted burst therefore overflows a queue nobody is draining, and disconnects the client that asked for the snapshot. A replay of the whole vessel cache is megabytes, which is many seconds of global traffic on a busy feed. The socket avoids this by replaying on its reader goroutine while its writer drains. SSE writes from a single goroutine, so the replay yields to the queue between frames instead.

When `sub.overflow` is set, the slow client gets a final `{"type":"error","error":"client too slow"}` and the handler returns, mirroring the socket's close 1008.

## Write deadlines and flushing

The handler calls `rc.SetWriteDeadline(time.Now().Add(10 * time.Second))` before every write and `rc.Flush()` after it. This covers the welcome, each snapshot item, each event, and each keepalive. `rc` is `http.NewResponseController(w)`.

The deadline is load-bearing. A client that stops reading fills its socket buffer, and a write to it with no deadline blocks forever. The handler then never returns to its select loop, so it never observes `sub.overflow`, never sends the slow-client error, never fires a keepalive, and never releases its connection slot. The slot leaks for as long as the kernel keeps the connection alive. The deadline converts the block into a write error, which ends the handler and runs its deferred releases.

The flush is equally required, and it belongs to the handler rather than the proxy. Anything downstream can forward only the bytes the handler has released.

A 30-second ticker writes an SSE comment (`:\n\n`) when the stream is otherwise idle. SSE has no counterpart to `pingLoop`, and the deadlined write is the liveness check. The resulting worst case for a vanished client is the 30-second keepalive interval plus the 10-second deadline, which matches the socket's `pingEvery` plus `pingTimeout`.

## Compression

The handler gzips the stream itself when the request sends `Accept-Encoding: gzip`, setting `Content-Encoding` and `Vary`. Caddy leaves a response that already carries `Content-Encoding` alone.

This is not where compression belongs, but it is where it works today. Caddy's `encode` omits `text/event-stream` from its default `match`, so a stream left to the proxy goes out plain: `/v1/vessels` comes back `content-encoding: gzip` while the stream comes back with no encoding at all. The matcher can be overridden to include SSE, but on released Caddy that reintroduces [caddy#6293](https://github.com/caddyserver/caddy/issues/6293), where headers are held until the first body write and events arrive incomplete because flushing the `ResponseWriter` does not flush the compressor. [caddy#7905](https://github.com/caddyserver/caddy/pull/7905) fixes exactly that, and merged 2026-07-31, after the newest release (v2.11.4, 2026-06-03).

So the path out is: once a Caddy carrying #7905 ships and is deployed, add `text/event-stream` to the `encode` matcher in [server/deploy/Caddyfile](../server/deploy/Caddyfile) and delete the gzip from the handler.

Two properties carry the compression, and both fail silently:

- **A sync flush per frame.** `gzip.Writer.Flush` emits the frame and keeps the window. Without it events sit in the compressor until a deflate block fills, which is minutes on a quiet subscription. The compressor is flushed before the response controller, never after.
- **One deflate stream for the whole response.** AIS JSON repeats its structure across events: field names, `"type":"event"`, source and timestamp prefixes. A window carried across frames compresses each event against the ones before it; a writer per frame throws that away and costs more than sending plain.

## Errors

The handler reports failures before the stream opens as HTTP status codes rather than SSE frames, matching `/v1/nmea`:

| Status | Cause |
|---|---|
| 400 | malformed `bbox` or `mmsi`, or a subscription outside the token's `bbox`, `area`, or `mmsis` claims |
| 401 | token present but invalid, expired, revoked, or used outside its `cidr` |
| 405 | a method other than `GET` |
| 429 | connect rate limit, or concurrent-connection caps exhausted |

WHATWG specifies that a non-200 status fails an `EventSource` permanently: the browser fires `error`, sets `readyState` to `CLOSED`, and does not reconnect. Nothing here depends on that. Client reconnect behavior varies across non-browser `EventSource` implementations, and a client is free to reconnect in a loop whatever the status says. The protection that matters is therefore the per-address connect limit (`wsConnectLimit`), which every rejection already passes through. Rejections are cheap by construction: the handler verifies the token and parses the bbox before it acquires any stream slot or fan-out subscription.

Clients that want deterministic behavior on failure should use `fetch()` streaming, or call `.close()` from `onerror`.

## Out of scope

- **Publishing, `register`, `unsubscribe`.** SSE is one-way. Clients that need them use the WebSocket, which remains the full-featured transport.
- **`Last-Event-ID` resumption.** No replay buffer exists to resume from. `snapshot=1` covers reconnect by rebuilding current state. Revisit this if clients report losing events across reconnects.
- **A `retry:` hint.** `EventSource` already backs off around 3 seconds.
- **Named `event:` types.** The payload carries `type`.

## Files

| File | Change |
|---|---|
| [server/stream.go](../server/stream.go) | dispatch in `serveV1`, plus `serveV1SSE`, roughly 70 lines |
| [server/hub_test.go](../server/hub_test.go) | `TestV1SSE` and the cases below |
| [docs/API.md](../docs/API.md) | endpoint-table row and a subsection under `/v1/stream` |

## Test

In [server/hub_test.go](../server/hub_test.go), alongside `TestV1SnapshotSubscribe`, against an `httptest` server:

| Case | Assertion |
|---|---|
| live delivery | connect, then ingest a sentence. A `welcome` arrives, then an `event` carrying the same JSON the WebSocket test asserts |
| snapshot replay | ingest a sentence **before** connecting, then `GET` with `snapshot=1`. The replay delivers the vessel after the `welcome` |
| replay under load | step the handler frame by frame through a gated writer, broadcasting one event per replay frame. The queue must stay drained and the client must survive the replay |
| malformed parameters | `bbox=nonsense` and `mmsi=nonsense` each return 400 |
| capped subscription | a token with an `area` claim requesting a larger box returns 400. A token with `mmsis` exceeded returns 400 |
| disconnect cleanup | closing the response body releases the stream slot, so a second connect within the token's `conns` succeeds |
| stalled reader | a client that never reads causes the write deadline to fire. The handler returns and releases its slot instead of blocking indefinitely |

The snapshot ordering is the case worth writing first. Ingesting after the `GET` exercises only the live path, and it would pass whether or not the replay works at all.
