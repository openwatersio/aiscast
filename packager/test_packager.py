"""Packager tests: the committed fixture is pipeline-genuine (produced by `aiscast replay` over
real archived records); edge cases are layered on top as handcrafted envelopes."""

import copy
import glob
import gzip
import json
import math
import os
import sys
from pathlib import Path

import duckdb
import pyarrow as pa
import pyarrow.parquet as pq
import pytest

sys.path.insert(0, str(Path(__file__).parent))
import packager

DAY = "2026-09-01"
FIXTURE = Path(__file__).parent / "testdata"


def fixture_envelopes():
    out = []
    for f in sorted(FIXTURE.rglob("*.gz")):
        with gzip.open(f, "rt") as fh:
            out += [json.loads(ln) for ln in fh if ln.strip()]
    return out


def template(envs, kind, msg_type=None):
    for e in envs:
        if e["k"] != kind:
            continue
        if msg_type and e["r"].get("msg_type") != msg_type:
            continue
        return copy.deepcopy(e)
    raise AssertionError(f"no {kind}/{msg_type} in fixture")


def make_tree(tmp_path, extra_day=(), extra_boundary=()):
    """Fixture file verbatim, plus crafted envelopes in the day's hour 13 and in D+1 hour 00."""
    root = tmp_path / "normalized"
    day_dir = root / "normalized/v1/2026/09/01"
    day_dir.mkdir(parents=True)
    (day_dir / "12.gz").write_bytes((FIXTURE / "normalized/v1/2026/09/01/12.gz").read_bytes())
    if extra_day:
        with gzip.open(day_dir / "13.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in extra_day)
    if extra_boundary:
        nxt = root / "normalized/v1/2026/09/02"
        nxt.mkdir(parents=True)
        with gzip.open(nxt / "00.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in extra_boundary)
    return root


def event_at(tpl, id, ts, recv=None, nofix=False, **flags):
    e = copy.deepcopy(tpl)
    e["r"]["id"], e["r"]["time"], e["t"] = hexid(id), ts, recv or ts
    if nofix:  # the server leaves lat/lon off when the report has no usable position
        del e["r"]["lat"], e["r"]["lon"]
    e.update(flags)
    return e


def copy_at(tpl, id, ts, source="aishub", license="aishub-terms", recv=None, tx=None):
    c = copy.deepcopy(tpl)
    c["r"].update(id=hexid(id), time=ts, tx=tx or ts, source=source, station=source, license=license)
    c["t"] = recv or ts
    return c


def crafted(envs):
    ev = template(envs, "event", "PositionReport")
    cp = template(envs, "copy")
    methyd = template(envs, "methyd")
    extra_day = [
        # a transmission and a late aishub copy of it: one position, two receptions
        event_at(ev, "aaaa0001", "2026-09-01T13:00:00Z"),
        copy_at(cp, "aaaa0001", "2026-09-01T13:00:00Z", source="kystverket", license="NLOD-2.0"),
        copy_at(cp, "aaaa0001", "2026-09-01T13:00:03Z", tx="2026-09-01T13:00:00Z"),
        # a crash-window re-accept: two events, same id, 3 s apart, collapses to one row
        event_at(ev, "bbbb0002", "2026-09-01T13:10:00Z"),
        copy_at(cp, "bbbb0002", "2026-09-01T13:10:00Z"),
        event_at(ev, "bbbb0002", "2026-09-01T13:10:03Z"),
        copy_at(cp, "bbbb0002", "2026-09-01T13:10:03Z"),
        # re-accepts at 0 s, 9 s, 18 s: the server compares with the last kept time, so 18 s is a
        # second transmission even though it is 9 s after the one before it
        event_at(ev, "abab0008", "2026-09-01T13:15:00Z"),
        copy_at(cp, "abab0008", "2026-09-01T13:15:00Z"),
        event_at(ev, "abab0008", "2026-09-01T13:15:09Z"),
        copy_at(cp, "abab0008", "2026-09-01T13:15:09Z"),
        event_at(ev, "abab0008", "2026-09-01T13:15:18Z"),
        copy_at(cp, "abab0008", "2026-09-01T13:15:18Z"),
        # an identical re-accept at the same instant still folds to one row
        event_at(ev, "abab0009", "2026-09-01T13:16:00Z"),
        copy_at(cp, "abab0009", "2026-09-01T13:16:00Z"),
        event_at(ev, "abab0009", "2026-09-01T13:16:00Z"),
        copy_at(cp, "abab0009", "2026-09-01T13:16:00Z"),
        # the same bits on the air three minutes apart: legitimately two transmissions
        event_at(ev, "cccc0003", "2026-09-01T13:20:00Z"),
        copy_at(cp, "cccc0003", "2026-09-01T13:20:00Z"),
        event_at(ev, "cccc0003", "2026-09-01T13:23:00Z"),
        copy_at(cp, "cccc0003", "2026-09-01T13:23:00Z"),
        # implausible: archived in the stream, withheld from the table like the live stream withheld it
        event_at(ev, "dddd0004", "2026-09-01T13:30:00Z", implausible=True),
        copy_at(cp, "dddd0004", "2026-09-01T13:30:00Z"),
        # the server left lat/lon off (a (0,0) or not-available report): the row stays, without coordinates
        event_at(ev, "adad0011", "2026-09-01T13:40:00Z", nofix=True),
        copy_at(cp, "adad0011", "2026-09-01T13:40:00Z"),
        # only an unauthenticated sender heard it
        event_at(ev, "aeae0012", "2026-09-01T13:41:00Z", uncorroborated=True),
        copy_at(cp, "aeae0012", "2026-09-01T13:41:00Z"),
        # a satellite pass relayed nearly eighteen hours late: transmitted the previous evening,
        # received today, so it belongs to today with its own timestamp
        event_at(ev, "ffff0006", "2026-08-31T20:00:00Z", recv="2026-09-01T13:50:00Z"),
        copy_at(cp, "ffff0006", "2026-08-31T20:00:00Z", source="barentswatch", license="NLOD-2.0", recv="2026-09-01T13:50:00Z"),
    ]
    wx = copy.deepcopy(methyd)
    wx["r"]["msgtime"] = "2026-09-01T13:40:00+00:00"
    extra_day.append(wx)
    boundary = [
        # transmitted just before midnight, received just after: it belongs to the day it arrived
        event_at(ev, "eeee0005", "2026-09-01T23:59:58Z", recv="2026-09-02T00:00:01Z"),
        copy_at(cp, "eeee0005", "2026-09-01T23:59:58Z", recv="2026-09-02T00:00:01Z"),
    ]
    return extra_day, boundary


@pytest.fixture
def packaged(tmp_path):
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    extra_day, boundary = crafted(envs)
    root = make_tree(tmp_path, extra_day, boundary)
    files = sorted(glob.glob(f"{root}/**/*.gz", recursive=True))
    catalog = packager.get_catalog()
    con = duckdb.connect()
    packager.process_day(DAY, files, con, catalog)
    return envs, catalog, con, files


def hexid(short):
    """A crafted id as the 32 hex characters the stream carries: short names pad with zeros."""
    return short if len(short) == 32 else short.ljust(32, "0")


def rows(catalog, name):
    """Rows with ids read back as the short names the tests use (real fixture ids stay 32 hex)."""
    out = catalog.load_table(f"ais.{name}").scan().to_arrow().to_pylist()
    for r in out:
        if isinstance(r.get("id"), bytes):
            assert len(r["id"]) == 16, "ids are stored as 16 raw bytes"
            h = r["id"].hex()
            r["id"] = h[:8] if h[8:] == "0" * 24 else h
    return out


def test_package_day(packaged):
    envs, catalog, con, files = packaged
    positions = rows(catalog, "positions")
    receptions = rows(catalog, "receptions")
    weather = rows(catalog, "weather")
    vessels = rows(catalog, "vessels")

    # expected positions, modeled independently of the SQL: unflagged position events received in
    # the day, crash-window pair collapsed
    fix_pos = sum(
        1 for e in envs
        if e["k"] == "event" and not e.get("implausible") and not e.get("stale")
        and e["r"]["msg_type"] in ("PositionReport", "StandardClassBPositionReport", "ExtendedClassBPositionReport", "LongRangeAisBroadcastMessage")
        and e["t"].startswith("2026-09-01")
    )
    want = fix_pos + 1 + 1 + 2 + 1 + 2 + 2 + 1  # dup-copy tx, collapsed pair, anchored triple, same-instant pair, repeat pair, no-fix and uncorroborated, late satellite tx
    assert len(positions) == want

    keys = {(p["id"], p["ts"]) for p in positions}
    assert len(keys) == len(positions), "id+ts must be unique"
    assert sum(1 for p in positions if p["id"] == "bbbb0002") == 1, "crash pair must collapse"
    assert sorted(p["ts"].second for p in positions if p["id"] == "abab0008") == [0, 18], "collapse anchors on the kept row"
    assert sum(1 for p in positions if p["id"] == "abab0009") == 1, "a same-instant re-accept folds to one row"
    by_id = {p["id"]: p for p in positions}
    assert by_id["adad0011"]["lat6"] is None and by_id["adad0011"]["lon6"] is None, "coordinates follow the server's decision"
    assert by_id["aeae0012"]["corroborated"] is False and by_id["aaaa0001"]["corroborated"] is True
    assert all(p["lat6"] is not None for p in positions if p["id"] != "adad0011"), "real fixes keep their coordinates"
    joined = sorted(r["ts"].second for r in rows(catalog, "receptions") if r["id"] == "abab0008")
    assert joined == [0, 0, 18], "each copy joins the one transmission it names; the folded 9 s copy follows its row"
    assert sum(1 for p in positions if p["id"] == "cccc0003") == 2, "re-transmission must not collapse"
    assert not any(p["id"] == "dddd0004" for p in positions), "implausible stays out"
    assert not any(p["id"] == "eeee0005" for p in positions), "a transmission received after midnight belongs to the next day"
    late = [p for p in positions if p["id"] == "ffff0006"]
    assert len(late) == 1, "a report received hours late must not be lost"
    assert late[0]["ts"].isoformat().startswith("2026-08-31T20:00"), "it keeps its own timestamp"
    assert all(p["day"].isoformat() == DAY for p in positions)

    by_id = {}
    for r in receptions:
        by_id.setdefault(r["id"], []).append(r)
    assert len(by_id["aaaa0001"]) == 2, "late copy joins its transmission"
    assert {r["license"] for r in by_id["aaaa0001"]} == {"NLOD-2.0", "aishub-terms"}
    assert len(receptions) >= len(positions)

    assert len(weather) == 2, "every observation received in the day, whatever its own clock says"
    wx = next(w for w in weather if w["avg_wind_speed"] is not None)
    assert wx["avg_wind_speed"] is not None and isinstance(wx["sea_state"], str)
    assert wx["day"].isoformat() == DAY

    assert any(v["name"] for v in vessels), "statics fold into vessels"

    for name, want in (("positions", ["day", "mmsi_bucket"]), ("receptions", ["day", "mmsi_bucket"]), ("weather", ["day"])):
        spec = catalog.load_table(f"ais.{name}").spec()
        assert [f.name for f in spec.fields] == want, f"{name} partitions"
    assert "bucket[32]" in str(catalog.load_table("ais.positions").spec().fields[1].transform)
    assert catalog.load_table("ais.positions").properties[packager.ROW_GROUP_KEY] == packager.ROW_GROUP_ROWS

    # ids are the stream's hex, stored as its 16 raw bytes
    stream_ids = {e["r"]["id"] for e in envs if e["k"] == "event"}
    raw = catalog.load_table("ais.positions").scan().to_arrow().column("id").to_pylist()
    assert all(len(b) == 16 for b in raw) and {b.hex() for b in raw} & stream_ids, "ids unhex from the stream"

    # every reception carries its position's mmsi
    pos_mmsi = {(p["id"], p["ts"]): p["mmsi"] for p in positions}
    for r in rows(catalog, "receptions"):
        if (r["id"], r["ts"]) in pos_mmsi:
            assert r["mmsi"] == pos_mmsi[(r["id"], r["ts"])]

    # cell is the one-degree cell of the position, null without one
    for p in positions:
        if p["lat6"] is None:
            assert p["cell"] is None
        else:
            lat, lon = p["lat6"] / 600000, p["lon6"] / 600000
            assert p["cell"] == min(math.floor(lat) + 90, 179) * 360 + min(math.floor(lon) + 180, 359)

    # within each written file, positions run in cell order, so a bbox query prunes on cell statistics
    for f in catalog.load_table("ais.positions").inspect.files().to_pylist():
        cells = [c for c in pq.read_table(f["file_path"].removeprefix("file://"), columns=["cell"]).column("cell").to_pylist() if c is not None]
        assert cells == sorted(cells), f"{f['file_path']} is not in cell order"


def test_rerun_replaces_day(packaged):
    envs, catalog, con, files = packaged
    first = {(p["id"], p["ts"]) for p in rows(catalog, "positions")}
    packager.process_day(DAY, files, con, catalog)
    again = {(p["id"], p["ts"]) for p in rows(catalog, "positions")}
    assert first == again


def test_unknown_envelope_version_fails(tmp_path):
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    alien = template(envs, "event", "PositionReport")
    alien["v"] = 2
    root = make_tree(tmp_path, [alien])
    files = sorted(glob.glob(f"{root}/**/*.gz", recursive=True))
    with pytest.raises(SystemExit, match="unknown version"):
        packager.process_day(DAY, files, duckdb.connect(), packager.get_catalog())


def test_positions_without_copies_fails(tmp_path):
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    ev, cp = template(envs, "event", "PositionReport"), template(envs, "copy")
    # totals match (two positions, two receptions) while one position has none of its own
    only_events = [event_at(ev, "ffff0006", "2026-09-01T13:00:00Z"), event_at(ev, "ffff0007", "2026-09-01T13:01:00Z"),
                   copy_at(cp, "ffff0007", "2026-09-01T13:01:00Z"), copy_at(cp, "ffff0007", "2026-09-01T13:01:01Z", tx="2026-09-01T13:01:00Z")]
    root = tmp_path / "normalized"
    d = root / "normalized/v1/2026/09/01"
    d.mkdir(parents=True)
    with gzip.open(d / "12.gz", "wt") as f:
        f.writelines(json.dumps(e) + "\n" for e in only_events)
    files = sorted(glob.glob(f"{root}/**/*.gz", recursive=True))
    with pytest.raises(SystemExit, match="join lost data"):
        packager.process_day(DAY, files, duckdb.connect(), packager.get_catalog())


def test_copy_after_midnight_joins_the_previous_days_transmission(tmp_path):
    """A copy that arrives after midnight for a transmission received before it still lands in
    receptions, joined to the previous day's position."""
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    ev, cp = template(envs, "event", "PositionReport"), template(envs, "copy")
    root = tmp_path / "normalized"
    days = {
        "2026-08-31": [event_at(ev, "abab0007", "2026-08-31T23:59:59Z"),
                       copy_at(cp, "abab0007", "2026-08-31T23:59:59Z", source="kystverket", license="NLOD-2.0")],
        "2026-09-01": [copy_at(cp, "abab0007", "2026-08-31T23:59:59Z", recv="2026-09-01T00:00:40Z")],
    }
    catalog = packager.get_catalog()
    con = duckdb.connect()
    for day, envelopes in days.items():
        d = root / f"normalized/v1/{day.replace('-', '/')}"
        d.mkdir(parents=True)
        hour = "23" if day == "2026-08-31" else "00"
        with gzip.open(d / f"{hour}.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in envelopes)
        packager.process_day(day, sorted(glob.glob(f"{d}/*.gz")), con, catalog)

    rx = [r for r in rows(catalog, "receptions") if r["id"] == "abab0007"]
    assert {r["day"].isoformat() for r in rx} == {"2026-08-31", "2026-09-01"}, "the late copy must not drop"
    assert len({r["ts"] for r in rx}) == 1, "both copies join the one transmission"


def test_reaccept_after_midnight_folds_into_the_previous_day(tmp_path):
    """A crash-window re-accept a few seconds after midnight duplicates a transmission the previous
    day already packaged, and must not become a second position."""
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    ev, cp = template(envs, "event", "PositionReport"), template(envs, "copy")
    root = tmp_path / "normalized"
    days = {
        "2026-08-31": [event_at(ev, "acac0010", "2026-08-31T23:59:59Z"), copy_at(cp, "acac0010", "2026-08-31T23:59:59Z")],
        "2026-09-01": [event_at(ev, "acac0010", "2026-09-01T00:00:02Z"), copy_at(cp, "acac0010", "2026-09-01T00:00:02Z")],
    }
    catalog = packager.get_catalog()
    con = duckdb.connect()
    for day, envelopes in days.items():
        d = root / f"normalized/v1/{day.replace('-', '/')}"
        d.mkdir(parents=True)
        hour = "23" if day == "2026-08-31" else "00"
        with gzip.open(d / f"{hour}.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in envelopes)
        packager.process_day(day, sorted(glob.glob(f"{d}/*.gz")), con, catalog)

    assert [p["day"].isoformat() for p in rows(catalog, "positions") if p["id"] == "acac0010"] == ["2026-08-31"]
    rx = [r for r in rows(catalog, "receptions") if r["id"] == "acac0010"]
    assert len(rx) == 2 and len({r["ts"] for r in rx}) == 1, "the re-accept's copy joins the earlier transmission"


def test_weather_columns_cover_every_methyd_field():
    """Every field of a real MetHyd record is a weather column, read directly, or ignored on
    purpose: a field outside all three would reach the stream and vanish from the table."""
    def camel(snake):
        head, *rest = snake.split("_")
        return head + "".join(w.capitalize() for w in rest)

    known = {camel(c) for c in packager.WEATHER_NUM + packager.WEATHER_STR}
    known |= set(packager.WEATHER_READ) | set(packager.WEATHER_IGNORED)
    record = template(fixture_envelopes(), "methyd")["r"]
    assert set(record) - known == set(), f"unhandled MetHyd fields: {sorted(set(record) - known)}"


def test_main_repackages_changed_days_and_isolates_failures(tmp_path, monkeypatch, capsys):
    """A day is skipped only when packaged from exactly its current hours; an hour that lands late
    repackages it. A day that fails does not stop the rest of the week, but fails the run."""
    from datetime import datetime, timedelta, timezone

    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    ev, cp = template(envs, "event", "PositionReport"), template(envs, "copy")
    root = tmp_path / "normalized"
    now = datetime.now(timezone.utc)
    d1, d2, d3 = ((now - timedelta(days=n)).strftime("%Y-%m-%d") for n in (1, 2, 3))

    def hour(day, hh, envelopes):
        d = root / f"normalized/v1/{day.replace('-', '/')}"
        d.mkdir(parents=True, exist_ok=True)
        with gzip.open(d / f"{hh}.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in envelopes)

    def tx(day, hh, id):
        t = f"{day}T{hh}:00:00Z"
        return [event_at(ev, id, t), copy_at(cp, id, t)]

    def run():
        monkeypatch.setattr(sys, "argv", ["packager", "--normalized", str(root), "--min-hours", "1"])
        try:
            packager.main()
        except SystemExit as e:
            return str(e)

    hour(d2, "01", tx(d2, "01", "d2000001"))
    hour(d1, "01", tx(d1, "01", "d1000001"))
    bad = copy.deepcopy(ev)
    bad["v"] = 99
    hour(d3, "01", [bad])
    assert run() == f"failed: {d3}", "the bad day fails the run"
    days = {p["day"].isoformat() for p in rows(packager.get_catalog(), "positions")}
    assert days == {d1, d2}, "the days after the bad one still package"

    capsys.readouterr()
    run()
    assert "positions" not in capsys.readouterr().err, "unchanged days are not repackaged"

    hour(d2, "05", tx(d2, "05", "d2000002"))  # an upload that arrived late
    run()
    err = capsys.readouterr().err
    assert f"{d2}: 2 positions" in err and f"{d1}:" not in err, "only the changed day repackages"
    assert sum(1 for p in rows(packager.get_catalog(), "positions") if p["day"].isoformat() == d2) == 2

    # a replay can overwrite an hour with different content of the same size
    f1 = root / f"normalized/v1/{d1.replace('-', '/')}/01.gz"
    os.utime(f1, (f1.stat().st_atime, f1.stat().st_mtime + 60))
    run()
    assert f"{d1}: 1 positions" in capsys.readouterr().err, "an overwritten hour repackages its day"


def test_vessel_fields_merge_on_their_own_times_in_any_day_order(tmp_path):
    """Each vessel field keeps its own observation time. A day packaged after a later one (a backfill,
    a late hour) must win a field it saw more recently than what is stored, even when the stored row
    was touched later by a different field."""
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    envs = fixture_envelopes()
    st, cp = template(envs, "event", "ShipStaticData"), template(envs, "copy")
    mmsi = st["r"]["mmsi"]

    def static(id, ts, recv, name="", callsign=""):
        e = event_at(st, id, ts, recv=recv)
        e["r"]["message"].update(Name=name, CallSign=callsign)
        return [e, copy_at(cp, id, ts, recv=recv)]

    root = tmp_path / "normalized"
    days = {  # packaged in this order
        "2026-09-02": static("5a000001", "2026-09-01T05:00:00Z", "2026-09-02T01:00:00Z", callsign="C5")
                      + static("5a000002", "2026-09-01T10:00:00Z", "2026-09-02T01:00:00Z", name="N10"),
        "2026-09-01": static("5a000003", "2026-09-01T08:00:00Z", "2026-09-01T08:00:00Z", callsign="C8"),
    }
    catalog = packager.get_catalog()
    con = duckdb.connect()
    for day, envelopes in days.items():
        d = root / f"normalized/v1/{day.replace('-', '/')}"
        d.mkdir(parents=True)
        with gzip.open(d / "08.gz", "wt") as f:
            f.writelines(json.dumps(e) + "\n" for e in envelopes)
        packager.process_day(day, sorted(glob.glob(f"{d}/*.gz")), con, catalog)

    [v] = [v for v in rows(catalog, "vessels") if v["mmsi"] == mmsi]
    assert (v["name"], v["callsign"]) == ("N10", "C8"), "the callsign seen at 08:00 beats the one seen at 05:00"


def test_cell_covers_the_poles_and_the_antimeridian():
    """Every position has a cell in 0..64799 (latitude 90 and longitude 180 fold into the last row and
    column), and no position means no cell."""
    con = duckdb.connect()
    for lat, lon, want in ((-90, -180, 0), (0, 0, 90 * 360 + 180), (90, 180, 179 * 360 + 359), (59.9, 10.7, 149 * 360 + 190),
                           ("NULL", 10.7, None), (59.9, "NULL", None)):
        got = con.execute(f"SELECT {packager.cell_sql(lat, lon)}").fetchone()[0]
        assert got == want, (lat, lon, got, want)


def test_an_older_table_layout_is_refused(tmp_path):
    """A table created by an earlier packager (string ids, day-only partitions) is never written into."""
    packager.HERE = tmp_path / "home"
    packager.HERE.mkdir()
    old = pa.schema([("id", pa.string()), ("mmsi", pa.int32()), ("ts", pa.timestamp("us")), ("day", pa.date32())])
    from pyiceberg.catalog import load_catalog
    wh = packager.HERE / "warehouse"
    wh.mkdir()
    cat = load_catalog("local", uri=f"sqlite:///{wh}/catalog.db", warehouse=f"file://{wh}")
    cat.create_namespace("ais")
    cat.create_table("ais.positions", schema=old)
    with pytest.raises(SystemExit, match="older layout"):
        packager.get_catalog()
