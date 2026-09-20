package server

import (
	"sync"

	"github.com/morethancoder/ideacheck/internal/pipeline"
)

// maxRuns bounds the in-memory registry; older finished runs are dropped (their
// results stay available from the history store).
const maxRuns = 200

// run is one check's live state: every event so far (replayed to subscribers
// that connect late) and, once finished, the result.
type run struct {
	mu     sync.Mutex
	events []pipeline.Event
	result *pipeline.Result
	err    error
	done   bool
	subs   map[chan struct{}]struct{}
}

func newRun() *run { return &run{subs: map[chan struct{}]struct{}{}} }

// wake nudges every subscriber without blocking; a subscriber that is already
// flagged will re-read state anyway.
func (r *run) wake() {
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (r *run) add(e pipeline.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	r.wake()
}

func (r *run) finish(res *pipeline.Result, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result, r.err, r.done = res, err, true
	r.wake()
}

// since returns the events after index from, and whether the run is finished.
func (r *run) since(from int) ([]pipeline.Event, *pipeline.Result, error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.Event(nil), r.events[from:]...), r.result, r.err, r.done
}

func (r *run) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

type registry struct {
	mu    sync.Mutex
	runs  map[string]*run
	order []string
}

func newRegistry() *registry { return &registry{runs: map[string]*run{}} }

func (g *registry) start(id string) *run {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := newRun()
	g.runs[id] = r
	g.order = append(g.order, id)
	if len(g.order) > maxRuns {
		delete(g.runs, g.order[0])
		g.order = g.order[1:]
	}
	return r
}

func (g *registry) get(id string) (*run, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.runs[id]
	return r, ok
}
