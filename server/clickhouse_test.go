package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCH records every batch and token it is handed, and fails the first `fail` of them.
type fakeCH struct {
	mu      sync.Mutex
	fail    int
	tokens  []string
	batches [][]trackPoint
}

func (f *fakeCH) insert(_ context.Context, token string, points []trackPoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	f.batches = append(f.batches, append([]trackPoint(nil), points...))
	if f.fail > 0 {
		f.fail--
		return errors.New("clickhouse is down")
	}
	return nil
}

func ingestAt(p *Pipeline, mmsi uint32, at time.Time, lat float64) {
	p.ingestPacket("kystverket", "kystverket", at, at, posReport(mmsi, lat, 10.7))
}

func TestClickHouseSendsAFailedBatchAgainUnderItsToken(t *testing.T) {
	p := testPipeline(t)
	f := &fakeCH{fail: 1}
	p.attachClickHouse(&chStore{w: f})
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	ingestAt(p, 257000001, start, 59.90)
	if err := p.flushClickHouse(); err == nil {
		t.Fatal("the first batch should fail")
	}
	ingestAt(p, 257000001, start.Add(time.Minute), 59.91)
	for range 2 {
		if err := p.flushClickHouse(); err != nil {
			t.Fatal(err)
		}
	}
	// A failure can follow a commit, so the batch goes again exactly as it was, for ClickHouse to recognize.
	if len(f.batches) != 3 || len(f.batches[1]) != 1 || !f.batches[1][0].ts.Equal(start) || f.tokens[1] != f.tokens[0] {
		t.Fatalf("the failed batch goes again alone under its token: %v %+v", f.tokens, f.batches)
	}
	if len(f.batches[2]) != 1 || !f.batches[2][0].ts.Equal(start.Add(time.Minute)) || f.tokens[2] == f.tokens[0] {
		t.Fatalf("what queued behind it follows under a new token: %v %+v", f.tokens, f.batches)
	}
	if p.ch.failures.Load() != 1 || p.ch.written.Load() != 2 {
		t.Errorf("failures %d written %d", p.ch.failures.Load(), p.ch.written.Load())
	}
}

func TestClickHouseDropsWhatItsQueueCannotHold(t *testing.T) {
	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: &fakeCH{}})
	p.vmu.Lock()
	p.chQueue = make([]trackPoint, maxPending)
	p.vmu.Unlock()
	ingestAt(p, 257000001, time.Now(), 59.9)
	if p.ch.dropped.Load() != 1 {
		t.Errorf("a full queue drops the newest position and counts it: %d", p.ch.dropped.Load())
	}
}

// TestClickHouseWritesPositionsAndRollups runs against a real server named by CLICKHOUSE_TEST_URL, such as
// clickhouse://127.0.0.1:9000, in a database of its own that it drops afterward.
func TestClickHouseWritesPositionsAndRollups(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL is not set")
	}
	db := fmt.Sprintf("aiscast_test_%d", time.Now().UnixNano())
	conn, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.conn.Exec(context.Background(), "DROP DATABASE "+db); conn.conn.Close() })
	if _, err := openClickHouse(context.Background(), strings.TrimRight(url, "/")+"/"+db); err != nil {
		t.Fatalf("the schema applies again over itself: %v", err)
	}

	p := testPipeline(t)
	p.attachClickHouse(&chStore{w: conn})
	slot := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	ingestAt(p, 257000001, slot.Add(2*time.Minute), 59.90)
	ingestAt(p, 257000001, slot.Add(9*time.Minute), 59.91)
	if err := p.flushClickHouse(); err != nil {
		t.Fatal(err)
	}
	// The pipeline withholds a report older than the vessel's last as stale, but history loaded later from the
	// lake arrives out of order: an earlier position written after the others still wins its window.
	early := trackPoint{mmsi: 257000001, ts: slot.Add(time.Minute), lat6: int32(59.89 * 600000), lon6: int32(10.7 * 600000),
		sog10: 1023, cog10: 3600, heading: 511, navStatus: 15, source: "kystverket"}
	if err := conn.insert(context.Background(), "early", []trackPoint{early}); err != nil {
		t.Fatal(err)
	}
	// The same batch under the same token, as after an insert that failed once ClickHouse had committed it.
	if err := conn.insert(context.Background(), "early", []trackPoint{early}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var n uint64
	if err := conn.conn.QueryRow(ctx, "SELECT count() FROM "+db+".positions WHERE mmsi = 257000001").Scan(&n); err != nil || n != 3 {
		t.Fatalf("every position: %d %v", n, err)
	}
	for _, rollup := range []string{"positions_15m", "positions_1h"} {
		var first time.Time
		var lat6 int32
		q := "SELECT tupleElement(argMinMerge(first), 1), tupleElement(argMinMerge(first), 2) FROM " + db + "." + rollup + " WHERE mmsi = 257000001"
		if err := conn.conn.QueryRow(ctx, q).Scan(&first, &lat6); err != nil {
			t.Fatal(rollup, err)
		}
		if !first.Equal(slot.Add(time.Minute)) || lat6 != int32(59.89*600000) {
			t.Errorf("%s keeps the earliest position in its window: %v %d", rollup, first, lat6)
		}
	}
}

// slowCH holds each insert open a while and records the most inserts it ever saw in flight.
type slowCH struct {
	mu            sync.Mutex
	inFlight, max int
}

func (s *slowCH) insert(context.Context, string, []trackPoint) error {
	s.mu.Lock()
	s.inFlight++
	s.max = max(s.max, s.inFlight)
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return nil
}

func TestClickHouseFlushesOneAtATime(t *testing.T) {
	// The writer's tick and the shutdown flush can call at once; a second insert must wait for the first.
	p := testPipeline(t)
	s := &slowCH{}
	p.attachClickHouse(&chStore{w: s})
	var wg sync.WaitGroup
	for i := range 4 {
		ingestAt(p, 257000001, time.Now().Add(time.Duration(i)*time.Second), 59.9)
		wg.Go(func() { p.flushClickHouse() })
	}
	wg.Wait()
	if s.max != 1 {
		t.Errorf("%d inserts in flight at once", s.max)
	}
}
