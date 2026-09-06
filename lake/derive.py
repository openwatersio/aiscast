#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["pyais", "pyarrow", "duckdb", "pyiceberg[sql-sqlite]"]
# ///
"""Derive job: raw per-source archive -> deduplicated Iceberg tables.

See README.md for the schema, source handling, and consumer contract.
One invocation processes whole UTC days: decode every source's hour files
into a staging Parquet, dedupe with DuckDB, append to ais.messages and
ais.receptions, and refresh ais.vessels latest-wins.
"""

import argparse
import glob
import gzip
import json
import os
import re
import sys
import zlib
from datetime import datetime, timedelta, timezone
from pathlib import Path

import duckdb
import pyarrow as pa
import pyarrow.parquet as pq
from pyais import decode

HERE = Path(os.environ.get("LAKE_HOME", Path(__file__).parent))  # stage/ and warehouse/ live here

# wire-unit n/a sentinels
NA_SOG, NA_COG, NA_HDG, NA_NAV = 1023, 3600, 511, -1
POS_TYPES = {1, 2, 3, 18, 19, 27}
STATIC_TYPES = {5, 24}
AISHUB_FRESH_S = 120  # snapshot records older than this are stale state, not new receptions

TAG_STATION = re.compile(rb"s:([0-9]+)")
TAG_TIME = re.compile(rb"c:(\d+)")
AISSTREAM_META = re.compile(rb'"time_utc":"([0-9: .-]+)')
LICENSES = {"kystverket": "NLOD-2.0", "digitraffic": "CC-BY-4.0", "aisstream": "aisstream-io-terms", "aishub": "aishub-terms"}

POS_SCHEMA = pa.schema(
    [
        ("mmsi", pa.int32()),
        ("msg_type", pa.int8()),
        ("lat6", pa.int32()),
        ("lon6", pa.int32()),
        ("sog10", pa.int16()),
        ("cog10", pa.int16()),
        ("heading", pa.int16()),
        ("navstat", pa.int8()),
        ("canon_ts", pa.timestamp("us")),
        ("recv_ts", pa.timestamp("us")),
        ("source", pa.string()),
        ("station", pa.string()),
    ]
)
STATIC_SCHEMA = pa.schema(
    [
        ("mmsi", pa.int32()),
        ("name", pa.string()),
        ("callsign", pa.string()),
        ("ship_type", pa.int16()),
        ("draught10", pa.int16()),
        ("cls", pa.string()),
        ("ts", pa.timestamp("us")),
    ]
)


def valid_mmsi(mmsi):
    mid = mmsi // 1_000_000
    return 200 <= mid <= 775 and not 578 <= mid <= 599


def valid_pos(lat6, lon6):
    return -54_000_000 < lat6 < 54_000_000 and -108_000_000 < lon6 < 108_000_000 and not (abs(lat6) < 6000 and abs(lon6) < 6000)


def parse_recv(tscol):
    # RFC3339Nano from the server; [:26] trims to µs, fromisoformat (3.11+) takes the rest
    return datetime.fromisoformat(tscol[:26].decode()).replace(tzinfo=None)


def canonical(src_epoch_us, recv):
    if src_epoch_us:
        src = datetime.fromtimestamp(src_epoch_us / 1e6, tz=timezone.utc).replace(tzinfo=None)
        if abs((src - recv).total_seconds()) <= 30:
            return src
    return recv


def safe_lines(path):
    try:
        with gzip.open(path, "rb") as f:
            while True:
                try:
                    line = f.readline()
                except (EOFError, OSError, zlib.error):
                    print(f"  gzip error, skipping rest of {path}", file=sys.stderr)
                    return
                if not line:
                    return
                yield line
    except OSError:
        return


class Batcher:
    """Accumulates rows and flushes to a ParquetWriter in chunks."""

    def __init__(self, path, schema, chunk=500_000):
        self.writer = pq.ParquetWriter(path, schema)
        self.schema, self.chunk, self.rows, self.n = schema, chunk, [], 0

    def add(self, row):
        self.rows.append(row)
        self.n += 1
        if len(self.rows) >= self.chunk:
            self.flush()

    def flush(self):
        if self.rows:
            cols = list(zip(*self.rows))
            self.writer.write_table(pa.table({f.name: pa.array(cols[i], f.type) for i, f in enumerate(self.schema)}, schema=self.schema))
            self.rows = []

    def close(self):
        self.flush()
        self.writer.close()


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


def sog_wire(kn):
    return NA_SOG if kn is None or kn < 0 or kn >= 102.3 else min(round(kn * 10), NA_SOG)


def cog_wire(deg):
    return NA_COG if deg is None or deg < 0 or deg >= 360 else round(deg * 10)


def decode_day(files, pos_out, stat_out):
    """Decode one day's hour files from every source into the staging Batchers."""
    parts = {}  # multipart NMEA buffers
    n_err = 0
    for path in files:
        json_buf = None
        for line in safe_lines(path):
            if json_buf is not None:  # digitraffic continuation lines have no tab prefix
                json_buf[1].append(line)
                if line.startswith(b"}"):
                    topic_mmsi, buf = json_buf
                    json_buf = None
                    try:
                        digitraffic_record(topic_mmsi, b"".join(buf), pos_out, stat_out)
                    except Exception:
                        n_err += 1
                continue
            try:
                tscol, src, payload = line.rstrip(b"\r\n").split(b"\t", 2)
            except ValueError:
                continue
            source = src.decode()
            try:
                if source == "aisstream":
                    aisstream_record(payload, parse_recv(tscol), pos_out, stat_out)
                elif source == "aishub":
                    aishub_snapshot(payload, parse_recv(tscol), pos_out, stat_out)
                elif source == "digitraffic":
                    head, brace, _ = payload.partition(b" {")
                    if brace:
                        seg = head.split(b"/")  # vessels-v2/<mmsi>/<topic>
                        json_buf = ((int(seg[1]), seg[2].decode(), parse_recv(tscol)), [b"{"])
                else:
                    nmea_line(source, payload, parse_recv(tscol), parts, pos_out, stat_out)
            except Exception:
                n_err += 1
        if len(parts) > 10_000:
            parts.clear()
    return n_err


def emit_pos(out, mmsi, msg_type, lat6, lon6, sog10, cog10, heading, navstat, canon, recv, source, station):
    if valid_mmsi(mmsi) and valid_pos(lat6, lon6):
        out.add((mmsi, msg_type, lat6, lon6, sog10, cog10, heading, navstat, canon, recv, source, station))


def nmea_line(source, payload, recv, parts, pos_out, stat_out):
    station, src_us = source, None
    if payload.startswith(b"\\"):
        end = payload.find(b"\\", 1)
        if end < 0:
            return
        tag = payload[:end]
        m = TAG_TIME.search(tag)
        if m:
            v = int(m.group(1))
            src_us = v * 1000 if len(m.group(1)) > 12 else v * 1_000_000  # ms vs s heuristic
        if source == "kystverket":
            m = TAG_STATION.search(tag)
            if m:
                station = m.group(1).decode()
        payload = payload[end + 1 :]
    fields = payload.split(b",")
    if len(fields) < 6 or b"VDM" not in fields[0]:
        return
    total, num, seq, chan, body = fields[1], fields[2], fields[3], fields[4], fields[5]
    if total == b"1":
        first = body[:1]
        if first not in b"123BCK5H":
            return
        msg = decode(payload)
    else:
        key = (station, seq, chan)
        buf = parts.setdefault(key, {})
        buf[num] = payload
        if len(buf) != int(total):
            return
        msg = decode(*(buf[k] for k in sorted(buf)))
        del parts[key]
    canon = canonical(src_us, recv)
    t = msg.msg_type
    if t in POS_TYPES:
        hdg = getattr(msg, "heading", None)  # 0 (due north) is valid; only absence means n/a
        emit_pos(
            pos_out, msg.mmsi, t,
            round(float(msg.lat) * 600000), round(float(msg.lon) * 600000),
            sog_wire(getattr(msg, "speed", None)), cog_wire(getattr(msg, "course", None)),
            NA_HDG if hdg is None else hdg,
            int(getattr(msg, "status", NA_NAV)) if getattr(msg, "status", None) is not None else NA_NAV,
            canon, recv, source, station,
        )
    elif t in STATIC_TYPES and valid_mmsi(msg.mmsi):
        d = getattr(msg, "draught", None)
        stat_out.add((
            msg.mmsi, getattr(msg, "shipname", None) or None, getattr(msg, "callsign", None) or None,
            int(getattr(msg, "ship_type", 0) or 0), round(d * 10) if d else 0,
            "A" if t == 5 else "B", canon,
        ))


def digitraffic_record(topic, raw, pos_out, stat_out):
    mmsi, kind, recv = topic
    d = json.loads(raw)
    if kind == "location":
        canon = canonical(d["time"] * 1_000_000, recv)
        emit_pos(
            pos_out, mmsi, 0,
            round(d["lat"] * 600000), round(d["lon"] * 600000),
            sog_wire(d.get("sog")), cog_wire(d.get("cog")),
            d.get("heading", NA_HDG), d.get("navStat", NA_NAV),
            canon, recv, "digitraffic", "digitraffic",
        )
    elif kind == "metadata" and valid_mmsi(mmsi):
        stat_out.add((mmsi, d.get("name"), d.get("callSign"), int(d.get("type") or d.get("shipType") or 0), int(d.get("draught") or 0), "A", recv))


def aisstream_record(payload, recv, pos_out, stat_out):
    d = json.loads(payload)
    mtype = d.get("MessageType")
    body = d["Message"].get(mtype) or {}
    m = AISSTREAM_META.search(payload)
    canon = recv
    if m:
        try:
            canon_dt = datetime.fromisoformat(m.group(1)[:26].decode().replace(" ", "T", 1).rstrip(" "))
            canon = canonical(int(canon_dt.replace(tzinfo=timezone.utc).timestamp() * 1e6), recv)
        except ValueError:
            pass
    if mtype in ("PositionReport", "StandardClassBPositionReport", "ExtendedClassBPositionReport"):
        wire_type = {"PositionReport": body.get("MessageID", 1), "StandardClassBPositionReport": 18, "ExtendedClassBPositionReport": 19}[mtype]
        emit_pos(
            pos_out, body["UserID"], wire_type,
            round(body["Latitude"] * 600000), round(body["Longitude"] * 600000),
            sog_wire(body.get("Sog")), cog_wire(body.get("Cog")),
            body.get("TrueHeading", NA_HDG), body.get("NavigationalStatus", NA_NAV),
            canon, recv, "aisstream", "aisstream",
        )
    elif mtype == "ShipStaticData" and valid_mmsi(body.get("UserID", 0)):
        d10 = round((body.get("MaximumStaticDraught") or 0) * 10)
        stat_out.add((body["UserID"], (body.get("Name") or "").strip() or None, (body.get("CallSign") or "").strip() or None, int(body.get("Type") or 0), d10, "A", canon))
    elif mtype == "StaticDataReport" and valid_mmsi(d["MetaData"]["MMSI"]):
        rep = body.get("ReportB") or {}
        stat_out.add((d["MetaData"]["MMSI"], (d["MetaData"].get("ShipName") or "").strip() or None, (rep.get("CallSign") or "").strip() or None, int(rep.get("ShipType") or 0), 0, "B", canon))


def aishub_snapshot(payload, recv, pos_out, stat_out):
    _header, records = json.loads(payload)
    recv_epoch = recv.replace(tzinfo=timezone.utc).timestamp()  # recv is naive UTC
    for r in records:
        t = int(r.get("TIME", 0))
        if recv_epoch - t > AISHUB_FRESH_S or t > recv_epoch + 60:
            continue
        canon = datetime.fromtimestamp(t, tz=timezone.utc).replace(tzinfo=None)
        emit_pos(
            pos_out, r["MMSI"], 0,
            r["LATITUDE"], r["LONGITUDE"],  # already 1/600000 units
            r.get("SOG", NA_SOG), min(r.get("COG", NA_COG), NA_COG),
            r.get("HEADING", NA_HDG), r.get("NAVSTAT", NA_NAV),
            canon, recv, "aishub", "aishub",
        )
        if r.get("NAME") and valid_mmsi(r["MMSI"]):
            stat_out.add((r["MMSI"], r["NAME"], r.get("CALLSIGN") or None, int(r.get("TYPE") or 0), int(r.get("DRAUGHT") or 0), "A", canon))


GROUP_SQL = """
CREATE OR REPLACE TABLE grouped AS
WITH keyed AS (
  SELECT *, md5(mmsi || ':' || lat6 || ':' || lon6 || ':' || sog10 || ':' || cog10 || ':' || heading) AS ck
  FROM read_parquet(?)
  WHERE canon_ts >= ? AND canon_ts < ?
), lagged AS (
  SELECT *, CASE WHEN canon_ts - LAG(canon_ts) OVER w > INTERVAL 10 SECONDS OR LAG(canon_ts) OVER w IS NULL THEN 1 ELSE 0 END AS new_run
  FROM keyed
  WINDOW w AS (PARTITION BY mmsi, ck ORDER BY canon_ts)
), runs AS (
  SELECT *, SUM(new_run) OVER (PARTITION BY mmsi, ck ORDER BY canon_ts ROWS UNBOUNDED PRECEDING) AS grp
  FROM lagged
)
SELECT *, md5(ck || ':' || grp || ':' || mmsi) AS id,
       min(canon_ts) OVER (PARTITION BY mmsi, ck, grp) AS grp_ts
FROM runs
"""


def process_day(day, files, con, catalog, keep_stage, reuse_stage):
    stage = HERE / "stage"
    stage.mkdir(exist_ok=True)
    pos_path, stat_path = str(stage / f"pos-{day}.parquet"), str(stage / f"stat-{day}.parquet")
    n_err = 0
    if reuse_stage and os.path.exists(pos_path):
        n_pos = n_stat = -1
    else:
        pos_out, stat_out = Batcher(pos_path, POS_SCHEMA), Batcher(stat_path, STATIC_SCHEMA)
        n_err = decode_day(files, pos_out, stat_out)
        pos_out.close()
        stat_out.close()
        n_pos, n_stat = pos_out.n, stat_out.n

    day_start = datetime.fromisoformat(day)
    day_end = day_start + timedelta(days=1)  # exclusive
    con.execute(GROUP_SQL, [pos_path, day_start, day_end])
    # COPY streams to disk instead of materializing arrow tables
    msg_path, rx_path = str(stage / f"messages-{day}.parquet"), str(stage / f"receptions-{day}.parquet")
    con.execute(
        f"""
        COPY (
          SELECT id, mmsi, min(canon_ts) AS ts, max(msg_type) AS msg_type,
                 any_value(lat6) AS lat6, any_value(lon6) AS lon6, any_value(sog10) AS sog10,
                 any_value(cog10) AS cog10, any_value(heading) AS heading, max(navstat) AS navstat,
                 CAST(min(canon_ts) AS DATE) AS day
          FROM grouped GROUP BY id, mmsi ORDER BY mmsi, ts
        ) TO '{msg_path}' (FORMAT parquet)
        """
    )
    lic = "CASE " + " ".join(f"WHEN source LIKE '{k}%' THEN '{v}'" for k, v in LICENSES.items()) + " ELSE 'feeder' END"
    con.execute(
        f"""
        COPY (
          SELECT id AS msg_id, source, station, min(recv_ts) AS recv_ts, CAST(min(grp_ts) AS DATE) AS day, {lic} AS license
          FROM grouped GROUP BY id, source, station
          ORDER BY source, station, min(recv_ts)
        ) TO '{rx_path}' (FORMAT parquet)
        """
    )
    n_msg = con.execute("SELECT count(DISTINCT id) FROM grouped").fetchone()[0]
    con.execute("DROP TABLE grouped")

    for name, path in [("ais.messages", msg_path), ("ais.receptions", rx_path)]:

        def replace_day(name=name, path=path):
            tbl = catalog.load_table(name)
            # delete + chunked appends land as one commit: a crashed or retried run never
            # exposes a partial day. Chunking keeps memory flat; retry redoes the whole table.
            with tbl.transaction() as tx:
                tx.delete(f"day = '{day}'")  # rerunning a day replaces it
                pf = pq.ParquetFile(path)
                for batch in pf.iter_batches(batch_size=2_000_000):
                    tx.append(pa.Table.from_batches([batch], schema=pf.schema_arrow))

        retry(replace_day)
    refresh_vessels(con, catalog, stat_path, pos_path)

    print(f"{day}: {n_pos} position receptions, {n_msg} messages, {n_err} decode errors", file=sys.stderr)
    if not keep_stage:
        for p in (pos_path, stat_path, msg_path, rx_path):
            os.remove(p)


def refresh_vessels(con, catalog, stat_path, pos_path):
    tbl = retry(lambda: catalog.load_table("ais.vessels"))
    existing = retry(lambda: tbl.scan().to_arrow())
    con.register("existing_vessels", existing)
    merged = con.execute(
        """
        WITH evidence AS (  -- position message types are the truthful class signal; JSON statics are not
          SELECT mmsi, CASE WHEN bool_or(msg_type IN (18, 19)) THEN 'B' WHEN bool_or(msg_type IN (1, 2, 3)) THEN 'A' END AS ev_cls
          FROM read_parquet(?) GROUP BY mmsi
        )
        SELECT u.mmsi, name, callsign, ship_type, draught10, coalesce(ev_cls, cls) AS cls, ts AS updated_ts FROM (
          SELECT *, ROW_NUMBER() OVER (PARTITION BY mmsi ORDER BY has_name DESC, ts DESC) AS rn FROM (
            SELECT mmsi, name, callsign, ship_type, draught10, cls, ts, (name IS NOT NULL) AS has_name FROM read_parquet(?)
            UNION ALL
            SELECT mmsi, name, callsign, ship_type, draught10, cls, updated_ts, (name IS NOT NULL) FROM existing_vessels
          )
        ) u LEFT JOIN evidence ON u.mmsi = evidence.mmsi
        WHERE rn = 1 ORDER BY u.mmsi
        """,
        [pos_path, stat_path],
    ).to_arrow_table()
    retry(lambda: tbl.overwrite(merged.cast(tbl.schema().as_arrow())))


def get_catalog():
    from pyiceberg.catalog import load_catalog

    uri = os.environ.get("LAKE_CATALOG_URI")
    if uri:
        catalog = load_catalog("r2", uri=uri, warehouse=os.environ["LAKE_WAREHOUSE"], token=os.environ["LAKE_CATALOG_TOKEN"])
    else:
        wh = HERE / "warehouse"
        wh.mkdir(exist_ok=True)
        catalog = load_catalog("local", uri=f"sqlite:///{wh}/catalog.db", warehouse=f"file://{wh}")
    retry(lambda: catalog.create_namespace_if_not_exists("ais"))
    for name, schema in [
        ("messages", pa.schema([("id", pa.string()), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("msg_type", pa.int8()), ("lat6", pa.int32()), ("lon6", pa.int32()), ("sog10", pa.int16()), ("cog10", pa.int16()), ("heading", pa.int16()), ("navstat", pa.int8()), ("day", pa.date32())])),
        ("receptions", pa.schema([("msg_id", pa.string()), ("source", pa.string()), ("station", pa.string()), ("recv_ts", pa.timestamp("us")), ("day", pa.date32()), ("license", pa.string())])),
        ("vessels", STATIC_SCHEMA.remove(6).append(pa.field("updated_ts", pa.timestamp("us")))),
    ]:
        if ("ais", name) not in retry(lambda: list(catalog.list_tables("ais"))):
            retry(lambda: catalog.create_table(f"ais.{name}", schema=schema))
    return catalog


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--archive", required=True)
    ap.add_argument("--date", help="UTC day YYYY-MM-DD (default: yesterday)")
    ap.add_argument("--all", action="store_true", help="every day present in the archive")
    ap.add_argument("--keep-stage", action="store_true")
    ap.add_argument("--reuse-stage", action="store_true", help="skip decode when the day's staging parquet exists")
    args = ap.parse_args()

    by_day = {}
    for f in glob.glob(f"{args.archive}/**/*.gz", recursive=True):
        m = re.search(r"(\d{4})/(\d{2})/(\d{2})/\d{2}\.gz$", f)
        if m:
            by_day.setdefault("-".join(m.groups()), []).append(f)
    yesterday = (datetime.now(timezone.utc) - timedelta(days=1)).strftime("%Y-%m-%d")
    days = sorted(by_day) if args.all else [args.date or yesterday]

    catalog = get_catalog()
    stage = HERE / "stage"
    stage.mkdir(exist_ok=True)
    con = duckdb.connect(str(stage / "derive.duckdb"))
    con.execute(f"SET memory_limit='4GB'; SET temp_directory='{stage}/tmp'")
    for day in days:
        if day not in by_day:
            sys.exit(f"no archive files for {day}")
        process_day(day, sorted(by_day[day]), con, catalog, args.keep_stage, args.reuse_stage)


if __name__ == "__main__":
    main()
