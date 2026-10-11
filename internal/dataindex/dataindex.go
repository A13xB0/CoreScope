// Package dataindex holds every packet of the last few days, one compact row
// per receiving area, and answers the Data tab from memory: counts along every
// dimension (the cube), a summary matrix with the flows between areas, and the
// packets behind any figure.
//
// Rows hold numbers only. Every repeated string (area, scope, channel, relay)
// is stored once in a side table and hashes are kept as 8-byte integers, so the
// garbage collector never has to scan the rows, however many there are.
package dataindex

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/origin"
)

// Window is one period the tab can show. Bucket is the finest time step its
// cube carries; the page rolls buckets up into coarser ones itself.
type Window struct {
	Key    string        `json:"key"`
	Label  string        `json:"label"`
	Span   time.Duration `json:"-"`
	Bucket time.Duration `json:"-"`
}

// Windows offered, shortest first. The server offers those the index reaches.
var Windows = []Window{
	{"24h", "Last 24 hours", 24 * time.Hour, 10 * time.Minute},
	{"7d", "Last 7 days", 7 * 24 * time.Hour, time.Hour},
	{"30d", "Last 30 days", 30 * 24 * time.Hour, 6 * time.Hour},
}

// WindowByKey finds a window.
func WindowByKey(key string) (Window, bool) {
	for _, w := range Windows {
		if w.Key == key {
			return w, true
		}
	}
	return Window{}, false
}

// Unknown is how an area that cannot be placed is named in every answer.
const Unknown = "unknown"

// How a receiving area got a packet.
const (
	HowRelayed = "relayed" // one of its own repeaters carried it in
	HowAcross  = "across"  // heard straight from the starting area
	HowUnclear = "unclear"
)

var howNames = [3]string{HowRelayed, HowAcross, HowUnclear}

// Scope classes. They say only what kind of code a packet carried, so they
// hold for any deployment's scope tree.
const (
	ClassDirect       = "direct"       // routed along a known path, not flooded
	ClassUnscoped     = "unscoped"     // a flood with no transport code
	ClassUnrecognised = "unrecognised" // a code nobody here holds a key for
	ClassScoped       = "scoped"       // a named region scope
)

// Scope is a packet's region scope as the tab shows it.
type Scope struct {
	Key   string `json:"key"`
	Class string `json:"class"`
}

// MeshCore route types.
const (
	routeTransportFlood  = 0
	routeFlood           = 1
	routeDirect          = 2
	routeTransportDirect = 3
)

// ScopeOf names the scope a packet carried. scoped is false when the packet
// carried no transport code; scope is CoreScope's name for the code, "" when
// it cannot name it.
func ScopeOf(route int, scope string, scoped bool) Scope {
	switch route {
	case routeDirect, routeTransportDirect:
		return Scope{"direct", ClassDirect}
	}
	if !scoped {
		if route == routeFlood {
			return Scope{"unscoped", ClassUnscoped}
		}
		return Scope{"unrecognised", ClassUnrecognised}
	}
	code := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(scope), "#"))
	if code == "" {
		return Scope{"unrecognised", ClassUnrecognised}
	}
	return Scope{"#" + code, ClassScoped}
}

func classOf(key string) string {
	switch key {
	case "direct":
		return ClassDirect
	case "unscoped":
		return ClassUnscoped
	case "unrecognised":
		return ClassUnrecognised
	}
	return ClassScoped
}

// TypeNames are MeshCore's payload types in plain words.
var TypeNames = map[int]string{
	0: "Request", 1: "Response", 2: "Direct message", 3: "Acknowledgement", 4: "Advert",
	5: "Channel message", 6: "Channel data", 7: "Anonymous request", 8: "Path return",
	9: "Trace", 10: "Multipart", 11: "Control",
}

// TypeName names a payload type.
func TypeName(t int) string {
	if n, ok := TypeNames[t]; ok {
		return n
	}
	return "Type " + strconv.Itoa(t)
}

// Group is a set of packet types the tab can be narrowed to.
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Types []int  `json:"types"`
	// FloodOnly limits the group to flooded packets, scoped or not.
	FloodOnly bool `json:"floodOnly,omitempty"`
}

// Groups split traffic by what it is for. An advert counts as one only when
// flooded: a directly routed advert is a reply to one node, not a node
// announcing itself, and is its own kind, "direct-advert".
var Groups = []Group{
	{"adverts", "Adverts (flood)", []int{4}, true},
	{"messages", "Messages", []int{2, 5, 6}, false},
	{"requests", "Requests and routing", []int{0, 1, 3, 7, 8, 9, 10, 11}, false},
}

// KindDirectAdvert is the kind of an advert that was not flooded.
const KindDirectAdvert = "direct-advert"

func isFlood(route int) bool { return route == routeFlood || route == routeTransportFlood }

// KindOf is the group key a packet belongs to.
func KindOf(typ, route int) string {
	for _, g := range Groups {
		for _, x := range g.Types {
			if x != typ {
				continue
			}
			if g.FloodOnly && !isFlood(route) {
				return KindDirectAdvert
			}
			return g.Key
		}
	}
	return "requests"
}

// Options configures an index.
type Options struct {
	// Areas lists the area codes in the order the tab shows them.
	Areas []string
	// Home, when set, is the area flows count "via home" for.
	Home string
	// NoPlaces is set when there are no areas: every packet is then heard in
	// Unknown, and that row is the packet's count rather than a placeholder.
	NoPlaces bool
}

// row flag bits.
const (
	flagFirst   = 1 << iota // first row of its packet
	flagCrossed             // set on the first row: some area outside the start heard it
	flagViaHome             // a home repeater passed it on
)

// row is one packet as heard in one area. No field holds a pointer.
type row struct {
	hash uint64 // the packet hash as a number, when it is 16 hex digits
	at   int64
	// hashAlt points at the hash in the string table when it is not 16 hex
	// digits; 0 otherwise.
	hashAlt, scope, through, channel, from, gate uint32
	origin, heard, basis, observers              uint16
	typ, route, how, flags                       uint8
}

// Index is safe for concurrent use: writers take the lock briefly, readers
// share it.
type Index struct {
	mu        sync.RWMutex
	areas     []string
	areaOrder map[string]int
	home      string
	strs      []string
	ids       map[string]uint32
	rows      []row
	covered   int64
	unknownID uint32
	noPlaces  bool
}

// New makes an empty index.
func New(opt Options) *Index {
	ix := &Index{areas: append([]string(nil), opt.Areas...), areaOrder: map[string]int{}, home: opt.Home, ids: map[string]uint32{}, noPlaces: opt.NoPlaces}
	for i, a := range opt.Areas {
		ix.areaOrder[a] = i
	}
	ix.id("")
	ix.unknownID = ix.id(Unknown)
	return ix
}

// unplaced reports whether x is a placeholder row: a packet no placed
// observer heard, kept for the summary's starts but not counted as heard.
// Without areas every row is heard in Unknown and every row counts.
func (ix *Index) unplaced(x *row) bool {
	return !ix.noPlaces && uint32(x.heard) == ix.unknownID
}

func (ix *Index) id(s string) uint32 {
	if i, ok := ix.ids[s]; ok {
		return i
	}
	i := uint32(len(ix.strs))
	ix.strs = append(ix.strs, s)
	ix.ids[s] = i
	return i
}

func area(a string) string {
	if a == origin.Unknown {
		return Unknown
	}
	return a
}

// through names the areas a packet's repeaters stood in on its way, beyond
// where it started and where it was heard, in the tab's area order.
func (ix *Index) through(via []string, from, heard string) string {
	var buf [8]string
	out := buf[:0]
	for _, v := range via {
		if v != from && v != heard && v != origin.Unknown {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	sort.Slice(out, func(i, j int) bool { return ix.less(out[i], out[j]) })
	return strings.Join(out, "+")
}

// less orders areas: configured ones in their order, then the rest by code,
// unknown last.
func (ix *Index) less(a, b string) bool {
	oa, okA := ix.areaOrder[a]
	ob, okB := ix.areaOrder[b]
	switch {
	case okA && okB:
		return oa < ob
	case okA != okB:
		return okA
	case (a == Unknown) != (b == Unknown):
		return b == Unknown
	}
	return a < b
}

func clamp16(n int) uint16 {
	if n > 65535 {
		return 65535
	}
	return uint16(n)
}

func (ix *Index) appendRecord(rows []row, r origin.Record) []row {
	sc := ScopeOf(r.Route, r.Scope, r.Scoped)
	base := row{
		at: r.At, scope: ix.id(sc.Key), channel: ix.id(r.Channel),
		origin: uint16(ix.id(area(r.Origin))), basis: uint16(ix.id(r.Basis)),
		typ: uint8(r.Type), route: uint8(r.Route),
	}
	if len(r.Hash) == 16 {
		if v, err := strconv.ParseUint(r.Hash, 16, 64); err == nil {
			base.hash = v
		} else {
			base.hashAlt = ix.id(r.Hash)
		}
	} else {
		base.hashAlt = ix.id(r.Hash)
	}
	var flags uint8 = flagFirst
	if ix.home != "" {
		for _, v := range r.Via {
			if v == ix.home {
				flags |= flagViaHome
			}
		}
	}
	for _, h := range r.Heard {
		if h.Area != origin.Unknown && r.Origin != origin.Unknown && h.Area != r.Origin {
			flags |= flagCrossed
		}
	}
	if len(r.Heard) == 0 {
		x := base
		x.heard, x.how, x.flags = uint16(ix.unknownID), 2, flags
		return append(rows, x)
	}
	for _, h := range r.Heard {
		x := base
		x.heard = uint16(ix.id(area(h.Area)))
		x.through = ix.id(ix.through(r.Via, r.Origin, h.Area))
		x.from, x.gate = ix.id(h.From), ix.id(h.Gate)
		x.observers = clamp16(h.Observers)
		x.how = 2
		switch {
		case h.Relayed:
			x.how = 0
		case h.Across:
			x.how = 1
		}
		x.flags = flags
		flags &^= flagFirst | flagCrossed
		rows = append(rows, x)
	}
	return rows
}

// search is the first row at or after t.
func (ix *Index) search(t int64) int {
	return sort.Search(len(ix.rows), func(i int) bool { return ix.rows[i].at >= t })
}

// Replace drops every row from since onwards and adds recs in their place:
// the background fill passes 0, each refresh re-derives a recent stretch.
func (ix *Index) Replace(since int64, recs []origin.Record) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	cut := ix.search(since)
	ix.rows = ix.rows[:cut]
	start := len(ix.rows)
	for _, r := range recs {
		ix.rows = ix.appendRecord(ix.rows, r)
	}
	added := ix.rows[start:]
	sorted := sort.SliceIsSorted(added, func(i, j int) bool { return added[i].at < added[j].at })
	if !sorted || (start > 0 && len(added) > 0 && added[0].at < ix.rows[start-1].at) {
		// Stable, so a packet's rows stay together.
		sort.SliceStable(ix.rows, func(i, j int) bool { return ix.rows[i].at < ix.rows[j].at })
	}
}

// Prune drops rows older than before.
func (ix *Index) Prune(before int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	i := ix.search(before)
	if i == 0 {
		return
	}
	n := copy(ix.rows, ix.rows[i:])
	ix.rows = ix.rows[:n]
	if cap(ix.rows) > 2*len(ix.rows)+1024 {
		ix.rows = append([]row(nil), ix.rows...)
	}
}

// SetCovered records how far back the index holds every packet.
func (ix *Index) SetCovered(from int64) {
	ix.mu.Lock()
	ix.covered = from
	ix.mu.Unlock()
}

// Covered is how far back the index holds every packet; 0 before any fill.
func (ix *Index) Covered() int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.covered
}

// Len is the number of rows held.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.rows)
}

// RowBytes is the memory each row takes.
const RowBytes = 56

// coverage says how much of a window the index holds. A window counts as
// complete when the gap at its start is under an hour.
func (ix *Index) coverage(from int64) (time.Time, bool) {
	cov := from
	if ix.covered == 0 || ix.covered > from {
		cov = ix.covered
	}
	return time.Unix(cov, 0).UTC(), ix.covered != 0 && ix.covered <= from+3600
}

// Cube is every packet heard in a placed area, counted along each dimension
// the tab can filter or split by. Rows are [bucket, origin, heard, type,
// scope, how, route, through, channel, packets], each dimension an index into
// the list beside it, so the browser can slice it any way without asking
// again.
type Cube struct {
	Window        string    `json:"window"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	CoveredFrom   time.Time `json:"coveredFrom"`
	Complete      bool      `json:"complete"`
	BucketSeconds int64     `json:"bucketSeconds"`
	Buckets       []int64   `json:"buckets"`
	Origins       []string  `json:"origins"`
	Heard         []string  `json:"heard"`
	Types         []int     `json:"types"`
	Scopes        []Scope   `json:"scopes"`
	How           []string  `json:"how"`
	Routes        []int     `json:"routes"`
	// Through: "" for none, otherwise area codes joined with "+".
	Through []string `json:"through"`
	// Channels: names, "hash:XX" for channels nobody here holds a key for,
	// "" for packets that are not channel traffic.
	Channels []string  `json:"channels"`
	Rows     [][10]int `json:"rows"`
}

type cubeKey struct {
	bucket                  int64
	scope, through, channel uint32
	origin, heard           uint16
	typ, route, how         uint8
}

// byTotal orders ids by count, largest first, then by string.
func (ix *Index) byTotal(m map[uint32]int) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return ix.strs[out[i]] < ix.strs[out[j]]
	})
	return out
}

func (ix *Index) areaList(set map[uint16]bool) ([]string, map[uint16]int) {
	ids := make([]uint16, 0, len(set))
	for k := range set {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool { return ix.less(ix.strs[ids[i]], ix.strs[ids[j]]) })
	names := make([]string, len(ids))
	pos := make(map[uint16]int, len(ids))
	for i, k := range ids {
		names[i], pos[k] = ix.strs[k], i
	}
	return names, pos
}

func intList(set map[uint8]bool) ([]int, map[uint8]int) {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, int(k))
	}
	sort.Ints(out)
	pos := make(map[uint8]int, len(out))
	for i, v := range out {
		pos[uint8(v)] = i
	}
	return out, pos
}

// Cube builds one window's cube.
func (ix *Index) Cube(w Window, now time.Time) Cube {
	step := int64(w.Bucket / time.Second)
	from := now.Add(-w.Span).Unix()
	start := from - from%step
	c := Cube{Window: w.Key, From: time.Unix(from, 0).UTC(), To: now.UTC(), BucketSeconds: step, How: howNames[:]}
	for t := start; t <= now.Unix(); t += step {
		c.Buckets = append(c.Buckets, t)
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	c.CoveredFrom, c.Complete = ix.coverage(from)
	first := ix.search(from)
	counts := make(map[cubeKey]int, (len(ix.rows)-first)/4)
	for i := first; i < len(ix.rows); i++ {
		x := &ix.rows[i]
		if ix.unplaced(x) || x.at > now.Unix() {
			continue
		}
		counts[cubeKey{
			bucket: x.at - x.at%step, origin: x.origin, heard: x.heard, typ: x.typ, route: x.route,
			scope: x.scope, how: x.how, through: x.through, channel: x.channel,
		}]++
	}
	origins, heard := map[uint16]bool{}, map[uint16]bool{}
	types, routes := map[uint8]bool{}, map[uint8]bool{}
	scopes, thr, chans := map[uint32]int{}, map[uint32]int{}, map[uint32]int{}
	for k, n := range counts {
		origins[k.origin], heard[k.heard], types[k.typ], routes[k.route] = true, true, true, true
		scopes[k.scope] += n
		thr[k.through] += n
		chans[k.channel] += n
	}
	var oi, hi map[uint16]int
	c.Origins, oi = ix.areaList(origins)
	c.Heard, hi = ix.areaList(heard)
	var ti, ri map[uint8]int
	c.Types, ti = intList(types)
	c.Routes, ri = intList(routes)
	si, xi, chi := map[uint32]int{}, map[uint32]int{}, map[uint32]int{}
	for i, k := range ix.byTotal(scopes) {
		c.Scopes = append(c.Scopes, Scope{ix.strs[k], classOf(ix.strs[k])})
		si[k] = i
	}
	for i, k := range ix.byTotal(thr) {
		c.Through = append(c.Through, ix.strs[k])
		xi[k] = i
	}
	for i, k := range ix.byTotal(chans) {
		c.Channels = append(c.Channels, ix.strs[k])
		chi[k] = i
	}
	c.Rows = make([][10]int, 0, len(counts))
	for k, n := range counts {
		b := int((k.bucket - start) / step)
		if b < 0 || b >= len(c.Buckets) {
			continue
		}
		c.Rows = append(c.Rows, [10]int{b, oi[k.origin], hi[k.heard], ti[k.typ], si[k.scope], int(k.how), ri[k.route], xi[k.through], chi[k.channel], n})
	}
	slices.SortFunc(c.Rows, func(a, b [10]int) int {
		for x := 0; x < 9; x++ {
			if a[x] != b[x] {
				return a[x] - b[x]
			}
		}
		return 0
	})
	if c.Scopes == nil {
		c.Scopes, c.Through, c.Channels, c.Origins, c.Heard = []Scope{}, []string{}, []string{}, []string{}, []string{}
	}
	return c
}

// Cell is one start and receiving area pair. A packet counts once per
// receiving area: Relayed when a copy there came from one of its own
// repeaters, otherwise Across when one came straight from the start.
type Cell struct {
	Origin  string `json:"origin"`
	Heard   string `json:"heard"`
	Packets int    `json:"packets"`
	Relayed int    `json:"relayed"`
	Across  int    `json:"across"`
}

// RelayRow is a relay and how many of a flow's packets it carried.
type RelayRow struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Area    string `json:"area"`
	Packets int    `json:"packets"`
}

// MaxRelays is how many relays a flow lists.
const MaxRelays = 8

// Flow is everything that started in one area and was heard in another.
type Flow struct {
	Cell
	// ViaHome counts packets a home repeater passed on along the way.
	ViaHome int `json:"viaHome"`
	// From lists the relays observers in the receiving area heard it from.
	From []RelayRow `json:"from"`
	// Gates lists the home relays at the boundary (with a home area set).
	Gates []RelayRow `json:"gates"`
}

// AreaView is an area with what a window saw of it.
type AreaView struct {
	Code string `json:"code"`
	// Observed is true when an observer there heard anything this window.
	Observed   bool `json:"observed"`
	Originated int  `json:"originated"`
}

// Summary is one window's headline figures.
type Summary struct {
	Window        string            `json:"window"`
	Label         string            `json:"label"`
	From          time.Time         `json:"from"`
	To            time.Time         `json:"to"`
	CoveredFrom   time.Time         `json:"coveredFrom"`
	Complete      bool              `json:"complete"`
	BucketSeconds int64             `json:"bucketSeconds"`
	Packets       int               `json:"packets"`
	CrossBorder   int               `json:"crossBorder"`
	Bases         map[string]int    `json:"bases"`
	Areas         []AreaView        `json:"areas"`
	Matrix        []Cell            `json:"matrix"`
	MatrixByGroup map[string][]Cell `json:"matrixByGroup"`
	Flows         []Flow            `json:"flows"`
}

type pair struct{ o, h uint16 }

type flowAcc struct {
	flow        Flow
	from, gates map[uint32]int
}

// Summary builds one window's summary. relay names a relay key; nil leaves
// keys unnamed.
func (ix *Index) Summary(w Window, now time.Time, relay func(string) (string, string)) Summary {
	from := now.Add(-w.Span).Unix()
	s := Summary{Window: w.Key, Label: w.Label, From: time.Unix(from, 0).UTC(), To: now.UTC(),
		BucketSeconds: int64(w.Bucket / time.Second), Bases: map[string]int{}, MatrixByGroup: map[string][]Cell{}}
	matrix := map[pair]*Cell{}
	byGroup := map[string]map[pair]*Cell{}
	flows := map[pair]*flowAcc{}
	observed := map[uint16]bool{}
	originated := map[uint16]int{}

	ix.mu.RLock()
	s.CoveredFrom, s.Complete = ix.coverage(from)
	for i := ix.search(from); i < len(ix.rows); i++ {
		x := &ix.rows[i]
		if x.at > now.Unix() {
			continue
		}
		if x.flags&flagFirst != 0 {
			s.Packets++
			s.Bases[ix.strs[x.basis]]++
			originated[x.origin]++
			if x.flags&flagCrossed != 0 {
				s.CrossBorder++
			}
		}
		if uint32(x.heard) == ix.unknownID {
			continue
		}
		observed[x.heard] = true
		key := pair{x.origin, x.heard}
		kind := KindOf(int(x.typ), int(x.route))
		cellsTo := []map[pair]*Cell{matrix}
		if kind != KindDirectAdvert {
			if byGroup[kind] == nil {
				byGroup[kind] = map[pair]*Cell{}
			}
			cellsTo = append(cellsTo, byGroup[kind])
		}
		for _, m := range cellsTo {
			c := m[key]
			if c == nil {
				c = &Cell{Origin: ix.strs[x.origin], Heard: ix.strs[x.heard]}
				m[key] = c
			}
			c.Packets++
			switch x.how {
			case 0:
				c.Relayed++
			case 1:
				c.Across++
			}
		}
		if x.origin == x.heard || uint32(x.origin) == ix.unknownID {
			continue
		}
		f := flows[key]
		if f == nil {
			f = &flowAcc{flow: Flow{Cell: Cell{Origin: ix.strs[x.origin], Heard: ix.strs[x.heard]}}, from: map[uint32]int{}, gates: map[uint32]int{}}
			flows[key] = f
		}
		f.flow.Packets++
		switch x.how {
		case 0:
			f.flow.Relayed++
		case 1:
			f.flow.Across++
		}
		if x.flags&flagViaHome != 0 {
			f.flow.ViaHome++
		}
		if x.from != 0 {
			f.from[x.from]++
		}
		if x.gate != 0 {
			f.gates[x.gate]++
		}
	}
	areaIDs := make([]uint16, len(ix.areas))
	for i, a := range ix.areas {
		if id, ok := ix.ids[a]; ok {
			areaIDs[i] = uint16(id)
		} else {
			areaIDs[i] = 0xffff
		}
	}
	strs := ix.strs
	ix.mu.RUnlock()

	for i, a := range ix.areas {
		s.Areas = append(s.Areas, AreaView{Code: a, Observed: observed[areaIDs[i]], Originated: originated[areaIDs[i]]})
	}
	s.Matrix = ix.cells(matrix)
	for _, g := range Groups {
		s.MatrixByGroup[g.Key] = ix.cells(byGroup[g.Key])
	}
	top := func(m map[uint32]int) []RelayRow {
		rows := make([]RelayRow, 0, len(m))
		for k, n := range m {
			r := RelayRow{Key: strs[k], Name: strs[k], Area: Unknown, Packets: n}
			if relay != nil {
				r.Name, r.Area = relay(strs[k])
				r.Area = area(r.Area)
			}
			rows = append(rows, r)
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Packets != rows[j].Packets {
				return rows[i].Packets > rows[j].Packets
			}
			return rows[i].Key < rows[j].Key
		})
		if len(rows) > MaxRelays {
			rows = rows[:MaxRelays]
		}
		return rows
	}
	s.Flows = []Flow{}
	for _, f := range flows {
		fl := f.flow
		fl.From, fl.Gates = top(f.from), top(f.gates)
		s.Flows = append(s.Flows, fl)
	}
	sort.Slice(s.Flows, func(i, j int) bool {
		if s.Flows[i].Packets != s.Flows[j].Packets {
			return s.Flows[i].Packets > s.Flows[j].Packets
		}
		return s.Flows[i].Origin+s.Flows[i].Heard < s.Flows[j].Origin+s.Flows[j].Heard
	})
	return s
}

func (ix *Index) cells(m map[pair]*Cell) []Cell {
	out := make([]Cell, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Origin != out[j].Origin {
			return ix.less(out[i].Origin, out[j].Origin)
		}
		return ix.less(out[i].Heard, out[j].Heard)
	})
	return out
}

// Filter is one of the explorer's filters: a dimension, whether matching
// values are kept (N 0) or dropped (N 1), and the values. A "through" filter
// matches a packet that passed through any listed area, unless Exact is set,
// when it must match the whole list: what a click on one "through" series
// means.
type Filter struct {
	D     string   `json:"d"`
	N     int      `json:"n"`
	V     []string `json:"v"`
	Exact int      `json:"x,omitempty"`
}

// Dimensions a filter can name.
var Dimensions = []string{"border", "origin", "heard", "through", "kind", "type", "channel", "scope", "sclass", "how", "routing"}

// PlaceDimensions are the Dimensions that need areas: without them every
// packet starts and is heard in Unknown, so they say nothing.
var PlaceDimensions = map[string]bool{"border": true, "origin": true, "heard": true, "through": true, "how": true}

func (f Filter) matches(v string) bool {
	hit := false
	if f.D == "through" && f.Exact == 0 {
		for _, a := range strings.Split(v, "+") {
			for _, w := range f.V {
				if a == w {
					hit = true
				}
			}
		}
	} else {
		for _, w := range f.V {
			if v == w {
				hit = true
			}
		}
	}
	return hit != (f.N != 0)
}

// value is a row's value on one of the explorer's dimensions, the same values
// the cube carries, so a list always matches the figure it came from.
func (ix *Index) value(x *row, dim string) string {
	switch dim {
	case "border":
		switch {
		case uint32(x.origin) == ix.unknownID:
			return "unknown"
		case x.origin == x.heard:
			return "local"
		}
		return "crossed"
	case "origin":
		return ix.strs[x.origin]
	case "heard":
		return ix.strs[x.heard]
	case "through":
		if t := ix.strs[x.through]; t != "" {
			return t
		}
		return "straight"
	case "kind":
		return KindOf(int(x.typ), int(x.route))
	case "type":
		return strconv.Itoa(int(x.typ))
	case "channel":
		return ix.strs[x.channel]
	case "scope":
		return ix.strs[x.scope]
	case "sclass":
		return classOf(ix.strs[x.scope])
	case "how":
		return howNames[x.how]
	case "routing":
		if isFlood(int(x.route)) {
			return "flood"
		}
		return "direct"
	}
	return ""
}

// PacketRow is one packet as heard in one receiving area: the unit every
// figure counts.
type PacketRow struct {
	Hash        string `json:"hash"`
	At          int64  `json:"at"`
	Type        int    `json:"type"`
	TypeName    string `json:"typeName"`
	Kind        string `json:"kind"`
	Routing     string `json:"routing"`
	Scope       string `json:"scope"`
	ScopeClass  string `json:"scopeClass"`
	Channel     string `json:"channel"`
	Border      string `json:"border"`
	Origin      string `json:"origin"`
	Basis       string `json:"basis"`
	Heard       string `json:"heard"`
	Through     string `json:"through"`
	How         string `json:"how"`
	Observers   int    `json:"observers"`
	HeardFrom   string `json:"heardFrom,omitempty"`
	HeardFromIn string `json:"heardFromIn,omitempty"`
	Boundary    string `json:"boundary,omitempty"`
	BoundaryIn  string `json:"boundaryIn,omitempty"`
}

// PacketResult is the packets behind a figure, newest first.
type PacketResult struct {
	Total int         `json:"total"`
	Limit int         `json:"limit"`
	Rows  []PacketRow `json:"rows"`
}

// MaxPacketRows caps what one list returns; Total still counts them all.
const MaxPacketRows = 2000

func (ix *Index) hashOf(x *row) string {
	if x.hashAlt != 0 {
		return ix.strs[x.hashAlt]
	}
	const hexd = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = hexd[(x.hash>>(4*(15-uint(i))))&0xf]
	}
	return string(b[:])
}

// Packets lists the rows of a window that pass every filter, inside
// [from, to) when those are set, newest first.
func (ix *Index) Packets(w Window, now time.Time, from, to int64, filters []Filter, relay func(string) (string, string)) PacketResult {
	lo := now.Add(-w.Span).Unix()
	if from > lo {
		lo = from
	}
	hi := now.Unix() + 1
	if to > 0 && to < hi {
		hi = to
	}
	res := PacketResult{Limit: MaxPacketRows, Rows: []PacketRow{}}
	type hit struct{ from, gate string }
	var hits []hit
	ix.mu.RLock()
	first := ix.search(lo)
	for i := len(ix.rows) - 1; i >= first; i-- {
		x := &ix.rows[i]
		if x.at >= hi || ix.unplaced(x) {
			continue
		}
		ok := true
		for _, f := range filters {
			if !f.matches(ix.value(x, f.D)) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		res.Total++
		if len(res.Rows) >= MaxPacketRows {
			continue
		}
		sc := ix.strs[x.scope]
		res.Rows = append(res.Rows, PacketRow{
			Hash: ix.hashOf(x), At: x.at, Type: int(x.typ), TypeName: TypeName(int(x.typ)),
			Kind: ix.value(x, "kind"), Routing: ix.value(x, "routing"), Scope: sc, ScopeClass: classOf(sc),
			Channel: ix.strs[x.channel], Border: ix.value(x, "border"), Origin: ix.strs[x.origin],
			Basis: ix.strs[x.basis], Heard: ix.strs[x.heard], Through: ix.value(x, "through"),
			How: howNames[x.how], Observers: int(x.observers),
		})
		hits = append(hits, hit{from: ix.strs[x.from], gate: ix.strs[x.gate]})
	}
	ix.mu.RUnlock()
	// Relays are named outside the lock: naming may take the server's own.
	if relay != nil {
		for i := range res.Rows {
			if k := hits[i].from; k != "" {
				res.Rows[i].HeardFrom, res.Rows[i].HeardFromIn = relay(k)
				res.Rows[i].HeardFromIn = area(res.Rows[i].HeardFromIn)
			}
			if k := hits[i].gate; k != "" {
				res.Rows[i].Boundary, res.Rows[i].BoundaryIn = relay(k)
				res.Rows[i].BoundaryIn = area(res.Rows[i].BoundaryIn)
			}
		}
	}
	return res
}

// Origin is where a packet started and how that was decided (origin.Basis*).
type Origin struct {
	Area  string `json:"area"`
	Basis string `json:"basis"`
}

// hashKey is how the index keys a hash: the number for 16 hex digits, else
// the lower-cased string.
func hashKey(h string) (uint64, string) {
	if len(h) == 16 {
		if v, err := strconv.ParseUint(h, 16, 64); err == nil {
			return v, ""
		}
	}
	return 0, strings.ToLower(h)
}

func (ix *Index) rowKey(x *row) (uint64, string) {
	if x.hashAlt != 0 {
		return 0, strings.ToLower(ix.strs[x.hashAlt])
	}
	return x.hash, ""
}

// StartedIn returns a test for the hashes of packets that started in area,
// as the index holds them now. It is nil when area is not one the index
// places in, so a caller can tell "nothing started there" from "cannot say".
func (ix *Index) StartedIn(area string) func(hash string) bool {
	ix.mu.RLock()
	if _, ok := ix.areaOrder[area]; !ok {
		ix.mu.RUnlock()
		return nil
	}
	nums := map[uint64]struct{}{}
	alts := map[string]struct{}{}
	if id, ok := ix.ids[area]; ok {
		for i := range ix.rows {
			x := &ix.rows[i]
			if uint32(x.origin) != id {
				continue
			}
			if n, s := ix.rowKey(x); s == "" {
				nums[n] = struct{}{}
			} else {
				alts[s] = struct{}{}
			}
		}
	}
	ix.mu.RUnlock()
	return func(hash string) bool {
		n, s := hashKey(hash)
		if s == "" {
			_, ok := nums[n]
			return ok
		}
		_, ok := alts[s]
		return ok
	}
}

// OriginsOf names where each of hashes started, keyed as given. Hashes the
// index does not hold are left out.
func (ix *Index) OriginsOf(hashes []string) map[string]Origin {
	out := make(map[string]Origin, len(hashes))
	if len(hashes) == 0 {
		return out
	}
	nums := make(map[uint64][]string, len(hashes))
	alts := map[string][]string{}
	for _, h := range hashes {
		if n, s := hashKey(h); s == "" {
			nums[n] = append(nums[n], h)
		} else {
			alts[s] = append(alts[s], h)
		}
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for i := range ix.rows {
		x := &ix.rows[i]
		var want []string
		if n, s := ix.rowKey(x); s == "" {
			want = nums[n]
		} else {
			want = alts[s]
		}
		for _, h := range want {
			out[h] = Origin{Area: ix.strs[x.origin], Basis: ix.strs[x.basis]}
		}
	}
	return out
}
