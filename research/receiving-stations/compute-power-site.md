# Volunteer AIS Station: Compute, Power, Network, and Site Research

Research compiled 2026-08-22 for a public guide to running a volunteer AIS receiving station. All claims are sourced inline; anything not independently confirmed is flagged **[UNVERIFIED]**. Primary sources used: [AIS-catcher GitHub](https://github.com/jvde-github/AIS-catcher), [AIS-catcher docs](https://jvde-github.github.io/AIS-catcher-docs/), [aiscatcher.org](https://www.aiscatcher.org/), RTL-SDR blog, Raspberry Pi docs, and community forum threads.

## 1. Compute options

### What AIS-catcher actually needs

AIS-catcher's own docs don't publish a minimum CPU/RAM spec — the software is described simply as turning "an inexpensive SDR dongle into a complete AIS receiving station, set up and run entirely from the browser" ([Overview](https://jvde-github.github.io/AIS-catcher-docs/getting-started/overview/)). The practical constraint is one hard compatibility cutoff plus a well-documented CPU/sample-rate tradeoff:

- **Original Pi 1 and the original Pi Zero/Zero W are explicitly unsupported** by the prebuilt packages: "The pre-built packages are not compatible with the first versions of the Raspberry Pi and Zero due to their limited support for floating point hardware acceleration" ([Raspberry Pi install docs](https://jvde-github.github.io/AIS-catcher-docs/installation/raspberrypi/)).
- AIS-catcher auto-selects a sample rate but supports `-s` to override; internally it upsamples to one of a fixed list — 96K/192K/288K/384K/768K/1536K/3072K/6144K/12288K — and "there is no efficiency advantage of using other rates than in this list apart from limiting the bandwidth and data throughput" ([Sample Rate docs](https://jvde-github.github.io/AIS-catcher-docs/advanced/samplerate/)). Lower rate = less CPU, at some cost to sensitivity on marginal signals. Troubleshooting guidance for dropped-sample situations is to drop to a lower sample rate like 288000, per search-indexed content from the docs' [Troubleshooting page](https://docs.aiscatcher.org/advanced/troubleshooting/) (direct fetch of this page 403'd for the fetch tool; content came through via search index).

### Raspberry Pi Zero 2 W

This is the most-asked question and the evidence is consistent: **it works for AIS-only reception**, it just can't also decode ADS-B at the same time.

- A combined ADS-B+AIS guide states plainly: for Pi 1B/1B+/original Zero, "insufficient computing power for dual operation"; Pi 2B/3B/3B+ "work very well"; Pi 4B is "overkill but works without issues" (via search-indexed content from [chaos-consulting/adsberry ais.md](https://github.com/chaos-consulting/adsberry/blob/master/ais.md), direct fetch 403'd).
- A from-scratch build log for a project "designed exclusively for the Raspberry Pi Zero 2W" (RTL-SDR Blog V3/V4 dongles) reports it "works fine on the Raspberry Pi 3B+" too, and specifically measured **CPU load averaging around 0.20 on both the Pi Zero 2 W and Pi 3B+** — i.e., barely loaded ([AIS Receiver Mk3](https://www.sarcnet.org/ais-receiver.html) via search index).
- A solar-powered remote-station build explicitly chose the **Pi Zero 2 W "for its lightweight architecture and minimal power consumption"** as the whole-system platform, running RTL-SDR V4 continuously off a 10 W solar panel and 12 V 7 Ah SLA battery ([DIY Raspberry Pi AIS Receiver, worldwideais.org](https://www.worldwideais.org/post/diy-raspberry-pi-ais-receiver-build-a-solar-powered-station-for-under-100)).
- One community discussion notes the Pi Zero 2 W "barely uses resources" for AIS reception, to the point that a Pi 5 is "overkill" for the task and is better spent on other services (via search index, sources include the AIS-catcher docs and general Pi-hobbyist commentary).
- Caveat: a single GitHub discussion thread ([#316, Fitipower FC0012 segfault](https://github.com/jvde-github/AIS-catcher/discussions/316)) reports a user's Pi Zero WH (**note: WH, the original Zero form factor with header — not confirmed whether Zero 2 W or original Zero silicon**) throwing USB timeouts and segfaults with certain dongles, resolved by moving to a Pi 3B; the AIS-catcher maintainer's own retest on a "raspberry pi zero rev 1.1 WH" with three different dongles found no issues, suggesting the failure was dongle/USB-port/power specific rather than a Pi Zero (2 W) class problem. This thread is about the **original Zero silicon** (rev 1.1), not the Zero 2 W, and given the hard incompatibility with the original Zero noted above, it's likely irrelevant to Zero 2 W buyers — but it's worth flagging since it surfaces as a top search result for "Pi Zero AIS-catcher" and could be misread.

**Bottom line for Zero 2 W: works well for AIS-only, single-dongle stations; avoid it only if you also want to run ADS-B or multiple SDRs on the same board.**

### Pi 3B+, Pi 4, Pi 5

- Pi 3B+ is repeatedly cited as the practical sweet spot: "A Pi 2B, Pi 3B or Pi 3B+ will work very well for AIS reception" (via search index, [garykessler.net/library/ais_pi.html](https://www.garykessler.net/library/ais_pi.html) and the adsberry doc above both converge on this).
- Pi 4 is consistently called "overkill but works without issues," and is recommended only if you want headroom for extra features (a second SDR, ADS-B alongside AIS, running Node-RED/OpenCPN/other services on the same box) (adsberry ais.md, via search index).
- Pi 5 is called out directly as overkill for AIS: "even a Pi Zero 2W barely uses resources for this task, while the Pi 5 is great for many other things" (via search index). No source found benchmarking AIS-catcher specifically on a Pi 5 — the "overkill" framing is a proportionality argument, not a measured bottleneck.
- **Recommended default for this guide: Raspberry Pi 3B+** for a single AIS-catcher + RTL-SDR station — cheap, widely available secondhand, ample CPU headroom, and it's the version the community most often points to as "just works." Pi Zero 2 W is the right call when minimizing power draw/cost matters more than having spare capacity (solar/off-grid, PoE-only budget, boat installs).

### Multi-SDR / thermal notes

Running two SDR dongles (e.g., AIS + ADS-B) on one Pi: "a Pi with two sdr dongles will use up to 6 watts of peak power," rising toward 10 W with more SDRs, at which point a powered USB hub is recommended; for Pi 4 multi-SDR builds, "aluminum enclosures with passive cooling fins" are recommended for thermal stability (adsberry ais.md, via search index).

### Other SBCs (Orange Pi, Radxa, etc.)

No AIS-catcher-specific benchmarks or compatibility reports were found for Orange Pi, Radxa, or other non-Raspberry-Pi SBCs in this research pass. AIS-catcher builds from source on any Linux/ARM64/ARMHF target with the standard toolchain and RTL-SDR/librtlsdr support, so there's no reason to expect it wouldn't run, but **no community reports were found confirming it in practice — flag as [UNVERIFIED]** rather than recommend a specific board.

### x86: old laptops, mini PCs, thin clients, Windows PCs

- AIS-catcher officially supports Windows, Linux, and macOS builds ([GitHub topics](https://github.com/jvde-github/AIS-catcher): `linux`, `macos`, `windows`), and Docker images are published for `amd64` in addition to ARM.
- No AIS-catcher-specific CPU-usage numbers were found for x86 hardware. General mini-PC/thin-client power figures: modern low-power mini PCs (Intel N100/N200 class) draw "under 15W at full load, and often 4-8W at idle"; older thin clients (e.g. AMD GX-415GA) draw roughly 5 W idle / 12 W peak; older nettops like the CompuLab fit-PC2 draw "no more than 8 watts" (general hardware sources, via search index — not AIS-specific).
- Given AIS-catcher's demonstrated ease on a Pi Zero 2 W, any x86 box from the last 10-15 years — including a retired thin client or laptop with the screen closed — has enormous headroom for a single AIS dongle. The main reasons to pick x86 over a Pi are: reusing hardware you already have, or wanting to co-host AIS-catcher with other Docker services (NAS, Home Assistant, etc.) on a box with more RAM/storage.

### NAS (Synology) via Docker

AIS-catcher publishes official container images (`ghcr.io/jvde-github/ais-catcher:latest` and `:edge`) and Docker install docs give explicit `docker run` invocations requiring `--device /dev/bus/usb` (or `--device-cgroup-rule='c 189:* rmw'` plus a `/dev/bus/usb` volume mount for hot-replug support) ([Docker docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker/)). **No Synology-specific walkthrough was found** — Synology's Container Manager does support standard `docker run`/compose syntax and USB passthrough on compatible models, so it should work, but this is **[UNVERIFIED]** for AIS-catcher specifically; no community writeup surfaced confirming a Synology deployment. One documented limitation: **SDRplay devices are not supported in the Docker images** regardless of host.

### OpenWrt routers

**No evidence found that AIS-catcher runs on OpenWrt.** Search turned up only a generic OpenWrt install guide and an unrelated result. OpenWrt's typical hardware (low RAM, musl libc, no glibc) and AIS-catcher's dependency footprint make this a nonstandard target; nothing in the AIS-catcher docs or discussions mentions OpenWrt. Treat as **not supported / not attempted by the community** rather than recommend it.

### Android phones

AIS-catcher has a dedicated Android app ([jvde-github/AIS-catcher-for-Android](https://github.com/jvde-github/AIS-catcher-for-Android), also on [Google Play](https://play.google.com/store/apps/details?id=com.jvdegithub.aiscatcher) and [IzzyOnDroid/F-Droid](https://android.izzysoft.de/repo/apk/com.jvdegithub.aiscatcher)):

- Works with RTL-SDR dongles or Airspy devices via **USB OTG cable** — "the app directly accesses the USB device and does not need additional drivers."
- Requires the phone/tablet to both power the USB SDR and run decoding: "your phone or tablet has to power the USB device and run the decoding algorithm and this will be a drain on your battery."
- Outputs UDP to plotting apps (OpenCPN port 10110, Boat Beacon port 10111 in the docs' examples), has a built-in map view (needs internet), a foreground service so it keeps running when the app is backgrounded, and an auto-start option.
- Google Play listing was deliberately not used for a period because "the Google Play Store introduced new requirements for developers to publish their personal details like address which we dont want to adhere to" — it is on Play now per the search results, but IzzyOnDroid is the maintainer's preferred distribution channel.
- Good fit for a **portable/on-the-water station** (dinghy, kayak, temporary dockside setup) rather than a permanent unattended installation, given the battery-drain caveat.

### ESP32-based AIS receivers

There is **no working, finished ESP32 AIS decoder**. The one project found, [cmbahadir/AIS-Receiver](https://github.com/cmbahadir/AIS-Receiver), is explicitly "still under construction" — it ships schematic/PCB/BOM files for an ESP32-based hardware design but the repo itself flags incomplete firmware/testing. Treat any ESP32 AIS option as **[UNVERIFIED / not production-ready]**.

**dAISy is not an ESP32 product** — worth correcting a common mix-up. The dAISy HAT (Wegmatt LLC, sold via [Tindie](https://www.tindie.com/products/astuder/daisy-hat-ais-receiver-for-raspberry-pi/), [The Pi Hut](https://thepihut.com/products/daisy-hat-ais-receiver-for-the-raspberry-pi), [Uputronics](https://store.uputronics.com/products/daisy-hat-ais-receiver-for-raspberry-pi)) is a dedicated 2-channel AIS receiver **HAT for Raspberry Pi 3/4/5**, built around an STM32 microcontroller (per the astuder/dAISy project it derives from), not ESP32. It draws "less than 200mW in receive mode (<40mA at 5V)" and outputs standard NMEA/AIVDM over serial at 38400 baud — a low-power, no-SDR-tuning alternative to RTL-SDR + AIS-catcher, at the cost of higher per-unit price than a $30-45 RTL-SDR dongle.

## 2. Storage and OS

### AIS-catcher's own install path

There is **no dedicated prebuilt AIS-catcher Raspberry Pi disk image** — the project ships an install *script*, not an `.img`. The documented path ([Raspberry Pi install docs](https://jvde-github.github.io/AIS-catcher-docs/installation/raspberrypi/)) is:

```
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/jvde-github/AIS-catcher/main/scripts/aiscatcher-install) -p -M"
```

run on top of a standard Raspberry Pi OS (or any Debian/Ubuntu) install. `-p` installs prebuilt packages (skip to compile from source, "optimizes the executable for your hardware but can take a significant amount of time (20 minutes on a Raspberry Pi 4)"); `-M` enables **Managed Mode**, which stands up a browser dashboard at `http://<host>:8118` for configuration and control, with an optional companion `aiscatcher-control` package adding "browser-based control of the AIS-catcher process (start and stop, live log) and of the host itself (one-click updates, reboot)." Omitting `-M` configures via command line/config file instead.

A third-party script, [abcd567a/install-aiscatcher](https://github.com/abcd567a/install-aiscatcher), builds AIS-catcher from source into a systemd service and exposes its own web UI on port 8383 — popular in the ADS-B feeder community (abcd567a maintains several ADS-B feeder installers) as a bolt-on for existing Pi-based ADS-B stations.

### Docker as the OS-agnostic path

Official images: `ghcr.io/jvde-github/ais-catcher:latest` (stable) and `:edge` (main branch). This is the natural route for DietPi, Armbian, NAS boxes, or any existing Docker host — sidesteps OS-specific packaging entirely. USB passthrough requires either `--device /dev/bus/usb` or the cgroup-rule + volume-mount pattern shown above for hot-replug tolerance ([Docker docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker/)).

### DietPi / Armbian

No AIS-catcher-specific DietPi or Armbian guide was found. Both are Debian-based distros for SBCs, so the standard install script (`aiscatcher-install`) or the Docker image should apply unmodified — this is an inference from the shared Debian base, not a confirmed community writeup, so flag as **[largely UNVERIFIED, but low-risk]**. DietPi's own docs list a general "distributed projects" software catalog ([dietpi.com/docs/software/distributed_projects](https://dietpi.com/docs/software/distributed_projects/)) that did not come up with an AIS-catcher entry in this pass.

### SD card wear, storage medium, read-only overlay

No AIS-catcher-specific discussion of SD card wear or read-only-overlay setups was found in this pass. General guidance that applies (not AIS-catcher-specific, standard Pi hobbyist practice):
- AIS-catcher's logging footprint is modest — it's a stream processor, not a database; the community feed upload is the main continuous I/O, and it's network traffic, not disk writes, unless you enable local file logging.
- A build log for a Pi-based AIS setup recommends "a 32 GB or 64 GB microSD card" for OS + data (Gary Kessler's AIS/Pi page, via search index) — generous headroom given AIS-catcher itself doesn't need much space; the sizing is more about OS/margin than AIS logging volume.
- Raspberry Pi OS's built-in **overlay filesystem** (via `raspi-config` → Performance Options → Overlay FS) makes the root filesystem read-only and is the standard community answer for unattended/remote Pi deployments to avoid SD corruption from power loss — this is generic Raspberry Pi OS functionality, not something specific to AIS-catcher, and wasn't found cross-referenced in any AIS-catcher doc directly.
- USB SSD boot is supported natively on Pi 4/5 and is the standard reliability upgrade over SD for any always-on Pi service; no AIS-catcher-specific benchmark of SD-vs-SSD reliability was found.

## 3. Power

### Baseline consumption figures

The most concrete, citable numbers come from a general Pi power-consumption benchmark page, not an AIS-specific source, but they match the "Pi + RTL-SDR ≈ a few watts" figures repeated across AIS build logs:

| Board | Idle | Under load (400% CPU) |
|---|---|---|
| Pi Zero 2 W (HDMI/LEDs disabled) | 100 mA / 0.6 W (0.7 W with Wi-Fi) | not listed |
| Pi 3 B+ | 350 mA / 1.9 W | 980 mA / 5.1 W |
| Pi 3 B+ (HDMI/LEDs disabled) | 350 mA / 1.7 W | not listed |
| Pi 4 B | 540 mA / 2.7 W | 1280 mA / 6.4 W |
| Pi 5 | no data found | no data found |

Source: [pidramble.com power-consumption benchmarks](https://www.pidramble.com/wiki/benchmarks/power-consumption). These figures exclude USB peripherals (i.e., the RTL-SDR dongle itself) unless noted.

Add the dongle: an RTL-SDR draws on the order of tens to ~150-200 mA at 5V depending on model/tuner (R820T2 datasheet-level "ultra-low power" threshold is cited as <178 mA; general RTL-SDR power discussion via search index, not independently re-verified against a datasheet in this pass). In practice, **Pi + single RTL-SDR dongle lands around 2-4 W continuous for a Zero 2 W/3B+, up to 6-10 W for a Pi 4 running two SDRs** (adsberry ais.md figure: "a Pi with two sdr dongles will use up to 6 watts of peak power," rising toward 10 W with more SDRs and requiring a powered hub).

### USB power-supply quality with RTL-SDR

The Pi's own USB ports have limited current budget per port on some models, and RTL-SDR reliability issues (dropouts, resets, "timeout" errors) are a recurring theme in AIS-catcher discussions — one Pi Zero WH user's segfault/timeout saga (discussion #316, above) was initially blamed on power delivery, though the maintainer's retest suggested it wasn't purely a power problem in that case. The generic, widely-repeated community advice (not AIS-catcher-specific) is to use a quality official Pi power supply and, for multi-SDR or marginal setups, a powered USB hub between the Pi and the dongle(s).

### PoE for antenna-adjacent placement

Official **Raspberry Pi PoE+ HAT**: outputs 5 V at up to 4 A (20 W rated, engineered to a 25 W ceiling for margin) when paired with an 802.3at ("PoE+")-capable switch or injector; recommendation is to stay at or under 20 W continuous draw for longest life ([Raspberry Pi PoE+ HAT product page](https://www.raspberrypi.com/products/poe-plus-hat/); wattage figures via search index of the same page plus [CNX Software coverage](https://www.cnx-software.com/2021/05/24/raspberry-pi-poe-plus-hat-25-5-watts/)). At the ~2-10 W draw of a Pi + RTL-SDR AIS station, PoE has huge headroom — this is a good option for putting the Pi+SDR physically near the antenna/mast base and running a single Ethernet cable for both power and data back to the shack, instead of running separate power and USB-extended-SDR cabling. **No AIS-catcher-specific PoE deployment writeup was found** — this is a general Pi capability applied to the AIS use case, not a confirmed community pattern; flag as **[UNVERIFIED for AIS-catcher specifically, but standard/low-risk Pi hardware]**.

### 12 V DC on boats

Real-world consensus from boater forums: run the Pi off a dedicated 12V→5V buck (step-down) converter rather than an inverter, because "DC-DC converters are more efficient than inverters, particularly since Raspberry Pi is DC-powered," and users report buck converters "very reliable and stable even when input voltage varies by several volts" (Raspberry Pi Forums threads, e.g. ["Pi 3 on a sailboat"](https://forums.raspberrypi.com/viewtopic.php?t=184964) and ["Help/Advice Wanted - Raspberry Pi 12V Marine Use Case"](https://forums.raspberrypi.com/viewtopic.php?t=324864), via search index). Widely available marine/automotive-rated waterproof 12/24V-to-5V USB-C buck converters (8-32V wide input, 3A/15W output) are sold for exactly this use case. One audio-focused thread specifically flags the need for a supply that "does not introduce noise/ground loop hum" — relevant if the Pi shares power/ground with sensitive receive electronics.

### UPS / battery and solar sizing

The clearest end-to-end example found is a documented solar build (worldwideais.org, "under €100"):
- **Pi Zero 2 W** + RTL-SDR Blog V4, chosen specifically for low power draw.
- **10 W, 12 V solar panel**, sized to "output at least 14V in full sun to properly charge a 12V lead-acid battery."
- **12 V 7 Ah sealed lead-acid battery** as the buffer.
- A basic PWM charge controller (~€5).
- For remote deployments adding a 4G modem (0.5-0.8 A draw at 5V), the guide bumps the panel to **15 W** for northern-latitude (lower sun) sites.
- IP65 enclosure with "adequate airflow," and attention to corrosion-proofing terminal connections in coastal air.

This gives a rough sizing rule of thumb for this guide: **a Pi Zero 2 W + single RTL-SDR AIS station draws roughly 2-4 W continuous (0.4-0.8 Ah/day at 12V); a 10 W panel + 7 Ah SLA battery is reported as sufficient in a real deployment, with 15 W recommended once a cellular modem is added.** No UPS-specific (mains-backup battery) product recommendations were found for AIS-catcher; any small USB-C UPS HAT or standard 12V SLA jump-battery + buck converter would serve the same role for short outages.

## 4. Network

### Wi-Fi vs Ethernet

No AIS-catcher-specific comparison was found. Generic guidance applies: Ethernet is preferred for reliability at a fixed installation (no association drops, no Wi-Fi interference near the RF-noisy antenna feedline), Wi-Fi is the practical option when running Pi+SDR at a remote mast/roof location without a cable run — PoE (above) is the way to get both power and reliable Ethernet to that same remote location in one cable.

### Cellular data volume

No AIS-catcher-specific bandwidth benchmark was found, but the underlying protocol numbers give a solid estimate:
- AIS Dispatcher (AISHub's forwarding tool, same underlying message format as AIS-catcher's UDP output) documents a **downsampling feature specifically because raw feeds can be bandwidth-heavy**: "Downsampling reduces outgoing traffic several times!"; a sample raw feed clip was "~85K messages (3 minute live AISHub feed)" — roughly 28,000 messages/minute from an aggregated multi-station feed, illustrating that a *busy aggregation point* is very different in scale from a *single station* ([AISHub AIS Dispatcher](https://www.aishub.net/ais-dispatcher)).
- A separate estimate (via search index, MarineTraffic help content) models a single-station scenario: "30 vessels within range transmitting their position 10 times per minute on average" at roughly 128 bytes/message average → **well under 1 MB/hour (2-3 KB/second)** for a moderately busy single receiver. Scaling that to a month (steady 30-vessel traffic, no growth): under 1 MB/hour × 24 × 30 ≈ **well under 1 GB/month** for a single-station raw NMEA feed — a rough back-of-envelope extrapolation from the hourly figure, not a directly cited monthly number, so treat the monthly total as **[estimated, not a primary-source figure]**.
- A cellular-linked live-aboard AIS setup (seabits.com) reports the practical constraint is a monthly data cap (the author cites a 40 GB LTE plan) rather than the AIS stream itself: sending the *same* raw stream redundantly to multiple servers is what threatens the cap, and the fix used is downsampling slow-moving-vessel updates (`-D 60`) plus filtering to AIS-only messages, then fanning out from one central relay rather than multiple parallel cellular uploads ([Sending AIS data over a slow cellular link, seabits.com](https://seabits.com/sending-ais-data-slow-cellular-link/)).
- **Bottom line for the guide**: a single hobbyist AIS station's raw NMEA/UDP upstream is low — almost certainly under a few GB/month even on a busy coastline — and well within any modern prepaid/IoT cellular data plan; the practical risk is redundant multi-destination feeding (feeding MarineTraffic + VesselFinder + ShipXplorer + aiscatcher.org simultaneously from a cellular-linked station), which multiplies the same small stream 3-5x. AIS-catcher's built-in web dashboard also supports gzip for its HTTP/JSON output, which would reduce dashboard-viewing bandwidth (confirmed as a general AIS-catcher web server feature; specific gzip compression ratio not benchmarked in this research pass — **[UNVERIFIED specific number]**).

### CGNAT and remote access

- **Outbound UDP/TCP feeding to aggregators (aiscatcher.org, MarineTraffic, etc.) works fine behind CGNAT/Starlink/cellular** — it's an outbound connection the station initiates, no inbound port-forwarding required. This is standard networking behavior, not AIS-specific, but directly resolves the practical CGNAT worry for anyone feeding data out.
- **Inbound access to the station's own web dashboard is the part CGNAT breaks**, since there's no public IP to forward a port to. Community-standard fixes, none of which are AIS-catcher-specific but all directly applicable:
  - **Tailscale** — a WireGuard-based mesh VPN explicitly designed to work "on cellular... through hotel WiFi... through CGNAT — no port forwarding, no exposed SSH port on the internet" ([Tailscale marketing/docs](https://tailscale.com/docs/reference/troubleshooting/network-configuration/cgnat-conflicts), via search index). Install on the Pi and on your viewing device; access the dashboard over the Tailscale IP.
  - **Raspberry Pi Connect** (official, built into Raspberry Pi OS Desktop/Full/Lite) — provides browser-based remote shell and (on non-Lite, Wayland-based installs) screen sharing; explicitly relays through Raspberry Pi's own servers when a direct connection can't be established, meaning it works "regardless of network configuration, including behind CGNAT... users don't need to configure port forwarding" ([Raspberry Pi Connect docs](https://www.raspberrypi.com/documentation/services/connect.html)).
  - Cloudflare Tunnel and ZeroTier are standard alternatives in this space (not independently verified against AIS-catcher in this pass, but architecturally equivalent to Tailscale for this purpose).
- **SSH** remains available for remote shell access without Tailscale/Connect if the station is on a network you *can* port-forward or VPN into (e.g., home broadband, not cellular CGNAT).

## 5. Site selection and expectations

### Radio horizon / antenna height vs range

The standard maritime VHF line-of-sight formula, using the 4/3-effective-Earth-radius refraction model, gives combined range as a function of both antenna heights:

**d (km) ≈ 4.12 × (√h1 + √h2)**, with h1 and h2 in meters (your antenna height and the target vessel's antenna height above sea level).

This is mathematically the same "4/3 Earth radius" horizon model documented (in a nautical-mile/feet form) at [arundaleais.github.io's VHF Radio Horizon page](https://arundaleais.github.io/docs/ais/horizon.html), which gives `dr = √((4587 + h/6076)² − 4587²)` (nm, h in feet, 4587 nm = 4/3 × Earth's radius) and notes "the radio horizon between the transmitter and receiver is the addition of the horizon of both masts" — and that "reception beyond this range is caused by propagation conditions and will be flukey" (i.e., ducting/anomalous propagation can and does exceed these numbers, but shouldn't be *planned* around). The 4.12-km-coefficient form is the standard equivalent commonly used in maritime radio references; treat the exact coefficient as **[widely used, not independently re-derived from a single primary citation beyond the nm/ft form above]**.

Worked table — combined theoretical LOS range for a station at height h1 against typical vessel antenna heights h2 (ships commonly carry AIS antennas roughly 10-30 m above the waterline; small craft closer to 3-5 m):

| Your antenna height (h1) | vs. h2 = 10 m (small ship/large yacht) | vs. h2 = 20 m (medium ship) | vs. h2 = 30 m (large ship) |
|---|---|---|---|
| 10 m (typical house/mast) | 26 km / 14 nm | 31 km / 17 nm | 36 km / 19 nm |
| 20 m (taller mast/small tower) | 31 km / 17 nm | 37 km / 20 nm | 41 km / 22 nm |
| 50 m (hill, tall tower) | 42 km / 23 nm | 48 km / 26 nm | 52 km / 28 nm |
| 100 m (hilltop/ridge site) | 54 km / 29 nm | 60 km / 32 nm | 64 km / 34 nm |

(Calculated by this research pass from the formula above; treat as theoretical maximum line-of-sight, not a guarantee — actual reported ranges vary with terrain, RF noise floor, antenna gain/quality, and propagation conditions.)

### Real-world reports

- A Scotland (Isle of Skye) station build reports picking up traffic "up the Inner Sound, over to Kyleakin, Raasay and possibly as far as Kishorn," and tracking "36-37" concurrent vessels on a simple homemade flowerpot/Yagi antenna setup — a qualitative confirmation that a modest coastal antenna at typical house-roof height comfortably sees inshore traffic in the 10-20 nm class range implied by the table above ([AIS Receiver Mk3](https://www.sarcnet.org/ais-receiver.html), via search index).
- **aiscatcher.org itself blocks automated fetches (403) for this research pass**, so leaderboard-level figures (top stations' max range, messages/day) could not be pulled directly. From what did come through search indexing, the site's per-station detail pages track messages/second/minute/hour/day and "Max Distance" for last-hour and last-day windows, plus per-message-type counts and vessel-per-minute figures — confirming the site's stats model, but **specific "what a good station achieves" numbers are [UNVERIFIED — not retrievable in this pass]**. Recommend the guide author browse `aiscatcher.org` interactively (a browser, not an automated fetch) to pull current leaderboard numbers for a concrete "here's what a great station looks like" callout.

### Urban RF noise, inland/river stations, mobile/boat stations

- No AIS-catcher-specific data was found quantifying urban RF noise floor impact, though it's a well-known general SDR/VHF phenomenon (switching power supplies, LED drivers, etc. raise the noise floor and shrink effective range even where line-of-sight math suggests better).
- Inland river AIS (Rhine, Mississippi, Great Lakes) is a real and active use case for AIS-catcher — vessels on regulated inland waterways in Europe (Rhine, Danube) carry "Inland AIS," and Great Lakes/Mississippi commercial traffic also carries standard Class A/B AIS — but **no specific inland-station build report or range data was found in this pass**; flag as a gap if the guide wants concrete inland examples.
- **Mobile/boat stations**: this project's own codebase already handles this — `server/aishub.go` in this repo keys shared/public UDP stations by AIVDO MMSI (per the git status context), consistent with treating a boat under way as its own mobile "station" rather than a fixed site. No external primary source was needed here since this is the project's own design; worth a callout in the guide that a boat-mounted station is a legitimate, different category (mobile coverage footprint moves with the vessel) from a fixed shore station.

## 6. Weatherproof outdoor enclosures

No AIS-catcher-specific enclosure product or build was found with detailed specs, but the pattern from general Pi/RTL-SDR outdoor deployments (via search index) is consistent and directly transferable:

- **IP65-rated enclosures** are the standard target rating for mast-base/outdoor placement — e.g. the [Sixfab IP65 Outdoor Project Enclosure for Raspberry Pi](https://sixfab.com/product/raspberry-pi-ip65-outdoor-iot-project-enclosure/), built from RF-friendly ABS plastic so an internal antenna can work through the case, or external antenna runs through waterproof cable glands.
- Best practices repeated across sources: **waterproof cable glands** for the antenna coax and power/Ethernet runs, and a **small vent or desiccant pack** to manage condensation from daily thermal cycling in a sealed box.
- The solar-build source above explicitly calls for mounting "all components in a waterproof enclosure with adequate airflow" and securing/protecting terminal connections against coastal corrosion.
- **Heat**: no AIS-catcher-specific thermal data was found for an outdoor-boxed Pi, but given the very low idle/load power figures above (0.6-6 W depending on model), heat buildup inside a sealed IP65 box in direct sun is a bigger risk from solar gain on the box itself than from the Pi's own dissipation — passive ventilation/shading of the box, not just the electronics, is the practical mitigation implied by the "adequate airflow" guidance above.
- **PoE at the mast base** (Section 3) pairs naturally with this: one Ethernet cable in through a single gland handles both power and data, minimizing penetrations into the enclosure.

## 7. Cost tiers

Prices below are current list/street prices found via product pages during this research pass (2026-08-22); confirm at time of purchase as SDR dongle availability in particular has been volatile (see V4 note).

| Component | Minimal (~$60-100) | Recommended (~$150-250) | Premium |
|---|---|---|---|
| Compute | Raspberry Pi Zero 2 W (~$15, [raspberrypi.com](https://www.raspberrypi.com/products/raspberry-pi-zero-2-w/), price not confirmed via fetch in this pass — commonly listed around $15) | Raspberry Pi 3B+ or 4 (~$35-55) | Raspberry Pi 4/5 + PoE+ HAT (~$55-100 + ~$20-25 for [PoE+ HAT](https://www.raspberrypi.com/products/poe-plus-hat/), price not confirmed via fetch) |
| SDR | RTL-SDR Blog V3, ~$35-40 ([rtl-sdr.com](https://www.rtl-sdr.com/buy-rtl-sdr-dvb-t-dongles/)) | RTL-SDR Blog V3/V4, ~$35-45 (V4 availability flagged as inconsistent in this pass — **[UNVERIFIED: one source described V4 as "EOL," which is unconfirmed and worth checking at purchase time]**) | dAISy HAT (~$50-70 typical Tindie/Wegmatt pricing range, not independently re-confirmed this pass) for SDR-tuning-free dual-channel reception |
| Antenna | Basic VHF whip/flowerpot DIY (~$10-20 in parts) | Dedicated marine AIS/VHF antenna (~$30-60) | Higher-gain marine antenna + coax upgrade + lightning arrestor (~$80-150) |
| Power | Standard USB supply (~$8-10) | Official Pi power supply + basic UPS/battery backup (~$20-40) | PoE+ injector/switch port, or 12V buck converter + SLA battery + solar (~$60-120 depending on solar sizing) |
| Storage | 16-32 GB microSD (~$8-10) | 32-64 GB microSD or USB SSD boot (~$15-30) | USB SSD (~$30-50) for reliability under continuous logging |
| Enclosure | None / indoor placement ($0) | Basic indoor case (~$10) | IP65 outdoor enclosure + cable glands (~$30-60) |
| Networking | Existing home Wi-Fi ($0) | Ethernet run or existing Wi-Fi ($0-20 for cable) | PoE (bundled with PoE+ HAT above) or cellular IoT data plan (~$10-30/month recurring, not a one-time cost) |
| **Approximate total** | **~$60-90** | **~$150-230** | **~$300-500+** depending on solar/cellular/antenna choices |

Notes on the tiers:
- The minimal tier is essentially the worldwideais.org solar build's indoor-equivalent: Pi Zero 2 W + RTL-SDR + basic antenna, wired to existing home network and power.
- The recommended tier reflects the community's repeated "sweet spot" pick (Pi 3B+ or Pi 4, RTL-SDR Blog dongle, real marine antenna) with a proper power supply and better storage.
- The premium tier is where PoE-at-the-mast, solar/off-grid, or dAISy's SDR-free reliability come in — none of these are necessary for good reception, they trade money for either physical flexibility (mast placement, off-grid siting) or reduced fiddling (dAISy's fixed-tuning hardware vs. RTL-SDR gain/PPM tuning).
- Recurring costs (cellular data if used) are called out separately since they're not comparable to one-time hardware spend.

## Flagged gaps for follow-up

- **aiscatcher.org leaderboard/top-station numbers** — site blocks automated fetches; needs a manual browser pull for concrete "what a great station looks like" figures.
- **Orange Pi / Radxa / other SBC compatibility** — no community reports found; likely works (standard Linux/ARM build) but unconfirmed.
- **Synology-specific AIS-catcher Docker walkthrough** — not found; likely works via standard Docker Compose + USB passthrough, unconfirmed.
- **DietPi/Armbian AIS-catcher confirmation** — inferred from shared Debian base, no direct community writeup found.
- **RTL-SDR Blog V4 "EOL" claim** — surfaced from one page summary, contradicts this model's status as the current flagship RTL-SDR Blog dongle in general knowledge; verify directly with rtl-sdr.com before publishing a price/availability claim.
- **AIS-catcher web dashboard gzip compression ratio** — feature exists, no measured bandwidth-savings figure found.
- **Inland river station (Rhine/Mississippi/Great Lakes) build reports** — none found with concrete range/setup data.
- **Raspberry Pi 5 AIS-catcher benchmark** — no direct measurement found, only proportionality/overkill commentary.
