package origin

import (
	"testing"

	"github.com/meshcore-analyzer/areamap"
)

func fp(v float64) *float64 { return &v }

// Three rectangles standing in for real boundaries: SCO above 55°N, IOI west
// of 5.5°W below it, and NEE east of 2.5°W below it. Knock's airport code
// places observers that report no position.
const testAreas = `{"type":"FeatureCollection","features":[
 {"type":"Feature","properties":{"code":"SCO","label":"Scotland"},
  "geometry":{"type":"Polygon","coordinates":[[[-8,55.0],[-0.5,55.0],[-0.5,61],[-8,61],[-8,55.0]]]}},
 {"type":"Feature","properties":{"code":"IOI","label":"Island of Ireland","iata":["NOC"]},
  "geometry":{"type":"Polygon","coordinates":[[[-11,51],[-5.5,51],[-5.5,55.0],[-11,55.0],[-11,51]]]}},
 {"type":"Feature","properties":{"code":"NEE","label":"North East","iata":["MME"]},
  "geometry":{"type":"Polygon","coordinates":[[[-2.5,54],[-0.5,54],[-0.5,55.0],[-2.5,55.0],[-2.5,54]]]}}
]}`

// The test mesh: two Scottish repeaters, one in Belfast, one in Newcastle, one
// in Dublin, and observers in Edinburgh, Knock (no position) and Teesside (no
// position).
func testLocator(t testing.TB, home string) *Locator {
	t.Helper()
	areas, err := areamap.ParseGeoJSON([]byte(testAreas))
	if err != nil {
		t.Fatal(err)
	}
	m, err := areamap.New(areas)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{
		{PublicKey: "AA111100", Name: "Edinburgh", Relay: true, Lat: fp(55.95), Lon: fp(-3.19)},
		{PublicKey: "AB222200", Name: "Glasgow", Relay: true, Lat: fp(55.86), Lon: fp(-4.25)},
		{PublicKey: "CC333300", Name: "Belfast", Relay: true, Lat: fp(54.60), Lon: fp(-5.93)},
		{PublicKey: "DD444400", Name: "Newcastle", Relay: true, Lat: fp(54.97), Lon: fp(-1.61)},
		{PublicKey: "AC555500", Name: "Dublin", Relay: true, Lat: fp(53.35), Lon: fp(-6.26)},
		{PublicKey: "EE666600", Name: "Dundee companion", Lat: fp(56.46), Lon: fp(-2.97)},
	}
	obs := []Observer{
		{ID: "OBS-EDI", IATA: "EDI", Lat: fp(56.05), Lon: fp(-3.29)},
		{ID: "OBS-NOC", IATA: "NOC"},
		{ID: "OBS-MME", IATA: "mme"},
	}
	return NewLocator(Options{Areas: m, Home: home}, nodes, obs)
}

func heard(r Record, area string) Heard {
	for _, h := range r.Heard {
		if h.Area == area {
			return h
		}
	}
	return Heard{}
}

func TestLocatorPlacesNodesObserversAndPrefixes(t *testing.T) {
	l := testLocator(t, "")
	if got := l.Key("aa111100"); got != "SCO" {
		t.Errorf("node by key = %q, want SCO", got)
	}
	if got := l.Key("ffff"); got != Unknown {
		t.Errorf("an unknown key = %q, want unknown", got)
	}
	if got := l.Prefix("CC33"); got != "IOI" {
		t.Errorf("a prefix one relay holds = %q, want IOI", got)
	}
	// "a" prefixes: Edinburgh and Glasgow are Scottish, Dublin is not.
	if got := l.Prefix("AA11"); got != "SCO" {
		t.Errorf("AA11 = %q, want SCO", got)
	}
	if got := l.Prefix("EE66"); got != Unknown {
		t.Errorf("a companion never places a hop, got %q", got)
	}
	if got := l.Observer("obs-noc"); got != "IOI" {
		t.Errorf("an observer with no position = %q, want IOI by its airport code", got)
	}
	if got := l.Observer("OBS-MME"); got != "NEE" {
		t.Errorf("airport codes match whatever their case, got %q", got)
	}
	if got := l.Observer("OBS-EDI"); got != "SCO" {
		t.Errorf("an observer with a position = %q, want SCO", got)
	}
	if name, area := l.Relay("ab22"); name != "Glasgow" || area != "SCO" {
		t.Errorf("relay ab22 = %q/%q, want Glasgow/SCO", name, area)
	}
	if name, area := l.Relay("9f9f"); name != "9F9F" || area != Unknown {
		t.Errorf("an unknown relay = %q/%q, want its prefix and unknown", name, area)
	}
}

func TestAShortPrefixPlacesOnlyWhenEveryHolderAgrees(t *testing.T) {
	l := NewLocator(Options{Areas: testLocator(t, "").areas}, []Node{
		{PublicKey: "A5000001", Relay: true, Lat: fp(55.95), Lon: fp(-3.19)}, // Scotland
		{PublicKey: "A5000002", Relay: true, Lat: fp(54.60), Lon: fp(-5.93)}, // Ireland
		{PublicKey: "A6000001", Relay: true, Lat: fp(55.95), Lon: fp(-3.19)}, // Scotland
		{PublicKey: "A6000002", Relay: true, Lat: fp(57.10), Lon: fp(-2.10)}, // Scotland
		{PublicKey: "A6000003", Relay: true},                                  // no position
	}, nil)
	if got := l.Prefix("A500"); got != Unknown {
		t.Errorf("a prefix shared across a border = %q, want unknown", got)
	}
	if got := l.Prefix("A600"); got != "SCO" {
		t.Errorf("a prefix shared inside one area = %q, want SCO", got)
	}
}

func TestDeriveSplitsRelayedFromHeardAcross(t *testing.T) {
	l := testLocator(t, "SCO")
	r := l.Derive(Packet{
		Hash: "h1", At: 1000, Type: 5, Route: 0, Scope: "#sco", Scoped: true,
		Observations: []Observation{
			{ObserverID: "OBS-EDI", Path: []string{"AA11"}},                 // from the Edinburgh repeater
			{ObserverID: "OBS-NOC", Path: []string{"AA11", "AB22", "CC33"}}, // after Belfast passed it on
			{ObserverID: "OBS-MME", Path: []string{"AA11", "AB22"}},         // straight from Glasgow
		},
	})
	if r.Origin != "SCO" || r.Basis != BasisRelay {
		t.Fatalf("origin = %q/%q, want SCO by first relay", r.Origin, r.Basis)
	}
	if r.Hash != "h1" || r.At != 1000 || r.Type != 5 || r.Scope != "#sco" || !r.Scoped {
		t.Errorf("packet fields not carried: %+v", r)
	}
	if h := heard(r, "IOI"); !h.Relayed || h.Across || h.Observers != 1 {
		t.Errorf("IOI = %+v, want relayed in by its own repeater", h)
	}
	if h := heard(r, "NEE"); h.Relayed || !h.Across {
		t.Errorf("NEE = %+v, want heard straight across", h)
	}
	if h := heard(r, "SCO"); !h.Relayed || h.Observers != 1 {
		t.Errorf("SCO = %+v, want relayed by its own repeater", h)
	}
	if len(r.Heard) != 3 || r.Heard[0].Area != "IOI" || r.Heard[1].Area != "NEE" || r.Heard[2].Area != "SCO" {
		t.Errorf("heard = %+v, want IOI, NEE, SCO in order", r.Heard)
	}
	if len(r.Via) != 2 || r.Via[0] != "IOI" || r.Via[1] != "SCO" {
		t.Errorf("via = %v, want [IOI SCO]", r.Via)
	}
	if h := heard(r, "IOI"); h.From != "cc33" || h.Gate != "ab22" {
		t.Errorf("IOI from/gate = %q/%q, want Belfast and the Glasgow relay that handed it over", h.From, h.Gate)
	}
}

func TestGateIsTheHomeRelayAtTheBoundary(t *testing.T) {
	// Arriving: the first home relay it reached.
	l := testLocator(t, "SCO")
	in := l.Derive(Packet{Type: 2, Observations: []Observation{
		{ObserverID: "OBS-EDI", Path: []string{"CC33", "AB22", "AA11"}},
	}})
	if h := heard(in, "SCO"); h.Gate != "ab22" {
		t.Errorf("arriving gate = %q, want ab22, the first Scottish relay", h.Gate)
	}
	// Without a home area there is no gate.
	none := testLocator(t, "").Derive(Packet{Type: 2, Observations: []Observation{
		{ObserverID: "OBS-NOC", Path: []string{"AA11", "AB22", "CC33"}},
	}})
	if h := heard(none, "IOI"); h.Gate != "" {
		t.Errorf("gate with no home area = %q, want none", h.Gate)
	}
}

func TestDeriveOrigin(t *testing.T) {
	l := testLocator(t, "")
	// An advert says where its sender is, whatever the path says.
	ad := l.Derive(Packet{Type: 4, Route: 1, Advert: &Advert{PublicKey: "x", Lat: fp(54.60), Lon: fp(-5.93)},
		Observations: []Observation{{ObserverID: "OBS-EDI", Path: []string{"AA11"}}}})
	if ad.Origin != "IOI" || ad.Basis != BasisAdvert {
		t.Errorf("advert origin = %q/%q, want IOI by advert", ad.Origin, ad.Basis)
	}
	// An advert at 0,0 falls back to the sender's node record.
	ad2 := l.Derive(Packet{Type: 4, Route: 1, Advert: &Advert{PublicKey: "DD444400", Lat: fp(0), Lon: fp(0)}})
	if ad2.Origin != "NEE" || ad2.Basis != BasisNode {
		t.Errorf("positionless advert origin = %q/%q, want NEE from its node record", ad2.Origin, ad2.Basis)
	}
	// Heard with no relay at all: the sender is in range of the observer.
	zh := l.Derive(Packet{Type: 2, Route: 1, Observations: []Observation{{ObserverID: "OBS-NOC"}}})
	if zh.Origin != "IOI" || zh.Basis != BasisHeard {
		t.Errorf("zero-hop origin = %q/%q, want IOI zero-hop", zh.Origin, zh.Basis)
	}
	// An observer that relayed the packet is the last hop of its own path.
	self := l.Derive(Packet{Type: 2, Route: 1, Observations: []Observation{{ObserverID: "OBS-MME", Relayed: true, Path: []string{"DD44"}}}})
	if self.Origin != "NEE" {
		t.Errorf("origin through a relaying observer = %q, want NEE", self.Origin)
	}
	if h := heard(self, "NEE"); h.Relayed {
		t.Errorf("an observer relaying its own copy is not its area relaying it in: %+v", h)
	}
	// A resolved key beats an ambiguous prefix.
	key := "cc333300"
	res := l.Derive(Packet{Type: 2, Route: 1, Observations: []Observation{{ObserverID: "OBS-EDI", Path: []string{"ZZZZ"}, Resolved: []string{key}}}})
	if res.Origin != "IOI" {
		t.Errorf("resolved origin = %q, want IOI", res.Origin)
	}
	// A 1-byte hop places nothing, even when CoreScope resolved it.
	one := l.Derive(Packet{Type: 2, Route: 1, Observations: []Observation{{ObserverID: "OBS-NOC", Path: []string{"AA", "CC"}, Resolved: []string{key, key}}}})
	if one.Origin != Unknown || one.Basis != BasisNone || len(one.Via) != 0 {
		t.Errorf("1-byte path: origin %q/%q via %v, want unknown and no relays placed", one.Origin, one.Basis, one.Via)
	}
	if h := heard(one, "IOI"); h.Relayed || h.Across || h.Observers != 1 {
		t.Errorf("1-byte path heard as %+v, want counted but neither relayed nor across", h)
	}
	// Two observers that disagree on the first relay: the majority decides.
	maj := l.Derive(Packet{Type: 2, Observations: []Observation{
		{ObserverID: "OBS-EDI", Path: []string{"AA11"}},
		{ObserverID: "OBS-NOC", Path: []string{"AB22", "CC33"}},
		{ObserverID: "OBS-MME", Path: []string{"DD44"}},
	}})
	if maj.Origin != "SCO" {
		t.Errorf("majority origin = %q, want SCO", maj.Origin)
	}
}

func TestAnObserverCountsOncePerArea(t *testing.T) {
	l := testLocator(t, "")
	r := l.Derive(Packet{Type: 2, Observations: []Observation{
		{ObserverID: "OBS-EDI", Path: []string{"AA11"}},
		{ObserverID: "obs-edi", Path: []string{"AB22", "AA11"}},
		{ObserverID: "OBS-UNPLACED"},
	}})
	if h := heard(r, "SCO"); h.Observers != 1 {
		t.Errorf("SCO observers = %d, want the one observer counted once", h.Observers)
	}
	if h := heard(r, Unknown); h.Observers != 1 {
		t.Errorf("an observer that cannot be placed still counts, under unknown: %+v", r.Heard)
	}
}

func TestLayersAreIndependent(t *testing.T) {
	areas, _ := areamap.ParseGeoJSON([]byte(`{"type":"FeatureCollection","features":[
	 {"type":"Feature","properties":{"code":"GB","layer":"country"},"geometry":{"type":"Polygon","coordinates":[[[-8,50],[2,50],[2,61],[-8,61],[-8,50]]]}},
	 {"type":"Feature","properties":{"code":"EDN","layer":"city"},"geometry":{"type":"Polygon","coordinates":[[[-3.4,55.85],[-3.0,55.85],[-3.0,56.0],[-3.4,56.0],[-3.4,55.85]]]}}]}`))
	m, err := areamap.New(areas)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{{PublicKey: "AA111100", Relay: true, Lat: fp(55.95), Lon: fp(-3.19)}}
	if got := NewLocator(Options{Areas: m, Layer: "country"}, nodes, nil).Key("AA111100"); got != "GB" {
		t.Errorf("country layer = %q, want GB", got)
	}
	if got := NewLocator(Options{Areas: m, Layer: "city"}, nodes, nil).Key("AA111100"); got != "EDN" {
		t.Errorf("city layer = %q, want EDN", got)
	}
}

func TestANilAreaMapPlacesNothing(t *testing.T) {
	l := NewLocator(Options{}, []Node{{PublicKey: "AA111100", Relay: true, Lat: fp(55.95), Lon: fp(-3.19)}}, nil)
	r := l.Derive(Packet{Type: 2, Observations: []Observation{{ObserverID: "X", Path: []string{"AA11"}}}})
	if r.Origin != Unknown || len(r.Heard) != 1 || r.Heard[0].Area != Unknown {
		t.Errorf("with no areas: %+v, want everything unknown", r)
	}
}

func BenchmarkDerive(b *testing.B) {
	l := testLocator(b, "SCO")
	key := "cc333300"
	p := Packet{Hash: "h", At: 1, Type: 5, Scope: "#sco", Scoped: true, Observations: []Observation{
		{ObserverID: "OBS-EDI", Path: []string{"AA11"}},
		{ObserverID: "OBS-NOC", Path: []string{"AA11", "AB22", "CC33"}, Resolved: []string{"", "", key}},
		{ObserverID: "OBS-MME", Path: []string{"AA11", "AB22"}},
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.Derive(p)
	}
}

func TestDeriveAllocations(t *testing.T) {
	l := testLocator(t, "SCO")
	p := Packet{Type: 5, Observations: []Observation{
		{ObserverID: "OBS-EDI", Path: []string{"AA11"}},
		{ObserverID: "OBS-NOC", Path: []string{"AA11", "AB22", "CC33"}},
		{ObserverID: "OBS-MME", Path: []string{"AA11", "AB22"}},
	}}
	// One slice for Heard and one for Via; nothing per hop or per observer.
	if n := testing.AllocsPerRun(200, func() { _ = l.Derive(p) }); n > 2 {
		t.Errorf("Derive allocates %.0f times per packet, want at most 2", n)
	}
}
