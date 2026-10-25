package main

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// Vectors produced by openhop_core (the plugin's crypto): same shared seed,
// repeater key, ECDH secret and an encrypted RESPONSE payload. If Go's
// standard-library ECDH reproduces the secret and decrypts the payload, the
// shared-key decoder is byte-compatible with what repeaters and the plugin do.
const (
	vecSeed     = "8dbaa94fbcff9dddc124533648f27adb42ddd30c264ab0e4a3f4a24505f533d4"
	vecRepPub   = "8f84eae602bedace6a04614f9325511688f6eec8341430cdcb53c4b6e227565d"
	vecSecret   = "839454274ee0f39ff2f549c46911b324139116750fab469c765f209a92951308"
	vecRespPay  = "f08f151a92dc3c127cc89d08266895dd75947615cc8dbf3b0f32e000f9c1689469de4b8d952b5f8e0ea494c6feedd5aa166b1e81e90b525b307d85d5cc579464b9b5c349"
	vecTag      = 1792953600
	vecBatteryV = 4053
)

func hexb(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSharedGuestSecretMatchesOpenhopCore(t *testing.T) {
	got := sharedGuestSecret(hexb(t, vecSeed), hexb(t, vecRepPub))
	if hex.EncodeToString(got) != vecSecret {
		t.Fatalf("secret = %s, want %s", hex.EncodeToString(got), vecSecret)
	}
}

func TestSharedGuestDecryptResponse(t *testing.T) {
	secret := hexb(t, vecSecret)
	payload := hexb(t, vecRespPay)
	// payload = dest_hash(1) + src_hash(1) + MAC(2) + ciphertext
	plaintext, ok := sharedGuestDecrypt(secret, payload[2:])
	if !ok {
		t.Fatal("decrypt/MAC failed")
	}
	tag := binary.LittleEndian.Uint32(plaintext[:4])
	if tag != vecTag {
		t.Fatalf("tag = %#x, want %#x", tag, vecTag)
	}
	batt := binary.LittleEndian.Uint16(plaintext[4:6]) // first RepeaterStats field
	if int(batt) != vecBatteryV {
		t.Fatalf("battery_mv = %d, want %d", batt, vecBatteryV)
	}
}
