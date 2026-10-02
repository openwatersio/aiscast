package main

// The lake's engine: DuckDB in process, its iceberg extension attached to the R2 Data Catalog. DuckDB reads only
// the Parquet row groups a query needs, with range requests, and caches file metadata and data between queries,
// so a repeated read of a vessel's days costs a fraction of a second. A query that names the lake refers to it as
// lake.ais.<table>.
//
// This is the one part of the server that needs cgo.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

// errLakeUnavailable is a lake that failed to attach in the last minute; the next attempt waits that long.
var errLakeUnavailable = errors.New("lake unavailable")

type duckLake struct {
	token, acct, bucket, dir string

	mu       sync.Mutex
	db       *sql.DB // nil until the catalog is attached
	failedAt time.Time
}

// duckLakeFromEnv is the lake, or nil when LAKE_CATALOG_TOKEN is unset. The token needs R2 Data Catalog read and
// object read on the bucket LAKE_BUCKET names. DuckDB keeps its extensions under LAKE_DUCKDB_DIR.
func duckLakeFromEnv() *duckLake {
	token, acct := os.Getenv("LAKE_CATALOG_TOKEN"), os.Getenv("R2_ACCOUNT_ID")
	if token == "" || acct == "" {
		return nil
	}
	return &duckLake{token: token, acct: acct, bucket: env("LAKE_BUCKET", "ais-lake"), dir: env("LAKE_DUCKDB_DIR", "duckdb")}
}

// open attaches the catalog on first use. A failure is remembered for a minute, so an outage costs each request
// an immediate error rather than another attempt.
func (d *duckLake) open(ctx context.Context) (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return d.db, nil
	}
	if time.Since(d.failedAt) < time.Minute {
		return nil, errLakeUnavailable
	}
	// Not the request's context: a caller that hangs up mid-attach would otherwise fail the lake for a minute.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	db, err := d.attach(ctx)
	if err != nil {
		d.failedAt = time.Now()
		return nil, err
	}
	d.db = db
	return db, nil
}

func (d *duckLake) attach(ctx context.Context) (*sql.DB, error) {
	dir, err := filepath.Abs(d.dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(lakeConns)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	stmts := []string{
		"SET GLOBAL home_directory = " + quote(dir),
		"SET GLOBAL extension_directory = " + quote(filepath.Join(dir, "extensions")),
		// Bounded so a heavy history read cannot crowd out ingest and fan-out.
		"SET GLOBAL threads = 4",
		"SET GLOBAL memory_limit = '1GB'",
		"INSTALL iceberg", "LOAD iceberg", "INSTALL httpfs", "LOAD httpfs",
		"SET GLOBAL enable_http_metadata_cache = true",
		"SET GLOBAL enable_external_file_cache = true",
		"CREATE SECRET lake_catalog (TYPE ICEBERG, TOKEN " + quote(d.token) + ")",
		fmt.Sprintf("ATTACH %s AS lake (TYPE ICEBERG, ENDPOINT %s, READ_ONLY)",
			quote(d.acct+"_"+d.bucket), quote("https://catalog.cloudflarestorage.com/"+d.acct+"/"+d.bucket)),
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			db.Close()
			if strings.Contains(s, "SECRET") {
				s = "CREATE SECRET"
			}
			return nil, fmt.Errorf("duckdb %s: %w", s, err)
		}
	}
	// The first query of a table reads every manifest, several seconds for a lake of a few months, and later
	// queries reuse them. A query that matches nothing pays that here instead of in a request. A lake without
	// the table yet has nothing to load.
	db.ExecContext(ctx, `SELECT 1 FROM lake.ais.positions WHERE day = DATE '1970-01-01' LIMIT 0`)
	return db, nil
}

// query hands each row to each as JSON values keyed by column, the shape the lake's readers decode: dates and
// timestamps as RFC 3339 strings, integers as numbers, bytes as base64. The row's map is reused.
func (d *duckLake) query(ctx context.Context, q string, each func(map[string]json.RawMessage) error) error {
	db, err := d.open(ctx)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		// Only a missing table: an object missing from R2 ("key does not exist") is a failure, not an empty day.
		if strings.Contains(err.Error(), "Catalog Error: Table with name") {
			return errLakeEmpty
		}
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals, ptrs := make([]any, len(cols)), make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	m := make(map[string]json.RawMessage, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, c := range cols {
			if m[c], err = json.Marshal(vals[i]); err != nil {
				return fmt.Errorf("lake %s: %w", c, err)
			}
		}
		if err := each(m); err != nil {
			return err
		}
	}
	return rows.Err()
}
