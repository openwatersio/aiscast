#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb"]
# ///
"""Load packaged positions from the lake into ClickHouse, one day at a time.

Run on the box, with the lake's credentials from /etc/aiscast.env:

    set -a; . /etc/aiscast.env; set +a
    ./clickhouse-load.py 2026-08-20 2026-10-01

Each day of ais.positions is exported to Parquet with DuckDB and inserted with clickhouse-client, in the
encodings the server writes. Inserting into positions fills the rollups. A day loaded twice is in
positions twice: TRUNCATE the tables before loading again.
"""

import os
import subprocess
import sys
import tempfile
from datetime import date, timedelta

import duckdb

first, last = (date.fromisoformat(a) for a in sys.argv[1:3])
db = os.environ.get("CLICKHOUSE_DB", "aiscast")
acct, bucket = os.environ["R2_ACCOUNT_ID"], os.environ.get("LAKE_BUCKET", "ais-lake")

con = duckdb.connect()
for s in ["SET threads = 4", "SET memory_limit = '2GB'", "INSTALL iceberg", "LOAD iceberg", "INSTALL httpfs", "LOAD httpfs",
          f"CREATE SECRET lake (TYPE ICEBERG, TOKEN '{os.environ['LAKE_CATALOG_TOKEN']}')",
          f"ATTACH '{acct}_{bucket}' AS lake (TYPE ICEBERG, ENDPOINT 'https://catalog.cloudflarestorage.com/{acct}/{bucket}', READ_ONLY)"]:
    con.execute(s)

d = first
while d <= last:
    with tempfile.NamedTemporaryFile(suffix=".parquet") as f:
        # The lake keeps -1 for a missing navigational status and nulls for missing motion; the server
        # writes 15, 1023, 3600, and 511. A source reduces to its kind, as the track store keeps it.
        con.execute(f"""COPY (
            SELECT mmsi::UINTEGER AS mmsi, ts, lat6, lon6,
                   coalesce(sog10, 1023)::USMALLINT AS sog10, coalesce(cog10, 3600)::USMALLINT AS cog10,
                   coalesce(heading, 511)::USMALLINT AS heading,
                   (CASE WHEN navstat BETWEEN 0 AND 14 THEN navstat ELSE 15 END)::UTINYINT AS navstat,
                   split_part(coalesce(source, ''), ':', 1) AS source
            FROM lake.ais.positions WHERE day = DATE '{d}' AND lat6 IS NOT NULL
        ) TO '{f.name}' (FORMAT parquet)""")
        with open(f.name, "rb") as data:
            subprocess.run(["clickhouse-client", "--query", f"INSERT INTO {db}.positions FORMAT Parquet"], stdin=data, check=True)
    print(d, flush=True)
    d += timedelta(days=1)
