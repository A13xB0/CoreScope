package main

import (
	"net/http"
	"strings"
	"time"
)

// Polling ownership: one poller per repeater. Each observer publishes the
// repeaters it polls (meshcore/<region>/<observer>/polling), which the
// ingestor keeps in guest_polling_claims. When several observers claim one
// repeater, all but one should stand down — they would otherwise all poll it
// — and /api/guest-polling/owners tells an observer, for each repeater it
// claims, which observer that one is.
//
// The owner is picked from the observers that claimed the repeater in the
// last hour (so a poller that goes quiet hands over within the hour), ranked
// by: a battery reading credited to it in the last two hours first (it is
// demonstrably getting answers), then fewer reply hops, then the earliest
// first claim, then observer id.

const (
	pollOwnerClaimWindow   = time.Hour
	pollOwnerReadingWindow = 2 * time.Hour
)

// guestPollingClaimsPresent reports whether guest_polling_claims exists; an
// older ingestor never creates it, so it is re-probed until it does.
func (db *DB) guestPollingClaimsPresent() bool {
	return db.columnPresent(&db.guestPollingClaims,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'guest_polling_claims'`)
}

// pollClaim is one eligible claimant of a node.
type pollClaim struct {
	node, observer string
	hops           int
	firstSeen      string
	hasReading     bool
}

// pollOwnerLess ranks two claimants of one node: the earlier is the owner.
func pollOwnerLess(a, b pollClaim) bool {
	if a.hasReading != b.hasReading {
		return a.hasReading
	}
	if a.hops != b.hops {
		return a.hops < b.hops
	}
	if a.firstSeen != b.firstSeen {
		return a.firstSeen < b.firstSeen
	}
	return a.observer < b.observer
}

// GetPollingOwners returns, for every node the observer claimed within the
// claim window, the owning observer. One query: the claims on those nodes,
// each with whether it has a recent reading.
func (db *DB) GetPollingOwners(observer string, now time.Time) (map[string]string, error) {
	out := map[string]string{}
	if !db.guestPollingClaimsPresent() {
		return out, nil
	}
	claimCut := now.UTC().Add(-pollOwnerClaimWindow).Format(guestKeyTimeLayout)
	readCut := now.UTC().Add(-pollOwnerReadingWindow).Format(guestKeyTimeLayout)
	hasReading := "0"
	args := []interface{}{}
	if db.neighborBatteryPresent() {
		hasReading = `EXISTS (SELECT 1 FROM neighbor_battery b
			WHERE b.node_id = c.node_id AND b.observer_id = c.observer_id AND b.sampled_at >= ?)`
		args = append(args, readCut)
	}
	args = append(args, claimCut, observer, claimCut)
	rows, err := db.conn.Query(`
		SELECT c.node_id, c.observer_id, c.reply_hops, c.first_seen, `+hasReading+`
		FROM guest_polling_claims c
		WHERE c.last_seen >= ?
		  AND c.node_id IN (SELECT node_id FROM guest_polling_claims WHERE observer_id = ? AND last_seen >= ?)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	best := map[string]pollClaim{}
	for rows.Next() {
		var c pollClaim
		var has int
		if err := rows.Scan(&c.node, &c.observer, &c.hops, &c.firstSeen, &has); err != nil {
			return nil, err
		}
		c.hasReading = has != 0
		if cur, ok := best[c.node]; !ok || pollOwnerLess(c, cur) {
			best[c.node] = c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for node, c := range best {
		out[node] = c.observer
	}
	return out, nil
}

// handleGuestPollingOwners serves GET /api/guest-polling/owners?observer=<64-hex>.
func (s *Server) handleGuestPollingOwners(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	obs := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("observer")))
	if !hex64Re.MatchString(obs) {
		writeError(w, http.StatusBadRequest, "observer must be 64 hex")
		return
	}
	owners, err := s.db.GetPollingOwners(obs, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := map[string]string{}
	for _, o := range owners {
		names[o] = ""
	}
	s.db.fillObserverNames(names)
	nodes := make(map[string]interface{}, len(owners))
	for n, o := range owners {
		nodes[n] = map[string]interface{}{"owner": o, "owner_name": names[o], "mine": o == obs}
	}
	writeJSON(w, map[string]interface{}{"observer": obs, "nodes": nodes})
}
