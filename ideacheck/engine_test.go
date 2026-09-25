package ideacheck

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/rubric"
)

type memFiles map[string]string

func (m memFiles) Read(name string) ([]byte, error) {
	if s, ok := m[name]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}
func (m memFiles) List(string) ([]string, error) { return nil, nil }

func containsSub(s, sub string) bool { return strings.Contains(s, sub) }

func engine(t *testing.T, j judge.Judge) *Engine {
	t.Helper()
	s, err := DefaultSettings("mock", "")
	if err != nil {
		t.Fatal(err)
	}
	s.Retry.MaxAttempts = 1
	return &Engine{Settings: s, Files: configs.Defaults(), Judge: j}
}

func fixture(t *testing.T, dir, id string, a judge.Answer) {
	t.Helper()
	b, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

var (
	// idea states a background, so the founder-context gap is settled and only
	// the gaps a test sets up can stop a check.
	idea = Intake{Idea: "A CLI that scores startup ideas", Profile: map[string]string{"background": "ten years in payroll software"}}
	// bare says nothing about the person.
	bare = Intake{Idea: "A CLI that scores startup ideas"}
)

func TestCheckEndToEnd(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "idea_type", judge.Answer{Choice: "business", Confidence: 0.91})
	res, err := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir}).Check(context.Background(), idea, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || res.Rubric == nil || res.Rubric.Name != "business" {
		t.Fatalf("status=%q rubric=%+v error=%q", res.Status, res.Rubric, res.Error)
	}
	if res.IdeaType == nil || res.IdeaType.Choice != "business" || res.IdeaType.Confidence != 0.91 {
		t.Errorf("idea_type = %+v", res.IdeaType)
	}
	if len(res.Answers) != 6+1+17 {
		t.Errorf("answers = %d, want 6 gaps (the background is given) + 1 router + 17 business", len(res.Answers))
	}
	if res.Composite <= 0 || res.Composite >= 1 || res.Verdict == "" || res.VerdictReason == "" {
		t.Errorf("composite=%v verdict=%q reason=%q", res.Composite, res.Verdict, res.VerdictReason)
	}
	if len(res.Dimensions) != 17 || res.Dimensions[0].ID != "problem_acuity" || res.Dimensions[0].Value == nil || res.Dimensions[0].Weight != 2 || res.Dimensions[1].Polarity != -1 {
		t.Errorf("dimensions = %+v", res.Dimensions[:2])
	}
	if res.Backend != "mock" || res.Model != "mock" || res.Method != "mock" {
		t.Errorf("backend/model/method = %q %q %q", res.Backend, res.Model, res.Method)
	}
	if !strings.HasPrefix(res.ID, "chk_") || !strings.HasPrefix(res.ConfigHash, "sha256:") || len(res.Missing) != 0 {
		t.Errorf("id=%q hash=%q missing=%v", res.ID, res.ConfigHash, res.Missing)
	}

	again, _ := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir}).Check(context.Background(), idea, Options{})
	if again.Composite != res.Composite || again.Verdict != res.Verdict {
		t.Error("mock backend must be deterministic for a seed")
	}
}

func TestGapsStopTheCheckUnlessProceed(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.21})
	fixture(t, dir, "has_problem", judge.Answer{Noul: 0.5}) // exactly at threshold: not missing
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})

	res, err := e.Check(context.Background(), idea, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusNeedsInput || res.Verdict != "" || res.Rubric != nil {
		t.Fatalf("status=%q verdict=%q rubric=%v", res.Status, res.Verdict, res.Rubric)
	}
	if len(res.Missing) != 1 {
		t.Fatalf("missing = %+v", res.Missing)
	}
	m := res.Missing[0]
	if m.ID != "has_why_now" || m.Probability != 0.21 || m.Fills != "why_now" || !strings.HasPrefix(m.Ask, "What changed recently") {
		t.Errorf("missing[0] = %+v", m)
	}

	res, _ = e.Check(context.Background(), idea, Options{Proceed: true})
	if res.Status != StatusOK || res.Verdict == "" || len(res.Missing) != 1 {
		t.Errorf("proceed: status=%q verdict=%q missing=%d", res.Status, res.Verdict, len(res.Missing))
	}
}

// A fact the intake already carries is not missing, however the prose reads; a
// field the user was offered and left blank is reported but never stops the
// check to ask again.
func TestGapsOnAnsweredFieldsDoNotStopTheCheck(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.2})
	fixture(t, dir, "has_differentiation", judge.Answer{Noul: 0.1})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})
	filled := idea.With("why_now", "a vague reason")

	res, _ := e.Check(context.Background(), filled, Options{Answered: []string{"differentiation"}})
	if res.Status != StatusOK || len(res.Missing) != 1 || res.Missing[0].Fills != "differentiation" {
		t.Errorf("status=%q missing=%+v, want ok reporting only differentiation", res.Status, res.Missing)
	}
	for _, a := range res.Answers {
		if a.ID == "has_why_now" {
			t.Error("the judge was asked whether why_now is stated, although the intake fills it")
		}
	}
	res, _ = e.Check(context.Background(), filled, Options{})
	if ask := Unanswered(res.Missing, filled, nil); res.Status != StatusNeedsInput || len(ask) != 1 || ask[0].Fills != "differentiation" {
		t.Errorf("status=%q ask=%+v, want only the never-offered differentiation", res.Status, ask)
	}
}

func TestRouting(t *testing.T) {
	cases := []struct {
		name, choice, forced, want string
		warn                       bool
	}{
		{"router choice", "content", "", "content", false},
		{"other falls back", "other", "", "business", true},
		{"type without a rubric falls back", "podcast", "", "business", true},
		{"forced beats router", "content", "research", "research", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		fixture(t, dir, "idea_type", judge.Answer{Choice: c.choice, Confidence: 0.8})
		res, err := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir}).Check(context.Background(), idea, Options{Rubric: c.forced})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		fellBack := false
		for _, w := range res.Warnings {
			fellBack = fellBack || strings.Contains(w, "has no rubric")
		}
		if res.Rubric.Name != c.want || fellBack != c.warn {
			t.Errorf("%s: rubric=%q warnings=%v", c.name, res.Rubric.Name, res.Warnings)
		}
		if c.forced != "" {
			for _, a := range res.Answers {
				if a.ID == "idea_type" {
					t.Errorf("%s: the router was asked although the rubric was forced", c.name)
				}
			}
			if res.IdeaType == nil || res.IdeaType.Choice != c.forced {
				t.Errorf("%s: idea_type = %+v, want the forced rubric", c.name, res.IdeaType)
			}
		}
	}
	if _, err := engine(t, &mock.Judge{}).Check(context.Background(), idea, Options{Rubric: "nonexistent"}); err == nil {
		t.Error("forcing a rubric that does not exist must fail")
	}
}

type failing struct{ *mock.Judge }

func (failing) Evaluate(context.Context, judge.State, judge.Question) (judge.Answer, error) {
	return judge.Answer{}, errors.New("backend down")
}

func TestBackendDownYieldsErrorStatusNotAVerdict(t *testing.T) {
	res, err := engine(t, failing{&mock.Judge{}}).Check(context.Background(), idea, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError || res.Verdict != "" || res.Error == "" {
		t.Errorf("status=%q verdict=%q error=%q", res.Status, res.Verdict, res.Error)
	}
	if len(res.Missing) != 0 {
		t.Errorf("failed gap questions reported as gaps: %+v", res.Missing)
	}
	if res.Answers[0].Err != "backend down" {
		t.Errorf("answers[0] = %+v", res.Answers[0])
	}
}

func TestCostEstimate(t *testing.T) {
	a := judge.Answer{TokensIn: 2_000_000, TokensOut: 500_000}
	if got := cost(Price{In: 3, Out: 15}, a); !near(got, 13.5) {
		t.Errorf("cost = %v, want 13.5", got)
	}
	a.TokensCached = 1_000_000 // half the input read from cache at a tenth of the price
	if got := cost(Price{In: 3, Out: 15, CachedIn: 0.3}, a); !near(got, 3+0.3+7.5) {
		t.Errorf("cached cost = %v, want 10.8", got)
	}
}

func TestCostSummary(t *testing.T) {
	e := engine(t, &mock.Judge{})
	e.Settings.Pricing = map[string]Price{"claude-haiku-4-5": {In: 1, Out: 5}}
	priced := []judge.Answer{{Model: "claude-haiku-4-5-20251001", TokensIn: 1_000_000, TokensOut: 100_000}}
	if c := e.cost(priced, nil); c.Basis != CostPriced || !near(c.USD, 1.5) || c.Price == nil || c.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("priced = %+v", c)
	}
	reported := append(priced, judge.Answer{Model: "claude-haiku-4-5", TokensIn: 10, CostUSD: 0.25})
	if c := e.cost(reported, nil); c.Basis != CostReported || !near(c.USD, 1.75) {
		t.Errorf("reported = %+v, want the reported 0.25 plus the priced 1.5", c)
	}
	if c := e.cost([]judge.Answer{{Model: "mystery", TokensIn: 5}}, nil); c.Basis != CostUnpriced || c.USD != 0 || !strings.Contains(c.Note, "mystery") {
		t.Errorf("unpriced = %+v", c)
	}
	e.Settings.Judge.Billing, e.Settings.Writer.Billing = BillingLocal, BillingLocal
	if c := e.cost(priced, nil); c.Basis != CostFree || c.USD != 0 || c.TokensIn != 1_000_000 {
		t.Errorf("local = %+v", c)
	}
	// A free local judge next to a paid writer: only the writer's calls cost.
	e.Settings.Writer = Role{Model: "claude-sonnet-5"}
	if c := e.cost(priced, priced); c.Basis != CostPriced || !near(c.USD, 1.5) || c.TokensIn != 2_000_000 {
		t.Errorf("local judge + paid writer = %+v, want the writer's 1.5 only", c)
	}
}

func TestIntake(t *testing.T) {
	in, err := ParseIntake([]byte(`  {"idea":" x ","fields":{"problem":"p","why_now":"  "},"profile":{"skills":"go"}} `))
	if err != nil {
		t.Fatal(err)
	}
	s := in.State()
	ideaState := s["idea"].(map[string]any)
	if ideaState["text"] != "x" || ideaState["problem"] != "p" || len(ideaState) != 2 {
		t.Errorf("idea state = %v (blank fields must be omitted)", ideaState)
	}
	if s["profile"].(map[string]any)["skills"] != "go" {
		t.Errorf("profile state = %v", s["profile"])
	}
	if in, _ := ParseIntake([]byte("just text")); in.Idea != "just text" || len(in.State()) != 1 {
		t.Errorf("plain text intake = %+v state=%v", in, in.State())
	}
	for _, bad := range []string{"", "   ", `{"idea":""}`, `{"idea":"x","fields":{"pitch":"y"}}`, `{"idea":"x","extra":1}`, `{"idea":`} {
		if _, err := ParseIntake([]byte(bad)); err == nil {
			t.Errorf("ParseIntake(%q) should fail", bad)
		}
	}
	filled := in.With("why_now", "new API").With("profile.background", "10y payroll")
	if filled.Fields["why_now"] != "new API" || filled.Profile["background"] != "10y payroll" || in.Fields["why_now"] != "  " {
		t.Errorf("With = %+v (original must be untouched: %+v)", filled, in)
	}
	if !KnownField("problem") || !KnownField("profile.background") || KnownField("profile.problem") || KnownField("pitch") {
		t.Error("KnownField misclassifies")
	}
}

func TestEventsCarryQuestionStageAndValue(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "idea_type", judge.Answer{Choice: "business", Confidence: 0.9})
	fixture(t, dir, "problem_acuity", judge.Answer{Score: 1.5})
	ch := make(chan Event, 128)
	if _, err := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir}).Check(context.Background(), bare, Options{Events: ch, Proceed: true}); err != nil {
		t.Fatal(err)
	}
	close(ch)
	stages := map[string]int{}
	var acuity *Event
	for e := range ch {
		if e.Type == judge.EventAnswered {
			stages[e.Stage]++
		}
		if e.Type == judge.EventAnswered && e.Question.ID == "problem_acuity" {
			acuity = &e
		}
	}
	// 7 gaps (founder context derived, not asked, but shown) + the router; 13
	// of the business rubric's 17: founder_market_fit and personal_want require
	// a profile, and this idea has none, and two more require research.
	if stages[StagePreflight] != 8 || stages[StageScore] != 13 {
		t.Errorf("answered per stage = %v, want preflight 8, score 13", stages)
	}
	if acuity == nil || acuity.Value == nil || !near(*acuity.Value, 0.5) || acuity.Question.Weight != 2 {
		t.Errorf("problem_acuity event = %+v", acuity)
	}
}

func TestSummaryIsWrittenAfterScoring(t *testing.T) {
	e := engine(t, &mock.Judge{Seed: 1})
	e.Settings.Explain = true
	ch := make(chan Event, 128)
	res, err := e.Check(context.Background(), idea, Options{Events: ch})
	if err != nil {
		t.Fatal(err)
	}
	close(ch)
	if !strings.HasPrefix(res.Summary, "Mock summary") {
		t.Errorf("summary = %q", res.Summary)
	}
	var explained int
	for ev := range ch {
		if ev.Stage == StageExplain {
			explained++
		}
	}
	if explained != 2 {
		t.Errorf("explain events = %d, want started + answered", explained)
	}
	e.Settings.Explain = false
	if res, _ := e.Check(context.Background(), idea, Options{}); res.Summary != "" {
		t.Error("explain: false must skip the summary")
	}
}

func TestFindingsPutTheBiggestPullFirstInPlainWords(t *testing.T) {
	qs := []judge.Question{
		{ID: "info", Kind: judge.Noul, Instructions: "informational"},
		{ID: "small", Kind: judge.Score, Weight: 1, Polarity: 1, Instructions: "small", Levels: []string{"low", "mid", "high"}},
		{ID: "tarpit", Kind: judge.Noul, Weight: 2, Polarity: -1, Instructions: "tarpit"},
		{ID: "down", Kind: judge.Noul, Weight: 1, Polarity: 1, Instructions: "failed"},
	}
	answers := []judge.Answer{{Noul: 0.5}, {Score: 1.8}, {Noul: 0.9}, {Err: "timeout"}}
	got := findings(qs, answers)
	if len(got) != 3 {
		t.Fatalf("failed answers must be left out: %+v", got)
	}
	if got[0].Question != "tarpit" || !got[0].Weighted || !near(got[0].Good, 0.1) || !got[0].Noul || got[0].Probability != 0.9 {
		t.Errorf("first = %+v, want the heavy tarpit, bad news with polarity applied", got[0])
	}
	if got[1].Reading != "high" || !near(got[1].Good, 0.9) || got[2].Weighted {
		t.Errorf("rest = %+v", got[1:])
	}
}

// A question that requires state the check does not have is left unscored, not
// guessed: its weight goes to the questions that could be answered.
func TestQuestionsRequiringAProfileAreNotGuessed(t *testing.T) {
	e := engine(t, &mock.Judge{Seed: 1})
	res, err := e.Check(context.Background(), bare, Options{Rubric: "business", Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := dimension(res, "founder_market_fit")
	if !ok || d.Value != nil || !containsSub(d.Error, "no profile") {
		t.Fatalf("without a profile: dimension = %+v", d)
	}
	if res.Verdict == "" || res.Composite == 0 {
		t.Errorf("one unscored dimension must not stop the verdict: %+v", res)
	}
	withProfile := bare.With("profile.background", "ten years in payroll software")
	res, err = e.Check(context.Background(), withProfile, Options{Rubric: "business", Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := dimension(res, "founder_market_fit"); !ok || d.Value == nil || d.Error != "" {
		t.Errorf("with a profile: dimension = %+v", d)
	}
}

// Scoring around gaps is allowed, but the result says so and the confidence is
// discounted by the share of facts nobody stated.
func TestPartialResultDiscountsConfidence(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.2})
	e := engine(t, &mock.Judge{Seed: 1, FixturesDir: dir})

	full, err := e.Check(context.Background(), idea.With("why_now", "the models got cheap"), Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.Partial || len(full.Missing) != 0 || !near(full.CompositeConfidence, weightedConfidence(full)) {
		t.Fatalf("nothing missing: partial=%v missing=%+v confidence=%v", full.Partial, full.Missing, full.CompositeConfidence)
	}
	part, err := e.Check(context.Background(), idea, Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	// Every gap the run reports is open here, and _gaps.yaml asks for the
	// confidence to be halved when all of them are.
	gaps, err := rubric.Load(e.Files, e.Settings.RubricsDir, rubric.GapsName)
	if err != nil {
		t.Fatal(err)
	}
	if len(part.Missing) == 0 {
		t.Fatal("expected at least the why_now gap")
	}
	want := weightedConfidence(part) * (1 - gaps.ConfidencePenalty*float64(len(part.Missing))/float64(len(gaps.Questions)))
	if !part.Partial || part.Status != StatusOK || !near(part.CompositeConfidence, want) {
		t.Errorf("partial=%v status=%q confidence=%v, want %v", part.Partial, part.Status, part.CompositeConfidence, want)
	}
}

// weightedConfidence is the undiscounted composite confidence, read back off
// the dimensions the way Combine computes it: nouls do not count.
func weightedConfidence(res *Result) float64 {
	kinds := map[string]judge.Kind{}
	for _, a := range res.Answers {
		kinds[a.ID] = a.Kind
	}
	var sum, weights float64
	for _, d := range res.Dimensions {
		if d.Value == nil || d.Weight <= 0 || d.Polarity == 0 || kinds[d.ID] == judge.Noul {
			continue
		}
		sum, weights = sum+d.Confidence*d.Weight, weights+d.Weight
	}
	return sum / weights
}

func dimension(res *Result, id string) (Dimension, bool) {
	for _, d := range res.Dimensions {
		if d.ID == id {
			return d, true
		}
	}
	return Dimension{}, false
}

// extractingJudge answers like the mock, and also reads two fields out of any
// document — the stand-in for a backend that can fill in what the prose states.
type extractingJudge struct {
	*mock.Judge
	values map[string]string
	asked  [][]string
}

func (j *extractingJudge) Extract(_ context.Context, _, _ string, fields []string) (judge.Extraction, error) {
	j.asked = append(j.asked, fields)
	return judge.Extraction{Values: j.values, Model: "mock"}, nil
}

// A fact the description states is read out of it once, up front, so it is not
// reported missing and does not depend on how a gap question reads that run.
func TestExtractFillsOnlyEmptyFields(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, "has_why_now", judge.Answer{Noul: 0.1})
	j := &extractingJudge{Judge: &mock.Judge{Seed: 1, FixturesDir: dir},
		values: map[string]string{"why_now": "the models got cheap", "problem": "  ", "audience": "solo founders"}}
	e := engine(t, j)
	e.Settings.Extract = true

	in := idea.With("problem", "nobody can tell a good idea from a bad one")
	res, err := e.Check(context.Background(), in, Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(j.asked) != 1 || containsSub(strings.Join(j.asked[0], ","), "problem") {
		t.Fatalf("extraction asked for %v: a field the user filled is not asked for", j.asked)
	}
	if len(res.Extracted) != 2 || res.Extracted[0] != "audience" || res.Extracted[1] != "why_now" {
		t.Errorf("extracted = %v, want the two fields the document answered", res.Extracted)
	}
	for _, m := range res.Missing {
		if m.Fills == "why_now" {
			t.Error("why_now was read out of the document but reported missing")
		}
	}

	e.Settings.Extract = false
	res, err = e.Check(context.Background(), in, Options{Proceed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(j.asked) != 1 || len(res.Extracted) != 0 {
		t.Errorf("extract: false must not call the backend: asked=%v extracted=%v", j.asked, res.Extracted)
	}
}
