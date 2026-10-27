package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func nodesListRouter(t *testing.T, db *DB, cfg *Config) *mux.Router {
	t.Helper()
	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	return r
}

func listNodes(t *testing.T, r *mux.Router) map[string]map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes?limit=100", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]interface{}{}
	for _, n := range body.Nodes {
		out[n["public_key"].(string)] = n
	}
	return out
}

// Each repeater row carries its newest reading from either source, graded
// and flagged stale; rows without one, and non-repeaters, carry none; and the
// field is absent altogether with neighborBattery off.
func TestNodesListBattery(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db) // nbNode, a repeater, with neighbor_battery
	selfNode := "deadbeefcafef00d1122334455667788deadbeefcafef00d1122334455667788"
	staleNode := "0badc0de0badc0de0badc0de0badc0de0badc0de0badc0de0badc0de0badc0de"
	companion := "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	bare := "beef0000beef0000beef0000beef0000beef0000beef0000beef0000beef0000"
	for _, n := range []struct{ pk, role string }{{selfNode, "repeater"}, {staleNode, "room"}, {companion, "companion"}, {bare, "repeater"}} {
		db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen) VALUES (?, ?, ?, ?, ?)`,
			n.pk, "N"+n.pk[:4], n.role, now.Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
	}
	// nbNode: an older own report, a newer neighbour reading (3.2 V, low).
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(nbNode), now.Add(-3*time.Hour).Format(time.RFC3339), 4100)
	insertNeighborBattery(t, db, nbObsA, now.Add(-2*time.Hour), 3900, 5, 0)
	insertNeighborBattery(t, db, nbObsB, now.Add(-1*time.Hour), 3200, 5, 0)
	// selfNode: own reports only, newest 0 mV (no battery fitted).
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(selfNode), now.Add(-2*time.Hour).Format(time.RFC3339), 3700)
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(selfNode), now.Add(-1*time.Hour).Format(time.RFC3339), 0)
	// staleNode: one reading 30 h ago (past the 24 h hideAfterHours default).
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		staleNode, now.Add(-30*time.Hour).Format(time.RFC3339), 2900)
	// The companion has a reading, but the column is for repeaters/rooms.
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(companion), now.Add(-1*time.Hour).Format(time.RFC3339), 3900)

	on := &Config{Port: 3000, NeighborBattery: &NeighborBatteryServerConfig{Enabled: true}}
	got := listNodes(t, nodesListRouter(t, db, on))
	check := func(pk string, mv float64, status string, stale bool) {
		t.Helper()
		b, ok := got[pk]["battery"].(map[string]interface{})
		if !ok {
			t.Fatalf("%.8s: no battery in %v", pk, got[pk])
		}
		if b["mv"] != mv || b["status"] != status || b["stale"] != stale {
			t.Errorf("%.8s battery = %v, want %v mV %s stale=%v", pk, b, mv, status, stale)
		}
		if _, err := time.Parse(time.RFC3339, b["ts"].(string)); err != nil {
			t.Errorf("%.8s ts = %v", pk, b["ts"])
		}
	}
	check(nbNode, 3200, "low", false)
	check(selfNode, 0, "no_battery", false)
	check(staleNode, 2900, "critical", true)
	for _, pk := range []string{companion, bare} {
		if _, has := got[pk]["battery"]; has {
			t.Errorf("%.8s has a battery field: %v", pk, got[pk]["battery"])
		}
	}

	off := listNodes(t, nodesListRouter(t, db, &Config{Port: 3000}))
	for pk, n := range off {
		if _, has := n["battery"]; has {
			t.Errorf("%.8s has battery with neighborBattery off", pk)
		}
	}
}

// Repeater rows carry the newest temperature an observer read, beside the
// battery; non-repeaters never do.
func TestNodesListTemperature(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	if _, err := db.conn.Exec(`ALTER TABLE neighbor_battery ADD COLUMN temperature_c REAL`); err != nil {
		t.Fatal(err)
	}
	insertNeighborBattery(t, db, nbObsA, now.Add(-3*time.Hour), 3900, 5, 0)
	insertNeighborBattery(t, db, nbObsA, now.Add(-2*time.Hour), 3910, 5, 0)
	insertNeighborBattery(t, db, nbObsB, now.Add(-1*time.Hour), 3920, 5, 0)
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format("2006-01-02T15:04:05Z") }
	db.conn.Exec(`UPDATE neighbor_battery SET temperature_c = 6.2 WHERE sampled_at = ?`, ts(-3*time.Hour))
	db.conn.Exec(`UPDATE neighbor_battery SET temperature_c = 8.5 WHERE sampled_at = ?`, ts(-2*time.Hour))

	on := &Config{Port: 3000, NeighborBattery: &NeighborBatteryServerConfig{Enabled: true}}
	got := listNodes(t, nodesListRouter(t, db, on))
	tc, ok := got[nbNode]["temperature"].(map[string]interface{})
	if !ok {
		t.Fatalf("no temperature in %v", got[nbNode])
	}
	if tc["c"] != 8.5 || tc["ts"] != ts(-2*time.Hour) {
		t.Errorf("temperature = %v, want the newest reading that had one (8.5 at -2h)", tc)
	}
}
