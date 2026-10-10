package main

import (
	"testing"
)

// ?area= on /api/scope-audit keeps the repeaters that STAND in a configured
// area (their node position), in both views. It answers "are this area's
// repeaters set up right?", which observer regions cannot: those say who
// heard the traffic, not where the repeater is.

// withAreas gives the audit fixture node positions and two areas: A stands
// in Belgium, B in France, C has no position.
func withAreas(t *testing.T, srv *Server) {
	t.Helper()
	for _, col := range []string{"lat REAL", "lon REAL"} {
		if _, err := srv.db.conn.Exec(`ALTER TABLE nodes ADD COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []struct {
		pk       string
		lat, lon float64
	}{{testFullPubkeyA, 50.85, 4.35}, {testFullPubkeyB, 48.85, 2.35}} {
		if _, err := srv.db.conn.Exec(`UPDATE nodes SET lat = ?, lon = ? WHERE public_key = ?`, p.lat, p.lon, p.pk); err != nil {
			t.Fatal(err)
		}
	}
	areas := map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
		"FRA": {Label: "France", Polygon: [][2]float64{{42.0, -5.0}, {49.9, -5.0}, {49.9, 8.0}, {42.0, 8.0}}},
		"ESP": {Label: "Spain", Polygon: [][2]float64{{36.0, -9.0}, {41.9, -9.0}, {41.9, 3.0}, {36.0, 3.0}}},
	}
	srv.cfg.Areas = areas
	if srv.store != nil && srv.store.config != nil {
		srv.store.config.Areas = areas
	}
}

func transportKeys(rows []ScopeTransportRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.PublicKey] = true
	}
	return out
}

func TestScopeTransport_AreaKeepsRepeatersStandingThere(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	withAreas(t, srv)

	got := getScopeTransport(t, router, "&area=BEL")
	if got.Area != "BEL" {
		t.Errorf("area = %q, want BEL echoed", got.Area)
	}
	if keys := transportKeys(got.Repeaters); len(keys) != 1 || !keys[testFullPubkeyA] {
		t.Errorf("area=BEL lists %v, want only A (it stands in Belgium)", keys)
	}
	// Counts are over the area's repeaters: B carries fr but is not in BEL.
	sc := getScopeTransport(t, router, "&area=BEL&scope=fr")
	if sc.Carrying == nil || *sc.Carrying != 0 || sc.NotCarry == nil || *sc.NotCarry != 1 {
		t.Errorf("area=BEL&scope=fr: carrying=%v notCarrying=%v, want 0 and 1", sc.Carrying, sc.NotCarry)
	}
	if keys := transportKeys(getScopeTransport(t, router, "&area=FRA").Repeaters); len(keys) != 1 || !keys[testFullPubkeyB] {
		t.Errorf("area=FRA lists %v, want only B", keys)
	}
}

func TestScopeTransport_AreaMatchesNothingRatherThanEverything(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	withAreas(t, srv)
	for _, q := range []string{"&area=NOWHERE", "&area=ESP"} {
		if got := getScopeTransport(t, router, q); len(got.Repeaters) != 0 {
			t.Errorf("%s: %d repeaters, want none (an unknown or empty area must not widen to all)", q, len(got.Repeaters))
		}
	}
}

func TestScopeTransport_AreaDoesNotChangeTheCachedView(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	withAreas(t, srv)
	_ = getScopeTransport(t, router, "&area=BEL")
	all := getScopeTransport(t, router, "")
	if all.Area != "" {
		t.Errorf("no area asked, area = %q", all.Area)
	}
	if keys := transportKeys(all.Repeaters); !keys[testFullPubkeyA] || !keys[testFullPubkeyB] {
		t.Errorf("after an area request the unfiltered view lists %v, want A and B", keys)
	}
}

func TestScopeAudit_AreaFiltersTheDeclaredView(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	withAreas(t, srv)
	keys := func(r ScopeAuditResponse) map[string]bool {
		out := map[string]bool{}
		for _, row := range r.Repeaters {
			out[row.PublicKey] = true
		}
		return out
	}
	all := getScopeAudit(t, router, "")
	if !keys(all)[testFullPubkeyA] {
		t.Fatalf("fixture: A should be in the declared view: %v", keys(all))
	}
	bel := getScopeAudit(t, router, "?area=BEL")
	if bel.Area != "BEL" || !keys(bel)[testFullPubkeyA] {
		t.Errorf("area=BEL: area=%q rows=%v, want BEL with A", bel.Area, keys(bel))
	}
	if fra := getScopeAudit(t, router, "?area=FRA"); keys(fra)[testFullPubkeyA] {
		t.Errorf("area=FRA must not list A, which stands in Belgium: %v", keys(fra))
	}
	if again := getScopeAudit(t, router, ""); !keys(again)[testFullPubkeyA] || again.Area != "" {
		t.Errorf("the unfiltered declared view changed after an area request: area=%q rows=%v", again.Area, keys(again))
	}
}
