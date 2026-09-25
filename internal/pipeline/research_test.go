package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
)

var found = []judge.Finding{
	{Topic: "competitors", Title: "Weave", Summary: "Texts overdue dental patients.", URL: "https://example.com/weave"},
	{Topic: "competitors", Title: "A pizza blog", Summary: "Recipes.", URL: "https://example.com/pizza"},
	{Topic: "market", Title: "120k US dental practices", Summary: "ADA count.", URL: "https://example.com/ada"},
}

func findingsFixture(t *testing.T, dir string) {
	t.Helper()
	b, _ := json.Marshal(found)
	if err := os.WriteFile(filepath.Join(dir, "research.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func dim(res *Result, id string) Dimension {
	d, _ := dimension(res, id)
	return d
}

// Without research the evidence-only questions are skipped, not guessed; with
// it they are asked, the unrelated finding never reaches them, and a gap the
// search answered is no longer reported.
func TestResearchFeedsTheRubric(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "idea_type", judge.Answer{Choice: "business", Confidence: 0.9})
	fixture(t, dir, "has_competitor_awareness", judge.Answer{Noul: 0.1})
	fixture(t, dir, "relation.1", judge.Answer{Choice: "direct", Confidence: 0.9})
	fixture(t, dir, "relation.2", judge.Answer{Choice: "unrelated", Confidence: 0.95})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	e.Config.Research.Sift = config.SiftAlways

	res, err := e.Check(context.Background(), idea, Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Research != nil || dim(res, "differentiation_holds").Error == "" || len(res.Missing) != 1 {
		t.Fatalf("no findings fixture: research=%+v holds=%+v missing=%v", res.Research, dim(res, "differentiation_holds"), res.Missing)
	}

	findingsFixture(t, dir)
	res, err = e.Check(context.Background(), idea, Options{}) // not Proceed: a researchable gap must not stop the check
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || res.Research == nil || len(res.Research.Findings) != 3 {
		t.Fatalf("status=%q research=%+v", res.Status, res.Research)
	}
	if f := res.Research.Findings; !f[0].Used || f[1].Used || f[1].Relation != "unrelated" || f[0].URL == "" {
		t.Errorf("findings = %+v, want the pizza blog dropped as unrelated", f)
	}
	if d := dim(res, "differentiation_holds"); d.Error != "" || d.Value == nil {
		t.Errorf("differentiation_holds = %+v, want it asked once evidence exists", d)
	}
	if len(res.Missing) != 0 || res.Partial {
		t.Errorf("missing=%v partial=%v: the search found competitors, so that gap is settled", res.Missing, res.Partial)
	}
}

func TestEvidenceState(t *testing.T) {
	topics, _, err := LoadTopics(config.NewFiles(""))
	if err != nil {
		t.Fatal(err)
	}
	s := evidenceState(topics, []Evidence{{Finding: found[0], Relation: "direct", Used: true}, {Finding: found[1], Used: false}})
	b, _ := json.Marshal(s)
	got := string(b)
	for _, want := range []string{`"prior_attempts":[]`, `"relation":"direct"`, `"title":"Weave"`} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence %s lacks %s", got, want)
		}
	}
	if strings.Contains(got, "pizza") || strings.Contains(got, "example.com") {
		t.Errorf("evidence %s must leave out dropped findings and URLs", got)
	}
}

// A page reported twice under one topic, or a finding with nothing to say, is
// dropped before any judge reads it; the same page under another topic stays.
func TestDistinctDropsRepeatsAndBlanks(t *testing.T) {
	got := distinct([]judge.Finding{
		{Topic: "competitors", Summary: "Texts patients.", URL: "https://weave.example/dental"},
		{Topic: "competitors", Summary: "Again.", URL: "http://www.weave.example/dental/#top"},
		{Topic: "market", Summary: "Raised $100M.", URL: "https://weave.example/dental"},
		{Topic: "competitors", Summary: "  ", URL: "https://blank.example"},
		{Topic: "competitors", Summary: "No source given."},
		{Topic: "competitors", Summary: "Nor here."},
	})
	if len(got) != 4 || got[1].Topic != "market" || got[3].Summary != "Nor here." {
		t.Errorf("distinct = %+v", got)
	}
}

type memCache struct {
	m    map[string][]byte
	puts int
}

func (c *memCache) Get(_ context.Context, k string, _ time.Duration) ([]byte, bool) {
	b, ok := c.m[k]
	return b, ok
}

func (c *memCache) Put(_ context.Context, k string, v []byte) error {
	c.m[k], c.puts = v, c.puts+1
	return nil
}

func TestResearchIsCachedPerIdea(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	cache := &memCache{m: map[string][]byte{}}
	e.Cache = cache

	first, _ := e.Check(context.Background(), idea, Options{Proceed: true})
	second, _ := e.Check(context.Background(), idea, Options{Proceed: true})
	other, _ := e.Check(context.Background(), Intake{Idea: "A different idea"}, Options{Proceed: true})
	if first.Research.Cached || !second.Research.Cached || other.Research.Cached || cache.puts != 2 {
		t.Errorf("cached = %v %v %v, puts = %d; want false true false, 2", first.Research.Cached, second.Research.Cached, other.Research.Cached, cache.puts)
	}
	if len(second.Research.Findings) != len(first.Research.Findings) {
		t.Errorf("cached findings = %d, want %d", len(second.Research.Findings), len(first.Research.Findings))
	}
}

// How the judge typed a cached finding is cached with it; an entry from before
// that was remembered carries no relation and is sifted as it always was.
func TestSiftIsCachedWithTheFindings(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	fixture(t, dir, "relation.2", judge.Answer{Choice: "unrelated", Confidence: 0.95})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	e.Config.Research.Sift = config.SiftAlways
	cache := &memCache{m: map[string][]byte{}}
	e.Cache = cache
	sifted := func(res *Result) (n int) {
		for _, a := range res.Answers {
			if strings.HasPrefix(a.ID, "relation.") {
				n++
			}
		}
		return n
	}

	first, _ := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "business"})
	second, _ := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "business"})
	if sifted(first) != 3 || sifted(second) != 0 || !second.Research.Cached {
		t.Fatalf("sift questions = %d then %d, want 3 then none", sifted(first), sifted(second))
	}
	if f := second.Research.Findings; f[1].Relation != "unrelated" || f[1].Used || f[0].Relation == "" {
		t.Errorf("cached findings = %+v, want the relations and the drop kept", f)
	}

	old, _ := json.Marshal(found) // an entry written before sifting was cached
	for k := range cache.m {
		cache.m[k] = old
	}
	third, _ := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "business"})
	if sifted(third) != 3 || third.Research.Findings[1].Used {
		t.Errorf("an old cache entry: %d sift questions, findings %+v", sifted(third), third.Research.Findings)
	}
}

// The rubric is known before research, so only the topics its questions read
// are searched; a rubric that reads no evidence costs no search, and one that
// reads a topic research.yaml does not have is a config error.
func TestOnlyTheTopicsTheRubricReadsAreSearched(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	res, err := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "side_project"})
	if err != nil || res.Research == nil || len(res.Research.Findings) != 2 {
		t.Fatalf("side_project research = %+v, %v; want the two competitors only", res.Research, err)
	}
	for _, f := range res.Research.Findings {
		if f.Topic != "competitors" {
			t.Errorf("searched %q, which no side_project question reads", f.Topic)
		}
	}

	user := t.TempDir()
	if err := os.MkdirAll(filepath.Join(user, "rubrics"), 0o755); err != nil {
		t.Fatal(err)
	}
	rubricFile := func(name, uses string) {
		body := "questions:\n  - {id: a, kind: noul, instructions: x, uses: " + uses + ", weight: 1, polarity: 1}\nverdict:\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n"
		if err := os.WriteFile(filepath.Join(user, "rubrics", name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rubricFile("quiet", "[idea]")
	rubricFile("odd", "[idea, evidence.weather]")
	e.Files = config.NewFiles(user)
	if res, err := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "quiet"}); err != nil || res.Research != nil {
		t.Errorf("a rubric that reads no evidence: research = %+v, %v", res.Research, err)
	}
	if _, err := e.Check(context.Background(), idea, Options{Proceed: true, Rubric: "odd"}); err == nil || !strings.Contains(err.Error(), "evidence.weather") {
		t.Errorf("a topic research.yaml does not have: %v", err)
	}
}

// writer is a prose backend that cannot judge: Evaluate must never be called.
type writer struct {
	*mock.Judge
	t *testing.T
}

func (w writer) Name() string { return "scribe" }
func (w writer) Evaluate(context.Context, judge.State, judge.Question) (judge.Answer, error) {
	w.t.Error("a typed question went to the writer; every judgment belongs to the judge")
	return judge.Answer{}, nil
}

func TestWriterWritesAndJudgeJudges(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	e := engine(t, &mock.Judge{Seed: 1}) // no fixtures: this judge cannot research
	e.Writer = writer{&mock.Judge{FixturesDir: dir}, t}

	res, err := e.Check(context.Background(), idea, Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != "mock" || res.Writer != "scribe" || res.Research == nil || res.Research.By != "scribe" || res.Summary == "" {
		t.Fatalf("backend=%q writer=%q research=%+v summary=%q", res.Backend, res.Writer, res.Research, res.Summary)
	}
	// sift: auto — someone else searched, so the judge types every finding.
	for _, f := range res.Research.Findings {
		if f.Relation == "" {
			t.Errorf("finding %q was not typed by the judge", f.Title)
		}
	}
}

func TestFollowUpsAreFewAndSkipWhatResearchAnswers(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	for _, id := range []string{"has_problem", "has_audience", "has_solution", "has_why_now", "has_competitor_awareness"} {
		fixture(t, dir, id, judge.Answer{Noul: 0.1})
	}
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	res, err := e.Check(context.Background(), idea, Options{})
	if err != nil {
		t.Fatal(err)
	}
	asks := e.FollowUps(res, idea, nil)
	if res.Status != StatusNeedsInput || len(res.Missing) != 5 || len(asks) != 3 || asks[0].Fills != "problem" {
		t.Fatalf("status=%q missing=%d asks=%+v; want 3 asks, problem first", res.Status, len(res.Missing), asks)
	}
	for _, m := range asks {
		if m.Fills == "competitors_known" {
			t.Error("asked for competitors although research is about to look them up")
		}
	}
}
