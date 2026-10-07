package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BertoldVdb/go-ais"
)

// AISHub is reciprocal: we feed them our volunteer receivers' stream over UDP (their assigned port), and poll
// their aggregate snapshot (all stations, positions downsampled to ≤60 s) every 20 s. Their terms grant "use"
// with no stated restriction and no stated term, i.e. revocable at will, so this source is flagged in
// `source`/archive tags and can be switched off and purged; see docs/policy.md.

// ---- feed out ----

type udpFeeder struct {
	conn *net.UDPConn
	mu   sync.Mutex
}

func newUDPFeeder(addr string) (*udpFeeder, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return nil, err
	}
	return &udpFeeder{conn: c}, nil
}

// feedable: only data received directly from volunteer stations goes to AISHub. Their terms prohibit feeding
// synthesized data and data from public AIS sources (Kystverket, Digitraffic, aisstream, their own snapshot).
func feedable(ev *Event) bool {
	if ev.Synthesized || ev.Packet == nil {
		return false
	}
	for _, pfx := range []string{"station:", "udp:", "mmsi:"} { // mmsi: is a UDP station re-keyed by its !AIVDO
		if strings.HasPrefix(ev.Source, pfx) {
			return true
		}
	}
	return false
}

// send re-encodes the event as plain !AIVDM (no TAG block, AI talker) so any aggregator accepts it.
func (f *udpFeeder) send(p *Pipeline, ev *Event) {
	if !feedable(ev) {
		return
	}
	if ev.LowTrust && !ev.Corroborated { // UDP traffic nobody else has heard stays local until corroborated
		p.stats.uncorroborated.Add(1)
		return
	}
	ch := ev.Channel
	if ch == 0 {
		ch = 'A'
	}
	p.mu.Lock()
	lines := p.encoder.EncodeSentence(aisnmeaPacket(ch, ev.Payload))
	p.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range lines {
		f.conn.Write([]byte(l + "\r\n"))
	}
}

// ---- poll in ----

// aishubRow is one vessel in format=0 (AIS encoding): raw AIS field scales, TIME = epoch seconds as a string.
type aishubRow struct {
	MMSI      uint32
	Time      string `json:"TIME"`
	Longitude int32  `json:"LONGITUDE"` // 1/10000 min
	Latitude  int32  `json:"LATITUDE"`
	Cog       int32  `json:"COG"` // 1/10 deg, 3600 = n/a
	Sog       int32  `json:"SOG"` // 1/10 kn, 1023 = n/a
	Heading   uint16 `json:"HEADING"`
	Rot       int16  `json:"ROT"`
	NavStat   uint8  `json:"NAVSTAT"`
	PAC       uint8  `json:"PAC"` // position accuracy: 1 = high, better than 10 m
	IMO       uint32 `json:"IMO"`
	Name      string `json:"NAME"`
	CallSign  string `json:"CALLSIGN"`
	Type      uint8  `json:"TYPE"`
	A, B      uint16
	C, D      uint8
	Draught   uint16 `json:"DRAUGHT"` // 1/10 m
	Dest      string `json:"DEST"`
	Eta       uint32 `json:"ETA"` // packed month/day/hour/minute
}

func (r aishubRow) position(t time.Time) ais.Packet {
	if r.Rot > 127 || r.Rot < -128 { // AISHub reports ROT n/a as 128; AIS encodes it as -128
		r.Rot = -128
	}
	return ais.PositionReport{
		Header: ais.Header{MessageID: 1, UserID: r.MMSI}, Valid: true,
		NavigationalStatus: r.NavStat, RateOfTurn: r.Rot, Sog: ais.Field10(float64(r.Sog) / 10), PositionAccuracy: r.PAC == 1,
		Longitude: ais.FieldLatLonFine(float64(r.Longitude) / 600000), Latitude: ais.FieldLatLonFine(float64(r.Latitude) / 600000),
		Cog: ais.Field10(float64(r.Cog) / 10), TrueHeading: r.Heading, Timestamp: uint8(t.Second()),
	}
}

func (r aishubRow) static() ais.Packet {
	return ais.ShipStaticData{
		Header: ais.Header{MessageID: 5, UserID: r.MMSI}, Valid: true,
		ImoNumber: r.IMO, CallSign: r.CallSign, Name: r.Name, Type: r.Type,
		Dimension:            ais.FieldDimension{A: r.A, B: r.B, C: r.C, D: r.D},
		Eta:                  ais.FieldETA{Month: uint8(r.Eta >> 16 & 0xF), Day: uint8(r.Eta >> 11 & 0x1F), Hour: uint8(r.Eta >> 6 & 0x1F), Minute: uint8(r.Eta & 0x3F)},
		MaximumStaticDraught: ais.Field10(float64(r.Draught) / 10), Destination: r.Dest,
	}
}

// ingestAishub maps one snapshot into events: a position when TIME advanced, a static when static fields changed.
// Every row is ingested at once, as the raw archive records the snapshot, so the vessel cache, dedupe, and
// ClickHouse see the order replay reproduces. Live, only delivery to subscribers is paced (deliverPaced).
func (p *Pipeline) ingestAishub(body []byte, now time.Time) (int, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(body, &parts); err != nil {
		return 0, err
	}
	var rows []aishubRow
	for _, part := range parts {
		if len(part) > 0 && part[0] == '[' {
			if err := json.Unmarshal(part, &rows); err != nil {
				return 0, err
			}
			if shadowSample("aishub") {
				var rr []json.RawMessage
				if json.Unmarshal(part, &rr) == nil && len(rr) > 0 {
					shadowCheck("aishub", rr[0], aishubKnown)
				}
			}
		} else if len(part) > 0 && part[0] == '{' {
			var meta struct {
				Error   bool
				Message string `json:"ERROR_MESSAGE"`
			}
			json.Unmarshal(part, &meta)
			if meta.Error {
				return 0, fmt.Errorf("aishub: %s", meta.Message)
			}
		}
	}
	n := 0
	for _, r := range rows {
		if r.MMSI == 0 {
			p.stats.invalidMMSI.Add(1)
			continue
		}
		// A row's own TIME is the only thing that says whether it is news, so a row without one, or one
		// stamped in the future, is skipped. Capping a future stamp to the receive time would make the
		// same unchanged row look newer on every snapshot. The next snapshot carries it again, by then
		// in the past, and it goes through once with its true time.
		secs, err := strconv.ParseInt(r.Time, 10, 64)
		if err != nil || secs <= 0 {
			continue
		}
		t := time.Unix(secs, 0)
		if t.After(now) {
			continue
		}
		// A snapshot repeats every vessel AISHub holds, most of them unchanged since the last one, so a
		// row becomes an event only when the vessel cache says it is news. The cache survives a restart,
		// so the first snapshot after one does not re-send what the stream already carried.
		if r.Latitude != 0 && r.Longitude != 0 && p.positionIsNew(r.MMSI, t, now) {
			p.ingestPacketAt("aishub", "aishub", t, now, r.position(t))
			n++
		}
		if r.Name != "" || r.IMO != 0 {
			if pkt := r.static(); p.staticIsNew(r.MMSI, t, now, p.asDecoded(pkt)) {
				p.ingestPacketAt("aishub", "aishub", t, now, pkt)
				n++
			}
		}
	}
	return n, nil
}

// aishubSnapshot archives and ingests one snapshot as a single reception, then hands its events to
// paced delivery once the locks are released, so other sources never wait on delivery.
func (p *Pipeline) aishubSnapshot(body []byte, start time.Time) (int, error) {
	start, ok := p.admit(start)
	if !ok {
		return -1, nil
	}
	p.arch.write(Reception{Source: "aishub", Station: "aishub", RecvTime: start, Body: strings.TrimSpace(string(body))})
	n, err := p.ingestAishub(body, start)
	batch := p.aishubBatch
	p.aishubBatch = nil
	p.release()
	if p.aishubPace != nil && len(batch) > 0 {
		p.aishubPace <- batch
	}
	return n, err
}

// startAishubPacing sets up paced delivery of AISHub snapshots. main calls it before any source
// starts, so every emit sees it set.
func (p *Pipeline) startAishubPacing(budget time.Duration) {
	p.aishubPace = make(chan []*Event, 4)
	go p.deliverPaced(p.aishubPace, budget)
}

// deliverPaced broadcasts each snapshot's events spread evenly over budget. Emitted back to back, a
// ~22k-event snapshot overruns every subscriber's queue (a far client drains ~3k events/s) and uses up
// a rate-limited client's per-second allowance, so fresher reports from other sources in that second
// would be thinned; spread out, they are not. The rows are about a minute old already. Events from
// every other source broadcast as they are ingested, so they interleave with a snapshot being
// delivered rather than wait behind it.
func (p *Pipeline) deliverPaced(batches <-chan []*Event, budget time.Duration) {
	var pending []*Event
	var gap time.Duration
	next := time.NewTimer(0)
	for {
		if len(pending) == 0 {
			pending = <-batches
			gap = budget / time.Duration(len(pending))
			next.Reset(0)
		}
		select {
		case b := <-batches:
			// A snapshot arriving before the last is delivered joins the backlog, and the whole backlog is
			// re-spread over budget from now: nothing waits more than budget behind the newest snapshot,
			// and the fetch loop never blocks on delivery.
			pending = append(pending, b...)
			gap = budget / time.Duration(len(pending))
		case <-next.C:
			p.broadcast(pending[0])
			pending = pending[1:]
			if p.closing.Load() { // once shutdown starts, the rest goes out at once, while the archives drain
				gap = 0
			}
			next.Reset(gap)
		}
	}
}

// ingestPacketAt is ingestPacket for sources whose timestamps are trusted minutes back (AISHub rows carry the
// station's receive time, downsampled): the canonical time is the row's time even when it is older than the skew.
func (p *Pipeline) ingestPacketAt(source, station string, t, recv time.Time, pkt ais.Packet) {
	p.ingestPacket(source, station, t, recv, pkt)
}

func runAishub(p *Pipeline, username string, interval time.Duration) {
	url := "https://data.aishub.net/ws.php?username=" + username + "&format=0&output=json&compress=2"
	client := &http.Client{Timeout: 50 * time.Second}
	var lastHash [32]byte
	for {
		start := time.Now()
		n, err := func() (int, error) {
			res, err := client.Get(url)
			if err != nil {
				return 0, err
			}
			defer res.Body.Close()
			var rd io.Reader = res.Body
			if gz, err := gzip.NewReader(res.Body); err == nil { // body is a gzip file, not Content-Encoding
				rd = gz
			} else {
				return 0, fmt.Errorf("aishub: %s: not gzip (%v)", res.Status, err)
			}
			body, err := io.ReadAll(io.LimitReader(rd, 256<<20))
			if err != nil {
				return 0, err
			}
			// AISHub regenerates the world snapshot about once a minute and serves the same bytes in between
			if h := sha256.Sum256(body); h == lastHash {
				return -1, nil
			} else {
				lastHash = h
			}
			return p.aishubSnapshot(body, start)
		}()
		switch {
		case err != nil:
			log.Printf("aishub: %v", err)
		case n >= 0:
			log.Printf("aishub: %d events from new snapshot in %s", n, time.Since(start).Truncate(time.Millisecond))
		}
		// AISHub answers "Too frequent requests!" when polled faster than its limit (20 s for our account)
		time.Sleep(max(interval-time.Since(start), 10*time.Second))
	}
}
