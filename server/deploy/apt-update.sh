# Refreshes the apt lists and installs the box's packages. Sourced by apply.sh, as root on the box, after
# the vendor repositories are set up.
#
# A repository can refuse its index for days (Caddy's Cloudsmith repository answers 402 when its bandwidth
# quota runs out), and apt then fails the whole update. When every error comes from a repository, the
# vendors' or Ubuntu's, that refused its index with a 402 or a 5xx, and the box already has every package
# and ClickHouse, the deploy goes on
# without installing, since an upgrade would fetch from the repository that refuses, and warns on the CI
# run. Any other failure, such as a key apt cannot verify, still stops the deploy, as does a box missing a
# package.

packages="alloy caddy curl fail2ban jq unattended-upgrades"

installed() {
	for p in "$@"; do
		[ "$(dpkg-query -W -f='${db:Status-Abbrev}' "$p" 2>/dev/null)" = "ii " ] || return 1
	done
}

# Removed as soon as it is read, rather than by a trap, which would replace one apply.sh sets.
apt_log=$(mktemp)
# C: the messages below are matched in English.
if LC_ALL=C apt-get update -q >"$apt_log" 2>&1; then
	cat "$apt_log"
	rm -f "$apt_log"
	# confold: rootfs/ owns config files such as /etc/alloy/config.alloy, so a package upgrade must not stop to ask.
	# shellcheck disable=SC2086 # the package names are words
	apt-get install -yq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold $packages
else
	cat "$apt_log"
	# A refusing repository fails like this, and apt then calls it unsigned, since its signed index did not come:
	#   E: Failed to fetch https://dl.cloudsmith.io/…/InRelease  402  Payment Required [IP: … 443]
	#   E: The repository 'https://dl.cloudsmith.io/… any-version InRelease' is no longer signed.
	# Only those two lines, for a host that refused, are let through: "no longer signed" alone can mean a
	# signature was stripped.
	# Space-separated, since awk -v takes no newline on every awk.
	refused=$(sed -nE 's#^E: Failed to fetch https?://([^/ ]+)/[^ ]* +(402|5[0-9][0-9]) .*#\1#p' "$apt_log" | sort -u | tr '\n' ' ')
	other=$(grep '^E:' "$apt_log" | awk -v hosts="$refused" '
		BEGIN { n = split(hosts, h, " ") }
		/^E: Failed to fetch https?:\/\/[^ ]+ +(402|5[0-9][0-9]) / { next }
		/^E: Some index files failed to download/ { next }
		# A source names its host and then a path, or a space when it has none, as Grafana'"'"'s does.
		/ is no longer signed\.$/ {
			for (i = 1; i <= n; i++)
				for (s = 1; s <= 2; s++) {
					prefix = "E: The repository '"'"'" (s == 1 ? "https" : "http") "://" h[i]
					rest = substr($0, length(prefix) + 1, 1)
					if (index($0, prefix) == 1 && (rest == "/" || rest == " ")) next
				}
		}
		{ print }
	')
	rm -f "$apt_log"
	# shellcheck disable=SC2086
	if [ -n "$refused" ] && [ -z "$other" ] && installed $packages && command -v clickhouse-server >/dev/null; then
		echo "::warning title=apt::apply.sh: ${refused}refused its package index; deployed without updating packages"
	else
		echo "apply.sh: apt-get update failed" >&2
		exit 1
	fi
fi
