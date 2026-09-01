# Data policy

This document states what aiscast re-serves, under what terms, what it refuses, and how it intends to pay for itself. Consumer-facing attribution strings are in the [README](../README.md#licensing-and-attribution). This document gives the reasoning behind them.

## Licensing is per source

Each reception carries its license. Each output shows that license. Licenses are never merged. aiscast does not relicense the aggregate: each event is re-served under the terms of its source. For this reason, `source` is on every event, every vessel, and every archived hour.

This arrangement is what ODbL calls a collective database. The [ODbL preamble](https://opendatacommons.org/licenses/odbl/1-0/) describes it: "If the contents have multiple sets of different rights, Licensors should describe what rights govern what contents together in the individual record or in some other way that clarifies what rights apply." Share-alike attaches only to the volunteer aggregate (ODbL §4.5(a)). It never reaches the other sources served alongside it.

| Source                 | License                    | Obligations                                                                      | Can we re-serve?                                                                                                                                   |
| ---------------------- | -------------------------- | -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Kystverket             | NLOD 2.0                   | Attribution. Not sublicensable, so NLOD binds downstream users directly          | Yes, under NLOD                                                                                                                                    |
| Fintraffic Digitraffic | CC BY 4.0                  | `Source: Fintraffic / digitraffic.fi, license CC 4.0 BY`                         | Yes, under CC BY                                                                                                                                   |
| AISHub                 | Membership, "use for free" | Keep feeding receiver-only data and credit AISHub. Revocable at will             | Yes, with attribution. AISHub confirmed in writing on 2026-08-22 that commercial use and redistribution are fine. It is never the basis for an SLA |
| BarentsWatch           | NLOD                       | "Data delivered by BarentsWatch". High-traffic use may be charged                | Yes. The same AIS Norge data as Kystverket plus satellite/offshore coverage; ingested continuously, only copies of transmissions another source already delivered are withheld |
| EuRIS                  | Open data with attribution | The literal string `API/Service [name] incorporated from EuRIS (eurisportal.eu)` | Yes, on a `/v1` overlay                                                                                                                            |
| Embry-Riddle           | None published             | Their AIS team has been asked for terms                                          | Best effort, purgeable                                                                                                                             |
| aisstream.io           | No terms exist             | Ask before relying on it                                                         | Unclear                                                                                                                                            |
| Denmark DMA archive    | None stated                | Confirm reuse rights with DMA before any redistribution                          | Blocked                                                                                                                                            |
| Volunteer stations     | [Contributor agreement](contributor-agreement.md) | Per that agreement                                                               | Yes: receptions are CC0, the aggregate is ODbL                                                                                                     |

Reception and redistribution of AIS is lawful where it matters:

- US: 47 USC §605(a) exempts broadcasts "for the use of the general public, which relates to ships".
- Germany: § 5 TDDDG covers it.
- UK: the position is grey on paper but settled in practice.

IMO MSC 79's 2004 condemnation is non-binding and universally ignored.

## Sources deliberately not used

- **Paid aggregator feeds** (MarineTraffic, VesselFinder, ShipXplorer feeder programs): what they offer feeders is a web plan, not data. In exchange, they take a perpetual license over what is fed. Their retail APIs forbid redistribution outright.
- **Scraping any commercial map**: this is circumvention, not a grey area.
- **aprs.fi** (no caching permitted), **Global Fishing Watch** (CC BY-NC and itself downstream of a commercial provider), **Kystverket's restricted tier** and **HELCOM raw** (explicit agreements required).
- **Public AIS-catcher dashboards**: AIS-catcher obfuscates the NMEA payload unless the operator opts in. To ingest a dashboard, we must ask each operator to feed directly. A direct feed is the better arrangement anyway.
- **Commercial and satellite AIS**: the market is consolidated into Kpler and S&P Global. A global wholesale feed prices around $437,500/month. Every retail tier forbids redistribution. This data only enters as bring-your-own-key and is never relayed.

## The contributor agreement

Volunteer data comes in under the [contributor agreement](contributor-agreement.md), written in plain language. Its summary:

> You dedicate the data you share to the public domain. We publish the aggregate under an open license and keep it open, even if the project is sold. You confirm that the data is your station's own reception. You can stop sharing at any time. Changes to this agreement arrive as public pull requests.

Anonymous UDP senders on port 10110 may never have seen the agreement, so its representations do not bind them. Their receptions are still treated as CC0: receptions of a public broadcast are facts in which no rights subsist, and to the extent a sender holds any, deliberately sending them here is the dedication. Do not send data you have no right to send. Cross-source dedupe drops reports already received from another feed, which limits relaying another network's data through this lane, and anything suspect is purgeable per source. A token, and with it the agreement, is the price of a named and credited station.

The agreement guards against the failure mode of the ADS-B Exchange sale and the feeder exodus that followed. No existing AIS network publishes its aggregate under an open license or offers a plain-language contributor agreement. The agreement is therefore the pitch as much as the paperwork.

## Privacy

The public-facing statement is [docs/privacy-policy.md](privacy-policy.md). The position behind it:

- **Vessel opt-out**: a documented request path and a suppression list applied at fan-out and in history queries. The stated default is to publish all and honor opt-outs. Opt-outs cover small craft tied to an identifiable person, which is the GDPR Article 21 objection path that legitimate interest requires. Commercial traffic is excluded, because its AIS carriage is mandated. Norway's open feed excludes fishing vessels under 15 m and leisure craft under 45 m, which shows the same dial set differently.
- **Retention**: receptions from open-licensed sources are kept indefinitely. Volunteer-contributed receptions are kept per the contributor agreement. A deletion procedure removes opt-out vessels from history.
- **Station locations**: never asked for, and derived locations are shown only coarse. A feed that includes own-vessel (`!AIVDO`) sentences publishes its own position, and the station is then identified by that MMSI. UDP stations without own-vessel sentences are identified by a keyed hash of the address, never by the address itself.
- **Own-ship position** from a boat's own transponder is shared once the Signal K plugin is enabled. The plugin ships disabled, so enabling it is the consent. Own-ship sharing also has its own switch.
- **Abuse response**: per-key revocation, per-IP limits, and a published contact address.
- **GDPR basis** for Class B small-craft data is legitimate interest in broadcast data. It is reviewed alongside the contributor agreement.

## Funding

The goal is to cover infrastructure and make the time feel worthwhile, not to build a company. The model is open data, paid access.

The data is never relicensed. NLOD forbids added terms. CC BY 4.0 forbids downstream restrictions. ODbL on a volunteer aggregate permits commercial use by anyone. A non-commercial restriction on the data would be unenforceable and would break the contributor pitch. Open data is also the differentiator, because no commercial API allows redistribution at all.

What is sold is the hosted service: reliable bbox fan-out, history queries, an SLA, and permission to use this service commercially. Open-Meteo, OpenSky Network, and OSM hosting by Mapbox and MapTiler are the precedents. Tiers meter cost axes rather than judging intent: bbox area, concurrent connections per key, update rate, history depth. A plotter needs 50 nm around the boat. A fleet tracker needs an ocean. Current tiers are in [docs/limits.md](limits.md).

Feeders get the commercial tier free, because growth of the network is the coverage problem anyway. The live stream stays free. Tracks, `/v1/history`, and bulk queries pay for the query infrastructure that costs money to run. The raw reception archive can still sit in a public bucket, because R2 has no egress fee.
