package main

import "testing"

// relayCandidates runs once per hop of every observation the cold loader
// resolves. With any listener-only observer known it built a filtered copy
// of the candidate nodeInfo structs on every call: about 1 GB allocated over
// one startup on a week of a busy network. The filtered lists are built once
// when the listeners are marked.
func TestRelayCandidatesDoesNotAllocate(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a4aa000000000000000000000000000000000000000000000000000000000001", Role: "repeater"},
		{PublicKey: "a4bb000000000000000000000000000000000000000000000000000000000002", Role: "repeater"},
		{PublicKey: "c1cc000000000000000000000000000000000000000000000000000000000003", Role: "repeater"},
	})
	pm.markNonRelay([]string{"A4BB000000000000000000000000000000000000000000000000000000000002"})

	if got := pm.relayCandidates("A4"); len(got) != 1 || got[0].PublicKey[:4] != "a4aa" {
		t.Fatalf("relayCandidates(A4) = %v, want only the a4aa relay", got)
	}
	if got := pm.relayCandidates("a4bb"); len(got) != 0 {
		t.Fatalf("relayCandidates(a4bb) = %v, want none (the only match is a listener)", got)
	}
	if got := pm.relayCandidates("c1"); len(got) != 1 {
		t.Fatalf("relayCandidates(c1) = %v, want one", got)
	}
	for _, hop := range []string{"a4", "a4bb", "c1", "ff"} {
		if n := testing.AllocsPerRun(100, func() { _ = pm.relayCandidates(hop) }); n != 0 {
			t.Errorf("relayCandidates(%q) allocates %.0f times per call, want 0", hop, n)
		}
	}
}

// Marking more listeners later still filters them.
func TestRelayCandidatesAfterSecondMark(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a4aa000000000000000000000000000000000000000000000000000000000001", Role: "repeater"},
		{PublicKey: "a4bb000000000000000000000000000000000000000000000000000000000002", Role: "repeater"},
	})
	pm.markNonRelay([]string{"a4aa000000000000000000000000000000000000000000000000000000000001"})
	pm.markNonRelay([]string{"a4bb000000000000000000000000000000000000000000000000000000000002"})
	if got := pm.relayCandidates("a4"); len(got) != 0 {
		t.Fatalf("relayCandidates(a4) = %v, want none after both are marked", got)
	}
}
