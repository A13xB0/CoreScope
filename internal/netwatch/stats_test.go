package netwatch

import (
	"math"
	"testing"
)

func TestLowerTailWeighsHowUnlikelyAQuietSpellIs(t *testing.T) {
	steady := []float64{30, 31, 29, 32, 30, 31, 30}
	if p := lowerTail(0, 31, steady); p > 1e-10 {
		t.Errorf("0 when 31 are expected and it never varies: p=%g, want tiny", p)
	}
	if p := lowerTail(25, 31, steady); p < 0.05 {
		t.Errorf("25 when 31 are expected: p=%g, want ordinary", p)
	}
	bursty := []float64{5, 60, 2, 45, 30, 0, 70}
	if p := lowerTail(0, 30, bursty); p < 1e-4 {
		t.Errorf("0 from a repeater that is often near 0: p=%g, want not alarming", p)
	}
	if p := lowerTail(0, 9, []float64{9, 9, 9}); p < 1e-4 || p > 2e-4 {
		t.Errorf("Poisson P(0|9) = %g", p)
	}
}

func TestShareRangeWidensForSmallSamplesAndSwingingDays(t *testing.T) {
	steady := []float64{0.86, 0.88, 0.87, 0.85, 0.9, 0.87}
	lo, _ := shareRange(0.87, steady, 300)
	if lo < 0.7 || lo > 0.82 {
		t.Errorf("steady share, many tries: low %v", lo)
	}
	lo2, _ := shareRange(0.87, steady, 17)
	if lo2 >= lo {
		t.Errorf("17 tries should widen the range: %v vs %v", lo2, lo)
	}
	swinging := []float64{0.8, 0.75, 0.73, 0.71, 0.47, 0.53, 0.9, 0.87}
	if lo3, _ := shareRange(0.74, swinging, 200); lo3 > 0.5 {
		t.Errorf("a share that swings 47-90%% should allow 50%%: low %v", lo3)
	}
}

func TestStepChangeFindsWhenAShareDroppedAndStayedDown(t *testing.T) {
	x := []float64{0.9, 0.91, 0.87, 0.89, 0.88, 0.92, 0.88, 0.82, 0.34, 0.26, 0.29, 0.22, 0.48}
	ns := make([]int, len(x))
	for i := range ns {
		ns[i] = 190
	}
	at, before, down, ok := stepChange(x, ns)
	if !ok || at != 8 || !down || math.Abs(before-0.885) > 0.02 {
		t.Fatalf("step at %d before %v down %v ok %v, want day 8, about 0.88, down", at, before, down, ok)
	}
	if _, _, _, ok := stepChange([]float64{0.9, 0.88, 0.91, 0.5, 0.89, 0.9}, ns[:6]); ok {
		t.Error("one bad day is not a step")
	}
	gaps := []float64{0.9, -1, 0.88, 0.91, -1, 0.3, 0.25}
	if at, _, _, ok := stepChange(gaps, []int{100, 0, 100, 100, 0, 100, 100}); !ok || at != 5 {
		t.Errorf("days with too little to say are skipped: at %d ok %v", at, ok)
	}
}

// A slot that grew from tens to hundreds over two weeks varies by multiples;
// a slot with nothing in it is still far outside that.
func TestASilentSlotStandsOutEvenWhereTrafficVariesByMultiples(t *testing.T) {
	growing := []float64{13, 125, 71, 48, 53, 127, 80, 132, 232, 379, 257, 333}
	if p := lowerTail(0, 150, growing); p > 1e-4 {
		t.Errorf("0 in a slot that held 13 to 379: p=%g, want under 1e-4", p)
	}
	if p := lowerTail(40, 150, growing); p < 1e-3 {
		t.Errorf("40 in a slot that held 13 to 379: p=%g, want ordinary", p)
	}
}

// A share that rose for a week and then fell back is dated from its fall,
// against the week before it, not against every day in the window.
func TestStepChangeComparesWithTheWeekBefore(t *testing.T) {
	x := []float64{0.15, 0.15, 0.35, 0.19, 0.29, 0.46, 0.43, 0.23, 0.38, 0.24, 0.74, 0.86, 0.91, 0.87, 0.91, 0.87, 0.82, 0.36, 0.26, 0.29, 0.22}
	ns := make([]int, len(x))
	for i := range ns {
		ns[i] = 200
	}
	at, before, down, ok := stepChange(x, ns)
	if !ok || at != 17 || !down || before < 0.85 {
		t.Fatalf("step at %d before %v down %v ok %v, want a fall on day 17 from about 0.87", at, before, down, ok)
	}
}
