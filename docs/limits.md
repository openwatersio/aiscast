# Access tiers and limits

aiscast is free to use and free to feed. Tokens keep the service healthy and recognise contribution. They do not protect the data: everything aiscast re-serves is open data or volunteer data that the contributor [chose to share](contributor-agreement.md). The limits below are the starting point. They loosen as capacity grows, and a token can always carry more than its tier.

## Tiers at a glance

|                                   | Anonymous                            | Personal                                                                                                                  | Feeder                                              | Commercial                          |
| --------------------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- | ----------------------------------- |
| How you get it                    | nothing to do                        | one click on the [token page](https://openwatersio.github.io/aiscast/token.html), or automatically by the Signal K plugin | a personal token whose station is contributing data | by arrangement: hello@openwaters.io |
| Cost                              | free                                 | free                                                                                                                      | free                                                | paid                                |
| Concurrent streams                | 2 per address                        | 2                                                                                                                         | 5                                                   | 10 or more                          |
| Messages per second, per stream   | 20                                   | 50                                                                                                                        | 200                                                 | unlimited                           |
| Subscribed area                   | 100 square degrees (about 10° × 10°) | 400 square degrees (about 20° × 20°)                                                                                      | unlimited                                           | unlimited                           |
| Raw NMEA feed (`/v1/nmea`)        | no                                   | no                                                                                                                        | yes                                                 | yes                                 |
| History and bulk queries (coming) | no                                   | no                                                                                                                        | yes                                                 | yes                                 |
| Sending data                      | no                                   | yes                                                                                                                       | yes                                                 | yes                                 |
| Expiry                            | —                                    | never                                                                                                                     | never                                               | as agreed                           |

aiscast enforces limits per token, and per network address on top of that. One address gets at most 32 concurrent streams, no matter how many tokens are behind it (2 without a token). That is roomy enough for a marina, a carrier-grade NAT, or a VPN exit, and it still makes minting many tokens pointless. When you exceed a per-second rate, aiscast thins the stream: you get fewer messages and the connection stays up. When you exceed a connection, area, or MMSI-list limit, aiscast refuses the request with a message that says so. The MMSI list bounds how many vessels you follow, not the area, so every tier allows a world-wide MMSI subscription.

## What each tier is for

**Anonymous** is for a first try and for the public map. Open the viewer, subscribe to a region or follow up to ten vessels by MMSI on `/v1/stream`, and poll `/v1/vessels`. There is no sign-up. Two concurrent streams per network address, so a reconnect on a flaky link still works and a plotter and a phone on one boat both work.

**Personal** is for a boat, a hobby project, or a developer's laptop. aiscast mints a personal token for a keypair you generate. It costs nothing, needs no account, and never expires. Two concurrent streams, 50 messages a second each, a 20°×20° area or up to 50 vessels followed by MMSI wherever they go: enough for a chart plotter, a home dashboard, a buddy-boat tracker, or a regional app. Personal tokens can also publish what their own receiver hears, which is the first step to becoming a feeder.

**Feeder** is earned by feeding, and there is no application. When the station behind a personal token has delivered at least 1,000 messages in the last 24 hours, aiscast treats every connection that token makes as a feeder: five streams, 200 messages a second, any area, the raw NMEA feed, and history when it exists. Stop feeding and the tier lapses after a day. Start again and it returns. Nobody has to email anyone. If you feed by UDP only, switch your receiver to the authenticated path (AIS-catcher `-H` with your token, or the Signal K plugin) and the same station counts. Bind a fixed UDP source address to your token on the token page and UDP counts too.

**Commercial** is for products and fleets: anything that needs more than the feeder tier, or wants it without running a receiver, or wants an agreement and a contact. Terms are per arrangement. The tier exists to sustain the project, not to gate data. aiscast can mint a fleet token that follows only its own vessels: any number of MMSIs world-wide, and no bounding-box or whole-world subscriptions.

## Sending data

- **Authenticated** (AIS-catcher `-H … USERPWD x:<token>`, the Signal K plugin, `/v1/stream` publish): 6,000 sentences per minute per token, 1,000 per frame, 1 MB per HTTP post. Your station appears under your token's id, and aiscast credits the data to you.
- **UDP** (`ais.openwaters.io:10110`, any forwarder): no token, 500 sentences per second per source address. UDP carries no authentication, so it is the lowest-trust path. aiscast shows it on the map and in the stream with `source: udp:<id>`. It never raises a vessel's trust level. aiscast forwards it to partners and the raw feed only once another station or an open feed has corroborated the same traffic. If you want credit and feeder status for a UDP station, bind its source address to your token.

Tokens do not block bad data. Rate caps, plausibility checks (impossible position jumps, invalid identifiers), and corroboration catch it. Every message is archived with its source, so aiscast can purge a bad source. Report a problem station to hello@openwaters.io.

## Everything else

aiscast rate-limits every HTTP endpoint per network address:

- 120 requests per minute for `/v1/vessels`, `/v1/stations`, and `/v1/stats`.
- 10 per minute for `/v1/keys` and the in-band `register` frame on `/v1/stream`, in one shared window. One token is all a client ever needs, and a shared address may hold several clients.
- 20 WebSocket connections per minute across `/v0/stream`, `/v1/stream`, and `/v1/nmea`. aiscast counts them per token on `/v1/stream` and `/v1/nmea` when a client presents one, and per address otherwise. `/v0/stream` counts per address, because its token arrives after the handshake. A working client connects once and reconnects only on failure.

Behind the proxy, the client address is the last hop of `X-Forwarded-For`. aiscast disconnects a client that cannot keep up with its stream (1,024 queued events) and gives a close reason.

## Revocation

Tokens do not expire, so aiscast handles misuse by revocation. It refuses a revoked token on every endpoint from the next connection. Write to hello@openwaters.io if you need more than your tier, you are a feeder, or you are unsure which tier applies.
