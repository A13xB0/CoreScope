package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
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
	insT, _ := tx.Prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type, from_pubkey) VALUES ('00', ?, ?, ?, ?)`)
	insO, _ := tx.Prepare(`INSERT INTO observations (transmission_id, observer_idx, timestamp, resolved_path) VALUES (?, 1, ?, ?)`)
	n := 0
	add := func(at time.Time, typ int, from string, path []string) {
		n++
		res, err := insT.Exec(fmt.Sprintf("%016x", n), at.UTC().Format("2006-01-02T15:04:05Z"), typ, from)
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
	}}, Routes: []NetWatchRoute{{Name: "North link", From: nwNorth, To: nwSouth}}}}
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
	if len(st.Open) != 1 || st.Open[0].Node != nwKeir || st.Open[0].Name != "Keir Hill" {
		t.Fatalf("open = %+v, want Keir Hill", st.Open)
	}
	inc := st.Open[0]
	if inc.Usual < 100 || inc.Now > inc.Usual/5 || inc.AdvertEveryH != 2 {
		t.Errorf("incident %+v", inc)
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
	if len(st.Routes) != 1 || st.Routes[0].FromName != "Inverness" || st.Routes[0].ToName != "Edinburgh" {
		t.Fatalf("routes %+v", st.Routes)
	}
	if r := st.Routes[0]; r.LastDay != 15*24 || r.UsualDay != 35*24 {
		t.Errorf("north link last day %d usual %d, want %d and %d (Keir carried 20 of 35 an hour)", r.LastDay, r.UsualDay, 15*24, 35*24)
	}
}

func TestNetWatchNodeSeries(t *testing.T) {
	r, _ := netWatchRouter(t, true)
	var s NetWatchNodeResponse
	if code := getNW(t, r, "/api/netwatch/node/"+nwKeir, &s); code != 200 {
		t.Fatalf("status %d", code)
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
