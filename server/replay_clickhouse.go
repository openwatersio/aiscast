package main

// aiscast replay -clickhouse rebuilds days of the record from the raw archive through the code at hand, so a fix
// to decoding, dedupe, or staleness reaches history. Each day is replayed, with a lead-in that builds state and
// writes nothing, into a staging table; compared with what receptions holds; and only then swapped in for the
// network's own copies that arrived that day. Rows from historical archives are never touched, and an archive
// day beside a replayed one is marked to load again, so its copies match the replayed transmissions.

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"golang.org/x/sync/errgroup"
)

// replayStaging begins the name of the table a replayed day is written to before it replaces the stored one;
// each run has its own, so a replay never touches another's.
const replayStaging = "receptions_replay_"

// replayShare is how much of a stored day's copies a replay must have to replace it without -force: less means
// raw hours are missing, or a change drops far more than it should.
const replayShare = 0.9

// replaySettings keeps replay's queries to what the live writer can spare.
var replaySettings = clickhouse.Settings{"max_threads": 2, "max_memory_usage": 1_500_000_000, "max_execution_time": 0}

func replayToClickHouse(url, archiveDir string, fetch bool, from, to time.Time, warmup time.Duration, dryRun, force bool) error {
	// The live server uploads an hour again if the bucket's copy is smaller, for two hours after it closes, and
	// keeps receiving copies stamped in the day after it ends; a day is replayed once both are well past.
	if latest := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1); to.After(latest) {
		return fmt.Errorf("-to %s: replay days that ended over a day ago, up to %s", to.Format("2006-01-02"), latest.Format("2006-01-02"))
	}
	ctx := context.Background()
	// Dialed, not opened: replay runs as the loader user, whose grants do not reach the schema. A day's delete,
	// insert, and rebuilds can each run past the client's default five minutes, and a timeout between the delete
	// and the insert would leave the day short.
	c, err := dialClickHouse(url, func(o *clickhouse.Options) { o.ReadTimeout = 6 * time.Hour })
	if err != nil {
		return err
	}
	defer c.conn.Close()
	var keys []s3Object
	if fetch {
		s3 := s3FromEnv()
		if s3 == nil {
			return fmt.Errorf("-fetch needs R2_BUCKET and its keys")
		}
		// One listing serves every day. ponytail: it walks the whole bucket, about 150,000 objects for 45 days, a
		// minute or so; list each source's day prefixes if that grows too slow.
		if keys, err = s3.list(ctx, ""); err != nil {
			return err
		}
	}
	for day := from; day.Before(to); day = day.AddDate(0, 0, 1) {
		dir := archiveDir
		if fetch {
			dir = filepath.Join(archiveDir, "replay-"+day.Format("2006-01-02"))
			if err := fetchRawDay(s3FromEnv(), keys, day, warmup, dir); err != nil {
				return fmt.Errorf("%s: %w", day.Format("2006-01-02"), err)
			}
		}
		err := c.replayDay(ctx, dir, day, warmup, dryRun, force)
		if fetch {
			os.RemoveAll(dir)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", day.Format("2006-01-02"), err)
		}
	}
	return nil
}

// fetchRawDay copies the raw hours replaying day reads, the day's and its lead-in's, from the bucket into dir.
func fetchRawDay(s3 *s3Client, keys []s3Object, day time.Time, warmup time.Duration, dir string) error {
	start := day.Add(-warmup).Truncate(time.Hour)
	if err := os.RemoveAll(dir); err != nil { // what a replay that stopped partway left
		return err
	}
	var want []s3Object
	var size int64
	for _, k := range keys {
		if strings.Contains("/"+k.Key+"/", "/../") || strings.HasPrefix(k.Key, "/") {
			return fmt.Errorf("raw key %q would leave %s", k.Key, dir)
		}
		if strings.HasPrefix(k.Key, "normalized/") || strings.HasPrefix(k.Key, "access/") {
			continue
		}
		at, ok := rawHourOf(k.Key)
		if !ok || at.Before(start) || !at.Before(day.Add(24*time.Hour)) {
			continue
		}
		want, size = append(want, k), size+k.Size
	}
	n := len(want)
	if n == 0 {
		return fmt.Errorf("no raw hours in the bucket")
	}
	// A day is thousands of small hour files, so one at a time waits on each request's round trip; several at once
	// fill the link.
	var g errgroup.Group
	g.SetLimit(fetchWorkers)
	for _, k := range want {
		g.Go(func() error { return s3.get(k.Key, filepath.Join(dir, k.Key)) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	log.Printf("replay: %s: fetched %d raw hours, %.1f GB", day.Format("2006-01-02"), n, float64(size)/1e9)
	return nil
}

// fetchWorkers is how many raw hours a replay downloads at once.
const fetchWorkers = 16

// rawHourOf is the hour a raw archive key holds, from its trailing YYYY/MM/DD/HH.gz.
func rawHourOf(key string) (time.Time, bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 5 || !strings.HasSuffix(key, ".gz") {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006/01/02/15.gz", strings.Join(parts[len(parts)-4:], "/"), time.UTC)
	return t, err == nil
}

// replayWindow selects the copies of the network's own sources that arrived on day. A copy stamped more than two
// days from its arrival, a reset clock's, falls outside it on both sides, so it is neither replaced nor added. The
// bound on the stamp alone keeps the delete to the partitions beside the day.
func replayWindow(day time.Time, archives []string) (string, []any) {
	const twoDays = 2 * 24 * 60 * 60 * 1000
	return "ts >= ? AND ts < ? AND fromUnixTimestamp64Milli(toUnixTimestamp64Milli(ts) + recv_delay, 'UTC') >= ? AND " +
			"fromUnixTimestamp64Milli(toUnixTimestamp64Milli(ts) + recv_delay, 'UTC') < ? AND abs(recv_delay) <= ? AND NOT has(?, source)",
		[]any{day.AddDate(0, 0, -2), day.AddDate(0, 0, 3), day, day.AddDate(0, 0, 1), twoDays, archives}
}

// replayDay replays one day from the raw hours under dir and, unless dryRun, swaps it in.
func (c *chConn) replayDay(ctx context.Context, dir string, day time.Time, warmup time.Duration, dryRun, force bool) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(replaySettings))
	started := time.Now()
	// Rows in the first layout keep their arrival in recv_ts, which the day's window does not read.
	if legacy, err := c.legacy(ctx); err != nil || legacy {
		if legacy {
			err = fmt.Errorf("receptions still has rows in the first layout; run aiscast convert-receptions and clickhouse-cleanup first")
		}
		return err
	}
	stage, err := c.createReplayStaging(ctx)
	if err != nil {
		return err
	}
	defer c.exec(context.Background(), "DROP TABLE IF EXISTS {db}."+stage)

	readers, err := collectReaders(dir, day.Add(-warmup), day.Add(24*time.Hour))
	if err != nil {
		return err
	}
	if len(readers) == 0 {
		return fmt.Errorf("no raw files under %s", dir)
	}
	p := newPipeline(newArchive("", nil))
	p.replayGate = day
	// Historical archives' rows never passed through live's cache, so they seed nothing, and the swap leaves them be.
	archives, err := chColumn[string](ctx, c.conn, "SELECT DISTINCT source FROM "+c.db+".history_loads")
	if err != nil {
		return err
	}
	if p.anchorSeeds, err = c.anchorsBefore(ctx, day.Add(-warmup), archives); err != nil {
		return fmt.Errorf("anchors: %w", err)
	}
	if err := c.seedVessels(ctx, p, day.Add(-warmup), archives); err != nil {
		return fmt.Errorf("vessels: %w", err)
	}
	// Own-ship sightings are staged beside the day and reach station_own only once the day is swapped in, so a dry
	// run or a replay refused for its share changes nothing.
	ownStage := stage + "_own"
	if err := c.exec(ctx, "CREATE TABLE {db}."+ownStage+" AS {db}.station_own"); err != nil {
		return err
	}
	defer c.exec(context.Background(), "DROP TABLE IF EXISTS {db}."+ownStage)
	staging := &chStore{w: &chConn{conn: c.conn, db: c.db, table: stage}, own: &chConn{conn: c.conn, db: c.db, own: ownStage}}
	p.attachClickHouse(staging)
	var flushNow func() error
	flush := func() error {
		// One AISHub record is a snapshot of thousands of vessels, so the queue, not the records read, says when:
		// at a third of its bound, which leaves room for any one record before a copy could be dropped.
		// Own-ship sightings have a bound of their own, so either queue filling sends both.
		p.chMu.Lock()
		queued, owned := len(p.chQueue), len(p.chOwn)
		p.chMu.Unlock()
		if queued < maxPending/3 && owned < maxOwnPending/3 {
			return nil
		}
		return flushNow()
	}
	flushNow = func() error {
		for range 3 { // a refused batch goes again under its token, so a retry never doubles it
			if err := p.flushClickHouse(); err == nil {
				return nil
			} else {
				log.Printf("replay: %v", err)
			}
			time.Sleep(5 * time.Second)
		}
		return p.flushClickHouse()
	}
	n, err := replayReaders(p, readers, day.Add(24*time.Hour), flush)
	if err != nil {
		return err
	}
	if err := flushNow(); err != nil {
		return err
	}
	if err := flushNow(); err != nil { // the batch the first sent, if it was resending one
		return err
	}
	if d := staging.dropped.Load(); d > 0 {
		return fmt.Errorf("%d copies dropped from a full queue", d)
	}
	// Sightings past a sender's allowance were refused live too; only ones lost to a full map leave the day short.
	if d := staging.ownDropped.Load(); d > 0 {
		return fmt.Errorf("%d own-ship sightings dropped from a full map", d)
	}

	where, args := replayWindow(day, archives)
	stored, err := c.replayCounts(ctx, "receptions", where, args)
	if err != nil {
		return err
	}
	replayed, err := c.replayCounts(ctx, stage, where, args)
	if err != nil {
		return err
	}
	printReplayCounts(day, stored, replayed)
	share := float64(replayed[""].copies) / max(float64(stored[""].copies), 1)
	log.Printf("replay: %s: %d receptions read, %d copies replayed against %d stored (%.1f%%), in %s",
		day.Format("2006-01-02"), n, replayed[""].copies, stored[""].copies, 100*share, time.Since(started).Round(time.Second))
	if dryRun {
		return nil
	}
	if share < replayShare && !force {
		return fmt.Errorf("the replay has %.1f%% of the stored copies, under %.0f%%; check the raw hours, or pass -force", 100*share, 100*replayShare)
	}

	if err := c.conn.Exec(chDeleteSync(ctx), "DELETE FROM "+c.db+".receptions WHERE "+where, args...); err != nil {
		return fmt.Errorf("delete the stored day: %w", err)
	}
	// A token of this run's own: replaying a day again inserts the same blocks, which receptions' deduplication
	// window would otherwise drop although the delete before has removed their rows.
	insert := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"insert_deduplication_token": fmt.Sprintf("replay-%s-%d", day.Format("2006-01-02"), time.Now().UnixNano())}))
	if err := c.copyStaged(insert, stage, where, args...); err != nil {
		return fmt.Errorf("insert the replayed day, after deleting the stored one; replay it again: %w", err)
	}
	// Copies that arrived on day carry stamps on the days beside it too.
	for d := day.AddDate(0, 0, -1); !d.After(day.AddDate(0, 0, 1)); d = d.AddDate(0, 0, 1) {
		if err := c.rebuildPositions1m(ctx, d); err != nil {
			return fmt.Errorf("positions_1m %s: %w", d.Format("2006-01-02"), err)
		}
		if err := c.rebuildCoverage(ctx, d); err != nil {
			return fmt.Errorf("coverage %s: %w", d.Format("2006-01-02"), err)
		}
	}
	// An archive's copies of the network's transmissions name them; the loader loads those days again.
	if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".history_loads SELECT source, file, day, size, etag, false, read, unplaced, repeated, "+
		"kept, matched, implausible, now64(3) FROM "+c.db+".history_loads FINAL WHERE complete AND matched > 0 AND day >= ? AND day <= ?",
		day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)); err != nil {
		return fmt.Errorf("mark archive days to load again: %w", err)
	}
	// Own-ship sightings last, so a failure here leaves the receptions' rollups already rebuilt. The hours received on
	// the day are replaced, as its receptions are: a correction that changes which vessel a station claimed must not
	// leave the old claim. The staged sightings are exactly those hours, since the replay takes messages by arrival.
	if err := c.conn.Exec(chDeleteSync(ctx), "DELETE FROM "+c.db+".station_own WHERE hour >= ? AND hour < ?", day, day.AddDate(0, 0, 1)); err != nil {
		return fmt.Errorf("station_own: %w", err)
	}
	if err := c.conn.Exec(ctx, "INSERT INTO "+c.db+".station_own (hour, station, mmsi, last_ts) SELECT hour, station, mmsi, last_ts FROM "+c.db+"."+ownStage); err != nil {
		return fmt.Errorf("station_own: %w", err)
	}
	log.Printf("replay: %s: replaced, in %s", day.Format("2006-01-02"), time.Since(started).Round(time.Second))
	return nil
}

// rebuildCoverage bins day's usable positions into coverage again. Its distinct sets only ever grow, so a copy
// replay replaced or judged implausible leaves the cell it counted toward unless the day is deleted first.
func (c *chConn) rebuildCoverage(ctx context.Context, day time.Time) error {
	if err := c.conn.Exec(chDeleteSync(ctx), "DELETE FROM "+c.db+".coverage WHERE day = ?", day); err != nil {
		return err
	}
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".coverage "+chCoverageSelect(c.db+".receptions", chUsable+" AND ts >= ? AND ts < ?"),
		day, day.AddDate(0, 0, 1))
}

// copyStaged inserts the staged rows matching where into receptions, by column name rather than position, so a
// column a migration adds to receptions while a replay runs reads as its default instead of failing the insert
// after the stored day is deleted.
func (c *chConn) copyStaged(ctx context.Context, stage, where string, args ...any) error {
	cols, err := chColumn[string](ctx, c.conn, "SELECT name FROM system.columns WHERE database = ? AND table = ? ORDER BY position", c.db, stage)
	if err != nil {
		return fmt.Errorf("the staged columns: %w", err)
	}
	list := strings.Join(cols, ", ")
	return c.conn.Exec(ctx, "INSERT INTO "+c.db+".receptions ("+list+") SELECT "+list+" FROM "+c.db+"."+stage+" WHERE "+where, args...)
}

// createReplayStaging makes an empty staging table of the run's own and returns its name. An explicit engine keeps receptions' columns but not its TTL
// or storage policy, and no view reads it. Its deduplication window, like receptions', makes a batch sent again
// after a lost acknowledgement land once.
func (c *chConn) createReplayStaging(ctx context.Context) (string, error) {
	name := fmt.Sprintf("%s%d", replayStaging, time.Now().UnixNano())
	return name, c.exec(ctx, "CREATE TABLE {db}."+name+" AS {db}.receptions ENGINE = MergeTree PARTITION BY toYYYYMMDD(ts) ORDER BY (mmsi, ts)"+
		" SETTINGS non_replicated_deduplication_window = 1000")
}

// seedVessels fills the cache, at the start of a replay's lead-in, with what live held then for each vessel it
// still kept: its last plausible position, when a source that is not low-trust last reported it, and the anchor in
// p.anchorSeeds. A lead-in
// that started from nothing would judge plausibility and corroboration against whichever report it met first, and
// where two vessels share an MMSI, or a feed repeats a bad position, that can be the other one: live, holding the
// vessel since long before, flagged the rest as implausible and replay would flag the opposite. positions_1m holds
// only usable accepted reports, a row a minute while moving and every 30 minutes while still, so each vessel heard
// within vesselTTL has its last there.
func (c *chConn) seedVessels(ctx context.Context, p *Pipeline, at time.Time, archives []string) error {
	rows, err := c.conn.Query(ctx, "SELECT mmsi, max(ts), argMax(lat6, ts), argMax(lon6, ts), argMax(sog10, ts), argMax(cog10, ts), argMax(heading, ts),"+
		" maxIf(ts, source NOT IN ('udp', 'mmsi'))"+
		" FROM "+c.db+".positions_1m WHERE slot >= ? AND slot < ? AND ts < ? AND NOT has(?, source) AND NOT ("+invalidMMSIWhere()+") GROUP BY mmsi HAVING max(ts) >= ?",
		at.Add(-corroborationWindow).Truncate(30*time.Minute), at, at, archives, at.Add(-vesselTTL))
	if err != nil {
		return err
	}
	defer rows.Close()
	p.vmu.Lock()
	defer p.vmu.Unlock()
	for rows.Next() {
		var mmsi uint32
		var last time.Time
		var lat6, lon6 int32
		var sog10, cog10, heading uint16
		var trusted time.Time
		if err := rows.Scan(&mmsi, &last, &lat6, &lon6, &sog10, &cog10, &heading, &trusted); err != nil {
			return err
		}
		v := newVessel()
		v.Lat, v.Lon, v.HasPos, v.PosAt, v.Seen = float64(lat6)/600000, float64(lon6)/600000, true, last, last
		v.Sog, v.Cog, v.Heading = float64(sog10)/10, float64(cog10)/10, heading
		if trusted.Year() > 1970 {
			v.TrustedAt = trusted
		}
		if a := p.anchorSeeds[mmsi]; a != nil {
			v.moved = *a // where it was last moving, as live's anchor; its last position may be a still one
		}
		p.putVesselLocked(mmsi, v)
	}
	return rows.Err()
}

// replayCount is one source's copies on a day, and how many of them were accepted and judged implausible; the
// "" source is every source together.
type replayCount struct{ copies, accepted, implausible uint64 }

func (c *chConn) replayCounts(ctx context.Context, table, where string, args []any) (map[string]replayCount, error) {
	rows, err := c.conn.Query(ctx, "SELECT source, count(), countIf(accepted), countIf(implausible) FROM "+c.db+"."+table+
		" WHERE "+where+" GROUP BY source", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]replayCount{}
	for rows.Next() {
		var source string
		var n replayCount
		if err := rows.Scan(&source, &n.copies, &n.accepted, &n.implausible); err != nil {
			return nil, err
		}
		out[source] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var total replayCount
	for _, n := range out {
		total.copies, total.accepted, total.implausible = total.copies+n.copies, total.accepted+n.accepted, total.implausible+n.implausible
	}
	out[""] = total
	return out, nil
}

func printReplayCounts(day time.Time, stored, replayed map[string]replayCount) {
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(w, "%s\tstored copies\treplayed\taccepted\treplayed\timplausible\treplayed\t\n", day.Format("2006-01-02"))
	sources := map[string]bool{}
	for s := range stored {
		sources[s] = true
	}
	for s := range replayed {
		sources[s] = true
	}
	for _, s := range slices.Sorted(maps.Keys(sources)) {
		if s != "" {
			a, b := stored[s], replayed[s]
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t\n", s, a.copies, b.copies, a.accepted, b.accepted, a.implausible, b.implausible)
		}
	}
	a, b := stored[""], replayed[""]
	fmt.Fprintf(w, "all\t%d\t%d\t%d\t%d\t%d\t%d\t\n", a.copies, b.copies, a.accepted, b.accepted, a.implausible, b.implausible)
	w.Flush()
}
