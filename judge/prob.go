package judge

import (
	"math"
	"strconv"
)

// EntropyConfidence is 1 - H(p)/ln(n): 1 when all mass is on one outcome, 0 when
// uniform. Backends without a native confidence (logprob, mock, Jev nouls) share
// this definition so the number means the same thing everywhere.
func EntropyConfidence(probs []float64) float64 {
	if len(probs) < 2 {
		return 1
	}
	var h float64
	for _, p := range probs {
		if p > 0 {
			h -= p * math.Log(p)
		}
	}
	c := 1 - h/math.Log(float64(len(probs)))
	return math.Max(0, math.Min(1, c))
}

// NoulConfidence is EntropyConfidence over {yes, no}.
func NoulConfidence(pYes float64) float64 {
	return EntropyConfidence([]float64{pYes, 1 - pYes})
}

// ExpectedLevel is Σ i·p_i over probabilities keyed by level index.
func ExpectedLevel(probs map[string]float64) float64 {
	var sum, mass float64
	for k, p := range probs {
		i, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		sum += float64(i) * p
		mass += p
	}
	if mass == 0 {
		return 0
	}
	return sum / mass
}

// Argmax returns the highest-probability key; ties break alphabetically so the
// result is deterministic.
func Argmax(probs map[string]float64) string {
	best, bestP := "", -1.0
	for k, p := range probs {
		if p > bestP || (p == bestP && k < best) {
			best, bestP = k, p
		}
	}
	return best
}
