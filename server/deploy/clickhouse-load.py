#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb"]
# ///
"""Fill receptions in ClickHouse: from the lake, or by converting receptions_v1 to the current layout.

Run on the box, with the lake's credentials from /etc/aiscast.env. It runs under uv, which fetches DuckDB
for it; apply.sh does not install uv, so a box needs it once first:

    curl -LsSf https://astral.sh/uv/install.sh | sh

    set -a; . /etc/aiscast.env; set +a
    ./clickhouse-load.py 2026-08-20 2026-10-04 --until 2026-10-04T15:30:00Z
    ./clickhouse-load.py --convert 2026-08-20 2026-10-08

From the lake, each day of ais.receptions is joined to ais.positions for the position each copy carried,
exported to Parquet with DuckDB, and inserted with clickhouse-client. A copy's transmission is named as the
server names it: the time its accepted copy was stamped, and tx_disc, the low byte of the event id's first 64
bits. The lake's copies all carry their transmission's time, so tx_off is 0. The copy that arrived first is the
accepted one, which positions_1m reads. A position is moving when it reports more than half a knot, or, with no
speed, when it is more than 50 m from the vessel's previous transmission, as the server decides.

The lake holds less than the server now writes. Its positions are the transmissions the stream accepted, and
its receptions only the copies of those, each joined to its transmission: the stale and implausible events the
stream withheld are in the normalized archive and nowhere in the lake. So for these days receptions has every
accepted transmission and every copy dedupe matched to one, but not the rebuilt copies the server now matches
to a recent position, the genuine late reports it now keeps, or the implausible ones it now flags. Replaying
the raw archive through a server attached to ClickHouse would write those as the live writer does.

--until is when the server began writing receptions, the time of its "renamed aiscast.positions to
positions_old" log line: copies received from then on are already there. Wait until the packager has packaged
the day of the switch, then load through it.

--convert copies receptions_v1, the first layout, with a 64-bit tx and recv_ts, which the server renamed when it
moved to the current one, into receptions, a day at a time. A v1 tx was the event id's first 64 bits XORed with
the accepted copy's time in milliseconds, so the accepted copy's time recovers the id's byte exactly, and a
transmission whose copies straddle the switch has one name. Each day runs in eight passes of whole vessels,
within 1.5 GB, so it never takes the memory the live writer needs. Drop receptions_v1 once the days compare.

clickhouse-client connects with its defaults, localhost:9000 as the default user, which is where the box runs
ClickHouse, into the database CLICKHOUSE_URL names, or aiscast. Inserting into receptions fills positions_1m,
which keeps one row per window however often a day is loaded. receptions does not: each day goes in under its
own deduplication token, so a day loaded again whole is skipped, but a day loaded after a partial load is in
receptions twice. Delete a day before loading it again.
"""

import os
import subprocess
import sys
import tempfile
from datetime import date, datetime, timedelta, timezone
from urllib.parse import urlparse

db = urlparse(os.environ.get("CLICKHOUSE_URL", "")).path.strip("/") or "aiscast"

COLUMNS = "mmsi, ts, tx_off, tx_disc, recv_delay, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, clock_bad, moving"
CONVERTED = "mmsi, ts, tx_off, tx_disc, recv_delay, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, implausible, clock_bad, moving"

# The server's discOf, from a hex event id.
DISC = "toUInt8(reinterpretAsUInt64(reverse(unhex(substring(id, 1, 16)))) % 256)"

# How each column comes out of the Parquet the lake load writes.
INSERT = f"""INSERT INTO {db}.receptions ({COLUMNS})
    SELECT mmsi, ts, 0, {DISC}, toInt32(least(greatest(dateDiff('millisecond', ts, recv_ts), -2147483648), 2147483647)),
           lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated,
           dateDiff('second', ts, recv_ts) >= 86400, moving
    FROM input('mmsi UInt32, ts DateTime64(3, \\'UTC\\'), id String, recv_ts DateTime64(3, \\'UTC\\'), lat6 Int32, lon6 Int32,
                sog10 UInt16, cog10 UInt16, heading UInt16, navstat UInt8, source String, station String,
                accepted Bool, corroborated Bool, moving Bool')
    FORMAT Parquet"""

# Every query runs within these, so loading never takes what the live writer needs.
LIMITS = ["--max_memory_usage=1500000000", "--max_threads=2", "--max_bytes_before_external_sort=500000000",
          "--max_bytes_before_external_group_by=500000000"]


def clickhouse(query, token=None, stdin=None):
    args = ["clickhouse-client", *LIMITS, "--query", query]
    if token:
        args.insert(1, f"--insert_deduplication_token={token}")
    subprocess.run(args, stdin=stdin, check=True)


def days(first, last):
    d = first
    while d <= last:
        yield d
        d += timedelta(days=1)


def from_lake(first, last, until):
    import duckdb

    acct, bucket = os.environ["R2_ACCOUNT_ID"], os.environ.get("LAKE_BUCKET", "ais-lake")
    con = duckdb.connect()
    for s in ["SET threads = 4", "SET memory_limit = '2GB'", "INSTALL iceberg", "LOAD iceberg", "INSTALL httpfs", "LOAD httpfs",
              f"CREATE SECRET lake (TYPE ICEBERG, TOKEN '{os.environ['LAKE_CATALOG_TOKEN']}')",
              f"ATTACH '{acct}_{bucket}' AS lake (TYPE ICEBERG, ENDPOINT 'https://catalog.cloudflarestorage.com/{acct}/{bucket}', READ_ONLY)"]:
        con.execute(s)
    before = ""
    if until is not None:
        before = "AND r.recv_ts < TIMESTAMP '" + until.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f") + "'"
    for d in days(first, last):
        with tempfile.NamedTemporaryFile(suffix=".parquet") as f:
            # A copy's day is the day it arrived, which can be the day after its transmission's: the server drops
            # a copy more than a day behind. So positions join from the day before as well, and a copy is the
            # accepted one only if it arrived first among the copies of both days, or a late copy of yesterday's
            # transmission would count as a first copy today. The lake keeps -1 for a missing navigational status
            # and nulls for missing motion; the server writes 15, 1023, 3600, and 511. A source reduces to its kind.
            # The distance to the vessel's previous transmission is flat-earth, plenty for 50 m.
            con.execute(f"""COPY (
                SELECT r.mmsi::UINTEGER AS mmsi, r.ts, lower(hex(r.id)) AS id, r.recv_ts, p.lat6, p.lon6,
                       coalesce(p.sog10, 1023)::USMALLINT AS sog10, coalesce(p.cog10, 3600)::USMALLINT AS cog10,
                       coalesce(p.heading, 511)::USMALLINT AS heading,
                       (CASE WHEN p.navstat BETWEEN 0 AND 14 THEN p.navstat ELSE 15 END)::UTINYINT AS navstat,
                       split_part(r.source, ':', 1) AS source, r.station, r.accepted,
                       coalesce(p.corroborated, true) AS corroborated,
                       CASE WHEN p.sog10 IS NOT NULL AND p.sog10 != 1023 THEN p.sog10 > 5
                            WHEN p.plat6 IS NULL THEN true
                            ELSE sqrt(power((p.lat6 - p.plat6) / 600000 * 111320, 2)
                                    + power((p.lon6 - p.plon6) / 600000 * 111320 * cos(radians(p.lat6 / 600000)), 2)) > 50
                       END AS moving
                FROM (
                    SELECT *, row_number() OVER (PARTITION BY id, ts ORDER BY recv_ts) = 1 AS accepted
                    FROM lake.ais.receptions WHERE day BETWEEN DATE '{d}' - 1 AND DATE '{d}'
                ) r
                JOIN (
                    SELECT *, lag(lat6) OVER (PARTITION BY mmsi ORDER BY ts) AS plat6, lag(lon6) OVER (PARTITION BY mmsi ORDER BY ts) AS plon6
                    FROM lake.ais.positions WHERE day BETWEEN DATE '{d}' - 1 AND DATE '{d}' AND lat6 IS NOT NULL
                ) p ON p.id = r.id AND p.ts = r.ts
                WHERE r.day = DATE '{d}' {before}
            ) TO '{f.name}' (FORMAT parquet)""")
            with open(f.name, "rb") as data:
                clickhouse(INSERT, token=f"lake-{d}", stdin=data)
        print(d, flush=True)


def convert(first, last, passes=8):
    for d in days(first, last):
        lo, hi = f"toDateTime64('{d}', 3, 'UTC')", f"toDateTime64('{d}', 3, 'UTC') + INTERVAL 1 DAY"
        for k in range(passes):
            # A transmission's accepted copy is its earliest-arriving accepted one; its copies sit within 5 minutes
            # of it, so the day is read with 10 minutes either side and only its own rows are written. A
            # transmission with no accepted copy in reach takes its earliest copy's time. A vessel's previous
            # transmission, for whether a position without a speed is moving, is its previous accepted copy.
            clickhouse(f"""INSERT INTO {db}.receptions ({CONVERTED})
                SELECT r.mmsi, r.ts, toInt32(toUnixTimestamp64Milli(a.at) - toUnixTimestamp64Milli(r.ts)),
                       toUInt8(bitXor(r.tx, toUInt64(toUnixTimestamp64Milli(a.at))) % 256),
                       toInt32(least(greatest(dateDiff('millisecond', r.ts, r.recv_ts), -2147483648), 2147483647)),
                       r.lat6, r.lon6, r.sog10, r.cog10, r.heading, r.navstat, r.source, r.station,
                       r.accepted, r.corroborated, r.implausible, r.clock_bad,
                       if(r.sog10 != 1023, r.sog10 > 5,
                          r.pts < toDateTime64('2000-01-01', 3, 'UTC')
                          OR greatCircleDistance(r.lon6 / 600000, r.lat6 / 600000, r.plon6 / 600000, r.plat6 / 600000) > 50)
                FROM (
                    SELECT *, lagInFrame(lat6) OVER w AS plat6, lagInFrame(lon6) OVER w AS plon6, lagInFrame(ts) OVER w AS pts
                    FROM {db}.receptions_v1
                    WHERE ts >= {lo} - INTERVAL 10 MINUTE AND ts < {hi} + INTERVAL 10 MINUTE AND mmsi % {passes} = {k}
                    WINDOW w AS (PARTITION BY mmsi, accepted ORDER BY ts ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
                ) AS r
                INNER JOIN (
                    SELECT mmsi, tx, if(countIf(accepted) > 0, argMinIf(ts, recv_ts, accepted), min(ts)) AS at
                    FROM {db}.receptions_v1
                    WHERE ts >= {lo} - INTERVAL 10 MINUTE AND ts < {hi} + INTERVAL 10 MINUTE AND mmsi % {passes} = {k}
                    GROUP BY mmsi, tx
                ) AS a ON a.mmsi = r.mmsi AND a.tx = r.tx
                WHERE r.ts >= {lo} AND r.ts < {hi}""", token=f"convert-{d}-{k}")
        print(d, flush=True)


args = sys.argv[1:]
if args and args[0] == "--convert":
    convert(*(date.fromisoformat(a) for a in args[1:3]))
else:
    until = None
    if "--until" in args:
        i = args.index("--until")
        until = datetime.fromisoformat(args[i + 1].replace("Z", "+00:00"))
        del args[i:i + 2]
    from_lake(*(date.fromisoformat(a) for a in args[:2]), until)
