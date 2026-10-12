// Package pathwatch follows the path between two repeaters: how many packets
// that passed the first went on to reach the second, day by day, the routes
// they took between the two, and when that route changed and why.
//
// Days are 24-hour buckets ending at Input.Now, newest last, so the newest
// bucket is always "the last 24 hours". Everything here is computed from
// what the caller hands in; nothing is kept between calls.
package pathwatch

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Crossing is one packet that passed A and then reached B: when it was first
// heard, and the repeaters it went through between the two (none when it went
// straight from A to B).
type Crossing struct {
	At    time.Time
	Route []string
}

// Input is a path's packets.
type Input struct {
	Now     time.Time
	Passed  []time.Time // every packet that passed A (or came from it)
	Reached []Crossing  // those that then reached B
}

// Activity is what the caller knows of a repeater beyond this path.
type Activity interface {
	// Relays is how many packets went through pk in [from, to).
	Relays(pk string, from, to time.Time) int
	// FirstHeard is when pk was first heard at all.
	FirstHeard(pk string) (time.Time, bool)
}

// Options tunes the watch. Zero values take the defaults in brackets.
type Options struct {
	WindowDays int     // days shown [30]
	BaseDays   int     // days a share's usual is taken from [14]
	MinPassed  int     // packets passing A a day needs before its share means anything [10]
	MinReached int     // crossings needed before routes are compared [10]
	DropFrom   float64 // a repeater carrying at least this share... [0.2]
	DropTo     float64 // ...that falls under this has dropped out [0.05]
	RiseFrom   float64 // a repeater carrying under this share... [0.05]
	RiseTo     float64 // ...that rises to at least this has joined [0.15]
	MainMove   float64 // the main route's share moving this far is a change [0.25]
	QuietRatio float64 // relaying under this share of its usual everywhere is quiet [0.2]
	NewDays    int     // first heard this recently is a new repeater [14]
	FlagDays   int     // a change stays a flag this long [7]
}

func (o Options) withDefaults() Options {
	di := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	df := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	di(&o.WindowDays, 30)
	di(&o.BaseDays, 14)
	di(&o.MinPassed, 10)
	di(&o.MinReached, 10)
	df(&o.DropFrom, 0.2)
	df(&o.DropTo, 0.05)
	df(&o.RiseFrom, 0.05)
	df(&o.RiseTo, 0.15)
	df(&o.MainMove, 0.25)
	df(&o.QuietRatio, 0.2)
	di(&o.NewDays, 14)
	di(&o.FlagDays, 7)
	return o
}

// Kinds of route change, most telling first.
const (
	KindRepeaterQuiet = "repeater-quiet" // dropped out of the path and stopped relaying everywhere
	KindRerouted      = "rerouted"       // dropped out of the path but still busy elsewhere
	KindNewRepeater   = "new-repeater"   // started carrying, and is new
	KindSharing       = "sharing"        // started carrying, and is not new
	KindRouteChanged  = "route-changed"  // the main route moved
)

var kindRank = map[string]int{KindRepeaterQuiet: 0, KindRerouted: 1, KindNewRepeater: 2, KindSharing: 3, KindRouteChanged: 4}

// Day is one 24-hour bucket.
type Day struct {
	Day     time.Time `json:"day"` // its start
	Passed  int       `json:"passed"`
	Reached int       `json:"reached"`
	Share   float64   `json:"share"` // -1 when too few passed to say
}

// Hops is how many hops it took from A to B.
type Hops struct {
	Median int `json:"median"`
	Min    int `json:"min"`
	Max    int `json:"max"`
}

// Route is one way the crossings went, with its share of them over the
// window and over the week before the last 24 hours.
type Route struct {
	Via   []string `json:"via"`
	Share float64  `json:"share"`
	Usual float64  `json:"usual"`
}

// Change is a shift in the route on the day starting Day, against the week
// before it. Node is the repeater it is about (none for a route change);
// Was and Now its share of the crossings; From and To the main route before
// and after a route change; ShareWas and ShareNow the share of packets that
// got through, before and on the day.
type Change struct {
	Day      time.Time `json:"day"`
	Kind     string    `json:"kind"`
	Node     string    `json:"node,omitempty"`
	Was      float64   `json:"was"`
	Now      float64   `json:"now"`
	From     []string  `json:"from,omitempty"`
	To       []string  `json:"to,omitempty"`
	ShareWas float64   `json:"shareWas"`
	ShareNow float64   `json:"shareNow"`
}

// Result is what a watch shows.
type Result struct {
	Days    []Day    `json:"days"` // oldest first, the last 24 hours last
	Share   float64  `json:"share"`
	Usual   float64  `json:"usual"`
	Low     float64  `json:"low"`
	High    float64  `json:"high"`
	Verdict string   `json:"verdict"` // worse, better, normal, new; "" when there is not enough to say
	Hops    Hops     `json:"hops"`
	Routes  []Route  `json:"routes"`
	Changes []Change `json:"changes"` // oldest first
	Flag    *Change  `json:"flag,omitempty"`
}

type bucket struct {
	passed   int
	crossing []Crossing
}

// Analyse works out a watch from a path's packets.
func Analyse(in Input, act Activity, opt Options) Result {
	o := opt.withDefaults()
	n := o.WindowDays + o.BaseDays
	if m := o.WindowDays + 7; m > n {
		n = m
	}
	b := make([]bucket, n) // b[0] is the last 24 hours
	idx := func(t time.Time) int {
		if !t.Before(in.Now) {
			return -1
		}
		i := int((in.Now.Sub(t) - 1) / (24 * time.Hour)) // a bucket holds [its start, its end)
		if i >= n {
			return -1
		}
		return i
	}
	oldest := in.Now
	for _, t := range in.Passed {
		if i := idx(t); i >= 0 {
			b[i].passed++
			if t.Before(oldest) {
				oldest = t
			}
		}
	}
	for _, c := range in.Reached {
		if i := idx(c.At); i >= 0 {
			b[i].crossing = append(b[i].crossing, c)
		}
	}
	have := int((in.Now.Sub(oldest)-1)/(24*time.Hour)) + 1 // buckets that hold any data
	if have > n {
		have = n
	}
	start := func(i int) time.Time { return in.Now.Add(-time.Duration(i+1) * 24 * time.Hour) }
	share := func(i int) float64 {
		if b[i].passed < o.MinPassed {
			return -1
		}
		return math.Min(1, float64(len(b[i].crossing))/float64(b[i].passed))
	}

	r := Result{Days: []Day{}, Routes: []Route{}, Changes: []Change{}}
	show := o.WindowDays
	if show > have {
		show = have
	}
	for i := show - 1; i >= 0; i-- {
		r.Days = append(r.Days, Day{Day: start(i), Passed: b[i].passed, Reached: len(b[i].crossing), Share: share(i)})
	}

	// The last 24 hours against the BaseDays before them.
	r.Share = share(0)
	var prev []float64
	for i := 1; i <= o.BaseDays && i < have; i++ {
		if s := share(i); s >= 0 {
			prev = append(prev, s)
		}
	}
	if r.Share >= 0 && len(prev) >= 3 {
		r.Usual = median(prev)
		r.Low, r.High = shareRange(r.Usual, prev, b[0].passed)
		switch {
		case r.Usual < 0.02:
			r.Verdict = "new"
		case r.Share < r.Low:
			r.Verdict = "worse"
		case r.Share > r.High:
			r.Verdict = "better"
		default:
			r.Verdict = "normal"
		}
	}

	// Hops and routes over the window shown.
	var window, week []Crossing
	for i := 0; i < show; i++ {
		window = append(window, b[i].crossing...)
	}
	for i := 1; i <= 7 && i < have; i++ {
		week = append(week, b[i].crossing...)
	}
	if len(window) > 0 {
		hops := make([]float64, len(window))
		for k, c := range window {
			hops[k] = float64(len(c.Route) + 1)
		}
		sort.Float64s(hops)
		r.Hops = Hops{Median: int(median(hops)), Min: int(hops[0]), Max: int(hops[len(hops)-1])}
		count, usual := routeCounts(window), routeCounts(week)
		for key, c := range count {
			rt := Route{Via: splitRoute(key), Share: float64(c) / float64(len(window))}
			if len(week) > 0 {
				rt.Usual = float64(usual[key]) / float64(len(week))
			}
			r.Routes = append(r.Routes, rt)
		}
		sort.Slice(r.Routes, func(i, j int) bool {
			if r.Routes[i].Share != r.Routes[j].Share {
				return r.Routes[i].Share > r.Routes[j].Share
			}
			return strings.Join(r.Routes[i].Via, ">") < strings.Join(r.Routes[j].Via, ">")
		})
		if len(r.Routes) > 5 {
			r.Routes = r.Routes[:5]
		}
	}

	// Route changes, day by day over the window shown, each day against the
	// week before it; a change already seen in that week is not repeated.
	for i := show - 1; i >= 0; i-- {
		if i+7 >= have {
			continue
		}
		for _, c := range w7changes(b, i, start, share, act, o) {
			if !seenRecently(r.Changes, c, start(i), o) {
				r.Changes = append(r.Changes, c)
			}
		}
	}
	for k := range r.Changes {
		c := r.Changes[k]
		if in.Now.Sub(c.Day) > time.Duration(o.FlagDays)*24*time.Hour {
			continue
		}
		if r.Flag == nil || c.Day.After(r.Flag.Day) || (c.Day.Equal(r.Flag.Day) && kindRank[c.Kind] < kindRank[r.Flag.Kind]) {
			cc := c
			r.Flag = &cc
		}
	}
	return r
}

// w7changes compares day i with the seven days before it.
func w7changes(b []bucket, i int, start func(int) time.Time, share func(int) float64, act Activity, o Options) []Change {
	var base []Crossing
	passedBase := 0
	for k := i + 1; k <= i+7; k++ {
		base = append(base, b[k].crossing...)
		passedBase += b[k].passed
	}
	day := b[i].crossing
	if len(base) < o.MinReached || b[i].passed < o.MinPassed {
		return nil
	}
	shareWas, shareNow := 0.0, share(i)
	if passedBase > 0 {
		shareWas = float64(len(base)) / float64(passedBase)
	}
	was, now := nodeShares(base), nodeShares(day)
	from, to := start(i), start(i).Add(24*time.Hour)
	var out []Change
	keys := make([]string, 0, len(was)+len(now))
	for pk := range was {
		keys = append(keys, pk)
	}
	for pk := range now {
		if _, ok := was[pk]; !ok {
			keys = append(keys, pk)
		}
	}
	sort.Strings(keys)
	for _, pk := range keys {
		w, n := was[pk], now[pk]
		switch {
		case w >= o.DropFrom && n < o.DropTo:
			kind := KindRerouted
			if act != nil {
				var usual []float64
				for k := 1; k <= 7; k++ {
					usual = append(usual, float64(act.Relays(pk, from.Add(-time.Duration(k)*24*time.Hour), to.Add(-time.Duration(k)*24*time.Hour))))
				}
				if m := median(usual); m > 0 && float64(act.Relays(pk, from, to)) < o.QuietRatio*m {
					kind = KindRepeaterQuiet
				}
			}
			out = append(out, Change{Day: from, Kind: kind, Node: pk, Was: w, Now: n, ShareWas: shareWas, ShareNow: shareNow})
		case w < o.RiseFrom && n >= o.RiseTo && len(day) >= o.MinReached:
			kind := KindSharing
			if act != nil {
				if first, ok := act.FirstHeard(pk); ok && to.Sub(first) <= time.Duration(o.NewDays)*24*time.Hour {
					kind = KindNewRepeater
				}
			}
			out = append(out, Change{Day: from, Kind: kind, Node: pk, Was: w, Now: n, ShareWas: shareWas, ShareNow: shareNow})
		}
	}
	if len(out) == 0 && len(day) >= o.MinReached {
		mw, sw := mainRoute(base)
		mn, sn := mainRoute(day)
		if mw != mn || math.Abs(sw-sn) >= o.MainMove {
			out = append(out, Change{Day: from, Kind: KindRouteChanged, Was: sw, Now: sn, From: splitRoute(mw), To: splitRoute(mn), ShareWas: shareWas, ShareNow: shareNow})
		}
	}
	return out
}

// seenRecently says whether the same change (kind and repeater, or for a
// route change the same new route) was already recorded in the week before day.
func seenRecently(list []Change, c Change, day time.Time, o Options) bool {
	for k := len(list) - 1; k >= 0; k-- {
		p := list[k]
		if day.Sub(p.Day) > 7*24*time.Hour {
			break
		}
		if p.Kind == c.Kind && p.Node == c.Node && strings.Join(p.To, ">") == strings.Join(c.To, ">") {
			return true
		}
	}
	return false
}

func routeKey(r []string) string { return strings.Join(r, ">") }

func splitRoute(k string) []string {
	if k == "" {
		return []string{}
	}
	return strings.Split(k, ">")
}

func routeCounts(cs []Crossing) map[string]int {
	m := map[string]int{}
	for _, c := range cs {
		m[routeKey(c.Route)]++
	}
	return m
}

func mainRoute(cs []Crossing) (string, float64) {
	if len(cs) == 0 {
		return "", 0
	}
	best, bn := "", -1
	for k, c := range routeCounts(cs) {
		if c > bn || (c == bn && k < best) {
			best, bn = k, c
		}
	}
	return best, float64(bn) / float64(len(cs))
}

// nodeShares is, for each repeater on the crossings' routes, the share of
// crossings that went through it.
func nodeShares(cs []Crossing) map[string]float64 {
	m := map[string]float64{}
	if len(cs) == 0 {
		return m
	}
	for _, c := range cs {
		seen := map[string]bool{}
		for _, pk := range c.Route {
			if pk != "" && !seen[pk] {
				seen[pk] = true
				m[pk]++
			}
		}
	}
	for k := range m {
		m[k] /= float64(len(cs))
	}
	return m
}

func median(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	s := append([]float64(nil), a...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func quantile(x []float64, q float64) float64 {
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	pos := q * float64(len(s)-1)
	i := int(pos)
	if i+1 >= len(s) {
		return s[len(s)-1]
	}
	return s[i] + (pos-float64(i))*(s[i+1]-s[i])
}

// shareRange is the range a share usually falls in on a day with n tries:
// three spreads either side of its usual, the spread being the larger of how
// much it moved from day to day (interquartile) and what chance alone gives
// with n.
func shareRange(usual float64, days []float64, n int) (lo, hi float64) {
	sd := 0.0
	if len(days) >= 2 {
		sd = (quantile(days, 0.75) - quantile(days, 0.25)) / 1.349
	}
	if n > 0 {
		sd = math.Max(sd, math.Sqrt(usual*(1-usual)/float64(n)))
	}
	sd = math.Max(sd, 0.02)
	return math.Max(0, usual-3*sd), math.Min(1, usual+3*sd)
}
