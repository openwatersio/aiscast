# The software layer of a volunteer AIS station

Research notes for a public guide. Everything here is about the step between "a receiver is producing something" and "an aggregator has your `!AIVDM` sentences". Researched 2026-08-22; version numbers and dates are as of that date.

The reference aggregator throughout is **aiscast** (`ais.openwaters.io`), which accepts three things:

| Transport | Endpoint | Auth | Station identity |
|---|---|---|---|
| UDP NMEA | `ais.openwaters.io:10110` | none | `udp:<keyed hash of sender IP>` |
| HTTP POST | `https://ais.openwaters.io/v1/receive` | `USERPWD x:<token>` (HTTP Basic) | `http:<station id>` |
| Signal K plugin | `signalk-aiscast` | self-minted Ed25519 token | per key |

Source: [aiscast README, "Contributing data"](https://github.com/openwatersio/aiscast#contributing-data). The HTTP endpoint parses AIS-catcher's `jsonaiscatcher` envelope *or* plain newline-separated NMEA, and accepts `Content-Encoding: gzip` (verified in `server/ingest.go` in this repo).

Companion files in this directory carry the exhaustive per-tool detail: **[`forwarders.md`](forwarders.md)** (multiplexers, Signal K, AIS Dispatcher, docker-shipfeeder), **[`ops.md`](ops.md)** (systemd, Docker, SD card wear, monitoring, aggregator terms), **[`decoders-legacy.md`](decoders-legacy.md)** (everything that is not AIS-catcher).

## The short version

The software layer collapsed onto one project. **AIS-catcher** is the decoder, the multiplexer, the dashboard, and the feeder, on every platform that matters, whether your receiver is an SDR dongle or a serial AIS box. A guide can be organised around it and treat everything else as either a special case or history.

| If you have… | Run | Feeds aiscast with |
|---|---|---|
| An RTL-SDR / Airspy / HackRF dongle | AIS-catcher | `-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15` |
| A dAISy HAT, dAISy USB, dAISy-catcher, or any serial NMEA AIS receiver | AIS-catcher with `-e <baud> <port>` | same |
| A boat already running Signal K | the `signalk-aiscast` plugin, or `ais-forwarder` to UDP | plugin token / UDP |
| A desire to feed six aggregators from one Pi | docker-shipfeeder (wraps AIS-catcher) | a custom UDP target |
| A chartplotter or NMEA network with nothing else on it | socat under systemd, or kplex | UDP `:10110` |
| Windows and a sound-card receiver from 2009 | see §1.11, and consider replacing it | UDP `:10110` |

The two decisions that actually matter for a volunteer: **HTTP with a token beats UDP** (it works behind any NAT, it authenticates you, and it gzips), and **run it under systemd or Docker with `Restart=always`**, because an unattended station's failure mode is silence nobody notices.

---

## 1. Decoders: RF or IQ in, `!AIVDM` out

### 1.1 The state of play

One package dominates and everything else is legacy, niche, or dead. If a guide recommends exactly one thing, it should recommend **AIS-catcher**.

| Software | Maintainer | Latest | Input | NMEA out | Verdict |
|---|---|---|---|---|---|
| **AIS-catcher** | jvde-github | **v0.70**, 2026-06-19 ([release](https://github.com/jvde-github/AIS-catcher/releases/tag/v0.70)); `main` pushed 2026-08-22 | RTL-SDR, Airspy R2/Mini/HF+, HackRF, HydraSDR, SDRplay, SoapySDR, SpyServer, RTL-TCP, ZMQ, **serial NMEA**, **UDP NMEA**, **TCP NMEA**, NMEA 2000 socketCAN, WAV/raw file | UDP, TCP client, TCP server, HTTP, MQTT, N2K, console, Postgres/SQLite/CSV | **Use this.** 774 stars, GPL-3.0, active daily |
| rtl_ais | dgiardini | see §1.11 | RTL-SDR only | UDP (`-h`/`-P`), TCP (`-T`) | legacy; not packaged for bookworm or noble |
| gnuais | rubund | see §1.11 | sound card / discriminator audio | **none over the network** | **dead, and cannot feed an aggregator** |
| AISdeco2 | Sergey Serov (closed) — *not* AISHub/VesselFinder | see §1.11 | RTL-SDR | UDP + own TCP server | **dead; xdeco.org returns HTTP 500** |
| SDRangel | f4exb | see §1.11 | most SDRs | **UDP, format NMEA** | alive; GUI-first |
| AISRec / ShipPlotter / AIS Decoder | various, Windows | see §1.11 | sound card / SDR | varies | Windows-only niche |
| gr-ais | bistromath | see §1.11 | GNU Radio 3.8 | flowgraph | dead; won't build on GR 3.10+ |

(§1.11 carries the dated, sourced detail for the non-AIS-catcher entries; the exhaustive version is in [`decoders-legacy.md`](decoders-legacy.md).)

### 1.2 What AIS-catcher is

A single C++ binary, GPL-3.0, by `jvde.github at gmail.com`, first committed 2021-04-26. It is a *dual-channel* receiver: it decodes AIS channel A (161.975 MHz) and channel B (162.025 MHz) simultaneously from one IQ stream, which is the main reason it out-performs older single-channel or FM-demodulator-based decoders. Project home: [aiscatcher.org](https://www.aiscatcher.org). Docs: [jvde-github.github.io/AIS-catcher-docs](https://jvde-github.github.io/AIS-catcher-docs/). Source: [github.com/jvde-github/AIS-catcher](https://github.com/jvde-github/AIS-catcher).

The disclaimer matters for a public guide and should be carried through: *"created for research and educational purposes … DO NOT rely upon this software in any way including for navigation and/or safety of life or property purposes"*, plus a note that whether you may receive and handle AIS varies by administration ([README](https://github.com/jvde-github/AIS-catcher/blob/main/README.md)).

Scale of the community: aiscatcher.org's [station list](https://www.aiscatcher.org/stations) was issuing station IDs around **3728** on 2026-08-22, so roughly 3,700 stations have registered with the community hub alone (many more run without registering).

### 1.3 Installing it

The README's own [installation index](https://jvde-github.github.io/AIS-catcher-docs/installation/overview) lists six paths. Note that the historically-referenced `install_debian_ubuntu.sh` no longer exists — the current script is `scripts/aiscatcher-install`.

| Platform | Command / artefact | Notes |
|---|---|---|
| Debian / Ubuntu / Raspberry Pi OS / **Fedora** | `sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/jvde-github/AIS-catcher/main/scripts/aiscatcher-install) -p -M"` | `-p` = install pre-built package rather than compiling; `-M` = managed mode. Installs deps, sets up a systemd service and starts it. ([docs](https://jvde-github.github.io/AIS-catcher-docs/installation/ubuntu-debian)) |
| Same, manual mode | drop the `-M` | Service infra still installed; you configure via `/etc/AIS-catcher/config.cmd` / `config.json` |
| Same, build from source | drop the `-p` | ~20 min on a Pi 4. Required for PostgreSQL support. |
| `.deb` direct | [release page](https://github.com/jvde-github/AIS-catcher/releases/tag/v0.70) | v0.70 ships debs for bookworm/bullseye/trixie and Ubuntu focal/jammy/noble/plucky/questing/resolute, each in `amd64`/`arm64`/`armhf` |
| Docker | `ghcr.io/jvde-github/ais-catcher` | tags `latest` (release) and `edge` (main). ([docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker)) |
| Windows | `AIS-catcher.x64.zip` / `.x86.zip` from the release page | unzip, run Zadig once per dongle, double-click `start-GUI.bat` |
| macOS | Homebrew deps + build from source (`brew install git make gcc cmake pkg-config sqlite3 librtlsdr`, then cmake) | **no Homebrew formula for AIS-catcher itself** — you compile it |
| Android | [jvde-github/AIS-catcher-for-Android](https://github.com/jvde-github/AIS-catcher-for-Android) | separate repo, GPL-3.0, 152 stars, pushed 2026-08-19. RTL-SDR / Airspy R2 / Mini / HF+ / RTL-TCP / SpyServer; **output via UDP** plus a built-in map |

Two important caveats from the [Linux install page](https://jvde-github.github.io/AIS-catcher-docs/installation/ubuntu-debian):

- The pre-built packages **statically link the latest Osmocom librtlsdr specifically to guarantee RTL-SDR **V4** support** — which is the single most common failure mode people hit with older decoders.
- The pre-built packages are **not compatible with the original Raspberry Pi 1 or the original Zero** (insufficient hardware floating point).

**No ESP32 port exists.** I searched the GitHub API for `ais-catcher esp32` and `AIS receiver esp32`: there is no `AIS-catcher-ESP32` and no ESP32 target in the AIS-catcher repo. Unrelated hobby projects exist ([cmbahadir/AIS-Receiver](https://github.com/cmbahadir/AIS-Receiver), last pushed 2021-01-17; [seregamorph/esp32-ais](https://github.com/seregamorph/esp32-ais), 2022-04-11) but they are not AIS-catcher and not maintained. **Treat "AIS-catcher on ESP32" as non-existent.**

Also non-existent: **`sdr-enthusiasts/docker-aiscatcher`**. The GitHub API returns 404. The real sdr-enthusiasts container is [`docker-shipfeeder`](https://github.com/sdr-enthusiasts/docker-shipfeeder) (see §3).

### 1.4 Two operating modes since v0.70/Edge

This is new enough that older tutorials are wrong about it, and a guide needs to be explicit.

- **Managed mode** (`-E <config file> <address:port>`, default `127.0.0.1:8118`): a built-in browser control panel with a first-run setup wizard. Configure the device, gain, and every output from the browser; no config file editing. The wizard's step 3 offers *"pre-defined feeds like aiscatcher.org and AISHub"* ([setup wizard docs](https://jvde-github.github.io/AIS-catcher-docs/managed/setup-wizard)). In managed mode the web viewer is started automatically **on the port above the control panel — 8119 by default** ([what's new](https://jvde-github.github.io/AIS-catcher-docs/what-is-new/)).
- **Manual mode**: the classic CLI / `config.cmd` / `config.json` route, which is what every existing tutorial and every aggregator's feeding guide assumes.

Switching an existing manual install to managed mode **does not carry your settings over** — `/etc/AIS-catcher/config.json` and `config.cmd` are left on disk untouched but ignored, and you re-enter everything in the wizard ([install docs](https://jvde-github.github.io/AIS-catcher-docs/installation/raspberrypi)).

Ports, all defaults, worth a table in the guide because there are now four of them:

| Port | What | Exposed by default? |
|---|---|---|
| 8100 | web viewer (`-N 8100`) | only if you pass `-N` |
| 8110 | AIS-catcher-**control** (separate package: start/stop service, host updates, reboot) | only if you install it; default password `admin`, forced change on first login |
| 8118 | managed-mode control panel (`-E`) | bound to `127.0.0.1` unless you say otherwise; password forced when bound elsewhere |
| 8119 | web viewer auto-started by managed mode | follows the control panel |

Sources: [web viewer](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/web-viewer), [AIS-catcher-control](https://jvde-github.github.io/AIS-catcher-docs/usage/gui), [remote access](https://jvde-github.github.io/AIS-catcher-docs/managed/remote-access).

### 1.5 Command-line reference (verbatim from `AIS-catcher -h`, v0.70+)

Reproduced from the [CLI page](https://jvde-github.github.io/AIS-catcher-docs/usage/cli), which embeds the program's own help text. This is the authoritative list; several flags commonly cited in older guides have moved.

**Output flags**

| Flag | Meaning |
|---|---|
| `-u <host> <port>` | UDP destination. **Repeatable** — this is how you feed several aggregators. |
| `-P <host> <port>` | TCP **client** — connect out to a listener |
| `-S <port>` | TCP **server** — up to 64 clients read NMEA from you (rewritten in v0.70 on `poll()`, up to 128 connections) |
| `-H [url]` | HTTP POST output |
| `-N [port] [settings]` | web viewer / HTTP server |
| `-X [key]` | community feed to aiscatcher.org; `-X off` disables |
| `-Q` | MQTT publish |
| `-D <conn>` | database: `postgres`, `sqlite:file.db`, `csv:dir` |
| `-I <iface>` | push as NMEA 2000 onto socketCAN (Linux) |
| `-f <filename>` | write NMEA lines to a file |
| `-o <0-5>` | screen output mode (see below) |
| `-n` | = `-o 1`, plain NMEA |
| `-q` | = `-o 0`, silent |

**Input flags** — these are the ones a dAISy/serial owner needs, and the ones most often mis-cited:

| Flag | Meaning |
|---|---|
| `-e [baudrate] <port>` | **serial NMEA in** (e.g. `-e 38400 /dev/serial0`) |
| `-x [server] [port]` | **UDP NMEA in** (AIS-catcher runs a UDP server) |
| `-t [[protocol]] [host [port]]` | TCP in. Protocol may be `txt` (NMEA0183), `gpsd`, `ws`, `mqtt`, `wsmqtt`, `rtltcp` (raw IQ), `basestation`, `beast`, `raw1090`. e.g. `-t txt://192.168.1.120:5011` |
| `-i <iface>` | NMEA 2000 in from socketCAN (Linux) |
| `-y [host [port]]` | SpyServer |
| `-z [format] [endpoint]` | ZMQ |
| `-r [format] <file>` / `-w <file>` | raw IQ file / WAV file |
| `-d:x` / `-d <serial>` | select device by index or serial |
| `-l` / `-L` | list devices / list compiled-in hardware support |

**Tuning flags**

| Flag | Meaning |
|---|---|
| `-gr TUNER [auto/0-50] RTLAGC [on/off] BIASTEE [on/off]` | RTL-SDR gain settings |
| `-a <Hz>` | tuner bandwidth (default off). `-a 192K` is the community's usual first thing to try |
| `-p <ppm>` | frequency correction |
| `-s <Hz>` | sample rate (default device-dependent; 1536K for RTL-SDR) |
| `-F` | fast/approximate downsampling — RTL-SDR at 1536K only. Cuts decode time from ~17.3 s to ~7.7 s on a 700 MHz Pi and brings a Zero W to ~40% CPU |
| `-go AFC_WIDE / FP_DS / PS_EMA / SOXR / SRC / DROOP [on/off]` | decoder model tuning |
| `-m <n>` | select decoding model (default 2) |
| `-c [AB/CD]` | select AIS channels and NMEA channel designations |
| `-v [seconds]` | verbose, with optional stats interval |
| `-M T/D/M` | metadata to generate (see §5) |
| `-O <MMSI>` | own MMSI of the receiver |
| `-Z <lat> <lon>` | receiver location |
| `-C <file>` | read JSON configuration |
| `-E <file> <addr:port>` | managed mode |
| `-G LEVEL <lvl> / SYSTEM on` | logging control (`DEBUG`/`INFO`/`WARNING`/`ERROR`/`CRITICAL`, or syslog) |

The recommended RTL-SDR starting point from the docs, verbatim:

```bash
AIS-catcher -gr RTLAGC on TUNER auto -a 192K
```

with the caveat that `-a 192K` helps some stations and not others, and you should change one parameter at a time ([CLI docs](https://jvde-github.github.io/AIS-catcher-docs/usage/cli)).

Casing rule, which trips people up: **CLI setting names and boolean values are case-insensitive; JSON config keys are case-sensitive and must be lowercase.**

### 1.6 Feeding aiscast specifically

**HTTP (preferred — works behind any NAT, authenticated, gzip'd):**

```bash
AIS-catcher -gr RTLAGC on TUNER auto \
  -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15 \
  -N 8100 \
  -q
```

Every part of this is a documented `-H` setting ([HTTP output docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/HTTP)):

| Setting | Type | Default | Note |
|---|---|---|---|
| `url` | string | — | the endpoint |
| `userpwd` | string | — | `user:password`, sent as HTTP Basic. aiscast wants `x:<token>` |
| `gzip` | bool | `false` | needs zlib; big bandwidth saving |
| `interval` | int | `60` | seconds between posts, 1–86400 |
| `protocol` | string | `AISCATCHER` | one of `AISCATCHER`, `MINIMAL`, `LIST`, `AIRFRAMES`, `APRS`, `NMEA` |
| `stationid` (aliases `id`, `callsign`) | string | — | station identifier in the envelope |
| `timeout` | int | `10` | 1–30 s |
| `response` | bool | `true` | print the server's reply; set `off` to only show errors |
| `ssl_verify` | bool | `true` | leave it on |
| `lat` / `lon` | float | `0.0` | station position, sent in the feed metadata |
| `model`, `model_setting`, `product`, `vendor`, `serial`, `device_setting` | string | — | receiver/device description in the metadata |

aiscast's endpoint parses the default `AISCATCHER` protocol, so **do not set `PROTOCOL`**. The envelope it receives looks like this (from the AIS-catcher docs, and matching `jsonaiscatcher` in `server/ingest.go`):

```json
{
  "protocol": "jsonaiscatcher",
  "encodetime": "20221102171325",
  "stationid": "MyStation",
  "receiver": { "description": "AIS-catcher v0.39", "version": 39, "engine": "Base (non-coherent)", "setting": "droop ON fp_ds OFF " },
  "device": { "product": "FILE-RAW", "vendor": "", "serial": "", "setting": "rate 1536K file posterholt.raw format CU8" },
  "msgs": [
    {"class":"AIS","device":"AIS-catcher","rxtime":"20221102171324","scaled":true,"channel":"A","nmea":["!AIVDM,1,1,,A,13`fL1PP140KCELMBO7SS?wH0@Jv,0*50"],"ppm":0.0,"type":1,"mmsi":244030470,"lon":5.964237,"lat":51.185970, "...":"..."}
  ]
}
```

Operational notes on the HTTP path:

- `http` is an **array** in the config file, so you can POST to several services with different intervals and protocols at once.
- **`response off`** is worth setting on a headless station: by default AIS-catcher prints the server's reply to the console after every post, which fills the journal. With `response off` it prints only on error.
- v0.70 gave the HTTP and TCP output queues a **fixed size that drops the oldest message when a slow consumer can't keep up**, and added a **`Dropped` counter** to the output statistics. On a station with a marginal uplink, that counter is the thing to watch.
- **Unverified:** whether AIS-catcher posts an empty batch as a heartbeat when the station hears nothing during an interval. I could not locate the batching decision in the source (`Source/IO/HTTPClient.cpp` is the transport only). Do not build "station is up" monitoring on the assumption that posts keep arriving during a quiet period.

Useful comparison for the guide: the same mechanism feeds **APRS.fi** (`-H http://aprs.fi/jsonais/post/<secret-key> id <callsign> protocol aprs interval 30 -q`) and **Chaos Consulting** (`-H https://ais.chaos-consulting.de/shipin/index.php userpwd Station:Password gzip on interval 5`) — so a reader who already feeds one of those recognises the shape immediately.

**UDP (no token, simplest):**

```bash
AIS-catcher -u ais.openwaters.io 10110
```

**Both at once, plus two other aggregators** — `-u` is simply repeatable:

```bash
AIS-catcher -gr RTLAGC on TUNER auto -a 192K \
  -u ais.openwaters.io 10110 \
  -u 5.9.207.224 12345 \
  -u 192.168.1.50 10110 \
  -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15 \
  -N 8100 station "My Station" lat 41.5 lon -70.6 share_loc on \
  -v 60 -q
```

### 1.7 A complete `config.json`

Built from the documented schema ([JSON configuration](https://jvde-github.github.io/AIS-catcher-docs/usage/json-configuration)) — this is a composition of the documented per-section examples, not a snippet copied verbatim from the docs, so test it before publishing. Run it with `AIS-catcher -C config.json`, or drop it at `/etc/AIS-catcher/config.json` for the systemd service to pick up.

```json
{
  "config": "aiscatcher",
  "version": 1,

  "sharing": true,
  "sharing_key": "00000000-0000-0000-0000-000000000000",

  "receiver": [
    {
      "input": "rtlsdr",
      "serial": "ais",
      "verbose": true,
      "verbose_time": 60,
      "screen": 0,
      "meta": "TD",
      "rtlsdr": {
        "active": true,
        "rtlagc": true,
        "tuner": "auto",
        "bandwidth": "192K",
        "sample_rate": "1536K",
        "biastee": false
      }
    }
  ],

  "server": [
    {
      "active": true,
      "port": 8100,
      "station": "My Station",
      "share_loc": true,
      "lat": 41.5,
      "lon": -70.6,
      "file": "/var/lib/ais-catcher/stat.bin",
      "backup": 10,
      "prome": true
    }
  ],

  "http": [
    {
      "active": true,
      "url": "https://ais.openwaters.io/v1/receive",
      "userpwd": "x:YOUR_TOKEN",
      "gzip": true,
      "interval": 15,
      "response": false,
      "lat": 41.5,
      "lon": -70.6
    }
  ],

  "udp": [
    { "active": true, "host": "ais.openwaters.io", "port": 10110 },
    { "active": true, "host": "5.9.207.224",       "port": 12345 }
  ],

  "tcp_listener": [
    { "active": true, "port": 5011 }
  ]
}
```

Rules the docs call out explicitly and that are easy to get wrong:

- `config` must be `"aiscatcher"` and `version` must be `1`.
- Keys are **lowercase and case-sensitive**.
- Outputs (`udp`, `http`, `tcp`, `tcp_listener`, `mqtt`, `server`, `db`) are **arrays**, so you can have several of each.
- `sharing` / `sharing_key` are **root-level keys**, not an output array entry — a common mistake because everything else is an array.
- Receivers go in the `receiver` array. Root-level `input`/`serial` still works but is legacy and "may be removed in a future release".
- `active` defaults to true if omitted.
- Screen filtering can only be set on the CLI, not in JSON.
- **`config.json` takes precedence over `config.cmd`** when the systemd service reads both.

### 1.8 Using AIS-catcher as a forwarder for a non-SDR receiver

This is the answer for a **dAISy HAT / dAISy USB / Quark-elec / Vesper / chartplotter** owner who wants the AIS-catcher dashboard and multi-aggregator fan-out without an SDR. AIS-catcher takes NMEA in on serial, UDP, or TCP, and re-emits it on all its outputs.

```bash
# dAISy HAT on a Pi (GPIO serial), plus dashboard, plus two aggregators
AIS-catcher -e 38400 /dev/serial0 \
  -N 8100 \
  -u ais.openwaters.io 10110 \
  -H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15
```

For the newer **dAISy-catcher** (the Wegmatt device co-developed with jvde-github, sold as USB or Pi HAT), the docs are specific and different from the plain dAISy HAT:

```bash
# baud is 115200 — NOT the dAISy HAT's 38400
AIS-catcher -e 115200 /dev/serial0 -ge init_seq co2,v
```

`init_seq co2,v` sends a startup command to the device that turns on signal-level and frequency-offset reporting, so the dashboard's RSSI and Drift columns populate ([dAISy-catcher docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/input/daisy-catcher)). Over USB use the stable path: `ls /dev/serial/by-id/` and use `/dev/serial/by-id/usb-...` rather than `/dev/ttyACM0`, which renumbers.

Debug what's actually arriving on the wire with `-ge dump on`.

Other serial settings (`-ge`): `baudrate` (default 38400), `port`, `dump`, `dump_file`, `init_seq`, `flowcontrol` (`NONE`/`HARDWARE`/`SOFTWARE`).

Taking NMEA from the network instead:

```bash
AIS-catcher -x 0.0.0.0 10110          # listen for UDP NMEA
AIS-catcher -t txt://192.168.1.120:5011   # pull NMEA from a TCP server
AIS-catcher -t gpsd://localhost:2947      # from gpsd
```

TCP input has `persist` (reconnect, default on), `keep_alive`, `reset N` (periodically re-establish, with ±10% jitter, to recover wedged links) and `timeout` — worth mentioning because a station left alone for months will hit a half-open TCP connection eventually.

### 1.9 Downsampling and filtering

Relevant to aggregator etiquette and to metered/cellular links. All of these are per-output, set after the `-u`/`-H`/`-o` they apply to ([message filtering docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/message-filtering)):

```bash
# only send the message types an aggregator actually plots
AIS-catcher -u ais.openwaters.io 10110 filter on allow_type 1,2,3,5,18,19,24,27

# at most one position report per vessel per 30 s
AIS-catcher -u ais.openwaters.io 10110 position_interval 30

# drop duplicate content within a window (only meaningful when aggregating several sources)
AIS-catcher -o 1 unique on

# rate-limit your own transponder's VDO messages
AIS-catcher -O 368168720 -u host port own_interval 10
```

`filter on` is required for `allow_type`/`block_type`/`allow_mmsi`/`block_mmsi`/`allow_channel` to take effect (default off). `position_interval`, `own_interval` and `unique` work independently of the `filter` switch. No spaces in the comma-separated lists — the shell/parser will not cope.

For a guide's purposes: **do not downsample by default when feeding aiscast**, because deduplication and cross-station comparison are the point. Downsampling belongs in the "I'm on a metered LTE link" section.

### 1.10 The four things that actually go wrong

From the [troubleshooting page](https://jvde-github.github.io/AIS-catcher-docs/advanced/troubleshooting), which is short and mostly correct about what a new operator hits:

1. **Frequency error.** AIS-catcher tunes 162 MHz; a cheap dongle's oscillator is off, and you get poor or zero reception. Correct with `-p <ppm>`, measured with [kalibrate-rtl](https://github.com/steve-m/kalibrate-rtl), or read the web viewer's frequency-shift plot and use the long-run average. 1 ppm ≈ 162 Hz; **deviations between −3 and +3 don't matter**, so a modern stabilised dongle usually needs nothing.
2. **Thermal drift.** Cheap dongles drift as they warm. `-go AFC_WIDE on` handles it — and **has been the default model since v0.48**, so this is mostly a "don't turn it off" note.
3. **USB throughput.** Some machines can't sustain 1.536 MHz from the dongle. Test with `rtl_test -s 1536000` (from the osmocom rtl-sdr package); if you see lost samples, drop to `-s 288000` (or try `-s 2304000` if the machine is fast). On a Pi Zero W, `-F` first, then `-s 288K`, and accept the reception hit.
4. **Nothing at all.** Antenna not connected before start, no traffic in range, or another program holding the dongle. Run with `-v` to watch the counter and `-gr TUNER auto` to let gain auto-tune ([aiscatcher.org FAQ](https://www.aiscatcher.org/quickstart)).

Known issue worth carrying: `rtlsdr_close` can crash on Windows — a librtlsdr bug, not AIS-catcher; the shipped Windows binaries bundle a patched library.

### 1.11 Everything that is not AIS-catcher

Dates from the GitHub REST API on 2026-08-22. Fuller treatment in [`decoders-legacy.md`](decoders-legacy.md).

| Project | Latest release | Last commit (default branch) | Stars | Licence | Alive? |
|---|---|---|---|---|---|
| [dgiardini/rtl-ais](https://github.com/dgiardini/rtl-ais) | **v0.3, 2018-08-01** | 2026-08-05 | 308 | NOASSERTION | maintained-by-PR |
| [rubund/gnuais](https://github.com/rubund/gnuais) | **0.3.3, 2014-11-02** | **2015-12-25** | 126 | GPL-2.0 | **dead** |
| [bistromath/gr-ais](https://github.com/bistromath/gr-ais) | **never tagged one** | **2020-08-13** | 155 | none | **dead** |
| AISdeco2 (Sergey Serov, closed) | newest build **2016-11-12** | — | — | closed | **dead; site gone** |
| ShipPlotter (COAA) | 12.5.6.1, installer touched 2025-02-03 | — | — | €25 shareware | frozen |
| AISRec / AISRecWinFull (Feverlaysoft) | 2.304, **2023-05-27** | — | — | closed | Windows-only, stale |
| AIS Decoder / NMEA Router (N. Arundale) | 3.5.149 / 1.3.66, **2017-11-06** | site pushed 2018-07-17 | — | unstated | frozen; not a demodulator |
| [f4exb/sdrangel](https://github.com/f4exb/sdrangel) | **v7.27.2, 2026-08-19** | 2026-08-22 | 3,926 | GPL-3.0 | very alive |
| [M0r13n/pyais](https://github.com/M0r13n/pyais) | **v3.2.1, 2026-08-09** | 2026-08-09 | 253 | MIT | very alive |
| [schwehr/libais](https://github.com/schwehr/libais) | v0.15, 2015-06-16 | 2026-06-30 | 260 | NOASSERTION | maintained, unreleased |
| [dma-ais/AisLib](https://github.com/dma-ais/AisLib) | **v2.8.7, 2026-07-17** | 2026-07-17 | 194 | NOASSERTION | alive (Java) |
| `aiscat` (AIS-catcher's own bindings) | on [PyPI](https://pypi.org/project/aiscat/) | — | — | GPL-3.0 | new, alive |

**rtl_ais** is the interesting one, and the usual "it's dead" line is wrong: there has been no *release* since v0.3 in **August 2018**, but the repo still merges community PRs — the most recent, on **2026-08-05**, added a `-U` flag for multiple UDP destinations, i.e. the fan-out AIS-catcher has had for years. Output: UDP via `-h <host>` / `-P <port>` (default `127.0.0.1:10110`), TCP listener via `-T`. It decodes both channels but with a simpler demodulator.

The packaging detail that decides it for most readers: `rtl-ais` is in **Debian trixie / forky / sid but *not* bookworm**, and in **Ubuntu from plucky 25.04 onward but *not* noble 24.04 LTS**. Since Raspberry Pi OS is still bookworm-based for most people, **the audience most likely to reach for rtl_ais has to build it from source anyway** — at which point installing AIS-catcher's prebuilt `.deb` is strictly less work. Recommend rtl_ais only to someone who already has it running.

**gnuais** is upstream-dead — last release November 2014, last commit **2015-12-25** — but it is still packaged in distros, which is why people keep finding it. The disqualifier is not age, it is capability: **gnuais has no UDP or TCP NMEA output at all**. Its outputs are serial, MySQL, and an aprs.fi jsonais uplink (confirmed in `src/cfg.c`). It cannot feed aiscast or any UDP aggregator. Do not recommend it.

**gr-ais** never tagged a release and last saw a commit **2020-08-13**. It needs GNU Radio 3.8 and still builds a `swig/` directory — SWIG bindings were removed in GNU Radio 3.9, so it will not build against 3.10/3.11 without porting. The one fork optimistically named `maint-3.10` has a top commit reading "Bump to gr 3.8, **examples broken**". Dead.

**AISdeco2 — and a correction to a widely repeated claim.** The research brief for this document assumed AISdeco2 came from "the AISHub/VesselFinder people". **It did not.** The bundled licence reads "Copyright (c) 2014 Sergey Serov and other contributors, http://xdeco.org/" and the docs are signed `/sergsero`; no connection to AISHub or VesselFinder was found. The confusion is probably with AISHub's separate **AIS Dispatcher**, which is a forwarder, not a decoder (§2.3).

AISdeco2 is dead. **xdeco.org returns HTTP 500** ("Error establishing a database connection"); the last good Wayback capture is 2021-09-17. The newest binary is **2016-11-12**, surviving only on a third-party mirror ([xginn8/aisdeco](https://github.com/xginn8/aisdeco), last pushed 2017) that the Arch AUR package pulls from — and that AUR entry has been **flagged out-of-date since 2024-02-08**. The OpenCPN wiki's AIS software table notes "**xDeco.org is missing in action**"; wiki.luntti.net (updated 2026-05-08) says flatly it is "**not working anymore (2024)**".

**Flag for the guide author**: [rtl-sdr.com's AIS tutorial](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/) is still the top search result for "RTL-SDR AIS" and still says "the software we most recommend is AISdeco2". It is badly out of date, and a new guide should say so explicitly, because that page is where most beginners land.

**Windows sound-card tools, disentangled** — three products are routinely conflated:

- **AISMon** was *MarineTraffic's*, not Neal Arundale's: a freeware Windows sound-card demodulator, last public version 2.2.0. It is now effectively gone — the help article 404s after the Kpler migration and the installer URL returns 403.
- **Neal Arundale's AIS Decoder (3.5.149) and NMEA Router (1.3.66)**, both dated **2017-11-06**, are **not demodulators**. They take NMEA in from serial/USB/UDP/TCP/log files and convert, route and export it. Free, frozen, only useful downstream of a real decoder.
- **AISRec / AISRecWinFull** is Feverlaysoft's, a four-channel Windows receiver supporting RTL-SDR and Airspy over USB or TCP plus WAV input, **v2.304, 2023-05-27**, closed source, price not stated. The Android build was demoed but never publicly released.

**ShipPlotter** (COAA) is the only sound-card tool still commercially alive: 21-day trial, then **€25 personal / €215 professional** plus VAT. v12.5.6.1, installer last touched 2025-02-03, Win11 supported — but the change log stopped in **2020**, and it needs audio piped from SDR#.

**SDRangel is the live replacement for AISdeco2 for GUI users**, and most AIS guides omit it: 3,926 stars, GPL-3.0, v7.27.2 released 2026-08-19, last commit 2026-08-22, supporting RTL-SDR, Airspy, Airspy HF+, BladeRF, HackRF, LimeSDR, PlutoSDR, SDRplay and FunCube. Its [AIS demodulator plugin](https://github.com/f4exb/sdrangel/blob/master/plugins/channelrx/demodais/readme.md) does GMSK/FM at 9600 baud, **one instance per channel** — the documented pattern is to centre at 162 MHz with ≥100 kSa/s and run two demods at −25 kHz and +25 kHz. Decoded vessels also feed SDRangel's AIS and Map features.

Getting NMEA out to an aggregator is a checkbox: control **7 (UDP)** — *"When checked, received messages are forwarded to the specified UDP address (8) and port (9)"* — with control **10 (UDP format)** set to **NMEA** rather than binary. Tuning notes from the plugin readme worth carrying: RF bandwidth defaults to 25 kHz but *"more messages seem to be able to be received if this is around 16 kHz"*; the correlation threshold defaults to 0.6, where *"real preambles correlate above 0.9"*, and lowering it *"increases processor usage sharply"*.

It is GUI-first, so it is a poor fit for an unattended headless station — but it is the right answer for someone who wants to *see* the AIS signal, and a fair second opinion when AIS-catcher is hearing nothing.

**One genuinely new thing**: [ibelinp/libaisdemod](https://github.com/ibelinp/libaisdemod) — "AIS receiver in C99, complex baseband IQ in, AIVDM sentences out, MIT licensed, no dependencies" — created **2026-08-09**. Unproven and 19 stars, but it is the only fresh open-source demodulator core in this cycle and the right shape for embedding. Worth watching, not worth recommending yet.

**Decoding libraries**, for anyone processing sentences rather than producing them: **pyais** (MIT, v3.2.1 released 2026-08-09) is the healthiest and the one AIS-catcher's own docs recommend alongside libais and `gpsdecode`. **libais** has not cut a release since 2015 but is still being maintained (CI updated for Python 3.14 in June 2026). **AisLib** (Danish Maritime Authority, Java) released v2.8.7 in July 2026. AIS-catcher now also publishes **`aiscat`**, its own decoder as a Python package with wheels for Linux x86_64/aarch64/armv7l, macOS and Windows, Python 3.9–3.14 — output dictionaries match AIS-catcher's documented JSON field-for-field.

**Sound-card decoding** (ShipPlotter, AISMon, Neal Arundale's AIS Decoder / NMEA Router) is a pre-SDR technique: feed a scanner's discriminator audio into a soundcard and demodulate in software. It still works and a handful of long-running stations still use it, but a £20 RTL-SDR dongle plus AIS-catcher decodes **both AIS channels simultaneously** where an audio path gives you one, and needs no scanner. For a 2026 guide the honest framing is: sound-card decoding is a curiosity, not a recommendation. See [`decoders-legacy.md`](decoders-legacy.md) for the per-product status and pricing.

---

## 2. Forwarders and multiplexers for a serial / USB / NMEA receiver

> Exhaustive version with every config file, version date and caveat: **[`forwarders.md`](forwarders.md)** alongside this file. Condensed here.

If your receiver is a dAISy HAT, dAISy USB, dAISy-catcher, Quark-elec, Vesper, or a chartplotter with an NMEA 0183 output, you need something to read the serial port and push the sentences to a host on the internet. Ranked:

| Tool | Platform | Licence | Last release | Last commit | Remote UDP out | HTTP out | Verdict |
|---|---|---|---|---|---|---|---|
| **AIS-catcher `-e`** | everything | GPL-3.0 | v0.70, 2026-06-19 | 2026-08-22 | yes, repeatable | **yes** | **Best default.** See §1.8 |
| **docker-shipfeeder** | Docker amd64/arm64/armhf | GPL-3.0 | untagged | 2026-08-22 | yes (`UDP_FEEDS`) | yes (via extra options) | Best if feeding many aggregators |
| **socat + systemd** | Linux/macOS/BSD | GPL-2.0 | 1.8.1.3, 2026-06-26 | — | yes | no | Simplest possible bridge |
| **Signal K server** | Node ≥22 | Apache-2.0 | 2.31.1, 2026-08-16 | 2026-08-22 | via plugin | via plugin | Right answer on a boat |
| **AIS Dispatcher** | Linux x86_64/ARM, Windows | none stated | Linux 2.3 (2026-04-01); Windows 1.5.1 (2022-06-24) | — | yes, unlimited | no | Works, closed, no licence |
| **kplex** | Linux/macOS/BSD | GPLv3 | v1.4, **2019-01-06** | master 2020-08-27 | yes | no | Effectively abandoned |
| **ser2net** | Linux | GPL-2.0 | 4.6.8, 2026-08-03 | 2026-08-14 | no (it's a server) | no | Wrong shape — see below |
| **OpenCPN** | desktop | GPL-2.0 | 5.14.0, 2026-04-09 | 2026-08-20 | yes | no | Needs a desktop running |
| **sxfeeder** | armhf/arm64 deb | closed | — | — | no | no | ShipXplorer only |

**One implementation detail that constrains every choice**: aiscast's UDP listener splits *each datagram* on `\n`. Several sentences in one datagram are fine; a sentence split *across* two datagrams is corrupted. Whatever you use must emit whole lines per datagram.

### 2.1 socat — the one-liner

```bash
socat -d -d \
  /dev/serial/by-id/usb-Wegmatt_LLC_dAISy_2_plus-if00,b38400,cs8,parenb=0,cstopb=0,clocal=1,icanon=1,echo=0 \
  UDP-SENDTO:ais.openwaters.io:10110
```

Two things the widely copy-pasted version gets wrong:

- **`icanon=1`, not `raw`.** socat's man page says `raw` "is obsolete, use option `rawer` or `cfmakeraw` instead" — and, worse, raw mode reads up to the 8192-byte transfer block whenever bytes are available, so a sentence can straddle two datagrams. `icanon=1` puts the tty in canonical mode: one `read()` per line, therefore **one NMEA sentence per datagram**.
- **`UDP-SENDTO`, not `UDP-DATAGRAM`.** `UDP-SENDTO` is the datagram-client form for a single remote peer. `UDP-DATAGRAM` is for broadcast/multicast.

Use `/dev/serial/by-id/...` rather than `/dev/ttyACM0`, which renumbers on replug.

Real pitfalls: socat **exits when the device disappears** (fix: `Restart=always`), resolves **DNS once at startup** (fix: restart periodically), and cannot fan out to two destinations — it is strictly point-to-point. For fan-out, use AIS-catcher or kplex.

```ini
# /etc/systemd/system/ais-udp-forward.service
[Unit]
Description=Forward AIS NMEA from serial to aiscast over UDP
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ais
SupplementaryGroups=dialout
ExecStart=/usr/bin/socat -d -d /dev/serial/by-id/usb-...-if00,b38400,cs8,parenb=0,cstopb=0,clocal=1,icanon=1,echo=0 UDP-SENDTO:ais.openwaters.io:10110
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**ser2net is the wrong shape.** It is healthy (v4.6.8, 2026-08-03) but it exposes a serial port as a *server* clients connect to. Aggregators never connect to you. Use it to put a receiver on your LAN and point AIS-catcher (`-t host port`) at it; not to feed an aggregator.

### 2.2 Signal K

Current server is **2.31.1 (2026-08-16), Node ≥22, Apache-2.0**, actively developed. Add the receiver at Server → Data Connections → Add, Data Type `NMEA0183`, sub-type `serial`, device and baud rate. The `nmea0183` event carries every raw incoming line verbatim, `!AIVDM` and `!AIVDO` included, before parsing.

Getting it *out* is where it gets interesting:

- **`ais-forwarder`** ([npm](https://www.npmjs.com/package/ais-forwarder), [hkapanen/ais-forwarder](https://github.com/hkapanen/ais-forwarder)) is still the right tool and still the one aiscast's README names. v0.4.1, **last published 2023-03-20** — dormant for three and a half years, with no declared `engines`, so it is untested against Node 22. It is ~138 lines of `dgram` with zero dependencies, so the staleness is low-risk, but say so. It filters to AIS (so you send AIS rather than your whole NMEA firehose), appends `\n` to every message (exactly what aiscast wants), and takes an array of endpoints. **`aivdm` and `aivdo` both default to `false`** — a fresh install forwards nothing until you tick the boxes. For aiscast: `ipaddress: ais.openwaters.io`, `port: 10110`, both booleans on.
- **`@signalk/udp-nmea-plugin`** (v2.0.0, 2023-09-12) is the official alternative. No AIS filter, and `lineDelimiter` **defaults to `None`** — set it to `LF` explicitly or aiscast sees one run-on line.
- **Native UDP output exists in code but is not reachable from the admin UI.** `packages/streams/src/udp.ts` honours an `outEvent` option, but `BasicProvider.tsx` renders the "Output Events" field only for `serial`, `tcp` and `gpsd`, not `udp`. You can hand-edit `~/.signalk/settings.json` to add a `providers/udp` element with `"outEvent": "nmea0183"`, but it also binds the local port it sends to, appends no line terminator, and forwards *every* sentence rather than just AIS. **Unverified** — derived from reading the source, not run.
- **There is no built-in HTTP output at all.** No stream element or interface posts NMEA over HTTP; feeding `/v1/receive` from Signal K requires a plugin, which is what `signalk-aiscast` is for.
- **`@signalk/signalk-to-nmea0183` does not emit AIS** — its own README says so. If your AIS arrives over NMEA 2000, the piece that produces AIVDM is `signalk-n2kais-to-nmea0183`; pair it with `ais-forwarder`. (`signalk-aiscast` needs neither: it re-encodes the AIS PGNs itself.)

### 2.3 AIS Dispatcher (AISHub)

Alive, and more so than most people assume — but two products share the name with independent version numbers:

| Build | Version | Date |
|---|---|---|
| Linux 2.x | **2.3** | package built 2026-04-01 |
| Windows | **1.5.1** | 2022-06-24 |
| Linux/macOS 1.2 | 1.2 | 2015 — marked deprecated by AISHub |

Linux 2.x is a 14.5 MB `full.tar.bz2` of prebuilt binaries for `x86_64` and five ARM variants, unpacked into `/home/ais` — **not a .deb, not an .rpm**, no macOS. Install:

```bash
wget https://www.aishub.net/downloads/dispatcher/install_dispatcher
chmod 755 install_dispatcher
sudo ./install_dispatcher
```

Inputs: serial (default 38400,8,N,1), TCP client, TCP server, UDP server. **Output is UDP only** — no TCP, no HTTP. It removes duplicates, can add NMEA 4.10 tag blocks, and downsamples to one position report per ship per 0–60 s — but note the caveat from AISHub's own page: **"Downsampling affects AIS messages 1,2,3,18,19 only!"** Polygon and message-type filtering is Windows-only.

**It is not locked to AISHub.** Destinations are free-form host:port, and `-d` takes a comma-separated list, so:

```
AISD_OPTS='-m serial -D /dev/ttyACM0 -d data.aishub.net:2222,ais.openwaters.io:10110 -s mystation -w 10'
```

Four things to warn readers about:

1. **It feeds AISHub by default.** "By default, AIS Dispatcher streams data to AISHub anonymous port and your data is displayed at VesselFinder." Remove that destination if you don't want it.
2. **The web UI on port 8080 ships with `admin`/`admin`**, binds `::` (all interfaces), and there is an undocumented WebSocket listener on 8081 that also binds `::`.
3. **It auto-updates itself** via a systemd timer polling AISHub's `version.txt`. Fine for a hobby box, not fine if you pin versions.
4. **There is no licence.** No `LICENSE`/`EULA` in the tarball, no licence text on the product page, and `aishub.net/terms` and `/privacy-policy` both return 404. It is a no-cost closed binary with no stated grant of use. *(Flagged as unverified because absence of terms is what was found, not permissive terms.)*

No official Docker image; the community ones are stale (the most-linked wraps v1.2, not the 2.x rewrite).

### 2.4 kplex — works, abandoned

The classic Unix NMEA multiplexer, and the honest verdict is worse than "quiet". Last **release** v1.4, **2019-01-06**. `master` has not moved since **2020-08-27**. The only post-2020 activity is one merged PR on `develop` in Feb 2024. Six PRs open, oldest from 2020 — including *"Add files needed to make Debian package"*, still unmerged. **`kplex.net` has no DNS A record as of 2026-08-22**, so the site that distributed the `.deb` and macOS packages is gone. It is not in Debian (`sources.debian.org` search: no results) or Ubuntu (Launchpad: `total_size: 0`). You must build from source. There is an open, unanswered issue specifically about *"TCP sockets left after closed connection from marinetraffic.com"* — i.e. about feeding an aggregator.

It still works, and it is genuinely good at multi-source merging with filters, which AIS-catcher does not do as well:

```ini
# /etc/kplex.conf — serial in, fan out to two aggregators and a local TCP server
[global]
mode=background
checksum=yes

[serial]
direction=in
filename=/dev/ttyACM0
baud=38400
name=daisy

[udp]
direction=out
address=ais.openwaters.io
port=10110
ofilter=+AIVDM:+AIVDO:-all

[udp]
direction=out
address=data.aishub.net
port=1234

[tcp]
direction=out
mode=server
port=10110
```

Filters match the 5 characters after `!`/`$`; `+` allows, `-` denies, `~` rate-limits, and **you must end with `-all`** because anything unmatched is allowed. The repo ships `kplex.service`; run it as an unprivileged user in `dialout` — the README says running as root "is neither required nor recommended".

Recommend kplex only to readers who already run it or who need real multi-source NMEA merging. Everyone else: AIS-catcher.

### 2.5 The rest, briefly

- **OpenCPN** 5.14.0 (2026-04-09) can do serial in and a UDP output connection (Options → Connections → Add → Network → UDP, protocol NMEA0183). It works, but it is a desktop chart plotter with a GUI — a poor fit for an unattended station that must survive reboots headless.
- **ShipXplorer `sxfeeder`** is closed source from **AirNav Systems** (the RadarBox company — *not* Flightradar24, a correction worth making since docker-shipfeeder's own Dockerfile names the keyring `flightradar24.gpg`). It uploads to shipxplorer.com only and cannot fan out. Historically armhf-only, which broke Pi 5 users.
- **Node-RED**: a `serial in` node into a `udp out` node is about four clicks, and reasonable if you already run Node-RED. Not worth installing Node-RED for.
- **gpsd** passes AIVDM through but **ignores TAG blocks**; `gpsdecode` is a decoder, not a forwarder. AIS-catcher can read gpsd directly with `-t gpsd://localhost:2947`.
- **Hardware routers** — Yacht Devices, Actisense, Digital Yacht iAIS/AISnet, Quark-elec — mostly offer NMEA-over-UDP on the LAN, i.e. broadcast to a local port. Getting that to a cloud endpoint still needs something on a Pi; point AIS-catcher's `-x` at the broadcast port.
- **AvNav / OpenPlotter** bundle multiplexing for boat use; OpenPlotter's kplex module ([e-sailing/openplotter-kplex](https://github.com/e-sailing/openplotter-kplex)) was abandoned in 2020.

---

## 3. Feeding several aggregators at once

### 3.1 It is normal and nearly universal

The default posture of the hobby is multi-homing. One receiver, one antenna, one Pi, and the sentences go to five or ten places. Three mechanisms:

**AIS-catcher's repeatable `-u`.** The simplest. Each aggregator hands you a host and a port (usually a per-feeder port that *is* your identity, with no other auth), and you add one `-u host port` per aggregator. There is no practical limit; the cost is bandwidth, and each stream is a few kbit/s.

**docker-shipfeeder.** [`sdr-enthusiasts/docker-shipfeeder`](https://github.com/sdr-enthusiasts/docker-shipfeeder) (GPL-3.0, 92 stars, pushed 2026-08-22) wraps AIS-catcher in one container that fans a single receiver out to roughly 18 aggregators from environment variables. It is the best single map of the whole aggregator landscape — a host/port/protocol table for services you would otherwise have to email to discover. AIS-catcher's own docs point at it: *"For manual-mode setups focused on feeding aggregators, docker-shipfeeder by the sdr-enthusiasts community is a user-friendly alternative with excellent documentation and support. Note that it runs AIS-catcher in manual mode and does not offer the managed-mode dashboard"* ([Docker install docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker)). Detail in §2.

The aiscast recipe inside docker-shipfeeder is one line, because it has a generic escape hatch for aggregators it doesn't know about:

```yaml
- UDP_FEEDS=ais.openwaters.io:10110
```

Documented as: *"If you want to feed an additional AIS aggregator that uses a hostname/UDP port that is not listed above, then simply add a comma separated list of hostnames/ip addresses and UDP ports to the `UDP_FEEDS` parameter. Format: `UDP_FEEDS=domain1.com:port1[:params],domain2.com:port2[:params],...`"* ([docker-shipfeeder README](https://github.com/sdr-enthusiasts/docker-shipfeeder#readme)). To use the authenticated HTTP path instead, pass it through: `AISCATCHER_EXTRA_OPTIONS=-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15`.

The known-aggregator variables follow a `SERVICE_UDP_PORT` / `SERVICE_TCP_PORT` / `SERVICE_SHAREDATA` pattern, and each accepts either a bare port (`AISHUB_UDP_PORT=1234`) or `host:port` to override the default host. There is also `AISCATCHER_UDP_INPUTS=host:port[:CHANNELS],...` for chaining instances together.

Its aggregator table is the most complete public map of where a volunteer's sentences can go, and it doubles as a "who to sign up with" list. Condensed from the [README](https://github.com/sdr-enthusiasts/docker-shipfeeder#readme):

| Service | Variable | Default host | Protocol | Signup |
|---|---|---|---|---|
| aiscatcher.org | `AISCATCHER_SHAREDATA=true`, `AISCATCHER_FEEDER_KEY` | — | built-in community hub | [aiscatcher.org/addstation_ac](https://www.aiscatcher.org/addstation_ac) |
| AISHub | `AISHUB_UDP_PORT` | `data.aishub.net` | UDP | [aishub.net/join-us](https://www.aishub.net/join-us) |
| MarineTraffic | `MARINETRAFFIC_UDP_PORT` / `_TCP_PORT` | `5.9.207.224` | UDP or TCP (**not both**) | [marinetraffic.com/en/join-us/cover-your-area](https://www.marinetraffic.com/en/join-us/cover-your-area) |
| VesselFinder | `VESSELFINDER_UDP_PORT` / `_TCP_PORT` | `ais.vesselfinder.com` | UDP or TCP | [stations.vesselfinder.com/become-partner](https://stations.vesselfinder.com/become-partner) |
| ShipXplorer | `SHIPXPLORER_SHARING_KEY` + `SHIPXPLORER_SERIAL_NUMBER`, or `SHIPXPLORER_UDP_PORT` | `hub.shipxplorer.com` | sxfeeder, or plain UDP | [shipxplorer.com/addcoverage](https://www.shipxplorer.com/addcoverage) |
| APRS.fi | `APRSFI_FEEDER_KEY` + `APRSFI_STATION_ID` | `aprs.fi/jsonais/post/<key>` | **HTTP** (AIS-catcher `protocol aprs`) | [aprs.fi/?c=account](https://aprs.fi/?c=account); station ID is your ham callsign |
| Airframes | `AIRFRAMES_STATION_ID` | `feed.airframes.io:5599` | HTTP | no signup; ID form `II-STATIONNAME-AIS` |
| SDRMap | `SDRMAP_STATION_ID` + `SDRMAP_PASSWORD` | `ais.feed.sdrmap.org` | HTTP | [sdrmap docs wiki](https://github.com/sdrmap/docs/wiki/2.1-Feeding#request-api-credentials) |
| RadarVirtuel / ADSB-Network | `RADARVIRTUEL_STATION_ID` | `ais.adsbnetwork.com/ingester/insert/lourd` | HTTP | email support@adsbnetwork.com |
| MyShipTracking | `MYSHIPTRACKING_UDP_PORT` / `_TCP_PORT` | `178.162.215.175` | UDP or TCP | [myshiptracking.com …/add-your-station](https://www.myshiptracking.com/help-center/contributors/add-your-station) |
| ShippingExplorer | `SHIPPINGEXPLORER_UDP_PORT` / `_TCP_PORT` | `144.76.54.111` | UDP or TCP | request a port via their contact form |
| VesselTracker | `VESSELTRACKER_UDP_PORT` / `_TCP_PORT` | `83.220.137.136` | UDP or TCP | [vesseltracker.com … antenna-partner](https://www.vesseltracker.com/en/static/antenna-partner.html) |
| BoatBeacon (Pocket Mariner) | `BOATBEACON_SHAREDATA=true` | `boatbeaconapp.com:5322` | UDP/TCP | no key needed for `SHAREDATA` |
| ShipFinder | `SHIPFINDER_SHAREDATA=true` | `ais.shipfinder.co.uk:4001` | UDP | [shipfinder.co/about/coverage](https://shipfinder.co/about/coverage/) |
| MLAT.uk | `MLATUK_SHAREDATA=true` | `feed.mlat.uk:50001` | UDP | [mlat.uk/contribute#ais](https://www.mlat.uk/contribute#ais) |
| AIS Friends | `AISFRIENDS_UDP_PORT` | `ais.aisfriends.com` | UDP | [aisfriends.com/register](https://www.aisfriends.com/register) |
| HPRadar | `HPRADAR_UDP_PORT` | `aisfeed.hpradar.com` | UDP | — |
| **aiscast** | `UDP_FEEDS=ais.openwaters.io:10110` | — | UDP (or HTTP via extra options) | [token page](https://openwatersio.github.io/aiscast/token.html), one click |

The pattern is stark and worth pointing out: **the older commercial aggregators all use plain unauthenticated UDP where a per-feeder port number is your entire identity.** Newer entrants (aiscatcher.org, SDRMap, APRS.fi, Airframes, RadarVirtuel, aiscast) use TCP or HTTP with a key. A guide should explain that the port-as-identity scheme means anyone who learns your port can impersonate your station, and that it is a reason to prefer the authenticated HTTP path where one is offered.

**kplex / AIS Dispatcher fan-out.** For a serial receiver where you would rather not run AIS-catcher. AIS Dispatcher does up to 12 UDP destinations on Windows, unlimited on Linux ([AISHub](https://www.aishub.net/ais-dispatcher)). See §2.

One AIS-catcher quirk to relay: aggregators that choke on the long-range channel designators want `CD` relabelled to `AB`; docker-shipfeeder does this relabelling for you.

### 3.2 The terms tension

This is where a public guide has to be careful, because the technical answer ("just add another `-u`") and the licence answer are not the same.

| Aggregator | What the feeder gets back | What you sign away |
|---|---|---|
| **AISHub** | aggregated REST snapshot, 1 request/min. Gate: ≥10 vessels, ≥90% uptime, ≤60 s downsampling, ≤10 s delay | no terms page at all beyond the join page's "Terms of Use" section; contributors "are allowed to use the aggregated data for free" ([join-us](https://www.aishub.net/join-us)) |
| **MarineTraffic** (Kpler) | free premium account, station page, sometimes free hardware | a *"perpetual, transferable, irrevocable and royalty-free licence"*; data *"shall remain the exclusive property of Kpler"* ([terms](https://www.marinetraffic.com/en/p/terms)) |
| **VesselFinder** | premium features | §11.2 *"transferable, sublicensable license to … commercialize"*; §11.3 *"AIS Station data is not submitted in confidence"*; §11.5 benefits are *"discretionary"*; §7.4 bans AI training and competing services ([terms](https://www.vesselfinder.com/terms), dated 2026-01-15) |
| **ShipXplorer** (AirNav/RadarBox) | free Business plan, station page, ranking, free hardware near ports | no feeder data agreement at all |
| **aiscatcher.org** | station page, community map, coverage stats | no terms, no privacy page. Aggregate is closed: `robots.txt` disallows `/api/`, `/hub/`, `/stations`, `/livemap`, `/tiles/`, and API paths return 403 |
| **aiscast** | the deduplicated raw stream back (`wss://.../v1/nmea`), a station page, and the commitment that the aggregate carries an open licence | beta; a written feeder agreement is stated as the next stage |

**Nobody claims exclusivity, and MarineTraffic says so in as many words.** The operative sentence from [MarineTraffic/Kpler's Terms of Use](https://www.marinetraffic.com/en/p/terms):

> By using this Service, you grant us a **non-exclusive**, worldwide, perpetual, transferable, irrevocable and royalty-free licence to use, store, process, reproduce, modify, anonymise and aggregate the maritime related datasets you are adding on Kpler's Platforms…

"Non-exclusive" settles the question: you keep the right to license the same data to anyone else. **The restrictive clauses all run the other direction — over the aggregate they give *you*, not the feed you send them.** MarineTraffic's data "shall remain the exclusive property of Kpler", may not be redistributed, and "commercial use of any Kpler's data gathered through this Service is prohibited". VesselFinder §7.4 bars using their Content "to build or support a competing vessel-tracking, AIS, maritime intelligence, compliance, or analogous service."

**AISHub's terms do exist** — not on a terms page (`aishub.net/terms-of-use` 404s) but under a "Terms of Use" heading on [join-us](https://www.aishub.net/join-us), which is the whole contract:

> Every AISHub contributor is required to provide at least one raw AIS feed in NMEA format… Contributors who apply for API access… must meet the following quality requirements: Coverage of at least 10 vessels (average over the last 7 days); At least 90% uptime; Maximum downsampling rate of 60 seconds; Maximum delay of 10 seconds…
>
> The following AIS feeds are strictly prohibited: Synthesized or artificially generated NMEA data; Scraped or stolen data; **Data from publicly available AIS sources or services**.
>
> All contributors are allowed to use the aggregated data for free.

Read carefully, that prohibition is about **provenance, not exclusivity**: do not send them data you did not receive off the air. Piping another aggregator's stream into AISHub violates it; feeding your own antenna to AISHub *and* MarineTraffic does not.

**VesselFinder's §11.2 grant conspicuously omits "non-exclusive"** where MarineTraffic's includes it — "a worldwide, royalty-free, transferable, sublicensable license to host, store, reproduce, process, adapt/modify…, distribute, display, and otherwise use User Content to operate, secure, improve, and **commercialize** the Service". §11.3 adds that "AIS Station data is not submitted in confidence unless expressly agreed in writing", though it does promise not to publish precise station coordinates without opt-in. §11.5 makes every perk "**discretionary**".

Three things worth stating plainly in the guide:

1. **Feed as many aggregators as you like.** No AIS network's terms forbid it, and the tooling assumes it — docker-shipfeeder exists to feed ~20 services from one dongle. No report was found anywhere of a station being warned, throttled or removed for multi-homing.
2. **AISHub is the only aggregator that hands back a machine-readable copy of the pooled data**, and the only one whose bargain is written as a reciprocity requirement on published, objective thresholds rather than a discretionary reward. If a reader wants something back, feed AISHub first.
3. **Not one volunteer AIS network applies an open licence to its aggregate.** No CC, no ODbL, no equivalent of ADS-B's [adsb.lol](https://www.adsb.lol/docs/open-data/api/) (ODbL 1.0, history CC0, daily GitHub releases). That asymmetry — you grant a perpetual transferable licence, you get a map popup — is the honest framing for why a new open network is being proposed at all.

What contributors actually get:

| Aggregator | Account perk | Hardware | Data back |
|---|---|---|---|
| AISHub | account + feed monitoring; offline email after 6 h | none | **aggregated feed via API**, gated on ≥10 vessels and ≥90% uptime |
| MarineTraffic | Essential plan (>40% availability over 3 months) or Enterprise (>85%), auto-applied | free gear by application, in gap areas | viewing only; redistribution and commercial use prohibited |
| VesselFinder | free Premium, explicitly "discretionary" | none documented | viewing + embeddable map |
| ShipXplorer | Business membership, station page, ranking | free receiver near ports/routes, returnable if you stop | viewing |
| aiscatcher.org | community overlay in your own viewer | none | overlay only; no public API |
| aprs.fi | map + free non-commercial API | none | API |

> **Caveat on community sentiment.** Reddit and the aggregator forums could not be reached from this research environment (Reddit returns 403 to every method including a real browser; the web-search budget was exhausted). The "MarineTraffic is stingy, AISHub is fair" framing above is supported *structurally, from the terms themselves* — AISHub contractually owes API access on objective thresholds, MarineTraffic gates on performance and VesselFinder's perks are contractually discretionary. That is defensible to publish. **Attributing it to community sentiment is not**, on what was verified here. Spot-check against a live forum thread before publishing that framing.

### 3.3 aiscast's own stance

From the [README](https://github.com/openwatersio/aiscast#contributing-data): volunteer receptions are forwarded to AISHub as part of a reciprocal feed, and **only** volunteer receptions — *"only volunteer receptions go there; public feeds are never relayed, per their terms"*. Government open feeds and synthesized events are excluded from the AISHub relay. The corresponding rule on the way in, from aiscatcher.org's own registration page and worth adopting verbatim as community etiquette:

> please feed only live AIS data your station picks up off-air — not synthetic, simulated, or replayed signals, and not data copied from other AIS services.

The practical etiquette list for a guide:

1. **Feed as many aggregators as you like.** Nobody forbids it, and MarineTraffic's licence is explicitly non-exclusive.
2. **Feed each aggregator once.** Never both UDP and TCP to the same service unless they asked you to. This is the only "don't feed twice" rule that exists anywhere in the ecosystem, and it is routinely misremembered as a rule against multi-homing.
3. **Never relay another aggregator's data as your own reception.** This is the one thing AISHub's terms flatly prohibit, and it corrupts everyone's provenance and deduplication.
4. **Use one honest station identity and location.** Aggregators use station metadata for coverage modelling and fraud detection — VesselFinder §11.3 says so outright.
5. **Do not re-publish the aggregate you get back.** MarineTraffic and VesselFinder both bar redistribution and commercial use; AISHub's is free but members-only.
6. **Do not downsample below what the receiving network asks for.** AISHub's gate is ≤60 s downsampling and ≤10 s delay; aiscast wants none at all, because cross-station comparison is the point.
7. **Keep uptime up — it is the only currency.** >40%/>85% at MarineTraffic, ≥90% at AISHub, "discretionary and quality-linked" at VesselFinder.
8. **Read what you are granting before you feed a commercial aggregator.** "Perpetual, transferable, irrevocable" is a real sentence in a real contract, and *transferability* is what bit ADSBexchange's feeders when it was sold in 2023 ([Forbes](https://www.forbes.com/sites/cyrusfarivar/2023/02/02/adsb-exchange-flight-tracking-elonjet/)) — the network lost 15–20% of 9,000 feeders in a week.

---

## 4. Running it as a service

> Full ops detail — hardening, SD card wear, watchdogs, monitoring, aggregator status pages — in **[`ops.md`](ops.md)**.

### 4.1 Docker

**The official image**, `ghcr.io/jvde-github/ais-catcher`, tags `latest` (release) and `edge` (main). **SDRplay is not supported in the Docker images.**

```bash
docker run --rm -it --network=host \
  --device-cgroup-rule='c 189:* rmw' -v /dev/bus/usb:/dev/bus/usb \
  -v ais-config:/config \
  ghcr.io/jvde-github/ais-catcher:edge -E /config/config.json 127.0.0.1:8118
```

`189` is the Linux `usb_device` major number. The cgroup rule plus the `/dev/bus/usb` bind is what **survives a USB replug** — a plain `--device /dev/bus/usb/001/004` breaks the moment the dongle re-enumerates. For a serial receiver, use `--device /dev/ttyACM0` instead. In manual mode remember `-p 8100:8100` (or `--network=host`) if you run the web viewer.

**docker-shipfeeder**, for a station feeding several aggregators. Adapted from its [compose sample](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/config-examples/docker-compose.yml.sample):

```yaml
services:
  shipfeeder:
    image: ghcr.io/sdr-enthusiasts/docker-shipfeeder
    container_name: shipfeeder
    hostname: shipfeeder
    restart: always
    environment:
      - STATION_NAME=My&nbsp;Station
      - FEEDER_LAT=41.512345
      - FEEDER_LONG=-70.612345
      - STATION_HISTORY=3600
      - BACKUP_INTERVAL=5
      - SITESHOW=on
      - PROMETHEUS_ENABLE=on
      - SDR_TYPE=RTLSDR
      - RTLSDR_DEVICE_SERIAL=00000001
      - RTLSDR_DEVICE_GAIN=auto
      - RTLSDR_DEVICE_PPM=0
      - RTLSDR_DEVICE_BANDWIDTH=192K
      - AISCATCHER_DECODER_AFC_WIDE=on
      - AISHUB_UDP_PORT=12345
      - AISCATCHER_SHAREDATA=true
      # aiscast:
      - UDP_FEEDS=ais.openwaters.io:10110
      # or authenticated HTTP instead:
      # - AISCATCHER_EXTRA_OPTIONS=-H https://ais.openwaters.io/v1/receive USERPWD x:<token> GZIP on INTERVAL 15 RESPONSE off
    ports:
      - 90:80            # web viewer (container port is 80)
    device_cgroup_rules:
      - 'c 189:* rwm'
    tmpfs:
      - /tmp
    volumes:
      - /etc/localtime:/etc/localtime:ro
      - /etc/timezone:/etc/timezone:ro
      - /opt/ais/shipfeeder:/data
      - /dev:/dev:rw
```

Points worth making in the guide: `restart: always`; `tmpfs: /tmp` keeps scratch writes off the SD card; `/data` must persist or the statistics file, web plugins and `about.md` are lost on every restart; the viewer is on container port **80**, not 8100.

**Which to use**: one aggregator → the official image or plain systemd. Several → docker-shipfeeder. The upstream docs endorse this split themselves.

### 4.2 systemd

**The `.deb` does not ship a systemd unit.** `debian/rules` installs only the binary, plugins, DBMS schemas, udev rules and bundled libraries; `debian/postinst` installs udev rules and runs `ldconfig`. There is no `debian/*.service` in the repo. The unit is **written at install time by [`scripts/aiscatcher-install`](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install)** — which matters, because someone who installs the `.deb` by hand gets no service at all.

| Thing | Path |
|---|---|
| Service | `ais-catcher.service` |
| Unit file | `/etc/systemd/system/ais-catcher.service` |
| Reboot-watchdog unit | `/etc/systemd/system/ais-catcher-reboot.service` |
| JSON config | `/etc/AIS-catcher/config.json` |
| Command-line config | `/etc/AIS-catcher/config.cmd` |
| Managed-mode config | `/etc/AIS-catcher/aiscatcher.json` |
| Stat backup / plugins / web assets | `/etc/AIS-catcher/stat.bin`, `/plugins`, `/webassets` |
| Install log | `/var/log/aiscatcher-install.log` |

There is **no `/etc/default/ais-catcher`**. `config.json` takes precedence over `config.cmd`, and the unit passes the latter using systemd's `@file` argument-file syntax. The generated unit, with defaults:

```ini
[Unit]
Description=AIS-catcher Service
After=network.target
StartLimitIntervalSec=0
StartLimitBurst=0

[Service]
User=aiscatcher
Group=aiscatcher
SupplementaryGroups=plugdev dialout
ExecStart=/usr/bin/AIS-catcher -G level debug -o 0 -C /etc/AIS-catcher/config.json @/etc/AIS-catcher/config.cmd
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

It runs as a locked system user `aiscatcher` in `plugdev` and `dialout` — worth pointing out, because most hand-rolled AIS tutorials run everything as root. (`--no-user`, used by the official Docker image, omits that block and runs as root.) `--managed`/`-M` swaps `ExecStart` for `-E /etc/AIS-catcher/aiscatcher.json 0.0.0.0:8118`.

**The reboot watchdog is the best-hidden feature here.** Off by default, and specifically for *"recovering from unresolvable USB device failures"* — the failure mode where an SDR dongle wedges so hard that restarting the process cannot fix it:

```bash
sudo aiscatcher-install --set-reboot-on-failure          # after 3 restarts in 30 min
sudo aiscatcher-install --set-reboot-on-failure 5 3600   # after 5 restarts in 60 min
sudo aiscatcher-install --unset-reboot-on-failure
sudo aiscatcher-install --set-auto-restart / --unset-auto-restart
```

It schedules `shutdown -r +5`, then polls every 10 s and **cancels the reboot if the service comes back**, so a transient failure does not cost you a reboot. Cancel manually with `shutdown -c`. Note the interaction the script itself documents: `OnFailure=` only fires when `StartLimitBurst > 0`; with the default burst of 0, `Restart=always` retries forever and the watchdog never triggers. **Enabling the watchdog is a deliberate act, and a guide for unattended stations should recommend it.**

`config.cmd` holds plain command-line parameters, one place, `#` for comments — the simplest thing for someone who followed a CLI tutorial:

```sh
# /etc/AIS-catcher/config.cmd
-gr RTLAGC on TUNER auto -a 192K
-N 8100 station "My Station" lat 41.5 lon -70.6 share_loc on file /var/lib/ais-catcher/stat.bin backup 10
-u ais.openwaters.io 10110
-v 60 -q
```

Put the token in `config.json` rather than `config.cmd` and `chmod 600` it, so it does not show up in `ps`.

For a hand-rolled forwarder (socat, a script), the template that matters is the one in §2.1: `Restart=always`, `RestartSec=5`, a dedicated `User=` in `SupplementaryGroups=dialout`, and `After=network-online.target`/`Wants=network-online.target`. The unplug case and the DNS-changed case are both fixed by the same restart loop.

### 4.3 Knowing whether it is still working

The characteristic failure of a volunteer station is not a crash, it is **silence nobody notices for three months**. Four layers, cheapest first:

**Your own station page on each aggregator.** aiscatcher.org's [station table](https://www.aiscatcher.org/stations) gives every registered station a row with: status, name, operator, country, city, elevation, equipment, last update, messages, vessels, unique vessels, and an **Online %**. That last column is the one to check — it reads as a rolling uptime figure, and stations at 0 messages / blank online % are visibly dead in the list. Registration is a form at [addstation_ac](https://www.aiscatcher.org/addstation_ac) (requires AIS-catcher ≥ 0.58) which returns a UUID sharing key; **save it, because you need it to edit the station later**.

For aiscast, your station page is the map with `?station=<your id>`, and the same numbers are at `GET /v1/stations/{id}`: vessels heard, coverage extent, message counts, and how many of your receptions were heard elsewhere first.

**Prometheus.** `-N 8100 prome on` exposes `/metrics`, and `/metrics` is one of the endpoints AIS-catcher deliberately CORS-exposes. If you already run Prometheus, this is the least-effort path to real alerting, and there is a [Grafana guide](https://jvde-github.github.io/AIS-catcher-docs/advanced/grafana) upstream.

**A dead-man's switch.** A cron on the Pi that curls a healthchecks.io / Uptime Kuma push URL *only when the message counter has moved* turns "my station is dead" into an email. `/api/stat.json` on the web viewer is the counter to read. This is better than pinging on a timer, which only tells you the Pi is on.

**Do not rely on the aggregator noticing.** AISHub emails after **6 hours** offline (verified on their join page); VesselFinder emails on performance problems. MarineTraffic is widely said to send station-offline mail, but its help-centre article 404s and the site blocks non-browser fetches, so **the wording and threshold are unverified — do not quote a number.**

### 4.4 Restart-on-no-data, which nothing ships

Neither AIS-catcher nor docker-shipfeeder has a stale-feed watchdog, so it is a pattern you assemble. The right signal is specific: `/api/stat.json` serves both a `msg_rate` and a **monotonic `received` counter** (verified in [`Source/Web/WebViewer.cpp`](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Web/WebViewer.cpp)). **Alarm on the counter, not the rate** — a quiet harbour at 03:00 legitimately reads a rate of zero, and a naive `msg_rate == 0` check will restart a perfectly healthy station every night.

```bash
#!/bin/bash
# Restart AIS-catcher only if the lifetime message counter has not advanced
# across several consecutive checks. Pair with a 5-minute systemd timer.
set -uo pipefail
URL="http://127.0.0.1:8100/api/stat.json"
STATE=/var/lib/ais-staleness/state
STALE_CYCLES=6      # ~30 minutes of true silence at a 5-minute interval
mkdir -p "$(dirname "$STATE")"
now=$(curl -sf --max-time 10 "$URL" | jq -r '.received // empty')
...
```

Full script and timer in [`ops.md`](ops.md) §6.2.

**The hardware watchdog** catches the layer below: `CONFIG_BCM2835_WDT=y` is built in — not a module — in every current Pi kernel defconfig **including the Pi 5's `bcm2712`**, so `/dev/watchdog0` exists on stock Raspberry Pi OS with nothing to load and no `dtoverlay`. Hand it to systemd:

```ini
# /etc/systemd/system.conf.d/10-watchdog.conf
[Manager]
RuntimeWatchdogSec=15
RebootWatchdogSec=2min
```

The bcm2835 watchdog tops out near 15 s, so that is the ceiling. This recovers a **kernel hang only** — it does nothing about AIS-catcher going quiet, which is what the counter check above is for, and nothing about a wedged dongle, which is what AIS-catcher's own `--set-reboot-on-failure` is for. Three layers, three different failures.

**USB dongles falling off the bus** is the classic long-run failure. Note one trap: **`uhubctl` cannot power-cycle a single port on a Raspberry Pi** — all onboard ports are ganged on every model, and the Pi 5 reports per-port switching support that it does not have. A powered external hub is the real fix.

### 4.5 SD card wear

An AIS station writes continuously for years. Ranked by payoff:

1. **Boot from USB SSD or NVMe.** Removes the card from the write path entirely. `sudo raspi-config` → Advanced Options → Boot Order → an option including NVMe (boot mode `6`) or USB-MSD (`04`). Best single fix.
2. **Get swap off the card**: `sudo systemctl disable --now dphys-swapfile.service`, or better keep swap in compressed RAM with [zram-swap](https://github.com/wiedehopf/adsb-wiki/wiki/zram-swap).
3. **Cap the journal.** The defaults are proportional, not absolute: `SystemMaxUse=` "defaults to 10% … capped to 4G", so on a 32 GB card that is 3.2 GB of journal you never asked for.

   ```ini
   # /etc/systemd/journald.conf.d/00-station.conf
   [Journal]
   Storage=persistent
   SystemMaxUse=200M
   SystemKeepFree=500M
   SystemMaxFileSize=20M
   MaxRetentionSec=2week
   RateLimitIntervalSec=30s
   RateLimitBurst=1000
   ```
4. **log2ram** or plain tmpfs mounts for `/tmp`, `/var/tmp`, `/var/log`.
5. **A2-rated card** if you must use SD. The Pi docs publish 4 kB random IOPS: Pi 4 (DDR50) 3,200 read / 1,200 write; Pi 5 (SDR104) 5,000 / 2,000.
6. **Read-only root via overlayfs** (`raspi-config` → Performance Options → Overlay File System) is the strongest protection and the most annoying: **AIS-catcher's `stat.bin` backup and every config edit are lost on reboot** unless you mount something writable for them. Right for a remote unattended site, overkill for a station you tinker with.

Also cap Docker's logs, which otherwise grow without limit: `logging: {driver: json-file, options: {max-size: 10m, max-file: "3"}}`.

### 4.6 Remote access

**The rule: never port-forward 8100 or 8118 to the internet.** This is upstream's position, not just good practice (§6). sdr-enthusiasts make the same point about a feeder's status page: *"Please be careful not to expose the status website to the internet as users may be able to start/stop/change the service from there."*

Tailscale is the recommended shape — the station dials out, nothing is forwarded:

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --advertise-tags=tag:ais-station
sudo tailscale set --ssh        # current syntax; older guides say `tailscale up --ssh`
```

Tailscale SSH needs **two** things in the tailnet policy: a network grant *and* an `ssh` rule — a common stumbling block. Then the web UI is just `http://<tailscale-ip>:8100`. Alternatives: Cloudflare Tunnel (good for publishing a *read-only* map with Access in front; putting the writable dashboard behind it defeats the point), ZeroTier, plain WireGuard, or `ssh -L 8100:localhost:8100`. Whichever you pick, still run `ufw`/`nftables` default-deny inbound so a misconfigured router cannot silently expose 8100.

---

## 5. Station metadata: position, name, timestamps, TAG blocks

Verified flags, since this is the section most likely to contain folklore.

**Station position** is set in *three independent places* and they do different things:

| Where | Flag | What it does |
|---|---|---|
| Receiver-level | `-Z <lat> <lon>` | sets the receiver location globally |
| Web viewer | `-N ... lat 50 lon 3.14 share_loc on` | lets the viewer draw range rings and per-vessel distance/bearing. `share_loc` defaults to **off**; without it the viewer does not get the location |
| HTTP feed | `-H ... lat 41.5 lon -70.6` | the position sent to the receiving service in the feed metadata |
| Community feed | registered on the [addstation form](https://www.aiscatcher.org/addstation_ac), not on the command line | you enter lat/lon/elevation/country/city there and get a UUID sharing key back |

Sources: [CLI help](https://jvde-github.github.io/AIS-catcher-docs/usage/cli), [web viewer](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/web-viewer), [HTTP](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/HTTP).

**Station name**: `-N ... station "Southwood" station_link http://example.com` for the viewer; `-H ... stationid MyStation` (aliases `id`, `callsign`) for the HTTP feed. There is no single global "station name".

**Own MMSI**: `-O <MMSI>` at the receiver, or `own_mmsi` in the viewer/receiver config. Matters if your station is on a vessel with a transponder — it lets AIS-catcher distinguish `!AIVDO` and lets `own_interval` rate-limit it.

**Metadata generation** — `-M`, off by default *"to keep the program as light as possible when running as a server on low spec devices"*:

| Value | Effect |
|---|---|
| `-M T` | timestamp NMEA messages |
| `-M D` | decoder metadata: signal power (dB) and applied ppm correction |
| `-M M` | country of the station derived from MMSI, in JSON output |

Combine as `-M TD`.

**TAG blocks** are real and supported, and this is where most existing guides are wrong. The mechanism is **not** `-o 5` and there is no `NMEA_TAG` env var — it is a **`msgformat` value**:

- `msgformat NMEA_TAG` on any network output, or `-o 7` on screen ([what's new, v0.53-era entry](https://jvde-github.github.io/AIS-catcher-docs/what-is-new/): *"NMEA 4.0 Tag Blocks: support for NMEA tag blocks (`msgformat nmea_tag` for networking or `-o 7` for screen)"*).
- The block is IEC 61162-450 style, carrying **source and timestamp**; the timestamp lands in the `c:` field and uses microsecond resolution where available. The **group ID is only included for multi-line sentences**.
- Round-tripping: AIS-catcher (and the `aiscat` Python decoder) read a tag block's `c:` field back into a `toa` field on decode.

So to send aiscast tag-blocked NMEA over UDP:

```bash
AIS-catcher -u ais.openwaters.io 10110 msgformat NMEA_TAG
```

aiscast's README says TAG blocks are welcome on the UDP path.

**Screen/`-o` output modes**, for reference:

| `-o` | `msgformat` | Output |
|---|---|---|
| 0 | `NONE` | silent (also `-q`) |
| 1 | `NMEA` | bare `!AIVDM,...` (also `-n`) |
| 2 | `FULL` | NMEA + `( MSG: 3, REPEAT: 0, MMSI: …, signalpower: -44.0, ppm: 0, timestamp: … )` — **the default** |
| 3 | `JSON_NMEA` | that metadata wrapped in JSON |
| 4 | `JSON_SPARSE` | sparse JSON decode |
| 5 | `JSON_FULL` | full JSON decode of every field |
| 7 | `NMEA_TAG` | NMEA prefixed with a tag block |
| — | `BINARY_NMEA`, `COMMUNITY_HUB` | internal formats |

**Timestamps in the feed**: the `jsonaiscatcher` HTTP envelope carries `encodetime` (when the batch was built) and a per-message `rxtime` in `YYYYMMDDHHMMSS` UTC. aiscast parses `rxtime` as the source time. `JSON_FULL` additionally carries `rxuxtime` with microsecond resolution (added in v0.69).

**Persistence of statistics**: the web viewer's plots reset on restart unless you back them up:

```bash
AIS-catcher -N 8100 file /var/lib/ais-catcher/stat.bin backup 10
```

Minimum backup interval is 5 minutes; `backup` range is 5–2880 minutes. Also `stats_on_close` writes stats on shutdown.

**Prometheus/Grafana**: `-N 8100 prome on` exposes `/metrics`. There's a [Grafana guide](https://jvde-github.github.io/AIS-catcher-docs/advanced/grafana) in the docs. This is the clean way to get station uptime into an existing monitoring stack.

---

## 6. Security and privacy

What a volunteer is actually exposing, and what to do about it.

**What leaves the station**

| Path | Carries |
|---|---|
| `-u host port` (plain NMEA) | the sentences only. aiscast derives a station id from a **keyed hash** of your source IP, never publishes the address |
| `-u ... msgformat NMEA_TAG` | + receive timestamp and source tag |
| `-u ... JSON on` / `JSON_FULL` | + timestamp, ppm, signal level (AIS-catcher-to-AIS-catcher only; most consumers can't parse it) |
| `-H url` | the full `jsonaiscatcher` envelope: `stationid`, `receiver.description`/`version`/`engine`/`setting`, `device.product`/`vendor`/`serial`, plus `lat`/`lon` if set. **Your dongle's serial number and your station coordinates go to the endpoint.** |
| `-X <key>` | raw AIS to aiscatcher.org, tied to the station you registered there (name, operator, city, country, elevation, lat/lon, equipment description) |

> **Verified discrepancy — the community feed is on by default, whatever the docs say.** The [community feed docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/community-feed) state "Sharing is **not enabled by default**". The source disagrees. In [`Source/Application/Engine.cpp`](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Application/Engine.cpp) (lines 95–102, `main` as of 2026-08-22):
>
> ```cpp
> // sharing is on by default as soon as there is anything to share with
> if (!xshare_defined && !comm_feed && (has_server || !msg.empty()))
> {
>     if (!control)
>         Warning() << "Hint: Use '-X on' to share with aiscatcher.org community (enables community overlay) or '-X off' to disable. Currently ON by default.";
>     createCommunityFeed();
> }
> ```
>
> So: **if you never pass `-X`, and you have configured a web server or any output at all, AIS-catcher opens an anonymous community feed to aiscatcher.org.** It logs a hint saying so, but a station operator reading only the docs would not expect it. Pass `-X off` (or `"sharing": false`) to actually opt out. A guide that tells people "your data only goes where you send it" is wrong unless it says this.

Practical advice for the guide:

- The community-feed icon in the AIS-catcher viewer shows sharing state: **red = off, orange = anonymous, green = keyed**. Worth telling readers to check it.
- aiscatcher.org's registration form has a **"Hide station on map"** checkbox — data still shared, no public marker — and a **"Roaming"** flag for vessel-mounted stations (which then requires an MMSI). Good precedent for a privacy-conscious feeder.
- aiscast's stated position is that volunteer station locations are never published with precision and UDP stations are keyed-hashed ([README](https://github.com/openwatersio/aiscast#licensing-and-attribution)).

**Token handling**

- `-H ... USERPWD x:<token>` puts the token in the process command line, so it is visible in `ps` to any local user and in the systemd unit / `config.cmd` file. Prefer `/etc/AIS-catcher/config.json` with `"userpwd": "x:TOKEN"` and `chmod 600` it.
- `ssl_verify` defaults to `true`. Never turn it off — the token is sent as HTTP Basic and TLS is the only thing protecting it.
- aiscast tokens are self-serve and never expire, so a leaked one is a standing problem; the token page keeps the key in the browser.

**Do not expose the web interfaces to the internet.** The AIS-catcher docs say this outright: *"The dashboard is intended for use on your local network. If you want to expose your station's data publicly, do not expose the dashboard — share your data via the community feed or add a web viewer instead"* ([remote access](https://jvde-github.github.io/AIS-catcher-docs/managed/remote-access)).

Concretely:

- The managed control panel (**8118**) binds to `127.0.0.1` by default; binding it to `0.0.0.0` *forces* a password on first access, but that is a password on a hobby HTTP server, not a hardened auth system.
- **AIS-catcher-control (8110) ships with the default password `admin`.** It can start/stop services, run update scripts, and reboot the host. Never port-forward it. Change the password immediately (it prompts, but confirm it took).
- The web viewer (**8100/8119**) has no authentication at all. It does have `server_mode` (a "hardened multi-station server mode") and `ip_bind` if you must expose it, and it deliberately sends `Access-Control-Allow-Origin: *` **only** on the data endpoints (`/api/ships.json`, `/api/stat.json`, `/metrics`, `/kml`, …), not on the HTML.
- Exposing the viewer publishes your station's location and range to anyone, if `share_loc on` is set.

**Reaching it remotely, the right way**: Tailscale (or WireGuard/ZeroTier/Cloudflare Tunnel) rather than a port forward, so the dashboards stay on a private network. See §4.

**Legal**: reception rules vary by administration. The AIS-catcher disclaimer and aiscatcher.org's FAQ both say receiving is permitted in most countries but *republishing* rules differ — a guide aimed at an international audience should repeat that rather than assert it's fine.

**Feed authenticity**: aiscatcher.org's registration page states the community norm plainly and it's worth borrowing: *"please feed only live AIS data your station picks up off-air — not synthetic, simulated, or replayed signals, and not data copied from other AIS services."*

---

## What could not be verified

Flagged here so nothing in the guide rests on it silently.

**Community sentiment, everywhere in this document.** Reddit returns 403 to every method tried — the JSON API, `old.reddit.com`, and a real browser (bot challenge) — and the session's web-search budget was exhausted early. **No Reddit thread, MarineTraffic forum post, AISHub forum post or OpenCPN forum thread was read directly.** Every "community verdict" here rests instead on maintained wiki pages (the [OpenCPN AIS software wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_software:ais-software), [wiki.luntti.net](https://wiki.luntti.net/index.php?title=RTL-SDR_AIS_Ship_Tracking) updated 2026-05-08), distro packaging state, AUR out-of-date flags, GitHub issue trackers, and the aggregators' own pages. That is defensible evidence, but it is not the same thing, and the "MarineTraffic is stingy vs AISHub is fair" framing in particular should be spot-checked against a live thread before publishing it as sentiment rather than as a reading of the terms.

**MarineTraffic's station-offline email** — wording and threshold unknown. The help-centre article 404s and the site blocks non-browser fetches. AISHub's 6-hour figure *is* verified; do not state a MarineTraffic number.

**AIS-catcher's empty-batch heartbeat.** Whether `-H` posts during an interval in which the station heard nothing could not be located in the source. Do not build "station is up" monitoring on the assumption that posts keep arriving during a quiet period.

**AIS Dispatcher's licence.** There is no `LICENSE`, `EULA` or terms text in the tarball or on the product page, and `aishub.net/terms-of-use` and `/privacy-policy` both 404. What was found is an *absence* of stated terms, not permissive terms. Its shipped binary's internal build tag also reads `v2.1` while the package is labelled 2.3, and there is no published changelog for the Linux 2.x line.

**AISdeco2 and rtl_ais against an RTL-SDR Blog V4.** The library-level reasoning is sound (both link `librtlsdr.so.0`; osmocom added V4 support in 2.0.x without changing SOVERSION), but no on-air confirmation was found for either. The one rtl_ais V4 issue is inconclusive and was closed for lack of feedback.

**Signal K's native `providers/udp` output for NMEA 0183.** Derived from reading `udp.ts`, `tcp.ts`, `simple.ts` and `providers.ts` — not run, and not documented or demonstrated anywhere in the repo. Also unverified: whether hand-added keys survive a round-trip through the admin UI.

**ser2net pushing to an outbound UDP host.** gensio addresses are symmetric in principle, but no documented ser2net example does it. Treat serial→outbound-UDP as socat's job.

**The `config.json` in §1.7** is composed from the documented per-section examples in the AIS-catcher docs, not copied verbatim from a single working file. Test it before publishing.

**ShipXplorer's feeder software details** — whether `sxfeeder` accepts a generic RTL-SDR or takes NMEA from a third-party decoder is not stated on any public page.

**A measured rtl_ais vs AIS-catcher benchmark.** None found from a primary source. AIS-catcher's own README contains no comparison against rtl_ais, AISdeco2 or gnuais. The "AIS-catcher hears more" claim is widely repeated and plausible from its dual-channel coherent design, but it is not sourced to a published measurement here.

**Two premises in the research brief that turned out to be wrong**, corrected in the body above and worth restating: **AISdeco2 is not from the AISHub/VesselFinder people** (it is Sergey Serov's, xdeco.org), and **`sdr-enthusiasts/docker-aiscatcher` does not exist** (GitHub returns 404; a repo search returns zero results). There is also **no ESP32 port of AIS-catcher**. And **`install_debian_ubuntu.sh` no longer exists** — the current installer is `scripts/aiscatcher-install`.
