package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

const (
	pwGoyle = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	pwKeir  = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	pwWard  = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	pwInsh  = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"
	pwOther = "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5"
)

// pathWatchRouter seeds 20 days: each day 20 packets pass Goyle Hill, Keir
// Hill and Insheswood in that order, and Keir Hill relays its own traffic;
// in the last 24 hours Keir Hill is silent, and only 5 of Goyle Hill's 20
// reach Insheswood, through Ward Hill.
func pathWatchRouter(t *testing.T, enabled bool) *mux.Router {
	t.Helper()
	db := setupTestDB(t)
	now := time.Now().UTC()
	for _, n := range [][3]string{{pwGoyle, "Goyle Hill", "2026-01-01"}, {pwKeir, "Keir Hill", "2026-01-01"}, {pwWard, "Ward Hill", "2026-01-01"}, {pwInsh, "Insheswood", "2026-01-01"}, {pwOther, "Other", "2026-01-01"}} {
		mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count) VALUES (?, ?, 'repeater', 57, -3, ?, ?, 1)`, n[0], n[1], now.Format(time.RFC3339), n[2])
	}
	id := 0
	add := func(at time.Time, path ...string) {
		id++
		raw := make([]string, len(path))
		for i, p := range path {
			raw[i] = strings.ToUpper(p[:6])
		}
		rj, _ := json.Marshal(raw)
		pj, _ := json.Marshal(path)
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type) VALUES (?, 'AA', ?, ?, 5)`, id, fmt.Sprintf("h%06d", id), at.Format("2006-01-02T15:04:05.000Z"))
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, NULL, ?, ?, ?)`, id, string(rj), at.Unix(), string(pj))
	}
	for d := 19; d >= 0; d-- {
		for k := 0; k < 20; k++ {
			at := now.Add(-time.Duration(d+1)*24*time.Hour + time.Duration(k)*time.Hour + time.Minute)
			switch {
			case d > 0:
				add(at, pwGoyle, pwKeir, pwInsh)
				add(at.Add(time.Minute), pwKeir, pwOther)
			case k < 5:
				add(at, pwGoyle, pwWard, pwInsh)
			default:
				add(at, pwGoyle, pwOther)
			}
		}
	}
	return pathRouterFor(t, db, enabled)
}

// pathRouterFor serves the path watch over db, its index filled.
func pathRouterFor(t *testing.T, db *DB, enabled bool) *mux.Router {
	t.Helper()
	cfg := &Config{Port: 3000}
	if enabled {
		cfg.Data = &DataConfig{Enabled: true, Days: 7, PathDays: 30}
	}
	srv := NewServer(db, cfg, NewHub())
	if enabled {
		srv.paths = newPathIndex(db, cfg.PathDays())
		if err := srv.paths.fill(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	r := mux.NewRouter()
	r.HandleFunc("/api/data/path", srv.handleDataPath).Methods("GET")
	r.HandleFunc("/api/data/paths", srv.handleDataPaths).Methods("POST")
	return r
}

func TestDataPathFollowsTwoRepeatersAndFlagsARepeaterGoneQuiet(t *testing.T) {
	r := pathWatchRouter(t, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path?from="+pwGoyle+"&to="+pwInsh+"&window=7d", nil))
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var p DataPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.From.Name != "Goyle Hill" || p.To.Name != "Insheswood" || len(p.Days) != 7 {
		t.Fatalf("from %+v to %+v days %d", p.From, p.To, len(p.Days))
	}
	if last := p.Days[len(p.Days)-1]; last.Passed != 20 || last.Reached != 5 {
		t.Errorf("last 24 h %+v, want 5 of 20", last)
	}
	if p.Share != 0.25 || p.Usual != 1 || p.Verdict != "worse" {
		t.Errorf("share %v usual %v verdict %q", p.Share, p.Usual, p.Verdict)
	}
	if len(p.Routes) == 0 || p.Routes[0].Via[0].Name != "Keir Hill" {
		t.Errorf("routes %+v, want Keir Hill first", p.Routes)
	}
	if p.Flag == nil || p.Flag.Kind != "repeater-quiet" || p.Flag.Node == nil || p.Flag.Node.Name != "Keir Hill" ||
		!strings.Contains(p.Flag.Text, "Keir Hill dropped out of this path") || !strings.Contains(p.Flag.Text, "gone quiet") {
		t.Errorf("flag %+v, want Keir Hill gone quiet", p.Flag)
	}
}

func TestDataPathsAnswersSeveralPairsAtOnce(t *testing.T) {
	r := pathWatchRouter(t, true)
	body, _ := json.Marshal(DataPathsRequest{Window: "7d", Pairs: []DataPathPair{{From: pwGoyle, To: pwInsh}, {From: pwInsh, To: pwGoyle}}})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/data/paths", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var out DataPathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Paths) != 2 || out.Paths[0].Share != 0.25 || out.Paths[1].Days[len(out.Paths[1].Days)-1].Reached != 0 {
		t.Fatalf("paths %+v", out.Paths)
	}
}

func TestDataPathIsOffWithTheDataTabAndChecksItsInput(t *testing.T) {
	off := pathWatchRouter(t, false)
	w := httptest.NewRecorder()
	off.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path?from="+pwGoyle+"&to="+pwInsh, nil))
	if w.Code != 404 {
		t.Errorf("off: status %d, want 404", w.Code)
	}
	on := pathWatchRouter(t, true)
	for _, q := range []string{"?from=" + pwGoyle, "?from=zz&to=" + pwInsh, "?from=" + pwGoyle + "&to=" + pwGoyle} {
		w := httptest.NewRecorder()
		on.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path"+q, nil))
		if w.Code != 400 {
			t.Errorf("%s: status %d, want 400", q, w.Code)
		}
	}
}

func TestDashboardsTakeWatchPanelsAndRepeaterVariables(t *testing.T) {
	d := Dashboard{Schema: 1, ID: "links", Title: "Links we watch",
		Variables: []DashboardVariable{{Name: "from", Kind: "repeater"}, {Name: "to", Kind: "repeater"}},
		Panels: []DashboardPanel{
			{ID: "one", Title: "One link", Type: "watch", Query: DashboardQuery{From: "$from", To: "$to"}, Layout: DashboardLayout{W: 12, H: 6}},
			{ID: "all", Title: "Watches", Type: "watchlist", Options: &DashboardPanelOptions{Watches: []DashboardWatch{{Name: "North link", From: pwGoyle, To: pwInsh, Both: true}}}, Layout: DashboardLayout{Y: 6, W: 12, H: 5}},
		}}
	if err := validateDashboard(d); err != nil {
		t.Fatalf("valid watch dashboard refused: %v", err)
	}
	bad := d
	bad.Panels = []DashboardPanel{{ID: "one", Title: "One link", Type: "watch", Query: DashboardQuery{From: pwGoyle}, Layout: DashboardLayout{W: 6, H: 4}}}
	if validateDashboard(bad) == nil {
		t.Error("a watch without both repeaters was accepted")
	}
	bad.Panels = []DashboardPanel{{ID: "all", Title: "Watches", Type: "watchlist", Options: &DashboardPanelOptions{Watches: []DashboardWatch{{Name: "x", From: "nothex", To: pwInsh}}}, Layout: DashboardLayout{W: 6, H: 4}}}
	if validateDashboard(bad) == nil {
		t.Error("a watch with a bad key was accepted")
	}
}

// A built-in "Path" dashboard follows any two repeaters picked at the top,
// with or without areas.
func TestThePathDashboardIsBuiltIn(t *testing.T) {
	for _, places := range []bool{true, false} {
		var path *Dashboard
		for _, d := range builtInDashboards(places) {
			if d.ID == "path" {
				d := d
				path = &d
			}
		}
		if path == nil {
			t.Fatalf("places=%v: no built-in path dashboard", places)
		}
		if err := validateDashboard(*path); err != nil {
			t.Fatalf("places=%v: %v", places, err)
		}
		if len(path.Variables) != 2 || path.Variables[0].Kind != "repeater" || path.Panels[0].Type != "watch" || path.Panels[0].Query.From != "$from" {
			t.Errorf("places=%v: %+v", places, path)
		}
	}
}

// A packet counts as getting through when any observer saw it pass the first
// repeater and then the second, not only the observer with the longest path.
func TestDataPathCountsEveryObservation(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()
	for _, k := range []string{pwGoyle, pwInsh, pwOther, pwWard} {
		mustExec(t, db, `INSERT INTO nodes (public_key, name, role, first_seen) VALUES (?, 'n', 'repeater', '2026-01-01')`, k)
	}
	id := 0
	for d := 9; d >= 0; d-- {
		for k := 0; k < 12; k++ {
			id++
			at := now.Add(-time.Duration(d+1)*24*time.Hour + time.Duration(k)*time.Hour + time.Minute)
			mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type) VALUES (?, 'AA', ?, ?, 5)`, id, fmt.Sprintf("e%06d", id), at.Format("2006-01-02T15:04:05.000Z"))
			long, _ := json.Marshal([]string{pwGoyle, pwOther, pwWard, pwOther})
			short, _ := json.Marshal([]string{pwGoyle, pwInsh})
			mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, 1, '["A1","E5","C3","E5"]', ?, ?)`, id, at.Unix(), string(long))
			mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, 2, '["A1","D4"]', ?, ?)`, id, at.Unix(), string(short))
		}
	}
	r := pathRouterFor(t, db, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path?from="+pwGoyle+"&to="+pwInsh, nil))
	var p DataPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if p.Share != 1 {
		t.Errorf("share %v, want every packet counted (the shorter observation reached Insheswood)", p.Share)
	}
}

// History reaches back past what the packet store holds: a change ten days
// ago is still the flag when flags last a fortnight.
func TestDataPathRemembersAFortnight(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()
	for _, n := range [][2]string{{pwGoyle, "Goyle Hill"}, {pwKeir, "Keir Hill"}, {pwWard, "Ward Hill"}, {pwInsh, "Insheswood"}} {
		mustExec(t, db, `INSERT INTO nodes (public_key, name, role, first_seen) VALUES (?, ?, 'repeater', '2026-01-01')`, n[0], n[1])
	}
	id := 0
	for d := 25; d >= 0; d-- {
		for k := 0; k < 20; k++ {
			id++
			at := now.Add(-time.Duration(d+1)*24*time.Hour + time.Duration(k)*time.Hour + time.Minute)
			via := pwKeir
			if d <= 10 {
				via = pwWard
			}
			pj, _ := json.Marshal([]string{pwGoyle, via, pwInsh})
			mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type) VALUES (?, 'AA', ?, ?, 5)`, id, fmt.Sprintf("f%06d", id), at.Format("2006-01-02T15:04:05.000Z"))
			mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, 1, '["A1","B2","D4"]', ?, ?)`, id, at.Unix(), string(pj))
		}
	}
	r := pathRouterFor(t, db, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path?from="+pwGoyle+"&to="+pwInsh+"&flagDays=14", nil))
	var p DataPathResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if p.Flag == nil || p.Flag.Node == nil || p.Flag.Node.Name != "Keir Hill" {
		t.Fatalf("flag %+v, want Keir Hill dropping out ten days ago", p.Flag)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/data/path?from="+pwGoyle+"&to="+pwInsh+"&flagDays=99", nil))
	if w.Code != 400 {
		t.Errorf("flagDays=99: status %d, want 400", w.Code)
	}
}

// Observations of one packet read days later (a fill of old history) still
// make one packet, not one per observation.
func TestPathIndexCountsPacketsNotObservations(t *testing.T) {
	db := setupTestDB(t)
	at := time.Now().UTC().Add(-10 * 24 * time.Hour)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type) VALUES (1, 'AA', 'p1', ?, 5)`, at.Format("2006-01-02T15:04:05.000Z"))
	for i, rp := range []string{`["` + pwGoyle + `"]`, `["` + pwGoyle + `","` + pwInsh + `"]`, `["` + pwKeir + `"]`} {
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, ?, '[]', ?, ?)`, i+1, at.Unix(), rp)
	}
	idx := newPathIndex(db, 21)
	if err := idx.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.txs) != 1 || len(idx.recent) != 0 {
		t.Fatalf("packets %d, recent %d; want one packet, none recent", len(idx.txs), len(idx.recent))
	}
	n := 0
	eachPath(idx.txs[0].hops, func([]nodeID) { n++ })
	if n != 2 {
		t.Errorf("paths kept %d, want 2 (Goyle alone is a stretch of Goyle, Insheswood)", n)
	}
}
