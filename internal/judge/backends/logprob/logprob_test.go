package logprob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/openai"
	"github.com/morethancoder/ideacheck/internal/prompt"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func tok(pairs ...any) openai.TokenLogprob {
	var t openai.TokenLogprob
	for i := 0; i < len(pairs); i += 2 {
		t.TopLogprobs = append(t.TopLogprobs, struct {
			Token   string  `json:"token"`
			Logprob float64 `json:"logprob"`
		}{pairs[i].(string), math.Log(pairs[i+1].(float64))})
	}
	return t
}

// Real Ollama output splits one label across " N", "N" — all variants must count.
func TestDistributionSumsTokenVariantsAndIgnoresNonLabels(t *testing.T) {
	first := tok(" N", 0.30, " Y", 0.20, "N", 0.10, "\n", 0.25, "y", 0.05, "Maybe", 0.10)
	got, err := distribution(first, map[string]string{"Y": "yes", "N": "no"})
	if err != nil {
		t.Fatal(err)
	}
	// label mass: no = 0.40, yes = 0.25 → renormalized over 0.65
	if !near(got["no"], 0.40/0.65) || !near(got["yes"], 0.25/0.65) {
		t.Errorf("distribution = %v", got)
	}
	if _, err := distribution(tok("Maybe", 0.9, "\n", 0.1), map[string]string{"Y": "yes", "N": "no"}); err == nil {
		t.Error("no label among the top tokens must be an error, not a 50/50 answer")
	}
}

func TestLayoutShuffleIsReproducibleAndKeepsTheMapping(t *testing.T) {
	q := judge.Question{ID: "t", Kind: judge.Choice, Options: map[string]string{"a": "A", "b": "B", "c": "C", "d": "D", "other": "O"}}
	natural, _ := layoutFor(q, 0)
	if natural.labels[0].Label != "A" || natural.outcome["A"] != "a" || natural.outcome["E"] != "other" {
		t.Errorf("run 0 must be the natural order: %+v", natural)
	}
	s1, _ := layoutFor(q, 1)
	s1again, _ := layoutFor(q, 1)
	if fmt.Sprint(s1) != fmt.Sprint(s1again) {
		t.Error("a shuffled layout must be reproducible for (question, run)")
	}
	if fmt.Sprint(s1.outcome) == fmt.Sprint(natural.outcome) {
		t.Error("run 1 should present a different order than run 0")
	}
	seen := map[string]bool{}
	for _, o := range s1.outcome {
		seen[o] = true
	}
	if len(seen) != 5 {
		t.Errorf("shuffle lost options: %v", s1.outcome)
	}
	score := judge.Question{ID: "s", Kind: judge.Score, Levels: []string{"low", "mid", "high"}}
	sl, _ := layoutFor(score, 1)
	for _, l := range sl.labels {
		if sl.outcome[l.Label] != l.Label {
			t.Errorf("a score label must always be its own level index: %+v", sl)
		}
	}
}

// A model that always says "A" is pure order bias; averaging shuffled runs must
// spread that mass across whichever options landed in slot A.
func TestShuffleRunsAverageAwayPositionBias(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		io.WriteString(w, `{"model":"qwen3:8b","choices":[{"message":{"role":"assistant","content":"A"},"logprobs":{"content":[{"token":"A","logprob":-0.01,"top_logprobs":[{"token":"A","logprob":-0.01},{"token":" the","logprob":-5}]}]}}],"usage":{"prompt_tokens":40,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	prompts, err := prompt.Load(config.NewFiles(""), "prompts")
	if err != nil {
		t.Fatal(err)
	}
	j := &Judge{Client: &openai.Client{BaseURL: srv.URL}, Prompts: prompts, Model: "qwen3:8b", TopLogprobs: 20, ShuffleRuns: 2}
	q := judge.Question{ID: "idea_type", Kind: judge.Choice, Instructions: "Kind?", Options: map[string]string{"business": "b", "content": "c", "creative": "x", "research": "r", "other": "o"}}
	a, err := j.Evaluate(context.Background(), judge.State{"idea": "x"}, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || a.TokensIn != 80 || a.Method != "logprob" || a.Model != "qwen3:8b" {
		t.Fatalf("calls=%d answer=%+v", len(bodies), a)
	}
	first, _ := layoutFor(q, 0)
	second, _ := layoutFor(q, 1)
	if !near(a.Probabilities[first.outcome["A"]], 0.5) || !near(a.Probabilities[second.outcome["A"]], 0.5) {
		t.Errorf("probabilities = %v, want 0.5 on each run's slot-A option", a.Probabilities)
	}
	// Pure position bias moves 1.0 of mass off one option and onto another: 2/5 per option.
	if !near(a.OrderBias, 0.4) {
		t.Errorf("order bias = %v, want 0.4", a.OrderBias)
	}
	b := bodies[0]
	if b["logprobs"] != true || b["top_logprobs"] != float64(20) || b["temperature"] != float64(0) {
		t.Errorf("request = %v", b)
	}
	// A thinking model must answer at once, and Ollama returns no logprobs for
	// one when max_tokens is 1 — either slip makes every question fail.
	if b["reasoning_effort"] != "none" || b["max_tokens"] != float64(2) {
		t.Errorf("reasoning_effort = %v, max_tokens = %v; want none, 2", b["reasoning_effort"], b["max_tokens"])
	}
	j.Effort = EffortUnset
	if _, err := j.Evaluate(context.Background(), judge.State{"idea": "x"}, q); err != nil {
		t.Fatal(err)
	}
	if _, sent := bodies[len(bodies)-1]["reasoning_effort"]; sent {
		t.Error("EffortUnset must leave reasoning_effort out for servers that reject it")
	}
	user := b["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.HasSuffix(strings.TrimRight(user, "\n"), "Answer:") || !strings.Contains(user, "A) business") {
		t.Errorf("prompt must list labelled options and end with `Answer:`:\n%s", user)
	}
}

func TestNoulAndScoreAnswers(t *testing.T) {
	a := finish(judge.Question{Kind: judge.Noul}, judge.Answer{}, map[string]float64{"yes": 0.9, "no": 0.1})
	if a.Noul != 0.9 || math.Abs(a.Confidence-0.531004) > 1e-6 {
		t.Errorf("noul = %+v", a)
	}
	a = finish(judge.Question{Kind: judge.Score}, judge.Answer{}, map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7})
	if !near(a.Score, 1.6) {
		t.Errorf("score = %v, want Σ i·p_i = 1.6", a.Score)
	}
}
