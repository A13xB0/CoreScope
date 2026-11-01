package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/pollpolicy"
)

const (
	cpObsC = "cc33dd00112233445566778899aabbccddeeff00112233445566778899007788"
	cpObsD = "dd44ee00112233445566778899aabbccddeeff00112233445566778899009911"
)

var cpNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func cpSNR(v float64) *float64 { return &v }

// claim builds a fresh, eligible-by-default claim on nbNode first seen 30 min
// ago (well inside any takeover).
func claim(obs string, hops int) pollClaimRow {
	return pollClaimRow{Node: nbNode, Observer: obs, Hops: hops, Key: "shared", Community: true,
		PluginVersion: "0.8.0", FirstSeen: cpNow.Add(-30 * time.Minute), LastSeen: cpNow.Add(-5 * time.Minute)}
}

func with(c pollClaimRow, f func(*pollClaimRow)) pollClaimRow { f(&c); return c }

func order(el nodeElection) []string {
	var out []string
	for _, c := range el.Claims {
		out = append(out, c.Observer[:4]+":"+c.Role)
	}
	return out
}

func TestElectNodeRanking(t *testing.T) {
	pol := (&pollpolicy.Config{}).Effective(cpNow)
	recent := cpNow.Add(-20 * time.Minute)
	cases := []struct {
		name   string
		claims []pollClaimRow
		want   []string
	}{
		{"confirmed direct beats 1 hop",
			[]pollClaimRow{claim(nbObsA, 1), with(claim(nbObsB, 0), func(c *pollClaimRow) { c.DirectEdge = true })},
			[]string{"3b9f:polling", "7e76:standby"}},
		{"unconfirmed direct ranks as 1 hop (earlier first claim wins)",
			[]pollClaimRow{with(claim(nbObsB, 0), func(c *pollClaimRow) { c.FirstSeen = cpNow.Add(-20 * time.Minute) }), claim(nbObsA, 1)},
			[]string{"7e76:polling", "3b9f:standby"}},
		{"direct confirmed by a recent reading",
			[]pollClaimRow{claim(nbObsA, 1), with(claim(nbObsB, 0), func(c *pollClaimRow) { c.LastReading = cpNow.Add(-110 * time.Minute) })},
			[]string{"3b9f:polling", "7e76:standby"}},
		{"a reading older than 2x direct does not confirm",
			[]pollClaimRow{with(claim(nbObsA, 1), func(c *pollClaimRow) { c.LastReading = recent; c.FirstSeen = cpNow.Add(-40 * time.Minute) }),
				with(claim(nbObsB, 0), func(c *pollClaimRow) { c.LastReading = cpNow.Add(-130 * time.Minute) })},
			[]string{"7e76:polling", "3b9f:standby"}}, // both 1 hop and proven: A's earlier first claim wins
		{"proven beats unproven at equal hops",
			[]pollClaimRow{claim(nbObsA, 1), with(claim(nbObsB, 1), func(c *pollClaimRow) { c.LastReading = recent; c.FirstSeen = cpNow })},
			[]string{"3b9f:polling", "7e76:standby"}},
		{"failing direct loses to a 1-hop standby (takeover)",
			[]pollClaimRow{
				with(claim(nbObsA, 0), func(c *pollClaimRow) {
					c.DirectEdge = true
					c.FirstSeen = cpNow.Add(-3 * time.Hour) // > 2 x 60 min
					c.LastReading = cpNow.Add(-121 * time.Minute)
				}),
				claim(nbObsB, 1)},
			[]string{"3b9f:polling", "7e76:standby"}},
		{"a direct poller inside its takeover keeps it",
			[]pollClaimRow{
				with(claim(nbObsA, 0), func(c *pollClaimRow) {
					c.DirectEdge = true
					c.FirstSeen = cpNow.Add(-3 * time.Hour)
					c.LastReading = cpNow.Add(-100 * time.Minute)
				}),
				claim(nbObsB, 1)},
			[]string{"7e76:polling", "3b9f:standby"}},
		{"snr descending, none last",
			[]pollClaimRow{claim(nbObsA, 1), with(claim(nbObsB, 1), func(c *pollClaimRow) { c.SNR = cpSNR(-3) }),
				with(claim(cpObsC, 1), func(c *pollClaimRow) { c.SNR = cpSNR(6.5) })},
			[]string{"cc33:polling", "3b9f:standby", "7e76:standby"}},
		{"first_seen then observer id",
			[]pollClaimRow{claim(cpObsC, 1), claim(nbObsA, 1), with(claim(nbObsB, 1), func(c *pollClaimRow) { c.FirstSeen = cpNow.Add(-time.Hour) })},
			[]string{"3b9f:polling", "7e76:standby", "cc33:standby"}},
		{"community off is independent, after the eligible",
			[]pollClaimRow{with(claim(nbObsA, 0), func(c *pollClaimRow) { c.Community = false; c.DirectEdge = true }), claim(nbObsB, 2)},
			[]string{"3b9f:polling", "7e76:independent"}},
		{"stale claim is not elected",
			[]pollClaimRow{with(claim(nbObsA, 0), func(c *pollClaimRow) { c.LastSeen = cpNow.Add(-61 * time.Minute); c.DirectEdge = true }), claim(nbObsB, 1)},
			[]string{"3b9f:polling", "7e76:stale"}},
		{"nobody eligible", []pollClaimRow{with(claim(nbObsA, 1), func(c *pollClaimRow) { c.Community = false })}, []string{"7e76:independent"}},
	}
	for _, tc := range cases {
		el := electNode(nbNode, tc.claims, pol, cpNow)
		if got := order(el); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestElectNodeWindowAndFlags(t *testing.T) {
	pol := (&pollpolicy.Config{NodeMinutes: map[string]int{nbNode2: 240}}).Effective(cpNow)
	el := electNode(nbNode, []pollClaimRow{with(claim(nbObsA, 0), func(c *pollClaimRow) { c.DirectEdge = true })}, pol, cpNow)
	if el.Window != time.Hour || el.Owner == nil || !el.Owner.DirectConfirmed || el.Owner.EffHops != 0 {
		t.Errorf("direct owner: window %v owner %+v", el.Window, el.Owner)
	}
	el = electNode(nbNode, []pollClaimRow{claim(nbObsA, 0)}, pol, cpNow)
	if el.Window != 2*time.Hour || el.Owner.DirectConfirmed || el.Owner.EffHops != 1 {
		t.Errorf("unconfirmed owner: window %v %+v", el.Window, el.Owner)
	}
	c := claim(nbObsA, 0)
	c.Node, c.DirectEdge = nbNode2, true
	if el = electNode(nbNode2, []pollClaimRow{c}, pol, cpNow); el.Window != 4*time.Hour {
		t.Errorf("node window override = %v", el.Window)
	}
	// No eligible claimant: the window the best-placed one would have.
	paused := (&pollpolicy.Config{Paused: true}).Effective(cpNow)
	el = electNode(nbNode, []pollClaimRow{with(claim(nbObsA, 0), func(c *pollClaimRow) { c.DirectEdge = true })}, paused, cpNow)
	if el.Owner != nil || el.Window != time.Hour {
		t.Errorf("paused: owner %+v window %v", el.Owner, el.Window)
	}
}

func TestEligibility(t *testing.T) {
	base := claim(nbObsA, 0)
	cases := []struct {
		name string
		cfg  pollpolicy.Config
		c    pollClaimRow
		want string
	}{
		{"eligible", pollpolicy.Config{}, base, ""},
		{"blocked", pollpolicy.Config{BlockedObservers: []string{strings.ToUpper(nbObsA)}}, base, roleBlocked},
		{"below minimum", pollpolicy.Config{MinPluginVersion: "0.8.2"}, base, roleVersionDisabled},
		{"no version under a minimum", pollpolicy.Config{MinPluginVersion: "0.1.0"}, with(base, func(c *pollClaimRow) { c.PluginVersion = "" }), roleVersionDisabled},
		{"at minimum", pollpolicy.Config{MinPluginVersion: "0.8.0"}, base, ""},
		{"excluded", pollpolicy.Config{ExcludedNodes: []string{nbNode}}, base, roleExcluded},
		{"community off", pollpolicy.Config{}, with(base, func(c *pollClaimRow) { c.Community = false }), roleIndependent},
		{"claim over an hour old", pollpolicy.Config{}, with(base, func(c *pollClaimRow) { c.LastSeen = cpNow.Add(-61 * time.Minute) }), roleStale},
		{"paused", pollpolicy.Config{Paused: true}, base, rolePaused},
		{"pause lapsed", pollpolicy.Config{Paused: true, PausedUntil: "2026-11-01T09:00:00Z"}, base, ""},
		{"blocked wins over paused", pollpolicy.Config{Paused: true, BlockedObservers: []string{nbObsA}}, base, roleBlocked},
	}
	for _, tc := range cases {
		if got := ineligibleRole(tc.c, tc.cfg.Effective(cpNow), cpNow); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ── HTTP ──────────────────────────────────────────────────────────────────

func createPollingClaimsV2Table(t *testing.T, db *DB) {
	t.Helper()
	createPollingClaimsTable(t, db)
	for _, col := range []string{"key_mode TEXT", "snr REAL", "plugin_version TEXT", "policy_version TEXT", "community INTEGER NOT NULL DEFAULT 1"} {
		if _, err := db.conn.Exec(`ALTER TABLE guest_polling_claims ADD COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.conn.Exec(`CREATE TABLE IF NOT EXISTS neighbor_edges (node_a TEXT NOT NULL, node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1, last_seen TEXT, PRIMARY KEY (node_a, node_b))`); err != nil {
		t.Fatal(err)
	}
}

type v2Claim struct {
	node, obs, via, key, version string
	hops                         int
	snr                          *float64
	community                    bool
	first, last                  time.Time
}

func insertV2Claim(t *testing.T, db *DB, c v2Claim) {
	t.Helper()
	if c.key == "" {
		c.key = "shared"
	}
	comm := 0
	if c.community {
		comm = 1
	}
	var via interface{}
	if c.via != "" {
		via = c.via
	}
	if _, err := db.conn.Exec(`INSERT INTO guest_polling_claims
		(node_id, observer_id, reply_hops, via, first_seen, last_seen, key_mode, snr, plugin_version, policy_version, community)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.node, c.obs, c.hops, via, c.first.UTC().Format(time.RFC3339), c.last.UTC().Format(time.RFC3339),
		c.key, c.snr, c.version, "", comm); err != nil {
		t.Fatal(err)
	}
}

func insertEdge(t *testing.T, db *DB, a, b string, at time.Time) {
	t.Helper()
	if a > b {
		a, b = b, a
	}
	if _, err := db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?,?,1,?)`,
		a, b, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func policyServer(t *testing.T, db *DB, enabled bool, pc *pollpolicy.Config) (*Server, *mux.Router) {
	t.Helper()
	srv := NewServer(db, &Config{Port: 3000, NeighborBattery: &NeighborBatteryServerConfig{Enabled: enabled, Polling: pc}}, NewHub())
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	return srv, r
}

func getJSON(t *testing.T, r *mux.Router, url string, wantCode int) map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != wantCode {
		t.Fatalf("%s: status %d, want %d: %s", url, w.Code, wantCode, w.Body.String())
	}
	if wantCode != 200 {
		return nil
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGuestPollingPolicyEndpoint(t *testing.T) {
	db := setupTestDB(t)
	_, off := policyServer(t, db, false, nil)
	getJSON(t, off, "/api/guest-polling/policy", 404)

	_, r := policyServer(t, db, true, nil)
	body := getJSON(t, r, "/api/guest-polling/policy", 200)
	if body["direct_s"] != 3600.0 || body["one_hop_s"] != 7200.0 || body["grace_s"] != 1800.0 ||
		body["max_requests"] != 2.0 || body["takeover_after"] != 2.0 || body["paused"] != false ||
		body["version_ok"] != true || body["min_plugin_version"] != "" {
		t.Errorf("defaults %v", body)
	}
	if _, ok := body["observer_blocked"]; ok {
		t.Error("observer_blocked without an observer")
	}
	if ex, ok := body["excluded_nodes"].([]interface{}); !ok || len(ex) != 0 {
		t.Errorf("excluded_nodes = %v, want []", body["excluded_nodes"])
	}
	if v, _ := body["version"].(string); len(v) != 8 {
		t.Errorf("version %v", body["version"])
	}
	getJSON(t, r, "/api/guest-polling/policy?observer=xyz", 400)

	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	cfg := &pollpolicy.Config{DirectMinutes: 90, Paused: true, PausedReason: "survey", PausedUntil: until.Format(time.RFC3339),
		ExcludedNodes: []string{strings.ToUpper(nbNode)}, BlockedObservers: []string{nbObsA},
		MinPluginVersion: "0.8.2", MinVersionReason: "login storm", NodeMinutes: map[string]int{nbNode2: 240}}
	_, r = policyServer(t, db, true, cfg)
	cases := []struct {
		query           string
		versionOK       interface{}
		observerBlocked interface{}
	}{
		{"", true, nil},
		{"?plugin_version=0.8.1", false, nil},
		{"?plugin_version=0.8.2", true, nil},
		{"?plugin_version=openhop.neighbour_battery/0.9.0&observer=" + strings.ToUpper(nbObsA), true, true},
		{"?observer=" + nbObsB, true, false},
	}
	for _, tc := range cases {
		body = getJSON(t, r, "/api/guest-polling/policy"+tc.query, 200)
		if body["version_ok"] != tc.versionOK || body["observer_blocked"] != tc.observerBlocked {
			t.Errorf("%s: version_ok %v observer_blocked %v", tc.query, body["version_ok"], body["observer_blocked"])
		}
	}
	if body["direct_s"] != 5400.0 || body["paused"] != true || body["paused_reason"] != "survey" ||
		body["paused_until"] != until.Format(time.RFC3339) || body["min_plugin_version"] != "0.8.2" ||
		body["min_version_reason"] != "login storm" {
		t.Errorf("configured %v", body)
	}
	if ex := body["excluded_nodes"].([]interface{}); len(ex) != 1 || ex[0] != nbNode {
		t.Errorf("excluded %v", ex)
	}
	if nw := body["node_windows"].(map[string]interface{}); nw[nbNode2] != 14400.0 {
		t.Errorf("node_windows %v", nw)
	}
}

// Hot reload: the server reads the policy through its Holder, so a reload
// takes effect on the next request.
func TestGuestPollingPolicyHotReload(t *testing.T) {
	db := setupTestDB(t)
	srv, r := policyServer(t, db, true, &pollpolicy.Config{DirectMinutes: 90})
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"neighborBattery":{"enabled":true,"polling":{"directMinutes":90}}}`), 0o644)
	c, _ := pollpolicy.ReadFile(path)
	srv.pollPolicy = pollpolicy.NewHolder(c)
	w := pollpolicy.NewWatcher(srv.pollPolicy, []string{path}, nil)
	before := getJSON(t, r, "/api/guest-polling/policy", 200)
	if before["direct_s"] != 5400.0 {
		t.Fatalf("before %v", before)
	}
	os.WriteFile(path, []byte(`{"neighborBattery":{"enabled":true,"polling":{"directMinutes":45,"paused":true}}}`), 0o644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(path, future, future)
	if changed, err := w.Check(false); !changed || err != nil {
		t.Fatalf("reload: %v %v", changed, err)
	}
	after := getJSON(t, r, "/api/guest-polling/policy", 200)
	if after["direct_s"] != 2700.0 || after["paused"] != true || after["version"] == before["version"] {
		t.Errorf("after %v", after)
	}
}

type ownersBody struct {
	Observer string                            `json:"observer"`
	Nodes    map[string]map[string]interface{} `json:"nodes"`
	Policy   map[string]interface{}            `json:"policy"`
}

func getOwnersBody(t *testing.T, r *mux.Router, query string) ownersBody {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/guest-polling/owners?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var b ownersBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// seedOwners: nbNode claimed by A (direct, confirmed by an edge) and B
// (1 hop via a relay); nbNode2 claimed by B with community off.
func seedOwners(t *testing.T, db *DB, versionA, versionB string) time.Time {
	t.Helper()
	now := seedNeighborNode(t, db)
	createPollingClaimsV2Table(t, db)
	insertV2Claim(t, db, v2Claim{node: nbNode, obs: nbObsA, hops: 0, version: versionA, community: true, snr: cpSNR(6.5),
		first: now.Add(-30 * time.Minute), last: now.Add(-2 * time.Minute)})
	insertV2Claim(t, db, v2Claim{node: nbNode, obs: nbObsB, hops: 1, via: strings.Repeat("ab", 32), version: versionB, community: true,
		first: now.Add(-20 * time.Minute), last: now.Add(-2 * time.Minute)})
	insertV2Claim(t, db, v2Claim{node: nbNode2, obs: nbObsB, hops: 0, version: versionB, community: false,
		first: now.Add(-20 * time.Minute), last: now.Add(-2 * time.Minute)})
	insertEdge(t, db, nbObsA, nbNode, now.Add(-time.Hour))
	insertNeighborBattery(t, db, nbObsA, now.Add(-10*time.Minute), 4040, 6.5, 0)
	return now
}

func TestGuestPollingOwnersContract(t *testing.T) {
	sentinel := strings.Repeat("0", 64)
	cases := []struct {
		name     string
		cfg      *pollpolicy.Config
		query    string
		node     string
		owner    string
		ownerNm  string
		mine     bool
		role     string
		excluded bool
	}{
		{"poller", nil, "observer=" + nbObsA + "&plugin_version=0.8.0", nbNode, nbObsA, "Corstorphine OBS", true, "polling", false},
		{"standby", nil, "observer=" + nbObsB + "&plugin_version=0.8.0", nbNode, nbObsA, "Corstorphine OBS", false, "standby", false},
		{"independent: community off", nil, "observer=" + nbObsB, nbNode2, nbObsB, "Pentland OBS", true, "independent", false},
		{"paused", &pollpolicy.Config{Paused: true}, "observer=" + nbObsB, nbNode, sentinel, "nobody (paused)", false, "paused", false},
		{"excluded", &pollpolicy.Config{ExcludedNodes: []string{nbNode}}, "observer=" + nbObsA, nbNode, sentinel, "nobody (excluded)", false, "excluded", true},
		{"blocked asker", &pollpolicy.Config{BlockedObservers: []string{nbObsA}}, "observer=" + nbObsA, nbNode, sentinel,
			"nobody (this observer is disabled)", false, "disabled", false},
		{"blocked poller hands over", &pollpolicy.Config{BlockedObservers: []string{nbObsA}}, "observer=" + nbObsB, nbNode, nbObsB,
			"Pentland OBS", true, "polling", false},
		{"version param below minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"}, "observer=" + nbObsA + "&plugin_version=0.8.1",
			nbNode, sentinel, "nobody (this observer is disabled)", false, "disabled", false},
		// An old plugin sends no plugin_version: its claims' version (0.7.0) decides.
		{"old plugin, claim below minimum", &pollpolicy.Config{MinPluginVersion: "0.8.0"}, "observer=" + nbObsB, nbNode, sentinel,
			"nobody (this observer is disabled)", false, "disabled", false},
		{"upgraded plugin before its next claim", &pollpolicy.Config{MinPluginVersion: "0.8.0"}, "observer=" + nbObsB + "&plugin_version=0.8.0",
			nbNode, nbObsA, "Corstorphine OBS", false, "standby", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			seedOwners(t, db, "0.8.0", "0.7.0")
			_, r := policyServer(t, db, true, tc.cfg)
			b := getOwnersBody(t, r, tc.query)
			e := b.Nodes[tc.node]
			if e == nil {
				t.Fatalf("node missing: %v", b.Nodes)
			}
			if e["owner"] != tc.owner || e["owner_name"] != tc.ownerNm || e["mine"] != tc.mine || e["role"] != tc.role || e["excluded"] != tc.excluded {
				t.Errorf("got %v", e)
			}
			if b.Policy == nil || b.Policy["version"] == nil {
				t.Errorf("policy missing: %v", b.Policy)
			}
		})
	}
}

func TestGuestPollingOwnersFields(t *testing.T) {
	db := setupTestDB(t)
	now := seedOwners(t, db, "0.8.0", "0.8.0")
	_, r := policyServer(t, db, true, &pollpolicy.Config{MinPluginVersion: "0.8.2"})
	// A plugin at 0.8.0 asking without a version: its claims are below 0.8.2.
	b := getOwnersBody(t, r, "observer="+nbObsB)
	if b.Policy["version_ok"] != false || b.Policy["observer_blocked"] != false {
		t.Errorf("policy %v", b.Policy)
	}

	_, r = policyServer(t, db, true, nil)
	b = getOwnersBody(t, r, "observer="+nbObsB+"&plugin_version=0.8.0")
	e := b.Nodes[nbNode]
	latest := now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if e["latest_ts"] != latest || e["window_s"] != 3600.0 || e["owner_hops"] != 0.0 {
		t.Errorf("fields %v", e)
	}
	if e["next_due"] != now.Add(50*time.Minute).UTC().Format(time.RFC3339) ||
		e["standby_due"] != now.Add(80*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("due %v %v", e["next_due"], e["standby_due"])
	}
	// The independent node has no reading: latest and dues are null, its own
	// (unconfirmed direct, so 1-hop) window applies.
	e = b.Nodes[nbNode2]
	if e["latest_ts"] != nil || e["next_due"] != nil || e["standby_due"] != nil || e["window_s"] != 7200.0 {
		t.Errorf("independent %v", e)
	}
	if b.Policy["version_ok"] != true {
		t.Errorf("policy %v", b.Policy)
	}
}

// An older ingestor's claims table (no v2 columns, no neighbor_edges) still
// works: every claim reads as shared, community, version unknown.
func TestGuestPollingOwnersV1Table(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	createPollingClaimsTable(t, db)
	insertPollingClaim(t, db, nbNode, nbObsA, 0, now.Add(-30*time.Minute), now.Add(-time.Minute))
	_, r := policyServer(t, db, true, nil)
	if e := getOwnersBody(t, r, "observer="+nbObsA).Nodes[nbNode]; e["owner"] != nbObsA || e["role"] != "polling" || e["owner_hops"] != 1.0 {
		t.Errorf("v1 %v", e)
	}
	// With a minimum, an unknown version is below it.
	_, r = policyServer(t, db, true, &pollpolicy.Config{MinPluginVersion: "0.1.0"})
	if e := getOwnersBody(t, r, "observer="+nbObsA).Nodes[nbNode]; e["role"] != "disabled" || e["mine"] != false {
		t.Errorf("v1 under a minimum %v", e)
	}
}

func TestNodeMonitorsEndpoint(t *testing.T) {
	db := setupTestDB(t)
	now := seedOwners(t, db, "0.8.0", "0.8.0")
	// C: a stale claim (3 h old); D: on a disabled version.
	insertV2Claim(t, db, v2Claim{node: nbNode, obs: cpObsC, hops: 0, version: "0.8.0", community: true,
		first: now.Add(-5 * time.Hour), last: now.Add(-3 * time.Hour)})
	insertV2Claim(t, db, v2Claim{node: nbNode, obs: cpObsD, hops: 0, version: "0.7.0", community: true,
		first: now.Add(-5 * time.Hour), last: now.Add(-time.Minute)})
	_, off := policyServer(t, db, false, nil)
	getJSON(t, off, "/api/nodes/"+nbNode+"/monitors", 404)
	_, r := policyServer(t, db, true, &pollpolicy.Config{MinPluginVersion: "0.8.0"})
	getJSON(t, r, "/api/nodes/xyz/monitors", 400)
	body := getJSON(t, r, "/api/nodes/"+strings.ToUpper(nbNode)+"/monitors", 200)
	if body["node_id"] != nbNode || body["window_s"] != 3600.0 || body["paused"] != false || body["excluded"] != false || body["policy_version"] == "" {
		t.Errorf("head %v", body)
	}
	mons := body["monitors"].([]interface{})
	var roles []string
	for _, m := range mons {
		row := m.(map[string]interface{})
		roles = append(roles, row["observer_id"].(string)[:4]+":"+row["role"].(string))
	}
	if strings.Join(roles, " ") != "7e76:polling 3b9f:standby cc33:stale dd44:version_disabled" {
		t.Errorf("rows %v", roles)
	}
	a := mons[0].(map[string]interface{})
	if a["name"] != "Corstorphine OBS" || a["hops"] != 0.0 || a["effective_hops"] != 0.0 || a["direct_confirmed"] != true ||
		a["via"] != nil || a["key"] != "shared" || a["snr"] != 6.5 || a["plugin_version"] != "0.8.0" || a["community"] != true ||
		a["readings_7d"] != 1.0 || a["last_reading"] != now.Add(-10*time.Minute).UTC().Format(time.RFC3339) ||
		a["next_due"] != now.Add(50*time.Minute).UTC().Format(time.RFC3339) || a["standby_due"] != nil {
		t.Errorf("poller row %v", a)
	}
	b := mons[1].(map[string]interface{})
	if b["effective_hops"] != 1.0 || b["direct_confirmed"] != false || b["via"] != strings.Repeat("ab", 32) ||
		b["last_reading"] != nil || b["readings_7d"] != 0.0 || b["standby_due"] != now.Add(80*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("standby row %v", b)
	}
	if d := mons[3].(map[string]interface{}); d["next_due"] != nil || d["standby_due"] != nil {
		t.Errorf("ineligible row has dues %v", d)
	}

	// Paused: every row says paused (the stale and disabled keep their reasons).
	_, r = policyServer(t, db, true, &pollpolicy.Config{Paused: true, PausedReason: "survey", MinPluginVersion: "0.8.0"})
	body = getJSON(t, r, "/api/nodes/"+nbNode+"/monitors", 200)
	roles = nil
	for _, m := range body["monitors"].([]interface{}) {
		roles = append(roles, m.(map[string]interface{})["role"].(string))
	}
	if strings.Join(roles, " ") != "paused paused stale version_disabled" || body["paused"] != true || body["paused_reason"] != "survey" {
		t.Errorf("paused rows %v %v", roles, body["paused"])
	}
}

func TestObserverMonitorsEndpoint(t *testing.T) {
	db := setupTestDB(t)
	seedOwners(t, db, "0.8.0", "0.8.0")
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'Lucklaw Hill', 'repeater')`, nbNode2)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'Relay One', 'repeater')`, strings.Repeat("ab", 32))
	_, r := policyServer(t, db, true, nil)
	getJSON(t, r, "/api/observers/xyz/monitors", 400)
	body := getJSON(t, r, "/api/observers/"+strings.ToUpper(nbObsB)+"/monitors", 200)
	if body["observer_id"] != nbObsB || body["name"] != "Pentland OBS" || body["blocked"] != false ||
		body["plugin_version"] != "0.8.0" || body["version_ok"] != true {
		t.Errorf("head %v", body)
	}
	nodes := body["nodes"].([]interface{})
	if len(nodes) != 2 {
		t.Fatalf("nodes %v", nodes)
	}
	first, second := nodes[0].(map[string]interface{}), nodes[1].(map[string]interface{})
	if first["node_id"] != nbNode || first["role"] != "standby" || first["name"] != "West Lomond" ||
		first["via_name"] != "Relay One" || first["effective_hops"] != 1.0 {
		t.Errorf("first %v", first)
	}
	if second["node_id"] != nbNode2 || second["role"] != "independent" || second["name"] != "Lucklaw Hill" {
		t.Errorf("second %v", second)
	}
	_, r = policyServer(t, db, true, &pollpolicy.Config{BlockedObservers: []string{nbObsB}, MinPluginVersion: "0.9.0"})
	body = getJSON(t, r, "/api/observers/"+nbObsB+"/monitors", 200)
	if body["blocked"] != true || body["version_ok"] != false || body["min_plugin_version"] != "0.9.0" {
		t.Errorf("blocked head %v", body)
	}
	if n := body["nodes"].([]interface{})[0].(map[string]interface{}); n["role"] != "blocked" {
		t.Errorf("blocked row %v", n)
	}
}

func TestNodesMonitoredEnrichment(t *testing.T) {
	db := setupTestDB(t)
	seedOwners(t, db, "0.8.0", "0.8.0")
	excludedOnly := strings.Repeat("ee", 32)
	srv, _ := policyServer(t, db, true, &pollpolicy.Config{ExcludedNodes: []string{excludedOnly}})
	nodes := []map[string]interface{}{
		{"public_key": strings.ToUpper(nbNode), "role": "repeater"},
		{"public_key": nbNode2, "role": "repeater"},
		{"public_key": excludedOnly, "role": "repeater"},
		{"public_key": strings.Repeat("ff", 32), "role": "repeater"},
	}
	srv.enrichNodesMonitored(nodes)
	m, ok := nodes[0]["monitored"].(*nodeMonitoring)
	if !ok || m.Count != 2 || m.Direct != 1 || m.Poller != nbObsA || m.Name != "Corstorphine OBS" || m.WindowS != 3600 {
		t.Errorf("node 1 %+v", nodes[0]["monitored"])
	}
	// Only an independent claimant: claimed, but nobody eligible.
	if m, ok := nodes[1]["monitored"].(*nodeMonitoring); !ok || m.Count != 0 || m.Poller != "" {
		t.Errorf("node 2 %+v", nodes[1]["monitored"])
	}
	if m, ok := nodes[2]["monitored"].(*nodeMonitoring); !ok || !m.Excluded {
		t.Errorf("excluded %+v", nodes[2]["monitored"])
	}
	if _, ok := nodes[3]["monitored"]; ok {
		t.Error("unclaimed node got monitored")
	}
	// Disabled feature: no field.
	srvOff, _ := policyServer(t, db, false, nil)
	plain := []map[string]interface{}{{"public_key": nbNode, "role": "repeater"}}
	srvOff.enrichNodesMonitored(plain)
	if _, ok := plain[0]["monitored"]; ok {
		t.Error("monitored with neighborBattery off")
	}
}

// The polling block is part of the server's config, read at start.
func TestServerConfigPollingBlock(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"neighborBattery":{"enabled":true,"polling":{"oneHopMinutes":180,"excludedNodes":["`+nbNode+`"]}}}`), 0o644)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(nil, cfg, nil)
	p := srv.pollingPolicy(time.Now())
	if p.OneHopMinutes != 180 || p.DirectMinutes != 60 || !p.IsExcluded(nbNode) {
		t.Errorf("policy %+v", p)
	}
}

func TestGuestPollingSummary(t *testing.T) {
	db := setupTestDB(t)
	now := seedOwners(t, db, "0.8.0", "0.7.0")
	insertV2Claim(t, db, v2Claim{node: nbNode, obs: cpObsC, hops: 1, version: "0.8.0", community: true,
		first: now.Add(-time.Hour), last: now.Add(-time.Minute)})
	_, off := policyServer(t, db, false, nil)
	getJSON(t, off, "/api/guest-polling/summary", 404)
	_, r := policyServer(t, db, true, &pollpolicy.Config{MinPluginVersion: "0.8.0", BlockedObservers: []string{cpObsC, cpObsD},
		ExcludedNodes: []string{strings.Repeat("ee", 32)}})
	body := getJSON(t, r, "/api/guest-polling/summary", 200)
	sum := body["summary"].(map[string]interface{})
	want := map[string]float64{"monitored_nodes": 1, "direct_nodes": 1, "excluded_nodes": 1, "observers": 1,
		"observers_below_min": 1, "blocked_observers": 2, "blocked_observers_on": 1}
	for k, v := range want {
		if sum[k] != v {
			t.Errorf("%s = %v, want %v", k, sum[k], v)
		}
	}
	if p := body["policy"].(map[string]interface{}); p["min_plugin_version"] != "0.8.0" {
		t.Errorf("policy %v", p)
	}
}

// Standbys queue: each waits one more grace period, so a silent poller is
// replaced by one standby rather than by every standby at once.
func TestElectNodeStandbysQueue(t *testing.T) {
	const obsC = "cc" + "11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11cc11"
	pol := (&pollpolicy.Config{}).Effective(cpNow)
	el := electNode(nbNode, []pollClaimRow{claim(obsC, 1), claim(nbObsB, 1), claim(nbObsA, 1)}, pol, cpNow)
	ranks := []int{}
	for _, c := range el.Claims {
		ranks = append(ranks, c.StandbyRank)
	}
	if len(ranks) != 3 || ranks[0] != 0 || ranks[1] != 1 || ranks[2] != 2 {
		t.Errorf("ranks = %v, want the poller 0 and standbys 1, 2", ranks)
	}
	if standbyWait(30*time.Minute, 2) != time.Hour || standbyWait(30*time.Minute, 0) != 30*time.Minute {
		t.Errorf("standbyWait wrong")
	}
}
