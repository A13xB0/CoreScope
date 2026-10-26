package main

import (
	"crypto/aes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gkEncryptReply builds the RESPONSE a repeater sends to a polling key: the
// plugin's encrypt_then_mac, mirrored — AES-128-ECB (key = secret[:16]) over
// the zero-padded plaintext, then the first 2 bytes of HMAC-SHA256(secret,
// ciphertext) in front. The plaintext is the reflected tag and a
// RepeaterStats reply carrying battery_mv.
func gkEncryptReply(t *testing.T, seed, repPub []byte, tag uint32, mv uint16) *DecodedPacket {
	t.Helper()
	secret := sharedGuestSecret(seed, repPub)
	plain := make([]byte, 16)
	binary.LittleEndian.PutUint32(plain[0:], tag)
	binary.LittleEndian.PutUint16(plain[4:], mv)
	block, err := aes.NewCipher(secret[:16])
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)[:2]
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	pkt.Payload.DestHash = hex.EncodeToString(pub[:1])
	pkt.Payload.SrcHash = hex.EncodeToString(repPub[:1])
	pkt.Payload.MAC = hex.EncodeToString(mac)
	pkt.Payload.EncryptedData = hex.EncodeToString(ct)
	return pkt
}

// gkKeysDB creates a guest_keys.db the way the server does and returns its
// path and a writable handle for the test to change it.
func gkKeysDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := guestKeysDBPath(filepath.Join(t.TempDir(), "meshcore.db"))
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE guest_keys (observer_id TEXT PRIMARY KEY, public_key TEXT NOT NULL,
		private_key TEXT NOT NULL, registered_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func gkRegister(t *testing.T, db *sql.DB, observer string, seed []byte, at time.Time) {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	ts := at.UTC().Format(batteryTimeLayout)
	if _, err := db.Exec(`INSERT OR REPLACE INTO guest_keys VALUES (?,?,?,?,?)`,
		observer, hex.EncodeToString(pub), hex.EncodeToString(seed), ts, ts); err != nil {
		t.Fatal(err)
	}
}

// gkSeedsSharingByte returns two polling-key seeds whose public keys share a
// first byte (the dest hash), neither equal to the shared key's.
func gkSeedsSharingByte(t *testing.T) ([]byte, []byte) {
	t.Helper()
	seen := map[byte][]byte{}
	for i := 0; i < 100000; i++ {
		_, priv, _ := ed25519.GenerateKey(nil)
		b := priv.Public().(ed25519.PublicKey)[0]
		if b == 0xf0 {
			continue
		}
		if other, ok := seen[b]; ok {
			return other, priv.Seed()
		}
		seen[b] = priv.Seed()
	}
	t.Fatal("no two keys share a first byte")
	return nil, nil
}

func gkRepeater(t *testing.T, s *Store) []byte {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := s.UpsertNode(hex.EncodeToString(pub), "Guest Test Repeater", "repeater", nil, nil,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	return pub
}

type gkRow struct {
	observer, keyMode, via string
	mv, hops               int
}

func gkRows(t *testing.T, s *Store, node []byte) []gkRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT observer_id, COALESCE(key_mode,''), COALESCE(via,''), battery_mv, reply_hops
		FROM neighbor_battery WHERE node_id = ? ORDER BY sampled_at`, hex.EncodeToString(node))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []gkRow
	for rows.Next() {
		var r gkRow
		if err := rows.Scan(&r.observer, &r.keyMode, &r.via, &r.mv, &r.hops); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

const (
	gkObsA    = "a0a0a00011223344556677889900aabbccddeeff00112233445566778899aaaa"
	gkObsB    = "b0b0b00011223344556677889900aabbccddeeff00112233445566778899bbbb"
	gkHearer  = "ccddee00112233445566778899aabbccddeeff00112233445566778899003344"
	gkRelayID = "abababababababababababababababababababababababababababababababab"
)

// A reply to an observer's registered key is decoded and credited to that
// observer, with the route from its own claim — even when another observer
// claimed the node more recently. Two keys sharing a dest hash both decode.
func TestGuestKeyRepliesCreditTheOwner(t *testing.T) {
	s := newTestStore(t)
	rep := gkRepeater(t, s)
	seedA, seedB := gkSeedsSharingByte(t)
	path, kdb := gkKeysDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	gkRegister(t, kdb, gkObsA, seedA, now)
	gkRegister(t, kdb, gkObsB, seedB, now)

	d := newSharedGuestDecoder(s)
	d.UseGuestKeys(path)
	node := hex.EncodeToString(rep)
	d.HandleClaim(gkObsA, map[string]interface{}{"nodes": []interface{}{
		map[string]interface{}{"node_id": node, "reply_hops": float64(1), "via": gkRelayID}}}, now.Add(-2*time.Minute))
	d.HandleClaim(gkObsB, map[string]interface{}{"nodes": []interface{}{
		map[string]interface{}{"node_id": node, "reply_hops": float64(3)}}}, now.Add(-1*time.Minute))

	d.Handle(gkHearer, gkEncryptReply(t, seedA, rep, uint32(now.Unix()-60), 3950), now)
	d.Handle(gkHearer, gkEncryptReply(t, seedB, rep, uint32(now.Unix()-30), 3940), now)

	got := gkRows(t, s, rep)
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want one per key", got)
	}
	if want := (gkRow{observer: gkObsA, keyMode: "shared", via: gkRelayID, mv: 3950, hops: 1}); got[0] != want {
		t.Errorf("reply to A's key = %+v, want %+v", got[0], want)
	}
	if want := (gkRow{observer: gkObsB, keyMode: "shared", mv: 3940, hops: 3}); got[1] != want {
		t.Errorf("reply to B's key = %+v, want %+v", got[1], want)
	}
}

// A key removed from guest_keys.db (lapsed, or replaced) stops decoding at the
// next reload; a missing file is tolerated and picked up once it appears.
func TestGuestKeyRemovedStopsDecoding(t *testing.T) {
	s := newTestStore(t)
	rep := gkRepeater(t, s)
	_, seed, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	missing := guestKeysDBPath(filepath.Join(dir, "meshcore.db"))
	now := time.Now().UTC().Truncate(time.Second)

	d := newSharedGuestDecoder(s)
	d.UseGuestKeys(missing) // not there yet: nothing but the shared key
	d.Handle(gkHearer, gkEncryptReply(t, seed.Seed(), rep, uint32(now.Unix()-90), 3900), now)
	if n := len(gkRows(t, s, rep)); n != 0 {
		t.Fatalf("decoded under an unregistered key: %d rows", n)
	}

	path, kdb := gkKeysDB(t)
	d.guest.path = path
	gkRegister(t, kdb, gkObsA, seed.Seed(), now)
	d.RefreshGuestKeys(now)
	d.Handle(gkHearer, gkEncryptReply(t, seed.Seed(), rep, uint32(now.Unix()-60), 3900), now)
	if got := gkRows(t, s, rep); len(got) != 1 || got[0].observer != gkObsA {
		t.Fatalf("after registering: %+v, want one row credited to A", got)
	}

	if _, err := kdb.Exec(`DELETE FROM guest_keys`); err != nil {
		t.Fatal(err)
	}
	d.RefreshGuestKeys(now)
	d.Handle(gkHearer, gkEncryptReply(t, seed.Seed(), rep, uint32(now.Unix()-30), 3800), now)
	if got := gkRows(t, s, rep); len(got) != 1 {
		t.Errorf("after removal: %+v, want no new row", got)
	}

	// A key whose updated_at is over 24 h old is ignored even before the
	// server prunes it.
	gkRegister(t, kdb, gkObsA, seed.Seed(), now.Add(-25*time.Hour))
	d.RefreshGuestKeys(now)
	if keys := d.keysFor(ed25519.NewKeyFromSeed(seed.Seed()).Public().(ed25519.PublicKey)[0]); len(keys) > 1 ||
		(len(keys) == 1 && keys[0].owner != "") {
		t.Errorf("lapsed key still in force: %+v", keys)
	}
}

// With per-observer keys loaded the shared key still decodes, credited to the
// most recent claimant.
func TestGuestKeySharedKeyStillWorks(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertNode(vecRepPub, "Test Repeater", "repeater", nil, nil,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	path, kdb := gkKeysDB(t)
	_, other, _ := ed25519.GenerateKey(nil)
	gkRegister(t, kdb, gkObsA, other.Seed(), time.Unix(int64(vecTag), 0))
	d := newSharedGuestDecoder(s)
	d.UseGuestKeys(path)
	at := time.Unix(int64(vecTag), 0).UTC()
	d.HandleClaim(gkObsA, map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"node_id": vecRepPub}}}, at)
	d.HandleClaim(gkObsB, map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"node_id": vecRepPub, "reply_hops": float64(2)}}}, at.Add(time.Second))

	payload := hexb(t, vecRespPay)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	pkt.Payload.DestHash = hex.EncodeToString(payload[0:1])
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])
	d.Handle(gkHearer, pkt, at.Add(30*time.Second))

	got := gkRows(t, s, hexb(t, vecRepPub))
	if len(got) != 1 || got[0].observer != gkObsB || got[0].hops != 2 || got[0].mv != vecBatteryV {
		t.Errorf("shared-key reply = %+v, want one row credited to the latest claimant B", got)
	}
}

// Each /polling claim is kept in guest_polling_claims: first_seen survives a
// renewal, the route follows the newest claim, and rows not renewed for 24 h
// are pruned.
func TestGuestPollingClaimsPersisted(t *testing.T) {
	s := newTestStore(t)
	d := newSharedGuestDecoder(s)
	node := strings.Repeat("9a", 32)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-30 * time.Hour)
	claim := func(obs string, hops float64, at time.Time) {
		d.HandleClaim(obs, map[string]interface{}{"origin_id": obs, "nodes": []interface{}{
			map[string]interface{}{"node_id": node, "reply_hops": hops}}}, at)
	}
	claim(gkObsA, 2, t0)
	claim(gkObsB, 1, t0)
	claim(gkObsA, 0, t0.Add(29*time.Hour))

	var first, last string
	var hops, n int
	if err := s.db.QueryRow(`SELECT first_seen, last_seen, reply_hops FROM guest_polling_claims WHERE node_id = ? AND observer_id = ?`,
		node, gkObsA).Scan(&first, &last, &hops); err != nil {
		t.Fatal(err)
	}
	if first != t0.Format(batteryTimeLayout) || last != t0.Add(29*time.Hour).Format(batteryTimeLayout) || hops != 0 {
		t.Errorf("A's claim = first %s last %s hops %d", first, last, hops)
	}
	// The renewal 29 h on was the first claim in over an hour, so it pruned
	// B's claim, last renewed 29 h before.
	s.db.QueryRow(`SELECT COUNT(*) FROM guest_polling_claims`).Scan(&n)
	if n != 1 {
		t.Errorf("claims after prune = %d, want 1 (B's lapsed)", n)
	}
	// A spoofed origin_id stores nothing.
	d.HandleClaim(gkObsB, map[string]interface{}{"origin_id": gkObsA, "nodes": []interface{}{
		map[string]interface{}{"node_id": node}}}, t0.Add(29*time.Hour))
	s.db.QueryRow(`SELECT COUNT(*) FROM guest_polling_claims`).Scan(&n)
	if n != 1 {
		t.Errorf("spoofed claim stored: %d rows", n)
	}
}
