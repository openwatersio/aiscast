# Running an AIS receiving station: every way to get `!AIVDM` to aiscast

Researched 2026-08-22. Answers two questions for a contributor guide: what are all the ways to capture an AIS signal and turn it into bytes that can be forwarded to aiscast, and what are the components in the system and the common options for each. This file is the synthesis; the evidence, with ~450 source links and per-product prices, is in [receiving-stations/](receiving-stations/):

| File | Covers |
|---|---|
| [rf-front-end.md](receiving-stations/rf-front-end.md) | propagation and horizon, antennas (DIY to commercial), coax, connectors, filters and LNAs, bias tee, lightning, mounting, active VHF splitters |
| [sdr-receivers.md](receiving-stations/sdr-receivers.md) | RTL-SDR family (V3/V4/V4L, Nooelec, generics), TCXO and ppm, AIS-catcher input support, sample rates, gain and decoder tuning, pitfalls, multi-receiver |
| [high-end-sdr.md](receiving-stations/high-end-sdr.md) | Airspy, HydraSDR, SDRplay, HackRF, LimeSDR, KrakenSDR, with bench measurements |
| [dedicated-receivers.md](receiving-stations/dedicated-receivers.md) | dAISy family and dAISy-catcher, Quark-elec, Comar, Digital Yacht, NASA, SR162, aggregator-supplied hardware, transponders, VHF radios with AIS, plotters, gateways, NMEA 2000 → `!AIVDM` |
| [compute-power-site.md](receiving-stations/compute-power-site.md) | Pi models and other hosts, OS and storage, power and solar, network and CGNAT, horizon tables, enclosures, cost tiers |
| [software.md](receiving-stations/software.md) | AIS-catcher in depth (install, modes, CLI, config.json, feeding aiscast, filtering), other decoders, forwarders, multi-aggregator feeding and terms, running as a service, metadata and TAG blocks, security |
| [forwarders.md](receiving-stations/forwarders.md), [ops.md](receiving-stations/ops.md), [decoders-legacy.md](receiving-stations/decoders-legacy.md) | exhaustive per-tool detail behind software.md |
| [existing-guides.md](receiving-stations/existing-guides.md) | survey of ~20 existing guides (aggregators, rtl-sdr.com, ADS-B analogues), a recommended outline, and the FAQ people actually ask |

## Headline findings

- The system is a seven-stage pipeline, and only the capture stage has real alternatives: antenna → feedline → optional filter/LNA → receiver → decoder → forwarder → uplink. Everything downstream of the receiver has collapsed onto one program, AIS-catcher, which is decoder, forwarder, multiplexer and dashboard in one binary and also reads serial/USB/network NMEA from non-SDR receivers (`-e`, `-x`, `-t`).
- There are four capture paths, and no existing guide presents them side by side: (1) an SDR dongle plus AIS-catcher (~$35 for the receiver, the community default); (2) a dedicated receiver, where the Wegmatt dAISy-catcher ($149, co-designed with the AIS-catcher author, −120 dBm @ 20% PER, 500 mW) is the 2026 standout and the older dAISy HAT/2+ are simpler but, per Wegmatt's own pages, less sensitive; (3) marine electronics a boat already has (transponder, AIS-capable VHF, receiver, plotter) reached over NMEA 0183, WiFi, or NMEA 2000 via Signal K; (4) set-and-forget ethernet appliances (Comar R/SLR series, Digital Yacht AISnet $1,050) including free hardware from MarineTraffic, ShipXplorer and VesselFinder on their terms.
- Antenna height beats every other decision. Range is the station's radio horizon plus the ship's (`d_nm ≈ 1.23√h_ft`, `d_km ≈ 4.12√h_m`): a 10 m antenna hears a 30 m ship at ~19 nm, a 100 m hilltop at ~35 nm. Every primary source (Wegmatt, MarineTraffic, RTL-SDR Blog, the AIS-catcher community) says the antenna and its elevation are the most important piece of equipment. Gain cannot see past the horizon; hills are showstoppers.
- The antenna recommendation for most people is a $40–100 marine VHF whip (Tram 1600-HC, Shakespeare 5215 or 5215-AIS, Glomex RA300AIS); the stock RTL-SDR dipole at 44 cm per element is a verification antenna only. The best-documented DIY option, an 8-section coax collinear with ground plane, measured 35–45 miles versus "slightly better than wire" for a generic whip at the same site.
- The receiver matters less than folklore says. RTL-SDR Blog V3 ($34.95) is the de facto default, the device AIS-catcher was developed against, and on an independent August 2026 bench the most sensitive device at VHF. The V4 is EOL (counterfeit R828D supply) and was worse for AIS by 2–3 dB; the V4L (Aug 2026) is too new; Airspy and SDRplay deliver −10% to +15% messages for 3–8× the price. A TCXO is non-negotiable (generic dongles measure 44 ppm off and drift 6–7 ppm warming up; AIS-catcher's don't-care band is ±3 ppm).
- Filters and LNAs are a targeted fix, not an upgrade: a SAW-filtered dongle gave +10–25% messages at a noisy shared site, two Uputronics filtered preamps gave "no improvement" at a quiet one, and the RTL-SDR wideband LNA made reception worse near transmitters. First purchase if the noise floor plot says so: a $33 passive 160–162 MHz bandpass; a filtered LNA only at the mast head to beat long coax.
- Coax loss is silently expensive (RG-58 ≈ 1.6 dB per 10 m at 162 MHz; 1 dB ≈ 12% range ≈ 26% coverage area), and the right answer for any long run is no coax: a Pi and dongle in an IP65 box at the antenna, with PoE Ethernet down. MarineTraffic's own install guide says the same.
- Compute is a non-issue. A Pi Zero 2 W runs AIS-catcher for AIS alone (CPU load ~0.2); Pi 3B+ is the sweet spot; Pi 4/5 are overkill for one dongle; any x86 box works; Android phones work via OTG; there is no ESP32 decoder and no OpenWrt path. A whole station draws 2–4 W. There is no prebuilt AIS-catcher SD image; the install script with `-M` gives a browser setup wizard on port 8118, and `ghcr.io/jvde-github/ais-catcher` is the Docker path.
- Feeding aiscast is three flags or fewer: `-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15` (authenticated, works behind any NAT, preferred), `-u ais.openwaters.io 10110` (no token), or the `signalk-aiscast` plugin on a boat. `UDP_FEEDS=ais.openwaters.io:10110` is the docker-shipfeeder equivalent. Multi-homing to many aggregators is normal and nobody's terms forbid it; MarineTraffic's licence is explicitly non-exclusive, AISHub's only prohibition is on provenance (relaying other services' data).
- Several things widely repeated in existing guides are wrong or gone: there is no Nooelec SAWbird+ AIS and no RTL-SDR Blog AIS filter; `sdr-enthusiasts/docker-aiscatcher` does not exist (it is `docker-shipfeeder`); AISdeco2 is dead and was never AISHub's; rtl-sdr.com's top-ranked AIS tutorial still recommends it; TAG blocks are `msgformat NMEA_TAG`, not `-o 5`; AIS-catcher shares anonymously with aiscatcher.org unless `-X off` (source disagrees with docs); the Vesper SP160 splitter and XB-8000 transponder are discontinued after the Garmin acquisition; Comar's AST200 is gone; kplex.net has no DNS record.

## The pipeline

```
  air ──► antenna ──► feedline ──► [filter / LNA] ──► receiver ──► decoder ──► forwarder ──► uplink ──► aiscast
          (VHF whip,   (coax or    (SAW BPF,           (SDR dongle   (AIS-catcher,  (AIS-catcher   (UDP :10110,
           collinear,   Ethernet    filtered preamp,    or dedicated   or inside the  -u / -H,       HTTP /v1/receive,
           dipole)      to a Pi at  active splitter)    AIS receiver,  dedicated      docker-        Signal K plugin)
                        the mast)                       or the boat's  receiver)      shipfeeder,
                                                        electronics)                  Signal K, socat)
```

The bytes that cross the last hop are NMEA 0183 `!AIVDM` (other vessels) and `!AIVDO` (own vessel from a transponder) sentences, optionally wrapped in NMEA 4.10 TAG blocks (`s:` station, `c:` time) or in AIS-catcher's `jsonaiscatcher` HTTP envelope. aiscast ingests both: UDP datagrams of newline-separated sentences (500 sentences/s per source address, TAG blocks honoured, sender identified as `udp:<keyed hash>` or `mmsi:<own MMSI>` once it sees `!AIVDO`), and `POST /v1/receive` with AIS-catcher's envelope or plain NMEA, gzip accepted, 600 posts/min, `rxtime` used when within 30 s of arrival ([the API reference](https://openwaters.io/api/ais/)). aiscast's UDP listener splits each datagram on `\n`, so any forwarder must emit whole sentences per datagram (socat needs `icanon=1`, not `raw`).

## The four capture paths

| Path | What produces the bytes | Receiver cost | How it reaches aiscast | Who it suits | Trade-off |
|---|---|---|---|---|---|
| **SDR + AIS-catcher** | RTL-SDR Blog V3 (or Airspy/SDRplay/HydraSDR/HackRF) on a Pi or PC; AIS-catcher demodulates both channels from one IQ stream | ~$35 | AIS-catcher `-H` / `-u` directly | land hobbyist; anyone who already has a dongle | needs a host CPU, gain/ppm attention, USB reliability; most flexible and cheapest |
| **Dedicated receiver** | dAISy-catcher ($149, USB or Pi HAT, driven by AIS-catcher over serial at 115200), dAISy HAT ($79) / dAISy 2+ ($119) / dAISy USB ($79) / Quark-elec QK-A021/A022/A026 ($55–150) emitting `!AIVDM` at 38400 baud | $55–150 | AIS-catcher `-e <baud> <port>`, or socat / AIS Dispatcher / Signal K | hobbyist who wants no RF tuning; remote or solar sites (≤0.5 W) | dAISy-catcher: SDR-grade sensitivity, no ppm, no thermal drift; HAT/2+: simplest but Wegmatt says lower sensitivity and slower acquisition; supply lumpy (Tindie paused, Uputronics sold out 2026-08-22) |
| **Boat electronics you already have** | Class B/B+ transponder (em-trak B92x/B95x, Vesper Cortex, Digital Yacht AIT2000/5000, Garmin AIS 800, Raymarine AIS700, NAIS-500, B&G V60-B), AIS-capable VHF (Standard Horizon GX2400/GX6000 emit genuine `!AIVDM` with talker `AI`; Icom M510/M605 over 0183/N2K), receive-only units (Raymarine AIS350, DY AIS100), or a plotter | $0 | NMEA 0183 (RS-422 → USB into Signal K or AIS-catcher), the device's own WiFi TCP/UDP server (Vesper, em-trak B922/B924/B954, AIT5000 are the easiest feeders that exist), or NMEA 2000 → Signal K + `signalk-n2kais-to-nmea0183` or a YDWG-02/YDEN-02 gateway | cruisers; mobile coverage through places no shore station sees; the only path that yields `!AIVDO` | N2K-only boats (Raymarine Axiom has no 0183 port) need reconstruction, which re-encodes and may drop types 5/24; intermittent and metered links; forwarding own `!AIVDO` is a privacy decision |
| **Ethernet appliance / supplied hardware** | Comar R450-X / R550Ni / SLR450Ni (quote-only; SLR450Ni is what MarineTraffic hands out, five user destinations), Digital Yacht AISnet ($1,049.95), ShipXplorer SeaRange, Quark-elec QK-A027-plus ($232, ethernet + N2K + WiFi) | $232–1,050 or free | the box pushes NMEA to configured TCP/UDP destinations itself | marina, yacht club, anyone who wants no Linux box | finite destination list; free units come with a perpetual licence to the supplier's data (MarineTraffic "perpetual, transferable, irrevocable", non-exclusive); a Pi + dAISy-catcher in an enclosure is ~5× cheaper and more sensitive |

Paths also combine: a Pi running AIS-catcher with a dongle can take a second NMEA source in over `-x`/`-t`, and docker-shipfeeder fans any of them out to ~18 aggregators plus aiscast.

## Components and options

### Antenna

162 MHz (λ = 1.85 m; quarter wave 44 cm, half wave 88 cm after velocity factor), vertical polarization always. Class A transmits 12.5 W, Class B/SO 5 W, Class B/CS 2 W, so a station that hears Class A at 40 nm loses small craft inside 15 nm; compare ranges by class.

| Tier | Option | Cost | Evidence |
|---|---|---|---|
| 0, verify it works | stock RTL-SDR Blog dipole, both elements at 44 cm, vertical, in a window | $0–18 | "enough to see your first ships in a busy area" (AIS-catcher docs); a few miles indoors |
| 1, DIY | quarter-wave ground plane (44 cm vertical, 46–48 cm radials at 30–45°), Slim Jim / J-pole, coax collinear | $0–20 | Arun Dale measured: wire ~10 mi, generic marine whip slightly better, Diamond F-22 20–25 mi, 4-section collinear 25–30 mi, 8-section + ground plane 35–45 mi |
| 2, the recommendation | Tram 1600-HC ($40–50, Wegmatt's pick), Shakespeare 5215 / 5215-AIS / 5250-AIS ($85–100), Glomex RA300AIS (€49), Digital Yacht KS30 (£85), Metz AIS (€140) | $40–140 | "-AIS" tuning matters on 8 ft Galaxy-class antennas (3 MHz spec window around 156.8 MHz, 162 is outside it), barely on a 3 ft 3 dB whip; take it if within $20 |
| 3, fixed shore mast | Shakespeare 5225-XT-AIS 8 ft 6 dB ($200–260), Morad VHF-162 HD-AIS 6 dB ($263) / 10 dB ($1,150), DIY 9 dB collinear ($20) | $20–1,150 | high gain flattens the lobe: useless on a heeling sailboat, and even ashore can put nearby low-antenna small craft below the beam; 3 dB for harbour watching, gain for a distant lane |
| already owned | Diamond X30/X50, Comet GP-3 (ham dual-band, ~10% off frequency) | — | work (45–50 nm reported on an X30); not worth buying new |
| avoid | discones and wideband scanner antennas; the antenna bundled with a DVB-T dongle; TV antennas | — | broadband front-end overload; OpenCPN wiki warns explicitly |

### Feedline and connectors

| Cable | dB/100 ft @ 162 MHz | 10 m run | Note |
|---|---|---|---|
| RG-174 | 11.1 | 3.6 dB | jumpers only |
| RG-58 | 5.0 | 1.6 dB | fine ≤10–15 m; MarineTraffic's limit is 15–20 m |
| RG-8X | 4.0 | 1.3 dB | ships with most marine antennas |
| LMR-240 | 3.1 | 1.0 dB | sweet spot ≤15 m |
| RG-213 | 2.8 | 0.9 dB | thick, cheap, stiff |
| LMR-400 | 1.6 | 0.5 dB | standard for long runs |

Loss ahead of the first amplifier adds to noise figure one for one. Marine antennas are SO-239; dongles are SMA female (Nooelec Mini: MCX); splitter AIS ports are BNC. The PL-259→SMA adapter ($4–12) is the part everyone forgets. UHF connectors are fine at 162 MHz. Past ~20 m, put the receiver at the antenna and run Ethernet (PoE+ HAT or a PoE→USB-C splitter, ~$14), minding heat in a sealed box (oscillator drift; the official PoE+ HAT is rated to 50 °C).

### Filters, LNAs, splitters

| Symptom | Fix | Products |
|---|---|---|
| noise floor swings, message count dips, urban / near FM, pagers, LTE | passive bandpass first | GPIO Labs 160–162 MHz BPF $33 (2.8 dB insertion loss); Wegmatt TB0436A narrowband SAW $29 (3–6 dB, ~100 kHz); RTL-SDR Blog FM band-stop $16.95 |
| coax run >20–30 m that cannot be shortened | filtered LNA **at the antenna**, bias-tee powered | Uputronics 162 MHz filtered preamp $59 / £44 (22 dB, 0.78 dB NF, 50 mA, powered by the V3's 4.5 V bias tee); GPIO Labs AIS filtered LNA $84 |
| quiet rural site, short coax | usually nothing | wideband LNAs (RTL-SDR Blog $19.95) degrade reception near any strong signal |
| must share the boat's VHF antenna | **active** splitter only | Glomex RA201 (~€100–150, the one Wegmatt customers report success with), Digital Yacht SPL1500 £275 / SPL2000 £315 (fail-safe), Comar AS350 (20 µs switching), em-trak S300 ($360), Shakespeare 5257-S (~$75, receive-only). Vesper SP160 and Comar AST200 discontinued |

A passive splitter puts ~+40 dBm of a 25 W radio into a receiver rated 0 dBm absolute maximum; it destroys the front end on the first transmission. Ashore, a second antenna is cheaper and strictly better; NMEA's minimum separation from a transmitting antenna is 6 ft, Wegmatt says ≥3 m from radar or VHF. Never enable a dongle's bias tee into a DC-grounded antenna (Shakespeare 5225-XT-AIS is DC-shorted) or into a splitter's AIS port; DC-blocked lightning arrestors silently kill bias-tee power (use a gas-discharge type).

### Receiver: SDR dongles

| Device | Price | TCXO | Bias tee | Verdict |
|---|---|---|---|---|
| **RTL-SDR Blog V3** | $34.95 ($44.95 with dipole kit) | 1 ppm | yes | the default; AIS-catcher's reference device; most sensitive at VHF on the Tech-ni-shn bench; V5 not before 2027 |
| RTL-SDR Blog V4 | $44.95, **EOL** | 1 ppm | yes | 2–3 dB less sensitive at VHF by the vendor's own admission; driver-lag failure mode was zero messages; an operator measured it worse than V3 |
| RTL-SDR Blog V4L "Lite" | $37.95, Aug 2026 | 1 ppm | yes | diplexed, no notches, "identical intermod to V3", possibly the better AIS dongle, but driver support is weeks old and China-warehouse only; not for a roof today |
| Nooelec NESDR SMArTee v2 | $41.95 | 0.5 ppm | yes | fine; the only Nooelec with a bias tee; off the beaten path for support |
| Nooelec SMArt v5 / Nano 3 / Mini 2+ | $42–48 | 0.5 ppm | no | fine, no mast-head LNA |
| ShipXplorer AIS dongle | ~$67–85 | yes | n/a | RTL-SDR + LNA + SAW filter; +10–25% over a V3 in the maintainer's Meteotoren test; patchy stock |
| generic no-name DVB-T stick | $10–20 | no | no | weekend test only; 44 ppm offset, drift, counterfeits |
| Airspy Mini / R2 / HF+ Discovery, HydraSDR, SDRplay RSP1B/RSPdx-R2 | $99–292 | yes | varies | use if owned; not worth buying for AIS (−10% to +15%); RSPdx-R2/RSPduo DAB notch covers 160–230 MHz if enabled; SDRplay needs a source build on Linux and is excluded from Docker |
| HackRF One/Pro, LimeSDR Mini 2.0, KrakenSDR | $340–750 | varies | — | wrong tool: 8-bit/10 dB NF, no native driver, or direction-finding coherence AIS cannot use |

One dongle receives both channels (both sit within ±25 kHz of 162.000 MHz); dual/quad dongles buy nothing except the long-range C/D pair at 156.8 MHz (`-c CD` on a second receiver). Plug into USB 2, not a USB 3 hub; prefer longer coax to a longer USB cable, and longer Ethernet to either.

### Receiver: dedicated and marine hardware

| Device | Price | Channels | Output | Note |
|---|---|---|---|---|
| **Wegmatt dAISy-catcher** | $149 (+$39 GPS, +$19 case) | dual, all four AIS frequencies | USB or Pi HAT, 115200 serial, driven by AIS-catcher (`-e 115200 /dev/serial0 -ge init_seq co2,v`) | −120 dBm @ 20% PER; "comparable to AIS-catcher with an Airspy HF+" at half the power; 3–4× the dAISy HAT's vessels in beta; hardware attenuator and overload LED |
| dAISy HAT | $79 | dual | Pi UART0 @ 38400 | simplest; takes Bluetooth's UART on a Pi 3; Wegmatt: lower sensitivity than traditional receivers |
| dAISy 2+ | $119 | dual | USB + NMEA 0183 terminal block, 12–36 V | the one for a plotter or boat power; ~40% more messages than the single-channel original |
| dAISy (original) | $79 | single, hopping | USB only, no 0183 | hopping loses messages in busy water |
| dAISy Mini / FeatherWing | $85–109 | dual, LNA | serial, I2C, JST-GH / Feather | embedded and drone integration |
| Quark-elec QK-A021 / A022 / A026 / A027-plus | $55 / — / $150 / $232 | single (A021) or dual | USB, 0183, WiFi, GPS, multiplexer; A027-plus adds ethernet and N2K | receiver + gateway in one box, no computer; resellers mislabel hopping units as dual |
| Comar R250 / R450-X / R550Ni / SLR450Ni | quote only | dual | USB+WiFi / ethernet (Lantronix, 4 TCP + 1 UDP) / ARM box with Comar Connect / Pi-based, 5 destinations | the OEM behind MarineTraffic's free receivers |
| Digital Yacht AISnet / AIS100 | $1,049.95 / ~$200 | dual | ethernet / serial-USB | the publicly priced appliance; AIS100 needs a host |
| NASA/Clipper AIS Engine 3, SR162, Matsutec HA-102, AliExpress "dual" USB | $181 / ~$359 NOS / $275 / $30–100 | hopping / dual / transponder / often hopping | RS-232 / RS-232-422 / black box / USB | Engine 3 hops; SR162 is new-old-stock; Matsutec is a 5 W transponder (not for shore); generics lose to a V3 |
| Transponders with WiFi (Vesper Cortex $1,299+, em-trak B922/B924/B954, Digital Yacht AIT5000) | already owned | dual | WiFi TCP/UDP NMEA 0183 incl. `!AIVDO` | easiest feeders in existence; XB-8000 discontinued but widely installed |
| Standard Horizon GX2400 / GX6000, Icom M510 / M605, B&G V60-B | already owned | dual | 0183 (`!AIVDM`, talker `AI`) and N2K | an AIS VHF is a real feeder |
| Gateways: Yacht Devices YDWG-02 / YDEN-02 / YDNU-02 ($249), Actisense W2K-1 (~$250–286), Quark QK-A032 ($160), ShipModul MiniPlex-3Wi-N2K ($556) | — | — | N2K → WiFi/ethernet/USB NMEA 0183 | YD confirm VDM/VDO ↔ PGN 129038/129039; other PGNs unconfirmed; Actisense NGW-1-AIS goes the wrong way (0183→N2K) |

NMEA 2000 carries AIS as PGNs (129038/129039/129040/129041/129793/129794/129809/129810), not sentences; reconstructing `!AIVDM` re-encodes the payload, so it will not dedupe against the same message heard off-air, and some gateways drop types 5/24 (names). The correct-direction tool is Signal K + `signalk-n2kais-to-nmea0183` (v2.0.3, Aug 2025); canboat's n2kd emits JSON only (AIVDM encoder requested since 2018). Prefer any 0183 source on the boat over N2K reconstruction; verify by logging an hour and checking types 1/2/3, 5, 18, 19, 21, 24 all appear. Plotter WiFi is a trap: Raymarine Axiom is N2K-only, Furuno NavNet prepends an 8-byte proprietary preamble to NMEA-over-UDP, Navico GoFree and Garmin ActiveCaptain relaying are unverified; log the bytes with `nc` before designing around a plotter.

### Compute, power, network

| Component | Options | Finding |
|---|---|---|
| Host | Pi Zero 2 W ($15), Pi 3B+ (sweet spot), Pi 4/5 (overkill for one dongle), any x86 box or NAS via Docker, Android via USB-OTG (battery drain, portable use), Windows/macOS | original Pi 1 / Zero unsupported by prebuilt packages; Zero 2 W may need `-F` or `-s 288K`; no ESP32 decoder exists (dAISy is STM32, not ESP32); no OpenWrt path; Orange Pi/Radxa/Synology/DietPi unverified but standard Debian/Docker paths should apply |
| OS / install | `sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/jvde-github/AIS-catcher/main/scripts/aiscatcher-install) -p -M"` on Raspberry Pi OS/Debian/Ubuntu/Fedora; `ghcr.io/jvde-github/ais-catcher` (`latest`/`edge`); Windows zip + Zadig; macOS build from source (no Homebrew formula); Android app | no prebuilt SD image (SARCNET Mk3 is a third-party Zero 2 W image); managed mode wizard on 8118, viewer 8119, control package 8110, manual viewer 8100; switching manual→managed discards existing config |
| Storage | microSD (A2), USB SSD boot (Pi 4/5), overlay read-only root | AIS-catcher barely writes; journald defaults to 10% of disk; cap it, move swap to zram, tmpfs `/tmp`; overlayfs loses `stat.bin` and config edits |
| Power | official Pi PSU; PoE+ HAT (20 W) or PoE→USB-C splitter; 12 V buck converter on boats; solar | Pi Zero 2 W + dongle ≈ 2–4 W; documented solar build: 10 W panel + 12 V 7 Ah SLA (15 W with a 4G modem); underpowered USB chargers are the ADS-B community's #1 failure mode and apply here |
| Network | Ethernet > Wi-Fi at a fixed site; cellular/Starlink fine for outbound | single station well under 1 GB/month raw; outbound UDP/HTTP works behind CGNAT; inbound dashboard needs Tailscale / Raspberry Pi Connect / ZeroTier, never a port-forward of 8100/8118 |
| Enclosure | IP65 box, cable glands, bulkhead SMA, desiccant, shade | heat is the failure mode at a mast head (oscillator drift, PoE+ HAT 50 °C limit) |

### Software: decoders

| Software | Status | Role |
|---|---|---|
| **AIS-catcher** v0.70 (2026-06-19), GPL-3.0, daily commits, ~3,700 registered stations | **use this** | RTL-SDR/Airspy/HF+/HydraSDR/HackRF/SDRplay/Soapy/SpyServer/RTL-TCP/ZMQ/serial/UDP/TCP/N2K-socketCAN/file in; UDP/TCP/HTTP/MQTT/N2K/DB/file out; web viewer with signal and ppm plots; Prometheus; Python bindings `aiscat` |
| SDRangel v7.27.2 | alive, GUI | one AIS demod per channel at ±25 kHz, UDP out in NMEA format; a second opinion when AIS-catcher hears nothing |
| rtl_ais | no release since 2018, PRs merged Aug 2026 | not packaged for bookworm/noble, so its audience builds from source anyway; recommend only to existing users |
| gnuais, gr-ais, AISdeco2, AISMon | dead | gnuais has no network output at all; AISdeco2's site returns 500 (and was Sergey Serov's, not AISHub's); rtl-sdr.com's top tutorial still recommends AISdeco2 |
| AISRec, ShipPlotter (€25), AIS Decoder / NMEA Router | Windows niche, frozen | sound-card era; NMEA Router is a router, not a demodulator |
| pyais, libais, AisLib, libaisdemod (new, Aug 2026) | libraries | for consumers of sentences, not producers |

Defaults are already right: `-m 2`, `AFC_WIDE on` (±10 ppm tolerant), `DROOP on` (+4.2%), 1536K. Recommended start `AIS-catcher -gr RTLAGC on TUNER auto -a 192K`; experienced operators then fix the tuner gain (AGC cannot settle on an AIS burst) using the viewer's signal-level plot, and try `-go sensitivity_high on` with spare CPU. `-s 2304K` is cargo cult; `-F` or `-s 288K` for weak hosts.

### Software: forwarders for non-SDR receivers

| Tool | Verdict |
|---|---|
| **AIS-catcher `-e`/`-x`/`-t`** | best default even without an SDR: dashboard plus fan-out, `-u ... reset 60` fixes stale sockets |
| **docker-shipfeeder** (sdr-enthusiasts, wraps AIS-catcher + sxfeeder) | best for feeding many aggregators; `UDP_FEEDS=ais.openwaters.io:10110` (comma-separated; README's semicolon is a docs bug) or `AISCATCHER_EXTRA_OPTIONS=-H ...` |
| **Signal K** 2.31.1 + `ais-forwarder` (v0.4.1, 2023; `aivdm`/`aivdo` default **off**) or `signalk-aiscast` | right on a boat; `udp-nmea-plugin` needs `lineDelimiter: LF`; no built-in HTTP output |
| socat + systemd | simplest bridge; `icanon=1` and `UDP-SENDTO`; exits on unplug (Restart=always), resolves DNS once |
| AIS Dispatcher (AISHub) Linux 2.3 (Apr 2026) | works, forwards anywhere, UDP out only, feeds AISHub by default, web UI `admin`/`admin` on all interfaces, auto-updates, no licence text |
| kplex | works, abandoned (v1.4 2019, site gone, open WLAN-reconnect bug) |
| ser2net, OpenCPN, Node-RED, gpsd, sxfeeder | wrong shape, desktop-only, fine-if-already-running, no TAG blocks, ShipXplorer-only respectively |

### Feeding aiscast

```bash
# SDR, authenticated HTTP (preferred), with dashboard
AIS-catcher -gr RTLAGC on TUNER auto -a 192K \
  -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15 RESPONSE off \
  -N 8100 station "My Station" lat 41.5 lon -70.6 share_loc on -q

# UDP, no token, optionally tag-blocked
AIS-catcher -u ais.openwaters.io 10110 msgformat NMEA_TAG

# dAISy HAT as the receiver
AIS-catcher -e 38400 /dev/serial0 -N 8100 -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15
```

Token: one click at [openwatersio.github.io/aiscast/token.html](https://openwatersio.github.io/aiscast/token.html). Put it in `/etc/AIS-catcher/config.json` (`chmod 600`), not `config.cmd`. Station position lives in three independent places (`-Z`, `-N ... lat lon share_loc on`, `-H ... lat lon`). Do not downsample for aiscast (cross-station comparison is the point); `position_interval` and `allow_type` exist for metered links. The installer's systemd unit runs as user `aiscatcher` with `Restart=always`; enable `aiscatcher-install --set-reboot-on-failure` for wedged dongles; alarm on `/api/stat.json`'s monotonic `received` counter, not `msg_rate`.

## What to expect

| Antenna height | Your horizon | Range to a ship with a 15 m antenna | with a 30 m antenna |
|---|---|---|---|
| 2 m (rail) | 3.2 nm | 11.8 nm | 15.4 nm |
| 10 m (roof, masthead) | 7.0 nm | 15.6 nm | 19.2 nm |
| 20 m | 9.9 nm | 18.5 nm | 22.1 nm |
| 100 m (hilltop) | 22.3 nm | 30.9 nm | 34.5 nm |
| 700 m (island mountain) | 59 nm | 67.6 nm | 71.2 nm |

MarineTraffic's published figures match (15 m → 15–20 nm, 20 m → ~25 nm, elevated → 40–60 nm, 700 m → 200 nm); AIS-catcher's docs say a 5–10 m rooftop antenna hears large ships to 25–30 km. A good UK coastal station logs >1,000 messages/min; tropospheric ducting produces 140–700 nm outliers that are weather, not equipment. AISHub's quality bar (≥10 vessels, ≥90% uptime, ≤60 s downsampling, ≤10 s delay) is a reasonable definition of "good station" for any network.

## Recommended builds

| Build | Parts | Cost | Notes |
|---|---|---|---|
| Try it | RTL-SDR Blog V3 + dipole kit, Pi Zero 2 W, PSU + SD | ~$80 | dipole at 44 cm in the highest window; `-F` on the Zero |
| Most volunteer stations | V3, Pi 3B+/4, official PSU, Tram 1600-HC or Shakespeare 5215(-AIS) on the roof, ≤10 m LMR-240/RG-8X, PL-259→SMA, self-amalgamating tape | $150–250 | outperforms a $600 antenna in an attic |
| Plug-and-play / remote / solar | dAISy-catcher HAT on a Pi (Zero 2 W for solar), marine whip, AIS-catcher | ~$220 | no ppm, no drift, 0.5 W receiver; enclosure + PoE or 10 W panel + 7 Ah SLA |
| Diagnosed problems | + GPIO Labs BPF $33 (noise floor) or Uputronics filtered preamp $59 at the mast (long coax) or both in the GPIO Labs filtered LNA $84 | +$30–90 | only after watching the viewer's plots |
| Serious fixed station | 6 dB Shakespeare 5225-XT-AIS or Morad, or DIY 9 dB collinear, filtered preamp at the antenna, LMR-400, GDT arrestor, dAISy-catcher or Airspy for strong-signal handling | $300–600 | beyond this, spend on elevation |
| Cruiser | nothing new: transponder/VHF/plotter → Signal K (`signalk-aiscast`) or WiFi NMEA → AIS-catcher `-t`/`-x` | $0 | forward `!AIVDO` as an explicit opt-in |
| Club / marina with no volunteer | Digital Yacht AISnet, Comar R550Ni/SLR450Ni, or free MarineTraffic hardware | $0–1,050 | appliance vs. a Pi + dAISy-catcher at 5× less |

## Corrections to common guidance

- No Nooelec SAWbird+ AIS; no RTL-SDR Blog AIS filter or filtered dongle. Real AIS-specific parts: Uputronics 162 MHz preamp, GPIO Labs filter/LNA, Wegmatt TB0436A SAW, ShipXplorer dongle.
- No `sdr-enthusiasts/docker-aiscatcher`; no AIS-catcher ESP32 port; no `install_debian_ubuntu.sh` (it is `scripts/aiscatcher-install`); the `.deb` ships no systemd unit (the script writes it).
- TAG blocks: `msgformat NMEA_TAG` on a network output or `-o 7` on screen.
- Community sharing to aiscatcher.org is enabled in `Engine.cpp` whenever no `-X` is given and any output exists; `-X off` to opt out (docs say off by default).
- V3 vs V4: newer is not better for AIS; the V4 is EOL anyway. Airspy Mini's slowest rate is 3 MSPS and R2's 2.5 is "not recommended", so the HF+ Discovery at 192K is the Airspy for a Pi.
- dAISy is STM32-based, not ESP32. The "−11 dBm vs −85 dBm" RTL-SDR/dAISy numbers are uncalibrated-vs-calibrated levels, not a sensitivity comparison.
- Vesper SP160 and XB-8000, Comar AST200/AST300/ASR-100, Garmin AIS 300/600, RSP1A, RSPdx, HackRF One, LimeSDR Mini v1: discontinued. kplex.net: gone. AISdeco2: gone.
- MarineTraffic's feeder licence is non-exclusive; AISHub's "prohibited" list is about provenance; "don't feed twice" means UDP+TCP to the same service, not multi-homing. No volunteer AIS network applies an open licence to its aggregate.

## Open gaps

- No rigorous public V3-vs-V4 AIS benchmark, and no measured V4 insertion loss at 162 MHz; the V4L has no AIS data at all yet.
- No HydraSDR AIS benchmark; no dAISy-catcher vs Comar SLR450Ni head-to-head (the most useful test someone could run); no independent dAISy-catcher vs V3 message counts beyond Wegmatt's beta claims.
- aiscatcher.org blocks automated fetches; leaderboard figures for "what a great station achieves" need a manual browser pull.
- Reddit and several vendor forums were unreachable; community sentiment is inferred from AIS-catcher discussions and terms text, not forum threads.
- Navico GoFree AIS-over-WiFi, Garmin ActiveCaptain relaying, Icom transmit-suppression, YDWG round-tripping of PGNs other than 129038/129039, Synology/DietPi/Orange Pi specifics, Pi 5 benchmarks, inland-river station reports, Comar retail prices, Airspy/SDRplay EUR prices: unverified.
- Whether AIS-catcher posts an empty HTTP batch as a heartbeat during silence is unverified; do not build uptime monitoring on it.
