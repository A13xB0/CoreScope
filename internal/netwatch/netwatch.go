// Package netwatch learns what normal traffic looks like for every repeater,
// observer, link and corridor of a mesh, from the packets CoreScope already
// hears, and says when one goes quiet.
//
// Everything is kept as hourly counts in fixed rings (a window of hours), fed
// one observation at a time. Hours are absolute: unix seconds / 3600.
//
// A repeater is flagged only when two signs agree: its own adverts (signed
// with its key, so unambiguous) have missed several of its usual gaps, and the
// packets it relays over the last Slot hours have fallen well below the same
// hours of the day on the previous Days days. An observer that goes quiet is
// reported as itself, and the repeaters mostly heard through it are not blamed.
package netwatch

import (
	"sort"
	"sync"
)

// Options tunes the watch. Zero values take the defaults in brackets.
type Options struct {
	WindowHours   int     // hours of history kept [360]
	Slot          int     // hours compared at a time [6]
	Days          int     // days of history a slot is compared with [7]
	MinUsual      int     // a slot must usually hold this many relays to be judged [20]
	QuietRatio    float64 // flagged below this share of usual [0.2]
	BackRatio     float64 // back at or above this share of usual [0.5]
	AdvertGaps    int     // adverts silent for this many of its usual gaps [3]
	MinSilence    int     // and at least this many hours [6]
	MinGaps       int     // advert gaps needed before its rhythm is known [5]
	ObserverShare float64 // a repeater mostly heard through quiet observers is not blamed [0.5]
	Corridors     []Corridor
	Routes        []Route
}

// Route is a named pair of repeaters whose traffic the watch follows: packets
// whose path passes From and later To.
type Route struct {
	Name     string
	From, To string
}

func (o Options) withDefaults() Options {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	deff := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&o.WindowHours, 360)
	def(&o.Slot, 6)
	def(&o.Days, 7)
	def(&o.MinUsual, 20)
	deff(&o.QuietRatio, 0.2)
	deff(&o.BackRatio, 0.5)
	def(&o.AdvertGaps, 3)
	def(&o.MinSilence, 6)
	def(&o.MinGaps, 5)
	deff(&o.ObserverShare, 0.5)
	return o
}

// Zone is a box on the map, in degrees.
type Zone struct {
	Name                           string
	LatMin, LatMax, LonMin, LonMax float64
}

func (z Zone) contains(lat, lon float64) bool {
	return lat >= z.LatMin && lat <= z.LatMax && lon >= z.LonMin && lon <= z.LonMax
}

// Corridor counts packets whose path joins a repeater in A and one in B.
type Corridor struct {
	Name string
	A, B Zone
}

// Locator places a repeater by its key; nil places nothing (no corridors).
type Locator interface {
	Position(pk string) (lat, lon float64, ok bool)
}

// ring holds one count per hour for the window ending at Watch.newest.
type ring []uint16

func slotOf(h int64, n int) int { return int(((h % int64(n)) + int64(n)) % int64(n)) }

func (r ring) at(w *Watch, h int64) uint16 {
	if h > w.newest || h <= w.newest-int64(len(r)) {
		return 0
	}
	return r[slotOf(h, len(r))]
}

func (r ring) add(w *Watch, h int64, n uint16) {
	if h > w.newest || h <= w.newest-int64(len(r)) {
		return
	}
	i := slotOf(h, len(r))
	if r[i] > 0xFFFF-n {
		r[i] = 0xFFFF // saturate rather than wrap
		return
	}
	r[i] += n
}

func (r ring) sum(w *Watch, from, to int64) int {
	s := 0
	for h := from; h <= to; h++ {
		s += int(r.at(w, h))
	}
	return s
}

type node struct {
	relays  ring
	adverts ring
	byObs   map[string]ring
	open    *Incident
}

type corridorState struct {
	Corridor
	count    ring
	carriers map[int64]map[string]int // day -> carrier -> packets
}

type routeState struct {
	Route
	count ring
}

type seenKey struct {
	a, b string
	tx   int64
}

// Watch is safe for concurrent use.
type Watch struct {
	mu        sync.Mutex
	opt       Options
	loc       Locator
	newest    int64
	nodes     map[string]*node
	observers map[string]ring
	links     map[[2]string]ring
	total     ring
	corridors []*corridorState
	routes    []*routeState
	seen      map[int64]map[seenKey]struct{} // hour -> keys already counted
	closed    []Incident
	evaluated int64
}

// New starts an empty watch.
func New(opt Options, loc Locator) *Watch {
	opt = opt.withDefaults()
	w := &Watch{opt: opt, loc: loc, nodes: map[string]*node{}, observers: map[string]ring{},
		links: map[[2]string]ring{}, total: make(ring, opt.WindowHours), seen: map[int64]map[seenKey]struct{}{}}
	for _, c := range opt.Corridors {
		w.corridors = append(w.corridors, &corridorState{Corridor: c, count: make(ring, opt.WindowHours), carriers: map[int64]map[string]int{}})
	}
	for _, r := range opt.Routes {
		w.routes = append(w.routes, &routeState{Route: r, count: make(ring, opt.WindowHours)})
	}
	return w
}

// advance moves the window so it ends at hour h, clearing the hours it enters.
func (w *Watch) advance(h int64) {
	if h <= w.newest {
		return
	}
	n := int64(w.opt.WindowHours)
	from := w.newest + 1
	if w.newest == 0 || h-w.newest >= n {
		from = h - n + 1
	}
	clear := func(r ring) {
		for x := from; x <= h; x++ {
			r[slotOf(x, int(n))] = 0
		}
	}
	for _, nd := range w.nodes {
		clear(nd.relays)
		clear(nd.adverts)
		for _, r := range nd.byObs {
			clear(r)
		}
	}
	for _, r := range w.observers {
		clear(r)
	}
	for _, r := range w.links {
		clear(r)
	}
	clear(w.total)
	for _, r := range w.routes {
		clear(r.count)
	}
	for _, c := range w.corridors {
		clear(c.count)
		for d := range c.carriers {
			if d < (h-n)/24 {
				delete(c.carriers, d)
			}
		}
	}
	w.newest = h
	for hh := range w.seen {
		if hh < h-72 {
			delete(w.seen, hh)
		}
	}
}

func (w *Watch) once(h int64, k seenKey) bool {
	m := w.seen[h]
	if m == nil {
		m = map[seenKey]struct{}{}
		w.seen[h] = m
	}
	if _, ok := m[k]; ok {
		return false
	}
	m[k] = struct{}{}
	return true
}

func (w *Watch) nodeOf(pk string) *node {
	nd := w.nodes[pk]
	if nd == nil {
		nd = &node{relays: make(ring, w.opt.WindowHours), adverts: make(ring, w.opt.WindowHours), byObs: map[string]ring{}}
		w.nodes[pk] = nd
	}
	return nd
}

// AddObservation counts one observation of packet tx, first seen in hour h,
// by observer obs, with the resolved path (repeater keys, first relay first;
// unresolved hops as "").
func (w *Watch) AddObservation(tx, h int64, obs string, path []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance(h)
	if h <= w.newest-int64(w.opt.WindowHours) {
		return
	}
	if w.once(h, seenKey{tx: tx}) {
		w.total.add(w, h, 1)
	}
	if obs != "" && w.once(h, seenKey{a: "\x00obs", b: obs, tx: tx}) {
		r := w.observers[obs]
		if r == nil {
			r = make(ring, w.opt.WindowHours)
			w.observers[obs] = r
		}
		r.add(w, h, 1)
	}
	for i, pk := range path {
		if pk == "" {
			continue
		}
		nd := w.nodeOf(pk)
		if w.once(h, seenKey{a: pk, tx: tx}) {
			nd.relays.add(w, h, 1)
		}
		if obs != "" && w.once(h, seenKey{a: pk, b: "\x00" + obs, tx: tx}) {
			r := nd.byObs[obs]
			if r == nil {
				r = make(ring, w.opt.WindowHours)
				nd.byObs[obs] = r
			}
			r.add(w, h, 1)
		}
		if i+1 < len(path) && path[i+1] != "" && path[i+1] != pk {
			k := [2]string{pk, path[i+1]}
			if w.once(h, seenKey{a: k[0], b: k[1], tx: tx}) {
				r := w.links[k]
				if r == nil {
					r = make(ring, w.opt.WindowHours)
					w.links[k] = r
				}
				r.add(w, h, 1)
			}
		}
	}
	if w.loc != nil && len(w.corridors) > 0 {
		w.countCorridors(tx, h, path)
	}
	for ri, r := range w.routes {
		if passes(path, r.From, r.To) && w.once(h, seenKey{a: "\x00route", b: r.Name, tx: tx + int64(ri)<<48}) {
			r.count.add(w, h, 1)
		}
	}
}

// passes reports whether path visits from and later to.
func passes(path []string, from, to string) bool {
	seenFrom := false
	for _, pk := range path {
		if pk == from {
			seenFrom = true
		} else if seenFrom && pk == to {
			return true
		}
	}
	return false
}

func (w *Watch) countCorridors(tx, h int64, path []string) {
	type placed struct {
		pk       string
		lat, lon float64
	}
	var ps []placed
	for _, pk := range path {
		if pk == "" {
			continue
		}
		if lat, lon, ok := w.loc.Position(pk); ok {
			ps = append(ps, placed{pk, lat, lon})
		}
	}
	for ci, c := range w.corridors {
		inA, inB := false, false
		for _, p := range ps {
			inA = inA || c.A.contains(p.lat, p.lon)
			inB = inB || c.B.contains(p.lat, p.lon)
		}
		if !inA || !inB || !w.once(h, seenKey{a: "\x00cor", b: c.Name, tx: tx + int64(ci)<<48}) {
			continue
		}
		c.count.add(w, h, 1)
		d := h / 24
		m := c.carriers[d]
		if m == nil {
			m = map[string]int{}
			c.carriers[d] = m
		}
		for _, p := range ps {
			if !c.A.contains(p.lat, p.lon) && !c.B.contains(p.lat, p.lon) {
				m[p.pk]++
			}
		}
	}
}

// AddAdvert counts an advert from repeater pk, first seen in hour h.
func (w *Watch) AddAdvert(tx, h int64, pk string) {
	if pk == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance(h)
	if h <= w.newest-int64(w.opt.WindowHours) {
		return
	}
	if w.once(h, seenKey{a: "\x00adv", b: pk, tx: tx}) {
		w.nodeOf(pk).adverts.add(w, h, 1)
	}
}

// Incident is a repeater that went quiet. End is nil while it lasts.
type Incident struct {
	Node         string `json:"node"`
	Start        int64  `json:"start"`   // the hour after its last advert
	Flagged      int64  `json:"flagged"` // the hour the watch raised it
	End          *int64 `json:"end,omitempty"`
	Usual        int    `json:"usual"`        // relays usually in the slot
	Now          int    `json:"now"`          // relays in the slot when flagged
	AdvertEveryH int    `json:"advertEveryH"` // its usual advert gap, hours (90th percentile)
}

func median(a []int) int {
	if len(a) == 0 {
		return 0
	}
	s := append([]int(nil), a...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// slot is the count over the Slot hours ending at t, and the median of the
// same hours on each of the previous Days days.
func (w *Watch) slot(r ring, t int64) (now, usual int) {
	s := int64(w.opt.Slot)
	now = r.sum(w, t-s+1, t)
	prev := make([]int, 0, w.opt.Days)
	for d := int64(1); d <= int64(w.opt.Days); d++ {
		e := t - 24*d
		prev = append(prev, r.sum(w, e-s+1, e))
	}
	return now, median(prev)
}

func (w *Watch) covered(t int64) bool {
	return t-24*int64(w.opt.Days)-int64(w.opt.Slot) > w.newest-int64(w.opt.WindowHours)
}

// advertSilence says whether pk's adverts have been silent at hour t for
// AdvertGaps of its usual gaps, and what that gap is. known is false until it
// has advertised enough to have a rhythm.
func (w *Watch) advertSilence(nd *node, t int64) (silent bool, gap int, last int64, known bool) {
	var hours []int64
	for h := w.newest - int64(w.opt.WindowHours) + 1; h <= t; h++ {
		if nd.adverts.at(w, h) > 0 {
			hours = append(hours, h)
		}
	}
	if len(hours) < w.opt.MinGaps+1 {
		return false, 0, 0, false
	}
	gaps := make([]int, 0, len(hours)-1)
	for i := 1; i < len(hours); i++ {
		gaps = append(gaps, int(hours[i]-hours[i-1]))
	}
	if len(gaps) > 40 {
		gaps = gaps[len(gaps)-40:]
	}
	sort.Ints(gaps)
	gap = gaps[int(0.9*float64(len(gaps)-1))]
	last = hours[len(hours)-1]
	need := int64(w.opt.AdvertGaps * gap)
	if need < int64(w.opt.MinSilence) {
		need = int64(w.opt.MinSilence)
	}
	return t-last >= need, gap, last, true
}

func (w *Watch) quietObservers(t int64) map[string]bool {
	q := map[string]bool{}
	for id, r := range w.observers {
		now, usual := w.slot(r, t)
		if usual >= w.opt.MinUsual && float64(now) <= w.opt.QuietRatio*float64(usual) {
			q[id] = true
		}
	}
	return q
}

// Evaluate judges every repeater at hour t (normally the last complete hour),
// opening and closing incidents. Hours are evaluated in order; earlier hours
// already evaluated are skipped.
func (w *Watch) Evaluate(t int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t <= w.evaluated || t > w.newest || !w.covered(t) {
		if t > w.evaluated && t <= w.newest {
			w.evaluated = t
		}
		return
	}
	w.evaluated = t
	quietObs := w.quietObservers(t)
	s := int64(w.opt.Slot)
	for pk, nd := range w.nodes {
		now, usual := w.slot(nd.relays, t)
		silent, gap, last, known := w.advertSilence(nd, t)
		if nd.open != nil {
			if !silent && float64(now) >= w.opt.BackRatio*float64(nd.open.Usual) {
				end := t
				nd.open.End = &end
				w.closed = append(w.closed, *nd.open)
				nd.open = nil
			}
			continue
		}
		if !known || !silent || usual < w.opt.MinUsual || float64(now) > w.opt.QuietRatio*float64(usual) {
			continue
		}
		// Not blamed when most of what it relays reached us through
		// observers that have gone quiet themselves.
		if len(quietObs) > 0 {
			base, viaQuiet := 0, 0
			from, to := t-24*int64(w.opt.Days)-s+1, t-s
			for obs, r := range nd.byObs {
				n := r.sum(w, from, to)
				base += n
				if quietObs[obs] {
					viaQuiet += n
				}
			}
			if base > 0 && float64(viaQuiet) >= w.opt.ObserverShare*float64(base) {
				continue
			}
		}
		nd.open = &Incident{Node: pk, Start: last + 1, Flagged: t, Usual: usual, Now: now, AdvertEveryH: gap}
	}
	// Forget closed incidents that have left the window.
	keep := w.closed[:0]
	for _, c := range w.closed {
		if *c.End > w.newest-int64(w.opt.WindowHours) {
			keep = append(keep, c)
		}
	}
	w.closed = keep
}

// ObserverStatus is one observer's traffic against its usual.
type ObserverStatus struct {
	ID    string `json:"id"`
	Now   int    `json:"now"`
	Usual int    `json:"usual"`
	Quiet bool   `json:"quiet"`
}

// Carrier is a repeater outside both ends of a corridor that carried its packets.
type Carrier struct {
	Node    string `json:"node"`
	Packets int    `json:"packets"`
}

// CorridorStatus is a corridor's last complete day against its usual day.
type CorridorStatus struct {
	Name     string    `json:"name"`
	A        string    `json:"a"`
	B        string    `json:"b"`
	LastDay  int       `json:"lastDay"`
	UsualDay int       `json:"usualDay"`
	Daily    []int     `json:"daily"`    // packets per day, oldest first, ending with the last complete day
	Carriers []Carrier `json:"carriers"` // on the last complete day, busiest first
}

// RouteStatus is a configured route's traffic against its usual: over the
// last slot, and over the last complete day.
type RouteStatus struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Now      int    `json:"now"`
	Usual    int    `json:"usual"`
	LastDay  int    `json:"lastDay"`
	UsualDay int    `json:"usualDay"`
	Daily    []int  `json:"daily"`
}

// NetworkStatus is all traffic over the last slot against usual.
type NetworkStatus struct {
	Now   int `json:"now"`
	Usual int `json:"usual"`
}

// Status is what the watch shows at its last evaluated hour.
type Status struct {
	Hour      int64            `json:"hour"`
	Judged    int              `json:"judged"`
	Open      []Incident       `json:"open"`
	Recent    []Incident       `json:"recent"` // closed, newest first
	Observers []ObserverStatus `json:"observers"`
	Network   NetworkStatus    `json:"network"`
	Corridors []CorridorStatus `json:"corridors"`
	Routes    []RouteStatus    `json:"routes"`
}

// Status reports the watch at its last evaluated hour.
func (w *Watch) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.evaluated
	st := Status{Hour: t, Open: []Incident{}, Recent: []Incident{}, Observers: []ObserverStatus{}, Corridors: []CorridorStatus{}, Routes: []RouteStatus{}}
	if t == 0 {
		return st
	}
	for _, nd := range w.nodes {
		if _, usual := w.slot(nd.relays, t); usual >= w.opt.MinUsual {
			st.Judged++
		}
		if nd.open != nil {
			st.Open = append(st.Open, *nd.open)
		}
	}
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].Flagged > st.Open[j].Flagged })
	for i := len(w.closed) - 1; i >= 0; i-- {
		st.Recent = append(st.Recent, w.closed[i])
	}
	sort.SliceStable(st.Recent, func(i, j int) bool { return *st.Recent[i].End > *st.Recent[j].End })
	for id, r := range w.observers {
		now, usual := w.slot(r, t)
		st.Observers = append(st.Observers, ObserverStatus{ID: id, Now: now, Usual: usual,
			Quiet: usual >= w.opt.MinUsual && float64(now) <= w.opt.QuietRatio*float64(usual)})
	}
	sort.Slice(st.Observers, func(i, j int) bool { return st.Observers[i].ID < st.Observers[j].ID })
	st.Network.Now, st.Network.Usual = w.slot(w.total, t)
	for _, c := range w.corridors {
		st.Corridors = append(st.Corridors, w.corridorStatus(c, t))
	}
	for _, r := range w.routes {
		rs := RouteStatus{Name: r.Name, From: r.From, To: r.To}
		rs.Now, rs.Usual = w.slot(r.count, t)
		rs.Daily, rs.LastDay, rs.UsualDay = w.daily(r.count, t)
		st.Routes = append(st.Routes, rs)
	}
	return st
}

// daily is a ring's packets per complete day over the window, the last of
// them, and the median of the Days before it.
func (w *Watch) daily(r ring, t int64) (days []int, last, usual int) {
	days = []int{}
	lastDay := (t+1)/24 - 1
	firstDay := (w.newest-int64(w.opt.WindowHours))/24 + 1
	for d := firstDay; d <= lastDay; d++ {
		days = append(days, r.sum(w, d*24, d*24+23))
	}
	if n := len(days); n > 0 {
		last = days[n-1]
		lo := n - 1 - w.opt.Days
		if lo < 0 {
			lo = 0
		}
		usual = median(days[lo : n-1])
	}
	return days, last, usual
}

func (w *Watch) corridorStatus(c *corridorState, t int64) CorridorStatus {
	cs := CorridorStatus{Name: c.Name, A: c.A.Name, B: c.B.Name, Carriers: []Carrier{}}
	lastDay := (t+1)/24 - 1 // the last complete day
	cs.Daily, cs.LastDay, cs.UsualDay = w.daily(c.count, t)
	for pk, n := range c.carriers[lastDay] {
		cs.Carriers = append(cs.Carriers, Carrier{Node: pk, Packets: n})
	}
	sort.Slice(cs.Carriers, func(i, j int) bool {
		if cs.Carriers[i].Packets != cs.Carriers[j].Packets {
			return cs.Carriers[i].Packets > cs.Carriers[j].Packets
		}
		return cs.Carriers[i].Node < cs.Carriers[j].Node
	})
	if len(cs.Carriers) > 5 {
		cs.Carriers = cs.Carriers[:5]
	}
	return cs
}

// Series is hourly traffic for a chart, oldest first, ending at the hour asked.
type Series struct {
	End      int64     `json:"end"`
	Relays   []int     `json:"relays"`
	Adverts  []int     `json:"adverts"`
	Expected []float64 `json:"expected"` // median of the same hour on the previous Days days; -1 where unknown
}

func (w *Watch) series(r ring, end int64) ([]int, []float64) {
	n := w.opt.WindowHours
	out, exp := make([]int, n), make([]float64, n)
	for i := 0; i < n; i++ {
		h := end - int64(n-1-i)
		out[i] = int(r.at(w, h))
		if h-24*int64(w.opt.Days) <= w.newest-int64(n) {
			exp[i] = -1
			continue
		}
		prev := make([]int, 0, w.opt.Days)
		for d := int64(1); d <= int64(w.opt.Days); d++ {
			prev = append(prev, int(r.at(w, h-24*d)))
		}
		exp[i] = float64(median(prev))
	}
	return out, exp
}

// NodeSeries is a repeater's hourly relays and adverts over the window ending at end.
func (w *Watch) NodeSeries(pk string, end int64) Series {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Series{End: end}
	nd := w.nodes[pk]
	if nd == nil {
		nd = &node{relays: make(ring, w.opt.WindowHours), adverts: make(ring, w.opt.WindowHours)}
	}
	s.Relays, s.Expected = w.series(nd.relays, end)
	s.Adverts, _ = w.series(nd.adverts, end)
	return s
}

// Hop is one link on a probed route, against its usual.
type Hop struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Now      int       `json:"now"`   // packets over the last slot
	Usual    int       `json:"usual"` // usually in that slot
	Quiet    bool      `json:"quiet"`
	Series   []int     `json:"series"` // the last 72 hours, oldest first
	Expected []float64 `json:"expected"`
}

// ProbeResult is the usual route between two repeaters, hop by hop.
type ProbeResult struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Hour  int64    `json:"hour"`
	Route []string `json:"route"`
	Hops  []Hop    `json:"hops"`
	Note  string   `json:"note,omitempty"`
}

// Probe finds the route packets usually take from a to b (judged on the week
// before the last slot, so a broken hop does not hide its own route) and
// reports every hop against its usual. The route maximises its weakest hop's
// usual traffic, then prefers fewer hops.
func (w *Watch) Probe(a, b string, t int64) ProbeResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	res := ProbeResult{From: a, To: b, Hour: t, Route: []string{}, Hops: []Hop{}}
	s := int64(w.opt.Slot)
	from, to := t-24*int64(w.opt.Days)-s+1, t-s
	weight := map[[2]string]int{}
	adj := map[string][]string{}
	for k, r := range w.links {
		if n := r.sum(w, from, to); n >= w.opt.Days { // at least about once a day
			weight[k] = n
			adj[k[0]] = append(adj[k[0]], k[1])
		}
	}
	// Widest path (maximise the minimum weight), ties to fewer hops.
	type state struct {
		width, hops int
		prev        string
	}
	best := map[string]state{a: {width: 1 << 62}}
	done := map[string]bool{}
	for {
		cur, ok := "", false
		for n, st := range best {
			if done[n] {
				continue
			}
			if !ok || st.width > best[cur].width || (st.width == best[cur].width && st.hops < best[cur].hops) {
				cur, ok = n, true
			}
		}
		if !ok || cur == b {
			break
		}
		done[cur] = true
		for _, nx := range adj[cur] {
			wdt := best[cur].width
			if e := weight[[2]string{cur, nx}]; e < wdt {
				wdt = e
			}
			st, seen := best[nx]
			if !seen || wdt > st.width || (wdt == st.width && best[cur].hops+1 < st.hops) {
				best[nx] = state{width: wdt, hops: best[cur].hops + 1, prev: cur}
			}
		}
	}
	if _, ok := best[b]; !ok || a == b {
		res.Note = "No usual route between these two in the last week: packets seen in the paths never join them."
		return res
	}
	for n := b; ; n = best[n].prev {
		res.Route = append([]string{n}, res.Route...)
		if n == a {
			break
		}
	}
	for i := 0; i+1 < len(res.Route); i++ {
		k := [2]string{res.Route[i], res.Route[i+1]}
		r := w.links[k]
		now, usual := w.slot(r, t)
		ser, exp := w.series(r, t)
		if len(ser) > 72 {
			ser, exp = ser[len(ser)-72:], exp[len(exp)-72:]
		}
		res.Hops = append(res.Hops, Hop{From: k[0], To: k[1], Now: now, Usual: usual,
			Quiet:  usual >= w.opt.MinUsual && float64(now) <= w.opt.QuietRatio*float64(usual),
			Series: ser, Expected: exp})
	}
	return res
}

// Prune drops repeaters, observers and links with nothing left in the window.
func (w *Watch) Prune() {
	w.mu.Lock()
	defer w.mu.Unlock()
	lo, hi := w.newest-int64(w.opt.WindowHours)+1, w.newest
	for pk, nd := range w.nodes {
		if nd.open == nil && nd.relays.sum(w, lo, hi) == 0 && nd.adverts.sum(w, lo, hi) == 0 {
			delete(w.nodes, pk)
		}
	}
	for k, r := range w.links {
		if r.sum(w, lo, hi) == 0 {
			delete(w.links, k)
		}
	}
	for k, r := range w.observers {
		if r.sum(w, lo, hi) == 0 {
			delete(w.observers, k)
		}
	}
}

// Newest is the newest hour the watch holds.
func (w *Watch) Newest() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.newest
}
