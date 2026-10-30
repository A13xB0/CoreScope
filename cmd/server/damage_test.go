package main

import (
	"encoding/hex"
	"testing"
)

// The genuine and damaged payloads from the meshcore.co.uk post: byte 27
// changed from 57 to C7, two bits in one nibble.
const (
	postGenuine = "34B82DB71B72B2293A421F37EAF1EC8E6C9A394C365D7F26D759D557D19DCB4A1434E9"
	postDamaged = "34B82DB71B72B2293A421F37EAF1EC8E6C9A394C365D7F26D759D5C7D19DCB4A1434E9"
)

var damageTxID = 0

// floodTx builds a FLOOD transmission of payloadType with a path of hops
// one-byte hashes and the given payload.
func floodTx(payloadType int, payloadHex, firstSeen string, hops int) *StoreTx {
	damageTxID++
	raw := []byte{byte(payloadType<<2 | 1), byte(hops)}
	for i := 0; i < hops; i++ {
		raw = append(raw, byte(0xA0+i))
	}
	p, _ := hex.DecodeString(payloadHex)
	raw = append(raw, p...)
	pt, rt := payloadType, 1
	return &StoreTx{
		ID:          damageTxID,
		RawHex:      hex.EncodeToString(raw),
		Hash:        hex.EncodeToString([]byte{byte(damageTxID >> 8), byte(damageTxID)}),
		FirstSeen:   firstSeen,
		PayloadType: &pt,
		RouteType:   &rt,
	}
}

func TestDamagePostExample(t *testing.T) {
	d := newDamageIndex()
	genuine := floodTx(PayloadGRP_TXT, postGenuine, "2026-10-28T03:18:48Z", 0)
	damaged := floodTx(PayloadGRP_TXT, postDamaged, "2026-10-28T03:19:10Z", 17)
	d.add(genuine)
	d.add(damaged)
	if genuine.Damage != nil {
		t.Fatalf("genuine packet marked damaged: %+v", genuine.Damage)
	}
	g := damaged.Damage
	if g == nil {
		t.Fatal("damaged copy not detected")
	}
	if g.Of != genuine.Hash || g.Kind != "body" || g.Bits != 2 || len(g.Offsets) != 1 || g.Offsets[0] != 27 {
		t.Errorf("got %+v, want copy of %s, body, 2 bits at byte 27", g, genuine.Hash)
	}
}

// The damaged copy can be logged before the genuine one; the later of the two
// is the one marked.
func TestDamageMarksTheLaterCopy(t *testing.T) {
	d := newDamageIndex()
	damaged := floodTx(PayloadGRP_TXT, postDamaged, "2026-10-28T03:19:10Z", 17)
	genuine := floodTx(PayloadGRP_TXT, postGenuine, "2026-10-28T03:18:48Z", 0)
	d.add(damaged)
	d.add(genuine)
	if genuine.Damage != nil || damaged.Damage == nil || damaged.Damage.Of != genuine.Hash {
		t.Errorf("genuine %+v, damaged %+v", genuine.Damage, damaged.Damage)
	}
}

func TestDamageKinds(t *testing.T) {
	tail := postGenuine[:len(postGenuine)-2] + "E8" // last byte E9 -> E8
	longer := postGenuine + "15"
	shorter := postGenuine[:len(postGenuine)-2]
	for _, c := range []struct{ name, copy, kind string }{
		{"tail", tail, "tail"},
		{"longer", longer, "longer"},
		{"shorter", shorter, "shorter"},
	} {
		d := newDamageIndex()
		genuine := floodTx(PayloadTXT_MSG, postGenuine, "2026-10-28T03:18:48Z", 0)
		copy := floodTx(PayloadTXT_MSG, c.copy, "2026-10-28T03:18:58Z", 3)
		d.add(genuine)
		d.add(copy)
		if copy.Damage == nil || copy.Damage.Kind != c.kind {
			t.Errorf("%s: got %+v", c.name, copy.Damage)
		}
	}
}

func TestDamageNotFlagged(t *testing.T) {
	// A different message of the same length: AES output, far apart.
	other := "34B82D" + "0F1E2D3C4B5A69788796A5B4C3D2E1F00112233445566778899AABBCCDDEEFF0"
	for _, c := range []struct {
		name     string
		pt       int
		a, b     string
		at1, at2 string
	}{
		{"unrelated", PayloadGRP_TXT, postGenuine, other, "2026-10-28T03:18:48Z", "2026-10-28T03:18:50Z"},
		{"outside the window", PayloadGRP_TXT, postGenuine, postDamaged, "2026-10-28T03:18:48Z", "2026-10-28T03:21:49Z"},
		{"not encrypted", PayloadADVERT, postGenuine, postDamaged, "2026-10-28T03:18:48Z", "2026-10-28T03:18:50Z"},
		{"too short", PayloadGRP_TXT, postGenuine[:20], postDamaged[:18] + "00", "2026-10-28T03:18:48Z", "2026-10-28T03:18:50Z"},
	} {
		d := newDamageIndex()
		a := floodTx(c.pt, c.a, c.at1, 0)
		b := floodTx(c.pt, c.b, c.at2, 4)
		d.add(a)
		d.add(b)
		if a.Damage != nil || b.Damage != nil {
			t.Errorf("%s: flagged %+v %+v", c.name, a.Damage, b.Damage)
		}
	}
}

// A damaged copy of a damaged copy points at the genuine packet, and a
// copy already marked is never a parent.
func TestDamageChainsPointAtTheGenuine(t *testing.T) {
	d := newDamageIndex()
	genuine := floodTx(PayloadGRP_TXT, postGenuine, "2026-10-28T03:18:48Z", 0)
	first := floodTx(PayloadGRP_TXT, postDamaged, "2026-10-28T03:19:10Z", 17)
	second := floodTx(PayloadGRP_TXT, postDamaged[:len(postDamaged)-2]+"E8", "2026-10-28T03:19:30Z", 25)
	for _, tx := range []*StoreTx{genuine, first, second} {
		d.add(tx)
	}
	if second.Damage == nil || second.Damage.Of != genuine.Hash || second.Damage.Bits != 3 {
		t.Errorf("second: %+v", second.Damage)
	}
}

func TestDamageRemove(t *testing.T) {
	d := newDamageIndex()
	genuine := floodTx(PayloadGRP_TXT, postGenuine, "2026-10-28T03:18:48Z", 0)
	d.add(genuine)
	d.remove(genuine)
	if len(d.buckets) != 0 || len(d.byTx) != 0 {
		t.Fatalf("index not empty: %d buckets, %d txs", len(d.buckets), len(d.byTx))
	}
	damaged := floodTx(PayloadGRP_TXT, postDamaged, "2026-10-28T03:19:10Z", 17)
	d.add(damaged)
	if damaged.Damage != nil {
		t.Error("matched an evicted packet")
	}
}

func TestDamagedPacketFilterAndAPIShape(t *testing.T) {
	s := NewPacketStore(nil, nil)
	genuine := floodTx(PayloadGRP_TXT, postGenuine, "2026-10-28T03:18:48Z", 0)
	damaged := floodTx(PayloadGRP_TXT, postDamaged, "2026-10-28T03:19:10Z", 17)
	for _, tx := range []*StoreTx{genuine, damaged} {
		s.packets = append(s.packets, tx)
		s.byHash[tx.Hash] = tx
		s.damage.add(tx)
	}
	got := s.filterPackets(PacketQuery{Damaged: true}, packetFilterSets{})
	if len(got) != 1 || got[0] != damaged {
		t.Fatalf("damaged filter returned %d packets", len(got))
	}
	if all := s.filterPackets(PacketQuery{}, packetFilterSets{}); len(all) != 2 {
		t.Fatalf("unfiltered returned %d packets", len(all))
	}
	m := txToMap(damaged)
	dm, ok := m["damage"].(map[string]interface{})
	if !ok || dm["of"] != genuine.Hash || dm["kind"] != "body" || dm["bits"] != 2 {
		t.Errorf("txToMap damage = %#v", m["damage"])
	}
	if _, ok := txToMap(genuine)["damage"]; ok {
		t.Error("genuine packet carries a damage field")
	}
	page := groupedRowsToPage([]groupedRow{snapshotGroupedRow(damaged)}, 1)
	if _, ok := page.Packets[0]["damage"]; !ok {
		t.Error("grouped row has no damage field")
	}
}
