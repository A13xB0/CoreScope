package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// advertFixture seeds observers and ADVERT traffic into a test store and
// records the rowids it created.
type advertFixture struct {
	t         *testing.T
	s         *Store
	observers map[string]int64 // observer id -> rowid
	nextHash  int
	nextPath  int
}

func newAdvertFixture(t *testing.T, s *Store) *advertFixture {
	t.Helper()
	return &advertFixture{t: t, s: s, observers: map[string]int64{}}
}

func (f *advertFixture) exec(q string, args ...any) int64 {
	f.t.Helper()
	res, err := f.s.db.Exec(q, args...)
	if err != nil {
		f.t.Fatalf("%v\n%s", err, q)
	}
	id, err := res.LastInsertId()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// observer adds an observer with the given IATA tag (stored verbatim, so
// padding and case exercise the region filter's normalisation).
func (f *advertFixture) observer(id string, iata any) int64 {
	f.t.Helper()
	f.exec(`INSERT INTO observers (id, name, iata) VALUES (?, ?, ?)`, id, id, iata)
	var rowid int64
	if err := f.s.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, id).Scan(&rowid); err != nil {
		f.t.Fatal(err)
	}
	f.observers[id] = rowid
	return rowid
}

// node adds a nodes row so the legacy query, which filters nodes, sees it.
func (f *advertFixture) node(pk string) {
	f.t.Helper()
	f.exec(`INSERT OR IGNORE INTO nodes (public_key, name) VALUES (?, ?)`, pk, "n-"+pk)
}

// tx adds a transmission. fromPubkey nil stores NULL, as on a legacy row
// BackfillFromPubkey has not reached; decoded carries the pubKey it will use.
func (f *advertFixture) tx(payloadType int, fromPubkey any, decoded, firstSeen string) int64 {
	f.t.Helper()
	f.nextHash++
	return f.exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
		VALUES ('', ?, ?, 1, ?, ?, ?)`, fmt.Sprintf("h%04d", f.nextHash), firstSeen, payloadType, decoded, fromPubkey)
}

// obs adds an observation of txID by observer rowid (nil stores NULL). Each
// gets its own path, as a re-heard copy does, so the dedup index
// (transmission, observer, path) accepts repeats of the same pair.
func (f *advertFixture) obs(txID int64, observerIdx any) int64 {
	f.t.Helper()
	f.nextPath++
	return f.exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, observerIdx, fmt.Sprintf(`["%04x"]`, f.nextPath), time.Now().Unix())
}

// advert adds an ADVERT by pk heard by the given observers.
func (f *advertFixture) advert(pk, firstSeen string, observerIDs ...string) int64 {
	f.t.Helper()
	f.node(pk)
	txID := f.tx(int(payloadADVERT), pk, `{"pubKey":"`+pk+`"}`, firstSeen)
	for _, o := range observerIDs {
		f.obs(txID, f.observers[o])
	}
	return txID
}

func (f *advertFixture) query(q string, args ...any) []string {
	f.t.Helper()
	rows, err := f.s.db.Query(q, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, pk)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func regionPlaceholders(codes []string) (string, []any) {
	ph := make([]string, len(codes))
	args := make([]any, len(codes))
	for i, c := range codes {
		ph[i] = "?"
		args[i] = c
	}
	return strings.Join(ph, ","), args
}

// legacy is the server's pre-#2101 region subquery, verbatim: the oracle.
func (f *advertFixture) legacy(codes ...string) []string {
	f.t.Helper()
	ph, args := regionPlaceholders(codes)
	return f.query(`SELECT public_key FROM nodes WHERE public_key IN (
		SELECT DISTINCT t.from_pubkey
		FROM transmissions t
		JOIN observations o ON o.transmission_id = t.id
		JOIN observers obs ON obs.rowid = o.observer_idx
		WHERE t.payload_type = 4
		AND UPPER(TRIM(obs.iata)) IN (`+ph+`)
	)`, args...)
}

// fromTable is the server's #2101 region subquery over node_advert_observers.
func (f *advertFixture) fromTable(codes ...string) []string {
	f.t.Helper()
	ph, args := regionPlaceholders(codes)
	return f.query(`SELECT public_key FROM nodes WHERE public_key IN (
		SELECT nao.public_key
		FROM node_advert_observers nao
		JOIN observers obs ON obs.rowid = nao.observer_idx
		WHERE UPPER(TRIM(obs.iata)) IN (`+ph+`)
	)`, args...)
}

// assertParity compares table and oracle for each region set. wantNonEmpty
// guards against a vacuous pass on a fixture that matched nothing.
func (f *advertFixture) assertParity(wantNonEmpty bool, regionSets ...[]string) {
	f.t.Helper()
	for _, codes := range regionSets {
		want := f.legacy(codes...)
		if wantNonEmpty && len(want) == 0 {
			f.t.Fatalf("region %v: legacy query matched nothing, the comparison would be vacuous", codes)
		}
		if got := f.fromTable(codes...); strings.Join(got, ",") != strings.Join(want, ",") {
			f.t.Errorf("region %v: table %v, legacy %v", codes, got, want)
		}
	}
}

func (f *advertFixture) pair(pk, observerID string) (lastSeen string, lastObsID int64, ok bool) {
	f.t.Helper()
	err := f.s.db.QueryRow(`SELECT last_seen, last_obs_id FROM node_advert_observers
		WHERE public_key = ? AND observer_idx = ?`, pk, f.observers[observerID]).Scan(&lastSeen, &lastObsID)
	if err != nil {
		return "", 0, false
	}
	return lastSeen, lastObsID, true
}

func catchUp(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.catchUpAdvertObservers(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

const recent = "2026-09-30T12:00:00Z"

// The table answers every region filter exactly as the legacy subquery did:
// padded and lower-case IATA tags, multi-region sets, nodes heard in two
// regions, and the rows the legacy query never matched (non-ADVERTs, empty
// from_pubkey, observations without an observer, untagged observers).
func TestNodeAdvertObserversMatchesLegacyQuery(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.observer("o-edi2", " edi ")
	f.observer("o-gla", "GLA")
	f.observer("o-none", nil)

	f.advert("aa01", recent, "o-edi")
	f.advert("aa02", recent, "o-gla")
	f.advert("aa03", recent, "o-edi2", "o-gla")
	f.advert("aa04", recent, "o-none")
	// Not adverts: never region members.
	f.node("bb01")
	f.obs(f.tx(5, "bb01", `{}`, recent), f.observers["o-edi"])
	// Advert whose pubkey could not be decoded: BackfillFromPubkey's "" sentinel.
	f.node("cc01")
	f.obs(f.tx(int(payloadADVERT), "", `{}`, recent), f.observers["o-edi"])
	// Advert observation with no observer.
	f.node("dd01")
	f.obs(f.tx(int(payloadADVERT), "dd01", `{"pubKey":"dd01"}`, recent), nil)

	catchUp(t, s)
	// Codes arrive upper-cased (server normalizeRegionCodes); the stored
	// tags' case and padding are what " edi " exercises.
	f.assertParity(true, []string{"EDI"}, []string{"GLA"}, []string{"EDI", "GLA"})
	f.assertParity(false, []string{"AMS"})
}

// A legacy ADVERT row whose from_pubkey is still NULL is recorded under the
// pubkey BackfillFromPubkey will write, so once that backfill has run the
// table and the legacy query agree without the builder revisiting the row.
func TestNodeAdvertObserversNullFromPubkeyMatchesBackfill(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.node("ee01")
	f.obs(f.tx(int(payloadADVERT), nil, `{"pubKey":"ee01"}`, recent), f.observers["o-edi"])

	catchUp(t, s)
	if got := f.fromTable("EDI"); strings.Join(got, ",") != "ee01" {
		t.Fatalf("NULL from_pubkey advert should be recorded via decoded_json, got %v", got)
	}
	s.BackfillFromPubkey(100, 0, nil)
	f.assertParity(true, []string{"EDI"})
}

// Steps advance through batches that contain no adverts at all; a watermark
// that only moved on a match would never leave the first such batch.
func TestNodeAdvertObserversBatchesAdvanceThroughGaps(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	for i := range 6 {
		f.obs(f.tx(5, nil, `{}`, recent), f.observers["o-edi"]) // non-advert padding
		f.advert(fmt.Sprintf("ff%02d", i), recent, "o-edi")
	}
	for range 5 {
		f.obs(f.tx(5, nil, `{}`, recent), f.observers["o-edi"])
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.catchUpAdvertObserversBatched(context.Background(), 0, 1)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("catch-up with batch 1 did not finish: the watermark is not advancing")
	}
	f.assertParity(true, []string{"EDI"})
	var maxObs int64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM observations`).Scan(&maxObs); err != nil {
		t.Fatal(err)
	}
	s.advertObs.mu.Lock()
	wm := s.advertObs.watermark
	s.advertObs.mu.Unlock()
	if wm != maxObs {
		t.Errorf("watermark %d, want MAX(observations.id) %d", wm, maxObs)
	}
}

// New observations are folded in on the next catch-up, and a pair keeps the
// newest transmission first_seen and observation id it has seen, whatever
// order the observations arrive in.
func TestNodeAdvertObserversDeltaKeepsNewest(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.observer("o-gla", "GLA")
	newer := f.advert("ab01", "2026-09-30T12:00:00Z", "o-edi")
	catchUp(t, s)

	// A late copy of an OLDER advert, folded in its own step: the upsert, not
	// the in-step aggregation, must keep the newer last_seen, or prune would
	// drop the pair while its newer advert is still retained.
	older := f.advert("ab01", "2026-09-01T12:00:00Z")
	lateOld := f.obs(older, f.observers["o-edi"])
	if n := catchUp(t, s); n == 0 {
		t.Fatal("delta folded nothing")
	}
	lastSeen, lastObs, ok := f.pair("ab01", "o-edi")
	if !ok {
		t.Fatal("pair ab01/o-edi missing")
	}
	if lastSeen != "2026-09-30T12:00:00Z" {
		t.Errorf("last_seen = %s after an older advert, want the newest advert's first_seen", lastSeen)
	}
	if lastObs != lateOld {
		t.Errorf("last_obs_id = %d, want %d", lastObs, lateOld)
	}

	lateID := f.obs(newer, f.observers["o-edi"])
	f.advert("ab02", recent, "o-gla")
	catchUp(t, s)
	if _, lastObs, _ = f.pair("ab01", "o-edi"); lastObs != lateID {
		t.Errorf("last_obs_id = %d, want %d", lastObs, lateID)
	}
	f.assertParity(true, []string{"EDI"}, []string{"GLA"})
	if n := catchUp(t, s); n != 0 {
		t.Errorf("caught-up builder folded %d pairs, want 0", n)
	}
}

// After a restart the builder resumes from the table rather than rescanning
// the whole history.
func TestNodeAdvertObserversResumesFromTable(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.advert("ac01", recent, "o-edi")
	catchUp(t, s)
	_, lastObs, _ := f.pair("ac01", "o-edi")

	s.advertObs.mu.Lock()
	s.advertObs.loaded, s.advertObs.watermark = false, 0 // as on a fresh process
	s.advertObs.mu.Unlock()
	if _, _, err := s.stepAdvertObservers(context.Background(), lastObs, nodeAdvertObserversBatch); err != nil {
		t.Fatal(err)
	}
	s.advertObs.mu.Lock()
	wm := s.advertObs.watermark
	s.advertObs.mu.Unlock()
	if wm != lastObs {
		t.Errorf("resumed watermark %d, want MAX(last_obs_id) %d", wm, lastObs)
	}
}

// PruneOldPackets drops a pair once every advert behind it is pruned, and
// keeps one with any advert left, so the table tracks the legacy query
// across retention.
func TestNodeAdvertObserversPrunedWithPackets(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	old := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	fresh := time.Now().UTC().Format(time.RFC3339)
	f.advert("ad01", old, "o-edi")
	f.advert("ad02", old, "o-edi")
	f.advert("ad02", fresh, "o-edi")
	f.advert("ad03", fresh, "o-edi")
	catchUp(t, s)

	if _, err := s.PruneOldPackets(7); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.pair("ad01", "o-edi"); ok {
		t.Error("ad01's only advert was pruned, its pair should be gone")
	}
	if _, _, ok := f.pair("ad02", "o-edi"); !ok {
		t.Error("ad02 still has a fresh advert, its pair should stay")
	}
	f.assertParity(true, []string{"EDI"})
}

// The builder's backfill runs as an async migration: it is recorded done,
// the table is complete, and a later start on the same database does not
// run it again but resumes folding on the ticker.
func TestNodeAdvertObserversBuilderBackfillAndTick(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.advert("ae01", recent, "o-edi")

	stop := s.StartNodeAdvertObserversBuilder(10 * time.Millisecond)
	t.Cleanup(stop)
	s.WaitForAsyncMigrations()
	if st, err := s.AsyncMigrationStatus(dbschema.NodeAdvertObserversBackfill); err != nil || st != "done" {
		t.Fatalf("backfill status %q (%v), want done", st, err)
	}
	if !s.advertObs.ready.Load() {
		t.Fatal("builder not ready after its backfill")
	}
	f.assertParity(true, []string{"EDI"})

	f.advert("ae02", recent, "o-edi")
	waitUntil(t, func() bool { _, _, ok := f.pair("ae02", "o-edi"); return ok })
	stop()

	// A second start (a restart) finds the migration done and is ready at once.
	s.advertObs.ready.Store(false)
	stop2 := s.StartNodeAdvertObserversBuilder(10 * time.Millisecond)
	t.Cleanup(stop2)
	if !s.advertObs.ready.Load() {
		t.Error("restart with a done backfill should be ready immediately")
	}
}

// A backfill that fails is recorded failed and leaves the builder not ready,
// so the server keeps its legacy query rather than reading a partial table.
func TestNodeAdvertObserversBackfillFailureNotReady(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`DROP TABLE node_advert_observers`); err != nil {
		t.Fatal(err)
	}
	stop := s.StartNodeAdvertObserversBuilder(time.Hour)
	t.Cleanup(stop)
	s.WaitForAsyncMigrations()
	if st, _ := s.AsyncMigrationStatus(dbschema.NodeAdvertObserversBackfill); st != "failed" {
		t.Errorf("backfill status %q, want failed", st)
	}
	if s.advertObs.ready.Load() {
		t.Error("builder must not be ready after a failed backfill")
	}
}

// Builder steps, packet pruning and live inserts run together without a
// race (go test -race) or a deadlock on the single connection, and the
// table still agrees with the oracle afterwards.
func TestNodeAdvertObserversConcurrentWithPruneAndIngest(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	old := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	for i := range 20 {
		f.advert(fmt.Sprintf("af%02d", i), old, "o-edi")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 20 {
			if _, err := s.catchUpAdvertObserversBatched(context.Background(), 0, 3); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 5 {
			if _, err := s.PruneOldPackets(7); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 20 {
			if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name) VALUES (?, 'n')`, fmt.Sprintf("ag%02d", i)); err != nil {
				errs <- err
				return
			}
			res, err := s.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey)
				VALUES ('', ?, ?, 1, 4, '{}', ?)`, fmt.Sprintf("hag%02d", i), time.Now().UTC().Format(time.RFC3339), fmt.Sprintf("ag%02d", i))
			if err != nil {
				errs <- err
				return
			}
			txID, _ := res.LastInsertId()
			if _, err := s.db.Exec(`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (?, ?, ?)`,
				txID, f.observers["o-edi"], time.Now().Unix()); err != nil {
				errs <- err
				return
			}
		}
	}()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("builder, prune and ingest deadlocked")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// A final prune and catch-up settle the table; it must match the oracle.
	if _, err := s.PruneOldPackets(7); err != nil {
		t.Fatal(err)
	}
	catchUp(t, s)
	f.assertParity(true, []string{"EDI"})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 10s")
}

func advertWatermark(s *Store) int64 {
	s.advertObs.mu.Lock()
	defer s.advertObs.mu.Unlock()
	return s.advertObs.watermark
}

// A step whose write fails must not advance the watermark, or the
// observations it covered would never be folded in.
func TestNodeAdvertObserversFailedWriteKeepsWatermark(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.advert("ba01", recent, "o-edi")
	catchUp(t, s)
	before := advertWatermark(s)

	f.advert("ba02", recent, "o-edi")
	f.exec(`ALTER TABLE node_advert_observers RENAME TO node_advert_observers_gone`)
	_, err := s.catchUpAdvertObservers(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "prepare upsert") {
		t.Fatalf("expected the upsert to fail, got %v", err)
	}
	if got := advertWatermark(s); got != before {
		t.Fatalf("watermark moved %d -> %d past observations that were not written", before, got)
	}

	f.exec(`ALTER TABLE node_advert_observers_gone RENAME TO node_advert_observers`)
	catchUp(t, s)
	if _, _, ok := f.pair("ba02", "o-edi"); !ok {
		t.Error("observations behind a failed step were not folded in on the retry")
	}
}

// Cancelling a multi-step catch-up stops between steps; the steps already
// written stand and the watermark covers exactly them.
func TestNodeAdvertObserversCatchUpCancelled(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	for i := range 5 {
		f.advert(fmt.Sprintf("bb%02d", i), recent, "o-edi")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.catchUpAdvertObserversBatched(ctx, time.Hour, 1) // yields between steps
		done <- err
	}()
	waitUntil(t, func() bool { return advertWatermark(s) >= 1 })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled catch-up returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled catch-up did not return")
	}
	var lastObs int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(last_obs_id), 0) FROM node_advert_observers`).Scan(&lastObs); err != nil {
		t.Fatal(err)
	}
	if wm := advertWatermark(s); wm != 1 || lastObs != 1 {
		t.Errorf("after one step: watermark %d, MAX(last_obs_id) %d; want both 1", wm, lastObs)
	}
}

// Ticks are skipped until the backfill is done, and a failing tick is
// logged and survived: the next tick folds normally.
func TestNodeAdvertObserversTickSkipsAndSurvivesErrors(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.observer("o-edi", "EDI")
	f.advert("bc01", recent, "o-edi")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.runAdvertObserversTicker(ctx, 5*time.Millisecond) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(50 * time.Millisecond) // several ticks while not ready
	if _, _, ok := f.pair("bc01", "o-edi"); ok {
		t.Fatal("ticker folded before the backfill was done")
	}

	f.exec(`ALTER TABLE node_advert_observers RENAME TO node_advert_observers_gone`)
	s.advertObs.ready.Store(true)
	time.Sleep(50 * time.Millisecond) // several failing ticks
	f.exec(`ALTER TABLE node_advert_observers_gone RENAME TO node_advert_observers`)
	waitUntil(t, func() bool { _, _, ok := f.pair("bc01", "o-edi"); return ok })
}

// PruneOldPackets reports a failure to prune the pairs table.
func TestNodeAdvertObserversPruneError(t *testing.T) {
	s := newTestStore(t)
	f := newAdvertFixture(t, s)
	f.exec(`DROP TABLE node_advert_observers`)
	if _, err := s.PruneOldPackets(7); err == nil || !strings.Contains(err.Error(), "node_advert_observers") {
		t.Fatalf("expected the pairs prune to fail, got %v", err)
	}
}

// A non-positive interval falls back to the default, as the neighbor
// builder does.
func TestNodeAdvertObserversDefaultInterval(t *testing.T) {
	s := newTestStore(t)
	stop := s.StartNodeAdvertObserversBuilder(0)
	s.WaitForAsyncMigrations()
	stop()
	stop() // idempotent
}
