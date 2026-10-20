package main

import "testing"

func hopContextFixture() (*StoreTx, *prefixMap) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "aa11000000000000000000000000000000000000000000000000000000000001", Role: "repeater"},
		{PublicKey: "bb22000000000000000000000000000000000000000000000000000000000002", Role: "repeater"},
		{PublicKey: "bb33000000000000000000000000000000000000000000000000000000000003", Role: "repeater"},
	})
	tx := &StoreTx{
		DecodedJSON: `{"pubKey":"cc44000000000000000000000000000000000000000000000000000000000004"}`,
		ObserverID:  "DD55000000000000000000000000000000000000000000000000000000000005",
		PathJSON:    `["AA","BB","aa","AA11"]`,
	}
	tx.ParsedDecoded()
	txGetParsedPath(tx)
	return tx, pm
}

// buildHopContextPubkeys runs per transmission in every topology and
// distance pass and per matched packet in subpath detail. Its de-dup map,
// sized for 16 keys, was allocated on every call (150MB of churn per
// analytics recompute on a busy week) to de-duplicate a handful of keys,
// and live hops and observer IDs are upper-case, so each was lowered into
// a new string just to look it up. The fixture is cased like live data.
func TestBuildHopContextPubkeysAllocatesOnlyItsResult(t *testing.T) {
	tx, pm := hopContextFixture()
	// The result slice, and the lowered observer ID it holds.
	if n := testing.AllocsPerRun(200, func() { _ = buildHopContextPubkeys(tx, pm) }); n > 2 {
		t.Fatalf("buildHopContextPubkeys allocates %.0f times per call, want at most 2", n)
	}
}

// Same keys, same order: sender, observer, then unambiguous hop anchors,
// lower-cased and de-duplicated.
func TestBuildHopContextPubkeysOrderAndDedup(t *testing.T) {
	tx, pm := hopContextFixture()
	got := buildHopContextPubkeys(tx, pm)
	want := []string{
		"cc44000000000000000000000000000000000000000000000000000000000004",
		"dd55000000000000000000000000000000000000000000000000000000000005",
		"aa11000000000000000000000000000000000000000000000000000000000001",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Most transmissions carry no "pubKey" (on a live week ~80% are text,
// group and control packets). buildHopContextPubkeys only reads that key,
// but going through ParsedDecoded decoded and cached the whole payload as a
// map for every one of them, and the cache lives as long as the packet:
// ~15 MB of maps on a 39k-transmission store that nothing else reads.
func TestBuildHopContextPubkeysLeavesPayloadsWithoutPubKeyUndecoded(t *testing.T) {
	_, pm := hopContextFixture()
	tx := &StoreTx{
		DecodedJSON: `{"type":"TXT_MSG","destHash":"a1","srcHash":"b2"}`,
		ObserverID:  "DD55000000000000000000000000000000000000000000000000000000000005",
		PathJSON:    `["AA11"]`,
	}
	got := buildHopContextPubkeys(tx, pm)
	if tx.parsedDecoded != nil {
		t.Errorf("payload without pubKey was decoded and cached: %v", tx.parsedDecoded)
	}
	want := []string{
		"dd55000000000000000000000000000000000000000000000000000000000005",
		"aa11000000000000000000000000000000000000000000000000000000000001",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
