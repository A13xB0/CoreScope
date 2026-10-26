package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/guestkeys"
)

// gkFixture is a server with neighborBattery on, a guest_keys.db in a temp
// dir and one known observer whose identity key signs the uploads.
type gkFixture struct {
	srv    *Server
	router *mux.Router
	obsPub string
	obsKey ed25519.PrivateKey
	now    time.Time
}

func newGKFixture(t *testing.T) *gkFixture {
	t.Helper()
	db := setupTestDB(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := hex.EncodeToString(pub)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'Pole OBS', ?, ?)`,
		strings.ToUpper(obs), now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, &Config{Port: 3000, NeighborBattery: &NeighborBatteryServerConfig{Enabled: true}}, NewHub())
	st, err := guestkeys.Open(guestkeys.Path(filepath.Join(t.TempDir(), "meshcore.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &gkFixture{srv: srv, obsPub: obs, obsKey: priv, now: now}
	g := &guestKeyStore{Store: st, now: func() time.Time { return f.now }}
	srv.guestKeys = g
	f.router = mux.NewRouter()
	srv.RegisterRoutes(f.router)
	return f
}

// upload builds a correctly signed body for a fresh polling key, lets mutate
// adjust it, and POSTs it.
func (f *gkFixture) upload(t *testing.T, mutate func(m map[string]interface{})) (int, map[string]interface{}, string) {
	t.Helper()
	_, gPriv, _ := ed25519.GenerateKey(nil)
	gPub := hex.EncodeToString(gPriv.Public().(ed25519.PublicKey))
	ts := f.now.Unix()
	msg := fmt.Sprintf("corescope-guest-key/v1|%s|%s|%d", f.obsPub, gPub, ts)
	body := map[string]interface{}{
		"observer_id": f.obsPub,
		"public_key":  gPub,
		"private_key": hex.EncodeToString(gPriv.Seed()),
		"timestamp":   ts,
		"signature":   hex.EncodeToString(ed25519.Sign(f.obsKey, []byte(msg))),
	}
	if mutate != nil {
		mutate(body)
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("POST", "/api/guest-keys", bytes.NewReader(raw)))
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func (f *gkFixture) get(t *testing.T, obs string) (int, map[string]interface{}, string) {
	t.Helper()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/guest-keys/"+obs, nil))
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// sign re-signs body for its current observer/public key/timestamp.
func (f *gkFixture) sign(m map[string]interface{}) {
	msg := fmt.Sprintf("corescope-guest-key/v1|%s|%s|%d", m["observer_id"], m["public_key"], m["timestamp"])
	m["signature"] = hex.EncodeToString(ed25519.Sign(f.obsKey, []byte(msg)))
}

func TestGuestKeyUploadHappyPath(t *testing.T) {
	f := newGKFixture(t)
	code, out, raw := f.upload(t, nil)
	if code != 200 || out["ok"] != true {
		t.Fatalf("status %d: %s", code, raw)
	}
	if want := f.now.Add(24 * time.Hour).Format(time.RFC3339); out["expires_at"] != want {
		t.Errorf("expires_at = %v, want %s", out["expires_at"], want)
	}
	if strings.Contains(raw, "private_key") {
		t.Errorf("upload response mentions the private key: %s", raw)
	}
	var priv string
	if err := f.srv.guestKeys.DB().QueryRow(`SELECT private_key FROM guest_keys WHERE observer_id = ?`, f.obsPub).Scan(&priv); err != nil || len(priv) != 64 {
		t.Fatalf("stored private key = %q, %v", priv, err)
	}

	code, got, raw := f.get(t, f.obsPub)
	if code != 200 || got["registered"] != true || got["public_key"] != out["public_key"] {
		t.Fatalf("GET %d: %s", code, raw)
	}
	if strings.Contains(raw, "private") || strings.Contains(raw, priv) {
		t.Errorf("GET leaks the private key: %s", raw)
	}

	// A replacement a minute later keeps registered_at and bumps updated_at.
	f.now = f.now.Add(61 * time.Second)
	code, out2, raw := f.upload(t, nil)
	if code != 200 {
		t.Fatalf("replacement %d: %s", code, raw)
	}
	var n int
	var reg, upd, pub string
	f.srv.guestKeys.DB().QueryRow(`SELECT COUNT(*) FROM guest_keys`).Scan(&n)
	f.srv.guestKeys.DB().QueryRow(`SELECT registered_at, updated_at, public_key FROM guest_keys`).Scan(&reg, &upd, &pub)
	if n != 1 || pub != out2["public_key"] || reg == upd {
		t.Errorf("after replacement: rows=%d pub=%s registered_at=%s updated_at=%s", n, pub, reg, upd)
	}
}

func TestGuestKeyUploadRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *gkFixture, m map[string]interface{})
		code   int
	}{
		{"bad hex", func(f *gkFixture, m map[string]interface{}) { m["public_key"] = "zz" }, 400},
		{"stale timestamp", func(f *gkFixture, m map[string]interface{}) {
			m["timestamp"] = f.now.Add(-11 * time.Minute).Unix()
			f.sign(m)
		}, 401},
		{"bad signature", func(f *gkFixture, m map[string]interface{}) {
			m["signature"] = strings.Repeat("ab", 64)
		}, 401},
		{"seed/pub mismatch", func(f *gkFixture, m map[string]interface{}) {
			_, other, _ := ed25519.GenerateKey(nil)
			m["private_key"] = hex.EncodeToString(other.Seed())
		}, 400},
		{"shared key", func(f *gkFixture, m map[string]interface{}) {
			m["public_key"] = sharedGuestKeyPublic
			m["private_key"] = sharedGuestKeyPrivate
			f.sign(m)
		}, 400},
		{"unknown observer", func(f *gkFixture, m map[string]interface{}) {
			pub, priv, _ := ed25519.GenerateKey(nil)
			f.obsKey = priv
			m["observer_id"] = hex.EncodeToString(pub)
			f.sign(m)
		}, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGKFixture(t)
			code, _, raw := f.upload(t, func(m map[string]interface{}) { c.mutate(f, m) })
			if code != c.code {
				t.Errorf("status %d, want %d: %s", code, c.code, raw)
			}
			var n int
			f.srv.guestKeys.DB().QueryRow(`SELECT COUNT(*) FROM guest_keys`).Scan(&n)
			if n != 0 {
				t.Errorf("rejected upload stored %d row(s)", n)
			}
		})
	}
}

func TestGuestKeyUploadRateLimit(t *testing.T) {
	f := newGKFixture(t)
	if code, _, raw := f.upload(t, nil); code != 200 {
		t.Fatalf("first upload %d: %s", code, raw)
	}
	f.now = f.now.Add(30 * time.Second)
	if code, _, raw := f.upload(t, nil); code != 429 {
		t.Fatalf("second upload within a minute: %d, want 429: %s", code, raw)
	}
	f.now = f.now.Add(31 * time.Second)
	if code, _, raw := f.upload(t, nil); code != 200 {
		t.Fatalf("upload after a minute: %d: %s", code, raw)
	}
}

// A key not refreshed for 24 h reads as unregistered and is pruned on the
// next upload (by anyone) and by the ticker's prune.
func TestGuestKeyExpiryPruning(t *testing.T) {
	f := newGKFixture(t)
	if code, _, raw := f.upload(t, nil); code != 200 {
		t.Fatalf("upload %d: %s", code, raw)
	}
	// A second, stale row from another observer.
	old := f.now.Add(-25 * time.Hour).Format(time.RFC3339)
	if _, err := f.srv.guestKeys.DB().Exec(`INSERT INTO guest_keys VALUES (?, ?, ?, ?, ?)`,
		strings.Repeat("cd", 32), strings.Repeat("ef", 32), strings.Repeat("01", 32), old, old); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.get(t, strings.Repeat("cd", 32)); out["registered"] != false {
		t.Errorf("lapsed key reads as registered: %v", out)
	}
	if n, err := f.srv.guestKeys.Prune(f.now); err != nil || n != 1 {
		t.Fatalf("prune removed %d (%v), want 1", n, err)
	}
	f.now = f.now.Add(25 * time.Hour)
	if _, out, _ := f.get(t, f.obsPub); out["registered"] != false {
		t.Errorf("key 25 h old reads as registered: %v", out)
	}
	if n, _ := f.srv.guestKeys.Prune(f.now); n != 1 {
		t.Errorf("prune after 25 h removed %d, want 1", n)
	}
}

func TestGuestKeyRoutesOffWhenDisabled(t *testing.T) {
	db := setupTestDB(t)
	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	for _, req := range []struct{ method, path string }{
		{"POST", "/api/guest-keys"},
		{"GET", "/api/guest-keys/" + strings.Repeat("ab", 32)},
		{"GET", "/api/guest-polling/owners?observer=" + strings.Repeat("ab", 32)},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(req.method, req.path, strings.NewReader("{}")))
		if w.Code != 404 {
			t.Errorf("%s %s = %d, want 404 with neighborBattery off", req.method, req.path, w.Code)
		}
	}
}
