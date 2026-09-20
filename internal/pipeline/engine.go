package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/rubric"
)

// Engine runs checks. It is safe for concurrent use.
type Engine struct {
	Config config.Config
	Files  rubric.Reader
	Judge  judge.Judge

	Now   func() time.Time // nil = time.Now
	NewID func() string    // nil = "chk_" + ULID
}

// Options vary per check.
type Options struct {
	ID         string // preset check id (the server needs it before the check ends); "" = generate
	Rubric     string // force a rubric instead of routing
	Sequential bool
	// Proceed scores the idea even when gaps remain, reporting them next to the
	// scores (status ok, partial true). It is the default everywhere except the
	// TUI, which can ask; without it a gap stops the check with needs_input.
	Proceed bool
	// Answered lists intake fields the user was already offered and chose to
	// leave blank (the TUI's detail steps). A gap on such a field, or on a field
	// that has a value, is reported but never stops the check: asking again for
	// something the user just answered is noise.
	Answered []string
	// Events receives live progress. The caller must drain it until Check returns;
	// Check never closes it.
	Events chan<- Event
}

// Check runs the whole pipeline. It returns an error only for problems that stop
// a result being produced at all (bad rubric, bad gate); model failures land in
// the Result.
func (e *Engine) Check(ctx context.Context, in Intake, o Options) (*Result, error) {
	start := e.now()
	res := e.newResult(start)
	if o.ID != "" {
		res.ID = o.ID
	}
	gaps, router, err := e.loadPreflight()
	if err != nil {
		return nil, err
	}
	var extra []judge.Answer
	in, read := e.extract(ctx, in, res, o)
	if read != nil {
		extra = append(extra, *read)
	}
	state := in.State()
	pre := e.fanout(ctx, state, append(append([]judge.Question{}, gaps.Questions...), router.Questions...), o, StagePreflight)
	gapAnswers, routeAnswer := pre[:len(gaps.Questions)], pre[len(gaps.Questions)]
	res.Answers = pre
	res.Missing = FindMissing(gaps, gapAnswers, in)
	res.IdeaType = ideaType(routeAnswer)

	open := Unanswered(res.Missing, in, o.Answered)
	if len(open) > 0 && !o.Proceed {
		res.Status = StatusNeedsInput
		return e.finish(res, start, extra...), nil
	}
	res.Partial = len(open) > 0

	name, warnings := e.pickRubric(o.Rubric, routeAnswer)
	res.Warnings = warnings
	rb, err := rubric.Load(e.Files, e.Config.RubricsDir, name)
	if err != nil {
		return nil, err
	}
	res.Rubric = &RubricRef{Name: rb.Name, Hash: rb.Hash}

	ask, held := withheld(rb.Questions, state)
	answers := restore(e.fanout(ctx, state, ask, o, StageScore), held, len(rb.Questions))
	res.Answers = append(res.Answers, answers...)
	if err := e.conclude(res, rb, answers); err != nil {
		return nil, err
	}
	if res.Partial {
		res.CompositeConfidence *= confidenceFactor(gaps, len(open))
		res.Warnings = append(res.Warnings, fmt.Sprintf("scored with %d fact(s) the description does not state: confidence is discounted; see missing[]", len(open)))
	}
	if a := e.explain(ctx, res, state, rb, answers, o); a != nil {
		extra = append(extra, *a)
	}
	return e.finish(res, start, extra...), nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// NewID returns a fresh check id.
func NewID() string { return "chk_" + ulid.Make().String() }

func (e *Engine) newResult(start time.Time) *Result {
	id := NewID()
	if e.NewID != nil {
		id = e.NewID()
	}
	return &Result{
		ID:           id,
		Status:       StatusOK,
		Backend:      e.Judge.Name(),
		ConfigHash:   e.Config.Hash(),
		CreatedAt:    start.UTC().Format(time.RFC3339),
		Missing:      []Missing{},
		Dimensions:   []Dimension{},
		TopStrengths: []Contribution{},
		TopRisks:     []Contribution{},
	}
}

// loadPreflight loads the two always-run rubrics and checks every gap can be filled.
func (e *Engine) loadPreflight() (gaps, router *rubric.Rubric, err error) {
	if gaps, err = rubric.Load(e.Files, e.Config.RubricsDir, rubric.GapsName); err != nil {
		return nil, nil, err
	}
	for _, q := range gaps.Questions {
		if !KnownField(q.Fills) {
			return nil, nil, fmt.Errorf("%s: %s fills unknown intake field %q", rubric.GapsName, q.ID, q.Fills)
		}
	}
	router, err = rubric.Load(e.Files, e.Config.RubricsDir, rubric.RouterName)
	return gaps, router, err
}

func (e *Engine) fanout(ctx context.Context, state judge.State, qs []judge.Question, o Options, stage string) []judge.Answer {
	c := e.Config
	opts := judge.Options{
		MaxConcurrent:   c.MaxConcurrent(),
		QuestionTimeout: c.Timeouts.Question,
		BatchTimeout:    c.Timeouts.Batch,
		Sequential:      o.Sequential,
		Batch:           c.Active().Batch,
		Retry:           judge.RetryPolicy{MaxAttempts: c.Retries.MaxAttempts, Base: c.Retries.BaseBackoff, Max: c.Retries.MaxBackoff},
	}
	if o.Events == nil {
		return judge.Run(ctx, e.Judge, state, qs, opts)
	}
	// Buffered for every event the fan-out can emit, so a slow UI never stalls a question.
	raw := make(chan judge.Event, 2*len(qs))
	relayed := make(chan struct{})
	go func() {
		relay(raw, o.Events, stage, qs)
		close(relayed)
	}()
	opts.Events = raw
	answers := judge.Run(ctx, e.Judge, state, qs, opts)
	close(raw)
	<-relayed
	return answers
}

// FindMissing turns gap nouls below the threshold into follow-up questions. A gap
// question that failed is not evidence of a gap, so it is skipped — and neither
// is one whose field the intake already fills: that fact is not missing, however
// the prose reads.
func FindMissing(gaps *rubric.Rubric, answers []judge.Answer, in Intake) []Missing {
	missing := []Missing{}
	for i, q := range gaps.Questions {
		a := answers[i]
		if !a.Failed() && a.Noul < gaps.Threshold && !in.Has(q.Fills) {
			missing = append(missing, Missing{ID: q.ID, Probability: a.Noul, Ask: q.Ask, Fills: q.Fills})
		}
	}
	return missing
}

// confidenceFactor discounts the composite confidence for facts nobody stated.
// The size of the discount is the gaps rubric's business, not this code's:
// confidence_penalty 1 means "all gaps open, no confidence left".
func confidenceFactor(gaps *rubric.Rubric, open int) float64 {
	if gaps.ConfidencePenalty <= 0 || len(gaps.Questions) == 0 {
		return 1
	}
	return 1 - gaps.ConfidencePenalty*float64(open)/float64(len(gaps.Questions))
}

// withheld holds back questions whose `requires:` state was not given. Scoring a
// dimension from material we do not have (founder fit with no profile) reads as
// a judgment of the idea when it is really a judgment of the input.
func withheld(qs []judge.Question, s judge.State) (ask []judge.Question, held map[int]judge.Answer) {
	held = map[int]judge.Answer{}
	for i, q := range qs {
		if absent := absentState(q, s); len(absent) > 0 {
			held[i] = judge.Answer{ID: q.ID, Kind: q.Kind, Err: "no " + strings.Join(absent, " or ") + " given"}
			continue
		}
		ask = append(ask, q)
	}
	return ask, held
}

// absentState names the state a question requires that the check does not have.
func absentState(q judge.Question, s judge.State) []string {
	var absent []string
	for _, key := range q.Requires {
		if v, ok := s[key]; !ok || v == nil {
			absent = append(absent, key)
		}
	}
	return absent
}

// restore puts the withheld answers back at their question's index, so answers
// stay aligned with the rubric.
func restore(answers []judge.Answer, held map[int]judge.Answer, n int) []judge.Answer {
	if len(held) == 0 {
		return answers
	}
	out, next := make([]judge.Answer, n), 0
	for i := range out {
		if a, ok := held[i]; ok {
			out[i] = a
			continue
		}
		out[i], next = answers[next], next+1
	}
	return out
}

// Unanswered is the part of missing worth asking about: gaps whose field is
// still empty and was not already offered to the user.
func Unanswered(missing []Missing, in Intake, answered []string) []Missing {
	out := []Missing{}
	for _, m := range missing {
		if !in.Has(m.Fills) && !contains(answered, m.Fills) {
			out = append(out, m)
		}
	}
	return out
}

func ideaType(a judge.Answer) *IdeaType {
	if a.Failed() {
		return nil
	}
	return &IdeaType{Choice: a.Choice, Confidence: a.Confidence}
}

// pickRubric honors a forced rubric, else the router's choice, else the fallback.
func (e *Engine) pickRubric(forced string, route judge.Answer) (string, []string) {
	if forced != "" {
		return forced, nil
	}
	if route.Failed() {
		return rubric.Fallback, []string{fmt.Sprintf("could not classify the idea (%s); using the %s rubric", route.Err, rubric.Fallback)}
	}
	names, _ := rubric.Names(e.Files, e.Config.RubricsDir)
	if route.Choice == rubric.Other || !contains(names, route.Choice) {
		return rubric.Fallback, []string{fmt.Sprintf("idea type %q has no rubric; using the %s rubric", route.Choice, rubric.Fallback)}
	}
	return route.Choice, nil
}

// conclude aggregates and decides. With nothing answered there is no verdict.
func (e *Engine) conclude(res *Result, rb *rubric.Rubric, answers []judge.Answer) error {
	agg := Combine(rb.Questions, answers)
	reasons := map[string]string{}
	for _, a := range answers {
		reasons[a.ID] = a.Err
	}
	for i, q := range rb.Questions {
		d := Dimension{ID: q.ID, Weight: q.Weight, Polarity: q.Polarity, Confidence: answers[i].Confidence, Error: answers[i].Err}
		if v, ok := agg.Values[q.ID]; ok {
			d.Value = &v
		}
		res.Dimensions = append(res.Dimensions, d)
	}
	for _, id := range agg.Failed {
		res.Warnings = append(res.Warnings, fmt.Sprintf("question %s is excluded from the composite: %s", id, reasons[id]))
	}
	if agg.Answered == 0 {
		res.Status = StatusError
		res.Error = "no weighted question was answered; see answers[].error"
		return nil
	}
	v, err := Decide(rb.Verdict, agg)
	if err != nil {
		return err
	}
	res.Composite, res.CompositeConfidence = agg.Composite, agg.Confidence
	res.Verdict, res.VerdictReason = v.Label, v.Reason
	res.TopStrengths = append(res.TopStrengths, agg.Strengths...)
	res.TopRisks = append(res.TopRisks, agg.Risks...)
	return nil
}

// finish fills the fields derived from the full answer set. extra are calls
// that are not question answers (the summary) but still cost money.
func (e *Engine) finish(res *Result, start time.Time, extra ...judge.Answer) *Result {
	res.Timing.TotalMS = e.now().Sub(start).Milliseconds()
	var slowest int64 = -1
	for _, a := range res.Answers {
		if a.LatencyMS > slowest {
			slowest, res.Timing.SlowestQuestion = a.LatencyMS, a.ID
		}
		if res.Model == "" && !a.Failed() {
			res.Model, res.Method = a.Model, a.Method
		}
	}
	res.Cost = e.cost(append(append([]judge.Answer{}, res.Answers...), extra...))
	res.CostEstimateUSD = res.Cost.USD
	return res
}

const perMTok = 1_000_000

// cost totals the check's spend. A provider-reported cost wins; other tokens
// are priced from config; a local model costs nothing.
func (e *Engine) cost(answers []judge.Answer) Cost {
	c := Cost{Basis: CostPriced}
	billing := e.Config.Active().Billing
	for _, a := range answers {
		c.TokensIn += a.TokensIn
		c.TokensCached += a.TokensCached
		c.TokensOut += a.TokensOut
		if c.Model == "" && a.Model != "" {
			c.Model = a.Model
		}
	}
	unpriced := ""
	for _, a := range answers {
		switch p, ok := e.Config.PriceFor(a.Model); {
		case a.CostUSD > 0:
			c.USD += a.CostUSD
			c.Basis = CostReported
		case billing == BillingLocal || a.TokensIn+a.TokensOut == 0:
		case ok:
			c.USD += cost(p, a)
			c.Price = &p
		default:
			unpriced = a.Model
		}
	}
	switch {
	case billing == BillingLocal:
		c.Basis, c.Note = CostFree, "local model: no per-token charge"
	case c.TokensIn+c.TokensOut == 0 && c.USD == 0:
		c.Basis, c.Note = CostUnpriced, "the backend reported no token usage"
	case unpriced != "" && c.Basis != CostReported:
		c.Basis, c.Note = CostUnpriced, fmt.Sprintf("no price for %q under pricing: in config.yaml", unpriced)
	case billing == BillingSubscription && c.Basis == CostReported:
		c.Note = "API list price as reported by the CLI; your subscription covers it"
	case billing == BillingSubscription:
		c.Note = "API list price; your subscription covers it"
	}
	return c
}

// Backend billing modes (backends.<name>.billing); "" and "api" mean pay per token.
const (
	BillingSubscription = "subscription"
	BillingLocal        = "local"
)

func cost(p config.Price, a judge.Answer) float64 {
	cached := min(a.TokensCached, a.TokensIn)
	rate := p.CachedIn
	if rate == 0 {
		rate = p.In
	}
	return (float64(a.TokensIn-cached)*p.In + float64(cached)*rate + float64(a.TokensOut)*p.Out) / perMTok
}
