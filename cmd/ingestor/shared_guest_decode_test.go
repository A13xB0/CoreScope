package main

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// A real shared-key RESPONSE (the crypto test's vector) decoded end to end:
// with the repeater known, Handle stores a battery reading credited to the
// observer, marked key_mode = shared.
func TestSharedGuestHandleStoresReading(t *testing.T) {
	s := newTestStore(t)
	repPub := vecRepPub // 8f84eae6…, first byte 0x8f
	if err := s.UpsertNode(repPub, "Test Repeater", "repeater", nil, nil,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	d := newSharedGuestDecoder(s)
	if d == nil {
		t.Fatal("decoder nil")
	}
	payload := hexb(t, vecRespPay) // f0 8f 98 1d + ciphertext
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	pkt.Payload.DestHash = hex.EncodeToString(payload[0:1])
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])

	// The same reply, heard by three different observers, is one reading.
	for _, obs := range []string{
		"7e76aa00112233445566778899aabbccddeeff00112233445566778899009400",
		"aabbcc00112233445566778899aabbccddeeff00112233445566778899001122",
		"ccddee00112233445566778899aabbccddeeff00112233445566778899003344",
	} {
		d.Handle(obs, pkt, time.Unix(int64(vecTag), 30).UTC())
	}

	if batteryRows(t, s) != 1 {
		t.Fatalf("battery rows = %d, want 1 (one reading per reply)", batteryRows(t, s))
	}
	// One reading, but all three observers are recorded as having heard it.
	var heard int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM neighbor_battery_heard WHERE node_id = ?`, repPub).Scan(&heard); err != nil {
		t.Fatal(err)
	}
	if heard != 3 {
		t.Errorf("heard rows = %d, want 3 (one per observer that overheard the reply)", heard)
	}
	var nodeID, obs, keyMode, sampledAt string
	var mv int
	err := s.db.QueryRow(`SELECT node_id, observer_id, battery_mv, COALESCE(key_mode,''), sampled_at FROM neighbor_battery`).
		Scan(&nodeID, &obs, &mv, &keyMode, &sampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != repPub {
		t.Errorf("node=%s", nodeID)
	}
	if obs != d.sharedObs { // credited to the shared key, not an observer
		t.Errorf("observer=%s, want the shared key %s", obs, d.sharedObs)
	}
	if mv != vecBatteryV {
		t.Errorf("battery_mv = %d, want %d", mv, vecBatteryV)
	}
	if keyMode != "shared" {
		t.Errorf("key_mode = %q, want shared", keyMode)
	}
	// Timed by the reply's tag, not ingest time, so every observer agrees.
	if want := time.Unix(int64(vecTag), 0).UTC().Format(batteryTimeLayout); sampledAt != want {
		t.Errorf("sampled_at = %s, want %s (the tag time)", sampledAt, want)
	}
}

// A packet not addressed to the shared key, or with no known repeater, stores
// nothing.
func TestSharedGuestHandleIgnoresOthers(t *testing.T) {
	s := newTestStore(t)
	d := newSharedGuestDecoder(s)
	payload := hexb(t, vecRespPay)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	// Wrong dest hash: not the shared key.
	pkt.Payload.DestHash = "11"
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])
	d.Handle("7e76aa00112233445566778899aabbccddeeff00112233445566778899009400", pkt, time.Now().UTC())
	if batteryRows(t, s) != 0 {
		t.Fatalf("stored a reading for a non-shared packet")
	}
}

// With a claim in place, a decoded reply is credited to the poller that
// requested it, not the shared-key fallback.
func TestSharedGuestClaimCreditsThePoller(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertNode(vecRepPub, "Test Repeater", "repeater", nil, nil,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	d := newSharedGuestDecoder(s)
	poller := "aa11bb00112233445566778899aabbccddeeff00112233445566778899001122"
	d.HandleClaim(poller, map[string]interface{}{
		"origin_id": poller,
		"nodes":     []interface{}{map[string]interface{}{"node_id": vecRepPub, "reply_hops": float64(1), "via": strings.Repeat("ab", 32)}},
	}, time.Unix(int64(vecTag), 10).UTC())

	payload := hexb(t, vecRespPay)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	pkt.Payload.DestHash = hex.EncodeToString(payload[0:1])
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])
	// Overheard by a different observer than the poller.
	d.Handle("ccddee00112233445566778899aabbccddeeff00112233445566778899003344", pkt, time.Unix(int64(vecTag), 30).UTC())

	var obs, via string
	var hops int
	if err := s.db.QueryRow(`SELECT observer_id, reply_hops, COALESCE(via,'') FROM neighbor_battery`).
		Scan(&obs, &hops, &via); err != nil {
		t.Fatal(err)
	}
	if obs != poller {
		t.Errorf("observer = %s, want the poller %s", obs, poller)
	}
	// The route comes from the claim, not the overheard reply.
	if hops != 1 || via != strings.Repeat("ab", 32) {
		t.Errorf("route = %d hops via %s, want 1 via the claimed relay", hops, via)
	}
}
