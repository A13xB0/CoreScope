package main

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/pollpolicy"
)

// Community polling (internal/pollpolicy): the neighborBattery.polling block
// decides which observers may poll which repeaters. The ingestor stores every
// claim as sent (the server decides eligibility at query time) and applies
// the policy to what it stores:
//
//   - a /battery report from a blocked observer, or from a plugin below
//     minPluginVersion, is dropped; a reading for an excluded node is dropped;
//   - a shared-key reply decoded from overheard packets is kept, but credited
//     to nobody (the shared key itself) when its requester is blocked or
//     below the minimum.
//
// The policy is re-read while running (pollpolicy.Watcher), so pollingPolicy
// is read through the Holder each time, never cached.

// pollingPolicy is the policy in force; set in main, nil-safe (defaults).
var pollingPolicy *pollpolicy.Holder

// currentPollingPolicy is the effective policy at now.
func currentPollingPolicy(now time.Time) *pollpolicy.Policy {
	return pollingPolicy.Policy(now)
}

// Claim keys: how the poller logs in to the repeater.
const (
	claimKeyShared   = "shared"
	claimKeyOwn      = "own"
	claimKeyPassword = "password"
)

// claimViaRe accepts a relay as a full key or the hex prefix the plugin has.
var claimViaRe = regexp.MustCompile(`^[0-9a-f]{2,64}$`)

// claimHeader is the message-level part of a /polling claim.
type claimHeader struct {
	version       int    // 1 or 2
	pluginVersion string // "x.y.z" or ""
	policyVersion string
	community     bool
}

// pollingClaim is one node in a /polling claim: the route the poller
// reaches it by, and (v2) the key it logs in with and the SNR it hears it at.
type pollingClaim struct {
	node string
	hops int
	via  string
	key  string
	snr  *float64

	pluginVersion string
	policyVersion string
	community     bool
}

var policyVersionRe = regexp.MustCompile(`^[0-9a-f]{1,16}$`)

// parsePollingClaims reads a /polling message:
//
//	{"v":2, "origin_id":"…", "plugin":"openhop.corescope_observer_plus/0.8.0",
//	 "policy_version":"a1b2c3d4", "community":true,
//	 "nodes":[{"node_id":"<64-hex>", "reply_hops":0, "via":null, "key":"shared", "snr":6.5}]}
//
// A v1 claim (no v/key/snr/community) reads as key "shared", community true.
// ok is false when the message is not usable at all (bad observer, or an
// origin_id that disagrees with the topic). Bad node entries are skipped.
func parsePollingClaims(observerID string, msg map[string]interface{}) (string, claimHeader, []pollingClaim, bool) {
	observerID = strings.ToLower(strings.TrimSpace(observerID))
	if !targetPubkeyRe.MatchString(observerID) {
		return "", claimHeader{}, nil, false
	}
	// The topic names the publisher; a payload that disagrees is spoofing, so
	// ignore it (same check as the /battery path).
	if o, ok := msg["origin_id"].(string); ok && o != "" && strings.ToLower(o) != observerID {
		return "", claimHeader{}, nil, false
	}
	h := claimHeader{version: 1, community: true}
	if v, ok := toFloat64(msg["v"]); ok && v == 2 {
		h.version = 2
	}
	if p, ok := msg["plugin"].(string); ok {
		h.pluginVersion = pollpolicy.PluginVersionString(p)
	}
	if pv, ok := msg["policy_version"].(string); ok {
		pv = strings.ToLower(strings.TrimSpace(pv))
		if policyVersionRe.MatchString(pv) {
			h.policyVersion = pv
		}
	}
	if c, ok := msg["community"].(bool); ok {
		h.community = c
	}
	nodes, _ := msg["nodes"].([]interface{})
	var out []pollingClaim
	seen := map[string]bool{}
	for _, n := range nodes {
		entry, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		node, _ := entry["node_id"].(string)
		node = strings.ToLower(strings.TrimSpace(node))
		if !targetPubkeyRe.MatchString(node) || node == observerID || seen[node] {
			continue
		}
		seen[node] = true
		c := pollingClaim{node: node, key: claimKeyShared,
			pluginVersion: h.pluginVersion, policyVersion: h.policyVersion, community: h.community}
		if hops, ok := toFloat64(entry["reply_hops"]); ok && hops >= 0 && hops <= 64 && hops == float64(int(hops)) {
			c.hops = int(hops)
		}
		if v, ok := entry["via"].(string); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			if claimViaRe.MatchString(v) && v != node {
				c.via = v
			}
		}
		if k, ok := entry["key"].(string); ok {
			switch k = strings.ToLower(strings.TrimSpace(k)); k {
			case claimKeyShared, claimKeyOwn, claimKeyPassword:
				c.key = k
			}
		}
		if s, ok := toFloat64(entry["snr"]); ok && s >= -64 && s <= 64 {
			c.snr = &s
		}
		out = append(out, c)
	}
	return observerID, h, out, true
}

// storePollingClaims is the /polling path when the shared-key decoder is not
// running: the claims are still stored for the server's election.
func storePollingClaims(store *Store, observerID string, msg map[string]interface{}, now time.Time) {
	obs, _, claims, ok := parsePollingClaims(observerID, msg)
	if !ok || len(claims) == 0 {
		return
	}
	if err := store.UpsertPollingClaims(obs, claims, now); err != nil {
		log.Printf("[polling] store claims from %.8s: %v", obs, err)
	}
}

// migrateGuestPollingClaimsV2 adds the claim v2 columns to a
// guest_polling_claims table created before them. Idempotent.
func migrateGuestPollingClaimsV2(db *sql.DB) error {
	for _, col := range []struct{ name, typ string }{
		{"key_mode", "TEXT"},
		{"snr", "REAL"},
		{"plugin_version", "TEXT"},
		{"policy_version", "TEXT"},
		{"community", "INTEGER NOT NULL DEFAULT 1"},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('guest_polling_claims') WHERE name = ?`, col.name).Scan(&n); err != nil {
			return fmt.Errorf("guest_polling_claims columns: %w", err)
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE guest_polling_claims ADD COLUMN ` + col.name + ` ` + col.typ); err != nil {
			return fmt.Errorf("adding guest_polling_claims.%s: %w", col.name, err)
		}
		log.Printf("[migration] guest_polling_claims.%s added", col.name)
	}
	return nil
}

// dropLog logs a dropped report at most once per observer and reason per
// hour, so a misbehaving station doesn't flood the log.
type dropLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

var pollingDrops = &dropLog{last: map[string]time.Time{}}

const dropLogEvery = time.Hour

func (d *dropLog) logf(observer, reason string, now time.Time, format string, args ...interface{}) {
	key := observer + "|" + reason
	d.mu.Lock()
	if t, ok := d.last[key]; ok && now.Sub(t) < dropLogEvery {
		d.mu.Unlock()
		return
	}
	if len(d.last) > 4096 { // bound the map; old entries only rate-limit logging
		for k, t := range d.last {
			if now.Sub(t) >= dropLogEvery {
				delete(d.last, k)
			}
		}
	}
	d.last[key] = now
	d.mu.Unlock()
	log.Printf(format, args...)
}

// batteryReportDrop says why a whole /battery report must be dropped under
// the policy ("" to keep it): a blocked observer, or a plugin below the
// minimum version (from the payload's plugin field).
func batteryReportDrop(pol *pollpolicy.Policy, observerID string, msg map[string]interface{}) string {
	if pol.IsBlocked(observerID) {
		return "blocked"
	}
	if pol.HasMinVersion() {
		plugin, _ := msg["plugin"].(string)
		if !pol.VersionOK(plugin) {
			return "version"
		}
	}
	return ""
}
