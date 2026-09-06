# Desktop and commercial navigation software as aiscast forwarders

Researched 2026-08-24. OpenCPN is covered in [receiving-stations/forwarders.md](../receiving-stations/forwarders.md) §8 (verdict: forwards `!AIVDM` to remote UDP natively). This file covers the rest of the desktop field.

## Coastal Explorer (Rose Point Navigation Systems)

1. **Live AIS ingest**: Yes. NMEA 0183 (serial or network) and NMEA 2000 via Actisense/Nemo Gateway, including AIS receivers/transponders. ([Coastal Explorer Reference](https://manualzz.com/doc/6732406/coastal-explorer-reference))
2. **NMEA output/forward**: Yes, via "Add a Network Port" → "NMEA 0183 Over UDP" or a TCP "Data Server." Default output sentence set includes AIVDM/AIVDO passthrough. **Caveat**: per the CE manual, "the address box is not used as UDP is always broadcast" — native UDP output is **LAN-broadcast only**, not unicast to a remote IP. The TCP option is a server other software connects *into*, which doesn't cross NAT to a cloud endpoint. ([CE forum](https://www.coastalexplorer.net/forums/support/topics/84575), [getting_started.pdf](https://www.rosepoint.com/support/legacy/coastal-explorer/getting_started.pdf))
3. **Plugin/SDK**: None publicly documented; Rose Point does OEM collaborations. (unverified)
4. **Feeds an AIS network today**: Not found.
5. **Contact**: Rose Point Navigation Systems, Redmond WA; community.rosepoint.com; named contact surfaced (joe@rosepoint.com, 425-605-0985 — verify before outreach). (unverified)
6. **User base**: Dominant U.S. commercial/inland-tow ECS vendor plus large recreational trawler/cruiser following.
7. **Verdict: BUILDABLE / INQUIRY.** A local relay (or aiscast's own bridge) listening for CE's LAN broadcast on 10110 and re-sending unicast would work today; otherwise ask Rose Point for a remote-unicast destination in the Data Server config.

## Rose Point Nemo Gateway

1. **Live AIS ingest**: Yes — NMEA 0183 listener ports (incl. AIS devices) plus NMEA 2000, merged. ([seabits.com](https://seabits.com/nemo-gateway-easy-nmea-networking-for-your-boat/))
2. **NMEA output/forward**: UDP stream of NMEA 0183 on port 10110 by default with per-port sentence selection — but documented as LAN streaming to "multiple PCs, tablets, phones," not a configurable remote unicast target. ([Configuration Guide PDF](https://www.rosepoint.com/nemo-gateway/support/Configuration%20Guide.pdf))
3. **Verdict: INQUIRY** — same ask as Coastal Explorer.

## TimeZero TZ Navigator / TZ Professional (Nobeltec / MaxSea / Furuno)

1. **Live AIS ingest**: Yes — NMEA 0183 serial, NMEA 2000, Furuno NavNet, and network (UDP/TCP) connections including AIS. ([Manual Port Configuration](https://userguide.mytimezero.com/tz-navigator/Manual_Port_Configuration.htm))
2. **NMEA output/forward**: Two features, both limited: "External Output" is autopilot sentences only (APB/XTE/RMC/AAM/GGA/GLL/MWV — no AIVDM) ([Autopilot_or_External_Output.htm](https://userguide.mytimezero.com/tz-professional/Autopilot_or_External_Output.htm)); "Network Repeater" broadcasts nav data **and AIS** to other TimeZero instances over UDP port 31000, but docs show LAN broadcast only and the wire format is unconfirmed (possibly proprietary, not raw AIVDM). ([Network_Repeater.htm](https://userguide.mytimezero.com/tz-navigator/Network_Repeater.htm)) (unverified)
3. **Plugin/SDK**: None found.
4. **Feeds an AIS network today**: **Yes, confirmed.** TZ Online AIS: "When enabled, TimeZero will automatically share AIS data with the community if connected to a local AIS receiver" — TimeZero already crowdsources user-received AIS into its MarineTraffic-powered community layer, anonymized. The exact pipe aiscast wants already exists in the product, pointed at MarineTraffic. ([TZ Professional AIS Online](https://userguide.mytimezero.com/tz-professional/AIS_Online.htm))
5. **Contact**: mytimezero.com/contact; Nobeltec US (503) 579-1414; MaxSea International, Bidart, France.
6. **User base**: One of the largest commercial-fishing/professional and recreational nav-software vendors in North America/Europe.
7. **Verdict: INQUIRY (highest value in this file).** The anonymized AIS-uplink plumbing exists; the ask is adding aiscast as an additional feed destination.

## Expedition (sailing/racing nav)

1. **Live AIS ingest**: Yes — serial and network (UDP/TCP) instrument connections including AIS. ([Expedition forum](https://expedition.boardhost.com/viewtopic.php?id=201))
2. **NMEA output/forward**: Yes — per-connection "Broadcast" option: "UDP" (LAN broadcast) or **"UDP to IP address"** (unicast to a specified IP), port configurable, forwarding incoming data including AIVDM. ([forum: "Copy on UDP port"](https://expedition.boardhost.com/viewtopic.php?pid=6180), [zapfware setup guide](https://www.zapfware.de/nmearemote/expedition/how-to-setup-expedition/))
3. **Feeds an AIS network today**: Not found.
4. **Contact**: Expedition Marine (Nick White); expedition.boardhost.com forum.
5. **User base**: Dominant in offshore/grand-prix sailboat racing; small but influential.
6. **Verdict: FORWARD-TODAY (likely).** Configure the AIS connection's output as "UDP to IP address" → `ais.openwaters.io:10110`. WAN behavior unverified from docs alone — needs an in-app test before publishing a guide.

## qtVlm (Meltemus)

1. **Live AIS ingest**: Yes — NMEA multiplexer with serial/TCP/UDP/GPSD inputs; parses AIVDM/AIVDO. ([David Burch — AIS in qtVlm](http://davidburchnavigation.blogspot.com/2022/05/qtVlm-AIS.html))
2. **NMEA output/forward**: **Yes, documented proxy feature.** "Configure qtVlm as NMEA proxy": activate the outgoing channel, choose UDP (TCP output requires a listening server), enable "retransmit all" to echo everything received (including AIS) to a remote IP:port — genuine unicast. ([Meltemus forum: NMEA proxy](https://www.meltemus.com/index.php/en/forum/qtvlm-application/208-configure-qtvlm-as-nmea-proxy), [AIS hub thread](https://www.meltemus.com/index.php/en/forum/qtvlm-application/193-ais-hub))
3. **Plugin/SDK**: None; closed-source freeware from Meltemus SAS.
4. **Feeds an AIS network today**: Consumes a Meltemus-run internet AIS feed (blending Sinagot and other sources); not documented as uploading user-received AIS.
5. **Contact**: support@meltemus.com / contact@meltemus.com. ([qtVlm documentation](https://download.meltemus.com/qtvlm/qtVlm_documentation_en.pdf))
6. **User base**: Free, cross-platform (Windows/Mac/Linux/iOS/Android/Pi), very popular among cruising and racing sailors, especially Europe.
7. **Verdict: FORWARD-TODAY.** Outgoing UDP channel → `ais.openwaters.io:10110`, "retransmit all", fed by the local AIS input channel. The best-documented working forwarder in this file.

## PolarView NS (Polar Navy)

Product abandoned — vendor stopped supporting it years ago, no active contact, no confirmed forwarding. ([Trawler Forum](https://www.trawlerforum.com/threads/opencpn-vs-polarview-polarnavy.68808/)) **Verdict: SKIP.**

## Adrena

1. **Live AIS ingest**: Yes — NMEA 0183 incl. WiFi/UDP; AIS/MOB features. ([Adrena — instruments](https://www.adrena-software.com/connecter-aux-instruments-de-navigation/))
2. **NMEA output/forward**: "Port out" streams a UDP port with Adrena's computed nav data; AIVDM passthrough unconfirmed. (unverified)
3. **Contact**: adrena-software.com contact form (their instrument page returned HTTP 500 during research).
4. **User base**: French offshore/ocean racing and cruising; moderate, Francophone.
5. **Verdict: INQUIRY** — ask whether Port-out can re-emit received AIVDM.

## WinGPS (Stentec Navigation)

1. **Live AIS ingest**: Yes — AIS via USB/serial or WiFi; has an "NMEA-repeater" function and NMEA Monitor. ([Stentec support](https://www.stentec.com/nl/support/hulpcentrum?id=16&view=question), [NMEA monitor help](https://help.stentec.com/pro14/NMEA_monitor.htm))
2. **NMEA output/forward**: TCP/UDP connections take an IP and port, but the repeater's scope (LAN vs remote unicast, AIS inclusion) is unverified.
3. **Contact**: stentec.com/en/support contact form.
4. **User base**: Popular Dutch/Benelux recreational nav software; moderate regional.
5. **Verdict: INQUIRY** — confirm repeater scope before claiming forward-today.

## SeaPro (Euronav)

1. **Live AIS ingest**: Yes — "any AIS Receiver or Transponder" over NMEA 0183. ([seaPro Standard](https://www.euronav.co.uk/Products/Leisure/seaPro_Standard/seaPro_Standard.html))
2. **NMEA output/forward**: Not documented either way. (unverified)
3. **Contact**: sales@euronav.co.uk / support@euronav.co.uk, +44 (0)23 9298 8806. ([contact](https://www.euronav.co.uk/CompanyInfo/ContactInfo.htm))
4. **Verdict: INQUIRY** (small UK vendor, low priority).

## SmarterTrack (Digital Yacht)

1. **Live AIS ingest**: Yes — bundled free with Digital Yacht AIS hardware; two configurable NMEA inputs (USB or their WLN10/WLN30 WiFi servers). ([Digital Yacht — configure apps](https://digitalyacht.support/tutorials/how-to-configure-apps-software/))
2. **NMEA output/forward**: Not confirmed for the app itself. The interesting angle is Digital Yacht's gateway hardware (WLN10/WLN30, NavLink2) — covered in [hardware-gateways.md](hardware-gateways.md).
3. **Contact**: digitalyacht.support / digitalyachtamerica.com.
4. **Verdict: INQUIRY** — via the hardware side, not the app.

## ShipPlotter (COAA)

1. **Live AIS ingest**: Yes — serial AIS receiver or VHF+discriminator.
2. **NMEA output/forward**: **Yes, proven at internet scale.** UDP/IP output section supports multiple remote targets by IP/DNS name and port — genuine unicast to an arbitrary internet host. This is exactly how MarineTraffic documents feeding from ShipPlotter. ([MarineTraffic ShipPlotter article](https://support.marinetraffic.com/en/articles/9552955-shipplotter))
3. **Feeds an AIS network today**: Yes — official documented MarineTraffic feeder client.
4. **Contact**: support@shipplotter.com (COAA, small operation). ([FAQ](http://www.coaa.co.uk/shipplotterfaq.htm))
5. **User base**: Legacy Windows tool, aging but still used by radio hobbyists and some MarineTraffic/AISHub feeders.
6. **Verdict: FORWARD-TODAY.** UDP/IP output → remote IP/DNS `ais.openwaters.io`, remote port `10110`.

## SDRangel

1. **Live AIS ingest**: Yes — AIS Demodulator channel plugin decodes from an SDR directly. ([demodais readme](https://github.com/f4exb/sdrangel/blob/master/plugins/channelrx/demodais/readme.md))
2. **NMEA output/forward**: **Yes, confirmed.** AIS Demod UI: UDP checkbox, "UDP address" (any host) + "UDP port", format selector set to NMEA. ([demodais readme](https://github.com/f4exb/sdrangel/blob/master/plugins/channelrx/demodais/readme.md))
3. **Contact**: Open source (F4EXB / Edouard Griffiths), github.com/f4exb/sdrangel — no partnership needed, just documentation.
4. **User base**: Popular multi-mode SDR app; big overlap with RTL-SDR AIS hobbyists.
5. **Verdict: FORWARD-TODAY.** AIS Demod → UDP on, address `ais.openwaters.io`, port `10110`, format NMEA (one demod channel per AIS channel). Publish in aiscast setup docs.

## Summary table

| Product | Ingests live AIS | Forward to remote host today | Verdict |
|---|---|---|---|
| OpenCPN | Yes | **Yes** (Connections → UDP output) | FORWARD-TODAY (see receiving-stations/forwarders.md §8) |
| qtVlm | Yes | **Yes** (NMEA proxy, retransmit all) | FORWARD-TODAY |
| ShipPlotter | Yes | **Yes** (UDP/IP remote targets) | FORWARD-TODAY |
| SDRangel | Yes (is a decoder) | **Yes** (AIS Demod UDP NMEA) | FORWARD-TODAY |
| Expedition | Yes | Likely ("UDP to IP address") | FORWARD-TODAY (verify in-app) |
| Coastal Explorer / Nemo | Yes | No (UDP broadcast-only) | BUILDABLE (LAN relay) / INQUIRY |
| TimeZero TZ Nav/Pro | Yes | No (LAN repeater only) | INQUIRY — **already crowdsources AIS to MarineTraffic** |
| Adrena | Yes | Unconfirmed | INQUIRY |
| WinGPS | Yes | Unconfirmed | INQUIRY |
| SeaPro | Yes | Unconfirmed | INQUIRY (low) |
| SmarterTrack | Yes | Unconfirmed | INQUIRY via Digital Yacht hardware |
| PolarView NS | Yes | — | SKIP (abandoned) |
