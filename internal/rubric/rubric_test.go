package rubric

import (
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
)

type memFiles map[string]string

func (m memFiles) Read(name string) ([]byte, error) {
	if s, ok := m[name]; ok {
		return []byte(s), nil
	}
	return nil, errNotFound
}
func (m memFiles) List(string) ([]string, error) { return nil, nil }

var errNotFound = &notFound{}

type notFound struct{}

func (*notFound) Error() string { return "not found" }

// Every rubric we ship must pass our own rules.
func TestEmbeddedRubricsAreValid(t *testing.T) {
	files := config.NewFiles("")
	names, err := Names(files, "rubrics")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "business,content,creative,research,side_project" {
		t.Fatalf("scoring rubrics = %s", got)
	}
	for _, name := range append(names, GapsName, RouterName) {
		rb, err := Load(files, "rubrics", name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.HasPrefix(rb.Hash, "sha256:") {
			t.Errorf("%s: hash = %q", name, rb.Hash)
		}
	}
	router, _ := Load(files, "rubrics", RouterName)
	for _, name := range names {
		if _, ok := router.Questions[0].Options[name]; !ok {
			t.Errorf("router has no option for rubric %q", name)
		}
	}
}

const scoringTail = `
verdict:
  thresholds: {build: 0.7, explore: 0.5, park: 0.3}
  min_confidence: 0.45
`

func load(t *testing.T, name, body string) error {
	t.Helper()
	_, err := Load(memFiles{"rubrics/" + name + ".yaml": body}, "rubrics", name)
	return err
}

func TestValidationRejectsBrokenRubrics(t *testing.T) {
	q := func(extra string) string {
		return "questions:\n  - id: a\n    instructions: x\n    uses: [idea]\n" + extra + scoringTail
	}
	cases := []struct{ name, body, want string }{
		{"unknown yaml key", "questions: []\ntypo: 1", "typo"},
		{"no questions", "questions: []" + scoringTail, "no questions"},
		{"bad kind", q("    kind: essay\n    weight: 1\n    polarity: 1\n"), "kind"},
		{"choice without other", q("    kind: choice\n    options: {x: X, y: Y}\n"), `"other"`},
		{"one level", q("    kind: score\n    weight: 1\n    polarity: 1\n    levels: [only]\n"), "levels"},
		{"eleven levels", q("    kind: score\n    weight: 1\n    polarity: 1\n    levels: [a,b,c,d,e,f,g,h,i,j,k]\n"), "levels"},
		{"negative weight", q("    kind: noul\n    weight: -1\n    polarity: 1\n"), "negative"},
		{"weight without polarity", q("    kind: noul\n    weight: 1\n"), "polarity"},
		{"bad polarity", q("    kind: noul\n    weight: 1\n    polarity: 2\n"), "polarity"},
		{"unknown state field", "questions:\n  - id: a\n    kind: noul\n    instructions: x\n    weight: 1\n    polarity: 1\n    uses: [secrets]\n" + scoringTail, "uses"},
		{"evidence without a topic", "questions:\n  - {id: a, kind: noul, instructions: x, weight: 1, polarity: 1, uses: [idea, evidence.]}\n" + scoringTail, "one research topic"},
		{"requires a field it reads no part of", "questions:\n  - {id: a, kind: noul, instructions: x, weight: 1, polarity: 1, uses: [idea], requires: [evidence]}\n" + scoringTail, "does not use"},
		{"no uses", "questions:\n  - id: a\n    kind: noul\n    instructions: x\n    weight: 1\n    polarity: 1\n" + scoringTail, "uses"},
		{"nothing weighted", q("    kind: noul\n"), "no weighted"},
		{"weighted choice without values", q("    kind: choice\n    weight: 1\n    polarity: 1\n    options: {x: X, other: O}\n"), "values"},
		{"duplicate id", q("    kind: noul\n    weight: 1\n    polarity: 1\n  - id: a\n    kind: noul\n    instructions: y\n    uses: [idea]\n"), "duplicate"},
		{"thresholds out of order", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  thresholds: {build: 0.5, explore: 0.5, park: 0.3}\n", "thresholds"},
		{"gate with misspelled id", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  gates: [{when: \"aa > 0.5\", max: park, reason: r}]\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n", `unknown question "aa"`},
		{"gate with bad max", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  gates: [{when: \"a > 0.5\", max: maybe, reason: r}]\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n", "max"},
		{"criteria on a score", q("    kind: score\n    weight: 1\n    polarity: 1\n    levels: [a, b]\n    criteria: {yes: y, no: n}\n"), "criteria"},
		{"one-sided noul criteria", q("    kind: noul\n    weight: 1\n    polarity: 1\n    criteria: {yes: y}\n"), "both yes and no"},
		{"backend gate with misspelled id", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n  backends:\n    jev:\n      gates: [{when: \"aa > 0.5\", max: park, reason: r}]\n", `backends.jev gate 0: when "aa > 0.5" references unknown question "aa"`},
		{"backend thresholds out of order", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n  backends:\n    jev:\n      thresholds: {build: 0.4, explore: 0.5, park: 0.3}\n", "backends.jev: thresholds"},
		{"gate that is not boolean", "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\nverdict:\n  gates: [{when: \"a + 1\", max: park, reason: r}]\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n", "when"},
	}
	for _, c := range cases {
		err := load(t, "custom", c.body)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
}

func TestRoleSpecificRules(t *testing.T) {
	gap := "threshold: %s\nquestions:\n  - {id: g, kind: noul, instructions: x, uses: [idea]%s}\n"
	if err := load(t, GapsName, strings.NewReplacer("%s", "0.5").Replace(gap[:14])+"questions:\n  - {id: g, kind: noul, instructions: x, uses: [idea]}\n"); err == nil || !strings.Contains(err.Error(), "ask") {
		t.Errorf("gap without ask: %v", err)
	}
	if err := load(t, GapsName, "threshold: 1\nquestions:\n  - {id: g, kind: noul, instructions: x, uses: [idea], ask: q, fills: problem}\n"); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Errorf("gap threshold 1: %v", err)
	}
	if err := load(t, GapsName, "threshold: 0.5\nquestions:\n  - {id: g, kind: noul, instructions: x, uses: [idea], ask: q, fills: problem}\n"); err != nil {
		t.Errorf("valid gaps: %v", err)
	}
	if err := load(t, RouterName, "questions:\n  - {id: r, kind: noul, instructions: x, uses: [idea]}\n"); err == nil || !strings.Contains(err.Error(), "router") {
		t.Errorf("router with noul: %v", err)
	}
	router := "questions:\n  - {id: r, kind: choice, instructions: x, uses: [idea], options: {a: A, other: O}}\n"
	if err := load(t, RouterName, router); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Errorf("router without a fallback: %v", err)
	}
	if err := load(t, RouterName, "fallback: business\n"+router); err != nil {
		t.Errorf("valid router: %v", err)
	}
}

func TestGateFiresOnlyWithAllInputsPresent(t *testing.T) {
	rb, err := Load(config.NewFiles(""), "rubrics", "business")
	if err != nil {
		t.Fatal(err)
	}
	sisp := &rb.Verdict.Gates[0] // sisp > 0.8 && problem_acuity < 0.34
	cases := []struct {
		name   string
		values map[string]float64
		want   bool
	}{
		{"both conditions hold", map[string]float64{"sisp": 0.9, "problem_acuity": 0.2}, true},
		{"sisp exactly at the bound", map[string]float64{"sisp": 0.8, "problem_acuity": 0.2}, false},
		{"acute problem", map[string]float64{"sisp": 0.9, "problem_acuity": 0.67}, false},
		// An unanswered question must not read as 0 and satisfy `< 0.34`.
		{"problem_acuity unanswered", map[string]float64{"sisp": 0.9}, false},
	}
	for _, c := range cases {
		got, err := sisp.Fires(c.values)
		if err != nil || got != c.want {
			t.Errorf("%s: Fires = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestRank(t *testing.T) {
	order := []string{Kill, Park, Explore, Build}
	for i, v := range order {
		if r, ok := Rank(v); !ok || r != i {
			t.Errorf("Rank(%s) = %d, %v", v, r, ok)
		}
	}
	if _, ok := Rank(Uncertain); ok {
		t.Error("uncertain is not a rankable verdict")
	}
}

// A backend override replaces only what it sets, and its gates are compiled
// and ready to fire like the defaults.
func TestVerdictForBackend(t *testing.T) {
	body := "questions:\n  - {id: a, kind: noul, instructions: x, uses: [idea], weight: 1, polarity: 1}\n" +
		"verdict:\n  gates: [{when: \"a > 0.7\", max: park, reason: r}]\n  thresholds: {build: 0.7, explore: 0.5, park: 0.3}\n  min_confidence: 0.45\n" +
		"  backends:\n    jev:\n      gates: [{when: \"a > 0.6\", max: park, reason: calibrated}]\n      min_confidence: 0.3\n"
	rb, err := Load(memFiles{"rubrics/custom.yaml": body}, "rubrics", "custom")
	if err != nil {
		t.Fatal(err)
	}
	jev, other := rb.Verdict.For("jev"), rb.Verdict.For("logprob")
	if jev.MinConfidence != 0.3 || jev.Thresholds != other.Thresholds || other.MinConfidence != 0.45 {
		t.Errorf("jev = %+v, logprob = %+v", jev, other)
	}
	at := map[string]float64{"a": 0.65}
	if fires, err := jev.Gates[0].Fires(at); err != nil || !fires {
		t.Errorf("jev gate at 0.65: %v, %v", fires, err)
	}
	if fires, _ := other.Gates[0].Fires(at); fires {
		t.Error("the default gate must keep its own cut")
	}
}
