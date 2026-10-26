// Package guestkeys stores the per-observer polling ("guest") keys in
// guest_keys.db, a small file beside the main database. cmd/server writes it
// (POST /api/guest-keys) and cmd/ingestor only reads it, read-only, to decrypt
// the repeater replies sent to those keys. It lives outside cmd/server because
// the server is otherwise the read path and may not open a database for
// writing (#1287); like internal/users, this is its own file, not the
// measurement database.
//
// The caller registers the SQLite driver: the package uses the "sqlite3"
// driver name (github.com/mattn/go-sqlite3).
package guestkeys

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

const (
	// FileName is the database file, in the directory of the main database.
	FileName = "guest_keys.db"
	// TTL: a key lapses this long after its last upload.
	TTL = 24 * time.Hour
	// MinInterval: at most one accepted upload per observer this often.
	MinInterval = 60 * time.Second
	// TimeLayout is how registered_at and updated_at are stored.
	TimeLayout = "2006-01-02T15:04:05Z"
)

// Path is guest_keys.db in the directory of the main database file.
func Path(mainDB string) string {
	return filepath.Join(filepath.Dir(mainDB), FileName)
}

// Store is the read-write handle on guest_keys.db.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serialises the rate-limit check with the upsert
}

// Open opens (creating if needed) guest_keys.db read-write in WAL mode.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS guest_keys (
		observer_id   TEXT PRIMARY KEY,
		public_key    TEXT NOT NULL,
		private_key   TEXT NOT NULL,
		registered_at TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("guest_keys schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the handle.
func (s *Store) Close() error { return s.db.Close() }

// DB is the underlying handle, for tests.
func (s *Store) DB() *sql.DB { return s.db }

// Prune drops keys not refreshed within TTL of now.
func (s *Store) Prune(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM guest_keys WHERE updated_at < ?`,
		now.UTC().Add(-TTL).Format(TimeLayout))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Key is one registered key, without its private half.
type Key struct {
	PublicKey string
	UpdatedAt time.Time
}

// Get returns the observer's live key, or ok=false when it has none (or it
// lapsed and has not been pruned yet).
func (s *Store) Get(observer string, now time.Time) (Key, bool, error) {
	var pub, updated string
	err := s.db.QueryRow(`SELECT public_key, updated_at FROM guest_keys WHERE observer_id = ?`, observer).Scan(&pub, &updated)
	if err == sql.ErrNoRows {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, err
	}
	at, err := time.Parse(time.RFC3339, updated)
	if err != nil || now.Sub(at) >= TTL {
		return Key{}, false, nil
	}
	return Key{PublicKey: pub, UpdatedAt: at}, true, nil
}

// Put stores the observer's key, replacing any earlier one and keeping its
// registered_at, unless the observer uploaded one less than MinInterval ago
// (limited=true, nothing stored). Lapsed keys are pruned on the way, best
// effort.
func (s *Store) Put(observer, pub, priv string, now time.Time) (limited bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated string
	err = s.db.QueryRow(`SELECT updated_at FROM guest_keys WHERE observer_id = ?`, observer).Scan(&updated)
	if err == nil {
		if at, perr := time.Parse(time.RFC3339, updated); perr == nil {
			if d := now.Sub(at); d < MinInterval && d > -MinInterval {
				return true, nil
			}
		}
	} else if err != sql.ErrNoRows {
		return false, err
	}
	ts := now.UTC().Format(TimeLayout)
	if _, err := s.db.Exec(`INSERT INTO guest_keys (observer_id, public_key, private_key, registered_at, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(observer_id) DO UPDATE SET
			public_key = excluded.public_key, private_key = excluded.private_key, updated_at = excluded.updated_at`,
		observer, pub, priv, ts, ts); err != nil {
		return false, err
	}
	// Best effort: the server's hourly prune catches anything missed here.
	_, _ = s.Prune(now)
	return false, nil
}
