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
	// Notify, when it names a webhook, posts each incident as it opens and
	// closes (over the internet; nothing is sent on the mesh).
	Notify *NetWatchNotify `json:"notify,omitempty"`
	// Tiers, when present, also judges core repeaters (traffic through them
	// every few minutes) over a couple of hours and local ones on how long
	// they have gone unheard. Zero values take the defaults.
	Tiers *NetWatchTiers `json:"tiers,omitempty"`
}

// NetWatchTiers tunes the core and local tiers.
type NetWatchTiers struct {
	CoreEveryMinutes  int `json:"coreEveryMinutes,omitempty"`  // core: usually a packet at least this often (default 10)
	CoreHours         int `json:"coreHours,omitempty"`         // core: judged over the last this many hours (default 2)
	LocalSilenceHours int `json:"localSilenceHours,omitempty"` // local: unheard this long, or its usual longest gap if more (default 3)
}

// NetWatchNotify is where incident notifications go: a webhook taking
// Discord ({"content"}), Slack ({"text"}) or plain JSON posts, and the site
// address to link back to.
type NetWatchNotify struct {
	Webhook string `json:"webhook"`
	Format  string `json:"format,omitempty"` // discord (default), slack or json
	BaseURL string `json:"baseUrl,omitempty"`
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
// Basis picks the steady traffic its reach is judged on: "adverts" (the
// default) or "chat" (channel messages, from the first repeater that relayed
// them) where adverts no longer cross.
// Target, when set, is the share (0..1) that should get across each way.
type NetWatchCorridor struct {
	Name   string       `json:"name"`
	A      NetWatchZone `json:"a"`
	B      NetWatchZone `json:"b"`
	Basis  string       `json:"basis,omitempty"`
	Target float64      `json:"target,omitempty"`
}

// NetWatchRoute follows packets that pass From and later To (repeater public
// keys); Basis is as for NetWatchCorridor.
type NetWatchRoute struct {
	Name   string  `json:"name"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Basis  string  `json:"basis,omitempty"`
	Target float64 `json:"target,omitempty"`
}

const (
	netWatchBatch       = 5000
	netWatchDefaultDays = 22
	netWatchBaseDays    = 14 // each hour is compared with the same hours on this many days
	netWatchDefaultPoll = 300
	netWatchMinDays     = 7   // judging starts once this many baseline days are in the window
	netWatchTwinKm      = 150 // a shared hop prefix this close to the resolved repeater could be either
	netWatchSeenDays    = 30  // repeaters heard this recently can share a prefix
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
	nodes     map[string]nwNode   // lower-case public key
	prefixes  map[string][]string // 1- and 2-byte hop prefix (lower hex) -> repeaters heard lately
	obsByIdx  map[int64]string    // observers.rowid -> id
	obsNames  map[string]string   // id -> name
	lastObsID int64
	evaluated int64 // last hour judged
	ready     bool
	hops      [2]int64 // short hops read, and those dropped as ambiguous
	notifier  *netWatchNotifier
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
	opt := netwatch.Options{WindowHours: s.window, Days: netWatchBaseDays, MinDays: netWatchMinDays}
	if a := c.Area; a != nil {
		opt.Area = &netwatch.Zone{Name: a.Name, LatMin: a.LatMin, LatMax: a.LatMax, LonMin: a.LonMin, LonMax: a.LonMax, Polygon: a.Polygon}
	}
	for _, cr := range c.Corridors {
		opt.Corridors = append(opt.Corridors, netwatch.Corridor{Name: cr.Name,
			A:      netwatch.Zone{Name: cr.A.Name, LatMin: cr.A.LatMin, LatMax: cr.A.LatMax, LonMin: cr.A.LonMin, LonMax: cr.A.LonMax, Polygon: cr.A.Polygon},
			B:      netwatch.Zone{Name: cr.B.Name, LatMin: cr.B.LatMin, LatMax: cr.B.LatMax, LonMin: cr.B.LonMin, LonMax: cr.B.LonMax, Polygon: cr.B.Polygon},
			Basis:  netwatch.Basis(strings.ToLower(cr.Basis)),
			Target: cr.Target})
	}
	for _, r := range c.Routes {
		opt.Routes = append(opt.Routes, netwatch.Route{Name: r.Name, From: strings.ToLower(r.From), To: strings.ToLower(r.To), Basis: netwatch.Basis(strings.ToLower(r.Basis)), Target: r.Target})
	}
	if t := c.Tiers; t != nil {
		opt.Tiers = &netwatch.Tiers{CoreEveryMin: t.CoreEveryMinutes, CoreHours: t.CoreHours, LocalSilence: t.LocalSilenceHours}
	}
	s.watch = netwatch.New(opt, s)
	if c.Notify != nil && c.Notify.Webhook != "" {
		s.notifier = &netWatchNotifier{cfg: *c.Notify, client: &http.Client{Timeout: 10 * time.Second}}
	}
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
		wasReady := s.isReady()
		if err := s.catchUp(ctx); err != nil {
			log.Printf("[netwatch] %v", err)
		} else {
			if !wasReady {
				s.mu.RLock()
				hops := s.hops
				s.mu.RUnlock()
				log.Printf("[netwatch] filled in %s; %d of %d short hops set aside as ambiguous", time.Since(start).Round(time.Millisecond), hops[1], hops[0])
			}
			if s.notifier != nil {
				s.notifier.update(ctx, s.response())
			}
		}
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
	prefixes := map[string][]string{}
	recent := s.now().Add(-netWatchSeenDays * 24 * time.Hour)
	rows, err := s.db.conn.Query(`SELECT public_key, COALESCE(name,''), COALESCE(role,''), lat, lon, COALESCE(last_seen,'') FROM nodes`)
	if err != nil {
		return fmt.Errorf("nodes: %w", err)
	}
	for rows.Next() {
		var pk, name, role, seen string
		var lat, lon sql.NullFloat64
		if rows.Scan(&pk, &name, &role, &lat, &lon, &seen) != nil {
			continue
		}
		pk = strings.ToLower(pk)
		n := nwNode{name: name, role: role}
		if lat.Valid && lon.Valid && (lat.Float64 != 0 || lon.Float64 != 0) {
			n.lat, n.lon, n.placed = lat.Float64, lon.Float64, true
		}
		nodes[pk] = n
		if at, ok := parseFirstSeen(seen); ok && at.After(recent) && (role == "repeater" || role == "room") && len(pk) >= 4 {
			prefixes[pk[:2]] = append(prefixes[pk[:2]], pk)
			prefixes[pk[:4]] = append(prefixes[pk[:4]], pk)
		}
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
	s.nodes, s.obsByIdx, s.obsNames, s.prefixes = nodes, byIdx, names, prefixes
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
		COALESCE(t.payload_type,-1), COALESCE(t.from_pubkey,''), COALESCE(o.path_json,''), o.snr,
		CASE WHEN t.payload_type = 4 THEN json_extract(t.decoded_json, '$.timestamp') END
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
	s.mu.Lock()
	s.ready = true // the whole window is read and judged; not before
	s.mu.Unlock()
	return nil
}

func (s *netWatchService) readBatch(q string, after int64) (int, int64, error) {
	rows, err := s.db.conn.Query(q, after, netWatchBatch)
	if err != nil {
		return 0, after, fmt.Errorf("observations: %w", err)
	}
	defer rows.Close()
	s.mu.RLock()
	nodes, prefixes := s.nodes, s.prefixes
	s.mu.RUnlock()
	var shortHops, ambiguous int64
	n, last := 0, after
	for rows.Next() {
		var id, tx, typ int64
		var obs sql.NullString
		var rp, firstSeen, from, pj string
		var snr sql.NullFloat64
		var sent sql.NullInt64
		if err := rows.Scan(&id, &tx, &obs, &rp, &firstSeen, &typ, &from, &pj, &snr, &sent); err != nil {
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
		// A short hop (1 or 2 bytes) that another repeater heard lately
		// shares, close enough to be either, is not credited to the one it
		// was resolved to.
		if pj != "" && len(path) > 0 {
			var hops []string
			if json.Unmarshal([]byte(pj), &hops) == nil {
				for i, hop := range hops {
					if i >= len(path) || path[i] == "" || len(hop) > 4 {
						continue
					}
					shortHops++
					if nwAmbiguous(strings.ToLower(hop), path[i], nodes, prefixes) {
						path[i] = ""
						ambiguous++
					}
				}
			}
		}
		from = strings.ToLower(from)
		if typ == 4 && from != "" {
			s.watch.AddAdvertObservation(tx, h, observer, from, path)
			if sent.Valid {
				s.watch.AddAdvertSent(from, sent.Int64)
			}
		} else if typ == 5 { // GRP_TXT: a channel message
			s.watch.AddChatObservation(tx, h, observer, path)
		} else {
			s.watch.AddObservation(tx, h, observer, path)
		}
		// The signal heard is the last hop's, or the sender's when an advert
		// came straight to the observer.
		if snr.Valid {
			if len(path) > 0 {
				s.watch.AddSignal(h, observer, path[len(path)-1], snr.Float64)
			} else if typ == 4 {
				s.watch.AddSignal(h, observer, from, snr.Float64)
			}
		}
	}
	s.mu.Lock()
	s.hops[0] += shortHops
	s.hops[1] += ambiguous
	s.mu.Unlock()
	return n, last, rows.Err()
}

// nwAmbiguous says whether a short hop prefix resolved to pk could as well
// be another repeater heard lately: one sharing the prefix within
// netWatchTwinKm of it, or any other when pk has no position.
func nwAmbiguous(prefix, pk string, nodes map[string]nwNode, prefixes map[string][]string) bool {
	cands := prefixes[prefix]
	if len(cands) < 2 {
		return false
	}
	me, ok := nodes[pk]
	if !ok || !me.placed {
		return true
	}
	for _, c := range cands {
		if c == pk {
			continue
		}
		if o := nodes[c]; o.placed && haversineKm(me.lat, me.lon, o.lat, o.lon) <= netWatchTwinKm {
			return true
		}
	}
	return false
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
	// Kind is quiet (adverts and relays stopped) or not-relaying (still
	// heard). Chance is how likely its quiet slot was by chance, judged
	// through Observers observers up both now and in its usual.
	Kind string `json:"kind"`
	// Tier is core or local when tiers are on; WindowH the hours Usual and
	// Now cover; HeardEveryH a local repeater's usual longest silence.
	Tier        string   `json:"tier,omitempty"`
	WindowH     int      `json:"windowH,omitempty"`
	HeardEveryH int      `json:"heardEveryH,omitempty"`
	Chance      float64  `json:"chance"`
	Observers   int      `json:"observers"`
	Repeats     int      `json:"repeats"`
	Affects     []string `json:"affects,omitempty"`
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

// NetWatchReach is the share of adverts (or channel messages, by Basis) that
// crossed (0..1), by day.
type NetWatchReach struct {
	Last        float64   `json:"last"`
	Usual       float64   `json:"usual"`
	Basis       string    `json:"basis"` // adverts or chat
	LastAdverts int       `json:"lastAdverts"`
	LastReached int       `json:"lastReached"`
	Daily       []float64 `json:"daily"` // -1 where a day had too few adverts
	// Low and High are the range it usually falls in; Verdict is worse,
	// better, normal or new; Since dates a recent step to its present level
	// (Usual is then the level before).
	Low     float64        `json:"low"`
	High    float64        `json:"high"`
	Verdict string         `json:"verdict"`
	Since   *string        `json:"since,omitempty"`
	Cause   *NetWatchCause `json:"cause,omitempty"`
	Target  float64        `json:"target,omitempty"`
	Burn    string         `json:"burn,omitempty"` // fast, slow or ok against Target
}

// NetWatchCause is the repeater whose crossings fell away on a worse day.
type NetWatchCause struct {
	Node     string  `json:"node"`
	Name     string  `json:"name"`
	Explains float64 `json:"explains"` // share of the drop it accounts for
	Usual    float64 `json:"usual"`
	Last     float64 `json:"last"`
}

// NetWatchReroute is a change of a corridor direction's main carrier.
type NetWatchReroute struct {
	From     string `json:"from"`
	FromName string `json:"fromName"`
	To       string `json:"to"`
	ToName   string `json:"toName"`
	Since    string `json:"since"`
}

func (s *netWatchService) reachResp(r *netwatch.Reach) *NetWatchReach {
	if r == nil {
		return nil
	}
	out := &NetWatchReach{Last: r.Last, Usual: r.Usual, Basis: string(r.Basis), LastAdverts: r.LastAdverts, LastReached: r.LastReached, Daily: r.Daily,
		Low: r.Low, High: r.High, Verdict: r.Verdict, Target: r.Target, Burn: r.Burn}
	if r.Since != nil {
		v := hourISO(*r.Since)
		out.Since = &v
	}
	if c := r.Cause; c != nil {
		out.Cause = &NetWatchCause{Node: c.Node, Name: s.node(c.Node).name, Explains: c.Explains, Usual: c.Usual, Last: c.Last}
	}
	return out
}

// NetWatchCorridorDirection is one direction of a corridor against usual.
type NetWatchCorridorDirection struct {
	From          string            `json:"from"`
	To            string            `json:"to"`
	LastDay       int               `json:"lastDay"`
	UsualDay      int               `json:"usualDay"`
	Daily         []int             `json:"daily"`
	Carriers      []NetWatchCarrier `json:"carriers"`
	ObserversDown []string          `json:"observersDown,omitempty"`
	// Reach is the share of adverts from repeaters in From that reached To.
	Reach   *NetWatchReach   `json:"reach,omitempty"`
	Reroute *NetWatchReroute `json:"reroute,omitempty"`
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
	// Directions are A to B, then B to A.
	Directions []NetWatchCorridorDirection `json:"directions"`
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
	// Reach is the share of adverts passing From that then passed To.
	Reach *NetWatchReach `json:"reach,omitempty"`
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
	Feed         NetWatchFeedStatus       `json:"feed"`
	Tiers        *netwatch.TierCounts     `json:"tiers,omitempty"` // how many judged repeaters are core and local
	Nightly      []NetWatchNightly        `json:"nightly"`
	Drift        []NetWatchDrift          `json:"drift"`
	Events       []NetWatchEvent          `json:"events"` // newest first
}

// NetWatchFeedStatus says whether the packet feed itself has stopped.
type NetWatchFeedStatus struct {
	Down  bool    `json:"down"`
	Since *string `json:"since,omitempty"`
}

// NetWatchNightly is a repeater that goes dark at night; hours are UTC.
type NetWatchNightly struct {
	Node     string  `json:"node"`
	Name     string  `json:"name"`
	DarkFrom int     `json:"darkFrom"`
	DarkTo   int     `json:"darkTo"`
	Nights   int     `json:"nights"`
	Earlier  bool    `json:"earlier"`
	ShiftH   float64 `json:"shiftH"`
}

// NetWatchDrift is a repeater whose signal at an observer has fallen (dB).
type NetWatchDrift struct {
	Node         string  `json:"node"`
	Name         string  `json:"name"`
	Observer     string  `json:"observer"`
	ObserverName string  `json:"observerName"`
	Before       float64 `json:"before"`
	Now          float64 `json:"now"`
	Since        string  `json:"since"`
}

// NetWatchEvent is one line of the timeline, with Text in words.
type NetWatchEvent struct {
	Hour      string  `json:"hour"`
	Kind      string  `json:"kind"` // quiet, not-relaying, back, step, reroute, drift
	Node      string  `json:"node,omitempty"`
	Name      string  `json:"name,omitempty"`
	Other     string  `json:"other,omitempty"`
	OtherName string  `json:"otherName,omitempty"`
	Where     string  `json:"where,omitempty"`
	Before    float64 `json:"before,omitempty"`
	After     float64 `json:"after,omitempty"`
	Text      string  `json:"text"`
}

// NetWatchNodeResponse is GET /api/netwatch/node/{pubkey}: hourly, oldest first.
type NetWatchNodeResponse struct {
	Node     string    `json:"node"`
	Name     string    `json:"name"`
	End      string    `json:"end"`
	Relays   []int     `json:"relays"`
	Adverts  []int     `json:"adverts"`
	Expected []float64 `json:"expected"`
	// AdvertEveryMin is how often it adverts (from the times its adverts
	// carry), AdvertDelivery the share of them heard per day, and Why the
	// evidence it is judged on at End.
	AdvertEveryMin int                  `json:"advertEveryMin"`
	AdvertDelivery []float64            `json:"advertDelivery"`
	Why            netwatch.Explanation `json:"why"`
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
		Usual: i.Usual, Now: i.Now, AdvertEveryH: i.AdvertEveryH, Kind: i.Kind, Chance: i.Chance, Observers: i.Observers,
		Repeats: i.Repeats, Affects: i.Affects, Tier: i.Tier, WindowH: i.WindowH, HeardEveryH: i.HeardEveryH}
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
	writeJSON(w, nw.response())
}

// response is the whole status, with names: the API, the feed and the
// notifications all read it.
func (nw *netWatchService) response() NetWatchResponse {
	st := nw.watch.Status()
	resp := NetWatchResponse{Ready: nw.isReady(), Hour: hourISO(st.Hour), SlotHours: 6, Days: netWatchBaseDays, Judged: st.Judged,
		Open: []NetWatchIncident{}, Recent: []NetWatchIncident{}, Observers: []NetWatchObserver{},
		NetworkNow: st.Network.Now, NetworkUsual: st.Network.Usual, Corridors: []NetWatchCorridorStatus{}, Routes: []NetWatchRouteStatus{},
		Nightly: []NetWatchNightly{}, Drift: []NetWatchDrift{}, Events: []NetWatchEvent{}}
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
		cs.Directions = []NetWatchCorridorDirection{}
		for _, d := range c.Directions {
			nd := NetWatchCorridorDirection{From: d.From, To: d.To, LastDay: d.LastDay, UsualDay: d.UsualDay, Daily: d.Daily,
				Carriers: []NetWatchCarrier{}, ObserversDown: nw.observerNames(d.ObserversDown), Reach: nw.reachResp(d.Reach)}
			for _, k := range d.Carriers {
				nd.Carriers = append(nd.Carriers, NetWatchCarrier{Node: k.Node, Name: nw.node(k.Node).name, Packets: k.Packets})
			}
			if rr := d.Reroute; rr != nil {
				nd.Reroute = &NetWatchReroute{From: rr.From, FromName: nw.node(rr.From).name, To: rr.To, ToName: nw.node(rr.To).name, Since: hourISO(rr.Since)}
			}
			cs.Directions = append(cs.Directions, nd)
		}
		resp.Corridors = append(resp.Corridors, cs)
	}
	for _, rt := range st.Routes {
		resp.Routes = append(resp.Routes, NetWatchRouteStatus{Name: rt.Name, From: rt.From, FromName: nw.node(rt.From).name,
			To: rt.To, ToName: nw.node(rt.To).name, Now: rt.Now, Usual: rt.Usual, LastDay: rt.LastDay, UsualDay: rt.UsualDay, Daily: rt.Daily,
			ObserversDown: nw.observerNames(rt.ObserversDown), Reach: nw.reachResp(rt.Reach)})
	}
	resp.Tiers = st.Tiers
	if st.Feed.Down {
		v := hourISO(st.Feed.Since)
		resp.Feed = NetWatchFeedStatus{Down: true, Since: &v}
	}
	for _, n := range st.Nightly {
		resp.Nightly = append(resp.Nightly, NetWatchNightly{Node: n.Node, Name: nw.node(n.Node).name, DarkFrom: n.DarkFrom, DarkTo: n.DarkTo,
			Nights: n.Nights, Earlier: n.Earlier, ShiftH: n.ShiftH})
	}
	for _, d := range st.Drift {
		resp.Drift = append(resp.Drift, NetWatchDrift{Node: d.Node, Name: nw.node(d.Node).name, Observer: d.Observer,
			ObserverName: nw.observerNames([]string{d.Observer})[0], Before: d.Before, Now: d.Now, Since: hourISO(d.Since)})
	}
	for _, e := range st.Events {
		resp.Events = append(resp.Events, nw.event(e))
	}
	return resp
}

// event names an event's repeaters and says it in words.
func (nw *netWatchService) event(e netwatch.Event) NetWatchEvent {
	out := NetWatchEvent{Hour: hourISO(e.Hour), Kind: e.Kind, Node: e.Node, Other: e.Other, Where: e.Where, Before: e.Before, After: e.After}
	if e.Node != "" {
		out.Name = nw.node(e.Node).name
	}
	if e.Other != "" {
		if e.Kind == "drift" {
			out.OtherName = nw.observerNames([]string{e.Other})[0]
		} else {
			out.OtherName = nw.node(e.Other).name
		}
	}
	pc := func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }
	switch e.Kind {
	case netwatch.KindQuiet:
		out.Text = out.Name + " went quiet"
	case netwatch.KindNotRelaying:
		out.Text = out.Name + " stopped relaying, though it is still heard"
	case netwatch.KindSlowed:
		out.Text = out.Name + " slowed right down"
	case netwatch.KindUnheard:
		out.Text = out.Name + " has not been heard for longer than usual"
	case "back":
		out.Text = out.Name + " is back"
	case "step":
		way := "fell"
		if e.After > e.Before {
			way = "rose"
		}
		out.Text = fmt.Sprintf("%s %s from %s to %s of adverts getting through", e.Where, way, pc(e.Before), pc(e.After))
		if down := nw.observerNames(e.ObserversDown); len(down) > 0 {
			verb := " is"
			if len(down) > 1 {
				verb = " are"
			}
			out.Text += ", while " + strings.Join(down, " and ") + verb + " offline"
		}
	case "reroute":
		out.Text = fmt.Sprintf("%s rerouted from %s to %s", e.Where, out.Name, out.OtherName)
	case "drift":
		out.Text = fmt.Sprintf("%s's signal at %s fell from %.0f dB to %.0f dB", out.Name, out.OtherName, e.Before, e.After)
	default:
		out.Text = e.Kind
	}
	return out
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
		Relays: ser.Relays, Adverts: ser.Adverts, Expected: ser.Expected,
		AdvertEveryMin: ser.AdvertEveryMin, AdvertDelivery: ser.AdvertDelivery, Why: nw.watch.Explain(pk, end)})
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
