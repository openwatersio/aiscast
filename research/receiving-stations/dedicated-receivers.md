# Dedicated AIS receiver hardware and marine-electronics AIS sources

Research compiled 2026-08-22 for a public guide on running a volunteer AIS receiving station. Scope: everything that is *not* "RTL-SDR + AIS-catcher" — purpose-built AIS receivers, shore-station boxes, and the marine electronics a boat already carries. Every claim carries a source link. Prices are as listed on the cited page on 2026-08-22 and move around; treat them as indicative.

The question this document answers is narrow: **what box produces `!AIVDM` sentences, and how do those bytes get onto a network socket?**

## Headline

- The single most interesting product for a volunteer shore station in 2026 is the **Wegmatt dAISy-catcher, $149** — an SDR front-end co-designed with the author of AIS-catcher, sold as a USB stick or a Raspberry Pi HAT, with a claimed sensitivity of **better than −120 dBm @ 20% PER** and beta sites reporting **3–4× more vessels than the dAISy HAT** ([Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)). It closes the "dedicated receiver is less sensitive than an SDR" gap that made RTL-SDR the default recommendation.
- The classic dedicated receivers (dAISy HAT $79, dAISy 2+ $119) are **easier** than SDR but genuinely **less sensitive**; Wegmatt says so on its own product pages — "longer acquisition time, lower sensitivity and less range than some traditional AIS receivers" ([Wegmatt dAISy HAT](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)).
- For a land hobbyist the honest cost ranking is: RTL-SDR ~$30 (cheapest, needs CPU + tuning) → dAISy HAT $79 (simplest) → dAISy-catcher $149 (best performance per watt) → Comar/Digital Yacht ethernet boxes $500–$1,050 (set-and-forget, no computer).
- **Antenna and its height beat every receiver choice.** The AIS-catcher author's own side-by-side survey concluded "your antenna is the most important piece of equipment that affects the number of ships you will receive" and that an Airspy HF+ bought only ~10–15% more messages than an RTL-SDR v3 ([AIS-catcher discussion #333](https://github.com/jvde-github/AIS-catcher/discussions/333)).
- MarineTraffic still gives free receivers (Comar SLR450Ni/SLR400Ni/SLR350Ni and Raspberry-Pi RSK editions) to hosts in uncovered coastal locations; AISHub gives away **no hardware at all** — it is purely a data exchange ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552934-marinetraffic-provided-ais-receivers), [AISHub](https://www.aishub.net/join-us)).

---

## 1. Hobbyist dedicated receivers

### 1.1 Wegmatt dAISy family

Wegmatt LLC (Seattle, USA) is the reference brand for low-cost dedicated AIS receivers. Boards are assembled in the USA with a 12-month warranty ([Wegmatt](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)). Full catalogue and prices from [shop.wegmatt.com](https://shop.wegmatt.com/collections/all).

| Model | USD | GBP (Uputronics) | Channels | Interfaces | Power | Notes |
|---|---|---|---|---|---|---|
| [dAISy — The Original](https://shop.wegmatt.com/products/daisy-ais-receiver) | $79 | [£65.99](https://store.uputronics.com/collections/wegmatt-ais-receivers) | **Single channel with smart channel hopping** (A↔B) | Mini-USB (data+power), BNC antenna, optional 3.3 V TTL serial (needs soldering) | <100 mW (<20 mA @ 5 V) | 38400 baud AIVDM over USB CDC, no drivers on Win/macOS/Linux. **No NMEA 0183 output.** Claims 40–60% more messages than a plain single-channel receiver |
| [dAISy 2+](https://shop.wegmatt.com/products/daisy-2-dual-channel-ais-receiver-with-nmea-0183) | $119 | £113.88 | True dual, A + B simultaneously | Mini-USB, **NMEA 0183 terminal block (4800/9600/38400)**, aux TTL serial header, BNC | <500 mW (<100 mA @5 V, <40 mA @12 V) | Integrated DC/DC accepts **12–36 V**, so it runs off boat power with no USB brick. ~40% more messages than the single-channel dAISy |
| [dAISy HAT](https://shop.wegmatt.com/products/daisy-hat-ais-receiver) | $79 | £71.99 | True dual | Raspberry Pi HAT on UART0/serial0 @38400; two breakout serial pads (Serial 1 mirror always on, Serial 2 configurable in/out); I2C pads; SMA | <200 mW (<40 mA @5 V) | Works standalone if you feed 5 V/3.3 V to the pads. **Pi 3 caveat: UART0 is the Bluetooth radio, so Bluetooth may be unavailable.** Pi 1 A+/B+, 2, 3, 4, 5, Zero |
| [dAISy Mini](https://shop.wegmatt.com/products/daisy-mini-ais-receiver) | $85 ($109 w/ USB adaptor) | £89.99 | True dual, two independent radios, integrated LNA | 38400 primary serial, 4800–38400 secondary (NMEA in *or* AIS out), I2C, JST-GH (Ardupilot TELEM compatible), SMA | 70 mA LNA on / 36 mA LNA off / 0.4 mA sleep @5 V | **7 g.** For embedded/drone/MCU integration. Supports all four AIS frequencies incl. 156.775/156.825 |
| [dAISy FeatherWing](https://shop.wegmatt.com/products/daisy-featherwing-ais-receiver) | $79–$93 | £76.79 | True dual | Adafruit Feather form factor/pinout | — | Same radio core as Mini; stacks on any Feather board |
| **[dAISy-catcher](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)** | **$149** | £132.00 | True dual, continuous, all four AIS frequencies, all message types 1–28 | USB **or** Raspberry Pi HAT; SMA female; 115200 bps primary serial; secondary serial for GNSS add-on | **500 mW**, 5 V power / 3.3 V logic, 20 g | **Sensitivity better than −120 dBm @ 20% PER.** SDR architecture, co-developed with the AIS-catcher project. Case $19, GPS add-on $39 (£37.20) |

Wegmatt is candid about the trade-off on the cheaper units: the dAISy HAT page states the architecture gives "longer acquisition time, lower sensitivity and less range than some traditional AIS receivers" while outperforming other sub-$100 options ([Wegmatt](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)), and the dAISy 2+ page repeats that it has "lower range and is slower to acquire targets" than traditional dual-channel receivers from established brands ([Wegmatt](https://shop.wegmatt.com/products/daisy-2-dual-channel-ais-receiver-with-nmea-0183)).

**Community verdict on dAISy 2+**: 46 Tindie reviews averaging 4.65/5. Representative comments: "Works straight out of the box, picked up ships as far away as 16nm"; another user reported 45 miles with a good antenna, one 111 NM from altitude; one disappointed user got only 2 NM (Wegmatt offered support) ([Tindie](https://www.tindie.com/products/astuder/daisy-2-dual-channel-ais-receiver-with-nmea-0183/)). OpenCPN's hardware wiki summarises the original dAISy as "work like a charm" and a "low cost alternative" ([OpenCPN wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_hardware:ais_devices)).

> **Buying note:** Wegmatt's Tindie store is paused ("on break until May 31, 2027 … Due to technical problems with Tindie, this store is on pause. Please visit https://wegmatt.com to buy our products") ([Tindie](https://www.tindie.com/products/astuder/daisy-2-dual-channel-ais-receiver-with-nmea-0183/)). Buy direct from [shop.wegmatt.com](https://shop.wegmatt.com/) (US) or [Uputronics](https://store.uputronics.com/collections/wegmatt-ais-receivers) / [The Pi Hut](https://thepihut.com/products/daisy-hat-ais-receiver-for-the-raspberry-pi) (UK/EU), though Uputronics showed every Wegmatt line sold out on 2026-08-22.

### 1.2 The dAISy-catcher in detail — the notable 2026 product

The dAISy-catcher was developed by jvde-github (author of AIS-catcher) together with Wegmatt. The stated design goal was "to combine the advantages of an SDR-based AIS receiver (reception quality, flexibility) with the advantages of a hardware solution (plug-and-play … low power consumption, accurate signal levels and timestamps, and AIS-tuned filters for robustness)" ([AIS-catcher discussion #563](https://github.com/jvde-github/AIS-catcher/discussions/563)).

Claims and measurements:

| Claim | Value | Source |
|---|---|---|
| Sensitivity | better than −120 dBm @ 20% PER | [Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver) |
| Vessels vs dAISy HAT | beta sites reported **3–4×** more vessels | [Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver) |
| vs a good SDR | "comparable to running AIS-catcher with a good SDR like the Airspy HF+" | [discussion #563](https://github.com/jvde-github/AIS-catcher/discussions/563) |
| Power | 500 mW; astuder: "uses about half the power of just the SDR" | [Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver), [#563](https://github.com/jvde-github/AIS-catcher/discussions/563) |
| Reported extreme range | 450+ miles during tropospheric ducting, Pi 3 + dual-band ham antenna (jimarndt) | [#563](https://github.com/jvde-github/AIS-catcher/discussions/563) |
| Weakest decoded messages | ~−97 dBm (doc-nl) | [#563](https://github.com/jvde-github/AIS-catcher/discussions/563) |

It runs under AIS-catcher over serial, e.g. `AIS-catcher -e 115200 /dev/ttyAMA0 -ge init_seq co2,v`, with `cg1` in the init sequence to enable the optional GPS add-on ([#563](https://github.com/jvde-github/AIS-catcher/discussions/563)). It is explicitly positioned as "ideally suited for shore stations that seek to maximize coverage" ([Wegmatt](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)).

Important caveat: unlike the older dAISy units, the dAISy-catcher does **not** decode standalone into AIVDM on the wire in the same plug-and-play way — it is designed to be driven by AIS-catcher, which is also the software that will forward it. That is fine for a Linux/Pi station and awkward for a "plug into a chartplotter" use case, where the dAISy 2+ remains the right product.

### 1.3 Quark-elec (UK)

Quark-elec builds AIS receivers with WiFi and NMEA multiplexing built in — attractive because the box does receiver **and** network gateway in one, with no computer.

| Model | Price | Channels | Interfaces | Notes |
|---|---|---|---|---|
| [QK-A021](https://www.quark-elec.com/product/qk-a021-ais-receiver-dongle/) | ~$55 | Single channel (OpenCPN wiki describes it as dual channel alternating) | USB dongle, NMEA 0183 out | Cheapest way into the range |
| [QK-A022](https://www.quark-elec.com/product/qk-a022-dual-channel-ais-receiver/) | — | **True dual**, two receivers monitoring both channels simultaneously | USB, NMEA 0183 | Receive-only |
| QK-A023 | discontinued | Single channel | USB + WiFi | Quark-elec now points buyers to the **A024** as the successor with "newer technology, improved specifications, and additional features" ([Quark-elec](https://www.quark-elec.com/category/marine-electronics-articles/quark-elec-aisnmea-products/qk-a023/)) |
| [QK-A026](https://www.quark-elec.com/category/marine-electronics-articles/quark-elec-aisnmea-products/qk-a026/) | **$150** ([Quark-Marine](https://www.quark-marine.com/product-tag/ais-receiver/)) | Dual | **WiFi + USB + NMEA 0183 simultaneously**, integrated GPS, NMEA multiplexer | Reviewed favourably by [Panbo](https://panbo.com/quark-elec-a026-ais-receiver-with-wifi-gps-and-nmea-0183-multiplexing/). The multiplexer means the same box carries your AIS *and* your other instruments to a tablet |
| QK-A026-plus | $282.49 | Dual | adds **NMEA 2000** output | |
| [QK-A027](https://www.quark-elec.com/category/marine-electronics-articles/quark-elec-aisnmea-products/qk-a027/) | — | Dual | WiFi, USB, NMEA 0183, GPS (22 sat / 66 channel), **SeaTalk1 converter** | For Raymarine ST40/ST60 boats |
| [QK-A027-plus](https://www.quark-marine.com/product/a027-plus-nmea-2000-ais-receiver/) | $232.00 | Dual | **LAN (ethernet)**, NMEA 2000, WiFi, USB | The only Quark AIS receiver with a wired ethernet port — relevant for a set-and-forget shore box |

Quark-elec publish a useful explainer on the dual-vs-single distinction and note that resellers frequently mislabel channel-hopping units as "dual channel" ([Quark-elec](https://www.quark-elec.com/do-i-need-a-dual-or-single-ais-receiver/)).

### 1.4 Comar Systems (UK) — the shore-station brand

Comar is the OEM behind most of MarineTraffic's free receivers. Everything is quote-only; no public retail prices were found on any Comar page.

**Current lineup** ([Comar shop](https://comarsystems.com/shop/)):

| Model | Type | Interfaces | Notes |
|---|---|---|---|
| R250 | Dual-channel receiver | USB, NMEA 0183 VDM, built-in WiFi, 12/24 V or USB power | Can multiplex an external GNSS NMEA input with the AIS data ([Comar](https://comarsystems.com/product/r250-dual-channel-ais-receiver/)) |
| R450-X | Network receiver | **Ethernet**, WiFi (AP/STA/AP+STA), USB | Uses a **Lantronix XPort**; supports **up to 4 TCP and 1 UDP client/server connections** ([Comar](https://comarsystems.com/product/r450-x-network-ais-receiver/)) |
| R450-XG | Network receiver + GNSS | as R450-X plus GNSS | |
| R550Ni | "Intelligent" network receiver | Ethernet, WiFi, **Bluetooth 5.0**, 2× USB-A, HDMI, USB-C power | Quad-core ARM Cortex-A72, 16 GB eMMC, ships with **Comar Connect** forwarding software ([Comar](https://comarsystems.com/product/r550ni-intelligent-network-ais-receiver/)) |
| R550NGi | as R550Ni + GNSS | | |
| COM100 | Lightweight OEM module | — | For UAV/drone and integrators; sold bundled with AV20/AV30 antennas |

**Legacy but widely deployed** (these are what you will actually be handed by MarineTraffic):

| Model | Interfaces | Notes |
|---|---|---|
| SLR350Ni | BNC antenna, RJ45 10/100, 4× USB 2.0, WiFi, HDMI | **Built on a Raspberry Pi 3.** Streams NMEA 0183 VDM @38400 to **up to five user-defined destinations** over TCP/UDP/HTTP, excluding the reserved MarineTraffic stream ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552937-comar-slr350ni), [Comar](https://comarsystems.com/product/slr350ni-intelligent-network-receiver/)). Marked "Legacy product" by Comar |
| SLR400Ni | Ethernet/WiFi | Introduced by MarineTraffic July 2022 ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552936-comar-slr400ni)) |
| SLR450Ni | Ethernet, WiFi, USB, BNC, HDMI; 95×90×28 mm, 180 g | **MarineTraffic's current generation since the beginning of 2024.** Raspberry-Pi powered, dual-channel, decodes Class A, Class B, AtoN and SART; NMEA 0183 VDM @38400; up to five user destinations; TCP/UDP/ARP/ICMP/TFTP/Telnet/DHCP/BOOTP/HTTP/AUTOIP ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552935-comar-slr450ni)) |
| R400N (ex-SLR350N) | Ethernet | Third-gen digital dual-channel parallel receiver; came into MarineTraffic's fleet via the FleetMon merger ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552938-comar-r400n-slr350n)) |
| SLR200N | Ethernet, IP-addressable | Dual-channel parallel ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552940-comar-slr200n)) |

### 1.5 Digital Yacht

| Model | Price | Interfaces | Notes |
|---|---|---|---|
| [AISnet](https://digitalyachtamerica.com/product/aisnet/) | **$1,049.95** | **Ethernet RJ45** + USB; universal 110/240 V mains adapter → 12 V DC | Purpose-built shore base station: "fit and forget black box solution" for home or office. You register with a tracking service, get an IP and port, and it pushes there. Explicitly aimed at MarineTraffic / AIS Live. Dual channel. Still listed as an active product. Bundles SmarterTrack Lite for local PC monitoring over USB while simultaneously feeding over ethernet |
| AIS100 | see retailers, ~$200 street | Serial NMEA 0183 / USB variants | "Value-priced, entry level AIS receiver … same dual channel receiver as the AIS200 range, but without the multiplexer" ([Digital Yacht](https://digitalyachtamerica.com/product-tag/aisnet/)). Needs a computer or gateway to reach the network |

AISnet is the classic "marina or yacht club" answer, and at ~$1,050 it is roughly **7× the price of a dAISy-catcher** for a box that does the same job with less sensitivity but zero maintenance and no Linux.

### 1.6 NASA Marine, SR162, Matsutec, and the cheap end

| Model | Price | Channels | Output | Notes |
|---|---|---|---|---|
| [NASA Marine AIS Engine 3](https://www.nasamarine.com/product/ais-engine-3/) | ~£150 / ~$181 as Clipper AIS Engine 3 ([NVN Marine](https://nvnmarine.com/products/48466-clipper-ais-engine-3)) | **Channel-hopping**, not simultaneous | 9-pin D-type serial, NMEA 0183 HS @38400; USB via optional adapter; BNC | 10–16 V, 43 mA. Receiver only, no display. MarineTraffic used to hand these out ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552934-marinetraffic-provided-ais-receivers)) |
| NASA AIS Radar / SART plotter | chandlery-dependent | same core | built-in plotter + GPS | Forum users caution that NASA's "dual channel" and "radar" naming is marketing, not literal ([YBW Forum](https://forums.ybw.com/threads/nasa-ais-any-good.260057/), [Panbo](https://panbo.com/nasa-ais-radar/)) |
| SR162 "Smart Radio" | ~$359 on secondhand/surplus listings ([PicClick](https://picclick.ca/Smart-Radio-SR162-AIS-Receiver-SR-162-Dual-173970555830.html)); also via Milltech Marine | **True simultaneous dual** (SR161 is single, channel A only) | RS-232/RS-422 serial, BNC, 12 V | The classic pre-dAISy hobbyist receiver. Cruisers Forum users specifically choose SR162 over SR161 for busy traffic, and note the SR162 emits a diagnostic message useful for troubleshooting ([CF](https://www.cruisersforum.com/forums/f13/sr161-ais-receiver-4435-2.html)). Whether it is still in active manufacture in 2026 is unclear |
| Matsutec HP-33A | ~$250–$379 ([listing](https://matsutec.marinegps.org/matsutec-hp-33a-color-lcd-class-ais-transponder-combo-high-marine-gps-navigator.html)) | Dual, independent | 4.3" LCD, standalone | **This is a Class B transponder (5 W TX), not a receiver.** For a shore station it is the wrong product and probably illegal to transmit from land |
| Matsutec HA-102 | ~$275 | Dual | black box, no display | Same transponder core as HP-33A minus the screen |
| Generic AliExpress/eBay "dual channel AIS receiver USB" | ~$30–$100 ([e.g. Amazon](https://www.amazon.com/Receiver-Stainless-Foldable-Convenient-Reception/dp/B0CMBYBJGV)) | Advertised dual, often actually hopping | USB | Typically claim "better than −103 dBm @ 20% BER". Verify true simultaneous dual before buying; Quark-elec document that resellers routinely mislabel hopping units as dual-channel ([Quark-elec](https://www.quark-elec.com/do-i-need-a-dual-or-single-ais-receiver/)). At this price an RTL-SDR v3 + AIS-catcher is a strictly better bet |
| ELGR 162-ETH | legacy, not sold | Dual (161.975/162.025) | **Ethernet** via WIZnet WIZ107SR serial-to-ethernet module; TCP/IP, UDP, DHCP; BNC | Historic MarineTraffic unit, superseded by the Comar Ni series ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552943-elgr-162-eth)) |
| [ShipXplorer AIS Dongle](https://www.shipxplorer.com/blog/shipxplorer-ais-hardware-ais-dongle) | see [store](https://www.shipxplorer.com/store) | Dual (161.975 / 162.025) | USB 2.0, NMEA output | AirNav's branded dongle; it is an **RTL-SDR variant tuned and filtered for AIS**, supported natively by AIS-catcher, so it sits between "SDR" and "dedicated" |
| ShipXplorer SeaRange | see store | — | Ethernet | Ships with a 30 ft SMA cable, PSU and ethernet leads — an all-in-one shore box |

### 1.7 What the aggregators give away or recommend

| Network | Gives hardware? | Which | Conditions |
|---|---|---|---|
| **MarineTraffic** | **Yes** | Comar **SLR450Ni** (current since early 2024), SLR400Ni, SLR350Ni, R400N/SLR350N; plus Raspberry-Pi-based limited editions **RSK450Ni** and **RSK400Ni** made during supply-chain shortages, which "lack some of the LEDs for AIS reception" ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552934-marinetraffic-provided-ais-receivers)) | Within ~10 km of the coast **or** high with clear line of sight to the water; permanent power and internet; ability to mount an outdoor antenna; area not already covered; busy port / high-traffic route / high altitude preferred; prior experience with radio kit is a plus ([MarineTraffic](https://help.marinetraffic.com/hc/en-us/articles/204665498-What-kind-of-equipment-and-software-do-I-need-)). Hosts get premium account upgrades and extended history ([apply](https://www.marinetraffic.com/en/p/apply-for-free-ais-receiver)) |
| **VesselFinder** | Yes (AIS Partner program) | model not published | Free Premium account, live map access, performance alerts ([VesselFinder](https://stations.vesselfinder.com/become-partner)) |
| **ShipXplorer** (AirNav) | Yes, on loan | own dongle / SeaRange | Within ~5 km of coastline or shipping lanes, 360° unobstructed view, go live within 1 week of receiving the unit, 24/7 uptime, **return the hardware if you stop hosting** ([ShipXplorer](https://www.shipxplorer.com/addcoverage)) |
| **FleetMon** | No longer separately | — | Station network folded into the MarineTraffic partnership; new feeders directed to MarineTraffic ([FleetMon](https://www.fleetmon.com/my/ais-stations)) |
| **AISHub** | **No hardware at all** | — | Bring your own receiver and software. Requirements are on the *data*: raw NMEA, ≥10 vessels 7-day average, ≥90% uptime 7-day average, downsampling ≤60 s, message delay ≤10 s, and no synthesized/scraped/stolen/publicly-sourced data. "All contributors are allowed to use the aggregated data for free" ([AISHub](https://www.aishub.net/join-us)) |
| **vesseltracker.com** | Yes | — | [Antenna partner program](https://www.vesseltracker.com/en/static/antenna-partner.html) |

MarineTraffic's own software ranking, worth quoting in the guide: **AIS Dispatcher** as the recommended default, **NMEA Router** when forwarding to many destinations, and **AIS-catcher** recommended specifically for SDR receivers; AIS Decoder, AIS Logger and AIS Mon are flagged outdated ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552948-what-is-the-best-ais-software-for-my-needs)).

---

## 2. Dedicated receiver vs RTL-SDR + AIS-catcher

### The state of the argument in 2026

The old received wisdom — "an RTL-SDR isn't very good for AIS, so the dAISy HAT is the way to go" ([FlightAware forum](https://discussions.flightaware.com/t/does-anyone-monitor-ais-ship-and-vessel-tracking/53634)) — was overturned by AIS-catcher's decoder quality, and has now been partly re-overturned by the dAISy-catcher bringing SDR-grade reception into a dedicated box. The current picture:

| Axis | Dedicated receiver (dAISy HAT / 2+, Comar, Digital Yacht) | RTL-SDR + AIS-catcher | dAISy-catcher |
|---|---|---|---|
| Hardware cost | $79–$1,050 | ~$30 dongle ([worldwideais](https://www.worldwideais.org/post/how-to-build-a-low-cost-ais-receiver-station-diy-guide-to-tracking-ships)) + host computer | $149 + host computer |
| Sensitivity | Wegmatt's own pages admit "lower sensitivity and less range than some traditional AIS receivers" for the HAT/2+ ([Wegmatt](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)) | Very good with a decent decoder; Airspy HF+ gives only ~10–15% more messages than an RTL-SDR v3 ([#333](https://github.com/jvde-github/AIS-catcher/discussions/333)) | **−120 dBm @ 20% PER**, "comparable to … Airspy HF+" ([#563](https://github.com/jvde-github/AIS-catcher/discussions/563)) |
| CPU load | ~zero — the box emits ASCII, the host just moves bytes | Real DSP load. On a Pi Zero W vs Pi 3, one operator measured the **Pi 3 detecting ~50% more targets** than the Zero W, purely from compute/64-bit ([pysselilivet](https://pysselilivet.blogspot.com/2023/12/ais-receiver-and-dispatcher-best.html)) | Low — AIS-catcher reads ASCII over serial, no IQ processing |
| Power | dAISy HAT <200 mW; 2+ <500 mW | dongle alone typically ~1 W plus host | 500 mW; astuder: "about half the power of just the SDR" ([#563](https://github.com/jvde-github/AIS-catcher/discussions/563)) |
| Setup | Plug in, read `/dev/ttyACM0` at 38400 | Install driver, pick gain, pick decoding model, blacklist DVB-T kernel modules | AIS-catcher, one serial flag |
| Reliability | No tuning drift, no USB dongle overheating, deterministic | Gain and temperature sensitive; cheap dongles drift | Fixed-function |
| Signal levels | dAISy's reported signal strength is corrected for amplification and AGC, representing the level at the antenna port to within about ±1 dB | SDR reports are relative/uncalibrated without work | Product page claims "accurate signal levels and timestamps" ([#563](https://github.com/jvde-github/AIS-catcher/discussions/563)) |
| Flexibility | AIS only | Same dongle does ADS-B, weather sats, ham | AIS only |

### The concrete measurements

From [AIS-catcher discussion #563](https://github.com/jvde-github/AIS-catcher/discussions/563), doc-nl comparing an RTL-SDR v4 with a dAISy-catcher on the same signals:

| Scenario | RTL-SDR v4 reported level | dAISy-catcher reported level |
|---|---|---|
| Ship a couple of km away | −11 dBm | −85 dBm |
| Ship ~200 m away | −2.5 dBm | −47 dBm |

**Read these carefully — they are not a sensitivity comparison.** They show the RTL-SDR's power figures are uncalibrated relative numbers while the dAISy-catcher reports true dBm at the antenna port (−85 dBm for a ship a few km out is a physically plausible AIS signal; −11 dBm is not). The genuine sensitivity claim is the −120 dBm @ 20% PER figure and the "3–4× more vessels than the dAISy HAT" beta result. ymmengin also compared a Nooelec NESDR SMArTee v2 + Uputronics preamp against a dAISy-catcher on the same antenna and found the dAISy showed a better noise floor ([#563](https://github.com/jvde-github/AIS-catcher/discussions/563)).

### What actually limits your station

The AIS-catcher author's own survey ([#333](https://github.com/jvde-github/AIS-catcher/discussions/333)) is the most useful single source here, and its conclusions are uncomfortable for anyone shopping for receivers:

1. **"Your antenna is the most important piece of equipment that affects the number of ships you will receive."**
2. The RTL-SDR **v4 gave worse results than the v3** in the author's testing, contradicting the manufacturer's claims.
3. An **Airspy HF+ bought ~10–15% more messages** than the RTL-SDR v3 — "may not be worth buying as an upgrade."
4. A **Uputronics AIS SAW-filtered preamp** changed measured signal levels but produced "no apparent difference in number of messages received" at that site.
5. Wideband LNAs can *degrade* reception in RF-noisy locations; they help only in quiet ones.

The community's practical rule of thumb, from a FlightAware AIS thread: antenna "as high as possible and as close to the sea as possible since AIS is still mostly line of sight," with ~50 miles for Class A under good conditions and occasional ducting to 150 NM ([FlightAware](https://discussions.flightaware.com/t/does-anyone-monitor-ais-ship-and-vessel-tracking/53634)).

### The known limits of the dAISy line

- **Original dAISy is single-channel with hopping**, not dual. Wegmatt claims the hopping recovers 40–60% over a naive single-channel receiver, and the dAISy 2+ then adds ~40% over the original ([Wegmatt](https://shop.wegmatt.com/products/daisy-ais-receiver), [Wegmatt](https://shop.wegmatt.com/products/daisy-2-dual-channel-ais-receiver-with-nmea-0183)). For a shore station in busy water, hopping loses messages; buy dual.
- **The original dAISy has no NMEA 0183 output** — USB only. For a chartplotter you need the 2+ ([Wegmatt](https://shop.wegmatt.com/products/daisy-ais-receiver)).
- **dAISy HAT occupies UART0 on a Pi 3**, which is the Bluetooth radio; Bluetooth may become unavailable ([Wegmatt](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)).
- **Sensitivity/acquisition are explicitly below traditional commercial receivers** on the HAT/2+/original — this is Wegmatt's own wording, not a critic's.
- **Availability is lumpy.** Tindie store paused; Uputronics showing sold out across the line on 2026-08-22.

---

## 3. AIS a boat already has

For a cruiser the cheapest receiving station costs nothing: the transponder, VHF or plotter already aboard is an AIS receiver. The work is not buying hardware, it is getting `!AIVDM` out of the existing bus and onto a socket.

Items below marked **(unverified)** could not be confirmed from a primary source in this pass and need checking before they go in a public guide.

### 3.1 Class B / B+ transponders

| Model | Class | TX power | Interfaces | Price | Source |
|---|---|---|---|---|---|
| em-trak B921 / B922 / B923 / B924 | Class B **CSTDMA** | 2 W | NMEA 0183 + NMEA 2000; B922/B924 add **WiFi + Bluetooth**; B923/B924 add a VHF splitter | from $637 ex-VAT on em-trak's store | [em-trak B900](https://em-trak.com/products/b900/), [Defender](https://defender.com/en_us/em-trak-b921-2w-cstdma-class-b-ais-transceiver-430-0001) |
| em-trak B951 / B952 / B953 / B954 | Class B+ **SOTDMA** | 5 W | same tiering; B954 = WiFi + Bluetooth + splitter | not confirmed | [B954 datasheet](https://em-trak.com/wp-content/uploads/Em-trak-Product_Datasheet_B954.pdf) |
| Vesper **Cortex M1** | Class B+ SOTDMA | 5 W | NMEA 2000; NMEA 0183 via the M1 I/O expansion port (min 38400 baud); **integrated WiFi TCP/UDP**; optional cellular | $1,299 hub alone; $1,849 as the V1 kit with an H1 handset; handsets $599 | [Panbo test](https://panbo.com/testing-vesper-cortex-m1-excellent-ais-monitoring-and-much-more-in-one-box/), [Fisheries Supply](https://www.fisheriessupply.com/vesper-m1-sotdma-smartais-transponder-with-remote-vessel-monitoring/010-02815-20) |
| Vesper **XB-8000** | Class B | 2 W | WiFi, NMEA 2000, NMEA 0183 | **discontinued** | see note below |
| Digital Yacht AIT2000 | Class B | 2 W | 2× NMEA 0183 in/out (38400/4800), NMEA 2000, USB | not confirmed | [Digital Yacht](https://digitalyacht.eu.com/product/ait2000/) |
| Digital Yacht **AIT5000** | Class B+ SOTDMA | 5 W | multiplexed NMEA 0183, NMEA 2000, USB, **WiFi TCP/UDP up to 7 simultaneous clients**, integrated ZeroLoss splitter | not confirmed | [Digital Yacht America](https://digitalyachtamerica.com/product/ait5000/) |
| Garmin AIS 800 | Class B | 5 W | NMEA 2000 + bare-wire NMEA 0183. **No built-in WiFi** | not confirmed | [The GPS Store](https://www.thegpsstore.com/Marine-Electronics/Safety-Equipment/Garmin-AIS-800-Automatic-Identification-System-Transceiver) |
| Raymarine AIS700 | Class B | — | SeaTalk NG, NMEA 2000, NMEA 0183, USB (setup/diagnostics only). **No built-in WiFi** | $1,209.99 West Marine; £829 UK | [Raymarine](https://www.raymarine.com/en-us/our-products/ais/ais-receivers-and-transceivers/ais700-class-b-transceiver), [Yachting Monthly](https://www.yachtingmonthly.com/gear/raymarine-ais700-65645) |
| B&G / Simrad / Lowrance NAIS-500 | Class B | — | NMEA 2000, NMEA 0183, USB. **No built-in WiFi** — needs a GoFree module on the MFD side | not confirmed | [Simrad](https://www.navico-commercial.com/simradcommercial/ais/nais-500/), [West Marine](https://www.westmarine.com/simrad-nais-500-ais-class-b-transceiver-17933987.html) |
| B&G V60-B | VHF with Class B TX/RX | — | NMEA 2000 + NMEA 0183; forwards AIS/GPS/MOB over N2K. **No built-in WiFi** | not confirmed | [B&G](https://www.bandg.com/bg/type/vhf-ais/v60-series-vhf-radios/vhf-marine-radio-dsc-ais-rxtx-v60-b/) |
| Matsutec HP-33A / HA-102 | Class B | 5 W | HP-33A has a 4.3" LCD; HA-102 is the black-box version | ~$250–$379 / ~$275 | [listing](https://matsutec.marinegps.org/matsutec-hp-33a-color-lcd-class-ais-transponder-combo-high-marine-gps-navigator.html) |

**CSTDMA vs SOTDMA matters for naming.** The em-trak B92x family uses CSTDMA — listen for a free slot then transmit, the original Class B scheme. The B95x family is SOTDMA ("Class B+"), reserving slots and transmitting at 5 W with faster update rates in congested water ([em-trak](https://em-trak.com/support/b921-b922-b923-b924/)).

**Vesper is now Garmin, and the XB-8000 is gone.** Garmin acquired Vesper Marine in January 2022 and discontinued the entire WatchMate line — XB-6000, XB-8000, Vision2 — citing a third-party manufacturer's inability to source components; the WatchMate app also lost active development. Cortex is now the sole product, launched at roughly $600 more than the XB-8000 it replaced ([Panbo](https://panbo.com/garmin-shrinks-vesper-product-line-will-only-cortex-remain/), [Attainable Adventure Cruising](https://www.morganscloud.com/jhhtips/garmin-cut-vesper-users-loose/)). Anyone writing a guide should stop recommending the XB-8000 and should expect a lot of existing installations still running it — those boats have WiFi NMEA 0183 out of the box and are the easiest possible feeders.

Ocean Signal and ACR were **not verified** in this pass. Ocean Signal's current AIS products appear to be personal MOB/SART beacons rather than vessel transponders, and ACR does not appear to list a Class B transponder — but treat both as unresearched rather than confirmed absent.

**The practical ranking for a feeder**: any transponder with built-in WiFi (em-trak B922/B924/B952/B954, Vesper Cortex, Digital Yacht AIT5000) is the easiest AIS source that exists — connect to its TCP or UDP NMEA 0183 server and you have raw AIVDM with zero extra hardware. Everything else needs a gateway.

### 3.2 Receive-only units on boats

| Model | Status | Interfaces | Price | Source |
|---|---|---|---|---|
| Garmin AIS 300 | **discontinued**, receive-only | NMEA 2000 | was MSRP $499.99, street $404–447 | [Garmin](https://www.garmin.com/en-US/newsroom/press-release/marine/2010-garmin-introduces-the-ais-300-receiver-with-class-a-and-class-b-reception-nmea-2000-support/) |
| Garmin AIS 600 | **discontinued** — and it was a transceiver, not receive-only | NMEA 2000 / 0183 | — | superseded by AIS 800 |
| Raymarine AIS350 | current, dual-channel receive-only | SeaTalk, NMEA 2000, NMEA 0183, USB; built-in multiplexer for legacy MFD/VHF | not confirmed | [Raymarine](https://www.raymarine.com/en-us/our-products/ais/ais-receivers-and-transceivers/ais350-receiver-only) |
| Digital Yacht AIS100 | current | **USB only**; same dual-channel receiver as the AIS200 line without the multiplexer, ~20 nm | not confirmed | [Digital Yacht America](https://digitalyachtamerica.com/product-category/ais-receivers/) |
| Digital Yacht AISnet | current | **Ethernet** | $1,049.95 | [Digital Yacht America](https://digitalyachtamerica.com/product/aisnet/) — see section 1.5 |
| NASA / Clipper AIS Engine 3 | current | RS-232 NMEA 0183 out @38400, 4800-baud GPS in, BNC | ~$181 | [NASA manual](https://www.nasamarine.com/wp-content/uploads/2015/12/AIS-Engine.pdf), [NVN Marine](https://nvnmarine.com/products/48466-clipper-ais-engine-3) |

### 3.3 VHF radios with a built-in AIS receiver

| Model | AIS output path | Caveats | Source |
|---|---|---|---|
| Standard Horizon GX2400 | NMEA 2000 and NMEA 0183 (configured in the GPS setup submenu) | **Outputs a genuine `!AIVDM` sentence with talker `AI`** after receiving an AIS transmission. Standard Horizon does not publish which of the 27 AIVDM message types it re-emits. Integrated VHF/AIS antenna coupler shares one antenna. No documented way to disable the internal AIS receiver when you add an external transponder — the community workaround is to prioritise by SRC field on the MFD | [Defender GX2400B](https://defender.com/en_us/standard-horizon-vhf-radios-with-ais-gps-nmea2000-gx2400b) |
| Standard Horizon GX6000 | NMEA 2000 and NMEA 0183 | Same AIVDM behaviour; **requires a dedicated AIS antenna**, no shared coupler | [Standard Horizon](https://standardhorizon.com/product-detail.aspx?Model=GX6000&CatName=Fixed+Mount+VHF) |
| Icom M510 / M510EVO / M510BB | NMEA 0183 (**38400 baud required**) and NMEA 2000 | Sends and receives AIS/GPS over either bus. Whether the radio suppresses AIS output while transmitting on VHF is **(unverified)** | [Icom manual & FAQ](https://icomamericasupport.zendesk.com/hc/en-us/articles/35050991753620-M510-M510EVO-Instruction-Manual-and-FAQs) |
| Icom M605 | NMEA 2000 (exchanges AIS reports and GNSS position) | Same caveat, **(unverified)** | [Icom](https://www.icomamerica.com/lineup/products/IC-M605/) |
| B&G V60-B | NMEA 2000 + NMEA 0183 | Not a passive receiver — it is a VHF with a full Class B transceiver, so it also emits own-ship AIVDO | [B&G](https://www.bandg.com/bg/type/vhf-ais/v60-series-vhf-radios/vhf-marine-radio-dsc-ais-rxtx-v60-b/) |
| Garmin VHF 215/315, Raymarine Ray90/Ray91 | — | **Not researched** in this pass; absence here is not evidence they lack an AIS receiver | — |

The GX2400/GX6000 finding is the useful one: an AIS-capable Standard Horizon radio is a **real AIVDM source**, not merely a target display. A boat with one of these and no transponder is still a viable feeder.

### 3.4 Chartplotters and MFDs — the `!AIVDM` vs decoded-target distinction

This is where most "just tap the plotter's WiFi" plans die. A plotter that shows AIS targets does not necessarily re-emit the raw sentences that produced them.

| Platform | What comes out | Source |
|---|---|---|
| **Raymarine Axiom / LightHouse** | **NMEA 2000 only at the unit.** The Axiom "does not have any NMEA 0183 connector and doesn't even have any SeaTalk NG connector anymore — it only has the DeviceNet micro connector for NMEA 2000." LightHouse's WiFi "Data Master" feature syncs waypoints and routes over SeaTalkHS/Ethernet, **not raw AIS sentences**. To get `!AIVDM` onto a socket you need a separate N2K→WiFi bridge (Actisense W2K-1, Yacht Devices YDWG-02, Quark-elec, or a Signal K box) | [Segeln-Forum](https://www.segeln-forum.de/thread/79711-ais-nmea-0183-auf-raymarine-axiom-seatalkng-bzw-nmea-2000-darstellen/), [YBW Forum](https://forums.ybw.com/threads/raymarine-axiom-9-mfd-and-ais.559803/page-3) |
| **Furuno** (NavNet era, DATA2 port) | Raw `!AIVDM`/`!AIVDO` on the DATA2 serial port at 38400 baud (a dedicated AIS-only port on some models). **Over ethernet, NavNet VX prepends a proprietary 8-byte preamble to each UDP packet before the NMEA sentence, with byte 2 as an incrementing sequence counter** — generic "NMEA over UDP" tools will not parse it without stripping the preamble | [Medium writeup](https://bluedefender.medium.com/feeding-ais-data-to-a-furuno-navnet-vx2-plotter-with-a-raspberry-pi-6348bbca6909), [kplex group](https://groups.google.com/g/kplex/c/laHZu7RKRV0) |
| **Garmin ActiveCaptain / Marine Network** | Primarily chart sync and connected services, not a raw-NMEA relay. **No confirmation** found that ActiveCaptain forwards `!AIVDM` to third-party apps the way Navico's GoFree does. Garmin's ecosystem is generally more closed to third-party NMEA-over-WiFi than Navico's — **(unverified)** | [Garmin FAQ](https://www.garmin.com/en-US/blog/marine/common-questions-new-activecaptain-users/) |
| **B&G / Simrad / Navico GoFree** | GoFree WiFi-1 modules are generally understood to stream NMEA 0183 including AIS over TCP/UDP to apps such as Navionics Boating, but this was **not freshly verified** in this pass. Confirm directly before publishing — **(unverified)** | — |

**Rule for the guide:** before designing around a plotter, log the actual bytes. `nc`, `socat`, or Wireshark on the plotter's WiFi will tell you in thirty seconds whether you are getting `!AIVDM` or `$RATTM`-style decoded targets. Decoded target sentences are useless for feeding an aggregator — they have already lost the MMSI-level fidelity and raw payload the network needs.

### 3.5 Gateways and multiplexers

| Device | Price | What it emits | Notes | Source |
|---|---|---|---|---|
| Yacht Devices **YDWG-02** (N2K WiFi Gateway) | $249 | **TCP and UDP servers simultaneously**, unlimited UDP clients, web config | Bidirectional N2K↔0183; docs confirm VDO/VDM ↔ PGN 129038 (Class A) and 129039 (Class B) in both directions | [Yacht Devices](https://yachtdevicesus.com/products/nmea-2000-wi-fi-gateway-ydwg-02) |
| Yacht Devices **YDEN-02** (Ethernet) | $249 | TCP/UDP over wired ethernet | Same conversion engine, wired | [Yacht Devices](https://yachtdevicesus.com/products/nmea-2000-ethernet-gateway-yden-02) |
| Yacht Devices **YDNU-02** (USB) | $249 | USB serial | N2K + AIS + 0183 to a PC/Mac/Linux host | [Yacht Devices](https://yachtdevicesus.com/products/nmea-2000-usb-gateway) |
| **Actisense W2K-1** | ~$250–286 | N2K → WiFi TCP/UDP; converts AIS messages to 0183 for WiFi nav software; logs to onboard microSD (~16 days) | Pack-of-cards form factor | [The GPS Store](https://www.thegpsstore.com/Marine-Electronics/NMEA-2000-Components/Actisense-W2K-1-NMEA-2000-to-Wi-Fi-Gateway), [Panbo](https://panbo.com/actisense-adds-nmea-2000-insight-to-w2k-1-with-actisense-i/) |
| **Actisense NGW-1 / NGW-1-ISO** | £141–163 (~$180–210) | Bidirectional N2K↔0183 | See section 3.6 — the "-AIS" SKU converts **0183 AIS → N2K**, the wrong direction for a feeder | [Actisense](https://actisense.com/products/nmea-2000-gateway-ngw-1/) |
| **Quark-elec QK-A032 / QK-A032-AIS** | $160 base / ~€190 AIS variant | N2K/0183 bidirectional + USB + WiFi | An AIS-specific SKU exists, implying a dedicated conversion path, but no independent technical breakdown found | [NavStore](https://navstore.com/products/quark-elec-nmea-2000-0183-w-ais-bi-directional-gateway-usb-wifi-qk-a032-ais) |
| **ShipModul MiniPlex-3Wi** | $423–450 | NMEA 0183 over WiFi + USB, up to 57600 baud, 4 sources → 7 instruments | **0183 only, no N2K input** on the base variant | [Milltech](https://www.milltechmarine.com/shipmodul.html), [ShipModul](https://www.shipmodul.com/miniplex-3wi.html) |
| **ShipModul MiniPlex-3Wi-N2K** | $556 | Combines N2K + 0183 + SeaTalk + AIS into one WiFi 0183 stream | The variant you need if your AIS is N2K-only | [SVB24](https://www.svb24.com/en/miniplex-3wi-n2k-nmea-multiplexer-with-wifi-and-nmea2000.html) |
| Digital Yacht WLN10 / WLN30 / iKonvert / NavLink2 | — | 0183 → WiFi; iKonvert is N2K → serial/0183 | Not individually re-verified in this pass | [Digital Yacht](https://digitalyachtamerica.com/) |
| **AK-Homberger NMEA2000-AIS-Gateway** (ESP32, open source) | ~$15–30 in parts | Reads `!AIVDM` off a UART at 38400 and writes **N2K PGNs** | **Opposite direction from a feeder's needs.** Useful only to put an existing 0183 AIS receiver onto an N2K bus | [GitHub](https://github.com/AK-Homberger/NMEA2000-AIS-Gateway) |
| **`signalk-n2kais-to-nmea0183`** (Signal K plugin) | free | **N2K AIS PGNs → `!AIVDM`/`!AIVDO`**, served on the Signal K server's 0183 TCP port | The correct-direction tool. v2.0.3, last released August 2025, maintained by sbender9, 17 releases. The README does not enumerate supported PGNs or lossiness | [GitHub](https://github.com/sbender9/signalk-n2kais-to-nmea0183), [npm](https://www.npmjs.com/package/signalk-n2kais-to-nmea0183) |

### 3.6 NMEA 2000 → `!AIVDM` reconstruction — the hard part

On NMEA 2000 there are no sentences. AIS arrives as PGNs: **129038** Class A position, **129039** Class B position, **129040** Class B extended, **129041** AtoN, **129793** UTC/date, **129794** Class A static and voyage, **129809/129810** Class B static parts A and B. To feed an aggregator you must turn these back into `!AIVDM`. Findings on each candidate:

| Tool | N2K → AIVDM? | Detail |
|---|---|---|
| **Signal K + `signalk-n2kais-to-nmea0183`** | **Yes — the recommended path** | Purpose-built for exactly this: converts N2K AIS PGNs to `!AIVDM`/`!AIVDO` and serves them on Signal K's NMEA 0183 TCP port, aimed at apps like iSailor and iNavX. Actively maintained (v2.0.3, Aug 2025). The most-used solution in the Signal K / OpenPlotter community ([GitHub](https://github.com/sbender9/signalk-n2kais-to-nmea0183)) |
| **Yacht Devices YDWG-02 / YDEN-02** | **Yes for 129038/129039**, rest unconfirmed | Bidirectional AIS conversion is documented, with VDO/VDM ↔ 129038 and 129039 in both directions. Whether 129040, 129041, 129793, 129794, 129809/129810 round-trip into AIVDM types 5, 21 and 24A/24B, or are silently dropped on the N2K→0183 leg, is **not confirmed** from public docs; the full PDF manual is the canonical source ([Yacht Devices](https://yachtdevicesus.com/products/nmea-2000-wi-fi-gateway-ydwg-02)) |
| **Actisense NGW-1** | **Probably wrong direction** | The `-AIS` SKU is documented as converting **0183 AIS sentences into N2K PGNs**. Whether the base bidirectional unit reconstructs AIVDM on the N2K→0183 leg needs Actisense's "NGW-1 Full Conversion List" PDF, which was not machine-readable ([Actisense](https://actisense.com/products/nmea-2000-gateway-ngw-1/)) |
| **canboat** (`analyzer`, `n2kd`) | **No, not ready-made** | `analyzer` decodes N2K PGNs including AIS to JSON, and `n2kd` distributes that JSON to TCP clients — but n2kd does **not** encode AIVDM. A request for an AIVDM encoder has been open since October 2018, referencing gpsd's encoder as a possible base but flagging a GPL-3 (canboat) vs BSD (gpsd) licence mismatch. Whether that has since been closed was not confirmed ([issue #129](https://github.com/canboat/canboat/issues/129), [n2kd wiki](https://github.com/canboat/canboat/wiki/n2kd)) |
| **OpenCPN** | **Unverified, probably no** | Parses AIVDM/AIVDO on 0183 and reads N2K AIS PGNs via its CAN driver to display targets, but no confirmation it re-serialises N2K-sourced targets back out as `!AIVDM` on an outgoing connection. It is built as a display end-point, not a re-broadcast gateway |
| **Actisense NGT-1 + Signal K** | **Yes, via the plugin above** | NGT-1 is a raw N2K↔PC interface; Signal K plus `signalk-n2kais-to-nmea0183` does the reconstruction |

**Lossiness is real and you must measure it.** The clearest community statement: "A gateway can only convert what it receives — and only at the speed the 0183 device can output," with commonly-reported losses including missing vessel names, CPA/TCPA, vessel dimensions, and Class B static data when AIS is bridged to legacy 0183-speed devices ([sailboat-cruising.com](https://www.sailboat-cruising.com/AIS-on-NMEA-2000.html)).

There is also a structural point worth stating plainly in the guide: **an N2K PGN is a decoded product, not the original radio payload.** Reconstructing `!AIVDM` from it means re-encoding — bit-packing fields back into a six-bit ASCII armour payload. Anything the gateway didn't decode, or decoded lossily, cannot be recovered, and the reconstructed sentence is not byte-identical to what the ship transmitted. If your aggregator does any duplicate detection on raw payload, N2K-sourced messages will not dedupe against the same message received directly. **Recommendation: if you can reach a 0183 AIS source anywhere on the boat, use it in preference to reconstructing from N2K.**

Practical verification recipe for the guide: log raw sentences for an hour with a decoder that reports message types, and check that types 1/2/3, 5, 18, 19, 21 and 24 all appear. If types 5 and 24 are missing you are getting positions with no vessel names, which is a materially degraded feed.

---

## 4. Own-vessel `!AIVDO`

By NMEA/IEC convention `!AIVDM` reports *other* vessels and `!AIVDO` reports the transponder's *own* position, generated from the unit's own GPS and navigation sensors. This is a structural distinction in the standard, not vendor-specific behaviour ([gpsd AIVDM/AIVDO reference](https://gpsd.gitlab.io/gpsd/AIVDM.html)).

Every Class B/B+ transponder surveyed — em-trak B9xx, Vesper Cortex, Digital Yacht AIT2000/AIT5000, Garmin AIS 800, Raymarine AIS700, B&G NAIS-500 and V60-B — generates its own-position AIVDO on the NMEA 0183 output as always-on behaviour, because connected MFDs need it to render "own ship." No vendor documentation was found suggesting AIVDO output is toggleable or off by default on any of them. Vesper's Cortex M1 was directly confirmed to push both target data and own-ship data over its WiFi TCP output to third-party apps including Coastal Explorer, TimeZero iBoat and Navionics Boating ([Panbo](https://panbo.com/testing-vesper-cortex-m1-excellent-ais-monitoring-and-much-more-in-one-box/)).

Why this matters for a volunteer network: **aiscast accepts AIVDO**, and for a cruising feeder the own-ship AIVDO is the one message where the station is the authoritative source. A boat in an area with no shore coverage contributes a track nobody else has. Two practical notes for the guide:

- A receive-only station (dAISy, RTL-SDR) will **never** see its own AIVDO — there is no transponder to emit one. AIVDO only appears on transponder-equipped boats.
- Forwarding your own AIVDO is a privacy decision, not just a technical one. It publishes your boat's position under your MMSI. Say so plainly and make it opt-in.

---

## 5. Where a dedicated receiver's bytes go

The receiver hands you a stream of ASCII lines. Something has to move them to a UDP or TCP destination. One line each — a separate document covers the software in depth.

| Tool | What it is | Source |
|---|---|---|
| **AIS-catcher** | Decoder *and* forwarder in one; reads serial with `-e [baudrate] [port]` (e.g. `-e 38400 /dev/serial1`, `-e 115200 /dev/ttyAMA0` for the dAISy-catcher), UDP NMEA in with `-x [server][port]`, and forwards with `-u host port` (UDP), `-P host port` (TCP client), `-S port` (TCP server), `-H url` (HTTP), `-N port` (built-in web UI), `-Q` (MQTT), `-D` (PostgreSQL); serial tuning via `-ge baudrate/init_seq/flowcontrol/dump` | [AIS-catcher CLI docs](https://jvde-github.github.io/AIS-catcher-docs/usage/cli/), [serial input docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/input/serial/) |
| **AIS Dispatcher** | AISHub's free forwarder: serial/TCP/UDP/stdin in, UDP out to 12 destinations (Windows) or unlimited (Linux); CRC validation, dedup, 0–300 s downsampling, message-type and polygon filters, KML export. Windows v1.5.1, Linux x86_64 + ARM/Pi with a web UI, deprecated macOS v1.2. Free | [AISHub](https://www.aishub.net/ais-dispatcher) |
| **kplex** | Classic Unix NMEA 0183 multiplexer: text config of input/output stanzas (serial, TCP, UDP, file), fans one receiver out to several aggregators plus a local WiFi NMEA server. Backend option in OpenPlotter | [OpenPlotter AIS docs](https://openplotter.readthedocs.io/3.x.x/sdr-vhf/ais.html) |
| **socat** | One-liner serial→network relay, e.g. `socat UDP-LISTEN:5005,reuseaddr,fork FILE:/dev/ttyUSB0,b38400,raw,echo=0`. No AIS awareness, no dedup, no reconnect logic | [example](https://gist.github.com/ayusufsirin/479eca2338e853b6ea516bb18a18f574) |
| **ser2net** | Linux daemon exposing a serial port as a raw-TCP or telnet socket, configured in `/etc/ser2net.yaml`; supports RFC 2217 remote serial-parameter control | [ser2net man page](https://www.systutorials.com/docs/linux/man/8-ser2net/) |
| **Signal K server** | The hub option: add a Serial / TCP client / TCP server / UDP NMEA 0183 connection in the admin UI; a TCP server on **port 10110** always echoes all multiplexed input. UDP out needs the `udp-nmea-plugin`; `signalk-to-nmea0183` re-emits internal Signal K data as NMEA 0183 | [Signal K NMEA0183 server docs](https://demo.signalk.org/documentation/Guides/NMEA0183_Data_Server.html), [udp-nmea-plugin](https://github.com/SignalK/udp-nmea-plugin) |
| **OpenCPN** | In Connections, uncheck "Receive Input on this port" and check "Output on this port" to make OpenCPN a source; UDP output needs an explicit target IP and port. Default outgoing talker ID is `EC` | [OpenCPN Connections wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:manual_basic:set_options:connections) |
| **docker-shipfeeder** | sdr-enthusiasts container (renamed from docker-shipxplorer in March 2024) wrapping AIS-catcher plus AirNav's `sxfeeder`; one config feeds ShipXplorer, VesselFinder, MarineTraffic and others; amd64/armhf/arm64 | [GitHub](https://github.com/sdr-enthusiasts/docker-shipfeeder) |
| **Node-RED** | No official AIS forwarder; generic serial-in → udp-out/tcp-out flows, plus `node-red-contrib-ais-decoder` which listens on UDP 10110 and decodes AIVDM to JSON | [node-red-contrib-ais-decoder](https://flows.nodered.org/node/node-red-contrib-ais-decoder) |
| **gpsd** | **Yes, it decodes AIVDM/AIVDO** — but its client model assumes low-rate single-fix GPS polling, and because successive AIS reports of the same type don't obsolete earlier ones in the buffer, a client must poll faster than every 2 s or get stale/duplicate data. Usable, but not the right multiplexer | [gpsd AIVDM docs](https://gpsd.gitlab.io/gpsd/AIVDM.html), [client HOWTO](https://gpsd.gitlab.io/gpsd/client-howto.html) |
| **OpenPlotter** | Raspberry Pi distribution bundling kplex and Signal K with a GUI AIS panel: pick the AIS source (SDR or serial device), assign it to kplex or Signal K, set baud, enable autostart | [OpenPlotter AIS docs](https://openplotter.readthedocs.io/3.x.x/sdr-vhf/ais.html) |
| **NMEA Router** | Windows forwarder; MarineTraffic recommends it specifically when you need many destinations | [MarineTraffic](https://support.marinetraffic.com/en/articles/9552948-what-is-the-best-ais-software-for-my-needs) |

Ethernet-native boxes (Comar Ni/R-series, Digital Yacht AISnet, ShipXplorer SeaRange, Quark-elec A027-plus) skip this table entirely — they push to configured destinations themselves, which is exactly what a marina wants and exactly what a hobbyist who wants to feed a *new* network like aiscast may find limiting, since the destination list is finite (five on the Comar Ni series).

### Physical-layer gotcha

NMEA 0183 on a boat is **RS-422 differential at 4800 baud (or 38400 for "high speed"/AIS)**, not RS-232 and not TTL. To get it into a computer you need an RS-422→USB adapter, not a generic RS-232 cable; wiring TX-A/TX-B backwards produces silence, and grounding one leg of a differential pair to make it single-ended works often enough to be a trap. USB-output receivers (dAISy, Quark A021/A022) and network boxes avoid this entirely, which is one more reason to prefer them for a shore station.

---

## 6. Price/performance tiers and what to recommend

### (a) Hobbyist on land

**Recommended: Raspberry Pi + dAISy-catcher ($149) + a real outdoor VHF/AIS antenna, running AIS-catcher.**

Rationale: it is the only dedicated receiver with SDR-grade sensitivity ([−120 dBm @ 20% PER](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)), it uses 500 mW, there is no gain to tune, and AIS-catcher forwards to every aggregator plus your own endpoint out of the box.

Cheaper alternatives, in order:

| Budget | Build | Trade-off |
|---|---|---|
| ~$30 + Pi you own | RTL-SDR v3 + AIS-catcher | Cheapest; needs gain tuning and real CPU; the v3 outperformed the v4 in the author's own tests ([#333](https://github.com/jvde-github/AIS-catcher/discussions/333)) |
| $79 | dAISy HAT on a Pi | Simplest possible; explicitly lower sensitivity and slower acquisition ([Wegmatt](https://shop.wegmatt.com/products/daisy-hat-ais-receiver)); costs Bluetooth on a Pi 3 |
| $149 | dAISy-catcher | Best messages-per-watt and per-hassle |
| $0 | Apply to MarineTraffic for a free Comar SLR450Ni | Free hardware, but you are feeding their network on their terms and the box only takes five extra destinations |

Spend the difference on antenna height. Every primary source in this document that measured anything concluded the antenna dominates.

### (b) Cruiser with an existing transponder or plotter

**Recommended: forward what you already have. Buy nothing.**

The boat already carries a receiver — the transponder, the AIS-capable VHF, or the plotter. The job is a gateway and a hub, not a receiver:

- **NMEA 0183 already on the boat**: RS-422→USB adapter into a Pi running Signal K, or a WiFi gateway (Digital Yacht WLN10/WLN30, Quark-elec A03x, Yacht Devices YDWG-02) and read its TCP/UDP stream.
- **NMEA 2000 only**: this is the hard path — the AIS is in PGNs, not sentences, and something has to reconstruct `!AIVDM`. See section 3F.
- **Transponder with built-in WiFi** (Vesper, em-trak, Digital Yacht AIT5000): the cheapest possible path — connect to its TCP/UDP NMEA 0183 server and you have raw AIVDM with no extra hardware at all.

Also forward `!AIVDO` if the transponder emits it (section 4) — aiscast accepts it, and it is the one message where the boat is the authoritative source.

The realistic caveat: a cruiser's station is intermittent, moving, and on a metered or absent internet link. Design the feed to buffer and to tolerate long offline periods, and expect the coverage contribution to be "a track through places no shore station sees" rather than "continuous coverage of a port."

### (c) Marina or yacht club wanting a set-and-forget ethernet box

**Recommended: Comar R550Ni or SLR450Ni** if you want the box that the professional networks actually deploy, **Digital Yacht AISnet ($1,049.95)** if you want a fixed price you can put on a purchase order today, or **ShipXplorer SeaRange** if you are happy to be inside AirNav's ecosystem.

| Option | Price | Why |
|---|---|---|
| [Digital Yacht AISnet](https://digitalyachtamerica.com/product/aisnet/) | $1,049.95 | Publicly priced, mains-powered, ethernet, "fit and forget," pushes to a registered IP/port. No computer, no OS to patch |
| Comar R450-X | quote | Lantronix XPort, up to 4 TCP + 1 UDP connections ([Comar](https://comarsystems.com/product/r450-x-network-ais-receiver/)) |
| Comar R550Ni | quote | ARM Cortex-A72, ethernet + WiFi + BT 5.0, ships with Comar Connect forwarding software ([Comar](https://comarsystems.com/product/r550ni-intelligent-network-ais-receiver/)) |
| Comar SLR450Ni | quote / free from MarineTraffic | The current MarineTraffic fleet unit since early 2024; 5 user destinations plus the reserved MarineTraffic stream ([MarineTraffic](https://support.marinetraffic.com/en/articles/9552935-comar-slr450ni)) |
| Quark-elec QK-A027-plus | $232 | Much cheaper ethernet option, plus NMEA 2000 and WiFi ([Quark-Marine](https://www.quark-marine.com/product/a027-plus-nmea-2000-ais-receiver/)) |
| Pi + dAISy-catcher in an enclosure | ~$220 all-in | 5× cheaper than AISnet, better sensitivity, but somebody has to own the Linux box |

The honest advice for a club: if there is a technical volunteer, the Pi build wins on every axis except "who fixes it when they move away." If there is not, buy the appliance.

### Cost summary

| Tier | Hardware | Cost | Sensitivity | Effort |
|---|---|---|---|---|
| Cheapest that works | RTL-SDR v3 + Pi + AIS-catcher | ~$30 + host | Good | Medium (gain tuning, CPU) |
| Simplest | dAISy HAT + Pi | $79 + host | Fair — Wegmatt says below traditional receivers | Lowest |
| Best hobbyist | **dAISy-catcher + Pi** | **$149 + host** | **Best in class for the price, −120 dBm** | Low |
| Boat-friendly | dAISy 2+ | $119 | Fair | Low; 12–36 V input, NMEA 0183 to plotter |
| All-in-one, no computer | Quark-elec QK-A026 / A027-plus | $150 / $232 | Good | Low; WiFi/ethernet + multiplexer + GPS |
| Appliance | Digital Yacht AISnet | $1,049.95 | Good | None |
| Professional | Comar R550Ni / SLR450Ni | quote | Good | None |
| Free | Apply to MarineTraffic / VesselFinder / ShipXplorer | $0 | Good | You feed their network on their terms |

---

## Open questions and gaps

- **No public retail price exists for any Comar unit.** Everything is quote-only; the units mostly reach volunteers through MarineTraffic rather than through a shop.
- Comar's newest R-series (R450-X, R550Ni) publishes no sensitivity figure, so it cannot be compared numerically with the dAISy-catcher's −120 dBm.
- Whether the SR162 is still in active manufacture in 2026 is unconfirmed; listings look like new-old-stock.
- Wegmatt availability was showing sold out across the Uputronics range on 2026-08-22 and the Tindie store is paused until 2027 — check stock before recommending a specific SKU in the guide.
- No independent head-to-head of dAISy-catcher vs Comar SLR450Ni exists. Given the price gap that is the most useful test somebody could run for this community.
