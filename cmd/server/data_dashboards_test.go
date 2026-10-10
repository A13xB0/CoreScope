package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodDashboards = `[
 {"schema":1,"id":"cross-border","title":"Cross-border traffic","window":"7d",
  "variables":[{"name":"home","kind":"area","default":"SCO"}],
  "panels":[
   {"id":"leaving","title":"Leaving $home","type":"stat",
    "query":{"filters":[{"d":"border","v":["crossed"]},{"d":"origin","v":["$home"]}]},
    "layout":{"x":0,"y":0,"w":3,"h":2}},
   {"id":"matrix","title":"Who hears whom","type":"matrix","query":{"rows":"origin","cols":"heard"},
    "layout":{"x":0,"y":2,"w":12,"h":5}},
   {"id":"note","title":"About","type":"text","options":{"text":"Counts by **start**."},"layout":{"x":3,"y":0,"w":9,"h":2}}
  ]}
]`

func writeDashboards(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dashboards.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDataDashboardsReadsTheFile(t *testing.T) {
	dir := writeDashboards(t, goodDashboards)
	cfg := &Config{Data: &DataConfig{Enabled: true, DashboardsFile: "dashboards.json", DefaultDashboard: "cross-border"}}
	ds, err := LoadDataDashboards(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Dashboards) < 2 || ds.Dashboards[0].Source != "built-in" {
		t.Fatalf("dashboards = %+v, want the built-in ones first", ds.Dashboards)
	}
	last := ds.Dashboards[len(ds.Dashboards)-1]
	if last.ID != "cross-border" || last.Source != "operator" || len(last.Panels) != 3 {
		t.Errorf("operator dashboard = %+v", last)
	}
	if ds.Default != "cross-border" {
		t.Errorf("default = %q, want cross-border", ds.Default)
	}
}

func TestLoadDataDashboardsWithoutAFileGivesTheBuiltIns(t *testing.T) {
	ds, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Dashboards) == 0 || ds.Default != ds.Dashboards[0].ID {
		t.Errorf("no file: %d dashboards, default %q; want the built-ins with the first as default", len(ds.Dashboards), ds.Default)
	}
	for _, d := range ds.Dashboards {
		if err := validateDashboard(d); err != nil {
			t.Errorf("built-in %q does not validate: %v", d.ID, err)
		}
	}
}

func TestLoadDataDashboardsRefusesBadFiles(t *testing.T) {
	panel := `{"id":"p","title":"P","type":"bar","query":{},"layout":{"x":0,"y":0,"w":6,"h":4}}`
	dash := func(extra string) string {
		return `[{"schema":1,"id":"d","title":"D",` + extra + `}]`
	}
	cases := map[string]string{
		"invalid JSON":          `[{`,
		"schema 2":              `[{"schema":2,"id":"d","title":"D","panels":[]}]`,
		"no title":              `[{"schema":1,"id":"d","panels":[]}]`,
		"bad id":                `[{"schema":1,"id":"Not OK","title":"D","panels":[]}]`,
		"unknown field":         dash(`"panels":[],"colour":"red"`),
		"unknown panel type":    dash(`"panels":[{"id":"p","title":"P","type":"pie","query":{},"layout":{"x":0,"y":0,"w":6,"h":4}}]`),
		"unknown dimension":     dash(`"panels":[{"id":"p","title":"P","type":"bar","query":{"filters":[{"d":"colour","v":["x"]}]},"layout":{"x":0,"y":0,"w":6,"h":4}}]`),
		"unknown split":         dash(`"panels":[{"id":"p","title":"P","type":"bar","query":{"split":"colour"},"layout":{"x":0,"y":0,"w":6,"h":4}}]`),
		"too wide":              dash(`"panels":[{"id":"p","title":"P","type":"bar","query":{},"layout":{"x":0,"y":0,"w":13,"h":4}}]`),
		"duplicate panel":       dash(`"panels":[` + panel + `,` + panel + `]`),
		"bad window":            dash(`"window":"1y","panels":[]`),
		"bad variable":          dash(`"variables":[{"name":"Home Area","kind":"area"}],"panels":[]`),
		"clashes with built-in": `[{"schema":1,"id":"overview","title":"Mine","panels":[]}]`,
		"duplicate dashboard":   `[{"schema":1,"id":"a","title":"A","panels":[]},{"schema":1,"id":"a","title":"B","panels":[]}]`,
	}
	// 25 distinct panels.
	var ps []string
	for i := 0; i < 25; i++ {
		ps = append(ps, strings.Replace(panel, `"id":"p"`, `"id":"p`+strings.Repeat("x", i+1)+`"`, 1))
	}
	cases["too many panels"] = dash(`"panels":[` + strings.Join(ps, ",") + `]`)
	for name, body := range cases {
		dir := writeDashboards(t, body)
		cfg := &Config{Data: &DataConfig{Enabled: true, DashboardsFile: "dashboards.json"}}
		if _, err := LoadDataDashboards(cfg, dir); err == nil {
			t.Errorf("%s: loaded, want refused", name)
		}
	}
	if _, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true, DashboardsFile: "missing.json"}}, t.TempDir()); err == nil {
		t.Error("a missing file should be refused")
	}
	dir := writeDashboards(t, goodDashboards)
	if _, err := LoadDataDashboards(&Config{Data: &DataConfig{Enabled: true, DashboardsFile: "dashboards.json", DefaultDashboard: "nope"}}, dir); err == nil {
		t.Error("a default naming no dashboard should be refused")
	}
}

func TestDataDashboardsEndpoint(t *testing.T) {
	f := newDataFixture(t, true)
	dir := writeDashboards(t, goodDashboards)
	f.cfg.Data.DashboardsFile = "dashboards.json"
	ds, err := LoadDataDashboards(f.cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.dashboards = ds
	if err := f.svc.fill(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := dataRouter(f)
	r.HandleFunc("/api/data/dashboards", (&Server{db: f.db, cfg: f.cfg, data: f.svc}).handleDataDashboards).Methods("GET")
	var got struct {
		Default    string `json:"default"`
		Home       string `json:"home"`
		Dashboards []struct {
			ID, Source string
			Panels     []struct{ Type string }
		} `json:"dashboards"`
	}
	if code := getJSON(t, r, "/api/data/dashboards", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got.Home != "SCO" || len(got.Dashboards) < 2 || got.Dashboards[len(got.Dashboards)-1].ID != "cross-border" {
		t.Errorf("dashboards response = %+v", got)
	}
	off := newDataFixture(t, false)
	ro := dataRouter(off)
	ro.HandleFunc("/api/data/dashboards", (&Server{db: off.db, cfg: off.cfg}).handleDataDashboards).Methods("GET")
	if code := getJSON(t, ro, "/api/data/dashboards", nil); code != http.StatusNotFound {
		t.Errorf("tab off: status %d, want 404", code)
	}
}
