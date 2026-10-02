package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

const (
	regionSJC = "SJC" // seedTestData: obs1
	regionSFO = "SFO" // seedTestData: obs2
)

// legacyRegionPubkeys runs the pre-#2101 region subquery directly: the
// oracle the table-backed filter must agree with.
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
	q := fmt.Sprintf(`SELECT public_key FROM nodes WHERE public_key IN (
		SELECT DISTINCT t.from_pubkey
		FROM transmissions t
		JOIN observations o ON o.transmission_id = t.id
		JOIN observers obs ON obs.rowid = o.observer_idx
		WHERE t.payload_type = 4
		AND UPPER(TRIM(obs.iata)) IN (%s)
	)`, strings.Join(ph, ","))
	return queryPubkeys(t, db, q, args...)
}

func queryPubkeys(t *testing.T, db *DB, q string, args ...any) []string {
	t.Helper()
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

// regionTestDB is seedTestData under the real schema, with
// node_advert_observers filled from the advert history the way the ingestor
// builds it (its builder is checked against the same oracle in
// cmd/ingestor/node_advert_observers_test.go).
func regionTestDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedTestData(t, db)
	if err := dbschema.Apply(db.conn, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO node_advert_observers (public_key, observer_idx, last_seen, last_obs_id)
		SELECT t.from_pubkey, o.observer_idx, MAX(t.first_seen), MAX(o.id)
		FROM observations o JOIN transmissions t ON t.id = o.transmission_id
		WHERE t.payload_type = 4 AND t.from_pubkey IS NOT NULL AND t.from_pubkey != ''
		  AND o.observer_idx IS NOT NULL
		GROUP BY t.from_pubkey, o.observer_idx`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS _async_migrations (
		name TEXT PRIMARY KEY, status TEXT NOT NULL, started_at TEXT, ended_at TEXT, error TEXT)`)
	return db
}

func setBackfillStatus(t *testing.T, db *DB, status string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO _async_migrations (name, status) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET status = excluded.status`, dbschema.NodeAdvertObserversBackfill, status)
	db.nodeAdvertObserversCheckedAt.Store(0) // don't wait out the re-check interval
}

// addPhantomPair records a pair the advert history does not contain, so a
// result that includes it proves the filter read the table.
func addPhantomPair(t *testing.T, db *DB) string {
	t.Helper()
	const pk = "1122334455667788" // seedTestData's TestRoom: no ADVERT in SFO
	mustExec(t, db, `INSERT INTO node_advert_observers (public_key, observer_idx, last_seen, last_obs_id)
		VALUES (?, 2, '2026-01-01T00:00:00Z', 1)`, pk)
	return pk
}

func TestRegionFilterMatchesLegacyFromTable(t *testing.T) {
	db := regionTestDB(t)
	setBackfillStatus(t, db, "done")

	for _, region := range []string{regionSJC, regionSFO, "SJC,SFO", "sfo , sjc", "SJC,SJC"} {
		want := legacyRegionPubkeys(t, db, region)
		if len(want) == 0 {
			t.Fatalf("region %q: fixture matched nothing, the comparison would be vacuous", region)
		}
		if got := regionNodePubkeys(t, db, region); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("region %q: table %v, legacy %v", region, got, want)
		}
	}
	if got := regionNodePubkeys(t, db, "AMS"); len(got) != 0 {
		t.Errorf("unknown region should match nothing, got %v", got)
	}
	if !db.nodeAdvertObserversLatched.Load() {
		t.Fatal("expected the table path to be latched")
	}
	phantom := addPhantomPair(t, db)
	if got := regionNodePubkeys(t, db, regionSFO); !contains(got, phantom) {
		t.Errorf("filter did not read node_advert_observers: %v lacks %s", got, phantom)
	}
}

// Until the backfill is done the table may be partial, so it is not read.
func TestRegionFilterLegacyUntilBackfillDone(t *testing.T) {
	for _, status := range []string{"", "pending_async", "failed"} {
		t.Run("status="+status, func(t *testing.T) {
			db := regionTestDB(t)
			if status != "" {
				setBackfillStatus(t, db, status)
			}
			phantom := addPhantomPair(t, db)
			got := regionNodePubkeys(t, db, regionSFO)
			if contains(got, phantom) {
				t.Errorf("table read before its backfill was done: %v", got)
			}
			if want := legacyRegionPubkeys(t, db, regionSFO); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("legacy path: got %v, want %v", got, want)
			}
		})
	}
}

// An ingestor that predates #2101 has no _async_migrations row and may have
// no table at all; the server keeps the legacy query.
func TestRegionFilterLegacyWithoutAsyncMigrationsTable(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	if db.nodeAdvertObserversReady() {
		t.Fatal("ready without an _async_migrations table")
	}
	if got, want := regionNodePubkeys(t, db, regionSJC), legacyRegionPubkeys(t, db, regionSJC); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A server that started before the backfill finished switches over within
// the re-check interval, without a restart, and then stays switched.
func TestRegionFilterSwitchesWithoutRestart(t *testing.T) {
	db := regionTestDB(t)
	setBackfillStatus(t, db, "pending_async")
	phantom := addPhantomPair(t, db)
	if contains(regionNodePubkeys(t, db, regionSFO), phantom) {
		t.Fatal("table read while pending")
	}

	// Done, but inside the re-check interval: still the legacy query.
	mustExec(t, db, `UPDATE _async_migrations SET status = 'done' WHERE name = ?`, dbschema.NodeAdvertObserversBackfill)
	if contains(regionNodePubkeys(t, db, regionSFO), phantom) {
		t.Fatal("re-checked inside the interval")
	}

	db.nodeAdvertObserversCheckedAt.Store(1) // the interval has passed
	if !contains(regionNodePubkeys(t, db, regionSFO), phantom) {
		t.Fatal("did not switch to the table once the backfill was done")
	}

	// Latched: a later status change does not switch back.
	mustExec(t, db, `UPDATE _async_migrations SET status = 'failed' WHERE name = ?`, dbschema.NodeAdvertObserversBackfill)
	db.nodeAdvertObserversCheckedAt.Store(1)
	if !contains(regionNodePubkeys(t, db, regionSFO), phantom) {
		t.Error("switched back after latching")
	}
}

// The v2 schema keys observations by observer_id, which the table does not
// model; the ingestor no longer writes it, so the legacy query stays.
func TestRegionFilterV2SchemaStaysLegacy(t *testing.T) {
	db := setupTestDBV2(t)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE _async_migrations (name TEXT PRIMARY KEY, status TEXT NOT NULL)`)
	mustExec(t, db, `INSERT INTO _async_migrations (name, status) VALUES (?, 'done')`, dbschema.NodeAdvertObserversBackfill)
	if db.nodeAdvertObserversReady() {
		t.Error("v2 schema must keep the legacy region query")
	}
}

// Readiness is looked up at most once per interval while not ready, from any
// number of concurrent requests (go test -race), and a concurrent switch is
// safe.
func TestRegionFilterReadinessCheckBounded(t *testing.T) {
	db := regionTestDB(t)
	setBackfillStatus(t, db, "pending_async")
	if db.nodeAdvertObserversReady() {
		t.Fatal("ready while pending")
	}
	checked := db.nodeAdvertObserversCheckedAt.Load()
	for range 50 {
		db.nodeAdvertObserversReady()
	}
	if db.nodeAdvertObserversCheckedAt.Load() != checked {
		t.Error("re-checked inside the interval")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 8 {
				// Not setBackfillStatus: t.Fatal must not run off the test goroutine.
				if _, err := db.conn.Exec(`UPDATE _async_migrations SET status = 'done' WHERE name = ?`,
					dbschema.NodeAdvertObserversBackfill); err != nil {
					errs <- err
					return
				}
				db.nodeAdvertObserversCheckedAt.Store(0)
			}
			if _, _, _, err := db.GetNodes(50, 0, "", "", "", "", "", regionSJC); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// The table-backed filter never touches observations or transmissions: its
// cost is bounded by the pairs table, not the packet history (rule 0 proof).
func TestRegionFilterPlanAvoidsHistory(t *testing.T) {
	db := regionTestDB(t)
	q := `EXPLAIN QUERY PLAN SELECT public_key FROM nodes WHERE ` +
		nodeAdvertObserversRegionFilter([]string{"?", "?"})
	rows, err := db.conn.Query(q, regionSJC, regionSFO)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, " | ")
	if !strings.Contains(joined, "node_advert_observers") {
		t.Errorf("plan does not read node_advert_observers: %s", joined)
	}
	for _, history := range []string{"observations", "transmissions"} {
		if strings.Contains(joined, history) {
			t.Errorf("plan touches %s: %s", history, joined)
		}
	}
}
