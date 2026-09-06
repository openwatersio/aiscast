# Platforms that could forward AIS to aiscast

Researched 2026-08-24. Question: beyond AIS-catcher and the Signal K plugin, which systems that already have live AIS flowing through them (nav apps, desktop software, hardware gateways, crowd networks) can become aiscast forwarders — and for each, can users configure it today, can we build it, or does it take vendor cooperation? Evidence with sources is in [forwarder-platforms/](forwarder-platforms/):

| File | Covers |
|---|---|
| [mobile-apps.md](forwarder-platforms/mobile-apps.md) | Navionics, Aqua Map, iNavX, iSailor, SEAiq, TZ iBoat, Weather4D, Orca, savvy navvy, Boat Beacon/SeaNav, NV Charts, ActiveCaptain, AIS-catcher Android |
| [desktop-software.md](forwarder-platforms/desktop-software.md) | Coastal Explorer/Nemo, TimeZero, Expedition, qtVlm, PolarView, Adrena, WinGPS, SeaPro, SmarterTrack, ShipPlotter, SDRangel |
| [hardware-gateways.md](forwarder-platforms/hardware-gateways.md) | Yacht Devices, Actisense, ShipModul, Digital Yacht, Quark-elec, Vesper Cortex, em-trak, Venus OS, OpenPlotter, BBN, GoFree/Navico, Raymarine, Garmin, Furuno |
| [crowd-networks.md](forwarder-platforms/crowd-networks.md) | Pocket Mariner, MarineTraffic/VesselFinder apps, AISHub, aiscatcher.org, Airframes, aprs.fi, MSSIS, and other networks as partnership targets |

OpenCPN is covered in [receiving-stations/forwarders.md](receiving-stations/forwarders.md) §8: it relays `!AIVDM` to a remote UDP destination natively (Connections → UDP output, output filter to AIS).

## Headline findings

- **Seven platforms forward today with zero cooperation**, all needing only a setup-guide page: OpenCPN (UDP output connection), qtVlm (NMEA proxy with "retransmit all"), ShipPlotter (UDP/IP remote targets — the exact mechanism MarineTraffic feeders use), SDRangel (AIS Demod → UDP, NMEA format), AIS-catcher for Android (two configurable UDP outputs; sideload-only since its 2024 Play Store delisting), and any Signal K host — which now confirmed includes Victron Venus OS Large on a Cerbo GX, OpenPlotter, and Bareboat Necessities. Expedition very likely joins them ("UDP to IP address" per-connection output) pending an in-app test.
- **Every mass-market phone app is an inbound-only NMEA client.** Navionics (9M+ installs), Aqua Map, iNavX, iSailor, SEAiq, TZ iBoat, savvy navvy (3M+), NV Charts: all connect *into* a WiFi gateway to display AIS; none can push data out to a configurable host. No config trick unlocks them — only vendor feature work.
- **The precedent we need already exists, three times.** TimeZero desktop has a "share AIS data with the community" toggle feeding its MarineTraffic-powered layer from the user's own receiver; Weather4D runs its own server feeding user-received AIS into AISHub; Pocket Mariner (Boat Beacon) forwards its network to AISHub, MarineTraffic, and ShipFinder. "App vendor uplinks user AIS to a third-party aggregator" is an established pattern — the pitch to every other vendor is "add aiscast as a destination," not "invent something new."
- **One hardware line can push to the internet by itself**: Yacht Devices (YDWG-02/YDEN-02/YDNR-02) ships an "outgoing connection" mode — outbound TCP or addressed UDP to a configured remote address with auto-reconnect. Every other gateway surveyed (Actisense, ShipModul, Digital Yacht, Quark-elec, Vesper, em-trak) and every MFD ecosystem (Navico GoFree — now settled server-only by spec text, Raymarine, Garmin, Furuno) is a LAN server only, which a Pi running AIS-catcher/Signal K already covers. Open question on Yacht Devices: whether the destination field takes a hostname or only a numeric IP.
- **Feeder acquisition beats app integration on effort-per-station.** The largest pools of people already forwarding AIS are AIS-catcher installs (1,259 stations on aiscatcher.org, multi-homing by design) and AISHub/MarineTraffic feeders running AIS Dispatcher or ShipPlotter — all one config line away from adding aiscast. Docs PRs (AIS-catcher docs, docker-shipfeeder) and the contributor guide reach them; no vendor negotiation involved.
- **Best partnership conversations**: Airframes (open-leaning multi-domain tracking community, AIS-catcher HTTP ingest already live, marine API not yet public — a genuine data-exchange candidate), then the three precedent-setters above (TimeZero, Weather4D, Pocket Mariner), then savvy navvy (scale plus a demonstrated appetite for integrations via its Actisense partnership).

## Verdict matrix

| Platform | Verdict | The move |
|---|---|---|
| OpenCPN, qtVlm, ShipPlotter, SDRangel, AIS-catcher Android | FORWARD-TODAY | per-platform setup guide |
| Signal K hosts: Venus OS Large, OpenPlotter, BBN, iKommunicate(?) | FORWARD-TODAY | mention in Signal K plugin docs |
| Expedition | FORWARD-TODAY (verify) | in-app WAN test, then guide |
| Yacht Devices gateways | BUILDABLE | verify hostname support; vendor ask if not |
| Coastal Explorer / Nemo | BUILDABLE / INQUIRY | document LAN-relay recipe; ask Rose Point for remote unicast |
| TimeZero, Weather4D, Pocket Mariner | INQUIRY (warm — pipe exists) | "add aiscast as a destination" |
| savvy navvy, Aqua Map, SEAiq | INQUIRY (receptive vendors) | feature request / feed offer |
| Navionics, iNavX, iSailor, TZ iBoat, Orca, NV Charts, Adrena, WinGPS, SeaPro, Actisense, Navico partner API, Raymarine | INQUIRY (cold, low priority) | batch later |
| Airframes, aiscatcher.org | PARTNER-INQUIRY | data-exchange talk; docs PRs |
| ShipModul, Digital Yacht WLN/NavLink2, Quark-elec, Vesper, em-trak, GoFree, Garmin, Furuno, PolarView | DEAD-END | covered by the LAN-computer path or abandoned |

## Plan

### 1. Document what works today (no dependencies)

Lead with placement in existing documentation rather than writing our own — the per-doc list with edit mechanisms (PR vs open wiki vs author outreach) is in [forwarder-platforms/existing-docs.md](forwarder-platforms/existing-docs.md). Direct-edit targets: our own Signal K blog post, AIS-catcher docs, docker-shipfeeder, AIS-catcher for Android, OpenPlotter docs, SDRangel readme, AISdispatcher.pl. Outreach targets: SARCNET's aggregator list (best fit), Pi Stack, rtl-sdr.com, qtVlm, Panbo, David Burch, Expedition forum, ShipPlotter.

Our own guides then only fill what's left:

- [ ] Setup pages for platforms whose docs won't carry the detail: OpenCPN, qtVlm, ShipPlotter, SDRangel, AIS-catcher for Android — each a screenshot-and-three-fields recipe pointing at `ais.openwaters.io:10110` UDP.
- [ ] Signal K plugin docs: add "runs on Venus OS Large (Cerbo GX), OpenPlotter, Bareboat Necessities" with the Venus OS Large install path.
- [ ] A "my plotter/app can't forward" page: the LAN-computer recipe (Pi + AIS-catcher `-t`/`-e` or Signal K) for Vesper/em-trak/GoFree/MFD owners, per receiving-stations research.

### 2. Verify, then document

- [ ] Expedition: test "UDP to IP address" across WAN with an AIS input; publish guide if it works.
- [ ] Yacht Devices outgoing connection: confirm hostname vs numeric-IP in current firmware (manual re-read with a real PDF tool, forum, or vendor email — see inquiries).
- [ ] iKommunicate: confirm whether its embedded Signal K server takes plugins/outbound connections.
- [ ] Follow-up reads flagged by extraction failures: Actisense W2K-2 manual, Digital Yacht manuals, Quark-elec manual, Raymarine LightHouse operating manual, Furuno third-party-integration PDF (403'd), MacArthur HAT.

### 3. Build (small, high leverage)

- [ ] PR to AIS-catcher docs / community sinks list adding aiscast as a documented destination; issue on AIS-catcher-for-Android proposing an aiscast preset beside the Boat Beacon/OpenCPN ones.
- [ ] PR to sdr-enthusiasts/docker-shipfeeder docs listing aiscast in the aggregator table (`UDP_FEEDS=ais.openwaters.io:10110` already works).
- [ ] No new software: every BUILDABLE case reduces to either a docs recipe (socat/AIS-catcher relay for Coastal Explorer's LAN broadcast) or a vendor fix (Yacht Devices hostname).

### 4. Inquiries (drafts in [forwarder-platforms/inquiries.md](forwarder-platforms/inquiries.md); send only on explicit go-ahead)

Wave 1 — warm, the pipe already exists: TimeZero/Nobeltec, Weather4D, Pocket Mariner, Yacht Devices, Airframes.
Wave 2 — receptive vendors, feature ask: savvy navvy, Aqua Map (GEC), SEAiq, Rose Point, Actisense.
Wave 3 — cold/large, batch when there's traction to point at: Navionics/Garmin, iNavX, iSailor, Orca, NV Charts, Adrena, WinGPS, SeaPro, Navico Dev Portal, Raymarine.

The pitch, one paragraph: aiscast is an open AIS aggregation service; your app already receives AIS from users' own receivers; three vendors (TimeZero, Weather4D, Pocket Mariner) already uplink user AIS to aggregators; adding aiscast is a UDP/HTTP destination away, and your users get an open feed and coverage map back rather than a one-way donation.
