package pathwatch

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// activity is a fake of the store: relays per repeater per day back from
// now, and when each was first heard.
type activity struct {
	quietFrom map[string]time.Time // stops relaying from then
	first     map[string]time.Time
}

func (a activity) Relays(pk string, from, to time.Time) int {
	if q, ok := a.quietFrom[pk]; ok && !from.Before(q) {
		return 0
	}
	return int(to.Sub(from).Hours()) * 10
}

func (a activity) FirstHeard(pk string) (time.Time, bool) {
	t, ok := a.first[pk]
	return t, ok
}

// day is the start of the 24 h bucket i days back (0 = the last 24 h).
func day(i int) time.Time { return now.Add(-time.Duration(i+1) * 24 * time.Hour) }

// feed builds 30 days of traffic: perDay packets pass A each day, a share of
// them reach B by the route route(i) on day i (0 = the last 24 h).
func feed(perDay int, share func(i int) float64, route func(i int) []string) Input {
	in := Input{Now: now}
	for i := 29; i >= 0; i-- {
		for k := 0; k < perDay; k++ {
			at := day(i).Add(time.Duration(k) * 24 * time.Hour / time.Duration(perDay))
			in.Passed = append(in.Passed, at)
			if float64(k) < share(i)*float64(perDay) {
				in.Reached = append(in.Reached, Crossing{At: at, Route: route(i)})
			}
		}
	}
	return in
}

func TestDailyShareAndItsUsualRange(t *testing.T) {
	in := feed(100, func(i int) float64 {
		if i == 0 {
			return 0.2
		}
		return 0.8
	}, func(int) []string { return []string{"keir"} })
	r := Analyse(in, activity{}, Options{})
	if len(r.Days) != 30 || r.Days[29].Passed != 100 || r.Days[29].Reached != 20 {
		t.Fatalf("days %d, last %+v", len(r.Days), r.Days[len(r.Days)-1])
	}
	if r.Share != 0.2 || r.Usual != 0.8 || r.Verdict != "worse" || r.Low >= 0.8 || r.Low < 0.6 {
		t.Errorf("share %v usual %v range %v-%v verdict %q", r.Share, r.Usual, r.Low, r.High, r.Verdict)
	}
	if r.Hops.Median != 2 || r.Hops.Min != 2 || r.Hops.Max != 2 {
		t.Errorf("hops %+v, want 2 (A, keir, B)", r.Hops)
	}
}

func TestRoutesAreRankedWithTheirUsualShare(t *testing.T) {
	in := feed(100, func(int) float64 { return 0.5 }, func(i int) []string {
		if i == 0 {
			return []string{"goyle"}
		}
		return []string{"keir"}
	})
	r := Analyse(in, activity{}, Options{WindowDays: 7})
	if len(r.Days) != 7 {
		t.Fatalf("days %d, want 7 shown", len(r.Days))
	}
	if len(r.Routes) != 2 || r.Routes[0].Via[0] != "keir" || r.Routes[1].Via[0] != "goyle" {
		t.Fatalf("routes %+v", r.Routes)
	}
	if r.Routes[1].Usual != 0 || r.Routes[0].Usual != 1 {
		t.Errorf("usual shares %+v", r.Routes)
	}
}

// When the repeater a path ran through drops out and has stopped relaying
// everywhere, the watch says it went quiet; when it is still busy elsewhere,
// that the path was rerouted.
func TestARepeaterThatDroppedOutIsQuietOrRerouted(t *testing.T) {
	route := func(i int) []string {
		if i <= 1 {
			return []string{"other"}
		}
		return []string{"keir"}
	}
	in := feed(100, func(int) float64 { return 0.6 }, route)
	quiet := Analyse(in, activity{quietFrom: map[string]time.Time{"keir": day(1)}}, Options{})
	if quiet.Flag == nil || quiet.Flag.Kind != KindRepeaterQuiet || quiet.Flag.Node != "keir" || quiet.Flag.Was < 0.9 || quiet.Flag.Now > 0.05 {
		t.Fatalf("flag %+v, want keir quiet", quiet.Flag)
	}
	if !quiet.Flag.Day.Equal(day(1)) {
		t.Errorf("flagged on %v, want the day it happened %v", quiet.Flag.Day, day(1))
	}
	busy := Analyse(in, activity{}, Options{})
	if busy.Flag == nil || busy.Flag.Kind != KindRerouted || busy.Flag.Node != "keir" {
		t.Fatalf("flag %+v, want rerouted", busy.Flag)
	}
}

// A repeater that starts carrying a good share is new if it was first heard
// lately, else it is sharing the load.
func TestARepeaterThatStartsCarryingIsNewOrSharing(t *testing.T) {
	route := func(i int) []string {
		if i <= 2 && i%2 == 0 {
			return []string{"ward"}
		}
		return []string{"keir"}
	}
	in := feed(100, func(int) float64 { return 0.6 }, route)
	fresh := Analyse(in, activity{first: map[string]time.Time{"ward": now.Add(-5 * 24 * time.Hour)}}, Options{})
	var found *Change
	for i := range fresh.Changes {
		if fresh.Changes[i].Node == "ward" {
			found = &fresh.Changes[i]
		}
	}
	if found == nil || found.Kind != KindNewRepeater {
		t.Fatalf("changes %+v, want ward new", fresh.Changes)
	}
	old := Analyse(in, activity{first: map[string]time.Time{"ward": now.Add(-90 * 24 * time.Hour)}}, Options{})
	found = nil
	for i := range old.Changes {
		if old.Changes[i].Node == "ward" {
			found = &old.Changes[i]
		}
	}
	if found == nil || found.Kind != KindSharing {
		t.Fatalf("changes %+v, want ward sharing the load", old.Changes)
	}
}

// A steady path raises nothing, and a change older than a week is history,
// not a flag.
func TestSteadyPathsAndOldChangesRaiseNoFlag(t *testing.T) {
	steady := Analyse(feed(100, func(int) float64 { return 0.6 }, func(int) []string { return []string{"keir"} }), activity{}, Options{})
	if steady.Flag != nil || len(steady.Changes) != 0 {
		t.Fatalf("steady path: flag %+v changes %+v", steady.Flag, steady.Changes)
	}
	old := Analyse(feed(100, func(int) float64 { return 0.6 }, func(i int) []string {
		if i <= 12 {
			return []string{"goyle"}
		}
		return []string{"keir"}
	}), activity{}, Options{})
	if old.Flag != nil || len(old.Changes) == 0 {
		t.Fatalf("old change: flag %+v changes %+v, want history only", old.Flag, old.Changes)
	}
}

// Too few packets to say anything: no share, no usual, no flags.
func TestTooLittleTrafficSaysSo(t *testing.T) {
	r := Analyse(feed(3, func(int) float64 { return 1 }, func(int) []string { return nil }), activity{}, Options{})
	if r.Days[len(r.Days)-1].Share != -1 || r.Verdict != "" || r.Flag != nil {
		t.Fatalf("result %+v", r)
	}
}

// On a thin path a handful of packets proves nothing: a repeater that
// carried a quarter of 16 crossings a day is 4 packets, and a day without
// it is chance.
func TestThinPathsRaiseNoChanges(t *testing.T) {
	// Four repeaters share 16 crossings a day; each day one of them carries
	// none of them, which is chance at these numbers.
	nodes := []string{"w", "x", "y", "z"}
	in := feed(100, func(int) float64 { return 0.16 }, func(int) []string { return nil })
	for k := range in.Reached {
		i := int(now.Sub(in.Reached[k].At) / (24 * time.Hour))
		in.Reached[k].Route = []string{nodes[(i+1+k%3)%4]}
	}
	r := Analyse(in, activity{}, Options{})
	if len(r.Changes) != 0 {
		t.Fatalf("changes on a thin path: %+v", r.Changes)
	}
}

// When the main route stays the same but its share moves, the change says
// so rather than naming the same route twice.
func TestTheMainRouteKeepingItsPlaceButLosingShareIsAShareChange(t *testing.T) {
	in := feed(200, func(int) float64 { return 0.5 }, func(i int) []string { return []string{"keir"} })
	for k := range in.Reached { // on the last day, two in five go straight across instead
		if in.Reached[k].At.After(day(0)) && k%5 < 2 {
			in.Reached[k].Route = []string{}
		}
	}
	r := Analyse(in, activity{}, Options{MainMove: 0.25, DropFrom: 0.9, DropTo: 0.01})
	if r.Flag == nil || r.Flag.Kind != KindRouteShare || r.Flag.Was != 1 || r.Flag.Now > 0.7 {
		t.Fatalf("flag %+v, want the main route's share falling", r.Flag)
	}
}

// A repeater that drops out of the path halfway through its last busy day
// first looks rerouted (it relayed elsewhere earlier that day); once it has
// plainly stopped everywhere, the same change becomes "quiet" rather than a
// second entry.
func TestARerouteThatTurnsOutQuietIsOneChange(t *testing.T) {
	route := func(i int) []string {
		if i <= 3 {
			return []string{"other"}
		}
		return []string{"keir"}
	}
	in := feed(100, func(int) float64 { return 0.6 }, route)
	r := Analyse(in, activity{quietFrom: map[string]time.Time{"keir": day(2)}}, Options{})
	var keir []Change
	for _, c := range r.Changes {
		if c.Node == "keir" {
			keir = append(keir, c)
		}
	}
	if len(keir) != 1 || keir[0].Kind != KindRepeaterQuiet || !keir[0].Day.Equal(day(3)) {
		t.Fatalf("keir changes %+v, want one, quiet, dated the day it dropped out", keir)
	}
}

// A flag can be kept for longer than a week, and changes are looked for over
// that long even when fewer days are shown.
func TestAFlagCanBeKeptForTwoWeeks(t *testing.T) {
	in := feed(100, func(int) float64 { return 0.6 }, func(i int) []string {
		if i <= 10 {
			return []string{"goyle"}
		}
		return []string{"keir"}
	})
	week := Analyse(in, activity{}, Options{WindowDays: 7})
	if week.Flag != nil {
		t.Fatalf("a change 10 days back flagged with the default week: %+v", week.Flag)
	}
	fortnight := Analyse(in, activity{}, Options{WindowDays: 7, FlagDays: 14})
	if fortnight.Flag == nil || !fortnight.Flag.Day.Equal(day(10)) || len(fortnight.Days) != 7 {
		t.Fatalf("flag %+v days %d, want the change of 10 days ago flagged and 7 days shown", fortnight.Flag, len(fortnight.Days))
	}
}
