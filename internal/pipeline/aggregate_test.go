package pipeline

import (
	"math"
	"testing"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/rubric"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

var (
	levels4 = []string{"a", "b", "c", "d"}
	qs      = []judge.Question{
		{ID: "acuity", Kind: judge.Score, Levels: levels4, Weight: 2, Polarity: 1},
		{ID: "sisp", Kind: judge.Noul, Weight: 1.5, Polarity: -1},
		{ID: "insight", Kind: judge.Noul, Weight: 1.5, Polarity: 1},
		{ID: "competitors", Kind: judge.Noul, Weight: 0, Polarity: 0},
	}
)

func TestCombineWeightsPolarityAndConfidence(t *testing.T) {
	agg := Combine(qs, []judge.Answer{
		{Score: 1.5, Confidence: 0.6}, // normalized 0.5
		{Noul: 0.9, Confidence: 0.8},  // bad-when-high → 0.1
		{Noul: 0.8, Confidence: 1.0},
		{Noul: 0.95, Confidence: 0.1}, // informational: no effect on composite
	})
	// (0.5*2 + 0.1*1.5 + 0.8*1.5) / 5 = 0.47
	if !near(agg.Composite, 0.47) {
		t.Errorf("composite = %v, want 0.47", agg.Composite)
	}
	// Only the score has a spread to be unsure about: the nouls are
	// probabilities already, so their entropy does not count.
	if !near(agg.Confidence, 0.6) {
		t.Errorf("confidence = %v, want 0.6", agg.Confidence)
	}
	// Gates read the raw normalized value, before polarity.
	if !near(agg.Values["sisp"], 0.9) || !near(agg.Values["competitors"], 0.95) || !near(agg.Values["acuity"], 0.5) {
		t.Errorf("values = %v", agg.Values)
	}
	if len(agg.Risks) != 1 || agg.Risks[0].ID != "sisp" || !near(agg.Risks[0].Value, 0.1) {
		t.Errorf("risks = %+v", agg.Risks)
	}
	if len(agg.Strengths) != 2 || agg.Strengths[0].ID != "insight" {
		t.Errorf("strengths = %+v (insight pulls 0.45, acuity pulls 0)", agg.Strengths)
	}
}

func TestCombineExcludesFailedAndRenormalizes(t *testing.T) {
	agg := Combine(qs, []judge.Answer{
		{Err: "timeout"},
		{Noul: 0.2, Confidence: 0.5},
		{Noul: 0.6, Confidence: 0.5},
		{Noul: 0.5},
	})
	// acuity dropped: (0.8*1.5 + 0.6*1.5) / 3 = 0.7 — not divided by the full 5.
	if !near(agg.Composite, 0.7) || !near(agg.Answered, 3) {
		t.Errorf("composite = %v answered = %v", agg.Composite, agg.Answered)
	}
	// With no score or choice answered, confidence falls back to the nouls'.
	if !near(agg.Confidence, 0.5) {
		t.Errorf("confidence = %v, want the noul fallback 0.5", agg.Confidence)
	}
	if len(agg.Failed) != 1 || agg.Failed[0] != "acuity" {
		t.Errorf("failed = %v", agg.Failed)
	}
	if _, ok := agg.Values["acuity"]; ok {
		t.Error("failed question must be absent from gate values, not 0")
	}
}

func TestCombineNothingAnswered(t *testing.T) {
	agg := Combine(qs[:1], []judge.Answer{{Err: "x"}})
	if agg.Composite != 0 || agg.Answered != 0 || math.IsNaN(agg.Composite) {
		t.Errorf("agg = %+v", agg)
	}
}

func TestNormalize(t *testing.T) {
	score := judge.Question{Kind: judge.Score, Levels: levels4}
	if v, ok := Normalize(score, judge.Answer{Score: 3}); !ok || !near(v, 1) {
		t.Errorf("top level = %v %v", v, ok)
	}
	if v, _ := Normalize(score, judge.Answer{Score: 4.2}); v != 1 {
		t.Errorf("out-of-range score not clamped: %v", v)
	}
	choice := judge.Question{Kind: judge.Choice, Values: map[string]float64{"yes": 0.9}}
	if v, ok := Normalize(choice, judge.Answer{Choice: "yes"}); !ok || v != 0.9 {
		t.Errorf("mapped choice = %v %v", v, ok)
	}
	if _, ok := Normalize(choice, judge.Answer{Choice: "other"}); ok {
		t.Error("unmapped choice must not contribute")
	}
}

func TestTopKeepsThreeStrongestPulls(t *testing.T) {
	s, _ := split([]Contribution{
		{ID: "light", Value: 1.0, Weight: 0.5}, // pull 0.25
		{ID: "heavy", Value: 0.7, Weight: 2.0}, // pull 0.40
		{ID: "mid", Value: 0.8, Weight: 1.0},   // pull 0.30
		{ID: "weak", Value: 0.55, Weight: 1.0}, // pull 0.05
	})
	if len(s) != 3 || s[0].ID != "heavy" || s[1].ID != "mid" || s[2].ID != "light" {
		t.Errorf("strengths = %+v", s)
	}
}

func verdictRubric(t *testing.T) rubric.Verdict {
	t.Helper()
	rb, err := rubric.Load(memFiles{"r/x.yaml": `
questions:
  - {id: tarpit, kind: noul, instructions: i, uses: [idea], weight: 1, polarity: -1}
  - {id: barrier, kind: noul, instructions: i, uses: [idea], weight: 1, polarity: 1}
verdict:
  gates:
    - {when: "tarpit > 0.7 && barrier < 0.3", max: explore, reason: Likely tarpit}
    - {when: "tarpit > 0.95", max: park, reason: Certain tarpit}
  thresholds: {build: 0.70, explore: 0.50, park: 0.30}
  min_confidence: 0.45
`}, "r", "x")
	if err != nil {
		t.Fatal(err)
	}
	return rb.Verdict
}

func TestDecide(t *testing.T) {
	v := verdictRubric(t)
	calm := map[string]float64{"tarpit": 0.1, "barrier": 0.9}
	cases := []struct {
		name      string
		composite float64
		conf      float64
		values    map[string]float64
		want      string
		reason    string
	}{
		{"at build threshold", 0.70, 0.9, calm, rubric.Build, ""},
		{"just under build", 0.6999, 0.9, calm, rubric.Explore, ""},
		{"at explore threshold", 0.50, 0.9, calm, rubric.Explore, ""},
		{"at park threshold", 0.30, 0.9, calm, rubric.Park, ""},
		{"below park", 0.2999, 0.9, calm, rubric.Kill, ""},
		{"gate caps build to explore", 0.9, 0.9, map[string]float64{"tarpit": 0.8, "barrier": 0.1}, rubric.Explore, "Likely tarpit"},
		{"lowest cap wins", 0.9, 0.9, map[string]float64{"tarpit": 0.99, "barrier": 0.1}, rubric.Park, "Certain tarpit"},
		{"gate never raises a verdict", 0.1, 0.9, map[string]float64{"tarpit": 0.8, "barrier": 0.1}, rubric.Kill, ""},
		{"gate input unanswered", 0.9, 0.9, map[string]float64{"tarpit": 0.8}, rubric.Build, ""},
		{"at confidence floor", 0.9, 0.45, calm, rubric.Build, ""},
		{"under confidence floor", 0.9, 0.4499, calm, rubric.Uncertain, "build"},
	}
	for _, c := range cases {
		got, err := Decide(v, Aggregate{Composite: c.composite, Confidence: c.conf, Values: c.values})
		if err != nil || got.Label != c.want {
			t.Errorf("%s: verdict = %q (%v), want %q", c.name, got.Label, err, c.want)
		}
		if c.reason != "" && !contains([]string{got.Reason}, c.reason) && !containsSub(got.Reason, c.reason) {
			t.Errorf("%s: reason = %q, want containing %q", c.name, got.Reason, c.reason)
		}
	}
}
