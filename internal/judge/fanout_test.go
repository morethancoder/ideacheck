package judge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJudge is a scriptable Judge. fn decides each call's outcome.
type fakeJudge struct {
	caps     Capabilities
	fn       func(ctx context.Context, s State, q Question, call int) (Answer, error)
	mu       sync.Mutex
	calls    map[string]int
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeJudge) Name() string               { return "fake" }
func (f *fakeJudge) Capabilities() Capabilities { return f.caps }

func (f *fakeJudge) Evaluate(ctx context.Context, s State, q Question) (Answer, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for p := f.peak.Load(); n > p && !f.peak.CompareAndSwap(p, n); p = f.peak.Load() {
	}
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[q.ID]++
	call := f.calls[q.ID]
	f.mu.Unlock()
	return f.fn(ctx, s, q, call)
}

func (f *fakeJudge) callsFor(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func nouls(n int) []Question {
	qs := make([]Question, n)
	for i := range qs {
		qs[i] = Question{ID: fmt.Sprintf("q%d", i), Kind: Noul, Uses: []string{"idea"}}
	}
	return qs
}

func sleeper(d time.Duration) func(context.Context, State, Question, int) (Answer, error) {
	return func(ctx context.Context, _ State, _ Question, _ int) (Answer, error) {
		select {
		case <-time.After(d):
			return Answer{Noul: 0.75}, nil
		case <-ctx.Done():
			return Answer{}, ctx.Err()
		}
	}
}

// The spec's hard requirement: wall time ≈ slowest question, not the sum.
func TestRunIsConcurrent(t *testing.T) {
	j := &fakeJudge{fn: sleeper(200 * time.Millisecond)}
	start := time.Now()
	got := Run(context.Background(), j, State{"idea": "x"}, nouls(12), Options{})
	if took := time.Since(start); took >= 600*time.Millisecond {
		t.Fatalf("12 x 200ms questions took %v, want < 600ms", took)
	}
	if len(got) != 12 {
		t.Fatalf("got %d answers, want 12", len(got))
	}
	for i, a := range got {
		if a.ID != fmt.Sprintf("q%d", i) || a.Kind != Noul || a.Noul != 0.75 || a.Failed() {
			t.Errorf("answer %d = %+v", i, a)
		}
	}
	if p := j.peak.Load(); p != 12 {
		t.Errorf("peak in-flight = %d, want 12", p)
	}
}

func TestRunSequentialRunsOneAtATime(t *testing.T) {
	j := &fakeJudge{fn: sleeper(5 * time.Millisecond)}
	Run(context.Background(), j, State{}, nouls(6), Options{Sequential: true, MaxConcurrent: 8})
	if p := j.peak.Load(); p != 1 {
		t.Fatalf("peak in-flight = %d, want 1", p)
	}
}

func TestRunHonorsMaxConcurrent(t *testing.T) {
	j := &fakeJudge{fn: sleeper(20 * time.Millisecond)}
	Run(context.Background(), j, State{}, nouls(9), Options{MaxConcurrent: 3})
	if p := j.peak.Load(); p != 3 {
		t.Fatalf("peak in-flight = %d, want 3", p)
	}
}

func TestLimit(t *testing.T) {
	cases := []struct {
		name string
		o    Options
		c    Capabilities
		want int
	}{
		{"default", Options{}, Capabilities{}, 16},
		{"capability", Options{}, Capabilities{MaxConcurrent: 32}, 32},
		{"option beats capability", Options{MaxConcurrent: 4}, Capabilities{MaxConcurrent: 32}, 4},
		{"sequential beats all", Options{Sequential: true, MaxConcurrent: 4}, Capabilities{MaxConcurrent: 32}, 1},
	}
	for _, c := range cases {
		if got := c.o.limit(c.c); got != c.want {
			t.Errorf("%s: limit = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOneFailureNeverFailsTheBatch(t *testing.T) {
	j := &fakeJudge{fn: func(_ context.Context, _ State, q Question, _ int) (Answer, error) {
		if q.ID == "q1" {
			return Answer{Noul: 0.9}, errors.New("boom")
		}
		return Answer{Noul: 0.4}, nil
	}}
	got := Run(context.Background(), j, State{}, nouls(3), Options{Retry: RetryPolicy{MaxAttempts: 4}})
	if got[0].Failed() || got[2].Failed() || got[0].Noul != 0.4 {
		t.Fatalf("healthy questions affected: %+v", got)
	}
	bad := got[1]
	if bad.Err != "boom" || bad.ID != "q1" || bad.Kind != Noul || bad.Noul != 0 {
		t.Fatalf("failed answer = %+v", bad)
	}
	if n := j.callsFor("q1"); n != 1 {
		t.Errorf("non-retryable error was tried %d times, want 1", n)
	}
}

func TestQuestionTimeoutIsPerQuestionAndRetried(t *testing.T) {
	j := &fakeJudge{fn: func(ctx context.Context, s State, q Question, call int) (Answer, error) {
		if q.ID == "q0" {
			return sleeper(time.Second)(ctx, s, q, call)
		}
		return Answer{Noul: 0.6}, nil
	}}
	var slept []time.Duration
	o := Options{
		QuestionTimeout: 20 * time.Millisecond,
		Retry:           RetryPolicy{MaxAttempts: 3, Base: time.Millisecond},
		Sleep:           func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
		Jitter:          func(d time.Duration) time.Duration { return d },
	}
	got := Run(context.Background(), j, State{}, nouls(2), o)
	if !got[0].Failed() || got[1].Failed() {
		t.Fatalf("got %+v", got)
	}
	if n := j.callsFor("q0"); n != 3 {
		t.Errorf("timed-out question tried %d times, want 3", n)
	}
	if len(slept) != 2 || slept[0] != time.Millisecond || slept[1] != 2*time.Millisecond {
		t.Errorf("slept %v, want [1ms 2ms]", slept)
	}
}

func TestBatchDeadlineStopsRetries(t *testing.T) {
	j := &fakeJudge{fn: sleeper(time.Second)}
	start := time.Now()
	got := Run(context.Background(), j, State{}, nouls(2), Options{
		BatchTimeout: 30 * time.Millisecond,
		Retry:        RetryPolicy{MaxAttempts: 5, Base: time.Millisecond},
	})
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("batch deadline ignored: took %v", took)
	}
	if !got[0].Failed() || !got[1].Failed() {
		t.Fatalf("got %+v", got)
	}
	if n := j.callsFor("q0"); n != 1 {
		t.Errorf("retried %d times after the batch deadline, want 1 attempt", n)
	}
}

func TestRetryHonorsRetryAfterThenSucceeds(t *testing.T) {
	j := &fakeJudge{fn: func(_ context.Context, _ State, _ Question, call int) (Answer, error) {
		if call < 3 {
			return Answer{}, &RetryableError{Err: errors.New("429"), After: 7 * time.Second}
		}
		return Answer{Noul: 0.8}, nil
	}}
	var slept []time.Duration
	got := Run(context.Background(), j, State{}, nouls(1), Options{
		Retry:  RetryPolicy{MaxAttempts: 4, Base: time.Second, Max: 5 * time.Second},
		Sleep:  func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
		Jitter: func(time.Duration) time.Duration { t.Fatal("jitter applied to Retry-After"); return 0 },
	})
	if got[0].Failed() || got[0].Noul != 0.8 {
		t.Fatalf("got %+v", got[0])
	}
	if len(slept) != 2 || slept[0] != 7*time.Second || slept[1] != 7*time.Second {
		t.Errorf("slept %v, want [7s 7s]", slept)
	}
}

func TestRetryStopsAtMaxAttempts(t *testing.T) {
	for _, max := range []int{0, 1, 2, 4} {
		j := &fakeJudge{fn: func(context.Context, State, Question, int) (Answer, error) {
			return Answer{}, &RetryableError{Err: errors.New("503")}
		}}
		got := Run(context.Background(), j, State{}, nouls(1), Options{
			Retry: RetryPolicy{MaxAttempts: max},
			Sleep: func(context.Context, time.Duration) error { return nil },
		})
		want := max
		if want < 1 {
			want = 1
		}
		if n := j.callsFor("q0"); n != want {
			t.Errorf("MaxAttempts=%d: %d calls, want %d", max, n, want)
		}
		if got[0].Err != "503" {
			t.Errorf("MaxAttempts=%d: Err = %q", max, got[0].Err)
		}
	}
}

func TestRetryStopsWhenSleepIsInterrupted(t *testing.T) {
	j := &fakeJudge{fn: func(context.Context, State, Question, int) (Answer, error) {
		return Answer{}, &RetryableError{Err: errors.New("503")}
	}}
	got := Run(context.Background(), j, State{}, nouls(1), Options{
		Retry: RetryPolicy{MaxAttempts: 5},
		Sleep: func(context.Context, time.Duration) error { return context.Canceled },
	})
	if n := j.callsFor("q0"); n != 1 || got[0].Err != "503" {
		t.Fatalf("calls=%d answer=%+v", n, got[0])
	}
}

func TestBackoff(t *testing.T) {
	const base, max = 500 * time.Millisecond, 5 * time.Second
	want := []time.Duration{base, time.Second, 2 * time.Second, 4 * time.Second, max, max}
	for i, w := range want {
		if got := backoff(i+1, base, max); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(4, time.Second, 0); got != 8*time.Second {
		t.Errorf("uncapped backoff(4) = %v, want 8s", got)
	}
	if got := backoff(1, 10*time.Second, max); got != max {
		t.Errorf("base above max = %v, want %v", got, max)
	}
	if got := backoff(3, time.Second, 4*time.Second); got != 4*time.Second {
		t.Errorf("backoff landing exactly on max = %v, want 4s", got)
	}
}

func TestEqualJitterBounds(t *testing.T) {
	const d = 100 * time.Millisecond
	for range 200 {
		if got := equalJitter(d); got < d/2 || got >= d {
			t.Fatalf("equalJitter(%v) = %v, want in [%v, %v)", d, got, d/2, d)
		}
	}
	if got := equalJitter(1); got != 1 {
		t.Errorf("equalJitter(1ns) = %v, want 1ns", got)
	}
	if got := equalJitter(0); got != 0 {
		t.Errorf("equalJitter(0) = %v, want 0", got)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleepCtx = %v", err)
	}
}

// Context-rot rule: a question sees only the state fields it lists in Uses.
func TestQuestionSeesOnlyTheStateItUses(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]State{}
	j := &fakeJudge{fn: func(_ context.Context, s State, q Question, _ int) (Answer, error) {
		mu.Lock()
		seen[q.ID] = s
		mu.Unlock()
		return Answer{}, nil
	}}
	qs := []Question{
		{ID: "a", Kind: Noul, Uses: []string{"idea"}},
		{ID: "b", Kind: Noul, Uses: []string{"idea", "profile", "absent"}},
	}
	Run(context.Background(), j, State{"idea": "i", "profile": "p", "secret": "s"}, qs, Options{})
	if len(seen["a"]) != 1 || seen["a"]["idea"] != "i" {
		t.Errorf("a saw %v", seen["a"])
	}
	if len(seen["b"]) != 2 || seen["b"]["profile"] != "p" {
		t.Errorf("b saw %v", seen["b"])
	}
}

func TestEvents(t *testing.T) {
	j := &fakeJudge{fn: func(_ context.Context, _ State, q Question, _ int) (Answer, error) {
		if q.ID == "q1" {
			return Answer{}, errors.New("nope")
		}
		return Answer{Noul: 0.3}, nil
	}}
	ch := make(chan Event, 16)
	Run(context.Background(), j, State{}, nouls(2), Options{Events: ch})
	close(ch)
	got := map[string][]Event{}
	for e := range ch {
		got[e.QuestionID] = append(got[e.QuestionID], e)
	}
	for id, last := range map[string]EventType{"q0": EventAnswered, "q1": EventFailed} {
		es := got[id]
		if len(es) != 2 || es[0].Type != EventStarted || es[0].Answer != nil || es[1].Type != last {
			t.Fatalf("%s events = %+v", id, es)
		}
		if es[1].Answer == nil || es[1].Answer.ID != id {
			t.Errorf("%s final event answer = %+v", id, es[1].Answer)
		}
	}
	if got["q0"][1].Answer.Noul != 0.3 || got["q1"][1].Answer.Err != "nope" {
		t.Errorf("event payloads wrong: %+v", got)
	}
}

func TestEmitDoesNotBlockAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		Options{Events: make(chan Event)}.emit(ctx, Event{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emit blocked on an undrained channel after cancel")
	}
}

func TestFinishKeepsBackendLatency(t *testing.T) {
	q := Question{ID: "x", Kind: Score}
	if a := finish(Answer{LatencyMS: 42}, q, nil, 9*time.Second); a.LatencyMS != 42 {
		t.Errorf("backend latency overwritten: %d", a.LatencyMS)
	}
	if a := finish(Answer{}, q, nil, 1500*time.Millisecond); a.LatencyMS != 1500 || a.ID != "x" || a.Kind != Score {
		t.Errorf("finish = %+v", a)
	}
}

func TestProbabilitiesIsNeverNull(t *testing.T) {
	noul := finish(Answer{Noul: 0.7}, Question{ID: "n", Kind: Noul}, nil, 0)
	if len(noul.Probabilities) != 2 || noul.Probabilities["yes"] != 0.7 || !near(noul.Probabilities["no"], 0.3) {
		t.Errorf("noul probabilities = %v", noul.Probabilities)
	}
	failed := finish(Answer{}, Question{ID: "n", Kind: Noul}, errors.New("x"), 0)
	if failed.Probabilities == nil || len(failed.Probabilities) != 0 {
		t.Errorf("failed probabilities = %v, want empty object", failed.Probabilities)
	}
	given := finish(Answer{Probabilities: map[string]float64{"a": 1}}, Question{ID: "c", Kind: Choice}, nil, 0)
	if len(given.Probabilities) != 1 {
		t.Errorf("backend probabilities overwritten: %v", given.Probabilities)
	}
}

func TestRetryAfterClassification(t *testing.T) {
	if d, ok := retryAfter(&RetryableError{Err: errors.New("x"), After: time.Second}); !ok || d != time.Second {
		t.Errorf("RetryableError: %v %v", d, ok)
	}
	wrapped := fmt.Errorf("wrap: %w", &RetryableError{Err: errors.New("x")})
	if d, ok := retryAfter(wrapped); !ok || d != 0 {
		t.Errorf("wrapped RetryableError: %v %v", d, ok)
	}
	if _, ok := retryAfter(context.DeadlineExceeded); !ok {
		t.Error("deadline exceeded should be retryable")
	}
	if _, ok := retryAfter(errors.New("plain")); ok {
		t.Error("plain error should not be retryable")
	}
	inner := errors.New("inner")
	if !errors.Is(&RetryableError{Err: inner}, inner) {
		t.Error("RetryableError must unwrap")
	}
}
