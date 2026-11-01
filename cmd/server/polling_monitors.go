package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/pollpolicy"
)

// Who monitors what, for the UI: the observers claiming a repeater
// (/api/nodes/{pk}/monitors, the node page's "Monitored by" card), the
// repeaters an observer claims (/api/observers/{id}/monitors), and a compact
// monitored {count, direct} on the /api/nodes rows. All of it is the same
// election as /api/guest-polling/owners (polling_election.go).

// resolveViaNames names each relay in vias: a full key by its node, a prefix
// (what a plugin may have) by the one node it matches, else "".
func (db *DB) resolveViaNames(vias map[string]string) {
	full := map[string]string{}
	for v := range vias {
		if len(v) == 64 {
			full[v] = ""
		}
	}
	db.fillNodeNames(full)
	for v := range vias {
		if len(v) == 64 {
			vias[v] = full[v]
			continue
		}
		rows, err := db.conn.Query(`SELECT COALESCE(name, '') FROM nodes WHERE public_key LIKE ? LIMIT 2`, strings.ToLower(v)+"%")
		if err != nil {
			continue
		}
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
		if len(names) == 1 {
			vias[v] = names[0]
		}
	}
}

func viaOrNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// handleNodeMonitors serves GET /api/nodes/{pubkey}/monitors.
func (s *Server) handleNodeMonitors(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	node := strings.ToLower(mux.Vars(r)["pubkey"])
	if !hex64Re.MatchString(node) {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex")
		return
	}
	now := time.Now().UTC()
	pol := s.pollingPolicy(now)
	claims, err := s.db.loadPollClaims(`c.node_id = ?`, []interface{}{node}, now.Add(-pollClaimShown), now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	el := electNode(node, claims[node], pol, now)
	latest := s.db.latestNodeReadings([]string{node})[node]
	names, vias := map[string]string{}, map[string]string{}
	for _, c := range el.Claims {
		names[c.Observer] = ""
		if c.Via != "" {
			vias[c.Via] = ""
		}
	}
	s.db.fillObserverNames(names)
	s.db.resolveViaNames(vias)
	monitors := make([]map[string]interface{}, 0, len(el.Claims))
	for _, c := range el.Claims {
		var nextDue, standbyDue interface{}
		if c.Eligible && !latest.IsZero() {
			nextDue = isoOrNil(latest.Add(el.Window))
			if c.Role == roleStandby {
				standbyDue = isoOrNil(latest.Add(el.Window + standbyWait(pol.Grace(), c.StandbyRank)))
			}
		}
		monitors = append(monitors, map[string]interface{}{
			"observer_id":      c.Observer,
			"name":             names[c.Observer],
			"hops":             c.Hops,
			"effective_hops":   c.EffHops,
			"direct_confirmed": c.Hops == 0 && c.DirectConfirmed,
			"via":              viaOrNil(c.Via),
			"via_name":         vias[c.Via],
			"key":              c.Key,
			"snr":              c.SNR,
			"plugin_version":   c.PluginVersion,
			"community":        c.Community,
			"role":             c.Role,
			"first_seen":       isoOrNil(c.FirstSeen),
			"last_claim":       isoOrNil(c.LastSeen),
			"last_reading":     isoOrNil(c.LastReading),
			"readings_7d":      c.Readings7d,
			"next_due":         nextDue,
			"standby_due":      standbyDue,
			"standby_rank":     c.StandbyRank,
		})
	}
	writeJSON(w, map[string]interface{}{
		"node_id":            node,
		"window_s":           int(el.Window / time.Second),
		"grace_s":            pol.StandbyGraceMinutes * 60,
		"latest_ts":          isoOrNil(latest),
		"paused":             pol.Paused,
		"paused_reason":      pol.PausedReason,
		"paused_until":       pol.PausedUntil,
		"excluded":           pol.IsExcluded(node),
		"policy_version":     pol.Version,
		"min_plugin_version": pol.MinPluginVersion,
		"monitors":           monitors,
	})
}

var observerRoleOrder = map[string]int{
	rolePolling: 0, roleStandby: 1, roleIndependent: 2, rolePaused: 3, roleExcluded: 4,
	roleStale: 5, roleVersionDisabled: 6, roleBlocked: 7,
}

// handleObserverMonitors serves GET /api/observers/{id}/monitors.
func (s *Server) handleObserverMonitors(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	obs := strings.ToLower(mux.Vars(r)["id"])
	if !hex64Re.MatchString(obs) {
		writeError(w, http.StatusBadRequest, "observer id must be 64 hex")
		return
	}
	now := time.Now().UTC()
	pol := s.pollingPolicy(now)
	shown := now.Add(-pollClaimShown)
	claims, err := s.db.loadPollClaims(
		`c.node_id IN (SELECT node_id FROM guest_polling_claims WHERE observer_id = ? AND last_seen >= ?)`,
		[]interface{}{obs, shown.Format(guestKeyTimeLayout)}, shown, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entry struct {
		c    electedClaim
		name string
	}
	var mine []entry
	var newest time.Time
	pluginVersion := ""
	nodeNames, vias := map[string]string{}, map[string]string{}
	for node, rows := range claims {
		el := electNode(node, rows, pol, now)
		for _, c := range el.Claims {
			if c.Observer != obs {
				continue
			}
			mine = append(mine, entry{c: c})
			nodeNames[node] = ""
			if c.Via != "" {
				vias[c.Via] = ""
			}
			if c.LastSeen.After(newest) {
				newest, pluginVersion = c.LastSeen, c.PluginVersion
			}
		}
	}
	s.db.fillNodeNames(nodeNames)
	s.db.resolveViaNames(vias)
	sort.Slice(mine, func(i, j int) bool {
		a, b := mine[i].c, mine[j].c
		if observerRoleOrder[a.Role] != observerRoleOrder[b.Role] {
			return observerRoleOrder[a.Role] < observerRoleOrder[b.Role]
		}
		na, nb := strings.ToLower(nodeNames[a.Node]), strings.ToLower(nodeNames[b.Node])
		if na != nb {
			return na < nb
		}
		return a.Node < b.Node
	})
	nodes := make([]map[string]interface{}, 0, len(mine))
	for _, e := range mine {
		c := e.c
		nodes = append(nodes, map[string]interface{}{
			"node_id":        c.Node,
			"name":           nodeNames[c.Node],
			"hops":           c.Hops,
			"effective_hops": c.EffHops,
			"via":            viaOrNil(c.Via),
			"via_name":       vias[c.Via],
			"key":            c.Key,
			"role":           c.Role,
			"last_reading":   isoOrNil(c.LastReading),
			"readings_7d":    c.Readings7d,
		})
	}
	names := map[string]string{obs: ""}
	s.db.fillObserverNames(names)
	writeJSON(w, map[string]interface{}{
		"observer_id":        obs,
		"name":               names[obs],
		"blocked":            pol.IsBlocked(obs),
		"plugin_version":     pluginVersion,
		"version_ok":         pol.VersionOK(pluginVersion),
		"min_plugin_version": pol.MinPluginVersion,
		"min_version_reason": pol.MinVersionReason,
		"policy_version":     pol.Version,
		"paused":             pol.Paused,
		"nodes":              nodes,
	})
}

// nodeMonitoring is a node's monitored summary for the nodes list and map.
type nodeMonitoring struct {
	Count    int    `json:"count"`
	Direct   int    `json:"direct"`
	Excluded bool   `json:"excluded,omitempty"`
	Paused   bool   `json:"paused,omitempty"`
	Poller   string `json:"poller,omitempty"`
	Name     string `json:"poller_name,omitempty"`
	WindowS  int    `json:"window_s,omitempty"`
}

// GetNodesMonitoring returns, for every node with a fresh claim, the number
// of eligible claimants (and how many are effectively direct) and its poller.
func (db *DB) GetNodesMonitoring(pol *pollpolicy.Policy, now time.Time) map[string]*nodeMonitoring {
	out := map[string]*nodeMonitoring{}
	claims, err := db.loadPollClaims("", nil, now.Add(-pollClaimFresh), now)
	if err != nil {
		return out
	}
	for node, rows := range claims {
		el := electNode(node, rows, pol, now)
		m := &nodeMonitoring{Excluded: pol.IsExcluded(node), Paused: pol.Paused}
		for _, c := range el.Claims {
			if !c.Eligible {
				continue
			}
			m.Count++
			if c.EffHops == 0 {
				m.Direct++
			}
		}
		if el.Owner != nil {
			m.Poller, m.WindowS = el.Owner.Observer, int(el.Window/time.Second)
		}
		out[node] = m
	}
	return out
}

// enrichNodesMonitored sets node["monitored"] on the rows some observer
// claims (fresh), and on excluded rows. No-op unless neighborBattery is on.
func (s *Server) enrichNodesMonitored(nodes []map[string]interface{}) {
	if s.db == nil || !s.cfg.NeighborBatteryEnabled() || len(nodes) == 0 {
		return
	}
	now := time.Now().UTC()
	pol := s.pollingPolicy(now)
	mon := s.db.GetNodesMonitoring(pol, now)
	if len(mon) == 0 && len(pol.ExcludedNodes) == 0 {
		return
	}
	names := map[string]string{}
	for _, n := range nodes {
		pk, _ := n["public_key"].(string)
		if m, ok := mon[strings.ToLower(pk)]; ok && m.Poller != "" {
			names[m.Poller] = ""
		}
	}
	s.db.fillObserverNames(names)
	for _, n := range nodes {
		pk, _ := n["public_key"].(string)
		pk = strings.ToLower(pk)
		m, ok := mon[pk]
		if !ok {
			if !pol.IsExcluded(pk) {
				continue
			}
			m = &nodeMonitoring{Excluded: true, Paused: pol.Paused}
		}
		m.Name = names[m.Poller]
		n["monitored"] = m
	}
}

// pollingSummary is the network-wide picture for the public policy panel.
type pollingSummary struct {
	MonitoredNodes     int `json:"monitored_nodes"`      // nodes with an elected poller
	DirectNodes        int `json:"direct_nodes"`         // … whose poller is effectively direct
	ExcludedNodes      int `json:"excluded_nodes"`       // excludedNodes entries
	Observers          int `json:"observers"`            // observers with an eligible claim
	ObserversBelowMin  int `json:"observers_below_min"`  // observers with a fresh claim below minPluginVersion
	BlockedObservers   int `json:"blocked_observers"`    // blockedObservers entries
	BlockedObserversOn int `json:"blocked_observers_on"` // … still claiming (fresh)
}

// GetPollingSummary counts the fresh claims under the policy.
func (db *DB) GetPollingSummary(pol *pollpolicy.Policy, now time.Time) (pollingSummary, error) {
	out := pollingSummary{ExcludedNodes: len(pol.ExcludedNodes), BlockedObservers: len(pol.BlockedObservers)}
	claims, err := db.loadPollClaims("", nil, now.Add(-pollClaimFresh), now)
	if err != nil {
		return out, err
	}
	eligible, below, blocked := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for node, rows := range claims {
		el := electNode(node, rows, pol, now)
		if el.Owner != nil {
			out.MonitoredNodes++
			if el.Owner.EffHops == 0 {
				out.DirectNodes++
			}
		}
		for _, c := range el.Claims {
			switch {
			case c.Eligible:
				eligible[c.Observer] = true
			case c.Role == roleVersionDisabled:
				below[c.Observer] = true
			case c.Role == roleBlocked:
				blocked[c.Observer] = true
			}
		}
	}
	out.Observers, out.ObserversBelowMin, out.BlockedObserversOn = len(eligible), len(below), len(blocked)
	return out, nil
}

// handleGuestPollingSummary serves GET /api/guest-polling/summary: the policy
// (as /api/guest-polling/policy) and how many repeaters and observers take
// part, for the read-only policy panel on the analytics page.
func (s *Server) handleGuestPollingSummary(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	now := time.Now().UTC()
	pol := s.pollingPolicy(now)
	sum, err := s.db.GetPollingSummary(pol, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"policy": policyJSON(pol, "", true), "summary": sum})
}
