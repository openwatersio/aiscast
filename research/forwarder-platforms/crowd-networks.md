# Crowd-sourced AIS networks and feeder apps: partnership targets

Researched 2026-08-24. Complements [community-networks.md](../community-networks.md) (aggregator terms and feeder economics); this file scores each network as a partnership/inquiry target for aiscast.

## Pocket Mariner / Electric Pocket (Boat Beacon, SeaNav, Boat Watch)

1. **How AIS gets in**: Company-run terrestrial receiver network. Contributors run a physical receiver (dAISy USB, Digital Yacht, COMAR SLR200, EasyAIS, etc.) and send UDP to `boatbeaconapp.com` (`54.225.113.225:5322`), or use ShipPlotter. The phone apps are consumers, not receivers. All three apps are one company (founder/CEO Steve Bennett); one shared network feeds them. ([Add AIS Coverage](https://pocketmariner.com/ais-ship-tracking/cover-your-area/), [SeaNav](https://pocketmariner.com/mobile-apps/seanavapp/))
2. **Openness**: Closed/commercial. Contributors get a station page and sometimes free hardware; no written data-ownership terms on the contribution page. Aggregate sold via AISWatch/FleetWatch, quote-only. ([commercial services](https://pocketmariner.com/commercial-services/aiswatch/fleetwatch/))
3. **Partnership value**: They already forward AIS to AISHub, MarineTraffic, and ShipFinder (per their support pages), so redistribution deals are normal for them — but the value flows their way. Feeder cross-promotion angle only; stations multi-home freely.
4. **Contact**: coverage@pocketmariner.com (station onboarding), boatbeacon@electricpocket.com (support).
5. **Verdict: PARTNER-INQUIRY (low priority)** — cross-promotion, not data exchange.

## MarineTraffic mobile app

The app is a viewer; feeding requires separate hardware/software (DataHub, NMEA Router, AIS Dispatcher). No WiFi-NMEA-to-app upload path found (unverified negative). ([NMEA Router](https://support.marinetraffic.com/en/articles/9552951-nmea-router), [share AIS data](https://support.marinetraffic.com/en/articles/9552963-how-can-i-share-ais-data-from-my-ais-receiver-transponder)) Kpler-owned, perpetual-license feeder terms. **Verdict: ALREADY-COVERED / IGNORE** (see community-networks.md).

## VesselFinder app

Viewer only; separate "AIS Partner" station registration (UDP anonymous upload to `195.201.71.220:5964`) via [stations.vesselfinder.com/become-partner](https://stations.vesselfinder.com/become-partner). ToS bans redistribution/competing use. **Verdict: ALREADY-COVERED / IGNORE.**

## ShipFinder.co (Pinkfroot)

Built by **Pinkfroot** (Plane Finder), unrelated to Pocket Mariner; community UDP/TCP to `ais.shipfinder.co.uk:4001`; network dormant since ~2014. ([Pinkfroot ShipFinder](https://my.pinkfroot.com/page/shipfinder-co)) **Verdict: IGNORE (dormant).**

## FleetMon Mobile / Kpler

Viewer app; FleetMon folded into Kpler (MarineTraffic/Spire family); fully commercial. **Verdict: ALREADY-COVERED / IGNORE.**

## AISHub

Reciprocal feed: UDP to `data.aishub.net` per-feeder port; API gated at ≥10 vessels, ≥90% uptime. Confirmed in writing (2026-08-22) that redistribution and commercial use of the aggregate are unrestricted, no attribution required, but the feed key is revocable at will. aiscast already polls AISHub every 20 s as a member. Could feed aiscast's aggregate back to strengthen standing, but that trades leverage for a network we already have. **Verdict: ALREADY-COVERED** (production integration exists).

## OpenSeaMap

No AIS network of its own — the "AIS" layer is MarineTraffic raster tiles. Its crowdsourcing culture (depth-sounding uploads) is a plausible model/community for a future basemap conversation, not an AIS one. **Verdict: IGNORE for AIS.**

## aiscatcher.org (AIS-catcher community hub, jvde-github)

1. **How AIS gets in**: Any AIS-catcher install shares by default unless `-X off`; registration issues a sharing key streaming to `185.77.96.227:4242` in COMMUNITY_HUB binary framing. ([AIS-catcher docs: Community](https://jvde-github.github.io/AIS-catcher-docs/community/))
2. **Openness**: Closed — API paths 403/Turnstile-gated, `ai-train=no`, feeders get a map popup and station stats.
3. **Partnership value**: The largest pool of existing AIS-catcher feeders (1,259 online / 843 public stations) — the single best feeder-acquisition channel, since multi-homing is one config line (`-u ais.openwaters.io 10110` or `-H .../v1/receive`). Not a data-exchange partner; a documentation/visibility play: get aiscast listed as a documented sink in AIS-catcher docs and docker-shipfeeder.
4. **Contact**: [GitHub Discussions](https://github.com/jvde-github/AIS-catcher/discussions) / Issues on jvde-github/AIS-catcher.
5. **Verdict: PARTNER-INQUIRY (high leverage)** — feeder cross-promotion via docs PRs, not data exchange.

## Airframes (airframes.io)

1. **How AIS gets in**: AIS-catcher HTTP output → `feed.airframes.io:5599`, no signup, confirmed live on their forum. Marine REST output endpoints (`/v1/marine`, `/v1/vessels`) 404 as of last check — ingest exists, public output doesn't yet. ([Airframes AIS-Catcher HTTP Ingest is LIVE](https://community.airframes.io/t/airframes-ais-catcher-http-ingest-is-live/83))
2. **Openness**: Community-run; attribution-based licensing, free feeder API for their aviation data; leans open rather than closed-by-default. ([licensing](https://docs.airframes.io/api/licensing))
3. **Partnership value**: Best data-exchange candidate in this beat — multi-domain open-tracking community actively standing up AIS; aiscast could offer its aggregate for cross-listing or coordinate on licensing.
4. **Contact**: Discord ([invite](https://discord.com/invite/airframes)), code@airframes.io, [github.com/airframesio](https://github.com/airframesio), or the forum thread.
5. **Verdict: PARTNER-INQUIRY (draft-worthy, best fit).**

## aprs.fi

HTTP `jsonais` POST feeding exists, but ToS is non-commercial-only, bars caching/archival and aggregator cloning — exactly what aiscast is. Developer Heikki Hannikainen (OH7LZB), reachable via the [aprs.fi Google Group](https://groups.google.com/g/aprsfi). **Verdict: IGNORE** for data exchange (anti-aggregator terms).

## ShipXplorer (AirNav)

Feeders run AIS Dispatcher or closed `sxfeeder`; free Business plan + free hardware near coverage gaps as incentives; nothing re-exposed openly. Their free-hardware bootstrap tactic is worth studying for aiscast's own feeder program (flagged in community-networks.md). **Verdict: ALREADY-COVERED / IGNORE for partnership.**

## MSSIS (Volpe / US DOT)

Government-to-government reciprocal pool (60+ nations, TV32 client); accounts for government agencies only, contractors need a sponsor. Notable as prior art for "feed in, get the whole pool back." Contact (unverified): Volpe-ais@dot.gov. **Verdict: IGNORE** (no path in for a private service; conceivable future government/academic-sponsor angle).

## SeaVision

US Navy/Volpe visualization layer on MSSIS-class feeds; not a crowd-sourcing network. **Verdict: IGNORE.**

## Saildrone

Own-fleet telemetry published via NOAA ERDDAP; not an AIS crowdsourcing network. **Verdict: IGNORE.**

## noforeignland (adjacent, not AIS)

Crowd-sourced cruiser tracking via satellite trackers, not AIS. Anti-paywall ethos and an audience of position-sharing offshore cruisers make it a plausible cross-promotion lead outside this beat. **Verdict: IGNORE for AIS; possible cross-promotion lead.**

## Partnership shortlist

1. **Airframes** — real data-exchange conversation; open-leaning, actively building AIS ingest; Discord/email/GitHub all live.
2. **aiscatcher.org / AIS-catcher docs** — highest-leverage feeder acquisition: docs PRs adding aiscast as a documented sink (AIS-catcher docs, docker-shipfeeder).
3. **Pocket Mariner** — low-priority cross-promotion; they already multi-feed AISHub/MarineTraffic/ShipFinder.
