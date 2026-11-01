// Package origin decides where a MeshCore packet started and how each area
// heard it, from the packet's observations, the node positions CoreScope
// holds and a map of named areas (internal/areamap).
//
// Areas are places, not operators: a repeater counts for where it stands,
// whoever runs it. Nothing is guessed: what cannot be placed is Unknown.
package origin

import (
	"strings"

	"github.com/meshcore-analyzer/areamap"
)

// Unknown is the area of anything that cannot be placed.
const Unknown = ""

// Bases: how a packet's starting area was decided, first rule that answers.
const (
	BasisAdvert = "advert"      // the advert's own reported position
	BasisNode   = "node"        // an advert without one: the sender's node record
	BasisRelay  = "first-relay" // the first repeater to pass it on
	BasisHeard  = "zero-hop"    // heard with no relay, so near the observer
	BasisNone   = "unknown"
)

// minHopHex is the shortest hop prefix allowed to place a relay. A 1-byte
// prefix is shared by many relays on a mesh of any size and CoreScope resolves
// many of them by guessing, so a 1-byte hop places nothing.
const minHopHex = 4

// prefixLens are the hop prefix sizes indexed, in hex characters.
var prefixLens = [...]int{2, 4, 6, 8}

// Options configures a Locator.
type Options struct {
	// Areas places positions; nil places nothing.
	Areas *areamap.Map
	// Layer is the areamap layer to place in; "" is areamap.DefaultLayer.
	Layer string
	// Home, when set, is the area whose boundary relays are reported as Gate.
	Home string
}

// Node is what the locator needs to know about a node.
type Node struct {
	PublicKey string
	Name      string
	// Relay is true for nodes that pass packets on (repeaters, rooms): only
	// these can be a hop.
	Relay    bool
	Lat, Lon *float64
}

// Observer is what the locator needs to know about an observer.
type Observer struct {
	ID string
	// IATA places an observer that reports no position, through the areas'
	// iata lists.
	IATA     string
	Lat, Lon *float64
}

// Packet is one transmission and every observation of it.
type Packet struct {
	Hash  string
	At    int64
	Type  int
	Route int
	// Scope is the scope name the packet carried; Scoped is false when it
	// carried no transport code at all.
	Scope   string
	Scoped  bool
	Channel string
	// Advert is set for adverts: the sender's own key and reported position.
	Advert       *Advert
	Observations []Observation
}

// Advert is what an advert says about its sender.
type Advert struct {
	PublicKey string
	Lat, Lon  *float64
}

// Observation is one observer's copy of a packet.
type Observation struct {
	ObserverID string
	// Relayed is true when the observer is itself a repeater that passed the
	// packet on, in which case it is the last entry of its own path.
	Relayed bool
	// Path is the hop prefixes in order, origin side first.
	Path []string
	// Resolved is CoreScope's full key for each hop, "" where it could not
	// decide. It may be shorter than Path or absent.
	Resolved []string
}

// Heard is how one area heard a packet.
type Heard struct {
	Area      string
	Observers int
	// Relayed: an observer there received it from a repeater standing there.
	Relayed bool
	// Across: an observer there received it straight from the starting area,
	// with no repeater of its own involved.
	Across bool
	// From is the relay an observer there heard it from.
	From string
	// Gate is the home relay at the boundary: for an area outside home, the
	// last home relay before it left; for home, the first one it reached.
	Gate string
}

// Record is one packet, reduced to where it started and who heard it.
type Record struct {
	Hash    string
	At      int64
	Type    int
	Route   int
	Scope   string
	Scoped  bool
	Channel string
	Origin  string
	Basis   string
	// Heard has one entry per receiving area, ordered by area code.
	Heard []Heard
	// Via lists, in order, every area whose placeable repeaters passed it on.
	Via []string
}

type nodeInfo struct{ key, name, area string }

type prefixInfo struct {
	key    string    // the prefix, lower case
	area   string    // the area every placed holder stands in, or Unknown
	unique *nodeInfo // the one relay holding it, if only one does
}

// Locator places nodes, hops and observers. It is read-only once built and
// safe for concurrent use.
type Locator struct {
	areas    *areamap.Map
	layer    string
	home     string
	byKey    map[string]*nodeInfo
	byPrefix map[string]*prefixInfo
	observer map[string]string
}

// NewLocator indexes the nodes and observers known now.
func NewLocator(opt Options, nodes []Node, observers []Observer) *Locator {
	if opt.Layer == "" {
		opt.Layer = areamap.DefaultLayer
	}
	l := &Locator{
		areas: opt.Areas, layer: opt.Layer, home: opt.Home,
		byKey:    make(map[string]*nodeInfo, len(nodes)),
		byPrefix: map[string]*prefixInfo{},
		observer: make(map[string]string, len(observers)),
	}
	holders := map[string]int{}
	conflict := map[string]bool{}
	for _, n := range nodes {
		key := strings.ToLower(n.PublicKey)
		ni := &nodeInfo{key: key, name: n.Name, area: l.at(n.Lat, n.Lon)}
		l.byKey[key] = ni
		if !n.Relay {
			continue
		}
		for _, k := range prefixLens {
			if len(key) < k {
				continue
			}
			p := key[:k]
			pi := l.byPrefix[p]
			if pi == nil {
				pi = &prefixInfo{key: p}
				l.byPrefix[p] = pi
			}
			holders[p]++
			pi.unique = ni
			if ni.area == Unknown || conflict[p] {
				continue
			}
			if pi.area == Unknown {
				pi.area = ni.area
			} else if pi.area != ni.area {
				pi.area, conflict[p] = Unknown, true
			}
		}
	}
	for p, n := range holders {
		if n > 1 {
			l.byPrefix[p].unique = nil
		}
	}
	for _, o := range observers {
		area := l.at(o.Lat, o.Lon)
		if area == Unknown {
			// An observer is a node too, so its own advert may carry a position.
			if ni := l.node(o.ID); ni != nil {
				area = ni.area
			}
		}
		if area == Unknown && l.areas != nil && o.IATA != "" {
			area, _ = l.areas.LocateIATA(l.layer, o.IATA)
		}
		l.observer[strings.ToLower(o.ID)] = area
	}
	return l
}

func (l *Locator) at(lat, lon *float64) string {
	if l.areas == nil || lat == nil || lon == nil {
		return Unknown
	}
	a, _ := l.areas.Locate(l.layer, *lat, *lon)
	return a
}

// get looks s up in a map keyed in lower case without allocating.
func get[V any](m map[string]V, s string) (V, bool) {
	var buf [80]byte
	if len(s) > len(buf) {
		v, ok := m[strings.ToLower(s)]
		return v, ok
	}
	b := buf[:len(s)]
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	v, ok := m[string(b)]
	return v, ok
}

func (l *Locator) node(key string) *nodeInfo {
	ni, _ := get(l.byKey, key)
	return ni
}

// Key places a node by its full public key.
func (l *Locator) Key(pubkey string) string {
	if ni := l.node(pubkey); ni != nil {
		return ni.area
	}
	return Unknown
}

// Prefix places a hop by its prefix. A short prefix is shared by many relays,
// but that only matters when they stand in different areas: if every relay
// with the prefix and a known position is in one area, the hop is there.
func (l *Locator) Prefix(hex string) string {
	if pi, ok := get(l.byPrefix, hex); ok {
		return pi.area
	}
	return Unknown
}

// Relay names a relay from its full key or a prefix of it, with its area. An
// unknown or shared prefix comes back as the prefix itself, upper case.
func (l *Locator) Relay(key string) (name, area string) {
	if pi, ok := get(l.byPrefix, key); ok && pi.unique != nil {
		return pi.unique.name, pi.unique.area
	}
	if ni := l.node(key); ni != nil {
		return ni.name, ni.area
	}
	return strings.ToUpper(key), l.Prefix(key)
}

// Observer places an observer.
func (l *Locator) Observer(id string) string {
	a, _ := get(l.observer, id)
	return a
}

func resolved(o *Observation, i int) string {
	if i < len(o.Resolved) {
		return o.Resolved[i]
	}
	return ""
}

// hop places hop i of an observation: CoreScope's resolved key first, then
// the prefix. A 1-byte hop places nothing.
func (l *Locator) hop(o *Observation, i int) string {
	if i >= len(o.Path) || len(o.Path[i]) < minHopHex {
		return Unknown
	}
	if r := resolved(o, i); r != "" {
		if a := l.Key(r); a != Unknown {
			return a
		}
	}
	return l.Prefix(o.Path[i])
}

// hopKey names hop i as well as the packet allows, in lower case: the
// resolved key where there is one, otherwise the prefix. "" for a 1-byte hop.
func (l *Locator) hopKey(o *Observation, i int) string {
	if i >= len(o.Path) || len(o.Path[i]) < minHopHex {
		return ""
	}
	if r := resolved(o, i); r != "" {
		if ni := l.node(r); ni != nil {
			return ni.key
		}
		return strings.ToLower(r)
	}
	if pi, ok := get(l.byPrefix, o.Path[i]); ok {
		return pi.key
	}
	return strings.ToLower(o.Path[i])
}

// tally counts votes for areas in a small fixed array.
type tally struct {
	area [8]string
	n    [8]int
	len  int
}

func (t *tally) add(a string) {
	for i := 0; i < t.len; i++ {
		if t.area[i] == a {
			t.n[i]++
			return
		}
	}
	if t.len < len(t.area) {
		t.area[t.len], t.n[t.len] = a, 1
		t.len++
	}
}

// top is the area with most votes, the lower code on a tie.
func (t *tally) top() string {
	best, n := Unknown, 0
	for i := 0; i < t.len; i++ {
		if t.n[i] > n || (t.n[i] == n && t.area[i] < best) {
			best, n = t.area[i], t.n[i]
		}
	}
	return best
}

func (l *Locator) origin(p *Packet) (string, string) {
	if p.Advert != nil {
		if a := l.at(p.Advert.Lat, p.Advert.Lon); a != Unknown {
			return a, BasisAdvert
		}
		if a := l.Key(p.Advert.PublicKey); a != Unknown {
			return a, BasisNode
		}
	}
	// Each observation names a first relay; they normally agree, and where a
	// flood left through two repeaters the majority decides.
	var relay, zero tally
	for i := range p.Observations {
		o := &p.Observations[i]
		if len(o.Path) == 0 {
			if a := l.Observer(o.ObserverID); a != Unknown {
				zero.add(a)
			}
			continue
		}
		if a := l.hop(o, 0); a != Unknown {
			relay.add(a)
		}
	}
	if a := relay.top(); a != Unknown {
		return a, BasisRelay
	}
	if a := zero.top(); a != Unknown {
		return a, BasisHeard
	}
	return Unknown, BasisNone
}

// gate finds the home relay at the boundary on one observation's path.
func (l *Locator) gate(o *Observation, path []string, heard, origin string) string {
	switch {
	case l.home == "":
	case heard != l.home && heard != Unknown:
		for i := len(path) - 1; i >= 0; i-- {
			if l.hop(o, i) == l.home {
				return l.hopKey(o, i)
			}
		}
	case heard == l.home && origin != l.home && origin != Unknown:
		for i := range path {
			if l.hop(o, i) == l.home {
				return l.hopKey(o, i)
			}
		}
	}
	return ""
}

// Derive reduces a packet to a record. It allocates only the record's Heard
// and Via slices.
func (l *Locator) Derive(p Packet) Record {
	rec := Record{
		Hash: p.Hash, At: p.At, Type: p.Type, Route: p.Route,
		Scope: p.Scope, Scoped: p.Scoped, Channel: p.Channel,
	}
	rec.Origin, rec.Basis = l.origin(&p)

	var heardBuf [8]Heard
	heard := heardBuf[:0]
	var viaBuf [8]string
	via := viaBuf[:0]
	type seenObs struct{ area, id string }
	var seenBuf [16]seenObs
	seen := seenBuf[:0]

	for oi := range p.Observations {
		o := &p.Observations[oi]
		area := l.Observer(o.ObserverID)
		path := o.Path
		if o.Relayed && len(path) > 0 {
			path = path[:len(path)-1]
		}
		from := rec.Origin
		if len(path) > 0 {
			from = l.hop(o, len(path)-1)
		}
		for i := range path {
			if a := l.hop(o, i); a != Unknown && !contains(via, a) {
				via = append(via, a)
			}
		}
		hi := -1
		for i := range heard {
			if heard[i].Area == area {
				hi = i
				break
			}
		}
		if hi < 0 {
			heard = append(heard, Heard{Area: area})
			hi = len(heard) - 1
		}
		h := &heard[hi]
		dup := false
		for _, s := range seen {
			if s.area == area && strings.EqualFold(s.id, o.ObserverID) {
				dup = true
				break
			}
		}
		if !dup {
			seen = append(seen, seenObs{area, o.ObserverID})
			h.Observers++
		}
		if from != Unknown && from == area && len(path) > 0 {
			h.Relayed = true
		}
		if from != Unknown && from != area && from == rec.Origin {
			h.Across = true
		}
		if h.From == "" && len(path) > 0 {
			h.From = l.hopKey(o, len(path)-1)
		}
		if h.Gate == "" {
			h.Gate = l.gate(o, path, area, rec.Origin)
		}
	}
	if len(heard) > 0 {
		sortHeard(heard)
		rec.Heard = append(make([]Heard, 0, len(heard)), heard...)
	}
	if len(via) > 0 {
		sortStrings(via)
		rec.Via = append(make([]string, 0, len(via)), via...)
	}
	return rec
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Insertion sorts: the slices hold a handful of areas, and sort.Slice would
// allocate.
func sortHeard(h []Heard) {
	for i := 1; i < len(h); i++ {
		for j := i; j > 0 && h[j].Area < h[j-1].Area; j-- {
			h[j], h[j-1] = h[j-1], h[j]
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
