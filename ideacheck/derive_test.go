package ideacheck

import (
	"context"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/rubric"
)

func TestDeriveRules(t *testing.T) {
	gaps := &rubric.Rubric{Threshold: 0.5, Questions: []judge.Question{
		{ID: "has_why_now", Fills: "why_now"}, {ID: "has_audience", Fills: "audience"}}}
	noul := func(d judge.Derive) judge.Question { return judge.Question{ID: "q", Kind: judge.Noul, Derive: &d} }
	competitors := noul(judge.Derive{Evidence: &judge.EvidenceRule{Topic: "competitors", Relation: "direct"}})
	report := func(fs ...Evidence) *ResearchReport { return &ResearchReport{Findings: fs} }
	typed := func(topic, relation string) Evidence {
		return Evidence{Finding: judge.Finding{Topic: topic}, Relation: relation}
	}
	low := map[string]judge.Answer{"has_why_now": {Noul: 0.2}, "has_audience": {Noul: 0.7, Confidence: 0.4}}
	cases := []struct {
		name string
		q    judge.Question
		k    known
		want float64
		ok   bool
	}{
		{"present, one field given", noul(judge.Derive{Present: []string{"profile.background", "profile.skills"}}),
			known{in: Intake{Profile: map[string]string{"skills": "Go"}}}, 1, true},
		{"present, none given", noul(judge.Derive{Present: []string{"profile.background"}}), known{}, 0, true},
		{"same_as reuses the gap's answer", noul(judge.Derive{SameAs: "has_audience"}), known{answers: low}, 0.7, true},
		{"same_as without that answer asks", noul(judge.Derive{SameAs: "has_audience"}), known{}, 0, false},
		{"evidence, a direct finding", competitors, known{research: report(typed("competitors", "adjacent"), typed("competitors", "direct"))}, 1, true},
		{"evidence, all typed, none direct", competitors, known{research: report(typed("competitors", "adjacent"), typed("market", "direct"))}, 0, true},
		{"evidence, searched, found none", competitors, known{research: report()}, 0, true},
		{"evidence, an untyped finding asks", competitors, known{research: report(typed("competitors", ""))}, 0, false},
		{"evidence, not researched asks", competitors, known{}, 0, false},
		{"unstated and the gap says so", noul(judge.Derive{ZeroWhenUnstated: "why_now"}), known{gaps: gaps, answers: low}, 0, true},
		{"unstated but given asks", noul(judge.Derive{ZeroWhenUnstated: "why_now"}),
			known{in: Intake{Fields: map[string]string{"why_now": "rules changed"}}, gaps: gaps, answers: low}, 0, false},
		{"unstated but the gap read it asks", noul(judge.Derive{ZeroWhenUnstated: "audience"}), known{gaps: gaps, answers: low}, 0, false},
	}
	for _, c := range cases {
		a, ok := derive(c.q, c.k)
		if ok != c.ok || (ok && (a.Noul != c.want || a.Method != MethodDerived || a.ID != "q")) {
			t.Errorf("%s: %+v, %v; want %v, %v", c.name, a, ok, c.want, c.ok)
		}
	}
}

// What the judge would only have repeated is answered in code, marked as
// such, and counts toward the composite like any answer.
func TestDerivedAnswersAreMarkedAndCount(t *testing.T) {
	dir := t.TempDir()
	findingsFixture(t, dir)
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.2})
	fixture(t, dir, "relation.1", judge.Answer{Choice: "direct", Confidence: 0.9})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	e.Settings.Research.Sift = SiftAlways
	res, err := e.Check(context.Background(), idea, CheckOptions{Rubric: "business", Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]struct {
		rule  string
		value float64
	}{"competitors_exist": {"evidence", 1}, "why_now_confirmed": {"zero_when_unstated", 0}} {
		d := dim(res, id)
		if d.Derived != want.rule || d.Value == nil || *d.Value != want.value {
			t.Errorf("%s = %+v, want derived by %s as %v", id, d, want.rule, want.value)
		}
	}
	for _, a := range res.Answers {
		if a.Method == MethodDerived && a.Model != "" {
			t.Errorf("a derived answer names a model: %+v", a)
		}
	}
	if res.Model != "mock" || res.Method != "mock" {
		t.Errorf("model/method = %q/%q: derived answers must not stand for the judge", res.Model, res.Method)
	}
}

// A rule that names what the other files do not have fails the check loudly.
func TestDeriveNamesAreChecked(t *testing.T) {
	e := engine(t, &mock.Judge{})
	gaps, _, err := e.loadPreflight()
	if err != nil {
		t.Fatal(err)
	}
	for rule, want := range map[string]judge.Derive{
		"same_as":            {SameAs: "has_vibes"},
		"present":            {Present: []string{"profile.shoe_size"}},
		"zero_when_unstated": {ZeroWhenUnstated: "title"},
		"relation":           {Evidence: &judge.EvidenceRule{Topic: "competitors", Relation: "rival"}},
	} {
		rb := &rubric.Rubric{Name: "custom", Questions: []judge.Question{{ID: "q", Kind: judge.Noul, Derive: &want}}}
		if err := e.derivable(rb, gaps); err == nil || !strings.Contains(err.Error(), "derive") {
			t.Errorf("%s: %v", rule, err)
		}
	}
}
