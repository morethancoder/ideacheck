package bench

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPercentileNearestRank(t *testing.T) {
	xs := []float64{50, 10, 40, 20, 30}
	for p, want := range map[float64]float64{50: 30, 95: 50, 100: 50, 1: 10, 20: 10, 21: 20} {
		if got := Percentile(xs, p); got != want {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
	if xs[0] != 50 {
		t.Error("Percentile must not reorder its input")
	}
	if Percentile(nil, 50) != 0 {
		t.Error("empty input")
	}
}

func TestStdDev(t *testing.T) {
	if got := StdDev([]float64{2, 4, 4, 4, 5, 5, 7, 9}); !near(got, 2) {
		t.Errorf("StdDev = %v, want 2", got)
	}
	if StdDev([]float64{3}) != 0 {
		t.Error("a single run has no spread")
	}
}

func TestCohenKappa(t *testing.T) {
	// Textbook 2x2: 20 yes/yes, 5 yes/no, 10 no/yes, 15 no/no → po=0.7, pe=0.5, κ=0.4
	var a, b []string
	add := func(n int, x, y string) {
		for range n {
			a, b = append(a, x), append(b, y)
		}
	}
	add(20, "y", "y")
	add(5, "y", "n")
	add(10, "n", "y")
	add(15, "n", "n")
	if got := CohenKappa(a, b); !near(got, 0.4) {
		t.Errorf("kappa = %v, want 0.4", got)
	}
	if got := CohenKappa([]string{"a", "b"}, []string{"a", "b"}); !near(got, 1) {
		t.Errorf("perfect agreement = %v", got)
	}
	if got := CohenKappa([]string{"a", "a"}, []string{"a", "a"}); got != 1 {
		t.Errorf("constant identical raters = %v, want 1 (not NaN)", got)
	}
}

func TestSpearman(t *testing.T) {
	if got := Spearman([]float64{1, 2, 3, 4}, []float64{10, 20, 35, 90}); !near(got, 1) {
		t.Errorf("monotone = %v", got)
	}
	if got := Spearman([]float64{1, 2, 3, 4}, []float64{9, 7, 5, 1}); !near(got, -1) {
		t.Errorf("reversed = %v", got)
	}
	// ties get average ranks: a=[1,2.5,2.5,4], b=[1,2,3,4] → r = 4.5/sqrt(4.5*5)
	if got := Spearman([]float64{1, 2, 2, 3}, []float64{1, 2, 3, 4}); !near(got, 4.5/math.Sqrt(22.5)) {
		t.Errorf("with ties = %v", got)
	}
	if Spearman([]float64{1, 1, 1}, []float64{1, 2, 3}) != 0 {
		t.Error("a constant series has no rank correlation")
	}
}
