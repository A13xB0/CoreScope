package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/areamap"
)

// A rough Scotland on the "country" layer, in GeoJSON's [lon, lat] order.
const scotlandGeoJSON = `{"type":"FeatureCollection","features":[
 {"type":"Feature","properties":{"code":"SCO","label":"Scotland","layer":"country","iata":["EDI"]},
  "geometry":{"type":"Polygon","coordinates":[[[-7.5,54.6],[-1.5,54.6],[-1.5,60.9],[-7.5,60.9],[-7.5,54.6]]]}}]}`

func writeAreasFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "areas.geojson"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func belgium() map[string]AreaEntry {
	return map[string]AreaEntry{
		"BEL":      {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
		"_comment": {},
	}
}

func TestLoadAreaMapReadsConfigAreasAndTheFile(t *testing.T) {
	dir := writeAreasFile(t, scotlandGeoJSON)
	cfg := &Config{Areas: belgium(), AreasFile: "areas.geojson"}
	m, err := LoadAreaMap(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.areaMap != m {
		t.Error("the map must be kept on the config for the store and handlers")
	}
	// config.json polygons are [lat, lon]; they must land the right way round.
	if got, _ := m.Locate(areamap.DefaultLayer, 50.85, 4.35); got != "BEL" {
		t.Errorf("Brussels = %q, want BEL from config.json", got)
	}
	if got, _ := m.Locate("country", 55.95, -3.19); got != "SCO" {
		t.Errorf("Edinburgh = %q, want SCO from the file", got)
	}
	if got, _ := m.LocateIATA("country", "edi"); got != "SCO" {
		t.Errorf("EDI = %q, want SCO", got)
	}
}

func TestLoadAreaMapRefusesBadFiles(t *testing.T) {
	dup := `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"code":"BEL","label":"Again"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`
	cases := map[string]struct {
		body, file string
	}{
		"a code also in config.json": {dup, "areas.geojson"},
		"a missing file":             {scotlandGeoJSON, "nope.geojson"},
		"not GeoJSON":                {`{`, "areas.geojson"},
	}
	for name, c := range cases {
		dir := writeAreasFile(t, c.body)
		if _, err := LoadAreaMap(&Config{Areas: belgium(), AreasFile: c.file}, dir); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
	if m, err := LoadAreaMap(&Config{}, t.TempDir()); err != nil || len(m.Areas("")) != 0 {
		t.Errorf("no areas at all = %v, %v; want an empty map and no error", m, err)
	}
}

func TestHandleConfigAreasListsFileAreasWithTheirLayer(t *testing.T) {
	dir := writeAreasFile(t, scotlandGeoJSON)
	cfg := &Config{Areas: belgium(), AreasFile: "areas.geojson"}
	if _, err := LoadAreaMap(cfg, dir); err != nil {
		t.Fatal(err)
	}
	r := mux.NewRouter()
	srv := &Server{db: setupTestDBv2(t), cfg: cfg}
	r.HandleFunc("/api/config/areas", srv.handleConfigAreas).Methods("GET")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config/areas", nil))
	var got []map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	layers := map[string]string{}
	for _, a := range got {
		layers[a["key"]] = a["layer"]
	}
	if len(got) != 2 || layers["BEL"] != areamap.DefaultLayer || layers["SCO"] != "country" {
		t.Errorf("areas = %v, want BEL (region) and SCO (country)", got)
	}
}

func TestAreaFilterWorksForFileAreas(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('edinburgh', 55.95, -3.19)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('newcastle', 54.97, -1.61)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('dublin', 53.35, -6.26)`)
	dir := writeAreasFile(t, scotlandGeoJSON)
	cfg := &Config{AreasFile: "areas.geojson"}
	if _, err := LoadAreaMap(cfg, dir); err != nil {
		t.Fatal(err)
	}
	s := newTestStoreWithDB(t, db, cfg)
	got := s.resolveAreaNodes("SCO")
	if len(got) != 1 || !got["edinburgh"] {
		t.Errorf("nodes in SCO = %v, want only edinburgh", got)
	}
	// config.json areas keep their own path, unchanged.
	cfg.Areas = belgium()
	if got := s.resolveAreaNodes("BEL"); got != nil {
		t.Errorf("no Belgian nodes, got %v", got)
	}
}
