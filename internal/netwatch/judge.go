package netwatch

import "sort"

// Kinds of incident.
const (
	// KindQuiet: its own adverts and the packets it relays both stopped.
	KindQuiet = "quiet"
	// KindNotRelaying: still heard (its adverts arrive) but it stopped
	// relaying while the repeaters around it carry on.
	KindNotRelaying = "not-relaying"
)

// surpriseChance is how unlikely a quiet slot must be, by chance alone,
// before a repeater is flagged.
const surpriseChance = 1e-4

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
}

// Baseline days whose slot fell inside one of the repeater's own incidents
// (pk; "" for none) are left out: an outage does not teach what is normal.
func (w *Watch) matched(byObs map[string]ring, t int64, quiet map[string]bool, pk string) matchedSlot {
	s := int64(w.opt.Slot)
	var use []int64 // the end hour of each baseline day's slot that counts
	for d := 1; d <= w.opt.Days; d++ {
		e := t - 24*int64(d)
		if !w.duringIncident(pk, e-s+1, e) {
			use = append(use, e)
		}
	}
	m := matchedSlot{days: make([]float64, len(use))}
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
	return lowerTail(int(float64(m.now)/f), float64(m.usual)/f, days)
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
