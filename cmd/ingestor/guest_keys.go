package main

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Per-observer polling keys: each observer registers its own polling
// ("guest") key with the server (POST /api/guest-keys), which keeps them in
// guest_keys.db beside the main database (internal/guestkeys). The ingestor
// only reads that file, read-only, once per guestKeysRefresh, and decodes the
// replies sent to those keys the way it decodes the shared key's
// (shared_guest_decode.go). A key the server let lapse (24 h without a
// re-upload) is gone from the file, so it stops decoding at the next read.

const (
	guestKeysRefresh = time.Minute
	guestKeysTTL     = 24 * time.Hour // matches internal/guestkeys.TTL
)

// guestKeysDBPath is guest_keys.db in the directory of the main database, the
// same rule the server uses.
func guestKeysDBPath(mainDB string) string {
	return filepath.Join(filepath.Dir(mainDB), "guest_keys.db")
}

// guestKeyFile reads guest_keys.db. Used from one goroutine at a time
// (startup, then the refresh ticker).
type guestKeyFile struct {
	path    string
	db      *sql.DB // opened lazily, read-only
	failing bool    // the last read failed; logged once per failure streak
	count   int     // keys in the last successful read
}

// read returns the live keys, ok=false when the file could not be read (not
// there yet, or locked), in which case the caller keeps what it has.
func (f *guestKeyFile) read(now time.Time) ([]sgKey, bool) {
	keys, err := f.query(now)
	if err != nil {
		if !f.failing {
			if os.IsNotExist(err) {
				log.Printf("[shared-guest] no guest_keys.db at %s yet; will retry", f.path)
			} else {
				log.Printf("[shared-guest] guest keys unavailable (%v); keeping the %d in force", err, f.count)
			}
			f.failing = true
		}
		return nil, false
	}
	if f.failing {
		log.Printf("[shared-guest] guest keys readable from %s", f.path)
		f.failing = false
	}
	if len(keys) != f.count {
		log.Printf("[shared-guest] %d per-observer polling key(s) in force", len(keys))
	}
	f.count = len(keys)
	return keys, true
}

func (f *guestKeyFile) query(now time.Time) ([]sgKey, error) {
	if f.db == nil {
		if _, err := os.Stat(f.path); err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite3", "file:"+f.path+"?mode=ro&_busy_timeout=5000")
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		f.db = db
	}
	rows, err := f.db.Query(`SELECT observer_id, public_key, private_key FROM guest_keys WHERE updated_at >= ?`,
		now.UTC().Add(-guestKeysTTL).Format(batteryTimeLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sgKey
	for rows.Next() {
		var obs, pubHex, privHex string
		if err := rows.Scan(&obs, &pubHex, &privHex); err != nil {
			return nil, err
		}
		obs = strings.ToLower(obs)
		pub, err1 := hex.DecodeString(strings.ToLower(pubHex))
		seed, err2 := hex.DecodeString(strings.ToLower(privHex))
		if !targetPubkeyRe.MatchString(obs) || err1 != nil || err2 != nil || len(pub) != 32 || len(seed) != 32 {
			continue
		}
		// The server checked this on upload; checking again keeps a damaged
		// row from producing a key that can never decrypt anything.
		if string(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)) != string(pub) {
			continue
		}
		out = append(out, sgKey{pub: pub, seed: seed, owner: obs})
	}
	return out, rows.Err()
}

// Close closes the read-only handle, if any.
func (f *guestKeyFile) Close() {
	if f.db != nil {
		f.db.Close()
	}
}
