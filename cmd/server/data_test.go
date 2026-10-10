package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Two areas with real-ish edges: SCO above 55°N, IOI west of 5.5°W below it.
const dataAreasGeoJSON = `{"type":"FeatureCollection","features":[
 {"type":"Feature","properties":{"code":"SCO","label":"Scotland","layer":"country","iata":["EDI"]},
  "geometry":{"type":"Polygon","coordinates":[[[-8,55.0],[-0.5,55.0],[-0.5,61],[-8,61],[-8,55.0]]]}},
 {"type":"Feature","properties":{"code":"IOI","label":"Island of Ireland","layer":"country","iata":["NOC"]},
  "geometry":{"type":"Polygon","coordinates":[[[-11,51],[-5.5,51],[-5.5,55.0],[-11,55.0],[-11,51]]]}}
]}`

type dataFixture struct {
	db  *DB
	cfg *Config
	svc *dataService
	now time.Time
}

func dataTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// newDataFixture seeds a mesh of two repeaters (Edinburgh, Belfast) and two
// observers (one in Edinburgh, one placed only by its airport code), with
// three packets: two in the last day and one older than the index reaches.
func newDataFixture(t *testing.T, enabled bool) *dataFixture {
	t.Helper()
	db := setupTestDB(t)
	mustExecDB(t, db, `ALTER TABLE transmissions ADD COLUMN scope_name TEXT`)
	db.hasScopeName = true
	now := time.Now().UTC().Truncate(time.Second)
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('AA11110000000000000000000000000000000000000000000000000000000000', 'Edinburgh Rpt', 'repeater', 55.95, -3.19)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('CC33330000000000000000000000000000000000000000000000000000000000', 'Belfast Rpt', 'repeater', 54.60, -5.93)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('0B5E000000000000000000000000000000000000000000000000000000000000', 'Edinburgh Obs', 'repeater', 55.94, -3.20)`,
		`INSERT INTO observers (id, name, iata) VALUES ('0B5E000000000000000000000000000000000000000000000000000000000000', 'Edinburgh Obs', 'EDI')`,
		`INSERT INTO observers (id, name, iata) VALUES ('OBSNOC', 'Knock', 'NOC')`,
	} {
		mustExecDB(t, db, q)
	}
	// p1: a Scottish channel message on #sco, heard at home and in Ireland.
	// p2: an Irish advert with its own position, heard in Edinburgh.
	// p3: ten days old, outside a 7-day index.
	tx := func(id int, hash string, at time.Time, typ, route int, scope any, decoded string) {
		t.Helper()
		if _, err := db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json, scope_name) VALUES (?, '00', ?, ?, ?, ?, ?, ?)`,
			id, hash, dataTS(at), route, typ, decoded, scope); err != nil {
			t.Fatal(err)
		}
	}
	obs := func(txID, observerIdx int, path, resolved string) {
		t.Helper()
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, path_json, resolved_path, timestamp) VALUES (?, ?, 'rx', ?, ?, ?)`,
			txID, observerIdx, path, resolved, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	tx(1, "00000000000000p1", now.Add(-2*time.Hour), 5, 0, "#sco", `{"channel":"Public"}`)
	obs(1, 1, `["AA11"]`, `["AA11110000000000000000000000000000000000000000000000000000000000"]`)
	obs(1, 2, `["AA11","CC33"]`, ``)
	tx(2, "00000000000000a2", now.Add(-time.Hour), 4, 1, nil, `{"pubKey":"FFEE","lat":54.6,"lon":-5.9,"flags":{"hasLocation":true}}`)
	obs(2, 1, `["CC33","AA11"]`, ``)
	tx(3, "00000000000000a3", now.Add(-10*24*time.Hour), 2, 1, nil, `{}`)
	obs(3, 1, `["AA11"]`, ``)

	dir := writeAreasFile(t, dataAreasGeoJSON)
	cfg := &Config{AreasFile: "areas.geojson", Data: &DataConfig{Enabled: enabled, HomeArea: "SCO"}}
	if _, err := LoadAreaMap(cfg, dir); err != nil {
		t.Fatal(err)
	}
	svc, err := newDataService(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if svc != nil {
		svc.now = func() time.Time { return now }
	}
	return &dataFixture{db: db, cfg: cfg, svc: svc, now: now}
}

func TestDataConfigDefaults(t *testing.T) {
	var c *Config = &Config{}
	if c.DataEnabled() || c.DataDays() != 7 {
		t.Errorf("no data block: enabled %v days %d, want off and 7", c.DataEnabled(), c.DataDays())
	}
	c.Data = &DataConfig{Enabled: true, Days: 90}
	if c.DataDays() != 30 {
		t.Errorf("days 90 = %d, want capped at 30", c.DataDays())
	}
	c.Data.Days = -1
	if c.DataDays() != 7 {
		t.Errorf("days -1 = %d, want 7", c.DataDays())
	}
}

func TestDataServiceIsOffUnlessEnabledWithAreas(t *testing.T) {
	f := newDataFixture(t, false)
	if f.svc != nil {
		t.Error("data.enabled false should give no service")
	}
	if _, err := newDataService(f.db, &Config{Data: &DataConfig{Enabled: true}}); err == nil {
		t.Error("enabled with no areas should refuse with an error")
	}
}

func TestDataServiceFillsFromSQLite(t *testing.T) {
	f := newDataFixture(t, true)
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	ix := f.svc.index()
	// p1 heard in SCO and IOI, p2 heard in SCO; p3 is too old.
	if ix.Len() != 3 {
		t.Fatalf("index rows = %d, want 3", ix.Len())
	}
	if c := ix.Covered(); c == 0 || c > f.now.Add(-7*24*time.Hour+time.Minute).Unix() {
		t.Errorf("covered from %v, want the full 7 days", time.Unix(c, 0))
	}
	res := f.svc.packets("7d", 0, 0, nil)
	byHeard := map[string]string{}
	for _, r := range res.Rows {
		byHeard[r.Hash+"@"+r.Heard] = r.Origin + "/" + r.Basis + "/" + r.Scope + "/" + r.Channel + "/" + r.How
	}
	want := map[string]string{
		"00000000000000p1@SCO": "SCO/first-relay/#sco/Public/relayed",
		"00000000000000p1@IOI": "SCO/first-relay/#sco/Public/relayed",
		"00000000000000a2@SCO": "IOI/advert/unscoped//relayed",
	}
	for k, v := range want {
		if byHeard[k] != v {
			t.Errorf("%s = %q, want %q", k, byHeard[k], v)
		}
	}
	// Relays are named from the node table.
	for _, r := range res.Rows {
		if r.Hash == "00000000000000p1" && r.Heard == "IOI" && r.HeardFrom != "Belfast Rpt" {
			t.Errorf("p1 in IOI heard from %q, want Belfast Rpt", r.HeardFrom)
		}
	}
}

func TestDataRefreshPicksUpLateObservations(t *testing.T) {
	f := newDataFixture(t, true)
	ctx := context.Background()
	if err := f.svc.fill(ctx); err != nil {
		t.Fatal(err)
	}
	// Knock hears p2 late.
	mustExecDB(t, f.db, `INSERT INTO observations (transmission_id, observer_idx, direction, path_json, timestamp) VALUES (2, 2, 'rx', '["CC33"]', 0)`)
	// A brand new packet arrives.
	if _, err := f.db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (4, '00', '00000000000000a4', ?, 1, 2, '{}')`, dataTS(f.now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	mustExecDB(t, f.db, `INSERT INTO observations (transmission_id, observer_idx, direction, path_json, timestamp) VALUES (4, 1, 'rx', '["AA11"]', 0)`)
	f.svc.refreshSpan = 2 * time.Hour
	if err := f.svc.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.svc.index().Len(); n != 5 {
		t.Errorf("rows after refresh = %d, want 5 (p2 now also heard in IOI, a4 new)", n)
	}
}

func dataRouter(f *dataFixture) *mux.Router {
	srv := &Server{db: f.db, cfg: f.cfg, data: f.svc}
	r := mux.NewRouter()
	r.HandleFunc("/api/data/config", srv.handleDataConfig).Methods("GET")
	r.HandleFunc("/api/data/summary", srv.handleDataSummary).Methods("GET")
	r.HandleFunc("/api/data/cube", srv.handleDataCube).Methods("GET")
	r.HandleFunc("/api/data/packets", srv.handleDataPackets).Methods("GET")
	return r
}

func getJSON(t *testing.T, r http.Handler, path string, into any) int {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if into != nil && w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(into); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return w.Code
}

func TestDataEndpointsWhenDisabled(t *testing.T) {
	f := newDataFixture(t, false)
	r := dataRouter(f)
	var cfg map[string]any
	if code := getJSON(t, r, "/api/data/config", &cfg); code != 200 || cfg["enabled"] != false {
		t.Errorf("config = %d %v, want 200 and enabled false", code, cfg)
	}
	for _, p := range []string{"/api/data/summary?window=24h", "/api/data/cube?window=24h", "/api/data/packets?window=24h"} {
		if code := getJSON(t, r, p, nil); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 while disabled", p, code)
		}
	}
}

func TestDataEndpoints(t *testing.T) {
	f := newDataFixture(t, true)
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := dataRouter(f)

	var cfg struct {
		Enabled bool   `json:"enabled"`
		Layer   string `json:"layer"`
		Home    string `json:"home"`
		Areas   []struct {
			Code, Label string
		} `json:"areas"`
		Windows []struct{ Key string } `json:"windows"`
		Groups  []struct{ Key string } `json:"groups"`
		Days    int                    `json:"days"`
		Rows    int                    `json:"rows"`
	}
	if code := getJSON(t, r, "/api/data/config", &cfg); code != 200 {
		t.Fatalf("config = %d", code)
	}
	if !cfg.Enabled || cfg.Layer != "country" || cfg.Home != "SCO" || cfg.Days != 7 || cfg.Rows != 3 {
		t.Errorf("config = %+v", cfg)
	}
	if len(cfg.Areas) != 2 || cfg.Areas[0].Code != "SCO" || cfg.Areas[0].Label != "Scotland" {
		t.Errorf("areas = %+v, want SCO then IOI with labels", cfg.Areas)
	}
	if len(cfg.Windows) != 2 || cfg.Windows[0].Key != "24h" || cfg.Windows[1].Key != "7d" {
		t.Errorf("windows = %+v, want 24h and 7d for a 7-day index", cfg.Windows)
	}
	if len(cfg.Groups) != 3 {
		t.Errorf("groups = %+v", cfg.Groups)
	}

	var cube struct {
		Rows    [][10]int `json:"rows"`
		Heard   []string  `json:"heard"`
		Origins []string  `json:"origins"`
	}
	if code := getJSON(t, r, "/api/data/cube?window=7d", &cube); code != 200 {
		t.Fatalf("cube = %d", code)
	}
	total := 0
	for _, x := range cube.Rows {
		total += x[9]
	}
	if total != 3 || len(cube.Heard) != 2 {
		t.Errorf("cube total %d heard %v, want 3 over SCO and IOI", total, cube.Heard)
	}
	if code := getJSON(t, r, "/api/data/cube?window=30d", nil); code != http.StatusBadRequest {
		t.Errorf("a window past the index = %d, want 400", code)
	}

	var sum struct {
		Packets     int `json:"packets"`
		CrossBorder int `json:"crossBorder"`
	}
	if code := getJSON(t, r, "/api/data/summary?window=24h", &sum); code != 200 || sum.Packets != 2 || sum.CrossBorder != 2 {
		t.Errorf("summary = %d %+v, want 2 packets, both crossing", code, sum)
	}

	var pk struct {
		Total int `json:"total"`
		Rows  []struct {
			Hash  string `json:"hash"`
			Heard string `json:"heard"`
		} `json:"rows"`
	}
	f1 := url.QueryEscape(`[{"d":"heard","v":["IOI"]}]`)
	if code := getJSON(t, r, "/api/data/packets?window=7d&f="+f1, &pk); code != 200 || pk.Total != 1 || pk.Rows[0].Hash != "00000000000000p1" {
		t.Errorf("packets heard in IOI = %d %+v", code, pk)
	}
	if code := getJSON(t, r, "/api/data/packets?window=7d&f=notjson", nil); code != http.StatusBadRequest {
		t.Errorf("bad filter = %d, want 400", code)
	}
	if code := getJSON(t, r, `/api/data/packets?window=7d&f=`+url.QueryEscape(`[{"d":"nosuch","v":["x"]}]`), nil); code != http.StatusBadRequest {
		t.Errorf("unknown dimension = %d, want 400", code)
	}
}

func TestDataCubeIsCachedAndBuiltOnce(t *testing.T) {
	f := newDataFixture(t, true)
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, err := f.svc.cubeJSON("7d")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := f.svc.cubeJSON("7d")
	if &a[0] != &b[0] {
		t.Error("a second request inside the cache time should reuse the first answer")
	}
}
