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

const (
	regionSJC = "SJC" // seedTestData: obs1
	regionSFO = "SFO" // seedTestData: obs2
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
	args := make([]any, len(codes))
	for i, c := range codes {
		ph[i] = "?"
		args[i] = c
	}
	joinCond := "obs.rowid = o.observer_idx"
	if !db.isV3 {
		joinCond = "obs.id = o.observer_id"
	}
	q := fmt.Sprintf(`SELECT public_key FROM nodes WHERE public_key IN (
		SELECT DISTINCT t.from_pubkey
		FROM transmissions t
		JOIN observations o ON o.transmission_id = t.id
		JOIN observers obs ON %s
		WHERE t.payload_type = 4
		AND UPPER(TRIM(obs.iata)) IN (%s)
	)`, joinCond, strings.Join(ph, ","))
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, pk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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
		pk, ok := n["public_key"].(string)
		if !ok {
			t.Fatalf("region %q: public_key %v is not a string", region, n["public_key"])
		}
		out = append(out, pk)
	}
	sort.Strings(out)
	return out
}

// seedRegionAdvert adds a node heard via ADVERT by observer observerIdx (v3).
func seedRegionAdvert(t *testing.T, db *DB, pubkey, hash string, observerIdx int) {
	t.Helper()
	now := time.Now().UTC()
	mustExec(t, db, `INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen)
		VALUES (?, ?, 'repeater', ?, ?)`, pubkey, "n-"+pubkey, now.Format(time.RFC3339), now.Format(time.RFC3339))
	res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('AA', ?, ?, 1, 4, '{}', ?)`, hash, now.Format(time.RFC3339), pubkey)
	if err != nil {
		t.Fatal(err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, ?, 5, -100, '[]', ?)`, txID, observerIdx, now.Unix())
}

// ageNodeRegionEntries makes every cached entry stale, and optionally due a
// full rebuild, without sleeping through the real intervals. Entries are
// immutable in production, so each is replaced rather than edited.
func ageNodeRegionEntries(db *DB, rebuild bool) {
	db.nodeRegionCacheMu.Lock()
	defer db.nodeRegionCacheMu.Unlock()
	for k, e := range db.nodeRegionCache {
		aged := *e
		aged.refreshed = aged.refreshed.Add(-2 * nodeRegionFreshTTL)
		if rebuild {
			aged.built = aged.built.Add(-2 * nodeRegionRebuildInterval)
		}
		db.nodeRegionCache[k] = &aged
	}
}

// waitForRegionNodes polls until region's node list equals want: after a
// stale hit the refresh runs in the background.
func waitForRegionNodes(t *testing.T, db *DB, region string, want []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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

func assertMatchesLegacy(t *testing.T, db *DB, region string, wantNonEmpty bool) {
	t.Helper()
	want := legacyRegionPubkeys(t, db, region)
	if wantNonEmpty && len(want) == 0 {
		t.Fatalf("region %q: fixture produced no legacy matches, the comparison would be vacuous", region)
	}
	if got := regionNodePubkeys(t, db, region); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("region %q: cached %v, legacy %v", region, got, want)
	}
}

func TestNodeRegionCacheMatchesLegacyQuery(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedRegionAdvert(t, db, "cafe000000000001", "h-sfo-only", 2)

	for _, region := range []string{regionSJC, regionSFO, "SJC,SFO", "sfo , sjc", "SJC,SJC", "SJC,AMS"} {
		assertMatchesLegacy(t, db, region, true)
	}
	assertMatchesLegacy(t, db, "AMS", false)
}

func TestNodeRegionCacheMatchesLegacyQueryV2(t *testing.T) {
	db := setupTestDBV2(t)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-v2-1', 'V2 Observer', ' lax ', ?, '2026-01-01T00:00:00Z', 10)`, now)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen)
		VALUES ('v2pubkey11223344', 'V2Node', 'repeater', ?, '2026-01-01T00:00:00Z')`, now)
	mustExec(t, db, `INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('AABB', 'v2hash0001', ?, 1, 4, '{}', 'v2pubkey11223344')`, now)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (1, 'obs-v2-1', 'V2 Observer', 10.0, -90, '[]', ?)`, time.Now().Unix())

	assertMatchesLegacy(t, db, "LAX", true)
	assertMatchesLegacy(t, db, "lax", true)
	assertMatchesLegacy(t, db, "JFK", false)
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
		for offset := range 3 {
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
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := db.GetNodes(50, 0, "", "", "", "", "", regionSJC)
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

	before := regionNodePubkeys(t, db, regionSJC)
	seedRegionAdvert(t, db, "cafe000000000002", "h-new-sjc", 1)
	if got := regionNodePubkeys(t, db, regionSJC); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("fresh entry should be served unchanged, got %v want %v", got, before)
	}

	builtBefore := db.getNodeRegionEntry(regionSJC).built
	ageNodeRegionEntries(db, false)
	waitForRegionNodes(t, db, regionSJC, legacyRegionPubkeys(t, db, regionSJC))

	e := db.getNodeRegionEntry(regionSJC)
	if !e.built.Equal(builtBefore) {
		t.Errorf("expected a delta refresh (built unchanged), built moved %v -> %v", builtBefore, e.built)
	}
	if !contains(e.keys, "cafe000000000002") {
		t.Errorf("new node missing from refreshed keys %v", e.keys)
	}
}

// A delta that finds nothing new keeps the encoded keys and advances the
// watermark past observations that did not change membership.
func TestNodeRegionCacheDeltaWithoutNewNodes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	regionNodePubkeys(t, db, regionSJC)
	prev := db.getNodeRegionEntry(regionSJC)

	// A new observation of a node already in the region.
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 5, -100, '[]', ?)`, time.Now().Unix())
	ageNodeRegionEntries(db, false)
	e, err := db.refreshNodeRegion(regionSJC, []string{regionSJC})
	if err != nil {
		t.Fatal(err)
	}
	if e.keysJSON != prev.keysJSON {
		t.Errorf("membership unchanged, keysJSON should be reused: %s vs %s", e.keysJSON, prev.keysJSON)
	}
	if e.watermark <= prev.watermark {
		t.Errorf("watermark should advance past the new observation: %d -> %d", prev.watermark, e.watermark)
	}
}

// Deltas only add; the periodic full rebuild drops nodes whose region
// observations have since been pruned.
func TestNodeRegionCacheFullRebuildDropsPruned(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedRegionAdvert(t, db, "cafe000000000003", "h-prune-sjc", 1)
	if got := regionNodePubkeys(t, db, regionSJC); !contains(got, "cafe000000000003") {
		t.Fatalf("seeded node missing: %v", got)
	}

	mustExec(t, db, `DELETE FROM observations WHERE transmission_id IN
		(SELECT id FROM transmissions WHERE hash = 'h-prune-sjc')`)
	ageNodeRegionEntries(db, true)
	waitForRegionNodes(t, db, regionSJC, legacyRegionPubkeys(t, db, regionSJC))
}

// A failed refresh reports the error and leaves the previous entry serving.
func TestNodeRegionCacheRefreshFailureKeepsPrevious(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	want := regionNodePubkeys(t, db, regionSJC)
	prev := db.getNodeRegionEntry(regionSJC)

	mustExec(t, db, `ALTER TABLE observations RENAME TO observations_gone`)
	ageNodeRegionEntries(db, false)
	if _, err := db.refreshNodeRegion(regionSJC, []string{regionSJC}); err == nil {
		t.Fatal("expected refresh to fail without an observations table")
	}
	if e := db.getNodeRegionEntry(regionSJC); e.keysJSON != prev.keysJSON {
		t.Errorf("failed refresh replaced the entry: %s -> %s", prev.keysJSON, e.keysJSON)
	}
	// Stale hits keep serving the previous set while refreshes fail.
	if got := regionNodePubkeys(t, db, regionSJC); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stale entry should still be served, got %v want %v", got, want)
	}
}

// The cache is bounded: distinct region sets beyond nodeRegionMaxEntries
// reset it instead of growing it.
func TestNodeRegionCacheBounded(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	for i := range nodeRegionMaxEntries + 5 {
		if _, _, _, err := db.GetNodes(1, 0, "", "", "", "", "", fmt.Sprintf("SJC,X%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	db.nodeRegionCacheMu.Lock()
	n := len(db.nodeRegionCache)
	db.nodeRegionCacheMu.Unlock()
	if n > nodeRegionMaxEntries {
		t.Errorf("cache holds %d entries, cap is %d", n, nodeRegionMaxEntries)
	}
}

// Refreshes must hand every connection back. The production pool is 4
// (OpenDB); a leaked *sql.Row per refresh exhausted it after four stale
// hits and hung every DB-backed endpoint. The prepared MAX(id) statement is
// set up here because production always has it.
func TestNodeRegionCacheReleasesConnections(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	stmt, err := db.conn.Prepare("SELECT COALESCE(MAX(id), 0) FROM observations")
	if err != nil {
		t.Fatal(err)
	}
	db.stmtMaxObsID = stmt

	// The test pool holds one connection, so a leak deadlocks rather than
	// failing; refresh off the test goroutine and fail on a deadline instead.
	// Closing is left to the success path: with a connection leaked, Close
	// would block on it too.
	for i := range 10 {
		seedRegionAdvert(t, db, fmt.Sprintf("cafe0000000001%02d", i), fmt.Sprintf("h-leak-%d", i), 1)
		ageNodeRegionEntries(db, i%3 == 0)
		done := make(chan error, 1)
		go func() {
			_, err := db.refreshNodeRegion(regionSJC, []string{regionSJC})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("refresh %d did not complete: a pooled connection was not returned", i)
		}
	}
	if inUse := db.conn.Stats().InUse; inUse != 0 {
		t.Errorf("expected all connections returned after refreshes, %d still in use", inUse)
	}
	if err := stmt.Close(); err != nil {
		t.Error(err)
	}
	if err := db.Close(); err != nil {
		t.Error(err)
	}
}
