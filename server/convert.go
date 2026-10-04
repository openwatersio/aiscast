package main

// aiscast convert-receptions: copy receptions_v1, the first receptions layout, into the current one, a day at
// a time. chMigrate renames the old table when the server first starts on the current layout; until this runs,
// fine tracks past the 48-hour window find nothing for the days before. It runs the live writer's own code, so a
// converted row is the row the server would have written: the same transmission name, and the same anchor
// deciding whether a vessel was moving, which a single SQL statement cannot carry through a vessel's day.

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

func runConvertReceptions(args []string) {
	fset := flag.NewFlagSet("convert-receptions", flag.ExitOnError)
	fromS := fset.String("from", "", "first day to convert, YYYY-MM-DD UTC (required)")
	toS := fset.String("to", "", "last day to convert, YYYY-MM-DD UTC (required)")
	fset.Parse(args)
	from, err1 := time.ParseInLocation("2006-01-02", *fromS, time.UTC)
	to, err2 := time.ParseInLocation("2006-01-02", *toS, time.UTC)
	if err1 != nil || err2 != nil || to.Before(from) {
		log.Fatalf("convert-receptions: -from and -to must be YYYY-MM-DD with from <= to")
	}
	url := os.Getenv("CLICKHOUSE_URL")
	if url == "" {
		log.Fatalf("convert-receptions: CLICKHOUSE_URL is not set")
	}
	ctx := context.Background()
	c, err := openClickHouse(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer c.conn.Close()
	anchors := map[uint32]*anchor{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		start := time.Now()
		n, err := c.convertDay(ctx, d, anchors)
		if err != nil {
			log.Fatalf("convert-receptions: %s: %v", d.Format("2006-01-02"), err)
		}
		fmt.Printf("%s %d rows in %s\n", d.Format("2006-01-02"), n, time.Since(start).Round(time.Second))
	}
}

// v1Row is a receptions_v1 row.
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
// whose copies straddle the switch has one name. Its copies sit within 5 minutes of it, so the day is read with
// 10 minutes either side; only its own rows are written. anchors carries each vessel's anchor from day to day.
func (c *chConn) convertDay(ctx context.Context, day time.Time, anchors map[uint32]*anchor) (int, error) {
	end := day.Add(24 * time.Hour)
	rctx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_memory_usage": 1_500_000_000, "max_threads": 2, "optimize_read_in_order": 1, "max_execution_time": 3600,
	}))
	rows, err := c.conn.Query(rctx, "SELECT mmsi, ts, recv_ts, tx, lat6, lon6, sog10, cog10, heading, navstat, toString(source), toString(station),"+
		" accepted, corroborated, implausible, clock_bad FROM "+c.db+".receptions_v1 WHERE ts >= ? AND ts < ? ORDER BY mmsi, ts",
		day.Add(-10*time.Minute), end.Add(10*time.Minute))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var batch []trackPoint
	written, batches := 0, 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		token := fmt.Sprintf("convert-%s-%d", day.Format("2006-01-02"), batches)
		if err := c.insert(ctx, token, batch); err != nil {
			return err
		}
		written += len(batch)
		batches++
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
	return written, flush()
}

// convertVessel converts one vessel's rows, in time order, and returns those in [day, end). A transmission's
// accepted copy is its earliest-arriving accepted one; a transmission with no accepted copy in reach takes its
// earliest copy's time. The anchor advances on the rows that enter positions_1m, as the live writer's does: not
// on a stale one, which arrived after an accepted report stamped more than a second later, staleFor's test.
// ponytail: staleFor also counts statics and a rebuilt source's ties, which receptions_v1 does not hold; a row
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
	for i, r := range rows {
		if r.ts.Before(day) || !r.ts.Before(end) {
			continue
		}
		txAt := at[r.tx].ts
		pt := trackPoint{mmsi: r.mmsi, ts: r.ts, lat6: r.lat6, lon6: r.lon6, sog10: r.sog10, cog10: r.cog10, heading: r.heading,
			navStatus: r.navstat, source: r.source, txAt: txAt, txDisc: disc[r.tx], recv: r.recv,
			station: r.station, dup: !r.accepted, uncorroborated: !r.corroborated, implausible: r.implausible, clockBad: r.clockBad}
		pt.still = a.still(pt, nil, r.accepted && !stale[i] && !r.implausible && !r.clockBad)
		out = append(out, pt)
	}
	return out
}
