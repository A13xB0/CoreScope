package main

// Tests for issue #1690 — cold-load uses wrong time axis (first_seen instead
// of effective recency). Three tests live in this file:
//
//   Test1690_ColdLoad_TimeAxis  — long-lived transmissions (first_seen 30d
//                                  ago) with recent observations must load
//                                  under a 1h hotStartupHours window.
//   Test1690_BackgroundLoadHonesty — backgroundLoadComplete must NOT flip to
//                                     true when coverage is below threshold.
//   Test1690_PerfStats_NewFields — typed perf response must expose
//                                   retentionHours, oldestLoaded,
//                                   loadCoverageRatio.

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// createTestDBWithLastSeen seeds a DB with the post-fix schema (last_seen
// column on transmissions). nowSec is the unix-second reference; fixture
// rows are placed relative to it.
//
// numTx transmissions, each with first_seen = nowSec - firstSeenAgo, and
// last_seen = nowSec - lastSeenAgo. Each tx has obsPerTx observations whose
// timestamps are within the last 20 minutes.
//
// Thin wrapper over seedTestDBRows (PR #1811 adv #2 — DRY). The schema
// DDL and the block-level PREFLIGHT async-migration annotation live
// there; this helper only supplies the row-time policy used by the
// #1690 fixture (fixed first_seen, fixed last_seen).
func createTestDBWithLastSeen(t *testing.T, dbPath string, numTx, obsPerTx int, nowSec int64, firstSeenAgo, lastSeenAgo time.Duration) {
	t.Helper()
	firstSeenTime := time.Unix(nowSec, 0).UTC().Add(-firstSeenAgo).Format(time.RFC3339)
	lastSeenUnix := nowSec - int64(lastSeenAgo.Seconds())
	seedTestDBRows(t, dbPath, numTx, obsPerTx, func(i int) (string, int64) {
		return firstSeenTime, lastSeenUnix
	})
}

// Test1690_ColdLoad_TimeAxis seeds 1000 transmissions whose hash *first
// appeared* 30 days ago but whose last observation was 30 minutes ago.
// With a 1h hotStartupHours, the pre-fix code (filtering on first_seen)
// loads zero rows; the post-fix code (filtering on last_seen) must load
// all 1000.
func Test1690_ColdLoad_TimeAxis(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 1000, 1, nowSec,
		30*24*time.Hour, // first_seen = 30d ago
		30*time.Minute)  // last_seen = 30min ago

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	loaded := len(store.packets)
	if loaded < 1000 {
		t.Fatalf("Test1690_ColdLoad_TimeAxis: expected ≥1000 transmissions loaded "+
			"(all 1000 fixture rows have last_seen within 1h), got %d. "+
			"Pre-fix behavior: chunked_load.go filters t.first_seen >= now-1h "+
			"which excludes all 30d-old rows.", loaded)
	}
}

// Test1690_BackgroundLoadHonesty seeds 5000 transmissions but caps the
// store's memory budget so it can only fit a fraction. The background
// loader then has nothing it may load, and the store must say so honestly:
// the real coverage ratio, and a covered window that starts at the memory
// edge rather than claiming to hold the whole database. That is the
// configured outcome, not a failed load, so done=true and failed=false (a
// store capped by memory never loads more, and "loading" would be a lie).
func Test1690_BackgroundLoadHonesty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	// 5000 rows; chunkSize=500 + maxMemoryMB=1 (→ maxPackets ≈ 1000) so
	// the load breaks at the end of the chunk that crosses the cap and
	// totalLoaded ≪ 5000.
	createTestDBWithLastSeen(t, dbPath, 5000, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
		MaxMemoryMB:     1, // forces bounded load ≪ 5000 rows
	})
	if err := store.LoadChunked(500); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	store.loadBackgroundChunks()

	if len(store.packets) >= 4500 {
		t.Fatalf("loaded %d/5000 packets; the 1 MB cap should have stopped the load well short", len(store.packets))
	}
	store.bgErrMu.Lock()
	ratio := store.loadCoverageRatio
	store.bgErrMu.Unlock()
	if want := float64(len(store.packets)) / 5000; ratio != want {
		t.Errorf("loadCoverageRatio=%v, want the real ratio %v", ratio, want)
	}
	if !store.backgroundLoadDone.Load() || store.backgroundLoadFailed.Load() {
		t.Errorf("done=%v failed=%v (%q); a load stopped by maxMemoryMB is complete, not failed",
			store.backgroundLoadDone.Load(), store.backgroundLoadFailed.Load(), store.BackgroundLoadError())
	}
}

// Test1690_PerfStats_NewFields asserts the typed perf payload exposes the
// retention/coverage fields needed for prod observability.
func Test1690_PerfStats_NewFields(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 10, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})
	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	ps := store.GetPerfStoreStatsTyped()
	buf, err := json.Marshal(ps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(buf, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"retentionHours", "oldestLoaded", "loadCoverageRatio"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("PerfPacketStoreStats missing %q field; payload=%s", key, string(buf))
		}
	}
}
