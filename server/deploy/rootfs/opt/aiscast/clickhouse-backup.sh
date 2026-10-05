#!/bin/sh
# Nightly backup of the aiscast database to the backups disk on R2. The first backup of a month is full;
# every other night is incremental against that month's full backup, so a restore needs two backups at most.
# The bucket's lifecycle rule deletes objects after 70 days, which always leaves the current month's chain whole.
set -eu
month=$(date -u +%Y-%m)
day=$(date -u +%Y-%m-%dT%H%M)
q() { clickhouse-client --query "$1"; }
# backup_log exists once the server has logged a backup, so a fresh box starts with a full one.
full=0
if [ "$(q "EXISTS TABLE system.backup_log")" = 1 ]; then
	full=$(q "SELECT count() FROM system.backup_log WHERE status = 'BACKUP_CREATED' AND name = 'Disk(\\'backups\\', \\'full-$month\\')'")
fi
if [ "$full" = 0 ]; then
	q "BACKUP DATABASE aiscast TO Disk('backups', 'full-$month')"
else
	q "BACKUP DATABASE aiscast TO Disk('backups', 'incr-$day') SETTINGS base_backup = Disk('backups', 'full-$month')"
fi
# Alloy's textfile collector ships this, and ClickHouseBackupStale alerts on its age.
dir=/var/lib/alloy/textfile
mkdir -p "$dir"
printf '# TYPE aiscast_clickhouse_backup_last_success_timestamp_seconds gauge\naiscast_clickhouse_backup_last_success_timestamp_seconds %s\n' "$(date +%s)" >"$dir/clickhouse-backup.prom.tmp"
mv "$dir/clickhouse-backup.prom.tmp" "$dir/clickhouse-backup.prom"
