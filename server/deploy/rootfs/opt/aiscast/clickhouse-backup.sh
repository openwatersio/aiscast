#!/bin/sh
# Nightly backup of the aiscast database to the backups disk on R2. The first backup of a month is full;
# every other night is incremental against that month's full backup, so a restore needs two backups at most.
# The bucket's lifecycle rule deletes objects after 70 days, which always leaves the current month's chain whole.
set -eu
month=$(date -u +%Y-%m)
day=$(date -u +%Y-%m-%dT%H%M)
q() { clickhouse-client --query "$1"; }
# An incremental backup needs the month's full one, which lives in the bucket rather than in this box's logs, so
# a replacement box restored from it carries on. Without it, as on the first night of a month, the full one is made.
if ! q "BACKUP DATABASE aiscast TO Disk('backups', 'incr-$day') SETTINGS base_backup = Disk('backups', 'full-$month')"; then
	q "BACKUP DATABASE aiscast TO Disk('backups', 'full-$month')"
fi
# Alloy's textfile collector ships this, and ClickHouseBackupStale alerts on its age.
dir=/var/lib/alloy/textfile
mkdir -p "$dir"
printf '# TYPE aiscast_clickhouse_backup_last_success_timestamp_seconds gauge\naiscast_clickhouse_backup_last_success_timestamp_seconds %s\n' "$(date +%s)" >"$dir/clickhouse-backup.prom.tmp"
mv "$dir/clickhouse-backup.prom.tmp" "$dir/clickhouse-backup.prom"
