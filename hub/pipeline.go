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

	mu      sync.Mutex // ponytail: one lock around parse+dedupe; shard per station if it shows up in profiles
	codecs  map[string]*aisnmea.NMEACodec
	pending map[string][]string // per-station fragment lines awaiting assembly
	seen    map[string]time.Time
	nSeen   int

	vmu     sync.RWMutex
	vessels map[uint32]*vessel

	smu  sync.RWMutex
	subs map[*subscriber]struct{}

	last  atomic.Int64 // unix nanos of last event
	stats struct{ parseErr, decodeFail, dup, events atomic.Int64 }
}

func newPipeline(arch *archive) *Pipeline {
	c := ais.CodecNewFast(false, false, true) // reflection codec is ~4× slower
	c.DropSpace = true
	return &Pipeline{
		arch: arch, codec: c,
		codecs:  map[string]*aisnmea.NMEACodec{},
		pending: map[string][]string{},
		seen:    map[string]time.Time{},
		vessels: map[uint32]*vessel{},
		subs:    map[*subscriber]struct{}{},
	}
}

func (p *Pipeline) lastEvent() time.Time { return time.Unix(0, p.last.Load()) }

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

	key := string(pkt.Payload) + string(pkt.Channel)
	p.mu.Lock()
	if prev, ok := p.seen[key]; ok && absDur(t.Sub(prev)) < dedupeWindow {
		p.mu.Unlock()
		p.stats.dup.Add(1)
		return
	}
	p.seen[key] = t
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

	ch := byte('A')
	if pkt.Channel == 2 {
		ch = 'B'
	}
	sum := sha256.Sum256([]byte(key))
	ev := &Event{
		ID: hex.EncodeToString(sum[:16]), Time: t, Source: rx.Source, Station: station,
		Channel: ch, Payload: pkt.Payload, Packet: pkt.Packet, Type: typeName(pkt.Packet),
		MMSI: pkt.Packet.GetHeader().UserID, Sentences: sentences,
	}
	p.updateVessel(ev)
	p.stats.events.Add(1)
	p.last.Store(time.Now().UnixNano())
	p.broadcast(ev)
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

func typeName(p ais.Packet) string {
	t := reflect.TypeOf(p)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

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
	p.smu.RLock()
	for s := range p.subs {
		select {
		case s.ch <- ev:
		default:
			s.overflow.Store(true) // slow client: its writer disconnects it
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
