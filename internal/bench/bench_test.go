package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
	"github.com/morethancoder/ideacheck/internal/pipeline"
)

func catalog(t *testing.T) map[string]judge.Question {
	t.Helper()
	c, err := Catalog(config.NewFiles(""), "rubrics")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The dataset we ship must load: every label names a real question with a legal value.
func TestSeedDatasetIsValid(t *testing.T) {
	ideas, err := Load(filepath.Join("..", "..", "bench", "ideas.jsonl"), catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	var vague int
	for _, i := range ideas {
		if len(i.ExpectMissing) > 0 {
			vague++
		}
	}
	if len(ideas) != 25 || vague < 3 {
		t.Errorf("ideas = %d, vague = %d; want 25 with 3-5 vague", len(ideas), vague)
	}
}

func TestLoadRejectsLabelsThatCouldNeverMatch(t *testing.T) {
	for name, line := range map[string]string{
		"unknown question":   `{"id":"a","idea":"x","labels":{"tarpitt":1}}`,
		"level out of range": `{"id":"a","idea":"x","labels":{"problem_acuity":4}}`,
		"noul not 0/1":       `{"id":"a","idea":"x","labels":{"tarpit":0.5}}`,
		"bad choice":         `{"id":"a","idea":"x","labels":{"idea_type":"startup"}}`,
		"not a gap":          `{"id":"a","idea":"x","expect_missing":["tarpit"]}`,
		"unknown field":      `{"id":"a","idea":"x","label":{}}`,
		"duplicate id":       `{"id":"a","idea":"x"}` + "\n" + `{"id":"a","idea":"y"}`,
	} {
		p := filepath.Join(t.TempDir(), "d.jsonl")
		_ = os.WriteFile(p, []byte(line), 0o644)
		if _, err := Load(p, catalog(t)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func TestMeasureScoresAgainstLabels(t *testing.T) {
	ideas := []Idea{
		{ID: "i1", Labels: map[string]any{"tarpit": 1.0, "sisp": 0.0, "problem_acuity": 3.0, "idea_type": "business"}},
		{ID: "vague", ExpectMissing: []string{"has_problem", "has_why_now"}},
	}
	res := func(composite float64, ms int64) *pipeline.Result {
		return &pipeline.Result{Status: pipeline.StatusOK, Verdict: "explore", Composite: composite, Model: "m", CostEstimateUSD: 0.01,
			Timing: pipeline.Timing{TotalMS: ms},
			Answers: []judge.Answer{
				{ID: "tarpit", Kind: judge.Noul, Noul: 0.8},           // correct, brier 0.04
				{ID: "sisp", Kind: judge.Noul, Noul: 0.6},             // wrong,   brier 0.36
				{ID: "problem_acuity", Kind: judge.Score, Score: 2.5}, // |2.5-3| = 0.5
				{ID: "idea_type", Kind: judge.Choice, Choice: "business"},
				{ID: "clarity", Kind: judge.Score, Score: 1}, // unlabelled: ignored
				{ID: "why_now", Kind: judge.Score, Err: "timeout"},
			}}
	}
	runs := []Run{
		{IdeaID: "i1", Result: res(0.50, 100)}, {IdeaID: "i1", Repeat: 1, Result: res(0.60, 300)},
		{IdeaID: "vague", Result: &pipeline.Result{Status: pipeline.StatusOK, Verdict: "park", Missing: []pipeline.Missing{{ID: "has_problem"}}, Timing: pipeline.Timing{TotalMS: 200}}},
		{IdeaID: "i1", Repeat: 2, Err: "boom"},
	}
	m := Measure("x", runs, ideas, catalog(t))
	want := Metrics{Accuracy: ptr(2.0 / 3), ScoreMAE: ptr(0.5), Brier: ptr(0.2), SelfConsistency: ptr(0.05), GapRecall: ptr(0.5)}
	for name, pair := range map[string][2]*float64{"accuracy": {m.Accuracy, want.Accuracy}, "mae": {m.ScoreMAE, want.ScoreMAE},
		"brier": {m.Brier, want.Brier}, "self": {m.SelfConsistency, want.SelfConsistency}, "recall": {m.GapRecall, want.GapRecall}} {
		if pair[0] == nil || !near(*pair[0], *pair[1]) {
			t.Errorf("%s = %v, want %v", name, num(pair[0], "%v"), *pair[1])
		}
	}
	if m.Failed != 1 || m.Runs != 4 || m.LatencyP50MS != 200 || m.LatencyP95MS != 300 || !near(m.CostUSD, 0.02) || m.OrderBias != nil {
		t.Errorf("metrics = %+v", m)
	}
}

func TestDryRunEndToEnd(t *testing.T) {
	files := config.NewFiles("")
	cfg, err := config.Load(files, config.LoadOptions{Environ: func() []string { return nil }, Overrides: map[string]any{"backend": "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	ideas, err := Load(filepath.Join("..", "..", "bench", "ideas.jsonl"), catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := func(seed int64) Checker {
		return &pipeline.Engine{Config: cfg, Files: files, Judge: &mock.Judge{Seed: seed}}
	}
	r := Runner{Engines: map[string]Checker{"a": engine(1), "b": engine(1), "c": engine(2)}, Order: []string{"a", "b", "c"}, Repeats: 2, Parallel: 4}
	rep := r.Execute(context.Background(), "bench/ideas.jsonl", ideas, catalog(t), time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))

	if len(rep.Runs) != 25*2*3 || len(rep.Backends) != 3 || len(rep.Agreement) != 3 || rep.Backends[0].Failed != 0 {
		t.Fatalf("runs=%d backends=%d agreement=%d failed=%d", len(rep.Runs), len(rep.Backends), len(rep.Agreement), rep.Backends[0].Failed)
	}
	if sc := rep.Backends[0].SelfConsistency; sc == nil || *sc != 0 {
		t.Errorf("a deterministic backend must have zero spread across repeats, got %v", num(sc, "%v"))
	}
	same, diff := rep.Agreement[0], rep.Agreement[1] // a-b identical seeds; a-c different seeds
	if !near(same.Kappa, 1) || !near(same.Spearman, 1) || same.Ideas != 25 {
		t.Errorf("identical backends must agree perfectly: %+v", same)
	}
	if diff.Kappa >= 0.99 {
		t.Errorf("different seeds should not agree perfectly: %+v", diff)
	}

	dir := t.TempDir()
	path, err := rep.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadReport(path)
	if err != nil || len(back.Runs) != len(rep.Runs) || back.Backends[0].Backend != "a" {
		t.Errorf("round trip: %v %+v", err, back.Backends)
	}
	csv, _ := os.ReadFile(strings.TrimSuffix(path, ".json") + ".csv")
	if lines := strings.Count(string(csv), "\n"); lines != 151 {
		t.Errorf("csv has %d lines, want header + 150 runs", lines)
	}
	if out := rep.Table(); !strings.Contains(out, "25 ideas × 2 repeats") || !strings.Contains(out, "a vs b") {
		t.Errorf("table:\n%s", out)
	}
	if cmp := Compare(back, rep); !strings.Contains(cmp, "+0.0000") || !strings.Contains(cmp, "accuracy") {
		t.Errorf("compare:\n%s", cmp)
	}
}
