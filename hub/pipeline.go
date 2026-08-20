package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/BertoldVdb/go-ais/aisnmea"
	nmea "github.com/adrianmo/go-nmea"
)

// Reception is one thing received, exactly as received. Immutable; archived before any processing.
type Reception struct {
	Source     string // kystverket, http:<id>, udp:<ip>, v1:<id>
	Station    string // refined by TAG s: when present
	RecvTime   time.Time
	SourceTime time.Time // feeder-supplied, zero if none
	Body       string
}

// Event is one decoded AIS message after reassembly and dedupe.
type Event struct {
	ID          string
	Time        time.Time // canonical: validated source time, else receive time
	Source      string
	Station     string
	Channel     byte // 'A' or 'B'
	Payload     []byte
	Packet      ais.Packet
	Type        string // go-ais struct name == aisstream MessageType
	MMSI        uint32
	Name        string // from vessel cache, untrimmed
	Lat, Lon    float64
	HasPos      bool
	Sentences   []string
	Synthesized bool

	v0Once sync.Once
	v0     []byte
}

type subscriber struct {
	ch       chan *Event
	overflow atomic.Bool
}

type Pipeline struct {
	arch  *archive
	codec *ais.Codec

	mu      sync.Mutex         // ponytail: one lock around parse+dedupe; shard per station if it shows up in profiles
	encoder *aisnmea.NMEACodec // for synthesized events; has its own sequence counter
	codecs  map[string]*aisnmea.NMEACodec
	pending map[string][]string // per-station fragment lines awaiting assembly
	seen    map[string]time.Time
	nSeen   int

	vmu     sync.RWMutex
	vessels map[uint32]*vessel

	smu  sync.RWMutex
	subs map[*subscriber]struct{}

	upstreams    []string   // configured upstream source names; /health watches them
	feeder       *udpFeeder // optional: forward received (non-synthesized) events to an aggregator
	last         atomic.Int64
	lastBySource sync.Map // source → time.Time of last event; /health and /metrics read it
	stats        struct {
		parseErr, decodeFail, dup, events, clientDrops, rateLimited atomic.Int64
		bySource                                                    sync.Map // source → *counterT
	}
}

func newPipeline(arch *archive) *Pipeline {
	c := ais.CodecNewFast(false, false, true) // reflection codec is ~4× slower
	c.DropSpace = true
	return &Pipeline{
		arch: arch, codec: c,
		encoder: aisnmea.NMEACodecNew(c),
		codecs:  map[string]*aisnmea.NMEACodec{},
		pending: map[string][]string{},
		seen:    map[string]time.Time{},
		vessels: map[uint32]*vessel{},
		subs:    map[*subscriber]struct{}{},
	}
}

func (p *Pipeline) lastEvent() time.Time { return time.Unix(0, p.last.Load()) }

type counterT = atomic.Int64

func (p *Pipeline) touch(source string) {
	p.lastBySource.Store(source, time.Now())
	c, _ := p.stats.bySource.LoadOrStore(source, new(counterT))
	c.(*counterT).Add(1)
}

// sourceAge returns how long since source last produced an event (or since boot if never).
func (p *Pipeline) sourceAge(source string) time.Duration {
	if v, ok := p.lastBySource.Load(source); ok {
		return time.Since(v.(time.Time))
	}
	return time.Since(bootTime)
}

var bootTime = time.Now()

// Ingest archives a reception and feeds it to the pipeline.
func (p *Pipeline) Ingest(rx Reception) {
	p.arch.write(rx)
	p.ingestLine(rx)
}

// ingestLine parses one NMEA sentence (callers archive separately when the body isn't line-per-sentence).
func (p *Pipeline) ingestLine(rx Reception) {
	line := strings.TrimSpace(rx.Body)
	if line == "" {
		return
	}
	line = stripTrailingFields(line)
	s, err := nmea.Parse(line)
	if err != nil {
		p.stats.parseErr.Add(1)
		return
	}
	vdm, ok := s.(nmea.VDMVDO)
	if !ok {
		return
	}
	station := rx.Station
	if vdm.TagBlock.Source != "" {
		station = rx.Source + "/" + vdm.TagBlock.Source
	}

	p.mu.Lock()
	nc := p.codecs[station]
	if nc == nil {
		nc = aisnmea.NMEACodecNew(p.codec)
		p.codecs[station] = nc
	}
	var sentences []string
	if vdm.NumFragments > 1 {
		// ponytail: assumes one multipart sequence in flight per station; interleaved sequences mix their sentence lists
		p.pending[station] = append(p.pending[station], line)
	}
	pkt, err := nc.ParseVDMVDO(&vdm)
	if pkt != nil {
		if vdm.NumFragments > 1 {
			sentences, p.pending[station] = p.pending[station], nil
		} else {
			sentences = []string{line}
		}
	}
	p.mu.Unlock()
	if err != nil || pkt == nil {
		return
	}
	if pkt.Packet == nil {
		p.stats.decodeFail.Add(1)
		return
	}

	t := rx.RecvTime
	if st := tagTime(vdm.TagBlock.Time); !st.IsZero() && absDur(st.Sub(t)) <= maxSkew {
		t = st
	} else if !rx.SourceTime.IsZero() && absDur(rx.SourceTime.Sub(t)) <= maxSkew {
		t = rx.SourceTime
	}
	ch := byte('A')
	if pkt.Channel == 2 {
		ch = 'B'
	}
	p.emit(&Event{Time: t, Source: rx.Source, Station: station, Channel: ch, Payload: pkt.Payload, Packet: pkt.Packet, Sentences: sentences})
}

// ingestPacket takes an already-decoded message from a non-NMEA source (Digitraffic JSON, a peer's structs).
// The packet is re-encoded so dedupe, the event id, and the /v1 `nmea` field work exactly as for VHF receptions,
// and decoded again so the struct is what a receiver would have produced (quantized lat/lon, sentinels).
func (p *Pipeline) ingestPacket(source, station string, t time.Time, pkt ais.Packet) {
	payload := p.codec.EncodePacket(pkt)
	if payload == nil {
		p.stats.decodeFail.Add(1)
		return
	}
	decoded := p.codec.DecodePacket(payload)
	if decoded == nil {
		p.stats.decodeFail.Add(1)
		return
	}
	p.mu.Lock()
	sentences := p.encoder.EncodeSentence(aisnmeaPacket('A', payload))
	p.mu.Unlock()
	p.emit(&Event{Time: t, Source: source, Station: station, Payload: payload, Packet: decoded, Sentences: sentences, Synthesized: true})
}

// aisnmeaPacket builds a VdmPacket for EncodeSentence, which wants the channel as 1/2, not 'A'/'B'.
func aisnmeaPacket(channel byte, payload []byte) aisnmea.VdmPacket {
	ch := byte(1)
	if channel == 'B' || channel == 2 {
		ch = 2
	}
	return aisnmea.VdmPacket{Channel: ch, TalkerID: "AI", MessageType: "VDM", Payload: payload}
}

// emit is the common tail: dedupe on (payload, channel) within the window, then id, vessel cache, fan-out.
func (p *Pipeline) emit(ev *Event) {
	key := string(ev.Payload) + string(ev.Channel)
	p.mu.Lock()
	if prev, ok := p.seen[key]; ok && absDur(ev.Time.Sub(prev)) < dedupeWindow {
		p.mu.Unlock()
		p.stats.dup.Add(1)
		return
	}
	p.seen[key] = ev.Time
	p.nSeen++
	if p.nSeen%4096 == 0 {
		cutoff := time.Now().Add(-6 * dedupeWindow) // generous: canonical times can skew from wall clock
		for k, v := range p.seen {
			if v.Before(cutoff) {
				delete(p.seen, k)
			}
		}
	}
	p.mu.Unlock()

	sum := sha256.Sum256([]byte(key))
	ev.ID = hex.EncodeToString(sum[:16])
	ev.Type = typeName(ev.Packet)
	ev.MMSI = ev.Packet.GetHeader().UserID
	p.updateVessel(ev)
	p.stats.events.Add(1)
	p.last.Store(time.Now().UnixNano())
	p.touch(ev.Source)
	p.broadcast(ev)
}

// stripTrailingFields drops USCG-style fields after the checksum (`…,0*5B,s36310,d-081,T59.0`), which
// go-nmea would otherwise reject as a checksum mismatch.
func stripTrailingFields(line string) string {
	i := strings.LastIndexAny(line, "!$")
	if i < 0 {
		return line
	}
	if j := strings.IndexByte(line[i:], '*'); j >= 0 && len(line) > i+j+3 && line[i+j+3] == ',' {
		return line[:i+j+3]
	}
	return line
}

const (
	maxSkew      = 30 * time.Second
	dedupeWindow = 10 * time.Second
)

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// tagTime interprets a TAG block c: value; the unit is unspecified in practice (s, ms, µs all seen).
func tagTime(v int64) time.Time {
	switch {
	case v <= 0:
		return time.Time{}
	case v < 1e12:
		return time.Unix(v, 0)
	case v < 1e15:
		return time.UnixMilli(v)
	default:
		return time.UnixMicro(v)
	}
}

// typeName is the aisstream MessageType: go-ais struct names, except where aisstream's documented enum
// spells a name differently from go-ais (unverified against live aisstream output; see golden tests).
func typeName(p ais.Packet) string {
	t := reflect.TypeOf(p)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if n, ok := typeNameOverride[t.Name()]; ok {
		return n
	}
	return t.Name()
}

var typeNameOverride = map[string]string{"AddessedSafetyMessage": "AddressedSafetyMessage"}

func (p *Pipeline) subscribe() *subscriber {
	s := &subscriber{ch: make(chan *Event, 1024)}
	p.smu.Lock()
	p.subs[s] = struct{}{}
	p.smu.Unlock()
	return s
}

func (p *Pipeline) unsubscribe(s *subscriber) {
	p.smu.Lock()
	delete(p.subs, s)
	p.smu.Unlock()
}

func (p *Pipeline) broadcast(ev *Event) {
	if p.feeder != nil {
		p.feeder.send(p, ev)
	}
	p.smu.RLock()
	for s := range p.subs {
		select {
		case s.ch <- ev:
		default:
			s.overflow.Store(true) // slow client: its writer disconnects it
			p.stats.clientDrops.Add(1)
		}
	}
	p.smu.RUnlock()
}

func (p *Pipeline) logStats() {
	for range time.Tick(30 * time.Second) {
		nv := p.sweepVessels(time.Now().Add(-vesselTTL))
		p.smu.RLock()
		ns := len(p.subs)
		p.smu.RUnlock()
		log.Printf("events=%d dup=%d parse_err=%d decode_fail=%d vessels=%d subs=%d",
			p.stats.events.Load(), p.stats.dup.Load(), p.stats.parseErr.Load(), p.stats.decodeFail.Load(), nv, ns)
	}
}
