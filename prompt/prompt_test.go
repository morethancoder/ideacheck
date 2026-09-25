package prompt

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/judge"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	state  = judge.State{"idea": map[string]any{"text": "An app", "problem": "slow payroll"}}
	choice = judge.Question{ID: "idea_type", Kind: judge.Choice, Instructions: "What kind of idea is this?",
		Options: map[string]string{"other": "None of the above", "business": "Makes money", "content": "Media"}}
	score = judge.Question{ID: "clarity", Kind: judge.Score, Instructions: "How clear?", Levels: []string{"Vague", "Clear"}}
	noul  = judge.Question{ID: "tarpit", Kind: judge.Noul, Instructions: "This is a tarpit idea."}
)

// Golden files pin the exact text the model sees; a prompt edit must be deliberate.
// Regenerate with: go test ./internal/prompt -update
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from golden file:\n%s", name, got)
	}
}

func TestRenderedPromptsMatchGolden(t *testing.T) {
	s, err := Load(config.NewFiles(""), "prompts")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.State(state)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "state.golden", st)
	for _, mode := range []string{"vote", "verbalized"} {
		got, err := s.Questions([]judge.Question{choice, score, noul}, mode)
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "questions_"+mode+".golden", got)
	}
	brief, err := s.Explain(Brief{State: state, Verdict: "park", Reason: "Tarpit fired",
		Findings: []Finding{
			{Question: "This is a tarpit idea.", Noul: true, Probability: 0.7, Weighted: true, Good: 0.3},
			{Question: "How clear?", Reading: "Clear", Weighted: true, Good: 1},
		},
		Missing: []string{"Who exactly is the first user?"}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "explain.golden", brief)
	lp, err := s.Logprob(state, choice, []Label{{"A", "content — Media"}, {"B", "other — None of the above"}, {"C", "business — Makes money"}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "logprob_choice.golden", lp)
	if !strings.HasSuffix(lp, "Answer:\n") && !strings.HasSuffix(lp, "Answer:") {
		t.Errorf("logprob prompt must end with the literal line `Answer:` so the next token is the label:\n%q", lp[len(lp)-30:])
	}
	lpNoul, _ := s.Logprob(state, noul, nil)
	if !strings.Contains(lpNoul, "Y) yes  N) no") {
		t.Errorf("noul logprob prompt:\n%s", lpNoul)
	}
}

// The explain brief puts values into words with bands kept in explain.tmpl;
// these pin the embedded bands at their edges.
func TestExplainBandsLiveInTheTemplate(t *testing.T) {
	s, _ := Load(config.NewFiles(""), "prompts")
	cases := []struct {
		f    Finding
		want string
	}{
		{Finding{Question: "q", Noul: true, Probability: 0.8, Weighted: true, Good: 0.6}, "[strength] q → clearly true"},
		{Finding{Question: "q", Noul: true, Probability: 0.6, Weighted: true, Good: 0.4}, "[weakness] q → probably true"},
		{Finding{Question: "q", Noul: true, Probability: 0.4, Weighted: true, Good: 0.5}, "[mixed] q → probably not true"},
		{Finding{Question: "q", Noul: true, Probability: 0.2}, "[context] q → clearly not true"},
		{Finding{Question: "q", Noul: true, Probability: 0.5}, "[context] q → unclear"},
		{Finding{Question: "q", Reading: "Clear"}, "[context] q → Clear"},
	}
	for _, c := range cases {
		got, err := s.Explain(Brief{State: state, Findings: []Finding{c.f}})
		if err != nil || !strings.Contains(got, "- "+c.want+"\n") {
			t.Errorf("%+v: want %q in\n%s (%v)", c.f, c.want, got, err)
		}
	}
}

// A noul's criteria must reach every backend, or the same question would mean
// one thing to Jev and another to a prompted model.
func TestNoulCriteriaReachEveryPrompt(t *testing.T) {
	s, _ := Load(config.NewFiles(""), "prompts")
	q := noul
	q.Criteria = &judge.NoulCriteria{Yes: "a known failure pattern", No: "a fresh angle"}
	lp, _ := s.Logprob(judge.State{"idea": "x"}, q, nil)
	if !strings.Contains(lp, "Y) yes: a known failure pattern\nN) no: a fresh angle") {
		t.Errorf("logprob prompt lacks the criteria:\n%s", lp)
	}
	st, _ := s.Questions([]judge.Question{q}, "verbalized")
	if !strings.Contains(st, "True means: a known failure pattern\nFalse means: a fresh angle") {
		t.Errorf("structured prompt lacks the criteria:\n%s", st)
	}
}

func TestVoteModeAsksForNoProbabilities(t *testing.T) {
	s, _ := Load(config.NewFiles(""), "prompts")
	vote, _ := s.Questions([]judge.Question{choice, noul}, "vote")
	if strings.Contains(vote, "probabilit") {
		t.Errorf("vote prompt must not request probabilities:\n%s", vote)
	}
	verbal, _ := s.Questions([]judge.Question{choice, noul}, "verbalized")
	if !strings.Contains(verbal, "probabilities") {
		t.Error("verbalized prompt must request probabilities")
	}
}

type mem map[string]string

func (m mem) Read(n string) ([]byte, error) {
	if s, ok := m[n]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

func TestLoadRejectsBrokenOverrides(t *testing.T) {
	base := mem{"p/judge_system.md": "sys", "p/question_logprob.tmpl": "x", "p/question_structured.tmpl": `{{define "state"}}s{{end}}{{define "questions"}}q{{end}}`,
		"p/explain_system.md": "exp", "p/explain.tmpl": "e",
		"p/extract_system.md": "ext", "p/extract.tmpl": "x"}
	if _, err := Load(base, "p"); err != nil {
		t.Fatalf("valid set: %v", err)
	}
	for name, body := range map[string]string{
		"p/question_structured.tmpl": `{{define "state"}}s{{end}}`, // user deleted a block
		"p/question_logprob.tmpl":    `{{ .Unclosed `,
		"p/explain.tmpl":             `{{ range }}`,
	} {
		broken := mem{}
		for k, v := range base {
			broken[k] = v
		}
		broken[name] = body
		if _, err := Load(broken, "p"); err == nil {
			t.Errorf("broken %s was accepted", name)
		}
	}
}
