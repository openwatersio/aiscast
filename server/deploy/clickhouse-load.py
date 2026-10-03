#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb"]
# ///
"""Load packaged positions from the lake into ClickHouse, one day at a time.

Run on the box, with the lake's credentials from /etc/aiscast.env. It runs under uv, which fetches DuckDB
for it; apply.sh does not install uv, so a box needs it once first:

    curl -LsSf https://astral.sh/uv/install.sh | sh

    set -a; . /etc/aiscast.env; set +a
    ./clickhouse-load.py 2026-08-20 2026-10-01

Each day of ais.positions is exported to Parquet with DuckDB and inserted with clickhouse-client, in the
encodings the server writes, into the database CLICKHOUSE_URL names, or aiscast. clickhouse-client connects
with its defaults, localhost:9000 as the default user, which is where the box runs ClickHouse. Inserting into positions
fills the rollups. Load through the day the server started writing to ClickHouse, so the rollups have all
of it: the part of that day written live is in positions twice until it expires, and the rollups keep one
first position per window either way. A day loaded again may be in positions twice, so TRUNCATE the tables
before reloading.
"""

import os
import subprocess
import sys
import tempfile
from datetime import date, timedelta
from urllib.parse import urlparse

import duckdb

first, last = (date.fromisoformat(a) for a in sys.argv[1:3])
db = urlparse(os.environ.get("CLICKHOUSE_URL", "")).path.strip("/") or "aiscast"
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
