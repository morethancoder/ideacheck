package judge

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

type EventType string

const (
	EventStarted  EventType = "started"
	EventAnswered EventType = "answered"
	EventFailed   EventType = "failed"
)

// Event is emitted as each question starts and lands, so a UI can render progress.
type Event struct {
	Type       EventType `json:"type"`
	QuestionID string    `json:"question_id"`
	Answer     *Answer   `json:"answer,omitempty"`
}

type RetryPolicy struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
}

const defaultMaxConcurrent = 16

// Options controls one fan-out. The caller owns Events and must keep draining it
// until Run returns; Run never closes it.
type Options struct {
	MaxConcurrent   int // 0 = backend capability, then defaultMaxConcurrent
	QuestionTimeout time.Duration
	BatchTimeout    time.Duration
	Sequential      bool // debug: one question at a time
	Batch           bool // use EvaluateBatch when the backend supports it
	Retry           RetryPolicy
	Events          chan<- Event

	// Test seams; nil means real time and random jitter.
	Sleep  func(ctx context.Context, d time.Duration) error
	Jitter func(d time.Duration) time.Duration
}

// Run evaluates every question and returns answers in question order. A question
// that fails or times out yields an Answer with Err set; Run itself never fails.
func Run(ctx context.Context, j Judge, state State, qs []Question, o Options) []Answer {
	o = o.withDefaults()
	if o.BatchTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.BatchTimeout)
		defer cancel()
	}
	if bj, ok := j.(BatchJudge); ok && o.Batch && j.Capabilities().NativeBatch {
		return runBatch(ctx, bj, state, qs, o)
	}
	return runEach(ctx, j, state, qs, o)
}

func (o Options) withDefaults() Options {
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Jitter == nil {
		o.Jitter = equalJitter
	}
	if o.Retry.MaxAttempts < 1 {
		o.Retry.MaxAttempts = 1
	}
	return o
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// equalJitter keeps half the delay and randomizes the other half.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half)
}

// limit resolves how many questions may be in flight at once.
func (o Options) limit(c Capabilities) int {
	switch {
	case o.Sequential:
		return 1
	case o.MaxConcurrent > 0:
		return o.MaxConcurrent
	case c.MaxConcurrent > 0:
		return c.MaxConcurrent
	}
	return defaultMaxConcurrent
}

func runEach(ctx context.Context, j Judge, state State, qs []Question, o Options) []Answer {
	answers := make([]Answer, len(qs))
	var g errgroup.Group
	g.SetLimit(o.limit(j.Capabilities()))
	for i, q := range qs {
		g.Go(func() error {
			answers[i] = evaluateOne(ctx, j, state, q, o)
			return nil
		})
	}
	_ = g.Wait()
	return answers
}

func evaluateOne(ctx context.Context, j Judge, state State, q Question, o Options) Answer {
	o.emit(ctx, Event{Type: EventStarted, QuestionID: q.ID})
	start := time.Now()
	var ans Answer
	err := o.retry(ctx, func(ctx context.Context) error {
		if o.QuestionTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, o.QuestionTimeout)
			defer cancel()
		}
		var err error
		ans, err = j.Evaluate(ctx, state.Sub(q.Uses), q)
		return err
	})
	ans = finish(ans, q, err, time.Since(start))
	o.emitResult(ctx, ans)
	return ans
}

// finish stamps identity and latency, and turns an error into a per-question failure.
func finish(ans Answer, q Question, err error, took time.Duration) Answer {
	if err != nil {
		ans = Answer{Err: err.Error()}
	}
	ans.ID, ans.Kind = q.ID, q.Kind
	if ans.Probabilities == nil {
		ans.Probabilities = defaultProbabilities(ans)
	}
	if ans.LatencyMS == 0 {
		ans.LatencyMS = took.Milliseconds()
	}
	return ans
}

// defaultProbabilities keeps the output contract honest: probabilities is always
// an object. A noul's distribution is implied by P(yes); a failure has none.
func defaultProbabilities(a Answer) map[string]float64 {
	if a.Kind == Noul && !a.Failed() {
		return map[string]float64{"yes": a.Noul, "no": 1 - a.Noul}
	}
	return map[string]float64{}
}

// retry runs fn until it succeeds, fails non-retryably, or attempts run out.
func (o Options) retry(ctx context.Context, fn func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		after, retryable := retryAfter(err)
		if !retryable || attempt >= o.Retry.MaxAttempts || ctx.Err() != nil {
			return err
		}
		if o.Sleep(ctx, o.delay(attempt, after)) != nil {
			return err
		}
	}
}

// delay honors a server Retry-After; otherwise exponential backoff with jitter.
func (o Options) delay(attempt int, after time.Duration) time.Duration {
	if after > 0 {
		return after
	}
	return o.Jitter(backoff(attempt, o.Retry.Base, o.Retry.Max))
}

// backoff is base·2^(attempt-1), capped at max (when max > 0).
func backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if max > 0 && d >= max {
			return max
		}
	}
	if max > 0 && d > max {
		return max
	}
	return d
}

func (o Options) emit(ctx context.Context, e Event) {
	if o.Events == nil {
		return
	}
	select {
	case o.Events <- e:
	case <-ctx.Done():
	}
}

func (o Options) emitResult(ctx context.Context, a Answer) {
	t := EventAnswered
	if a.Failed() {
		t = EventFailed
	}
	o.emit(ctx, Event{Type: t, QuestionID: a.ID, Answer: &a})
}

// runBatch sends one request per distinct Uses set, so batching still obeys the
// context-rot rule: questions only share a request when they need the same state.
func runBatch(ctx context.Context, bj BatchJudge, state State, qs []Question, o Options) []Answer {
	answers := make([]Answer, len(qs))
	var g errgroup.Group
	if o.Sequential {
		g.SetLimit(1)
	}
	for _, idx := range groupByUses(qs) {
		g.Go(func() error {
			group := pick(qs, idx)
			for k, a := range evaluateGroup(ctx, bj, state, group, o) {
				answers[idx[k]] = a
			}
			return nil
		})
	}
	_ = g.Wait()
	return answers
}

// groupByUses returns question indexes grouped by identical Uses, in first-seen order.
func groupByUses(qs []Question) [][]int {
	var groups [][]int
	at := map[string]int{}
	for i, q := range qs {
		key := strings.Join(q.Uses, "\x00")
		n, ok := at[key]
		if !ok {
			n = len(groups)
			at[key] = n
			groups = append(groups, nil)
		}
		groups[n] = append(groups[n], i)
	}
	return groups
}

func pick(qs []Question, idx []int) []Question {
	out := make([]Question, len(idx))
	for k, i := range idx {
		out[k] = qs[i]
	}
	return out
}

func evaluateGroup(ctx context.Context, bj BatchJudge, state State, qs []Question, o Options) []Answer {
	for _, q := range qs {
		o.emit(ctx, Event{Type: EventStarted, QuestionID: q.ID})
	}
	start := time.Now()
	var got []Answer
	err := o.retry(ctx, func(ctx context.Context) error {
		var err error
		got, err = bj.EvaluateBatch(ctx, state.Sub(qs[0].Uses), qs)
		return err
	})
	out := matchAnswers(qs, got, err, time.Since(start))
	for _, a := range out {
		o.emitResult(ctx, a)
	}
	return out
}

// matchAnswers aligns batch answers to questions by ID; a question the backend
// skipped becomes a per-question failure rather than failing the batch.
func matchAnswers(qs []Question, got []Answer, err error, took time.Duration) []Answer {
	byID := make(map[string]Answer, len(got))
	for _, a := range got {
		byID[a.ID] = a
	}
	out := make([]Answer, len(qs))
	for i, q := range qs {
		a, ok := byID[q.ID]
		qerr := err
		if qerr == nil && !ok {
			qerr = errNoAnswer
		}
		out[i] = finish(a, q, qerr, took)
	}
	return out
}
