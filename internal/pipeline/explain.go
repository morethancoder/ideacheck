package pipeline

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/prompt"
	"github.com/morethancoder/ideacheck/internal/rubric"
)

// StageExplain is the summary written after scoring.
const StageExplain = "explain"

// summaryQuestion stands in for the summary in live events; it is not asked.
var summaryQuestion = judge.Question{ID: "summary", Instructions: "Plain-language summary of the verdict"}

// explain asks the backend for the plain-language summary. It never fails the
// check: without a summary the scores still stand, so a failure is a warning.
// It returns the call's usage as an answer so the cost includes it.
func (e *Engine) explain(ctx context.Context, res *Result, state judge.State, rb *rubric.Rubric, answers []judge.Answer, o Options) *judge.Answer {
	narrator, ok := e.writer().(judge.Narrator)
	if !ok || !e.Config.Explain || res.Status != StatusOK {
		return nil
	}
	prompts, err := prompt.Load(e.Files, e.Config.PromptsDir)
	if err != nil {
		res.Warnings = append(res.Warnings, "no summary: "+err.Error())
		return nil
	}
	brief, err := prompts.Explain(prompt.Brief{State: state, Verdict: res.Verdict, Reason: res.VerdictReason,
		Findings: findings(rb.Questions, answers), Missing: asks(res.Missing)})
	if err != nil {
		res.Warnings = append(res.Warnings, "no summary: "+err.Error())
		return nil
	}
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageExplain, Question: summaryQuestion})
	ctx, cancel := context.WithTimeout(ctx, e.Config.Timeouts.Batch)
	defer cancel()
	start := e.now()
	n, err := narrator.Narrate(ctx, prompts.ExplainSystem, brief)
	a := judge.Answer{ID: summaryQuestion.ID, Model: n.Model, LatencyMS: e.now().Sub(start).Milliseconds(),
		TokensIn: n.TokensIn, TokensOut: n.TokensOut, TokensCached: n.TokensCached, CostUSD: n.CostUSD}
	if err != nil {
		a.Err = err.Error()
		res.Warnings = append(res.Warnings, "no summary: "+err.Error())
		emit(o.Events, Event{Type: judge.EventFailed, Stage: StageExplain, Question: summaryQuestion, Answer: &a})
		return &a
	}
	res.Summary = n.Text
	emit(o.Events, Event{Type: judge.EventAnswered, Stage: StageExplain, Question: summaryQuestion, Answer: &a})
	return &a
}

func emit(ch chan<- Event, e Event) {
	if ch != nil {
		ch <- e
	}
}

func asks(missing []Missing) []string {
	out := make([]string, len(missing))
	for i, m := range missing {
		out[i] = m.Ask
	}
	return out
}

// findings turns the rubric's answers into words, most influential first:
// weighted questions ordered by how far they pull the composite, then the
// informational ones.
func findings(qs []judge.Question, answers []judge.Answer) []prompt.Finding {
	type ranked struct {
		prompt.Finding
		pull float64
	}
	var rs []ranked
	for i, q := range qs {
		a := answers[i]
		if a.Failed() {
			continue
		}
		f := prompt.Finding{Effect: "context", Question: q.Instructions, Reading: reading(q, a)}
		pull := -1.0 // informational questions sort last
		if v, ok := Normalize(q, a); ok && q.Polarity != 0 && q.Weight > 0 {
			good := v
			if q.Polarity < 0 {
				good = 1 - v
			}
			f.Effect = effect(good)
			pull = q.Weight * math.Abs(good-0.5)
		}
		rs = append(rs, ranked{f, pull})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].pull > rs[j].pull })
	out := make([]prompt.Finding, len(rs))
	for i, r := range rs {
		out[i] = r.Finding
	}
	return out
}

// effect reads a polarity-adjusted value (1 is always good).
func effect(good float64) string {
	switch {
	case good >= 0.6:
		return "strength"
	case good <= 0.4:
		return "weakness"
	}
	return "mixed"
}

// reading is the answer in the rubric's own words.
func reading(q judge.Question, a judge.Answer) string {
	switch q.Kind {
	case judge.Score:
		if len(q.Levels) == 0 {
			return ""
		}
		i := min(max(int(math.Round(a.Score)), 0), len(q.Levels)-1)
		return q.Levels[i]
	case judge.Choice:
		if d, ok := q.Options[a.Choice]; ok {
			return fmt.Sprintf("%s (%s)", a.Choice, d)
		}
		return a.Choice
	}
	switch p := a.Noul; {
	case p >= 0.8:
		return "clearly true"
	case p >= 0.6:
		return "probably true"
	case p > 0.4:
		return "unclear"
	case p > 0.2:
		return "probably not true"
	}
	return "clearly not true"
}
