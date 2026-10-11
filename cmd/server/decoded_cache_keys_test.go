package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const decodedCacheFixture = `{"type":"ADVERT","pubKey":"ab12","name":"Rpt","lat":55.9,"lon":-3.2,` +
	`"flags":{"hasLocation":true,"repeater":true},"timestamp":1760000000,"timestampISO":"2025-10-09T08:53:20Z",` +
	`"signature":"` + "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff" + `",` +
	`"signatureValid":true,"encryptedData":"deadbeef","mac":"cafe","pathData":"0102","ctrlPubKey":"ff","ctrlSubtype":1,` +
	`"ctrlTag":2,"ctrlFlags":"x","ctrlZeroHop":true,"ctrlLength":3}`

// The decoded-payload cache is filled for every stored packet by the
// analytics passes and kept for the packet's lifetime: about 40MB on a busy
// week. A third of the JSON it holds is in fields no server code reads
// (ciphertext, MACs, signatures, ISO copies of timestamps, control-packet
// details), so the cache drops them.
func TestParsedDecodedDropsUnreadKeys(t *testing.T) {
	tx := &StoreTx{DecodedJSON: decodedCacheFixture}
	d := tx.ParsedDecoded()
	for _, k := range decodedCacheDroppedKeys {
		if _, ok := d[k]; ok {
			t.Errorf("the cache keeps %q, which no server code reads", k)
		}
	}
	for _, k := range []string{"type", "pubKey", "name", "lat", "lon", "flags", "timestamp"} {
		if _, ok := d[k]; !ok {
			t.Errorf("the cache lost %q", k)
		}
	}
}

// The live feed still sends the whole payload: it is decoded afresh rather
// than copied from the cache, so the frontend sees every field.
func TestBroadcastPayloadKeepsEveryKey(t *testing.T) {
	tx := &StoreTx{DecodedJSON: decodedCacheFixture}
	tx.ParsedDecoded()
	p := broadcastPayload(tx)
	for _, k := range append([]string{"type", "pubKey", "flags"}, decodedCacheDroppedKeys...) {
		if _, ok := p[k]; !ok {
			t.Errorf("broadcast payload is missing %q", k)
		}
	}
	p["pubKey"] = "changed"
	p["flags"].(map[string]interface{})["repeater"] = false
	if tx.ParsedDecoded()["pubKey"] != "ab12" || tx.ParsedDecoded()["flags"].(map[string]interface{})["repeater"] != true {
		t.Error("changing the broadcast payload changed the cache")
	}
}

// Guard: if server code starts reading one of the dropped keys, it must come
// off the list. Any string literal naming one outside a struct tag fails.
func TestDecodedCacheDroppedKeysAreUnread(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inList := false
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "var decodedCacheDroppedKeys") {
				inList = true
			}
			if inList {
				inList = !strings.HasPrefix(line, "}")
				continue
			}
			if strings.Contains(line, "`json:\"") {
				continue
			}
			for _, k := range decodedCacheDroppedKeys {
				if strings.Contains(line, `"`+k+`"`) {
					t.Errorf("%s:%d names %q, which ParsedDecoded drops: take it off decodedCacheDroppedKeys", f, i+1, k)
				}
			}
		}
	}
}
