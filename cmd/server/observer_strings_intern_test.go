package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

// Every observation carries its observer's ID, name, IATA code and the
// direction. A store holds hundreds of thousands of observations from a few
// hundred observers, so each loader shares one copy of each of those strings
// instead of keeping the copy every row scan makes.

func assertObserverStringsShared(t *testing.T, store *PacketStore, wantObs int) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	var all []*StoreObs
	for _, tx := range store.packets {
		all = append(all, tx.Observations...)
	}
	if len(all) < wantObs {
		t.Fatalf("loaded %d observations, want at least %d", len(all), wantObs)
	}
	first := all[0]
	for _, f := range []struct {
		name string
		get  func(*StoreObs) string
	}{
		{"ObserverID", func(o *StoreObs) string { return o.ObserverID }},
		{"ObserverName", func(o *StoreObs) string { return o.ObserverName }},
		{"Direction", func(o *StoreObs) string { return o.Direction }},
	} {
		want := f.get(first)
		if want == "" {
			t.Fatalf("fixture: %s is empty", f.name)
		}
		copies := map[*byte]bool{}
		for _, o := range all {
			if f.get(o) == want {
				copies[unsafe.StringData(f.get(o))] = true
			}
		}
		if len(copies) != 1 {
			t.Errorf("%s %q is held in %d copies across %d observations, want 1", f.name, want, len(copies), len(all))
		}
	}
}

func openInternTestStore(t *testing.T, numTx int) *PacketStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "intern.db")
	createTestDBAt(t, dbPath, numTx)
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPacketStore(db, &PacketStoreConfig{})
}

func TestObserverStringsShared_Load(t *testing.T) {
	store := openInternTestStore(t, 50)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}

func TestObserverStringsShared_LoadChunked(t *testing.T) {
	store := openInternTestStore(t, 50)
	if err := store.LoadChunked(10); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}

func TestObserverStringsShared_LoadChunk(t *testing.T) {
	store := openInternTestStore(t, 50)
	from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := store.loadChunk(from, from.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}
