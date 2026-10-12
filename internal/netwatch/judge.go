package netwatch

import (
	"math"
	"sort"
)

// Kinds of incident.
const (
	// KindQuiet: its own adverts and the packets it relays both stopped.
	KindQuiet = "quiet"
	// KindNotRelaying: still heard (its adverts arrive) but it stopped
	// relaying while the repeaters around it carry on.
	KindNotRelaying = "not-relaying"
)

// surpriseChance is how unlikely a quiet slot must be, by chance alone,
// before a repeater is flagged. A repeater still adverting has only one sign
// against it, so not relaying needs much stronger evidence, and enough
// traffic that its absence matters.
const (
	surpriseChance      = 1e-4
	notRelayingChance   = 1e-6
	notRelayingMinUsual = 30
)

// judgeable says whether a slot that usually holds this many packets could
// ever be surprising enough when nothing at all arrives.
func judgeable(usual int) bool { return usual > 0 && poissonCDF(0, float64(usual)) < surpriseChance }

// matchedSlot compares a slot with its usual only through observers that are
// up now and heard it in the baseline, like for like: an observer going
// offline leaves both sides of the comparison.
type matchedSlot struct {
	now, usual int
	days       []float64 // the same hours on each baseline day
	observers  int
	// onAir marks the baseline days its adverts were heard in the slot, so a
	// quiet day then was quiet traffic, not an outage.
	onAir []bool
}

// Baseline days whose slot fell inside one of the repeater's own incidents
// (pk; "" for none), or looks like one that was never flagged (before it
// existed, or before the watch started), are left out: an outage does not
// teach what is normal.
func (w *Watch) matched(byObs map[string]ring, t int64, quiet map[string]bool, pk string) matchedSlot {
	s := int64(w.opt.Slot)
	nd := w.nodes[pk]
	var use []int64 // the end hour of each baseline day's slot that counts
	for d := 1; d <= w.baseDays(t); d++ {
		e := t - 24*int64(d)
		if !w.duringIncident(pk, e-s+1, e) && !w.lookedOut(nd, e, t) {
			use = append(use, e)
		}
	}
	m := matchedSlot{days: make([]float64, len(use)), onAir: make([]bool, len(use))}
	if nd != nil {
		for i, e := range use {
			m.onAir[i] = nd.adverts.sum(w, e-s+1, e) > 0
		}
	}
	base := make([]int, len(use))
	tmp := make([]int, len(use))
	for obs, r := range byObs {
		if quiet[obs] {
			continue
		}
		tot := 0
		for i, e := range use {
			tmp[i] = r.sum(w, e-s+1, e)
			tot += tmp[i]
		}
		if tot == 0 {
			continue
		}
		m.observers++
		m.now += r.sum(w, t-s+1, t)
		for i := range base {
			base[i] += tmp[i]
		}
	}
	for i, v := range base {
		m.days[i] = float64(v)
	}
	m.usual = median(base)
	return m
}

// quietChance is how likely the matched slot would be by chance. Matched
// counts are observations, and one packet heard by three observers counts
// three times, so they are scaled back to packets (rawUsual) first: the
// evidence is not overstated.
func quietChance(rawUsual int, m matchedSlot) float64 {
	if m.usual <= 0 {
		return 1
	}
	f := 1.0
	if rawUsual > 0 && m.usual > rawUsual {
		f = float64(m.usual) / float64(rawUsual)
	}
	days := make([]float64, len(m.days))
	for i, v := range m.days {
		days[i] = v / f
	}
	p := lowerTail(int(float64(m.now)/f), float64(m.usual)/f, days)
	// A repeater that is often this quiet while plainly on air (its adverts
	// heard) is bursty: at least a fifth of days, that is how often it
	// happens. Fewer is left to the model, so the odd day an advert came
	// back before its relays does not excuse the next outage.
	same := 0
	for i, v := range m.days {
		if m.onAir[i] && v <= float64(m.now) {
			same++
		}
	}
	if n := len(m.days); n > 0 && same*5 >= n {
		p = math.Max(p, float64(same)/float64(n))
	}
	return p
}

// FeedStatus says whether the packet feed itself has stopped: every
// observer quiet at once, or nothing arriving. Nothing is judged meanwhile.
type FeedStatus struct {
	Down  bool  `json:"down"`
	Since int64 `json:"since,omitempty"`
}

func (w *Watch) checkFeed(t int64, quiet map[string]bool) {
	prev := make([]int, 0, w.opt.Days)
	for d := 1; d <= w.opt.Days; d++ {
		prev = append(prev, int(w.total.at(w, t-24*int64(d))))
	}
	usual, now := median(prev), int(w.total.at(w, t))
	busy := 0
	for _, r := range w.observers {
		if _, u := w.slot(r, t); u >= w.opt.MinUsual {
			busy++
		}
	}
	down := (usual >= 20 && now*10 < usual) || (busy >= 3 && len(quiet)*10 >= busy*6)
	switch {
	case down && !w.feed.Down:
		w.feed = FeedStatus{Down: true, Since: t}
	case !down:
		w.feed = FeedStatus{}
	}
}

// neighboursCarryOn says whether most of the repeaters pk usually shares a
// link with are still relaying (through matched observers): pk's silence is
// then its own.
func (w *Watch) neighboursCarryOn(pk string, t int64, quiet map[string]bool) bool {
	s := int64(w.opt.Slot)
	from, to := t-24*int64(w.opt.Days)-s+1, t-s
	nbs := map[string]bool{}
	for k, r := range w.links {
		if k[0] != pk && k[1] != pk {
			continue
		}
		if r.sum(w, from, to) >= w.opt.Days {
			nb := k[0]
			if nb == pk {
				nb = k[1]
			}
			nbs[nb] = true
		}
	}
	n, ok := 0, 0
	for nb := range nbs {
		nd := w.nodes[nb]
		if nd == nil {
			continue
		}
		m := w.matched(nd.byObs, t, quiet, nb)
		if m.usual < 10 {
			continue
		}
		n++
		if float64(m.now) >= 0.6*float64(m.usual) {
			ok++
		}
	}
	return n >= 2 && ok*2 >= n
}

// Nightly is a repeater that goes dark at night (solar, small battery) and
// comes back by day. Hours are UTC.
type Nightly struct {
	Node     string  `json:"node"`
	DarkFrom int     `json:"darkFrom"` // hour of day it went dark last night
	DarkTo   int     `json:"darkTo"`   // and came back
	Nights   int     `json:"nights"`   // of the last seven
	Earlier  bool    `json:"earlier"`  // going dark earlier than the nights before
	ShiftH   float64 `json:"shiftH"`   // by this many hours, on average
	minRel   int
	maxRel   int
	maxLen   int
}

// nightly looks at the last seven noon-to-noon windows ending before t for
// a dark run of 4 to 18 hours with traffic either side.
func (w *Watch) nightly(nd *node, t int64) (Nightly, bool) {
	end := t - ((t%24)-11+24)%24 // the last 11:00 at or before t
	type night struct{ rel, length int }
	var got []night // newest first
	var newest *night
	for k := int64(0); k < 7; k++ {
		e := end - 24*k
		b := e - 23
		if b <= w.newest-int64(w.opt.WindowHours) {
			return Nightly{}, false
		}
		bestS, bestL, curS, curL := int64(0), 0, int64(0), 0
		for h := b; h <= e; h++ {
			if nd.relays.at(w, h) == 0 && nd.adverts.at(w, h) == 0 {
				if curL == 0 {
					curS = h
				}
				curL++
				if curL > bestL {
					bestL, bestS = curL, curS
				}
				continue
			}
			curL = 0
		}
		// Night: it covers 01:00-03:00 UTC, and it relays in most hours
		// outside it (a sparse repeater has quiet hours any time of day).
		active, small := 0, 0
		for h := b; h <= e; h++ {
			if h >= bestS && h < bestS+int64(bestL) {
				if hh := h % 24; hh >= 1 && hh <= 3 {
					small++
				}
			} else if nd.relays.at(w, h) > 0 {
				active++
			}
		}
		if bestL >= 4 && bestL <= 18 && small == 3 && active*10 >= (24-bestL)*7 {
			got = append(got, night{rel: int(bestS - b), length: bestL})
			if k == 0 {
				newest = &got[len(got)-1]
			}
		}
	}
	if len(got) < 5 || newest == nil {
		return Nightly{}, false
	}
	lo, hi := 24, 0
	for _, g := range got {
		lo, hi = min(lo, g.rel), max(hi, g.rel)
	}
	if hi-lo > 5 { // it goes dark at no particular time
		return Nightly{}, false
	}
	n := Nightly{Nights: len(got), DarkFrom: (12 + newest.rel) % 24, DarkTo: (12 + newest.rel + newest.length) % 24, minRel: 24}
	var recent, older []float64
	for i, g := range got {
		if i < 3 {
			recent = append(recent, float64(g.rel))
		} else {
			older = append(older, float64(g.rel))
		}
		if g.rel < n.minRel {
			n.minRel = g.rel
		}
		if g.rel > n.maxRel {
			n.maxRel = g.rel
		}
		if g.length > n.maxLen {
			n.maxLen = g.length
		}
	}
	if len(recent) >= 2 && len(older) >= 2 {
		n.ShiftH = mean(older) - mean(recent)
		n.Earlier = n.ShiftH >= 1
	}
	return n, true
}

// inItsNight says whether a silence that began at hour start is the
// repeater's usual night: it began around when it usually goes dark, and has
// not yet outlasted its longest night by more than six hours.
func (n Nightly) inItsNight(start, t int64) bool {
	rel := int(((start-12)%24 + 24) % 24)
	return rel >= n.minRel-3 && rel <= n.maxRel+2 && t-start < int64(n.maxLen+6)
}

func mean(a []float64) float64 {
	s := 0.0
	for _, v := range a {
		s += v
	}
	return s / float64(len(a))
}

// advertsDuring says whether the repeater's adverts kept arriving through
// the quiet slot ending at t: at least half of those due every gap hours, and
// at least two. Positive evidence it is still on air, not merely a silence
// too short yet to count.
func (w *Watch) advertsDuring(nd *node, t int64, gap int) bool {
	s := int64(w.opt.Slot)
	due := 2
	if gap > 0 && int(s)/gap/2 > due {
		due = int(s) / gap / 2
	}
	return nd.adverts.sum(w, t-s+1, t) >= due
}

// quietSince is the first hour of the slot ending at t after the last hour
// that still held half its usual share of the slot's usual count.
func (w *Watch) quietSince(r ring, t int64, usual int) int64 {
	s := int64(w.opt.Slot)
	half := float64(usual) / float64(s) / 2
	for h := t; h > t-s; h-- {
		if float64(r.at(w, h)) >= half {
			return h + 1
		}
	}
	return t - s + 1
}

// lookedOut says whether the slot ending at e looks like an outage of nd
// that was never flagged: no adverts in it, though it adverts often enough
// to expect two, and next to no relays (at most a fifth of the hours'
// median over the window up to t); or nothing at all from it that day.
func (w *Watch) lookedOut(nd *node, e, t int64) bool {
	if nd == nil {
		return false
	}
	s := int64(w.opt.Slot)
	if nd.relays.sum(w, e-23, e) == 0 && nd.adverts.sum(w, e-23, e) == 0 {
		return true
	}
	if nd.adverts.sum(w, e-s+1, e) > 0 {
		return false
	}
	_, gap, _, known := w.advertSilence(nd, t)
	if !known || gap*2 > int(s) {
		return false
	}
	return nd.relays.sum(w, e-s+1, e)*5 <= w.typicalSlot(nd.relays, t)
}

// typicalSlot is the median Slot-hour count over the window up to t, a
// rough level for what next to nothing means.
func (w *Watch) typicalSlot(r ring, t int64) int {
	s := int64(w.opt.Slot)
	var v []int
	for e := t; e-s+1 > w.newest-int64(w.opt.WindowHours); e -= s {
		v = append(v, r.sum(w, e-s+1, e))
	}
	return median(v)
}

// duringIncident says whether pk had an incident open at any hour in [from, to].
func (w *Watch) duringIncident(pk string, from, to int64) bool {
	if pk == "" {
		return false
	}
	for _, c := range w.closed {
		if c.Node == pk && c.Start <= to && *c.End >= from {
			return true
		}
	}
	return false
}

func (w *Watch) repeats(pk string) int {
	n := 1
	for _, c := range w.closed {
		if c.Node == pk {
			n++
		}
	}
	return n
}

func (w *Watch) nightlyList(t int64) []Nightly {
	out := []Nightly{}
	for pk, nd := range w.nodes {
		if !w.judged(pk) {
			continue
		}
		if n, ok := w.nightly(nd, t); ok {
			n.Node = pk
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Earlier != out[j].Earlier {
			return out[i].Earlier
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// Explanation is how a repeater was judged at an hour: the evidence behind
// a verdict, or behind the lack of one.
type Explanation struct {
	Hour          int64   `json:"hour"`
	Now           int     `json:"now"`   // relays in the slot ending at Hour
	Usual         int     `json:"usual"` // usually, all observers
	MatchedNow    int     `json:"matchedNow"`
	MatchedUsual  int     `json:"matchedUsual"`
	Observers     int     `json:"observers"` // up both now and in the usual
	Chance        float64 `json:"chance"`    // of a slot this quiet by chance
	AdvertGapH    int     `json:"advertGapH"`
	LastAdvert    int64   `json:"lastAdvert"`
	AdvertSilent  bool    `json:"advertSilent"`
	AdvertsInSlot int     `json:"advertsInSlot"`
	Nightly       bool    `json:"nightly"`
	FeedDown      bool    `json:"feedDown"`
}

// Explain reports how pk is judged at hour t.
func (w *Watch) Explain(pk string, t int64) Explanation {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := Explanation{Hour: t, FeedDown: w.feed.Down}
	nd := w.nodes[pk]
	if nd == nil {
		return e
	}
	quiet := w.quietObservers(t)
	e.Now, e.Usual = w.slot(nd.relays, t)
	m := w.matched(nd.byObs, t, quiet, pk)
	e.MatchedNow, e.MatchedUsual, e.Observers = m.now, m.usual, m.observers
	e.Chance = quietChance(e.Usual, m)
	silent, gap, last, _ := w.advertSilence(nd, t)
	e.AdvertSilent, e.AdvertGapH, e.LastAdvert = silent, gap, last
	e.AdvertsInSlot = nd.adverts.sum(w, t-int64(w.opt.Slot)+1, t)
	_, e.Nightly = w.nightly(nd, t)
	return e
}
