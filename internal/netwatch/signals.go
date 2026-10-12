package netwatch

import (
	"fmt"
	"math"
	"sort"
)

// snrDays holds a repeater's SNR at one observer as a sum and count per day,
// in a small ring of days.
type snrDays struct {
	day []int64
	sum []float32
	n   []uint16
}

func (w *Watch) snrRing() *snrDays {
	d := w.opt.WindowHours/24 + 2
	return &snrDays{day: make([]int64, d), sum: make([]float32, d), n: make([]uint16, d)}
}

func (s *snrDays) add(day int64, snr float64) {
	i := int(day % int64(len(s.day)))
	if s.day[i] != day {
		s.day[i], s.sum[i], s.n[i] = day, 0, 0
	}
	if s.n[i] < 0xFFFF {
		s.sum[i] += float32(snr)
		s.n[i]++
	}
}

// mean is the day's mean SNR, and whether it had at least five samples.
func (s *snrDays) mean(day int64) (float64, bool) {
	i := int(day % int64(len(s.day)))
	if s.day[i] != day || s.n[i] < 5 {
		return 0, false
	}
	return float64(s.sum[i]) / float64(s.n[i]), true
}

// AddSignal counts one SNR reading of node's transmission at observer obs,
// in hour h: the last hop of a relayed packet, or the sender of one heard
// directly.
func (w *Watch) AddSignal(h int64, obs, node string, snr float64) {
	if obs == "" || node == "" || math.IsNaN(snr) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.advance(h)
	if h <= w.newest-int64(w.opt.WindowHours) {
		return
	}
	k := [2]string{node, obs}
	s := w.signal[k]
	if s == nil {
		s = w.snrRing()
		w.signal[k] = s
	}
	s.add(h/24, snr)
}

// Drift is a repeater whose signal at an observer has fallen over the last
// three days, in dB: Before is its usual (days 4 to 13 back), Now the last
// three days. Since is the first hour of the first of those days.
type Drift struct {
	Node     string  `json:"node"`
	Observer string  `json:"observer"`
	Before   float64 `json:"before"`
	Now      float64 `json:"now"`
	Since    int64   `json:"since"`
}

// driftDB is how far the last three days must sit below the usual.
const driftDB = 6

func (w *Watch) drifts(t int64) []Drift {
	lastDay := (t+1)/24 - 1
	worst := map[string]Drift{}
	for k, s := range w.signal {
		if !w.judged(k[0]) {
			continue
		}
		var before []float64
		for d := lastDay - 13; d <= lastDay-4; d++ {
			if m, ok := s.mean(d); ok {
				before = append(before, m)
			}
		}
		if len(before) < 5 {
			continue
		}
		b := medianF(before)
		var recent []float64
		ok3 := true
		for d := lastDay - 2; d <= lastDay; d++ {
			m, ok := s.mean(d)
			ok3 = ok3 && ok && m <= b-driftDB*2/3
			recent = append(recent, m)
		}
		if !ok3 {
			continue
		}
		now := mean(recent)
		if now > b-driftDB {
			continue
		}
		dr := Drift{Node: k[0], Observer: k[1], Before: b, Now: now, Since: (lastDay - 2) * 24}
		if cur, seen := worst[k[0]]; !seen || dr.Before-dr.Now > cur.Before-cur.Now {
			worst[k[0]] = dr
		}
	}
	out := make([]Drift, 0, len(worst))
	for _, d := range worst {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before-out[i].Now > out[j].Before-out[j].Now })
	return out
}

// AddAdvertSent records the time an advert from node says it was sent
// (unix seconds), so the watch learns how often it adverts and what share of
// its adverts reach any observer. Times far from the window are ignored
// (a repeater with no clock).
func (w *Watch) AddAdvertSent(node string, sent int64) {
	if node == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	lo, hi := (w.newest-int64(w.opt.WindowHours))*3600, (w.newest+24)*3600
	if sent < lo || sent > hi {
		return
	}
	nd := w.nodeOf(node)
	i := sort.Search(len(nd.sent), func(i int) bool { return nd.sent[i] >= sent })
	if i < len(nd.sent) && nd.sent[i] == sent {
		return
	}
	nd.sent = append(nd.sent, 0)
	copy(nd.sent[i+1:], nd.sent[i:])
	nd.sent[i] = sent
	// Forget what has left the window, and keep it bounded.
	cut := sort.Search(len(nd.sent), func(i int) bool { return nd.sent[i] >= lo })
	if keep := len(nd.sent) - 4096; keep > cut {
		cut = keep
	}
	if cut > 0 {
		nd.sent = append(nd.sent[:0], nd.sent[cut:]...)
	}
}

// advertEvery is how often a repeater adverts, in seconds: the low end (20th
// percentile) of the gaps between the adverts heard, since a missed advert
// doubles a gap. Zero until there are five gaps.
func advertEvery(sent []int64) int64 {
	var gaps []float64
	for i := 1; i < len(sent); i++ {
		if g := sent[i] - sent[i-1]; g >= 60 && g <= 2*86400 {
			gaps = append(gaps, float64(g))
		}
	}
	if len(gaps) < 5 {
		return 0
	}
	return int64(math.Round(quantile(gaps, 0.2)/60) * 60)
}

// advertDelivery is the share of the adverts due each complete day of the
// window that reached any observer (-1 where the interval is unknown).
func (w *Watch) advertDelivery(nd *node, end int64) (every int64, daily []float64) {
	every = advertEvery(nd.sent)
	firstDay := (w.newest-int64(w.opt.WindowHours))/24 + 1
	lastDay := (end+1)/24 - 1
	counts := map[int64]int{}
	for _, s := range nd.sent {
		counts[s/86400]++
	}
	for d := firstDay; d <= lastDay; d++ {
		if every == 0 {
			daily = append(daily, -1)
			continue
		}
		daily = append(daily, math.Min(1, float64(counts[d])/(86400/float64(every))))
	}
	return every, daily
}

// Event is one thing that happened, for the timeline.
type Event struct {
	Hour   int64   `json:"hour"`
	Kind   string  `json:"kind"` // quiet, not-relaying, back, step, reroute, drift
	Node   string  `json:"node,omitempty"`
	Other  string  `json:"other,omitempty"` // a reroute's new carrier; a drift's observer
	Where  string  `json:"where,omitempty"` // the corridor direction or route
	Before float64 `json:"before,omitempty"`
	After  float64 `json:"after,omitempty"`
}

func where(c CorridorStatus, d CorridorDirection) string {
	return fmt.Sprintf("%s: %s → %s", c.Name, d.From, d.To)
}

// explainedBy fills each open incident's Affects, and builds the timeline.
func (w *Watch) explainedBy(st *Status) {
	for i := range st.Open {
		pk := st.Open[i].Node
		for _, c := range st.Corridors {
			for _, d := range c.Directions {
				if (d.Reach != nil && d.Reach.Cause != nil && d.Reach.Cause.Node == pk) || (d.Reroute != nil && d.Reroute.From == pk) {
					st.Open[i].Affects = append(st.Open[i].Affects, where(c, d))
				}
			}
		}
		for _, r := range st.Routes {
			if r.Reach != nil && r.Reach.Cause != nil && r.Reach.Cause.Node == pk {
				st.Open[i].Affects = append(st.Open[i].Affects, r.Name)
			}
		}
	}
	ev := []Event{}
	for _, i := range st.Open {
		ev = append(ev, Event{Hour: i.Start, Kind: i.Kind, Node: i.Node})
	}
	for _, i := range st.Recent {
		ev = append(ev, Event{Hour: i.Start, Kind: i.Kind, Node: i.Node}, Event{Hour: *i.End, Kind: "back", Node: i.Node})
	}
	for _, c := range st.Corridors {
		for _, d := range c.Directions {
			if d.Reach != nil && d.Reach.Since != nil {
				ev = append(ev, Event{Hour: *d.Reach.Since, Kind: "step", Where: where(c, d), Before: d.Reach.Usual, After: d.Reach.Last})
			}
			if d.Reroute != nil {
				ev = append(ev, Event{Hour: d.Reroute.Since, Kind: "reroute", Node: d.Reroute.From, Other: d.Reroute.To, Where: where(c, d)})
			}
		}
	}
	for _, r := range st.Routes {
		if r.Reach != nil && r.Reach.Since != nil {
			ev = append(ev, Event{Hour: *r.Reach.Since, Kind: "step", Where: r.Name, Before: r.Reach.Usual, After: r.Reach.Last})
		}
	}
	for _, d := range st.Drift {
		ev = append(ev, Event{Hour: d.Since, Kind: "drift", Node: d.Node, Other: d.Observer, Before: d.Before, After: d.Now})
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].Hour > ev[j].Hour })
	if len(ev) > 60 {
		ev = ev[:60]
	}
	st.Events = ev
}
