package netwatch

import (
	"fmt"
	"testing"
)

const day = 24

// feed builds traffic hour by hour: rate(h) relays through node R heard by
// observer O, an advert from R every advertEvery hours while advertOn(h), and
// background traffic so the observer and the network have volume of their own.
type feed struct {
	w     *Watch
	tx    int64
	start int64
}

func newFeed(t *testing.T) *feed {
	t.Helper()
	w := New(Options{}, nil)
	return &feed{w: w, start: 500000}
}

func (f *feed) hour(h int64, obs string, relays map[string]int, adverts []string, background int) {
	abs := f.start + h
	for node, n := range relays {
		for i := 0; i < n; i++ {
			f.tx++
			f.w.AddObservation(f.tx, abs, obs, []string{node, "zz" + obs})
		}
	}
	for _, node := range adverts {
		f.tx++
		f.w.AddAdvert(f.tx, abs, node)
		f.w.AddObservation(f.tx, abs, obs, nil)
	}
	for i := 0; i < background; i++ {
		f.tx++
		f.w.AddObservation(f.tx, abs, obs, []string{"bg" + obs})
	}
}

// diurnal is busy by day and a tenth of that overnight.
func diurnal(h int64, peak int) int {
	if hh := h % 24; hh < 7 {
		return peak / 10
	}
	return peak
}

func TestSilentRepeaterIsFlaggedWhenAdvertsAndRelaysStop(t *testing.T) {
	f := newFeed(t)
	stop := int64(10 * day) // goes quiet at midnight on day 10
	for h := int64(0); h < 12*day; h++ {
		rate, adv := diurnal(h, 50), []string(nil)
		if h >= stop {
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"keir"}
		}
		f.hour(h, "obs1", map[string]int{"keir": rate}, adv, 80)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "keir" {
		t.Fatalf("open incidents = %+v, want keir", st.Open)
	}
	inc := st.Open[0]
	if inc.Start != f.start+stop-1 {
		t.Errorf("quiet since hour %d, want %d (the hour after its last advert)", inc.Start-f.start, stop-1)
	}
	if lag := inc.Flagged - (f.start + stop); lag < 0 || lag > 8 {
		t.Errorf("flagged %d h after it stopped, want within 8 h", lag)
	}
	if inc.Usual < 20 || inc.Now > inc.Usual/5 {
		t.Errorf("usual %d now %d", inc.Usual, inc.Now)
	}
}

func TestNightLullsAreNotOutages(t *testing.T) {
	f := newFeed(t)
	for h := int64(0); h < 14*day; h++ {
		var adv []string
		if h%2 == 0 {
			adv = []string{"r1"}
		}
		f.hour(h, "obs1", map[string]int{"r1": diurnal(h, 40)}, adv, diurnal(h, 100))
		f.w.Evaluate(f.start + h)
	}
	if st := f.w.Status(); len(st.Open)+len(st.Recent) != 0 {
		t.Fatalf("flagged a repeater that only went quiet at night: %+v %+v", st.Open, st.Recent)
	}
}

func TestAnObserverGoingOfflineDoesNotFlagTheRepeatersOnlyItHears(t *testing.T) {
	f := newFeed(t)
	stop := int64(10 * day)
	for h := int64(0); h < 12*day; h++ {
		var adv []string
		if h%2 == 0 {
			adv = []string{"r1"}
		}
		if h < stop { // obs1 hears r1 and then goes offline
			f.hour(h, "obs1", map[string]int{"r1": 30}, adv, 60)
		}
		f.hour(h, "obs2", nil, nil, 60) // the rest of the network carries on
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 0 {
		t.Fatalf("blamed the repeater for its observer: %+v", st.Open)
	}
	quiet := false
	for _, o := range st.Observers {
		if o.ID == "obs1" && o.Quiet {
			quiet = true
		}
	}
	if !quiet {
		t.Errorf("observer obs1 should be reported quiet: %+v", st.Observers)
	}
}

func TestARepeaterComesBack(t *testing.T) {
	f := newFeed(t)
	stop, back := int64(9*day), int64(10*day)
	for h := int64(0); h < 12*day; h++ {
		rate, adv := 40, []string(nil)
		if h >= stop && h < back {
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"r1"}
		}
		f.hour(h, "obs1", map[string]int{"r1": rate}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 0 || len(st.Recent) != 1 {
		t.Fatalf("open %+v recent %+v, want one closed incident", st.Open, st.Recent)
	}
	if end := st.Recent[0].End; end == nil || *end < f.start+back || *end > f.start+back+8 {
		t.Errorf("came back at %v, want soon after hour %d", end, back)
	}
}

// A quiet but steady repeater is judged on how unlikely its silence is: 12
// relays in every 6 h and then none is not chance. One whose traffic comes in
// bursts, often nothing for hours, is not flagged for the same silence.
func TestQuietRepeatersAreJudgedOnHowUnlikelyTheSilenceIs(t *testing.T) {
	f := newFeed(t)
	for h := int64(0); h < 12*day; h++ {
		steady, bursty, adv := 2, 0, []string(nil)
		if (h/6)%3 == 0 {
			bursty = 6 // 36 in one slot, then two slots of nothing
		}
		if h >= 10*day {
			steady, bursty = 0, 0
		} else if h%2 == 0 {
			adv = []string{"tiny", "burst"}
		}
		f.hour(h, "obs1", map[string]int{"tiny": steady, "burst": bursty}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "tiny" {
		t.Fatalf("open %+v, want only the steady quiet repeater", st.Open)
	}
	if p := st.Open[0].Chance; p <= 0 || p > 1e-4 {
		t.Errorf("chance %g, want a small probability as evidence", p)
	}
}

func TestAPacketHeardTwiceCountsOnce(t *testing.T) {
	w := New(Options{}, nil)
	w.AddObservation(1, 100, "a", []string{"r1"})
	w.AddObservation(1, 100, "b", []string{"r1"})
	if got := w.NodeSeries("r1", 100).Relays[len(w.NodeSeries("r1", 100).Relays)-1]; got != 1 {
		t.Fatalf("relays = %d, want 1", got)
	}
}

func TestOldHoursRollOff(t *testing.T) {
	w := New(Options{WindowHours: 48}, nil)
	w.AddObservation(1, 100, "a", []string{"r1"})
	w.AddObservation(2, 160, "a", []string{"r1"})
	s := w.NodeSeries("r1", 160)
	if len(s.Relays) != 48 {
		t.Fatalf("series length %d, want 48", len(s.Relays))
	}
	total := 0
	for _, v := range s.Relays {
		total += int(v)
	}
	if total != 1 {
		t.Fatalf("relays in window = %d, want 1 (hour 100 is outside a 48 h window ending at 160)", total)
	}
}

type grid map[string][2]float64

func (g grid) Position(pk string) (float64, float64, bool) { p, ok := g[pk]; return p[0], p[1], ok }

func TestCorridorsCountPacketsJoiningTwoZones(t *testing.T) {
	pos := grid{"north": {57.5, -4.2}, "south": {55.9, -3.2}, "mid": {56.9, -2.6}}
	w := New(Options{Corridors: []Corridor{{
		Name: "Highlands ⇄ central belt",
		A:    Zone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3},
		B:    Zone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2},
	}}}, pos)
	for h := int64(0); h < 9*day; h++ {
		n := 40
		if h >= 8*day {
			n = 10
		}
		for i := 0; i < n; i++ {
			id := h*1000 + int64(i)
			w.AddObservation(id, 1008+h, "o", []string{"north", "mid", "south"})
			w.AddObservation(id, 1008+h, "o2", []string{"north", "mid", "south"}) // heard twice, counted once
		}
		w.AddObservation(h*1000+999, 1008+h, "o", []string{"south"}) // not a crossing
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	if len(st.Corridors) != 1 {
		t.Fatalf("corridors %+v", st.Corridors)
	}
	c := st.Corridors[0]
	if c.LastDay != 10*24 || c.UsualDay != 40*24 {
		t.Errorf("last day %d usual %d, want %d and %d", c.LastDay, c.UsualDay, 240, 960)
	}
	if len(c.Carriers) == 0 || c.Carriers[0].Node != "mid" {
		t.Errorf("carriers %+v, want mid first", c.Carriers)
	}
	if fmt.Sprint(c.Daily[len(c.Daily)-1]) == "" {
		t.Error("no daily series")
	}
}

// A probe between two repeaters walks the route packets usually take between
// them and shows each hop against its own usual: the hop that broke stands out.
func TestProbeShowsWhichHopDroppedOff(t *testing.T) {
	w := New(Options{}, nil)
	var id int64
	for h := int64(0); h < 10*day; h++ {
		for i := 0; i < 30; i++ {
			id++
			path := []string{"a", "b", "c", "d"}
			if h >= 9*day {
				path = []string{"a", "b", "c"} // c stopped reaching d
			}
			w.AddObservation(id, 2000+h, "o", path)
		}
		for i := 0; i < 5; i++ { // a weaker detour a→x→d exists all along
			id++
			w.AddObservation(id, 2000+h, "o", []string{"a", "x", "d"})
		}
		w.Evaluate(2000 + h)
	}
	p := w.Probe("a", "d", 2000+10*day-1)
	if len(p.Route) != 4 || p.Route[0] != "a" || p.Route[3] != "d" || p.Route[2] != "c" {
		t.Fatalf("route %v, want a b c d (the usual way)", p.Route)
	}
	if len(p.Hops) != 3 {
		t.Fatalf("hops %+v", p.Hops)
	}
	for i, hop := range p.Hops {
		broken := hop.From == "c" && hop.To == "d"
		if broken != hop.Quiet {
			t.Errorf("hop %d %s→%s quiet=%v (now %d, usual %d)", i, hop.From, hop.To, hop.Quiet, hop.Now, hop.Usual)
		}
		if len(hop.Series) == 0 || len(hop.Series) != len(hop.Expected) {
			t.Errorf("hop %d has no series to chart", i)
		}
	}
	if p := w.Probe("a", "nowhere", 2000+10*day-1); len(p.Route) != 0 || p.Note == "" {
		t.Errorf("no route should say so: %+v", p)
	}
}

// A configured route counts the packets that pass its first repeater and then
// its last, and says how the last day and the last slot compare with usual.
func TestConfiguredRoutesShowBetterOrWorse(t *testing.T) {
	w := New(Options{Routes: []Route{{Name: "North link", From: "a", To: "d"}, {Name: "Back the other way", From: "d", To: "a"}}}, nil)
	var id int64
	for h := int64(0); h < 9*day; h++ {
		n := 30
		if h >= 8*day {
			n = 12 // the last day is worse
		}
		for i := 0; i < n; i++ {
			id++
			w.AddObservation(id, 1008+h, "o", []string{"a", "b", "d"})
			w.AddObservation(id, 1008+h, "o2", []string{"a", "b", "d"}) // heard twice, counted once
		}
		id++
		w.AddObservation(id, 1008+h, "o", []string{"a", "b"}) // never reaches d
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	if len(st.Routes) != 2 {
		t.Fatalf("routes %+v", st.Routes)
	}
	r := st.Routes[0]
	if r.Name != "North link" || r.LastDay != 12*24 || r.UsualDay != 30*24 {
		t.Errorf("route %+v, want last day %d usual %d", r, 12*24, 30*24)
	}
	if r.Now != 12*6 || r.Usual != 30*6 {
		t.Errorf("slot now %d usual %d", r.Now, r.Usual)
	}
	if back := st.Routes[1]; back.LastDay != 0 || back.UsualDay != 0 {
		t.Errorf("reverse route counted packets going the other way: %+v", back)
	}
}

// Boxes cannot follow a border (any box round the Borders takes in
// Northumberland too), so a zone may be a polygon of [lat, lon] points.
func TestPolygonZonesFollowABorder(t *testing.T) {
	scotland := Zone{Name: "Scotland", Polygon: [][2]float64{{61, -8}, {61, -0.7}, {55.81, -0.7}, {55.81, -2.03}, {55.6, -2.2}, {55.36, -2.48}, {55.17, -2.75}, {54.98, -3.06}, {54.6, -3.6}, {54.6, -5.3}, {55.5, -5.3}, {55.5, -8}}}
	for _, c := range []struct {
		name     string
		lat, lon float64
		in       bool
	}{
		{"Edinburgh", 55.95, -3.19, true},
		{"Kelso, in the Borders", 55.6, -2.43, true},
		{"Galloway", 54.85, -4.4, true},
		{"Berwick, in England", 55.77, -2.0, false},
		{"Newcastle", 54.97, -1.61, false},
		{"Carlisle", 54.89, -2.93, false},
	} {
		if got := scotland.contains(c.lat, c.lon); got != c.in {
			t.Errorf("%s: in Scotland = %v, want %v", c.name, got, c.in)
		}
	}
}

// With a 14-day baseline, an outage of five days is still an outage: the
// median of the same hours has not caught up with it.
func TestALongOutageKeepsItsUsual(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 22 * day}, nil)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		for i := 0; i < 30; i++ {
			id++
			path := []string{"a", "b", "c"}
			if h >= 15*day {
				path = []string{"a", "b"} // b stopped reaching c five days ago
			}
			w.AddObservation(id, 2016+h, "o", path)
		}
		w.Evaluate(2016 + h)
	}
	p := w.Probe("a", "c", 2016+20*day-1)
	if len(p.Hops) != 2 || !p.Hops[1].Quiet {
		t.Fatalf("after five days the broken hop should still be quiet: %+v", p.Hops)
	}
}

// A repeater far from every observer may have its adverts heard only now and
// then; once it relays its usual traffic again it is back.
func TestAnIncidentClosesWhenRelaysReturnEvenIfNoAdvertIsHeard(t *testing.T) {
	f := newFeed(t)
	stop, back := int64(9*day), int64(10*day)
	for h := int64(0); h < 12*day; h++ {
		rate, adv := 40, []string(nil)
		if h >= stop && h < back {
			rate = 0
		} else if h < stop && h%2 == 0 {
			adv = []string{"r1"} // no advert is heard after the outage
		}
		f.hour(h, "obs1", map[string]int{"r1": rate}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 0 || len(st.Recent) != 1 {
		t.Fatalf("open %+v recent %+v, want it back", st.Open, st.Recent)
	}
}

// Only repeaters inside the judged area are judged; others are still counted
// (for routes, links and corridors) but never flagged.
func TestOnlyRepeatersInsideTheAreaAreJudged(t *testing.T) {
	pos := grid{"inside": {56.0, -3.2}, "outside": {52.5, -3.5}}
	f := &feed{w: New(Options{Area: &Zone{Name: "Scotland", LatMin: 54.6, LatMax: 61, LonMin: -8, LonMax: -0.7}}, pos), start: 500000}
	for h := int64(0); h < 12*day; h++ {
		rate := 40
		var adv []string
		if h >= 10*day {
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"inside", "outside"}
		}
		f.hour(h, "obs1", map[string]int{"inside": rate, "outside": rate}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "inside" {
		t.Fatalf("open %+v, want only the repeater inside the area", st.Open)
	}
	// Repeaters with no position are judged: many publish none.
}

// When the observer that usually hears a route or a hop goes offline, the
// route and the hop drop too; they say the observer is down instead of
// calling the mesh worse.
func TestRoutesAndHopsBlameAnOfflineObserver(t *testing.T) {
	w := New(Options{Routes: []Route{{Name: "Gateway to Ayr", From: "g", To: "ayr"}}}, nil)
	var id int64
	for h := int64(0); h < 10*day; h++ {
		for i := 0; i < 30; i++ {
			id++
			if h < 9*day { // the Ayr observer hears it all, then goes offline
				w.AddObservation(id, 1008+h, "ayr-obs", []string{"g", "m", "ayr"})
			}
		}
		for i := 0; i < 40; i++ { // the rest of the network carries on
			id++
			w.AddObservation(id, 1008+h, "other", []string{"x"})
		}
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	if r := st.Routes[0]; len(r.ObserversDown) != 1 || r.ObserversDown[0] != "ayr-obs" {
		t.Errorf("route %+v should name ayr-obs as down", r)
	}
	p := w.Probe("g", "ayr", 1008+10*day-1)
	if len(p.Hops) != 2 {
		t.Fatalf("hops %+v", p.Hops)
	}
	for _, h := range p.Hops {
		if h.Quiet || len(h.ObserversDown) != 1 {
			t.Errorf("hop %s→%s quiet=%v down=%v: the observer is to blame, not the hop", h.From, h.To, h.Quiet, h.ObserversDown)
		}
	}
}

// A corridor is judged in each direction: a packet travels from the zone its
// path reaches first. A drop one way must not hide behind the other.
func TestCorridorsAreJudgedInEachDirection(t *testing.T) {
	pos := grid{"north": {57.5, -4.2}, "south": {55.9, -3.2}, "mid": {56.9, -2.6}}
	w := New(Options{Corridors: []Corridor{{
		Name: "Highlands and central belt",
		A:    Zone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3},
		B:    Zone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2},
	}}}, pos)
	var id int64
	for h := int64(0); h < 9*day; h++ {
		south := 40 // central belt to Highlands halves on the last day
		if h >= 8*day {
			south = 20
		}
		for i := 0; i < south; i++ {
			id++
			w.AddObservation(id, 1008+h, "o", []string{"south", "mid", "north"})
		}
		for i := 0; i < 10; i++ { // the other way carries on as usual
			id++
			w.AddObservation(id, 1008+h, "o", []string{"north", "mid", "south"})
		}
		w.Evaluate(1008 + h)
	}
	c := w.Status().Corridors[0]
	if len(c.Directions) != 2 {
		t.Fatalf("directions %+v", c.Directions)
	}
	ab, ba := c.Directions[0], c.Directions[1]
	if ab.From != "Highlands" || ab.To != "Central belt" || ab.LastDay != 10*24 || ab.UsualDay != 10*24 {
		t.Errorf("Highlands → central belt %+v, want unchanged at %d a day", ab, 240)
	}
	if ba.From != "Central belt" || ba.To != "Highlands" || ba.LastDay != 20*24 || ba.UsualDay != 40*24 {
		t.Errorf("central belt → Highlands %+v, want %d against %d", ba, 480, 960)
	}
	if len(ba.Carriers) == 0 || ba.Carriers[0].Node != "mid" {
		t.Errorf("carriers one way %+v", ba.Carriers)
	}
}

// Corridors and routes are also judged on adverts, which every repeater
// sends on its own schedule: of the adverts from one zone (or passing the
// route's first repeater), the share that reach the other zone (or pass its
// last). Chat bursts do not move it.
func TestAdvertReachIsSteadyWhereTrafficIsNot(t *testing.T) {
	pos := grid{"n1": {57.5, -4.2}, "n2": {57.6, -4.0}, "mid": {56.9, -2.6}, "s1": {55.9, -3.2}}
	w := New(Options{
		Corridors: []Corridor{{Name: "H and C", A: Zone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3}, B: Zone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2}}},
		Routes:    []Route{{Name: "North link", From: "mid", To: "s1"}},
	}, pos)
	var id int64
	for h := int64(0); h < 17*day; h++ {
		broken := h >= 16*day // on the last day only a quarter of the adverts get through
		for _, origin := range []string{"n1", "n2"} {
			for i := 0; i < 4; i++ { // four adverts an hour from each Highland repeater
				id++
				path := []string{"mid", "s1"}
				if broken && i > 0 {
					path = []string{"mid"} // it dies at mid
				}
				w.AddAdvertObservation(id, 1008+h, "o", origin, path)
			}
		}
		chat := 10 // chat across the corridor, bursty, unrelated to the link
		if h%24 == 20 {
			chat = 200
		}
		for i := 0; i < chat; i++ {
			id++
			w.AddObservation(id, 1008+h, "o", []string{"n1", "mid", "s1"})
		}
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	d := st.Corridors[0].Directions[0]
	if d.Reach == nil {
		t.Fatalf("no advert reach on %+v", d)
	}
	if d.Reach.Usual < 0.99 || d.Reach.Last > 0.3 || d.Reach.Last < 0.2 {
		t.Errorf("Highlands → central belt reach last %.2f usual %.2f, want about 0.25 against 1.0", d.Reach.Last, d.Reach.Usual)
	}
	if d.Reach.LastAdverts != 2*4*24 {
		t.Errorf("adverts from the Highlands yesterday = %d, want %d", d.Reach.LastAdverts, 2*4*24)
	}
	if len(d.Reach.Daily) == 0 {
		t.Error("no daily reach for a chart")
	}
	if back := st.Corridors[0].Directions[1]; back.Reach != nil {
		t.Errorf("no adverts start in the central belt, so no reach the other way: %+v", back.Reach)
	}
	r := st.Routes[0]
	if r.Reach == nil || r.Reach.Usual < 0.99 || r.Reach.Last > 0.3 {
		t.Errorf("route reach %+v, want about 0.25 against 1.0", r.Reach)
	}
}

// An advert no repeater relayed never entered the mesh, so it could not
// cross anything: it is not counted towards reach.
func TestAdvertsNobodyRelayedDoNotCountTowardsReach(t *testing.T) {
	pos := grid{"n1": {57.5, -4.2}, "s1": {55.9, -3.2}}
	w := New(Options{Corridors: []Corridor{{Name: "H and C", A: Zone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3}, B: Zone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2}}}}, pos)
	var id int64
	for h := int64(0); h < 9*day; h++ {
		for i := 0; i < 4; i++ {
			id++
			w.AddAdvertObservation(id, 1008+h, "o", "n1", []string{"s1"}) // relayed, got through
			id++
			w.AddAdvertObservation(id, 1008+h, "o", "n1", nil) // heard directly, never relayed
		}
		w.Evaluate(1008 + h)
	}
	r := w.Status().Corridors[0].Directions[0].Reach
	if r == nil || r.Last != 1 || r.LastAdverts != 4*24 {
		t.Fatalf("reach %+v, want every relayed advert counted and none of the direct ones", r)
	}
}

// Where adverts no longer cross (a neighbouring network that stopped
// flooding them), a corridor or route can be judged on channel messages
// instead: their origin is the first repeater that relayed them.
func TestChatBasedReachIgnoresAdverts(t *testing.T) {
	pos := grid{"ne1": {54.9, -1.6}, "bord": {55.4, -2.0}, "s1": {55.9, -3.2}}
	ne := Zone{Name: "North East", LatMin: 54.5, LatMax: 55.5, LonMin: -2.5, LonMax: -1}
	sc := Zone{Name: "Scotland", LatMin: 55.55, LatMax: 59, LonMin: -6, LonMax: -1.9}
	w := New(Options{
		Corridors: []Corridor{{Name: "NE and S", A: ne, B: sc, Basis: BasisChat}},
		Routes:    []Route{{Name: "Border", From: "ne1", To: "s1", Basis: BasisChat}},
	}, pos)
	var id int64
	for h := int64(0); h < 17*day; h++ {
		broken := h >= 16*day
		for i := 0; i < 4; i++ {
			id++
			path := []string{"ne1", "bord", "s1"}
			if broken && i > 0 {
				path = []string{"ne1", "bord"} // three in four die at the border
			}
			w.AddChatObservation(id, 1008+h, "o", path)
		}
		if h < 8*day { // NE adverts stop crossing half way through
			id++
			w.AddAdvertObservation(id, 1008+h, "o", "ne1", []string{"bord", "s1"})
		}
		id++
		w.AddChatObservation(id, 1008+h, "o", []string{"", "s1"}) // first relay unknown: no origin
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	r := st.Corridors[0].Directions[0].Reach
	if r == nil || r.Basis != BasisChat || r.Usual != 1 || r.Last != 0.25 || r.LastAdverts != 96 {
		t.Errorf("NE → Scotland chat reach %+v, want a quarter of 96 messages yesterday against all of them usually", r)
	}
	rr := st.Routes[0].Reach
	if rr == nil || rr.Basis != BasisChat || rr.Usual != 1 || rr.Last != 0.25 {
		t.Errorf("border route chat reach %+v", rr)
	}
}

// Repeaters are compared through the observers that are up both now and in
// the usual: one that dies while its main observer is offline is still
// caught through the others, at the right strength.
func TestARepeaterThatDiesWhileItsMainObserverIsDownIsStillCaught(t *testing.T) {
	f := newFeed(t)
	obsGone, dies := int64(9*day), int64(10*day)
	for h := int64(0); h < 12*day; h++ {
		var adv []string
		rate := 30
		if h >= dies {
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"r1"}
		}
		if h < obsGone { // obs1 hears most of r1, then goes offline
			f.hour(h, "obs1", map[string]int{"r1": rate}, adv, 60)
		}
		f.hour(h, "obs2", map[string]int{"r1": rate / 6}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "r1" {
		t.Fatalf("open %+v, want r1 caught through obs2", st.Open)
	}
	if st.Open[0].Observers != 1 {
		t.Errorf("judged through %d observers, want 1 (obs2)", st.Open[0].Observers)
	}
}

// When every observer goes quiet at once the feed is the problem: nothing is
// flagged, and the status says so.
func TestAFeedOutageFlagsNothing(t *testing.T) {
	f := newFeed(t)
	cut := int64(10 * day)
	for h := int64(0); h < 10*day+10; h++ {
		if h < cut {
			for _, o := range []string{"obs1", "obs2", "obs3"} {
				var adv []string
				if h%2 == 0 {
					adv = []string{"r" + o}
				}
				f.hour(h, o, map[string]int{"r" + o: 30}, adv, 60)
			}
		}
		f.w.Evaluate(f.start + h) // hours with no packets at all are still judged
	}
	st := f.w.Status()
	if len(st.Open) != 0 {
		t.Errorf("flagged during a feed outage: %+v", st.Open)
	}
	if !st.Feed.Down || st.Feed.Since != f.start+cut {
		t.Errorf("feed %+v, want down since hour %d", st.Feed, cut)
	}
}

// A repeater still heard (its adverts arrive) that stopped relaying while its
// neighbours carry on is a gray failure.
func TestARepeaterHeardButNotRelayingIsFlagged(t *testing.T) {
	f := newFeed(t)
	stop := int64(10 * day)
	for h := int64(0); h < 12*day; h++ {
		rate := 30
		if h >= stop {
			rate = 0
		}
		f.hour(h, "obs1", map[string]int{"deaf": rate, "nb1": 30, "nb2": 30}, []string{"deaf", "nb1", "nb2"}, 60)
		f.hour(h, "obs2", map[string]int{"deaf": rate / 3, "nb1": 10, "nb2": 10}, []string{"deaf"}, 60)
		for i := 0; i < 20; i++ { // deaf usually passes traffic between its neighbours
			f.tx++
			path := []string{"nb1", "deaf", "nb2"}
			if h >= stop {
				path = []string{"nb1", "nb2"}
			}
			f.w.AddObservation(f.tx, f.start+h, "obs1", path)
		}
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "deaf" || st.Open[0].Kind != KindNotRelaying {
		t.Fatalf("open %+v, want deaf flagged as heard but not relaying", st.Open)
	}
}

// A repeater that keeps going quiet says how often.
func TestRepeatedIncidentsAreCounted(t *testing.T) {
	f := newFeed(t)
	for h := int64(0); h < 21*day; h++ {
		rate, adv := 40, []string(nil)
		if d := h / day; d == 9 || d == 13 || d == 17 || d == 20 { // a day off now and then
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"r1"}
		}
		f.hour(h, "obs1", map[string]int{"r1": rate}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Repeats != 4 {
		t.Fatalf("open %+v, want the fourth time it went quiet", st.Open)
	}
}

// A repeater that goes dark every night (solar, small battery) is recognised
// as such, and warned about when it starts going dark earlier.
func TestNightlyDropOutsAreRecognisedAndAnEarlierStartIsWarned(t *testing.T) {
	f := newFeed(t)
	f.start = 500016 // midnight UTC
	for h := int64(0); h < 14*day; h++ {
		hh, d := h%24, h/24
		dark := int64(20)
		if d >= 11 {
			dark = 18 // the battery is giving out sooner
		}
		on := hh >= 7 && hh < dark
		rate, adv := 0, []string(nil)
		if on {
			rate = 20
			adv = []string{"solar"}
		}
		f.hour(h, "obs1", map[string]int{"solar": rate, "mains": 20}, append(adv, "mains"), 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open)+len(st.Recent) != 0 {
		t.Errorf("nightly drop-outs raised incidents: %+v %+v", st.Open, st.Recent)
	}
	if len(st.Nightly) != 1 || st.Nightly[0].Node != "solar" || !st.Nightly[0].Earlier || st.Nightly[0].DarkFrom != 18 {
		t.Fatalf("nightly %+v, want solar going dark earlier, from 18:00", st.Nightly)
	}
}

// A repeater that appeared a week ago is judged on the days it has been on
// air, not on the zeros before it existed.
func TestANewRepeaterIsJudgedOnTheDaysItHasBeenOnAir(t *testing.T) {
	f := &feed{w: New(Options{Days: 14}, nil), start: 500000}
	for h := int64(0); h < 18*day; h++ {
		rel, adv := map[string]int{"old": 30}, []string{}
		if h%2 == 0 {
			adv = append(adv, "old")
		}
		if h >= 9*day && h < 17*day { // "new" appears on day 9, dies on day 17
			rel["new"] = 30
			if h%2 == 0 {
				adv = append(adv, "new")
			}
		}
		f.hour(h, "obs1", rel, adv, 60)
		f.hour(h, "obs2", map[string]int{"new": rel["new"] / 3, "old": 10}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "new" {
		t.Fatalf("open %+v, want new flagged", st.Open)
	}
}

// Flagged as not relaying while its adverts still came, a repeater whose
// adverts then stop too has gone quiet altogether, and says so.
func TestNotRelayingTurnsQuietWhenTheAdvertsStopToo(t *testing.T) {
	f := newFeed(t)
	stop, mute := int64(10*day), int64(10*day+8)
	for h := int64(0); h < 11*day; h++ {
		rate := 30
		if h >= stop {
			rate = 0
		}
		adv := []string{"nb1", "nb2"}
		if h < mute {
			adv = append(adv, "deaf")
		}
		f.hour(h, "obs1", map[string]int{"deaf": rate, "nb1": 30, "nb2": 30}, adv, 60)
		f.hour(h, "obs2", map[string]int{"deaf": rate / 3, "nb1": 10, "nb2": 10}, adv, 60)
		for i := 0; i < 20; i++ {
			f.tx++
			path := []string{"nb1", "deaf", "nb2"}
			if h >= stop {
				path = []string{"nb1", "nb2"}
			}
			f.w.AddObservation(f.tx, f.start+h, "obs1", path)
		}
		f.w.Evaluate(f.start + h)
	}
	st := f.w.Status()
	if len(st.Open) != 1 || st.Open[0].Kind != KindQuiet || st.Open[0].Start != f.start+stop {
		t.Fatalf("open %+v, want deaf quiet since hour %d", st.Open, stop)
	}
}

// reachFeed sends n adverts an hour from origin, of which share get through
// to the far end via relay ("" for straight through), for the days asked.
func reachFeed(w *Watch, id *int64, h int64, origin string, n int, share float64, via, alt string) {
	for i := 0; i < n; i++ {
		*id++
		path := []string{"mid"}
		if float64(i) < share*float64(n) {
			path = []string{"mid", via, "s1"}
			if via == "" {
				path = []string{"mid", "s1"}
			}
		} else if alt != "" {
			path = []string{"mid", alt}
		}
		w.AddAdvertObservation(*id, h, "o", origin, path)
	}
}

var reachZones = []Corridor{{Name: "H and C", A: Zone{Name: "Highlands", LatMin: 57.3, LatMax: 59, LonMin: -6, LonMax: -3}, B: Zone{Name: "Central belt", LatMin: 55.5, LatMax: 56.4, LonMin: -5, LonMax: -2}}}
var reachGrid = grid{"n1": {57.5, -4.2}, "mid": {57.4, -4.0}, "keir": {57.17, -2.3}, "goyle": {56.93, -2.52}, "s1": {55.9, -3.2}}

// When a share drops and stays down, the corridor says since when and keeps
// comparing with the level before, even once most of the baseline is after.
func TestAReachThatDroppedSaysSinceWhenAndKeepsTheOldUsual(t *testing.T) {
	w := New(Options{WindowHours: 26 * day, Days: 14, Corridors: reachZones}, reachGrid)
	var id int64
	base := int64(1008)
	for h := int64(0); h < 26*day; h++ {
		share := 0.9
		if h >= 16*day {
			share = 0.3
		}
		reachFeed(w, &id, base+h, "n1", 8, share, "keir", "")
		w.Evaluate(base + h)
	}
	r := w.Status().Corridors[0].Directions[0].Reach
	if r == nil || r.Since == nil || *r.Since != base+16*day || r.Usual < 0.85 || r.Verdict != "worse" {
		t.Fatalf("reach %+v, want worse since day 16 against about 0.9", r)
	}
}

// A share is judged against the range it usually falls in: a share that
// swings from day to day is not worse for an ordinary low day.
func TestAReachIsJudgedAgainstItsUsualRange(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: reachZones}, reachGrid)
	var id int64
	swings := []float64{0.8, 0.5, 0.9, 0.55, 0.85, 0.6, 0.75, 0.9, 0.5, 0.8, 0.6, 0.85, 0.55, 0.9, 0.7, 0.8, 0.6, 0.85, 0.9, 0.55}
	for h := int64(0); h < 20*day; h++ {
		reachFeed(w, &id, 1008+h, "n1", 20, swings[h/day], "keir", "")
		w.Evaluate(1008 + h)
	}
	r := w.Status().Corridors[0].Directions[0].Reach
	if r == nil || r.Verdict != "normal" || r.Low > 0.55 {
		t.Fatalf("reach %+v, want normal with a range that allows its low days", r)
	}
}

// When a corridor gets worse, the watch names the repeater whose crossings
// fell away; and when its main carrier changes, it says it was rerouted.
func TestAWorseCorridorNamesItsLikelyCauseAndReroute(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: reachZones}, reachGrid)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		if h < 19*day {
			reachFeed(w, &id, 1008+h, "n1", 10, 0.9, "keir", "")
		} else { // keir is gone; a few get through via goyle
			reachFeed(w, &id, 1008+h, "n1", 10, 0.3, "goyle", "")
		}
		w.Evaluate(1008 + h)
	}
	d := w.Status().Corridors[0].Directions[0]
	if d.Reach == nil || d.Reach.Verdict != "worse" || d.Reach.Cause == nil || d.Reach.Cause.Node != "keir" || d.Reach.Cause.Explains < 0.9 {
		t.Fatalf("reach %+v, want worse with keir as the cause", d.Reach)
	}
	if d.Reroute == nil || d.Reroute.From != "keir" || d.Reroute.To != "goyle" || d.Reroute.Since != 1008+19*day {
		t.Errorf("reroute %+v, want keir to goyle on day 19", d.Reroute)
	}
}

// With a target, a corridor says whether it is burning through it fast (the
// last 6 and 24 hours both under) or slowly (the last 24 and 72 hours).
func TestACorridorWithATargetSaysHowFastItIsBurning(t *testing.T) {
	zones := []Corridor{reachZones[0]}
	zones[0].Target = 0.7
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: zones}, reachGrid)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		share := 0.9
		if h >= 20*day-12 {
			share = 0.2
		}
		reachFeed(w, &id, 1008+h, "n1", 10, share, "keir", "")
		w.Evaluate(1008 + h)
	}
	r := w.Status().Corridors[0].Directions[0].Reach
	if r == nil || r.Target != 0.7 || r.Burn != "fast" {
		t.Fatalf("reach %+v, want burning fast against 0.7", r)
	}
}

// A repeater whose signal at an observer has fallen steadily over the last
// days is reported, before it fails; a steady one is not.
func TestSignalDriftIsReported(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 20 * day}, nil)
	for h := int64(0); h < 20*day; h++ {
		w.Evaluate(1008 + h)
		for i := 0; i < 3; i++ {
			snr := 6.0
			if d := h / day; d >= 16 {
				snr = 6 - 3*float64(d-15) // 3, 0, -3, -6
			}
			w.AddSignal(1008+h, "obs1", "fading", snr)
			w.AddSignal(1008+h, "obs1", "steady", 4)
		}
	}
	w.Evaluate(1008 + 20*day)
	st := w.Status()
	if len(st.Drift) != 1 || st.Drift[0].Node != "fading" || st.Drift[0].Observer != "obs1" || st.Drift[0].Before < 5.5 || st.Drift[0].Now > -2 {
		t.Fatalf("drift %+v, want fading at obs1 from about 6 dB to about -3 dB", st.Drift)
	}
}

// Adverts carry the time they were sent, so the watch knows how often a
// repeater adverts and what share of them reach any observer.
func TestAdvertTimestampsGiveDelivery(t *testing.T) {
	w := New(Options{Days: 7, WindowHours: 12 * day}, nil)
	var id int64
	for h := int64(0); h < 12*day; h++ {
		for half := int64(0); half < 2; half++ { // every 30 minutes
			sent := (1008+h)*3600 + half*1800
			if h >= 11*day && half == 1 {
				continue // on the last day only half are heard
			}
			id++
			w.AddAdvertObservation(id, 1008+h, "o", "r1", []string{"x"})
			w.AddAdvertSent("r1", sent)
		}
		w.Evaluate(1008 + h)
	}
	s := w.NodeSeries("r1", 1008+12*day-1)
	if s.AdvertEveryMin != 30 {
		t.Errorf("advert interval %d min, want 30", s.AdvertEveryMin)
	}
	if n := len(s.AdvertDelivery); n == 0 || s.AdvertDelivery[n-1] < 0.45 || s.AdvertDelivery[n-1] > 0.55 || s.AdvertDelivery[n-2] < 0.95 {
		t.Errorf("delivery %v, want about 1 then 0.5 on the last day", s.AdvertDelivery)
	}
}

// What happened, in order: incidents opening and closing, steps, reroutes.
// An incident also lists the corridors and routes it explains.
func TestTimelineAndWhatAnIncidentAffects(t *testing.T) {
	pos := grid{"n1": {57.5, -4.2}, "mid": {57.4, -4.0}, "keir": {57.17, -2.3}, "goyle": {56.93, -2.52}, "s1": {55.9, -3.2}}
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: reachZones}, pos)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		if h < 18*day {
			reachFeed(w, &id, 1008+h, "n1", 10, 0.9, "keir", "")
			for i := 0; i < 30; i++ { // keir's own traffic, so it is judged
				id++
				w.AddObservation(id, 1008+h, "o", []string{"keir", "s1"})
			}
			if h%2 == 0 {
				id++
				w.AddAdvertObservation(id, 1008+h, "o", "keir", []string{"s1"})
			}
		} else {
			reachFeed(w, &id, 1008+h, "n1", 10, 0.3, "goyle", "")
		}
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	if len(st.Open) != 1 || st.Open[0].Node != "keir" {
		t.Fatalf("open %+v", st.Open)
	}
	if a := st.Open[0].Affects; len(a) != 1 || a[0] != "H and C: Highlands → Central belt" {
		t.Errorf("keir affects %v", a)
	}
	kinds := map[string]bool{}
	for _, e := range st.Events {
		kinds[e.Kind] = true
	}
	for _, k := range []string{"quiet", "step", "reroute"} {
		if !kinds[k] {
			t.Errorf("timeline %+v has no %s", st.Events, k)
		}
	}
	for i := 1; i < len(st.Events); i++ {
		if st.Events[i].Hour > st.Events[i-1].Hour {
			t.Fatalf("timeline not newest first: %+v", st.Events)
		}
	}
}

// When the repeater a corridor lost has itself gone quiet, it is named as the
// cause, even if a repeater downstream of it lost as many crossings.
func TestTheQuietRepeaterIsPreferredAsTheCause(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: reachZones}, reachGrid)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		if h < 19*day {
			for i := 0; i < 15; i++ { // 9 via keir and goyle, 5 via goyle alone, 1 lost
				id++
				path := []string{"mid", "keir", "goyle", "s1"}
				if i >= 9 {
					path = []string{"mid", "goyle", "s1"}
				}
				if i == 14 {
					path = []string{"mid"}
				}
				w.AddAdvertObservation(id, 1008+h, "o", "n1", path)
			}
			for i := 0; i < 30; i++ {
				id++
				w.AddObservation(id, 1008+h, "o", []string{"keir", "s1"})
			}
			if h%2 == 0 {
				id++
				w.AddAdvertObservation(id, 1008+h, "o", "keir", []string{"s1"})
			}
		} else { // keir gone; goyle drops out of these paths too
			reachFeed(w, &id, 1008+h, "n1", 15, 0.3, "", "")
		}
		w.Evaluate(1008 + h)
	}
	st := w.Status()
	c := st.Corridors[0].Directions[0].Reach.Cause
	if c == nil || c.Node != "keir" {
		t.Fatalf("cause %+v, want keir (quiet) rather than goyle (which lost more); open %+v candidates %+v", c, st.Open, st.Corridors[0].Directions[0].Reach.candidates)
	}
}

// Adverts flood on past where they cross. A repeater beyond the far zone
// says nothing about the crossing, so losing sight of it (its observer went
// offline) does not make it the cause.
func TestRepeatersBeyondTheCrossingAreNotCauses(t *testing.T) {
	pos := grid{"n1": {57.5, -4.2}, "mid": {57.4, -4.0}, "keir": {57.17, -2.3}, "s1": {55.9, -3.2}, "ayr": {55.46, -4.63}}
	w := New(Options{Days: 14, WindowHours: 20 * day, Corridors: reachZones}, pos)
	var id int64
	for h := int64(0); h < 20*day; h++ {
		for i := 0; i < 10; i++ {
			id++
			path := []string{"mid", "keir", "s1", "ayr"} // ayr hears it after it crossed
			if h >= 19*day {
				path = []string{"mid"} // keir gone, and ayr's observer with it
				if i < 3 {
					path = []string{"mid", "s1"}
				}
			}
			w.AddAdvertObservation(id, 1008+h, "o", "n1", path)
		}
		w.Evaluate(1008 + h)
	}
	c := w.Status().Corridors[0].Directions[0].Reach.Cause
	if c == nil || c.Node != "keir" {
		t.Fatalf("cause %+v, want keir, not ayr beyond the crossing", c)
	}
}

// A reroute is news for a week; after that the new carrier is simply the
// carrier.
func TestAnOldRerouteIsNoLongerReported(t *testing.T) {
	w := New(Options{Days: 14, WindowHours: 22 * day, Corridors: reachZones}, reachGrid)
	var id int64
	for h := int64(0); h < 22*day; h++ {
		via := "keir"
		if h >= 10*day {
			via = "goyle"
		}
		reachFeed(w, &id, 1008+h, "n1", 10, 0.9, via, "")
		w.Evaluate(1008 + h)
	}
	if r := w.Status().Corridors[0].Directions[0].Reroute; r != nil {
		t.Fatalf("reroute %+v from 12 days ago still reported", r)
	}
}
