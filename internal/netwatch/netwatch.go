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
	"fmt"
	"sort"
	"sync"
)

// Options tunes the watch. Zero values take the defaults in brackets.
type Options struct {
	WindowHours   int     // hours of history kept [360]
	Slot          int     // hours compared at a time [6]
	Days          int     // days of history a slot is compared with [7]
	MinDays       int     // judging starts once this many of them are in the window [7, or Days if fewer]
	MinUsual      int     // a slot must usually hold this many relays to be judged [20]
	QuietRatio    float64 // flagged below this share of usual [0.2]
	BackRatio     float64 // back at or above this share of usual [0.5]
	AdvertGaps    int     // adverts silent for this many of its usual gaps [3]
	MinSilence    int     // and at least this many hours [6]
	MinGaps       int     // advert gaps needed before its rhythm is known [5]
	ObserverShare float64 // a repeater mostly heard through quiet observers is not blamed [0.5]
	Corridors     []Corridor
	Routes        []Route
	// Area, when set, limits judging to repeaters placed inside it (by the
	// Locator); repeaters outside it are still counted but never flagged.
	Area *Zone
	// Tiers, when set, also judges core repeaters over a few hours and
	// local ones on how long they have gone unheard.
	Tiers *Tiers
}

// Route is a named pair of repeaters whose traffic the watch follows: packets
// whose path passes From and later To.
type Route struct {
	Name     string
	From, To string
	Basis    Basis
	Target   float64 // optional share of adverts that should get through, 0..1
}

// Basis is the steady traffic a corridor or route's reach is judged on.
type Basis string

const (
	// BasisAdverts (the default) follows adverts from their repeater.
	BasisAdverts Basis = "adverts"
	// BasisChat follows channel messages from the first repeater that
	// relayed them: for links a neighbouring network no longer floods
	// adverts across.
	BasisChat Basis = "chat"
)

func (b Basis) orDefault() Basis {
	if b == BasisChat {
		return b
	}
	return BasisAdverts
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
	def(&o.MinDays, 7)
	if o.MinDays > o.Days {
		o.MinDays = o.Days
	}
	def(&o.MinUsual, 20)
	deff(&o.QuietRatio, 0.2)
	deff(&o.BackRatio, 0.5)
	def(&o.AdvertGaps, 3)
	def(&o.MinSilence, 6)
	def(&o.MinGaps, 5)
	deff(&o.ObserverShare, 0.5)
	return o
}

// Zone is an area on the map: a polygon of [lat, lon] points when it has
// three or more, else a box, in degrees.
type Zone struct {
	Name                           string
	LatMin, LatMax, LonMin, LonMax float64
	Polygon                        [][2]float64
}

func (z Zone) contains(lat, lon float64) bool {
	if len(z.Polygon) >= 3 {
		in := false
		p := z.Polygon
		for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
			if (p[i][0] > lat) != (p[j][0] > lat) && lon < (p[j][1]-p[i][1])*(lat-p[i][0])/(p[j][0]-p[i][0])+p[i][1] {
				in = !in
			}
		}
		return in
	}
	return lat >= z.LatMin && lat <= z.LatMax && lon >= z.LonMin && lon <= z.LonMax
}

// Corridor counts packets whose path joins a repeater in A and one in B.
type Corridor struct {
	Name   string
	A, B   Zone
	Basis  Basis
	Target float64 // optional share each way that should get through, 0..1
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
	gray    int     // consecutive hours it has looked like it stopped relaying
	sent    []int64 // the send times its adverts carry, sorted, unix seconds
}

type corridorState struct {
	Corridor
	count    ring
	carriers map[int64]map[string]int // day -> carrier -> packets
	dir      [2]*corridorDir          // A to B, B to A
}

// corridorDir is one direction of a corridor: packets whose path reaches the
// zone it starts from before the other.
type corridorDir struct {
	count    ring
	carriers map[int64]map[string]int
	byObs    map[string]ring
	reach    reach
}

// reach counts adverts that could cross (den) and those that did (num), and
// by day the repeaters the ones that crossed went through (via).
type reach struct {
	den, num ring
	via      map[int64]map[string]int // the repeaters crossings went through
	denVia   map[int64]map[string]int // the repeaters every advert went through
}

func newReach(n int) reach {
	return reach{den: make(ring, n), num: make(ring, n), via: map[int64]map[string]int{}, denVia: map[int64]map[string]int{}}
}

// countVia counts, once per packet, each repeater on path (but its origin)
// in m for the day of hour h; key keeps corridors and routes apart.
func (w *Watch) countVia(m map[int64]map[string]int, h, tx int64, key, origin string, path []string) {
	day := m[h/24]
	for _, pk := range path {
		if pk == "" || pk == origin || !w.once(h, seenKey{a: key, b: pk, tx: tx}) {
			continue
		}
		if day == nil {
			day = map[string]int{}
			m[h/24] = day
		}
		day[pk]++
	}
}

func (r reach) prune(before int64) {
	for _, m := range []map[int64]map[string]int{r.via, r.denVia} {
		for d := range m {
			if d < before {
				delete(m, d)
			}
		}
	}
}

type routeState struct {
	Route
	count ring
	byObs map[string]ring
	reach reach
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
	feed      FeedStatus
	signal    map[[2]string]*snrDays // [repeater, observer]
}

// New starts an empty watch.
func New(opt Options, loc Locator) *Watch {
	opt = opt.withDefaults()
	w := &Watch{opt: opt, loc: loc, nodes: map[string]*node{}, observers: map[string]ring{},
		links: map[[2]string]ring{}, total: make(ring, opt.WindowHours), seen: map[int64]map[seenKey]struct{}{},
		signal: map[[2]string]*snrDays{}}
	for _, c := range opt.Corridors {
		cs := &corridorState{Corridor: c, count: make(ring, opt.WindowHours), carriers: map[int64]map[string]int{}}
		for i := range cs.dir {
			cs.dir[i] = &corridorDir{count: make(ring, opt.WindowHours), carriers: map[int64]map[string]int{}, byObs: map[string]ring{}, reach: newReach(opt.WindowHours)}
		}
		w.corridors = append(w.corridors, cs)
	}
	for _, r := range opt.Routes {
		w.routes = append(w.routes, &routeState{Route: r, count: make(ring, opt.WindowHours), byObs: map[string]ring{}, reach: newReach(opt.WindowHours)})
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
		clear(r.reach.den)
		clear(r.reach.num)
		r.reach.prune((h - n) / 24)
		for _, o := range r.byObs {
			clear(o)
		}
	}
	for _, c := range w.corridors {
		clear(c.count)
		for d := range c.carriers {
			if d < (h-n)/24 {
				delete(c.carriers, d)
			}
		}
		for _, cd := range c.dir {
			clear(cd.count)
			clear(cd.reach.den)
			clear(cd.reach.num)
			cd.reach.prune((h - n) / 24)
			for _, r := range cd.byObs {
				clear(r)
			}
			for d := range cd.carriers {
				if d < (h-n)/24 {
					delete(cd.carriers, d)
				}
			}
		}
	}
	w.newest = h
	// A packet heard again more than a day after it was first seen is rare
	// (0.15% of observations, the same at six hours as at a day); keeping a
	// day of keys rather than three halves the watch's memory.
	for hh := range w.seen {
		if hh < h-24 {
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
	w.addObservation(tx, h, obs, path)
}

// AddAdvertObservation counts one observation of an advert from repeater
// from: as an observation, as one of from's adverts, and towards the advert
// reach of the corridors and routes it could cross.
func (w *Watch) AddAdvertObservation(tx, h int64, obs, from string, path []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.addObservation(tx, h, obs, path)
	if from == "" || h <= w.newest-int64(w.opt.WindowHours) {
		return
	}
	if w.once(h, seenKey{a: "\x00adv", b: from, tx: tx}) {
		w.nodeOf(from).adverts.add(w, h, 1)
	}
	// Reach counts adverts that entered the mesh: an observation with no
	// relay on its path says nothing about what the advert could cross (the
	// same advert relayed elsewhere is counted through that observation).
	relayed := false
	for _, pk := range path {
		relayed = relayed || pk != ""
	}
	if relayed {
		w.countReach(tx, h, BasisAdverts, from, path)
	}
}

// AddChatObservation counts one observation of a channel message: as an
// observation, and towards the reach of chat-based corridors and routes,
// taking the first repeater that relayed it as where it came from.
func (w *Watch) AddChatObservation(tx, h int64, obs string, path []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.addObservation(tx, h, obs, path)
	if len(path) == 0 || path[0] == "" || h <= w.newest-int64(w.opt.WindowHours) {
		return
	}
	w.countReach(tx, h, BasisChat, path[0], path)
}

// countReach counts a packet that came from origin towards the reach of the
// corridors and routes judged on basis.
func (w *Watch) countReach(tx, h int64, basis Basis, origin string, path []string) {
	if w.loc != nil {
		if lat, lon, ok := w.loc.Position(origin); ok {
			for ci, c := range w.corridors {
				if c.Basis.orDefault() != basis {
					continue
				}
				for di, zones := range [2][2]Zone{{c.A, c.B}, {c.B, c.A}} {
					if !zones[0].contains(lat, lon) {
						continue
					}
					cd := c.dir[di]
					key := fmt.Sprint(ci, "/", di)
					if w.once(h, seenKey{a: "\x00reach-den", b: key, tx: tx}) {
						cd.reach.den.add(w, h, 1)
					}
					// Only the path up to where it first enters the far zone
					// says anything about the crossing; it floods on past it.
					at := w.pathEnters(path, zones[1])
					seg := path
					if at >= 0 {
						seg = path[:at+1]
					}
					w.countVia(cd.reach.denVia, h, tx, "\x00cvia-den"+key, origin, seg)
					if at >= 0 {
						if w.once(h, seenKey{a: "\x00reach-num", b: key, tx: tx}) {
							cd.reach.num.add(w, h, 1)
						}
						w.countVia(cd.reach.via, h, tx, "\x00cvia-num"+key, origin, seg)
					}
				}
			}
		}
	}
	for ri, r := range w.routes {
		if r.Basis.orDefault() != basis {
			continue
		}
		via := origin == r.From
		for _, pk := range path {
			via = via || pk == r.From
		}
		if !via {
			continue
		}
		key := fmt.Sprint(ri)
		if w.once(h, seenKey{a: "\x00rreach-den", b: key, tx: tx}) {
			r.reach.den.add(w, h, 1)
		}
		// The stretch from From to To is what the route is about.
		seg, reached := routeStretch(path, origin, r.From, r.To)
		w.countVia(r.reach.denVia, h, tx, "\x00rvia-den"+key, origin, seg)
		if reached {
			if w.once(h, seenKey{a: "\x00rreach-num", b: key, tx: tx}) {
				r.reach.num.add(w, h, 1)
			}
			w.countVia(r.reach.via, h, tx, "\x00rvia-num"+key, origin, seg)
		}
	}
}

func (w *Watch) pathReaches(path []string, z Zone) bool { return w.pathEnters(path, z) >= 0 }

// pathEnters is the index of the first repeater on path inside z, or -1.
func (w *Watch) pathEnters(path []string, z Zone) int {
	for i, pk := range path {
		if pk == "" {
			continue
		}
		if lat, lon, ok := w.loc.Position(pk); ok && z.contains(lat, lon) {
			return i
		}
	}
	return -1
}

// routeStretch is the part of path from from (or its start, when the packet
// came from from) up to to, and whether it got there.
func routeStretch(path []string, origin, from, to string) ([]string, bool) {
	start := -1
	if origin == from {
		start = 0
	}
	for i, pk := range path {
		if start < 0 && pk == from {
			start = i
		} else if start >= 0 && pk == to {
			return path[start : i+1], true
		}
	}
	if start < 0 {
		return nil, false
	}
	return path[start:], false
}

func (w *Watch) addObservation(tx, h int64, obs string, path []string) {
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
		w.countCorridors(tx, h, obs, path)
	}
	for ri, r := range w.routes {
		if !passes(path, r.From, r.To) {
			continue
		}
		if w.once(h, seenKey{a: "\x00route", b: r.Name, tx: tx + int64(ri)<<48}) {
			r.count.add(w, h, 1)
		}
		if obs != "" && w.once(h, seenKey{a: "\x00route-obs" + r.Name, b: obs, tx: tx}) {
			o := r.byObs[obs]
			if o == nil {
				o = make(ring, w.opt.WindowHours)
				r.byObs[obs] = o
			}
			o.add(w, h, 1)
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

func (w *Watch) countCorridors(tx, h int64, obs string, path []string) {
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
	bump := func(m map[int64]map[string]int, d int64, c *corridorState) {
		day := m[d]
		if day == nil {
			day = map[string]int{}
			m[d] = day
		}
		for _, p := range ps {
			if !c.A.contains(p.lat, p.lon) && !c.B.contains(p.lat, p.lon) {
				day[p.pk]++
			}
		}
	}
	for ci, c := range w.corridors {
		firstA, firstB := -1, -1
		for i, p := range ps {
			if firstA < 0 && c.A.contains(p.lat, p.lon) {
				firstA = i
			}
			if firstB < 0 && c.B.contains(p.lat, p.lon) {
				firstB = i
			}
		}
		if firstA < 0 || firstB < 0 {
			continue
		}
		key := tx + int64(ci)<<48
		if w.once(h, seenKey{a: "\x00cor", b: c.Name, tx: key}) {
			c.count.add(w, h, 1)
			bump(c.carriers, h/24, c)
		}
		di := 0
		if firstB < firstA {
			di = 1
		}
		cd := c.dir[di]
		if w.once(h, seenKey{a: "\x00cor-dir", b: fmt.Sprint(c.Name, di), tx: key}) {
			cd.count.add(w, h, 1)
			bump(cd.carriers, h/24, c)
		}
		if obs != "" && w.once(h, seenKey{a: "\x00cor-obs" + fmt.Sprint(c.Name, di), b: obs, tx: tx}) {
			r := cd.byObs[obs]
			if r == nil {
				r = make(ring, w.opt.WindowHours)
				cd.byObs[obs] = r
			}
			r.add(w, h, 1)
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
	Kind         string `json:"kind"`    // KindQuiet or KindNotRelaying
	Start        int64  `json:"start"`   // the hour after its last advert (or last relay)
	Flagged      int64  `json:"flagged"` // the hour the watch raised it
	End          *int64 `json:"end,omitempty"`
	Usual        int    `json:"usual"`        // relays usually in the slot
	Now          int    `json:"now"`          // relays in the slot when flagged
	AdvertEveryH int    `json:"advertEveryH"` // its usual advert gap, hours (90th percentile)
	// Tier is core or local when tiers are on; WindowH the hours Usual and
	// Now cover (Slot when 0); HeardEveryH a local repeater's usual longest
	// silence.
	Tier        string `json:"tier,omitempty"`
	WindowH     int    `json:"windowH,omitempty"`
	HeardEveryH int    `json:"heardEveryH,omitempty"`
	// Chance is how likely the quiet slot was by chance, judged through
	// Observers observers that were up both now and in the usual.
	Chance    float64 `json:"chance"`
	Observers int     `json:"observers"`
	Repeats   int     `json:"repeats"` // the how-many-th time in the window it went quiet
	// Affects names the corridor directions and routes it explains (it is
	// their likely cause, or the carrier they were rerouted from).
	Affects []string `json:"affects,omitempty"`
	// matchedUsual is the like-for-like usual at flag time, for closing.
	matchedUsual int
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
func (w *Watch) slot(r ring, t int64) (now, usual int) { return w.slotN(r, t, w.opt.Slot) }

// slotN is slot over hours hours rather than Slot.
func (w *Watch) slotN(r ring, t int64, hours int) (now, usual int) {
	s := int64(hours)
	now = r.sum(w, t-s+1, t)
	prev := make([]int, 0, w.opt.Days)
	for d := int64(1); d <= int64(w.baseDays(t)); d++ {
		e := t - 24*d
		prev = append(prev, r.sum(w, e-s+1, e))
	}
	return now, median(prev)
}

// baseDays is how many of the Days before t have their slot inside the
// window, so a watch that has just started compares with what it has.
func (w *Watch) baseDays(t int64) int {
	n := 0
	for d := 1; d <= w.opt.Days; d++ {
		if t-24*int64(d)-int64(w.opt.Slot) < w.newest-int64(w.opt.WindowHours) {
			break
		}
		n++
	}
	return n
}

// covered says whether hour t has enough history to be judged. Judging from
// MinDays rather than Days means a watch that has just filled has already
// recorded the incidents of its first weeks, and keeps them out of the
// baseline.
func (w *Watch) covered(t int64) bool { return w.baseDays(t) >= w.opt.MinDays }

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

// judged says whether pk is inside the area (or there is no area, or pk
// cannot be placed).
func (w *Watch) judged(pk string) bool {
	if w.opt.Area == nil || w.loc == nil {
		return true
	}
	lat, lon, ok := w.loc.Position(pk)
	return !ok || w.opt.Area.contains(lat, lon)
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

// observersDown names the quiet observers that, over the baseline before
// the slot ending at t, heard at least ObserverShare of what byObs counts.
// Traffic seen mostly through them says nothing about the mesh.
func (w *Watch) observersDown(byObs map[string]ring, t int64, quiet map[string]bool) []string {
	if len(quiet) == 0 || len(byObs) == 0 {
		return nil
	}
	s := int64(w.opt.Slot)
	from, to := t-24*int64(w.opt.Days)-s+1, t-s
	base, viaQuiet := 0, 0
	var names []string
	for obs, r := range byObs {
		n := r.sum(w, from, to)
		base += n
		if quiet[obs] && n > 0 {
			viaQuiet += n
			names = append(names, obs)
		}
	}
	if base == 0 || float64(viaQuiet) < w.opt.ObserverShare*float64(base) {
		return nil
	}
	sort.Strings(names)
	return names
}

// Evaluate judges every repeater at hour t (normally the last complete hour),
// opening and closing incidents. Hours are evaluated in order; earlier hours
// already evaluated are skipped. An hour with no packets at all is still
// judged: that silence is the news.
//
// A repeater is flagged when, through the observers up both now and in its
// usual, its last Slot hours would happen by chance less than once in
// 1/surpriseChance and hold at most QuietRatio of usual; and either its
// adverts have stopped too (quiet) or they still arrive while its neighbours
// carry on (not relaying).
func (w *Watch) Evaluate(t int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t > w.newest {
		w.advance(t)
	}
	if t <= w.evaluated || !w.covered(t) {
		if t > w.evaluated {
			w.evaluated = t
		}
		return
	}
	w.evaluated = t
	quietObs := w.quietObservers(t)
	w.checkFeed(t, quietObs)
	for pk, nd := range w.nodes {
		now, usual := w.slot(nd.relays, t)
		if nd.open != nil {
			// Still relaying nothing (or slowed, or unheard) and now its
			// adverts have stopped too: it has gone quiet altogether.
			if k := nd.open.Kind; k == KindNotRelaying || k == KindSlowed || k == KindUnheard {
				if silent, _, last, known := w.advertSilence(nd, t); known && silent {
					nd.open.Kind = KindQuiet
					nd.open.Start = min(nd.open.Start, last+1)
				}
			}
			// Back once it relays half of what was usual when it was flagged,
			// over the same hours, counted as it was then or like for like; a
			// local repeater once it is heard at all. Adverts are not required:
			// a repeater far from every observer may have them heard only now
			// and then.
			win := nd.open.WindowH
			if win == 0 {
				win = w.opt.Slot
			}
			wnow, _ := w.slotN(nd.relays, t, win)
			back := float64(wnow) >= w.opt.BackRatio*float64(nd.open.Usual)
			if nd.open.Tier == TierLocal {
				back = nd.relays.at(w, t) > 0 || (nd.open.Kind == KindUnheard && nd.adverts.at(w, t) > 0)
			}
			if !back && nd.open.matchedUsual > 0 {
				m := w.matchedN(nd.byObs, t, quietObs, pk, win)
				back = float64(m.now) >= w.opt.BackRatio*float64(nd.open.matchedUsual)
			}
			if back {
				end := t
				nd.open.End = &end
				w.closed = append(w.closed, *nd.open)
				nd.open = nil
			}
			continue
		}
		gray := nd.gray
		nd.gray = 0 // kept only by an hour that still looks like it
		if w.opt.Tiers != nil && !w.feed.Down && w.judged(pk) {
			if inc := w.judgeTier(pk, nd, t, quietObs); inc != nil {
				if n, ok := w.nightly(nd, t); !ok || !n.inItsNight(inc.Start, t) {
					if silent, _, last, known := w.advertSilence(nd, t); known && silent {
						inc.Kind, inc.Start = KindQuiet, min(inc.Start, last+1)
					}
					inc.Repeats = w.repeats(pk)
					nd.open = inc
					continue
				}
			}
		}
		// Cheap screen first: most repeaters are nowhere near quiet.
		if w.feed.Down || usual == 0 || float64(now) > 0.5*float64(usual) || !w.judged(pk) {
			continue
		}
		m := w.matched(nd.byObs, t, quietObs, pk)
		if m.observers == 0 || float64(m.now) > w.opt.QuietRatio*float64(m.usual) {
			continue
		}
		p := quietChance(usual, m)
		if p >= surpriseChance {
			continue
		}
		silent, gap, last, known := w.advertSilence(nd, t)
		inc := &Incident{Node: pk, Flagged: t, Usual: usual, Now: now, AdvertEveryH: gap,
			Chance: p, Observers: m.observers, matchedUsual: m.usual}
		switch {
		case known && silent:
			inc.Kind, inc.Start = KindQuiet, last+1
		case known && p < notRelayingChance && usual >= notRelayingMinUsual && m.observers >= 2 &&
			w.advertsDuring(nd, t, gap) && w.neighboursCarryOn(pk, t, quietObs):
			// One sign only, so it must hold for notRelayingHours in a row.
			if nd.gray = gray + 1; nd.gray < notRelayingHours {
				continue
			}
			inc.Kind, inc.Start = KindNotRelaying, w.quietSince(nd.relays, t-int64(nd.gray)+1, usual, w.opt.Slot)
		default:
			continue
		}
		if n, ok := w.nightly(nd, t); ok && n.inItsNight(inc.Start, t) {
			continue
		}
		inc.Repeats = w.repeats(pk)
		nd.open = inc
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

// Reach is the share of adverts that crossed, by day. Days with fewer than
// minReachAdverts adverts that could cross are left out (-1 in Daily).
type Reach struct {
	Last        float64   `json:"last"`        // the last complete day, 0..1
	Usual       float64   `json:"usual"`       // median of the previous Days days that have enough adverts
	Basis       Basis     `json:"basis"`       // what was counted: adverts, or channel messages
	LastAdverts int       `json:"lastAdverts"` // adverts (or messages) that could cross on the last day
	LastReached int       `json:"lastReached"`
	Daily       []float64 `json:"daily"`
	// Low and High are the range it usually falls in on a day like the last
	// one; Verdict is worse, better, normal or new.
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	Verdict string  `json:"verdict"`
	// Since is the first hour of the day it stepped to its present level,
	// when that is recent; Usual is then the level before.
	Since *int64 `json:"since,omitempty"`
	// Cause is the repeater whose crossings fell away, when one explains
	// most of a worse day.
	Cause *Cause `json:"cause,omitempty"`
	// Target, when set, and how fast the recent hours are burning through
	// it: fast (the last 6 and 24 hours both under), slow (24 and 72), ok.
	Target float64 `json:"target,omitempty"`
	Burn   string  `json:"burn,omitempty"`
	// candidates for Cause, most of the drop first; one that has itself gone
	// quiet is preferred (see preferQuiet).
	candidates []Cause
}

// CorridorDirection is one direction of a corridor, against its usual.
type CorridorDirection struct {
	From          string    `json:"from"`
	To            string    `json:"to"`
	LastDay       int       `json:"lastDay"`
	UsualDay      int       `json:"usualDay"`
	Daily         []int     `json:"daily"`
	Carriers      []Carrier `json:"carriers"`
	ObserversDown []string  `json:"observersDown,omitempty"`
	// Reach is the share of adverts from repeaters in From that reached To.
	Reach *Reach `json:"reach,omitempty"`
	// Reroute is set when its main carrier changed and the old one fell away.
	Reroute *Reroute `json:"reroute,omitempty"`
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
	// Directions are A to B, then B to A.
	Directions []CorridorDirection `json:"directions"`
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
	// ObserversDown names quiet observers that usually hear most of it: a
	// drop then says nothing about the mesh.
	ObserversDown []string `json:"observersDown,omitempty"`
	// Reach is the share of adverts passing From that then passed To.
	Reach *Reach `json:"reach,omitempty"`
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
	Feed      FeedStatus       `json:"feed"`
	Tiers     *TierCounts      `json:"tiers,omitempty"`
	Nightly   []Nightly        `json:"nightly"`
	Drift     []Drift          `json:"drift"`
	Events    []Event          `json:"events"` // newest first
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
	for pk, nd := range w.nodes {
		if _, usual := w.slot(nd.relays, t); judgeable(usual) && w.judged(pk) {
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
	st.Feed = w.feed
	if w.opt.Tiers != nil {
		c := w.tierCounts(t)
		st.Tiers = &c
	}
	st.Nightly = w.nightlyList(t)
	for _, c := range w.corridors {
		st.Corridors = append(st.Corridors, w.corridorStatus(c, t))
	}
	quiet := w.quietObservers(t)
	for _, r := range w.routes {
		rs := RouteStatus{Name: r.Name, From: r.From, To: r.To, ObserversDown: w.observersDown(r.byObs, t, quiet)}
		rs.Now, rs.Usual = w.slot(r.count, t)
		rs.Daily, rs.LastDay, rs.UsualDay = w.daily(r.count, t)
		rs.Reach = w.reachStatus(r.reach, r.Basis, r.Target, t)
		st.Routes = append(st.Routes, rs)
	}
	st.Drift = w.drifts(t)
	w.explainedBy(&st)
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

func topCarriers(m map[string]int) []Carrier {
	out := []Carrier{}
	for pk, n := range m {
		out = append(out, Carrier{Node: pk, Packets: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Packets != out[j].Packets {
			return out[i].Packets > out[j].Packets
		}
		return out[i].Node < out[j].Node
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func (w *Watch) corridorStatus(c *corridorState, t int64) CorridorStatus {
	cs := CorridorStatus{Name: c.Name, A: c.A.Name, B: c.B.Name}
	lastDay := (t+1)/24 - 1 // the last complete day
	cs.Daily, cs.LastDay, cs.UsualDay = w.daily(c.count, t)
	cs.Carriers = topCarriers(c.carriers[lastDay])
	quiet := w.quietObservers(t)
	names := [2][2]string{{c.A.Name, c.B.Name}, {c.B.Name, c.A.Name}}
	for i, cd := range c.dir {
		d := CorridorDirection{From: names[i][0], To: names[i][1], ObserversDown: w.observersDown(cd.byObs, t, quiet)}
		d.Daily, d.LastDay, d.UsualDay = w.daily(cd.count, t)
		d.Carriers = topCarriers(cd.carriers[lastDay])
		d.Reach = w.reachStatus(cd.reach, c.Basis, c.Target, t)
		d.Reroute = w.reroute(cd.carriers, t)
		cs.Directions = append(cs.Directions, d)
	}
	return cs
}

// Series is hourly traffic for a chart, oldest first, ending at the hour asked.
type Series struct {
	End      int64     `json:"end"`
	Relays   []int     `json:"relays"`
	Adverts  []int     `json:"adverts"`
	Expected []float64 `json:"expected"` // median of the same hour on the previous Days days; -1 where unknown
	// AdvertEveryMin is how often it adverts, from the times its adverts
	// carry; AdvertDelivery the share of them heard, per complete day.
	AdvertEveryMin int       `json:"advertEveryMin"`
	AdvertDelivery []float64 `json:"advertDelivery"`
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
	every, daily := w.advertDelivery(nd, end)
	s.AdvertEveryMin, s.AdvertDelivery = int(every/60), daily
	return s
}

// Hop is one link on a probed route, against its usual.
type Hop struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Now   int    `json:"now"`   // packets over the last slot
	Usual int    `json:"usual"` // usually in that slot
	Quiet bool   `json:"quiet"`
	// ObserversDown names quiet observers that usually hear most of what
	// passes this hop; it is then not called quiet.
	ObserversDown []string  `json:"observersDown,omitempty"`
	Series        []int     `json:"series"` // the last 72 hours, oldest first
	Expected      []float64 `json:"expected"`
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

// Probe finds the route packets usually take from a to b (judged on each
// link's median day in the week before the last slot, so a broken hop does
// not hide its own route) and reports every hop against its usual. The route
// maximises its weakest hop's usual traffic, then prefers fewer hops.
func (w *Watch) Probe(a, b string, t int64) ProbeResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	res := ProbeResult{From: a, To: b, Hour: t, Route: []string{}, Hops: []Hop{}}
	s := int64(w.opt.Slot)
	from, to := t-24*int64(w.opt.Days)-s+1, t-s
	// A link's weight is its median day over the week before the slot, so a
	// few bad days (the outage being probed) do not hide its usual route.
	weight := map[[2]string]int{}
	adj := map[string][]string{}
	for k, r := range w.links {
		if r.sum(w, from, to) < w.opt.Days { // seen less than about once a day
			continue
		}
		days := make([]int, 0, w.opt.Days)
		for d := int64(0); d < int64(w.opt.Days); d++ {
			e := to - 24*d
			days = append(days, r.sum(w, e-23, e))
		}
		if n := median(days); n > 0 {
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
	quiet := w.quietObservers(t)
	for i := 0; i+1 < len(res.Route); i++ {
		k := [2]string{res.Route[i], res.Route[i+1]}
		var down []string
		if nd := w.nodes[k[1]]; nd != nil {
			down = w.observersDown(nd.byObs, t, quiet)
		}
		r := w.links[k]
		now, usual := w.slot(r, t)
		ser, exp := w.series(r, t)
		if len(ser) > 72 {
			ser, exp = ser[len(ser)-72:], exp[len(exp)-72:]
		}
		res.Hops = append(res.Hops, Hop{From: k[0], To: k[1], Now: now, Usual: usual,
			Quiet:         len(down) == 0 && usual >= w.opt.MinUsual && float64(now) <= w.opt.QuietRatio*float64(usual),
			ObserversDown: down, Series: ser, Expected: exp})
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
	for k, sd := range w.signal {
		recent := false
		for _, d := range sd.day {
			recent = recent || d >= lo/24
		}
		if !recent {
			delete(w.signal, k)
		}
	}
}

// Newest is the newest hour the watch holds.
func (w *Watch) Newest() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.newest
}
