package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/netwatch"
)

const (
	nwKeir  = "aa00000000000000000000000000000000000000000000000000000000000001"
	nwNorth = "bb00000000000000000000000000000000000000000000000000000000000002"
	nwMid   = "cc00000000000000000000000000000000000000000000000000000000000003"
	nwSouth = "dd00000000000000000000000000000000000000000000000000000000000004"
	nwObs   = "0B5E000000000000000000000000000000000000000000000000000000000000"
)

// seedNetWatch writes seventeen days of traffic ending at now: Keir relays about 20
// packets an hour on the path north → keir → south and adverts every two
// hours, until it goes silent two days before now; a steady north → mid →
// south chain runs throughout.
func seedNetWatch(t *testing.T, db *DB, now time.Time) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('` + nwKeir + `', 'Keir Hill', 'repeater', 57.17, -2.30)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('` + nwNorth + `', 'Inverness', 'repeater', 57.48, -4.22)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('` + nwMid + `', 'Goyle Hill', 'repeater', 56.93, -2.52)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('` + nwSouth + `', 'Edinburgh', 'repeater', 55.95, -3.19)`,
		`INSERT INTO observers (id, name, iata) VALUES ('` + nwObs + `', 'Edinburgh Obs', 'EDI')`,
	} {
		mustExecDB(t, db, q)
	}
	tx, err := db.conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	insT, _ := tx.Prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type, from_pubkey, decoded_json) VALUES ('00', ?, ?, ?, ?, ?)`)
	insO, _ := tx.Prepare(`INSERT INTO observations (transmission_id, observer_idx, timestamp, resolved_path, snr) VALUES (?, 1, ?, ?, 4.5)`)
	n := 0
	add := func(at time.Time, typ int, from string, path []string) {
		n++
		dj := ""
		if typ == 4 {
			dj = fmt.Sprintf(`{"type":"ADVERT","timestamp":%d}`, at.Unix())
		}
		res, err := insT.Exec(fmt.Sprintf("%016x", n), at.UTC().Format("2006-01-02T15:04:05Z"), typ, from, dj)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		rp := ""
		if path != nil {
			b, _ := json.Marshal(path)
			rp = string(b)
		}
		if _, err := insO.Exec(id, at.Unix(), rp); err != nil {
			t.Fatal(err)
		}
	}
	silentFrom := now.Add(-48 * time.Hour).Truncate(time.Hour)
	for at := now.Add(-17 * 24 * time.Hour).Truncate(time.Hour); at.Before(now.Truncate(time.Hour)); at = at.Add(time.Hour) {
		for i := 0; i < 15; i++ {
			add(at.Add(time.Duration(i)*time.Minute), 5, "", []string{nwNorth, nwMid, nwSouth})
		}
		if at.Before(silentFrom) {
			for i := 0; i < 20; i++ {
				add(at.Add(time.Duration(i)*time.Minute), 5, "", []string{nwNorth, nwKeir, nwSouth})
			}
			if at.Hour()%2 == 0 {
				add(at.Add(30*time.Minute), 4, nwKeir, []string{})
			}
		}
		// Inverness adverts every hour; through Keir they reach Edinburgh,
		// once Keir is silent they stop at Goyle Hill.
		if at.Before(silentFrom) {
			add(at.Add(40*time.Minute), 4, nwNorth, []string{nwKeir, nwSouth})
		} else {
			add(at.Add(40*time.Minute), 4, nwNorth, []string{nwMid})
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func netWatchRouter(t *testing.T, enabled bool) (*mux.Router, time.Time) {
	t.Helper()
	db := setupTestDB(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(-time.Minute)
	seedNetWatch(t, db, now)
	cfg := &Config{NetWatch: &NetWatchConfig{Enabled: enabled, Corridors: []NetWatchCorridor{{
		Name: "Highlands ⇄ central belt",
		A:    NetWatchZone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3},
		B:    NetWatchZone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2},
	}}, Routes: []NetWatchRoute{{Name: "North link", From: nwNorth, To: nwSouth}, {Name: "North chat", From: nwNorth, To: nwSouth, Basis: "chat"}}}}
	srv := &Server{db: db, cfg: cfg}
	if svc := newNetWatchService(db, cfg); svc != nil {
		svc.now = func() time.Time { return now }
		if err := svc.catchUp(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.netwatch = svc
	}
	r := mux.NewRouter()
	r.HandleFunc("/api/netwatch", srv.handleNetWatch).Methods("GET")
	r.HandleFunc("/api/netwatch/node/{pubkey}", srv.handleNetWatchNode).Methods("GET")
	r.HandleFunc("/api/netwatch/probe", srv.handleNetWatchProbe).Methods("GET")
	r.HandleFunc("/api/netwatch/feed.atom", srv.handleNetWatchFeed).Methods("GET")
	return r, now
}

func getNW(t *testing.T, r http.Handler, path string, into any) int {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if into != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, w.Body.String())
		}
	}
	return w.Code
}

func TestNetWatchFlagsARepeaterThatWentQuiet(t *testing.T) {
	r, _ := netWatchRouter(t, true)
	var st NetWatchResponse
	if code := getNW(t, r, "/api/netwatch", &st); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !st.Ready {
		t.Error("ready should be true once the window is read")
	}
	if len(st.Open) != 1 || st.Open[0].Node != nwKeir || st.Open[0].Name != "Keir Hill" {
		t.Fatalf("open = %+v, want Keir Hill", st.Open)
	}
	inc := st.Open[0]
	if inc.Usual < 100 || inc.Now > inc.Usual/5 || inc.AdvertEveryH != 2 {
		t.Errorf("incident %+v", inc)
	}
	if inc.Kind != "quiet" || inc.Chance <= 0 || inc.Chance > 1e-4 || inc.Observers != 1 || inc.Repeats != 1 {
		t.Errorf("incident evidence %+v", inc)
	}
	if st.Feed.Down || st.Nightly == nil || st.Drift == nil {
		t.Errorf("feed %+v nightly %v drift %v", st.Feed, st.Nightly, st.Drift)
	}
	if len(st.Events) == 0 || st.Events[0].Kind != "quiet" || st.Events[0].Name != "Keir Hill" || st.Events[0].Text != "Keir Hill went quiet" {
		t.Errorf("timeline %+v, want Keir Hill going quiet first", st.Events)
	}
	if inc.Lat == nil || *inc.Lat != 57.17 {
		t.Errorf("incident has no position: %+v", inc)
	}
	if st.Judged < 3 {
		t.Errorf("judged %d repeaters, want the three busy ones", st.Judged)
	}
	if len(st.Observers) != 1 || st.Observers[0].Name != "Edinburgh Obs" || st.Observers[0].Quiet {
		t.Errorf("observers %+v", st.Observers)
	}
	if len(st.Corridors) != 1 || st.Corridors[0].UsualDay < 24*30 || st.Corridors[0].LastDay > st.Corridors[0].UsualDay {
		t.Errorf("corridor %+v", st.Corridors)
	}
	if d := st.Corridors[0].Directions; len(d) != 2 || d[0].From != "Highlands" || d[0].LastDay == 0 || d[1].LastDay != 0 {
		t.Errorf("corridor directions %+v, want all of it Highlands → central belt", d)
	}
	if r := st.Corridors[0].Directions[0].Reach; r == nil || r.Usual != 1 || r.Last != 0 || r.LastAdverts != 24 {
		t.Errorf("Highlands adverts reaching the central belt %+v, want none of 24 yesterday against all of them usually", r)
	} else if r.Verdict != "worse" || r.Since == nil || r.Cause == nil || r.Cause.Name != "Keir Hill" {
		t.Errorf("reach verdict %q since %v cause %+v, want worse since the drop, Keir Hill the cause", r.Verdict, r.Since, r.Cause)
	}
	if len(st.Routes) != 2 || st.Routes[0].FromName != "Inverness" || st.Routes[0].ToName != "Edinburgh" {
		t.Fatalf("routes %+v", st.Routes)
	}
	if r := st.Routes[0]; r.LastDay != 15*24 || r.UsualDay != 35*24 {
		t.Errorf("north link last day %d usual %d, want %d and %d (Keir carried 20 of 35 an hour)", r.LastDay, r.UsualDay, 15*24, 35*24)
	}
	if r := st.Routes[1].Reach; r == nil || r.Basis != "chat" || r.LastAdverts != 15*24 || r.Last != 1 {
		t.Errorf("north chat reach %+v, want every one of the 360 channel messages via Goyle Hill", r)
	}
}

func TestNetWatchNodeSeries(t *testing.T) {
	r, _ := netWatchRouter(t, true)
	var s NetWatchNodeResponse
	if code := getNW(t, r, "/api/netwatch/node/"+nwKeir, &s); code != 200 {
		t.Fatalf("status %d", code)
	}
	if s.AdvertEveryMin != 120 || len(s.AdvertDelivery) == 0 || s.Why.Observers != 1 || s.Why.Usual < 100 {
		t.Errorf("advert every %d min, delivery %v, why %+v", s.AdvertEveryMin, s.AdvertDelivery, s.Why)
	}
	if s.Name != "Keir Hill" || len(s.Relays) != len(s.Expected) || len(s.Relays) < 24*17 {
		t.Fatalf("series %q %d/%d", s.Name, len(s.Relays), len(s.Expected))
	}
	if last := s.Relays[len(s.Relays)-1]; last != 0 {
		t.Errorf("last hour relays %d, want 0", last)
	}
}

func TestNetWatchProbeShowsTheHopThatStopped(t *testing.T) {
	r, _ := netWatchRouter(t, true)
	var p NetWatchProbeResponse
	if code := getNW(t, r, "/api/netwatch/probe?from="+nwNorth+"&to="+nwSouth, &p); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(p.Route) != 3 || p.Route[1].Node != nwKeir || p.Route[1].Name != "Keir Hill" {
		t.Fatalf("route %+v, want Inverness → Keir Hill → Edinburgh (its usual way)", p.Route)
	}
	for _, h := range p.Hops {
		if !h.Quiet {
			t.Errorf("hop %s→%s should be quiet (now %d, usual %d)", h.FromName, h.ToName, h.Now, h.Usual)
		}
	}
	if code := getNW(t, r, "/api/netwatch/probe?from="+nwNorth, nil); code != 400 {
		t.Errorf("probe without both ends = %d, want 400", code)
	}
}

func TestNetWatchIsOffUnlessEnabled(t *testing.T) {
	r, _ := netWatchRouter(t, false)
	for _, p := range []string{"/api/netwatch", "/api/netwatch/node/" + nwKeir, "/api/netwatch/probe?from=a&to=b"} {
		if code := getNW(t, r, p, nil); code != 404 {
			t.Errorf("%s = %d, want 404 when off", p, code)
		}
	}
}

func TestNetWatchAtomFeedListsWhatHappened(t *testing.T) {
	r, _ := netWatchRouter(t, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/netwatch/feed.atom", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/atom+xml") {
		t.Fatalf("status %d type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if b := w.Body.String(); !strings.Contains(b, "<feed") || !strings.Contains(b, "<title>Keir Hill went quiet</title>") {
		t.Errorf("feed %s", b)
	}
}

// A 1-byte hop that another repeater nearby shares cannot be told apart, so
// it is not credited to the one it was resolved to.
func TestNetWatchIgnoresHopsItCannotTellApart(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(-time.Minute)
	twin := "aa11000000000000000000000000000000000000000000000000000000000099"
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen) VALUES ('`+nwKeir+`', 'Keir Hill', 'repeater', 57.17, -2.30, '`+now.Format(time.RFC3339)+`')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen) VALUES ('`+twin+`', 'Keir Twin', 'repeater', 57.30, -2.40, '`+now.Format(time.RFC3339)+`')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen) VALUES ('`+nwSouth+`', 'Edinburgh', 'repeater', 55.95, -3.19, '`+now.Format(time.RFC3339)+`')`)
	mustExecDB(t, db, `INSERT INTO observers (id, name, iata) VALUES ('`+nwObs+`', 'Edinburgh Obs', 'EDI')`)
	at := now.Add(-2 * time.Hour)
	for i, hops := range []string{`["AA","DD00"]`, `["AA00","DD00"]`} {
		mustExecDB(t, db, fmt.Sprintf(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES ('00', 'h%d', '%s', 5)`, i, at.Format("2006-01-02T15:04:05Z")))
		mustExecDB(t, db, fmt.Sprintf(`INSERT INTO observations (transmission_id, observer_idx, timestamp, resolved_path, path_json) VALUES (%d, 1, %d, '["%s","%s"]', '%s')`, i+1, at.Unix(), nwKeir, nwSouth, hops))
	}
	svc := newNetWatchService(db, &Config{NetWatch: &NetWatchConfig{Enabled: true}})
	svc.now = func() time.Time { return now }
	if err := svc.catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	ser := svc.watch.NodeSeries(nwKeir, now.Unix()/3600)
	total := 0
	for _, v := range ser.Relays {
		total += v
	}
	if total != 1 {
		t.Errorf("Keir Hill credited with %d relays, want 1 (the 2-byte hop; the 1-byte one could be its twin)", total)
	}
}

func TestNetWatchNotifiesNewAndClosedIncidentsButNotTheFirstFill(t *testing.T) {
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
	}))
	defer hook.Close()
	n := &netWatchNotifier{cfg: NetWatchNotify{Webhook: hook.URL, Format: "discord", BaseURL: "https://scope.example"}, client: hook.Client()}
	keir := NetWatchIncident{Node: nwKeir, Name: "Keir Hill", Kind: "quiet", Start: "2026-10-07T00:00:00Z", Flagged: "2026-10-07T05:00:00Z", Usual: 153, Now: 6, Chance: 1e-8, Affects: []string{"H and C: Highlands → Central belt"}}
	n.update(context.Background(), NetWatchResponse{Ready: true, Open: []NetWatchIncident{keir}})
	if len(got) != 0 {
		t.Fatalf("sent %v on the first fill", got)
	}
	tarbat := keir
	tarbat.Node, tarbat.Name, tarbat.Affects = nwNorth, "Tarbat RPT", nil
	n.update(context.Background(), NetWatchResponse{Ready: true, Open: []NetWatchIncident{keir, tarbat}})
	end := "2026-10-08T10:00:00Z"
	closed := keir
	closed.End = &end
	n.update(context.Background(), NetWatchResponse{Ready: true, Open: []NetWatchIncident{tarbat}, Recent: []NetWatchIncident{closed}})
	if len(got) != 2 || !strings.Contains(got[0], "Tarbat RPT went quiet") || !strings.Contains(got[0], `"content"`) ||
		!strings.Contains(got[1], "Keir Hill is back") || !strings.Contains(got[0], "https://scope.example/#/health") {
		t.Fatalf("sent %q", got)
	}
}

func TestNetWatchStepEventsNameAnOfflineObserver(t *testing.T) {
	svc := &netWatchService{nodes: map[string]nwNode{}, obsNames: map[string]string{nwObs: "AYR-Observer"}}
	e := svc.event(netwatch.Event{Kind: "step", Where: "Ireland gateway to Ayr", Before: 0.32, After: 0, ObserversDown: []string{nwObs}})
	if e.Text != "Ireland gateway to Ayr fell from 32% to 0% of adverts getting through, while AYR-Observer is offline" {
		t.Errorf("text %q", e.Text)
	}
}

// With tiers on, Keir Hill (about 20 relays an hour) is core: it is flagged
// as slowed within the core window, and the counts say how many are in each
// tier.
func TestNetWatchTiers(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(-time.Minute)
	seedNetWatch(t, db, now)
	cfg := &Config{NetWatch: &NetWatchConfig{Enabled: true, Tiers: &NetWatchTiers{CoreEveryMinutes: 10, CoreHours: 2, LocalSilenceHours: 3}}}
	svc := newNetWatchService(db, cfg)
	svc.now = func() time.Time { return now }
	if err := svc.catchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := svc.response()
	if st.Tiers == nil || st.Tiers.Core < 2 {
		t.Fatalf("tiers %+v, want the busy repeaters counted as core", st.Tiers)
	}
	var keir *NetWatchIncident
	for i := range st.Open {
		if st.Open[i].Node == nwKeir {
			keir = &st.Open[i]
		}
	}
	if keir == nil || keir.Tier != "core" || keir.WindowH == 0 {
		t.Fatalf("open %+v, want Keir Hill as a core incident", st.Open)
	}
	if e := svc.event(netwatch.Event{Kind: "slowed", Node: nwKeir}); e.Text != "Keir Hill slowed right down" {
		t.Errorf("slowed text %q", e.Text)
	}
	if e := svc.event(netwatch.Event{Kind: "unheard", Node: nwKeir}); e.Text != "Keir Hill has not been heard for longer than usual" {
		t.Errorf("unheard text %q", e.Text)
	}
}
