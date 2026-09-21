package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/prompt"
	"github.com/morethancoder/ideacheck/internal/rubric"
)

const (
	// StageResearch is the web lookup and the typing of what it found.
	StageResearch = "research"
	// ResearchFile lists what is looked up.
	ResearchFile = "research.yaml"
	// evidenceKey is the state the findings are read under.
	evidenceKey = "evidence"
	findingKey  = "finding"
	ideaKey     = "idea"
)

// researchQuestion stands in for the lookup in live events; it is not asked.
var researchQuestion = judge.Question{ID: "research", Instructions: "Look the idea up on the web"}

// Topics is research.yaml: what the writer looks up before an idea is scored.
type Topics struct {
	MaxFindings int     `yaml:"max_findings"`
	Topics      []Topic `yaml:"topics"`
}

// Topic is one thing worth looking up. Covers names the intake field the
// findings answer, so nobody is asked what a search already settled.
type Topic struct {
	ID      string `yaml:"id"`
	LookFor string `yaml:"look_for"`
	Covers  string `yaml:"covers"`
	// Queries are the searches run when no writer names better ones.
	Queries []string `yaml:"queries"`
}

// LoadTopics reads and checks research.yaml.
func LoadTopics(r interface{ Read(string) ([]byte, error) }) (*Topics, []byte, error) {
	b, err := r.Read(ResearchFile)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", ResearchFile, err)
	}
	var t Topics
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", ResearchFile, err)
	}
	if len(t.Topics) == 0 || t.MaxFindings < 1 {
		return nil, nil, fmt.Errorf("%s: needs at least one topic and max_findings >= 1", ResearchFile)
	}
	seen := map[string]bool{}
	for _, topic := range t.Topics {
		switch {
		case topic.ID == "" || topic.LookFor == "":
			return nil, nil, fmt.Errorf("%s: every topic needs id and look_for", ResearchFile)
		case seen[topic.ID]:
			return nil, nil, fmt.Errorf("%s: duplicate topic %q", ResearchFile, topic.ID)
		case topic.Covers != "" && !contains(ideaFields, topic.Covers):
			return nil, nil, fmt.Errorf("%s: %s covers unknown idea field %q", ResearchFile, topic.ID, topic.Covers)
		}
		seen[topic.ID] = true
	}
	return &t, b, nil
}

func (t *Topics) ids() []string {
	out := make([]string, len(t.Topics))
	for i, topic := range t.Topics {
		out[i] = topic.ID
	}
	return out
}

// covers are the intake fields research may answer.
func (t *Topics) covers() []string {
	var out []string
	for _, topic := range t.Topics {
		if topic.Covers != "" {
			out = append(out, topic.Covers)
		}
	}
	return out
}

// ResearchCache keeps findings between checks of the same idea: a search is
// slow, costs money, and returns something slightly different every time, which
// would make two checks of one idea disagree for no reason the user can see.
type ResearchCache interface {
	Get(ctx context.Context, key string, maxAge time.Duration) ([]byte, bool)
	Put(ctx context.Context, key string, value []byte) error
}

// Evidence is one finding as the result reports it: what was found, where, how
// the judge typed it, and whether the rubric got to read it.
type Evidence struct {
	judge.Finding
	Relation   string  `json:"relation,omitempty"`   // the judge's answer to rubrics/_evidence.yaml
	Confidence float64 `json:"confidence,omitempty"` // of that answer
	Used       bool    `json:"used"`                 // false = dropped as unrelated
}

// ResearchReport is what the web lookup contributed to a check.
type ResearchReport struct {
	By string `json:"by"` // what searched: a search service, or the writer backend's own web tool
	// Queries are the searches ideacheck ran; empty when the writer searched itself.
	Queries  []judge.Query `json:"queries,omitempty"`
	Model    string        `json:"model,omitempty"`
	Cached   bool          `json:"cached,omitempty"` // reused from an earlier check of the same idea
	Findings []Evidence    `json:"findings"`
}

// researchPlan is what a check will look up; nil means it will not research.
type researchPlan struct {
	topics *Topics
	raw    []byte
	search Searcher         // ideacheck searches itself, or
	by     judge.Researcher // the writer's own web tool does
}

// via names what does the searching.
func (p *researchPlan) via(writer string) string {
	if p.search != nil {
		return p.search.Name()
	}
	return writer
}

// plan decides, before anything is asked, whether this check will research.
// A writer that cannot search is not a fault: the check scores the description.
func (e *Engine) plan(in Intake, res *Result) *researchPlan {
	by, _ := e.writer().(judge.Researcher)
	if by != nil && !by.CanResearch() {
		by = nil
	}
	if !e.Config.Research.Enabled || in.Empty() || (e.Search == nil && by == nil) {
		return nil
	}
	topics, raw, err := LoadTopics(e.Files)
	if err != nil {
		if res != nil {
			res.Warnings = append(res.Warnings, "not researched: "+err.Error())
		}
		return nil
	}
	return &researchPlan{topics: topics, raw: raw, search: e.Search, by: by}
}

func (p *researchPlan) covers() []string {
	if p == nil {
		return nil
	}
	return p.topics.covers()
}

// research looks the idea up and returns the state with `evidence` added. It
// never fails the check: without findings the description is scored as it is.
// The returned answers are the writer's calls, for the cost line.
func (e *Engine) research(ctx context.Context, p *researchPlan, given Intake, state judge.State, res *Result, o Options) (judge.State, []judge.Answer) {
	if p == nil {
		return state, nil
	}
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageResearch, Question: researchQuestion})
	start := e.now()
	report := &ResearchReport{By: p.via(e.writer().Name())}
	found, call, cached, err := e.findings(ctx, p, given, state, report, o)
	a := judge.Answer{ID: researchQuestion.ID, Model: call.Model, LatencyMS: e.now().Sub(start).Milliseconds(),
		TokensIn: call.TokensIn, TokensOut: call.TokensOut, TokensCached: call.TokensCached, CostUSD: call.CostUSD}
	if err != nil {
		a.Err = err.Error()
		res.Warnings = append(res.Warnings, "not researched, scoring the description alone: "+err.Error())
		emit(o.Events, Event{Type: judge.EventFailed, Stage: StageResearch, Question: researchQuestion, Answer: &a})
		return state, []judge.Answer{a}
	}
	a.Choice = fmt.Sprintf("%d found", len(found))
	emit(o.Events, Event{Type: judge.EventAnswered, Stage: StageResearch, Question: researchQuestion, Answer: &a})

	report.Model, report.Cached, report.Findings = call.Model, cached, make([]Evidence, len(found))
	for i, f := range found {
		report.Findings[i] = Evidence{Finding: f, Used: true}
	}
	if e.sifts(p) {
		res.Answers = append(res.Answers, e.sift(ctx, state, report, res, o)...)
	}
	res.Research = report
	out := judge.State{}
	for k, v := range state {
		out[k] = v
	}
	out[evidenceKey] = evidenceState(p.topics, report.Findings)
	return out, []judge.Answer{a}
}

// findings returns cached findings when the same idea was researched recently,
// else searches. Only a successful search is cached. The search reads the idea
// with the extracted fields; it is remembered under the idea as the caller gave
// it, because extraction words the same fact differently from run to run.
func (e *Engine) findings(ctx context.Context, p *researchPlan, given Intake, state judge.State, report *ResearchReport, o Options) ([]judge.Finding, judge.Research, bool, error) {
	about := state.Sub([]string{ideaKey}) // who the person is has no bearing on what exists
	key, err := e.cacheKey(p, given.State().Sub([]string{ideaKey}))
	if err != nil {
		return nil, judge.Research{}, false, err
	}
	ttl := e.Config.Research.CacheTTL
	if e.Cache != nil && ttl > 0 {
		if b, ok := e.Cache.Get(ctx, key, ttl); ok {
			var found []judge.Finding
			if json.Unmarshal(b, &found) == nil {
				return found, judge.Research{}, true, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, e.Config.Research.Timeout)
	defer cancel()
	var call judge.Research
	var reported []judge.Finding
	if p.search != nil {
		reported, call, err = e.lookup(ctx, p, given, about, report, o)
	} else {
		reported, call, err = e.ask(ctx, p, about)
	}
	if err != nil {
		return nil, call, false, err
	}
	found := capped(reported, p.topics.MaxFindings)
	if e.Cache != nil && ttl > 0 {
		if b, err := json.Marshal(found); err == nil {
			_ = e.Cache.Put(ctx, key, b) // a cache that cannot be written costs a search next time, nothing more
		}
	}
	return found, call, false, nil
}

// ask leaves the whole lookup to the writer's own web tool.
func (e *Engine) ask(ctx context.Context, p *researchPlan, about judge.State) ([]judge.Finding, judge.Research, error) {
	topics := make([]prompt.Topic, len(p.topics.Topics))
	for i, t := range p.topics.Topics {
		topics[i] = prompt.Topic{ID: t.ID, LookFor: t.LookFor}
	}
	system, user, err := prompt.Research(e.Files, e.Config.PromptsDir, about, topics, p.topics.MaxFindings)
	if err != nil {
		return nil, judge.Research{}, err
	}
	call, err := p.by.Research(ctx, system, user, p.topics.ids())
	return call.Findings, call, err
}

// cacheKey names a search: who searched, for what, about which idea.
func (e *Engine) cacheKey(p *researchPlan, about judge.State) (string, error) {
	idea, err := prompt.StateJSON(about)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", p.via(""), e.writer().Name(), e.Config.Backends[e.Config.WriterName()].Model)
	h.Write(p.raw)
	h.Write([]byte{0})
	h.Write([]byte(idea))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// capped keeps the first n findings of each topic, in the order reported.
func capped(found []judge.Finding, n int) []judge.Finding {
	count := map[string]int{}
	out := []judge.Finding{}
	for _, f := range found {
		if count[f.Topic]++; count[f.Topic] <= n {
			out = append(out, f)
		}
	}
	return out
}

// sifts reports whether the judge types each finding. In auto mode that is
// when nobody the judge could second-guess has vetted them yet: another backend
// chose them, or they are raw search results no writer digested. Asking the
// model that chose the findings whether they are related adds a call per
// finding and no second opinion.
func (e *Engine) sifts(p *researchPlan) bool {
	switch e.Config.Research.Sift {
	case config.SiftAlways:
		return true
	case config.SiftNever:
		return false
	}
	_, digests := e.writer().(judge.Digester)
	return (e.Writer != nil && e.Writer.Name() != e.Judge.Name()) || (p.search != nil && !digests)
}

// sift asks the judge how each finding relates to the idea and marks the ones
// the rubric says to drop. A finding the judge could not type is kept: a failed
// question is not evidence that a finding is unrelated.
func (e *Engine) sift(ctx context.Context, state judge.State, report *ResearchReport, res *Result, o Options) []judge.Answer {
	rb, err := rubric.Load(e.Files, e.Config.RubricsDir, rubric.EvidenceName)
	if err != nil {
		res.Warnings = append(res.Warnings, "findings kept as reported: "+err.Error())
		return nil
	}
	q := rb.Questions[0]
	answers := make([]judge.Answer, len(report.Findings))
	slots := make(chan struct{}, max(e.Config.MaxConcurrent(), 1))
	var wg sync.WaitGroup
	for i := range report.Findings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			f := report.Findings[i]
			s := judge.State{ideaKey: state[ideaKey], findingKey: map[string]any{"topic": f.Topic, "title": f.Title, "summary": f.Summary}}
			// One id per finding keeps the answers apart in answers[]; the live
			// view shows the finding's name, which is what a person recognizes.
			asked, shown := q, q
			asked.ID = fmt.Sprintf("%s.%d", q.ID, i+1)
			shown.ID = fmt.Sprintf("%d. %s", i+1, f.Title)
			answers[i] = e.fanoutAs(ctx, s, asked, shown, o)
		}()
	}
	wg.Wait()
	dropped := 0
	for i, a := range answers {
		if a.Failed() {
			continue
		}
		f := &report.Findings[i]
		f.Relation, f.Confidence = a.Choice, a.Confidence
		if contains(rb.Drop, a.Choice) {
			f.Used = false
			dropped++
		}
	}
	if dropped > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d research finding(s) were judged %v and left out of the evidence; see research.findings", dropped, rb.Drop))
	}
	return answers
}

// fanoutAs asks one question and reports it to the live view as shown.
func (e *Engine) fanoutAs(ctx context.Context, s judge.State, asked, shown judge.Question, o Options) judge.Answer {
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageResearch, Question: shown})
	quiet := o
	quiet.Events = nil
	a := e.fanout(ctx, s, []judge.Question{asked}, quiet, StageResearch)[0]
	t := judge.EventAnswered
	if a.Failed() {
		t = judge.EventFailed
	}
	emit(o.Events, Event{Type: t, Stage: StageResearch, Question: shown, Answer: &a})
	return a
}

// evidenceState is what rubric questions read: every topic is present, and an
// empty list means "searched, found nothing" — which is itself a finding.
// Sources stay in the result; a URL is noise to a question.
func evidenceState(topics *Topics, found []Evidence) map[string]any {
	out := map[string]any{}
	for _, t := range topics.Topics {
		out[t.ID] = []any{}
	}
	for _, f := range found {
		if !f.Used {
			continue
		}
		item := map[string]any{"title": f.Title, "summary": f.Summary}
		if f.Relation != "" {
			item["relation"] = f.Relation
		}
		out[f.Topic] = append(out[f.Topic].([]any), item)
	}
	return out
}

// settled removes gaps a search answered: a topic that covers the gap's field
// and came back with findings the rubric can read.
func settled(missing []Missing, p *researchPlan, report *ResearchReport) []Missing {
	if p == nil || report == nil {
		return missing
	}
	answered := map[string]bool{}
	for _, f := range report.Findings {
		if f.Used {
			answered[f.Topic] = true
		}
	}
	out := []Missing{}
	for _, m := range missing {
		covered := false
		for _, t := range p.topics.Topics {
			covered = covered || (t.Covers == m.Fills && answered[t.ID])
		}
		if !covered {
			out = append(out, m)
		}
	}
	return out
}
