package judge

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEntropyConfidence(t *testing.T) {
	if got := EntropyConfidence([]float64{1, 0, 0}); !near(got, 1) {
		t.Errorf("certain = %v, want 1", got)
	}
	if got := EntropyConfidence([]float64{0.25, 0.25, 0.25, 0.25}); !near(got, 0) {
		t.Errorf("uniform = %v, want 0", got)
	}
	if got := NoulConfidence(0.5); !near(got, 0) {
		t.Errorf("coin flip = %v, want 0", got)
	}
	// H(0.9,0.1) = 0.325083 nats; 1 - H/ln2 = 0.531004
	if got := NoulConfidence(0.9); math.Abs(got-0.531004) > 1e-6 {
		t.Errorf("NoulConfidence(0.9) = %v, want 0.531004", got)
	}
	if NoulConfidence(0.1) != NoulConfidence(0.9) {
		t.Error("confidence must be symmetric in yes/no")
	}
}

func TestExpectedLevelAndArgmax(t *testing.T) {
	if got := ExpectedLevel(map[string]float64{"0": 0.1, "2": 0.5, "3": 0.4}); !near(got, 2.2) {
		t.Errorf("ExpectedLevel = %v, want 2.2", got)
	}
	// Unnormalized mass (e.g. softmax over a label subset) must still average correctly.
	if got := ExpectedLevel(map[string]float64{"1": 0.2, "3": 0.2}); !near(got, 2) {
		t.Errorf("unnormalized ExpectedLevel = %v, want 2", got)
	}
	if got := ExpectedLevel(nil); got != 0 {
		t.Errorf("empty = %v", got)
	}
	if got := Argmax(map[string]float64{"b": 0.4, "a": 0.4, "c": 0.2}); got != "a" {
		t.Errorf("Argmax tie = %q, want a", got)
	}
}
