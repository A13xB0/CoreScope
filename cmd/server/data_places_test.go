package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dataindex"
)

// placeDims are the dimensions that need areas to mean anything.
var placeDims = map[string]bool{"border": true, "origin": true, "heard": true, "through": true, "how": true}

// placelessFixture is the data fixture's database with the Data tab on and
// no areas at all: an operator who does not want to draw a GeoJSON file.
func placelessFixture(t *testing.T) *dataFixture {
	t.Helper()
	f := newDataFixture(t, false)
	f.cfg = &Config{Data: &DataConfig{Enabled: true}}
	svc, err := newDataService(f.db, f.cfg)
	if err != nil {
		t.Fatalf("enabled with no areas: %v, want the tab on without places", err)
	}
	if svc == nil {
		t.Fatal("enabled with no areas gave no service")
	}
	now := f.now
	svc.now = func() time.Time { return now }
	f.svc = svc
	return f
}

func TestDataTabRunsWithoutAreas(t *testing.T) {
	f := placelessFixture(t)
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One row per packet: nothing is placed, so every packet is heard in one
	// "unknown" area and started in it.
	if n := f.svc.index().Len(); n != 2 {
		t.Errorf("index rows = %d, want 2 (p1 and p2, each once)", n)
	}
	res := f.svc.packets("7d", 0, 0, nil)
	for _, r := range res.Rows {
		if r.Origin != dataindex.Unknown || r.Heard != dataindex.Unknown {
			t.Errorf("%s: origin %q heard %q, want both unknown with no areas", r.Hash, r.Origin, r.Heard)
		}
	}
}

func TestDataConfigWithoutAreasOffersNoPlaces(t *testing.T) {
	f := placelessFixture(t)
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Read as the browser does: a field this build lacks is simply absent.
	var cfg struct {
		Enabled    bool     `json:"enabled"`
		Places     *bool    `json:"places"`
		Home       string   `json:"home"`
		Areas      []any    `json:"areas"`
		Dimensions []string `json:"dimensions"`
	}
	if code := getJSON(t, dataRouter(f), "/api/data/config", &cfg); code != 200 {
		t.Fatalf("config = %d", code)
	}
	if !cfg.Enabled || cfg.Places == nil || *cfg.Places {
		t.Errorf("enabled %v places %v, want on, with places reported false", cfg.Enabled, cfg.Places)
	}
	if len(cfg.Areas) != 0 || cfg.Home != "" {
		t.Errorf("areas %v home %q, want none", cfg.Areas, cfg.Home)
	}
	want := []string{"kind", "type", "channel", "scope", "sclass", "routing"}
	if strings.Join(cfg.Dimensions, ",") != strings.Join(want, ",") {
		t.Errorf("dimensions = %v, want %v", cfg.Dimensions, want)
	}
	// With areas the place dimensions are offered and places is true.
	g := newDataFixture(t, true)
	full := cfg
	full.Places = nil
	getJSON(t, dataRouter(g), "/api/data/config", &full)
	if full.Places == nil || !*full.Places || len(full.Dimensions) != len(dataindex.Dimensions) {
		t.Errorf("with areas: places %v dimensions %v, want true and all of them", full.Places, full.Dimensions)
	}
}

func TestBuiltInDashboardsWithoutAreasLeavePlacesOut(t *testing.T) {
	ds, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Dashboards) == 0 {
		t.Fatal("no built-in dashboards without areas")
	}
	for _, d := range ds.Dashboards {
		if len(d.Panels) == 0 {
			t.Errorf("%s has no panels left", d.ID)
		}
		for _, p := range d.Panels {
			q := p.Query
			for _, dim := range []string{q.X, q.Split, q.Facet, q.Rows, q.Cols, q.Dim} {
				if placeDims[dim] {
					t.Errorf("%s/%s uses %q with no areas", d.ID, p.ID, dim)
				}
			}
			for _, fl := range q.Filters {
				if placeDims[fl.D] {
					t.Errorf("%s/%s filters on %q with no areas", d.ID, p.ID, fl.D)
				}
			}
		}
	}
}

func TestOperatorDashboardsNeedAreasForPlaces(t *testing.T) {
	cases := map[string]string{
		"a place dimension": `[{"schema":1,"id":"x","title":"X","panels":[{"id":"a","title":"A","type":"top","query":{"dim":"heard"},"layout":{"x":0,"y":0,"w":4,"h":4}}]}]`,
		"a place filter":    `[{"schema":1,"id":"x","title":"X","panels":[{"id":"a","title":"A","type":"stat","query":{"filters":[{"d":"border","n":0,"v":["crossed"]}]},"layout":{"x":0,"y":0,"w":4,"h":2}}]}]`,
		"an area variable":  `[{"schema":1,"id":"x","title":"X","variables":[{"name":"home","kind":"area"}],"panels":[]}]`,
	}
	for name, body := range cases {
		dir := writeDashboards(t, body)
		_, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true, DashboardsFile: "dashboards.json"}}, dir)
		if err == nil || !strings.Contains(err.Error(), "needs areas") {
			t.Errorf("%s with no areas: err %v, want one saying it needs areas", name, err)
		}
	}
	// The same scope-only dashboard loads fine.
	dir := writeDashboards(t, `[{"schema":1,"id":"x","title":"X","panels":[{"id":"a","title":"A","type":"top","query":{"dim":"scope"},"layout":{"x":0,"y":0,"w":4,"h":4}}]}]`)
	if _, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true, DashboardsFile: "dashboards.json"}}, dir); err != nil {
		t.Errorf("scope-only dashboard with no areas: %v", err)
	}
}

func TestHomeAreaNeedsAreas(t *testing.T) {
	f := newDataFixture(t, false)
	_, err := newDataService(f.db, &Config{Data: &DataConfig{Enabled: true, HomeArea: "SCO"}})
	if err == nil || !strings.Contains(err.Error(), "needs areas") {
		t.Errorf("homeArea with no areas: err %v, want one saying it needs areas", err)
	}
}
