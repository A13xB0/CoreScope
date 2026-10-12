package main

// Network Watch: the public Health page. It learns what normal traffic looks
// like for every repeater, observer, link, corridor and configured route from
// the packets already in SQLite, and says when one goes quiet. The detection
// itself lives in internal/netwatch; this file feeds it and serves it.
//
// Off unless netWatch.enabled is set. At startup it waits for the packet
// store's own load, then reads the window (15 days by default) of
// observations once, in batches; after that it reads only new observations
// every pollSeconds. Memory is a few megabytes of hourly counts.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/netwatch"
)

// NetWatchConfig is the netWatch block of config.json.
type NetWatchConfig struct {
	Enabled     bool               `json:"enabled"`
	WindowDays  int                `json:"windowDays,omitempty"`  // history kept (default 22; the first 14 days only teach it what is usual)
	PollSeconds int                `json:"pollSeconds,omitempty"` // how often new packets are read (default 300)
	Corridors   []NetWatchCorridor `json:"corridors,omitempty"`
	Routes      []NetWatchRoute    `json:"routes,omitempty"`
	// Area, when set, limits judging to repeaters placed inside it (those
	// with no position are still judged); others are counted, never flagged.
	Area *NetWatchZone `json:"area,omitempty"`
}

// NetWatchZone is a box on the map, in degrees.
type NetWatchZone struct {
	Name   string  `json:"name"`
	LatMin float64 `json:"latMin"`
	LatMax float64 `json:"latMax"`
	LonMin float64 `json:"lonMin"`
	LonMax float64 `json:"lonMax"`
	// Polygon, when given (three or more [lat, lon] points), replaces the box.
	Polygon [][2]float64 `json:"polygon,omitempty"`
}

// NetWatchCorridor counts packets whose path joins a repeater in A and one in B.
type NetWatchCorridor struct {
	Name string       `json:"name"`
	A    NetWatchZone `json:"a"`
	B    NetWatchZone `json:"b"`
}

// NetWatchRoute follows packets that pass From and later To (repeater public keys).
type NetWatchRoute struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

const (
	netWatchBatch       = 5000
	netWatchDefaultDays = 22
	netWatchBaseDays    = 14 // each hour is compared with the same hours on this many days
	netWatchDefaultPoll = 300
)

type nwNode struct {
	name, role string
	lat, lon   float64
	placed     bool
}

type netWatchService struct {
	db      *DB
	cfg     *NetWatchConfig
	watch   *netwatch.Watch
	now     func() time.Time
	waitFor <-chan struct{}
	window  int // hours

	mu        sync.RWMutex
	nodes     map[string]nwNode // lower-case public key
	obsByIdx  map[int64]string  // observers.rowid -> id
	obsNames  map[string]string // id -> name
	lastObsID int64
	evaluated int64 // last hour judged
	ready     bool
}

// newNetWatchService returns nil when the watch is off.
func newNetWatchService(db *DB, cfg *Config) *netWatchService {
	if cfg == nil || cfg.NetWatch == nil || !cfg.NetWatch.Enabled {
		return nil
	}
	c := cfg.NetWatch
	days := c.WindowDays
	if days <= 0 {
		days = netWatchDefaultDays
	}
	if days < netWatchBaseDays+2 {
		days = netWatchBaseDays + 2 // two weeks to learn from, and a day or two to judge
	}
	s := &netWatchService{db: db, cfg: c, now: time.Now, window: days * 24,
		nodes: map[string]nwNode{}, obsByIdx: map[int64]string{}, obsNames: map[string]string{}}
	opt := netwatch.Options{WindowHours: s.window, Days: netWatchBaseDays}
	if a := c.Area; a != nil {
		opt.Area = &netwatch.Zone{Name: a.Name, LatMin: a.LatMin, LatMax: a.LatMax, LonMin: a.LonMin, LonMax: a.LonMax, Polygon: a.Polygon}
	}
	for _, cr := range c.Corridors {
		opt.Corridors = append(opt.Corridors, netwatch.Corridor{Name: cr.Name,
			A: netwatch.Zone{Name: cr.A.Name, LatMin: cr.A.LatMin, LatMax: cr.A.LatMax, LonMin: cr.A.LonMin, LonMax: cr.A.LonMax, Polygon: cr.A.Polygon},
			B: netwatch.Zone{Name: cr.B.Name, LatMin: cr.B.LatMin, LatMax: cr.B.LatMax, LonMin: cr.B.LonMin, LonMax: cr.B.LonMax, Polygon: cr.B.Polygon}})
	}
	for _, r := range c.Routes {
		opt.Routes = append(opt.Routes, netwatch.Route{Name: r.Name, From: strings.ToLower(r.From), To: strings.ToLower(r.To)})
	}
	s.watch = netwatch.New(opt, s)
	return s
}

// Position places a repeater for the corridors (netwatch.Locator).
func (s *netWatchService) Position(pk string) (float64, float64, bool) {
	s.mu.RLock()
	n, ok := s.nodes[pk]
	s.mu.RUnlock()
	return n.lat, n.lon, ok && n.placed
}

func (s *netWatchService) pollEvery() time.Duration {
	if s.cfg.PollSeconds > 0 {
		return time.Duration(s.cfg.PollSeconds) * time.Second
	}
	return netWatchDefaultPoll * time.Second
}

// run fills the watch once the packet store has loaded, then keeps it current.
func (s *netWatchService) run(ctx context.Context) {
	if s.waitFor != nil {
		select {
		case <-s.waitFor:
		case <-ctx.Done():
			return
		}
	}
	t := time.NewTicker(s.pollEvery())
	defer t.Stop()
	for {
		start := time.Now()
		if err := s.catchUp(ctx); err != nil {
			log.Printf("[netwatch] %v", err)
		} else if !s.isReady() {
			log.Printf("[netwatch] filled in %s", time.Since(start).Round(time.Millisecond))
		}
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *netWatchService) isReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// refreshNames reloads repeater names and positions and the observer table.
func (s *netWatchService) refreshNames() error {
	nodes := map[string]nwNode{}
	rows, err := s.db.conn.Query(`SELECT public_key, COALESCE(name,''), COALESCE(role,''), lat, lon FROM nodes`)
	if err != nil {
		return fmt.Errorf("nodes: %w", err)
	}
	for rows.Next() {
		var pk, name, role string
		var lat, lon sql.NullFloat64
		if rows.Scan(&pk, &name, &role, &lat, &lon) != nil {
			continue
		}
		n := nwNode{name: name, role: role}
		if lat.Valid && lon.Valid && (lat.Float64 != 0 || lon.Float64 != 0) {
			n.lat, n.lon, n.placed = lat.Float64, lon.Float64, true
		}
		nodes[strings.ToLower(pk)] = n
	}
	rows.Close()
	byIdx, names := map[int64]string{}, map[string]string{}
	orows, err := s.db.conn.Query(`SELECT rowid, id, COALESCE(name,'') FROM observers`)
	if err != nil {
		return fmt.Errorf("observers: %w", err)
	}
	for orows.Next() {
		var idx int64
		var id, name string
		if orows.Scan(&idx, &id, &name) == nil {
			byIdx[idx], names[id] = id, name
		}
	}
	orows.Close()
	s.mu.Lock()
	s.nodes, s.obsByIdx, s.obsNames = nodes, byIdx, names
	s.mu.Unlock()
	return nil
}

func parseFirstSeen(v string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// catchUp reads every observation it has not seen yet (the whole window the
// first time) and judges every hour that has completed since.
func (s *netWatchService) catchUp(ctx context.Context) error {
	if err := s.refreshNames(); err != nil {
		return err
	}
	now := s.now()
	nowHour := now.Unix() / 3600
	s.mu.RLock()
	last := s.lastObsID
	s.mu.RUnlock()
	if last == 0 {
		cutoff := now.Add(-time.Duration(s.window) * time.Hour).Unix()
		var minID sql.NullInt64
		if err := s.db.conn.QueryRow(`SELECT MIN(id) FROM observations WHERE timestamp >= ?`, cutoff).Scan(&minID); err != nil {
			return fmt.Errorf("window start: %w", err)
		}
		if !minID.Valid {
			s.mu.Lock()
			s.evaluated = nowHour - 1
			s.mu.Unlock()
			return nil
		}
		last = minID.Int64 - 1
		s.mu.Lock()
		s.evaluated = nowHour - int64(s.window)
		s.mu.Unlock()
	}
	obsCol := "o.observer_idx"
	if !s.db.isV3 {
		obsCol = "o.observer_id"
	}
	q := `SELECT o.id, o.transmission_id, ` + obsCol + `, COALESCE(o.resolved_path,''), t.first_seen,
		COALESCE(t.payload_type,-1), COALESCE(t.from_pubkey,'')
		FROM observations o JOIN transmissions t ON t.id = o.transmission_id
		WHERE o.id > ? ORDER BY o.id LIMIT ?`
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, next, err := s.readBatch(q, last)
		if err != nil {
			return err
		}
		last = next
		s.mu.Lock()
		s.lastObsID = last
		s.mu.Unlock()
		if n < netWatchBatch {
			break
		}
		time.Sleep(10 * time.Millisecond) // leave room for requests while filling
	}
	s.mu.Lock()
	from := s.evaluated + 1
	s.mu.Unlock()
	for h := from; h <= nowHour-1; h++ {
		s.watch.Evaluate(h)
	}
	s.mu.Lock()
	if nowHour-1 > s.evaluated {
		s.evaluated = nowHour - 1
	}
	s.mu.Unlock()
	s.watch.Prune()
	return nil
}

func (s *netWatchService) readBatch(q string, after int64) (int, int64, error) {
	rows, err := s.db.conn.Query(q, after, netWatchBatch)
	if err != nil {
		return 0, after, fmt.Errorf("observations: %w", err)
	}
	defer rows.Close()
	n, last := 0, after
	for rows.Next() {
		var id, tx, typ int64
		var obs sql.NullString
		var rp, firstSeen, from string
		if err := rows.Scan(&id, &tx, &obs, &rp, &firstSeen, &typ, &from); err != nil {
			return n, last, err
		}
		n++
		last = id
		at, ok := parseFirstSeen(firstSeen)
		if !ok {
			continue
		}
		h := at.Unix() / 3600
		observer := obs.String
		if s.db.isV3 && obs.Valid {
			var idx int64
			fmt.Sscan(obs.String, &idx)
			s.mu.RLock()
			observer = s.obsByIdx[idx]
			s.mu.RUnlock()
		}
		var path []string
		if rp != "" {
			var raw []*string
			if json.Unmarshal([]byte(rp), &raw) == nil {
				path = make([]string, len(raw))
				for i, p := range raw {
					if p != nil {
						path[i] = strings.ToLower(*p)
					}
				}
			}
		}
		if typ == 4 && from != "" {
			s.watch.AddAdvert(tx, h, strings.ToLower(from))
		}
		s.watch.AddObservation(tx, h, observer, path)
	}
	return n, last, rows.Err()
}

// ---- API ---------------------------------------------------------------------

// NetWatchIncident is a repeater that went quiet.
type NetWatchIncident struct {
	Node         string   `json:"node"`
	Name         string   `json:"name"`
	Lat          *float64 `json:"lat,omitempty"`
	Lon          *float64 `json:"lon,omitempty"`
	Start        string   `json:"start"`
	Flagged      string   `json:"flagged"`
	End          *string  `json:"end,omitempty"`
	Usual        int      `json:"usual"`
	Now          int      `json:"now"`
	AdvertEveryH int      `json:"advertEveryH"`
}

// NetWatchObserver is an observer's traffic against its usual.
type NetWatchObserver struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Now   int    `json:"now"`
	Usual int    `json:"usual"`
	Quiet bool   `json:"quiet"`
}

// NetWatchCarrier is a repeater that carried a corridor's packets.
type NetWatchCarrier struct {
	Node    string `json:"node"`
	Name    string `json:"name"`
	Packets int    `json:"packets"`
}

// NetWatchCorridorStatus is a corridor's last day against usual.
type NetWatchCorridorStatus struct {
	Name     string            `json:"name"`
	A        string            `json:"a"`
	B        string            `json:"b"`
	LastDay  int               `json:"lastDay"`
	UsualDay int               `json:"usualDay"`
	Daily    []int             `json:"daily"`
	Carriers []NetWatchCarrier `json:"carriers"`
}

// NetWatchRouteStatus is a configured route against its usual.
type NetWatchRouteStatus struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	FromName string `json:"fromName"`
	To       string `json:"to"`
	ToName   string `json:"toName"`
	Now      int    `json:"now"`
	Usual    int    `json:"usual"`
	LastDay  int    `json:"lastDay"`
	UsualDay int    `json:"usualDay"`
	Daily    []int  `json:"daily"`
	// ObserversDown names quiet observers that usually hear most of it.
	ObserversDown []string `json:"observersDown,omitempty"`
}

// NetWatchResponse is GET /api/netwatch.
type NetWatchResponse struct {
	Ready        bool                     `json:"ready"`
	Hour         string                   `json:"hour"` // the last complete hour judged
	SlotHours    int                      `json:"slotHours"`
	Days         int                      `json:"days"`
	Judged       int                      `json:"judged"`
	Open         []NetWatchIncident       `json:"open"`
	Recent       []NetWatchIncident       `json:"recent"`
	Observers    []NetWatchObserver       `json:"observers"`
	NetworkNow   int                      `json:"networkNow"`
	NetworkUsual int                      `json:"networkUsual"`
	Corridors    []NetWatchCorridorStatus `json:"corridors"`
	Routes       []NetWatchRouteStatus    `json:"routes"`
}

// NetWatchNodeResponse is GET /api/netwatch/node/{pubkey}: hourly, oldest first.
type NetWatchNodeResponse struct {
	Node     string    `json:"node"`
	Name     string    `json:"name"`
	End      string    `json:"end"`
	Relays   []int     `json:"relays"`
	Adverts  []int     `json:"adverts"`
	Expected []float64 `json:"expected"`
}

// NetWatchRouteNode is one repeater on a probed route.
type NetWatchRouteNode struct {
	Node string   `json:"node"`
	Name string   `json:"name"`
	Lat  *float64 `json:"lat,omitempty"`
	Lon  *float64 `json:"lon,omitempty"`
}

// NetWatchHop is one link on a probed route against its usual.
type NetWatchHop struct {
	From     string `json:"from"`
	FromName string `json:"fromName"`
	To       string `json:"to"`
	ToName   string `json:"toName"`
	Now      int    `json:"now"`
	Usual    int    `json:"usual"`
	Quiet    bool   `json:"quiet"`
	// ObserversDown names quiet observers that usually hear most of this hop.
	ObserversDown []string  `json:"observersDown,omitempty"`
	Series        []int     `json:"series"`
	Expected      []float64 `json:"expected"`
}

// NetWatchProbeResponse is GET /api/netwatch/probe?from=&to=.
type NetWatchProbeResponse struct {
	Hour  string              `json:"hour"`
	Route []NetWatchRouteNode `json:"route"`
	Hops  []NetWatchHop       `json:"hops"`
	Note  string              `json:"note,omitempty"`
}

// observerNames turns observer ids into their names.
func (s *netWatchService) observerNames(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(ids))
	for i, id := range ids {
		if n := s.obsNames[id]; n != "" {
			out[i] = n
		} else {
			out[i] = id[:min(8, len(id))]
		}
	}
	return out
}

func hourISO(h int64) string { return time.Unix(h*3600, 0).UTC().Format(time.RFC3339) }

func (s *netWatchService) node(pk string) nwNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[pk]
	if !ok || n.name == "" {
		n.name = pk[:min(8, len(pk))]
	}
	return n
}

func (s *netWatchService) incident(i netwatch.Incident) NetWatchIncident {
	n := s.node(i.Node)
	out := NetWatchIncident{Node: i.Node, Name: n.name, Start: hourISO(i.Start), Flagged: hourISO(i.Flagged),
		Usual: i.Usual, Now: i.Now, AdvertEveryH: i.AdvertEveryH}
	if n.placed {
		lat, lon := n.lat, n.lon
		out.Lat, out.Lon = &lat, &lon
	}
	if i.End != nil {
		e := hourISO(*i.End)
		out.End = &e
	}
	return out
}

func (s *Server) netWatchOr404(w http.ResponseWriter) *netWatchService {
	if s.netwatch == nil {
		writeError(w, http.StatusNotFound, "network watch is off (netWatch.enabled in config.json)")
		return nil
	}
	return s.netwatch
}

func (s *Server) handleNetWatch(w http.ResponseWriter, r *http.Request) {
	nw := s.netWatchOr404(w)
	if nw == nil {
		return
	}
	st := nw.watch.Status()
	resp := NetWatchResponse{Ready: nw.isReady() || st.Hour > 0, Hour: hourISO(st.Hour), SlotHours: 6, Days: netWatchBaseDays, Judged: st.Judged,
		Open: []NetWatchIncident{}, Recent: []NetWatchIncident{}, Observers: []NetWatchObserver{},
		NetworkNow: st.Network.Now, NetworkUsual: st.Network.Usual, Corridors: []NetWatchCorridorStatus{}, Routes: []NetWatchRouteStatus{}}
	for _, i := range st.Open {
		resp.Open = append(resp.Open, nw.incident(i))
	}
	for _, i := range st.Recent {
		resp.Recent = append(resp.Recent, nw.incident(i))
	}
	nw.mu.RLock()
	for _, o := range st.Observers {
		name := nw.obsNames[o.ID]
		if name == "" {
			name = o.ID[:min(8, len(o.ID))]
		}
		resp.Observers = append(resp.Observers, NetWatchObserver{ID: o.ID, Name: name, Now: o.Now, Usual: o.Usual, Quiet: o.Quiet})
	}
	nw.mu.RUnlock()
	for _, c := range st.Corridors {
		cs := NetWatchCorridorStatus{Name: c.Name, A: c.A, B: c.B, LastDay: c.LastDay, UsualDay: c.UsualDay, Daily: c.Daily, Carriers: []NetWatchCarrier{}}
		for _, k := range c.Carriers {
			cs.Carriers = append(cs.Carriers, NetWatchCarrier{Node: k.Node, Name: nw.node(k.Node).name, Packets: k.Packets})
		}
		resp.Corridors = append(resp.Corridors, cs)
	}
	for _, rt := range st.Routes {
		resp.Routes = append(resp.Routes, NetWatchRouteStatus{Name: rt.Name, From: rt.From, FromName: nw.node(rt.From).name,
			To: rt.To, ToName: nw.node(rt.To).name, Now: rt.Now, Usual: rt.Usual, LastDay: rt.LastDay, UsualDay: rt.UsualDay, Daily: rt.Daily,
			ObserversDown: nw.observerNames(rt.ObserversDown)})
	}
	writeJSON(w, resp)
}

func (s *Server) handleNetWatchNode(w http.ResponseWriter, r *http.Request) {
	nw := s.netWatchOr404(w)
	if nw == nil {
		return
	}
	pk := strings.ToLower(mux.Vars(r)["pubkey"])
	end := nw.watch.Status().Hour
	ser := nw.watch.NodeSeries(pk, end)
	writeJSON(w, NetWatchNodeResponse{Node: pk, Name: nw.node(pk).name, End: hourISO(end),
		Relays: ser.Relays, Adverts: ser.Adverts, Expected: ser.Expected})
}

func (s *Server) handleNetWatchProbe(w http.ResponseWriter, r *http.Request) {
	nw := s.netWatchOr404(w)
	if nw == nil {
		return
	}
	from := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("from")))
	to := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("to")))
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "from and to are both needed (repeater public keys)")
		return
	}
	t := nw.watch.Status().Hour
	p := nw.watch.Probe(from, to, t)
	resp := NetWatchProbeResponse{Hour: hourISO(t), Route: []NetWatchRouteNode{}, Hops: []NetWatchHop{}, Note: p.Note}
	for _, pk := range p.Route {
		n := nw.node(pk)
		rn := NetWatchRouteNode{Node: pk, Name: n.name}
		if n.placed {
			lat, lon := n.lat, n.lon
			rn.Lat, rn.Lon = &lat, &lon
		}
		resp.Route = append(resp.Route, rn)
	}
	for _, h := range p.Hops {
		resp.Hops = append(resp.Hops, NetWatchHop{From: h.From, FromName: nw.node(h.From).name, To: h.To, ToName: nw.node(h.To).name,
			Now: h.Now, Usual: h.Usual, Quiet: h.Quiet, ObserversDown: nw.observerNames(h.ObserversDown), Series: h.Series, Expected: h.Expected})
	}
	writeJSON(w, resp)
}
