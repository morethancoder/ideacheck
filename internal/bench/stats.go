// Package bench compares backends, models and rubrics on a labelled dataset.
package bench

import (
	"math"
	"sort"
)

// Percentile uses nearest-rank on a copy of xs; 0 for empty input.
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}

func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// StdDev is the population standard deviation.
func StdDev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := Mean(xs)
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(xs)))
}

// CohenKappa is chance-corrected agreement between two raters' labels.
// It is 1 for perfect agreement and 0 for chance-level; NaN-free: identical
// constant raters count as perfect agreement.
func CohenKappa(a, b []string) float64 {
	n := float64(len(a))
	if n == 0 || len(a) != len(b) {
		return 0
	}
	countA, countB := map[string]float64{}, map[string]float64{}
	var agree float64
	for i := range a {
		countA[a[i]]++
		countB[b[i]]++
		if a[i] == b[i] {
			agree++
		}
	}
	var expected float64
	for label, ca := range countA {
		expected += (ca / n) * (countB[label] / n)
	}
	if expected == 1 {
		return 1
	}
	return (agree/n - expected) / (1 - expected)
}

// Spearman is the rank correlation (Pearson on average ranks, so ties are handled).
func Spearman(a, b []float64) float64 {
	if len(a) < 2 || len(a) != len(b) {
		return 0
	}
	return pearson(ranks(a), ranks(b))
}

func ranks(xs []float64) []float64 {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return xs[idx[i]] < xs[idx[j]] })
	out := make([]float64, len(xs))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

func pearson(a, b []float64) float64 {
	ma, mb := Mean(a), Mean(b)
	var cov, va, vb float64
	for i := range a {
		cov += (a[i] - ma) * (b[i] - mb)
		va += (a[i] - ma) * (a[i] - ma)
		vb += (b[i] - mb) * (b[i] - mb)
	}
	if va == 0 || vb == 0 {
		return 0
	}
	return cov / math.Sqrt(va*vb)
}
