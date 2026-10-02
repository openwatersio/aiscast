# Running a volunteer AIS station: service, ops, monitoring, multi-feeding

Scope: operating the station once it decodes. Excludes the AIS-catcher flag reference and NMEA forwarder internals, which are covered elsewhere. Verified 2026-08-22.

Every claim below carries a source URL. Anything I could not retrieve is called out inline and collected in [What I could not verify](#what-i-could-not-verify).

---

## 1. systemd

### 1.1 Where the unit actually comes from

The `.deb` does **not** ship a systemd unit. `debian/rules` installs only the binary, plugins, DBMS schemas, udev rules and bundled `librtlsdr`/`libhydrasdr`; `debian/postinst` only installs udev rules and runs `ldconfig` ([debian/rules](https://github.com/jvde-github/AIS-catcher/blob/main/debian/rules), [debian/postinst](https://github.com/jvde-github/AIS-catcher/blob/main/debian/postinst)). There is no `debian/*.service` file anywhere in the repo tree.

The unit is written at install time by the **installer script**, [`scripts/aiscatcher-install`](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install), which is what the documented one-liner runs. The script's constants fix the names and paths ([lines 87–99](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install)):

| Thing | Path |
|---|---|
| Service name | `ais-catcher.service` |
| Unit file | `/etc/systemd/system/ais-catcher.service` |
| Reboot-watchdog unit | `/etc/systemd/system/ais-catcher-reboot.service` |
| Reboot-watchdog script | `/usr/lib/ais-catcher/wait-and-reboot.sh` |
| JSON config | `/etc/AIS-catcher/config.json` |
| Command-line config | `/etc/AIS-catcher/config.cmd` |
| Managed-mode config | `/etc/AIS-catcher/aiscatcher.json` |
| Plugins / web assets / stat backup | `/etc/AIS-catcher/plugins`, `/etc/AIS-catcher/webassets`, `/etc/AIS-catcher/stat.bin` |
| Binary | `/usr/bin/AIS-catcher` |
| Install log | `/var/log/aiscatcher-install.log` |

There is **no `/etc/default/ais-catcher`**. Runtime options live in `/etc/AIS-catcher/config.cmd`, which the unit passes with systemd's `@file` argument-file syntax. Note the precedence, stated in the docs: "`config.json` takes precedence over `config.cmd`" ([Running as a Service](https://jvde-github.github.io/AIS-catcher-docs/usage/service/)).

The installer refuses to proceed if a legacy `aiscatcher.service` (no hyphen) is enabled, and tells you to `systemctl disable aiscatcher.service` first ([lines 417–420](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install)).

### 1.2 The generated unit, verbatim

The script builds the file as a string ([`setup_systemd_service()`](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install)). With defaults — user creation on, watchdog off, auto-restart on — the resulting `/etc/systemd/system/ais-catcher.service` is:

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

Two variations the script emits:

- **`--set-reboot-on-failure`** inserts `OnFailure=ais-catcher-reboot.service` directly after `After=network.target`, and sets `StartLimitBurst`/`StartLimitIntervalSec` to the requested values (defaults `3` and `1800`). The script comments why the gate matters: "`OnFailure=` is only meaningful when StartLimitBurst > 0; with burst=0 the service restarts indefinitely via Restart=always and OnFailure would fire on every crash."
- **`--managed` (`-M`)** replaces `ExecStart` with `/usr/bin/AIS-catcher -E /etc/AIS-catcher/aiscatcher.json 0.0.0.0:8118`, the control-hub dashboard.
- **`--no-user`** (used by the official Docker image) omits the `User=`/`Group=`/`SupplementaryGroups=` block entirely and runs as root.

The `aiscatcher` account is a locked system user, added to the groups that grant SDR and serial access ([lines 779–798](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install)):

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin aiscatcher
usermod -a -G plugdev aiscatcher
usermod -a -G dialout aiscatcher
```

`/etc/AIS-catcher` is then `chown -R aiscatcher:aiscatcher`.

### 1.3 The reboot watchdog

`ais-catcher-reboot.service` is a one-shot escape hatch for hardware that has wedged past software recovery — the installer's own summary calls it "Useful for recovering from unresolvable USB device failures."

```ini
[Unit]
Description=Reboot system due to repeated AIS-catcher failures

[Service]
Type=simple
ExecStart=/usr/lib/ais-catcher/wait-and-reboot.sh
```

`/usr/lib/ais-catcher/wait-and-reboot.sh`, verbatim from the installer heredoc:

```bash
#!/bin/bash
# Schedule a reboot in 5 minutes; cancel it if AIS-catcher recovers in time.
# Checks every 10 seconds; cancels the pending shutdown if the service comes
# back active (e.g. after manual "systemctl reset-failed + start" intervention).
# To cancel a pending reboot manually: shutdown -c
REBOOT_MINUTES=5
shutdown -r "+${REBOOT_MINUTES}" "AIS-catcher failed repeatedly - rebooting in ${REBOOT_MINUTES} minutes. To cancel: shutdown -c"
for i in $(seq 30); do
    sleep 10
    if systemctl is-active --quiet ais-catcher.service; then
        shutdown -c
        exit 0
    fi
done
```

It is **off by default**. Toggling does not require reinstalling — the script edits the unit in place and reloads:

```bash
sudo aiscatcher-install --set-reboot-on-failure          # 3 restarts in 30 min
sudo aiscatcher-install --set-reboot-on-failure 5 3600   # 5 restarts in 60 min
sudo aiscatcher-install --unset-reboot-on-failure
sudo aiscatcher-install --set-auto-restart               # Restart=always
sudo aiscatcher-install --unset-auto-restart             # Restart=no, for debugging a crash loop
```

### 1.4 Everyday commands

Straight from the installer's closing summary and the [service docs](https://jvde-github.github.io/AIS-catcher-docs/usage/service/):

```bash
sudo systemctl status ais-catcher.service
sudo systemctl start ais-catcher.service
sudo systemctl stop ais-catcher.service
sudo systemctl restart ais-catcher.service       # after editing config.cmd or config.json
sudo systemctl enable ais-catcher.service        # start at boot
sudo systemctl disable ais-catcher.service
sudo journalctl -u ais-catcher.service -f

sudo nano /etc/AIS-catcher/config.cmd            # command-line options
sudo nano /etc/AIS-catcher/config.json           # JSON config (wins over config.cmd)
```

Recovering from a crash-loop lockout, which is what `StartLimitBurst` produces once armed:

```bash
sudo systemctl reset-failed ais-catcher.service && sudo systemctl start ais-catcher.service
sudo shutdown -c                                 # abort a pending watchdog reboot
```

### 1.5 Generic hardened unit for a forwarder (socat, custom script)

The community baseline is wiedehopf's [Generic systemd service](https://github.com/wiedehopf/adsb-wiki/wiki/Generic-systemd-service), which is deliberately minimal — `Type=simple`, `Restart=always`, `RestartSec=10`, options in an `EnvironmentFile` under `/etc/default/`, and `SyslogIdentifier` so `journalctl -t` works. That is the right shape. Below it is extended with the sandboxing directives that cost nothing on a single-purpose box.

`/etc/default/ais-forward`:

```ini
# Fan one local NMEA stream out to a second destination.
OPTS="-u UDP4-RECV:10110,reuseaddr UDP4-SENDTO:ais.example.org:12345"
```

`/etc/systemd/system/ais-forward.service`:

```ini
[Unit]
Description=AIS NMEA forwarder
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=10

[Service]
Type=simple
EnvironmentFile=/etc/default/ais-forward
ExecStart=/usr/bin/socat $OPTS
SyslogIdentifier=ais-forward

Restart=always
RestartSec=10s
# Back off instead of hammering a down aggregator.
RestartSteps=5
RestartMaxDelaySec=5min

# Least privilege. DynamicUser gives a transient uid/gid with no home and no shell.
DynamicUser=yes
# Drop DynamicUser and use these instead if the process needs a real account:
#User=aisforward
#Group=aisforward

NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

# Bound the damage from a leak or a runaway loop.
MemoryMax=128M
TasksMax=64

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ais-forward.service
sudo journalctl -u ais-forward -n 100 -f
sudo systemd-analyze security ais-forward.service   # scores the hardening above
```

Caveats worth stating in the guide:

- `PrivateDevices=yes` and `ProtectSystem=strict` will break anything that touches the SDR or a serial port. Use them for a pure network forwarder only; for a process that opens `/dev/ttyACM0` or a USB SDR, drop `PrivateDevices` and add `SupplementaryGroups=dialout plugdev` (which is exactly what AIS-catcher's own unit does).
- `RestartSteps=`/`RestartMaxDelaySec=` require systemd v254+. On Debian 12 (systemd 252) omit them and keep a flat `RestartSec=30`.
- **`WatchdogSec=` only works if the program calls `sd_notify(WATCHDOG=1)`.** Setting it on a process that does not is a guaranteed kill-loop. AIS-catcher is not a notify-type service, so do not add `Type=notify` or `WatchdogSec=` to `ais-catcher.service`. Per-service watchdogs are a different mechanism from the hardware watchdog in §6 ([systemd.service](https://man7.org/linux/man-pages/man5/systemd.service.5.html)).

---

## 2. Docker

### 2.1 The sdr-enthusiasts house style

The canonical reference is [`sdr-enthusiasts/docker-install/sample-docker-compose.yml`](https://github.com/sdr-enthusiasts/docker-install/blob/main/sample-docker-compose.yml), the file the community install script drops next to your `.env`. Distilled, the house conventions are:

| Convention | Value | Why |
|---|---|---|
| Restart policy | `restart: always` on every service | survives reboots and daemon restarts |
| SDR passthrough | `device_cgroup_rules: - "c 189:* rwm"` **plus** `volumes: - /dev/bus/usb:/dev/bus/usb` | 189 is the USB char-device major; the bind mount plus the wildcard cgroup rule survives re-plug without `privileged: true` |
| Never | `privileged: true` | not used anywhere in the sample |
| Ephemeral writes | `tmpfs: - /run:exec,size=64M`, `- /tmp:size=128M`, `- /var/log:size=32M` | keeps container churn off the SD card |
| Self-healing | `labels: - "autoheal=true"` + a `willfarrell/autoheal` service bind-mounting `/var/run/docker.sock` | restarts any labelled container whose healthcheck goes unhealthy |
| Updates | a `watchtower` service, `WATCHTOWER_POLL_INTERVAL=86400`, `WATCHTOWER_CLEANUP=true`, `WATCHTOWER_ROLLING_RESTART=true` | |
| Timezone | `- "/etc/localtime:/etc/localtime:ro"`, `- "/etc/timezone:/etc/timezone:ro"` | |

Two details worth copying deliberately:

- The `shipfeeder` README example mounts `/dev:/dev:rw`, but the `docker-install` sample mounts the narrower `/dev/bus/usb:/dev/bus/usb`. Prefer the narrow one — it is what the rest of the fleet uses, and the official AIS-catcher docs use it too.
- The house style has **no `logging:` driver stanza**. Log growth is handled by `tmpfs` inside the container and by the host daemon default outside it. Docker's default `json-file` driver has **no size limit**, so on a Pi you should add one yourself (§4.2). Treat that as a recommendation, not house style.

A candid comment in the same sample file underlines the port-exposure rule in §5: for the FR24 feeder, "Please be careful not to expose the status website to the internet as users may be able to start/stop/change the service from there."

`docker-install` also blacklists the DVB-T kernel drivers that otherwise claim the dongle — `rtl2832_sdr`, `dvb_usb_rtl2832u`, `dvb_usb_rtl28xxu`, `dvb_usb_v2`, `r820t`, `rtl2830`, `rtl2832`, `rtl2838`, `dvb_core` — writing both `blacklist X` and `install X /bin/false` into `/etc/modprobe.d/exclusions-rtl2832.conf`, then `depmod -a` ([docker-install.sh](https://github.com/sdr-enthusiasts/docker-install/blob/main/docker-install.sh)).

### 2.2 The official AIS-catcher image

`ghcr.io/jvde-github/ais-catcher`, tags `latest` (release) and `edge` (`main`). The [Dockerfile](https://github.com/jvde-github/AIS-catcher/blob/main/Dockerfile) is `debian:bookworm-slim` running the same installer with `--package --no-systemd --no-user`, so **the container runs as root and has no systemd inside** — restarts are Docker's job, not systemd's.

Documented invocations ([Docker install docs](https://jvde-github.github.io/AIS-catcher-docs/installation/docker/)):

```bash
# Managed mode (browser dashboard on :8118)
docker run --rm -it --network=host \
  --device-cgroup-rule='c 189:* rmw' \
  -v /dev/bus/usb:/dev/bus/usb \
  -v ais-config:/config \
  ghcr.io/jvde-github/ais-catcher:edge -E /config/config.json 127.0.0.1:8118

# Manual mode
docker run --rm -it --pull always --device /dev/bus/usb \
  ghcr.io/jvde-github/ais-catcher:latest <options>
```

The docs note that `--device-cgroup-rule` plus the `/dev/bus/usb` bind mount "keep working when a device is re-plugged", that a serial receiver wants `--device /dev/ttyACM0` instead, and that a web viewer on `-N 8100` needs `-p 8100:8100` or `--network=host`.

### 2.3 A complete compose file

This is `docker-shipfeeder` (the multi-aggregator path) in house style, with logging caps and a healthcheck added. Pair it with a `.env` holding the secrets.

`docker-compose.yml`:

```yaml
services:
  shipfeeder:
    image: ghcr.io/sdr-enthusiasts/docker-shipfeeder
    container_name: shipfeeder
    hostname: shipfeeder
    restart: always
    labels:
      - "autoheal=true"
    environment:
      # --- receiver ---
      - SDR_TYPE=RTLSDR
      - RTLSDR_DEVICE_SERIAL=${RTLSDR_DEVICE_SERIAL}
      - RTLSDR_DEVICE_GAIN=${RTLSDR_DEVICE_GAIN}
      - RTLSDR_DEVICE_PPM=${RTLSDR_DEVICE_PPM}
      - RTLSDR_DEVICE_BANDWIDTH=192K
      - AISCATCHER_DECODER_AFC_WIDE=on
      # --- station identity and web viewer ---
      - STATION_NAME=${STATION_NAME}
      - FEEDER_LAT=${FEEDER_LAT}
      - FEEDER_LONG=${FEEDER_LONG}
      - SITESHOW=on
      - REALTIME=on
      - STATION_HISTORY=3600
      - BACKUP_INTERVAL=5
      - PROMETHEUS_ENABLE=on
      # 0 silences AIS-catcher; 2 prints a summary every 60s (default)
      - VERBOSE_LOGGING=2
      - TZ=${FEEDER_TZ}
      # --- aggregators: delete the lines you are not feeding ---
      - AISCATCHER_SHAREDATA=true
      - AISHUB_UDP_PORT=${AISHUB_UDP_PORT}
      - MARINETRAFFIC_UDP_PORT=${MARINETRAFFIC_UDP_PORT}
      - VESSELFINDER_UDP_PORT=${VESSELFINDER_UDP_PORT}
      - SHIPXPLORER_SHARING_KEY=${SHIPXPLORER_SHARING_KEY}
      - SHIPXPLORER_SERIAL_NUMBER=${SHIPXPLORER_SERIAL_NUMBER}
      - AIRFRAMES_STATION_ID=${AIRFRAMES_STATION_ID}
      - APRSFI_FEEDER_KEY=${APRSFI_FEEDER_KEY}
      - APRSFI_STATION_ID=${APRSFI_STATION_ID}
      # Anything not in the built-in table:
      - UDP_FEEDS=${UDP_FEEDS}
    ports:
      - 8100:80          # web viewer — LAN ONLY, never port-forward (see §5)
      - 9988:9988/udp    # only if another instance feeds this one
    device_cgroup_rules:
      - "c 189:* rwm"
    volumes:
      - /dev/bus/usb:/dev/bus/usb
      - /opt/ais/shipfeeder/data:/data
      - /etc/localtime:/etc/localtime:ro
      - /etc/timezone:/etc/timezone:ro
    tmpfs:
      - /tmp:rw,nosuid,nodev,noexec,relatime,size=64M
      - /var/log:size=32M
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost/api/stat.json || exit 1"]
      interval: 60s
      timeout: 10s
      retries: 3
      start_period: 120s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

  autoheal:
    image: willfarrell/autoheal
    container_name: autoheal
    restart: always
    environment:
      - AUTOHEAL_CONTAINER_LABEL=autoheal
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    logging:
      driver: json-file
      options:
        max-size: "1m"
        max-file: "2"

  watchtower:
    image: nickfedor/watchtower
    container_name: watchtower
    restart: always
    environment:
      - TZ=${FEEDER_TZ}
      - WATCHTOWER_CLEANUP=true
      - WATCHTOWER_POLL_INTERVAL=86400
      - WATCHTOWER_ROLLING_RESTART=true
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    logging:
      driver: json-file
      options:
        max-size: "1m"
        max-file: "2"
```

`.env` (mode `600`), following the [README's sample](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/config-examples/.env.sample):

```ini
FEEDER_LAT=xx.xxxxxx
FEEDER_LONG=yy.yyyyyy
FEEDER_TZ=America/New_York
STATION_NAME=My&nbsp;Station&nbsp;Name
RTLSDR_DEVICE_SERIAL=DEVICE-SERIAL
RTLSDR_DEVICE_GAIN=33
RTLSDR_DEVICE_PPM=0
AISHUB_UDP_PORT=xxxxx
MARINETRAFFIC_UDP_PORT=xxxxx
VESSELFINDER_UDP_PORT=xxxxx
SHIPXPLORER_SHARING_KEY=xxxxxxxxxxxxxxxxxxx
SHIPXPLORER_SERIAL_NUMBER=SXTRPI00xxxx
AIRFRAMES_STATION_ID=XX-STATIONNAME-AIS
APRSFI_FEEDER_KEY=xxxxxxx
APRSFI_STATION_ID=MYCALL
UDP_FEEDS=
```

Notes on the additions I made beyond house style:

- The healthcheck path is real. `/api/stat.json` is one of the routes in AIS-catcher's web viewer route table ([WebViewer.cpp](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Web/WebViewer.cpp)). It only answers if the web server is enabled — in `shipfeeder` it is on port 80 inside the container unless `DISABLE_WEBSITE=true`. If you disable the website, drop the healthcheck or it will autoheal-loop forever.
- Raspberry Pi 5 and ShipXplorer's `sxfeeder`: that binary is armhf-only with 4 KB pages and dies on the Pi 5's default 16 KB-page kernel. Check with `getconf PAGE_SIZE`; if it is not 4096, either add `kernel=kernel8.img` to `/boot/firmware/config.txt` and reboot, or feed ShipXplorer over UDP with `SHIPXPLORER_UDP_PORT` instead of a sharing key ([README](https://github.com/sdr-enthusiasts/docker-shipfeeder#working-around-shipxplorer-issues-on-raspberry-pi-5)).

---

## 3. SD card wear and reliability on a Raspberry Pi

An AIS station writes continuously for years. Ranked by payoff:

### 3.1 Boot from USB SSD or NVMe (best single fix)

Removes the SD card from the write path entirely.

```bash
sudo apt update && sudo apt full-upgrade
sudo raspi-config
# Advanced Options > Boot Order > pick an option that includes NVMe (or USB)
```

The Pi documentation states this directly: "Under `Advanced Options` > `Boot Order`, specify an option that includes NVMe. It will then write these changes to the bootloader" ([NVMe SSD boot](https://www.raspberrypi.com/documentation/computers/raspberry-pi.html#nvme-ssd-boot)). NVMe is boot mode `6` in `BOOT_ORDER`; USB-MSD is `04`. Confirm the drive is seen first with `ls -l /dev/nvme*`.

### 3.2 Choose an A2 card if you must use SD

Official Raspberry Pi SD cards carry "Speed Class ratings: C10, U3, V30, A2" and support command queueing ([About Raspberry Pi SD cards](https://www.raspberrypi.com/documentation/accessories/sd-cards.html)). Matching those ratings is a reasonable buying rule. Random-IOPS figures the docs publish, on 4 kB random data:

| Model | Interface | Read | Write |
|---|---|---|---|
| Pi 4 | DDR50 | 3,200 IOPS | 1,200 IOPS |
| Pi 5 | SDR104 | 5,000 IOPS | 2,000 IOPS |

Sanity-check a suspect card before blaming software ([simple sd-card test](https://github.com/wiedehopf/adsb-wiki/wiki/simple-sd-card-test)):

```sh
time for i in {1..5}; do
  sudo dd if=/dev/urandom of=/run/testFile bs=1M count=50 status=none
  cp /run/testFile /tmp
  sync
  sudo tee /proc/sys/vm/drop_caches <<< "3" > /dev/null
  diff -s /run/testFile /tmp/testFile
done
```

It should report the files identical five times. If they differ, the card (or much less likely the Pi) is bad.

### 3.3 Get swap off the card

```bash
sudo systemctl disable --now dphys-swapfile.service
```

Better, keep swap but put it in compressed RAM ([zram-swap](https://github.com/wiedehopf/adsb-wiki/wiki/zram-swap)), which "prevents swap from wearing out the sd-card" and "is faster than disk backed swap":

```bash
sudo bash -c "$(wget -O - https://github.com/wiedehopf/adsb-scripts/raw/master/zram-swap-install.sh)"
sudo systemctl disable dphys-swapfile.service
zramctl                                   # verify
```

### 3.4 log2ram

Mounts `/var/log` as tmpfs and syncs to disk daily and on clean shutdown ([log2ram](https://github.com/azlux/log2ram)). Its own framing: "any writing of the log to the `/var/log` folder will not actually be written to disk … but directly to RAM."

```bash
. /etc/os-release
sudo wget -O /usr/share/keyrings/azlux-archive-keyring.gpg https://azlux.fr/repo.gpg
sudo tee /etc/apt/sources.list.d/azlux.list >/dev/null <<EOF
deb [signed-by=/usr/share/keyrings/azlux-archive-keyring.gpg] http://packages.azlux.fr/debian/ $VERSION_CODENAME main
EOF
sudo apt update && sudo apt install log2ram
sudo reboot
```

On Debian 13 (trixie) the upstream README adds an apt pin, because the Debian archive ships a worse log2ram ([bug 1122989](https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=1122989)):

```bash
sudo tee /etc/apt/preferences.d/log2ram.pref >/dev/null <<EOF
Package: log2ram
Pin: origin packages.azlux.fr
Pin-Priority: 1001
EOF
```

Verify and tune (`SIZE`, default `128M`, in `/etc/log2ram.conf`):

```bash
systemctl status log2ram
df -hT | grep log2ram
```

Caveat for a guide: with log2ram, anything not yet synced is lost on a power cut. That is usually fine for an AIS station and is exactly the trade you want, but say so.

### 3.5 Read-only root via overlayfs (kiosk-grade)

The Pi docs describe two independent switches ([Enable or disable overlay file system](https://www.raspberrypi.com/documentation/computers/configuration.html#enable-or-disable-overlay-file-system)): "Set the root file system to read-only. This uses a temporary overlay stored in RAM. Any changes made to files outside of `/boot` while the system is running aren't permanently saved and disappear when you power off or reboot"; and "Write-protect the boot partition."

```bash
sudo raspi-config
# 4 Performance Options > P2 Overlay File System
#   -> "Would you like the overlay file system to be enabled?"        Yes
#   -> "Would you like the boot partition to be write-protected?"     Yes
sudo reboot
```

This is the strongest protection and the most annoying: **AIS-catcher's `stat.bin` backup, aggregator state, and any config edit are lost on reboot** unless you mount a writable partition for them and disable the overlay to make changes. Reasonable for a remote unattended site; overkill for a station you tinker with.

### 3.6 Extra tmpfs mounts

Add to `/etc/fstab` if you are not using log2ram:

```
tmpfs  /tmp      tmpfs  defaults,noatime,nosuid,nodev,size=64M   0 0
tmpfs  /var/tmp  tmpfs  defaults,noatime,nosuid,nodev,size=32M   0 0
tmpfs  /var/log  tmpfs  defaults,noatime,nosuid,nodev,size=64M   0 0
```

Then find out what is actually writing, rather than guessing ([diagnose disk usage / IO](https://github.com/wiedehopf/adsb-wiki/wiki/diagnose-disk-usage-IO-inotify-IOPS)):

```bash
curl -o inotify-debug.sh https://raw.githubusercontent.com/wiedehopf/adsb-scripts/master/inotify-debug.sh
bash inotify-debug.sh
```

---

## 4. Logs

### 4.1 journald

Unbounded journals are a common cause of a full SD card. The defaults are proportional, not absolute: `SystemMaxUse=` "defaults to 10% … of the size of the respective file system … capped to 4G", and journald honours both `SystemMaxUse=` and `SystemKeepFree=`, "using the smaller of the two values" ([journald.conf(5)](https://man7.org/linux/man-pages/man5/journald.conf.5.html)). On a 32 GB card that is 3.2 GB of journal you did not ask for.

`/etc/systemd/journald.conf.d/00-station.conf`:

```ini
[Journal]
Storage=persistent
SystemMaxUse=200M
SystemKeepFree=500M
SystemMaxFileSize=20M
SystemMaxFiles=10
MaxRetentionSec=2week
RuntimeMaxUse=32M
# Cap a chatty service instead of letting it flood
RateLimitIntervalSec=30s
RateLimitBurst=1000
```

```bash
sudo systemctl restart systemd-journald
journalctl --disk-usage
sudo journalctl --vacuum-size=200M      # reclaim now
sudo journalctl --vacuum-time=14d
```

If you would rather keep no persistent journal at all — sensible with overlayfs — set `Storage=volatile` and let only `RuntimeMaxUse=` apply. Note the man page's warning that only *archived* files are deleted, so live usage can briefly exceed the cap.

### 4.2 Docker logs

Docker's default `json-file` driver is unbounded. Set a host-wide default in `/etc/docker/daemon.json` so you cannot forget it on a new container:

```json
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
```

```bash
sudo systemctl restart docker      # existing containers keep their old settings until recreated
```

Per-service `logging:` blocks (as in §2.3) override this and are worth keeping for clarity.

### 4.3 logrotate for anything writing real files

The installer writes `/var/log/aiscatcher-install.log`, and a custom forwarder may write its own file. `/etc/logrotate.d/ais-station`:

```
/var/log/ais-*.log {
    weekly
    rotate 4
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
```

### 4.4 AIS-catcher's own verbosity

The generated unit runs `-G level debug -o 0`, i.e. **console output off**, so a default systemd install is quiet by design and the journal stays small. Two knobs matter if you turn it up:

| Setting | Effect | Source |
|---|---|---|
| `-o 0` | silence message output | [unit `ExecStart`](https://github.com/jvde-github/AIS-catcher/blob/main/scripts/aiscatcher-install) |
| `-v 60` / `"verbose_time":60` | one summary line per 60 s | default `config.json` written by the installer |
| `VERBOSE_LOGGING` (Docker) | `0`–`5` maps to `-o`; any other non-empty string means `-o 2`; **`0` silences it** | [shipfeeder README](https://github.com/sdr-enthusiasts/docker-shipfeeder#sdr-and-receiver-related-variables) |

The trap to warn about: raising `-o` to a per-message format on a busy station writes one journal line per AIS message. In a good coverage area that is millions of lines a day. Keep `-o 0` and use the web viewer or `/metrics` for visibility instead.

---

## 5. Remote access

### 5.1 The rule

**Never port-forward the AIS-catcher web UI (8100) or the managed dashboard (8118) to the internet.** This is upstream's own position, not just good practice — the docs say the dashboard "is intended for use on your local network. If you want to expose your station's data publicly, do not expose the dashboard — share your data via the community feed or add a web viewer instead" ([Remote Access and Security](https://jvde-github.github.io/AIS-catcher-docs/managed/remote-access/)). The same page notes that binding to anything other than `127.0.0.1` forces a password on first access.

The `docker-install` sample makes the identical point about a feeder's status page: "Please be careful not to expose the status website to the internet as users may be able to start/stop/change the service from there" ([sample-docker-compose.yml](https://github.com/sdr-enthusiasts/docker-install/blob/main/sample-docker-compose.yml)).

The web viewer has no authentication of its own and exposes `/api/log`, `/api/decode` and the full ship database. Put it behind an overlay network, not a NAT rule.

### 5.2 Tailscale (recommended)

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
sudo tailscale set --ssh            # enable Tailscale SSH on this node
```

The one-line installer is the command Tailscale publishes on its [Linux download page](https://tailscale.com/download/linux). Note that current docs use `tailscale set --ssh` rather than the older `tailscale up --ssh` ([Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh)).

SSH access needs **two** things in the tailnet policy file — a network grant and an `ssh` rule. The default policy from the docs:

```json
{
  "grants": [
    { "src": ["*"], "dst": ["*"], "ip": ["*"] }
  ],
  "ssh": [
    {
      "action": "check",
      "src": ["autogroup:member"],
      "dst": ["autogroup:self"],
      "users": ["autogroup:nonroot", "root"]
    }
  ]
}
```

For a station, tighten it: tag the Pi (`tailscale up --advertise-tags=tag:ais-station`), then grant only your own account access to `tag:ais-station`, and use `"action": "check"` so re-authentication is periodically required. Reaching the web UI is then just `http://<tailscale-ip>:8100` with no forwarded ports at all.

### 5.3 Alternatives

| Option | Shape | Good for | Watch out for |
|---|---|---|---|
| **Cloudflare Tunnel** | `cloudflared` daemon dials out; no inbound ports | publishing a *read-only* map to the public web, with Cloudflare Access in front | putting the writable dashboard behind it defeats §5.1; free tier terms restrict heavy non-HTML traffic |
| **ZeroTier** | peer-to-peer L2/L3 overlay, self-hostable controller | people who want a flat LAN across sites | manual auth of each member; no built-in SSH broker |
| **WireGuard (plain)** | your own tunnel to a VPS or home router | no third-party dependency | you own key rotation, NAT traversal, and uptime |
| **ssh + port forward** | `ssh -L 8100:localhost:8100 pi@station` over an already-secured SSH path | one-off debugging | only if SSH itself is not exposed to the internet; if it is, keys-only, no passwords, non-standard port, and fail2ban |

Whatever you pick, still run `ufw`/`nftables` with a default-deny inbound policy and allow only the overlay interface, so a misconfigured router cannot silently expose 8100.

---

## 6. Watchdog and self-healing

### 6.1 Hardware watchdog

Verified: `CONFIG_BCM2835_WDT=y` in **all** current Raspberry Pi kernel defconfigs — `bcmrpi_defconfig`, `bcm2709_defconfig`, `bcm2711_defconfig` and `bcm2712_defconfig` (the Pi 5) — on the `rpi-6.12.y` branch ([raspberrypi/linux](https://github.com/raspberrypi/linux/tree/rpi-6.12.y/arch/arm64/configs)). It is built in, not a module, so `/dev/watchdog0` exists on a stock Raspberry Pi OS with nothing to load and no `dtoverlay` needed.

Hand it to systemd in `/etc/systemd/system.conf.d/10-watchdog.conf`:

```ini
[Manager]
RuntimeWatchdogSec=15
RebootWatchdogSec=2min
```

```bash
sudo systemctl daemon-reexec
# confirm:
journalctl -b | grep -i watchdog
wdctl                       # shows the device, timeout, and identity
```

What this actually does, from [systemd-system.conf(5)](https://man7.org/linux/man-pages/man5/systemd-system.conf.5.html): "If `RuntimeWatchdogSec=` is set to a non-zero value, the watchdog hardware (`/dev/watchdog0` …) will be programmed to automatically reboot the system if it is not contacted within the specified timeout interval. The system manager will ensure to contact it at least once in half the specified timeout interval." Defaults are `RuntimeWatchdogSec=0` (off) and `RebootWatchdogSec=10min`.

The bcm2835 watchdog's maximum timeout is roughly 15 s, so `RuntimeWatchdogSec=15` is about the ceiling; systemd picks "the closest available timeout" if you ask for more. This recovers a **kernel-level** hang only — it does nothing about AIS-catcher crashing or going quiet, which is what §6.2 is for.

### 6.2 Restart on no data

Neither AIS-catcher nor `shipfeeder` ships a stale-feed watchdog, so this is a pattern you assemble. The ingredients are solid, though: `/api/stat.json` (and its alias `/stat.json`) is served by the web viewer and `buildStatJSON()` writes both a `msg_rate` and a monotonic `received` counter, alongside `run_time`, `vessel_count` and `vessel_max` ([WebViewer.cpp](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Web/WebViewer.cpp)).

`msg_rate` is the wrong thing to alarm on — a quiet harbour at 03:00 legitimately reads zero. Watch the **monotonic `received` counter** and fire only when it has not moved across several checks.

`/usr/local/bin/ais-staleness-check`:

```bash
#!/bin/bash
# Restart AIS-catcher if its lifetime message counter has not advanced
# across STALE_CYCLES consecutive checks. Uses the counter, not msg_rate,
# so a genuinely quiet period does not trigger a restart.
set -uo pipefail

URL="http://127.0.0.1:8100/api/stat.json"
STATE=/var/lib/ais-staleness/state
STALE_CYCLES=6                      # with a 5-min timer, ~30 min of silence

mkdir -p "$(dirname "$STATE")"

now=$(curl -sf --max-time 10 "$URL" | jq -r '.received // empty')
if [[ -z "$now" ]]; then
    logger -t ais-staleness "stat.json unreachable; restarting ais-catcher"
    systemctl restart ais-catcher.service
    : > "$STATE"
    exit 0
fi

read -r prev count < "$STATE" 2>/dev/null || { prev=-1; count=0; }

if [[ "$now" == "$prev" ]]; then
    count=$((count + 1))
else
    count=0
fi

if (( count >= STALE_CYCLES )); then
    logger -t ais-staleness "counter stuck at $now for $count checks; restarting ais-catcher"
    systemctl restart ais-catcher.service
    count=0
fi

echo "$now $count" > "$STATE"
```

```bash
sudo chmod 755 /usr/local/bin/ais-staleness-check
sudo apt install jq
```

`/etc/systemd/system/ais-staleness.service`:

```ini
[Unit]
Description=Restart AIS-catcher if its message counter goes stale
After=ais-catcher.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/ais-staleness-check
```

`/etc/systemd/system/ais-staleness.timer`:

```ini
[Unit]
Description=Check AIS-catcher for a stalled message counter

[Timer]
OnBootSec=10min
OnUnitActiveSec=5min
AccuracySec=30s

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ais-staleness.timer
systemctl list-timers ais-staleness.timer
```

The web server must be enabled for this to work — set `"active": true` in the `server` block of `/etc/AIS-catcher/config.json`, since the installer's default config ships `"active":false` with `"port":"8100"`.

Under Docker, the equivalent is the healthcheck plus `autoheal` in §2.3 — same idea, less code. `willfarrell/autoheal` restarts any container labelled `autoheal=true` whose healthcheck reports unhealthy.

Layer the three mechanisms rather than choosing one:

| Failure | Caught by |
|---|---|
| process crash / exit | `Restart=always`, `RestartSec=10` in the unit |
| repeated crash loop | `StartLimitBurst` + `OnFailure=ais-catcher-reboot.service` |
| process alive but deaf (USB wedged, tuner stuck) | the staleness timer above / Docker healthcheck + autoheal |
| kernel hang, total lockup | `RuntimeWatchdogSec=15` + bcm2835_wdt |

### 6.3 USB dongles dropping off the bus

Two distinct problems get conflated:

**The kernel steals the device.** The DVB-T drivers claim RTL-SDR dongles on sight. Blacklist them ([Testing rtl-sdr USB receivers](https://github.com/wiedehopf/adsb-wiki/wiki/Testing-rtl-sdr-USB-receivers---DVB-T-sticks)):

```bash
echo -e 'blacklist rtl2832\nblacklist dvb_usb_rtl28xxu\nblacklist rtl8192cu\nblacklist rtl8xxxu\n' \
  | sudo tee /etc/modprobe.d/blacklist-rtl-sdr2.conf
sudo reboot
```

`docker-install` does the same, more thoroughly, and additionally writes `install <module> /bin/false` lines so nothing can pull them in on demand.

**The dongle browns out or wedges.** Symptoms are `RTLSDR: cannot open device` and startup failures after the device disappears from `lsusb` ([issue #374](https://github.com/jvde-github/AIS-catcher/issues/374), [issue #514](https://github.com/jvde-github/AIS-catcher/issues/514)). The AIS-catcher docs' own USB-throughput advice is to test with `rtl_test -s 1536000` and drop to a lower sample rate if you see lost data ([Troubleshooting](https://jvde-github.github.io/AIS-catcher-docs/advanced/troubleshooting/)):

```bash
rtl_test -s 1536000       # if this shows lost samples:
AIS-catcher -s 288000     # or 2304000 if the machine can take it
```

Fixes in order of preference:

1. **A good power supply and a powered hub.** RTL-SDR dongles plus an LNA and bias-tee draw more than a marginal Pi supply likes. The `docker-install` script's own closing warning is that externally powered USB devices can leave a Pi "stuck in the 'off' state" on reboot, so unplug them before rebooting a remote box you cannot physically reach.
2. **Power-cycle the port with `uhubctl`** rather than a full reboot. On a Pi the built-in root hubs are *ganged*, which is the detail people get wrong ([uhubctl README](https://github.com/mvp/uhubctl)): on the B+/2B/3B all four ports are controlled by port 2 of hub `1-1`; on the 4B they are ganged as hub `1-1` (USB2) and hub `2` (USB3), and the VL805 firmware must be newer than `00137ad` (`sudo rpi-eeprom-update`) for switching to work at all; on the **Pi 5 each of the four onboard hubs advertises per-port switching "but this is not true. In reality, Raspberry Pi 5 all 4 ports are ganged together in one group"**, so cutting VBUS means `uhubctl -l 2 -a 0` *and* `uhubctl -l 4 -a 0`. In other words, on a Pi you cannot power-cycle one dongle without cycling every USB device. A **powered external hub with real per-port switching** (see uhubctl's compatibility table — the UUGear MEGA4 is purpose-built for the Pi 4B) is the way to reset one SDR in isolation.
3. **`usbreset`** issues a `USBDEVFS_RESET` ioctl to re-enumerate without cutting power. It is bundled in Debian's `usbutils` package. Milder than a power cycle and often enough:
   ```bash
   sudo apt install usbutils
   lsusb                         # note Bus 001 Device 004: ID 0bda:2838 ...
   sudo usbreset 0bda:2838
   ```
4. **Reboot as last resort** — which is exactly what `ais-catcher-reboot.service` is for, and why the installer describes it as being for "unresolvable USB device failures".

Also pin the device by serial, not by index, so a re-enumeration does not silently hand AIS-catcher the wrong dongle on a multi-SDR box. `rtl_eeprom` renames a dongle's serial; `RTLSDR_DEVICE_SERIAL` / `-d <serial>` selects it.

---

## 7. Monitoring station uptime

### 7.1 Local, from the Pi itself

The web viewer's route table gives you a real API without any aggregator involved ([WebViewer.cpp](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Web/WebViewer.cpp)):

| Endpoint | Contents |
|---|---|
| `/api/stat.json`, `/stat.json` | `msg_rate`, `received` (lifetime counter), `run_time`, `vessel_count`, `vessel_max`, `sample_rate`, per-output stats, build/version, `memory`, `os`, `hardware` |
| `/api/output_stats.json` | per-output counters only (slim variant) |
| `/api/sharing_state.json` | community-feed connection state; the viewer polls this every 10 s |
| `/api/ships.json`, `/api/ships_array.json?since=`, `/api/ships_full.json` | current targets |
| `/api/history_full.json` | historical series behind the plots |
| `/metrics` | Prometheus exposition |
| `/api/sse` | server-sent events stream |
| `/geojson`, `/kml`, `/api/path.geojson` | export formats |

Prometheus metric names, from [Prometheus.cpp](https://github.com/jvde-github/AIS-catcher/blob/main/Source/Web/Prometheus.cpp): `ais_stat_count`, `ais_stat_count_channel_*`, `ais_stat_count_type_*`, `ais_stat_distance`, `ais_msg_level`, `ais_msg_ppm`. Enable with `-N … PROME on`, or `PROMETHEUS_ENABLE=on` in `shipfeeder`, which also has a [Grafana dashboard guide](https://github.com/sdr-enthusiasts/docker-shipfeeder/blob/main/README-grafana.md).

### 7.2 Dead-man's switch

Neither of these knows anything about AIS. They tell you the Pi stopped calling home, which is the failure you most want to hear about.

**healthchecks.io.** A check goes `Up` while pings arrive on time, `Late` inside the grace period, then `Down` and alerts ([docs](https://healthchecks.io/docs/)). Free "Hobbyist" tier monitors **20 jobs** with 100 log entries each ([pricing](https://healthchecks.io/pricing/)). URL forms from the [Pinging API](https://healthchecks.io/docs/http_api/):

```
https://hc-ping.com/<uuid>              success
https://hc-ping.com/<uuid>/start        job started
https://hc-ping.com/<uuid>/fail         failure
https://hc-ping.com/<uuid>/<exit-status>
https://hc-ping.com/<ping-key>/<slug>   slug form, supports auto-provisioning
```

Report *health*, not merely *aliveness* — ping success only when the counter has advanced:

```bash
#!/bin/bash
# /usr/local/bin/ais-heartbeat  — run from a systemd timer every 5 minutes
UUID="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
STATE=/var/lib/ais-heartbeat/last
mkdir -p "$(dirname "$STATE")"

now=$(curl -sf --max-time 10 http://127.0.0.1:8100/api/stat.json | jq -r '.received // empty')
prev=$(cat "$STATE" 2>/dev/null || echo -1)

if [[ -n "$now" && "$now" != "$prev" ]]; then
    curl -fsS -m 10 --retry 5 -o /dev/null "https://hc-ping.com/$UUID"
else
    curl -fsS -m 10 --retry 5 -o /dev/null --data-raw "counter stuck at ${now:-unreachable}" \
        "https://hc-ping.com/$UUID/fail"
fi
echo "${now:-$prev}" > "$STATE"
```

**Uptime Kuma**, self-hosted, has a `push` monitor type. The URL it generates is, from the source ([EditMonitor.vue](https://github.com/louislam/uptime-kuma/blob/master/src/pages/EditMonitor.vue)):

```
{baseURL}/api/push/{pushToken}?status=up&msg=OK&ping=
```

so:

```bash
curl -fsS -m 10 -o /dev/null "https://kuma.example.org/api/push/AbCdEf123456?status=up&msg=OK&ping=42"
```

Push tokens are 32 characters. Set the monitor's heartbeat interval a little longer than your cron interval.

### 7.3 What each aggregator shows you

| Aggregator | Station page | Self-serve monitoring | Offline alerts | Public API |
|---|---|---|---|---|
| **AISHub** | [`/stations`](https://www.aishub.net/stations) — ID, Status, Uptime %, Country, Location, Ships, Distinct, Contributor; filter All/Online/Offline/Dead | "Web-based real-time monitoring of your AIS feed" | **"Automatic email notifications if your feed is offline for more than 6 hours"** | members-only `stations.php` (below); plus an unauthenticated bulk export |
| **aiscatcher.org** | [Stations](https://www.aiscatcher.org/stations) — Status, Last Message, Last Seen, Ships, Unique, Messages, Online %, Activity (last 7 days) | station page after registering | not documented | none public — Cloudflare Turnstile gates the site and `/api/` is not reachable unauthenticated |
| **MarineTraffic** | Station Details Page, plus an "AIS receiver Dashboard" with live map and stats | dashboard: vessel categories, AIS messages/minute, average and max distance | see caveat below | no |
| **VesselFinder** | [`stations.vesselfinder.com`](https://stations.vesselfinder.com/) — ID, Status, Location, Country, Ships, Contributor; live Online/Offline counts | "Monitor your real time station performance and keep track of its statistics" | "Get notified by email if there is a problem with your station performance" | no |
| **ShipXplorer** | "My Station Page" under your account — "monitor your receiver and view, uptime and upload statistics"; plus New Units and Global Ranking | yes | not documented | no |

Details and exact wording:

**AISHub.** The contributor benefits are enumerated on [join-us](https://www.aishub.net/join-us): once you stream and notify them, "we will create your AISHub account, granting you access to: Web-based real-time monitoring of your AIS feed / Automatic email notifications if your feed is offline for more than 6 hours / A personal API key (provided your feed meets the quality requirements)." The members' stations API is `https://data.aishub.net/stations.php?username=USERNAME&output=json&id=STATIONID`, returning `ID`, `LASTUPDATE`, `COUNTRY`, `LOCATION`, `SHIPS`, `DISTINCT`, `CONTRIBUTOR` ([API docs](https://www.aishub.net/api)). The rate limit is blunt and explicit: "Don't access the webservice more frequently than once per minute! The web service will return nothing if executed more frequently!"

There is also an **unauthenticated** bulk export behind the public stations table — I fetched `https://www.aishub.net/stations/export-json` successfully and it returns `[{"id":"2007","country":"vn","location":"Ho Chi Minh","latitude":"10.77","longitude":"106.77","unix_time":"1787450701"}, …]`, with `/stations/export` for CSV. That is undocumented and could change; do not build on it without a fallback.

**aiscatcher.org.** Registration is at [`/addstation`](https://www.aiscatcher.org/addstation). The README describes the flow: "visit www.aiscatcher.org, and add your station. Upon registration, you'll receive a personal sharing key. Simply run AIS-catcher on the command line with `-X` followed by your sharing key" ([README](https://github.com/jvde-github/AIS-catcher#the-aiscatcherorg-community)). The `/addstation` page states "AIS-catcher Required — Our network exclusively supports AIS-catcher stations", and advertises "575+ Active Stations, 73 Countries, 125M+ Daily Messages, 80K+ Unique Ships". I could not retrieve any public JSON API: the site is behind Cloudflare Turnstile and the only endpoint exposed in the page source is `/api/turnstile/verify`.

**MarineTraffic.** The dashboard article is candid about its limits: "the statistics only go back 12 hours and are reset periodically, and when you restart the device. For statistics that go back further, use your Station's page on MarineTraffic" ([AIS receiver Dashboard Statistics and Map](https://support.marinetraffic.com/en/articles/9552968-ais-receiver-dashboard-statistics-and-map)). Availability, which is what your plan reward depends on, "can be checked on your Station Details Page" ([AIS Partner benefits](https://support.marinetraffic.com/en/articles/9552976-what-benefits-do-i-get-as-an-ais-partner)). MarineTraffic's help centre links a "Station Offline: Troubleshooting Guide", but **the article URL 404s** and marinetraffic.com blocks non-browser fetches, so I could not confirm the content or the wording of their station-offline emails — see [What I could not verify](#what-i-could-not-verify).

**ShipXplorer.** From [addcoverage](https://www.shipxplorer.com/addcoverage): "To monitor your receiver and view, uptime and upload statistics, just log into your ShipXplorer account", then Account → station page. Newly claimed receivers appear under "New Units" — the page advises "If you don't see it immediately, don't worry. Check back after 1 or 2 hours."

### 7.4 Suggested layering

1. `healthchecks.io` free tier as the outer dead-man's switch — it is the only one that alerts when the whole Pi or its internet connection dies.
2. The aggregators' own offline emails as a cross-check. AISHub's 6-hour threshold and VesselFinder's performance alerts are free and require no work.
3. Prometheus + Grafana locally if you care about gain, PPM drift and range trends over time.

---

## 8. Feeding multiple aggregators, and the etiquette

### 8.1 Technically: how the fan-out works

Feeding many services from one receiver is the **normal, expected** configuration, not a hack. Three mechanisms:

**AIS-catcher's own repeatable outputs.** `-u host port [setting value]` may appear multiple times; the JSON config represents them as an array (`"udp":[]` in the default config the installer writes). The [UDP output docs](https://jvde-github.github.io/AIS-catcher-docs/configuration/output/UDP/) list the per-output settings:

| Setting | Type | Default | Meaning |
|---|---|---|---|
| `host` / `port` | string | — | target |
| `msgformat` | string | `NMEA` | `NMEA`, `JSON_NMEA`, `JSON_FULL`, … |
| `broadcast` | bool | false | allow broadcast addresses |
| **`reset`** | int | off | **recreate the socket after N minutes (1–1440)** |
| `uuid` | string | — | identifier, must be a valid UUID |
| `include_sample_start` | bool | false | append sample-start counter |

`reset` is the ops-relevant one: aggregators move IPs, and a long-lived UDP socket holding a stale DNS resolution silently black-holes your feed. Setting `reset 60` on each aggregator output re-resolves hourly.

So a hand-rolled multi-feed line looks like:

```
-u data.aishub.net 12345 reset 60 \
-u 5.9.207.224 11111 reset 60 \
-u ais.vesselfinder.com 22222 reset 60 \
-P hub.shipxplorer.com 33333
```

with `-P` for TCP-client targets and `-H <url> ID <station> INTERVAL 30` for HTTP ones.

**docker-shipfeeder.** Wraps the same thing in environment variables and ships a host/port table for ~20 services. Its README is the single best map of the landscape ([Feeding AIS Aggregator Services](https://github.com/sdr-enthusiasts/docker-shipfeeder#feeding-ais-aggregator-services)). Named targets today: ADSB-Network/RadarVirtuel, Airframes, AIS-Catcher, AIS Friends, AISHub, APRS.fi, BoatBeacon, HPRadar, MarineTraffic, MLAT.uk, MyShipTracking, SDRMap, ShipFinder, ShippingExplorer, ShipXplorer (key or UDP), VesselFinder, VesselTracker — plus `UDP_FEEDS` / `TCP_FEEDS` for anything else and `-H` for arbitrary HTTP.

The README carries two etiquette warnings from the aggregators themselves, worth quoting in any guide:

> "Please use either the UDP option or the TCP option as instructed by MarineTraffic, but don't use both!"

> "We decided to allow parallel feeding to UDP and TCP ports because some aggregators have asked our users to do this temporarily for testing. However, the user should take caution not to feed duplicate data to any aggregator unless the aggregator specifically requested this for testing purposes."

The rule that emerges: **feeding many aggregators is fine; feeding the same aggregator twice is not.** Duplicate streams inflate their message counts and can look like spoofing.

**Chaining instances.** `AISCATCHER_UDP_INPUTS` / `-u` between containers lets a channel-CD receiver feed a channel-AB receiver, which then fans out to everything ([Aggregating multiple instances](https://github.com/sdr-enthusiasts/docker-shipfeeder#aggregating-multiple-instances-of-the-container)). A generic NMEA multiplexer (kplex, socat) does the same job for non-AIS-catcher receivers, but with AIS-catcher's own repeatable outputs there is usually no need for one.

### 8.2 The terms question: does anyone claim exclusivity?

Short answer, on the text I could retrieve: **no aggregator claims exclusivity.** Nothing I found forbids feeding competitors simultaneously. The tension is elsewhere — in what happens to *their* aggregate data once you receive it back.

| Aggregator | Exclusivity claimed? | What the contributor gets | Source |
|---|---|---|---|
| AISHub | **No.** Terms bar synthetic, scraped and re-relayed data, but say nothing about feeding others | API key to the aggregated feed (JSON/XML/CSV), feed monitoring, offline emails | [join-us](https://www.aishub.net/join-us) |
| MarineTraffic / Kpler | **No.** The licence you grant is explicitly "**non-exclusive**" | Essential plan (>40% availability) or Enterprise plan (>85%), auto-applied; sometimes free hardware | [terms](https://www.marinetraffic.com/en/p/terms), [AIS Partner benefits](https://support.marinetraffic.com/en/articles/9552976-what-benefits-do-i-get-as-an-ais-partner) |
| VesselFinder | **No exclusivity clause**, but the §11.2 grant to them is notably *not* qualified as non-exclusive | free Premium account, station stats, email alerts, embeddable map — all "discretionary" | [terms](https://www.vesselfinder.com/terms) |
| ShipXplorer / AirNav | **Silent** — no feeder data agreement located | free receiver hardware in gap areas, Business membership, station page and ranking | [addcoverage](https://www.shipxplorer.com/addcoverage) |
| MyShipTracking, ShippingExplorer, VesselTracker, BoatBeacon | **Silent** — no feeder terms located | account tiers / viewer access / sometimes hardware | [shipfeeder README table](https://github.com/sdr-enthusiasts/docker-shipfeeder#easy-sharing-with-other-services) |
| aiscatcher.org | **Silent** on other aggregators, but requires AIS-catcher software: "Our network exclusively supports AIS-catcher stations" | community-feed overlay in your own viewer; map presence | [addstation](https://www.aiscatcher.org/addstation) |

**AISHub** — the closest thing to a real contributor contract, and it is a reciprocity requirement, not an exclusivity one. Verbatim from [join-us](https://www.aishub.net/join-us), under the heading "Terms of Use":

> Every AISHub contributor is required to provide at least one raw AIS feed in NMEA format. Additional AIS sources are highly appreciated.
>
> Contributors who apply for API access to the aggregated AISHub feed must meet the following quality requirements for their AIS data feed:
> - Coverage of at least 10 vessels (average over the last 7 days)
> - At least 90% uptime (average over the last 7 days)
> - Maximum downsampling rate of 60 seconds
> - Maximum delay of 10 seconds for AIS messages
>
> The following AIS feeds are strictly prohibited:
> - Synthesized or artificially generated NMEA data
> - Scraped or stolen data
> - Data from publicly available AIS sources or services
>
> All contributors are allowed to use the aggregated data for free.
>
> AISHub is NOT RESPONSIBLE for any losses caused by technical failures or low-quality data feeds.

And on the home page: "To access real-time data from all available sources, users are required to share their own AIS feed with other contributors on AISHub" ([aishub.net](https://www.aishub.net/)). Also: "AISHub is a contributor-based network. Applications without an operational AIS station and feed will not be approved."

Read carefully, the "strictly prohibited" list is about **provenance**, not exclusivity: don't send them data you didn't receive off the air. Piping another aggregator's stream into AISHub would violate it; feeding your own antenna to AISHub *and* MarineTraffic does not. There is no standalone terms page — `https://www.aishub.net/terms-of-use` returns 404, and the "AISHub Terms of Use" the signup checkbox references is the join-us text itself.

**MarineTraffic / Kpler** — the operative sentence from the [Terms of Use](https://www.marinetraffic.com/en/p/terms), which I retrieved through a browser since curl is Cloudflare-blocked:

> By using this Service, you grant us a **non-exclusive**, worldwide, perpetual, transferable, irrevocable and royalty-free licence to use, store, process, reproduce, modify, anonymise and aggregate the maritime related datasets you are adding on Kpler's Platforms for the purpose of creating derived works, improve our Services and/or Data, and refine our internal models and Data, provided such Data cannot be used to identify you. We may also combine or incorporate your data with or into other similar data and information available, derived or obtained from other customers, users, or other sources.

"Non-exclusive" is the word that settles the question: you keep the right to license the same data to anyone else. The restrictive clauses run the other direction, over the data they give *you*:

> Any and all rights, title and interests in and to the Data … are and shall remain the exclusive property of Kpler or its third-party licensors … You are not entitled to reproduce, duplicate, copy, publish, sell, distribute or otherwise make available the Data (or any part of them) without our express and specific written permission.

> The display, the creation of derived data, and internal distribution of Data through the Service within your organisation are solely permitted for research and internal business purposes. Any other internal or external dissemination of the Data is strictly prohibited … commercial use of any Kpler's data gathered through this Service is prohibited.

The benefits, verbatim from [What Benefits Do I Get as an AIS Partner?](https://support.marinetraffic.com/en/articles/9552976-what-benefits-do-i-get-as-an-ais-partner) (updated 2 July 2025):

> To qualify for a Essential Plan and a fleet of 50, you must have at least one Station with > 40% availability during the past three months.
>
> To qualify for an Enterprise Plan, you need to have at least one Station with > 85% availability during the past three months.
>
> The aforementioned plans are automatically made available to the MarineTraffic account, which is used to register a receiving station.

And on free hardware, from [cover-your-area](https://www.marinetraffic.com/en/join-us/cover-your-area):

> If you're interested in joining our community but don't have the necessary equipment, you can apply to see if you qualify for hosting our gear. We'll cover the cost of the equipment and shipping, while you'll take responsibility for its installation.

Note the framing on the same page — "Become eligible for complimentary plan upgrades, **depending on your station's performance**." The reward is conditional and can move; nothing guarantees a tier.

**VesselFinder** — the most explicit modern feeder agreement, at [vesselfinder.com/terms](https://www.vesselfinder.com/terms). §11.3, verbatim:

> **AIS Station Data.** If you provide AIS data from an "AIS Station", VesselFinder may receive, aggregate, normalize, filter, and use such data and derived outputs as part of the Service (including making them available to subscribers and through VesselFinder's products and services). AIS Station data is not submitted in confidence unless expressly agreed in writing. You also authorize VesselFinder to use AIS Station metadata (e.g., station identifier, approximate location, technical characteristics, uptime/quality metrics) for coverage modeling, service quality, and fraud prevention; VesselFinder will not publish precise station coordinates unless you opt in or authorize it in writing.

§11.2, the licence grant — note the absence of "non-exclusive", unlike MarineTraffic's:

> **License.** By submitting User Content, you grant VesselFinder a worldwide, royalty-free, transferable, sublicensable license to host, store, reproduce, process, adapt/modify (for technical purposes), distribute, display, and otherwise use User Content to operate, secure, improve, and commercialize the Service and VesselFinder's products and services.

§11.5, on the value of the perks:

> Any Premium access or other benefits provided in connection with AIS contributions are **discretionary** and may be modified, suspended, or terminated (including if contributions stop, quality materially decreases, or misuse is suspected).

§7.4 restricts what you may do with **their** Content — you may not "use Content to build or support a competing vessel-tracking, AIS, maritime intelligence, compliance, or analogous service." That constrains the aggregate you receive back, not the feed you send out.

Benefits advertised at [become-partner](https://stations.vesselfinder.com/become-partner): free Premium account, ships on map, station statistics, email notifications on performance problems, community contribution, embeddable personalised map.

**ShipXplorer** — no feeder data agreement located. What is documented is the free-hardware programme and its conditions ([addcoverage](https://www.shipxplorer.com/addcoverage)):

> We welcome users to share data with ShipXplorer. We even provide free ShipXplorer receivers for users located in areas where we don't yet have proper coverage.
>
> - Feeders must be located within 5 kilometers (3 miles) of a major port or shipping route.
> - Feeders should have satisfactory reception conditions with an unobstructed view of the sea.
> - Feeders must be able to start sharing data with ShipXplorer within 7 days of equipment delivery.
> - Feeders must be able to keep the receiver online 24/7.
> - If the feeder can no longer host our equipment, ShipXplorer will arrange to ship the receiver back to the company.

The `shipfeeder` README adds that AirNav's support of the community "is much appreciated" and that ShipXplorer feeding makes you a "Business member".

### 8.3 What contributors actually receive, side by side

| Aggregator | Account perk | Hardware | Data back |
|---|---|---|---|
| AISHub | account + feed monitoring | none | **aggregated feed via API**, JSON/XML/CSV, 1 req/min, gated on ≥10 vessels and ≥90% uptime |
| MarineTraffic | Essential (>40% avail.) or Enterprise (>85%) plan | free gear by application, in gap areas | viewing only; redistribution and commercial use prohibited |
| VesselFinder | free Premium, explicitly discretionary | none documented | viewing + embeddable map; §7.4 bars building a competing service |
| ShipXplorer | Business membership, station page, global ranking | free receiver near ports/routes, returnable | viewing |
| aiscatcher.org | community-feed overlay in your own web viewer | none | overlay only; no public API |
| aprs.fi | map + free non-commercial API | none | API |
| Airframes | leaderboard; free REST API for feeders | none | API, non-commercial |

**AISHub is the only one that hands back a machine-readable copy of the pooled data**, and it is the only one whose bargain is written down as a reciprocity requirement rather than a discretionary reward. If the guide needs a one-line recommendation for someone who wants something back: feed AISHub first.

### 8.4 Community sentiment

I could not verify this section to the standard of the rest, and the guide should either drop it or soften it. Reddit blocks both plain HTTP JSON requests and a real browser from this network (`old.reddit.com` 302s, `www.reddit.com/*.json` returns the HTML shell, and thread pages hit "Prove your humanity"), and my web-search budget was exhausted before I reached the forums. What I can support:

- Multi-feeding is **built into the tooling as the default assumption**, which is the strongest available evidence that it is uncontroversial. `docker-shipfeeder` exists specifically to feed ~20 services from one dongle and its own tagline is "AIS feeder for ShipXplorer … The container can do so much more than just feeding ShipXplorer" ([README](https://github.com/sdr-enthusiasts/docker-shipfeeder)).
- The only "don't feed everyone" instruction I found anywhere in the sdr-enthusiasts corpus is about **one service twice**, not many services once (§8.2), plus an ADS-B-side analogue in the same maintainers' sample compose: "FR24 has requested NOT to enable MLAT for those station that feed to multiple services; as such, it's commented out" ([sample-docker-compose.yml](https://github.com/sdr-enthusiasts/docker-install/blob/main/sample-docker-compose.yml)). That is a request about MLAT specifically, from an ADS-B aggregator — it is the nearest thing to a multi-feeding restriction in this ecosystem, and it is not about AIS.
- I found **no report anywhere** of a station being warned, throttled or removed for feeding multiple AIS aggregators. Absence of evidence, given I could not reach Reddit or the OpenCPN forum.
- On AIS-catcher's own tracker, the multi-aggregator issues are purely technical — stability of simultaneous UDP streams to two services ([#162](https://github.com/jvde-github/AIS-catcher/issues/162)), community-feed reconnection behaviour ([#460](https://github.com/jvde-github/AIS-catcher/issues/460), [#626](https://github.com/jvde-github/AIS-catcher/issues/626)) — with no hint of a policy dispute.
- The "MarineTraffic is stingy vs AISHub is fair" claim is **structurally supported by the terms** even without community quotes: AISHub contractually owes contributors API access on published, objective thresholds; MarineTraffic's plan upgrades are performance-gated and VesselFinder's are contractually "discretionary". That is a defensible framing to publish. Attributing it as *community sentiment* is not, on what I could verify.

### 8.5 Practical etiquette to publish

1. Feed as many aggregators as you like. Nobody's terms forbid it, and MarineTraffic's licence is explicitly non-exclusive.
2. Feed each aggregator **once**. Never both UDP and TCP to the same service unless they asked you to for testing.
3. Never relay another aggregator's data as if it were your own reception — this is the one thing AISHub's terms flatly prohibit, and it corrupts everyone's provenance.
4. Use one honest station identity and location. Aggregators use station metadata for coverage modelling and fraud detection (VesselFinder §11.3 says so outright).
5. Don't re-publish the aggregate you get back. MarineTraffic and VesselFinder both bar redistribution and commercial use of their data; AISHub's is free for contributors but members-only.
6. Keep uptime up. It is the sole currency: >40%/>85% at MarineTraffic, ≥90% at AISHub, "discretionary" and quality-linked at VesselFinder.
7. Announce planned downtime to nobody and expect the alerts anyway — AISHub emails after 6 hours, VesselFinder on performance problems.

---

## What I could not verify

Flagged explicitly, since several bear on claims a public guide would otherwise make confidently.

**Terms and policy**

- **MarineTraffic's station-owner agreement, if a separate one exists.** `marinetraffic.com` returns HTTP 403 to curl and to WebFetch (Cloudflare). I got the Terms of Use and the join page through a real browser, and the help-centre articles over plain HTTP, but I found no document specific to station owners beyond the general Terms. If one is shown at station-registration time behind a login, I have not seen it.
- **MarineTraffic's "Station Offline" email wording and trigger threshold.** The help centre links a "Station Offline: Troubleshooting Guide", but `support.marinetraffic.com/en/articles/9552980-station-offline-troubleshooting-guide` returns 404 and I could not locate the live URL. I have no verified text for these emails or the delay before they fire. Do not state a threshold.
- **ShipXplorer / AirNav feeder terms.** None found. The `addcoverage` page has hardware conditions but no data-licensing text. Their general "Terms of Use" link exists in the site nav; I did not retrieve and read it.
- **MyShipTracking, ShippingExplorer, VesselTracker, BoatBeacon, HPRadar feeder terms.** No feeder agreements located for any of them. Treat all as "silent", not as "permissive".
- **aiscatcher.org terms and privacy policy.** No terms page found. The site is Turnstile-gated.
- Whether AISHub's prohibition on "Data from publicly available AIS sources or services" has ever been enforced against someone who *also* feeds commercial aggregators. My reading (it targets provenance, not multi-homing) is an interpretation, not a quoted ruling.

**Community sentiment (§8.4)**

- Reddit is unreachable from this network by every method tried — `search.json` via curl (HTML shell returned), `old.reddit.com` (302), and Playwright (bot challenge). No Reddit quotes, permalinks or vote counts in this document are sourced; I deliberately included none.
- The OpenCPN forum and the sdr-enthusiasts Discord were not reached. The Discord is invite-gated and not archivable by HTTP.
- WebSearch budget (200 calls) was exhausted at the start of this session, so all discovery ran through curl, DuckDuckGo via a browser, and the GitHub API.
- No first-hand accounts of what hardware people actually received, or of anyone being penalised for multi-feeding. Everything in §8.3 comes from the aggregators' own marketing pages, which describe what is *offered*, not what is *delivered*.

**Technical**

- I did not run any of the configuration in this document against a live station. The systemd unit is reconstructed from the installer source rather than read off an installed machine — accurate to the script, but a real box may differ if it was installed by an older version or the toggles were used.
- `/api/stat.json` field names come from `WebViewer.cpp` (`msg_rate`, `received`, `run_time`, `vessel_count`); I could not fetch a live station's `stat.json` to confirm the JSON shape end-to-end, so validate the `jq` filters in §6.2 and §7.2 against your own station before trusting them.
- The bcm2835 watchdog's ~15 s maximum timeout is widely repeated but I did not verify it against the driver source. `CONFIG_BCM2835_WDT=y` in all four Pi defconfigs **is** verified.
- AISHub's `/stations/export-json` and `/stations/export` endpoints work unauthenticated today but are undocumented; they may be withdrawn.
- The `usbreset` invocation is standard `usbutils` usage; I did not confirm the exact argument form against Debian trixie's packaged version, which has historically varied between `usbreset <vid:pid>` and `usbreset /dev/bus/usb/BBB/DDD`.
