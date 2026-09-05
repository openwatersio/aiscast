#!/bin/sh
# Converge this box to match rootfs/. Idempotent; runs as root on the box, invoked by provision.sh.
set -eu
cd "$(dirname "$0")"

export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -yq caddy curl fail2ban unattended-upgrades

id aiscast >/dev/null 2>&1 || useradd --system --shell /usr/sbin/nologin aiscast
id deploy >/dev/null 2>&1 || useradd --create-home --shell /bin/bash deploy

cp -R rootfs/. /
chown root:root /etc/sudoers.d/deploy
chmod 440 /etc/sudoers.d/deploy
visudo -c >/dev/null
sshd -t
systemctl reload ssh

mkdir -p /opt/aiscast /var/lib/aiscast/archive
chown -R aiscast:aiscast /var/lib/aiscast
chown deploy:deploy /opt/aiscast

# Seed only: an authorized_keys already on the box is never touched.
if [ ! -f /home/deploy/.ssh/authorized_keys ]; then
	install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
	install -m 600 -o deploy -g deploy deploy_authorized_keys /home/deploy/.ssh/authorized_keys
fi

# Seed only: secrets live on the box, never in the repo.
if [ ! -f /etc/aiscast.env ]; then
	install -m 600 aiscast.env.example /etc/aiscast.env
	echo 'created /etc/aiscast.env from template: fill in secrets before the service is useful' >&2
fi

systemctl daemon-reload
systemctl enable aiscast caddy fail2ban
systemctl reload-or-restart fail2ban
caddy validate --config /etc/caddy/Caddyfile
systemctl reload-or-restart caddy

if [ -x /opt/aiscast/aiscast ]; then
	systemctl restart aiscast
	sleep 3
	systemctl is-active aiscast
	curl -fsS localhost:8080/health
else
	echo 'no /opt/aiscast/aiscast yet: ship the binary with deploy.sh or the CI deploy job' >&2
fi
