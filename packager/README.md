# Packager

The packager turns the server's normalized archive into day-partitioned Iceberg tables that anyone can query. It contains no parsers and no dedup rule: the server decided all of that at ingest, and the normalized stream records what it decided.

```sh
./packager.py                                                       # fetch from the bucket: every closed day of the past week missing from the catalog
./packager.py --date 2026-09-01                                     # fetch that day from the bucket and package it
./packager.py --normalized ../server/normalized --date 2026-09-01   # package from a local normalized tree
```

Without `--normalized` the job fetches the day's hours from `NORMALIZED_BUCKET` into `raw/`, packages them, and deletes them, so it does not depend on what any box still holds on disk. That needs `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY`, the same S3 keys the server uploads with. The layout is flat, so a day costs three prefix listings rather than a walk of the bucket, and listing stays flat as the archive grows.

The local catalog is SQLite under `warehouse/`. Set `LAKE_CATALOG_URI`, `LAKE_WAREHOUSE`, and `LAKE_CATALOG_TOKEN` to write to R2 Data Catalog instead. `PACKAGER_HOME` moves `stage/` and `warehouse/`.

## Tables

All wire-precision conventions match the stream: lat/lon as 1/600000 degree integers, SOG in 0.1 kn (1023 = n/a), COG in 0.1 degree (3600 = n/a), heading in degrees (511 = n/a).

**ais.positions** — one row per accepted position transmission (types 1, 2, 3, 18, 19, 27). Key: (`id`, `ts`). `id` is the server's content hash, so identical payloads transmitted minutes apart share an `id` and are distinct rows; a consumer counting transmissions counts rows, one tracking content changes takes `DISTINCT id`. Events the stream flagged implausible or stale are archived upstream but withheld here, exactly as the live stream withheld them. A same-`id` pair inside the server's 10 s window (a crash-window re-accept) collapses to one row.

**ais.receptions** — one row per copy heard of each position: `id`, `ts`, `source`, `station`, `recv_ts`, `license`, joined to its transmission by id and canonical proximity, the same rule the server used to call it a copy. Licenses live here because a license governs a delivery, not the broadcast it carried.

**ais.vessels** — latest-wins static data per MMSI, per field: `name`, `callsign`, `ship_type`, `draught10`, `cls` (from position message types, the truthful class signal), `updated_ts`. Overwritten each run.

**ais.weather** — BarentsWatch MetHyd broadcasts (IMO SN.1/Circ.289 DAC 1 FI 31, and its FI 11 predecessor, both live): one row per broadcast from an instrumented aid to navigation, measurements as sent, enums as strings, nulls where the station has no such sensor. `functional_id` distinguishes the two message generations; the payload's embedded observation time is broken on real stations and is not carried.

## Contract

- A day partition is written once, after the UTC day closes. Its presence in `ais.positions` means the day is done: Iceberg has no cross-table transaction, so weather, receptions, and vessels commit first and positions last. Reruns and backfills replace whole days.
- Schema changes are additive; a breaking change means a new table name.
- Envelope versions the packager does not know fail the run loudly rather than skipping records.
- Consumers publishing derived work must credit the attribution-requiring sources (NLOD-2.0, CC-BY-4.0); the per-reception `license` column makes finer-grained terms questions filters, not judgment calls.

In production the box runs this nightly: `packager.timer` fires at 01:30 UTC, after the closed day's last hours have rotated into the bucket, and `packager.service` runs `/opt/aiscast/packager.py` under uv for every closed day of the past week still missing from the catalog, so a failed night heals on the next. It skips itself while `/etc/aiscast.env` has no `LAKE_CATALOG_URI`. Both units live in [server/deploy/rootfs](../server/deploy/rootfs/etc/systemd/system), and `packager.py` ships in the same bundle as the server binary, so a deploy updates the script and the units together. `PACKAGER_HOME` points the staging and fetch directories at `/var/lib/aiscast/packager`.
