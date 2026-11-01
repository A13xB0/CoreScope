package main

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/pollpolicy"
)

// Community polling election. Observers claim the repeaters they poll
// (meshcore/<region>/<observer>/polling → guest_polling_claims); at query
// time the server decides, per repeater, which claimants are eligible under
// the policy (neighborBattery.polling, internal/pollpolicy) and ranks them.
// The first eligible claimant polls; the others stand by.
//
// Eligible: observer not blocked, plugin at or above the minimum version,
// node not excluded, community on, claim renewed within the last hour, and
// polling not paused.
//
// Effective hops: a claimed direct route (0) counts as direct only when it
// is confirmed — the neighbor graph has an edge between observer and node
// seen in the last 24 h, or the observer has a reading credited for the node
// within 2 × directMinutes. Otherwise it ranks as 1 hop.
//
// Window: nodeMinutes[node], else directMinutes (effective hops 0), else
// oneHopMinutes. Failing: first claimed more than takeover × window ago and
// no credited reading within takeover × window. Proven: a credited reading
// within 2 × window.
//
// Ranking: not failing first, then effective hops, then proven, then SNR
// (higher first, none last), then the earliest first claim, then observer id.

const (
	pollClaimFresh      = time.Hour      // a claim renewed this recently can be elected
	pollClaimShown      = 24 * time.Hour // older claims are pruned; 1–24 h show as stale
	pollDirectEdgeFresh = 24 * time.Hour // a neighbor edge this recent confirms a direct claim
	pollReadingsWindow  = 7 * 24 * time.Hour
)

// Roles a claimant can have for one node.
const (
	rolePolling         = "polling"
	roleStandby         = "standby"
	roleIndependent     = "independent"
	roleExcluded        = "excluded"
	rolePaused          = "paused"
	roleVersionDisabled = "version_disabled"
	roleBlocked         = "blocked"
	roleStale           = "stale"
	roleDisabled        = "disabled" // /owners: the asking observer is blocked or below the minimum
)

// pollClaimRow is one stored claim with what the election needs about it.
type pollClaimRow struct {
	Node, Observer string
	Hops           int
	Via            string
	Key            string
	SNR            *float64
	PluginVersion  string
	Community      bool
	FirstSeen      time.Time
	LastSeen       time.Time
	DirectEdge     bool      // neighbor_edges has observer↔node in the last 24 h
	LastReading    time.Time // newest reading credited to this observer for the node (7 d); zero if none
	Readings7d     int
}

// electedClaim is a claim after the election.
type electedClaim struct {
	// StandbyRank is this standby\'s place in the queue, from 1. Each waits one
	// more grace period, so a silent poller is replaced by one standby and not
	// by all of them at once.
	StandbyRank int
	pollClaimRow
	EffHops         int
	DirectConfirmed bool
	Window          time.Duration
	Failing, Proven bool
	Eligible        bool
	Role            string
}

// nodeElection is the outcome for one node: claims in ranking order
// (eligible first), the owner (nil when no one is eligible) and the window.
type nodeElection struct {
	Node   string
	Claims []electedClaim
	Owner  *electedClaim
	Window time.Duration
}

func readingWithin(last time.Time, d time.Duration, now time.Time) bool {
	return !last.IsZero() && now.Sub(last) <= d
}

// ineligibleRole is the reason a claim cannot be elected, "" when it can.
// Reasons about the observer come first (they apply to every node it claims),
// then the claim's own age, then the node, then the network-wide pause.
func ineligibleRole(c pollClaimRow, pol *pollpolicy.Policy, now time.Time) string {
	switch {
	case pol.IsBlocked(c.Observer):
		return roleBlocked
	case !pol.VersionOK(c.PluginVersion):
		return roleVersionDisabled
	case now.Sub(c.LastSeen) > pollClaimFresh:
		return roleStale
	case pol.IsExcluded(c.Node):
		return roleExcluded
	case pol.Paused:
		return rolePaused
	case !c.Community:
		return roleIndependent
	}
	return ""
}

var ineligibleOrder = map[string]int{
	roleIndependent: 0, rolePaused: 1, roleExcluded: 2, roleStale: 3, roleVersionDisabled: 4, roleBlocked: 5,
}

// electedLess is the ranking among eligible claimants of one node.
func electedLess(a, b electedClaim) bool {
	if a.Failing != b.Failing {
		return !a.Failing
	}
	if a.EffHops != b.EffHops {
		return a.EffHops < b.EffHops
	}
	if a.Proven != b.Proven {
		return a.Proven
	}
	if (a.SNR == nil) != (b.SNR == nil) {
		return a.SNR != nil
	}
	if a.SNR != nil && *a.SNR != *b.SNR {
		return *a.SNR > *b.SNR
	}
	if !a.FirstSeen.Equal(b.FirstSeen) {
		return a.FirstSeen.Before(b.FirstSeen)
	}
	return a.Observer < b.Observer
}

// evaluateClaim works out a claim's effective hops, window, failing and
// proven, and whether it is eligible.
func evaluateClaim(c pollClaimRow, pol *pollpolicy.Policy, now time.Time) electedClaim {
	e := electedClaim{pollClaimRow: c, EffHops: c.Hops}
	if c.Hops == 0 {
		e.DirectConfirmed = c.DirectEdge ||
			readingWithin(c.LastReading, 2*time.Duration(pol.DirectMinutes)*time.Minute, now)
		if !e.DirectConfirmed {
			e.EffHops = 1
		}
	}
	e.Window = pol.Window(c.Node, e.EffHops)
	takeover := time.Duration(pol.TakeoverAfter) * e.Window
	e.Failing = now.Sub(c.FirstSeen) > takeover && !readingWithin(c.LastReading, takeover, now)
	e.Proven = readingWithin(c.LastReading, 2*e.Window, now)
	e.Role = ineligibleRole(c, pol, now)
	e.Eligible = e.Role == ""
	return e
}

// electNode runs the election over one node's claims.
func electNode(node string, claims []pollClaimRow, pol *pollpolicy.Policy, now time.Time) nodeElection {
	out := nodeElection{Node: node}
	for _, c := range claims {
		out.Claims = append(out.Claims, evaluateClaim(c, pol, now))
	}
	sort.SliceStable(out.Claims, func(i, j int) bool {
		a, b := out.Claims[i], out.Claims[j]
		if a.Eligible != b.Eligible {
			return a.Eligible
		}
		if a.Eligible {
			return electedLess(a, b)
		}
		if ineligibleOrder[a.Role] != ineligibleOrder[b.Role] {
			return ineligibleOrder[a.Role] < ineligibleOrder[b.Role]
		}
		if a.EffHops != b.EffHops {
			return a.EffHops < b.EffHops
		}
		return a.Observer < b.Observer
	})
	for i := range out.Claims {
		if !out.Claims[i].Eligible {
			break
		}
		if i == 0 {
			out.Claims[i].Role = rolePolling
			out.Owner = &out.Claims[i]
		} else {
			out.Claims[i].Role = roleStandby
			out.Claims[i].StandbyRank = i
		}
	}
	if out.Owner != nil {
		out.Window = out.Owner.Window
	} else {
		// No poller: the window the best-placed claimant would have.
		best := 1
		if len(claims) == 0 {
			best = 0
		}
		for _, c := range out.Claims {
			if c.EffHops < best {
				best = c.EffHops
			}
		}
		out.Window = pol.Window(node, best)
	}
	return out
}

// pollClaimsV2Present reports whether guest_polling_claims has the claim v2
// columns (an older ingestor never adds them).
func (db *DB) pollClaimsV2Present() bool {
	return db.columnPresent(&db.guestPollingClaimsV2,
		`SELECT COUNT(*) FROM pragma_table_info('guest_polling_claims') WHERE name = 'community'`)
}

func (db *DB) neighborEdgesPresent() bool {
	return db.columnPresent(&db.neighborEdgesTable,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'neighbor_edges'`)
}

// loadPollClaims reads the claims renewed since `since` that match where
// (an SQL condition on c, with its args), with each claim's neighbor edge
// and credited readings. Grouped by node.
func (db *DB) loadPollClaims(where string, args []interface{}, since, now time.Time) (map[string][]pollClaimRow, error) {
	out := map[string][]pollClaimRow{}
	if !db.guestPollingClaimsPresent() {
		return out, nil
	}
	cols := `'shared', NULL, '', 1`
	if db.pollClaimsV2Present() {
		cols = `COALESCE(c.key_mode, 'shared'), c.snr, COALESCE(c.plugin_version, ''), c.community`
	}
	var qargs []interface{}
	edge := `0`
	if db.neighborEdgesPresent() {
		edge = `EXISTS (SELECT 1 FROM neighbor_edges e
			WHERE e.node_a = MIN(c.observer_id, c.node_id) AND e.node_b = MAX(c.observer_id, c.node_id) AND e.last_seen >= ?)`
		qargs = append(qargs, now.Add(-pollDirectEdgeFresh).UTC().Format(guestKeyTimeLayout))
	}
	readings := `'', 0`
	if db.neighborBatteryPresent() {
		cut := now.Add(-pollReadingsWindow).UTC().Format(guestKeyTimeLayout)
		readings = `(SELECT COALESCE(MAX(b.sampled_at), '') FROM neighbor_battery b
				WHERE b.node_id = c.node_id AND b.observer_id = c.observer_id AND b.sampled_at >= ?),
			(SELECT COUNT(*) FROM neighbor_battery b
				WHERE b.node_id = c.node_id AND b.observer_id = c.observer_id AND b.sampled_at >= ?)`
		qargs = append(qargs, cut, cut)
	}
	qargs = append(qargs, since.UTC().Format(guestKeyTimeLayout))
	qargs = append(qargs, args...)
	if where != "" {
		where = " AND (" + where + ")"
	}
	rows, err := db.conn.Query(`
		SELECT c.node_id, c.observer_id, c.reply_hops, COALESCE(c.via, ''), `+cols+`,
			c.first_seen, c.last_seen, `+edge+`, `+readings+`
		FROM guest_polling_claims c
		WHERE c.last_seen >= ?`+where, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r pollClaimRow
		var snr sql.NullFloat64
		var community, edgeHit int
		var first, last, lastReading string
		if err := rows.Scan(&r.Node, &r.Observer, &r.Hops, &r.Via, &r.Key, &snr, &r.PluginVersion, &community,
			&first, &last, &edgeHit, &lastReading, &r.Readings7d); err != nil {
			return nil, err
		}
		r.Node, r.Observer = strings.ToLower(r.Node), strings.ToLower(r.Observer)
		if snr.Valid {
			v := snr.Float64
			r.SNR = &v
		}
		r.Community = community != 0
		r.DirectEdge = edgeHit != 0
		r.FirstSeen, _ = time.Parse(time.RFC3339, first)
		r.LastSeen, _ = time.Parse(time.RFC3339, last)
		if lastReading != "" {
			r.LastReading, _ = time.Parse(time.RFC3339, lastReading)
		}
		out[r.Node] = append(out[r.Node], r)
	}
	return out, rows.Err()
}

// latestNodeReadings returns the newest reading per node from anyone.
func (db *DB) latestNodeReadings(nodes []string) map[string]time.Time {
	out := map[string]time.Time{}
	if len(nodes) == 0 || !db.neighborBatteryPresent() {
		return out
	}
	for start := 0; start < len(nodes); start += nodeListBatteryChunk {
		end := start + nodeListBatteryChunk
		if end > len(nodes) {
			end = len(nodes)
		}
		args := make([]interface{}, 0, end-start)
		for _, n := range nodes[start:end] {
			args = append(args, strings.ToLower(n))
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		rows, err := db.conn.Query(`SELECT node_id, MAX(sampled_at) FROM neighbor_battery
			WHERE node_id IN (`+marks+`) GROUP BY node_id`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, ts string
			if rows.Scan(&id, &ts) == nil {
				if at, err := time.Parse(time.RFC3339, ts); err == nil {
					out[strings.ToLower(id)] = at
				}
			}
		}
		rows.Close()
	}
	return out
}

// pollingPolicy is the policy in force: the hot-reloaded block when main
// installed a watcher, else the one in the config the server was built with.
func (s *Server) pollingPolicy(now time.Time) *pollpolicy.Policy {
	if s.pollPolicy != nil {
		return s.pollPolicy.Policy(now)
	}
	var c *pollpolicy.Config
	if s.cfg != nil && s.cfg.NeighborBattery != nil {
		c = s.cfg.NeighborBattery.Polling
	}
	return c.Effective(now)
}

// isoOrNil formats t as RFC3339, or nil for the zero time.
func isoOrNil(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(guestKeyTimeLayout)
}
