#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["duckdb", "pyarrow", "pyiceberg[sql-sqlite,pyiceberg-core]"]
# ///
"""Packager: the normalized archive -> day-partitioned Iceberg tables.

Reads the server's normalized stream (versioned envelopes: accepted events, reception
copies, weather broadcasts) and packages closed UTC days into ais.positions,
ais.receptions, ais.vessels, and ais.weather, and rolls positions up into ais.tracks.
No parsers and no dedup rule live here; the server decided all of that at ingest. See
docs/normalized-archive.md.
"""

import argparse
import glob
import os
import re
import shutil
import sys
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

import duckdb
import pyarrow as pa

HERE = Path(os.environ.get("PACKAGER_HOME", Path(__file__).parent))  # stage/ and warehouse/ live here

NORM_VERSION = 1
KINDS = ("event", "copy", "methyd")
POS_TYPES = "('PositionReport', 'StandardClassBPositionReport', 'ExtendedClassBPositionReport', 'LongRangeAisBroadcastMessage')"
PREFIX = "normalized/v1"  # the stream's place in the archive bucket, beside the license-prefixed raw layout
WINDOW_S = 10  # the server's dedupe window; joins and the restart collapse use it, never re-derive it

# id is the server's 16-byte content hash, stored raw: the stream carries it as 32 hex characters,
# and hex would double the widest column in every row.
POSITIONS_SCHEMA = pa.schema([
    ("id", pa.binary(16)), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("msg_type", pa.int8()),
    ("lat6", pa.int32()), ("lon6", pa.int32()), ("cell", pa.int32()), ("sog10", pa.int16()), ("cog10", pa.int16()),
    ("heading", pa.int16()), ("navstat", pa.int8()), ("corroborated", pa.bool_()), ("day", pa.date32()),
    # the source whose copy the server accepted, the one a track's credit line names; last, since it
    # joined after the first days were packaged and a new column goes at the end
    ("source", pa.string()),
])
# A vessel's track at one position a minute, what the server answers tracks from past its 48-hour window.
TRACKS_SCHEMA = pa.schema([
    ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("lat6", pa.int32()), ("lon6", pa.int32()), ("sog10", pa.int16()),
    ("cog10", pa.int16()), ("heading", pa.int16()), ("navstat", pa.int8()), ("source", pa.string()), ("day", pa.date32()),
])
RECEPTIONS_SCHEMA = pa.schema([
    ("id", pa.binary(16)), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("source", pa.string()), ("station", pa.string()),
    ("recv_ts", pa.timestamp("us")), ("license", pa.string()), ("day", pa.date32()),
])
VESSELS_SCHEMA = pa.schema([
    ("mmsi", pa.int32()), ("name", pa.string()), ("callsign", pa.string()), ("ship_type", pa.int16()),
    ("draught10", pa.int16()), ("cls", pa.string()), ("updated_ts", pa.timestamp("us")),
    # each field's own observation time, so a merge decides field by field in any day order
    ("name_ts", pa.timestamp("us")), ("callsign_ts", pa.timestamp("us")), ("ship_type_ts", pa.timestamp("us")),
    ("draught_ts", pa.timestamp("us")), ("cls_ts", pa.timestamp("us")),
    # history for the server's vessel record: the earliest report of any kind, and the latest position
    ("first_ts", pa.timestamp("us")), ("last_ts", pa.timestamp("us")), ("last_lat6", pa.int32()), ("last_lon6", pa.int32()),
    ("last_source", pa.string()),  # the source of the report behind last_*, for its credit line
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
    import random
    import time

    for i in range(attempts):
        try:
            return fn()
        except Exception as e:
            if i == attempts - 1:
                raise
            print(f"  retrying after {type(e).__name__}: {e}", file=sys.stderr)
            time.sleep(5 * 2**i + random.uniform(0, 5))  # jitter, so parallel days don't collide again in step


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
        SELECT unhex(id) AS id, mmsi, ts, msg_type, lat6, lon6,
               {cell_sql("lat", "lon")} AS cell,
               sog10, cog10, heading, navstat, NOT uncorroborated AS corroborated, CAST(recv AS DATE) AS day, source
        FROM (
            SELECT id, mmsi, ct AS ts, recv, uncorroborated, source,
                   CAST(r->>'lat' AS DOUBLE) AS lat, CAST(r->>'lon' AS DOUBLE) AS lon,
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
        SELECT p.id, p.mmsi, p.ts, c.source, c.station, c.recv AS recv_ts, c.license, CAST(c.recv AS DATE) AS day
        FROM (SELECT unhex(id) AS id, tx, source, station, recv, license FROM env WHERE k = 'copy' AND recv >= ? AND recv < ?) c
        LEFT JOIN folded f ON f.id = c.id AND f.ts = c.tx
        JOIN (SELECT id, mmsi, ts FROM positions UNION ALL SELECT id, mmsi, ts FROM prior_positions) p
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

    build_tracks(con)
    refresh_vessels(con, catalog)
    # Iceberg has no cross-table transaction, so order is the guarantee: positions commits last, with
    # the inputs' fingerprint in the same transaction, and that fingerprint is the completion marker.
    for name in ("weather", "receptions", "tracks"):
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
                                    schema=pa.schema([("id", pa.binary()), ("ts", pa.timestamp("us")), ("to_ts", pa.timestamp("us"))])))
    if drop:
        con.register("dropped", pa.table({"row": drop}))
        con.execute("DELETE FROM positions WHERE rowid IN (SELECT row FROM dropped)")


def prior_positions(catalog, day):
    """The previous day's transmissions (id, mmsi, ts), for copies that arrive after midnight."""
    prev = (datetime.fromisoformat(day) - timedelta(days=1)).date().isoformat()
    tbl = retry(lambda: catalog.load_table("ais.positions"))
    return retry(lambda: tbl.scan(row_filter=f"day = '{prev}'", selected_fields=("id", "mmsi", "ts")).to_arrow())


def cell_sql(lat, lon):
    """The one-degree cell of a position, row-major from (-90, -180), null without a position. The poles
    and the antimeridian fold into the last row and column, so every position has a cell in 0..64799.
    least() skips nulls in DuckDB, so the null case is explicit."""
    return (f"CASE WHEN {lat} IS NULL OR {lon} IS NULL THEN NULL "
            f"ELSE CAST(least(floor({lat}) + 90, 179) * 360 + least(floor({lon}) + 180, 359) AS INTEGER) END")


# A minute a vessel spends within this many meters of the minutes either side is a repeat of them.
TRACK_MOVED_M = 50
# The first minute of every this many is kept however still the vessel is, so a moored vessel's track
# never falls silent for more than twice this, which readers draw as unheard past 30 minutes.
TRACK_BEAT_MIN = 15
M_PER_LAT6 = 111195 / 600000  # meters per 1/600000 degree of latitude


def meters_sql(lat, lon, lat2, lon2):
    """Distance in meters on a flat projection about the first point: exact enough at the tens of meters
    the rollup judges, and two orders cheaper than a haversine over a day of positions."""
    return (f"sqrt(pow(({lat2} - {lat}) * {M_PER_LAT6}, 2) "
            f"+ pow(({lon2} - {lon}) * {M_PER_LAT6} * cos(radians({lat} / 600000)), 2))")


def build_tracks(con):
    """Roll the staged positions up into tracks: each vessel's first position in each minute, without the
    minutes it spent within TRACK_MOVED_M of both neighbors, apart from the first minute of each
    TRACK_BEAT_MIN. Minutes and beats count from the epoch, as the server's thinning buckets do, so
    thinning these to whole minutes picks what thinning every position would, wherever the vessel moved.
    Spikes stay in: the server judges them after thinning, as it does every position."""
    minute = "epoch_us(ts) // 60000000"
    con.execute(
        f"""
        CREATE OR REPLACE TABLE tracks AS
        WITH first AS (
            SELECT mmsi, ts, lat6, lon6, sog10, cog10, heading, navstat, source, day, {minute} AS minute
            FROM positions WHERE lat6 IS NOT NULL
            QUALIFY row_number() OVER (PARTITION BY mmsi, {minute} ORDER BY ts, lat6, lon6) = 1
        ), around AS (
            SELECT *, lag(lat6) OVER w AS plat, lag(lon6) OVER w AS plon, lead(lat6) OVER w AS nlat, lead(lon6) OVER w AS nlon,
                   row_number() OVER (PARTITION BY mmsi, minute // {TRACK_BEAT_MIN} ORDER BY minute) AS nth
            FROM first WINDOW w AS (PARTITION BY mmsi ORDER BY minute)
        )
        SELECT mmsi, ts, lat6, lon6, sog10, cog10, heading, navstat, source, day FROM around
        WHERE nth = 1 OR plat IS NULL OR nlat IS NULL
           OR {meters_sql("plat", "plon", "lat6", "lon6")} > {TRACK_MOVED_M}
           OR {meters_sql("lat6", "lon6", "nlat", "nlon")} > {TRACK_MOVED_M}
        """
    )


# The lake's layout, set per warehouse so a new one can take another without a code change. A table
# records the positions sort and bucket count it was created with and refuses the others (check_layout).
# Sorting positions by cell lets a bbox query skip row groups on the cell column's statistics; sorting
# by mmsi lets one vessel's track read a few row groups of its bucket's file instead of all of them.
POSITIONS_SORTS = {"cell": "cell NULLS LAST, mmsi, ts", "mmsi": "mmsi, ts"}
POSITIONS_SORT = os.environ.get("LAKE_POSITIONS_SORT") or "cell"  # unset or empty: the defaults
MMSI_BUCKETS = int(os.environ.get("LAKE_MMSI_BUCKETS") or 32)
SORT_KEY = "aiscast.positions-sort"

# Row order within each written file.
ORDER = {"positions": POSITIONS_SORTS[POSITIONS_SORT], "receptions": "source, station, recv_ts", "weather": "mmsi, ts", "tracks": "mmsi, ts"}


# A day is written in this many passes, each holding whole mmsi buckets. The Arrow copies of a full
# day of positions (the result, its cast, pyiceberg's split by partition) outgrow the packager's
# memory on top of DuckDB's own; a pass holds a quarter of them and still writes one file per bucket.
WRITE_PASSES = 4


def write_passes(con, tbl, name, passes=WRITE_PASSES):
    """WHERE clauses that split a staged table into `passes` groups of whole mmsi buckets, computed
    with the table's own bucket transform, or one pass for a table not bucketed by mmsi."""
    import pyarrow as pa
    from pyiceberg.transforms import BucketTransform

    field = next((f for f in tbl.spec().fields if isinstance(f.transform, BucketTransform)), None)
    source = field and tbl.schema().find_field(field.source_id)
    mmsis = field and con.execute(f"SELECT DISTINCT {source.name} AS mmsi FROM {name} WHERE {source.name} IS NOT NULL").to_arrow_table()
    if not mmsis:  # no bucketing, or no vessel to bucket
        return [""]
    col = mmsis.column("mmsi").cast(tbl.schema().as_arrow().field(source.name).type)
    buckets = field.transform.pyarrow_transform(source.field_type)(col)
    con.register("write_bucket", pa.table({"mmsi": mmsis.column("mmsi"), "b": buckets}))
    con.execute("CREATE OR REPLACE TEMP TABLE write_bucket_t AS SELECT * FROM write_bucket")
    con.unregister("write_bucket")
    wheres = [f"WHERE {source.name} IN (SELECT mmsi FROM write_bucket_t WHERE b % {passes} = {i})" for i in range(passes)]
    wheres[0] += f" OR {source.name} IS NULL"
    return wheres


def replace_day(con, catalog, day, name, properties=None):
    def go():
        tbl = catalog.load_table(f"ais.{name}")
        schema = tbl.schema().as_arrow()
        with tbl.transaction() as tx:
            tx.delete(f"day = '{day}'")  # rerunning a day replaces it
            for where in write_passes(con, tbl, name):
                data = con.execute(f"SELECT * FROM {name} {where} ORDER BY {ORDER[name]}").to_arrow_table().cast(schema)
                if data.num_rows:
                    tx.append(data)
                del data
            if properties:
                tx.set_properties(properties)

    retry(go)


def refresh_vessels(con, catalog):
    # Days packaged in parallel each rewrite this table. A commit made from a stale read is refused,
    # so a retry reads the table again and merges again; the merge takes each field at its own time,
    # so the order the days land in does not matter.
    def go():
        tbl = catalog.load_table("ais.vessels")
        con.register("existing_vessels", tbl.scan().to_arrow())
        merged = con.execute(
            f"""
            WITH evidence AS (  -- position message types are the truthful class signal; statics are not
              SELECT mmsi, CASE WHEN bool_or(mt IN ('StandardClassBPositionReport', 'ExtendedClassBPositionReport')) THEN 'B'
                                WHEN bool_or(mt = 'PositionReport') THEN 'A' END AS cls,
                     max(ct) AS ts
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
            ), fields AS (
              -- one row per observation, each field with its own time; fresh ranks this run's inputs
              -- over the stored row on a tie, so repackaging a day with corrected inputs replaces
              -- what that day contributed rather than keeping the old value
              SELECT mmsi, name, ts AS name_ts, callsign, ts AS callsign_ts, ship_type, ts AS ship_type_ts,
                     draught10, ts AS draught_ts, NULL AS cls, NULL::TIMESTAMP AS cls_ts, 1 AS fresh,
                     NULL::TIMESTAMP AS first_ts, NULL::TIMESTAMP AS last_ts, NULL::INTEGER AS last_lat6, NULL::INTEGER AS last_lon6,
                     NULL::VARCHAR AS last_source FROM statics
              UNION ALL  -- the static's own class claim is the weakest signal: it only fills a gap
              SELECT mmsi, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, cls, TIMESTAMP '1970-01-01', 1,
                     NULL, NULL, NULL, NULL, NULL FROM statics
              UNION ALL
              SELECT mmsi, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, cls, ts, 1, NULL, NULL, NULL, NULL, NULL FROM evidence
              UNION ALL  -- first_ts is when the network first heard the vessel: the receive time of any event,
              -- flagged or not, since a stale or implausible report still means the vessel was heard. Not the
              -- message's own time, which a device with a reset clock sets years in the past.
              SELECT mmsi, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 1,
                     min(recv), NULL, NULL, NULL, NULL FROM env WHERE k = 'event' GROUP BY mmsi
              UNION ALL  -- the day's accepted positions: the latest with coordinates is last_*
              -- with the source whose copy the server accepted, the one its credit line names
              -- (first_ts comes from the receive times above: a position's own time can be years off too)
              SELECT p.mmsi, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 1,
                     NULL, CASE WHEN p.lat6 IS NOT NULL THEN p.ts END, p.lat6, p.lon6, e.source
              FROM positions p
              LEFT JOIN (SELECT DISTINCT unhex(id) AS id, ct, source FROM env WHERE k = 'event') e ON e.id = p.id AND e.ct = p.ts
              UNION ALL
              SELECT mmsi, name, name_ts, callsign, callsign_ts, ship_type, ship_type_ts,
                     draught10, draught_ts, cls, cls_ts, 0, first_ts, last_ts, last_lat6, last_lon6, last_source FROM existing_vessels
            ), merged AS (
              -- latest-wins per field, not per row: a type 24 part B can carry a callsign and no name,
              -- and that must not discard a name learned earlier, whatever order the days arrive in
              SELECT mmsi,
                arg_max(name, (name_ts, fresh)) FILTER (WHERE name IS NOT NULL) AS name,
                max(name_ts) FILTER (WHERE name IS NOT NULL) AS name_ts,
                arg_max(callsign, (callsign_ts, fresh)) FILTER (WHERE callsign IS NOT NULL) AS callsign,
                max(callsign_ts) FILTER (WHERE callsign IS NOT NULL) AS callsign_ts,
                coalesce(arg_max(ship_type, (ship_type_ts, fresh)) FILTER (WHERE ship_type > 0), 0) AS ship_type,
                max(ship_type_ts) FILTER (WHERE ship_type > 0) AS ship_type_ts,
                coalesce(arg_max(draught10, (draught_ts, fresh)) FILTER (WHERE draught10 > 0), 0) AS draught10,
                max(draught_ts) FILTER (WHERE draught10 > 0) AS draught_ts,
                arg_max(cls, (cls_ts, fresh)) FILTER (WHERE cls IS NOT NULL) AS cls,
                max(cls_ts) FILTER (WHERE cls IS NOT NULL) AS cls_ts,
                min(first_ts) AS first_ts,  -- the earliest report any packaged day holds; newer days never move it forward
                max(last_ts) FILTER (WHERE last_lat6 IS NOT NULL) AS last_ts,
                -- latitude, longitude, and source from the same report
                arg_max(struct_pack(lat := last_lat6, lon := last_lon6, source := last_source), (last_ts, fresh))
                    FILTER (WHERE last_lat6 IS NOT NULL) AS last_pos
              FROM fields GROUP BY mmsi
            )
            SELECT mmsi, name, callsign, ship_type, draught10, cls,
                   greatest(name_ts, callsign_ts, ship_type_ts, draught_ts) AS updated_ts,
                   name_ts, callsign_ts, ship_type_ts, draught_ts, cls_ts,
                   first_ts, last_ts, last_pos.lat AS last_lat6, last_pos.lon AS last_lon6, last_pos.source AS last_source
            -- every vessel any packaged day heard, statics or not, so history can create the server's record
            FROM merged WHERE first_ts IS NOT NULL OR coalesce(name_ts, callsign_ts, ship_type_ts, draught_ts) IS NOT NULL
            ORDER BY mmsi
            """
        ).to_arrow_table()
        tbl.overwrite(merged.cast(tbl.schema().as_arrow()))

    retry(go, attempts=8)


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


# A month is compacted once the packager no longer repackages its days on its own, so the nightly run
# does not rewrite it again the next night.
COMPACT_AFTER = timedelta(days=8)


def month_start(months):
    """The first day of the month `months` after January 1970, as Iceberg's month transform counts."""
    return date(1970 + months // 12, months % 12 + 1, 1)


def compact_tracks(catalog, stage, today):
    """Rewrite each closed month of ais.tracks as one file per mmsi bucket. Days are written a file per
    bucket each, so a month a vessel's year reads would otherwise be thirty files per vessel. A month
    with any bucket in more than one file qualifies, so a day repackaged into a compacted month brings
    its month back."""
    tbl = retry(lambda: catalog.load_table("ais.tracks"))
    parts = retry(lambda: tbl.inspect.partitions()).to_pylist()
    months = sorted({p["partition"]["day_month"] for p in parts if p["file_count"] > 1})
    for m in months:
        first = month_start(m)
        nxt = month_start(m + 1)
        if date.fromisoformat(today) < nxt + COMPACT_AFTER:
            continue
        con = open_stage(stage)
        try:
            def go():
                tbl = catalog.load_table("ais.tracks")
                where = f"day >= '{first}' AND day < '{nxt}'"
                # Read inside the retry, so a commit refused for a newer snapshot rewrites what that holds.
                reader = tbl.scan(row_filter=where).to_arrow_batch_reader()
                con.register("reader", reader)
                con.execute("CREATE OR REPLACE TABLE tracks AS SELECT * FROM reader")
                con.unregister("reader")
                schema = tbl.schema().as_arrow()
                with tbl.transaction() as tx:
                    tx.delete(where)
                    # a pass per bucket keeps each pass's Arrow copies to a thirty-second of a month
                    for w in write_passes(con, tbl, "tracks", passes=MMSI_BUCKETS):
                        data = con.execute(f"SELECT * FROM tracks {w} ORDER BY {ORDER['tracks']}").to_arrow_table().cast(schema)
                        if data.num_rows:
                            tx.append(data)
                        del data

            retry(go)
            print(f"tracks: compacted {first:%Y-%m}", file=sys.stderr)
        finally:
            con.close()
            shutil.rmtree(stage, ignore_errors=True)


def tracks_from_lake(day, con, catalog):
    """Roll a packaged day's positions up into tracks, for days packaged before ais.tracks existed."""
    cols = ("mmsi", "ts", "lat6", "lon6", "sog10", "cog10", "heading", "navstat", "source", "day")
    tbl = retry(lambda: catalog.load_table("ais.positions"))
    reader = retry(lambda: tbl.scan(row_filter=f"day = '{day}'", selected_fields=cols).to_arrow_batch_reader())
    con.register("reader", reader)
    con.execute("CREATE OR REPLACE TABLE positions AS SELECT * FROM reader")
    con.unregister("reader")
    n = con.execute("SELECT count(*) FROM positions").fetchone()[0]
    if not n:
        sys.exit(f"{day}: no positions in the lake to roll up")
    build_tracks(con)
    replace_day(con, catalog, day, "tracks")
    print(f"{day}: {con.execute('SELECT count(*) FROM tracks').fetchone()[0]} track points from {n} positions", file=sys.stderr)


# A reader skips data by row-group statistics, and at the default of about a million rows a day's
# file is one or two row groups spanning nearly every value of the sort key. Smaller groups each cover
# a narrow range for a query to skip on.
ROW_GROUP_KEY, ROW_GROUP_ROWS = "write.parquet.row-group-limit", os.environ.get("LAKE_ROW_GROUP_ROWS") or "32768"


# A compacted month is one file per bucket only while the file stays under the writer's target size, about
# 225 MB at 2026's traffic against a default of 512 MB; past it the month would split, count as uncompacted,
# and be rewritten every night.
TRACKS_PROPERTIES = {ROW_GROUP_KEY: ROW_GROUP_ROWS, "write.target-file-size-bytes": str(2 << 30)}


def _specs():
    from pyiceberg.transforms import BucketTransform, IdentityTransform, MonthTransform

    # Identity on day, so replacing a day rewrites that partition, not the table. Positions and
    # receptions also bucket by mmsi, so one vessel's history reads one file per day. Tracks partition
    # by month, so a compacted month is one file per bucket and a vessel's year reads twelve.
    by_day = [("day", IdentityTransform(), "day")]
    bucket = [("mmsi", BucketTransform(MMSI_BUCKETS), "mmsi_bucket")]
    by_month = [("day", MonthTransform(), "day_month")]
    return {"positions": by_day + bucket, "receptions": by_day + bucket, "weather": by_day, "tracks": by_month + bucket}


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
    for name, schema in [("positions", POSITIONS_SCHEMA), ("receptions", RECEPTIONS_SCHEMA), ("vessels", VESSELS_SCHEMA),
                         ("weather", WEATHER_SCHEMA), ("tracks", TRACKS_SCHEMA)]:
        if ("ais", name) not in retry(lambda: list(catalog.list_tables("ais"))):
            retry(lambda: catalog.create_table(f"ais.{name}", schema=schema))
        tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
        if name == "positions":
            # a table from before the sort was recorded is cell-sorted; a new one takes the setting
            sort = tbl.properties.get(SORT_KEY) or ("cell" if tbl.current_snapshot() else POSITIONS_SORT)
            if sort != POSITIONS_SORT:
                sys.exit(f"ais.positions is sorted by {sort}, not {POSITIONS_SORT}: a table keeps one order, "
                         f"so set LAKE_POSITIONS_SORT={sort} or package into a new warehouse")
            want = {SORT_KEY: sort, ROW_GROUP_KEY: ROW_GROUP_ROWS}
            if {k: tbl.properties.get(k) for k in want} != want:
                with tbl.transaction() as tx:
                    tx.set_properties(want)
                tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
        if name == "tracks" and {k: tbl.properties.get(k) for k in TRACKS_PROPERTIES} != TRACKS_PROPERTIES:
            with tbl.transaction() as tx:
                tx.set_properties(TRACKS_PROPERTIES)
            tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
        have = {f.name for f in tbl.schema().fields}
        if missing := [f for f in schema if f.name not in have]:
            # schema changes are additive: a new column joins the table at the end, null for the days before it
            with tbl.update_schema() as u:
                u.union_by_name(pa.schema(missing))
            tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
        spec = _specs().get(name, [])
        if spec and not tbl.spec().fields:
            with tbl.update_spec() as u:
                for field, transform, as_name in spec:
                    u.add_field(field, transform, as_name)
            tbl = retry(lambda: catalog.load_table(f"ais.{name}"))
        check_layout(tbl, name, schema, spec)
    return catalog


def check_layout(tbl, name, schema, spec):
    """Refuse a table laid out differently from what this packager writes. A table created by an
    earlier version cannot take these rows (a string id cannot become fixed bytes) or keeps the old
    partitioning for every day written into it, so it is dropped and repackaged, never written into."""
    def shape(s):  # as Iceberg stores it: no 8- or 16-bit integers, no large_ variants
        return [(f.name, {"int8": "int32", "int16": "int32"}.get(str(f.type), str(f.type).replace("large_", ""))) for f in s]

    have_spec = [(f.name, str(f.transform)) for f in tbl.spec().fields]
    want_spec = [(n, str(t)) for _, t, n in spec]
    if shape(tbl.schema().as_arrow()) != shape(schema) or have_spec != want_spec:
        sys.exit(f"ais.{name} has an older layout (columns {shape(tbl.schema().as_arrow())}, partitions {have_spec}); "
                 f"drop it and repackage: this packager writes columns {shape(schema)}, partitions {want_spec}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--normalized", help="local normalized tree (normalized/v1/YYYY/MM/DD/HH.gz); omit to fetch the day from the bucket")
    ap.add_argument("--date", help="UTC day YYYY-MM-DD; default: every closed day of the past week missing from the catalog")
    ap.add_argument("--min-hours", type=int, default=20, help="refuse a day with fewer distinct hours")
    ap.add_argument("--init", action="store_true", help="create or upgrade the tables and stop; run once before packaging days in parallel")
    ap.add_argument("--tracks-only", action="store_true", help="with --date, roll that packaged day's positions up into ais.tracks and stop")
    args = ap.parse_args()
    if args.tracks_only and not args.date:
        ap.error("--tracks-only needs --date")

    all_files = glob.glob(f"{args.normalized}/**/*.gz", recursive=True) if args.normalized else []
    now = datetime.now(timezone.utc)
    today = now.strftime("%Y-%m-%d")
    catalog = get_catalog()
    if args.init:
        return
    stage = HERE / "stage"
    if args.tracks_only:
        con = open_stage(stage)
        try:
            tracks_from_lake(args.date, con, catalog)
        finally:
            con.close()
            shutil.rmtree(stage, ignore_errors=True)
        return
    days = [args.date] if args.date else [(now - timedelta(days=n)).strftime("%Y-%m-%d") for n in range(7, 0, -1)]
    packaged = retry(lambda: catalog.load_table("ais.positions")).properties

    # A day's staging database is several times the day's compressed input (about 40 GB for a full
    # day of production traffic), so each day gets its own, deleted before the next day starts and
    # however the run ends: a week of days in one file does not fit on a runner's disk.
    shutil.rmtree(stage, ignore_errors=True)  # a run the OS killed leaves it behind
    try:
        failed = package_days(days, args, all_files, today, packaged, stage, catalog)
        # Only the nightly run compacts: days packaged by hand run in parallel, and a compaction racing
        # them would have its commit refused and read the month again.
        if not args.date:
            compact_tracks(catalog, stage, today)
    finally:
        shutil.rmtree(stage, ignore_errors=True)
    if failed:
        sys.exit(f"failed: {', '.join(failed)}")


def open_stage(stage):
    """A fresh staging database for one day."""
    shutil.rmtree(stage, ignore_errors=True)
    stage.mkdir(parents=True)
    con = duckdb.connect(str(stage / "packager.duckdb"))
    # Two threads keep a full day under the memory limit and leave the box's cores to the live server:
    # at the default of one thread per core, a 24-hour day runs out of memory. A machine that runs
    # nothing else (a CI runner) raises both. Every output is written with an explicit ORDER BY, so
    # insertion order need not be kept.
    threads, memory = os.environ.get("PACKAGER_THREADS", "2"), os.environ.get("PACKAGER_MEMORY", "4GB")
    con.execute(f"SET memory_limit='{memory}'; SET threads={int(threads)}; SET preserve_insertion_order=false; SET temp_directory='{stage}/tmp'")
    return con


def package_days(days, args, all_files, today, packaged, stage, catalog):
    failed = []
    for day in days:
        fetched = con = None
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
            con = open_stage(stage)
            process_day(day, files, con, catalog, fp)
        except (Exception, SystemExit) as e:  # one bad day must not hold back the rest of the week
            print(f"{day}: {e}", file=sys.stderr)
            failed.append(day)
        finally:
            if con:
                con.close()
                shutil.rmtree(stage, ignore_errors=True)
            if fetched:
                shutil.rmtree(fetched, ignore_errors=True)  # re-fetchable; the bucket is the source of truth
    return failed


if __name__ == "__main__":
    main()
