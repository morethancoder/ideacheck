package tui

import (
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/pipeline"
)

func TestBarRoundsToTheNearestCell(t *testing.T) {
	for value, filled := range map[float64]int{0: 0, 0.04: 0, 0.05: 1, 0.5: 6, 1: 12, 1.7: 12, -1: 0} {
		if got := strings.Count(Bar(value), "█"); got != filled || len([]rune(Bar(value))) != barWidth {
			t.Errorf("Bar(%v) = %q, want %d filled of %d", value, Bar(value), filled, barWidth)
		}
	}
}

func TestLiveRowsFillInAsAnswersLand(t *testing.T) {
	q := judge.Question{ID: "sisp", Kind: judge.Noul, Polarity: -1}
	m := newLive("mock · mock", nil)
	m = m.apply(pipeline.Event{Type: judge.EventStarted, Stage: pipeline.StageScore, Question: q})
	if len(m.rows) != 1 || m.rows[0].answer != nil {
		t.Fatalf("after started: %+v", m.rows)
	}
	v := 0.9
	m = m.apply(pipeline.Event{Type: judge.EventAnswered, Stage: pipeline.StageScore, Question: q, Answer: &judge.Answer{Noul: 0.9, Confidence: 0.53}, Value: &v})
	if len(m.rows) != 1 {
		t.Fatalf("answer must fill the existing row, not add one: %+v", m.rows)
	}
	view := m.View()
	for _, want := range []string{"sisp", "0.90", "53%", "↓", "Scoring"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	failed := m.apply(pipeline.Event{Question: judge.Question{ID: "x"}, Answer: &judge.Answer{Err: "timeout"}})
	if !strings.Contains(failed.View(), "timeout") {
		t.Error("a failed question must show its error")
	}
}

func TestCalibrationNotePerMethod(t *testing.T) {
	for method, want := range map[string]string{
		"vote:k=5": "empirical from 5 samples", "logprob": "raw logits, not calibrated",
		"jev": "vendor-calibrated", "whatever": "calibration unknown",
	} {
		if got := CalibrationNote(method); got != want {
			t.Errorf("CalibrationNote(%q) = %q, want %q", method, got, want)
		}
	}
}

func TestRenderResult(t *testing.T) {
	ok := RenderResult(&pipeline.Result{
		Status: pipeline.StatusOK, Verdict: "explore", VerdictReason: "Crowded space", Composite: 0.62, Method: "vote:k=5",
		TopRisks: []pipeline.Contribution{{ID: "differentiating_insight", Value: 0.22, Weight: 1.5}},
		Rubric:   &pipeline.RubricRef{Name: "business"},
	})
	for _, want := range []string{"EXPLORE", "0.62", "Crowded space", "differentiating_insight", "empirical from 5 samples", "not a success predictor"} {
		if !strings.Contains(ok, want) {
			t.Errorf("result missing %q:\n%s", want, ok)
		}
	}
	needs := RenderResult(&pipeline.Result{Status: pipeline.StatusNeedsInput, Missing: []pipeline.Missing{{Ask: "What changed recently?", Probability: 0.21}}})
	if !strings.Contains(needs, "NEEDS INPUT") || !strings.Contains(needs, "What changed recently?") {
		t.Errorf("needs_input view:\n%s", needs)
	}
}

func TestApplyRepliesSkipsBlanks(t *testing.T) {
	missing := []pipeline.Missing{{Fills: "why_now"}, {Fills: "profile.background"}, {Fills: "audience"}}
	got := ApplyReplies(pipeline.Intake{Idea: "x"}, missing, []string{" new API ", "10y payroll", "   "})
	if got.Fields["why_now"] != "new API" || got.Profile["background"] != "10y payroll" {
		t.Errorf("replies not stored: %+v", got)
	}
	if _, ok := got.Fields["audience"]; ok {
		t.Error("a blank reply must not create a field")
	}
}
