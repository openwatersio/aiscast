#!/bin/sh
# Converge this box to match rootfs/, install the bundled binary, restart once.
# Idempotent; runs as root on the box, invoked by deploy.sh.
set -eu
cd "$(dirname "$0")"

UV_VERSION=0.12.12

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

mkdir -p /opt/aiscast /var/lib/aiscast/archive /var/lib/aiscast/lake
chown -R aiscast:aiscast /var/lib/aiscast

# The nightly derive runs lake/derive.py under uv, which resolves the script's own dependencies
# and Python. Not in apt; pinned so a box and CI agree on what ran.
if [ "$(/usr/local/bin/uv --version 2>/dev/null | cut -d' ' -f2)" != "$UV_VERSION" ]; then
	curl -LsSf "https://astral.sh/uv/$UV_VERSION/install.sh" |
		UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 INSTALLER_NO_MODIFY_PATH=1 sh
	# a failed download still exits 0 through the pipe, so fail here rather than at 00:30 UTC
	/usr/local/bin/uv --version >/dev/null
fi
install -m 755 derive.py /opt/aiscast/derive.py

# Seed only: secrets live on the box, never in the repo.
if [ ! -f /etc/aiscast.env ]; then
	install -m 600 aiscast.env.example /etc/aiscast.env
	echo 'created /etc/aiscast.env from template: fill in secrets before the service is useful' >&2
fi

systemctl daemon-reload
systemctl enable aiscast caddy fail2ban
# The timer skips itself while /etc/aiscast.env has no LAKE_CATALOG_URI, so enabling it is safe
# on a box whose template is still unfilled.
systemctl enable --now derive.timer
systemctl reload-or-restart fail2ban
caddy validate --config /etc/caddy/Caddyfile
systemctl reload-or-restart caddy

if [ -f aiscast-linux ]; then
	install -m 755 aiscast-linux /opt/aiscast/aiscast.new
	mv /opt/aiscast/aiscast.new /opt/aiscast/aiscast
fi
systemctl restart aiscast
sleep 3
systemctl is-active aiscast
curl -fsS localhost:8080/health
