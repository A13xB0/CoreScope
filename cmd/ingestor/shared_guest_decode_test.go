package main

import (
	"encoding/hex"
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
	d := newSharedGuestDecoder(s, nil)
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

	observer := "7e76aa00112233445566778899aabbccddeeff00112233445566778899009400"
	d.Handle(observer, pkt, nil, nil, time.Now().UTC())

	if batteryRows(t, s) != 1 {
		t.Fatalf("battery rows = %d, want 1", batteryRows(t, s))
	}
	var nodeID, obs, keyMode string
	var mv int
	err := s.db.QueryRow(`SELECT node_id, observer_id, battery_mv, COALESCE(key_mode,'') FROM neighbor_battery`).
		Scan(&nodeID, &obs, &mv, &keyMode)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != repPub || obs != observer {
		t.Errorf("node=%s obs=%s", nodeID, obs)
	}
	if mv != vecBatteryV {
		t.Errorf("battery_mv = %d, want %d", mv, vecBatteryV)
	}
	if keyMode != "shared" {
		t.Errorf("key_mode = %q, want shared", keyMode)
	}
}

// A packet not addressed to the shared key, or with no known repeater, stores
// nothing.
func TestSharedGuestHandleIgnoresOthers(t *testing.T) {
	s := newTestStore(t)
	d := newSharedGuestDecoder(s, nil)
	payload := hexb(t, vecRespPay)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	// Wrong dest hash: not the shared key.
	pkt.Payload.DestHash = "11"
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])
	d.Handle("7e76aa00112233445566778899aabbccddeeff00112233445566778899009400", pkt, nil, nil, time.Now().UTC())
	if batteryRows(t, s) != 0 {
		t.Fatalf("stored a reading for a non-shared packet")
	}
}
