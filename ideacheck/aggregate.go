package ideacheck

import (
	"sort"

	"github.com/morethancoder/ideacheck/judge"
)

// Contribution is one question's pull on the composite. Value is the
// polarity-adjusted "goodness" in [0,1]: 1 is always good, 0 always bad.
type Contribution struct {
	ID     string  `json:"id"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
}

type Aggregate struct {
	Composite  float64
	Confidence float64
	Values     map[string]float64 // question id → normalized value, before polarity; gates read this
	Strengths  []Contribution
	Risks      []Contribution
	Answered   float64 // sum of weights that were answered
	Failed     []string
}

const topN = 3

// Normalize maps an answer to [0,1]; ok is false when the answer cannot be valued
// (failed, or a choice the rubric gives no value for).
func Normalize(q judge.Question, a judge.Answer) (float64, bool) {
	if a.Failed() {
		return 0, false
	}
	switch q.Kind {
	case judge.Noul:
		return clamp01(a.Noul), true
	case judge.Score:
		return clamp01(a.Score / float64(len(q.Levels)-1)), true
	}
	v, ok := q.Values[a.Choice]
	return v, ok
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Combine computes the weighted composite in ordinary code; the model is never
// asked for a score. Unanswered questions are excluded and the weights of the
// answered ones renormalize by construction (we divide by their sum).
//
// Confidence averages choice and score answers only. A noul is already a
// probability: 0.25 is a clear "probably not", not a doubtful answer, and Jev
// sends no confidence for one (docs.typesafe.ai/confidence). Counting an
// entropy of it would call every calibrated middle value uncertain. A rubric
// of nouls alone falls back to that entropy, since it has nothing else.
func Combine(qs []judge.Question, answers []judge.Answer) Aggregate {
	agg := Aggregate{Values: map[string]float64{}}
	var sum, confSum, confWeight, noulConfSum float64
	var all []Contribution
	for i, q := range qs {
		v, ok := Normalize(q, answers[i])
		if !ok {
			if answers[i].Failed() {
				agg.Failed = append(agg.Failed, q.ID)
			}
			continue
		}
		agg.Values[q.ID] = v
		if q.Weight <= 0 || q.Polarity == 0 {
			continue
		}
		if q.Polarity < 0 {
			v = 1 - v
		}
		sum += v * q.Weight
		if q.Kind == judge.Noul {
			noulConfSum += answers[i].Confidence * q.Weight
		} else {
			confSum += answers[i].Confidence * q.Weight
			confWeight += q.Weight
		}
		agg.Answered += q.Weight
		all = append(all, Contribution{ID: q.ID, Value: v, Weight: q.Weight})
	}
	if agg.Answered > 0 {
		agg.Composite = sum / agg.Answered
		agg.Confidence = noulConfSum / agg.Answered
		if confWeight > 0 {
			agg.Confidence = confSum / confWeight
		}
	}
	agg.Strengths, agg.Risks = split(all)
	return agg
}

// split ranks contributions by how hard they pull the composite away from the
// midpoint: weight × distance from 0.5, in each direction.
func split(all []Contribution) (strengths, risks []Contribution) {
	for _, c := range all {
		if c.Value >= 0.5 {
			strengths = append(strengths, c)
		} else {
			risks = append(risks, c)
		}
	}
	return top(strengths), top(risks)
}

func top(cs []Contribution) []Contribution {
	pull := func(c Contribution) float64 {
		d := c.Value - 0.5
		if d < 0 {
			d = -d
		}
		return d * c.Weight
	}
	sort.SliceStable(cs, func(i, j int) bool { return pull(cs[i]) > pull(cs[j]) })
	if len(cs) > topN {
		cs = cs[:topN]
	}
	return cs
}
