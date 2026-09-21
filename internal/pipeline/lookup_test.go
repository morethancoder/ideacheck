package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
	"github.com/morethancoder/ideacheck/internal/search"
)

// fakeSearch answers every query with two results named after it, plus one page
// every query finds, so deduplication has something to do.
type fakeSearch struct {
	mu      sync.Mutex
	queries []string
	fail    bool
}

func (f *fakeSearch) Name() string { return "fakesearch" }

func (f *fakeSearch) Search(_ context.Context, q string, n int) ([]search.Result, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if f.fail {
		return nil, errors.New("search service is down")
	}
	slug := strings.ReplaceAll(q, " ", "-")
	return []search.Result{
		{Title: "Result for " + q, URL: "https://" + slug + ".example/a", Snippet: "About " + q},
		{Title: "Everyone finds this", URL: "https://www.common.example/page/", Snippet: "Shared"},
	}, nil
}

type fakePages struct {
	mu   sync.Mutex
	read []string
}

func (f *fakePages) Read(_ context.Context, u string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = append(f.read, u)
	if strings.Contains(u, "common") {
		return "", errors.New("403")
	}
	return "PAGE TEXT of " + u, nil
}

// scribe plans and digests; it keeps what it was shown so the test can look.
type scribe struct {
	*mock.Judge
	plan   []judge.Query
	digest string
	found  []judge.Finding
}

func (s *scribe) Name() string { return "scribe" }

func (s *scribe) Plan(context.Context, string, string, []string) (judge.Plan, error) {
	return judge.Plan{Queries: s.plan, Model: "scribe-1", TokensIn: 300, TokensOut: 40}, nil
}

func (s *scribe) Digest(_ context.Context, _, user string, _ []string) (judge.Research, error) {
	s.digest = user
	return judge.Research{Findings: s.found, Model: "scribe-1", TokensIn: 5000, TokensOut: 400}, nil
}

// With no writer at all (Jev judging alone), research.yaml's own queries run,
// the results themselves are the findings, and the judge sifts every one.
func TestLookupWithoutAWriter(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "relation.2", judge.Answer{Choice: "unrelated", Confidence: 0.9})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	fs, fp := &fakeSearch{}, &fakePages{}
	e.Search, e.Pages = fs, fp

	res, err := e.Check(context.Background(), Intake{Idea: "x", Fields: map[string]string{"solution": "dental recall texting"}}, Options{Proceed: true})
	if err != nil || res.Research == nil {
		t.Fatalf("research = %+v, %v", res.Research, err)
	}
	r := res.Research
	if r.By != "fakesearch" || len(r.Queries) != 8 || r.Queries[0].Query != "dental recall texting software" || r.Queries[0].Topic != "competitors" {
		t.Errorf("by=%q queries=%+v, want 2 per topic rendered from research.yaml over the idea's solution", r.By, r.Queries)
	}
	shared := 0
	for _, f := range r.Findings {
		if strings.Contains(f.URL, "common.example") {
			shared++
		}
		if f.Relation == "" {
			t.Errorf("raw results must be sifted by the judge: %+v", f)
		}
	}
	if shared != 1 {
		t.Errorf("a page eight searches found is one finding, got %d", shared)
	}
	if r.Findings[1].Used {
		t.Errorf("the finding judged unrelated must be left out: %+v", r.Findings[1])
	}
	if len(fp.read) == 0 || len(fp.read) > 2*4 {
		t.Errorf("pages read = %d, want at most read_pages per topic", len(fp.read))
	}
}

// With a writer: its searches run instead, it sees the snippets and the pages
// boiled down, and a finding whose source it was never shown is dropped.
func TestLookupPlansSearchesAndDigests(t *testing.T) {
	e := engine(t, &mock.Judge{Seed: 1})
	w := &scribe{Judge: &mock.Judge{},
		plan: []judge.Query{{Topic: "competitors", Query: "dental recall"}, {Topic: "competitors", Query: "patient texting"},
			{Topic: "competitors", Query: "one too many"}, {Topic: "gossip", Query: "ignored"}},
		found: []judge.Finding{
			{Topic: "competitors", Title: "Real", Summary: "s", URL: "https://dental-recall.example/a"},
			{Topic: "competitors", Title: "Invented", Summary: "s", URL: "https://made-up.example/"}},
	}
	fs := &fakeSearch{}
	e.Writer, e.Search, e.Pages = w, fs, &fakePages{}

	res, err := e.Check(context.Background(), idea, Options{Proceed: true})
	if err != nil || res.Research == nil {
		t.Fatalf("research = %+v, %v", res.Research, err)
	}
	planned := 0
	for _, q := range res.Research.Queries {
		if q.Topic == "competitors" {
			planned++
		}
		if q.Query == "one too many" || q.Topic == "gossip" {
			t.Errorf("query %+v must not run: over queries_per_topic, or for a topic nobody asked about", q)
		}
	}
	if planned != 2 || len(res.Research.Queries) != 2+3*2 {
		t.Errorf("queries = %+v, want the writer's 2 for competitors and research.yaml's own for the rest", res.Research.Queries)
	}
	for _, want := range []string{"## Topic competitors", "url: https://dental-recall.example/a", "snippet: About dental recall", "PAGE TEXT of https://dental-recall.example/a"} {
		if !strings.Contains(w.digest, want) {
			t.Errorf("digest brief lacks %q:\n%s", want, w.digest)
		}
	}
	if f := res.Research.Findings; len(f) != 1 || f[0].Title != "Real" {
		t.Errorf("findings = %+v, want the one whose source was among the results", f)
	}
	if res.Cost.TokensIn < 5300 {
		t.Errorf("cost = %+v, want the plan and digest calls counted", res.Cost)
	}
}

// A search service that is down costs the research, never the check.
func TestLookupFailureScoresTheDescription(t *testing.T) {
	e := engine(t, &mock.Judge{Seed: 1})
	e.Search, e.Pages = &fakeSearch{fail: true}, &fakePages{}
	res, err := e.Check(context.Background(), idea, Options{Proceed: true})
	if err != nil || res.Status != StatusOK || res.Research != nil || res.Verdict == "" {
		t.Fatalf("status=%q research=%+v verdict=%q err=%v", res.Status, res.Research, res.Verdict, err)
	}
	if !containsSub(strings.Join(res.Warnings, "\n"), "search service is down") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestPageKey(t *testing.T) {
	same := []string{"https://www.Example.com/a/", "http://example.com/a", "https://example.com/a#pricing"}
	for _, u := range same[1:] {
		if pageKey(u) != pageKey(same[0]) {
			t.Errorf("%q and %q are one page", u, same[0])
		}
	}
	if pageKey("https://example.com/a?id=2") == pageKey(same[0]) || pageKey("nonsense") != "" {
		t.Error("a query string names another page; a non-URL names none")
	}
}
