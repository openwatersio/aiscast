package main

// The lake: packaged history in R2 Data Catalog, the Iceberg tables the nightly packager writes, read in process
// through DuckDB (lake_duckdb.go). The vessel record imports ais.vessels from it once a day (import.go). Tracks
// come from ClickHouse, not the lake.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// lakeClient runs one SQL query against the lake and hands each row to each as it arrives, so a busy vessel's
// day is never held as rows, only as the positions decoded from them. A row is valid only during the call.
// The interface is what tests fake.
type lakeClient interface {
	query(ctx context.Context, sql string, each func(row map[string]json.RawMessage) error) error
}

// errLakeEmpty is a query against a table the catalog does not have yet: an empty lake, not a failure.
var errLakeEmpty = errors.New("lake table not found")

// lake reads the lake through its client, counting queries for /metrics.
type lake struct {
	client lakeClient

	// read by /metrics
	queries, failures atomic.Int64
	queryNanos        atomic.Int64
}

// lakeConns is the lake queries run at once.
const lakeConns = 4

// run queries the lake, counting the query for /metrics. A table the lake does not have yet has no rows.
func (l *lake) run(ctx context.Context, q string, each func(map[string]json.RawMessage) error) error {
	start := time.Now()
	err := l.client.query(ctx, q, each)
	l.queryNanos.Add(int64(time.Since(start)))
	l.queries.Add(1)
	if errors.Is(err, errLakeEmpty) {
		return nil
	}
	if err != nil {
		l.failures.Add(1)
	}
	return err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// lakeTime reads a timestamp as the lake returns it: an ISO string with or without a zone, or microseconds
// since the epoch. Timestamps without a zone are UTC, as the packager writes them.
func lakeTime(raw json.RawMessage) (time.Time, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMicro(n).UTC(), nil
		}
		return time.Time{}, fmt.Errorf("lake ts %q", s)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return time.Time{}, fmt.Errorf("lake ts %s", raw)
	}
	return time.UnixMicro(n).UTC(), nil
}
