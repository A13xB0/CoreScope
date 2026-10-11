package main

// The Data tab's server side: a compact index of every packet of the last
// data.days days (internal/dataindex), each attributed to the area it started
// in and the areas that heard it (internal/origin), placed by the areas from
// config.json and areasFile (internal/areamap).
//
// The index reads SQLite directly, read-only, the way the packet store's own
// loader does: a background fill at startup, newest first, then every two
// minutes the last half hour is re-derived, because observations keep
// arriving after a packet is first seen. Node positions are re-read every
// half hour and the whole index is rebuilt once a day so moved nodes count
// where they stand now.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/areamap"
	"github.com/meshcore-analyzer/dataindex"
	"github.com/meshcore-analyzer/origin"
	"golang.org/x/sync/singleflight"
)

// DataConfig turns on the Data tab and sets how far back it reaches.
type DataConfig struct {
	Enabled bool `json:"enabled"`
	// Days of history the index holds: default 7, at most 30. Each day costs
	// about 0.7 MB per 10,000 packets a day.
	Days int `json:"days,omitempty"`
	// Layer is the areamap layer the tab places packets in. Default: the
	// first layer the areas define.
	Layer string `json:"layer,omitempty"`
	// HomeArea, when set, names the area whose boundary relays and
	// "via home" counts the flows report.
	HomeArea string `json:"homeArea,omitempty"`
	// DashboardsFile names a JSON file of ready-made dashboards, relative to
	// the config directory, offered to every visitor beside the built-in ones.
	DashboardsFile string `json:"dashboardsFile,omitempty"`
	// DefaultDashboard is the id of the dashboard the tab opens on.
	DefaultDashboard string `json:"defaultDashboard,omitempty"`
}

// DataEnabled reports whether the Data tab is switched on.
func (c *Config) DataEnabled() bool { return c != nil && c.Data != nil && c.Data.Enabled }

// DataDays is how many days the Data index holds.
func (c *Config) DataDays() int {
	if c == nil || c.Data == nil || c.Data.Days <= 0 {
		return 7
	}
	if c.Data.Days > 30 {
		return 30
	}
	return c.Data.Days
}

const (
	dataRefreshEvery  = 2 * time.Minute
	dataRefreshSpan   = 30 * time.Minute
	dataLocatorEvery  = 30 * time.Minute
	dataRebuildEvery  = 24 * time.Hour
	dataCacheFor      = 60 * time.Second
	dataFillChunk     = 2000
	dataPacketSlots   = 2
	dataSQLTimeFormat = "2006-01-02T15:04:05.000Z" // the store's own format
)

type dataCached struct {
	at   time.Time
	body []byte
}

// dataService owns the index and answers the /api/data endpoints.
type dataService struct {
	db    *DB
	areas *areamap.Map
	layer string
	home  string
	days  int

	now         func() time.Time
	refreshSpan time.Duration

	mu      sync.RWMutex
	ix      *dataindex.Index
	loc     *origin.Locator
	locAt   time.Time
	filling bool
	// waitFor, when set, holds the first fill until it closes: the packet
	// store's startup-load-done signal (see main.go).
	waitFor <-chan struct{}
	builtAt time.Time
	cache   map[string]dataCached
	sf      singleflight.Group
	slots   chan struct{}

	dashboards *DataDashboards
}

// newDataService returns nil when the tab is off, and an error when it is on
// but no areas are defined, since nothing could be placed.
func newDataService(db *DB, cfg *Config) (*dataService, error) {
	if !cfg.DataEnabled() {
		return nil, nil
	}
	d := &dataService{
		db: db, days: cfg.DataDays(),
		now: time.Now, refreshSpan: dataRefreshSpan,
		cache: map[string]dataCached{}, slots: make(chan struct{}, dataPacketSlots),
	}
	if !cfg.hasAreas() {
		// No areas: the tab counts by kind, type, channel, scope and routing,
		// and every packet starts and is heard in Unknown.
		if cfg.Data.HomeArea != "" {
			return nil, fmt.Errorf("data.homeArea %q needs areas: set areasFile or areas in config.json", cfg.Data.HomeArea)
		}
		return d, nil
	}
	m := cfg.areaMap
	layer := cfg.Data.Layer
	if layer == "" {
		layer = m.Layers()[0]
	}
	if len(m.Areas(layer)) == 0 {
		return nil, fmt.Errorf("data.layer %q has no areas (layers: %v)", layer, m.Layers())
	}
	if h := cfg.Data.HomeArea; h != "" && !m.Has(h) {
		return nil, fmt.Errorf("data.homeArea %q is not an area", h)
	}
	d.areas, d.layer, d.home = m, layer, cfg.Data.HomeArea
	return d, nil
}

// hasAreas reports whether config.json or areasFile defines any area, so the
// Data tab can place packets.
func (c *Config) hasAreas() bool {
	return c.areaMap != nil && len(c.areaMap.Areas("")) > 0
}

// places reports whether the tab places packets in areas.
func (d *dataService) places() bool { return d.areas != nil }

// dimensions are the filter dimensions that mean something here: all of
// them with areas, none of the place ones without.
func (d *dataService) dimensions() []string {
	if d.places() {
		return dataindex.Dimensions
	}
	var out []string
	for _, k := range dataindex.Dimensions {
		if !dataindex.PlaceDimensions[k] {
			out = append(out, k)
		}
	}
	return out
}

func (d *dataService) areaCodes() []string {
	var out []string
	if !d.places() {
		return out
	}
	for _, a := range d.areas.Areas(d.layer) {
		out = append(out, a.Code)
	}
	return out
}

func (d *dataService) index() *dataindex.Index {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ix
}

func (d *dataService) locator() *origin.Locator {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.loc
}

// run fills the index, then keeps it current until ctx ends.
func (d *dataService) run(ctx context.Context) {
	if d.waitFor != nil {
		// Never fill on top of the packet store's startup load: together they
		// outgrew a 1.5 GB container. Report filling while waiting.
		d.mu.Lock()
		d.filling = true
		d.mu.Unlock()
		select {
		case <-d.waitFor:
		case <-ctx.Done():
			return
		}
		log.Printf("[data] packet store loaded; filling the index")
	}
	if err := d.fill(ctx); err != nil {
		log.Printf("[data] fill failed: %v", err)
	}
	tick := time.NewTicker(dataRefreshEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var err error
		if d.now().Sub(d.builtAt) >= dataRebuildEvery {
			err = d.fill(ctx)
		} else {
			err = d.refresh(ctx)
		}
		if err != nil {
			log.Printf("[data] refresh failed: %v", err)
		}
	}
}

// loadLocator reads node and observer positions.
func (d *dataService) loadLocator(ctx context.Context) (*origin.Locator, error) {
	var nodes []origin.Node
	rows, err := d.db.conn.QueryContext(ctx, `SELECT public_key, COALESCE(name, ''), COALESCE(role, ''), lat, lon FROM nodes`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n origin.Node
		var role string
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&n.PublicKey, &n.Name, &role, &lat, &lon); err != nil {
			rows.Close()
			return nil, err
		}
		n.Relay = role == "repeater" || role == "room"
		if lat.Valid && lon.Valid {
			la, lo := lat.Float64, lon.Float64
			n.Lat, n.Lon = &la, &lo
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	var obs []origin.Observer
	rows, err = d.db.conn.QueryContext(ctx, `SELECT id, COALESCE(iata, '') FROM observers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o origin.Observer
		if err := rows.Scan(&o.ID, &o.IATA); err != nil {
			return nil, err
		}
		obs = append(obs, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return origin.NewLocator(origin.Options{Areas: d.areas, Layer: d.layer, Home: d.home}, nodes, obs), nil
}

// fill builds a fresh index of the last d.days days, newest chunk first, so
// the recent past is answerable within seconds. The first fill serves its
// index as it grows; later rebuilds swap in when complete.
func (d *dataService) fill(ctx context.Context) error {
	start, clock := d.now(), time.Now()
	loc, err := d.loadLocator(ctx)
	if err != nil {
		return err
	}
	ix := dataindex.New(dataindex.Options{Areas: d.areaCodes(), Home: d.home, NoPlaces: !d.places()})
	since := start.Add(-time.Duration(d.days) * 24 * time.Hour).Truncate(time.Second)
	d.mu.Lock()
	first := d.ix == nil
	d.loc, d.locAt = loc, start
	if first {
		d.ix, d.filling = ix, true
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.filling = false
		d.mu.Unlock()
	}()

	var lo, hi sql.NullInt64
	if err := d.db.conn.QueryRowContext(ctx, `SELECT MIN(id), MAX(id) FROM transmissions WHERE first_seen >= ?`,
		since.UTC().Format(dataSQLTimeFormat)).Scan(&lo, &hi); err != nil {
		return err
	}
	n := 0
	if lo.Valid {
		for b := hi.Int64; b >= lo.Int64; b -= dataFillChunk {
			a := b - dataFillChunk + 1
			if a < lo.Int64 {
				a = lo.Int64
			}
			pkts, err := d.readPackets(ctx, "t.id BETWEEN ? AND ? AND t.first_seen >= ?", a, b, since.UTC().Format(dataSQLTimeFormat))
			if err != nil {
				return err
			}
			recs := make([]origin.Record, len(pkts))
			oldest := int64(math.MaxInt64)
			for i := range pkts {
				recs[i] = loc.Derive(pkts[i])
				if recs[i].At < oldest {
					oldest = recs[i].At
				}
			}
			ix.Replace(math.MaxInt64, recs)
			n += len(recs)
			if first && oldest != math.MaxInt64 {
				ix.SetCovered(oldest)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	ix.SetCovered(since.Unix())
	d.mu.Lock()
	d.ix, d.builtAt = ix, start
	d.cache = map[string]dataCached{}
	d.mu.Unlock()
	log.Printf("[data] index filled: %d packets, %d rows over %d days in %v", n, ix.Len(), d.days, time.Since(clock).Round(time.Millisecond))
	return nil
}

// refresh re-derives the recent stretch and drops what has aged out.
func (d *dataService) refresh(ctx context.Context) error {
	now := d.now()
	ix := d.index()
	if ix == nil {
		return d.fill(ctx)
	}
	loc := d.locator()
	if now.Sub(d.locAt) >= dataLocatorEvery || loc == nil {
		l, err := d.loadLocator(ctx)
		if err != nil {
			return err
		}
		d.mu.Lock()
		d.loc, d.locAt, loc = l, now, l
		d.mu.Unlock()
	}
	since := now.Add(-d.refreshSpan).Truncate(time.Second)
	pkts, err := d.readPackets(ctx, "t.first_seen >= ?", since.UTC().Format(dataSQLTimeFormat))
	if err != nil {
		return err
	}
	recs := make([]origin.Record, len(pkts))
	for i := range pkts {
		recs[i] = loc.Derive(pkts[i])
	}
	// SQL compares first_seen as text from a whole second, and the index
	// cuts at the same second, so nothing falls between the two.
	ix.Replace(since.Unix(), recs)
	ix.Prune(now.Add(-time.Duration(d.days) * 24 * time.Hour).Unix())
	return nil
}

type dataAdvert struct {
	PubKey string   `json:"pubKey"`
	Lat    *float64 `json:"lat"`
	Lon    *float64 `json:"lon"`
	Flags  struct {
		HasLocation bool `json:"hasLocation"`
	} `json:"flags"`
	Channel string `json:"channel"`
	Hash    string `json:"channelHashHex"`
}

// readPackets reads transmissions matching where, with every observation.
//
// It reads every packet of the window, so the JSON columns are scanned as
// sql.RawBytes and decoded from the driver's buffer (valid until the next
// row) rather than copied into a string and again into a []byte, the scan
// targets are declared once, and only the payload types that use it select
// decoded_json at all.
func (d *dataService) readPackets(ctx context.Context, where string, args ...any) ([]origin.Packet, error) {
	scope := "NULL"
	if d.db.hasScopeName {
		scope = "t.scope_name"
	}
	rows, err := d.db.conn.QueryContext(ctx, `SELECT t.id, t.hash, t.first_seen, COALESCE(t.payload_type, -1), COALESCE(t.route_type, -1), `+scope+`,
		CASE WHEN t.payload_type IN (4, 5, 6) THEN COALESCE(t.decoded_json, '') ELSE '' END
		FROM transmissions t WHERE `+where+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	byID := map[int64]int{}
	var out []origin.Packet
	var lo, hi int64 = math.MaxInt64, 0
	var id int64
	var first string
	var decoded sql.RawBytes
	var sc sql.NullString
	for rows.Next() {
		var p origin.Packet
		if err := rows.Scan(&id, &p.Hash, &first, &p.Type, &p.Route, &sc, &decoded); err != nil {
			rows.Close()
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, first); err == nil {
			p.At = t.Unix()
		}
		p.Scope, p.Scoped = sc.String, sc.Valid
		if len(decoded) > 0 && (p.Type == 4 || p.Type == 5 || p.Type == 6) {
			var a dataAdvert
			if json.Unmarshal(decoded, &a) == nil {
				if p.Type == 4 && a.PubKey != "" {
					p.Advert = &origin.Advert{PublicKey: a.PubKey}
					if a.Flags.HasLocation {
						p.Advert.Lat, p.Advert.Lon = a.Lat, a.Lon
					}
				}
				if p.Type != 4 {
					switch {
					case a.Channel != "":
						p.Channel = a.Channel
					case a.Hash != "":
						p.Channel = "hash:" + strings.ToUpper(a.Hash)
					}
				}
			}
		}
		byID[id] = len(out)
		out = append(out, p)
		if id < lo {
			lo = id
		}
		if id > hi {
			hi = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	observer, join := "o.observer_id", ""
	if d.db.isV3 {
		observer, join = "obs.id", " JOIN observers obs ON obs.rowid = o.observer_idx"
	}
	resolved := "''"
	if d.db.hasResolvedPath {
		resolved = "COALESCE(o.resolved_path, '')"
	}
	orows, err := d.db.conn.QueryContext(ctx, `SELECT o.transmission_id, `+observer+`, COALESCE(o.direction, ''), COALESCE(o.path_json, ''), `+resolved+`
		FROM observations o`+join+` WHERE o.transmission_id BETWEEN ? AND ?`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	var tid int64
	var dir, path, res sql.RawBytes
	// Hops and relay keys repeat across the window's observations: each
	// distinct value is held once for this read.
	seen := make(map[string]string, 1024)
	for orows.Next() {
		var o origin.Observation
		if err := orows.Scan(&tid, &o.ObserverID, &dir, &path, &res); err != nil {
			return nil, err
		}
		i, ok := byID[tid]
		if !ok {
			continue
		}
		o.Relayed = bytes.EqualFold(dir, dataDirTx)
		if len(path) > 0 && string(path) != "[]" {
			if hops, ok := scanJSONStrings(path, seen); ok {
				o.Path = hops
			} else {
				_ = json.Unmarshal(path, &o.Path)
			}
		}
		if len(res) > 0 {
			// A null hop decodes to "", which is what it stood for.
			if rs, ok := scanJSONStrings(res, seen); ok {
				o.Resolved = rs
			} else {
				var rs []string
				if json.Unmarshal(res, &rs) == nil {
					o.Resolved = rs
				}
			}
		}
		out[i].Observations = append(out[i].Observations, o)
	}
	return out, orows.Err()
}

var dataDirTx = []byte("tx")

// scanJSONStrings reads b as a JSON array of escape-free ASCII strings and
// nulls (null reads as ""), as json.Unmarshal into a []string would, taking
// each value from seen so a repeated one is not allocated again. ok is
// false for anything else, which the caller decodes.
func scanJSONStrings(b []byte, seen map[string]string) (out []string, ok bool) {
	i := 0
	ws := func() {
		for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
			i++
		}
	}
	ws()
	if i >= len(b) || b[i] != '[' {
		return nil, false
	}
	i++
	ws()
	out = make([]string, 0, 8)
	if i < len(b) && b[i] == ']' {
		i++
		ws()
		return out, i == len(b)
	}
	for {
		switch {
		case i < len(b) && b[i] == '"':
			i++
			start := i
			for i < len(b) && b[i] != '"' {
				if b[i] == '\\' || b[i] < 0x20 || b[i] >= 0x80 {
					return nil, false
				}
				i++
			}
			if i >= len(b) {
				return nil, false
			}
			v, have := seen[string(b[start:i])]
			if !have {
				v = string(b[start:i])
				seen[v] = v
			}
			out = append(out, v)
			i++
		case bytes.HasPrefix(b[i:], []byte("null")):
			out = append(out, "")
			i += 4
		default:
			return nil, false
		}
		ws()
		if i >= len(b) {
			return nil, false
		}
		switch b[i] {
		case ',':
			i++
			ws()
		case ']':
			i++
			ws()
			return out, i == len(b)
		default:
			return nil, false
		}
	}
}

// windows are the periods the index reaches.
func (d *dataService) windows() []dataindex.Window {
	var out []dataindex.Window
	for _, w := range dataindex.Windows {
		if w.Span <= time.Duration(d.days)*24*time.Hour {
			out = append(out, w)
		}
	}
	return out
}

func (d *dataService) window(key string) (dataindex.Window, bool) {
	for _, w := range d.windows() {
		if w.Key == key {
			return w, true
		}
	}
	return dataindex.Window{}, false
}

func (d *dataService) relay(key string) (string, string) {
	if loc := d.locator(); loc != nil {
		return loc.Relay(key)
	}
	return strings.ToUpper(key), ""
}

// cached answers from the cache, building at most once at a time per key.
func (d *dataService) cached(key string, build func() ([]byte, error)) ([]byte, error) {
	d.mu.RLock()
	c, ok := d.cache[key]
	d.mu.RUnlock()
	if ok && d.now().Sub(c.at) < dataCacheFor {
		return c.body, nil
	}
	v, err, _ := d.sf.Do(key, func() (any, error) {
		b, err := build()
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.cache[key] = dataCached{at: d.now(), body: b}
		d.mu.Unlock()
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (d *dataService) cubeJSON(window string) ([]byte, error) {
	w, ok := d.window(window)
	if !ok {
		return nil, errBadWindow
	}
	return d.cached("cube:"+w.Key, func() ([]byte, error) {
		return json.Marshal(d.index().Cube(w, d.now()))
	})
}

func (d *dataService) summaryJSON(window string) ([]byte, error) {
	w, ok := d.window(window)
	if !ok {
		return nil, errBadWindow
	}
	return d.cached("summary:"+w.Key, func() ([]byte, error) {
		return json.Marshal(d.index().Summary(w, d.now(), d.relay))
	})
}

func (d *dataService) packets(window string, from, to int64, fs []dataindex.Filter) dataindex.PacketResult {
	w, _ := d.window(window)
	return d.index().Packets(w, d.now(), from, to, fs, d.relay)
}

var errBadWindow = errors.New("no such window")

func validDataFilters(fs []dataindex.Filter) error {
	for _, f := range fs {
		ok := false
		for _, dim := range dataindex.Dimensions {
			if f.D == dim {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("unknown dimension %q", f.D)
		}
	}
	return nil
}

type dataAreaInfo struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type dataConfigResponse struct {
	Enabled     bool               `json:"enabled"`
	Places      bool               `json:"places"`
	Layer       string             `json:"layer,omitempty"`
	Layers      []string           `json:"layers,omitempty"`
	Home        string             `json:"home,omitempty"`
	Areas       []dataAreaInfo     `json:"areas,omitempty"`
	Windows     []dataindex.Window `json:"windows,omitempty"`
	Groups      []dataindex.Group  `json:"groups,omitempty"`
	TypeNames   map[int]string     `json:"typeNames,omitempty"`
	Dimensions  []string           `json:"dimensions,omitempty"`
	Days        int                `json:"days,omitempty"`
	Rows        int                `json:"rows"`
	CoveredFrom *time.Time         `json:"coveredFrom,omitempty"`
	Filling     bool               `json:"filling"`
}

func writeDataJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write(body)
}

// GET /api/data/config
func (s *Server) handleDataConfig(w http.ResponseWriter, r *http.Request) {
	d := s.data
	if d == nil {
		writeJSON(w, dataConfigResponse{Enabled: false})
		return
	}
	resp := dataConfigResponse{
		Enabled: true, Places: d.places(), Layer: d.layer, Home: d.home,
		Windows: d.windows(), Groups: dataindex.Groups, TypeNames: dataindex.TypeNames,
		Dimensions: d.dimensions(), Days: d.days,
	}
	if d.places() {
		resp.Layers = d.areas.Layers()
		for _, a := range d.areas.Areas(d.layer) {
			resp.Areas = append(resp.Areas, dataAreaInfo{Code: a.Code, Label: a.Label})
		}
	}
	if ix := d.index(); ix != nil {
		resp.Rows = ix.Len()
		if c := ix.Covered(); c != 0 {
			t := time.Unix(c, 0).UTC()
			resp.CoveredFrom = &t
		}
	}
	d.mu.RLock()
	resp.Filling = d.filling
	d.mu.RUnlock()
	writeJSON(w, resp)
}

func (s *Server) dataOr404(w http.ResponseWriter) *dataService {
	if s.data == nil {
		http.Error(w, "the Data tab is not enabled", http.StatusNotFound)
		return nil
	}
	if s.data.index() == nil {
		// On but not built yet: the fill waits for the packet store's
		// startup load. Say so, so the page can tell the reader to wait.
		http.Error(w, "the Data tab is filling its index; try again shortly", http.StatusServiceUnavailable)
		return nil
	}
	return s.data
}

func (s *Server) serveDataBuilt(w http.ResponseWriter, body []byte, err error) {
	if errors.Is(err, errBadWindow) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeDataJSON(w, body)
}

// GET /api/data/summary?window=
func (s *Server) handleDataSummary(w http.ResponseWriter, r *http.Request) {
	if d := s.dataOr404(w); d != nil {
		b, err := d.summaryJSON(r.URL.Query().Get("window"))
		s.serveDataBuilt(w, b, err)
	}
}

// GET /api/data/cube?window=
func (s *Server) handleDataCube(w http.ResponseWriter, r *http.Request) {
	if d := s.dataOr404(w); d != nil {
		b, err := d.cubeJSON(r.URL.Query().Get("window"))
		s.serveDataBuilt(w, b, err)
	}
}

// GET /api/data/packets?window=&from=&to=&f=
func (s *Server) handleDataPackets(w http.ResponseWriter, r *http.Request) {
	d := s.dataOr404(w)
	if d == nil {
		return
	}
	q := r.URL.Query()
	if _, ok := d.window(q.Get("window")); !ok {
		http.Error(w, errBadWindow.Error(), http.StatusBadRequest)
		return
	}
	var fs []dataindex.Filter
	if f := q.Get("f"); f != "" {
		if err := json.Unmarshal([]byte(f), &fs); err != nil {
			http.Error(w, "f must be a JSON list of filters", http.StatusBadRequest)
			return
		}
		if err := validDataFilters(fs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-r.Context().Done():
		return
	}
	b, err := json.Marshal(d.packets(q.Get("window"), from, to, fs))
	s.serveDataBuilt(w, b, err)
}
