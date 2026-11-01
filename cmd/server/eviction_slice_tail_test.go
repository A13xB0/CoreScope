package main

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Eviction filters byObserver, byPayloadType, byNode, distHops and distPaths
// in place. The slots past the new length are still part of the backing
// array, so a pointer left there keeps that transmission or observation (and
// everything it references) alive.
//
// At startup the hot window (newest packets) loads first and the background
// loader appends older chunks after it, so in these lists the oldest packets
// sit at the end. Eviction removes the oldest, which leaves exactly those
// evicted pointers in the tail. On a copy of a live week this kept the live
// heap at ~367 MB after evicting down to 39k packets, against ~388 MB with
// nothing evicted at all.
func TestEvictStale_DropsEvictedPointersFromFilteredSlices(t *testing.T) {
	now := time.Now().UTC()
	// 10 packets, 5h apart, starting 40h ago: the oldest 4 fall outside a
	// 24h retention window and the other 6 stay, so every index keeps a
	// non-empty filtered slice.
	store := makeTestStore(10, now.Add(-40*time.Hour), 300)
	store.retentionHours = 24

	// Every packet also names a shared destination node, so byNode keeps a
	// list that eviction has to filter rather than delete outright.
	for i, tx := range store.packets {
		tx.DecodedJSON = fmt.Sprintf(`{"pubKey":"pk%04d","destPubKey":"shared"}`, i)
		store.byNode["shared"] = append(store.byNode["shared"], tx)
		if store.nodeHashes["shared"] == nil {
			store.nodeHashes["shared"] = make(map[int]struct{})
		}
		store.nodeHashes["shared"][tx.ID] = struct{}{}
	}

	// Startup load order: newest first, older chunks appended behind.
	for _, list := range store.byObserver {
		slices.Reverse(list)
	}
	for _, list := range store.byPayloadType {
		slices.Reverse(list)
	}
	slices.Reverse(store.byNode["shared"])
	slices.Reverse(store.distHops)
	slices.Reverse(store.distPaths)

	evicted := make(map[int]bool)
	for _, tx := range store.packets[:4] {
		evicted[tx.ID] = true
	}

	if n := store.EvictStale(); n != 4 {
		t.Fatalf("expected 4 evicted, got %d", n)
	}

	for id, list := range store.byObserver {
		for i, o := range list[:cap(list)] {
			if o != nil && evicted[o.TransmissionID] {
				t.Errorf("byObserver[%s] slot %d (len %d) still holds evicted observation %d", id, i, len(list), o.ID)
			}
		}
	}
	for pt, list := range store.byPayloadType {
		for i, tx := range list[:cap(list)] {
			if tx != nil && evicted[tx.ID] {
				t.Errorf("byPayloadType[%d] slot %d (len %d) still holds evicted tx %d", pt, i, len(list), tx.ID)
			}
		}
	}
	shared := store.byNode["shared"]
	if len(shared) != 6 {
		t.Fatalf("byNode[shared] has %d entries, want 6", len(shared))
	}
	for i, tx := range shared[:cap(shared)] {
		if tx != nil && evicted[tx.ID] {
			t.Errorf("byNode[shared] slot %d (len %d) still holds evicted tx %d", i, len(shared), tx.ID)
		}
	}
	for i, r := range store.distHops[:cap(store.distHops)] {
		if r.tx != nil && evicted[r.tx.ID] {
			t.Errorf("distHops slot %d (len %d) still holds evicted tx %d", i, len(store.distHops), r.tx.ID)
		}
	}
	for i, r := range store.distPaths[:cap(store.distPaths)] {
		if r.tx != nil && evicted[r.tx.ID] {
			t.Errorf("distPaths slot %d (len %d) still holds evicted tx %d", i, len(store.distPaths), r.tx.ID)
		}
	}
	for i, tx := range store.packets[:cap(store.packets)] {
		if tx != nil && evicted[tx.ID] {
			t.Errorf("packets slot %d (len %d) still holds evicted tx %d", i, len(store.packets), tx.ID)
		}
	}
}
