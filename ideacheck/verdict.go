package ideacheck

import (
	"fmt"

	"github.com/morethancoder/ideacheck/rubric"
)

type Verdict struct {
	Label  string
	Reason string
}

// Decide applies composite thresholds, then the ordered gates as hard caps, then
// the confidence floor. No model call happens here.
func Decide(v rubric.Verdict, agg Aggregate) (Verdict, error) {
	out := fromThresholds(v.Thresholds, agg.Composite)
	for i := range v.Gates {
		g := &v.Gates[i]
		fires, err := g.Fires(agg.Values)
		if err != nil {
			return Verdict{}, err
		}
		if fires && worse(g.Max, out.Label) {
			out = Verdict{Label: g.Max, Reason: g.Reason}
		}
	}
	if agg.Confidence < v.MinConfidence {
		out = Verdict{
			Label:  rubric.Uncertain,
			Reason: fmt.Sprintf("Confidence %.2f is below the rubric's minimum %.2f (would be %q)", agg.Confidence, v.MinConfidence, out.Label),
		}
	}
	return out, nil
}

func fromThresholds(t rubric.Thresholds, composite float64) Verdict {
	steps := []struct {
		label string
		min   float64
	}{{rubric.Build, t.Build}, {rubric.Explore, t.Explore}, {rubric.Park, t.Park}}
	for _, s := range steps {
		if composite >= s.min {
			return Verdict{s.label, fmt.Sprintf("Composite %.2f meets the %s threshold %.2f", composite, s.label, s.min)}
		}
	}
	return Verdict{rubric.Kill, fmt.Sprintf("Composite %.2f is below the park threshold %.2f", composite, t.Park)}
}

// worse reports whether verdict a ranks strictly below b.
func worse(a, b string) bool {
	ra, _ := rubric.Rank(a)
	rb, _ := rubric.Rank(b)
	return ra < rb
}
