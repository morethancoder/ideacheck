package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	stateFile = "update.json"
	// defaultEvery is how long a lookup is trusted before we ask GitHub again.
	defaultEvery = 24 * time.Hour
	// defaultWait is how long the result of a run may wait for a lookup that is
	// still in flight. A check is never worth making the user wait for.
	defaultWait = 400 * time.Millisecond
	// lookupTimeout bounds the background request itself.
	lookupTimeout = 5 * time.Second
)

// Checker looks for a newer release at most once a day and remembers the answer
// in <Dir>/update.json, so the runs in between cost nothing and GitHub is asked
// once per machine per day.
type Checker struct {
	Dir     string       // where the answer is remembered: the user config dir
	Current string       // the running version
	URL     string       // release endpoint; empty means LatestURL
	Client  *http.Client // nil means a client with a short timeout
	Now     func() time.Time
	Every   time.Duration // zero means 24h
	Wait    time.Duration // zero means 400ms
}

// Found is a release newer than the running binary.
type Found struct {
	Version string
	Page    string
}

// state is what update.json holds: the last answer and when it was given.
type state struct {
	CheckedAt time.Time `json:"checked_at"`
	Version   string    `json:"version"`
	Page      string    `json:"page"`
}

// Start begins the lookup and returns the function to call when the command has
// finished, which reports a newer release or nil. Start itself never blocks and
// never touches the network when the remembered answer is still fresh.
//
// The returned function waits Wait for a lookup still in flight and then gives
// up: an offline machine costs the user that much, once, and the answer it was
// waiting for lands in the cache for next time instead.
func (c Checker) Start(ctx context.Context) func() *Found {
	c = c.defaults()
	last := c.read()
	if c.Now().Sub(last.CheckedAt) < c.Every {
		return func() *Found { return c.found(last) }
	}
	done := make(chan state, 1)
	go func() {
		// The lookup outlives the command's own cancellation: its whole job is
		// to leave an answer behind for the next run.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lookupTimeout)
		defer cancel()
		next := state{CheckedAt: c.Now(), Version: last.Version, Page: last.Page}
		if rel, err := Latest(ctx, c.Client, c.URL); err == nil {
			next.Version, next.Page = rel.Version, rel.Page
		}
		// A failed lookup is written too: a machine with no network should back
		// off for the day rather than retry on every single run.
		c.write(next)
		done <- next
	}()
	return func() *Found {
		select {
		case next := <-done:
			return c.found(next)
		case <-time.After(c.Wait):
			return nil
		}
	}
}

func (c Checker) found(s state) *Found {
	if !Newer(c.Current, s.Version) {
		return nil
	}
	return &Found{Version: s.Version, Page: s.Page}
}

func (c Checker) defaults() Checker {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: lookupTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Every == 0 {
		c.Every = defaultEvery
	}
	if c.Wait == 0 {
		c.Wait = defaultWait
	}
	return c
}

// read returns the remembered answer. Anything unreadable counts as "never
// checked": this is a cache, and a corrupt one is not worth an error.
func (c Checker) read() state {
	var s state
	b, err := os.ReadFile(filepath.Join(c.Dir, stateFile))
	if err != nil {
		return state{}
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return state{}
	}
	return s
}

// write remembers the answer, best effort.
func (c Checker) write(s state) {
	b, err := json.Marshal(s)
	if err != nil || c.Dir == "" {
		return
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(c.Dir, stateFile), b, 0o644)
}
