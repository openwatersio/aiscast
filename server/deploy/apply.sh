#!/bin/sh
# Converge this box to match rootfs/, install the bundled binary, restart once.
# Idempotent; runs as root on the box, invoked by deploy.sh.
set -eu
cd "$(dirname "$0")"

export DEBIAN_FRONTEND=noninteractive
# Grafana's apt repository, for Alloy. The key is fetched once; apt checks every package against it.
if [ ! -f /etc/apt/keyrings/grafana.asc ]; then
	if ! command -v curl >/dev/null; then # a minimal image may lack it, and the install below comes after the key
		apt-get update -q
		apt-get install -yq curl
	fi
	mkdir -p /etc/apt/keyrings
	curl -fsSL https://apt.grafana.com/gpg.key -o /etc/apt/keyrings/grafana.asc
fi
echo 'deb [signed-by=/etc/apt/keyrings/grafana.asc] https://apt.grafana.com stable main' >/etc/apt/sources.list.d/grafana.list
apt-get update -q
# confold: rootfs/ owns config files such as /etc/alloy/config.alloy, so a package upgrade must not stop to ask.
apt-get install -yq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold alloy caddy curl fail2ban unattended-upgrades

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

mkdir -p /opt/aiscast /var/lib/aiscast/archive
chown -R aiscast:aiscast /var/lib/aiscast

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
systemctl enable aiscast caddy fail2ban
systemctl reload-or-restart fail2ban
caddy validate --config /etc/caddy/Caddyfile
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
curl -fsS localhost:8080/health
if [ -n "$alloy" ]; then
	systemctl is-active alloy
fi
