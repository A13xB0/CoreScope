// Package origin decides where a MeshCore packet started and how each area
// heard it, from the packet's observations, the node positions CoreScope
// holds and a map of named areas.
package origin

import "github.com/meshcore-analyzer/areamap"

// Unknown is the area of anything that cannot be placed.
const Unknown = ""

// Bases: how a packet's starting area was decided.
const (
	BasisAdvert = "advert"
	BasisNode   = "node"
	BasisRelay  = "first-relay"
	BasisHeard  = "zero-hop"
	BasisNone   = "unknown"
)

type Options struct {
	Areas *areamap.Map
	Layer string
	Home  string
}

type Node struct {
	PublicKey string
	Name      string
	Relay     bool
	Lat, Lon  *float64
}

type Observer struct {
	ID       string
	IATA     string
	Lat, Lon *float64
}

type Packet struct {
	Hash         string
	At           int64
	Type         int
	Route        int
	Scope        string
	Scoped       bool
	Channel      string
	Advert       *Advert
	Observations []Observation
}

type Advert struct {
	PublicKey string
	Lat, Lon  *float64
}

type Observation struct {
	ObserverID string
	Relayed    bool
	Path       []string
	Resolved   []string
}

type Heard struct {
	Area      string
	Observers int
	Relayed   bool
	Across    bool
	From      string
	Gate      string
}

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
	Heard   []Heard
	Via     []string
}

type Locator struct{ areas *areamap.Map }

func NewLocator(opt Options, nodes []Node, observers []Observer) *Locator {
	return &Locator{areas: opt.Areas}
}

func (l *Locator) Key(pubkey string) string             { return Unknown }
func (l *Locator) Prefix(hex string) string             { return Unknown }
func (l *Locator) Relay(key string) (name, area string) { return "", Unknown }
func (l *Locator) Observer(id string) string            { return Unknown }
func (l *Locator) Derive(p Packet) Record               { return Record{} }
