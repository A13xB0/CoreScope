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

func TestQuietRepeatersAreNotJudged(t *testing.T) {
	f := newFeed(t)
	for h := int64(0); h < 12*day; h++ {
		rate, adv := 2, []string(nil) // 12 relays in 6 h: below the 20 needed to judge
		if h >= 10*day {
			rate = 0
		} else if h%2 == 0 {
			adv = []string{"tiny"}
		}
		f.hour(h, "obs1", map[string]int{"tiny": rate}, adv, 60)
		f.w.Evaluate(f.start + h)
	}
	if st := f.w.Status(); len(st.Open) != 0 {
		t.Fatalf("judged a repeater with too little traffic: %+v", st.Open)
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
