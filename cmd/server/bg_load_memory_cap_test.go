package main

import (
	"path/filepath"
	"testing"
	"time"
)

// The background fill loader walked the whole retention window whatever
// maxMemoryMB said, so a store capped at 250 MB first grew to the full
// window (600+ MB of heap on a week of real data) and only then evicted the
// oldest rows back down. That peak is what the process keeps as RSS. The
// loader must stop once the store is at the cap, report it as a memory
// limit rather than a failed load, and leave the older days in the DB.
func TestBackgroundLoad_StopsAtMemoryCap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	nowSec := time.Now().UTC().Unix()
	const totalRows, spanDays = 4200, 14
	createTestDBSpreadOverDays(t, dbPath, totalRows, spanDays, nowSec)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	const capMB = 1
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  float64(spanDays * 24),
		HotStartupHours: 24,
		MaxMemoryMB:     capMB,
	})
	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}

	capBytes := int64(capMB) * 1048576
	if store.trackedBytes > 2*capBytes {
		t.Errorf("trackedBytes=%d after startup, want at most one day past the %d-byte cap", store.trackedBytes, capBytes)
	}
	if len(store.packets) >= totalRows {
		t.Fatalf("loaded all %d rows; the background loader ignored maxMemoryMB", totalRows)
	}
	if len(store.packets) <= totalRows/spanDays*2 {
		t.Fatalf("loaded only %d rows; the background loader should fill up to the cap", len(store.packets))
	}
	if store.backgroundLoadFailed.Load() || !store.backgroundLoadDone.Load() {
		t.Errorf("done=%v failed=%v (%q), want done and not failed: stopping at the cap is the configured outcome",
			store.backgroundLoadDone.Load(), store.backgroundLoadFailed.Load(), store.BackgroundLoadError())
	}
}
