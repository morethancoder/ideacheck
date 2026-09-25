package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"text/template"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/prompt"
	"github.com/morethancoder/ideacheck/internal/search"
)

// Searcher and PageReader are what the lookup needs from internal/search; tests
// replace them, so no pipeline test touches the network.
type Searcher interface {
	Name() string
	Search(ctx context.Context, query string, n int) ([]search.Result, error)
}

type PageReader interface {
	Read(ctx context.Context, pageURL string, maxChars int) (string, error)
}

// Live rows of the lookup; like researchQuestion they are shown, not asked.
var (
	planStep   = judge.Question{ID: "searches", Instructions: "Decide what to search for"}
	searchStep = judge.Question{ID: "results", Instructions: "Run the searches"}
	readStep   = judge.Question{ID: "pages", Instructions: "Read the best pages"}
)

const (
	searchesAtOnce = 4
	pagesAtOnce    = 6
)

// hit is one search result on its way to becoming evidence.
type hit struct {
	topic string
	search.Result
	text string // the page boiled down; "" when it was not read
}

// lookup is research with ideacheck doing the searching: the writer names the
// searches, Go runs them and reads the best pages, the writer says what the
// results amount to. The model never drives the loop, which is what keeps this
// to seconds and a few thousand tokens. Every step degrades instead of failing:
// no planner means the topic's own queries, an unreadable page means its
// snippet, no digester means the results themselves are the findings.
func (e *Engine) lookup(ctx context.Context, p *researchPlan, in Intake, about judge.State, report *ResearchReport, o Options) ([]judge.Finding, judge.Research, error) {
	var spent judge.Research
	queries := e.queries(ctx, p, in, about, &spent, o)
	report.Queries = queries

	hits, failed, err := e.searches(ctx, p, queries, o)
	if failed == len(queries) && err != nil {
		return nil, spent, err
	}
	if failed > 0 {
		report.partial = fmt.Sprintf("%d of %d searches failed, so the evidence is thinner than it should be: %v", failed, len(queries), err)
	}
	e.readPages(ctx, p, hits, o)

	digester, ok := e.writer().(judge.Digester)
	if !ok || len(hits) == 0 {
		return asFindings(hits, p.topics.SnippetChars), spent, nil
	}
	system, user, err := prompt.ResearchDigest(e.Files, e.Config.PromptsDir, about, topicsWith(p.topics, hits), p.topics.MaxFindings)
	if err != nil {
		return nil, spent, err
	}
	got, err := digester.Digest(ctx, system, user, p.topics.ids())
	add(&spent, got.Model, got.TokensIn, got.TokensOut, got.TokensCached, got.CostUSD)
	if err != nil {
		return nil, spent, fmt.Errorf("the results could not be digested: %w", err)
	}
	return sourced(got.Findings, hits), spent, nil
}

func add(to *judge.Research, model string, in, out, cached int, usd float64) {
	if to.Model == "" {
		to.Model = model
	}
	to.TokensIn, to.TokensOut, to.TokensCached, to.CostUSD = to.TokensIn+in, to.TokensOut+out, to.TokensCached+cached, to.CostUSD+usd
}

// queries asks the writer what to search for; when it cannot say, or says
// nothing usable for a topic, that topic's own queries from research.yaml run.
func (e *Engine) queries(ctx context.Context, p *researchPlan, in Intake, about judge.State, spent *judge.Research, o Options) []judge.Query {
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageResearch, Question: planStep})
	per := e.Config.Research.QueriesPerTopic
	byTopic := map[string][]string{}
	if planner, ok := e.writer().(judge.Planner); ok {
		topics := make([]prompt.Topic, len(p.topics.Topics))
		for i, t := range p.topics.Topics {
			topics[i] = prompt.Topic{ID: t.ID, LookFor: t.LookFor}
		}
		if system, user, err := prompt.ResearchPlan(e.Files, e.Config.PromptsDir, about, topics, per); err == nil {
			plan, err := planner.Plan(ctx, system, user, p.topics.ids())
			add(spent, plan.Model, plan.TokensIn, plan.TokensOut, plan.TokensCached, plan.CostUSD)
			for _, q := range plan.Queries {
				if text := strings.TrimSpace(q.Query); err == nil && text != "" && len(byTopic[q.Topic]) < per {
					byTopic[q.Topic] = append(byTopic[q.Topic], text)
				}
			}
		}
	}
	var out []judge.Query
	for _, t := range p.topics.Topics {
		texts := byTopic[t.ID]
		if len(texts) == 0 {
			texts = own(t, subject(in, p.topics.SubjectChars), per)
		}
		for _, text := range texts {
			out = append(out, judge.Query{Topic: t.ID, Query: text})
		}
	}
	a := judge.Answer{ID: planStep.ID, Choice: fmt.Sprintf("%d searches", len(out)), Model: spent.Model}
	emit(o.Events, Event{Type: judge.EventAnswered, Stage: StageResearch, Question: planStep, Answer: &a})
	return out
}

// own renders a topic's queries from research.yaml over the idea's subject.
func own(t Topic, subject string, n int) []string {
	var out []string
	for _, q := range t.Queries {
		tmpl, err := template.New(t.ID).Parse(q)
		var b bytes.Buffer
		if err != nil || tmpl.Execute(&b, map[string]string{"Subject": subject}) != nil || len(out) == n {
			continue
		}
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

// subject is the idea in a few words, from the most specific thing known.
func subject(in Intake, chars int) string {
	for _, s := range []string{in.Fields["solution"], in.Fields["problem"], in.Fields["title"], in.Idea, in.Context} {
		if s = strings.Join(strings.Fields(s), " "); s != "" {
			if r := []rune(s); chars > 0 && len(r) > chars {
				s = string(r[:chars])
			}
			return s
		}
	}
	return ""
}

// searches runs every query at once (a few at a time) and keeps each page once,
// under the first topic that found it. It reports how many searches failed and
// the last reason; the step itself fails only when every one of them did.
func (e *Engine) searches(ctx context.Context, p *researchPlan, queries []judge.Query, o Options) ([]hit, int, error) {
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageResearch, Question: searchStep})
	results := make([][]search.Result, len(queries))
	errs := make([]error, len(queries))
	slots := make(chan struct{}, searchesAtOnce)
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			results[i], errs[i] = p.search.Search(ctx, q.Query, e.Config.Research.ResultsPerQuery)
		}()
	}
	wg.Wait()

	var hits []hit
	seen := map[string]bool{}
	failed, lastErr := 0, error(nil)
	for i, rs := range results {
		if errs[i] != nil {
			failed, lastErr = failed+1, errs[i]
			continue
		}
		for _, r := range rs {
			if key := pageKey(r.URL); key != "" && !seen[key] && strings.TrimSpace(r.Title) != "" {
				seen[key] = true
				hits = append(hits, hit{topic: queries[i].Topic, Result: r})
			}
		}
	}
	a := judge.Answer{ID: searchStep.ID, Choice: fmt.Sprintf("%d results · %s", len(hits), p.search.Name())}
	if failed == len(queries) && failed > 0 {
		a.Err = lastErr.Error()
		emit(o.Events, Event{Type: judge.EventFailed, Stage: StageResearch, Question: searchStep, Answer: &a})
		return nil, failed, lastErr
	}
	emit(o.Events, Event{Type: judge.EventAnswered, Stage: StageResearch, Question: searchStep, Answer: &a})
	return hits, failed, lastErr
}

// pageKey names a page regardless of scheme, "www.", a trailing slash or a fragment.
func pageKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") + strings.TrimRight(u.Path, "/") + "?" + u.RawQuery
}

// readPages fetches the first read_pages results of each topic and boils them
// down. A page that cannot be read keeps its snippet; that is never an error.
func (e *Engine) readPages(ctx context.Context, p *researchPlan, hits []hit, o Options) {
	n := e.Config.Research.ReadPages
	if n == 0 || e.Pages == nil {
		return
	}
	emit(o.Events, Event{Type: judge.EventStarted, Stage: StageResearch, Question: readStep})
	perTopic := map[string]int{}
	slots := make(chan struct{}, pagesAtOnce)
	var wg sync.WaitGroup
	for i := range hits {
		if perTopic[hits[i].topic]++; perTopic[hits[i].topic] > n {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			ctx, cancel := context.WithTimeout(ctx, e.Config.Research.PageTimeout)
			defer cancel()
			hits[i].text, _ = e.Pages.Read(ctx, hits[i].URL, e.Config.Research.PageChars)
		}()
	}
	wg.Wait()
	read := 0
	for _, h := range hits {
		if h.text != "" {
			read++
		}
	}
	a := judge.Answer{ID: readStep.ID, Choice: fmt.Sprintf("%d read", read)}
	emit(o.Events, Event{Type: judge.EventAnswered, Stage: StageResearch, Question: readStep, Answer: &a})
}

// topicsWith groups the hits under their topics for the digest prompt.
func topicsWith(t *Topics, hits []hit) []prompt.Topic {
	out := make([]prompt.Topic, len(t.Topics))
	for i, topic := range t.Topics {
		out[i] = prompt.Topic{ID: topic.ID, LookFor: topic.LookFor}
		for _, h := range hits {
			if h.topic == topic.ID {
				out[i].Hits = append(out[i].Hits, prompt.Hit{Title: h.Title, URL: h.URL, Snippet: h.Snippet, Text: h.text})
			}
		}
	}
	return out
}

// asFindings is the lookup with no writer to digest it: each result is a
// finding as the search engine described it, and the judge sifts them.
func asFindings(hits []hit, chars int) []judge.Finding {
	out := make([]judge.Finding, 0, len(hits))
	for _, h := range hits {
		summary := h.Snippet
		if summary == "" {
			summary = h.text
		}
		if r := []rune(summary); chars > 0 && len(r) > chars {
			summary = string(r[:chars]) + "…"
		}
		out = append(out, judge.Finding{Topic: h.topic, Title: h.Title, Summary: summary, URL: h.URL})
	}
	return out
}

// sourced keeps the findings whose URL is one of the results handed over: a
// source the digest did not read is a source it made up.
func sourced(found []judge.Finding, hits []hit) []judge.Finding {
	known := map[string]bool{}
	for _, h := range hits {
		known[pageKey(h.URL)] = true
	}
	out := found[:0]
	for _, f := range found {
		if known[pageKey(f.URL)] {
			out = append(out, f)
		}
	}
	return out
}
