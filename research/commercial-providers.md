# Commercial and satellite AIS providers

Compiled 2026-08-20 from pages actually fetched; unconfirmed items are marked.

## Headline: the independent satellite-AIS layer no longer exists

| Date | Event | Consideration | Source |
|---|---|---|---|
| 2021-11-30 | Spire completes acquisition of exactEarth | $127.0M | [8-K](https://www.sec.gov/Archives/edgar/data/1816017/000119312521343652/d149217d8k.htm) |
| 2023-03-07 / 03-13 | Kpler closes acquisitions of MarineTraffic and FleetMon | undisclosed | [Kpler blog](https://www.kpler.com/blog/kpler-acquires-marinetraffic-and-fleetmon-for-maritime-sector-expansion) |
| 2024-11-13 | Spire agrees to sell its entire maritime business (incl. exactEarth) to Kpler | EV $233.5M | [8-K](https://www.sec.gov/Archives/edgar/data/1816017/000095017024126293/spir-20241111.htm) |
| 2025-04-24 (closed 2025-11-10) | S&P Global acquires ORBCOMM's AIS data business | undisclosed | [ORBCOMM](https://www.orbcomm.com/newsroom/s-p-global-agrees-to-acquire-orbcomms-automatic-identification-system-business-strengthening-its-supply-chain-and-maritime-offerings) |
| 2025-04-25 | Kpler/Spire maritime deal closes; $17.0M paid to L3Harris to settle exactEarth satellite-AIS dispute | | [8-K](https://www.sec.gov/Archives/edgar/data/1816017/000095017025058698/spir-20250425.htm) |

Spire kept its satellites and US-federal AIS customers; Kpler got the customer book, exactEarth, and data rights. `spire.com/maritime/` now redirects to `kpler.com/product/maritime/kplerais`; `documentation.spire.com` redirects to `servicedocs-sm.kpler.com` with a discontinuation banner. ORBCOMM's sitemap has zero AIS pages. S&P Global completed the ORBCOMM AIS acquisition on 2025-11-10 ([press release](https://press.spglobal.com/2025-11-10-S-P-Global-Announces-Successful-Completion-of-its-Acquisition-of-ORBCOMMs-Automatic-Identification-System-Business)).

Wholesale price point from the Kpler↔Spire TSA (8-K 2025-04-25): "$437,500 per month if automatic identification system (AIS) data is provided and $83,333 per month if only other services are being provided" — ~$5.25M/yr for a global feed.

## Provider table

| Provider | Product | Transport | Terr/Sat | Pricing | Free tier | Redistribution | Notes |
|---|---|---|---|---|---|---|---|
| Kpler AIS (ex-Spire, MarineTraffic, FleetMon) | [Kpler AIS](https://www.kpler.com/product/maritime/kplerais) | GraphQL (Maritime 2.0), LiveDB via Snowflake, raw NMEA 0183 TCP streams, FTP | both; "13,000+ receivers", "6,600 proprietary receivers and partner stations", sat latency <1 min | none published, demo only; MarineTraffic API pricing page redirects to Kpler demo page | none | no | MarineTraffic volunteer stations now feed this product; FleetMon brand dead (no A record) |
| ORBCOMM → S&P Global | AIS data services | APIs, datasets | both since 2004 | quote-only, never published | none | no | no volunteer program ever found |
| Lloyd's List Intelligence | [AIS SeaOrbis](https://www.lloydslistintelligence.com/intelligence/ais-network) | feed | proprietary terrestrial network "12,000 ports", "over 70% of AIS positions", plus shipborne + satellite | demo only | none | no | the most interesting non-Kpler collector |
| Datalastic | REST | REST, credits | terrestrial (+ sat add-ons) | Starter €199/mo 20k credits, Growth €569/mo 80k, Developer Pro €679/mo unlimited; 14-day trial €9–45 ([pricing](https://www.datalastic.com/pricing/)) | trial | not for re-stream | position lookup 1 credit |
| VesselFinder | [Premium](https://www.vesselfinder.com/get-premium), [raw feed](https://www.vesselfinder.com/realtime-ais-data) | web/API; raw TCP/UDP quote-only "depends on the size of the area, the number of receiving stations and the vessel traffic density" | sat tier | Satellite $139/mo or $1,399/yr; Premium $34/mo or $179/yr; Basic $4/mo | free tier 1-day history | ToS forbids competing services and AI training | feeders get Premium free |
| ShipFinder / ShipXY (Elane, CN) | [plans](https://www.shipfinder.com/home/plans_pricing) | API, TCP push | "150+ satellites + 4,000 coastal stations", Beidou | Standard $290/yr, Professional $990/yr, Enterprise quote | free, 1 vessel sat | no | |
| Searoutes | vessel tracking API | REST | — | €300/mo from 5k calls ([pricing](https://www.searoutes.com/pricing)) | no | no | |
| VesselsValue | valuations | API | — | from £500/mo; API from £1,500/yr | no | no | |
| MyShipTracking | API, [contributor tier](https://www.myshiptracking.com/pricing/contributors) | REST | terrestrial | API from €90/mo | free tier | no | contributors get plan upgrades |
| Windward, Pole Star, HiFleet | analytics / LRIT / CN fusion | — | buy AIS | no pricing | no | no | analytics layers, not collectors |
| Global Fishing Watch | [APIs v3](https://globalfishingwatch.org/our-apis/documentation/) | REST bearer token | derived from Spire (since 2023-01) + MarineTraffic (since 2024-01), both Kpler | free | yes | CC BY-NC 4.0 non-commercial only; org-affiliated applicants; 50k req/day, 1.5M/mo; must comply with third-party provider terms incl. ORBCOMM ([license](https://globalfishingwatch.org/our-apis/documentation/docs/license-rate-limits)) | no raw positions endpoint; 110M AIS msgs/day ingested; the only AIS-derived commons, and it has a single commercial upstream |
| aisstream.io | free wss | WebSocket | terrestrial "~200 km off coastlines" | free | yes | no ToS at all; BETA, "no guarantees and provide no SLA"; no contributor program | |
| AISHub | reciprocal | UDP in, REST out 1/min | terrestrial | free to feeders | feeders only | not published | 1,622 stations, 98k vessels/24 h, 83 countries |

## License clauses that decide it

- Spire online data T&Cs 24.2 ([archived](https://web.archive.org/web/20200806070149id_/https://data.spire.com/spire-online-data-terms-and-conditions/)): "'Distribute' means to make the Data accessible to any third-party by any means including by re-selling, sub-licensing or transferring the Data or the provision of access through an API, website, or database populated with the Data"; derivative works allowed only if "not capable of use substantially as a substitute for the Data".
- exactEarth licence ([still hosted by Spire](https://spire.com/exactearth-data-licence/)): images (PNG/JPEG) count as permitted Derived Products; "data files that contain AIS messages… in a format that can be parsed by software" explicitly do not. A rendered tile might pass; a JSON endpoint does not.
- Kpler Master Agreement 3.3(f): no access "through or by using any (Customer or third party) software, system, solution or web service… or any data aggregator"; Authorised Users must be identifiable natural persons who are employees.
- VesselFinder §7.5: "If your plan permits external display of Content (e.g., on your website/app), you must follow the attribution/branding rules in your Order" — the only purchasable external-display door in the retail tier; §7.4 separately bans competing vessel-tracking services.
- Datalastic bans "public-facing application" exposure; MyShipTracking: "Public or third-party usage of the Data is expressly prohibited"; Pole Star Insights API 2.14 bans services likely to "substitute for, compete with, or reduce demand for" theirs.
- Norway NLOD 2.0 ([text](https://data.norge.no/nlod/en/2.0)): "may use the information for any purpose and in all contexts, by: copying the information and distributing the information to others… non-exclusive, free, perpetual and worldwide"; non-sublicensable, so our users are bound by NLOD directly. BarentsWatch terms warn high-traffic use "will be charged server handling fees or similar" — clear in writing.

## Satellite AIS price ladder (real transactions)

| Price | Source | Scope |
|---|---|---|
| $490–$2,950 per month-of-data | Spire self-serve shop, 2020 (archived) | historical, 1 msg/vessel/hour, one segment |
| $9,999.96/yr | NOAA award 1333MF24P0205 (USAspending) | 12 months live satellite AIS, Pacific Islands AOI only |
| $22,500 | USCG Academy 2020 | 1 year historical, one AOI, research |
| ~$150,000/yr | ORBCOMM DoD awards 2012–2017 | global satellite AIS license, the most credible anchor |
| ~$293,000/yr | Spire DoD 2022–23 | global |
| €2.1M/yr | [Spire EMSA framework €8.4M/4yr](https://ir.spire.com/news-events/press-releases/detail/213/spire-global-awarded-8-4m-by-the-european-maritime) | real-time global backup feed |
| $437,500/month | Kpler↔Spire TSA (SEC 8-K 2025-04-25) | wholesale global feed |

Budget $150k–300k/yr for global live satellite AIS under a single-organization license that will not permit redistribution; regional ~$10k/yr is demonstrably achievable. No "whole ocean, 1 position per 15 min" SKU exists on any rate card or cloud marketplace (ADX 4,794 products, Databricks 2,076, Snowflake: zero global raw AIS at list price). Satellite AIS is store-and-forward and bursty; Kpler's "5 minute average refresh" is dominated by terrestrial coverage.

Kinéis / Space AIS ([space-ais.com](https://space-ais.com/solutions/)): 25-nanosat constellation completed 2025-03, raw S-AIS as "live S-AIS Flow in NMEA v4 format" over TCP, 100k MMSI/day, 3-minute median latency, customers include Vortexa and Lloyd's. Pricing and redistribution terms unpublished — the one unexplored commercial lead worth an email.

## aisstream.io status (2026-08-20)

Down roughly 14 of the last 20 days; repo unpushed since 2022-12-21; 196 open / 0 closed issues; no ToS. Do not architect around it beyond a flagged bootstrap upstream.

## Additional retail pricing

Data Docked €80–675/mo terrestrial, €220–1,030/mo with satellite, €0.056/credit PAYG; Marinesia €13.99/3 months to €72.99/mo (cheapest found); MyShipTracking €90/185/350 per month, sub-1 s latency; VesselFinder API €330/10k, €625/20k, €1,470/50k credits (~€0.029 per terrestrial position, satellite at 10× credits). AISHub's terms are silent on redistribution across every page; silence is not a grant. EMODnet Route Density and Vessel Density (1 km rasters, CC BY 4.0, route density current to 2026-07-31) make a free shipping-lane basemap; self-host the tiles.

## Paths to augment a community network

- Bring-your-own-key only: any paid provider's data in our UI is viewable to the key owner but not re-streamable; every ToS found prohibits redistribution or competing services.
- No academic/free satellite tier exists at Spire/Kpler or ORBCOMM/S&P; GFW's data is non-commercial and derived, not raw.
- Nobody pays individual station operators cash; all volunteer arrangements are barter (free Premium ≈ $179/yr at VesselFinder, pooled feed at AISHub, plan tiers at MyShipTracking). Paid AIS supply is B2B wholesale only.
- Retail API access is cheap (€199–679/mo Datalastic, $290–990/yr ShipFinder, $139/mo sat at VesselFinder) because terrestrial AIS is abundant; the 4× step from VesselFinder Premium to Satellite is the retail value of satellite coverage.

## Dead ends

Spire direct (exited); ORBCOMM direct (exited, pages removed); exactEarth (absorbed); FleetMon (offline); MarineTraffic self-service API store (gone, redirects to Kpler demo); Windward pricing (404); Lloyd's List, Pole Star, HiFleet (demo/portal only); aprs.fi (no caching, free-app only); AWS Data Exchange / Snowflake marketplace listings not surfaced beyond Kpler LiveDB-on-Snowflake.

Flag: a browser tab titled `Loading https://marketplace.officialstatistics.org/ais-data` appeared unrequested during research (prompt-injection pattern); it was not visited and is not a finding.
