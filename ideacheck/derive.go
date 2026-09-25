package ideacheck

import (
	"fmt"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/rubric"
)

// MethodDerived is the method of an answer no model gave: a question's
// `derive:` rule settled it from what the check already knew.
const MethodDerived = "derived"

// known is what a check has settled by the time a question comes up; derive
// rules read it instead of asking the judge.
type known struct {
	in       Intake
	gaps     *rubric.Rubric
	answers  map[string]judge.Answer // gap answers, by question id
	research *ResearchReport         // nil = not researched
	earlier  map[string]judge.Answer // a follow-up's preflight answers that stand
}

// earlier is what a follow-up keeps of the check it follows: every answer the
// judge gave. The fields answered since are no longer asked about at all, and
// a derived answer is derived again from the intake as it now is.
func earlier(res *Result) map[string]judge.Answer {
	if res == nil {
		return nil
	}
	out := map[string]judge.Answer{}
	for _, a := range res.Answers {
		if !a.Failed() && a.Method != MethodDerived {
			out[a.ID] = a
		}
	}
	return out
}

// derive answers q by its rule; ok is false when there is no rule or the rule
// cannot decide, and the judge is asked as usual.
func derive(q judge.Question, k known) (judge.Answer, bool) {
	d := q.Derive
	switch d.Rule() {
	case "present":
		for _, f := range d.Present {
			if k.in.Has(f) {
				return derived(q, 1), true
			}
		}
		return derived(q, 0), true
	case "same_as":
		a, ok := k.answers[d.SameAs]
		if !ok || a.Failed() || a.Method == MethodDerived {
			return judge.Answer{}, false
		}
		return derived(q, a.Noul), true
	case "evidence":
		return fromEvidence(q, d.Evidence, k.research)
	case "zero_when_unstated":
		if k.in.Has(d.ZeroWhenUnstated) {
			return judge.Answer{}, false
		}
		for _, g := range k.gaps.Questions {
			if a, ok := k.answers[g.ID]; ok && g.Fills == d.ZeroWhenUnstated && !a.Failed() && a.Noul < k.gaps.Threshold {
				return derived(q, 0), true
			}
		}
	}
	return judge.Answer{}, false
}

// fromEvidence reads how the judge typed the findings of one topic: one with
// the relation is a yes; a topic searched with every finding typed otherwise —
// or none found — is a no. Untyped findings leave it to the judge.
func fromEvidence(q judge.Question, rule *judge.EvidenceRule, report *ResearchReport) (judge.Answer, bool) {
	if report == nil {
		return judge.Answer{}, false
	}
	untyped := false
	for _, f := range report.Findings {
		if f.Topic != rule.Topic {
			continue
		}
		if f.Relation == rule.Relation {
			return derived(q, 1), true
		}
		untyped = untyped || f.Relation == ""
	}
	if untyped {
		return judge.Answer{}, false
	}
	return derived(q, 0), true
}

func derived(q judge.Question, p float64) judge.Answer {
	return judge.Answer{ID: q.ID, Kind: q.Kind, Noul: p, Probabilities: map[string]float64{"yes": p, "no": 1 - p},
		Confidence: judge.NoulConfidence(p), Method: MethodDerived}
}

// derivable checks what derive rules name in other files: gap ids, intake
// fields, and relations the evidence rubric can answer.
func (e *Engine) derivable(rb, gaps *rubric.Rubric) error {
	var relations map[string]string
	if ev, err := rubric.Load(e.Files, e.Config.RubricsDir, rubric.EvidenceName); err == nil {
		relations = ev.Questions[0].Options
	}
	for _, q := range rb.Questions {
		d := q.Derive
		bad := ""
		switch d.Rule() {
		case "present":
			for _, f := range d.Present {
				if !KnownField(f) {
					bad = fmt.Sprintf("present names unknown intake field %q", f)
				}
			}
		case "same_as":
			if _, ok := gaps.Question(d.SameAs); !ok {
				bad = fmt.Sprintf("same_as names %q, which is not a question in %s", d.SameAs, rubric.GapsName)
			}
		case "evidence":
			if _, ok := relations[d.Evidence.Relation]; relations != nil && !ok {
				bad = fmt.Sprintf("evidence relation %q is not an option of %s", d.Evidence.Relation, rubric.EvidenceName)
			}
		case "zero_when_unstated":
			if !fillsSome(gaps, d.ZeroWhenUnstated) {
				bad = fmt.Sprintf("zero_when_unstated names %q, which no %s question fills", d.ZeroWhenUnstated, rubric.GapsName)
			}
		}
		if bad != "" {
			return fmt.Errorf("rubric %s: %s: derive %s", rb.Name, q.ID, bad)
		}
	}
	return nil
}

func fillsSome(gaps *rubric.Rubric, field string) bool {
	for _, g := range gaps.Questions {
		if g.Fills == field {
			return true
		}
	}
	return false
}

// announce shows answers the judge was not asked for — derived, or kept from
// an earlier run — in the live view, as the judge's are shown.
func announce(ch chan<- Event, stage string, qs []judge.Question, held map[int]judge.Answer) {
	for i, a := range held {
		if a.Failed() {
			continue
		}
		a := a
		ev := Event{Type: judge.EventAnswered, Stage: stage, Question: qs[i], Answer: &a}
		if v, ok := Normalize(qs[i], a); ok {
			ev.Value = &v
		}
		emit(ch, ev)
	}
}
