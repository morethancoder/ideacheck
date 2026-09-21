package structured

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/prompt"
)

const Name = "structured"

// Completion is one model reply constrained to a JSON schema.
type Completion struct {
	Text         string
	Model        string
	TokensIn     int // all input, cached included
	TokensOut    int
	TokensCached int     // input read from a prompt cache
	CostUSD      float64 // cost reported by the provider (the Claude CLI does); 0 = unknown
}

// Completer is a provider: it sends (system, state, user) and returns JSON text
// matching schema. state is separate so providers can cache it across questions.
type Completer interface {
	Complete(ctx context.Context, system, state, user string, schema map[string]any) (Completion, error)
}

// Searcher is optionally implemented by a provider that can run the same call
// with live web search switched on. The reply is still JSON matching schema.
type Searcher interface {
	Search(ctx context.Context, system, user string, schema map[string]any, limits SearchLimits) (Completion, error)
}

// SearchLimits bound one research call; zero values leave the provider's own.
type SearchLimits struct {
	MaxSearches int
	MaxTokens   int
}

type Judge struct {
	Backend       string // reported by Name(): "structured" or "claude-cli"
	Prompts       *prompt.Set
	LLM           Completer
	Mode          string
	VoteK         int
	MaxConcurrent int
	Search        SearchLimits
}

func (j *Judge) Name() string { return j.Backend }

func (j *Judge) Capabilities() judge.Capabilities {
	return judge.Capabilities{NativeBatch: true, MaxConcurrent: j.MaxConcurrent}
}

func (j *Judge) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	answers, err := j.EvaluateBatch(ctx, state, []judge.Question{q})
	if err != nil {
		return judge.Answer{}, err
	}
	return answers[0], nil
}

// samples is how many independent replies one evaluation draws.
func (j *Judge) samples() int {
	if j.Mode == ModeVote && j.VoteK > 1 {
		return j.VoteK
	}
	return 1
}

// sampled is one reply covering every question in the request.
type sampled struct {
	replies map[string]reply
	done    Completion
	err     error
}

// EvaluateBatch draws the samples concurrently and tallies them per question.
func (j *Judge) EvaluateBatch(ctx context.Context, state judge.State, qs []judge.Question) ([]judge.Answer, error) {
	stateText, err := j.Prompts.State(state)
	if err != nil {
		return nil, err
	}
	user, err := j.Prompts.Questions(qs, j.Mode)
	if err != nil {
		return nil, err
	}
	results := make([]sampled, j.samples())
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = j.sample(ctx, stateText, user, qs)
		}()
	}
	wg.Wait()
	return j.tally(qs, results)
}

func (j *Judge) sample(ctx context.Context, state, user string, qs []judge.Question) sampled {
	schema := BatchSchema(qs, j.Mode)
	if len(qs) == 1 {
		schema = SchemaFor(qs[0], j.Mode)
	}
	done, err := j.LLM.Complete(ctx, j.Prompts.System, state, user, schema)
	if err != nil {
		return sampled{err: err}
	}
	s := sampled{done: done, replies: map[string]reply{}}
	if len(qs) == 1 {
		var r reply
		s.err = decode(done.Text, &r)
		s.replies[qs[0].ID] = r
		return s
	}
	s.err = decode(done.Text, &s.replies)
	return s
}

// tally fails only when every sample failed; otherwise it uses what came back
// and the method string reports the real sample count.
func (j *Judge) tally(qs []judge.Question, results []sampled) ([]judge.Answer, error) {
	var good []sampled
	var firstErr error
	for _, r := range results {
		switch {
		case r.err == nil:
			good = append(good, r)
		case firstErr == nil:
			firstErr = r.err
		}
	}
	if len(good) == 0 {
		return nil, firstErr
	}
	answers := make([]judge.Answer, len(qs))
	for i, q := range qs {
		answers[i] = j.answer(q, good)
		answers[i].ID = q.ID
	}
	// Token usage belongs to the request, not a question: book it once.
	for _, r := range results {
		answers[0].TokensIn += r.done.TokensIn
		answers[0].TokensOut += r.done.TokensOut
		answers[0].TokensCached += r.done.TokensCached
		answers[0].CostUSD += r.done.CostUSD
	}
	return answers, nil
}

func (j *Judge) answer(q judge.Question, good []sampled) judge.Answer {
	model := good[0].done.Model
	if j.Mode != ModeVote {
		a, err := fromVerbalized(q, good[0].replies[q.ID])
		if err != nil {
			return judge.Answer{Err: err.Error(), Model: model, Method: ModeVerbalized}
		}
		a.Method, a.Model = ModeVerbalized, model
		return a
	}
	var votes []string
	var lastErr error
	for _, s := range good {
		v, err := vote(q, s.replies[q.ID])
		if err != nil {
			lastErr = err
			continue
		}
		votes = append(votes, v)
	}
	if len(votes) == 0 {
		return judge.Answer{Err: lastErr.Error(), Model: model}
	}
	a := fromVotes(q, votes)
	a.Method, a.Model = fmt.Sprintf("vote:k=%d", len(votes)), model
	return a
}

var summarySchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"summary": map[string]any{"type": "string"}},
	"required":             []string{"summary"},
	"additionalProperties": false,
}

// extractSchema asks for every wanted field as a string. Nothing is required:
// a field the document does not answer comes back empty or absent.
func extractSchema(fields []string) object {
	props := object{}
	for _, f := range fields {
		props[f] = object{"type": "string"}
	}
	return object{"type": "object", "properties": props, "required": fields, "additionalProperties": false}
}

// Extract reads the wanted fields out of the document in one call. Like Narrate
// it is a single plain call: the values are quotes from the text, not judgments.
func (j *Judge) Extract(ctx context.Context, system, user string, fields []string) (judge.Extraction, error) {
	done, err := j.LLM.Complete(ctx, system, user, "Fill the fields now.", extractSchema(fields))
	if err != nil {
		return judge.Extraction{}, err
	}
	values := map[string]string{}
	if err := decode(done.Text, &values); err != nil {
		return judge.Extraction{}, err
	}
	for k, v := range values {
		values[k] = strings.TrimSpace(v)
	}
	return judge.Extraction{Values: values, Model: done.Model, TokensIn: done.TokensIn,
		TokensOut: done.TokensOut, TokensCached: done.TokensCached, CostUSD: done.CostUSD}, nil
}

// researchSchema asks for findings, each filed under one of the topics asked about.
func researchSchema(topics []string) object {
	finding := object{
		"type": "object",
		"properties": object{
			"topic":   object{"type": "string", "enum": topics},
			"title":   object{"type": "string"},
			"summary": object{"type": "string"},
			"url":     object{"type": "string"},
		},
		"required":             []string{"topic", "title", "summary", "url"},
		"additionalProperties": false,
	}
	return object{"type": "object", "properties": object{"findings": object{"type": "array", "items": finding}},
		"required": []string{"findings"}, "additionalProperties": false}
}

// planSchema asks for searches, each filed under one of the topics asked about.
func planSchema(topics []string) object {
	query := object{
		"type":                 "object",
		"properties":           object{"topic": object{"type": "string", "enum": topics}, "query": object{"type": "string"}},
		"required":             []string{"topic", "query"},
		"additionalProperties": false,
	}
	return object{"type": "object", "properties": object{"queries": object{"type": "array", "items": query}},
		"required": []string{"queries"}, "additionalProperties": false}
}

// Plan is one plain call: which searches are worth running for this idea.
func (j *Judge) Plan(ctx context.Context, system, user string, topics []string) (judge.Plan, error) {
	done, err := j.LLM.Complete(ctx, system, user, "List the searches now.", planSchema(topics))
	if err != nil {
		return judge.Plan{}, err
	}
	var out struct {
		Queries []judge.Query `json:"queries"`
	}
	if err := decode(done.Text, &out); err != nil {
		return judge.Plan{}, err
	}
	return judge.Plan{Queries: out.Queries, Model: done.Model, TokensIn: done.TokensIn,
		TokensOut: done.TokensOut, TokensCached: done.TokensCached, CostUSD: done.CostUSD}, nil
}

// Digest is one plain call over search results ideacheck fetched itself: the
// same findings Research reports, without the model ever touching the web.
func (j *Judge) Digest(ctx context.Context, system, user string, topics []string) (judge.Research, error) {
	done, err := j.LLM.Complete(ctx, system, user, "Report the findings now.", researchSchema(topics))
	if err != nil {
		return judge.Research{}, err
	}
	return findings(done, topics)
}

// CanResearch is true when the provider can search the web. A provider that
// serves several endpoints (Compat) says which of them can.
func (j *Judge) CanResearch() bool {
	_, ok := j.LLM.(Searcher)
	if some, limited := j.LLM.(interface{ CanSearch() bool }); limited {
		return ok && some.CanSearch()
	}
	return ok
}

// Research is one call with web search on. The findings are reported, not
// judged: a finding filed under a topic nobody asked about is dropped.
func (j *Judge) Research(ctx context.Context, system, user string, topics []string) (judge.Research, error) {
	searcher, ok := j.LLM.(Searcher)
	if !ok {
		return judge.Research{}, fmt.Errorf("the %s backend cannot search the web", j.Backend)
	}
	done, err := searcher.Search(ctx, system, user, researchSchema(topics), j.Search)
	if err != nil {
		return judge.Research{}, err
	}
	return findings(done, topics)
}

// findings reads a findings reply, keeping what is filed under a topic asked about.
func findings(done Completion, topics []string) (judge.Research, error) {
	var out struct {
		Findings []judge.Finding `json:"findings"`
	}
	// A search reply may wrap the object in prose: not every provider can
	// constrain the output while its search tool is on.
	if err := decode(firstObject(done.Text), &out); err != nil {
		return judge.Research{}, err
	}
	kept := out.Findings[:0]
	for _, f := range out.Findings {
		f.Topic, f.Title, f.Summary, f.URL = strings.TrimSpace(f.Topic), strings.TrimSpace(f.Title), strings.TrimSpace(f.Summary), strings.TrimSpace(f.URL)
		if f.Title != "" && slices.Contains(topics, f.Topic) {
			kept = append(kept, f)
		}
	}
	return judge.Research{Findings: kept, Model: done.Model, TokensIn: done.TokensIn,
		TokensOut: done.TokensOut, TokensCached: done.TokensCached, CostUSD: done.CostUSD}, nil
}

// Narrate writes the result summary with one plain call: no voting, since the
// text is shown, not counted. The brief goes in the cacheable state slot.
func (j *Judge) Narrate(ctx context.Context, system, brief string) (judge.Narration, error) {
	done, err := j.LLM.Complete(ctx, system, brief, "Write the summary now.", summarySchema)
	if err != nil {
		return judge.Narration{}, err
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if err := decode(done.Text, &out); err != nil {
		return judge.Narration{}, err
	}
	return judge.Narration{Text: strings.TrimSpace(out.Summary), Model: done.Model, TokensIn: done.TokensIn,
		TokensOut: done.TokensOut, TokensCached: done.TokensCached, CostUSD: done.CostUSD}, nil
}
