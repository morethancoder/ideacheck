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

	"github.com/morethancoder/ideacheck/judge"
)

// Reader is the slice of configs.Files this package needs.
type Reader interface {
	Read(name string) ([]byte, error)
}

const (
	systemFile     = "judge_system.md"
	structuredFile = "question_structured.tmpl"
	logprobFile    = "question_logprob.tmpl"
	explainSysFile = "explain_system.md"
	explainFile    = "explain.tmpl"
	extractSysFile = "extract_system.md"
	extractFile    = "extract.tmpl"
	researchSys    = "research_system.md"
	researchFile   = "research.tmpl"
	planSys        = "research_plan_system.md"
	planFile       = "research_plan.tmpl"
	digestSys      = "research_digest_system.md"
	digestFile     = "research_digest.tmpl"
)

// Set is every prompt the model-backed judges use.
type Set struct {
	System        string
	ExplainSystem string // system prompt for the plain-language summary
	ExtractSystem string // system prompt for reading fields out of the document
	structured    *template.Template
	logprob       *template.Template
	explain       *template.Template
	extract       *template.Template
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
	if s.explain, err = parse(r, dir, explainFile); err != nil {
		return nil, err
	}
	ext, err := r.Read(path.Join(dir, extractSysFile))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", extractSysFile, err)
	}
	s.ExtractSystem = string(bytes.TrimSpace(ext))
	s.extract, err = parse(r, dir, extractFile)
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
// Where a value turns into "strength" or "probably true" is the template's
// business, so the bands live in explain.tmpl, not here.
type Finding struct {
	Question string // the question's instructions
	Reading  string // a score or choice answer in the rubric's own words
	// Noul marks a statement answered with a probability: Probability.
	Noul        bool
	Probability float64
	// Weighted marks an answer that counts toward the composite; Good is then
	// its value with polarity applied (1 is always good news).
	Weighted bool
	Good     float64
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

// Topic is one thing the research call is asked to look up. Hits are the
// search results fetched for it, for the digest call only.
type Topic struct {
	ID, LookFor string
	Hits        []Hit
}

// Hit is one search result as the digest call reads it; Text is the page
// boiled down, empty when the page was not read.
type Hit struct{ Title, URL, Snippet, Text string }

// Research loads and renders the research prompts. They are read on demand
// rather than in Load: a prompts directory written before research existed
// still loads, and only the research stage notices what it lacks.
func Research(r Reader, dir string, state judge.State, topics []Topic, maxFindings int) (system, user string, err error) {
	return pair(r, dir, researchSys, researchFile, state, map[string]any{"Topics": topics, "MaxFindings": maxFindings})
}

// ResearchPlan renders the call that names the searches ideacheck will run.
func ResearchPlan(r Reader, dir string, state judge.State, topics []Topic, perTopic int) (system, user string, err error) {
	return pair(r, dir, planSys, planFile, state, map[string]any{"Topics": topics, "PerTopic": perTopic})
}

// ResearchDigest renders the call that turns fetched results into findings.
func ResearchDigest(r Reader, dir string, state judge.State, topics []Topic, maxFindings int) (system, user string, err error) {
	return pair(r, dir, digestSys, digestFile, state, map[string]any{"Topics": topics, "MaxFindings": maxFindings})
}

// pair loads a system prompt and renders its template over the state plus data.
func pair(r Reader, dir, sysFile, tmplFile string, state judge.State, data map[string]any) (system, user string, err error) {
	sys, err := r.Read(path.Join(dir, sysFile))
	if err != nil {
		return "", "", fmt.Errorf("prompt %s: %w", sysFile, err)
	}
	t, err := parse(r, dir, tmplFile)
	if err != nil {
		return "", "", err
	}
	js, err := StateJSON(state)
	if err != nil {
		return "", "", err
	}
	data["StateJSON"] = js
	user, err = render(t, tmplFile, data)
	return string(bytes.TrimSpace(sys)), user, err
}

// Field is one intake field the extraction call is asked to fill: the name the
// reply must use, and what it means, both from the fields catalogue.
type Field struct{ Name, Description string }

// Extract renders the document the extraction call reads. fields are the intake
// fields still empty — the only ones worth asking for.
func (s *Set) Extract(idea, context string, fields []Field) (string, error) {
	return render(s.extract, extractFile, map[string]any{"Idea": idea, "Context": context, "Fields": fields})
}

func render(t *template.Template, name string, data any) (string, error) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}
