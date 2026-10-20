package netwatch

import (
	"fmt"
	"runtime"
	"testing"
)

// The watch keeps 72 hours of "already counted" keys so an observation is
// counted once per packet for each thing it feeds. On pre-prod that set held
// 230 MB of a 485 MB heap: each key carried two string headers, and the keys
// built by concatenation ("\x00"+observer, ...) each pinned a fresh string.
// A key costs the set only its hash now.
func TestSeenKeysCostLittleEach(t *testing.T) {
	w := New(Options{}, nil)
	nodes := make([]string, 12)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("%064x", i+1)
	}
	observers := []string{"obs-north", "obs-south", "obs-east", "obs-west"}
	const h = 500000
	w.AddObservation(0, h, observers[0], nil) // open the window first

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for tx := int64(1); tx <= 20000; tx++ {
		for i, obs := range observers {
			path := []string{nodes[(int(tx)+i)%12], nodes[(int(tx)+i+3)%12], nodes[(int(tx)+i+7)%12]}
			w.AddObservation(tx, h, obs, path)
		}
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	entries := 0
	for _, m := range w.seen {
		entries += m.len()
	}
	if entries < 200000 {
		t.Fatalf("only %d seen entries; the fixture should exercise the set", entries)
	}
	per := float64(after.HeapAlloc-before.HeapAlloc) / float64(entries)
	t.Logf("%d seen entries, %.1f bytes each", entries, per)
	// An 8-byte hash in a flat open-addressing table that doubles at 3/4
	// full averages ~11-16 bytes. String keys measured 87, a Go map of the
	// hashes 26.
	if per > 16 {
		t.Errorf("seen set costs %.1f bytes per entry, want <= 16", per)
	}
	runtime.KeepAlive(w)
}

func TestHashSet(t *testing.T) {
	var s hashSet
	for i := uint64(0); i < 10000; i++ {
		if !s.add(i * 0x9E3779B97F4A7C15) {
			t.Fatalf("add(%d) reported a duplicate", i)
		}
	}
	for i := uint64(0); i < 10000; i++ {
		if s.add(i * 0x9E3779B97F4A7C15) {
			t.Fatalf("re-add(%d) reported new", i)
		}
	}
	if s.len() != 10000 {
		t.Fatalf("len = %d, want 10000", s.len())
	}
	// 0 is the empty marker: it is stored as 1, so the two are one key.
	var z hashSet
	if !z.add(0) || z.add(1) || z.add(0) {
		t.Fatal("0 and 1 must share a slot")
	}
}
