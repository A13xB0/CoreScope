package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/guestkeys"
)

// Per-observer polling ("guest") keys. Under the one shared guest key every
// observer lands in the same ACL entry on a repeater, so they fight over one
// reply route and one replay counter. Instead each observer generates its own
// polling key and registers it here, private half included, so the ingestor
// can still decrypt the repeater's overheard reply (cmd/ingestor
// shared_guest_decode.go). The keys live in guest_keys.db beside the main
// database (internal/guestkeys): the server's main connection is read-only,
// and the ingestor only ever reads this file.
//
// A registration is signed by the observer's own identity key, which is how
// an upload is tied to the observer: the signed message is
// "corescope-guest-key/v1|<observer_id>|<public_key>|<timestamp>". A key lapses
// 24 h after its last upload, so a plugin re-registers well before that.

const (
	guestKeyMaxSkew    = 10 * time.Minute
	guestKeyPruneEvery = time.Hour
	guestKeyTimeLayout = guestkeys.TimeLayout
	guestKeyBodyLimit  = 4 << 10
	guestKeySigPrefix  = "corescope-guest-key/v1|"
)

var (
	hex64Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex128Re = regexp.MustCompile(`^[0-9a-f]{128}$`)
)

// guestKeyStore is guest_keys.db (internal/guestkeys) with the clock the
// handlers judge timestamps and expiry by, swappable in tests.
type guestKeyStore struct {
	*guestkeys.Store
	now func() time.Time
}

// initGuestKeys opens guest_keys.db when neighborBattery is enabled and
// prunes it hourly. A failure leaves the routes answering 503 rather than
// stopping the server.
func (s *Server) initGuestKeys(mainDB string) {
	if !s.cfg.NeighborBatteryEnabled() {
		return
	}
	path := guestkeys.Path(mainDB)
	st, err := guestkeys.Open(path)
	if err != nil {
		log.Printf("[guest-keys] disabled: open %s: %v", path, err)
		return
	}
	g := &guestKeyStore{Store: st, now: time.Now}
	s.guestKeys = g
	s.guestKeysStop = make(chan struct{})
	log.Printf("[guest-keys] per-observer polling keys in %s", path)
	go func(stop chan struct{}) {
		t := time.NewTicker(guestKeyPruneEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if n, err := g.Prune(g.now()); err != nil {
					log.Printf("[guest-keys] prune: %v", err)
				} else if n > 0 {
					log.Printf("[guest-keys] pruned %d lapsed key(s)", n)
				}
			}
		}
	}(s.guestKeysStop)
}

func (s *Server) closeGuestKeys() {
	if s.guestKeys == nil {
		return
	}
	close(s.guestKeysStop)
	s.guestKeys.Close()
}

// requireGuestKeys 404s when neighborBattery is off (like other disabled
// features) and 503s when it is on but guest_keys.db could not be opened.
func (s *Server) requireGuestKeys(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return false
	}
	if s.guestKeys == nil {
		writeError(w, http.StatusServiceUnavailable, "guest keys unavailable")
		return false
	}
	return true
}

type guestKeyUpload struct {
	ObserverID string `json:"observer_id"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	Timestamp  int64  `json:"timestamp"`
	Signature  string `json:"signature"`
}

// handleGuestKeyUpload serves POST /api/guest-keys: an observer registers (or
// replaces) its polling key. The private key is stored but never logged or
// returned.
func (s *Server) handleGuestKeyUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireGuestKeys(w, r) {
		return
	}
	var in guestKeyUpload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, guestKeyBodyLimit)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	obs := strings.ToLower(strings.TrimSpace(in.ObserverID))
	pub := strings.ToLower(strings.TrimSpace(in.PublicKey))
	priv := strings.ToLower(strings.TrimSpace(in.PrivateKey))
	sigHex := strings.ToLower(strings.TrimSpace(in.Signature))
	if !hex64Re.MatchString(obs) || !hex64Re.MatchString(pub) || !hex64Re.MatchString(priv) ||
		!hex128Re.MatchString(sigHex) || in.Timestamp <= 0 {
		writeError(w, http.StatusBadRequest, "observer_id, public_key and private_key must be 64 hex, signature 128 hex, timestamp unix seconds")
		return
	}
	now := s.guestKeys.now()
	if d := now.Sub(time.Unix(in.Timestamp, 0)); d > guestKeyMaxSkew || d < -guestKeyMaxSkew {
		writeError(w, http.StatusUnauthorized, "timestamp outside the accepted window")
		return
	}
	obsKey, _ := hex.DecodeString(obs)
	sig, _ := hex.DecodeString(sigHex)
	msg := fmt.Sprintf("%s%s|%s|%d", guestKeySigPrefix, obs, pub, in.Timestamp)
	if !ed25519.Verify(ed25519.PublicKey(obsKey), []byte(msg), sig) {
		writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}
	seed, _ := hex.DecodeString(priv)
	derived := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if hex.EncodeToString(derived) != pub {
		writeError(w, http.StatusBadRequest, "private_key does not match public_key")
		return
	}
	if pub == obs || pub == sharedGuestKeyPublic {
		writeError(w, http.StatusBadRequest, "public_key must be a separate polling key")
		return
	}
	if !s.db.observerKnown(obs) {
		writeError(w, http.StatusForbidden, "unknown observer")
		return
	}
	limited, err := s.guestKeys.Put(obs, pub, priv, now)
	if err != nil {
		log.Printf("[guest-keys] store for %.8s: %v", obs, err)
		writeError(w, http.StatusInternalServerError, "could not store key")
		return
	}
	if limited {
		writeError(w, http.StatusTooManyRequests, "one upload per minute")
		return
	}
	log.Printf("[guest-keys] registered polling key %.8s for observer %.8s", pub, obs)
	writeJSON(w, map[string]interface{}{
		"ok":         true,
		"public_key": pub,
		"expires_at": now.UTC().Add(guestkeys.TTL).Format(guestKeyTimeLayout),
	})
}

// handleGuestKeyGet serves GET /api/guest-keys/{observer_id}: whether the
// observer has a live polling key, and which. Never the private key.
func (s *Server) handleGuestKeyGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireGuestKeys(w, r) {
		return
	}
	obs := strings.ToLower(strings.TrimSpace(mux.Vars(r)["observer_id"]))
	if !hex64Re.MatchString(obs) {
		writeError(w, http.StatusBadRequest, "observer_id must be 64 hex")
		return
	}
	row, ok, err := s.guestKeys.Get(obs, s.guestKeys.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !ok {
		writeJSON(w, map[string]interface{}{"registered": false})
		return
	}
	writeJSON(w, map[string]interface{}{
		"registered": true,
		"public_key": row.PublicKey,
		"updated_at": row.UpdatedAt.UTC().Format(guestKeyTimeLayout),
		"expires_at": row.UpdatedAt.UTC().Add(guestkeys.TTL).Format(guestKeyTimeLayout),
	})
}

// observerKnown reports whether the observers table has this key. observers.id
// is usually the uppercase key, so both cases are tried (as fillObserverNames
// does), which keeps the lookup on the primary key.
func (db *DB) observerKnown(id string) bool {
	var one int
	err := db.conn.QueryRow(`SELECT 1 FROM observers WHERE id IN (?, ?) LIMIT 1`,
		strings.ToUpper(id), strings.ToLower(id)).Scan(&one)
	return err == nil
}
