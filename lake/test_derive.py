"""Smoke test for the derive job: crafted fixtures for every archive format,
asserting dedup, hygiene filters, and vessel-class inference end to end."""

import gzip
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

import duckdb
import pytest
from pyais.encode import encode_dict

sys.path.insert(0, str(Path(__file__).parent))
import derive

DAY = "2026-08-21"
T0 = int(datetime(2026, 8, 21, 12, 0, 0, tzinfo=timezone.utc).timestamp())

B_MMSI, A_MMSI, CERULEAN = 257000001, 230111222, 368168720
# wire-exact values so every format encodes them identically
B_POS = dict(lat=59.5, lon=10.25, speed=6.5, course=123.4, heading=87)
A_POS = dict(lat=60.1, lon=24.9, speed=0.0, course=0.0, heading=0)  # heading 0 (due north) must survive, not decay to 511


def ts(offset):
    return datetime.fromtimestamp(T0 + offset, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000000Z")


def nmea_line(source, offset, payload_dict):
    # pyais encodes own-ship VDO; archives carry VDM, so rewrite and re-checksum
    def vdm(s):
        body = s.replace("VDO", "VDM").split("*")[0][1:]
        cs = 0
        for ch in body.encode():
            cs ^= ch
        return f"!{body}*{cs:02X}"

    sentences = [vdm(s) for s in encode_dict(payload_dict, talker_id="AI")]
    tag = f"\\c:{T0 + offset}*00\\"
    return "".join(f"{ts(offset)}\t{source}\t{tag}{s}\n" for s in sentences)


def write_gz(root, rel, text):
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    with gzip.open(path, "wt") as f:
        f.write(text)


@pytest.fixture
def fixture_archive(tmp_path):
    archive = tmp_path / "archive"
    y, m, d = DAY.split("-")
    hour = f"{y}/{m}/{d}/12.gz"

    kyst = (
        # Class B vessel, heard twice (same-source duplicate collapses to one reception)
        nmea_line("kystverket", 20, dict(type=18, mmsi=B_MMSI, **B_POS)) * 2
        # Class A vessel position, same content digitraffic reports below
        + nmea_line("kystverket", 33, dict(type=1, mmsi=A_MMSI, status=0, **A_POS))
        # multipart type 5 static for the A vessel
        + nmea_line("kystverket", 35, dict(type=5, mmsi=A_MMSI, shipname="FIXTURE A", ship_type=70, draught=3.3))
        # net-buoy MMSI in the unallocated MID gap: dropped
        + nmea_line("kystverket", 40, dict(type=18, mmsi=586123456, **B_POS))
    )
    write_gz(archive, f"NLOD-2.0/kystverket/{hour}", kyst)

    write_gz(
        archive,
        f"feeder/v1/mmsi/{CERULEAN}/{hour}",
        nmea_line(f"v1:mmsi:{CERULEAN}", 50, dict(type=18, mmsi=CERULEAN, lat=41.5, lon=-71.3, speed=0.0, course=0.0, heading=511)),
    )

    dt_loc = json.dumps({"lat": A_POS["lat"], "lon": A_POS["lon"], "sog": 0.0, "cog": 0.0, "heading": A_POS["heading"], "navStat": 0, "time": T0 + 30}, indent=2)
    dt_meta = json.dumps({"name": "FIXTURE A", "callSign": "OH123", "shipType": 70, "draught": 33, "time": T0 + 31}, indent=2)
    write_gz(
        archive,
        f"CC-BY-4.0/digitraffic/{hour}",
        f"{ts(30)}\tdigitraffic\tvessels-v2/{A_MMSI}/location {dt_loc}\n{ts(31)}\tdigitraffic\tvessels-v2/{A_MMSI}/metadata {dt_meta}\n",
    )

    aisstream = {
        "MessageType": "StandardClassBPositionReport",
        "MetaData": {"MMSI": B_MMSI, "time_utc": f"{DAY} 12:00:25.000000000 +0000 UTC"},
        "Message": {"StandardClassBPositionReport": {"UserID": B_MMSI, "Latitude": B_POS["lat"], "Longitude": B_POS["lon"], "Sog": B_POS["speed"], "Cog": B_POS["course"], "TrueHeading": B_POS["heading"]}},
    }
    cerulean_static = {
        "MessageType": "StaticDataReport",
        "MetaData": {"MMSI": CERULEAN, "ShipName": "CERULEAN", "time_utc": f"{DAY} 12:00:55.000000000 +0000 UTC"},
        "Message": {"StaticDataReport": {"ReportB": {"CallSign": "WDQ5444", "ShipType": 36}}},
    }
    write_gz(
        archive,
        f"aisstream-io-terms/aisstream/{hour}",
        f"{ts(25)}\taisstream\t{json.dumps(aisstream)}\n{ts(55)}\taisstream\t{json.dumps(cerulean_static)}\n",
    )

    snapshot = [
        {"USERNAME": "TEST", "RECORDS": 3},
        [
            # same transmission as the kystverket/aisstream Class B pair: third reception, one message
            {"MMSI": B_MMSI, "TIME": str(T0 + 28), "LATITUDE": round(B_POS["lat"] * 600000), "LONGITUDE": round(B_POS["lon"] * 600000), "SOG": 65, "COG": 1234, "HEADING": 87, "NAVSTAT": 15, "NAME": "TEST B"},
            # stale state (an hour old): not a fresh reception, dropped
            {"MMSI": 219000111, "TIME": str(T0 - 3600), "LATITUDE": 30000000, "LONGITUDE": 6000000, "SOG": 0, "COG": 0, "HEADING": 0, "NAVSTAT": 1},
            # null island (no GPS lock): dropped
            {"MMSI": 219000222, "TIME": str(T0 + 55), "LATITUDE": 0, "LONGITUDE": 0, "SOG": 0, "COG": 0, "HEADING": 511, "NAVSTAT": 15},
        ],
    ]
    write_gz(archive, f"aishub-terms/aishub/{hour}", f"{ts(60)}\taishub\t{json.dumps(snapshot)}\n")
    return archive


def test_derive_day(tmp_path, fixture_archive):
    derive.HERE = tmp_path / "lake"
    derive.HERE.mkdir()
    catalog = derive.get_catalog()
    con = duckdb.connect()
    files = sorted(str(p) for p in fixture_archive.rglob("*.gz"))
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False)

    messages = catalog.load_table("ais.messages").scan().to_arrow().to_pylist()
    receptions = catalog.load_table("ais.receptions").scan().to_arrow().to_pylist()
    vessels = {v["mmsi"]: v for v in catalog.load_table("ais.vessels").scan().to_arrow().to_pylist()}

    # three position messages: the B trio, the A pair, CERULEAN
    assert len(messages) == 3
    by_mmsi = {m["mmsi"]: m for m in messages}
    assert set(by_mmsi) == {B_MMSI, A_MMSI, CERULEAN}

    # hygiene: net buoy, stale aishub state, and null island never surface
    assert 586123456 not in by_mmsi
    assert not any(r["msg_id"] not in {m["id"] for m in messages} for r in receptions)

    # wire precision survives every format
    b = by_mmsi[B_MMSI]
    assert (b["lat6"], b["lon6"], b["sog10"], b["cog10"], b["heading"]) == (35700000, 6150000, 65, 1234, 87)

    # cross-source dedup: one B message heard by three sources, one A message by two
    rx_by_msg = {}
    for r in receptions:
        rx_by_msg.setdefault(r["msg_id"], set()).add(r["source"])
    assert rx_by_msg[by_mmsi[B_MMSI]["id"]] == {"kystverket", "aisstream", "aishub"}
    assert rx_by_msg[by_mmsi[A_MMSI]["id"]] == {"kystverket", "digitraffic"}
    # same-source duplicate lines collapse to one reception per station
    assert len(receptions) == 6

    # licenses ride along
    licenses = {r["source"]: r["license"] for r in receptions}
    assert licenses["kystverket"] == "NLOD-2.0"
    assert licenses[f"v1:mmsi:{CERULEAN}"] == "feeder"

    # vessels: names from statics, class from position-message evidence (not JSON defaults)
    assert vessels[A_MMSI]["name"] == "FIXTURE A"
    assert vessels[A_MMSI]["cls"] == "A"
    assert vessels[CERULEAN]["name"] == "CERULEAN"
    assert vessels[CERULEAN]["cls"] == "B"
    assert vessels[B_MMSI]["cls"] == "B"


def test_rerun_replaces_day(tmp_path, fixture_archive):
    derive.HERE = tmp_path / "lake"
    derive.HERE.mkdir()
    catalog = derive.get_catalog()
    con = duckdb.connect()
    files = sorted(str(p) for p in fixture_archive.rglob("*.gz"))
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False)
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False)
    assert len(catalog.load_table("ais.messages").scan().to_arrow()) == 3
