package main

import "testing"

// extractFromNode runs over every packet on each neighbor-graph rebuild.
// ParsedDecoded caches the decoded map on the transmission for its whole life
// in the store, so calling it for payloads that carry none of the fields read
// here kept a map per packet alive for nothing (20 MB on pre-prod).
func TestExtractFromNodeLeavesPayloadsWithoutOriginUndecoded(t *testing.T) {
	txt := PayloadTXT_MSG
	tx := &StoreTx{PayloadType: &txt, DecodedJSON: `{"type":"TXT_MSG","destHash":"a1","srcHash":"b2"}`}
	if got := extractFromNode(tx); got != "" {
		t.Errorf("extractFromNode = %q, want empty", got)
	}
	if tx.parsedDecoded != nil {
		t.Error("a payload with no origin field was decoded and cached")
	}

	adv := PayloadADVERT
	for _, c := range []struct {
		pt   *int
		json string
		want string
	}{
		{&adv, `{"type":"ADVERT","pubKey":"aabb"}`, "aabb"},
		{&txt, `{"from_node":"ccdd"}`, "ccdd"},
		{&txt, `{"from":"eeff"}`, "eeff"},
	} {
		tx := &StoreTx{PayloadType: c.pt, DecodedJSON: c.json}
		if got := extractFromNode(tx); got != c.want {
			t.Errorf("extractFromNode(%s) = %q, want %q", c.json, got, c.want)
		}
	}
	anon := PayloadANON_REQ
	tx = &StoreTx{PayloadType: &anon, DecodedJSON: `{"ephemeralPubKey":"1122"}`}
	if got := extractFromNode(tx); got != "1122" {
		t.Errorf("ANON_REQ extractFromNode = %q, want 1122", got)
	}
}
