package netwatch

import (
	"math"
	"sort"
)

// lowerTail is the chance of seeing k or fewer when about mean are expected
// and the samples (the same hours on earlier days) say how much it usually
// varies. Counts that vary more than chance alone use a negative binomial,
// else Poisson.
func lowerTail(k int, mean float64, samples []float64) float64 {
	if mean <= 0 {
		return 1
	}
	v := spread(samples)
	v *= v
	if v <= mean {
		return poissonCDF(k, mean)
	}
	r := mean * mean / (v - mean)
	return negBinCDF(k, r, r/(r+mean))
}

func poissonCDF(k int, m float64) float64 {
	if k < 0 {
		return 0
	}
	sum, term := 0.0, math.Exp(-m)
	for i := 0; i <= k; i++ {
		if i > 0 {
			term *= m / float64(i)
		}
		sum += term
	}
	return math.Min(sum, 1)
}

// negBinCDF is P(X <= k) for failures before r successes of chance p.
func negBinCDF(k int, r, p float64) float64 {
	if k < 0 {
		return 0
	}
	lgR, _ := math.Lgamma(r)
	sum := 0.0
	for i := 0; i <= k; i++ {
		a, _ := math.Lgamma(float64(i) + r)
		b, _ := math.Lgamma(float64(i) + 1)
		sum += math.Exp(a - b - lgR + r*math.Log(p) + float64(i)*math.Log1p(-p))
	}
	return math.Min(sum, 1)
}

// spread is a robust standard deviation: 1.4826 times the median absolute
// deviation, so one odd day does not widen it.
func spread(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m := medianF(x)
	d := make([]float64, len(x))
	for i, v := range x {
		d[i] = math.Abs(v - m)
	}
	return 1.4826 * medianF(d)
}

func medianF(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	s := append([]float64(nil), a...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// shareRange is the range a share usually falls in on a day with n tries:
// three spreads either side of its usual, where the spread is the larger of
// how much it moved from day to day and what chance alone gives with n.
func shareRange(usual float64, days []float64, n int) (lo, hi float64) {
	return shareRangeK(usual, days, n, 3)
}

func shareRangeK(usual float64, days []float64, n int, k float64) (lo, hi float64) {
	sd := spread(days)
	if n > 0 {
		sd = math.Max(sd, math.Sqrt(usual*(1-usual)/float64(n)))
	}
	sd = math.Max(sd, 0.02)
	return math.Max(0, usual-k*sd), math.Min(1, usual+k*sd)
}

// stepChange finds where the daily shares x (oldest first; -1 for days with
// too little to say) moved to a new level and stayed there: the earliest day
// from which every usable day falls outside the range of the days before it.
// A step is held to four spreads, a little stricter than a day's verdict, so
// a borderline day is not taken as its start. It needs at least three usable
// days before and two after. ok is false when there is no such step.
func stepChange(x []float64, ns []int) (at int, before float64, down, ok bool) {
	var idx []int
	for i, v := range x {
		if v >= 0 {
			idx = append(idx, i)
		}
	}
	for j := 3; j <= len(idx)-2; j++ {
		var prev []float64
		for _, i := range idx[:j] {
			prev = append(prev, x[i])
		}
		u := medianF(prev)
		allLow, allHigh := true, true
		for _, i := range idx[j:] {
			lo, hi := shareRangeK(u, prev, ns[i], 4)
			allLow = allLow && x[i] < lo
			allHigh = allHigh && x[i] > hi
		}
		if allLow || allHigh {
			return idx[j], u, allLow, true
		}
	}
	return 0, 0, false, false
}
