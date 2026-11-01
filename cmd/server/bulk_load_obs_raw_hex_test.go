package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The bulk loaders do not keep an observation's raw_hex (#881: the packet's
// own raw_hex serves it), so they should not read it either. Every row scan
// of it allocated a copy only to drop it: about 65MB per startup on a week
// of a busy network, where each of 400k observations carries ~160 hex chars.

func bigObsRawHexStore(t *testing.T) *PacketStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rawhex.db")
	createTestDBAt(t, dbPath, 200)
	t.Cleanup(func() { os.RemoveAll(dir) })
	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// 32KB of hex per observation: 6.4MB across the 200 rows.
	if _, err := conn.Exec(`UPDATE observations SET raw_hex = hex(zeroblob(16384))`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if !db.hasObsRawHex {
		t.Fatal("fixture: observations.raw_hex not detected")
	}
	return NewPacketStore(db, &PacketStoreConfig{})
}

func allocatedMB(fn func()) float64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return float64(b.TotalAlloc-a.TotalAlloc) / (1 << 20)
}

func TestBulkLoadersSkipObservationRawHex(t *testing.T) {
	const limitMB = 4 // the column alone is 6.4MB
	cases := map[string]func(s *PacketStore) error{
		"Load":        func(s *PacketStore) error { return s.Load() },
		"LoadChunked": func(s *PacketStore) error { return s.LoadChunked(50) },
		"loadChunk": func(s *PacketStore) error {
			from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
			return s.loadChunk(from, from.Add(72*time.Hour))
		},
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			s := bigObsRawHexStore(t)
			var err error
			mb := allocatedMB(func() { err = load(s) })
			if err != nil {
				t.Fatal(err)
			}
			if len(s.packets) != 200 {
				t.Fatalf("loaded %d packets, want 200", len(s.packets))
			}
			t.Logf("%s allocated %.1f MB", name, mb)
			if mb > limitMB {
				t.Errorf("%s allocated %.1f MB loading 200 packets, limit %d MB: it reads observation raw_hex", name, mb, limitMB)
			}
		})
	}
}
