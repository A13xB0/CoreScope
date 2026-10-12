package netwatch

import "sort"

// Tiers splits judged repeaters into core (traffic through them every few
// minutes, flagged soon after they slow down) and local (heard now and then,
// flagged when they have not been heard for hours). Zero values take the
// defaults in brackets.
type Tiers struct {
	CoreEveryMin int // core: usually a packet through it at least this often, minutes [10]
	CoreHours    int // core: the last this many hours are judged [2]
	LocalSilence int // local: flagged when unheard at least this many hours [3]
	// LocalGapFactor: and for longer than this many times its longest
	// silence in the week before [1]. Local repeaters are often unheard for
	// hours on end (coverage, not outages), so their own longest gap is the
	// yardstick.
	LocalGapFactor float64
}

func (t Tiers) withDefaults() Tiers {
	if t.CoreEveryMin <= 0 {
		t.CoreEveryMin = 10
	}
	if t.CoreHours <= 0 {
		t.CoreHours = 2
	}
	if t.LocalSilence <= 0 {
		t.LocalSilence = 3
	}
	if t.LocalGapFactor <= 0 {
		t.LocalGapFactor = 1
	}
	return t
}

// Tiers and the incidents they raise.
const (
	TierCore  = "core"
	TierLocal = "local"
	// KindSlowed: a core repeater whose traffic fell right away.
	KindSlowed = "slowed"
	// KindUnheard: a local repeater not heard for longer than usual.
	KindUnheard = "unheard"
)

// TierCounts is how many judged repeaters are in each tier.
type TierCounts struct {
	Core  int `json:"core"`
	Local int `json:"local"`
}

// tierOf places a repeater from the week before t (hours inside its own
// incidents left out): core when the median hour carries a packet at least
// every CoreEveryMin minutes; local when it is heard at least once a day
// (gap is then its longest silence that week, in hours);
// "" when it is too seldom heard to judge.
func (w *Watch) tierOf(nd *node, pk string, t int64) (tier string, gap int) {
	tc := w.opt.Tiers.withDefaults()
	from := max(t-24*7+1, w.newest-int64(w.opt.WindowHours)+1)
	to := t - int64(tc.CoreHours)
	if to-from < 72 {
		return "", 0
	}
	var counts []int
	var heard []int64
	for h := from; h <= to; h++ {
		if w.duringIncident(pk, h, h) {
			continue
		}
		r := int(nd.relays.at(w, h))
		counts = append(counts, r)
		if r > 0 || nd.adverts.at(w, h) > 0 {
			heard = append(heard, h)
		}
	}
	if len(heard) < 7 {
		return "", 0
	}
	if median(counts)*tc.CoreEveryMin >= 60 {
		return TierCore, 1
	}
	gaps := make([]int, 0, len(heard))
	for i := 1; i < len(heard); i++ {
		gaps = append(gaps, int(heard[i]-heard[i-1]))
	}
	sort.Ints(gaps)
	if len(gaps) == 0 {
		return "", 0
	}
	gap = gaps[len(gaps)-1]
	if gap > 24 {
		return "", 0
	}
	return TierLocal, gap
}

// lastHeard is the last hour at or before t with a relay or an advert.
func (w *Watch) lastHeard(nd *node, t int64) int64 {
	for h := t; h > w.newest-int64(w.opt.WindowHours); h-- {
		if nd.relays.at(w, h) > 0 || nd.adverts.at(w, h) > 0 {
			return h
		}
	}
	return w.newest - int64(w.opt.WindowHours)
}

// judgeTier opens a core or local incident for pk at hour t, or returns nil.
func (w *Watch) judgeTier(pk string, nd *node, t int64, quiet map[string]bool) *Incident {
	tc := w.opt.Tiers.withDefaults()
	tier, gap := w.tierOf(nd, pk, t)
	switch tier {
	case TierCore:
		s := tc.CoreHours
		now, usual := w.slotN(nd.relays, t, s)
		if usual == 0 || float64(now) > 0.5*float64(usual) {
			return nil
		}
		// Over a window this short an observer going offline does not show as
		// quiet yet, so those not listening at their usual level are left out
		// too.
		m := w.matchedN(nd.byObs, t, w.deafOver(nd, quiet, t-int64(s)+1, t), pk, s)
		if m.observers == 0 || float64(m.now) > w.opt.QuietRatio*float64(m.usual) {
			return nil
		}
		// A slowdown alone is one sign, over a short window: it needs the
		// stronger evidence, as not relaying does.
		p := quietChance(usual, m)
		if p >= notRelayingChance {
			return nil
		}
		return &Incident{Node: pk, Kind: KindSlowed, Tier: TierCore, Start: w.quietSince(nd.relays, t, usual, s), Flagged: t,
			Usual: usual, Now: now, WindowH: s, Chance: p, Observers: m.observers, matchedUsual: m.usual}
	case TierLocal:
		// Unheard for longer than LocalGapFactor times its longest silence
		// that week, and at least LocalSilence hours.
		last := w.lastHeard(nd, t)
		silence := t - last
		if float64(silence) <= tc.LocalGapFactor*float64(gap) || silence < int64(tc.LocalSilence) {
			return nil
		}
		// An observer that usually hears it must have been listening all that
		// time: an observer going offline takes hours to show as quiet.
		n := w.listening(nd, last+1, t)
		if n == 0 {
			return nil
		}
		_, usual := w.slotN(nd.relays, t, int(silence))
		return &Incident{Node: pk, Kind: KindUnheard, Tier: TierLocal, Start: last + 1, Flagged: t,
			Usual: usual, WindowH: int(silence), HeardEveryH: gap, Observers: n}
	}
	return nil
}

// listening counts the observers that heard nd in the week before from and
// that were listening at their usual level over [from, to].
func (w *Watch) listening(nd *node, from, to int64) int {
	n := 0
	for obs, r := range nd.byObs {
		if r.sum(w, from-24*7, from-1) > 0 && w.listeningOver(nd, obs, from, to) {
			n++
		}
	}
	return n
}

// listeningOver says whether obs's traffic over [from, to], leaving out
// nd's own (whose drop is the question), is at least half the same hours'
// usual.
func (w *Watch) listeningOver(nd *node, obs string, from, to int64) bool {
	all := w.observers[obs]
	if all == nil {
		return false
	}
	hours := int(to - from + 1)
	now, usual := w.slotN(all, to, hours)
	if mine := nd.byObs[obs]; mine != nil {
		mNow, mUsual := w.slotN(mine, to, hours)
		now, usual = now-mNow, usual-mUsual
	}
	return usual <= 0 || 2*now >= usual
}

// deafOver adds to quiet the observers that heard nd but were not listening
// at their usual level over [from, to].
func (w *Watch) deafOver(nd *node, quiet map[string]bool, from, to int64) map[string]bool {
	out := make(map[string]bool, len(quiet))
	for k, v := range quiet {
		out[k] = v
	}
	for obs := range nd.byObs {
		if !w.listeningOver(nd, obs, from, to) {
			out[obs] = true
		}
	}
	return out
}

func (w *Watch) tierCounts(t int64) TierCounts {
	var c TierCounts
	for pk, nd := range w.nodes {
		if !w.judged(pk) {
			continue
		}
		switch tier, _ := w.tierOf(nd, pk, t); tier {
		case TierCore:
			c.Core++
		case TierLocal:
			c.Local++
		}
	}
	return c
}
