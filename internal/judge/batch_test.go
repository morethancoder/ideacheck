package judge

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeBatch struct {
	fakeJudge
	mu     sync.Mutex
	groups [][]string
	states []State
	bfn    func(qs []Question, call int) ([]Answer, error)
	bcalls int
}

func (f *fakeBatch) EvaluateBatch(_ context.Context, s State, qs []Question) ([]Answer, error) {
	f.mu.Lock()
	f.bcalls++
	call := f.bcalls
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	f.groups = append(f.groups, ids)
	f.states = append(f.states, s)
	f.mu.Unlock()
	return f.bfn(qs, call)
}

func echo(qs []Question, _ int) ([]Answer, error) {
	out := make([]Answer, 0, len(qs))
	for i := len(qs) - 1; i >= 0; i-- { // reversed: matching must be by ID, not position
		out = append(out, Answer{ID: qs[i].ID, Noul: 0.5, Method: "m-" + qs[i].ID})
	}
	return out, nil
}

var mixed = []Question{
	{ID: "a", Kind: Noul, Uses: []string{"idea"}},
	{ID: "b", Kind: Noul, Uses: []string{"idea", "profile"}},
	{ID: "c", Kind: Score, Uses: []string{"idea"}},
}

func TestBatchGroupsByUsesAndKeepsOrder(t *testing.T) {
	j := &fakeBatch{fakeJudge: fakeJudge{caps: Capabilities{NativeBatch: true}}, bfn: echo}
	got := Run(context.Background(), j, State{"idea": 1, "profile": 2}, mixed, Options{Batch: true, Sequential: true})
	if want := [][]string{{"a", "c"}, {"b"}}; !reflect.DeepEqual(j.groups, want) {
		t.Fatalf("groups = %v, want %v", j.groups, want)
	}
	if len(j.states[0]) != 1 || len(j.states[1]) != 2 {
		t.Errorf("group states = %v", j.states)
	}
	for i, id := range []string{"a", "b", "c"} {
		if got[i].ID != id || got[i].Method != "m-"+id || got[i].Kind != mixed[i].Kind || got[i].Failed() {
			t.Errorf("answer %d = %+v", i, got[i])
		}
	}
}

func TestBatchOnlyWhenOptedInAndCapable(t *testing.T) {
	for _, c := range []struct {
		batch, native bool
		want          int
	}{{true, true, 2}, {false, true, 0}, {true, false, 0}} {
		j := &fakeBatch{bfn: echo, fakeJudge: fakeJudge{
			caps: Capabilities{NativeBatch: c.native},
			fn:   func(context.Context, State, Question, int) (Answer, error) { return Answer{}, nil },
		}}
		Run(context.Background(), j, State{}, mixed, Options{Batch: c.batch})
		if j.bcalls != c.want {
			t.Errorf("batch=%v native=%v: %d batch calls, want %d", c.batch, c.native, j.bcalls, c.want)
		}
	}
}

func TestBatchSkippedQuestionFailsAlone(t *testing.T) {
	j := &fakeBatch{fakeJudge: fakeJudge{caps: Capabilities{NativeBatch: true}},
		bfn: func(qs []Question, _ int) ([]Answer, error) { return []Answer{{ID: "a", Noul: 0.9}}, nil }}
	got := Run(context.Background(), j, State{}, mixed[:1:1], Options{Batch: true})
	if got[0].Failed() || got[0].Noul != 0.9 {
		t.Fatalf("got %+v", got[0])
	}
	got = Run(context.Background(), j, State{}, []Question{mixed[0], mixed[2]}, Options{Batch: true})
	if got[0].Failed() || got[1].Err != errNoAnswer.Error() || got[1].ID != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestBatchErrorFailsItsGroupOnlyAndRetries(t *testing.T) {
	j := &fakeBatch{fakeJudge: fakeJudge{caps: Capabilities{NativeBatch: true}}}
	j.bfn = func(qs []Question, call int) ([]Answer, error) {
		if len(qs) == 2 {
			return nil, &RetryableError{Err: errors.New("502")}
		}
		return echo(qs, call)
	}
	ch := make(chan Event, 16)
	got := Run(context.Background(), j, State{}, mixed, Options{
		Batch: true, Sequential: true, Events: ch,
		Retry: RetryPolicy{MaxAttempts: 3},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	close(ch)
	if got[0].Err != "502" || got[2].Err != "502" || got[1].Failed() {
		t.Fatalf("got %+v", got)
	}
	if j.bcalls != 4 {
		t.Errorf("batch calls = %d, want 3 retried + 1", j.bcalls)
	}
	counts := map[EventType]int{}
	for e := range ch {
		counts[e.Type]++
	}
	if counts[EventStarted] != 3 || counts[EventFailed] != 2 || counts[EventAnswered] != 1 {
		t.Errorf("event counts = %v", counts)
	}
}

func TestGroupByUses(t *testing.T) {
	if got := groupByUses(nil); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
	qs := []Question{{Uses: []string{"a", "b"}}, {Uses: []string{"ab"}}, {Uses: []string{"a", "b"}}}
	if got, want := groupByUses(qs), [][]int{{0, 2}, {1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("groupByUses = %v, want %v", got, want)
	}
}
