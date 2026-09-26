"""Packager tests: the committed fixture is pipeline-genuine (produced by `aiscast replay` over
real archived records); edge cases are layered on top as handcrafted envelopes."""

import copy
import glob
import gzip
import json
import sys
from pathlib import Path

import duckdb
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


def event_at(tpl, id, ts, **flags):
    e = copy.deepcopy(tpl)
    e["r"]["id"], e["r"]["time"], e["t"] = id, ts, ts
    e.update(flags)
    return e


def copy_at(tpl, id, ts, source="aishub", license="aishub-terms"):
    c = copy.deepcopy(tpl)
    c["r"].update(id=id, time=ts, source=source, station=source, license=license)
    c["t"] = ts
    return c


def crafted(envs):
    ev = template(envs, "event", "PositionReport")
    cp = template(envs, "copy")
    methyd = template(envs, "methyd")
    extra_day = [
        # a transmission and a late aishub copy of it: one position, two receptions
        event_at(ev, "aaaa0001", "2026-09-01T13:00:00Z"),
        copy_at(cp, "aaaa0001", "2026-09-01T13:00:00Z", source="kystverket", license="NLOD-2.0"),
        copy_at(cp, "aaaa0001", "2026-09-01T13:00:03Z"),
        # a crash-window re-accept: two events, same id, 3 s apart, collapses to one row
        event_at(ev, "bbbb0002", "2026-09-01T13:10:00Z"),
        copy_at(cp, "bbbb0002", "2026-09-01T13:10:00Z"),
        event_at(ev, "bbbb0002", "2026-09-01T13:10:03Z"),
        copy_at(cp, "bbbb0002", "2026-09-01T13:10:03Z"),
        # the same bits on the air three minutes apart: legitimately two transmissions
        event_at(ev, "cccc0003", "2026-09-01T13:20:00Z"),
        copy_at(cp, "cccc0003", "2026-09-01T13:20:00Z"),
        event_at(ev, "cccc0003", "2026-09-01T13:23:00Z"),
        copy_at(cp, "cccc0003", "2026-09-01T13:23:00Z"),
        # implausible: archived in the stream, withheld from the table like the live stream withheld it
        event_at(ev, "dddd0004", "2026-09-01T13:30:00Z", implausible=True),
        copy_at(cp, "dddd0004", "2026-09-01T13:30:00Z"),
    ]
    wx = copy.deepcopy(methyd)
    wx["r"]["msgtime"] = "2026-09-01T13:40:00+00:00"
    extra_day.append(wx)
    boundary = [
        # received in D+1's first hour, transmitted before midnight: belongs to D
        event_at(ev, "eeee0005", "2026-09-01T23:59:58Z"),
        copy_at(cp, "eeee0005", "2026-09-01T23:59:59Z"),
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


def rows(catalog, name):
    return catalog.load_table(f"ais.{name}").scan().to_arrow().to_pylist()


def test_package_day(packaged):
    envs, catalog, con, files = packaged
    positions = rows(catalog, "positions")
    receptions = rows(catalog, "receptions")
    weather = rows(catalog, "weather")
    vessels = rows(catalog, "vessels")

    # expected positions, modeled independently of the SQL: unflagged position events in the day,
    # crash-window pair collapsed
    fix_pos = sum(
        1 for e in envs
        if e["k"] == "event" and not e.get("implausible") and not e.get("stale")
        and e["r"]["msg_type"] in ("PositionReport", "StandardClassBPositionReport", "ExtendedClassBPositionReport", "LongRangeAisBroadcastMessage")
        and e["r"]["time"].startswith("2026-09-01")
    )
    want = fix_pos + 1 + 1 + 2 + 1  # dup-copy tx, collapsed pair, repeat pair, boundary tx
    assert len(positions) == want

    keys = {(p["id"], p["ts"]) for p in positions}
    assert len(keys) == len(positions), "id+ts must be unique"
    assert sum(1 for p in positions if p["id"] == "bbbb0002") == 1, "crash pair must collapse"
    assert sum(1 for p in positions if p["id"] == "cccc0003") == 2, "re-transmission must not collapse"
    assert not any(p["id"] == "dddd0004" for p in positions), "implausible stays out"
    assert any(p["id"] == "eeee0005" for p in positions), "midnight boundary transmission belongs to the day"
    assert all(p["day"].isoformat() == DAY for p in positions)

    by_id = {}
    for r in receptions:
        by_id.setdefault(r["id"], []).append(r)
    assert len(by_id["aaaa0001"]) == 2, "late copy joins its transmission"
    assert {r["license"] for r in by_id["aaaa0001"]} == {"NLOD-2.0", "aishub-terms"}
    assert len(receptions) >= len(positions)

    assert len(weather) == 1, "only the in-day observation; the fixture's out-of-day record stays out"
    wx = weather[0]
    assert wx["avg_wind_speed"] is not None and isinstance(wx["sea_state"], str)
    assert wx["day"].isoformat() == DAY

    assert any(v["name"] for v in vessels), "statics fold into vessels"

    for name in ("positions", "receptions", "weather"):
        spec = catalog.load_table(f"ais.{name}").spec()
        assert [f.name for f in spec.fields] == ["day"], f"{name} must be day-partitioned"


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
    ev = template(envs, "event", "PositionReport")
    only_events = [event_at(ev, "ffff0006", "2026-09-01T13:00:00Z")]
    root = tmp_path / "normalized"
    d = root / "normalized/v1/2026/09/01"
    d.mkdir(parents=True)
    with gzip.open(d / "12.gz", "wt") as f:
        f.writelines(json.dumps(e) + "\n" for e in only_events)
    files = sorted(glob.glob(f"{root}/**/*.gz", recursive=True))
    with pytest.raises(SystemExit, match="join lost data"):
        packager.process_day(DAY, files, duckdb.connect(), packager.get_catalog())
