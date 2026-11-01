package main

import (
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/pollpolicy"
)

const (
	cpObsA  = "aa11bb00112233445566778899aabbccddeeff00112233445566778899001122"
	cpObsB  = "bb22cc00112233445566778899aabbccddeeff00112233445566778899003344"
	cpNode2 = "c0ffee00112233445566778899aabbccddeeff00112233445566778899005566"
)

// withPolicy installs a polling policy for the test and restores the old one.
func withPolicy(t *testing.T, c *pollpolicy.Config) {
	t.Helper()
	old := pollingPolicy
	pollingPolicy = pollpolicy.NewHolder(c)
	t.Cleanup(func() { pollingPolicy = old })
}

func TestParsePollingClaims(t *testing.T) {
	via := strings.Repeat("ab", 32)
	cases := []struct {
		name   string
		obs    string
		msg    map[string]interface{}
		ok     bool
		header claimHeader
		claims []pollingClaim
	}{
		{
			name: "v1 defaults", obs: strings.ToUpper(cpObsA),
			msg: map[string]interface{}{"plugin": "openhop.neighbour_battery/0.7.3", "nodes": []interface{}{
				map[string]interface{}{"node_id": strings.ToUpper(testTargetPK), "reply_hops": 1.0, "via": via},
			}},
			ok: true, header: claimHeader{version: 1, pluginVersion: "0.7.3", community: true},
			claims: []pollingClaim{{node: testTargetPK, hops: 1, via: via, key: "shared", pluginVersion: "0.7.3", community: true}},
		},
		{
			name: "v2 full", obs: cpObsA,
			msg: map[string]interface{}{"v": 2.0, "origin_id": cpObsA, "plugin": "openhop.neighbour_battery/0.8.0",
				"policy_version": "A1B2C3D4", "community": false, "nodes": []interface{}{
					map[string]interface{}{"node_id": testTargetPK, "reply_hops": 0.0, "via": nil, "key": "own", "snr": 6.5},
					map[string]interface{}{"node_id": cpNode2, "reply_hops": 1.0, "via": "AB12", "key": "password", "snr": nil},
				}},
			ok: true, header: claimHeader{version: 2, pluginVersion: "0.8.0", policyVersion: "a1b2c3d4", community: false},
			claims: []pollingClaim{
				{node: testTargetPK, key: "own", snr: f64(6.5), pluginVersion: "0.8.0", policyVersion: "a1b2c3d4"},
				{node: cpNode2, hops: 1, via: "ab12", key: "password", pluginVersion: "0.8.0", policyVersion: "a1b2c3d4"},
			},
		},
		{
			name: "bad entries skipped", obs: cpObsA,
			msg: map[string]interface{}{"v": 2.0, "nodes": []interface{}{
				"nope",
				map[string]interface{}{"node_id": "12"},
				map[string]interface{}{"node_id": cpObsA}, // self
				map[string]interface{}{"node_id": testTargetPK, "key": "weird", "snr": 99.0, "reply_hops": 1.5, "via": "xyz"},
				map[string]interface{}{"node_id": testTargetPK, "reply_hops": 3.0}, // duplicate
			}},
			ok: true, header: claimHeader{version: 2, community: true},
			claims: []pollingClaim{{node: testTargetPK, key: "shared", community: true}},
		},
		{name: "spoofed origin", obs: cpObsA, msg: map[string]interface{}{"origin_id": cpObsB}, ok: false},
		{name: "bad observer", obs: "zz", msg: map[string]interface{}{}, ok: false},
	}
	for _, tc := range cases {
		obs, h, claims, ok := parsePollingClaims(tc.obs, tc.msg)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v", tc.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if obs != strings.ToLower(tc.obs) {
			t.Errorf("%s: observer %s", tc.name, obs)
		}
		if h != tc.header {
			t.Errorf("%s: header %+v, want %+v", tc.name, h, tc.header)
		}
		if len(claims) != len(tc.claims) {
			t.Errorf("%s: claims %+v", tc.name, claims)
			continue
		}
		for i, want := range tc.claims {
			got := claims[i]
			if (got.snr == nil) != (want.snr == nil) || (got.snr != nil && *got.snr != *want.snr) {
				t.Errorf("%s #%d: snr %v want %v", tc.name, i, got.snr, want.snr)
			}
			got.snr, want.snr = nil, nil
			if got != want {
				t.Errorf("%s #%d: %+v, want %+v", tc.name, i, got, want)
			}
		}
	}
}

func f64(v float64) *float64 { return &v }

type claimRow struct {
	hops                     int
	via, key, plugin, policy string
	snr                      sql.NullFloat64
	community                int
	firstSeen, lastSeen      string
}

func readClaim(t *testing.T, s *Store, node, obs string) (claimRow, bool) {
	t.Helper()
	var r claimRow
	err := s.db.QueryRow(`SELECT reply_hops, COALESCE(via,''), COALESCE(key_mode,''), COALESCE(plugin_version,''),
		COALESCE(policy_version,''), snr, community, first_seen, last_seen
		FROM guest_polling_claims WHERE node_id = ? AND observer_id = ?`, node, obs).
		Scan(&r.hops, &r.via, &r.key, &r.plugin, &r.policy, &r.snr, &r.community, &r.firstSeen, &r.lastSeen)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// Claim v2 fields are stored; a later v1 claim overwrites them with the v1
// defaults but keeps first_seen.
func TestClaimV2Storage(t *testing.T) {
	s := newTestStore(t)
	d := newSharedGuestDecoder(s)
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	d.HandleClaim(cpObsA, map[string]interface{}{"v": 2.0, "plugin": "openhop.neighbour_battery/0.8.0",
		"policy_version": "a1b2c3d4", "community": false, "nodes": []interface{}{
			map[string]interface{}{"node_id": testTargetPK, "reply_hops": 1.0, "via": "ab12", "key": "own", "snr": -3.25},
		}}, t1)
	r, ok := readClaim(t, s, testTargetPK, cpObsA)
	if !ok {
		t.Fatal("claim not stored")
	}
	if r.hops != 1 || r.via != "ab12" || r.key != "own" || r.plugin != "0.8.0" || r.policy != "a1b2c3d4" ||
		!r.snr.Valid || r.snr.Float64 != -3.25 || r.community != 0 {
		t.Errorf("v2 row %+v", r)
	}
	t2 := t1.Add(30 * time.Minute)
	d.HandleClaim(cpObsA, map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"node_id": testTargetPK}}}, t2)
	r, _ = readClaim(t, s, testTargetPK, cpObsA)
	if r.hops != 0 || r.via != "" || r.key != "shared" || r.plugin != "" || r.snr.Valid || r.community != 1 {
		t.Errorf("v1 overwrite %+v", r)
	}
	if r.firstSeen != t1.Format(batteryTimeLayout) || r.lastSeen != t2.Format(batteryTimeLayout) {
		t.Errorf("first/last %s %s", r.firstSeen, r.lastSeen)
	}
}

// Claims are stored whatever the policy says of the observer: the server
// decides eligibility at query time.
func TestClaimsStoredFromIneligibleObservers(t *testing.T) {
	withPolicy(t, &pollpolicy.Config{BlockedObservers: []string{cpObsA}, MinPluginVersion: "0.9.0",
		ExcludedNodes: []string{testTargetPK}, Paused: true})
	s := newTestStore(t)
	d := newSharedGuestDecoder(s)
	d.HandleClaim(cpObsA, map[string]interface{}{"plugin": "openhop.neighbour_battery/0.1.0",
		"nodes": []interface{}{map[string]interface{}{"node_id": testTargetPK}}}, time.Now().UTC())
	if _, ok := readClaim(t, s, testTargetPK, cpObsA); !ok {
		t.Error("claim from a blocked, old observer for an excluded node was not stored")
	}
}

// Without the shared-key decoder the claims are still stored, as long as
// neighborBattery is enabled (handled in main's topic switch).
func TestStorePollingClaimsWithoutDecoder(t *testing.T) {
	s := newTestStore(t)
	storePollingClaims(s, cpObsB, map[string]interface{}{"v": 2.0, "plugin": "openhop.neighbour_battery/0.8.0",
		"nodes": []interface{}{map[string]interface{}{"node_id": cpNode2, "key": "password", "snr": 4.0}}}, time.Now().UTC())
	r, ok := readClaim(t, s, cpNode2, cpObsB)
	if !ok || r.key != "password" || r.plugin != "0.8.0" {
		t.Errorf("row %+v %v", r, ok)
	}
	storePollingClaims(s, cpObsB, map[string]interface{}{"origin_id": cpObsA, "nodes": []interface{}{
		map[string]interface{}{"node_id": testTargetPK}}}, time.Now().UTC())
	if _, ok := readClaim(t, s, testTargetPK, cpObsB); ok {
		t.Error("spoofed claim stored")
	}
}

// The v2 columns are added to a claims table created before them, and the
// migration is idempotent.
func TestMigrateGuestPollingClaimsV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE guest_polling_claims (
		node_id TEXT NOT NULL, observer_id TEXT NOT NULL, reply_hops INTEGER NOT NULL DEFAULT 0,
		via TEXT, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
		PRIMARY KEY (node_id, observer_id)) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO guest_polling_claims VALUES (?,?,0,NULL,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`, testTargetPK, cpObsA)
	for i := 0; i < 2; i++ {
		if err := migrateGuestPollingClaimsV2(db); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var n, community int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('guest_polling_claims')
		WHERE name IN ('key_mode','snr','plugin_version','policy_version','community')`).Scan(&n)
	if n != 5 {
		t.Errorf("v2 columns = %d, want 5", n)
	}
	// An old row reads as community (the v1 default).
	db.QueryRow(`SELECT community FROM guest_polling_claims`).Scan(&community)
	if community != 1 {
		t.Errorf("old row community = %d", community)
	}
	os.Remove(path)
}

func batteryMsgFrom(obs, plugin string, readings ...map[string]interface{}) map[string]interface{} {
	m := batteryMsg(readings...)
	m["origin_id"] = obs
	if plugin != "" {
		m["plugin"] = plugin
	}
	return m
}

func TestBatteryReportDrops(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	cases := []struct {
		name   string
		policy *pollpolicy.Config
		obs    string
		plugin string
		nodes  []string
		want   []string // nodes stored
	}{
		{"no policy keeps all", nil, cpObsA, "", []string{testTargetPK, cpNode2}, []string{testTargetPK, cpNode2}},
		{"blocked observer", &pollpolicy.Config{BlockedObservers: []string{strings.ToUpper(cpObsA)}}, cpObsA,
			"openhop.neighbour_battery/0.8.0", []string{testTargetPK}, nil},
		{"other observer blocked", &pollpolicy.Config{BlockedObservers: []string{cpObsB}}, cpObsA, "", []string{testTargetPK}, []string{testTargetPK}},
		{"below minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"}, cpObsA, "openhop.neighbour_battery/0.8.1", []string{testTargetPK}, nil},
		{"no version with minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"}, cpObsA, "", []string{testTargetPK}, nil},
		{"at minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"}, cpObsA, "openhop.neighbour_battery/0.8.2", []string{testTargetPK}, []string{testTargetPK}},
		{"excluded node", &pollpolicy.Config{ExcludedNodes: []string{strings.ToUpper(cpNode2)}}, cpObsA, "",
			[]string{testTargetPK, cpNode2}, []string{testTargetPK}},
		{"paused still stores", &pollpolicy.Config{Paused: true}, cpObsA, "", []string{testTargetPK}, []string{testTargetPK}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPolicy(t, tc.policy)
			s := newTestStore(t)
			var rs []map[string]interface{}
			for _, n := range tc.nodes {
				rs = append(rs, batteryReading(n, 3900, now))
			}
			handleBatteryReport(s, batteryCfg(), "test", tc.obs, batteryMsgFrom(tc.obs, tc.plugin, rs...))
			rows, err := s.db.Query(`SELECT node_id FROM neighbor_battery ORDER BY node_id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var n string
				rows.Scan(&n)
				got = append(got, n)
			}
			want := map[string]bool{}
			for _, n := range tc.want {
				want[n] = true
			}
			if len(got) != len(want) {
				t.Fatalf("stored %v, want %v", got, tc.want)
			}
			for _, n := range got {
				if !want[n] {
					t.Errorf("stored %s, want %v", n, tc.want)
				}
			}
		})
	}
}

// A policy change (as a reload installs it) applies to the next report
// without a restart.
func TestBatteryDropsFollowReload(t *testing.T) {
	withPolicy(t, nil)
	s := newTestStore(t)
	now := time.Now().UTC()
	handleBatteryReport(s, batteryCfg(), "test", cpObsA, batteryMsgFrom(cpObsA, "", batteryReading(testTargetPK, 3900, now.Add(-time.Hour))))
	pollingPolicy.Set(&pollpolicy.Config{BlockedObservers: []string{cpObsA}})
	handleBatteryReport(s, batteryCfg(), "test", cpObsA, batteryMsgFrom(cpObsA, "", batteryReading(testTargetPK, 3800, now.Add(-time.Minute))))
	if n := batteryRows(t, s); n != 1 {
		t.Errorf("rows = %d, want 1 (second report blocked after the reload)", n)
	}
	pollingPolicy.Set(nil)
	handleBatteryReport(s, batteryCfg(), "test", cpObsA, batteryMsgFrom(cpObsA, "", batteryReading(testTargetPK, 3800, now.Add(-time.Minute))))
	if n := batteryRows(t, s); n != 2 {
		t.Errorf("rows = %d, want 2 (unblocked)", n)
	}
}

func TestDropLogRateLimit(t *testing.T) {
	d := &dropLog{last: map[string]time.Time{}}
	t0 := time.Now()
	logged := 0
	count := func(obs, reason string, at time.Time) {
		before := len(d.last)
		prev := d.last[obs+"|"+reason]
		d.logf(obs, reason, at, "%s", "x")
		if d.last[obs+"|"+reason] != prev || len(d.last) != before {
			logged++
		}
	}
	count(cpObsA, "blocked", t0)
	count(cpObsA, "blocked", t0.Add(10*time.Minute))
	count(cpObsA, "version", t0.Add(10*time.Minute))
	count(cpObsB, "blocked", t0.Add(10*time.Minute))
	count(cpObsA, "blocked", t0.Add(61*time.Minute))
	if logged != 4 {
		t.Errorf("logged %d, want 4 (once per observer and reason per hour)", logged)
	}
}

// decodeVector feeds the crypto test's shared-key reply to d as heard by obs.
func decodeVector(t *testing.T, d *sharedGuestDecoder, obs string) {
	t.Helper()
	payload := hexb(t, vecRespPay)
	pkt := &DecodedPacket{}
	pkt.Header.PayloadType = PayloadRESPONSE
	pkt.Payload.DestHash = hex.EncodeToString(payload[0:1])
	pkt.Payload.SrcHash = hex.EncodeToString(payload[1:2])
	pkt.Payload.MAC = hex.EncodeToString(payload[2:4])
	pkt.Payload.EncryptedData = hex.EncodeToString(payload[4:])
	d.Handle(obs, pkt, time.Unix(int64(vecTag), 30).UTC())
}

// A decoded shared-key reply whose requester is blocked or below the
// minimum is kept but credited to nobody; a claimant that logs in with its
// own key or a password is never credited with a shared-key reply.
func TestSharedReplyCredit(t *testing.T) {
	cases := []struct {
		name   string
		policy *pollpolicy.Config
		claims []map[string]interface{} // claims from cpObsA then cpObsB
		want   string                   // credited observer; "" = the shared key
	}{
		{"credited", nil, []map[string]interface{}{{"plugin": "openhop.neighbour_battery/0.8.0"}}, cpObsA},
		{"blocked", &pollpolicy.Config{BlockedObservers: []string{cpObsA}},
			[]map[string]interface{}{{"plugin": "openhop.neighbour_battery/0.8.0"}}, ""},
		{"below minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"},
			[]map[string]interface{}{{"plugin": "openhop.neighbour_battery/0.8.0"}}, ""},
		{"v1 without version under a minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"},
			[]map[string]interface{}{{}}, ""},
		{"at minimum", &pollpolicy.Config{MinPluginVersion: "0.8.2"},
			[]map[string]interface{}{{"plugin": "openhop.neighbour_battery/0.8.2"}}, cpObsA},
		{"own-key claimant skipped", nil, []map[string]interface{}{
			{"plugin": "openhop.neighbour_battery/0.8.0", "key": "shared"},
			{"plugin": "openhop.neighbour_battery/0.8.0", "key": "own"},
		}, cpObsA},
		{"only a password claimant", nil, []map[string]interface{}{{"key": "password"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPolicy(t, tc.policy)
			s := newTestStore(t)
			if err := s.UpsertNode(vecRepPub, "Test Repeater", "repeater", nil, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
				t.Fatal(err)
			}
			d := newSharedGuestDecoder(s)
			for i, c := range tc.claims {
				obs := []string{cpObsA, cpObsB}[i]
				node := map[string]interface{}{"node_id": vecRepPub}
				if k, ok := c["key"]; ok {
					node["key"] = k
				}
				msg := map[string]interface{}{"v": 2.0, "nodes": []interface{}{node}}
				if p, ok := c["plugin"]; ok {
					msg["plugin"] = p
				}
				// The later claim is the most recent, so it would win if eligible.
				d.HandleClaim(obs, msg, time.Unix(int64(vecTag), int64(10+i)).UTC())
			}
			decodeVector(t, d, "ccddee00112233445566778899aabbccddeeff00112233445566778899003344")
			var obs string
			if err := s.db.QueryRow(`SELECT observer_id FROM neighbor_battery`).Scan(&obs); err != nil {
				t.Fatalf("reading not kept: %v", err)
			}
			want := tc.want
			if want == "" {
				want = d.sharedObs
			}
			if obs != want {
				t.Errorf("credited %.8s, want %.8s", obs, want)
			}
		})
	}
}

// The polling block is part of the ingestor's config, read at start.
func TestConfigPollingBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"neighborBattery":{"enabled":true,"polling":{"directMinutes":45,"blockedObservers":["`+cpObsA+`"]}}}`), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := pollpolicy.NewHolder(cfg.NeighborBattery.Polling).Policy(time.Now())
	if p.DirectMinutes != 45 || !p.IsBlocked(cpObsA) || p.OneHopMinutes != 120 {
		t.Errorf("policy %+v", p)
	}
}
