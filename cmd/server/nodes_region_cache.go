package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Region-scoped node membership cache for GetNodes.
//
// `/api/nodes?region=X` restricts the node list to nodes whose ADVERTs were
// heard by an observer in region X. That used to be an inline
// `public_key IN (SELECT DISTINCT from_pubkey FROM transmissions ⋈
// observations ⋈ observers ...)` subquery, uncached, evaluated twice per
// request (COUNT(*) and the page). On a 1.4 GB database (1.47M observations)
// each evaluation took ~10s, so every region-filtered page cost ~20s of CPU,
// and fetchAllNodes() pages at 500 — a single open Nodes/Map tab pinned a
// 2-vCPU host.
//
// Membership only grows as observations arrive, so it is cached per region
// set with an observations.id watermark:
//   - fresh (< nodeRegionFreshTTL): served as-is;
//   - stale: served as-is while ONE background refresh scans only
//     observations with id > watermark (a rowid range, milliseconds);
//   - every nodeRegionRebuildInterval the refresh is a full rebuild instead,
//     so retention pruning, observer IATA changes and late from_pubkey
//     backfills are picked up.
// Only the first request for a region ever waits on the full scan, and
// concurrent first requests share it (same #1910 singleflight pattern as
// channelsSF).

const (
	nodeRegionFreshTTL        = 30 * time.Second
	nodeRegionRebuildInterval = 30 * time.Minute
)

type nodeRegionEntry struct {
	keys      map[string]struct{} // from_pubkeys heard in the region set
	keysJSON  string              // keys as a JSON array, bound to json_each(?)
	watermark int64               // max observations.id covered by keys
	refreshed time.Time
	built     time.Time // last full rebuild
}

// nodeRegionKey canonicalises normalised region codes so "EDI,GLA",
// "gla, edi" and "EDI,EDI,GLA" share one cache entry.
func nodeRegionKey(codes []string) string {
	seen := make(map[string]struct{}, len(codes))
	uniq := make([]string, 0, len(codes))
	for _, c := range codes {
		if _, ok := seen[c]; !ok {
			seen[c] = struct{}{}
			uniq = append(uniq, c)
		}
	}
	sort.Strings(uniq)
	return strings.Join(uniq, ",")
}

func (db *DB) getNodeRegionEntry(key string) *nodeRegionEntry {
	db.nodeRegionCacheMu.Lock()
	defer db.nodeRegionCacheMu.Unlock()
	return db.nodeRegionCache[key]
}

func (db *DB) setNodeRegionEntry(key string, e *nodeRegionEntry) {
	db.nodeRegionCacheMu.Lock()
	defer db.nodeRegionCacheMu.Unlock()
	if db.nodeRegionCache == nil || len(db.nodeRegionCache) > maxCacheEntries {
		db.nodeRegionCache = make(map[string]*nodeRegionEntry)
	}
	db.nodeRegionCache[key] = e
}

// nodeRegionKeysJSON returns the JSON array of node public keys heard in the
// given (normalised, non-empty) region codes, for binding to
// `public_key IN (SELECT value FROM json_each(?))`.
func (db *DB) nodeRegionKeysJSON(codes []string) (string, error) {
	key := nodeRegionKey(codes)

	if e := db.getNodeRegionEntry(key); e != nil {
		if time.Since(e.refreshed) >= nodeRegionFreshTTL {
			// Stale-while-revalidate: kick one refresh, don't wait on it.
			// DoChan dedups against an in-flight refresh for the same key.
			db.nodeRegionSF.DoChan(key, func() (interface{}, error) {
				return db.refreshNodeRegion(key, codes)
			})
		}
		return e.keysJSON, nil
	}

	v, err, _ := db.nodeRegionSF.Do(key, func() (interface{}, error) {
		return db.refreshNodeRegion(key, codes)
	})
	if err != nil {
		return "", err
	}
	return v.(*nodeRegionEntry).keysJSON, nil
}

// refreshNodeRegion brings the cache entry for key up to date: a delta scan
// past the watermark when an entry exists and is younger than the rebuild
// interval, a full scan otherwise. Always runs inside nodeRegionSF.
func (db *DB) refreshNodeRegion(key string, codes []string) (*nodeRegionEntry, error) {
	prev := db.getNodeRegionEntry(key)
	if prev != nil && time.Since(prev.refreshed) < nodeRegionFreshTTL {
		return prev, nil // a flight that finished just before this one got here
	}

	if db.nodeRegionQueryHook != nil {
		db.nodeRegionQueryHook()
	}

	// Pin the upper bound first. The ingestor is the single writer and ids are
	// AUTOINCREMENT, so every id <= wm is already committed and visible to the
	// scan below; anything newer is left for the next delta.
	var wm int64
	maxObs := db.conn.QueryRow("SELECT COALESCE(MAX(id), 0) FROM observations")
	if db.stmtMaxObsID != nil {
		maxObs = db.stmtMaxObsID.QueryRow()
	}
	if err := maxObs.Scan(&wm); err != nil {
		if prev != nil {
			return prev, err
		}
		return nil, err
	}

	full := prev == nil || time.Since(prev.built) >= nodeRegionRebuildInterval
	var lo int64
	keys := make(map[string]struct{})
	built := time.Now()
	if !full {
		if wm <= prev.watermark {
			e := *prev
			e.refreshed = time.Now()
			db.setNodeRegionEntry(key, &e)
			return &e, nil
		}
		lo = prev.watermark
		for k := range prev.keys {
			keys[k] = struct{}{}
		}
		built = prev.built
	}

	start := time.Now()
	added, err := db.scanNodeRegionKeys(codes, lo, wm, !full, keys)
	if err != nil {
		if prev != nil {
			log.Printf("[nodes-region] refresh %s failed, serving previous: %v", key, err)
			return prev, err
		}
		return nil, err
	}
	if full {
		log.Printf("[nodes-region] %s: full build, %d nodes in %v (obs id <= %d)", key, len(keys), time.Since(start).Round(time.Millisecond), wm)
	} else if added > 0 {
		log.Printf("[nodes-region] %s: +%d nodes from obs %d..%d in %v", key, added, lo+1, wm, time.Since(start).Round(time.Millisecond))
	}

	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	buf, _ := json.Marshal(sorted)

	e := &nodeRegionEntry{
		keys:      keys,
		keysJSON:  string(buf),
		watermark: wm,
		refreshed: time.Now(),
		built:     built,
	}
	db.setNodeRegionEntry(key, e)
	return e, nil
}

// scanNodeRegionKeys adds to keys every ADVERT from_pubkey observed by an
// observer in codes with lo < observations.id <= hi, returning how many were
// new. For a delta scan the join order is forced (CROSS JOIN) to drive from
// the observations rowid range; left to the planner, sqlite_stat1 (#2058)
// makes it start from the region's observers and walk their whole history.
func (db *DB) scanNodeRegionKeys(codes []string, lo, hi int64, delta bool, keys map[string]struct{}) (int, error) {
	placeholders := make([]string, len(codes))
	args := make([]interface{}, 0, len(codes)+2)
	for i, c := range codes {
		placeholders[i] = "?"
		args = append(args, c)
	}
	args = append(args, lo, hi)

	joinCond := "obs.rowid = o.observer_idx"
	if !db.isV3 {
		joinCond = "obs.id = o.observer_id"
	}
	join := "JOIN"
	if delta {
		join = "CROSS JOIN"
	}
	// #1143: from_pubkey is a dedicated, indexed column populated at ingest
	// (and backfilled) for ADVERT rows, so no JSON_EXTRACT is needed here.
	q := fmt.Sprintf(`SELECT DISTINCT t.from_pubkey
		FROM observations o
		%[1]s transmissions t ON t.id = o.transmission_id
		%[1]s observers obs ON %[2]s
		WHERE t.payload_type = 4
		AND t.from_pubkey IS NOT NULL
		AND UPPER(TRIM(obs.iata)) IN (%[3]s)
		AND o.id > ? AND o.id <= ?`, join, joinCond, strings.Join(placeholders, ","))

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	added := 0
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return added, err
		}
		if _, ok := keys[pk]; !ok {
			keys[pk] = struct{}{}
			added++
		}
	}
	return added, rows.Err()
}
