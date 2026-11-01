package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/meshcore-analyzer/pollpolicy"
)

// Polling ownership: one poller per repeater. Each observer publishes the
// repeaters it polls (meshcore/<region>/<observer>/polling), which the
// ingestor keeps in guest_polling_claims. /api/guest-polling/owners tells an
// observer, for each repeater it claims, who polls it and what it should do
// itself — the community polling election in polling_election.go, under the
// policy /api/guest-polling/policy serves.
//
// Old plugins only read owner and mine, so whenever this observer must not
// poll at all (it is blocked, its plugin is below the minimum, polling is
// paused, or the node is excluded) the owner is a sentinel all-zero key
// with mine false: an old plugin stands down without knowing why.

// guestPollingClaimsPresent reports whether guest_polling_claims exists; an
// older ingestor never creates it, so it is re-probed until it does.
func (db *DB) guestPollingClaimsPresent() bool {
	return db.columnPresent(&db.guestPollingClaims,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'guest_polling_claims'`)
}

// policyJSON is the /policy body. versionOK is reported as given;
// observer_blocked only when an observer was named.
func policyJSON(pol *pollpolicy.Policy, observer string, versionOK bool) map[string]interface{} {
	windows := make(map[string]int, len(pol.NodeMinutes))
	for k, m := range pol.NodeMinutes {
		windows[k] = m * 60
	}
	out := map[string]interface{}{
		"version":            pol.Version,
		"direct_s":           pol.DirectMinutes * 60,
		"one_hop_s":          pol.OneHopMinutes * 60,
		"grace_s":            pol.StandbyGraceMinutes * 60,
		"max_requests":       pol.MaxRequestsPerWindow,
		"takeover_after":     pol.TakeoverAfter,
		"paused":             pol.Paused,
		"paused_reason":      pol.PausedReason,
		"paused_until":       pol.PausedUntil,
		"excluded_nodes":     pol.ExcludedNodes,
		"node_windows":       windows,
		"min_plugin_version": pol.MinPluginVersion,
		"min_version_reason": pol.MinVersionReason,
		"version_ok":         versionOK,
	}
	if observer != "" {
		out["observer_blocked"] = pol.IsBlocked(observer)
	}
	return out
}

// pollingQuery reads the observer (required or optional) and plugin_version
// parameters; ok is false when a 400 was written.
func pollingQuery(w http.ResponseWriter, r *http.Request, required bool) (obs, pluginVersion string, ok bool) {
	q := r.URL.Query()
	obs = strings.ToLower(strings.TrimSpace(q.Get("observer")))
	if obs == "" && !required {
		return "", strings.TrimSpace(q.Get("plugin_version")), true
	}
	if !hex64Re.MatchString(obs) {
		writeError(w, http.StatusBadRequest, "observer must be 64 hex")
		return "", "", false
	}
	return obs, strings.TrimSpace(q.Get("plugin_version")), true
}

// handleGuestPollingPolicy serves GET /api/guest-polling/policy.
func (s *Server) handleGuestPollingPolicy(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	obs, pv, ok := pollingQuery(w, r, false)
	if !ok {
		return
	}
	pol := s.pollingPolicy(time.Now())
	versionOK := true
	if pv != "" {
		versionOK = pol.VersionOK(pv)
	}
	writeJSON(w, policyJSON(pol, obs, versionOK))
}

// ownerEntry is one node of the /owners answer.
type ownerEntry struct {
	Owner      string      `json:"owner"`
	OwnerName  string      `json:"owner_name"`
	Mine       bool        `json:"mine"`
	OwnerHops  *int        `json:"owner_hops"`
	Role       string      `json:"role"`
	LatestTs   interface{} `json:"latest_ts"`
	WindowS    int         `json:"window_s"`
	NextDue    interface{} `json:"next_due"`
	StandbyDue interface{} `json:"standby_due"`
	Excluded   bool        `json:"excluded"`
}

// GetPollingOwners runs the election for every node the observer claimed
// within the last hour and says, for each, who polls it and the observer's
// own role. pluginVersion is the asking plugin's version ("" when it did not
// say; then its claims' version is used).
func (db *DB) GetPollingOwners(observer, pluginVersion string, pol *pollpolicy.Policy, now time.Time) (map[string]*ownerEntry, bool, error) {
	out := map[string]*ownerEntry{}
	now = now.UTC()
	fresh := now.Add(-pollClaimFresh)
	claims, err := db.loadPollClaims(
		`c.node_id IN (SELECT node_id FROM guest_polling_claims WHERE observer_id = ? AND last_seen >= ?)`,
		[]interface{}{observer, fresh.Format(guestKeyTimeLayout)}, fresh, now)
	if err != nil {
		return nil, true, err
	}
	// The asking plugin's version: as it says now, else as its claims said.
	version := pluginVersion
	for _, rows := range claims {
		for i := range rows {
			if rows[i].Observer != observer {
				continue
			}
			if pluginVersion != "" {
				rows[i].PluginVersion = pollpolicy.PluginVersionString(pluginVersion)
			} else if version == "" {
				version = rows[i].PluginVersion
			}
		}
	}
	versionOK := pol.VersionOK(version)
	disabled := pol.IsBlocked(observer) || !versionOK

	nodes := make([]string, 0, len(claims))
	for n := range claims {
		nodes = append(nodes, n)
	}
	latest := db.latestNodeReadings(nodes)
	grace := pol.Grace()
	for node, rows := range claims {
		el := electNode(node, rows, pol, now)
		var mine *electedClaim
		for i := range el.Claims {
			if el.Claims[i].Observer == observer {
				mine = &el.Claims[i]
			}
		}
		if mine == nil {
			continue
		}
		e := &ownerEntry{Excluded: pol.IsExcluded(node), LatestTs: isoOrNil(latest[node])}
		window := mine.Window
		switch {
		case disabled:
			e.Owner, e.OwnerName, e.Role = pollpolicy.SentinelOwner, "nobody (this observer is disabled)", roleDisabled
		case pol.Paused:
			e.Owner, e.OwnerName, e.Role = pollpolicy.SentinelOwner, "nobody (paused)", rolePaused
		case e.Excluded:
			e.Owner, e.OwnerName, e.Role = pollpolicy.SentinelOwner, "nobody (excluded)", roleExcluded
		case !mine.Community || el.Owner == nil:
			hops := mine.EffHops
			e.Owner, e.Mine, e.Role, e.OwnerHops = observer, true, roleIndependent, &hops
		default:
			hops := el.Owner.EffHops
			e.Owner, e.Mine, e.OwnerHops = el.Owner.Observer, el.Owner.Observer == observer, &hops
			e.Role = roleStandby
			if e.Mine {
				e.Role = rolePolling
			}
			window = el.Window
		}
		e.WindowS = int(window / time.Second)
		if at := latest[node]; !at.IsZero() {
			e.NextDue = isoOrNil(at.Add(window))
			e.StandbyDue = isoOrNil(at.Add(window + grace))
		}
		out[node] = e
	}
	return out, versionOK, nil
}

// handleGuestPollingOwners serves
// GET /api/guest-polling/owners?observer=<64-hex>[&plugin_version=x.y.z].
func (s *Server) handleGuestPollingOwners(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.NeighborBatteryEnabled() {
		http.NotFound(w, r)
		return
	}
	obs, pv, ok := pollingQuery(w, r, true)
	if !ok {
		return
	}
	now := time.Now()
	pol := s.pollingPolicy(now)
	owners, versionOK, err := s.db.GetPollingOwners(obs, pv, pol, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := map[string]string{}
	for _, e := range owners {
		if e.Owner != pollpolicy.SentinelOwner {
			names[e.Owner] = ""
		}
	}
	s.db.fillObserverNames(names)
	for _, e := range owners {
		if e.Owner != pollpolicy.SentinelOwner {
			e.OwnerName = names[e.Owner]
		}
	}
	writeJSON(w, map[string]interface{}{"observer": obs, "nodes": owners, "policy": policyJSON(pol, obs, versionOK)})
}
