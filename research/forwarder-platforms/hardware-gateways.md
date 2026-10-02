# NMEA gateway, router, and MFD ecosystems as aiscast forwarders

Researched 2026-08-24. The question for each device: can it push NMEA (including AIVDM) to an arbitrary internet host itself, so a boat or marina can feed aiscast with no extra computer? A LAN-server-only device is a DEAD-END here — not because it can't feed aiscast, but because it needs a LAN computer anyway, and Signal K or AIS-catcher on that computer is already the documented path (see [receiving-stations/](../receiving-stations/)).

Extraction note: several vendor manual PDFs (Yacht Devices, Actisense, ShipModul, Quark-elec, Digital Yacht) resisted automated text extraction, so some (unverified) flags below are extraction failures rather than confirmed absences. A follow-up pass with a real PDF tool would resolve most of them.

## Yacht Devices (YDWG-02 WiFi, YDEN-02 Ethernet, YDNR-02 router)

1. **NMEA network features**: Shared firmware architecture across the line (plus YDWN-02): up to three configurable "NMEA Servers" (TCP and/or UDP, NMEA 0183 or N2K-RAW, custom ports), web-configured. Default Server #1 = TCP 1456. ([YDWG-02 manual](https://www.yachtd.com/downloads/ydwg02.pdf), [YDNR-02 manual](https://www.yachtd.com/downloads/ydnr02.pdf), [YDEN-02 manual](https://www.yachtd.com/downloads/yden02.pdf))
2. **WAN push**: **Yes — "outgoing connection"** (firmware ~1.21 on YDEN-02, ported across the line): Server #2 can initiate an outbound TCP connection or send addressed UDP datagrams to a specified remote address, with retry on drop. Vendor example targets `192.168.1.112:10110`. All documented examples are numeric IPs; whether the field accepts a DNS name is (unverified) — the firmware does DNS/mDNS generally, so it plausibly works or is a small vendor fix. ([Yacht Devices news: outgoing connections](https://www.yachtd.com/news/outgoing_connection.html))
3. **Own cloud**: Yacht Devices Cloud (`upload.yachtd.com`) — batch NMEA 2000 upload with a per-boat key; proprietary destination, not redirectable. ([cloud.yachtd.com/configure](https://cloud.yachtd.com/configure))
4. **Contact**: partnership/OEM Aleksandr Gorlach, agorlach@yachtdevices.com; support at yachtd.com/support.
5. **Verdict: BUILDABLE, near FORWARD-TODAY — the strongest hardware lead.** The outbound TCP/UDP client already ships across three product lines. Verify hostname support (or ask the vendor to add it) and this becomes a documented no-computer feeding path.

## Actisense W2K-1

1. **NMEA network features**: Up to three simultaneous TCP/UDP data servers (default ports 60001–60003), NMEA 0183/2000 per server, web-configured. ([product page](https://actisense.com/products/w2k-1-nmea-2000-wifi-gateway/), [manual](https://actisense.com/wp-content/uploads/2020/01/W2K-1-User-Manual-issue-2.10.pdf) — findings search-snippet-derived, re-verify against the PDF)
2. **WAN push**: No outbound client mode found; "client" in their docs means WiFi station mode. Whether the newer W2K-2 / Actisense-i adds one is (unverified).
3. **Own cloud**: Actisense-i / actisense.cloud — fleet diagnostics, not AIS relay. ([Panbo](https://panbo.com/actisense-adds-nmea-2000-insight-to-w2k-1-with-actisense-i/))
4. **Contact**: sales@actisense.com, +44 (0)1202 746682. Note: Actisense already partners with savvy navvy on NMEA Connect, so they do integrations.
5. **Verdict: DEAD-END as documented; INQUIRY-worthy** — Yacht Devices proves the category supports outbound push, and W2K-2 may already have it undocumented.

## ShipModul MiniPlex-3Wi / MiniPlex-3E-N2K

1. **NMEA network features**: NMEA on fixed port 10110, UDP (default) or TCP, via MPX-Config3 or web page; DHCP client for the boat LAN. ([Miniplex-3 manual](https://www.shipmodul.com/download/miniplex-3-v3.16-en.pdf))
2. **WAN push**: **No native outbound client.** The community workaround is kplex on a Pi in `mode=client` pointed at the remote — external software, not firmware. ([openmarine.net forum](https://forum.openmarine.net/showthread.php?tid=753))
3. **Contact**: sales@shipmodul.com, +31 592 375700.
4. **Verdict: DEAD-END** — the workaround requires the Pi that already solves the problem via Signal K/AIS-catcher.

## Digital Yacht NavLink2, iKommunicate, WLN10/WLN30

1. **NMEA network features**: NavLink2/WLN10/WLN30 are WiFi APs serving NMEA 0183 over TCP/UDP on a fixed local IP (192.168.1.1). iKommunicate is a Signal K server appliance (NMEA 0183/2000 → Signal K over HTTP/WebSockets). ([Digital Yacht support](https://support.digitalyacht.co.uk/product/wln30-smart-wireless-nmea-multiplexer/))
2. **WAN push**: No evidence of an outbound client mode on any of the three. (unverified — manuals not machine-readable this pass)
3. **Contact**: UK sales@digitalyacht.co.uk +44 (0)117 955 4474; US sales@digitalyachtamerica.com +1 (978) 277-1234.
4. **Verdict**: NavLink2/WLN10/WLN30 **DEAD-END**; iKommunicate **INQUIRY** — if its embedded Signal K server accepts plugins/outbound connections like a stock install, the existing aiscast Signal K plugin covers it with no new work.

## Quark-elec (QK-A027-plus and similar)

Setup flow is entirely LAN-local (app connects to the unit's IP); no outbound client mode found. (unverified — manual PDF not machine-readable) **Verdict: DEAD-END** pending a manual re-read. Contact: quark-elec.com/contact-information.

## Vesper Cortex (Garmin)

1. **NMEA network features**: Cortex Hub serves NMEA 0183 over **TCP port 39150 only** (no UDP) to up to 5 direct + 5 boat-network clients. ([Vesper support](https://support.vespermarine.com/hc/en-us/articles/360002009816-Using-Cortex-data-over-WiFi-Apps-and-Navigation-software-settings), [Panbo](https://panbo.com/vesper-cortex-update-boat-networks-squashing-bugs-more/))
2. **WAN push**: None. Third-party forwarding observed in the wild goes Cortex → FloatHub hardware → FloatHub cloud → MarineTraffic.
3. **Own cloud**: Cortex Onboard/Vesper monitoring (Garmin-owned), proprietary.
4. **Verdict: DEAD-END** — a LAN computer pointed at TCP 39150 covers it (and this is one of the easiest boat-electronics sources for that computer to consume; see receiving-stations/dedicated-receivers.md).

## em-trak B92x / B95x WiFi

Access-point/server only: apps connect to `192.168.2.1:5000`. No client mode documented. ([em-trak support](https://em-trak.com/support/b921-b922-b923-b924/)) **Verdict: DEAD-END.**

## Victron Venus OS / Cerbo GX

**FORWARD-TODAY via Signal K.** Venus OS Large (official image variant: Settings → Firmware → Online updates → Image type → Large) bundles an installable Signal K server; admin UI at `http://venus.local:3000`. A standard Signal K install runs the aiscast plugin like any other. Victron docs caveat: Signal K + Node-RED together can overload lower-power Venus GX units; Cerbo GX is fine. ([Victron Venus OS Large docs](https://www.victronenergy.com/live/venus-os:large)) No partnership needed — document it.

## OpenPlotter

**FORWARD-TODAY.** The "SDR VHF" app installs AIS-catcher and auto-wires it into Signal K via UDP 10110. Turnkey Pi image for exactly this use case; covered by existing Signal K plugin docs. ([OpenPlotter AIS docs](https://openplotter.readthedocs.io/3.x.x/sdr-vhf/ais.html))

## Bareboat Necessities (BBN Marine OS)

**FORWARD-TODAY via bundled Signal K** (also bundles kplex, OpenCPN, AvNav). Whether AIS-catcher is preinstalled the way OpenPlotter does it is (unverified). ([BBN OS docs](https://bareboat-necessities.github.io/my-bareboat/bareboat-os.html))

## MacArthur HAT

Pi HAT hardware (NMEA 2000/0183 interface) intended to run under OpenPlotter/Signal K; no distinguishing product documentation surfaced this pass. (unverified) Provisionally **FORWARD-TODAY by inheritance** if confirmed to run the OpenPlotter/Signal K stack.

## Navico / B&G / Simrad / Lowrance — GoFree

1. **Architecture**: GoFree Tier 1 is multicast-announce (`239.2.1.1:2050`), client-pull: apps open a TCP connection **into** the MFD. The spec is explicit: "Two way communication is not currently supported. Data transmitted by mobile devices to the MFDs will be ignored." This settles the question receiving-stations/dedicated-receivers.md left flagged as unverified: **server-only by design.** ([GoFree Tier 1 spec](https://silo.tips/download/gofree-tier-1-data-networking-specification-revision-06), [Panbo](https://panbo.com/navico-gofree-wifi1-the-0183-link/))
2. **Partner track**: Navico Dev Portal (developer.navico.com) exposes partner REST APIs over N2K data plus a Navico Connect integration program — a business-development conversation, not a device config. Contact: api.support@navico.com.
3. **Verdict: DEAD-END** for the protocol; **INQUIRY** on the partner-API track.

## Raymarine Axiom / LightHouse

Installation manuals document RayNet/physical connectivity only; whether LightHouse has an NMEA-over-IP output settings page could not be confirmed from retrievable text (forum threads reference one existing). (unverified) Raymarine runs a developer portal (dev.raymarine.com) and a Radar SDK, so OEM data-access partnerships exist. **Verdict: INQUIRY** after a follow-up read of the full LightHouse operating manual.

## Garmin MFDs (GPSMAP)

NMEA 0183 per serial port; the Garmin Marine Network is proprietary Ethernet sync; WiFi serves ActiveCaptain. No NMEA-over-IP server or client found. ([GPSMAP NMEA 0183 settings](https://www8.garmin.com/manuals/webhelp/GUID-413FE004-9D7D-474E-8423-3B787BC4A5BF/EN-US/GUID-16CE378B-2135-4CEF-B6B3-651C6E810CD3.html)) (unverified as an exhaustive negative — GPSMAP 8000/9000-series manuals deserve a check) **Verdict: DEAD-END** as documented; serial 0183 → LAN computer covers it.

## Furuno TZtouch2 / TZtouch3

MFD runs an internal "NMEA Gateway" **server** (TZ iBoat connects to `10.1.1.1:39150`); no outbound push documented. A Furuno tech doc "Integration with Third Party Devices via Ethernet" returned HTTP 403 and could not be read — fetch it before ruling out. ([TZ iBoat NMEA Gateway docs](https://userguide.mytimezero.com/tz-iboat/InstrumentsConnection/NMEA_Gateway.htm), [Furuno PDF, 403'd](https://www.furunousa.com/-/media/sites/furuno/document_library/technical_info/interfacing_and_installation/interfacing_and_installation/navnet_tztouch3_tzt2bb_integration_with_third_party_devices_via_ethernet.pdf)) **Verdict: DEAD-END** as documented (LAN computer covers it); soft INQUIRY pending that PDF.

## Summary table

| Device / ecosystem | WAN push today | Verdict |
|---|---|---|
| Yacht Devices YDWG-02/YDEN-02/YDNR-02 | **Yes (outgoing connection; hostname support unverified)** | BUILDABLE → near FORWARD-TODAY |
| Victron Venus OS Large / Cerbo GX | Via Signal K install | FORWARD-TODAY |
| OpenPlotter | Via bundled AIS-catcher + Signal K | FORWARD-TODAY |
| Bareboat Necessities | Via bundled Signal K | FORWARD-TODAY |
| MacArthur HAT | Via OpenPlotter stack (unverified) | FORWARD-TODAY (inherit) |
| Actisense W2K-1/W2K-2 | No (as documented) | INQUIRY |
| Digital Yacht iKommunicate | Maybe via embedded Signal K | INQUIRY |
| Navico partner API | N/A (business track) | INQUIRY |
| Raymarine LightHouse | Unknown (docs gap) | INQUIRY (follow-up read) |
| ShipModul MiniPlex | No | DEAD-END |
| Digital Yacht NavLink2/WLN10/WLN30 | No | DEAD-END |
| Quark-elec gateways | No (unverified) | DEAD-END |
| Vesper Cortex | No | DEAD-END |
| em-trak B92x/B95x | No | DEAD-END |
| Navico GoFree Tier 1 | No — by spec | DEAD-END (settled) |
| Garmin MFDs | No | DEAD-END |
| Furuno TZtouch | No (one PDF unread) | DEAD-END |
