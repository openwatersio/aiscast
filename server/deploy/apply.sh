#!/bin/sh
# Converge this box to match rootfs/, install the bundled binary, restart once.
# Idempotent; runs as root on the box, invoked by deploy.sh.
set -eu
cd "$(dirname "$0")"

export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -yq caddy curl fail2ban unattended-upgrades

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

systemctl daemon-reload
systemctl restart systemd-journald
systemctl enable aiscast caddy fail2ban
systemctl reload-or-restart fail2ban
caddy validate --config /etc/caddy/Caddyfile
# A reload can time out when long-lived SSE streams hold the old servers open; a restart is bounded.
systemctl reload-or-restart caddy || systemctl restart caddy

if [ -f aiscast-linux ]; then
	install -m 755 aiscast-linux /opt/aiscast/aiscast.new
	mv /opt/aiscast/aiscast.new /opt/aiscast/aiscast
fi
systemctl restart aiscast
sleep 3
systemctl is-active aiscast
curl -fsS localhost:8080/health
