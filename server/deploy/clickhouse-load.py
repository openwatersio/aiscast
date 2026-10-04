#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb"]
# ///
"""Load receptions into ClickHouse from the lake, for the days before the server wrote receptions itself.

Run on the box, with the lake's credentials from /etc/aiscast.env. It runs under uv, which fetches DuckDB
for it; apply.sh does not install uv, so a box needs it once first:

    curl -LsSf https://astral.sh/uv/install.sh | sh

    set -a; . /etc/aiscast.env; set +a
    ./clickhouse-load.py 2026-08-20 2026-10-04 --until 2026-10-04T15:30:00Z

From the lake, each day of ais.receptions is joined to ais.positions for the position each copy carried,
exported to Parquet with DuckDB, and inserted with clickhouse-client. A copy's transmission is named as the
server's txOf names it, the event id's first 64 bits XORed with the canonical time in milliseconds, so a copy
loaded here and one the server wrote agree; the copy that arrived first is the accepted one, which the rollups
read.

The lake holds less than the server now writes. Its positions are the transmissions the stream accepted, and
its receptions only the copies of those, each joined to its transmission: the stale and implausible events the
stream withheld are in the normalized archive and nowhere in the lake. So for these days receptions has every
accepted transmission and every copy dedupe matched to one, but not the rebuilt copies the server now matches
to a recent position, the genuine late reports it now keeps, or the implausible ones it now flags. Replaying
the raw archive through a server attached to ClickHouse would write those as the live writer does.

--until is when the server began writing receptions, the time of its "renamed aiscast.positions to
positions_old" log line: copies received from then on are already there. Wait until the packager has packaged
the day of the switch, then load through it. Every reception then comes from the server or the lake, and both
name transmissions with txOf, so a copy loaded here and another copy of the same transmission always agree.
positions_old held only accepted copies with no event id to name them by, so nothing loads from it.

clickhouse-client connects with its defaults, localhost:9000 as the default user, which is where the box runs
ClickHouse, into the database CLICKHOUSE_URL names, or aiscast. Inserting into receptions fills the rollups,
which keep one first position per window however often a day is loaded. receptions does not: each day goes in
under its own deduplication token, so a day loaded again whole is skipped, but a day loaded after a partial
load is in receptions twice. Delete a day before loading it again.
"""

import os
import subprocess
import sys
import tempfile
from datetime import date, datetime, timedelta, timezone
from urllib.parse import urlparse

db = urlparse(os.environ.get("CLICKHOUSE_URL", "")).path.strip("/") or "aiscast"

# The columns receptions takes, and how each comes out of the Parquet the load writes. tx is computed here,
# in ClickHouse, as the server's txOf computes it.
INSERT = f"""INSERT INTO {db}.receptions
    (mmsi, ts, tx, recv_ts, lat6, lon6, sog10, cog10, heading, navstat, source, station, accepted, corroborated, clock_bad)
    SELECT mmsi, ts, bitXor(reinterpretAsUInt64(reverse(unhex(substring(id, 1, 16)))), toUInt64(toUnixTimestamp64Milli(ts))),
           recv_ts, lat6, lon6, sog10, cog10,
           heading, navstat, source, station, accepted, corroborated, dateDiff('second', ts, recv_ts) >= 86400
    FROM input('mmsi UInt32, ts DateTime64(3, \\'UTC\\'), id String, recv_ts DateTime64(3, \\'UTC\\'), lat6 Int32, lon6 Int32,
                sog10 UInt16, cog10 UInt16, heading UInt16, navstat UInt8, source String, station String,
                accepted Bool, corroborated Bool')
    FORMAT Parquet"""


def insert(token, path):
    with open(path, "rb") as data:
        subprocess.run(["clickhouse-client", f"--insert_deduplication_token={token}", "--query", INSERT], stdin=data, check=True)


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
            con.execute(f"""COPY (
                SELECT r.mmsi::UINTEGER AS mmsi, r.ts, lower(hex(r.id)) AS id, r.recv_ts, p.lat6, p.lon6,
                       coalesce(p.sog10, 1023)::USMALLINT AS sog10, coalesce(p.cog10, 3600)::USMALLINT AS cog10,
                       coalesce(p.heading, 511)::USMALLINT AS heading,
                       (CASE WHEN p.navstat BETWEEN 0 AND 14 THEN p.navstat ELSE 15 END)::UTINYINT AS navstat,
                       split_part(r.source, ':', 1) AS source, r.station, r.accepted,
                       coalesce(p.corroborated, true) AS corroborated
                FROM (
                    SELECT *, row_number() OVER (PARTITION BY id, ts ORDER BY recv_ts) = 1 AS accepted
                    FROM lake.ais.receptions WHERE day BETWEEN DATE '{d}' - 1 AND DATE '{d}'
                ) r
                JOIN lake.ais.positions p ON p.id = r.id AND p.ts = r.ts
                WHERE r.day = DATE '{d}' AND p.day BETWEEN DATE '{d}' - 1 AND DATE '{d}' AND p.lat6 IS NOT NULL {before}
            ) TO '{f.name}' (FORMAT parquet)""")
            insert(f"lake-{d}", f.name)
        print(d, flush=True)


args = sys.argv[1:]
until = None
if "--until" in args:
    i = args.index("--until")
    until = datetime.fromisoformat(args[i + 1].replace("Z", "+00:00"))
    del args[i:i + 2]
from_lake(*(date.fromisoformat(a) for a in args[:2]), until)
