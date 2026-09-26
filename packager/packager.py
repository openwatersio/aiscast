#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb", "pyarrow", "pyiceberg[sql-sqlite]"]
# ///
"""Packager: the normalized archive -> day-partitioned Iceberg tables.

Reads the server's normalized stream (versioned envelopes: accepted events, reception
copies, weather broadcasts) and packages closed UTC days into ais.positions,
ais.receptions, ais.vessels, and ais.weather. No parsers and no dedup rule live here;
the server decided all of that at ingest. See docs/normalized-archive.md.
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
PREFIX = "normalized/v1"  # the stream's place in the archive bucket, beside the license-prefixed raw layout
WINDOW_S = 10  # the server's dedupe window; joins and the restart collapse use it, never re-derive it

POSITIONS_SCHEMA = pa.schema([
    ("id", pa.string()), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("msg_type", pa.int8()),
    ("lat6", pa.int32()), ("lon6", pa.int32()), ("sog10", pa.int16()), ("cog10", pa.int16()),
    ("heading", pa.int16()), ("navstat", pa.int8()), ("corroborated", pa.bool_()), ("day", pa.date32()),
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
# MetHyd fields the weather table reads outside the measurement columns, and those it drops on
# purpose; server/coverage.go declares the same contract for the live capture check.
WEATHER_READ = ["mmsi", "msgtime", "functionalId", "latitude", "longitude"]
WEATHER_IGNORED = [
    "type", "messageType", "stream",  # envelope markers
    "designatedAreaCode",  # 1 for every IMO message
    "day", "hour", "minute",  # embedded observation time, broken on real stations; msgtime is the clock
]
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
    """A day's hour files. Records are filed and assigned to days by receive time, so a day is its own hours."""
    return re.compile(hour_pattern(datetime.fromisoformat(day)))


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
               coalesce(uncorroborated, false) AS uncorroborated,
               r->>'id' AS id,
               CAST(r->>'time' AS TIMESTAMP) AS ct,
               CAST(r->>'tx' AS TIMESTAMP) AS tx,
               CAST(r->>'mmsi' AS INTEGER) AS mmsi,
               r->>'msg_type' AS mt,
               r->>'source' AS source, r->>'station' AS station, r->>'license' AS license,
               CAST(r->>'msgtime' AS TIMESTAMP) AS wxts,
               r->'message' AS message, r
        FROM read_json(?, columns={k:'VARCHAR', v:'BIGINT', t:'TIMESTAMP', implausible:'BOOLEAN', stale:'BOOLEAN', uncorroborated:'BOOLEAN', r:'JSON'},
                       format='newline_delimited')
        """,
        [files],
    )
    bad = con.execute(f"SELECT count(*) FROM env WHERE v != {NORM_VERSION} OR k NOT IN {KINDS!r}").fetchone()[0]
    if bad:
        sys.exit(f"{bad} envelopes with an unknown version or kind; this packager speaks v{NORM_VERSION} {KINDS}")


def process_day(day, files, con, catalog, fingerprint=None):
    load_envelopes(con, files)
    day_start = datetime.fromisoformat(day)
    day_end = day_start + timedelta(days=1)

    # A record belongs to the day the server received it, not the day of its canonical time.
    # BarentsWatch satellite passes arrive hours late (nearly ten, observed), and a day written once
    # after it closes can only be complete if nothing that arrives later belongs to it. ts keeps the
    # transmission's own time, so a late report sits in the day it arrived with its true timestamp.

    # positions: accepted position events received in the day. Implausible and stale events are
    # archived in the normalized stream but withheld here, exactly as the live stream withheld them.
    # A crash-window re-accept (same id inside the server's window) folds into one row afterwards.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE positions AS
        SELECT id, mmsi, ts, msg_type, lat6, lon6, sog10, cog10, heading, navstat, NOT uncorroborated AS corroborated,
               CAST(recv AS DATE) AS day
        FROM (
            SELECT id, mmsi, ct AS ts, recv, uncorroborated,
                   CAST(message->>'MessageID' AS TINYINT) AS msg_type,
                   -- the event's lat/lon, not the message's: the server leaves them off for the
                   -- not-available values and (0,0), and its rule is the only one
                   CAST(round(CAST(r->>'lat' AS DOUBLE) * 600000) AS INTEGER) AS lat6,
                   CAST(round(CAST(r->>'lon' AS DOUBLE) * 600000) AS INTEGER) AS lon6,
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
              AND recv >= ? AND recv < ?
        )
        ORDER BY mmsi, ts
        """,
        [day_start, day_end],
    )
    con.register("prior_positions", prior_positions(catalog, day))
    collapse_reaccepts(con)

    # receptions: every copy of a position received in the day, joined to the transmission the server
    # named on it (tx). Proximity would be ambiguous: transmissions 0 s and 18 s apart both sit within
    # the window of a copy at 9 s. A copy of a re-accept the collapse folded follows it into the kept
    # row. A copy can arrive after midnight for a transmission the server received before it, so the
    # previous day's positions join too; a copy more than a day behind its transmission drops.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE receptions AS
        SELECT p.id, p.ts, c.source, c.station, c.recv AS recv_ts, c.license, CAST(c.recv AS DATE) AS day
        FROM (SELECT id, tx, source, station, recv, license FROM env WHERE k = 'copy' AND recv >= ? AND recv < ?) c
        LEFT JOIN folded f ON f.id = c.id AND f.ts = c.tx
        JOIN (SELECT id, ts FROM positions UNION ALL SELECT id, ts FROM prior_positions) p
          ON p.id = c.id AND p.ts = coalesce(f.to_ts, c.tx)
        ORDER BY source, station, recv
        """,
        [day_start, day_end],
    )

    # weather: BarentsWatch MetHyd verbatim, received in the day; ts is the broadcast's own time. The
    # payload's embedded observation day/hour/minute is broken on real stations and stays out.
    con.execute(
        f"""
        CREATE OR REPLACE TABLE weather AS
        SELECT mmsi, wxts AS ts,
               CAST(r->>'functionalId' AS TINYINT) AS functional_id,
               CAST(r->>'latitude' AS DOUBLE) AS lat, CAST(r->>'longitude' AS DOUBLE) AS lon,
               {weather_cols()}, CAST(recv AS DATE) AS day
        FROM env WHERE k = 'methyd' AND recv >= ? AND recv < ?
        ORDER BY mmsi, ts
        """,
        [day_start, day_end],
    )

    n_pos, n_rx, n_wx = (con.execute(f"SELECT count(*) FROM {t}").fetchone()[0] for t in ("positions", "receptions", "weather"))
    orphans = con.execute(
        "SELECT count(*) FROM positions p WHERE NOT EXISTS (SELECT 1 FROM receptions r WHERE r.id = p.id AND r.ts = p.ts)"
    ).fetchone()[0]
    if orphans:
        sys.exit(f"{day}: {orphans} positions without a reception; every transmission has a first copy, so the join lost data")

    refresh_vessels(con, catalog)
    # Iceberg has no cross-table transaction, so order is the guarantee: positions commits last, with
    # the inputs' fingerprint in the same transaction, and that fingerprint is the completion marker.
    for name in ("weather", "receptions"):
        replace_day(con, catalog, day, name)
    replace_day(con, catalog, day, "positions", {f"packaged.{day}": fingerprint} if fingerprint else None)
    print(f"{day}: {n_pos} positions, {n_rx} receptions, {n_wx} weather", file=sys.stderr)


def collapse_reaccepts(con):
    """Drop the day's positions the server would have called copies had it not lost its dedupe state
    in a crash. Like the server, each id compares against its last kept transmission, not merely the
    one before: 0 s, 9 s, 18 s is two transmissions. The previous day's positions seed that anchor,
    so a re-accept just after midnight folds into the transmission already packaged before it."""
    near = con.execute(
        f"""
        WITH ids AS (SELECT id, ts, rowid AS row FROM positions
                     UNION ALL SELECT id, ts, NULL FROM prior_positions),
             gaps AS (SELECT id, ts - LAG(ts) OVER (PARTITION BY id ORDER BY ts) AS gap FROM ids)
        SELECT id, ts, row FROM ids
        WHERE id IN (SELECT id FROM gaps WHERE gap < INTERVAL {WINDOW_S} SECONDS)
        ORDER BY id, ts, row NULLS FIRST
        """
    ).fetchall()
    drop, folded, last_id, anchor = [], [], None, None
    for id, ts, row in near:  # only ids with a pair inside the window: a handful a day
        if id != last_id:
            last_id, anchor = id, None
        if row is None or anchor is None or (ts - anchor).total_seconds() >= WINDOW_S:
            anchor = ts
        else:
            drop.append(row)
            folded.append((id, ts, anchor))
    # ponytail: a fold is known only on the day it happened, so a copy arriving after midnight for a
    # re-accept folded the previous day drops; persist folds if that ever shows up in the counts
    con.register("folded", pa.table({"id": [f[0] for f in folded], "ts": [f[1] for f in folded], "to_ts": [f[2] for f in folded]},
                                    schema=pa.schema([("id", pa.string()), ("ts", pa.timestamp("us")), ("to_ts", pa.timestamp("us"))])))
    if drop:
        con.register("dropped", pa.table({"row": drop}))
        con.execute("DELETE FROM positions WHERE rowid IN (SELECT row FROM dropped)")


def prior_positions(catalog, day):
    """The previous day's transmissions (id, ts), for copies that arrive after midnight."""
    prev = (datetime.fromisoformat(day) - timedelta(days=1)).date().isoformat()
    tbl = retry(lambda: catalog.load_table("ais.positions"))
    return retry(lambda: tbl.scan(row_filter=f"day = '{prev}'", selected_fields=("id", "ts")).to_arrow())


def replace_day(con, catalog, day, name, properties=None):
    def go():
        tbl = catalog.load_table(f"ais.{name}")
        data = con.execute(f"SELECT * FROM {name}").to_arrow_table().cast(tbl.schema().as_arrow())
        with tbl.transaction() as tx:
            tx.delete(f"day = '{day}'")  # rerunning a day replaces it
            tx.append(data)
            if properties:
                tx.set_properties(properties)

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


def list_day(day):
    """A day's normalized hours in the bucket, as (fs, bucket, file infos).

    The layout is flat (normalized/v1/YYYY/MM/DD/HH.gz), so this lists one day prefix rather than walking
    the bucket: listing cost stays flat as the archive grows.
    """
    import pyarrow.fs as pafs

    fs, bucket = normalized_bucket()
    d = datetime.fromisoformat(day)
    want = day_key(day)
    sel = pafs.FileSelector(f"{bucket}/{PREFIX}/{d:%Y/%m/%d}", allow_not_found=True)
    return fs, bucket, [i for i in retry(lambda: fs.get_file_info(sel)) if want.search(i.path)]


def fetch_day(day, dest, listing):
    """Copy a listed day's hours into dest. Copying before reading keeps a mid-transfer reset a
    retryable per-file failure instead of a short day, and each file's size is checked against the
    object it came from."""
    import pyarrow.fs as pafs
    from concurrent.futures import ThreadPoolExecutor

    fs, bucket, infos = listing
    local = pafs.LocalFileSystem()

    def fetch(info):
        path = Path(dest) / info.path[len(bucket) + 1 :]
        path.parent.mkdir(parents=True, exist_ok=True)
        retry(lambda: _fetch(fs, local, info, path))
        return str(path)

    with ThreadPoolExecutor(8) as pool:
        out = list(pool.map(fetch, infos))
    print(f"{day}: fetched {len(out)} normalized hours from {bucket}", file=sys.stderr)
    return sorted(out)


def fingerprint(day, stats):
    """The day's inputs as hour:size:mtime triples. Packaging records it, and a later run repackages
    the day when it differs: an hour whose upload was delayed, one re-uploaded longer, or one
    overwritten by a replay at the same size lands in the tables instead of being skipped because
    the day was already there. Every write of an object changes its modification time."""
    pat = re.compile(hour_pattern(datetime.fromisoformat(day)))
    return ",".join(sorted(f"{m.group(1)}:{size}:{mtime}" for path, size, mtime in stats if (m := pat.search(path))))


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
    ap.add_argument("--normalized", help="local normalized tree (normalized/v1/YYYY/MM/DD/HH.gz); omit to fetch the day from the bucket")
    ap.add_argument("--date", help="UTC day YYYY-MM-DD; default: every closed day of the past week missing from the catalog")
    ap.add_argument("--min-hours", type=int, default=20, help="refuse a day with fewer distinct hours")
    args = ap.parse_args()

    all_files = glob.glob(f"{args.normalized}/**/*.gz", recursive=True) if args.normalized else []
    now = datetime.now(timezone.utc)
    today = now.strftime("%Y-%m-%d")
    catalog = get_catalog()
    days = [args.date] if args.date else [(now - timedelta(days=n)).strftime("%Y-%m-%d") for n in range(7, 0, -1)]
    packaged = retry(lambda: catalog.load_table("ais.positions")).properties

    stage = HERE / "stage"
    stage.mkdir(parents=True, exist_ok=True)
    con = duckdb.connect(str(stage / "packager.duckdb"))
    con.execute(f"SET memory_limit='4GB'; SET temp_directory='{stage}/tmp'")
    failed = []
    for day in days:
        fetched = None
        try:
            if day >= today:
                sys.exit(f"{day} is not over yet")
            if args.normalized:
                files = sorted(f for f in all_files if day_key(day).search(f))
                fp = fingerprint(day, ((f, os.path.getsize(f), os.path.getmtime(f)) for f in files))
            else:
                listing = list_day(day)
                fp = fingerprint(day, ((i.path, i.size, i.mtime.timestamp()) for i in listing[2]))
            if not args.date and packaged.get(f"packaged.{day}") == fp:
                continue  # packaged from exactly these hours
            if not fp:
                print(f"{day}: no normalized hours; nothing to package", file=sys.stderr)
                continue  # before the stream existed, or a day the server never ran
            n_hours = len(fp.split(","))
            if n_hours < args.min_hours:
                sys.exit(f"{day}: only {n_hours} distinct hours present, wanted {args.min_hours}; pass --min-hours to override")
            if not args.normalized:
                fetched = HERE / "raw" / day
                files = fetch_day(day, fetched, listing)
            process_day(day, files, con, catalog, fp)
        except (Exception, SystemExit) as e:  # one bad day must not hold back the rest of the week
            print(f"{day}: {e}", file=sys.stderr)
            failed.append(day)
        finally:
            if fetched:
                shutil.rmtree(fetched, ignore_errors=True)  # re-fetchable; the bucket is the source of truth
    if failed:
        sys.exit(f"failed: {', '.join(failed)}")


if __name__ == "__main__":
    main()
