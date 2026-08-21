# Access tiers and limits

aiscast is free to use and free to feed. Tokens exist to keep the service healthy and to recognise contribution, not to protect the data: everything aiscast re-serves is open data or volunteer data that the contributor chose to share. The limits below are the starting point; they will loosen as capacity grows, and a token can always be given more than its tier.

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

Limits are enforced per token, and on top of that per network address: no more than 8 concurrent streams from one address no matter how many tokens are behind it (2 without a token). That is what makes minting many tokens pointless. Going over a per-second rate thins the stream (you get fewer messages, the connection stays up); going over a connection, area, or MMSI-list limit is refused with a message that says so. Following vessels by MMSI is bounded by the list, not by area, so a world-wide MMSI subscription is allowed in every tier.

## What each tier is for

**Anonymous** is for trying things out and for the public map. Open the viewer, subscribe to a region or follow up to ten vessels by MMSI on `/v1/stream`, poll `/v1/vessels`: no sign-up. Two concurrent streams per network address so a reconnect on a flaky link does not lock you out and a plotter and a phone on one boat both work.

**Personal** is for a boat, a hobby project, or a developer's laptop. A personal token is minted for a keypair you generate; it costs nothing, needs no account, and does not expire. Two concurrent streams, 50 messages a second each, a 20°×20° area or up to 50 vessels followed by MMSI wherever they go: enough for a chart plotter, a home dashboard, a buddy-boat tracker, or a regional app. Personal tokens can also publish what their own receiver hears, which is the first step to becoming a feeder.

**Feeder** is earned, not applied for. When the station behind a personal token has delivered at least 1,000 messages in the last 24 hours, every connection that token makes is treated as a feeder: five streams, 200 messages a second, any area, the raw NMEA feed, and history when it exists. Stop feeding and the tier lapses after a day; start again and it is back. Nobody has to email anyone. If you feed by UDP only, switch your receiver to the authenticated path (AIS-catcher `-H` with your token, or the Signal K plugin) and the same station counts. Bind a fixed UDP source address to your token on the token page and UDP counts too.

**Commercial** is for products and fleets: anything that needs more than the feeder tier, or wants it without running a receiver, or wants an agreement and a contact. Terms are per arrangement; the point of the tier is to sustain the project, not to gate data.

## Sending data

- **Authenticated** (AIS-catcher `-H … USERPWD x:<token>`, the Signal K plugin, `/v1/stream` publish): 6,000 sentences per minute per token, 1,000 per frame, 1 MB per HTTP post. Your station appears under your token's id and is credited to you.
- **UDP** (`ais.openwaters.io:10110`, any forwarder): no token, 500 sentences per second per source address. UDP cannot be authenticated, so it is the lowest-trust path: it is shown on the map and in the stream with `source: udp:<id>`, it never raises a vessel's trust level, and it is forwarded to partners and the raw feed only once another station or an open feed has corroborated the same traffic. If you want credit and feeder status for a UDP station, bind its source address to your token.

Bad data is not blocked by tokens; it is caught by rate caps, plausibility checks (impossible position jumps, invalid identifiers), corroboration, and the fact that every message is archived with its source so a bad source can be purged. Report a problem station to hello@openwaters.io.

## Everything else

Every HTTP endpoint is rate-limited per network address: 120 requests per minute for `/v1/vessels`, `/v1/stations`, `/v1/stats`; 3 per minute for `/v1/keys` (one token is all a client ever needs); 20 WebSocket connections per minute across `/v0/stream`, `/v1/stream`, `/v1/nmea` (a working client connects once and reconnects only on failure). A client that cannot keep up with its stream (1,024 queued events) is disconnected with a close reason.

## Revocation

Tokens do not expire, so misuse is handled by revocation: a revoked token is refused on every endpoint from the next connection. If you need more than your tier, you are a feeder, or you are unsure which tier applies, write to hello@openwaters.io.
