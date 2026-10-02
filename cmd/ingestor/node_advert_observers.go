package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// node_advert_observers (#2101) records which observers have heard each
// node's ADVERTs: one row per (public_key, observer_idx). The server's
// /api/nodes?region= filter reads it instead of re-deriving the same set
// from transmissions ⋈ observations ⋈ observers on every request, which on
// a 1.4 GB database took ~10 s per evaluation, twice per page.
//
// The table is derived, and this file is its only writer. Observations are
// folded in by observations.id, in bounded steps:
//
//   - the first build is an async migration (dbschema.NodeAdvertObserversBackfill)
//     that walks the whole history; the server keeps its legacy query until
//     that migration is `done`;
//   - after it, a ticker folds in whatever has arrived since the last step;
//   - PruneOldPackets drops pairs whose newest advert it has just pruned.
//
// A pair matches the legacy subquery's semantics exactly: it exists while
// at least one ADVERT transmission with a non-empty from_pubkey, still in
// the database, was observed by that observer. The region code is not
// stored; the server joins observers at query time, so an observer moving
// region is reflected immediately, as before.

const (
	// NodeAdvertObserversInterval is how often new observations are folded
	// in after the backfill. It bounds how long a node newly heard in a
	// region takes to appear under that region's filter.
	NodeAdvertObserversInterval = 30 * time.Second

	// nodeAdvertObserversBatch caps the observation ids one step reads.
	// The ingestor has a single connection (db.go SetMaxOpenConns(1)), so a
	// step holds it for its read and its upsert; on a 2.4M-observation
	// database the slowest 50k step read in 0.34 s (#1339 uses the same cap).
	nodeAdvertObserversBatch = 50000

	// nodeAdvertObserversYield is the pause between steps of a multi-step
	// catch-up, so live ingest is never starved by a backfill.
	nodeAdvertObserversYield = 50 * time.Millisecond

	// nodeAdvertObserversSlowStep is logged loudly when exceeded.
	nodeAdvertObserversSlowStep = 2 * time.Second

	// nodeAdvertObserversStopWait bounds how long stop waits for an
	// in-flight step, matching the neighbor-edges builder.
	nodeAdvertObserversStopWait = 5 * time.Second
)

// advertObserversState is the builder's in-process state. mu serialises
// steps and the prune of stale pairs, so a step can never write back a pair
// whose transmissions a concurrent prune has just removed.
type advertObserversState struct {
	mu        sync.Mutex
	watermark int64 // highest observations.id folded in; guarded by mu
	loaded    bool  // watermark read from the table; guarded by mu
	ready     atomic.Bool
}

type advertObserverKey struct {
	publicKey   string
	observerIdx int64
}

type advertObserverPair struct {
	lastSeen  string
	lastObsID int64
}

// StartNodeAdvertObserversBuilder schedules the backfill (once per database)
// and the periodic fold, and returns a stop function.
func (s *Store) StartNodeAdvertObserversBuilder(interval time.Duration) func() {
	if interval <= 0 {
		interval = NodeAdvertObserversInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.scheduleAdvertObserversBackfill(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runAdvertObserversTicker(ctx, interval)
	}()

	var once sync.Once
	return func() {
		once.Do(cancel)
		select {
		case <-done:
		case <-time.After(nodeAdvertObserversStopWait):
		}
	}
}

// scheduleAdvertObserversBackfill runs the first full build as an async
// migration, unless a previous run already completed it.
func (s *Store) scheduleAdvertObserversBackfill(ctx context.Context) {
	status, err := s.AsyncMigrationStatus(dbschema.NodeAdvertObserversBackfill)
	if err == nil && status == "done" {
		s.advertObs.ready.Store(true)
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Not scheduled: the builder stays not ready and the server keeps
		// its legacy query, which is correct, just slow.
		log.Printf("[node-advert-observers] reading backfill status failed: %v", err)
		return
	}
	err = s.RunAsyncMigration(ctx, dbschema.NodeAdvertObserversBackfill, func(ctx context.Context, _ *sql.DB) error {
		start := time.Now()
		n, err := s.catchUpAdvertObservers(ctx, nodeAdvertObserversYield)
		if err != nil {
			return err
		}
		log.Printf("[node-advert-observers] backfill: %d pair updates in %s",
			n, time.Since(start).Round(time.Millisecond))
		s.advertObs.ready.Store(true)
		return nil
	})
	if err != nil {
		log.Printf("[node-advert-observers] scheduling backfill failed: %v", err)
	}
}

// runAdvertObserversTicker folds new observations every interval until ctx
// ends. Until the backfill is done it owns the catch-up, so ticks are skipped
// rather than contending for the same steps.
func (s *Store) runAdvertObserversTicker(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.advertObs.ready.Load() {
			continue
		}
		start := time.Now()
		n, err := s.catchUpAdvertObservers(ctx, nodeAdvertObserversYield)
		if err != nil {
			log.Printf("[node-advert-observers] tick error: %v", err)
		} else if n > 0 {
			log.Printf("[node-advert-observers] tick: %d pair updates in %s",
				n, time.Since(start).Round(time.Millisecond))
		}
	}
}

// catchUpAdvertObservers folds every observation up to the current
// MAX(observations.id) into node_advert_observers, one bounded step at a
// time, and returns the number of pair upserts.
func (s *Store) catchUpAdvertObservers(ctx context.Context, yield time.Duration) (int, error) {
	return s.catchUpAdvertObserversBatched(ctx, yield, nodeAdvertObserversBatch)
}

func (s *Store) catchUpAdvertObserversBatched(ctx context.Context, yield time.Duration, batch int64) (int, error) {
	var target int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM observations`).Scan(&target); err != nil {
		return 0, fmt.Errorf("read observations high-water mark: %w", err)
	}
	total := 0
	for {
		n, more, err := s.stepAdvertObservers(ctx, target, batch)
		total += n
		if err != nil || !more {
			return total, err
		}
		if yield > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(yield):
			}
		}
	}
}

// stepAdvertObservers folds the next batch of observations at or below
// target and reports whether more remain. The watermark advances by the
// whole batch even when it held no adverts.
func (s *Store) stepAdvertObservers(ctx context.Context, target, batch int64) (int, bool, error) {
	st := &s.advertObs
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.loaded {
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(last_obs_id), 0) FROM node_advert_observers`).Scan(&st.watermark); err != nil {
			return 0, false, fmt.Errorf("read node_advert_observers watermark: %w", err)
		}
		st.loaded = true
	}
	if st.watermark >= target {
		return 0, false, nil
	}
	hi := min(st.watermark+batch, target)

	start := time.Now()
	n, err := s.foldAdvertObservers(ctx, st.watermark, hi)
	if err != nil {
		return 0, false, err
	}
	if d := time.Since(start); d > nodeAdvertObserversSlowStep {
		log.Printf("[node-advert-observers] SLOW step: observations %d..%d took %s", st.watermark+1, hi, d.Round(time.Millisecond))
	}
	st.watermark = hi
	return n, hi < target, nil
}

// foldAdvertObservers upserts the (node, observer) pairs found in ADVERT
// observations with lo < observations.id <= hi. Caller holds advertObs.mu.
func (s *Store) foldAdvertObservers(ctx context.Context, lo, hi int64) (int, error) {
	// The read returns, and so releases the single connection, before
	// WriterTx asks for it.
	pairs, err := s.readAdvertObserverPairs(ctx, lo, hi)
	if err != nil || len(pairs) == 0 {
		return 0, err
	}
	return len(pairs), s.upsertAdvertObserverPairs(ctx, pairs)
}

// readAdvertObserverPairs collects the newest first_seen and observation id
// per (node, observer) among ADVERT observations in (lo, hi].
func (s *Store) readAdvertObserverPairs(
	ctx context.Context, lo, hi int64,
) (map[advertObserverKey]advertObserverPair, error) {
	// CROSS JOIN pins the join order to the observations rowid range, so the
	// step's cost is bounded by the batch whatever the planner statistics
	// say (#2058 changed the plan of the region query this table replaces).
	// decoded_json is read only where from_pubkey is still NULL: those are
	// legacy rows BackfillFromPubkey has not reached yet, and it will set
	// from_pubkey to exactly extractPubkeyFromAdvertJSON(decoded_json).
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.observer_idx, t.first_seen, t.from_pubkey,
		       CASE WHEN t.from_pubkey IS NULL THEN t.decoded_json END
		FROM observations o
		CROSS JOIN transmissions t ON t.id = o.transmission_id
		WHERE o.id > ? AND o.id <= ?
		  AND t.payload_type = ?
		  AND o.observer_idx IS NOT NULL`, lo, hi, payloadADVERT)
	if err != nil {
		return nil, fmt.Errorf("scan advert observations: %w", err)
	}
	defer rows.Close()
	pairs := make(map[advertObserverKey]advertObserverPair)
	for rows.Next() {
		var obsID, observerIdx int64
		var firstSeen string
		var fromPubkey, decodedJSON sql.NullString
		if err := rows.Scan(&obsID, &observerIdx, &firstSeen, &fromPubkey, &decodedJSON); err != nil {
			return nil, fmt.Errorf("scan advert observation: %w", err)
		}
		pk := fromPubkey.String
		if !fromPubkey.Valid {
			pk = extractPubkeyFromAdvertJSON(decodedJSON.String)
		}
		if pk == "" || firstSeen == "" {
			continue
		}
		k := advertObserverKey{publicKey: pk, observerIdx: observerIdx}
		p := pairs[k]
		p.lastSeen = max(p.lastSeen, firstSeen)
		p.lastObsID = max(p.lastObsID, obsID)
		pairs[k] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan advert observations: %w", err)
	}
	return pairs, nil
}

// upsertAdvertObserverPairs writes pairs in one writer transaction, keeping
// each row's newest last_seen and last_obs_id.
func (s *Store) upsertAdvertObserverPairs(ctx context.Context, pairs map[advertObserverKey]advertObserverPair) error {
	return s.WriterTx("node_advert_observers", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO node_advert_observers (public_key, observer_idx, last_seen, last_obs_id)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(public_key, observer_idx) DO UPDATE SET
			  last_seen = MAX(last_seen, excluded.last_seen),
			  last_obs_id = MAX(last_obs_id, excluded.last_obs_id)`)
		if err != nil {
			return fmt.Errorf("prepare upsert: %w", err)
		}
		defer stmt.Close()
		for k, p := range pairs {
			if _, err := stmt.ExecContext(ctx, k.publicKey, k.observerIdx, p.lastSeen, p.lastObsID); err != nil {
				return fmt.Errorf("upsert node_advert_observers: %w", err)
			}
		}
		return nil
	})
}

// pruneAdvertObservers drops pairs whose newest ADVERT is older than cutoff,
// the same transmissions.first_seen cutoff PruneOldPackets has just applied.
// It holds advertObs.mu so a builder step cannot interleave and write a pair
// back from transmissions that are already gone.
func (s *Store) pruneAdvertObservers(cutoff string) (int64, error) {
	s.advertObs.mu.Lock()
	defer s.advertObs.mu.Unlock()
	var n int64
	err := s.WriterTx("prune_node_advert_observers", func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM node_advert_observers WHERE last_seen < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("prune node_advert_observers: %w", err)
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
