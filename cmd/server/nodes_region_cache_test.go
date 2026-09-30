package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// legacyRegionPubkeys is the pre-cache GetNodes region subquery, kept here as
// the oracle the cached membership must agree with.
func legacyRegionPubkeys(t *testing.T, db *DB, region string) []string {
	t.Helper()
	codes := normalizeRegionCodes(region)
	if len(codes) == 0 {
		return nil
	}
	ph := make([]string, len(codes))
	args := make([]interface{}, len(codes))
	for i, c := range codes {
		ph[i] = "?"
		args[i] = c
	}
	joinCond := "obs.rowid = o.observer_idx"
	if !db.isV3 {
		joinCond = "obs.id = o.observer_id"
	}
	rows, err := db.conn.Query(`SELECT public_key FROM nodes WHERE public_key IN (
		SELECT DISTINCT t.from_pubkey
		FROM transmissions t
		JOIN observations o ON o.transmission_id = t.id
		JOIN observers obs ON `+joinCond+`
		WHERE t.payload_type = 4
		AND UPPER(TRIM(obs.iata)) IN (`+strings.Join(ph, ",")+`)
	)`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		rows.Scan(&pk)
		out = append(out, pk)
	}
	sort.Strings(out)
	return out
}

func regionNodePubkeys(t *testing.T, db *DB, region string) []string {
	t.Helper()
	nodes, total, _, err := db.GetNodes(500, 0, "", "", "", "", "", region)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(nodes) {
		t.Fatalf("region %q: total %d != page length %d", region, total, len(nodes))
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n["public_key"].(string))
	}
	sort.Strings(out)
	return out
}

// seedRegionAdvert adds a node heard via ADVERT by observer observerIdx (v3).
func seedRegionAdvert(t *testing.T, db *DB, pubkey, hash string, observerIdx int) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen)
		VALUES (?, ?, 'repeater', ?, ?)`, pubkey, "n-"+pubkey, now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('AA', ?, ?, 1, 4, '{}', ?)`, hash, now.Format(time.RFC3339), pubkey)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, ?, 5, -100, '[]', ?)`, txID, observerIdx, now.Unix()); err != nil {
		t.Fatal(err)
	}
}

// ageNodeRegionEntries makes every cached entry stale, and optionally due a
// full rebuild, without sleeping through the real intervals.
func ageNodeRegionEntries(db *DB, rebuild bool) {
	db.nodeRegionCacheMu.Lock()
	defer db.nodeRegionCacheMu.Unlock()
	for _, e := range db.nodeRegionCache {
		e.refreshed = e.refreshed.Add(-2 * nodeRegionFreshTTL)
		if rebuild {
			e.built = e.built.Add(-2 * nodeRegionRebuildInterval)
		}
	}
}

// waitForRegionNodes polls until region's node list equals want: after a
// stale hit the refresh runs in the background.
func waitForRegionNodes(t *testing.T, db *DB, region string, want []string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		got = regionNodePubkeys(t, db, region)
		if strings.Join(got, ",") == strings.Join(want, ",") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("region %q: got %v, want %v", region, got, want)
}

func TestNodeRegionCacheMatchesLegacyQuery(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedRegionAdvert(t, db, "cafe000000000001", "h-sfo-only", 2)

	for _, region := range []string{"SJC", "SFO", "SJC,SFO", "sfo , sjc", "SJC,SJC", "AMS", "SJC,AMS"} {
		want := legacyRegionPubkeys(t, db, region)
		got := regionNodePubkeys(t, db, region)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("region %q: cached %v, legacy %v", region, got, want)
		}
	}
}

func TestNodeRegionCacheMatchesLegacyQueryV2(t *testing.T) {
	db := setupTestDBV2(t)
	defer db.Close()
	now := time.Now().UTC()
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-v2-1', 'V2 Observer', ' lax ', ?, '2026-01-01T00:00:00Z', 10)`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen)
		VALUES ('v2pubkey11223344', 'V2Node', 'repeater', ?, '2026-01-01T00:00:00Z')`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('AABB', 'v2hash0001', ?, 1, 4, '{}', 'v2pubkey11223344')`, now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (1, 'obs-v2-1', 'V2 Observer', 10.0, -90, '[]', ?)`, now.Unix())

	for _, region := range []string{"LAX", "lax", "JFK"} {
		want := legacyRegionPubkeys(t, db, region)
		got := regionNodePubkeys(t, db, region)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("region %q: cached %v, legacy %v", region, got, want)
		}
	}
}

// Equivalent spellings of one region set share one cache entry, and the
// COUNT + page of a request, and later pages, do not rescan.
func TestNodeRegionCacheKeyAndReuse(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	var scans atomic.Int32
	db.nodeRegionQueryHook = func() { scans.Add(1) }

	for _, region := range []string{"SJC,SFO", "sfo, sjc", "SFO,SJC,SJC"} {
		for offset := 0; offset < 3; offset++ {
			if _, _, _, err := db.GetNodes(1, offset, "", "", "", "", "", region); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := scans.Load(); n != 1 {
		t.Errorf("expected 1 membership scan for one region set, got %d", n)
	}
}

// Concurrent cold requests for the same region share one scan (#1910 class).
func TestNodeRegionCacheCoalescesColdMisses(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	var scans atomic.Int32
	release := make(chan struct{})
	db.nodeRegionQueryHook = func() {
		scans.Add(1)
		<-release
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := db.GetNodes(50, 0, "", "", "", "", "", "SJC")
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // let every caller reach the flight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := scans.Load(); n != 1 {
		t.Errorf("expected 1 coalesced scan for %d cold callers, got %d", callers, n)
	}
}

// A node first heard after the cache was built appears after the next
// (delta) refresh; until the entry goes stale the cached set is served.
func TestNodeRegionCacheDeltaRefresh(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	before := regionNodePubkeys(t, db, "SJC")
	seedRegionAdvert(t, db, "cafe000000000002", "h-new-sjc", 1)
	if got := regionNodePubkeys(t, db, "SJC"); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("fresh entry should be served unchanged, got %v want %v", got, before)
	}

	builtBefore := db.getNodeRegionEntry("SJC").built
	ageNodeRegionEntries(db, false)
	waitForRegionNodes(t, db, "SJC", legacyRegionPubkeys(t, db, "SJC"))

	e := db.getNodeRegionEntry("SJC")
	if !e.built.Equal(builtBefore) {
		t.Errorf("expected a delta refresh (built unchanged), built moved %v -> %v", builtBefore, e.built)
	}
	if _, ok := e.keys["cafe000000000002"]; !ok {
		t.Errorf("new node missing from refreshed keys")
	}
}

// Deltas only add; the periodic full rebuild drops nodes whose region
// observations have since been pruned.
func TestNodeRegionCacheFullRebuildDropsPruned(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedRegionAdvert(t, db, "cafe000000000003", "h-prune-sjc", 1)
	if got := regionNodePubkeys(t, db, "SJC"); !contains(got, "cafe000000000003") {
		t.Fatalf("seeded node missing: %v", got)
	}

	if _, err := db.conn.Exec(`DELETE FROM observations WHERE transmission_id IN
		(SELECT id FROM transmissions WHERE hash = 'h-prune-sjc')`); err != nil {
		t.Fatal(err)
	}
	ageNodeRegionEntries(db, true)
	waitForRegionNodes(t, db, "SJC", legacyRegionPubkeys(t, db, "SJC"))
}

// Refreshes must hand every connection back. The production pool is 4
// (OpenDB); a leaked *sql.Row per refresh exhausted it after four stale
// hits and hung every DB-backed endpoint. The prepared MAX(id) statement is
// set up here because production always has it and the leak was on that path.
func TestNodeRegionCacheReleasesConnections(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	stmt, err := db.conn.Prepare("SELECT COALESCE(MAX(id), 0) FROM observations")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	db.stmtMaxObsID = stmt

	for i := 0; i < 10; i++ {
		seedRegionAdvert(t, db, fmt.Sprintf("cafe0000000001%02d", i), fmt.Sprintf("h-leak-%d", i), 1)
		ageNodeRegionEntries(db, i%3 == 0)
		waitForRegionNodes(t, db, "SJC", legacyRegionPubkeys(t, db, "SJC"))
	}
	if inUse := db.conn.Stats().InUse; inUse != 0 {
		t.Errorf("expected all connections returned after refreshes, %d still in use", inUse)
	}
}
