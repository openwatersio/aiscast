# AIS decoders other than AIS-catcher, and AIS decoding libraries

Research date: 2026-08-22. Every claim below carries a source URL. Anything I could not confirm from a primary source is called out in the "Could not verify" section at the end, and inline with **[UNVERIFIED]**.

Terminology used throughout: a **demodulator/decoder** turns RF (IQ) or audio into `!AIVDM` sentences. A **library** turns `!AIVDM` sentences into structured data. A **router/forwarder** (AIS Dispatcher, NMEA Router) moves already-formed sentences around and does neither.

## Comparison: RF / audio → !AIVDM

| Tool | Input | Output | Platform | License | Last commit | Last release | Alive? |
|---|---|---|---|---|---|---|---|
| [rtl_ais](https://github.com/dgiardini/rtl-ais) | RTL-SDR IQ (both channels at once) | AIVDM/AIVDO over UDP, TCP listener, stderr; raw samples to stdout with `-A` | Linux, Windows, macOS | GPL-2.0-or-later | 2026-08-05 | v0.3, 2018-08-01 | Alive, low activity; packaged in Debian/Ubuntu |
| [gnuais / gnuaisgui](https://github.com/rubund/gnuais) | Sound card (VHF discriminator audio) | Serial port NMEA, MySQL, jsonais HTTP uplink (aprs.fi). No UDP output | Linux | GPL-2.0 | 2015-12-25 | 0.3.3, 2014-11-02 | Upstream dead; alive only as a Debian/Ubuntu package |
| [AISdeco2](http://xdeco.org/) | RTL-SDR IQ (both channels) | AIVDM over UDP and own TCP server | Windows, Linux x86/x86-64, RPi (binary only) | MIT text bundled, no source published | Binary dated 2016-11-12 | 20161112 | **Dead.** Home site returns HTTP 500 |
| [AISMon](https://help.marinetraffic.com/hc/en-us/articles/205339707-AISMon) | Sound card (discriminator audio) | NMEA | Windows | Freeware, closed | n/a | 2.2.0 | Dead: help page 404s after Kpler migration, installer URL now 403 |
| [AIS Decoder / NMEA Router](https://arundaleais.github.io/docs/ais/ais_decoder.html) (Neal Arundale) | Serial/USB, UDP, TCP, log files — **NMEA in, not RF or audio** | Text, CSV, HTML, XML, KML/KMZ, NMEA; to file, FTP, UDP | Windows (Linux via Wine) | Freeware, closed, no price stated | n/a | 3.5.149 and 1.3.66, both 2017-11-06 | Frozen since 2017, still downloadable |
| [ShipPlotter](http://www.coaa.co.uk/shipplotter.htm) | Sound card from VHF receiver, or serial AIS receiver, or TCP | Plot display, NMEA serial out, HTTP server, Google Earth | Windows through Win11 | Shareware: 21-day trial, €25 personal / €215 professional | n/a | 12.5.6.1; installer file mtime 2025-02-03 | Alive but barely; site page mtime 2024-12-30 |
| [gr-ais](https://github.com/bistromath/gr-ais) | IQ via GNU Radio | GNU Radio blocks / `ais_rx` app | Any GNU Radio host | GPLv3 headers, no LICENSE file | 2020-08-13 | never tagged | Dead; needs GNU Radio 3.8 |
| [SDRangel AIS demod](https://github.com/f4exb/sdrangel/blob/master/plugins/channelrx/demodais/readme.md) | IQ from RTL-SDR, Airspy, HackRF, SDRplay, LimeSDR, Pluto, FunCube | NMEA or binary over UDP; feeds SDRangel's AIS + Map features | Linux, Windows, macOS | GPL-3.0 | 2026-08-22 | v7.27.2, 2026-08-19 | **Very much alive** |
| [AISRecWinFull](https://sites.google.com/site/feverlaysoft/) (Feverlay) | RTL-SDR USB/TCP, Airspy USB/TCP, AirspyHF, WAV files | NMEA to UDP and TCP server, NMEA log files | Windows | Closed, "Lite" free download | n/a | 2.304, 2023-05-27 | Semi-dormant, still downloadable |
| [libaisdemod](https://github.com/ibelinp/libaisdemod) | Complex baseband IQ (library API) | AIVDM sentences | C99, portable, no dependencies | MIT | 2026-08-10 | none yet | New (created 2026-08-09), unproven |

## Comparison: !AIVDM → structured data (libraries)

| Library | Language | License | Last commit | Last release | Verdict |
|---|---|---|---|---|---|
| [libais](https://github.com/schwehr/libais) | C++ with Python bindings | Apache-2.0 per [PyPI](https://pypi.org/pypi/libais/json) | 2026-06-30 | GitHub tag v0.15, 2015-06-16; PyPI 0.17, 2018-01-17 | Repo is being tended (CI, Python 3.14, a multi-fragment crash fix on 2026-06-30) but has not shipped a release in 8 years and carries 115 open issues |
| [pyais](https://github.com/M0r13n/pyais) | Pure Python | MIT | 2026-08-09 | v3.2.1, 2026-08-09 | Healthiest library in the set: frequent releases, zero open issues, handles single sentences, files, TCP and UDP sockets |
| [AisLib](https://github.com/dma-ais/AisLib) | Java | Apache-2.0 | 2026-08-13 | v2.8.7, 2026-07-17 | Alive, Danish Maritime Authority; reads serial/TCP/file, decodes and encodes |
| [gpsdecode](https://gpsd.gitlab.io/gpsd/gpsdecode.html) (gpsd) | C | BSD-2-Clause | project active 2026-08-21 | gpsd release-3.26, 2025-05-12 | Alive; ships with gpsd everywhere |
| [noaadata](https://github.com/schwehr/noaadata) | Python | not declared | 2026-08-20 | never released | Self-described research code; author says "no support unless you work for NOAA or the USCG" |
| [aisparser](https://github.com/bcl/aisparser) | C, plus SWIG Python and a Win32 DLL | BSD | 2020-11-23 | v1.10, 2019-03-17 | Effectively finished/abandoned; author dropped Windows support and prebuilt binaries |
| [aismessages](https://github.com/tbsalling/aismessages) | Java | not declared in API metadata | 2026-08-17 | 4.0.0, 2025-10-16 | Alive, zero-dependency alternative to AisLib |
| [Ais.Net](https://github.com/ais-dotnet/Ais.Net) | C# | — | 2026-02-11 | — | Alive-ish, zero-allocation .NET decoder |
| [go-ais](https://github.com/BertoldVdb/go-ais) | Go | MIT | 2024-10-23 | none | Quiet but complete decoder+encoder |
| [ais-decoder](https://github.com/aduvenhage/ais-decoder) | C++ with Python bindings | MIT | 2022-06-21 | none | Dormant |
| [ais-stream-decoder](https://registry.npmjs.org/ais-stream-decoder) | TypeScript/JS | MIT | — | 2.1.2, npm mtime 2022-04-11 | The best-maintained JS option, and it is still four years stale |

## Per-tool detail

### 1. rtl_ais (dgiardini/rtl-ais)

A single-purpose fork of `rtl_fm` that tunes both AIS channels at once, FM-demodulates them into a 48 kHz stereo stream, and runs a built-in AIS decoder on it. Source headers carry Kyle Keen's rtl_fm copyright and the GPL v2-or-later grant ([rtl_ais.c](https://raw.githubusercontent.com/dgiardini/rtl-ais/master/rtl_ais.c)); there is no top-level LICENSE file, which is why the GitHub API reports the license as `NOASSERTION` ([API](https://api.github.com/repos/dgiardini/rtl-ais)).

Status: 308 stars, not archived, last commit 2026-08-05 (`d84764bd`, merging PR #53 "Add -U option to send NMEA to extra UDP destinations", authored 2026-08-04) — so the project accepted a feature this month, but the last tagged release is still **v0.3 from 2018-08-01** ([releases](https://github.com/dgiardini/rtl-ais/releases), [commits](https://api.github.com/repos/dgiardini/rtl-ais/commits)). Issue traffic is thin and the maintainer does respond: [#48](https://github.com/dgiardini/rtl-ais/issues/48) got an answer the same day it was opened.

Output flags, verbatim from the [README](https://raw.githubusercontent.com/dgiardini/rtl-ais/master/README.md):

```
[-h host (default: 127.0.0.1)]
[-P port (default: 10110)]
[-U host:port also send NMEA to this UDP address (repeatable)]
[-T use TCP communication as tcp listener ( -h is ignored)]
[-n log NMEA sentences to console (stderr) (default off)]
```

Working example from the README (UDP to 127.0.0.1:10110, sentences echoed to the console):

```
rtl_ais -n
```

A more realistic station invocation, from the README's own Docker section:

```
docker run -d --name rtl-ais --restart=unless-stopped --log-driver=local --network=host \
  --device=/dev/bus/usb ghcr.io/bklofas/rtl-ais:latest ./rtl_ais -n -d 00000002 -h 127.0.0.1 -P 10110
```

Packaging. Debian source package `rtl-ais`, binary `rtl-ais`, section hamradio: `0.3+git20240507+ds-1` in trixie, `0.3+git20240507+ds-3` in forky/sid, on amd64, arm64, armhf, i386, riscv64 and more ([packages.debian.org](https://packages.debian.org/search?keywords=rtl-ais&searchon=names&suite=all&section=all), [sources.debian.org](https://sources.debian.org/api/src/rtl-ais/)). It is **not** in bookworm, so a Raspberry Pi OS bookworm image has no `apt install rtl-ais`. Ubuntu has it only from plucky (25.04) onward — plucky, questing, resolute, stonking — and **not in noble 24.04 LTS** ([Launchpad](https://api.launchpad.net/1.0/ubuntu/+archive/primary?ws.op=getPublishedSources&source_name=rtl-ais&exact_match=true&status=Published)). On bookworm/noble you build from source, which the README covers (`librtlsdr`, `libusb`, `libpthread`, then `make`).

RTL-SDR Blog V4: rtl_ais links `librtlsdr` dynamically, and V4 support lives in the library, not the app. Upstream osmocom rtl-sdr added "add rtl-sdr blog v4 support" on 2023-08-21 and shipped it in the 2.0.x series ([osmocom/rtl-sdr commits](https://github.com/osmocom/rtl-sdr/commits/master), [tags](https://github.com/osmocom/rtl-sdr/tags)). Debian trixie/forky/sid ship `rtl-sdr` 2.0.2; bookworm still has 0.6.0 ([sources.debian.org](https://sources.debian.org/api/src/rtl-sdr/)). The one V4 report on the tracker, [issue #48](https://github.com/dgiardini/rtl-ais/issues/48) (2024-07-20), shows the device opening fine — the log reads `0: RTLSDRBlog, Blog V4` and `Found Rafael Micro R828D tuner` — but the reporter said it "got stuck" and never followed up; the maintainer closed it 2024-09-01 for no feedback. **[UNVERIFIED]** whether rtl_ais decodes correctly on a V4 in practice; the library-level evidence says it should on librtlsdr ≥ 2.0.

Limitations relative to AIS-catcher, stated only as what rtl_ais does and does not have per its own README: it is a single fixed FM/NRZI decoder chain with no alternative decoder models, no built-in web server or JSON/HTTP output, no gain auto-tuning, no SDR support beyond RTL-SDR, and no built-in feed clients — output is UDP, a TCP listener, stderr, or raw samples. I found **no benchmark from a primary source** comparing the two, so treat "AIS-catcher decodes more" as community consensus rather than a measured fact (see the community section).

### 2. gnuais / gnuaisgui

Debian describes it exactly right: "AIS receiver which uses the discriminator output of VHF receivers" ([packages.debian.org/sid/gnuais](https://packages.debian.org/sid/gnuais)). Audio in via ALSA or PulseAudio, decoded in software. The [config example](https://github.com/rubund/gnuais/blob/master/gnuais.conf-example) shows `SoundDevice`, `SoundChannels both|mono|left|right`, a MySQL sink, a `serial_port` for NMEA export, and an `Uplink <name> json <url>` for posting jsonais to aprs.fi. Note what is missing: **there is no UDP or TCP NMEA output** — confirmed by the config parser's directive table in [src/cfg.c](https://github.com/rubund/gnuais/blob/master/src/cfg.c), which registers `uplink`, `serialport`/`serial_port`, MySQL keys and sound keys, and nothing for UDP. For a modern volunteer station that wants to fan out `!AIVDM` over UDP, that is disqualifying on its own.

Status: last release **0.3.3 on 2014-11-02**, last commit on master **2015-12-25** ("Fixed build issue with c11 standard") ([releases](https://github.com/rubund/gnuais/releases), [commits](https://api.github.com/repos/rubund/gnuais/commits)). The repo's `pushed_at` of 2023-11-14 is fork/branch churn, not new work; the most recent-looking fork, `hessu/gnuais` (2026-02-23), compares as **ahead 0, behind 0** against upstream master. Dead upstream.

It survives because Debian keeps patching it: `0.3.3-9.2` in forky/sid, `0.3.3-9.1+deb13u1` in trixie, `0.3.3-9+deb12u1` in bookworm ([sources.debian.org](https://sources.debian.org/api/src/gnuais/)), and Ubuntu carries it all the way from xenial to stonking ([Launchpad](https://api.launchpad.net/1.0/ubuntu/+archive/primary?ws.op=getPublishedSources&source_name=gnuais&exact_match=true&status=Published)). So `apt install gnuais gnuaisgui` works on essentially any Debian-family box — you are just installing 2015 code.

### 3. AISdeco2

Closed-source, binary-only, by **Sergey Serov** — the bundled `LICENSE.MIT` reads "Copyright (c) 2014 Sergey Serov and other contributors, http://xdeco.org/" and the docs are signed "/sergsero". I found **no evidence connecting it to AISHub or VesselFinder** — see "Could not verify".

Current availability: the home site **xdeco.org is down**, returning a WordPress "Error establishing a database connection" under HTTP 500 (checked 2026-08-23). Wayback shows the last HTTP 200 capture on 2021-09-17, with `-` or `500` on every capture since, including 500s on 2024-05-14 and 2026-08-01 ([CDX API](http://web.archive.org/cdx/search/cdx?url=xdeco.org&output=json&fl=timestamp,original,statuscode)). The only working source of the binaries is a third-party mirror, [github.com/xginn8/aisdeco](https://github.com/xginn8/aisdeco) (last pushed 2017-02-22), holding `aisdeco2_i386_20150415.tgz`, `aisdeco2_x86_64_20150415.tar.gz`, `aisdeco2_x86_64_20161112.tgz` and `aisdeco_rpi_20140704.tgz`. That mirror is what the Arch [AUR package `aisdeco2`](https://aur.archlinux.org/packages/aisdeco2) pulls from; the AUR entry is at version `20161112-2`, last updated 2020-05-18, and has been **flagged out-of-date since 2024-02-08** ([AUR RPC](https://aur.archlinux.org/rpc/v5/info?arg[]=aisdeco2)).

Newest build is therefore **2016-11-12**. Supported platforms per the mirror: Windows, Linux i386 and x86-64, and a 2014 Raspberry Pi build.

Usage, verbatim from the bundled `aisdeco2.txt` in `aisdeco2_x86_64_20161112.tgz`:

```
C:\>aisdeco2.exe --gain 33.8 --freq-correction 68 --freq 161975000 --freq 162025000 --net 30007 --udp 127.0.0.1:4159
```

Full option list from the same file: `--device-list`, `--device-index`, `--freq` (repeat once per channel), `--gain`, `--agc`, `--freq-correction`, `--net <port>` (its own TCP server), `--udp <address:port>`, `--no-console`, `--verbose`.

RTL-SDR Blog V4: I inspected the Linux binary. `strings ad2/aisdeco2` shows it dynamically links `librtlsdr.so.0` (alongside `libc.so.6`, `libstdc++.so.6`, `libusb` via that lib) — it does **not** statically embed a 2016 librtlsdr. Since osmocom's librtlsdr keeps `SOVERSION 0` in [src/CMakeLists.txt](https://github.com/osmocom/rtl-sdr/blob/master/src/CMakeLists.txt) and V4 support landed in 2.0.x, a 2016 aisdeco2 binary should load a V4-capable library on a modern distro, and the AUR package accordingly allows `rtl-sdr-blog` as a provider. **[UNVERIFIED]** in practice — I have no report of anyone running aisdeco2 against a V4, and the community reports below say aisdeco2 stopped working generally. On Windows the question is moot in a different way: aisdeco2 there ships/loads its own `librtlsdr.dll`, so V4 depends on dropping in the RTL-SDR Blog DLL **[UNVERIFIED]**.

Community verdict: the OpenCPN wiki's AIS software table lists AISdeco2 as Windows freeware with the note "**xDeco.org is missing in action**" ([opencpn.org wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_software:ais-software)), and the wiki.luntti.net RTL-SDR AIS page (last updated 2026-05-08) states flatly that AISdeco2 is "**not working anymore (2024)**", pointing readers to SDRangel and AIS-catcher instead ([wiki.luntti.net](https://wiki.luntti.net/index.php?title=RTL-SDR_AIS_Ship_Tracking)). Treat AISdeco2 as a historical artifact — and note that rtl-sdr.com's still widely linked tutorial has not caught up: it says "the software we most recommend is AISdeco2" ([rtl-sdr.com](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/)).

### 4. AISMon, AISRec, and Neal Arundale's tools — three different things, often conflated

**AISMon** is not Neal Arundale's. It was distributed by MarineTraffic: "AISMon is a freeware demodulator/decoder which outputs AIS-data in NMEA format. Data input may be from any installed sound card (radio discriminator output required)" ([MarineTraffic help, archived 2024-12-06](http://web.archive.org/web/20241206200035/https://help.marinetraffic.com/hc/en-us/articles/205339707-AISMon)). Windows, freeware, ~2 MB installer, last public version **AISMon 2.2.0** at `http://www.marinetraffic.com/files/AISMon_2.2.0.exe`. As of today that installer URL returns **403** and the help article redirects (301) to `https://help.kpler.com/en/articles/9552953-aismon`, which returns **404** — MarineTraffic's absorption into Kpler appears to have taken the page and the download with it. Effectively unavailable.

**Neal Arundale's tools** are `AIS Decoder` and `NMEA Router`, hosted at [arundaleais.github.io](https://arundaleais.github.io/docs/ais/ais_decoder.html). They are **not demodulators**: input is "Serial or USB from AIS receiver, UDP or TCP from network, Log File", including ShipPlotter `spnmea` logs. Output is text, CSV, HTML, XML, KML/KMZ and NMEA, to display, files, FTP or UDP. Windows (XP through 11, Linux under Wine). Downloads page: `AisDecoder_setup_3.5.149.exe` (**version 3.5.149, dated 06-Nov-17**, 5.0 MB) and `NmeaRouter_setup_1.3.66.exe` (**1.3.66, 06-Nov-17**, 3.1 MB), plus a 2009 "AIS Reception Analysis" spreadsheet ([downloads page](https://arundaleais.github.io/docs/ais/ais_decoder_v3_downloads.html)). **No price is stated anywhere on the site** — MarineTraffic's software list categorized these as freeware. The GitHub Pages site backing them, `arundaleais/arundaleais.github.io`, was last pushed **2018-07-17**. So: usable, free, frozen for nine years, and only useful downstream of a real decoder.

Do not confuse either with **AIS Dispatcher**, which is AISHub's forwarder: "an AIS data forwarding utility", input UDP/TCP/serial, output UDP to up to 12 destinations on Windows or unlimited on Linux, with CRC validation, deduplication and downsampling; Windows 1.5.1 dated 2022-06-24, Linux version with a web config UI ([aishub.net/ais-dispatcher](https://www.aishub.net/ais-dispatcher)). It does no RF decoding, and it is the only software AISHub names in its feeder instructions ([aishub.net](https://www.aishub.net/)).

**AISRec** is a fourth thing, by "Jane Feverlay" of Feverlaysoft, not Arundale. The current product is **AISRecWinFull**, "a four-channel AIS receiver for Windows", version **2.304 updated 2023-05-27**, supporting RTLSDR USB, RTLSDR TCP, AIRSPY USB, AIRSPY TCP, AIRSPYHF USB, plus WAV file input, with NMEA logging to file ([sites.google.com/site/feverlaysoft](https://sites.google.com/site/feverlaysoft/)). The [downloads page](https://sites.google.com/site/feverlaysoft/download) is live and lists Lite2.003 through Lite2.304 plus a modified `rtl_tcp`/`airspy_tcp` source drop. rtl-sdr.com's coverage says it can "output all types of AIS messages (including Class A and Class B) in NMEA formats to UDP ports" ([rtl-sdr.com](https://www.rtl-sdr.com/aisrec-windows-and-android-ais-decoder/)). An Android build was demoed — "high performance dual-channel AIS receiver for use with a single RTL-SDR dongle", with TCP server and UDP forwarding — but rtl-sdr.com noted "there does not seem to be a link available to download the software" ([rtl-sdr.com](https://www.rtl-sdr.com/aisrec-for-android-new-ais-decoder/)), and I could not find it on Google Play (three plausible package ids all 404). Windows-only in practice, three years without an update, closed source, licensing/price terms not stated on the site.

### 5. ShipPlotter (COAA)

Windows AIS receiver and plotter. Input from a sound card fed by a VHF receiver tuned to 161.975/162.025 MHz, or a serial AIS receiver, or a remote TCP/IP server; output includes an NMEA serial stream, an HTTP server and Google Earth export ([coaa.co.uk/shipplotter.htm](http://www.coaa.co.uk/shipplotter.htm)). Shareware: free download with a **21-day trial**, then **€25 for personal use, €215 professional/corporate** plus VAT in the EU (same page).

Status: current version **12.5.6.1**, supported on Windows through Win11. The page itself was last modified **2024-12-30** and the installer `shipplotter12_5_6_1.exe` has an HTTP `Last-Modified` of **2025-02-03** (5.46 MB), so binaries were still being touched 18 months ago — but the version change log page has not been updated since **2020-10-05**. There is a support group at [groups.io/g/shipplotter](https://groups.io/g/shipplotter) (page loads; member and message counts **[UNVERIFIED]** — groups.io returned HTTP 402 to my fetch). rtl-sdr.com's tutorial notes that ShipPlotter, like AISMon, "require[s] audio piping from SDR# software" rather than talking to the dongle directly ([rtl-sdr.com](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/)).

### 6. GNU Radio gr-ais

[bistromath/gr-ais](https://github.com/bistromath/gr-ais), Nick Foster's out-of-tree AIS module. 155 stars, not archived, **last commit 2020-08-13** ("Finish removing doc refs from swig/"), **never tagged a release**, no README at all. Its `CMakeLists.txt` does `find_package(Gnuradio "3.8" ...)` and still builds a `swig/` subdirectory — SWIG bindings were removed from GNU Radio in 3.9, so this will not build against GNU Radio 3.10/3.11 without porting. Ships one app, `apps/ais_rx`.

Maintained forks: none worth using. The only fork with real commits is [bkerler/gr-ais](https://github.com/bkerler/gr-ais), whose default branch is optimistically named `maint-3.10` but whose top commits are "Fix wrong compiler version" (2023-08-27) and "Bump to gr 3.8, **examples broken**" (2022-03-13). Every other fork I checked (`flegmaatikko`, `bomturbo`, and the rest of the fork list) is at or behind upstream. Treat gr-ais as dead; if you want a GNU Radio-flavored path today, SDRangel is the maintained option.

### 7. Decoding libraries

**libais** — [schwehr/libais](https://github.com/schwehr/libais), Kurt Schwehr's C++ decoder with Python bindings, the lineage behind a lot of NOAA/CCOM tooling. Apache-2.0 per [PyPI metadata](https://pypi.org/pypi/libais/json). Split personality: the repo is being maintained (last commit **2026-06-30**, including `fix(vdm): prevent crash on multi-fragment sentences` and CI moving to Python 3.14) but the last GitHub release is **v0.15 from 2015-06-16** and the last PyPI upload is **0.17 from 2018-01-17**, with **115 open issues**. Usable if you pin to git; do not expect a release cadence.

**pyais** — [M0r13n/pyais](https://github.com/M0r13n/pyais), pure Python, MIT, **v3.2.1 released 2026-08-09**, 253 stars, **0 open issues**, releases roughly monthly (3.1.0 in June, 3.2.0 and 3.2.1 in August 2026). Decodes and encodes AIVDM/AIVDO, and reads single messages, files, and **TCP/UDP sockets** directly, which makes it the natural choice for consuming a station's own UDP feed. Install: `pip install pyais` ([README](https://github.com/M0r13n/pyais#readme)).

**AisLib** — [dma-ais/AisLib](https://github.com/dma-ais/AisLib), Java, **Apache-2.0** (LICENSE.txt, "Copyright (c) 2011 Danish Maritime Authority"), **v2.8.7 released 2026-07-17** with 2.8.6 two days earlier. Reads from serial, TCP or file; handles proprietary source tags, doublet filtering, downsampling, decode and encode, and sending message types 6, 8, 12, 14. Requires Java 8 and Maven. Actively dependency-bumped by Dependabot, which is a fair description of its current activity level.

**gpsdecode** (gpsd) — batch decoder, not a demodulator: "decode GPS, RTCM or AIS streams into a readable format", reading NMEA/AIVDM/RTCM2/binary on **stdin** and writing **JSON** on stdout by default, `-n` for pseudo-NMEA0183, `-c` for pipe-separated AIS fields, `-u` for unscaled/lossless, `-t` to filter message types, `-e` to encode ([man page](https://gpsd.gitlab.io/gpsd/gpsdecode.html), [source](https://gitlab.com/gpsd/gpsd/-/blob/master/man/gpsdecode.adoc)). Its own man page ships no worked example. gpsd itself is alive: last activity on the repo **2026-08-21**, latest release **release-3.26, 2025-05-12** ([GitLab API](https://gitlab.com/api/v4/projects/gpsd%2Fgpsd/releases)). Practical use for a station is a one-liner such as `nc 127.0.0.1 10110 | gpsdecode` — **[UNVERIFIED]**, composed from documented flags rather than quoted from gpsd's docs.

**NOAA's** — the closest thing is [schwehr/noaadata](https://github.com/schwehr/noaadata), "Pure python AIS decode and encode", commits as recent as **2026-08-20**, never released, and its README warns: "This is research code. Do not expect things to work nicely and no support unless you work for NOAA or the USCG." Not a dependency for a station guide. The real NOAA-lineage library people use is libais above.

**ais-decoder JS / @aistools** — there is no `@aistools` scope on npm (registry returns 404 for `@aistools/ais-decoder`). The JS landscape is thin and stale: [`ais-stream-decoder`](https://registry.npmjs.org/ais-stream-decoder) (MIT, TypeScript, npm mtime 2022-04-11) is the most credible; [`aisdecoder`](https://registry.npmjs.org/aisdecoder) is at 0.0.2 from 2013; [`ais-decoder`](https://registry.npmjs.org/ais-decoder) on npm is actually part of the GeoGate project, not a standalone decoder; [doron2402/ais-protocol-decoding](https://github.com/doron2402/ais-protocol-decoding) (TypeScript, repo pushed 2026-02-28) is the liveliest repo. Confusingly, the GitHub project literally named [`ais-decoder`](https://github.com/aduvenhage/ais-decoder) is C++ with Python bindings, MIT, dormant since 2022-06-21.

**aisparser** — [bcl/aisparser](https://github.com/bcl/aisparser), Brian C. Lane's "AIS Parser SDK", BSD, C core plus SWIG Python bindings and a Win32 DLL, covering NMEA sentence handling, six-bit unpacking, AIVDM parsing and IMO/St. Lawrence Seaway binary messages ([README](https://github.com/bcl/aisparser/blob/master/README)). Last release **v1.10, 2019-03-17**; last substantive commit **2020-11-23**. The README itself says prebuilt Python modules and the DLL were removed and "I no longer support Windows as a development environment". Complete but finished.

### 8. What else people actually use in 2026

**SDRangel** is the answer to "what replaced aisdeco2 for GUI users". Its [AIS demodulator plugin](https://github.com/f4exb/sdrangel/blob/master/plugins/channelrx/demodais/readme.md) does GMSK/FM at 9600 baud, one instance per channel — the documented pattern is to center at 162 MHz with ≥100 kSa/s and run two demods at −25 kHz and +25 kHz. The project is extremely alive: last push **2026-08-22**, release **v7.27.2 on 2026-08-19**, GPL-3.0, 3.9k stars ([API](https://api.github.com/repos/f4exb/sdrangel)), and it supports RTL-SDR, Airspy, Airspy HF+, BladeRF, HackRF, LimeSDR, PlutoSDR, SDRplay and FunCube ([Readme](https://github.com/f4exb/sdrangel/blob/master/Readme.md)).

Getting NMEA out over UDP, quoting the plugin readme: control **7: UDP** — "When checked, received messages are forwarded to the specified UDP address (8) and port (9)"; control **8** is the UDP address, **9** the port, and control **10: UDP format** selects "either binary (which is useful for SDRangel's PERTester feature) or **NMEA** (which is useful for 3rd party applications such as OpenCPN)". So: add one AIS Demod per channel, tick UDP, set address/port, set format to NMEA. Decoded vessels also feed SDRangel's AIS feature and Map feature for a 2D/3D display. Tuning knobs worth knowing for a station: RF bandwidth defaults to 25 kHz but "more messages seem to be able to be received if this is around 16 kHz"; deviation defaults to 2.4 kHz; correlation threshold defaults to 0.6, where "real preambles correlate above 0.9" and lowering it "increases processor usage sharply".

**libaisdemod** — [ibelinp/libaisdemod](https://github.com/ibelinp/libaisdemod), "AIS receiver in C99 — complex baseband IQ in, AIVDM sentences out. MIT licensed, no dependencies", created **2026-08-09**, last pushed 2026-08-10, 19 stars, no releases. Brand new and unproven, but it is the only fresh open-source demodulator core I found this cycle, and it is the right shape for embedding in a station daemon.

**NyxScope** — [ICBizLabs/NyxScope](https://github.com/ICBizLabs/NyxScope), a Windows multi-protocol SDR receiver that "bundles the best open-source decoders behind one UI", AIS among them, v1.36.0 released 2026-08-06, created 2026-05-28, 68 stars, no license declared. Windows-only, closed licensing, wraps other people's decoders — mention it only as context.

**OpenCPN** does not demodulate anything. It consumes NMEA 0183 AIVDM from serial or network, which is exactly why its own wiki maintains a page of external AIS decoders to feed it ([opencpn.org wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_software:ais-software)). That page's current recommendation is AIS-catcher, "Lightweight command line utility delivering NMEA messages via UDP/HTTP/TCP with built-in web server".

**ShipXplorer** ships its own hardware — an "AIS Dongle", "AIS Antenna" and the "SeaRange AIS Receiver" — and runs a Share AIS Data / Claim Raspberry Pi program ([shipxplorer.com](https://www.shipxplorer.com/), [share-ais-data](https://www.shipxplorer.com/share-ais-data)). What their feeder software is, whether it accepts a generic RTL-SDR, and whether it takes NMEA from a third-party decoder is **[UNVERIFIED]** — the public pages do not say.

**Android**: the practical alternatives to AIS-catcher for Android are thin. AISRec's Android build has no published download (above). SDRangel has Android builds discussed in its wiki but I did not confirm AIS demod availability there — **[UNVERIFIED]**.

**Hardware sidestep**: for stations that would rather not run a demodulator at all, AIS-catcher's own README points at the dual-channel **dAISy-catcher** from Wegmatt, connecting over serial as USB or a Pi HAT ([shop.wegmatt.com](https://shop.wegmatt.com/products/daisy-catcher-high-performance-ais-receiver)). Any hardware receiver of this class emits `!AIVDM` on a serial port, which is then a router/library problem rather than a decoder problem.

## Is sound-card / audio-based AIS decoding still viable in 2026?

The honest answer for a volunteer-station guide: it works, nobody maintains it, and nobody recommends it any more.

What the record shows:

- Every sound-card decoder in this survey is frozen or gone. gnuais: last upstream commit **2015-12-25** ([commits](https://api.github.com/repos/rubund/gnuais/commits)). AISMon: installer 403, help article 404 after the Kpler migration. ShipPlotter is the sole exception with a 2025-dated binary, and it is €25 shareware whose change log stopped in 2020.
- The audio path is architecturally worse. rtl-sdr.com's own tutorial draws the line: AISdeco2 "directly connects to the RTL-SDR (requires no audio piping)" while AISMon and ShipPlotter "require audio piping from SDR# software" ([rtl-sdr.com](https://www.rtl-sdr.com/rtl-sdr-tutorial-cheap-ais-ship-tracking/)). Piping through a virtual audio cable adds a resampling stage, a Windows dependency, and a level-setting ritual — gnuais even logs peak levels every second and tells you to keep input "around 70-90%" and adjust your mixer ([gnuais.conf-example](https://github.com/rubund/gnuais/blob/master/gnuais.conf-example)).
- gnuais's genuine use case was tapping the **discriminator output of a physical VHF receiver** ([Debian description](https://packages.debian.org/sid/gnuais)) — cheap in 2010, when a VHF set was in the shack and an RTL-SDR was not. In 2026 the dongle is the cheap part.
- The community pages that are actually being updated point elsewhere. wiki.luntti.net, updated 2026-05-08, lists AIS-catcher and SDRangel as the working options and marks AISdeco2 "not working anymore (2024)" ([wiki.luntti.net](https://wiki.luntti.net/index.php?title=RTL-SDR_AIS_Ship_Tracking)). The OpenCPN wiki's table gives AIS-catcher the only "Active" status and flags AISdeco2 discontinued ([opencpn.org wiki](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:supplementary_software:ais-software)).
- Feed aggregators have stopped caring how you demodulate. AISHub's feeder instructions name only AIS Dispatcher and otherwise assume "many AIS receivers have built-in Ethernet capabilities and can stream data directly via UDP" ([aishub.net](https://www.aishub.net/)).

Where audio decoding still earns its place: you already own a VHF receiver with a discriminator tap and no dongle; you are on Windows and want ShipPlotter's plotting; or you are decoding recorded audio/WAV captures — AISRecWinFull explicitly accepts WAV input recorded from SDR# ([Feverlaysoft downloads](https://sites.google.com/site/feverlaysoft/download)). For a new volunteer station, no.

## Could not verify

- **AISdeco2's provenance.** The bundled license names Sergey Serov (`/sergsero`, xdeco.org). I found nothing connecting it to AISHub or VesselFinder. The premise in the research brief looks like a conflation with AISHub's separate AIS Dispatcher.
- **AISdeco2 and rtl_ais on RTL-SDR Blog V4 in practice.** The library-level facts are solid (dynamic `librtlsdr.so.0`; V4 support in osmocom librtlsdr 2.0.x; SOVERSION unchanged; Debian trixie ships 2.0.2). Actual on-air confirmation: none found for either. The one rtl_ais V4 report ([#48](https://github.com/dgiardini/rtl-ais/issues/48)) is inconclusive and was closed for no feedback.
- **Reddit and forum sentiment.** Reddit's API and HTML both return 403 to this environment, and DuckDuckGo/Mojeek/searx/Bing all returned CAPTCHAs, block pages or junk after the web-search budget was exhausted. Community verdicts above therefore rest on maintained wiki pages (OpenCPN, wiki.luntti.net), distro packaging state, AUR out-of-date flags and issue trackers — **no MarineTraffic-forum, AISHub-forum, OpenCPN-forum or r/RTLSDR thread was read directly**. Anyone finishing the guide should spot-check the sound-card-is-obsolete framing against a live forum thread.
- **AISMon's current download.** `marinetraffic.com/files/AISMon_2.2.0.exe` returns 403 to curl with both default and browser user agents; it may still work from a browser session. The help article itself is gone.
- **groups.io/g/shipplotter activity level.** Page returns 200 to curl but 402 to the fetch tool, so member/message counts and last-post date are unknown.
- **AISRec for Android.** No Play Store listing found under three plausible package ids; rtl-sdr.com said there was no download link at the time of writing. Assume unavailable.
- **ShipXplorer's feeder software.** Public pages do not name it or state RTL-SDR compatibility.
- **SDRangel on Android for AIS.** Not confirmed.
- **Neal Arundale's licensing/price.** No terms stated on his site; "freeware" comes from MarineTraffic's software listing, not from the author.
- **A measured rtl_ais vs AIS-catcher benchmark.** None found from a primary source. AIS-catcher's current README contains no comparison against rtl_ais, aisdeco2 or gnuais.
