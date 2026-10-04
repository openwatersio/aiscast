package main

// aiscast convert-receptions: rewrite the rows receptions holds from its first layout, named by tx and arriving
// at recv_ts, into the current one, a day at a time, in place. It runs the live writer's own code, so a converted
// row is the row the server would have written: the same transmission name, and the same anchor deciding
// whether a vessel was moving, which a single SQL statement cannot carry through a vessel's day. Rerunning it is
// safe: each finished day is recorded in receptions_converted and skipped, and a day it stopped partway through
// is deleted and done again. A converted row keeps its recv_ts, which marks it as converted until cleanup.
//
// aiscast clickhouse-cleanup: the steps that drop data, run by hand, never at start, and only once every day is
// converted. It drops positions_15m and positions_1h, which positions_1m replaces, and positions_old, the table
// before receptions, then deletes the rows in the first layout and drops tx and recv_ts. After that a server
// built before the current layout can no longer write.

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// convertBatch is how many rows go to ClickHouse in one insert. The insert borrows its own connection from the
// pool while the day's read holds another open, so batches go out as the read streams.
var convertBatch = 200_000

func chFromEnv(cmd string) (*chConn, context.Context) {
	url := os.Getenv("CLICKHOUSE_URL")
	if url == "" {
		log.Fatalf("%s: CLICKHOUSE_URL is not set", cmd)
	}
	ctx := context.Background()
	// A command's statements rewrite a day or the whole table, past the five minutes a server's query gets.
	c, err := openClickHouse(ctx, url, func(o *clickhouse.Options) { o.ReadTimeout = 6 * time.Hour })
	if err != nil {
		log.Fatalf("%s: %v", cmd, err)
	}
	return c, ctx
}

func runConvertReceptions(args []string) {
	fset := flag.NewFlagSet("convert-receptions", flag.ExitOnError)
	fromS := fset.String("from", "", "first day to convert, YYYY-MM-DD UTC (default: the first with rows to convert)")
	toS := fset.String("to", "", "last day to convert, YYYY-MM-DD UTC (default: the last)")
	fset.Parse(args)
	c, ctx := chFromEnv("convert-receptions")
	defer c.conn.Close()
	days, err := c.unconverted(ctx)
	if err != nil {
		log.Fatalf("convert-receptions: %v", err)
	}
	days = slices.DeleteFunc(days, func(d time.Time) bool {
		ds := d.Format("2006-01-02")
		return *fromS != "" && ds < *fromS || *toS != "" && ds > *toS
	})
	if err := c.convertDays(ctx, days, func(d time.Time, n int, took time.Duration) {
		fmt.Printf("%s %d rows in %s\n", d.Format("2006-01-02"), n, took.Round(time.Second))
	}); err != nil {
		log.Fatalf("convert-receptions: %v", err)
	}
}

// convertDays converts days in order, carrying each vessel's anchor from one day to the next. A day the run did
// not just convert the day before of, the first of a run or one a rollback left to do again, starts from the
// anchors positions_1m holds, so it judges moving as the day after a converted one would.
func (c *chConn) convertDays(ctx context.Context, days []time.Time, done func(time.Time, int, time.Duration)) error {
	anchors := map[uint32]*anchor{}
	var prev time.Time
	for _, d := range days {
		start := time.Now()
		if !d.Equal(prev.AddDate(0, 0, 1)) {
			var err error
			if anchors, err = c.anchorsBefore(ctx, d); err != nil {
				return fmt.Errorf("%s: anchors: %w", d.Format("2006-01-02"), err)
			}
		}
		n, err := c.convertDay(ctx, d, anchors)
		if err != nil {
			return fmt.Errorf("%s: %w", d.Format("2006-01-02"), err)
		}
		done(d, n, time.Since(start))
		prev = d
	}
	return nil
}

// anchorsBefore is each vessel's anchor at the start of day: where it was last moving, its latest moving row in
// positions_1m. It looks back a month; a vessel still for longer starts without one, and its first report that
// day counts as moving, a row a minute more.
func (c *chConn) anchorsBefore(ctx context.Context, day time.Time) (map[uint32]*anchor, error) {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"max_memory_usage": 1_500_000_000, "max_threads": 2}))
	rows, err := c.conn.Query(ctx, "SELECT mmsi, a.1, a.2 FROM (SELECT mmsi, argMax((lat6, lon6), ts) AS a FROM "+c.db+".positions_1m"+
		" WHERE cell = 0 AND slot >= ? AND slot < ? GROUP BY mmsi)", day.AddDate(0, 0, -31), day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anchors := map[uint32]*anchor{}
	for rows.Next() {
		var mmsi uint32
		a := &anchor{set: true}
		if err := rows.Scan(&mmsi, &a.lat6, &a.lon6); err != nil {
			return nil, err
		}
		anchors[mmsi] = a
	}
	return anchors, rows.Err()
}

// unconverted is every day with rows in the first layout that receptions_converted does not record as
// converted with as many rows as it has now, in order: a server rolled back to before the current layout writes
// such rows into a day the converter has done.
func (c *chConn) unconverted(ctx context.Context) ([]time.Time, error) {
	if legacy, err := c.legacy(ctx); err != nil || !legacy {
		return nil, err
	}
	return chColumn[time.Time](ctx, c.conn, "SELECT toDateTime(o.d, 'UTC') FROM (SELECT toDate(ts) AS d, count() AS n FROM "+c.db+".receptions"+
		" WHERE tx != 0 GROUP BY d) AS o LEFT JOIN (SELECT day, rows FROM "+c.db+".receptions_converted FINAL) AS c ON o.d = c.day"+
		" WHERE c.rows != o.n ORDER BY o.d")
}

// aiscast rebuild-positions-1m -from -to: rebuild positions_1m for each day, after a source is purged from
// receptions or an archive day reloaded.
func runRebuildPositions1m(args []string) {
	fset := flag.NewFlagSet("rebuild-positions-1m", flag.ExitOnError)
	fromS := fset.String("from", "", "first day, YYYY-MM-DD UTC (required)")
	toS := fset.String("to", "", "last day, YYYY-MM-DD UTC (required)")
	fset.Parse(args)
	from, err1 := time.ParseInLocation("2006-01-02", *fromS, time.UTC)
	to, err2 := time.ParseInLocation("2006-01-02", *toS, time.UTC)
	if err1 != nil || err2 != nil || to.Before(from) {
		log.Fatalf("rebuild-positions-1m: -from and -to must be YYYY-MM-DD with from <= to")
	}
	c, ctx := chFromEnv("rebuild-positions-1m")
	defer c.conn.Close()
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		start := time.Now()
		if err := c.rebuildPositions1m(ctx, d); err != nil {
			log.Fatalf("rebuild-positions-1m: %s: %v", d.Format("2006-01-02"), err)
		}
		fmt.Printf("%s in %s\n", d.Format("2006-01-02"), time.Since(start).Round(time.Second))
	}
}

func runClickHouseCleanup() {
	c, ctx := chFromEnv("clickhouse-cleanup")
	defer c.conn.Close()
	if err := c.cleanup(ctx, func(msg string) { fmt.Println(msg) }); err != nil {
		log.Fatalf("clickhouse-cleanup: %v", err)
	}
}

// cleanup drops what the current layout no longer reads, saying each step as it goes.
func (c *chConn) cleanup(ctx context.Context, say func(string)) error {
	// Nothing goes while a day is left to convert: the rollups and positions_old are what a server rolled back
	// to the first layout reads.
	days, err := c.unconverted(ctx)
	if err != nil {
		return err
	}
	if len(days) > 0 {
		say(fmt.Sprintf("%d days from %s still to convert; run aiscast convert-receptions, then this again. Nothing was dropped",
			len(days), days[0].Format("2006-01-02")))
		return nil
	}
	say("dropping positions_15m, positions_1h, and their views, which positions_1m replaces")
	if err := c.exec(ctx, "DROP VIEW IF EXISTS {db}.positions_15m_mv", "DROP VIEW IF EXISTS {db}.positions_1h_mv",
		"DROP TABLE IF EXISTS {db}.positions_15m", "DROP TABLE IF EXISTS {db}.positions_1h"); err != nil {
		return err
	}
	say("dropping positions_old, the table before receptions")
	if err := c.exec(ctx, "DROP TABLE IF EXISTS {db}.positions_old"); err != nil {
		return err
	}
	if legacy, err := c.legacy(ctx); err != nil || !legacy {
		return err
	}
	// The rows go first, and at once, so the view that no longer knows tx never sees one; the columns then
	// go in the background, and system.mutations shows when.
	say("deleting rows in the first layout, then dropping tx and recv_ts in the background")
	if err := c.exec(clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"max_execution_time": 0})),
		"DELETE FROM {db}.receptions WHERE tx != 0"); err != nil {
		return err
	}
	return c.exec(ctx, chPositionsView(false), "ALTER TABLE {db}.receptions DROP COLUMN tx, DROP COLUMN recv_ts",
		"DROP TABLE {db}.receptions_converted")
}

// v1Row is a row in the first layout.
type v1Row struct {
	mmsi                                          uint32
	ts, recv                                      time.Time
	tx                                            uint64
	lat6, lon6                                    int32
	sog10, cog10, heading                         uint16
	navstat                                       uint8
	source, station                               string
	accepted, corroborated, implausible, clockBad bool
}

// convertDay converts one UTC day and reports the rows written. A v1 tx was the event id's first 64 bits XORed
// with the accepted copy's time in milliseconds, so that time recovers the id's byte exactly, and a transmission
// whose copies straddle midnight has one name. Its copies sit within 5 minutes of it, so the day is read with
// 10 minutes either side; only its own rows are written. anchors carries each vessel's anchor from day to day.
func (c *chConn) convertDay(ctx context.Context, day time.Time, anchors map[uint32]*anchor) (int, error) {
	end := day.Add(24 * time.Hour)
	partial := " FROM " + c.db + ".receptions WHERE ts >= ? AND ts < ? AND tx = 0 AND recv_ts != 0"
	var left, old uint64
	if err := c.conn.QueryRow(ctx, "SELECT countIf(tx = 0 AND recv_ts != 0), countIf(tx != 0) FROM "+c.db+".receptions WHERE ts >= ? AND ts < ?", day, end).
		Scan(&left, &old); err != nil {
		return 0, err
	}
	if left > 0 { // a run that stopped partway through the day, or a day with rows written since it was done
		if err := c.conn.Exec(ctx, "DELETE"+partial, day, end); err != nil {
			return 0, err
		}
	}
	rctx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_memory_usage": 1_500_000_000, "max_threads": 2, "optimize_read_in_order": 1, "max_execution_time": 3600,
	}))
	rows, err := c.conn.Query(rctx, "SELECT mmsi, ts, recv_ts, tx, lat6, lon6, sog10, cog10, heading, navstat, toString(source), toString(station),"+
		" accepted, corroborated, implausible, clock_bad FROM "+c.db+".receptions WHERE tx != 0 AND ts >= ? AND ts < ? ORDER BY mmsi, ts, recv_ts, tx",
		day.Add(-10*time.Minute), end.Add(10*time.Minute))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	// Each run's batches carry a token of their own. receptions skips a block it holds under the same token, or
	// with the same rows and no token, and a rerun writes the rows a deleted partial run wrote, which ClickHouse
	// 26.8 skips even with insert_deduplicate off.
	run := fmt.Sprintf("convert-%s-%d-", day.Format("2006-01-02"), time.Now().UnixNano())
	var batch []trackPoint
	written, batches := 0, 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batches++
		if err := c.write(ctx, run+fmt.Sprint(batches), batch, true); err != nil {
			return err
		}
		written += len(batch)
		batch = batch[:0]
		return nil
	}
	var vessel []v1Row
	emit := func() error {
		if len(vessel) == 0 {
			return nil
		}
		batch = append(batch, convertVessel(vessel, day, end, anchors)...)
		vessel = vessel[:0]
		if len(batch) >= convertBatch {
			return flush()
		}
		return nil
	}
	for rows.Next() {
		var r v1Row
		if err := rows.Scan(&r.mmsi, &r.ts, &r.recv, &r.tx, &r.lat6, &r.lon6, &r.sog10, &r.cog10, &r.heading, &r.navstat,
			&r.source, &r.station, &r.accepted, &r.corroborated, &r.implausible, &r.clockBad); err != nil {
			return written, err
		}
		if len(vessel) > 0 && vessel[0].mmsi != r.mmsi {
			if err := emit(); err != nil {
				return written, err
			}
		}
		vessel = append(vessel, r)
	}
	if err := rows.Err(); err != nil {
		return written, err
	}
	if err := emit(); err != nil {
		return written, err
	}
	if err := flush(); err != nil {
		return written, err
	}
	return written, c.conn.Exec(ctx, "INSERT INTO "+c.db+".receptions_converted VALUES (?, ?)", day, old)
}

// convertVessel converts one vessel's rows, in time order, and returns those in [day, end). A transmission's
// accepted copy is its earliest-arriving accepted one; a transmission with no accepted copy in reach takes its
// earliest copy's time. The anchor advances on the rows that enter positions_1m, as the live writer's does: not
// on a stale one, which arrived after an accepted report stamped more than a second later, staleFor's test.
// ponytail: staleFor also counts statics and a rebuilt source's ties, which the first layout does not hold; a row
// those alone made stale moves the anchor here, by under a second of track.
func convertVessel(rows []v1Row, day, end time.Time, anchors map[uint32]*anchor) []trackPoint {
	type first struct {
		ts, recv time.Time
		accepted bool
	}
	at := map[uint64]first{}
	for _, r := range rows {
		f, ok := at[r.tx]
		if !ok || r.accepted && (!f.accepted || r.recv.Before(f.recv)) || !r.accepted && !f.accepted && r.ts.Before(f.ts) {
			at[r.tx] = first{r.ts, r.recv, r.accepted}
		}
	}
	// Each transmission takes its id's byte, or the next one free among the vessel's transmissions stamped in the
	// same millisecond, in the order they were accepted, as the live writer's freeDisc does.
	order := make([]uint64, 0, len(at))
	for tx := range at {
		order = append(order, tx)
	}
	slices.SortFunc(order, func(x, y uint64) int {
		if c := at[x].recv.Compare(at[y].recv); c != 0 {
			return c
		}
		return cmp.Compare(x, y)
	})
	disc := make(map[uint64]uint8, len(at))
	used := map[int64][]uint8{}
	for _, tx := range order {
		ms := at[tx].ts.UnixMilli()
		d := uint8(tx ^ uint64(ms))
		for range 256 {
			if !slices.Contains(used[ms], d) {
				break
			}
			d++
		}
		used[ms] = append(used[ms], d)
		disc[tx] = d
	}
	stale := make([]bool, len(rows))
	earliest, k := time.Time{}, len(rows) // earliest: the first arrival among rows stamped after rows[i].ts + 1 s
	for i := len(rows) - 1; i >= 0; i-- {
		for k > 0 && rows[k-1].ts.After(rows[i].ts.Add(time.Second)) {
			if k--; rows[k].accepted && !rows[k].implausible && (earliest.IsZero() || rows[k].recv.Before(earliest)) {
				earliest = rows[k].recv
			}
		}
		stale[i] = !earliest.IsZero() && earliest.Before(rows[i].recv)
	}
	a := anchors[rows[0].mmsi]
	if a == nil {
		a = &anchor{}
		anchors[rows[0].mmsi] = a
	}
	var out []trackPoint
	var txs []uint64
	still := map[uint64]bool{}
	for i, r := range rows {
		if r.ts.Before(day) || !r.ts.Before(end) {
			continue
		}
		txAt := at[r.tx].ts
		pt := trackPoint{mmsi: r.mmsi, ts: r.ts, lat6: r.lat6, lon6: r.lon6, sog10: r.sog10, cog10: r.cog10, heading: r.heading,
			navStatus: r.navstat, source: r.source, txAt: txAt, txDisc: disc[r.tx], recv: r.recv,
			station: r.station, dup: !r.accepted, uncorroborated: !r.corroborated, implausible: r.implausible, clockBad: r.clockBad}
		pt.still = a.still(pt, nil, r.accepted && !stale[i] && !r.implausible && !r.clockBad)
		if r.accepted {
			still[r.tx] = pt.still
		}
		out, txs = append(out, pt), append(txs, r.tx)
	}
	// A copy carries its transmission's verdict on moving, as the live writer's do; one whose accepted copy fell
	// on the day before keeps the anchor's verdict on it.
	for j := range out {
		if s, ok := still[txs[j]]; ok && out[j].dup {
			out[j].still = s
		}
	}
	return out
}
