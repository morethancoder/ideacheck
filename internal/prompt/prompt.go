// Package prompt loads the prompt files from config and renders them. No prompt
// text lives in Go: editing configs/prompts/* changes what the model sees.
package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"text/template"

	"github.com/morethancoder/ideacheck/internal/judge"
)

// Reader is the slice of config.Files this package needs.
type Reader interface {
	Read(name string) ([]byte, error)
}

const (
	systemFile     = "judge_system.md"
	structuredFile = "question_structured.tmpl"
	logprobFile    = "question_logprob.tmpl"
	explainSysFile = "explain_system.md"
	explainFile    = "explain.tmpl"
)

// Set is every prompt the model-backed judges use.
type Set struct {
	System        string
	ExplainSystem string // system prompt for the plain-language summary
	structured    *template.Template
	logprob       *template.Template
	explain       *template.Template
}

func Load(r Reader, dir string) (*Set, error) {
	sys, err := r.Read(path.Join(dir, systemFile))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", systemFile, err)
	}
	s := &Set{System: string(bytes.TrimSpace(sys))}
	if s.structured, err = parse(r, dir, structuredFile); err != nil {
		return nil, err
	}
	for _, block := range []string{"state", "questions"} {
		if s.structured.Lookup(block) == nil {
			return nil, fmt.Errorf("prompt %s: missing {{define %q}} block", structuredFile, block)
		}
	}
	if s.logprob, err = parse(r, dir, logprobFile); err != nil {
		return nil, err
	}
	exp, err := r.Read(path.Join(dir, explainSysFile))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", explainSysFile, err)
	}
	s.ExplainSystem = string(bytes.TrimSpace(exp))
	s.explain, err = parse(r, dir, explainFile)
	return s, err
}

func parse(r Reader, dir, name string) (*template.Template, error) {
	b, err := r.Read(path.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", name, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", name, err)
	}
	return t, nil
}

// Option is one choice option in a fixed, deterministic order.
type Option struct{ Key, Description string }

// QuestionView is what templates see: the question plus its options sorted by key
// (map order would otherwise change the prompt — and the cache key — every run).
type QuestionView struct {
	judge.Question
	OptionList []Option
}

func view(q judge.Question) QuestionView {
	v := QuestionView{Question: q}
	for k, d := range q.Options {
		v.OptionList = append(v.OptionList, Option{k, d})
	}
	sort.Slice(v.OptionList, func(i, j int) bool { return v.OptionList[i].Key < v.OptionList[j].Key })
	return v
}

// StateJSON serializes state deterministically (encoding/json sorts map keys).
func StateJSON(state judge.State) (string, error) {
	b, err := json.MarshalIndent(state, "", "  ")
	return string(b), err
}

// State renders the shared, cacheable state block.
func (s *Set) State(state judge.State) (string, error) {
	js, err := StateJSON(state)
	if err != nil {
		return "", err
	}
	return render(s.structured, "state", map[string]any{"StateJSON": js})
}

// Questions renders the user turn for one or more questions. mode is "vote" or "verbalized".
func (s *Set) Questions(qs []judge.Question, mode string) (string, error) {
	views := make([]QuestionView, len(qs))
	for i, q := range qs {
		views[i] = view(q)
	}
	return render(s.structured, "questions", map[string]any{"Questions": views, "Mode": mode})
}

// Label is one single-token answer label and the text it stands for.
type Label struct{ Label, Text string }

// Logprob renders ONE question so that the next token is the answer label.
func (s *Set) Logprob(state judge.State, q judge.Question, labels []Label) (string, error) {
	js, err := StateJSON(state)
	if err != nil {
		return "", err
	}
	return render(s.logprob, logprobFile, map[string]any{"StateJSON": js, "Question": q, "Labels": labels})
}

// Finding is one scored question as the summary brief shows it.
type Finding struct {
	Effect   string // strength | weakness | mixed | context
	Question string // the question's instructions
	Reading  string // the answer in words: a level, an option, or how likely a statement is
}

// Brief is everything the summary writer sees.
type Brief struct {
	State    judge.State
	Verdict  string
	Reason   string
	Findings []Finding
	Missing  []string // follow-up questions nobody answered
}

// Explain renders the summary brief.
func (s *Set) Explain(b Brief) (string, error) {
	js, err := StateJSON(b.State)
	if err != nil {
		return "", err
	}
	return render(s.explain, explainFile, map[string]any{"StateJSON": js, "Verdict": b.Verdict, "Reason": b.Reason, "Findings": b.Findings, "Missing": b.Missing})
}

func render(t *template.Template, name string, data any) (string, error) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}
