package main

// Static data history: every distinct static state each source sent for each vessel each day, in statics, so a
// vessel's renames, destinations, and ETAs can be read back in order. The live writer adds a row for every static
// message, archive loads one for every distinct state in a file, and the vessel record seeds one per vessel once.

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"log"
	"maps"
	"math"
	"time"

	"github.com/BertoldVdb/go-ais"
	"github.com/ClickHouse/clickhouse-go/v2"
)

// chStatics keeps each source's distinct static states per vessel and day, with when each was first and last
// heard. A state is the fields one message carries, as sent, so a class B vessel's name (24A) and its type and
// call sign (24B) are two states, told apart by message. Min and max merge the same in any order and any number
// of times, so archive files loading newest first, reloads, retries, and the record's seed all leave the same rows.
// Kept as long as receptions are.
const chStatics = `CREATE TABLE IF NOT EXISTS {db}.statics (
	mmsi         UInt32,
	day          Date,
	source       LowCardinality(String),
	station      LowCardinality(String), -- a volunteer's station, so one can be purged alone; a feed's source
	message      LowCardinality(String), -- 5, 19, 24A, 24B, archive, or record
	name         String,
	callsign     String,
	imo          UInt32,
	ship_type    UInt8,
	to_bow       UInt16,
	to_stern     UInt16,
	to_port      UInt8,
	to_starboard UInt8,
	draught10    UInt16,
	destination  String,
	eta          String,                 -- MM-DD HH:MM as sent, the not-available values included; empty when the message has none
	first_ts     SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
	last_ts      SimpleAggregateFunction(max, DateTime64(3, 'UTC'))
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (mmsi, day, source, station, message, name, callsign, imo, ship_type, to_bow, to_stern, to_port, to_starboard, draught10, destination, eta)`

// chStaticsSeeded records that the record's seed finished, so a start after it does not read the record again.
const chStaticsSeeded = `CREATE TABLE IF NOT EXISTS {db}.statics_seeded (at DateTime64(3, 'UTC')) ENGINE = MergeTree ORDER BY at`

// maxStaticsPending bounds the static states waiting for ClickHouse. A flush a second takes a few hundred, and an
// AISHub snapshot tens of thousands at once, so the bound is for an outage, when unsent states stay in memory.
var maxStaticsPending = 100_000

// staticKey is one row of statics: a vessel's state from one source and message on one day.
type staticKey struct {
	mmsi                     uint32
	day                      int64 // unix days
	source, station, message string
	name, callsign           string
	imo                      uint32
	shipType                 uint8
	toBow, toStern           uint16
	toPort, toStarboard      uint8
	draught10                uint16
	destination, eta         string
}

// staticTimes is when a state was first and last heard.
type staticTimes struct{ first, last time.Time }

// staticsWriter writes static states.
type staticsWriter interface {
	insertStatics(ctx context.Context, rows map[staticKey]staticTimes) error
}

// staticOf is the state a static message carries, as sent, or false for any other message.
func staticOf(pkt ais.Packet) (staticKey, bool) {
	var k staticKey
	switch m := pkt.(type) {
	case ais.ShipStaticData:
		if !m.Valid {
			return k, false
		}
		k.message, k.name, k.callsign, k.imo, k.shipType = "5", m.Name, m.CallSign, m.ImoNumber, m.Type
		k.toBow, k.toStern, k.toPort, k.toStarboard = m.Dimension.A, m.Dimension.B, m.Dimension.C, m.Dimension.D
		k.draught10 = uint16(math.Round(float64(m.MaximumStaticDraught) * 10))
		k.destination = m.Destination
		k.eta = fmt.Sprintf("%02d-%02d %02d:%02d", m.Eta.Month, m.Eta.Day, m.Eta.Hour, m.Eta.Minute)
	case ais.ExtendedClassBPositionReport:
		if !m.Valid {
			return k, false
		}
		k.message, k.name, k.shipType = "19", m.Name, m.Type
		k.toBow, k.toStern, k.toPort, k.toStarboard = m.Dimension.A, m.Dimension.B, m.Dimension.C, m.Dimension.D
	case ais.StaticDataReport:
		if !m.Valid {
			return k, false
		}
		switch {
		case m.ReportA.Valid:
			k.message, k.name = "24A", m.ReportA.Name
		case m.ReportB.Valid:
			k.message, k.callsign, k.shipType = "24B", m.ReportB.CallSign, m.ReportB.ShipType
			d := m.ReportB.Dimension
			k.toBow, k.toStern, k.toPort, k.toStarboard = d.A, d.B, d.C, d.D
		default:
			return k, false
		}
	default:
		return k, false
	}
	return k, true
}

// noteStatic gathers a static message for statics, keeping each state's first and last time per vessel, day,
// source, and station until the next flush. Every copy counts, the one delivered first and the duplicates, each
// under its own source.
func (p *Pipeline) noteStatic(ev *Event) {
	p.noteStaticPacket(ev.Source, ev.Station, ev.Time, ev.RecvTime, ev.Packet)
}

// noteStaticPacket is noteStatic for a packet heard from source and station, stamped t and received at recv. A
// copy stamped a day or more before it arrived, or ahead of it, is a bad clock's, as receptions mark it, and
// would put a state on the wrong day.
func (p *Pipeline) noteStaticPacket(source, station string, t, recv time.Time, pkt ais.Packet) {
	if recv.Before(p.replayGate) {
		return // a replay's lead-in builds state and writes nothing
	}
	if recv.Sub(t) >= clockBadAge || t.After(recv.Add(maxSkew)) { // the bound receptions mark clock_bad at
		return
	}
	k, ok := staticOf(pkt)
	if !ok {
		return
	}
	k.mmsi, k.source, k.station = pkt.GetHeader().UserID, sourceKind(source), sourceKind(source)
	if volunteer(source) {
		k.station = station
	}
	t = t.UTC()
	k.day = t.Unix() / 86400
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if p.chStatics == nil {
		return
	}
	cur, ok := p.chStatics[k]
	if !ok && len(p.chStatics) >= maxStaticsPending {
		p.ch.staticsDropped.Add(1)
		return
	}
	if !ok || t.Before(cur.first) {
		cur.first = t
	}
	if t.After(cur.last) {
		cur.last = t
	}
	p.chStatics[k] = cur
}

// aishubStaticMoved reports whether a vessel's AISHub row carries a TIME later than the last one noted for statics.
// A snapshot repeats every vessel AISHub holds each minute, mostly unchanged, so only a new TIME is a new copy.
func (p *Pipeline) aishubStaticMoved(mmsi uint32, secs int64) bool {
	p.chMu.Lock()
	defer p.chMu.Unlock()
	if p.chStatics == nil {
		return false
	}
	if p.aishubStaticAt == nil {
		p.aishubStaticAt = map[uint32]int64{}
	}
	if secs <= p.aishubStaticAt[mmsi] {
		return false
	}
	p.aishubStaticAt[mmsi] = secs
	return true
}

// pruneAishubStatics forgets the AISHub TIMEs older than clockBadAge: a row that old is a bad clock's for statics
// anyway, and a vessel AISHub no longer carries would otherwise stay in the map for good.
func (p *Pipeline) pruneAishubStatics(now time.Time) {
	cutoff := now.Add(-clockBadAge).Unix()
	p.chMu.Lock()
	defer p.chMu.Unlock()
	maps.DeleteFunc(p.aishubStaticAt, func(_ uint32, secs int64) bool { return secs < cutoff })
}

// flushStatics writes the static states gathered since the last flush. A failed insert puts them back, merged
// with any heard meanwhile, to go with the next; min and max make a resend harmless.
func (p *Pipeline) flushStatics(c *chStore) error {
	if c.statics == nil {
		return nil
	}
	p.chMu.Lock()
	rows := p.chStatics
	if len(rows) > 0 {
		p.chStatics = map[staticKey]staticTimes{}
	}
	p.chMu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), chOwnInsertTimeout)
	err := c.statics.insertStatics(ctx, rows)
	cancel()
	if was := c.staticsFailing.Swap(err != nil); was != (err != nil) {
		if err != nil {
			log.Printf("clickhouse: statics: %v; static states wait for the next flush", err)
		} else {
			log.Printf("clickhouse: statics writing again")
		}
	}
	if err == nil {
		return nil
	}
	c.failures.Add(1)
	p.chMu.Lock()
	for k, t := range rows {
		cur, ok := p.chStatics[k]
		if !ok && len(p.chStatics) >= maxStaticsPending {
			c.staticsDropped.Add(1)
			continue
		}
		if !ok || t.first.Before(cur.first) {
			cur.first = t.first
		}
		if t.last.After(cur.last) {
			cur.last = t.last
		}
		p.chStatics[k] = cur
	}
	p.chMu.Unlock()
	return sideFailed{fmt.Errorf("statics: %w", err)}
}

func (c *chConn) insertStatics(ctx context.Context, rows map[staticKey]staticTimes) error {
	// A backlog from an outage can span many months, which are statics' partitions, as receptions' batches can.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"throw_on_max_partitions_per_insert_block": 0}))
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.db+"."+cmp.Or(c.statics, "statics")+" (mmsi, day, source, station, message, name, callsign, imo, ship_type, "+
		"to_bow, to_stern, to_port, to_starboard, draught10, destination, eta, first_ts, last_ts)")
	if err != nil {
		return err
	}
	for k, t := range rows {
		if err := batch.Append(k.mmsi, time.Unix(k.day*86400, 0).UTC(), k.source, k.station, k.message, k.name, k.callsign, k.imo, k.shipType,
			k.toBow, k.toStern, k.toPort, k.toStarboard, k.draught10, k.destination, k.eta, t.first, t.last); err != nil {
			batch.Abort()
			return err
		}
	}
	return batch.Send()
}

// seedStatics gives every vessel in the record one row with its current state, timed at the record's static time,
// or its last seen where it has none, with the source record, once: statics_seeded says it is done.
func (c *chConn) seedStatics(ctx context.Context, record *sql.DB) error {
	done, err := chColumn[uint64](ctx, c.conn, "SELECT count() FROM "+c.db+".statics_seeded")
	if err != nil || done[0] > 0 {
		return err
	}
	rows, err := record.QueryContext(ctx, `SELECT mmsi, name, callsign, imo, ship_type, to_bow, to_stern, to_port, to_starboard, draught,
		destination, eta, static_at, seen FROM vessels
		WHERE kind = 'vessel' AND (name != '' OR callsign != '' OR imo != 0 OR ship_type != 0 OR destination != '' OR eta != 0 OR draught != 0
			OR to_bow + to_stern + to_port + to_starboard != 0)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	batch := map[staticKey]staticTimes{}
	n := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := c.insertStatics(ctx, batch)
		n += len(batch)
		clear(batch)
		return err
	}
	for rows.Next() {
		var k staticKey
		var draught float64
		var eta uint32
		var staticAt, seen int64
		if err := rows.Scan(&k.mmsi, &k.name, &k.callsign, &k.imo, &k.shipType, &k.toBow, &k.toStern, &k.toPort, &k.toStarboard, &draught,
			&k.destination, &eta, &staticAt, &seen); err != nil {
			return err
		}
		if !validMMSI(k.mmsi) {
			continue
		}
		k.source, k.station, k.message = "record", "record", "record"
		k.draught10 = uint16(math.Round(draught * 10))
		if eta != 0 {
			k.eta = fmt.Sprintf("%02d-%02d %02d:%02d", eta>>24, eta>>16&0xff, eta>>8&0xff, eta&0xff)
		}
		at := time.UnixMilli(staticAt).UTC()
		if staticAt == 0 {
			at = time.UnixMilli(seen).UTC()
		}
		k.day = at.Unix() / 86400
		batch[k] = staticTimes{at, at}
		if len(batch) >= 50_000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	log.Printf("statics: seeded %d vessels from the record", n)
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".statics_seeded VALUES (now64(3))")
}

// runStatics seeds statics from the record once ClickHouse connects, retrying each minute until it has.
func (p *Pipeline) runStatics() {
	ctx := context.Background()
	for {
		p.vmu.RLock()
		var c *chConn
		if p.ch != nil {
			c, _ = p.ch.statics.(*chConn)
		}
		p.vmu.RUnlock()
		if p.store == nil {
			return // without the record there is nothing to seed
		}
		if c != nil {
			sctx, cancel := context.WithTimeout(ctx, 30*time.Minute) // a stalled server must not hold the record's read open
			err := c.seedStatics(sctx, p.store.db)
			cancel()
			if err == nil {
				return
			} else {
				log.Printf("statics: seed: %v", err)
			}
		}
		time.Sleep(time.Minute)
	}
}
