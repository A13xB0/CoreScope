package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// GetDBSizeStatsTyped backs the "sqlite" block of /api/perf, which the perf
// page polls every 5 seconds. Its COUNT(*) over observations is a full table
// scan: about 0.9 s on a database with 1.8M observations, holding one of the
// server's four read connections each time. The row counts change slowly and
// the page only displays them, so they are computed at most once per
// sqliteRowCountsTTL, and concurrent misses share one query.

// TestGetDBSizeStats_RowCountsCachedWithinTTL asserts that a second call
// inside the TTL reuses the counts instead of re-running the COUNT(*) queries.
//
// Anti-tautology: remove the cache and the hook fires twice.
func TestGetDBSizeStats_RowCountsCachedWithinTTL(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.rowCountsQueryHook = func() { atomic.AddInt32(&calls, 1) }

	first := db.GetDBSizeStatsTyped()
	second := db.GetDBSizeStatsTyped()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected the row counts to be queried once within the TTL, got %d", got)
	}
	if first.Rows == nil || second.Rows == nil {
		t.Fatal("expected row counts in both results")
	}
	if *first.Rows != *second.Rows {
		t.Fatalf("cached counts differ: %+v vs %+v", *first.Rows, *second.Rows)
	}
	if first.Rows.Transmissions == 0 || first.Rows.Observations == 0 {
		t.Fatalf("expected seeded rows to be counted, got %+v", *first.Rows)
	}
	if first.Rows == second.Rows {
		t.Fatal("expected each call to return its own copy of the counts, not a shared pointer")
	}
}

// TestGetDBSizeStats_RowCountsRefreshAfterTTL asserts that the counts are
// queried again once the cached ones are older than the TTL.
func TestGetDBSizeStats_RowCountsRefreshAfterTTL(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.rowCountsQueryHook = func() { atomic.AddInt32(&calls, 1) }

	db.GetDBSizeStatsTyped()
	db.expireRowCountsForTest()
	db.GetDBSizeStatsTyped()

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected a fresh query after the TTL, got %d queries", got)
	}
}

// TestGetDBSizeStats_RowCountsSingleflight asserts that concurrent callers
// that all miss the cache share one execution of the COUNT(*) queries.
//
// Anti-tautology: without singleflight every caller runs the queries.
func TestGetDBSizeStats_RowCountsSingleflight(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.rowCountsQueryHook = func() {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond) // make sure the callers overlap
	}

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if st := db.GetDBSizeStatsTyped(); st.Rows == nil {
				t.Error("expected row counts")
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 row-count query under singleflight, got %d", got)
	}
}

// BenchmarkGetDBSizeStats measures one /api/perf poll's SQLite block against a
// file database shaped like a production one: 600k transmissions with 1-5
// observations each (1.8M observations), with observation rows as wide as a
// production database's (raw_hex ~165, path_json ~50, resolved_path ~400
// bytes; about 1.2 GB on disk). COUNT(*) cost follows the table's page count,
// not its row count, so narrow fixture rows understate it badly. Building the
// fixture takes about a minute, so it is built once per benchmark process.
//
//	go test -run '^$' -bench BenchmarkGetDBSizeStats -benchtime 20x -count 10 ./cmd/server
func BenchmarkGetDBSizeStats(b *testing.B) {
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	db := sizeStatsBenchDB(b)
	db.GetDBSizeStatsTyped() // warm the page cache, as a running server would be
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.GetDBSizeStatsTyped()
	}
}

var (
	sizeStatsBenchOnce sync.Once
	sizeStatsBenchPath string
	benchFixtureDirs   []string // removed by TestMain once every test and benchmark has run
)

// removeBenchFixtures deletes fixture directories that outlive a single
// *testing.B (b.TempDir would be removed after the first b.N round).
func removeBenchFixtures() {
	for _, d := range benchFixtureDirs {
		os.RemoveAll(d)
	}
}

func sizeStatsBenchDB(b *testing.B) *DB {
	b.Helper()
	sizeStatsBenchOnce.Do(func() {
		dir, err := os.MkdirTemp("", "corescope-bench-")
		if err != nil {
			b.Fatal(err)
		}
		benchFixtureDirs = append(benchFixtureDirs, dir)
		sizeStatsBenchPath = filepath.Join(dir, "size-stats-bench.db")
		createWideObsDB(b, sizeStatsBenchPath, 600_000)
	})
	db, err := OpenDB(sizeStatsBenchPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}

// expireRowCountsForTest makes the cached row counts look older than the TTL.
func (db *DB) expireRowCountsForTest() { db.rowCountsAt = time.Time{} }

// createWideObsDB writes numTx transmissions with 1-5 observations each, with
// the observation columns filled to production widths (see the benchmark).
func createWideObsDB(tb testing.TB, dbPath string, numTx int) {
	tb.Helper()
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		tb.Fatal(err)
	}
	defer conn.Close()
	for _, stmt := range []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
			route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT,
			observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT,
			timestamp TEXT, raw_hex TEXT, resolved_path TEXT)`,
		`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, frequency REAL)`,
		`CREATE TABLE schema_version (version INTEGER)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			tb.Fatalf("bench fixture: %v\n%s", err, stmt)
		}
	}
	txStmt, err := conn.Prepare(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type,
		payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, 1, 4, 1, '{}')`)
	if err != nil {
		tb.Fatal(err)
	}
	defer txStmt.Close()
	obsStmt, err := conn.Prepare(`INSERT INTO observations (transmission_id, observer_id, observer_name,
		direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path)
		VALUES (?, ?, 'Obs', 'RX', -5, -90, 5, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer obsStmt.Close()

	rawHex := strings.Repeat("ab", 82)                                                                   // 164 chars
	pathJSON := `["a1b2c3","d4e5f6","a7b8c9","d0e1f2","a3b4c5","d6"]`                                    // 50 chars
	resolved := `[` + strings.TrimSuffix(strings.Repeat(`"`+strings.Repeat("c", 64)+`",`, 6), ",") + `]` // 400 chars
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := conn.Exec("BEGIN"); err != nil {
		tb.Fatal(err)
	}
	for i := 1; i <= numTx; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		if _, err := txStmt.Exec(i, rawHex, fmt.Sprintf("h%07d", i), ts); err != nil {
			tb.Fatal(err)
		}
		for j := 0; j < (i%5)+1; j++ {
			if _, err := obsStmt.Exec(i, fmt.Sprintf("obs%d", j), pathJSON, ts, rawHex, resolved); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if _, err := conn.Exec("COMMIT"); err != nil {
		tb.Fatal(err)
	}
}
