package areamap

import (
	"errors"
	"testing"
)

// Two countries and a city: "Mainland" has a lake (a hole) and an island (a
// second polygon); "City" sits inside Mainland on another layer; "Neighbour"
// borders Mainland to the east.
const fixture = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "properties": {"code": "MLD", "label": "Mainland", "layer": "country", "iata": ["MLA", "mlb"]},
     "geometry": {"type": "MultiPolygon", "coordinates": [
       [[[0,0],[10,0],[10,10],[0,10],[0,0]], [[4,4],[6,4],[6,6],[4,6],[4,4]]],
       [[[-5,2],[-3,2],[-3,4],[-5,4],[-5,2]]]
     ]}},
    {"type": "Feature", "properties": {"code": "NBR", "label": "Neighbour", "layer": "country"},
     "geometry": {"type": "Polygon", "coordinates": [[[10,0],[20,0],[20,10],[10,10],[10,0]]]}},
    {"type": "Feature", "properties": {"code": "CTY", "label": "City"},
     "geometry": {"type": "Polygon", "coordinates": [[[1,1],[3,1],[3,3],[1,3],[1,1]]]}}
  ]
}`

func load(t *testing.T) *Map {
	t.Helper()
	areas, err := ParseGeoJSON([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(areas)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseGeoJSONReadsEveryFeature(t *testing.T) {
	areas, err := ParseGeoJSON([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 3 {
		t.Fatalf("areas = %d, want 3", len(areas))
	}
	if a := areas[0]; a.Code != "MLD" || a.Label != "Mainland" || a.Layer != "country" || len(a.Polygons) != 2 || len(a.Polygons[0]) != 2 {
		t.Errorf("Mainland = %+v, want two parts, the first with a hole", a)
	}
	if a := areas[2]; a.Layer != DefaultLayer {
		t.Errorf("an area naming no layer is in %q, want %q", a.Layer, DefaultLayer)
	}
}

func TestLocateUsesLonLatOrderHolesAndIslands(t *testing.T) {
	m := load(t)
	cases := []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"inside the mainland", 8, 2, "MLD"},
		{"on the island", 3, -4, "MLD"},
		{"in the lake", 5, 5, ""},
		{"in the neighbour", 5, 15, "NBR"},
		{"at sea", 50, 50, ""},
		{"lat and lon swapped would land in the neighbour", 2, 15, "NBR"},
		{"unset GPS", 0, 0, ""},
	}
	for _, c := range cases {
		got, ok := m.Locate("country", c.lat, c.lon)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s (%v, %v) = %q %v, want %q", c.name, c.lat, c.lon, got, ok, c.want)
		}
	}
}

func TestLocateStaysInsideItsLayer(t *testing.T) {
	m := load(t)
	if got, _ := m.Locate("country", 2, 2); got != "MLD" {
		t.Errorf("country layer at the city = %q, want MLD", got)
	}
	if got, _ := m.Locate(DefaultLayer, 2, 2); got != "CTY" {
		t.Errorf("region layer at the city = %q, want CTY", got)
	}
	if got, ok := m.Locate("no-such-layer", 2, 2); ok {
		t.Errorf("an unknown layer placed a point in %q", got)
	}
}

func TestSmallestAreaWinsWhereALayerOverlaps(t *testing.T) {
	big := Area{Code: "BIG", Label: "Big", Layer: "x", Polygons: [][][][2]float64{{{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}}}
	small := Area{Code: "SML", Label: "Small", Layer: "x", Polygons: [][][][2]float64{{{{2, 2}, {4, 2}, {4, 4}, {2, 4}}}}}
	m, err := New([]Area{big, small})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Locate("x", 3, 3); got != "SML" {
		t.Errorf("overlap = %q, want the smaller area SML", got)
	}
	if got, _ := m.Locate("x", 8, 8); got != "BIG" {
		t.Errorf("outside the small area = %q, want BIG", got)
	}
}

func TestLayersAreasAndIATA(t *testing.T) {
	m := load(t)
	if l := m.Layers(); len(l) != 2 || l[0] != "country" || l[1] != DefaultLayer {
		t.Errorf("layers = %v, want [country region]", l)
	}
	if a := m.Areas("country"); len(a) != 2 || a[0].Code != "MLD" || a[1].Label != "Neighbour" {
		t.Errorf("country areas = %+v", a)
	}
	if a := m.Areas(""); len(a) != 3 {
		t.Errorf("all areas = %d, want 3", len(a))
	}
	if got, ok := m.LocateIATA("country", "MLB"); !ok || got != "MLD" {
		t.Errorf("IATA MLB = %q %v, want MLD (case-insensitive)", got, ok)
	}
	if _, ok := m.LocateIATA(DefaultLayer, "MLA"); ok {
		t.Error("an IATA code belongs to its own layer only")
	}
	if !m.Has("CTY") || m.Has("NOPE") {
		t.Error("Has must know every code and only those")
	}
	if !m.Contains("MLD", 3, -4) || m.Contains("MLD", 5, 5) || m.Contains("NOPE", 1, 1) {
		t.Error("Contains disagrees with Locate")
	}
}

func TestBadInputIsRefused(t *testing.T) {
	cases := map[string]string{
		"not json":         `{`,
		"not a collection": `{"type":"Feature"}`,
		"no code":          `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"label":"x"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`,
		"a point":          `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"code":"P","label":"x"},"geometry":{"type":"Point","coordinates":[0,0]}}]}`,
		"a two-point ring": `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"code":"P","label":"x"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0]]]}}]}`,
	}
	for name, in := range cases {
		if _, err := ParseGeoJSON([]byte(in)); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
	dup := []Area{
		{Code: "A", Label: "One", Layer: "x", Polygons: [][][][2]float64{{{{0, 0}, {1, 0}, {1, 1}}}}},
		{Code: "A", Label: "Two", Layer: "y", Polygons: [][][][2]float64{{{{0, 0}, {1, 0}, {1, 1}}}}},
	}
	if _, err := New(dup); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate code across layers: err = %v, want ErrDuplicate", err)
	}
}

// A realistic boundary: six areas of 2,000 vertices each.
func BenchmarkLocate(b *testing.B) {
	var areas []Area
	for a := 0; a < 6; a++ {
		ring := make([][2]float64, 0, 2000)
		for i := 0; i < 2000; i++ {
			// a jagged square, each area offset east of the last
			t := float64(i) / 2000
			var x, y float64
			switch {
			case t < .25:
				x, y = t*4, 0
			case t < .5:
				x, y = 1, (t-.25)*4
			case t < .75:
				x, y = 1-(t-.5)*4, 1
			default:
				x, y = 0, 1-(t-.75)*4
			}
			if i%2 == 1 {
				x, y = x*0.999+0.0005, y*0.999+0.0005
			}
			ring = append(ring, [2]float64{float64(a) + x, y})
		}
		areas = append(areas, Area{Code: string(rune('A' + a)), Label: "x", Layer: "x", Polygons: [][][][2]float64{{ring}}})
	}
	m, err := New(areas)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Locate("x", 0.5, float64(i%6)+0.5)
	}
}
