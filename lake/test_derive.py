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
from pyais.util import compute_checksum

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


def vdm(s):
    # pyais encodes own-ship VDO; archives carry VDM, so rewrite and re-checksum
    body = s.replace("VDO", "VDM").split("*")[0]
    return f"{body}*{compute_checksum(body.encode()):02X}"


def nmea_line(source, offset, payload_dict):
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

    # http feeder: AIS-catcher JSON envelope wrapping the same B sentence, fourth copy
    b_sentence = vdm(encode_dict(dict(type=18, mmsi=B_MMSI, **B_POS), talker_id="AI")[0])
    rxtime = datetime.fromtimestamp(T0 + 27, tz=timezone.utc).strftime("%Y%m%d%H%M%S")
    envelope = json.dumps({"protocol": "jsonaiscatcher", "msgs": [{"class": "AIS", "channel": "A", "rxtime": rxtime, "nmea": [b_sentence]}]})
    write_gz(archive, f"CC0-1.0/http/test/{hour}", f"{ts(27)}\thttp:test\t{envelope}\n")

    # barentswatch: line-delimited JSON, same Class A transmission the two above carry
    bw = json.dumps({
        "type": "Position", "messageType": 1, "mmsi": A_MMSI, "msgtime": ts(31), "stream": "terra",
        "latitude": A_POS["lat"], "longitude": A_POS["lon"], "speedOverGround": A_POS["speed"],
        "courseOverGround": A_POS["course"], "trueHeading": A_POS["heading"],
        "navigationalStatus": 0, "aisClass": "A",
    })
    bw_static = json.dumps({
        "type": "Staticdata", "mmsi": A_MMSI, "msgtime": ts(32), "stream": "terra",
        "name": "FIXTURE A", "callSign": "OH123", "shipType": 70, "draught": 33, "aisClass": "A",
    })
    write_gz(archive, f"NLOD-2.0/barentswatch/{hour}", f"{ts(31)}\tbarentswatch\t{bw}\n{ts(32)}\tbarentswatch\t{bw_static}\n")

    snapshot = [
        {"USERNAME": "TEST", "RECORDS": 3},
        [
            # same transmission as the kystverket/aisstream Class B pair: third reception, one message
            {"MMSI": B_MMSI, "TIME": str(T0 + 28), "LATITUDE": round(B_POS["lat"] * 600000), "LONGITUDE": round(B_POS["lon"] * 600000), "SOG": 65, "COG": 1234, "HEADING": 87, "NAVSTAT": 15, "NAME": "TEST B"},
            # stale state (an hour old): not a fresh reception, dropped
            {"MMSI": 219000111, "TIME": str(T0 - 3600), "LATITUDE": 30000000, "LONGITUDE": 6000000, "SOG": 0, "COG": 0, "HEADING": 0, "NAVSTAT": 1},
            # explicit JSON nulls: sentinels fill in, the identity hash must never be NULL
            {"MMSI": 219000333, "TIME": str(T0 + 40), "LATITUDE": 30600000, "LONGITUDE": 6100000, "SOG": None, "COG": None, "HEADING": None, "NAVSTAT": None},
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
    srcdirs = {Path(f).relative_to(fixture_archive).parts[1] for f in files}
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False, srcdirs=srcdirs)
    assert derive.derived_days(catalog) == {DAY}

    messages = catalog.load_table("ais.messages").scan().to_arrow().to_pylist()
    receptions = catalog.load_table("ais.receptions").scan().to_arrow().to_pylist()
    vessels = {v["mmsi"]: v for v in catalog.load_table("ais.vessels").scan().to_arrow().to_pylist()}

    # day partitions are real Iceberg partitions, not just a column
    for name in ("ais.messages", "ais.receptions"):
        assert [f.name for f in catalog.load_table(name).spec().fields] == ["day"]

    # four position messages: the B quartet, the A trio, CERULEAN, the null-field aishub vessel
    assert len(messages) == 4
    by_mmsi = {m["mmsi"]: m for m in messages}
    assert set(by_mmsi) == {B_MMSI, A_MMSI, CERULEAN, 219000333}

    # explicit nulls became wire sentinels, not a NULL identity
    nul = by_mmsi[219000333]
    assert nul["id"] is not None
    assert (nul["sog10"], nul["cog10"], nul["heading"], nul["navstat"]) == (1023, 3600, 511, -1)

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
    assert rx_by_msg[by_mmsi[B_MMSI]["id"]] == {"kystverket", "aisstream", "aishub", "http:test"}
    assert rx_by_msg[by_mmsi[A_MMSI]["id"]] == {"kystverket", "digitraffic", "barentswatch"}
    # same-source duplicate lines collapse to one reception per station
    assert len(receptions) == 9

    # licenses ride along
    licenses = {r["source"]: r["license"] for r in receptions}
    assert licenses["kystverket"] == "NLOD-2.0"
    assert licenses["barentswatch"] == "NLOD-2.0"  # not the 'feeder' fallback
    assert licenses[f"v1:mmsi:{CERULEAN}"] == "CC0-1.0"  # volunteer receptions, per the contributor agreement
    assert licenses["http:test"] == "CC0-1.0"

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
    first = {m["id"] for m in catalog.load_table("ais.messages").scan().to_arrow().to_pylist()}
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False)
    again = {m["id"] for m in catalog.load_table("ais.messages").scan().to_arrow().to_pylist()}
    assert len(again) == 4
    assert again == first  # ids are a pure function of the input, so a rerun reproduces them


def test_hours_present_ignores_neighbouring_boundary_hours():
    files = [
        "a/NLOD-2.0/kystverket/2026/08/21/00.gz",
        "a/aishub-terms/aishub/2026/08/21/00.gz",  # same hour, another source
        "a/NLOD-2.0/kystverket/2026/08/21/23.gz",
        "a/NLOD-2.0/kystverket/2026/08/20/23.gz",  # boundary hour, belongs to the previous day
        "a/NLOD-2.0/kystverket/2026/08/22/00.gz",  # boundary hour, belongs to the next day
        "a/NLOD-2.0/kystverket/2026/08/21/notanhour.gz",
    ]
    assert derive.hours_present(DAY, files) == {"00", "23"}
    assert derive.hours_present(DAY, []) == set()


def test_day_key_matches_any_depth_and_both_boundary_hours():
    want = derive.day_key("2026-08-21")
    assert want.search("ais-archive/NLOD-2.0/kystverket/2026/08/21/12.gz")
    assert want.search(f"ais-archive/feeder/v1/mmsi/{CERULEAN}/2026/08/21/00.gz")  # feeders nest deeper
    # canonical time can sit either side of midnight from the hour file that carries it
    assert want.search("ais-archive/NLOD-2.0/kystverket/2026/08/20/23.gz")
    assert want.search("ais-archive/NLOD-2.0/kystverket/2026/08/22/00.gz")
    # but only those two neighbouring hours
    assert not want.search("ais-archive/NLOD-2.0/kystverket/2026/08/20/22.gz")
    assert not want.search("ais-archive/NLOD-2.0/kystverket/2026/08/22/01.gz")
    assert not want.search("ais-archive/NLOD-2.0/kystverket/2026/08/21/12.gz.tmp")


def test_fetch_copies_gzip_bytes_verbatim(tmp_path):
    """pyarrow's default compression='detect' would decompress .gz on read and recompress on
    write, so the copy would neither match the source bytes nor its recorded size."""
    import pyarrow.fs as pafs

    src = tmp_path / "07.gz"
    with gzip.open(src, "wt") as f:
        f.write("2026-08-21T12:00:00.000000Z\tkystverket\t!AIVDM,1,1,,A,test,0*00\n" * 50)
    local = pafs.LocalFileSystem()
    info = local.get_file_info(str(src))

    dest = tmp_path / "copy" / "07.gz"
    dest.parent.mkdir()
    derive._fetch(local, local, info, dest)

    assert dest.read_bytes() == src.read_bytes()
    assert dest.stat().st_size == info.size
    with gzip.open(dest, "rb") as f:  # still a readable archive hour
        assert f.readline().count(b"\t") == 2


@pytest.fixture
def chained_archive(tmp_path):
    """One vessel repeating an identical report at 0 s, 9 s and 18 s."""
    archive = tmp_path / "chained"
    y, m, d = DAY.split("-")
    text = "".join(nmea_line("kystverket", off, dict(type=1, mmsi=A_MMSI, status=0, **A_POS)) for off in (0, 9, 18))
    # a second vessel at exactly the window width apart: two transmissions, not one
    text += "".join(nmea_line("kystverket", off, dict(type=18, mmsi=B_MMSI, **B_POS)) for off in (0, 10))
    write_gz(archive, f"NLOD-2.0/kystverket/{y}/{m}/{d}/12.gz", text)
    return archive


def test_ten_second_window_anchors_rather_than_chains(tmp_path, chained_archive):
    """Adjacent gaps are both under 10 s, but the window is measured from the last accepted copy,
    so 18 s is a second transmission. Chaining on the previous row would report one."""
    derive.HERE = tmp_path / "lake"
    derive.HERE.mkdir()
    catalog = derive.get_catalog()
    con = duckdb.connect()
    files = sorted(str(p) for p in chained_archive.rglob("*.gz"))
    derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False)

    messages = catalog.load_table("ais.messages").scan().to_arrow().to_pylist()
    a = [m for m in messages if m["mmsi"] == A_MMSI]
    b = [m for m in messages if m["mmsi"] == B_MMSI]
    assert {m["ts"].second for m in a} == {0, 18}
    assert {m["ts"].second for m in b} == {0, 10}  # a copy at exactly 10 s is a new transmission


class Cap:
    def __init__(self):
        self.rows = []

    def add(self, row):
        self.rows.append(row)


def test_type27_na_speed_maps_to_sentinel():
    """Type 27 carries whole knots with 63 = not available, not the 1023 wire sentinel."""
    pos = Cap()
    line = nmea_line("kystverket", 0, dict(type=27, mmsi=A_MMSI, speed=63, course=511, lat=60.1, lon=24.9))
    ts_col, src, payload = line.strip().split("\t", 2)
    derive.nmea_line(src, payload.encode(), derive.parse_recv(ts_col.encode()), {}, pos, Cap())
    (row,) = pos.rows
    assert row[4] == derive.NA_SOG
    assert row[5] == derive.NA_COG


def test_emit_pos_clamps_wire_domain():
    """A JSON source's unvalidated int must clamp to the sentinel, not poison the int16 batch."""
    pos = Cap()
    derive.emit_pos(pos, 230000001, 0, 30000000, 6000000, 40000, -5, 9999, 200, None, None, "aishub", "aishub")
    (row,) = pos.rows
    assert row[4:8] == (derive.NA_SOG, derive.NA_COG, derive.NA_HDG, derive.NA_NAV)


def test_aishub_bad_record_does_not_void_snapshot():
    pos = Cap()
    recv = datetime(2026, 8, 21, 12, 1, 0)
    snapshot = json.dumps([{}, [
        {"MMSI": 230000005, "TIME": "not-a-number"},
        {"MMSI": 230000006, "TIME": str(T0 + 55), "LATITUDE": 30000000, "LONGITUDE": 6000000, "SOG": 0, "COG": 0, "HEADING": 0, "NAVSTAT": 0},
    ]])
    derive.aishub_snapshot(snapshot.encode(), recv, pos, Cap())
    assert [r[0] for r in pos.rows] == [230000006]


def test_source_dropped_by_decoder_fails_the_day(tmp_path, fixture_archive):
    """A source dir that contributed raw files but decoded to nothing must fail, not vanish."""
    y, m, d = DAY.split("-")
    write_gz(fixture_archive, f"MIT/junk/{y}/{m}/{d}/12.gz", "this is not AIS\n")
    derive.HERE = tmp_path / "lake"
    derive.HERE.mkdir()
    catalog = derive.get_catalog()
    con = duckdb.connect()
    files = sorted(str(p) for p in fixture_archive.rglob("*.gz"))
    srcdirs = {Path(f).relative_to(fixture_archive).parts[1] for f in files}
    with pytest.raises(SystemExit, match="junk"):
        derive.process_day(DAY, files, con, catalog, keep_stage=False, reuse_stage=False, srcdirs=srcdirs)
