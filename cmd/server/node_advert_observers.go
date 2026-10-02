package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// /api/nodes?region= reads node membership from node_advert_observers
// (#2101), the (node, observer) pairs the ingestor derives from ADVERT
// observations (cmd/ingestor/node_advert_observers.go). The legacy filter
// re-derived the same set with a transmissions ⋈ observations ⋈ observers
// scan over the whole advert history, uncached and evaluated twice per
// request (COUNT and page): ~10 s per evaluation on a 1.4 GB database.
//
// The table is only trusted once the ingestor's backfill has completed, so a
// partly built table is never read. Until then, and on a v2 schema the
// ingestor no longer writes, GetNodes keeps the legacy query.

// nodeAdvertObserversRecheck bounds how often a server that started before
// the backfill finished looks again, so it switches over without a restart
// and without a query per request.
const nodeAdvertObserversRecheck = 30 * time.Second

// nodeAdvertObserversReady reports whether the region filter may read
// node_advert_observers. Once true it stays true for the process.
func (db *DB) nodeAdvertObserversReady() bool {
	if !db.isV3 {
		return false
	}
	if db.nodeAdvertObserversLatched.Load() {
		return true
	}
	now := time.Now().UnixNano()
	last := db.nodeAdvertObserversCheckedAt.Load()
	if last != 0 && now-last < int64(nodeAdvertObserversRecheck) {
		return false
	}
	// One caller per interval checks; the rest keep the legacy query.
	if !db.nodeAdvertObserversCheckedAt.CompareAndSwap(last, now) {
		return false
	}
	var status string
	err := db.conn.QueryRow(`SELECT status FROM _async_migrations WHERE name = ?`,
		dbschema.NodeAdvertObserversBackfill).Scan(&status)
	if err != nil || status != "done" {
		// No row, no table (an ingestor that predates #2101) or not done yet.
		return false
	}
	if db.nodeAdvertObserversLatched.CompareAndSwap(false, true) {
		log.Printf("[nodes] region filter now reads node_advert_observers (#2101)")
	}
	return true
}

// nodeAdvertObserversRegionFilter is the GetNodes WHERE clause for the region
// codes bound to placeholders: nodes with an ADVERT heard by an observer
// whose IATA tag is one of them. The tag is joined at query time, as in the
// legacy filter, so it follows an observer that changes region.
func nodeAdvertObserversRegionFilter(placeholders []string) string {
	return fmt.Sprintf(`public_key IN (
		SELECT nao.public_key
		FROM node_advert_observers nao
		JOIN observers obs ON obs.rowid = nao.observer_idx
		WHERE UPPER(TRIM(obs.iata)) IN (%s)
	)`, strings.Join(placeholders, ","))
}
