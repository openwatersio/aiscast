#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb", "pyarrow", "pyiceberg[sql-sqlite]"]
# ///
"""Packager: the normalized archive -> day-partitioned Iceberg tables.

Reads the server's normalized stream (versioned envelopes: accepted events, reception
copies, weather broadcasts) and packages closed UTC days into ais.positions,
ais.receptions, ais.vessels, and ais.weather. No parsers and no dedup rule live here;
the server decided all of that at ingest. See specs/normalized-archive.md.
"""

import argparse
import glob
import os
import re
import shutil
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

import duckdb
import pyarrow as pa

HERE = Path(os.environ.get("PACKAGER_HOME", Path(__file__).parent))  # stage/ and warehouse/ live here

NORM_VERSION = 1
KINDS = ("event", "copy", "methyd")
POS_TYPES = "('PositionReport', 'StandardClassBPositionReport', 'ExtendedClassBPositionReport', 'LongRangeAisBroadcastMessage')"
WINDOW_S = 10  # the server's dedupe window; joins and the restart collapse use it, never re-derive it

POSITIONS_SCHEMA = pa.schema([
    ("id", pa.string()), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("msg_type", pa.int8()),
    ("lat6", pa.int32()), ("lon6", pa.int32()), ("sog10", pa.int16()), ("cog10", pa.int16()),
    ("heading", pa.int16()), ("navstat", pa.int8()), ("day", pa.date32()),
])
RECEPTIONS_SCHEMA = pa.schema([
    ("id", pa.string()), ("ts", pa.timestamp("us")), ("source", pa.string()), ("station", pa.string()),
    ("recv_ts", pa.timestamp("us")), ("license", pa.string()), ("day", pa.date32()),
])
VESSELS_SCHEMA = pa.schema([
    ("mmsi", pa.int32()), ("name", pa.string()), ("callsign", pa.string()), ("ship_type", pa.int16()),
    ("draught10", pa.int16()), ("cls", pa.string()), ("updated_ts", pa.timestamp("us")),
])
WEATHER_NUM = [
    "avg_wind_speed", "wind_gust", "wind_direction", "wind_gust_direction", "air_temperature",
    "relative_humidity", "dew_point", "air_pressure", "horizontal_visibility", "water_level",
    "surface_current_speed", "surface_current_direction", "current_speed2", "current_direction2",
    "current_measuring_level2", "current_speed3", "current_direction3", "current_measuring_level3",
    "significant_wave_height", "wave_period", "wave_direction", "swell_height", "swell_period",
    "swell_direction", "water_temperature", "salinity",
]
WEATHER_STR = ["air_pressure_tendency", "water_level_trend", "sea_state", "precipitation_type", "ice"]
WEATHER_SCHEMA = pa.schema(
    [("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("functional_id", pa.int8()),
     ("lat", pa.float64()), ("lon", pa.float64())]
    + [(c, pa.float64()) for c in WEATHER_NUM]
    + [(c, pa.string()) for c in WEATHER_STR]
    + [("day", pa.date32())]
)


def retry(fn, attempts=4):
    """Catalog writes cross the network; transient resets must not kill a nightly run."""
    import time

    for i in range(attempts):
        try:
            return fn()
        except Exception as e:
            if i == attempts - 1:
                raise
            print(f"  retrying after {type(e).__name__}: {e}", file=sys.stderr)
            time.sleep(5 * 2**i)


def hour_pattern(d, hour=r"\d{2}"):
    return rf"/{d:%Y/%m/%d}/({hour})\.gz$"


def day_key(day):
    """The day's hour files plus the neighbouring hours whose canonical times can cross midnight."""
    d = datetime.fromisoformat(day)
    return re.compile("|".join((hour_pattern(d), hour_pattern(d - timedelta(days=1), "23"), hour_pattern(d + timedelta(days=1), "00"))))


def hours_present(day, files):
    pat = re.compile(hour_pattern(datetime.fromisoformat(day)))
    return {m.group(1) for f in files if (m := pat.search(f))}


# camelCase source fields -> snake_case columns, verbatim values.
def weather_cols():
    def camel(snake):
        head, *rest = snake.split("_")
        return head + "".join(w.capitalize() for w in rest)

    parts = [f"CAST(r->>'{camel(c)}' AS DOUBLE) AS {c}" for c in WEATHER_NUM]
    parts += [f"r->>'{camel(c)}' AS {c}" for c in WEATHER_STR]
    return ", ".join(parts)


def load_envelopes(con, files):
    """Materialize the day's envelopes once, scalars extracted into typed columns up front: DuckDB
    1.5 mis-plans filters over JSON columns, and one extraction pass is cheaper anyway. The guard
    makes an unknown version loud, not silent."""
    con.execute(
        """
        CREATE OR REPLACE TABLE env AS
        SELECT k, v, t AS recv,
               coalesce(implausible, false) AS implausible, coalesce(stale, false) AS stale,
               r->>'id' AS id,
               CAST(r->>'time' AS TIMESTAMP) AS ct,
               CAST(r->>'mmsi' AS INTEGER) AS mmsi,
               r->>'msg_type' AS mt,
               r->>'source' AS source, r->>'station' AS station, r->>'license' AS license,
               CAST(r->>'msgtime' AS TIMESTAMP) AS wxts,
               r->'message' AS message, r
        FROM read_json(?, columns={k:'VARCHAR', v:'BIGINT', t:'TIMESTAMP', implausible:'BOOLEAN', stale:'BOOLEAN', r:'JSON'},
                       format='newline_delimited')
        """,
        [files],
    )
    bad = con.execute(f"SELECT count(*) FROM env WHERE v != {NORM_VERSION} OR k NOT IN {KINDS!r}").fetchone()[0]
    if bad:
        sys.exit(f"{bad} envelopes with an unknown version or kind; this packager speaks v{NORM_VERSION} {KINDS}")


def process_day(day, files, con, catalog):
    load_envelopes(con, files)
    day_start = datetime.fromisoformat(day)
    day_end = day_start + timedelta(days=1)

    # positions: accepted position events inside the day. Implausible and stale events are archived
    # in the normalized stream but withheld here, exactly as the live stream withheld them. The
    # LAG collapse folds a crash-window re-accept (same id inside the server's window) into one row.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE positions AS
        SELECT * EXCLUDE (prev) FROM (
          SELECT id, mmsi, ts, msg_type, lat6, lon6, sog10, cog10, heading, navstat, CAST(ts AS DATE) AS day,
                 LAG(ts) OVER (PARTITION BY id ORDER BY ts) AS prev
          FROM (
            SELECT id, mmsi, ct AS ts,
                   CAST(message->>'MessageID' AS TINYINT) AS msg_type,
                   CAST(round(CAST(message->>'Latitude' AS DOUBLE) * 600000) AS INTEGER) AS lat6,
                   CAST(round(CAST(message->>'Longitude' AS DOUBLE) * 600000) AS INTEGER) AS lon6,
                   CAST(CASE WHEN mt = 'LongRangeAisBroadcastMessage'
                        THEN CASE WHEN CAST(message->>'Sog' AS DOUBLE) >= 63 THEN 1023 ELSE round(CAST(message->>'Sog' AS DOUBLE) * 10) END
                        ELSE round(CAST(message->>'Sog' AS DOUBLE) * 10) END AS SMALLINT) AS sog10,
                   CAST(CASE WHEN mt = 'LongRangeAisBroadcastMessage'
                        THEN CASE WHEN CAST(message->>'Cog' AS DOUBLE) >= 511 THEN 3600 ELSE round(CAST(message->>'Cog' AS DOUBLE) * 10) END
                        ELSE round(CAST(message->>'Cog' AS DOUBLE) * 10) END AS SMALLINT) AS cog10,
                   CAST(coalesce(CAST(message->>'TrueHeading' AS INTEGER), 511) AS SMALLINT) AS heading,
                   CAST(coalesce(CAST(message->>'NavigationalStatus' AS INTEGER), -1) AS TINYINT) AS navstat
            FROM env
            WHERE k = 'event' AND NOT implausible AND NOT stale
              AND mt IN {POS_TYPES}
              AND ct >= ? AND ct < ?
          )
        ) WHERE prev IS NULL OR ts - prev >= INTERVAL {WINDOW_S} SECONDS
        ORDER BY mmsi, ts
        """,
        [day_start, day_end],
    )

    # receptions: every copy heard of a position in the day, joined to its transmission by id and
    # canonical proximity, the same rule the server used to call it a copy in the first place.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE receptions AS
        SELECT p.id, p.ts, c.source, c.station, c.recv AS recv_ts, c.license, p.day
        FROM (SELECT id, ct, source, station, recv, license FROM env WHERE k = 'copy') c
        JOIN positions p ON p.id = c.id AND abs(epoch(c.ct - p.ts)) < {WINDOW_S}
        ORDER BY source, station, recv
        """
    )

    # weather: BarentsWatch MetHyd verbatim, day-assigned by its own timestamp. The payload's
    # embedded observation day/hour/minute is broken on real stations and stays out of the table.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE weather AS
        SELECT mmsi, wxts AS ts,
               CAST(r->>'functionalId' AS TINYINT) AS functional_id,
               CAST(r->>'latitude' AS DOUBLE) AS lat, CAST(r->>'longitude' AS DOUBLE) AS lon,
               {weather_cols()}, CAST(wxts AS DATE) AS day
        FROM env WHERE k = 'methyd' AND wxts >= ? AND wxts < ?
        ORDER BY mmsi, ts
        """,
        [day_start, day_end],
    )

    n_pos, n_rx, n_wx = (con.execute(f"SELECT count(*) FROM {t}").fetchone()[0] for t in ("positions", "receptions", "weather"))
    if n_pos and n_rx < n_pos:
        sys.exit(f"{day}: {n_pos} positions but only {n_rx} receptions; every transmission has a first copy, so the join lost data")

    refresh_vessels(con, catalog)
    # Iceberg has no cross-table transaction, so order is the guarantee: positions commits last and
    # its day partition is the completion marker.
    for name in ("weather", "receptions"):
        replace_day(con, catalog, day, name)
    replace_day(con, catalog, day, "positions")
    print(f"{day}: {n_pos} positions, {n_rx} receptions, {n_wx} weather", file=sys.stderr)


def replace_day(con, catalog, day, name):
    def go():
        tbl = catalog.load_table(f"ais.{name}")
        data = con.execute(f"SELECT * FROM {name}").to_arrow_table().cast(tbl.schema().as_arrow())
        with tbl.transaction() as tx:
            tx.delete(f"day = '{day}'")  # rerunning a day replaces it
            tx.append(data)

    retry(go)


def refresh_vessels(con, catalog):
    tbl = retry(lambda: catalog.load_table("ais.vessels"))
    con.register("existing_vessels", retry(lambda: tbl.scan().to_arrow()))
    merged = con.execute(
        f"""
        WITH evidence AS (  -- position message types are the truthful class signal; statics are not
          SELECT mmsi, CASE WHEN bool_or(mt IN ('StandardClassBPositionReport', 'ExtendedClassBPositionReport')) THEN 'B'
                            WHEN bool_or(mt = 'PositionReport') THEN 'A' END AS ev_cls
          FROM env WHERE k = 'event' AND mt IN {POS_TYPES}
          GROUP BY mmsi
        ), statics AS (  -- stale statics still carry names; nothing here rots
          SELECT mmsi,
                 nullif(trim(coalesce(message->>'Name', message->'ReportA'->>'Name')), '') AS name,
                 nullif(trim(coalesce(message->>'CallSign', message->'ReportB'->>'CallSign')), '') AS callsign,
                 CAST(coalesce(CAST(message->>'Type' AS INTEGER), CAST(message->'ReportB'->>'ShipType' AS INTEGER), 0) AS SMALLINT) AS ship_type,
                 CAST(coalesce(round(CAST(message->>'MaximumStaticDraught' AS DOUBLE) * 10), 0) AS SMALLINT) AS draught10,
                 CASE WHEN mt = 'StaticDataReport' THEN 'B' ELSE 'A' END AS cls,
                 ct AS ts
          FROM env WHERE k = 'event' AND mt IN ('ShipStaticData', 'StaticDataReport')
        ), all_static AS (
          SELECT * FROM statics
          UNION ALL
          SELECT mmsi, name, callsign, ship_type, draught10, cls, updated_ts FROM existing_vessels
        ), merged AS (
          -- latest-wins per field, not per row: a type 24 part B can carry a callsign and no name,
          -- and that must not discard a name learned earlier
          SELECT mmsi,
            arg_max(name, ts) FILTER (WHERE name IS NOT NULL) AS name,
            arg_max(callsign, ts) FILTER (WHERE callsign IS NOT NULL) AS callsign,
            coalesce(arg_max(ship_type, ts) FILTER (WHERE ship_type > 0), 0) AS ship_type,
            coalesce(arg_max(draught10, ts) FILTER (WHERE draught10 > 0), 0) AS draught10,
            arg_max(cls, ts) FILTER (WHERE cls IS NOT NULL) AS cls,
            max(ts) AS updated_ts
          FROM all_static GROUP BY mmsi
        )
        SELECT m.mmsi, name, callsign, ship_type, draught10, coalesce(ev_cls, cls) AS cls, updated_ts
        FROM merged m LEFT JOIN evidence ON m.mmsi = evidence.mmsi ORDER BY m.mmsi
        """
    ).to_arrow_table()
    retry(lambda: tbl.overwrite(merged.cast(tbl.schema().as_arrow())))


def normalized_bucket():
    """The normalized stream bucket over the S3 API, with the same keys the server uploads with."""
    import pyarrow.fs as pafs

    return pafs.S3FileSystem(
        access_key=os.environ["R2_ACCESS_KEY_ID"],
        secret_key=os.environ["R2_SECRET_ACCESS_KEY"],
        endpoint_override=f"https://{os.environ['R2_ACCOUNT_ID']}.r2.cloudflarestorage.com",
        region="auto",
        scheme="https",
    ), os.environ["NORMALIZED_BUCKET"]


def fetch_day(day, dest):
    """Copy a day's normalized hours, and the two boundary hours, out of the bucket into dest.

    The layout is flat (v1/YYYY/MM/DD/HH.gz), so this lists three day prefixes rather than walking
    the bucket: listing cost stays flat as the archive grows. Copying before reading keeps a
    mid-transfer reset a retryable per-file failure instead of a short day, and each file's size is
    checked against the object it came from.
    """
    import pyarrow.fs as pafs
    from concurrent.futures import ThreadPoolExecutor

    fs, bucket = normalized_bucket()
    local = pafs.LocalFileSystem()
    d = datetime.fromisoformat(day)
    want = day_key(day)
    infos = []
    for pd in (d - timedelta(days=1), d, d + timedelta(days=1)):
        sel = pafs.FileSelector(f"{bucket}/v1/{pd:%Y/%m/%d}", allow_not_found=True)
        infos += [i for i in retry(lambda sel=sel: fs.get_file_info(sel)) if want.search(i.path)]

    def fetch(info):
        path = Path(dest) / info.path[len(bucket) + 1 :]
        path.parent.mkdir(parents=True, exist_ok=True)
        retry(lambda: _fetch(fs, local, info, path))
        return str(path)

    with ThreadPoolExecutor(8) as pool:
        out = list(pool.map(fetch, infos))
    print(f"{day}: fetched {len(out)} normalized hours from {bucket}", file=sys.stderr)
    return sorted(out)


def _fetch(fs, local, info, path):
    # compression=None on both sides: these are .gz keys and the default "detect" would decompress
    # on read and recompress on write, transcoding the archive byte-for-byte.
    with fs.open_input_stream(info.path, compression=None) as r, local.open_output_stream(str(path), compression=None) as w:
        while chunk := r.read(8 << 20):
            w.write(chunk)
    got = path.stat().st_size
    if got != info.size:
        path.unlink(missing_ok=True)  # a partial copy must not look like a complete hour
        raise OSError(f"{info.path}: copied {got} of {info.size} bytes")


def derived_days(catalog):
    """Day partitions already present in ais.positions, from table metadata only."""
    tbl = retry(lambda: catalog.load_table("ais.positions"))
    return {str(r["partition"]["day"]) for r in retry(lambda: tbl.inspect.partitions()).to_pylist()}


def get_catalog():
    from pyiceberg.catalog import load_catalog

    uri = os.environ.get("LAKE_CATALOG_URI")
    if uri:
        catalog = load_catalog("r2", uri=uri, warehouse=os.environ["LAKE_WAREHOUSE"], token=os.environ["LAKE_CATALOG_TOKEN"])
    else:
        wh = HERE / "warehouse"
        wh.mkdir(parents=True, exist_ok=True)
        catalog = load_catalog("local", uri=f"sqlite:///{wh}/catalog.db", warehouse=f"file://{wh}")
    retry(lambda: catalog.create_namespace_if_not_exists("ais"))
    for name, schema in [("positions", POSITIONS_SCHEMA), ("receptions", RECEPTIONS_SCHEMA), ("vessels", VESSELS_SCHEMA), ("weather", WEATHER_SCHEMA)]:
        if ("ais", name) not in retry(lambda: list(catalog.list_tables("ais"))):
            retry(lambda: catalog.create_table(f"ais.{name}", schema=schema))
        if "day" in schema.names:
            # identity partition on day, so replacing a day rewrites that partition, not the table
            tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
            if not tbl.spec().fields:
                retry(lambda: tbl.update_spec().add_identity("day").commit())
    return catalog


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--normalized", help="local normalized tree (v1/YYYY/MM/DD/HH.gz); omit to fetch the day from the bucket")
    ap.add_argument("--date", help="UTC day YYYY-MM-DD; default: every closed day of the past week missing from the catalog")
    ap.add_argument("--min-hours", type=int, default=20, help="refuse a day with fewer distinct hours")
    args = ap.parse_args()

    all_files = glob.glob(f"{args.normalized}/**/*.gz", recursive=True) if args.normalized else []
    now = datetime.now(timezone.utc)
    today = now.strftime("%Y-%m-%d")
    catalog = get_catalog()
    if args.date:
        days = [args.date]
    else:
        window = [(now - timedelta(days=n)).strftime("%Y-%m-%d") for n in range(7, 0, -1)]
        done = derived_days(catalog)
        days = [d for d in window if d not in done]
        if not days:
            print("nothing to package: the last 7 days are all present", file=sys.stderr)
            return

    stage = HERE / "stage"
    stage.mkdir(parents=True, exist_ok=True)
    con = duckdb.connect(str(stage / "packager.duckdb"))
    con.execute(f"SET memory_limit='4GB'; SET temp_directory='{stage}/tmp'")
    for day in days:
        if day >= today:
            sys.exit(f"{day} is not over yet")
        fetched = None
        try:
            if args.normalized:
                want = day_key(day)
                files = sorted(f for f in all_files if want.search(f))
            else:
                fetched = HERE / "raw" / day
                files = fetch_day(day, fetched)
            n_hours = len(hours_present(day, files))
            if n_hours < args.min_hours:
                sys.exit(f"{day}: only {n_hours} distinct hours present, wanted {args.min_hours}; pass --min-hours to override")
            process_day(day, files, con, catalog)
        finally:
            if fetched:
                shutil.rmtree(fetched, ignore_errors=True)  # re-fetchable; the bucket is the source of truth


if __name__ == "__main__":
    main()
