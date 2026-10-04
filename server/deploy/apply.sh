#!/bin/sh
# Converge this box to match rootfs/, install the bundled binary, restart once.
# Idempotent; runs as root on the box, invoked by deploy.sh.
set -eu
cd "$(dirname "$0")"

export DEBIAN_FRONTEND=noninteractive
if ! command -v curl >/dev/null; then # a minimal image may lack it, and the repositories below need it first
	apt-get update -q
	apt-get install -yq curl
fi
# Caddy's own apt repository: Ubuntu's caddy package is too old for the Caddyfile. The paths are the
# ones Caddy's install instructions use, so a box set up by hand keeps its files and apt sees one
# source with one key. Each apt-get install below takes the newest release.
if [ ! -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg ]; then
	curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
fi
if [ ! -f /etc/apt/sources.list.d/caddy-stable.list ]; then
	curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt -o /etc/apt/sources.list.d/caddy-stable.list
fi
# Grafana's apt repository, for Alloy. The key is fetched once; apt checks every package against it.
if [ ! -f /etc/apt/keyrings/grafana.asc ]; then
	mkdir -p /etc/apt/keyrings
	curl -fsSL https://apt.grafana.com/gpg.key -o /etc/apt/keyrings/grafana.asc
fi
echo 'deb [signed-by=/etc/apt/keyrings/grafana.asc] https://apt.grafana.com stable main' >/etc/apt/sources.list.d/grafana.list
# ClickHouse's own apt repository, on its long-term-support releases.
if [ ! -f /usr/share/keyrings/clickhouse-keyring.gpg ]; then
	curl -fsSL https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key | gpg --dearmor -o /usr/share/keyrings/clickhouse-keyring.gpg
fi
echo "deb [signed-by=/usr/share/keyrings/clickhouse-keyring.gpg arch=$(dpkg --print-architecture)] https://packages.clickhouse.com/deb lts main" >/etc/apt/sources.list.d/clickhouse.list
apt-get update -q
# confold: rootfs/ owns config files such as /etc/alloy/config.alloy, so a package upgrade must not stop to ask.
apt-get install -yq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold alloy caddy curl fail2ban jq unattended-upgrades
# ClickHouse is installed once and upgraded by hand, so a deploy never changes the database under live data.
if ! command -v clickhouse-server >/dev/null; then
	apt-get install -yq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold clickhouse-client clickhouse-server
fi

id aiscast >/dev/null 2>&1 || useradd --system --shell /usr/sbin/nologin aiscast

# The sshd drop-in is the one file that can lock everyone out of the box, and a bad one
# only bites on the next reboot, so roll it back if it does not validate.
drop=/etc/ssh/sshd_config.d/10-hardening.conf
if [ -f "$drop" ]; then
	cp "$drop" "$drop.bak"
fi
cp -R rootfs/. /
if ! sshd -t; then
	if [ -f "$drop.bak" ]; then
		mv "$drop.bak" "$drop"
	else
		rm -f "$drop"
	fi
	echo 'sshd config invalid: rolled back the drop-in' >&2
	exit 1
fi
rm -f "$drop.bak"
systemctl reload ssh

mkdir -p /opt/aiscast /var/lib/aiscast/archive /var/lib/aiscast/normalized
chown -R aiscast:aiscast /var/lib/aiscast

# The packager runs on GitHub Actions (.github/workflows/packager.yml). Boxes converged before
# that may still carry any of its units, script, or staging directory.
systemctl disable --now packager.timer packager.service 2>/dev/null || true
rm -f /etc/systemd/system/packager.timer /etc/systemd/system/packager.service /opt/aiscast/packager.py
rm -rf /var/lib/aiscast/packager
# Tracks read from ClickHouse. Boxes converged before that may still carry the SQLite track store.
rm -f /var/lib/aiscast/tracks.db /var/lib/aiscast/tracks.db-*

# Seed only: secrets live on the box, never in the repo.
if [ ! -f /etc/aiscast.env ]; then
	install -m 600 aiscast.env.example /etc/aiscast.env
	echo 'created /etc/aiscast.env from template: fill in secrets before the service is useful' >&2
fi
if [ ! -f /etc/alloy.env ]; then
	install -m 600 alloy.env.example /etc/alloy.env
	echo 'created /etc/alloy.env from template: fill in the Grafana Cloud credentials to ship metrics' >&2
fi

systemctl daemon-reload
systemctl restart systemd-journald
systemctl enable aiscast caddy clickhouse-server fail2ban
# ClickHouse restarts only when its own files differ from the ones it last restarted with, recorded in a
# stamp, so a deploy does not interrupt it for nothing and one that stopped partway is caught up by the next.
# aiscast runs without ClickHouse, answering tracks with 503, so a ClickHouse that will not start warns and
# never fails the deploy;
# --no-block keeps a slow start from holding it up.
ch_sum=$(cat /etc/clickhouse-server/config.d/aiscast.xml /etc/systemd/system/clickhouse-server.service.d/10-aiscast.conf | md5sum)
ch_stamp=/var/lib/aiscast/clickhouse-config.md5
if [ "$ch_sum" != "$(cat "$ch_stamp" 2>/dev/null)" ]; then
	if systemctl --no-block restart clickhouse-server; then
		echo "$ch_sum" >"$ch_stamp"
	else
		echo 'clickhouse-server did not restart' >&2
	fi
else
	systemctl --no-block start clickhouse-server || echo 'clickhouse-server did not start' >&2
fi
systemctl reload-or-restart fail2ban
install -d -o caddy -g caddy /var/log/caddy
chown -h caddy:caddy /var/log/caddy/access.log 2>/dev/null || true # -h: never follow a link the caddy user planted
# as root, validate would create the access log root-owned, which Caddy then cannot open
runuser -u caddy -- env HOME=/var/lib/caddy caddy validate --config /etc/caddy/Caddyfile
# The Caddyfile's stream_close_delay keeps the reload from blocking on open WebSockets; the
# restart is the bounded fallback if it hangs anyway.
systemctl reload-or-restart caddy || systemctl restart caddy

# Alloy runs only once /etc/alloy.env names the Grafana Cloud endpoint; without one it would crash-loop.
alloy=
if grep -q '^GRAFANA_CLOUD_PROM_URL=.' /etc/alloy.env; then
	alloy=1
	# With the unit's environment, so the config is checked as Alloy will run it.
	# shellcheck source=/dev/null
	(set -a && . /etc/alloy.env && alloy validate /etc/alloy/config.alloy)
	systemctl enable alloy
	systemctl restart alloy
else
	systemctl disable --now alloy
	echo 'alloy off: fill in /etc/alloy.env to ship metrics' >&2
fi

if [ -f aiscast-linux ]; then
	install -m 755 aiscast-linux /opt/aiscast/aiscast.new
	mv /opt/aiscast/aiscast.new /opt/aiscast/aiscast
fi
systemctl restart aiscast
sleep 3
systemctl is-active aiscast
systemctl is-active caddy
# A boot that loads a large vessel record can take longer than the sleep before it listens.
curl -fsS --retry 20 --retry-connrefused --retry-delay 1 localhost:8080/health
if [ -n "$alloy" ]; then
	systemctl is-active alloy
fi
