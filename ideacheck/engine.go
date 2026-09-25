package ideacheck

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/rubric"
)

// Engine runs checks. It is safe for concurrent use.
type Engine struct {
	Settings Settings
	Files    rubric.Reader
	Judge    judge.Judge
	// Writer reads, researches and writes (extract, research, explain) when that
	// is not the judge's job too; nil = the judge does it all. Every typed
	// question — including how a research finding relates to the idea — goes to
	// Judge either way.
	Writer judge.Judge
	// Cache keeps research findings between checks of the same idea; nil = none.
	Cache ResearchCache
	// Search is the search service ideacheck queries itself and Pages reads the
	// results' pages; nil Search = leave searching to the writer's own web tool.
	Search Searcher
	Pages  PageReader

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
	// Earlier is the needs_input result this check follows up (the TUI, after
	// its questions). Its gap and router answers stand; only the fields
	// answered since are reconsidered, since those gaps are no longer asked.
	Earlier *Result
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
	given := in // as the caller sent it: what a search is remembered under
	in, read := e.extract(ctx, in, res, o)
	if read != nil {
		extra = append(extra, *read)
	}
	state := in.State()
	unknown := unstated(gaps.Questions, in)
	k := known{in: in, gaps: gaps, earlier: earlier(o.Earlier)}
	gapAsk, held := settle(unknown, state, k)
	route := router.Questions[0]
	routeAnswer, routed := k.earlier[route.ID]
	routed = routed && !routeAnswer.Failed()
	asked := append([]judge.Question{}, gapAsk...)
	if o.Rubric == "" && !routed { // a forced rubric leaves nothing for the router to decide
		asked = append(asked, route)
	}
	announce(o.Events, StagePreflight, unknown, held)
	pre := e.fanout(ctx, state, asked, o, StagePreflight)
	gapAnswers := restore(pre[:len(gapAsk)], held, len(unknown))
	res.Answers = append([]judge.Answer{}, gapAnswers...)
	res.Missing = FindMissing(gaps, gapAnswers, in)
	switch {
	case o.Rubric != "":
		res.IdeaType = &IdeaType{Choice: o.Rubric, Confidence: 1}
	case routed:
		announce(o.Events, StagePreflight, router.Questions, map[int]judge.Answer{0: routeAnswer})
	default:
		routeAnswer = pre[len(gapAsk)]
	}
	if o.Rubric == "" {
		res.IdeaType = ideaType(routeAnswer)
		res.Answers = append(res.Answers, routeAnswer)
	}
	k.answers, k.earlier = byID(gapAnswers), nil

	// The rubric is known before research, so only what it reads is looked up.
	name, warnings := e.pickRubric(o.Rubric, router.Fallback, routeAnswer)
	rb, err := rubric.Load(e.Files, e.Settings.RubricsDir, name)
	if err != nil {
		return nil, err
	}
	if err := e.derivable(rb, gaps); err != nil {
		return nil, err
	}
	// A fact the web can answer is not worth stopping to ask a person for.
	plan, err := e.plan(in, res, rb)
	if err != nil {
		return nil, err
	}
	if open := Unanswered(res.Missing, in, append(plan.covers(), o.Answered...)); len(open) > 0 && !o.Proceed {
		res.Status = StatusNeedsInput
		return e.finish(res, start, extra...), nil
	}
	state, searched := e.research(ctx, plan, given, state, res, o)
	extra = append(extra, searched...)
	res.Missing = settled(res.Missing, plan, res.Research)
	open := Unanswered(res.Missing, in, o.Answered)
	res.Partial = len(open) > 0

	res.Warnings = append(res.Warnings, warnings...)
	res.Rubric = &RubricRef{Name: rb.Name, Hash: rb.Hash}

	k.research = res.Research
	ask, held := settle(rb.Questions, state, k)
	announce(o.Events, StageScore, rb.Questions, held)
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

// writer is the backend that reads, researches and writes.
func (e *Engine) writer() judge.Judge {
	if e.Writer != nil {
		return e.Writer
	}
	return e.Judge
}

// FollowUps is what is worth asking a person after a needs_input result: gaps
// still open, minus what research is about to look up, most important first
// and no more than the gaps rubric's ask_limit. Asking for everything at once
// is how a one-line idea turns into a form.
func (e *Engine) FollowUps(res *Result, in Intake, answered []string) []Missing {
	plan, _ := e.plan(in, nil, e.chosen(res))
	open := Unanswered(res.Missing, in, append(plan.covers(), answered...))
	gaps, err := rubric.Load(e.Files, e.Settings.RubricsDir, rubric.GapsName)
	if err == nil && gaps.AskLimit > 0 && len(open) > gaps.AskLimit {
		open = open[:gaps.AskLimit]
	}
	return open
}

// chosen is the rubric a result was, or will be, scored with: the idea type's
// own, else the router's fallback; nil when neither loads.
func (e *Engine) chosen(res *Result) *rubric.Rubric {
	if res.IdeaType != nil {
		if rb, err := rubric.Load(e.Files, e.Settings.RubricsDir, res.IdeaType.Choice); err == nil {
			return rb
		}
	}
	router, err := rubric.Load(e.Files, e.Settings.RubricsDir, rubric.RouterName)
	if err != nil {
		return nil
	}
	rb, _ := rubric.Load(e.Files, e.Settings.RubricsDir, router.Fallback)
	return rb
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
	writer := ""
	if e.Writer != nil && e.Writer.Name() != e.Judge.Name() {
		writer = e.Writer.Name()
	}
	return &Result{
		ID:           id,
		Status:       StatusOK,
		Backend:      e.Judge.Name(),
		Writer:       writer,
		ConfigHash:   e.Settings.hash(),
		CreatedAt:    start.UTC().Format(time.RFC3339),
		Missing:      []Missing{},
		Dimensions:   []Dimension{},
		TopStrengths: []Contribution{},
		TopRisks:     []Contribution{},
	}
}

// loadPreflight loads the two always-run rubrics and checks every gap can be filled.
func (e *Engine) loadPreflight() (gaps, router *rubric.Rubric, err error) {
	if gaps, err = rubric.Load(e.Files, e.Settings.RubricsDir, rubric.GapsName); err != nil {
		return nil, nil, err
	}
	for _, q := range gaps.Questions {
		if !KnownField(q.Fills) {
			return nil, nil, fmt.Errorf("%s: %s fills unknown intake field %q", rubric.GapsName, q.ID, q.Fills)
		}
	}
	if err := e.derivable(gaps, gaps); err != nil {
		return nil, nil, err
	}
	router, err = rubric.Load(e.Files, e.Settings.RubricsDir, rubric.RouterName)
	return gaps, router, err
}

func byID(answers []judge.Answer) map[string]judge.Answer {
	out := make(map[string]judge.Answer, len(answers))
	for _, a := range answers {
		out[a.ID] = a
	}
	return out
}

func (e *Engine) fanout(ctx context.Context, state judge.State, qs []judge.Question, o Options, stage string) []judge.Answer {
	if len(qs) == 0 {
		return nil
	}
	s := e.Settings
	opts := judge.Options{
		MaxConcurrent:   s.MaxConcurrent,
		QuestionTimeout: s.Timeouts.Question,
		BatchTimeout:    s.Timeouts.Batch,
		Sequential:      o.Sequential,
		Batch:           s.Batch,
		Retry:           s.Retry,
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

// unstated are the gap questions worth asking: a field the intake already
// fills (given, or read out of the document) is not missing, however the
// prose reads, so asking the judge about it buys nothing.
func unstated(gaps []judge.Question, in Intake) []judge.Question {
	var out []judge.Question
	for _, q := range gaps {
		if !in.Has(q.Fills) {
			out = append(out, q)
		}
	}
	return out
}

// FindMissing turns gap nouls below the threshold into follow-up questions, in
// the gaps rubric's order. A gap question that failed, or was never asked, is
// not evidence of a gap — and neither is one whose field the intake fills.
func FindMissing(gaps *rubric.Rubric, answers []judge.Answer, in Intake) []Missing {
	given := byID(answers)
	missing := []Missing{}
	for _, q := range gaps.Questions {
		a, ok := given[q.ID]
		if ok && !a.Failed() && a.Noul < gaps.Threshold && !in.Has(q.Fills) {
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

// settle takes out of qs what the judge need not be asked, keyed by index for
// restore. A question whose `requires:` state was not given is held back
// unanswered: scoring a dimension from material we do not have (founder fit
// with no profile) reads as a judgment of the idea when it is really a
// judgment of the input. A question an earlier run of this check answered
// keeps that answer, and one whose `derive:` rule can decide is answered by it.
func settle(qs []judge.Question, s judge.State, k known) (ask []judge.Question, held map[int]judge.Answer) {
	held = map[int]judge.Answer{}
	for i, q := range qs {
		if absent := absentState(q, s); len(absent) > 0 {
			held[i] = judge.Answer{ID: q.ID, Kind: q.Kind, Probabilities: map[string]float64{}, Err: "no " + strings.Join(absent, " or ") + " given"}
			continue
		}
		if a, ok := k.earlier[q.ID]; ok {
			held[i] = a
			continue
		}
		if a, ok := derive(q, k); ok {
			held[i] = a
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

// pickRubric honors a forced rubric, else the router's choice, else the router's fallback.
func (e *Engine) pickRubric(forced, fallback string, route judge.Answer) (string, []string) {
	if forced != "" {
		return forced, nil
	}
	if route.Failed() {
		return fallback, []string{fmt.Sprintf("could not classify the idea (%s); using the %s rubric", route.Err, fallback)}
	}
	names, _ := rubric.Names(e.Files, e.Settings.RubricsDir)
	if route.Choice == rubric.Other || !contains(names, route.Choice) {
		return fallback, []string{fmt.Sprintf("idea type %q has no rubric; using the %s rubric", route.Choice, fallback)}
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
		if answers[i].Method == MethodDerived {
			d.Derived = q.Derive.Rule()
		}
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
	v, err := Decide(rb.Verdict.For(e.Judge.Name()), agg)
	if err != nil {
		return err
	}
	res.Composite, res.CompositeConfidence = agg.Composite, agg.Confidence
	res.Verdict, res.VerdictReason = v.Label, v.Reason
	res.TopStrengths = append(res.TopStrengths, agg.Strengths...)
	res.TopRisks = append(res.TopRisks, agg.Risks...)
	return nil
}

// finish fills the fields derived from the full answer set. written are the
// writer's calls (extract, research, summary): not answers, but still paid for.
func (e *Engine) finish(res *Result, start time.Time, written ...judge.Answer) *Result {
	res.Timing.TotalMS = e.now().Sub(start).Milliseconds()
	var slowest int64 = -1
	for _, a := range res.Answers {
		if a.LatencyMS > slowest {
			slowest, res.Timing.SlowestQuestion = a.LatencyMS, a.ID
		}
		if res.Model == "" && !a.Failed() && a.Method != MethodDerived {
			res.Model, res.Method = a.Model, a.Method
		}
	}
	res.Cost = e.cost(res.Answers, written)
	res.CostEstimateUSD = res.Cost.USD
	return res
}

const perMTok = 1_000_000

// cost totals the check's spend. A provider-reported cost wins; other tokens
// are priced from config; a local model costs nothing. The judge and the writer
// may bill differently (a free local judge next to a paid writer), so each
// call is priced under its own backend's billing.
func (e *Engine) cost(judged, written []judge.Answer) Cost {
	c := Cost{Basis: CostPriced}
	groups := []struct {
		answers []judge.Answer
		billing string
	}{
		{judged, e.Settings.Judge.Billing},
		{written, e.Settings.Writer.Billing},
	}
	unpriced, paid, subscription, named := "", false, false, false
	for _, g := range groups {
		for _, a := range g.answers {
			c.TokensIn += a.TokensIn
			c.TokensCached += a.TokensCached
			c.TokensOut += a.TokensOut
			used := a.TokensIn+a.TokensOut > 0 || a.CostUSD > 0
			// The model named is one that was paid for: next to a free judge,
			// that is the writer.
			if a.Model != "" && (c.Model == "" || (used && !named)) {
				c.Model, named = a.Model, used
			}
			paid = paid || (used && g.billing != BillingLocal)
			subscription = subscription || (used && g.billing == BillingSubscription)
			switch p, ok := e.Settings.Pricing.Find(a.Model); {
			case a.CostUSD > 0:
				c.USD += a.CostUSD
				c.Basis = CostReported
			case g.billing == BillingLocal || a.TokensIn+a.TokensOut == 0:
			case ok:
				c.USD += cost(p, a)
				c.Price = &p
			default:
				unpriced = a.Model
			}
		}
	}
	switch {
	case !paid && c.TokensIn+c.TokensOut > 0:
		c.Basis, c.Note = CostFree, "local model: no per-token charge"
	case c.TokensIn+c.TokensOut == 0 && c.USD == 0:
		c.Basis, c.Note = CostUnpriced, "the backend reported no token usage"
		if e.Settings.Judge.Billing == BillingLocal {
			c.Basis, c.Note = CostFree, "local model: no per-token charge"
		}
	case unpriced != "" && c.Basis != CostReported:
		c.Basis, c.Note = CostUnpriced, fmt.Sprintf("no price for %q under pricing: in config.yaml", unpriced)
	case subscription && c.Basis == CostReported:
		c.Note = "API list price as reported by the CLI; your subscription covers it"
	case subscription:
		c.Note = "API list price; your subscription covers it"
	}
	return c
}

// Backend billing modes (backends.<name>.billing); "" and "api" mean pay per token.
const (
	BillingSubscription = "subscription"
	BillingLocal        = "local"
)

func cost(p Price, a judge.Answer) float64 {
	cached := min(a.TokensCached, a.TokensIn)
	rate := p.CachedIn
	if rate == 0 {
		rate = p.In
	}
	return (float64(a.TokensIn-cached)*p.In + float64(cached)*rate + float64(a.TokensOut)*p.Out) / perMTok
}
