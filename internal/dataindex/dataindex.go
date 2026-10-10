// Package dataindex holds every packet of the last few days, one compact row
// per receiving area, and answers the Data tab from memory: counts along every
// dimension (the cube), a summary matrix, and the packets behind any figure.
package dataindex

import (
	"sync"
	"time"

	"github.com/meshcore-analyzer/origin"
)

type Window struct{ Key string }

var Windows = []Window{{"24h"}, {"7d"}, {"30d"}}

const (
	HowRelayed = "relayed"
	HowAcross  = "across"
	HowUnclear = "unclear"
)

const (
	ClassDirect       = "direct"
	ClassUnscoped     = "unscoped"
	ClassUnrecognised = "unrecognised"
	ClassScoped       = "scoped"
)

type Scope struct{ Key, Class string }

func ScopeOf(route int, scope string, scoped bool) Scope { return Scope{} }
func KindOf(typ, route int) string                       { return "" }

type Options struct {
	Areas []string
	Home  string
}

type Filter struct {
	D     string   `json:"d"`
	N     int      `json:"n"`
	V     []string `json:"v"`
	Exact int      `json:"x,omitempty"`
}

func (f Filter) matches(v string) bool { return false }

type row struct{ at int64 }

type Cube struct {
	BucketSeconds int64
	Complete      bool
	Buckets       []int64
	Origins       []string
	Heard         []string
	Types         []int
	Scopes        []Scope
	How           []string
	Routes        []int
	Through       []string
	Channels      []string
	Rows          [][10]int
}

type Cell struct {
	Origin, Heard            string
	Packets, Relayed, Across int
}

type RelayRow struct {
	Key, Name, Area string
	Packets         int
}

type Flow struct {
	Cell
	ViaHome     int
	From, Gates []RelayRow
}

type AreaView struct {
	Code       string
	Observed   bool
	Originated int
}

type Summary struct {
	Complete      bool
	CoveredFrom   time.Time
	Packets       int
	CrossBorder   int
	Bases         map[string]int
	Areas         []AreaView
	Matrix        []Cell
	MatrixByGroup map[string][]Cell
	Flows         []Flow
}

type PacketRow struct {
	Hash, TypeName, Kind, Border string
	At                           int64
	HeardFrom, Boundary          string
}

type PacketResult struct {
	Total int
	Rows  []PacketRow
}

type Index struct{ mu sync.RWMutex }

func New(opt Options) *Index                                { return &Index{} }
func (ix *Index) Replace(since int64, recs []origin.Record) {}
func (ix *Index) Prune(before int64)                        {}
func (ix *Index) SetCovered(from int64)                     {}
func (ix *Index) Len() int                                  { return 0 }
func (ix *Index) Cube(w Window, now time.Time) Cube         { return Cube{} }
func (ix *Index) Summary(w Window, now time.Time, relay func(string) (string, string)) Summary {
	return Summary{}
}
func (ix *Index) Packets(w Window, now time.Time, from, to int64, fs []Filter, relay func(string) (string, string)) PacketResult {
	return PacketResult{}
}
