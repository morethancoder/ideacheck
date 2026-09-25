package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
)

// counting answers like the mock and counts what reaches the judge: the
// number this file pins is the number of typed questions a check pays for.
type counting struct {
	*mock.Judge
	calls atomic.Int64
}

func (c *counting) Evaluate(ctx context.Context, s judge.State, q judge.Question) (judge.Answer, error) {
	c.calls.Add(1)
	return c.Judge.Evaluate(ctx, s, q)
}

// Only what simple code cannot decide goes to the judge. These are two
// representative checks; a change that asks the judge more must say why here.
func TestJudgeCallsPerCheck(t *testing.T) {
	dir := t.TempDir()
	b, _ := json.Marshal([]judge.Finding{
		{Topic: "competitors", Title: "Weave", Summary: "Texts overdue dental patients.", URL: "https://weave.example/dental"},
		{Topic: "competitors", Title: "Weave again", Summary: "The same page, found twice.", URL: "http://www.weave.example/dental/"},
		{Topic: "competitors", Title: "An empty result", Summary: " ", URL: "https://blank.example"},
		{Topic: "prior_attempts", Title: "DentaList", Summary: "Shut down in 2021.", URL: "https://dentalist.example"},
		{Topic: "market", Title: "120k US dental practices", Summary: "ADA count.", URL: "https://ada.example"},
		{Topic: "recent_changes", Title: "Texting rules eased", Summary: "2025 rule change.", URL: "https://rules.example"},
	})
	if err := os.WriteFile(filepath.Join(dir, "research.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	fixture(t, dir, "idea_type", judge.Answer{Choice: "side_project", Confidence: 0.9})
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.2})
	fixture(t, dir, "has_differentiation", judge.Answer{Noul: 0.2})
	fixture(t, dir, "relation.1", judge.Answer{Choice: "direct", Confidence: 0.9})

	j := &counting{Judge: &mock.Judge{Seed: 1, FixturesDir: dir}}
	e := engine(t, j)
	e.Config.Research.Sift = config.SiftAlways
	e.Cache = &memCache{m: map[string][]byte{}}
	routed := Intake{Idea: "A to-do app for dentists", Fields: map[string]string{"problem": "front desks lose track of recalls", "audience": "small dental practices"}}
	forced := routed.With("profile.skills", "Go and SvelteKit")

	cases := []struct {
		name string
		in   Intake
		o    Options
		want int64
	}{
		{"routed, researched and sifted", routed, Options{Proceed: true}, 15},
		{"the same idea again, research cached", routed, Options{Proceed: true}, 15},
		{"rubric forced, with a profile", forced, Options{Proceed: true, Rubric: "business"}, 28},
	}
	for _, c := range cases {
		j.calls.Store(0)
		res, err := e.Check(context.Background(), c.in, c.o)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusOK {
			t.Fatalf("%s: status %q", c.name, res.Status)
		}
		if got := j.calls.Load(); got != c.want {
			t.Errorf("%s: %d judge calls, want %d", c.name, got, c.want)
		}
	}
}
