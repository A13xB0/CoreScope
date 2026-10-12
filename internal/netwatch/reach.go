package netwatch

import "sort"

const (
	minReachAdverts = 10
	// acceptDays is how long a step to a new level is held against the level
	// before it, before the new level becomes the usual.
	acceptDays = 14
)

// Cause is the repeater whose share of the crossings fell away: Explains is
// how much of the drop that accounts for, 0..1.
type Cause struct {
	Node     string  `json:"node"`
	Explains float64 `json:"explains"`
	Usual    float64 `json:"usual"` // share of the adverts that crossed through it, usually
	Last     float64 `json:"last"`  // and on the last day
}

// Reroute is a corridor whose main carrier changed: From fell away and To
// carries most of it, since the first hour of the day Since.
type Reroute struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Since int64  `json:"since"`
}

// reachStatus is nil when there is not enough to say: too few adverts on
// the last day, or fewer than three usable days before it.
func (w *Watch) reachStatus(r reach, basis Basis, target float64, t int64) *Reach {
	den, _, _ := w.daily(r.den, t)
	num, _, _ := w.daily(r.num, t)
	n := len(den)
	if n == 0 || den[n-1] < minReachAdverts {
		return nil
	}
	firstDay := (w.newest-int64(w.opt.WindowHours))/24 + 1
	rates := make([]float64, n)
	for i := range den {
		rates[i] = -1
		if den[i] >= minReachAdverts {
			rates[i] = float64(num[i]) / float64(den[i])
		}
	}
	// The days the usual is taken from: the last Days usable days before the
	// last one, or, after a recent step, the Days before the step.
	upto := n - 1
	var since *int64
	if at, _, _, ok := stepChange(rates, den); ok && n-1-at < acceptDays {
		upto = at
		h := (firstDay + int64(at)) * 24
		since = &h
	}
	var idx []int
	for i := upto - 1; i >= 0 && len(idx) < w.opt.Days; i-- {
		if rates[i] >= 0 {
			idx = append(idx, i)
		}
	}
	if len(idx) < 3 {
		return nil
	}
	prev := make([]float64, len(idx))
	for j, i := range idx {
		prev[j] = rates[i]
	}
	usual := medianF(prev)
	last := rates[n-1]
	out := &Reach{Last: last, Usual: usual, Basis: basis.orDefault(), LastAdverts: den[n-1], LastReached: num[n-1],
		Daily: rates, Since: since, Target: target}
	out.Low, out.High = shareRange(usual, prev, den[n-1])
	switch {
	case usual < 0.02:
		out.Verdict = "new"
	case last < out.Low:
		out.Verdict = "worse"
		out.Cause = cause(r.via, den, idx, n, firstDay, usual, last)
	case last > out.High:
		out.Verdict = "better"
	default:
		out.Verdict = "normal"
	}
	if target > 0 {
		out.Burn = w.burn(r, target, t)
	}
	return out
}

// cause finds the repeater whose crossings fell furthest below what its
// usual share of the adverts would give on the last day, and keeps it when
// that accounts for at least half of the drop.
func cause(via map[int64]map[string]int, den []int, usualDays []int, n int, firstDay int64, usual, last float64) *Cause {
	total := (usual - last) * float64(den[n-1])
	if total <= 0 {
		return nil
	}
	nodes := map[string]bool{}
	for _, i := range usualDays {
		for pk := range via[firstDay+int64(i)] {
			nodes[pk] = true
		}
	}
	lastVia := via[firstDay+int64(n-1)]
	var best *Cause
	bestDrop := 0.0
	keys := make([]string, 0, len(nodes))
	for pk := range nodes {
		keys = append(keys, pk)
	}
	sort.Strings(keys)
	for _, pk := range keys {
		shares := make([]float64, 0, len(usualDays))
		for _, i := range usualDays {
			shares = append(shares, float64(via[firstDay+int64(i)][pk])/float64(den[i]))
		}
		share := medianF(shares)
		lastShare := float64(lastVia[pk]) / float64(den[n-1])
		if drop := (share - lastShare) * float64(den[n-1]); drop > bestDrop {
			bestDrop = drop
			best = &Cause{Node: pk, Usual: share, Last: lastShare}
		}
	}
	if best == nil {
		return nil
	}
	best.Explains = bestDrop / total
	if best.Explains > 1 {
		best.Explains = 1
	}
	if best.Explains < 0.5 {
		return nil
	}
	return best
}

// burn compares the share over the last 6, 24 and 72 hours with the target.
func (w *Watch) burn(r reach, target float64, t int64) string {
	under := func(hours int64) (bool, bool) {
		d := r.den.sum(w, t-hours+1, t)
		if d < minReachAdverts {
			return false, false
		}
		return float64(r.num.sum(w, t-hours+1, t))/float64(d) < target, true
	}
	u6, ok6 := under(6)
	u24, ok24 := under(24)
	u72, ok72 := under(72)
	switch {
	case ok6 && ok24 && u6 && u24:
		return "fast"
	case ok24 && ok72 && u24 && u72:
		return "slow"
	case ok24:
		return "ok"
	}
	return ""
}

// reroute looks for a change of main carrier: the run of days up to the
// last complete one with a new busiest carrier, after days where another one
// was busiest, which has since carried less than half its usual.
func (w *Watch) reroute(carriers map[int64]map[string]int, t int64) *Reroute {
	lastDay := (t+1)/24 - 1
	firstDay := (w.newest-int64(w.opt.WindowHours))/24 + 1
	top := func(d int64) string {
		best, bn := "", 0
		for pk, c := range carriers[d] {
			if c > bn || (c == bn && pk < best) {
				best, bn = pk, c
			}
		}
		return best
	}
	now := top(lastDay)
	if now == "" {
		return nil
	}
	s := lastDay
	for s-1 >= firstDay && top(s-1) == now {
		s--
	}
	if s == firstDay {
		return nil
	}
	count := map[string]int{}
	var oldCounts []int
	for d := s - 1; d >= firstDay && d >= s-int64(w.opt.Days); d-- {
		count[top(d)]++
	}
	old, on := "", 0
	for pk, c := range count {
		if pk != "" && (c > on || (c == on && pk < old)) {
			old, on = pk, c
		}
	}
	if old == "" || old == now {
		return nil
	}
	for d := s - 1; d >= firstDay && d >= s-int64(w.opt.Days); d-- {
		oldCounts = append(oldCounts, carriers[d][old])
	}
	if carriers[lastDay][old]*2 >= median(oldCounts) {
		return nil
	}
	return &Reroute{From: old, To: now, Since: s * 24}
}
