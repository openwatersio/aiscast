# Dead stream connections hold their slots for minutes

A `/v1/stream` client that disappears without closing the TCP connection (link drop, NAT or VPN timeout, a proxy egress that keeps the upstream socket open) is not noticed by the server until TCP keepalive gives up on it. Its entries in `conns` and `addrConns` stay counted for that long. With `personalConns` at 2, a boat whose link drops twice inside that window and reconnects with the same token is refused with `concurrent connections per key exceeded`, and `addrMaxStreams` fills the same way for everyone behind a shared address.

## Mechanism

- `serveV1` ([server/stream.go](../server/stream.go)) hijacks the connection in `websocket.Accept`, after which `r.Context()` is no longer cancelled when the peer goes away. The only thing that ends the handler is a read or write error.
- The reader goroutine blocks in `c.Read(ctx)` with no deadline; the writer only writes when the client is subscribed and events arrive. An unsubscribed publisher, which is the Signal K plugin's normal state, is pure idle: a half-open connection errors on neither side.
- The server's TCP peer is Caddy on localhost, so the server's own keepalives are always answered. Caddy's listener probes the client with Go's default 15 s keepalive and closes the upstream when the probes fail, roughly 2.5 minutes after the link dropped. That is the zombie's lifetime on the current deployment, and it is unbounded behind any TCP-terminating proxy (Cloudflare, if re-enabled) that answers probes on the client's behalf.
- The Signal K plugin pings every 30 s and reconnects after 60 s of silence, so it takes its second slot while the first is still counted for another ~90 s. A second drop in that window hits the cap.
- `/v1/nmea` ([server/nmea.go](../server/nmea.go)) and `/v0/stream` share the pattern. TCP feeder sources ([server/source.go](../server/source.go)) use a 2-minute read deadline, so the ingest side does not have the problem.

## Change

- Every stream handler runs `pingLoop`: `c.Ping(ctx)` every 30 s with a 10 s timeout; on failure it closes the socket without a handshake and cancels the handler's context, which releases both slots. coder/websocket answers pings only while `Read` is running, which these handlers already do, so any conforming client answers. A dead link now holds a slot for at most ~40 s, under the plugin's reconnect delay and independent of what sits in front of the server.
- The per-token and per-address caps keep refusing, rather than evicting the oldest connection. Two devices sharing one token get a clear error on one of them instead of silently kicking each other off.

## Checks

- `TestPingTimeoutReleasesSlots`: a client that connects and never reads (so never answers pings) is closed and its `conns` and `addrConns` entries released.
- `aiscast_ping_timeouts_total` counts connections closed this way, so the fix is visible in the dashboard.
