#!/usr/bin/env python3
"""Delivery delay per source from one archive hour, without the 60 s wraparound.

Reads the archive files (recv_time \\t station \\t body) in a directory, one .gz per source,
and prints two tables:

  1. upstream -> aiscast: our receive time minus the timestamp the upstream itself put on the
     message (Digitraffic `time`, BarentsWatch `msgtime`, aisstream `time_utc`, AISHub `TIME`,
     AIS-catcher `rxuxtime`, TAG block `c:`). Exact, but the upstream's clock is its own receipt,
     not the broadcast.
  2. cross-source: the same position report (mmsi + exact AIS lat/lon, moving vessels only) heard
     via several sources, each source's arrival relative to the first. Exact broadcast-relative delay
     for everything the fastest source also heard.

  python3 analysis/archive-delay.py <dir with *.gz>
"""
import gzip, json, sys, glob, os, re
from datetime import datetime, timezone

def ts(s):  # RFC3339 with nanoseconds -> unix float
    return datetime.fromisoformat(s.rstrip("Z")[:26] + "+00:00").timestamp()  # Go trims trailing zeros

# --- minimal AIS decoder for types 1/2/3/18/19: (mmsi, lat_raw, lon_raw, sog_tenths) -----
def bits(payload):
    return "".join(format((ord(c) - 48 - (8 if ord(c) > 88 else 0)), "06b") for c in payload)

def sint(b):
    v = int(b, 2)
    return v - (1 << len(b)) if b[0] == "1" else v

def decode(sentence):
    parts = sentence.split(",")
    if len(parts) < 7 or parts[1] != "1":
        return None
    b = bits(parts[5])
    t = int(b[:6], 2)
    if t in (1, 2, 3):
        sog, lon, lat = b[50:60], b[61:89], b[89:116]
    elif t in (18, 19):
        sog, lon, lat = b[46:56], b[57:85], b[85:112]
    else:
        return None
    return int(b[8:38], 2), sint(lat), sint(lon), int(sog, 2)

def key(mmsi, lat, lon, sog):
    if sog < 10 or lat == 91 * 600000 or lon == 181 * 600000:  # moving vessels only, no "unavailable"
        return None
    return (mmsi, lat, lon)

# --- one generator per archive format: yields (recv, upstream_time_or_None, key_or_None) --------
def nmea_lines(f):
    for line in f:
        recv, _, body = line.rstrip("\n").split("\t", 2)
        up = None
        if body.startswith("\\"):
            tag, body = body[1:].split("\\", 1)
            for kv in tag.split("*")[0].split(","):
                if kv.startswith("c:"):
                    up = int(kv[2:]) / (1000 if len(kv) > 12 else 1)
        if body.startswith("!"):
            d = decode(body)
            yield ts(recv), up, d and key(*d)

HEADER = re.compile(r"\d{4}-\d\d-\d\dT\S+\t")

def records(f):
    """(recv, body) per archive record; bodies may span lines (pretty-printed JSON)."""
    recv = None; buf = []
    for line in f:
        if HEADER.match(line):
            if recv:
                yield recv, "".join(buf)
            head = line.rstrip("\n").split("\t", 2)
            recv, buf = head[0], [head[2]]
        else:
            buf.append(line)
    if recv:
        yield recv, "".join(buf)

def aiscatcher(f):
    for recv, body in records(f):
        if not body.startswith("{"):
            continue
        for m in json.loads(body).get("msgs", []):
            up = m.get("rxuxtime") or datetime.strptime(m["rxtime"], "%Y%m%d%H%M%S").replace(tzinfo=timezone.utc).timestamp()
            d = decode(m["nmea"][0])
            yield ts(recv), up, d and key(*d)

def digitraffic(f):
    for recv, body in records(f):
        topic, js = body.split(" ", 1)
        try:
            m = json.loads(js)
        except json.JSONDecodeError:
            continue
        if "lat" in m:
            yield ts(recv), m["time"], key(int(topic.split("/")[1]), round(m["lat"] * 600000), round(m["lon"] * 600000), round(m["sog"] * 10))

def barentswatch(f):
    for line in f:
        recv, _, body = line.rstrip("\n").split("\t", 2)
        m = json.loads(body)
        if m.get("type") != "Position":
            continue
        up = datetime.fromisoformat(m["msgtime"]).timestamp()
        k = key(m["mmsi"], round(m["latitude"] * 600000), round(m["longitude"] * 600000), round((m["speedOverGround"] or 0) * 10))
        yield ts(recv), up, k

def aisstream(f):
    for line in f:
        recv, _, body = line.rstrip("\n").split("\t", 2)
        m = json.loads(body)
        if "MetaData" not in m:  # error frames
            continue
        t = m["MetaData"]["time_utc"]  # Go time.String(): "2026-09-04 11:59:45.446469494 +0000 UTC", fraction optional
        up = datetime.strptime(t[:19], "%Y-%m-%d %H:%M:%S").replace(tzinfo=timezone.utc).timestamp() + (float(t[19:].split(" ")[0]) if t[19:20] == "." else 0)
        pr = m["Message"].get("PositionReport") or m["Message"].get("StandardClassBPositionReport")
        k = pr and key(pr["UserID"], round(pr["Latitude"] * 600000), round(pr["Longitude"] * 600000), round(pr["Sog"] * 10))
        yield ts(recv), up, k

def aishub(f):
    seen = {}
    for line in f:
        recv, _, body = line.rstrip("\n").split("\t", 2)
        doc = json.loads(body)
        if doc[0].get("ERROR"):
            continue
        r = ts(recv)
        for row in doc[1]:
            if seen.get(row["MMSI"]) == row["TIME"]:  # unchanged since the last poll
                continue
            seen[row["MMSI"]] = row["TIME"]
            yield r, int(row["TIME"]), key(row["MMSI"], row["LATITUDE"], row["LONGITUDE"], row["SOG"])

RELAYS = {"aishub", "aisstream"}  # aggregators; never the reference for "first heard"

READERS = {"aishub": aishub, "aisstream": aisstream, "barentswatch": barentswatch, "digitraffic": digitraffic, "http": aiscatcher}

def pct(xs, p):
    return xs[min(len(xs) - 1, int(p * len(xs)))]

def row(name, xs):
    xs.sort()
    return f"{name:<28} {len(xs):>8} {pct(xs, .5):>8.1f} {pct(xs, .9):>8.1f} {pct(xs, .99):>8.1f} {xs[-1]:>8.1f}"

def main(d):
    upstream = {}   # source -> [recv - upstream]
    first = {}      # key -> (recv, source)
    arrivals = {}   # source -> {key: recv}
    for path in sorted(glob.glob(os.path.join(d, "*.gz"))):
        src = os.path.basename(path)[:-3]
        reader = READERS.get(src.split("_")[0], nmea_lines)
        ups, arr = upstream.setdefault(src, []), arrivals.setdefault(src, {})
        with gzip.open(path, "rt") as f:
            for recv, up, k in reader(f):
                if up is not None:
                    ups.append(recv - up)
                if k is not None and k not in arr:
                    arr[k] = recv
                    if src.split("_")[0] not in RELAYS and (k not in first or recv < first[k][0]):
                        first[k] = (recv, src)
    hdr = f"{'source':<28} {'n':>8} {'p50':>8} {'p90':>8} {'p99':>8} {'max':>8}"
    print("upstream timestamp -> aiscast receipt (s)\n" + hdr)
    for src, xs in sorted(upstream.items(), key=lambda kv: -len(kv[1])):
        if xs:
            print(row(src[:28], xs))
    print("\nsame message, arrival relative to the first direct (non-aggregator) source to deliver it (s)\n" + hdr + "   first%")
    shared = {k for k in first if sum(k in a for a in arrivals.values()) > 1}
    for src, arr in sorted(arrivals.items(), key=lambda kv: -len(kv[1])):
        xs = [recv - first[k][0] for k, recv in arr.items() if k in shared]
        if xs:
            won = sum(1 for k in arr if k in shared and first[k][1] == src)
            print(row(src[:28], xs) + f" {100 * won / len(xs):>7.0f}")

if __name__ == "__main__":
    main(sys.argv[1])
