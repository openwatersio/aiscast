# The feeder network

aiscast is only as good as its coverage, and coverage is volunteers with
antennas. This document states what a station gets for feeding, why the project
is built that way, and what it deliberately does not do. The terms are in the
[contributor agreement](contributor-agreement.md), the licensing reasoning is in
the [data policy](policy.md), and the tiers are in [limits.md](limits.md).

## What a feeder gets

Every other volunteer AIS network offers feeders a web plan. aiscast offers data
and identity.

- **The aggregate back.** Feeders get the deduplicated raw stream on
  `wss://ais.openwaters.io/v1/nmea`: every reception aiscast has, from every
  source, bbox-filterable, as `!AIVDM` with TAG blocks. Not a snapshot, not a
  rate-limited REST tier. No other AIS network returns the aggregate to the
  people who built it.
- **The feeder tier, automatically.** 1,000 messages in 24 hours and the token
  is a feeder token: five streams, 200 messages a second, any area, history.
  There is no application, no uptime gate, and nobody to email. Stop feeding and
  it lapses after a day; start again and it returns.
- **A named station** with a public page: vessels heard, coverage extent,
  message counts, and how many messages another station heard first.
- **An open aggregate that stays open.** Volunteer receptions are CC0, the
  aggregate is ODbL, and the [contributor agreement](contributor-agreement.md)
  says so before the data exists. It also says the aggregate stays open if the
  project is sold, and it changes only through public pull requests.

## Heard first

The number that matters on a station page is not messages received. It is
**messages this station heard first** — receptions that no other station and no
open feed had already delivered.

That is the honest measure of what a station adds. A rooftop in a well-covered
harbour can log millions of messages that three other stations also logged; a
station on an empty headland can log a fraction of that and be the only reason
those vessels appear at all. Ranking by volume would tell operators to point
antennas where the ships already are. Ranking by heard-first tells them to point
antennas at the gaps, which is the actual problem.

Coverage scales with the square of range — `d(nm) ≈ 1.23 × (√h_rx(ft) +
√h_tx(ft))`, so a 15 m rooftop reaches ~23 nm and a 500 m headland reaches
~64 nm. Height and placement beat count. The metric is designed to say so.

## Station identity

A station's id is derived, never asked for.

- **Authenticated stations** (AIS-catcher `-H`, the Signal K plugin) are keyed
  by their token's public key and can set a display name.
- **UDP stations** are keyed by a keyed hash of the source address, never the
  address itself. A sender whose `!AIVDO` sentences identify the vessel is keyed
  by that MMSI instead, because it has already published that.
- **Locations** are never requested, and derived locations are shown coarse.

Naming a station is optional and free. It exists so that a contribution has
somebody's name on it, not as a gate on anything.

## Multi-homing is fine

aiscast never asks a station to stop feeding anywhere else, and never will.
AIS-catcher fans to as many sinks as it is given;
[`docker-shipfeeder`](https://github.com/sdr-enthusiasts/docker-shipfeeder)
already fans one receiver to roughly eighteen aggregators. Adding aiscast is one
more output, not a switch.

This is not generosity. Exclusivity is how the incumbents extract a perpetual
transferable licence, and a network that needs exclusivity to survive has
already lost the argument about whose data it is.

## What this project does not build

- **A closed feeder binary or a proprietary framing.** aiscast accepts plain
  UDP NMEA, AIS-catcher's standard HTTP POST envelope, TCP push, Signal K, and
  MQTT. Every one is an existing format. Nothing here requires our software.
- **Default-on sharing.** The Signal K plugin ships disabled. Enabling it is the
  consent, and own-ship position has its own separate switch.
- **A relicensed aggregate.** Licences are per source and are never merged,
  which is why `source` is on every event. See [policy.md](policy.md).
- **Data sold out from under contributors.** What is sold is the hosted service:
  fan-out, history, an SLA, and permission to use it commercially. Feeders get
  the commercial tier free, because growing the network is the coverage problem
  anyway.

## Where to talk about it

- Station setup, coverage gaps, and the API: GitHub Discussions on this repo.
- Anything about the agreement, licensing, or a vessel opt-out: open a
  discussion or write to hello@openwaters.io.
- Changes to the contributor agreement arrive as public pull requests, so
  disagreeing with one is a review comment.
