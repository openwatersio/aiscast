# Mobile navigation apps as aiscast forwarders

Researched 2026-08-24. Question: which iOS/Android nav apps already have live AIS flowing through them (from an onboard receiver over WiFi NMEA, not internet AIS), and can any forward it to aiscast?

## Navionics Boating (Garmin)

1. **Ingests live AIS via WiFi/hardware:** Yes. Connects to a WiFi NMEA/AIS gateway; two known configs — UDP port 2000 (IP 0.0.0.0/blank) or TCP port 39150 (IP 192.168.15.1) depending on gateway. Menu path: Menu > Paired Devices > add device (Host/Port/Protocol TCP or UDP) > Save; then Menu > Map Options > AIS Settings > Display AIS Targets. ([Quark-elec forum](https://www.quark-elec.com/forums/topic/a031-wifi-output-to-navionics-boating-app-on-android-devices-solved/), [Yacht Devices](https://www.yachtd.com/news/navonics_app_sonarchart_live.html), [Garmin support](https://support.garmin.com/en-US/?faq=Glmk98lO4y4Zq0upTLnlm8))
2. **Can output/forward NMEA to a user destination:** No evidence found. Navionics is a WiFi *client* only (connects into a gateway's network); no setting to push data out to an arbitrary IP/port. (unverified beyond absence of evidence)
3. **Plugin/SDK:** Garmin offers a "Navionics Web API Developer Request" form, but this is for chart/map data API access, not an NMEA/AIS ingestion SDK. ([Garmin dev form](https://www.garmin.com/en-US/forms/navionics-web-api/))
4. **Crowd-source/upload today:** No dedicated built-in AIS upload feature found (Navionics displays AIS from local gateway or from paired Boat Beacon; doesn't appear to run its own AIS crowd-sourcing network). (unverified)
5. **Contact:** navionics.support@garmin.com; App menu > Submit Feedback. ([Garmin support](https://support.garmin.com/en-US/navionics/faq/l4YITQGhaW2NX9MGHansVA/))
6. **User base:** 9M+ installs on Google Play, 48K ratings, 2.8★ average. ([Play Store](https://play.google.com/store/apps/details?id=it.navionics.singleAppMarineLakesHD)) Owned by Garmin — by far the largest boating chartplotter app.
7. **Verdict: INQUIRY.** Huge install base but no user-facing output feature; would require Garmin/Navionics cooperation to add a "forward to aiscast" destination. Contact via navionics.support@garmin.com or the Navionics Web API developer form.

## Aqua Map (GEC / Globalmap)

1. **Ingests live AIS via WiFi:** Yes, extensively documented. Receives NMEA-0183 over TCP or UDP, or Signal K. Displays AIS targets from an onboard receiver connected via a WiFi gateway. Example config: Data format NMEA, Protocol UDP, Port 10110. TCP allows 1 device; UDP allows up to 7 simultaneous devices. Settings path: advanced features > "Wifi connections (NMEA)". ([Aqua Map support](https://www.aquamap.app/support/17-strumenti-avanzati/35-connessioni-wifi-nmea), [globalaquamaps.com](http://www.globalaquamaps.com/blog/Aqua_Map_NMEA.html))
2. **Output/forward to a destination:** Not found in documentation surveyed — configuration described is Aqua Map acting as a WiFi *client* pulling from a gateway. No "output"/"repeater" setting located. (unverified — worth a direct follow-up with GEC support since Aqua Map already consumes Boat Beacon AIS Share data, suggesting some interop exists)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** Integrates with Boat Beacon's AIS Share (Pocket Mariner) as a receiver, not an independent uploader. ([Pocket Mariner blog](https://pocketmariner.com/2023/11/10/using-aquamap-with-boat-beacon-ais-sharing/))
5. **Contact:** support@aquamap.app / info@aquamap.app; company GEC SRL, info@gec-it.com, Viareggio, Italy. ([Aqua Map support](https://www.aquamap.app/support), [GEC](https://www.gec-it.com/))
6. **User base:** ~290K downloads on Google Play, 860 ratings, 3.32★. ([AppBrain](https://www.appbrain.com/app/aqua-map-boating/com.gec.MarineApp.WorldViewerLite)) Mid-tier but well-regarded among cruisers for its NMEA/AIS integration depth ([Panbo](https://panbo.com/aqua-map-master-adds-ais-wifi-instrument-data-usace-surveys-and-route-explorer/)).
7. **Verdict: INQUIRY** (leaning BUILDABLE). No confirmed output feature, but GEC is a small, apparently responsive shop (direct support email, active development — v44.1 released May 2026) so a partnership/feature-request inquiry is plausible. Ask specifically whether Aqua Map's NMEA layer can add a UDP forward-out target.

## iNavX

1. **Ingests live AIS via WiFi:** Yes — "TCP/IP NMEA Client" (iOS only): enter host IP/port of an NMEA/AIS data server, TCP or UDP, then "enable Link". Works with WiFi AIS transponders (e.g., Digital Yacht iAISTX Plus). ([iNavX FAQ](https://inavx.com/tcpip-nmea-client))
2. **Output/forward to a destination:** Not found — iNavX's networking is described purely as an inbound TCP/IP NMEA *client*. (unverified)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** No built-in AIS-sharing/upload network found.
5. **Contact:** iNavX / GPSNavX — contact form at inavx.com (no direct email surfaced in this pass). (unverified)
6. **User base:** iOS: ~4.76–4.8★, ~19.4K–21K ratings (large, long-established niche app). Android: ~410K downloads, 3.18★/900 ratings.
7. **Verdict: INQUIRY.** Established app, iOS-heavy, respected among sailors, but no evidence of an output capability — would need direct developer engagement.

## iSailor (Wärtsilä/Transas)

1. **Ingests live AIS via WiFi:** Yes. AIS is a paid in-app unlock ($9.99). Requires onboard AIS receiver sending data over WiFi. iOS: Settings > AIS > configure WiFi source protocol (TCP=single device, UDP=multi-device), IP, port, name, "Auto-Join" ON. Android: Tools > Sensors > AIS & NMEA Connections > Add Connection. Passes standard VDM sentences for Class A/B targets. ([isailor.us FAQ](http://www.isailor.us/oldfaq/), [Kerlee blog](https://kerlee.com/blog/?p=586))
2. **Output/forward to a destination:** No evidence of an outbound/forwarding feature; purely a client. (unverified)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** None found.
5. **Contact:** isailor.support@wartsila.com; dev site isailor.us. ([appcontacter.com](https://appcontacter.com/contact/com.transas.uninav.plotter/w%C3%A4rtsil%C3%A4-isailor))
6. **User base:** Actively maintained in 2026 (iOS update as recent as Aug 2026), but no reliable install/rating counts surfaced. (unverified)
7. **Verdict: INQUIRY.** Corporate-owned (Wärtsilä), commercial/professional-leaning; approach via isailor.support@wartsila.com.

## SEAiq / SEAiq Open

1. **Ingests live AIS via WiFi:** Yes, extensively — Settings > NMEA & AIS > WiFi: Connection Type TCP (default) or UDP, Host, Port. There's also a separate "AIS Network Feed" (Settings > AIS & NMEA / Extra) for pulling a *second*, TCP-only, internet-based AIS feed (e.g., a base station) as a supplementary source. ([SEAiq NMEA & AIS](https://doc.seaiq.com/NMEAHelp.html), [WiFi settings](https://doc.seaiq.com/NMEA_WiFiHelp.html), [AIS Network Feed](https://doc.seaiq.com/AISExtraHelp.html))
2. **Output/forward to a destination:** Partial. SEAiq has an outbound "NMEA Server" (Server Enable / Server Port) — **but the documentation explicitly states it does NOT forward AIS data, only GNSS data (position/course/speed)**. So it cannot relay received AIS targets to aiscast. ([SEAiq NMEA & AIS](https://doc.seaiq.com/NMEAHelp.html))
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** Yes — "SEAiq AIS Sharing": app both receives a crowdsourced global AIS feed and (in SEAiq Pilot) can "Transmit Own-Ship" data into that shared network. If you operate a shore AIS base station, SEAiq's team will manually add your feed to their server on request: email info@seaiq.com with your station's IP and TCP/UDP port. "AIS Network feed data will not be sent to AIS sharing." ([AIS Sharing](https://doc.seaiq.com/AISShareHelp.html))
5. **Contact:** support@seaiq.com, sales@seaiq.com, info@seaiq.com; developer Sakhalin. ([seaiq.com/contact.html](http://seaiq.com/contact.html))
6. **User base:** No reliable aggregate found; multiple App Store variants (SEAiq, SEAiq USA, SEAiq Open, SEAiq Pilot) split the base — Pilot targets professional river/harbor pilots. (unverified)
7. **Verdict: INQUIRY.** The one app that already runs a working base-station AIS ingestion pipeline (email info@seaiq.com to add a feed). Best avenue: propose aiscast as one of their inbound network feeds, or ask them to extend their NMEA server to include AIS since the plumbing exists for GNSS-only output.

## TimeZero iBoat (Furuno/Nobeltec)

1. **Ingests live AIS via WiFi:** Yes. Menu > Initial Setup > "Connect To NMEA Gateway" — enter IP/port; compatible with any WiFi gateway sending NMEA0183 over TCP or UDP. Requires an active TZ iBoat Essential subscription. ([TZ iBoat NMEA Gateway](https://userguide.mytimezero.com/tz-iboat/InstrumentsConnection/NMEA_Gateway.htm), [digitalyacht.net FAQ](https://digitalyacht.net/hrf_faq/tz-iboat/))
2. **Output/forward to a destination:** Only a narrow autopilot-oriented output ($IIAAM/$IIRMB/$IIXTE for route steering), not a general AIS/position repeater. ([TZ iBoat FAQ](https://mytimezero.com/tz-iboat/faq))
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** None found.
5. **Contact:** Nobeltec, Inc. (Furuno-owned); via mytimezero.com support channels. (unverified — no direct email surfaced)
6. **User base:** Android edition only launched with TZ iBoat v4 in June 2026; Android base still small (~4.7K downloads). iOS is the established platform.
7. **Verdict: INQUIRY.** No general-purpose output; niche but professionally regarded (Furuno-backed).

## Weather4D Routing & Navigation

1. **Ingests live AIS via WiFi:** Yes. Full NMEA interface compatible with WiFi AIS/NMEA gateways (Digital Yacht iAIS/WLN10-20-30/NavLink/AIT series and others). ([digitalyacht.net](https://digitalyacht.net/2018/09/04/weather-4d/))
2. **Output/forward to a destination:** Partial/unclear. The app added "NMEA data output for external instruments" (e.g., LWY leeway sentences) aimed at autopilots/instruments, not a general AIS repeater. (unverified — needs vendor confirmation)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** **Yes, notably** — Weather4D operates its own server integrated as a station directly into the **AISHub network**, letting users share received AIS targets plus own position/route and weather/hydro sensor readings in real time. A functioning existing precedent for exactly the integration we want. ([Navigation-Mac.fr](https://www.navigation-mac.fr/weather4d-et-sailgrib-ameliorent-le-reseau-ais/?lang=en), [Android-Marine.fr](https://www.android-marine.fr/en/weather4d-et-sailgrib-ameliorent-le-reseau-ais/))
5. **Contact:** Contact form at weather4d.com/contact; developer/author Francis Fustier (blog.francis-fustier.fr). ([weather4d.com/contact](https://www.weather4d.com/contact/))
6. **User base:** No reliable count found — long-running, French-developed, niche but respected among offshore/routing sailors. (unverified)
7. **Verdict: INQUIRY (high value).** Already built and operates AIS crowd-sourcing integrated with AISHub — the strongest existing precedent among mobile apps for "app uploads onboard AIS to a third-party network." Ask about adding aiscast as an additional upstream, potentially reusing their AISHub-station code path.

## Orca

1. **Ingests live AIS via WiFi/hardware:** Yes, but primarily through Orca's own hardware — "Orca Core" (NMEA 2000/WiFi/Bluetooth hub) connects to an onboard AIS receiver and streams targets to the Orca app/Display. Without Orca Core, the app pulls internet AIS from MarineTraffic instead. ([Orca boat network guide](https://getorca.com/blog/boat-network-guide/), [Orca help center](https://help.getorca.com/en/articles/8820294-how-do-i-view-other-vessels-in-the-chart))
2. **Output/forward to a destination:** No user-facing NMEA output/forwarding found; architecture is hub → app, one direction. (unverified)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** None found.
5. **Contact:** hello@getorca.com. ([Orca support](https://getorca.com/support/))
6. **User base:** No reliable aggregate found. (unverified)
7. **Verdict: INQUIRY.** A forwarder would have to be built into Orca Core firmware/app by them.

## savvy navvy

1. **Ingests live AIS via WiFi:** Yes — "NMEA Connect" (launched with an Actisense partnership). User enters IP, port, protocol (TCP single device, UDP up to 7) on the same WiFi as an NMEA gateway (e.g., Actisense W2K-2, WGX-1). Pulls AIS plus wind/depth/engine/heading/SOG/COG. ([Actisense case study](https://actisense.com/case_study/savvy-navvy-brings-nmea-connectivity-to-millions-of-boaters-worldwide/), [savvy navvy help](https://help.savvy-navvy.com/en/article/how-to-connect-your-nmea-device-with-savvy-navvy-vvt3hn/), [GPS World](https://www.gpsworld.com/savvy-navvy-launches-nmea-connect-for-integrated-onboard-navigation/))
2. **Output/forward to a destination:** No evidence found — NMEA Connect is inbound only. (unverified)
3. **Plugin/SDK:** None found; the Actisense partnership suggests openness to vendor integrations generally.
4. **Crowd-source/upload:** No dedicated AIS crowd-sourcing beyond combining onboard + internet AIS for display.
5. **Contact:** help.savvy-navvy.com support portal (ticket-based). (unverified)
6. **User base:** 3M+ downloads claimed by savvy navvy; Android 4.22★/2.6K ratings, iOS ~4.6★. ([savvy-navvy.com](https://www.savvy-navvy.com/retail/brg-01)) One of the largest and fastest-growing apps in this list.
7. **Verdict: INQUIRY (high value — scale plus demonstrated openness to NMEA partnerships).** Pitch aiscast as an NMEA Connect output feature.

## Boat Beacon and SeaNav (Pocket Mariner)

1. **Ingests live AIS via WiFi/hardware:** Yes. Boat Beacon connects to WiFi NMEA/AIS gateways; SeaNav gets its local AIS feed through Boat Beacon (shared local link) or directly via WiFi NMEA; both support Port/Protocol(UDP or TCP)/address config. ([Pocket Mariner Android setup](https://pocketmariner.com/2025/06/16/boat-beacon-ais-on-your-mfd-android-instructions/), [SeaNav overview](https://mwm.ai/apps/seanav/857841271))
2. **Output/forward to a destination:** Boat Beacon's "AIS Share" forwards NMEA over TCP, but **only to a local loopback/LAN target** (127.0.0.1 or the device's own WiFi IP, default port 5353) for compatible apps (Navionics, iNavX, AquaMap, OpenCPN) on the same device/network — not to an arbitrary internet host. ([Pocket Mariner AIS Sharing](https://pocketmariner.com/2019/12/06/boat-beacon-real-time-internet-ais-sharing-navionics-inavx-opencpn-etc/))
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** **Yes, significant** — Pocket Mariner operates its own internet AIS network ("60,000+ live ship positions"), fed by their own shore receivers and users' Boat Beacon AIS Share. Per their support pages, **Pocket Mariner already forwards AIS data to AISHub, MarineTraffic, and ShipFinder**, and can set up a dedicated port for a station. ([Pocket Mariner](https://pocketmariner.com/tag/marine-navigation/))
5. **Contact:** Pocket Mariner via pocketmariner.com contact/support pages. (unverified — no direct email surfaced)
6. **User base:** Boat Beacon Android: ~20K downloads (iOS-first app, $14.99). Small compared to Navionics/savvy navvy, but valuable as a willing redistribution partner.
7. **Verdict: INQUIRY (warm lead).** They already run the exact redistribution relationships we want (they feed AISHub/MarineTraffic/ShipFinder) — approach them to add aiscast as another downstream partner.

## NV Charts app

1. **Ingests live AIS via WiFi:** Yes. Works with any AIS receiver sending NMEA 0183 over WiFi TCP/IP; tested with Actisense W2K-1 and Weatherdock. Settings: enter AIS router's IP and Port. ([nvcharts.com blog](https://nvcharts.com/blog/ais-in-der-nv-charts-app), [digitalyacht.de](https://digitalyacht.de/hrf_faq/nv-charts/))
2. **Output/forward to a destination:** No evidence found. (unverified)
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** None found.
5. **Contact:** Developer Plongo for NV Chart Group GmbH; contact form at us.nvcharts.com/us/contact or eu.nvcharts.com.
6. **User base:** ~190–200K downloads on Google Play, 1.6K ratings. Mid-tier, Europe-centric.
7. **Verdict: INQUIRY (low priority).**

## Garmin ActiveCaptain

1. **Ingests live AIS via WiFi/hardware:** Unclear/likely not applicable. ActiveCaptain is a companion app pairing with a Garmin MFD over the Garmin Marine Network for updates/chart sync/community data — not documented as a standalone NMEA/AIS WiFi client for third-party receivers. (unverified)
2. **Output/forward to a destination:** Not found; it mirrors data from a paired Garmin MFD rather than acting as an AIS gateway.
3. **Plugin/SDK:** None found.
4. **Crowd-source/upload:** Runs a marina/anchorage community-data platform, unrelated to AIS positions.
5. **Contact:** support.garmin.com.
6. **User base:** 1M+ Google Play downloads, 4.20★, but tied to owning Garmin hardware.
7. **Verdict: INQUIRY (low priority / likely out of scope).**

## AIS-catcher for Android (jvde-github)

1. **Ingests live AIS via hardware:** Yes, directly — turns an Android device + RTL-SDR/Airspy dongle (USB-OTG) into a portable dual-channel AIS receiver itself, decoding off the air. ([GitHub](https://github.com/jvde-github/AIS-catcher))
2. **Output/forward:** **Yes — confirmed, its designed purpose.** Settings (⋮) exposes two independently configurable UDP output connections with editable destination address and port (presets exist for Boat Beacon port 10110 and OpenCPN port 10111, but host/port are freely editable). ([Pocket Mariner setup guide](https://pocketmariner.com/2025/06/16/boat-beacon-ais-on-your-mfd-android-instructions/), [GitHub repo](https://github.com/jvde-github/AIS-catcher-for-Android))
3. **Plugin/SDK:** N/A — open source (GPL-3.0).
4. **Crowd-source/upload:** None built in; purely a local decoder/forwarder.
5. **Contact:** jvde-github via GitHub Issues on AIS-catcher-for-Android.
6. **User base:** **Removed from Google Play ~2024-07-11** (Google's developer-address-publication requirement); ships as a sideloaded APK from GitHub Releases and the IzzyOnDroid repo. Enthusiast tool, not mass-market. ([GitHub Releases](https://github.com/jvde-github/AIS-catcher-for-Android/releases))
7. **Verdict: FORWARD-TODAY.** Exact config: Settings > enable a UDP output > Address `ais.openwaters.io`, Port `10110`, UDP. Distribution friction (sideload, needs SDR hardware) caps the audience, but it works with zero vendor involvement. Worth a "power user" guide and a GitHub issue suggesting an aiscast preset alongside the Boat Beacon/OpenCPN ones.

## Summary table

| App | Ingests AIS/WiFi | Can forward to custom dest today | Crowd-sources AIS already | Verdict |
|---|---|---|---|---|
| Navionics Boating | Yes | No | No | INQUIRY |
| Aqua Map | Yes | Not found | Via Boat Beacon only | INQUIRY |
| iNavX | Yes | No | No | INQUIRY |
| iSailor | Yes | No | No | INQUIRY |
| SEAiq | Yes | Partial (GNSS only, not AIS) | Yes (SEAiq AIS Sharing) | INQUIRY |
| TimeZero iBoat | Yes | No (autopilot sentences only) | No | INQUIRY |
| Weather4D | Yes | Unclear (instrument output only) | **Yes — feeds AISHub directly** | INQUIRY (high value) |
| Orca | Yes (via Orca Core) | No | No | INQUIRY |
| savvy navvy | Yes | No | No | INQUIRY (high value, scale) |
| Boat Beacon / SeaNav | Yes | LAN-only relay | **Yes — feeds AISHub, MarineTraffic, ShipFinder** | INQUIRY (warm lead) |
| NV Charts | Yes | No | No | INQUIRY (low priority) |
| Garmin ActiveCaptain | Unclear/likely no | No | No | INQUIRY (low priority) |
| AIS-catcher for Android | Yes (SDR hardware) | **Yes, confirmed** | No | **FORWARD-TODAY** |

## Cross-cutting observations

- Every mass-market chartplotter app implements NMEA-over-WiFi as an **inbound-only client** — none expose a "forward everything I receive to this server" setting. The efficient path to unlocking any of them is a feature request/partnership, not a config trick.
- Two vendors already operate real AIS-redistribution pipelines and are the strongest partnership leads: **Weather4D** (feeds AISHub directly) and **Pocket Mariner/Boat Beacon** (feeds AISHub, MarineTraffic, ShipFinder). Both prove "app vendor redistributes user-received AIS to a third-party aggregator" is an established pattern to point at when pitching aiscast.
- **AIS-catcher for Android** is the only mobile forwarder usable today with zero cooperation, capped by SDR-hardware requirement and Play Store delisting.
- **savvy navvy** is the best cold-outreach target on scale (3M+ downloads, actively building NMEA partnerships via Actisense).
