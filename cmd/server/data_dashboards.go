package main

// Data tab dashboards: a dashboard is a JSON document of panels, each a
// query over the Data tab's cube and a way to draw it. CoreScope ships a
// couple of generic ones; an operator can offer more from a file
// (data.dashboardsFile); visitors build and export their own in the browser.
// The server only validates and serves them: it stores nothing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/meshcore-analyzer/dataindex"
)

// DashboardVariable is a value chosen on the page and written into panels as
// $name: an area, a scope, a channel or free text.
type DashboardVariable struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Default string `json:"default,omitempty"`
}

// DashboardQuery is what a panel counts. Fields a panel type does not use
// are left empty.
type DashboardQuery struct {
	Window  string             `json:"window,omitempty"`
	Filters []dataindex.Filter `json:"filters,omitempty"`
	X       string             `json:"x,omitempty"`
	Split   string             `json:"split,omitempty"`
	Facet   string             `json:"facet,omitempty"`
	Mode    string             `json:"mode,omitempty"`
	Rollup  string             `json:"rollup,omitempty"`
	Pick    []string           `json:"pick,omitempty"`
	Rows    string             `json:"rows,omitempty"`
	Cols    string             `json:"cols,omitempty"`
	Dim     string             `json:"dim,omitempty"`
	Top     int                `json:"top,omitempty"`
	// From and To are the two repeaters a watch panel follows: public keys,
	// or $variables of the repeater kind.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Basis is "adverts" to count only adverts on a watch (steadier), else all.
	Basis string `json:"basis,omitempty"`
}

// DashboardWatch is one pair a watch list follows.
type DashboardWatch struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
	Both bool   `json:"both,omitempty"` // show the way back too
}

// DashboardPanelOptions holds the settings only some panel types have.
type DashboardPanelOptions struct {
	Text    string           `json:"text,omitempty"`
	Watches []DashboardWatch `json:"watches,omitempty"` // a watchlist's pairs
}

// DashboardLayout is a panel's place on the 12-column grid.
type DashboardLayout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// DashboardPanel is one panel.
type DashboardPanel struct {
	ID      string                 `json:"id"`
	Title   string                 `json:"title"`
	Type    string                 `json:"type"`
	Query   DashboardQuery         `json:"query"`
	Options *DashboardPanelOptions `json:"options,omitempty"`
	Layout  DashboardLayout        `json:"layout"`
}

// Dashboard is one dashboard document, schema 1.
type Dashboard struct {
	Schema      int                 `json:"schema"`
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Window      string              `json:"window,omitempty"`
	Variables   []DashboardVariable `json:"variables,omitempty"`
	Panels      []DashboardPanel    `json:"panels"`
	// Source is set by the server: "built-in" or "operator". Never read
	// from a file.
	Source string `json:"source,omitempty"`
}

// DataDashboards is what GET /api/data/dashboards serves.
type DataDashboards struct {
	Default    string      `json:"default"`
	Home       string      `json:"home,omitempty"`
	Dashboards []Dashboard `json:"dashboards"`
}

// Limits, so a mistake in a file cannot put thousands of panels on a page.
const (
	maxDashboards          = 50
	maxDashboardPanels     = 24
	maxDashboardsFileBytes = 1 << 20
	maxDashboardTextBytes  = 4000
)

var (
	dashboardIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	dashboardVarRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	panelTypes     = map[string]bool{"bar": true, "line": true, "area": true, "matrix": true, "stat": true, "table": true, "top": true, "text": true, "watch": true, "watchlist": true}
	variableKinds  = map[string]bool{"area": true, "scope": true, "channel": true, "text": true, "repeater": true}
	watchEndRe     = regexp.MustCompile(`^([0-9a-f]{64}|\$[a-z][a-z0-9_]{0,31})$`)
	dashWindows    = map[string]bool{"": true, "24h": true, "7d": true, "30d": true}
	rollups        = map[string]bool{"": true, "10m": true, "30m": true, "1h": true, "6h": true, "1d": true, "1w": true}
)

func isDimension(d string) bool {
	for _, k := range dataindex.Dimensions {
		if k == d {
			return true
		}
	}
	return false
}

// optionalDim accepts "" or a known dimension; extra lists other words
// allowed in that field (the X axis also takes "time").
func optionalDim(field, v string, extra ...string) error {
	if v == "" || isDimension(v) {
		return nil
	}
	for _, e := range extra {
		if v == e {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not a dimension", field, v)
}

func validatePanel(p DashboardPanel) error {
	if !dashboardIDRe.MatchString(p.ID) {
		return fmt.Errorf("panel id %q: use lower-case letters, digits and dashes", p.ID)
	}
	if p.Title == "" || len(p.Title) > 120 {
		return fmt.Errorf("panel %q needs a title of at most 120 characters", p.ID)
	}
	if !panelTypes[p.Type] {
		return fmt.Errorf("panel %q: unknown type %q", p.ID, p.Type)
	}
	q := p.Query
	if !dashWindows[q.Window] || !rollups[q.Rollup] || (q.Mode != "" && q.Mode != "count" && q.Mode != "share") {
		return fmt.Errorf("panel %q: window, rollup or mode is not one the tab knows", p.ID)
	}
	for _, f := range q.Filters {
		if !isDimension(f.D) {
			return fmt.Errorf("panel %q: filter dimension %q is unknown", p.ID, f.D)
		}
	}
	for _, chk := range []error{optionalDim("x", q.X, "time"), optionalDim("split", q.Split), optionalDim("facet", q.Facet), optionalDim("rows", q.Rows), optionalDim("cols", q.Cols), optionalDim("dim", q.Dim)} {
		if chk != nil {
			return fmt.Errorf("panel %q: %w", p.ID, chk)
		}
	}
	if q.Top < 0 || q.Top > 50 {
		return fmt.Errorf("panel %q: top must be 1 to 50", p.ID)
	}
	if p.Options != nil && len(p.Options.Text) > maxDashboardTextBytes {
		return fmt.Errorf("panel %q: text is longer than %d bytes", p.ID, maxDashboardTextBytes)
	}
	if q.Basis != "" && q.Basis != "all" && q.Basis != "adverts" {
		return fmt.Errorf("panel %q: basis must be all or adverts", p.ID)
	}
	switch p.Type {
	case "watch":
		if !watchEndRe.MatchString(q.From) || !watchEndRe.MatchString(q.To) || q.From == q.To {
			return fmt.Errorf("panel %q: a watch needs two different repeaters, by public key or $variable", p.ID)
		}
	case "watchlist":
		if p.Options == nil || len(p.Options.Watches) == 0 || len(p.Options.Watches) > pathMaxPairs {
			return fmt.Errorf("panel %q: a watch list needs 1 to %d watches", p.ID, pathMaxPairs)
		}
		for _, wt := range p.Options.Watches {
			if wt.Name == "" || len(wt.Name) > 80 || !pubkeyRe.MatchString(wt.From) || !pubkeyRe.MatchString(wt.To) || wt.From == wt.To {
				return fmt.Errorf("panel %q: watch %q needs a name of at most 80 characters and two different repeater public keys", p.ID, wt.Name)
			}
		}
	}
	l := p.Layout
	if l.W < 1 || l.W > 12 || l.H < 1 || l.H > 12 || l.X < 0 || l.X+l.W > 12 || l.Y < 0 {
		return fmt.Errorf("panel %q: layout must fit the 12-column grid", p.ID)
	}
	return nil
}

// placeUse names the first thing in d that needs areas: an area variable or a
// place dimension in any panel. "" when d works without areas.
func placeUse(d Dashboard) string {
	for _, v := range d.Variables {
		if v.Kind == "area" {
			return fmt.Sprintf("variable %q is an area", v.Name)
		}
	}
	for _, p := range d.Panels {
		q := p.Query
		for _, dim := range []string{q.X, q.Split, q.Facet, q.Rows, q.Cols, q.Dim} {
			if dataindex.PlaceDimensions[dim] {
				return fmt.Sprintf("panel %q uses %q", p.ID, dim)
			}
		}
		for _, f := range q.Filters {
			if dataindex.PlaceDimensions[f.D] {
				return fmt.Sprintf("panel %q filters on %q", p.ID, f.D)
			}
		}
	}
	return ""
}

func validateDashboard(d Dashboard) error {
	if d.Schema != 1 {
		return fmt.Errorf("dashboard %q uses schema %d; this CoreScope reads schema 1", d.ID, d.Schema)
	}
	if !dashboardIDRe.MatchString(d.ID) {
		return fmt.Errorf("dashboard id %q: use lower-case letters, digits and dashes", d.ID)
	}
	if d.Title == "" || len(d.Title) > 120 {
		return fmt.Errorf("dashboard %q needs a title of at most 120 characters", d.ID)
	}
	if !dashWindows[d.Window] {
		return fmt.Errorf("dashboard %q: window %q is not 24h, 7d or 30d", d.ID, d.Window)
	}
	if len(d.Panels) > maxDashboardPanels {
		return fmt.Errorf("dashboard %q has %d panels; at most %d", d.ID, len(d.Panels), maxDashboardPanels)
	}
	vars := map[string]bool{}
	for _, v := range d.Variables {
		if !dashboardVarRe.MatchString(v.Name) || !variableKinds[v.Kind] || vars[v.Name] {
			return fmt.Errorf("dashboard %q: variable %q needs a unique lower-case name and a kind of area, scope, channel, repeater or text", d.ID, v.Name)
		}
		vars[v.Name] = true
	}
	seen := map[string]bool{}
	for _, p := range d.Panels {
		if err := validatePanel(p); err != nil {
			return fmt.Errorf("dashboard %q: %w", d.ID, err)
		}
		if seen[p.ID] {
			return fmt.Errorf("dashboard %q: panel id %q is used twice", d.ID, p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// LoadDataDashboards returns the built-in dashboards followed by the
// operator's, validated. A bad file is an error, so startup stops with the
// reason rather than serving half a set.
func LoadDataDashboards(cfg *Config, configDir string) (*DataDashboards, error) {
	out := &DataDashboards{}
	ids := map[string]bool{}
	places := cfg.hasAreas()
	for _, d := range builtInDashboards(places) {
		d.Source = "built-in"
		out.Dashboards = append(out.Dashboards, d)
		ids[d.ID] = true
	}
	dc := cfg.Data
	if dc != nil {
		out.Home = dc.HomeArea
	}
	if dc != nil && dc.DashboardsFile != "" {
		path := dc.DashboardsFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("data.dashboardsFile: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxDashboardsFileBytes+1))
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("data.dashboardsFile: %w", err)
		}
		if len(raw) > maxDashboardsFileBytes {
			return nil, fmt.Errorf("data.dashboardsFile is larger than %d bytes", maxDashboardsFileBytes)
		}
		var list []Dashboard
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&list); err != nil {
			return nil, fmt.Errorf("data.dashboardsFile %s: %w", path, err)
		}
		if len(list) > maxDashboards {
			return nil, fmt.Errorf("data.dashboardsFile has %d dashboards; at most %d", len(list), maxDashboards)
		}
		for _, d := range list {
			if err := validateDashboard(d); err != nil {
				return nil, fmt.Errorf("data.dashboardsFile: %w", err)
			}
			if use := placeUse(d); use != "" && !places {
				return nil, fmt.Errorf("data.dashboardsFile: dashboard %q: %s, which needs areas: set areasFile or areas in config.json, or drop it", d.ID, use)
			}
			if ids[d.ID] {
				return nil, fmt.Errorf("data.dashboardsFile: dashboard id %q is already used", d.ID)
			}
			ids[d.ID] = true
			d.Source = "operator"
			out.Dashboards = append(out.Dashboards, d)
		}
	}
	out.Default = out.Dashboards[0].ID
	if dc != nil && dc.DefaultDashboard != "" {
		if !ids[dc.DefaultDashboard] {
			return nil, fmt.Errorf("data.defaultDashboard %q is not a dashboard", dc.DefaultDashboard)
		}
		out.Default = dc.DefaultDashboard
	}
	return out, nil
}

// builtInDashboards are generic: they need an areas file at most. Without
// areas (places false) they keep to kind, type, channel, scope and routing.
// The last, Path, follows any two repeaters picked at the top.
func builtInDashboards(places bool) []Dashboard {
	return append(builtInCubeDashboards(places), Dashboard{Schema: 1, ID: "path", Title: "Path between two repeaters", Window: "7d",
		Description: "How much of what passes the first repeater gets to the second, the routes it takes and when they change. Pick the two at the top.",
		Variables:   []DashboardVariable{{Name: "from", Kind: "repeater", Label: "From"}, {Name: "to", Kind: "repeater", Label: "To"}},
		Panels:      []DashboardPanel{{ID: "watch", Title: "$from to $to", Type: "watch", Query: DashboardQuery{From: "$from", To: "$to"}, Layout: DashboardLayout{W: 12, H: 8}}}})
}

func builtInCubeDashboards(places bool) []Dashboard {
	lay := func(x, y, w, h int) DashboardLayout { return DashboardLayout{X: x, Y: y, W: w, H: h} }
	notChannel := dataindex.Filter{D: "channel", N: 1, V: []string{""}}
	adverts := dataindex.Filter{D: "kind", V: []string{"adverts"}}
	if !places {
		return []Dashboard{
			{Schema: 1, ID: "overview", Title: "Overview", Window: "24h",
				Description: "Every packet heard, by kind, scope and channel.",
				Panels: []DashboardPanel{
					{ID: "packets", Title: "Packets heard", Type: "stat", Layout: lay(0, 0, 4, 2)},
					{ID: "adverts", Title: "Flood adverts", Type: "stat", Query: DashboardQuery{Filters: []dataindex.Filter{adverts}}, Layout: lay(4, 0, 4, 2)},
					{ID: "messages", Title: "Messages", Type: "stat", Query: DashboardQuery{Filters: []dataindex.Filter{{D: "kind", V: []string{"messages"}}}}, Layout: lay(8, 0, 4, 2)},
					{ID: "by-kind", Title: "Packets over time by kind", Type: "area", Query: DashboardQuery{X: "time", Split: "kind"}, Layout: lay(0, 2, 8, 4)},
					{ID: "top-scopes", Title: "Busiest scopes", Type: "top", Query: DashboardQuery{Dim: "scope", Top: 8}, Layout: lay(8, 2, 4, 4)},
					{ID: "top-channels", Title: "Busiest channels", Type: "top", Query: DashboardQuery{Dim: "channel", Top: 8, Filters: []dataindex.Filter{notChannel}}, Layout: lay(0, 6, 5, 5)},
					{ID: "types", Title: "Packet types by routing", Type: "table", Query: DashboardQuery{X: "type", Split: "routing"}, Layout: lay(5, 6, 7, 5)},
				}},
			{Schema: 1, ID: "scopes", Title: "Scopes and channels", Window: "7d",
				Description: "Which region scopes and channels carry traffic, and what kind.",
				Panels: []DashboardPanel{
					{ID: "scope-time", Title: "Packets by scope", Type: "bar", Query: DashboardQuery{X: "time", Split: "scope", Rollup: "6h"}, Layout: lay(0, 0, 8, 4)},
					{ID: "scope-kinds", Title: "Scope kinds", Type: "top", Query: DashboardQuery{Dim: "sclass", Top: 4}, Layout: lay(8, 0, 4, 4)},
					{ID: "scope-kind", Title: "Scope by packet kind", Type: "matrix", Query: DashboardQuery{Rows: "scope", Cols: "kind"}, Layout: lay(0, 4, 7, 5)},
					{ID: "channel-time", Title: "Channel traffic", Type: "line", Query: DashboardQuery{X: "time", Split: "channel", Rollup: "6h", Filters: []dataindex.Filter{notChannel}}, Layout: lay(7, 4, 5, 5)},
				}},
		}
	}
	crossed := dataindex.Filter{D: "border", V: []string{"crossed"}}
	return []Dashboard{
		{Schema: 1, ID: "overview", Title: "Overview", Window: "24h",
			Description: "Every packet heard, by kind, by the area it started in and the areas that heard it.",
			Panels: []DashboardPanel{
				{ID: "packets", Title: "Packets heard", Type: "stat", Layout: lay(0, 0, 3, 2)},
				{ID: "crossed", Title: "Heard outside their start", Type: "stat", Query: DashboardQuery{Filters: []dataindex.Filter{crossed}}, Layout: lay(3, 0, 3, 2)},
				{ID: "adverts", Title: "Flood adverts", Type: "stat", Query: DashboardQuery{Filters: []dataindex.Filter{adverts}}, Layout: lay(6, 0, 3, 2)},
				{ID: "unknown", Title: "Start not known", Type: "stat", Query: DashboardQuery{Filters: []dataindex.Filter{{D: "border", V: []string{"unknown"}}}}, Layout: lay(9, 0, 3, 2)},
				{ID: "by-kind", Title: "Packets over time by kind", Type: "area", Query: DashboardQuery{X: "time", Split: "kind"}, Layout: lay(0, 2, 8, 4)},
				{ID: "top-scopes", Title: "Busiest scopes", Type: "top", Query: DashboardQuery{Dim: "scope", Top: 8}, Layout: lay(8, 2, 4, 4)},
				{ID: "matrix", Title: "Who hears whom", Type: "matrix", Query: DashboardQuery{Rows: "origin", Cols: "heard"}, Layout: lay(0, 6, 8, 5)},
				{ID: "top-channels", Title: "Busiest channels", Type: "top", Query: DashboardQuery{Dim: "channel", Top: 8, Filters: []dataindex.Filter{notChannel}}, Layout: lay(8, 6, 4, 5)},
			}},
		{Schema: 1, ID: "scopes", Title: "Scopes and channels", Window: "7d",
			Description: "Which region scopes and channels carry traffic, and where they are heard.",
			Panels: []DashboardPanel{
				{ID: "scope-time", Title: "Packets by scope", Type: "bar", Query: DashboardQuery{X: "time", Split: "scope", Rollup: "6h"}, Layout: lay(0, 0, 8, 4)},
				{ID: "scope-kinds", Title: "Scope kinds", Type: "top", Query: DashboardQuery{Dim: "sclass", Top: 4}, Layout: lay(8, 0, 4, 4)},
				{ID: "scope-heard", Title: "Scope by receiving area", Type: "matrix", Query: DashboardQuery{Rows: "scope", Cols: "heard"}, Layout: lay(0, 4, 7, 5)},
				{ID: "channel-time", Title: "Channel traffic", Type: "line", Query: DashboardQuery{X: "time", Split: "channel", Rollup: "6h", Filters: []dataindex.Filter{notChannel}}, Layout: lay(7, 4, 5, 5)},
			}},
	}
}

// GET /api/data/dashboards
func (s *Server) handleDataDashboards(w http.ResponseWriter, r *http.Request) {
	if s.data == nil || s.data.dashboards == nil {
		http.Error(w, "the Data tab is not enabled", http.StatusNotFound)
		return
	}
	writeJSON(w, s.data.dashboards)
}
