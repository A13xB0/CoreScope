package main

import (
	"fmt"
	"testing"
	"time"
)

// Startup loads the whole retention window and then evicts down to
// maxMemoryMB, so without shrinking every index keeps the capacity of the
// largest window it ever held. On a copy of a live week, 39k transmissions
// left in memory sat in lists sized for ~70k (byPathHop 630k entries in
// 1.29M slots, byNode 430k in 833k).
func TestEvictStale_ShrinksMostlyEmptyIndexLists(t *testing.T) {
	now := time.Now().UTC()
	// 400 packets, one a minute, the oldest 300 of them older than 24h.
	store := makeTestStore(400, now.Add(-30*time.Hour), 1)
	store.retentionHours = 24

	// A shared node and a shared path hop, so byNode and byPathHop each
	// have one long list that eviction filters rather than deletes.
	store.byPathHop = make(map[string][]*StoreTx)
	for i, tx := range store.packets {
		tx.DecodedJSON = fmt.Sprintf(`{"pubKey":"pk%04d","destPubKey":"shared"}`, i)
		store.byNode["shared"] = append(store.byNode["shared"], tx)
		if store.nodeHashes["shared"] == nil {
			store.nodeHashes["shared"] = make(map[int]struct{})
		}
		store.nodeHashes["shared"][tx.ID] = struct{}{}
		store.byPathHop["aa"] = append(store.byPathHop["aa"], tx)
	}

	evicted := store.EvictStale()
	if evicted < 250 {
		t.Fatalf("expected most packets evicted, got %d", evicted)
	}
	kept := 400 - evicted

	check := func(name string, l, c int) {
		t.Helper()
		if l == 0 {
			t.Fatalf("%s is empty; the test needs it to keep entries", name)
		}
		if c > l+l/2 {
			t.Errorf("%s: len %d, cap %d: more than half the backing array is unused", name, l, c)
		}
	}
	for id, list := range store.byObserver {
		check("byObserver["+id+"]", len(list), cap(list))
	}
	for pt, list := range store.byPayloadType {
		check(fmt.Sprintf("byPayloadType[%d]", pt), len(list), cap(list))
	}
	check("byNode[shared]", len(store.byNode["shared"]), cap(store.byNode["shared"]))
	check("byPathHop[aa]", len(store.byPathHop["aa"]), cap(store.byPathHop["aa"]))
	check("distHops", len(store.distHops), cap(store.distHops))
	check("distPaths", len(store.distPaths), cap(store.distPaths))
	check("packets", len(store.packets), cap(store.packets))
	if len(store.packets) != kept {
		t.Errorf("packets len %d, want %d", len(store.packets), kept)
	}
}
