# Forwarders and multiplexers for a volunteer AIS station

Research notes for a public guide on running a volunteer AIS receiving station. Scope: software that takes NMEA 0183 `!AIVDM` sentences from a serial/USB/TCP source and forwards them over the network to an aggregator.

All dates verified 2026-08-22 via the GitHub REST API, npm registry API, or the vendor's own site. Every claim below carries a source URL. Anything I could not confirm is called out with **UNVERIFIED**.

## The two aiscast ingest paths

| Path | Endpoint | Auth | Notes |
|---|---|---|---|
| Plain UDP NMEA | `ais.openwaters.io:10110` | none | Source: [`docs/API.md`](../../../docs/API.md) — "UDP `:10110` \| none \| raw NMEA ingest" |
| HTTP POST | `https://ais.openwaters.io/v1/receive` | token (`personal`/`feeder`/`peer`/`admin`) | AIS-catcher's `jsonaiscatcher` envelope or plain newline-separated NMEA; gzip accepted; 1 MB limit; 600 posts/min. Source: `docs/API.md` |

One implementation detail that shapes the advice below: aiscast's UDP listener splits **each datagram** on `\n` (`server/ingest.go:122`, `for _, line := range strings.Split(string(buf[:n]), "\n")`). So several sentences in one datagram are fine, but a sentence split *across* two datagrams is corrupted. Any forwarder you pick must emit whole lines per datagram. This is why the `socat` recipe below uses canonical (line-buffered) mode rather than raw mode.

---

## Summary table

| Tool | Platform | License | Last release | Last commit | Remote UDP out? | HTTP POST out? | Verdict for a volunteer station |
|---|---|---|---|---|---|---|---|
| [AIS-catcher](https://github.com/jvde-github/AIS-catcher) | Linux/RPi/Win/macOS/Android/Docker | GPL-3.0 | v0.70, 2026-06-19 | 2026-08-22 | yes (`-u host port`) | yes (`-H url`) | **Best default**, even with a hardware receiver — `-e` reads a serial AIS receiver directly |
| [docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) | Docker, amd64/arm64/armhf | GPL-3.0 | no tagged releases | 2026-08-22 | yes (`UDP_FEEDS`) | yes (`AISCATCHER_EXTRA_OPTIONS -H`) | **Best if you feed several aggregators**; wraps AIS-catcher |
| [socat](http://www.dest-unreach.org/socat/) | Linux/macOS/BSD | GPL-2.0 | 1.8.1.3, 2026-06-26 | — | yes (`UDP-SENDTO`) | no | Fine for a one-line serial→UDP bridge under systemd |
| [ser2net](https://github.com/cminyard/ser2net) | Linux | GPL-2.0 | v4.6.8, 2026-08-03 | 2026-08-14 | yes, via `remaddr: "!host,port"` connect-back (untested) | no | Designed as a serial→TCP server for the LAN; outbound works but socat is simpler |
| [kplex](https://github.com/stripydog/kplex) | Linux/macOS/BSD | GPLv3 | v1.4, 2019-01-06 | master 2020-08-27, develop 2024-02-25 | yes | no | Works, but effectively unmaintained; no Debian package; kplex.net is offline |
| [Signal K server](https://github.com/SignalK/signalk-server) | Node.js ≥22 | Apache-2.0 | 2.31.1, 2026-08-16 | 2026-08-22 | yes, via `ais-forwarder` plugin (or hand-edited `outEvent`) | no (needs a plugin) | Good if you already run Signal K on a boat |
| [`ais-forwarder`](https://www.npmjs.com/package/ais-forwarder) plugin | Signal K | Apache-2.0 | 0.4.1, 2023-03-20 | 2023-03-20 | yes, AIVDM/AIVDO-filtered | no | Right job, stale package, ~138 lines of `dgram` |
| [OpenCPN](https://github.com/OpenCPN/OpenCPN) | Win/macOS/Linux desktop | GPL-2.0 | 5.14.0, 2026-04-09 | 2026-08-20 | **yes** — it really does relay AIVDM | no | GUI-only, no daemon mode — poor fit for an unattended station |
| AIS Dispatcher | Linux x86_64/ARM, Windows | closed freeware, **no stated licence** | Linux 2.3, 2026-04-01; Win 1.5.1, 2022-06-24 | — | yes, unlimited arbitrary destinations | no | Capable, web UI on :8080, but unlicensed and self-updating |
| `sxfeeder` (ShipXplorer) | armhf/arm64 Debian | closed, licence undefined | 1:1.0.1, deb dated 2024-06-20 | — | no | no | ShipXplorer only; not a general forwarder |
| [Node-RED](https://nodered.org) | Node.js | Apache-2.0 | 5.0.4, 2026-07-30 | — | yes (`udp out` core node) | yes (`http request`) | Overkill alone; good if already running |
| [AvNav](https://github.com/wellenvogel/avnav) | Python 3, RPi/Debian/Android | MIT | GitHub releases stale; ships from wellenvogel.net | 2026-08-21 | yes (`AVNUdpWriter` `host`/`port`) | no | Underrated — active, filterable, has a UI |
| [gpsd](https://gpsd.gitlab.io/gpsd/) | Linux/BSD | BSD | release-3.27.5, 2026-01-14 | — | no — but `gpspipe -r \| socat` works | no | Only if you already run it for a GPS |

---

## 1. kplex

**What it does.** The classic Unix NMEA 0183 multiplexer: it reads sentences from any number of "interfaces" (serial, file/FIFO, TCP, UDP, pty, Navico GoFree), merges them into one central queue, and writes them out to any number of other interfaces, with per-interface allow/deny/rate-limit filters. Written in C, no dependencies. Source: [README, "Interface Types"](https://github.com/stripydog/kplex/blob/master/README).

**Platform.** Linux, macOS, FreeBSD, OpenWrt. Build is plain `make`; `make install` puts the single `kplex` binary in `/usr/bin` on Linux. Source: [README, "Installation From Source"](https://github.com/stripydog/kplex/blob/master/README).

**License.** GPLv3 — "Copyright Keith Young 2012-2018 … Details of the GPLv3 under which this software is distributed can be found in [COPYING]". Source: [README lines 1-5](https://github.com/stripydog/kplex/blob/master/README). GitHub reports the license as "Other/NOASSERTION" because there is no `LICENSE` file, only `COPYING`.

### Maintenance status — effectively dormant

| Fact | Value | Source |
|---|---|---|
| Last tagged version | **v1.4**, commit `715b18ca` dated **2019-01-06** | `GET /repos/stripydog/kplex/tags` + `GET /repos/stripydog/kplex/commits/715b18ca` |
| GitHub Releases | **none** (`/releases` returns `[]`) — only git tags | `GET /repos/stripydog/kplex/releases` |
| Last commit on `master` | **2020-08-27** (`2433ceab`, "Create CONTRIBUTING") | `GET /repos/stripydog/kplex/commits?sha=master` |
| Last commit on `develop` | **2024-02-25** (`7d97e632`, "Merge pull request #62 … serial-921600") | `GET /repos/stripydog/kplex/commits?sha=develop` |
| Open issues | 21, including ones from 2022, 2024 and 2025 with no maintainer reply | `GET /repos/stripydog/kplex/issues` |
| Oldest open PR | 2020-10-12, **"Add files needed to make Debian package"** — still unmerged | `GET /repos/stripydog/kplex/pulls?state=open` |
| `base_version` in repo | `1.4` | [base_version](https://github.com/stripydog/kplex/blob/master/base_version) |

So: your suspicion was right, and it is worse than "last release ~2018". The last *release* is January 2019; `master` has not moved since August 2020; the only post-2020 activity is a single merged PR on the `develop` branch in February 2024 (raising the maximum serial baud rate to 921600). Six PRs sit open, the oldest from September 2020.

**kplex.net is gone.** `kplex.net` and `www.kplex.net` have **no DNS A record** as of 2026-08-22 (`dig +short kplex.net A` returns nothing; `curl` returns exit code with HTTP `000`). The project's own website, which is where the `.deb` and macOS packages were distributed, is offline. Any guide should link to GitHub, not kplex.net.

**Is there a Debian package?** **No.**
- Not in Debian: `https://sources.debian.org/api/search/kplex/` returns `{"results":{"exact":null,"other":[]}}`.
- Not in Ubuntu: the Launchpad API (`getPublishedSources&source_name=kplex&exact_match=true`) returns `{"total_size": 0, "entries": []}`.
- The repo's README refers to installing "From .deb file", but [PR #46](https://github.com/stripydog/kplex/pull/46) (2020-10-12) that would add packaging files was never merged. Those `.deb` files were hosted on kplex.net, which is dead.
- OpenPlotter once shipped a kplex module ([e-sailing/openplotter-kplex](https://github.com/e-sailing/openplotter-kplex), last push 2020-05-29), now abandoned.

You must build kplex from source.

### Config file syntax

Config lives at (first match wins): `$KPLEXCONF`, `~/Library/Preferences/kplex.conf` (macOS), `~/.kplex.conf`, `/etc/kplex.conf`. Sections are the interface type in square brackets; one `key=value` per line; `#` starts a comment. A special `[global]` section holds non-interface options. Source: [README, "Invocation" and "Configuration File syntax"](https://github.com/stripydog/kplex/blob/master/README).

**Serial in → UDP out to a remote host** (`/etc/kplex.conf`):

```ini
# Read AIS from a dAISy HAT / USB receiver and unicast it to aiscast.
[global]
mode=background          # detach and run as a daemon
checksum=yes             # drop sentences with a bad checksum before forwarding

[serial]
direction=in
filename=/dev/ttyAMA0    # /dev/ttyACM0 for a USB dAISy, /dev/serial0 on a Pi
baud=38400
name=daisy

[udp]
direction=out
address=ais.openwaters.io
port=10110
```

Option names verified against the README: `filename` and `baud` are the serial interface options ("You must minimally specify a device name for a serial interface"); `address`, `port`, `device`, `type` and `coalesce` are the UDP interface options; `direction` is `in`/`out`/`both` and is common to all interfaces; `name=` labels an interface for use in filters. `port` defaults to the `nmea-0183` service, falling back to **10110** — the IANA port — so `port=10110` is technically redundant but worth writing explicitly. Sources: [README "Serial Interfaces"](https://github.com/stripydog/kplex/blob/master/README), [README "UDP Interfaces"](https://github.com/stripydog/kplex/blob/master/README).

**Fan-out to several destinations** — just add more output interfaces; every input feeds the shared queue and every output drains it:

```ini
[global]
mode=background
checksum=yes

[serial]
direction=in
filename=/dev/ttyACM0
baud=38400
name=daisy

# aiscast
[udp]
direction=out
address=ais.openwaters.io
port=10110

# a second aggregator on UDP
[udp]
direction=out
address=data.aishub.net
port=1234

# a local TCP server so OpenCPN / Signal K on the LAN can connect
[tcp]
direction=out
mode=server
port=10110

# a filtered log: one RMC per hour, nothing else
[file]
direction=out
filename=/var/log/nmea.log
append=yes
ofilter=~GPRMC/3600:-all
```

The equivalent one-liner form (interfaces can also be given on the command line, and are *combined* with those in the config file):

```bash
kplex serial:direction=in,filename=/dev/ttyACM0,baud=38400 \
      udp:direction=out,address=ais.openwaters.io,port=10110 \
      tcp:direction=out,mode=server,port=10110
```

Source for the command-line form and the fan-out pattern: [README, "Example usage"](https://github.com/stripydog/kplex/blob/master/README).

**Filtering to AIS only.** kplex filters on the 5 characters after `!`/`$` (talker + sentence type), with `+` allow, `-` deny, `~` rate-limit, rules separated by `:`, and an implicit allow if nothing matches — so you must end with `-all`:

```ini
[udp]
direction=out
address=ais.openwaters.io
port=10110
ofilter=+AIVDM:+AIVDO:-all
```

Source: [README, "Filtering"](https://github.com/stripydog/kplex/blob/master/README) — "To *only* allow AIS, GPS and Depth below transducer sentences, you need to deny what is not explicitly allowed by adding `-all` to the end of the filter specification".

**`coalesce`.** By default kplex sends one sentence per UDP datagram. `coalesce=yes` buffers the parts of a multi-part AIS message (up to 512 bytes) and sends them together. Either is safe for aiscast, which splits datagrams on `\n`. Source: [README, "UDP Interfaces"](https://github.com/stripydog/kplex/blob/master/README).

**Running it.** The repo ships `kplex.init` (a SysV init script for Debian) and `kplex.service` (a systemd unit). Both expect `/usr/bin/kplex` and `/etc/kplex.conf`. Run it as an unprivileged user in the `dialout` group, not root — the README says so explicitly: "kplex.init will run kplex as root, but this is neither required nor recommended." Sources: [kplex.init](https://github.com/stripydog/kplex/blob/master/kplex.init), [kplex.service](https://github.com/stripydog/kplex/blob/master/kplex.service), [README](https://github.com/stripydog/kplex/blob/master/README).

### Community verdict

kplex still works and is still recommended in older boat-computer guides, but the evidence of abandonment is unambiguous: dead website, no release in seven years, and open unanswered issues going back a decade. Three of them matter specifically to a feeding station:

- [#30, "Permanently drops writing to UDP socket on WLAN reconnnect"](https://github.com/stripydog/kplex/issues/30) — open since **2019-08-27**. A station on Wi-Fi silently stops feeding after a network blip and never recovers. This is the single most important thing to know before recommending kplex for an unattended station.
- [#63, "TCP sockets left after closed connection from marinetraffic.com"](https://github.com/stripydog/kplex/issues/63) — open since 2025-01-01, and *specifically* about feeding an aggregator.
- [#64, "TCP input does unwanted aggeration / buffering"](https://github.com/stripydog/kplex/issues/64) — open since 2025-07-13.

And on packaging: [PR #46, "Add files needed to make Debian package"](https://github.com/stripydog/kplex/pull/46) has sat open since 2020-10-12, alongside [#55 "Docker container version of kplex"](https://github.com/stripydog/kplex/pull/55) (2021-06-22) and four others. Nobody is merging anything.

**What people use instead:** Signal K server (for a boat that already has a Signal K instance), AIS-catcher (for a shore station — it reads a serial AIS receiver with `-e` and forwards with `-u`/`-H`, so it replaces both the receiver software and the multiplexer), or plain `socat` under systemd for the simplest possible bridge. For a *guide aimed at volunteer shore stations*, recommend AIS-catcher; mention kplex only for readers who already run it or who need genuine multi-source NMEA merging with filters.

---

## 2. socat and ser2net

### socat

**What it does.** A general bidirectional byte-stream relay. Not AIS-aware at all — it copies bytes from one address to another, which is exactly enough for serial → UDP.

**Current version: 1.8.1.3, released 2026-06-26.** Source: [socat download index](http://www.dest-unreach.org/socat/download/) (file `socat-1.8.1.3.tar.gz`, dated 2026-06-26; 1.8.1.2 was 2026-06-25, 1.8.1.1 was 2026-02-12). License GPL-2.0, by Gerhard Rieger. Packaged as `socat` in Debian/Ubuntu/Raspberry Pi OS, Homebrew, and essentially every distro.

**The recommended invocation:**

```bash
socat -d -d \
  /dev/ttyACM0,b38400,cs8,parenb=0,cstopb=0,clocal=1,icanon=1,echo=0 \
  UDP-SENDTO:ais.openwaters.io:10110
```

Why each piece, from the [socat man page](http://www.dest-unreach.org/socat/doc/socat.html):

- `b38400` — "Sets the serial line speed to 19200 baud. Some other rates are possible; use something like `socat -hh | grep ' b[1-9]'` to find all speeds supported by your implementation." Use 38400 for AIS (dAISy HAT, dAISy USB, most Quark-elec and Vesper NMEA 0183 AIS outputs); 4800 for a plain non-AIS NMEA talker.
- `icanon=1` — "Sets or clears canonical mode, enabling line buffering and some special characters." **This is the important one.** In canonical mode each `read()` returns exactly one line, so socat emits **one NMEA sentence per UDP datagram**. Without it socat reads up to the transfer block size (`-b`, "Default is 8192 bytes") whenever bytes are available, and a sentence can be split across two datagrams — which aiscast's line splitter (`strings.Split(datagram, "\n")`, `server/ingest.go:122`) would corrupt.
- `echo=0` — "Enables or disables local echo." Off, so nothing is written back down the serial line.
- `clocal=1` — ignore modem control lines, so a receiver that does not assert DCD/DSR still works.
- `UDP-SENDTO:<host>:<port>` — "Communicates with the specified peer socket … It sends packets to and receives packets from that peer socket only. This address effectively implements a datagram client." This is the correct address type for unicast to one aggregator.
- `-d -d` — log notices and errors to stderr, so `journalctl -u` shows something useful.

**About `raw`:** the man page says `raw` "Sets raw mode, thus passing input and output almost unprocessed. **This option is obsolete, use option `rawer` or `cfmakeraw` instead.**" The commonly copy-pasted `socat /dev/ttyACM0,b38400,raw,echo=0 UDP-DATAGRAM:host:10110` therefore uses a deprecated option **and** turns off line buffering. It usually works because AIS bursts are short and arrive as whole lines, but it can split sentences under load. Prefer `icanon=1`.

**`UDP-SENDTO` vs `UDP-DATAGRAM`:** `UDP-DATAGRAM:<address>:<port>` "Sends outgoing data to the specified address which may in particular be a broadcast or multicast address" and accepts return traffic from anyone matching its `range`/`tcpwrap` options. Use `UDP-DATAGRAM` (with the `broadcast` option) for LAN broadcast; use `UDP-SENDTO` for a single remote aggregator. Source: [socat man page](http://www.dest-unreach.org/socat/doc/socat.html).

**Fan-out to two aggregators.** socat is strictly point-to-point; there is no built-in tee to two UDP destinations. Two workable patterns:

```bash
# a) two independent units, each reading a different source — NOT possible from one serial port
# b) one socat reads serial to stdout, tee splits, two socats send
socat /dev/ttyACM0,b38400,cs8,clocal=1,icanon=1,echo=0 - \
  | tee >(socat - UDP-SENDTO:ais.openwaters.io:10110) \
  | socat - UDP-SENDTO:data.aishub.net:1234
```

**UNVERIFIED:** I have not tested the `tee >(...)` pipeline end-to-end; it is a standard bash process-substitution idiom rather than something documented by socat. If you need genuine fan-out, use AIS-catcher (`-u` repeated) or kplex instead — both are built for it.

**Pitfalls, all real:**

| Pitfall | Effect | Fix |
|---|---|---|
| No line buffering by default | sentences split across datagrams | `icanon=1` (above) |
| socat exits when the device disappears | forwarding stops silently on a USB replug | `systemd` unit with `Restart=always` |
| DNS is resolved once, at startup | if the aggregator's IP changes, packets go to the old address forever | restart the unit periodically, or use `RuntimeMaxSec=` / a `systemd` timer. **UNVERIFIED**: socat's man page does not document re-resolution behaviour for `UDP-SENDTO`; the conservative reading is that it resolves at address-open time. |
| Fixed device name | `/dev/ttyACM0` becomes `/dev/ttyACM1` after a replug | a `udev` rule giving a stable `/dev/serial/by-id/...` symlink |
| No buffering across outages | UDP is fire-and-forget; anything sent while the aggregator is down is lost | accept it, or use AIS-catcher, which can also feed a local web viewer and multiple outputs |

**systemd unit** (`/etc/systemd/system/ais-udp-forward.service`):

```ini
[Unit]
Description=Forward AIS NMEA from serial to aiscast over UDP
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ais
SupplementaryGroups=dialout
ExecStart=/usr/bin/socat -d -d \
  /dev/serial/by-id/usb-Wegmatt_LLC_dAISy_2_plus-if00,b38400,cs8,parenb=0,cstopb=0,clocal=1,icanon=1,echo=0 \
  UDP-SENDTO:ais.openwaters.io:10110
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

`Restart=always` plus `RestartSec=5` is the fix for the unplug case: socat exits, systemd brings it back five seconds later, and it reopens the device (and re-resolves DNS) each time.

### ser2net

**What it does.** Exposes a serial port as a **network server** — a TCP (or telnet/RFC2217, or UDP) listener that clients connect *to*. Source: [cminyard/ser2net README](https://github.com/cminyard/ser2net).

**Maintenance: healthy.** Last release **v4.6.8 on 2026-08-03**; last commit **2026-08-14**. License GPL-2.0. Source: `GET /repos/cminyard/ser2net/releases` and `/commits`. Debian/Ubuntu/Raspberry Pi OS package name: `ser2net`.

**The important caveat for this guide:** ser2net is a *server*. It waits for the aggregator to connect to you. Aggregators do not do that — they expect you to push data to them. So ser2net is the wrong shape for feeding aiscast directly. Where it *is* useful: put ser2net on the Pi that has the receiver, and point AIS-catcher (`-t host port`) or Signal K at it over the LAN.

ser2net v4 config is YAML at `/etc/ser2net.yaml`:

```yaml
%YAML 1.1
---
connection: &ais
    accepter: tcp,10110
    connector: serialdev,/dev/ttyACM0,38400n81,local
    options:
      kickolduser: true
```

`accepter` is what the network side listens on and `connector` is the serial device; the gensio address syntax means `accepter: udp,10110` also works for a UDP listener. Source: [ser2net README and `ser2net.yaml(5)`](https://github.com/cminyard/ser2net).

**It can push outbound after all — via a "connect back" remote address.** From [`ser2net.yaml(5)`](https://github.com/cminyard/ser2net/blob/master/ser2net.yaml.5):

> `remaddr: [!]<addr>[;[!]<addr>[;...]]` — "specifies the allowed remote connections… If a "!" is given at the beginning of the address, the address is a "connect back" address. If a connect back address is specified, one of the network connections (see max-connections) is reserved for that address. **If data comes in on the device, ser2net will attempt to connect to the address. This works on TCP and UDP.**"

and, in the man page's own UDP section:

> "If you set a remote address (remaddr) with a non-zero port and a connect back port…, ser2net will take one of the connections and assign it to that port permanently. This is called a fixed remote address. All the traffic from the device will go to that port."

So this should work:

```yaml
%YAML 1.1
---
connection: &ais-out
    accepter: udp,10110
    connector: serialdev,/dev/ttyACM0,38400n81,local
    options:
      remaddr: "!ais.openwaters.io,10110"
      max-connections: 2
```

**UNVERIFIED:** I did not run this. The `remaddr` connect-back semantics are documented, but no ser2net example in the README or man page shows serial → outbound-UDP-to-an-internet-host specifically, and the address quoting above is my reading of "a standard address in the form (see "network port" above)". For a guide, prefer `socat` for outbound UDP — it is one obvious line — and use ser2net when you want the LAN-server shape it was designed for.

---

## 3. Signal K server

**Current version: 2.31.1, published 2026-08-16.** Requires **Node.js >= 22** (`engines.node: ">=22"` in [package.json](https://github.com/SignalK/signalk-server/blob/master/package.json)). Apache-2.0. Sources: [registry.npmjs.org/signalk-server](https://registry.npmjs.org/signalk-server), `GET /repos/SignalK/signalk-server`. Recent cadence: 2.28.0 (2026-06-16), 2.29.0 (2026-06-30), 2.30.0 (2026-07-02), 2.31.0 (2026-08-14), 2.31.1 (2026-08-16). Very actively developed — last commit 2026-08-22.

### (a) Adding an NMEA 0183 serial provider

Server → Data Connections → Add. The "Data Type" dropdown offers exactly five values — `NMEA2000`, `NMEA0183`, `SignalK`, `Seatalk`, `FileStream` — hard-coded in [`packages/server-admin-ui/src/views/ServerConfig/BasicProvider.tsx`](https://github.com/SignalK/signalk-server/blob/master/packages/server-admin-ui/src/views/ServerConfig/BasicProvider.tsx) and echoed in [`docs/setup/configuration.md`](https://github.com/SignalK/signalk-server/blob/master/docs/setup/configuration.md).

Under `NMEA0183` the transport sub-types are `serial`, `tcp` (TCP client), `tcpserver` (TCP server on 10110), `udp`, and `gpsd`.

What the UI writes into `~/.signalk/settings.json`:

```json
{
  "pipedProviders": [
    {
      "id": "ais-serial",
      "enabled": true,
      "pipeElements": [
        {
          "type": "providers/simple",
          "options": {
            "logging": false,
            "type": "NMEA0183",
            "subOptions": {
              "type": "serial",
              "device": "/dev/ttyACM0",
              "baudrate": 38400,
              "validateChecksum": true,
              "appendChecksum": false,
              "removeNulls": false,
              "suppress0183event": false,
              "sentenceEvent": "",
              "ignoredSentences": [],
              "toStdout": []
            }
          }
        }
      ]
    }
  ]
}
```

Shape verified in [`src/interfaces/providers.ts`](https://github.com/SignalK/signalk-server/blob/master/src/interfaces/providers.ts) — `updateProvider` creates `{ type: 'providers/simple', options: {} }`, then `applyProviderSettings` sets `options.logging`, `options.type` and `Object.assign(options.subOptions, source.options)`. Note `options.type` is the *data type* and `subOptions.type` is the *transport*.

**Two UI field names that are easy to confuse:**

| UI label | settings key | meaning |
|---|---|---|
| **Input Event** | `subOptions.sentenceEvent` | an *extra* event emitted in addition to `nmea0183` for each incoming sentence |
| **Output Events** | `subOptions.toStdout` | list of events whose payloads get **written out** to this connection |

Only `sentenceEvent` is documented in [`docs/setup/configuration.md`](https://github.com/SignalK/signalk-server/blob/master/docs/setup/configuration.md); `toStdout` appears only in its own `helpText` ("Events that should be written as output to this connection. Example: nmea0183,nmea0183out").

**The event that carries AIS.** `nmea0183` is emitted for every raw incoming NMEA 0183 line, verbatim, before parsing — so it carries `!AIVDM` and `!AIVDO` unchanged, including multi-part fragments. Source: [`packages/streams/src/nmea0183-signalk.ts`](https://github.com/SignalK/signalk-server/blob/master/packages/streams/src/nmea0183-signalk.ts) (`this.sentenceEvents = options.suppress0183event ? [] : ['nmea0183']`, then `this.app.emit(eventName, sentence)`). `nmea0183out` is the separate bus for *generated* sentences.

### (c) Can Signal K do plain UDP output natively, without a plugin?

**Yes — but only by hand-editing `settings.json`. The admin UI does not expose the necessary field.**

The UDP stream supports output through an `outEvent` option ([`packages/streams/src/udp.ts`](https://github.com/SignalK/signalk-server/blob/master/packages/streams/src/udp.ts)):

```ts
if (this.options.outEvent && this.options.port !== undefined) {
  this.options.app.on(this.options.outEvent, (d: string) => {
    socket.send(d, 0, d.length, this.options.port,
                this.options.host ?? '255.255.255.255')
  })
}
```

But reading `function NMEA0183()` in `BasicProvider.tsx`, the "Output Events" field (`StdOutInput`) is rendered only for `serial`, `tcp` and `gpsd` — **not** for `udp`, which gets only a port field. So the UDP-out path exists in code and is used internally (`simple.ts` passes `outEvent: 'ydwg02-out'` to `Udp` for the `ydwg02-udp-canboatjs` NMEA 2000 gateway), but it is not reachable from the GUI for NMEA 0183.

Hand-written provider that does serial-in-elsewhere → UDP-out-to-aiscast:

```json
{
  "id": "aiscast-udp-out",
  "enabled": true,
  "pipeElements": [
    { "type": "providers/udp",
      "options": {
        "port": 10110,
        "host": "ais.openwaters.io",
        "outEvent": "nmea0183"
      } }
  ]
}
```

**Four caveats, all real and all worth writing into a guide:**

1. **`udp.ts` binds the same port it sends to.** `socket.bind(this.options.port, ...)` runs unconditionally in `pipe()`, so sending to remote 10110 also binds local UDP/10110.
2. **No line terminator is appended.** `socket.send(d, ...)` sends the sentence raw. aiscast splits datagrams on `\n` and `TrimSpace`s each line (`server/pipeline.go:133`), so a single terminator-less sentence per datagram still works — but this differs from `toStdout`, which appends `\r\n`.
3. **`outEvent` takes a single event name** (a string), unlike `toStdout` which accepts an array.
4. **No filtering.** Listening on `nmea0183` forwards every sentence from every NMEA 0183 connection, not just AIS. `ignoredSentences` is an *input* filter and its 3-char-talker regex does not cleanly express "AIVDM only".

**UNVERIFIED:** this exact serial-in → udp-out configuration is derived from reading `udp.ts`, `tcp.ts`, `simple.ts` and `providers.ts`; no documentation or issue in the repo demonstrates it, and it was not run. Also unverified: whether these hand-added keys survive a round-trip through the admin UI (`applyProviderSettings` uses `Object.assign`, a merge, and `ProviderOptions` has an `[key: string]: unknown` index signature, so they *should*).

**TCP out, by contrast, works from the GUI today.** Add a second NMEA0183 connection of sub-type `tcp` (TCP Client) pointed at the remote host and put `nmea0183` in its **Output Events** field. `tcp.ts` supports `toStdout` and appends `\r\n`. Set `suppress0183event: true` on that connection so anything the far end says back is not re-emitted as `nmea0183` (which would loop). aiscast does not currently offer a TCP ingest port, so this is only useful for other aggregators.

**There is no built-in HTTP output.** No stream element, interface, or provider in the server posts NMEA over HTTP. The only HTTP surfaces are the REST/WS APIs. Feeding `/v1/receive` from Signal K requires a plugin — which is what this repo's own `signalk-plugin/` is for.

**Also built-in and worth mentioning:** `src/interfaces/nmea-tcp.ts` runs a TCP *server* on port 10110 (`NMEA0183PORT` env override) relaying both `nmea0183` and `nmea0183out` to connected clients. Toggle at Server → Settings → Interfaces → nmea-tcp. Documented in [`docs/guides/navdataserver/navdataserver.md`](https://github.com/SignalK/signalk-server/blob/master/docs/guides/navdataserver/navdataserver.md). It is a listener, so it does not get you through NAT to a cloud endpoint.

### (b) The `ais-forwarder` plugin

| | |
|---|---|
| npm | [ais-forwarder](https://www.npmjs.com/package/ais-forwarder) |
| Latest version | **0.4.1** |
| Last publish | **2023-03-20T09:45:25Z** — dormant for ~3.5 years |
| Last commit | **2023-03-20** (`836aa931`, "Bump version.") |
| License | Apache-2.0 (Harri Kapanen; contributors Scott Bender, Ilker Temir) |
| Dependencies | **none** — only Node stdlib `dgram` and `os` |
| `engines` | **not declared** |

Sources: [registry.npmjs.org/ais-forwarder](https://registry.npmjs.org/ais-forwarder), `GET /repos/hkapanen/ais-forwarder`, [index.js](https://github.com/hkapanen/ais-forwarder/blob/master/index.js).

**Protocol: UDP only** (`dgram.createSocket('udp4')`). No TCP, no HTTP.

**AIVDM and AIVDO, each independently toggleable**, from `index.js`:

```js
if ((message.match(/!AIVDM/) && options.aivdm) ||
    (message.match(/!AIVDO/) && options.aivdo)) {
```

Both default to `false`, so a fresh install forwards nothing until you tick a box. There is also `convertaivdo`, which rewrites an `!AIVDO` talker to `AIVDM` and recomputes the XOR checksum, for aggregators that reject own-vessel sentences. Every message gets `message = message + '\n'` appended before sending — which is exactly what aiscast's line splitter wants.

**Destinations are freely configurable and there can be several.** Config fields, from the plugin's `schema()`:

| Field | Type | Default | Title |
|---|---|---|---|
| `endpoints` | array | — | "UDP endpoints to send updates" |
| `endpoints[].ipaddress` | string | `"0.0.0.0"` | "UDP endpoint IP address" |
| `endpoints[].port` | number | `12345` | "Port" |
| `event` | string | `"nmea0183"` | "NMEA 0183 Events" (comma-separated) |
| `aivdo` | boolean | `false` | "Forward AIVDO sentences (own vessel)" |
| `aivdm` | boolean | `false` | "Forward AIVDM sentences (other vessels)" |
| `convertaivdo` | boolean | `false` | "Convert AIVDO to AIVDM sentences (For endpoints not supporting AIVDO)" |

For aiscast: `ipaddress: ais.openwaters.io`, `port: 10110`, `aivdm: true`, `aivdo: true` (aiscast accepts both), `event: nmea0183`.

**Verdict.** Still the only plugin that does exactly the right job — UDP, AIS-filtered so you send only AIS rather than the whole NMEA firehose, newline-terminated, multiple endpoints, zero dependencies. Also three and a half years stale with no declared `engines`, so it is untested against Node 22 and server 2.31. It is ~138 lines of `dgram`, so the staleness is low-risk in practice, but say so in the guide.

### (d) `@signalk/signalk-to-nmea0183` — does not do AIS

Version **1.18.3**, published **2026-08-18**; pre-installed since server 2.2.0. It converts Signal K deltas *back* into NMEA 0183 (APB, BWC, RMB, XTE, GLL, HDG/HDM/HDT, ROT, RMC, VHW, VTG, VLW, DBT/DPT, MWD, MWV, RSA, GGA, ZDA and others) and emits them on `nmea0183out`. Its README says outright: "For **NMEA 2000 AIS → NMEA 0183** conversion use `signalk-n2kais-to-nmea0183` instead. This plugin does not emit AIS sentences." Sources: [registry.npmjs.org/@signalk/signalk-to-nmea0183](https://registry.npmjs.org/@signalk/signalk-to-nmea0183), [README](https://github.com/SignalK/signalk-to-nmea0183/blob/master/README.md).

### (e) Other Signal K plugins that push to a remote endpoint

| Plugin | Latest | Last publish | Notes |
|---|---|---|---|
| [`@signalk/udp-nmea-plugin`](https://www.npmjs.com/package/@signalk/udp-nmea-plugin) | 2.0.0 | 2023-09-12 | Official "UDP NMEA0183 Sender". Array of `destinations` with `ipaddress`/`broadcastAddress`/`port`, booleans `nmea0183`/`nmea0183out`, `additionalEvents`, `lineDelimiter` (`None`\|`LF`\|`CRLF`, **default `None`**). No AIS filter — forwards everything on the chosen events. Set `lineDelimiter` to `LF` explicitly. |
| [`@signalk/aisreporter`](https://www.npmjs.com/package/@signalk/aisreporter) | 1.3.2 | 2026-06-26 | Actively maintained, but reports **your own vessel** by synthesizing AIS type 18/24 from Signal K paths. Its README explicitly says it "will not forward positions that came in from your existing AIS — use `ais-forwarder` instead." |
| [`signalk-to-boating`](https://www.npmjs.com/package/signalk-to-boating) | 1.0.1 | 2026-08-21 | Newest of the bunch. Re-encodes AIS targets from the Signal K model as `!AIVDM` type 1/5/18/24 over UDP (broadcast or unicast, default port 2000). Aimed at Navionics Boating on the LAN, but the unicast option makes it usable off-boat. |
| [`signalk-n2kais-to-nmea0183`](https://www.npmjs.com/package/signalk-n2kais-to-nmea0183) | 2.0.3 | 2025-08-08 | Not a forwarder, but the piece that **produces** AIVDM on `nmea0183out` when your AIS arrives over NMEA 2000. Pair it with `ais-forwarder`. |
| [`signalk-vessels-to-ais`](https://www.npmjs.com/package/signalk-vessels-to-ais) | 2.1.1 | 2026-08-07 | Re-encodes model vessels back into NMEA 0183 AIS. |
| [`signalk-ais-navionics-converter`](https://www.npmjs.com/package/signalk-ais-navionics-converter) | 1.0.10 | 2026-04-23 | Signal K AIS → NMEA 0183 over TCP; can also generate N2K AIS PGNs. |
| [`ais-forwarder-peafy`](https://www.npmjs.com/package/ais-forwarder-peafy) | 0.0.9 | 2023-01-20 | Third-party fork of `ais-forwarder` predating upstream multi-endpoint support. No reason to prefer it. |
| [`signalk-net-relay`](https://www.npmjs.com/package/signalk-net-relay) | 1.1.0 | 2018-04-22 | Abandoned. README admits "Currently only UDP has been coded". Ignore. |

Search method: npm registry search API (`https://registry.npmjs.org/-/v1/search`) against `keywords:signalk-node-server-plugin` crossed with `ais`, `udp`, `nmea0183 forward`, `relay`. **No Signal K plugin forwards raw AIVDM over HTTP** — every network forwarder in the ecosystem is UDP.

---

## 4. AIS Dispatcher (AISHub)

**Two distinct products share the name, with independent version numbers:**

| Product | Version | Date | Evidence |
|---|---|---|---|
| **Linux 2.x (current)** | **2.3** | package built **2026-04-01**; version pointer updated 2026-04-14 | `https://www.aishub.net/downloads/dispatcher/version.txt` returns `2.3`; `HEAD .../packages/latest/full.tar.bz2` → `last-modified: Wed, 01 Apr 2026 13:30:25 GMT` |
| **Windows** | **1.5.1** | **2022-06-24** | [aishub.net/ais-dispatcher](https://www.aishub.net/ais-dispatcher): "Version 1.5.1 / Release Date 24-June-2022"; `HEAD .../aisdispatcher-1.5.1.zip` → `last-modified: Fri, 24 Jun 2022 07:38:27 GMT` |
| **Linux/macOS 1.2 (deprecated)** | 1.2 | 2015-05-15 | [aishub.net/ais-dispatcher](https://www.aishub.net/ais-dispatcher): "AIS Dispatcher v1.2 for Linux is deprecated. Please use the new version" |

`latest` == `2.3` was confirmed by MD5: `.../packages/latest/full.tar.bz2.md5` and `.../packages/2.3/full.tar.bz2.md5` both return `7cc021401860fa2023ead23975a082c7`.

**Platform and packaging.** Linux 2.x is a single 14.5 MB `full.tar.bz2` containing prebuilt binaries for every supported CPU, unpacked into `/home/ais`. **Not a .deb, not an .rpm.** Architectures shipped: `x86_64`, `armv8_a72`, `a72`, `a53`, `a7`, `arm1176` — chosen at boot by `bin/link_binary`, which parses `/proc/cpuinfo` `Revision` against a hardcoded Raspberry Pi revision table. So x86_64 and ARM Linux only; **no macOS in 2.x**, no Windows ARM. Windows is a plain `.zip` with no installer.

```bash
wget https://www.aishub.net/downloads/dispatcher/install_dispatcher
chmod 755 install_dispatcher
sudo ./install_dispatcher
```

The install script creates a `ais` system user, adds it to `systemd-journal`, `plugdev` and `dialout`, extracts to `/home/ais`, and runs `loginctl enable-linger ais`. It installs **systemd user units** (`aiscontrol.service`, `aisdispatcher@.service`, `aisdispatcher-update.service` + `.timer`, `link-binary.service`) rather than system units, and **auto-updates itself** — `bin/check-for-updates` polls `version.txt` on a timer and pulls new packages. Flag that in a guide if pinned versions matter.

**Inputs:** serial/USB (default `38400,8,N,1`), TCP client, TCP server, UDP server — confirmed by the binary's `mode,m` option accepting `tcp-client`, `tcp-server`, `udp-server`, `serial`. Windows 1.5.1 lacks TCP server.

**Outputs: UDP only.** The Linux docs say "Current version supports UDP data streaming to 1 or more destination IP addresses / UDP ports." There is no TCP or HTTP output.

**Processing options** (quoting [aishub.net/ais-dispatcher](https://www.aishub.net/ais-dispatcher)):
- **Downsampling** — "Reduces outgoing traffic by transmitting only 1 position report per ship in the specified time frame (in seconds from 0 to 60)". Important caveat from the same page: **"Downsampling affects AIS messages 1,2,3,18,19 only!"**
- **Duplicates removal** — "performs CRC check and removes duplicated NMEA messages"
- **Tag** — "Adds NMEA v4.10 tags in the beginning of output NMEA sentences"
- **Non-VDM** — "Dispatches all non-VDM (non-AIS) messages"
- Inactivity timeout, reconnect timeout, log verbosity
- **Polygon and message-type filtering is Windows-only.** The Linux config section has no filter option and no filter strings appear in the Linux binary.

**Web UI: port 8080, default credentials `admin` / `admin`.** From the docs: "Start your browser and open URL: `http://IPADDRESS:8080`… Default web login credentials are: Username: admin / Password: admin. WARNING Don't forget to change the default password after the first login!" Confirmed in the shipped `/home/ais/etc/aiscontrol.cfg`, which also reveals an **undocumented WebSocket listener on port 8081**:

```ini
listen_host = ::        # Web Server listen(bind) host
listen_port = 8080      # Web Server listen(bind) port
ws_listen_host = ::     # Web Socket listen(bind) host
ws_listen_port = 8081   # Web Socket listen(bind) port
```

Both bind `::` — all interfaces — by default. Windows has no web UI.

### Arbitrary destinations: YES, unrestricted

**AIS Dispatcher is not locked to AISHub.** It ships pointed at AISHub, but any host:port works. The shipped `/home/ais/etc/aisdispatcher.json` has free-form `host`/`port`/`desc` slots with AISHub as merely the first:

```json
{
  "rPiAIS001": {
    "destinations": [
      { "desc": "AISHub anonymous", "host": "data.aishub.net", "port": "2222" },
      { "desc": "", "host": "", "port": "" },
      { "desc": "", "host": "", "port": "" }
    ],
    "device": "/dev/ttyS0", "baudRate": "38400",
    "downsamplingTime": "10", "duplicatesTimeWindow": true,
    "enabled": false, "mode": "serial", "noneVDM": false,
    "inactivityTimeout": "300", "reconnectTimeout": "60",
    "tag": false, "verbosity": "0"
  }
}
```

The headless form is a systemd `EnvironmentFile` at `/home/ais/etc/aisdispatcher/aisdispatcher_<station>.opts`, consumed by `aisdispatcher@.service`:

```
AISD_OPTS='-m serial -D /dev/ttyS0 -d ais.openwaters.io:10110 -s mystation -w 10'
```

`-d`/`--dest` takes a comma-separated host:port list, so feeding two aggregators is:

```
AISD_OPTS='-m serial -D /dev/ttyACM0 -d data.aishub.net:2222,ais.openwaters.io:10110 -s mystation -w 10'
```

The site says "up to 12 destinations (unlimited for Linux version)", and the web UI's `appendDestination()` / `deleteDestination()` are a dynamic array of free-text inputs with no allowlist.

**Note the default:** "By default, AIS Dispatcher streams data to AISHub anonymous port and your data is displayed at VesselFinder." Anyone who installs it and does not edit the config is already feeding AISHub/VesselFinder. Tell users to remove that entry if they do not want it, and to change the `admin`/`admin` password.

**Licensing: UNVERIFIED.** There is **no license or terms of use anywhere**. No `LICENSE`, `COPYING`, `EULA` or `TERMS` file in the 2.3 tarball; no license text on the product page; `aishub.net/terms`, `/terms-of-use` and `/privacy-policy` all return **HTTP 404** (the footer "Privacy Policy" link is broken); `forum.aishub.net` returns 403. Factually: closed-source, no-cost binary distribution with no stated grant of use. It is **not** restricted to AISHub contributors — the download needs no account, key or registration. The contributor requirements at [aishub.net/join-us](https://www.aishub.net/join-us) ("at least one raw AIS feed in NMEA format", "Coverage of at least 10 vessels", "At least 90% uptime", "Maximum downsampling rate of 60 seconds", "Maximum delay of 10 seconds") govern access to AISHub's *aggregated data*, not use of the tool.

**Docker: no official image.** Community options, all unofficial and mostly stale: [`sanjusss/aisdispatcher`](https://hub.docker.com/r/sanjusss/aisdispatcher) (pushed 2018-11-10, so it wraps v1.2, not the 2.x rewrite), [dipalo/docker-aisdispatcher](https://github.com/dipalo/docker-aisdispatcher) (2023-05-09), [twigmarine/aisdispatcher](https://github.com/twigmarine/aisdispatcher) (2026-02-15 — a **Nix** flake/module, not Docker, and the most actively maintained third-party packaging), [TheFloatingLab/AISdispatcher.pl](https://github.com/TheFloatingLab/AISdispatcher.pl) (a Perl reimplementation, 2022-01-16), [tttonyyy/ais-dispatcher](https://github.com/tttonyyy/ais-dispatcher) (independent reimplementation, 2021-04-12). Containerizing it yourself is straightforward given the static-binary layout, but disable the auto-updater.

**Other unresolved points, flagged:** the shipped `aisdispatcher_x86_64` binary in the 2.3 tarball contains the embedded strings `_AISDispatcherv2.1` and `/tmp/aisdispatcherv2.1_`, i.e. the binary's internal build tag says 2.1 while the package is labelled 2.3. There is also **no published changelog** for the Linux 2.x line.

---

## 5. ShipXplorer `sxfeeder`

**Correction worth making in the guide: ShipXplorer is AirNav Systems (the RadarBox / radarbox24 company), not Flightradar24.** The deb's `Maintainer` is `Airnav Systems <support@airnavsystems.com>`, copyright `2017 AirNav Systems`, distributed from `apt.rb24.com` alongside `rbfeeder`. (docker-shipfeeder's Dockerfile names its keyring file `flightradar24.gpg` for that repo, which is probably where the confusion comes from.)

**What it is.** The closed-source client that uploads AIS data to shipxplorer.com. Package description: "Software for uploading AIS data to shipxplorer.com."

**Installation**, per [shipxplorer.com/raspberry-pi/guide](https://www.shipxplorer.com/raspberry-pi/guide):

```bash
sudo bash -c "$(wget -O - https://www.shipxplorer.com/install_sxfeeder.sh)"
```

That script adds AirNav's apt repo (`deb https://apt.rb24.com/ <bookworm|bullseye|stretch> main`, key `1D043681` via the deprecated `apt-key adv`) and runs `apt-get install sxfeeder -y`, then optionally `apt-get install aiscatcher -y`. Suites supported: `stretch`, `buster` (mapped to stretch), `bullseye`, `bookworm` — **no trixie**. Most useful line in the script:

> "By default, SXFeeder will listen for UDP data on port **34995**. If you need to change this port, edit configuration file located at `/etc/sxfeeder.ini`"

**Version and date:**

| Suite / arch | Version | Deb last-modified |
|---|---|---|
| bookworm arm64 | `1:1.0.1+bookworm` | 2024-06-20 |
| bookworm armhf | `1:1.0.1+bookworm` | 2024-06-20 |
| bullseye armhf | `1:1.0.1+deb11` | 2022-01-28 |
| **amd64** | **not present** | — |

Verified from `https://apt.rb24.com/dists/{bookworm,bullseye}/main/binary-{armhf,arm64,amd64}/Packages` and HEAD requests against the pool. The index is regenerated regularly (bookworm `Release` shows `Date: Sun, 16 Aug 2026 05:11:11 UTC`) but the **binary has not changed since June 2024**. Its internal Debian changelog has only two entries, the newest dated 2021-12-28 and marked `UNRELEASED`, with maintainer `pi@raspberrypi`, and the 1.0.0 entry calling it a "First beta-release".

**Not open source.** `usr/bin/sxfeeder` is an `ELF 64-bit LSB pie executable, ARM aarch64, dynamically linked, stripped`. A GitHub code search for `sxfeeder` returns **0 repositories** — there is no public source anywhere. The `copyright` file declares a license named `AIRNAV` whose body is **never defined**; only `AIRNAV1.0` (the packaging licence) has text, and that text is just a warranty disclaimer with a dangling "are available in these links:" followed by nothing. No grant-of-use terms at all. docker-shipfeeder's README states it plainly: "This binary is provided as closed-source by AirNav (the company that operates ShipXplorer)".

**Can it fan out to other aggregators? No.** Its entire CLI surface is `--config/-c`, `--showkey/-sw`, `--setkey/-sk`, `--no-start`, `--help`, `--version`. The complete shipped config is:

```ini
[client]
pid=/var/run/sxfeeder/sxfeeder.pid
disable_log=false
log_file=/var/log/sxfeeder.log

[network]
udp_listen_port=34995
```

There is no destination host option and no forwarding option. Every network string in the binary points at AirNav infrastructure (`sxrpiserver.rb24.com`, `rfsurvey.rb24.com`, `shipxplorer.com`). It ingests NMEA on one UDP port and ships a protobuf-framed stream to `sxrpiserver.rb24.com`. It is also hard-tied to Raspberry Pi identity: it derives a serial by grepping `serial\t\t:` from `/proc/cpuinfo` and pairs it with a 32-character sharing key.

**So fan-out is AIS-catcher's job, not sxfeeder's.** The standard pattern is AIS-catcher decoding once and sending a copy to loopback UDP 34995 for sxfeeder, alongside every other aggregator. docker-shipfeeder does exactly this — its AIS-catcher launcher unconditionally appends `-u 127.0.0.1 34994` and `-u 127.0.0.1 34995` ([`rootfs/etc/s6-overlay/scripts/aiscatcher`](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/rootfs/etc/s6-overlay/scripts/aiscatcher), lines 341-342), and writes `/etc/sxfeeder.ini` with `udp_listen_port=34995` from [`scripts/40-sxfeeder`](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/rootfs/etc/s6-overlay/scripts/40-sxfeeder).

**UNVERIFIED:** what listens on `34994`. AIS-catcher always feeds it, but nothing in the docker-shipfeeder repo (README, `40-sxfeeder`, `10-container-init`, `healthcheck.sh`, `etc/services`, Dockerfile) explains it.

**You can feed ShipXplorer without sxfeeder.** [shipxplorer.com/addcoverage](https://www.shipxplorer.com/addcoverage) lists three feeding paths — "AIS Dongle (Raspberry Pi)", "UDP (AIS Dispatcher/Catcher)", and their SeaRange receiver — so plain UDP works and sidesteps the closed binary entirely. In docker-shipfeeder terms that is `SHIPXPLORER_UDP_PORT` instead of `SHIPXPLORER_SHARING_KEY`.

**Raspberry Pi 5 is broken by default.** The binary needs 4 KB kernel pages; Debian for the Pi 5 defaults to 16 KB. Symptom from the container log: `[sxfeeder] FATAL: sxfeeder cannot be run natively, and QEMU is not available.` Check with `getconf PAGE_SIZE`; fix by adding `kernel=kernel8.img` to `/boot/firmware/config.txt` and rebooting, or by switching to the UDP feeding path. Source: [docker-shipfeeder README, "Working around ShipXplorer issues on Raspberry Pi 5"](https://github.com/sdr-enthusiasts/docker-shipfeeder#working-around-shipxplorer-issues-on-raspberry-pi-5).

**Note for anyone using AirNav's packaged AIS-catcher:** `aiscatcher` in `apt.rb24.com` is pinned at `1:0.31.0+deb11` (bullseye/armhf only) — far behind upstream v0.70. Install AIS-catcher from its own source or container instead.

---

## 6. docker-shipfeeder (sdr-enthusiasts)

**What it is.** A Docker container that runs AIS-catcher plus AirNav's `sxfeeder`, wraps them in environment variables, and feeds a long list of aggregators simultaneously from one receiver. Renamed from `docker-shipxplorer` on 2024-03-15; both image names still publish in parallel — `ghcr.io/sdr-enthusiasts/docker-shipfeeder` (current) and `ghcr.io/sdr-enthusiasts/shipxplorer` (legacy), identical except for the name. Source: [README, "What's New"](https://github.com/sdr-enthusiasts/docker-shipfeeder#whats-new).

| | |
|---|---|
| Repo | [sdr-enthusiasts/docker-shipfeeder](https://github.com/sdr-enthusiasts/docker-shipfeeder) |
| License | **GPL-3.0** (Copyright 2022-2025, Ramon F. Kolb, kx1t) |
| Tagged releases | **none** — the repo has no GitHub releases; it ships continuously off `main` |
| Last commit | **2026-08-22** ("chore: bump pinned ais-catcher digest to sha256:…") — Renovate bumps the pinned AIS-catcher digest several times a day |
| Stars / open issues | 92 / 2 |
| Architectures | `arm32v7`/`armhf`, `arm64`/`aarch64`, `amd64`/`x86_64`. Windows and macOS not supported |
| Support | [SDR-Enthusiasts Discord](https://discord.gg/sTf9uYF) |

Sources: `GET /repos/sdr-enthusiasts/docker-shipfeeder`, `GET .../releases` (empty), `GET .../commits`, [README](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/README.md).

### Adding a custom UDP target — the exact variable is `UDP_FEEDS`

> "If you want to feed and additional AIS aggregator that uses a hostname/UDP port that is not listed above, then simply add a comma separated list of hostnames/ip addresses and UDP ports to the `UDP_FEEDS` parameter. Format: `UDP_FEEDS=domain1.com:port1[:params],domain2,com:port2[:params],...`"
> — [README, "Feeding Additional Services Using UDP"](https://github.com/sdr-enthusiasts/docker-shipfeeder#feeding-additional-services-using-udp)

So for aiscast:

```yaml
- UDP_FEEDS=ais.openwaters.io:10110
```

There is **no** `AISCATCHER_UDP_...` output variable. The variable names are:

| Variable | Direction | Becomes | Separator |
|---|---|---|---|
| `UDP_FEEDS` | **out** | `-u <host> <port>` per entry | comma |
| `TCP_FEEDS` | **out** | `-P <host> <port>` per entry | comma |
| `AISCATCHER_UDP_INPUTS` | **in** | `-x <host> <port> -c <CHANNELS>` per entry | comma |
| `AISCATCHER_EXTRA_OPTIONS` | either | appended verbatim to the AIS-catcher command line | — |

Verified directly in [`rootfs/etc/s6-overlay/scripts/aiscatcher`](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/rootfs/etc/s6-overlay/scripts/aiscatcher):

```bash
if [[ -n "$UDP_FEEDS" ]]; then
    readarray -d "," -t feedsarray <<< "$UDP_FEEDS"
    for feeds in "${feedsarray[@]}"; do
        [[ -n "$feeds" ]] && FEEDSTRING+=("-u ${feeds//:/ }")
    done
fi
if [[ -n "$TCP_FEEDS" ]]; then
    readarray -d "," -t feedsarray <<< "$TCP_FEEDS"
    for feeds in "${feedsarray[@]}"; do
        [[ -n "$feeds" ]] && FEEDSTRING+=("-P ${feeds//:/ }")
    done
fi
```

**Documentation bug worth knowing:** the README's "Aggregating multiple instances of the container" section shows `UDP_FEEDS=.....;target_machine:9988` with a **semicolon**. The code uses `readarray -d ","` — the separator is a **comma**. Use commas.

Because `${feeds//:/ }` replaces *every* colon with a space, the optional `[:params]` in the documented format become trailing AIS-catcher settings on the `-u` switch — e.g. `UDP_FEEDS=host:10110:JSON:on` becomes `-u host 10110 JSON on`. **UNVERIFIED**: I did not find this documented explicitly, but it follows from the substitution and from [AIS-catcher's `-u` syntax](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/UDP).

### HTTP output to `/v1/receive`

Use `AISCATCHER_EXTRA_OPTIONS` with AIS-catcher's `-H`:

```yaml
- AISCATCHER_EXTRA_OPTIONS=-H https://ais.openwaters.io/v1/receive USERPWD x:ak1.<your-token> GZIP on INTERVAL 15 RESPONSE off
```

The README documents this pattern generally ("To feed additional AIS aggregators that are not listed above using HTTP…"), and multiple `-H` blocks can be chained in the same variable separated by spaces. Sources: [README, "Feeding Additional Services Using HTTP"](https://github.com/sdr-enthusiasts/docker-shipfeeder#feeding-additional-services-using-http); [`docs/API.md`](../../../docs/API.md) for the aiscast side.

### Aggregators it can feed simultaneously

All of these run at once from one SDR. Reproduced from the [README's aggregator table](https://github.com/sdr-enthusiasts/docker-shipfeeder#easy-sharing-with-other-services):

| Service | Variable(s) | Default endpoint | Protocol |
|---|---|---|---|
| ADSB-Network (RadarVirtuel) | `RADARVIRTUEL_STATION_ID` | `ais.adsbnetwork.com:3002/ingester/insert/lourd` | HTTP |
| Airframes | `AIRFRAMES_STATION_ID` | `feed.airframes.io:5599` | HTTP |
| aiscatcher.org | `AISCATCHER_SHAREDATA=true`, `AISCATCHER_FEEDER_KEY` | — | other |
| AIS Friends | `AISFRIENDS_UDP_PORT` | `ais.aisfriends.com` | UDP |
| AISHub | `AISHUB_UDP_PORT` | `data.aishub.net` | UDP |
| aprs.fi | `APRSFI_FEEDER_KEY`, `APRSFI_STATION_ID` | `aprs.fi/jsonais/post/$KEY` | HTTP |
| BoatBeacon (Pocket Mariner) | `BOATBEACON_SHAREDATA=true` \| `BOATBEACON_UDP_PORT` \| `BOATBEACON_TCP_PORT` | `boatbeaconapp.com:5322` | UDP/TCP |
| HPRadar | `HPRADAR_UDP_PORT` | `aisfeed.hpradar.com` | UDP |
| MarineTraffic | `MARINETRAFFIC_UDP_PORT` \| `MARINETRAFFIC_TCP_PORT` | `5.9.207.224` | UDP/TCP |
| MLAT.uk | `MLATUK_SHAREDATA=true` | `feed.mlat.uk:50001` | UDP |
| MyShipTracking | `MYSHIPTRACKING_UDP_PORT` \| `_TCP_PORT` | `178.162.215.175` | UDP/TCP |
| SDRMap | `SDRMAP_STATION_ID`, `SDRMAP_PASSWORD` | `ais.feed.sdrmap.org` | HTTP |
| ShipFinder | `SHIPFINDER_SHAREDATA=true` | `ais.shipfinder.co.uk:4001` | UDP |
| ShippingExplorer | `SHIPPINGEXPLORER_UDP_PORT` \| `_TCP_PORT` | `144.76.54.111` | UDP/TCP |
| ShipXplorer (key) | `SHIPXPLORER_SHARING_KEY`, `SHIPXPLORER_SERIAL_NUMBER` | — | `sxfeeder` |
| ShipXplorer (UDP alt) | `SHIPXPLORER_UDP_PORT` | `hub.shipxplorer.com` | UDP |
| VesselFinder | `VESSELFINDER_UDP_PORT` \| `_TCP_PORT` | `ais.vesselfinder.com` | UDP/TCP |
| VesselTracker | `VESSELTRACKER_UDP_PORT` \| `_TCP_PORT` | `83.220.137.136` | UDP/TCP |
| **anything else** | **`UDP_FEEDS`** / **`TCP_FEEDS`** / `AISCATCHER_EXTRA_OPTIONS -H` | you choose | UDP/TCP/HTTP |

Every `SERVICE_UDP_PORT` also accepts a `hostname:port` or `ip:port` form to override the default host. Precedence, per the README: an explicit `SERVICE_UDP_PORT` wins over `SERVICE_SHAREDATA`; a `SERVICE_TCP_PORT` is used *in addition* to any UDP port, which the README warns "may cause duplicate feeding".

**aiscast is not in that table.** For a guide, `UDP_FEEDS=ais.openwaters.io:10110` is the line to give people — it is a one-line addition to an existing shipfeeder setup, alongside whatever they already feed.

### Device passthrough

The README's own compose example does **not** use `--device /dev/bus/usb`. It uses a cgroup rule plus a bind mount of all of `/dev`, which is what survives a USB replug:

```yaml
    device_cgroup_rules:
      - 'c 189:* rwm'
    volumes:
      - /dev:/dev:rw
```

(`189` is the Linux `usb_device` major number.) For `docker run` the equivalent is `--device-cgroup-rule='c 189:* rwm' -v /dev/bus/usb:/dev/bus/usb`, which is what the AIS-catcher docs use. A hardware serial receiver instead needs `--device /dev/ttyACM0` (or a `devices:` entry in compose). Sources: [docker-shipfeeder README compose example](https://github.com/sdr-enthusiasts/docker-shipfeeder#up-and-running-with-docker-compose); [AIS-catcher Docker docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker).

### Complete working `docker-compose.yml`

Adapted from [`config-examples/docker-compose.yml.sample`](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/config-examples/docker-compose.yml.sample), trimmed to a station that feeds aiscast plus AISHub:

```yaml
services:
  shipfeeder:
    image: ghcr.io/sdr-enthusiasts/docker-shipfeeder
    container_name: shipfeeder
    hostname: shipfeeder
    restart: always
    environment:
      # station identity and web viewer
      - STATION_NAME=My&nbsp;Station&nbsp;Name
      - FEEDER_LAT=44.123456
      - FEEDER_LONG=-83.123456
      - STATION_HISTORY=3600
      - BACKUP_INTERVAL=5
      - SITESHOW=on
      - REALTIME=on
      - PROMETHEUS_ENABLE=on
      # SDR
      - SDR_TYPE=RTLSDR
      - RTLSDR_DEVICE_SERIAL=00000001
      - RTLSDR_DEVICE_GAIN=33.8
      - RTLSDR_DEVICE_PPM=0
      - RTLSDR_DEVICE_BANDWIDTH=192K
      - AISCATCHER_DECODER_AFC_WIDE=on
      # aggregators
      - AISHUB_UDP_PORT=12345
      - AISCATCHER_SHAREDATA=true
      # aiscast — this is the line that matters
      - UDP_FEEDS=ais.openwaters.io:10110
      # optional: aiscast over HTTP instead of / as well as UDP
      # - AISCATCHER_EXTRA_OPTIONS=-H https://ais.openwaters.io/v1/receive USERPWD x:ak1.<token> GZIP on INTERVAL 15 RESPONSE off
      # optional: accept NMEA from another AIS-catcher on the LAN
      # - AISCATCHER_UDP_INPUTS=other-host:9988:AB
    ports:
      - 90:80          # AIS-catcher web viewer at http://<host>:90
      - 9988:9988/udp  # only needed if you accept UDP inputs
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

Notes on the example: the web viewer is on container port `80` (mapped to `90` here); `/data` must be a persistent volume for `aiscatcher.bin` statistics, web plugins and `about.md` to survive restarts; `DISABLE_WEBSITE=true` turns the viewer off entirely.

### Other useful env vars

Receiver: `SDR_TYPE` (`RTLSDR`\|`AIRSPY`\|`SDRPLAY`), `RTLSDR_DEVICE_SERIAL`, `RTLSDR_DEVICE_GAIN` (or `auto`), `RTLSDR_DEVICE_PPM`, `RTLSDR_DEVICE_RTLAGC` (default `on`), `RTLSDR_DEVICE_BANDWIDTH` (default `192K`), `RTLSDR_DEVICE_BIASTEE`, `AISCATCHER_CHANNELS` (`AB`\|`CD`\|`CD AB`), `AISCATCHER_DECODER_MODEL` (default `2`), `AISCATCHER_DECODER_AFC_WIDE`/`_FP_DS`/`_PS_EMA`/`_SOXR`/`_SRC`/`_DROOP`, `VERBOSE_LOGGING` (`0`-`5`, default `2`).

Website: `DISABLE_WEBSITE`, `PLUGINS_FILE`, `STYLES_FILE`, `STATION_NAME`, `STATION_LINK`, `STATION_HISTORY`, `BACKUP_INTERVAL`, `BACKUP_RETENTION_TIME`, `SITESHOW`, `FEEDER_LAT`/`FEEDER_LONG`, `DISABLE_SHOWLASTMSG`, `PLUGIN_UPDATE_INTERVAL`, `REFRESHRATE`, `DISABLE_GEOJSON`, `ADSB_CONNECTOR`, `PROMETHEUS_ENABLE`.

MQTT: `AISCATCHER_MQTT_URL`, `_CLIENT_ID`, `_QOS`, `_TOPIC`, `_MSGFORMAT` (`NMEA`\|`NMEA_TAG`\|`FULL`\|`JSON_NMEA`\|`JSON_SPARSE`\|`JSON_FULL`).

ShipXplorer: `SHIPXPLORER_SHARING_KEY`, `SHIPXPLORER_SERIAL_NUMBER`, `SXFEEDER_EXTRA_OPTIONS`.

All from the [README's variable tables](https://github.com/sdr-enthusiasts/docker-shipfeeder#runtime-environment-variables).

### `sdr-enthusiasts/docker-aiscatcher` — does not exist

Confirmed: `GET /repos/sdr-enthusiasts/docker-aiscatcher` returns `404 Not Found`, and a GitHub repository search for `docker-aiscatcher` returns **zero results**. The only AIS repos in the org are `docker-shipfeeder` and [`docker-vesselalert`](https://github.com/sdr-enthusiasts/docker-vesselalert) (notifications for vessels captured with AIS-catcher, last pushed 2026-08-16). Whatever you may have seen elsewhere, there is no such repo — use `docker-shipfeeder` or the official image.

### The official `ghcr.io/jvde-github/ais-catcher` image

Published from [`jvde-github/AIS-catcher`](https://github.com/jvde-github/AIS-catcher) itself. Tags: `latest` (latest release) and `edge` (bleeding edge of `main`). SDRplay is **not** supported in the Docker images. Source: [AIS-catcher Docker docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker).

Two modes:

**Managed mode** (recommended by upstream) — browser-based configuration and control:

```bash
docker run --rm -it --network=host \
  --device-cgroup-rule='c 189:* rmw' -v /dev/bus/usb:/dev/bus/usb \
  -v ais-config:/config \
  ghcr.io/jvde-github/ais-catcher:edge -E /config/config.json 127.0.0.1:8118
```

Then a setup wizard at `http://localhost:8118`. For a hardware serial receiver, replace the USB passthrough with `--device /dev/ttyACM0`. Replace `127.0.0.1` with `0.0.0.0` to administer it remotely (a password is then required).

**Manual mode** — any AIS-catcher command line:

```bash
docker run --rm -it --pull always --device /dev/bus/usb \
  ghcr.io/jvde-github/ais-catcher:latest <options>
```

**Difference from docker-shipfeeder.** The upstream image is bare AIS-catcher — one binary, no wrapper, both a managed dashboard and a plain CLI. docker-shipfeeder is opinionated multi-aggregator plumbing on top: it bundles `sxfeeder`, turns ~40 environment variables into an AIS-catcher command line, adds Prometheus/Grafana, web plugin auto-update, and persistence. Upstream's own docs put it this way: "For manual-mode setups focused on feeding aggregators, [docker-shipfeeder] by the sdr-enthusiasts community is a user-friendly alternative with excellent documentation and support. Note that it runs AIS-catcher in manual mode and does not offer the managed-mode dashboard."

**Guide recommendation:** one aggregator → the official image or a plain systemd AIS-catcher. Several aggregators → docker-shipfeeder.

---

## 7. AIS-catcher itself as the forwarder (worth its own section)

Easy to overlook: **AIS-catcher does not need an SDR.** It reads a hardware AIS receiver over serial with `-e`, so for a dAISy HAT / dAISy USB / Quark-elec / chartplotter NMEA output it replaces both the receiver software *and* the multiplexer, and it is the most actively maintained thing in this entire document.

| | |
|---|---|
| Repo | [jvde-github/AIS-catcher](https://github.com/jvde-github/AIS-catcher) |
| Latest release | **v0.70, 2026-06-19** (v0.69 2026-06-14, v0.68 2026-04-27) |
| Last commit | **2026-08-22** |
| License | GPL-3.0 |
| Docs | <https://jvde-github.github.io/AIS-catcher-docs/> |
| Platforms | Linux/RPi, Windows, macOS, Android, Docker |

Sources: `GET /repos/jvde-github/AIS-catcher`, `/releases`, `/commits`.

**Serial input** (`-e [baudrate] port`, settings via `-ge`):

```bash
AIS-catcher -e 38400 /dev/ttyACM0
```

Serial settings: `baudrate` (default 38400), `port`, `dump`, `dump_file`, `init_seq`, `flowcontrol` (`NONE`\|`HARDWARE`\|`SOFTWARE`). Use `-ge dump on` to see raw bytes when debugging. Source: [Serial Port input docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/input/serial).

**UDP output** (`-u host port`):

```bash
AIS-catcher -e 38400 /dev/ttyACM0 -u ais.openwaters.io 10110
```

`-u` settings: `msgformat` (`NMEA` default, `JSON_NMEA`, `JSON_FULL`), `broadcast`, `reset` (recreate the socket after N minutes, 1-1440), `uuid`, `include_sample_start`. Leave `msgformat` at the default `NMEA` for aiscast. Source: [UDP output docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/UDP).

**The `reset` setting is worth calling out in a guide.** "Recreate socket after N minutes (1-1440)" is exactly the fix for the stale-DNS/stale-socket problem that bites `socat`. `-u ais.openwaters.io 10110 reset 60` re-creates the socket hourly.

**Fan-out** is just repeated switches:

```bash
AIS-catcher -e 38400 /dev/ttyACM0 \
  -u ais.openwaters.io 10110 \
  -u data.aishub.net 12345 \
  -N 8100 \
  -v 60
```

**HTTP output** (`-H url`), for `/v1/receive`:

```bash
AIS-catcher -e 38400 /dev/ttyACM0 \
  -H https://ais.openwaters.io/v1/receive USERPWD x:ak1.<token> GZIP on INTERVAL 15 RESPONSE off
```

`-H` settings include `userpwd` (`user:password`, sent as HTTP Basic — this is how an aiscast token travels), `ssl_verify`, `stationid`/`id`/`callsign`, `interval` (default 60 s), `timeout`, `gzip`, `response`, `protocol` (`AISCATCHER` default, `MINIMAL`, `AIRFRAMES`, `LIST`, `NMEA`, `APRS`), `lat`/`lon`. The default `AISCATCHER` protocol posts the `jsonaiscatcher` envelope, which is what aiscast's `/v1/receive` parses. Note the docs' build caveat: "to use and build AIS-catcher with HTTP support, please install the following libraries before running cmake: `sudo apt install libssl-dev zlib1g-dev`" — irrelevant if you use the prebuilt package or container. Source: [HTTP output docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/HTTP).

Also available: TCP client output (`-P`), TCP server, MQTT, NMEA2000, PostgreSQL, a built-in web viewer (`-N port`), and the aiscatcher.org community feed (`-X <sharing key>`). Source: [output overview](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/overview).

**Plug-and-play hardware.** Upstream now co-develops the **dAISy-catcher** with Wegmatt — a dual-channel hardware receiver that "connects directly to AIS-catcher over serial, as a USB device or Raspberry Pi HAT". Worth naming in a hardware section. Source: [AIS-catcher README](https://github.com/jvde-github/AIS-catcher#plug-and-play-solution).

---

## 8. OpenCPN

**Current version: 5.14.0, published 2026-04-09** (the release body is dated "8 April, 2026" and calls it "a service/maintenance release of OpenCPN"). Everything newer in the release list is a `prerelease: true` 5.14 beta. GPL-2.0. Sources: [Release_5.14.0](https://github.com/OpenCPN/OpenCPN/releases/tag/Release_5.14.0), `GET /repos/OpenCPN/OpenCPN/releases`.

Platforms, per the [download page](https://opencpn.org/OpenCPN/info/downloadopencpn.html) ("Stable Release 5.14.0 April 8, 2026"): Windows 8/8.1/10/11, "MacOS 11 and newer (Universal build for both Apple Silicon or Intel processors)", Raspberry Pi, Ubuntu PPA, Flatpak ("OpenCPN Version 5.2 and later is available as a Flatpak package"), Debian/Gentoo/Mageia repos, and Android via Google Play. No iOS. **UNVERIFIED:** the Android build's version number — the download page does not state one.

### Can OpenCPN relay received AIS to a remote UDP endpoint? Yes.

This is the answer that matters, and it is unambiguous. `Multiplexer::HandleN0183()` in [`model/src/multiplexer.cpp`](https://github.com/OpenCPN/OpenCPN/blob/Release_5.14.0/model/src/multiplexer.cpp) loops over every registered NMEA 0183 driver and re-sends the message:

```cpp
//  Allow re-transmit on same port (if type is SERIAL),
//  or any other NMEA0183 port supporting output
//  But, do not echo to the source network interface.  This will
//  likely recurse...
if ((!params_.DisableEcho && params_.Type == SERIAL) ||
    driver->iface != n0183_msg->source->iface) {
  if (params_.IOSelect == DS_TYPE_INPUT_OUTPUT ||
      params_.IOSelect == DS_TYPE_OUTPUT) {
    ...
    if (params_.SentencePassesFilter(n0183_msg->payload.c_str(), FILTER_OUTPUT)) {
```

There is **no sentence-type gate** — the only tests are that the driver is N0183, is configured for output, is not the source interface, and that the payload passes that connection's output filter. `!AIVDM,…` satisfies all four. The UDP driver then sends the payload byte-for-byte: `CommDriverN0183Net::SendSentenceNetwork` does `udp_socket->SendTo(m_addr, payload.mb_str(), payload.size())`, appending `\r\n` if missing, with `m_addr` built from the connection's `NetworkAddress`/`NetworkPort` ([`model/src/comm_drv_n0183_net.cpp`](https://github.com/OpenCPN/OpenCPN/blob/master/model/src/comm_drv_n0183_net.cpp)). Not a re-encode — a relay.

The manual says the same thing in prose ([Options → Connections](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:manual_basic:set_options:connections)):

> "OpenCPN has a built-in multiplexer that distributes all incoming and outgoing data." … "Any message can be received, and possibly re-transmitted according to the configuration established." … "**NOTE: Another scenario where output filtering is required is forwarding AIS messages to different networks or to other consumers like AIShub. One will just send the AIS messages and not the complete data stream.**"

**No version changed this.** The identical loop exists in [5.6.2's `src/multiplexer.cpp`](https://github.com/OpenCPN/OpenCPN/blob/Release_5.6.2/src/multiplexer.cpp) ("// Send to all the other outputs"), gated on the same `DS_TYPE_OUTPUT` and `FILTER_OUTPUT` tests. The 5.8 comms rewrite moved the file; the semantics are unchanged through `master`.

### Connections dialog, exact labels

Path: `Options` → `Connections` tab → **`Add new connection...`** → the panel titles itself **`Configure new connection`** (or `Edit Selected Connection`). The list grid columns are the enable checkbox, `Protocol`, `In/Out`, `Data port`, `Status`. Labels below are from [`gui/src/connection_edit.cpp` at `Release_5.14.0`](https://github.com/OpenCPN/OpenCPN/blob/Release_5.14.0/gui/src/connection_edit.cpp).

**Serial input:** `Serial` / `Network` type radios; **`Data port`** (device combo); **`Baudrate`** (150 … 921600); **`Protocol`** (`NMEA 0183` / `NMEA 2000`); **`User Comment`**; **`Receive Input on this Port`**; **`Output on this port (as autopilot or NMEA repeater)`**; an **`Advanced: `** section with `Use Garmin (GRMN) mode for input`, `APB bearing precision`, **`Input filtering`** (`Accept only sentences` / `Ignore sentences`) and **`Output filtering`** (`Transmit sentences` / `Drop sentences`).

**Network UDP output:** **`Network Protocol`** radios `TCP` / `UDP` / `GPSD` / `Signal K`; **`Data Protocol`** (`NMEA 0183` / `NMEA 2000`); **`Address`** — freely editable, this is the remote destination host; **`DataPort`** (note: network uses `DataPort`, serial uses `Data port`); **`Description`**; **`Receive Input on this Port`**; **`Output on this port (as autopilot or NMEA repeater)`**; `UDP Multicast` in Advanced. The manual spells the flow out ([Advanced connections](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:manual_basic:set_options:connections:advanced)): "If you uncheck //Receive Input on this Port// and then check //Output on this port//, then you are setting up a UDP output connection… Enter the IP address and Dataport for the UDP client you want to receive the data."

**Two fields the wiki still documents that no longer exist in 5.12+/5.14** — worth knowing so a guide does not repeat them:

- **`Control Checksum`** — removed. `connection_edit.cpp:2077` hard-codes `pConnectionParams->ChecksumCheck = true;` and a code search for `"Control checksum"` returns zero results. The behaviour became automatic: 5.14.0's release notes list "Allow NMEA0183 sentences without Checksum", implemented as `else if (has_checksum && !Is0183ChecksumOk(sentence)) state = kBadChecksum;` in [`model/src/comm_drv_n0183.cpp`](https://github.com/OpenCPN/OpenCPN/blob/Release_5.14.0/model/src/comm_drv_n0183.cpp) — a sentence with no `*hh` is accepted, not rejected.
- **`Priority` / `List Position`** — connection priority moved to a separate global dialog, reached from the Connections tab via **`Adjust Nav data priorities...`**. Documented at [data_priority](https://opencpn.org/wiki/dokuwiki/doku.php?id=opencpn:manual_basic:set_options:connections:data_priority).

The Connections dialog was reworked in [5.12.0](https://github.com/OpenCPN/OpenCPN/releases/tag/Release_5.12.0) (2025-07-15, "Improved Connections management dialog"), and the user wiki has not fully caught up.

### Working configuration

1. **Connection A (input)** — `Serial`, Baudrate `38400`, Protocol `NMEA 0183`. `Receive Input on this Port` ticked, `Output on this port` unticked.
2. **Connection B (output)** — `Network`, Network Protocol `UDP`, Data Protocol `NMEA 0183`, `Address` = `ais.openwaters.io`, `DataPort` = `10110`. `Receive Input on this Port` **unticked**, `Output on this port` **ticked**. `Output filtering` → `Transmit sentences` → add `AIVDM` (and `AIVDO` if you want own-ship).
3. Verify in the **Data Monitor** (hotkey `E`, or Tools → Alt-C; renamed from "Show NMEA Debug Window" in 5.12.0). Blue lines are transmitted output.

Filter syntax, quoted from the manual: "2 characters to filter by talker IDs, e.g. "GP", or "AI". 3 characters to filter by sentence types… 5 characters to filter by talker ID & sentence type, e.g "GPRMC", or **"AIVDM"**. valid REGEX expression matching the 6 characters". This matches `ConnectionParams::SentencePassesFilter` in [`model/src/conn_params.cpp`](https://github.com/OpenCPN/OpenCPN/blob/master/model/src/conn_params.cpp), which switches on filter-string length 2/3/5 and falls back to `wxRegEx`; an empty list means everything passes.

### Caveats that will bite

1. **NMEA 2000 AIS is not converted to AIVDM on output.** The multiplexer's output loop only touches `NavAddr::Bus::N0183` drivers. The manual: "retransmit all or some received NMEA 0183 (not NMEA 2000) data" and "Output for serial ports with NMEA 2000 protocol is not supported." Also: "Connection Filters do not apply to NMEA 2000 and Signal K data."
2. **UDP input and output are mutually exclusive per connection.** `OnCbOutput()` does `if (checked && m_rbNetProtoUDP->GetValue()) { m_cbInput->SetValue(FALSE); }` and `OnCbInput()` mirrors it. You need two connections — which is what you want anyway.
3. **Without an output filter you also send OpenCPN's own sentences** — synthesized `ECRMC`, `ECRMB`, `ECAPB`, everything from every other input, and anything plugins emit. Always set the output filter.
4. **An input filter that drops AIVDM also stops it being relayed**, by default. The `LegacyInputCOMPortFilterBehaviour` config key restores the old pass-through, defaults to `false` (`g_b_legacy_input_filter_behaviour = false` in [`model/src/gui_vars.cpp`](https://github.com/OpenCPN/OpenCPN/blob/Release_5.14.0/model/src/gui_vars.cpp)), and **has no GUI control** — it is edit-the-ini-file only, and serial-only.
5. **Loop protection is only "don't echo to the source interface".** Same UDP port in and out gets you the config-time warning "There is an enabled UDP input connection that uses the same data port. Please apply a filter on both connections to avoid a feedback loop."
6. `0.0.0.0` is not a usable UDP output address: "OpenCPN requires a routable, non-local IP address for UDP outputs."

### Why it is the wrong tool for an unattended station

OpenCPN is a wxWidgets GUI chartplotter with **no headless or daemon mode** — a code search for `headless` across the repo returns three files, none of them a no-GUI run mode. The binary always needs a display/session. The project's own tracker documents the friction:

- [#4059 "Navigation warning blocks unattended, headless start"](https://github.com/OpenCPN/OpenCPN/issues/4059) — "When used in a headless environment the navigation warning … blocks the start up sequence and requires manual intervention by pressing "OK" button."
- [#4099](https://github.com/OpenCPN/OpenCPN/issues/4099) — "Fully headless X11 over VNC erroneously thinks the display resolution is 0x0".
- [#3407 "Headless use case: New USB devices are not discovered"](https://github.com/OpenCPN/OpenCPN/issues/3407) — "Only restart of OpenCPN fixes this situation -- in the headless use case this actually turns out be done using a full system reboot."
- [#3922 "Modal 'device not found' dialog blocks boot."](https://github.com/OpenCPN/OpenCPN/issues/3922)
- [#3927](https://github.com/OpenCPN/OpenCPN/issues/3927) — AIS target list growth makes OpenCPN "sluggish and more often than not crash" on large feeds.

The developer manual's own "headless" feature (experimental REST pairing since 5.9) is about avoiding a click on both ends of an SSH-paired pair, not about running without a GUI: "The requirement to have access to both the server and client GUI is cumbersome when working with a headless server."

**Verdict:** OpenCPN's forwarding is genuinely capable and genuinely useful — on a boat computer that is already running the chartplotter, ticking two boxes gets your AIS to an aggregator for free. For a dedicated shore station that must survive reboots and USB replugs unattended, it is the wrong shape: you would need Xvfb or a persistent VNC session, autostart plumbing, a watchdog, and you would still hit modal startup dialogs and no hot-plug rediscovery. Recommend AIS-catcher or socat there.

**UNVERIFIED:** no cruisersforum.com threads were retrieved for community corroboration (search budget exhausted, and scraping returned CAPTCHAs). Every OpenCPN claim above rests on the manual, the source at the `Release_5.14.0` tag, the releases API, or the issue tracker.

---

## 9. Node-RED and one-liners


### Node-RED

Node-RED **5.0.4** (published 2026-07-30, [registry.npmjs.org/node-red](https://registry.npmjs.org/node-red)) ships the `udp` nodes in its core node set — [`packages/node_modules/@node-red/nodes/core/network/32-udp.html`](https://github.com/node-red/node-red/blob/master/packages/node_modules/%40node-red/nodes/core/network/32-udp.html), no palette install needed. It does **not** ship serial nodes — those come from **`node-red-node-serialport`**, version **2.0.3**, last published **2024-08-06** ([registry.npmjs.org/node-red-node-serialport](https://registry.npmjs.org/node-red-node-serialport), [flows.nodered.org/node/node-red-node-serialport](https://flows.nodered.org/node/node-red-node-serialport)). Install it from Menu → Manage palette → Install.

Minimal flow: **`serial in`** → **`udp out`**. Two nodes, one wire. The package "Provides four nodes - one to receive messages, and one to send, a request node … and a control node"; the input node reads a local serial port and can "wait for a "split" character (default `\n`). Also accepts hex notation (0x0a)", then "outputs `msg.payload` as either a UTF8 ascii string or a binary Buffer object." Source: [node-red-node-serialport README](https://github.com/node-red/node-red-nodes/blob/master/io/serialport/README.md).

The default `\n` split is exactly what you want: one NMEA sentence per message, therefore one sentence per datagram. Point the `udp out` node at `ais.openwaters.io` port `10110`.

Node-RED is a heavy dependency for what `socat` does in one line, but it earns its place when you already run it for other boat automation, or when you want to fan out, rate-limit, or tee the same stream into MQTT/InfluxDB alongside the aggregator feed.

**UNVERIFIED:** whether the `serial in` node strips the split character from `msg.payload`. It does not matter for aiscast — one sentence per datagram works with or without a trailing newline, since `Pipeline.Ingest` `TrimSpace`s every line (`server/pipeline.go:133`).

### Plain-Python one-liner

For readers who would rather see the mechanism than install anything, `pyserial` plus stdlib `socket` is about six lines:

```python
#!/usr/bin/env python3
import serial, socket
ser  = serial.Serial("/dev/ttyACM0", 38400, timeout=1)
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
dest = ("ais.openwaters.io", 10110)
while True:
    line = ser.readline()                       # one NMEA sentence per datagram
    if line.startswith((b"!", b"$")):
        sock.sendto(line, dest)
```

`ser.readline()` is the Python equivalent of socat's `icanon=1` — it guarantees one whole sentence per datagram. Wrap it in the same systemd unit shown above (`Restart=always`) and it is functionally equivalent to the socat recipe. Reach for it when you want to add filtering or a de-duplication window that socat cannot express.

**UNVERIFIED:** this snippet is illustrative and was not executed against real hardware.

## 10. Other multiplexers and hardware routers

The single question for each: **can it push received `!AIVDM` to an arbitrary remote UDP host and port over the internet?**

| Tool / device | Type | Remote UDP out? | Maintenance |
|---|---|---|---|
| **AvNav** | software, MIT | **yes** — `AVNUdpWriter` with `host`/`port` | very active, last commit 2026-08-21 |
| **OpenPlotter** | Pi distro, GPL-3.0 | n/a — it bundles Signal K, which does the forwarding | slow; `openplotter-settings` last commit 2025-03-08 |
| **marnav** | C++ library, 4-clause BSD | no — a parsing library, no daemon | dormant; last commit 2025-12-09, no releases |
| "nmea-multiplexer" repos | assorted hobby projects | mostly LAN broadcast only | see below |
| **gpsd** | daemon, BSD | **no** built-in forwarding; pass-through + a pipe works | active; release-3.27.5, 2026-01-14 |
| **Yacht Devices YDNR-02** | hardware router | **yes** — outgoing connection to a specific IP | firmware 1.71, 2024 |
| Yacht Devices YDWG-02 / YDEN-02 / YDNU-02 | hardware gateways | **no** — listeners only | — |
| **Actisense** W2K-1/W2K-2, NGT-1 | hardware gateways | **no** — listener / no network at all | manual v1.2, 2026 |
| **Digital Yacht AISnet** | AIS base station | **probably yes** — free-text "Host IP" field | old kit, firmware v01.50 |
| Digital Yacht NavLink2 / iAIS / iKonvert | hardware | **no** — broadcast or TCP server only | — |
| **Node-RED** | software, Apache-2.0 | **yes** — `udp out` is a core node | v5.0.4, 2026-07-30 |
| **ser2net** | software, GPL-2.0 | yes, via `connback` / `remaddr: !addr` | v4.6.8, 2026-08-03 |
| **ShipModul MiniPlex-3E/3Wi** | hardware | **yes** — "UDP Mode Directed" | manual v3.16 |
| **canboat** | N2K toolchain | no remote UDP sender — pipe it to socat | very active, v8.0.0-beta3, 2026-08-13 |

### AvNav — yes, and it is a genuinely good fit

AvNav ([wellenvogel/avnav](https://github.com/wellenvogel/avnav), **MIT**, Python 3, Raspberry Pi / Debian / Android) has a real outbound UDP client. From [`server/handler/udpwriter.py`](https://github.com/wellenvogel/avnav/blob/master/server/handler/udpwriter.py):

```python
class AVNUdpWriter(AVNWorker):
  P_HOST = WorkerParameter('host', 'localhost', description="target host for udp packages")
  P_PORT = WorkerParameter('port', 2000, type=WorkerParameter.T_NUMBER,
                           description='port for udp packages')
  P_BROADCAST = WorkerParameter('broadcast', True, type=WorkerParameter.T_BOOLEAN,
                                description="send broadcast packages")
```

`host`, `port`, plus `FILTER_PARAM` and `BLACKLIST_PARAM` for sentence filtering, and `canEdit()` returns `True` so it is configurable from the web UI. Set `broadcast: false` for plain unicast to an internet host. Other channel handlers in the same directory: `socketwriter` (TCP server), `socketreader` (TCP client), `udpreader`, `serialwriter`, `avnserial`, `avnusb`, `bluetoothhandler`, `signalkhandler`.

**Maintenance: very active** — last commit **2026-08-21**. **Caveat worth flagging:** its GitHub *Releases* are useless (latest tag is `release-2015-01-29`); real releases and images ship from [wellenvogel.net](https://www.wellenvogel.net/software/avnav/docs/install.html?lang=en), not GitHub. Do not judge AvNav's health by its GitHub release list. **UNVERIFIED:** the current AvNav release version string — the project's `releases.html` page did not resolve.

### OpenPlotter — a distro, not a multiplexer

Raspberry Pi OS image plus wxPython config apps. Current major version is **OpenPlotter 4**, targeting Debian 12 Bookworm / Ubuntu 22.04 ([docs](https://openplotter.readthedocs.io/latest/getting_started/downloading.html)). Pi editions ship "Settings - Docs - Signal K installer - OpenCPN installer - Serial - CAN bus - Dashboards - Notifications - Xygrib" — it *installs* Signal K and OpenCPN rather than routing NMEA itself. GPL-3.0. Older versions bundled kplex; the [openplotter-kplex](https://github.com/openplotter/openplotter-kplex) repo was last pushed 2020-05-29. `openplotter-settings` has no GitHub releases and its last commit is **2025-03-08** ("version 4.2.9 stable"); the docs repo last pushed 2025-02-19. **For forwarding, treat OpenPlotter as "Signal K in a box" and follow the Signal K section above.**

### marnav — a library, not a forwarder

[mariokonrad/marnav](https://github.com/mariokonrad/marnav): "This is a C++ library for **MAR**itime **NAV**igation purposes… It supports (partially): NMEA-0183, AIS, SeaTalk". It parses and generates sentences and can read serial ports, but ships no daemon, no routing, and no network output — you would write the socket yourself. License is **4-clause BSD** (GitHub reports `NOASSERTION`); note clause 3 is the advertising clause, which is GPL-incompatible. Dormant: zero releases, last commit **2025-12-09** ("Build: update to CMake 4") after a gap back to 2023-04-28. Mention only as a building block.

### "nmea-multiplexer" — nothing notable

A GitHub search for `NMEA-multiplexer in:name` turns up only hobby projects. The largest, [AK-Homberger/NMEA0183-WiFi-Multiplexer](https://github.com/AK-Homberger/NMEA0183-WiFi-Multiplexer) (ESP32, GPL-3.0, 15 stars, last push 2022-11-11), forwards "to USB-Serial and WLAN as **UDP broadcast**" — LAN-scoped, no remote host. `tkoning/nmea-multiplexer` is a two-file Arduino sketch with no license; `arnegue/NMEA-Seatalk-Multiplexer` (Python, 2025-04-27) does serial/TCP/file. Nothing on npm either. **The real answer to "open-source NMEA multiplexer" is kplex, Signal K, or AIS-catcher — not any repo by that name.**

### gpsd — yes it decodes AIS, no it cannot forward

Three separate questions, three different answers:

1. **Does gpsd handle AIVDM? Yes.** "gpsd is a monitor daemon that collects information from GPSes, differential-GPS radios, or **AIS receivers**" ([gpsd(8)](https://gpsd.gitlab.io/gpsd/gpsd.html)), and "The GPSD project ships an AIVDM/AIVDO sentence decoder as part of the daemon" ([AIVDM.html](https://gpsd.gitlab.io/gpsd/AIVDM.html)).
2. **Does `gpsdecode` decode AIS? Yes.** It "decode[s] GPS, RTCM or AIS streams into a readable format", producing JSON on stdout from stdin, explicitly supporting "AIVDM (the NMEA-derived sentence format used by AIS)" ([gpsdecode(1)](https://gpsd.gitlab.io/gpsd/gpsdecode.html)).
3. **Can it forward to a remote UDP host? No.** gpsd is a TCP server on 2947 speaking its own JSON protocol. Nothing in `gpsd(8)` offers a forwarding destination, and `gpspipe` has only `-o FILE` and `-s DEV` as output sinks — no network output ([gpspipe(1)](https://gpsd.gitlab.io/gpsd/gpspipe.html)). Note the asymmetry: gpsd *can read* from `udp://host:port` and `tcp://…` sources, but never write to one.

**Pass-through does work**, so a pipe gets you there. Per [gpsd_json(5)](https://gpsd.gitlab.io/gpsd/gpsd_json.html), with `raw=1` "gpsd reports the unprocessed NMEA or **AIVDM** data stream from whatever device is attached"; `raw=2` "reports the received data verbatim". The `gpspipe` flags map as `-r` → `WATCH_NMEA`, `-R` → `WATCH_RAW`; the man page on `-R`: "Causes super-raw (gps binary) data to be output. This will **forward exactly what the device sent**."

```bash
# NMEA-mode pass-through (includes !AIVDM), line-buffered, to aiscast
stdbuf -oL gpspipe -r | socat -u - UDP-SENDTO:ais.openwaters.io:10110

# verbatim device bytes instead
gpspipe -R -B | socat -u - UDP-SENDTO:ais.openwaters.io:10110
```

`stdbuf -oL` / `-B` matter — `gpspipe` block-buffers by default, which would batch sentences into large delayed datagrams.

**Version:** latest tag **release-3.27.5, 2026-01-14** ([GitLab tags](https://gitlab.com/gpsd/gpsd/-/tags)). Note GitLab's *Releases* page is misleading — its newest entry is literally named "GPSD **NOT** release 3.26"; go by tags. Debian package `gpsd`: **3.25-5+deb13u1 in trixie** (current stable), 3.27.5-3 in sid/forky.

**Guide advice:** if a reader already runs gpsd for a GPS and their AIS arrives on the same device, the pipe above works. Otherwise skip gpsd entirely — `socat` straight off the serial port is simpler and has one less moving part.

### Hardware routers

**Yacht Devices — only the YDNR-02.** The gateways expose "up to three server ports" configured by protocol + port + direction; clients connect *to them*. There is no destination-address field: "In the case of using UDP protocol, the number of devices or applications used the data port is not limited" ([YDWG-02 manual](https://www.yachtd.com/downloads/ydwg02.pdf) fw 1.71, 2024; [YDEN-02 manual](https://www.yachtd.com/downloads/yden02.pdf) fw 1.03, 2020). The **YDNR-02** router is the exception, from its [manual](https://www.yachtd.com/downloads/ydnr02.pdf) (fw 1.71, 2024, §V):

> "You can also enable **outgoing TCP/UDP connection to a specific IP address** on this page. For that, you need to configure Server #2 for a desired protocol and port number, check «outgoing connection» mark, and specify IP address."

Two caveats: the field takes an **IP address, not a hostname** — no DNS, so `ais.openwaters.io` would have to be resolved by hand and re-entered if it ever changes — and enabling it consumes Server #2 entirely. The **YDNU-02** is USB-only ("a virtual COM port (USB device class 2, subclass 2)"), so pair it with socat or AIS-catcher on the host.

**Actisense — no.** The W2K-1/W2K-2 Wi-Fi gateway's three data servers are configured with only Direction, Protocol and Port; there is no destination host field. From the [W2K-1/2 User Manual v1.2](https://actisense.com/wp-content/uploads/2025/09/W2K-1-2-User-Manual-v1.2-1.pdf): "UDP connections only require a Port Number", and the spec sheet says "IP support — Supports TCP & **UDP broadcast**". The NGT-1 has no network interface at all (USB or RS232/RS422). Both need a companion host running one of the software forwarders. **UNVERIFIED:** the newer WGX-1 gateway and "Actisense Cloud"/"Actisense Hub" were not evaluated.

**Digital Yacht — AISnet is the interesting one.** AISnet is a shore AIS base station with an RJ45 interface, marketed as "Perfect for use with Marine Traffic or AIS Live" ([support page](https://digitalyacht.support/aisnet/)). Internally it is a WIZnet serial-to-Ethernet module configured by a Java tool that exposes a **"Host IP" and "port"**. From the official FAQ ([How to configure AISNet to operate with a fixed IP address](https://digitalyacht.support/faqs/how-to-configure-aisnet-to-operate-with-a-fixed-ip-address/)): "You can use UDP mode, that is easy to setup. UDP broadcast and this requires you to enter a **'Host IP' = 255.255.255.255 – and a port = 2000**." Since the destination is a free-text Host IP field, a unicast remote address should work — broadcast is just their easy-mode example. **UNVERIFIED:** the AISnet manual PDF could not be retrieved (JS-gated download links), so it is unconfirmed whether the field accepts a hostname or only a dotted quad, and whether TCP-client mode exists. Firmware v01.50 — old kit.

The rest are listeners. **NavLink2:** "NavLink2 simultaneously transmits wireless NMEA data in both TCP and UDP modes… **UDP is a broadcast protocol**, where NavLink2 just sends out a continuous stream" (default 192.168.1.1:2000). **iAIS:** "The iAIS uses the **TCP/IP protocol**… IP Address 192.168.1.1, Port 2000" — its own Wi-Fi AP running a TCP server. **iKonvert:** USB/serial N2K gateway, no network stack.

**ShipModul MiniPlex-3E / 3Wi — yes**, and its manual addresses this use case directly ([MiniPlex-3 manual v3.16](https://www.shipmodul.com/download/miniplex-3-v3.16-en.pdf), p. 21):

> "UDP Broadcasts always remain within one network. If NMEA 0183 data needs to be transported across network boundaries or **over the Internet**, either UDP Mode Directed or TCP need to be used"

Directed mode requires "a destination IP address in the Destination field". Caveat: directed mode is unicast-to-one-host only and blinds the config tool, so set NMEA parameters first. **UNVERIFIED:** the newer MiniPlex-4 was not evaluated.

### canboat

[canboat/canboat](https://github.com/canboat/canboat) is an NMEA 2000 toolchain, not a forwarder: `n2kd` "multiplex[es] an analyzer-JSON stream to **TCP clients**", and the intended usage is a pipe (`actisense-serial -r <dev> | analyzer`). No remote UDP sender — pair it with socat. Very active: v8.0.0-beta3, 2026-08-13.

### One to avoid

**NMEA Router** ([arundaleais/nmearouter](https://github.com/arundaleais/nmearouter)) — a Windows VB6 app by the late Neal Arundale, source uploaded posthumously, **no license file**, last push 2019-07-26, and it still requires a reverse-engineered registration key. Do not recommend it.



## Recommendations for the guide

**Lead with AIS-catcher.** It is the most actively maintained project in this document by a wide margin (release v0.70 on 2026-06-19, commits daily), it reads a hardware serial AIS receiver directly with `-e`, it feeds UDP and HTTP and TCP simultaneously with repeated switches, and its `reset` setting solves the stale-socket problem that plagues the simpler tools. For most volunteer stations it replaces the "receiver software plus multiplexer" pair entirely.

**Second: docker-shipfeeder**, for anyone feeding more than one aggregator. Adding aiscast is literally one line:

```yaml
- UDP_FEEDS=ais.openwaters.io:10110
```

**Third: socat under systemd**, for readers who want to understand the mechanism or who have an oddball setup. One line, one unit file, `Restart=always`.

Then, in decreasing order of fit:

- **AvNav** for readers who want a Pi navigation server with a web UI — `AVNUdpWriter` takes a `host`, a `port` and a sentence filter, and the project is genuinely active. Underrated in most guides.
- **Signal K + `ais-forwarder`** for boats that already run Signal K. Note the plugin's age.
- **AIS Dispatcher** for people already using it — it forwards to arbitrary hosts fine — while flagging the missing licence and the `admin`/`admin` default.
- **kplex** only for readers who already run it, with issue #30 called out.
- **OpenCPN** as a "you already have this running on the boat computer" option, never as the recommended shore-station setup.

**Things to tell every reader regardless of tool:**

1. Use a stable device path (`/dev/serial/by-id/...`), not `/dev/ttyACM0`.
2. Run under systemd with `Restart=always`. Every tool here exits or wedges eventually.
3. AIS serial is normally **38400 baud**, not the 4800 of classic NMEA 0183.
4. Send whole sentences per datagram. aiscast splits datagrams on `\n`, so a sentence cut across two datagrams is lost.
5. UDP ingest at `ais.openwaters.io:10110` needs no token; `POST /v1/receive` does.

---

## Everything flagged UNVERIFIED

Collected in one place, since the guide should not assert any of these:

| Item | What is unverified |
|---|---|
| socat DNS re-resolution | The man page does not document whether `UDP-SENDTO` re-resolves a hostname after startup. Assume it does not. |
| socat `tee >(...)` fan-out | Standard bash idiom, not tested end-to-end, not a socat feature. |
| ser2net outbound UDP | `remaddr: "!host,port"` connect-back is documented for TCP and UDP, but no example shows serial → internet-host UDP, and the address quoting is my reading. |
| Signal K serial-in → udp-out | Derived from reading `udp.ts`/`simple.ts`/`providers.ts`; no doc or issue demonstrates it, and it was not run. Also unverified whether hand-added `host`/`outEvent` keys survive an admin-UI round trip. |
| docker-shipfeeder `UDP_FEEDS` `[:params]` | That trailing colon-separated params become AIS-catcher `-u` settings follows from `${feeds//:/ }`, but is not documented. |
| docker-shipfeeder UDP port 34994 | AIS-catcher always feeds it; nothing in the repo explains what listens there. |
| AIS Dispatcher licence | No licence file in the distribution, no terms on the site; `/terms`, `/terms-of-use`, `/privacy-policy` all 404. |
| AIS Dispatcher version | Package labelled 2.3; the shipped binary's embedded string says `v2.1`. No changelog published for the 2.x line. |
| AIS Dispatcher macOS on 2.x | No Darwin builds in the tarball and macOS appears only under the deprecated 1.2, but AISHub never says so outright. |
| AIS Dispatcher Docker images | `hbjoroy/ais-dispatcher` and `polyphemusnl/ais-dispatcher` on Docker Hub have no description or linked source; contents not inspected. |
| sxfeeder version | Deb says `1:1.0.1`; the binary's internal string says `1.0.2`; the changelog's newest entry is 2021-12-28 marked `UNRELEASED` while the deb was rebuilt 2024-06-20. |
| ShipXplorer exclusivity | The addcoverage page neither permits nor prohibits feeding other networks. Absence of a prohibition is not permission. |
| OpenCPN Android version | The download page states no Android version number. |
| OpenCPN community verdict | No cruisersforum.com threads retrieved (search budget exhausted, scrapers hit CAPTCHAs). All OpenCPN claims rest on the manual, source at `Release_5.14.0`, the releases API, and the issue tracker. |
| Node-RED `serial in` split char | Whether the node strips the split character from `msg.payload`. Does not matter for aiscast. |
| Python snippet | Illustrative; not executed against hardware. |
| AvNav current release version | GitHub releases are stale (2015); the project's own `releases.html` did not resolve, so the current version string is unknown. |
| Digital Yacht AISnet | The manual PDF is behind JS-gated download links. Unconfirmed whether "Host IP" accepts a hostname or only a dotted quad, and whether TCP-client mode exists. |
| Yacht Devices YDNR-02 | The outgoing-connection field is documented as taking an IP address; whether a hostname works was not tested. |
| Actisense WGX-1 / Actisense Cloud | Not evaluated. |
| ShipModul MiniPlex-4 | Not evaluated; only the MiniPlex-3 manual was read. |
| gpsd pipe one-liners | Flag semantics verified from man pages and source, but the commands were not executed. |
