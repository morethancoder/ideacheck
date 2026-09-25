package ideacheck

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
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
	e.Settings.Research.Sift = SiftAlways
	e.Cache = &memCache{m: map[string][]byte{}}
	routed := Intake{Idea: "A to-do app for dentists", Fields: map[string]string{"problem": "front desks lose track of recalls", "audience": "small dental practices"}}
	// Another idea, so its research is not the first one's cached.
	forced := Intake{Idea: "A recall tracker for dentists", Fields: routed.Fields, Profile: map[string]string{"skills": "Go and SvelteKit"}}

	cases := []struct {
		name string
		in   Intake
		o    CheckOptions
		want int64
	}{
		{"routed, researched and sifted", routed, CheckOptions{Proceed: true}, 9},
		{"the same idea again, research cached", routed, CheckOptions{Proceed: true}, 8},
		{"rubric forced, with a profile", forced, CheckOptions{Proceed: true, Rubric: "business"}, 22},
	}
	for _, c := range cases {
		j.calls.Store(0)
		var events []Event // sift asks from several goroutines: -race checks OnEvent is called one at a time
		c.o.OnEvent = func(ev Event) { events = append(events, ev) }
		res, err := e.Check(context.Background(), c.in, c.o)
		if err != nil || len(events) == 0 {
			t.Fatal(err, len(events))
		}
		if res.Status != StatusOK {
			t.Fatalf("%s: status %q", c.name, res.Status)
		}
		if got := j.calls.Load(); got != c.want {
			t.Errorf("%s: %d judge calls, want %d", c.name, got, c.want)
		}
	}
}

// A follow-up after needs_input keeps the judgments already made: the gaps the
// person answered are not asked (their fields are filled), the ones left
// blank and the router keep their earlier answers, and only scoring is new.
func TestFollowUpAsksOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "idea_type", judge.Answer{Choice: "side_project", Confidence: 0.9})
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.2})
	fixture(t, dir, "has_differentiation", judge.Answer{Noul: 0.2})
	j := &counting{Judge: &mock.Judge{Seed: 1, FixturesDir: dir}}
	e := engine(t, j)

	first, err := e.Check(context.Background(), idea, CheckOptions{})
	if err != nil || first.Status != StatusNeedsInput {
		t.Fatalf("first = %+v, %v", first, err)
	}
	j.calls.Store(0)
	replied := idea.With("why_now", "the models got cheap") // differentiation left blank
	res, err := e.Check(context.Background(), replied, CheckOptions{Proceed: true, Earlier: first})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("follow-up = %+v, %v", res, err)
	}
	// side_project with a background: its 8 questions, none held back or derived.
	if got := j.calls.Load(); got != 8 {
		t.Errorf("follow-up asked the judge %d times, want only the 8 scoring questions", got)
	}
	if len(res.Missing) != 1 || res.Missing[0].Fills != "differentiation" || res.IdeaType == nil || res.IdeaType.Choice != "side_project" {
		t.Errorf("missing=%+v idea_type=%+v, want the kept judgments reported as before", res.Missing, res.IdeaType)
	}
}
