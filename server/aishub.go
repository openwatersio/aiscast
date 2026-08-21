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

// AISHub is reciprocal: we feed them our deduplicated open-feed stream over UDP (their assigned port), and poll
// their aggregate snapshot (all stations, positions downsampled to ≤60 s) once a minute. Their terms grant "use"
// only, so this source is flagged in `source`/archive tags and can be switched off and purged; see PLAN.md.

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

// send re-encodes the event as plain !AIVDM (no TAG block, AI talker) so any aggregator accepts it. Synthesized
// events are skipped: AISHub wants receiver data, and re-feeding aisstream or their own snapshot would loop.
func (f *udpFeeder) send(p *Pipeline, ev *Event) {
	if ev.Synthesized || ev.Packet == nil {
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
		NavigationalStatus: r.NavStat, RateOfTurn: r.Rot, Sog: ais.Field10(float64(r.Sog) / 10),
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

func (r aishubRow) staticKey() string {
	return fmt.Sprintf("%d|%s|%s|%d|%d|%d|%d|%d|%d|%s|%d", r.IMO, r.Name, r.CallSign, r.Type, r.A, r.B, r.C, r.D, r.Draught, r.Dest, r.Eta)
}

type aishubState struct {
	lastTime   map[uint32]string // MMSI → TIME of the last position emitted
	lastStatic map[uint32]string // MMSI → staticKey of the last static emitted
}

// ingestAishub maps one snapshot into events: a position when TIME advanced, a static when static fields changed.
func (p *Pipeline) ingestAishub(body []byte, now time.Time, st *aishubState) (int, error) {
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
			continue
		}
		t := now
		if secs, err := strconv.ParseInt(r.Time, 10, 64); err == nil && secs > 0 {
			t = time.Unix(secs, 0)
		}
		if st.lastTime[r.MMSI] != r.Time && r.Latitude != 0 && r.Longitude != 0 {
			st.lastTime[r.MMSI] = r.Time
			p.ingestPacketAt("aishub", "aishub", t, r.position(t))
			n++
		}
		if k := r.staticKey(); (r.Name != "" || r.IMO != 0) && st.lastStatic[r.MMSI] != k {
			st.lastStatic[r.MMSI] = k
			p.ingestPacketAt("aishub", "aishub", t, r.static())
			n++
		}
	}
	return n, nil
}

// ingestPacketAt is ingestPacket for sources whose timestamps are trusted minutes back (AISHub rows carry the
// station's receive time, downsampled): the canonical time is the row's time even when it is older than the skew.
func (p *Pipeline) ingestPacketAt(source, station string, t time.Time, pkt ais.Packet) {
	p.ingestPacket(source, station, t, pkt)
}

func runAishub(p *Pipeline, username string, interval time.Duration) {
	url := "https://data.aishub.net/ws.php?username=" + username + "&format=0&output=json&compress=2"
	st := &aishubState{lastTime: map[uint32]string{}, lastStatic: map[uint32]string{}}
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
			// AISHub regenerates the world snapshot only every ~5 min and serves the same bytes in between
			if h := sha256.Sum256(body); h == lastHash {
				return -1, nil
			} else {
				lastHash = h
			}
			p.arch.write(Reception{Source: "aishub", Station: "aishub", RecvTime: start, Body: strings.TrimSpace(string(body))})
			return p.ingestAishub(body, start, st)
		}()
		switch {
		case err != nil:
			log.Printf("aishub: %v", err)
		case n >= 0:
			log.Printf("aishub: %d events from new snapshot in %s", n, time.Since(start).Truncate(time.Millisecond))
		}
		// never faster than once a minute: AISHub answers "Too frequent requests!" if polled more often
		time.Sleep(max(interval-time.Since(start), 10*time.Second))
	}
}
