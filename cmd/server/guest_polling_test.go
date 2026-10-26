package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func createPollingClaimsTable(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.conn.Exec(`CREATE TABLE guest_polling_claims (
		node_id TEXT NOT NULL, observer_id TEXT NOT NULL, reply_hops INTEGER NOT NULL DEFAULT 0,
		via TEXT, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
		PRIMARY KEY (node_id, observer_id)) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
}

func insertPollingClaim(t *testing.T, db *DB, node, obs string, hops int, first, last time.Time) {
	t.Helper()
	if _, err := db.conn.Exec(`INSERT INTO guest_polling_claims VALUES (?,?,?,NULL,?,?)`,
		node, obs, hops, first.UTC().Format(time.RFC3339), last.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func getOwners(t *testing.T, router *mux.Router, obs string) map[string]map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/guest-polling/owners?observer="+obs, nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Observer string                            `json:"observer"`
		Nodes    map[string]map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Observer != obs {
		t.Errorf("observer = %s", body.Observer)
	}
	return body.Nodes
}

func ownersRouter(t *testing.T, db *DB) *mux.Router {
	t.Helper()
	srv := NewServer(db, &Config{Port: 3000, NeighborBattery: &NeighborBatteryServerConfig{Enabled: true}}, NewHub())
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	return r
}

const nbNode2 = "a1b2c30011223344556677889900aabbccddeeff00112233445566779f41aa00"

// Ranking: a recent reading beats fewer hops; then fewer hops; then the
// earlier first claim. "mine" says whether the asker owns it, and the owner
// is named.
func TestGuestPollingOwnersRanking(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db) // observers A (Corstorphine) and B (Pentland), neighbor_battery
	createPollingClaimsTable(t, db)
	// nbNode: A is 2 hops but has a reading; B is direct with none -> A owns.
	insertPollingClaim(t, db, nbNode, nbObsA, 2, now.Add(-5*time.Hour), now.Add(-5*time.Minute))
	insertPollingClaim(t, db, nbNode, nbObsB, 0, now.Add(-6*time.Hour), now.Add(-5*time.Minute))
	insertNeighborBattery(t, db, nbObsA, now.Add(-30*time.Minute), 3900, 5, 2)
	// nbNode2: no readings; B has fewer hops -> B owns.
	insertPollingClaim(t, db, nbNode2, nbObsA, 1, now.Add(-9*time.Hour), now.Add(-5*time.Minute))
	insertPollingClaim(t, db, nbNode2, nbObsB, 0, now.Add(-1*time.Hour), now.Add(-5*time.Minute))

	got := getOwners(t, ownersRouter(t, db), nbObsB)
	if len(got) != 2 {
		t.Fatalf("nodes = %v", got)
	}
	if got[nbNode]["owner"] != nbObsA || got[nbNode]["mine"] != false || got[nbNode]["owner_name"] != "Corstorphine OBS" {
		t.Errorf("node 1 = %v, want owned by A (recent reading) not mine", got[nbNode])
	}
	if got[nbNode2]["owner"] != nbObsB || got[nbNode2]["mine"] != true {
		t.Errorf("node 2 = %v, want owned by B (fewer hops) and mine", got[nbNode2])
	}

	// Equal hops and no readings: the earlier first claim wins.
	db.conn.Exec(`UPDATE guest_polling_claims SET reply_hops = 0 WHERE node_id = ?`, nbNode2)
	got = getOwners(t, ownersRouter(t, db), nbObsB)
	if got[nbNode2]["owner"] != nbObsA {
		t.Errorf("node 2 with equal hops = %v, want A (earlier first claim)", got[nbNode2])
	}
}

// A claimant silent for over an hour is no longer eligible, so ownership
// hands over; and an asker whose own claim lapsed is told nothing about it.
func TestGuestPollingOwnersHandover(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	createPollingClaimsTable(t, db)
	insertPollingClaim(t, db, nbNode, nbObsA, 0, now.Add(-9*time.Hour), now.Add(-61*time.Minute))
	insertPollingClaim(t, db, nbNode, nbObsB, 3, now.Add(-1*time.Hour), now.Add(-1*time.Minute))
	insertNeighborBattery(t, db, nbObsA, now.Add(-70*time.Minute), 3900, 5, 0)

	router := ownersRouter(t, db)
	got := getOwners(t, router, nbObsB)
	if got[nbNode]["owner"] != nbObsB || got[nbNode]["mine"] != true {
		t.Errorf("node = %v, want handed over to B", got[nbNode])
	}
	if got := getOwners(t, router, nbObsA); len(got) != 0 {
		t.Errorf("A's lapsed claim still listed: %v", got)
	}
}

// Without the claims table (an older ingestor) the answer is empty, not 500;
// a malformed observer is 400.
func TestGuestPollingOwnersNoTable(t *testing.T) {
	db := setupTestDB(t)
	router := ownersRouter(t, db)
	if got := getOwners(t, router, nbObsA); len(got) != 0 {
		t.Errorf("nodes = %v, want none", got)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/guest-polling/owners?observer="+strings.Repeat("z", 64), nil))
	if w.Code != 400 {
		t.Errorf("bad observer = %d, want 400", w.Code)
	}
}
