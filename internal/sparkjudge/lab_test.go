package sparkjudge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/search"
	"github.com/morethancoder/ideacheck/server"
)

// memCache is the checks database's findings cache, in memory.
type memCache struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (c *memCache) Get(_ context.Context, key string, _ time.Duration) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.rows[key]
	return b, ok
}

func (c *memCache) Put(_ context.Context, key string, b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string][]byte{}
	}
	c.rows[key] = b
	return nil
}

// typer is a judge that files an item by a word in its title, and fails on
// "boom". It records every state it was shown.
type typer struct {
	mu   sync.Mutex
	seen []string
}

func (*typer) Name() string                     { return "typer" }
func (*typer) Capabilities() judge.Capabilities { return judge.Capabilities{} }
func (j *typer) Evaluate(_ context.Context, s judge.State, q judge.Question) (judge.Answer, error) {
	text, _ := s["idea"].(string)
	j.mu.Lock()
	j.seen = append(j.seen, text)
	j.mu.Unlock()
	if q.ID != "idea_type" || len(s) != 1 {
		return judge.Answer{}, fmt.Errorf("asked %s with %v", q.ID, s)
	}
	switch {
	case strings.Contains(text, "boom"):
		return judge.Answer{}, errors.New("judge down")
	case strings.Contains(text, "game"):
		return judge.Answer{Choice: "creative", Confidence: 0.9, CostUSD: 0.001}, nil
	}
	return judge.Answer{Choice: "business", Confidence: 0.9, CostUSD: 0.001}, nil
}

// scribe is a writer: Extract answers every field with its name and the
// prompt's length, and records the last prompt.
type scribe struct {
	*mock.Judge
	mu           sync.Mutex
	system, user string
	calls        int
}

func (w *scribe) Extract(_ context.Context, system, user string, fields []string) (judge.Extraction, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.system, w.user, w.calls = system, user, w.calls+1
	out := judge.Extraction{Values: map[string]string{}, Model: "scribe-1", CostUSD: 0.002}
	for _, f := range fields {
		out.Values[f] = "a " + f
	}
	return out, nil
}

// sources serves HN Algolia and GitHub search replies and counts requests.
type sources struct {
	hits    atomic.Int32
	failHN  atomic.Bool
	queries sync.Map // path → raw query, the last one
}

func (s *sources) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.queries.Store(r.URL.Path, r.URL.RawQuery)
		switch r.URL.Path {
		case "/hn":
			if s.failHN.Load() {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"hits":[
				{"title":"Show HN: A game about tides","url":"https://tides.example/","objectID":"1","points":120},
				{"title":"Show HN: Invoices for plumbers","url":"","objectID":"2","points":80},
				{"title":"Show HN: boom","url":"https://boom.example/","objectID":"3"},
				{"title":"Not a link","url":"javascript:alert(1)","objectID":"4"}]}`)
		case "/gh":
			if r.Header.Get("Authorization") != "Bearer gh-token" {
				t.Errorf("GitHub token not sent: %q", r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"items":[
				{"full_name":"acme/invoices","html_url":"https://github.com/acme/invoices","description":"  Invoices,\n fast  ","stargazers_count":900},
				{"full_name":"acme/dupe","html_url":"https://tides.example/","description":"same link as the HN item"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeSearch is the research search service.
type fakeSearch struct{}

func (fakeSearch) Name() string { return "fake" }
func (fakeSearch) Search(_ context.Context, q string, _ int) ([]search.Result, error) {
	return []search.Result{{Title: "News: " + q, URL: "https://news.example/" + strings.ReplaceAll(q, " ", "-"), Snippet: "A launch."}}, nil
}

const trendsFile = `refresh: 24h
timeout: 5s
max_items: 10
judge_timeout: 10s
sources:
  - {id: show_hn, name: Show HN, kind: hn, url: "%[1]s/hn?since={since_unix}", since: 168h, limit: 5, strip_prefix: "Show HN: ", item_url: "https://news.example/item?id={id}"}
  - {id: github, name: GitHub, kind: github, url: "%[1]s/gh?q=created:>{since_date}", since: 168h, limit: 5, token_env: GH_TOKEN}
  - {id: news, name: News, kind: search, queries: [launches], limit: 2}
spark: true
`

type lab struct {
	*harness
	judge  *typer
	writer *scribe
	src    *sources
	cache  *memCache
}

// newLabHarness serves the API with a Lab over the given writer (nil = a
// scribe), a typer judge and trends sources on an httptest server.
func newLabHarness(t *testing.T, writer judge.Judge, with ...func(*Service)) *lab {
	t.Helper()
	l := &lab{judge: &typer{}, src: &sources{}, cache: &memCache{}}
	srv := l.src.serve(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "trends.yaml"), []byte(fmt.Sprintf(trendsFile, srv.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	if writer == nil {
		l.writer = &scribe{Judge: &mock.Judge{}}
		writer = l.writer
	}
	settings, err := ideacheck.DefaultSettings("mock", "")
	if err != nil {
		t.Fatal(err)
	}
	l.harness = newHarness(t, nil, func(s *Service, _ *server.Server) {
		for _, f := range with {
			f(s)
		}
		s.Lab = &Lab{Judge: l.judge, Writer: writer, Files: configs.Over(dir), Settings: settings, Search: fakeSearch{}, Cache: l.cache,
			Getenv: func(k string) string { return map[string]string{"GH_TOKEN": "gh-token"}[k] }}
		if err := s.Lab.Load(s.Config.Lab); err != nil {
			t.Fatal(err)
		}
	})
	return l
}

func (l *lab) makePro(user string) {
	l.t.Helper()
	expires := l.clock.now().Add(30 * 24 * time.Hour).UnixMilli()
	if code, out := l.webhook(`{"id":"evt-` + user + `","type":"INITIAL_PURCHASE","app_user_id":"` + user + `","entitlement_ids":["pro"],"expiration_at_ms":` + itoa(expires) + `,"event_timestamp_ms":` + itoa(l.clock.now().UnixMilli()) + `}`); code != 200 || out["applied"] != true {
		l.t.Fatalf("purchase webhook = %d %v", code, out)
	}
}

// A day's trends are fetched, typed by the judge one item per question, and
// sparked by the writer once; the next request that day reads the cache, and
// the day after rebuilds.
func TestTrendsAreBuiltOncePerRefresh(t *testing.T) {
	l := newLabHarness(t, nil)
	var r TrendsReport
	if code := l.do(ann, http.MethodGet, "/v1/trends", "", &r); code != 200 {
		t.Fatalf("GET /v1/trends = %d", code)
	}
	if !r.FetchedAt.Equal(l.clock.now()) {
		t.Errorf("fetched_at = %v", r.FetchedAt)
	}
	got := map[string]TrendItem{}
	for _, it := range r.Items {
		got[it.Title] = it
	}
	want := map[string]struct{ typ, url, source string }{
		"A game about tides":    {"creative", "https://tides.example/", "Show HN"},
		"Invoices for plumbers": {"business", "https://news.example/item?id=2", "Show HN"},
		"boom":                  {"other", "https://boom.example/", "Show HN"},
		"acme/invoices":         {"business", "https://github.com/acme/invoices", "GitHub"},
		"News: launches":        {"business", "https://news.example/launches", "News"},
	}
	if len(r.Items) != len(want) {
		t.Errorf("items = %+v, want the %d distinct web links", r.Items, len(want))
	}
	for title, w := range want {
		it, ok := got[title]
		if !ok || it.IdeaType != w.typ || it.URL != w.url || it.Source != w.source {
			t.Errorf("%q = %+v, want %+v", title, it, w)
		}
	}
	if got["acme/invoices"].Summary != "Invoices, fast" || got["acme/invoices"].Points != 900 {
		t.Errorf("github item = %+v", got["acme/invoices"])
	}
	if len(l.judge.seen) != len(want) || !strings.Contains(strings.Join(l.judge.seen, "|"), "acme/invoices. Invoices, fast") {
		t.Errorf("the judge saw %q: want one item per question", l.judge.seen)
	}
	if len(r.Sparks) != 2 || r.Sparks[0] != (Spark{"business", "a business"}) || r.Sparks[1] != (Spark{"creative", "a creative"}) {
		t.Errorf("sparks = %+v", r.Sparks)
	}
	if !strings.Contains(l.writer.user, "creative (A game, story") || !strings.Contains(l.writer.user, "- A game about tides") || strings.Contains(l.writer.user, "boom") {
		t.Errorf("spark prompt:\n%s", l.writer.user)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "1 of 5 items could not be typed") {
		t.Errorf("warnings = %q", r.Warnings)
	}
	if q, _ := l.src.queries.Load("/gh"); q != "q=created:%3E2026-09-18" && q != "q=created:>2026-09-18" {
		t.Errorf("github query = %v, want a week back", q)
	}
	if spent, _ := l.accounts.Spend(context.Background(), "2026-09-25"); spent < 0.0059 || spent > 0.0061 {
		t.Errorf("spend = %v, want 4 judge answers (one failed) + 1 spark call", spent)
	}

	hits, sparked := l.src.hits.Load(), l.writer.calls
	l.clock.add(23 * time.Hour)
	var again TrendsReport
	if code := l.do(bob, http.MethodGet, "/v1/trends", "", &again); code != 200 || !again.FetchedAt.Equal(r.FetchedAt) {
		t.Errorf("same day = %d, fetched %v", code, again.FetchedAt)
	}
	if l.src.hits.Load() != hits || l.writer.calls != sparked {
		t.Errorf("a cached day fetched again: %d → %d hits", hits, l.src.hits.Load())
	}

	l.clock.add(2 * time.Hour)
	if code := l.do(ann, http.MethodGet, "/v1/trends", "", &again); code != 200 || !again.FetchedAt.Equal(l.clock.now()) {
		t.Errorf("next day = %d, fetched %v", code, again.FetchedAt)
	}
	if l.src.hits.Load() == hits {
		t.Error("a day old report was not rebuilt")
	}
}

// A failing source is a warning; when nothing can be fetched the last report
// is served again, marked stale, and with none there is a 502.
func TestTrendsSurviveFailingSources(t *testing.T) {
	l := newLabHarness(t, nil)
	l.src.failHN.Store(true)
	var r TrendsReport
	if code := l.do(ann, http.MethodGet, "/v1/trends", "", &r); code != 200 || len(r.Items) != 3 {
		t.Fatalf("HN down = %d, %d items", code, len(r.Items))
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "Show HN: ") {
		t.Errorf("warnings = %q", r.Warnings)
	}

	// Everything fails: the cached report stands in.
	l.harness.svc.Lab.trends.Sources = l.harness.svc.Lab.trends.Sources[:1]
	l.clock.add(25 * time.Hour)
	var stale TrendsReport
	if code := l.do(ann, http.MethodGet, "/v1/trends", "", &stale); code != 200 || !stale.Stale || len(stale.Items) != 3 {
		t.Errorf("all down with a cache = %d %+v", code, stale)
	}
	l.cache.rows = nil
	var refused apiError
	if code := l.do(ann, http.MethodGet, "/v1/trends", "", &refused); code != 502 || refused.Error != "trends_unavailable" {
		t.Errorf("all down, no cache = %d %+v", code, refused)
	}
}

const mixBody = `{"ideas":[
	{"title":"Tide game","idea":"A puzzle game about tides","fields":{"audience":"Sailors","title":"ignored","made_up":"dropped"}},
	{"title":"Plumber invoices","idea":"Invoices from a photo of the job","fields":{"problem":"Plumbers invoice late"}}]}`

// Mixing is Pro's on the server, takes one writer call, counts no check, adds
// its cost to the day and stops at the hour's limit.
func TestMix(t *testing.T) {
	l := newLabHarness(t, nil, func(s *Service) { s.Config.Lab.Mix.PerHour = 2 })
	var refused apiError
	if code := l.do(ann, http.MethodPost, "/v1/mix", mixBody, &refused); code != 402 || refused.Error != "pro_required" {
		t.Errorf("free user = %d %+v", code, refused)
	}
	l.makePro(ann)
	var m Mix
	if code := l.do(ann, http.MethodPost, "/v1/mix", mixBody, &m); code != 200 {
		t.Fatalf("pro mix = %d", code)
	}
	want := map[string]string{"title": "a title", "problem": "a problem", "audience": "a audience", "solution": "a solution"}
	if fmt.Sprint(m.Fields) != fmt.Sprint(want) || m.Model != "scribe-1" {
		t.Errorf("mix = %+v", m)
	}
	for _, s := range []string{"IDEA 1: Tide game", "A puzzle game about tides\naudience: Sailors", "IDEA 2: Plumber invoices", "problem: Plumbers invoice late",
		"- audience: "} {
		if !strings.Contains(l.writer.user, s) {
			t.Errorf("prompt lacks %q:\n%s", s, l.writer.user)
		}
	}
	if strings.Contains(l.writer.user, "made_up") || strings.Contains(l.writer.user, "ignored") || !strings.Contains(l.writer.system, "combine two or three ideas") {
		t.Errorf("prompt passes on what it should not, or the system prompt is not mix_system.md:\n%s", l.writer.user)
	}
	if me := l.me(ann); me.Used != 0 {
		t.Errorf("a mix counted as %d checks", me.Used)
	}
	if spent, _ := l.accounts.Spend(context.Background(), "2026-09-25"); spent != 0.002 {
		t.Errorf("spend after a mix = %v", spent)
	}
	if code := l.do(ann, http.MethodPost, "/v1/mix", mixBody, nil); code != 200 {
		t.Errorf("second mix = %d", code)
	}
	if code := l.do(ann, http.MethodPost, "/v1/mix", mixBody, &refused); code != 429 || refused.Error != "rate_limited" {
		t.Errorf("third mix in the hour = %d %+v", code, refused)
	}
	l.makePro(bob)
	for _, body := range []string{`{"ideas":[{"idea":"one"}]}`, `{"ideas":[{"idea":"a"},{"idea":"b"},{"idea":"c"},{"idea":"d"}]}`, `{"ideas":[{"idea":"a"},{"idea":" "}]}`, `nope`} {
		if code := l.do(bob, http.MethodPost, "/v1/mix", body, &refused); code != 400 || refused.Error != "bad_request" {
			t.Errorf("%s = %d %+v", body, code, refused)
		}
	}
	if err := l.accounts.AddSpend(context.Background(), "2026-09-25", 25); err != nil {
		t.Fatal(err)
	}
	if code := l.do(bob, http.MethodPost, "/v1/mix", mixBody, &refused); code != 503 || refused.Error != "daily_cap" {
		t.Errorf("over the daily cap = %d %+v", code, refused)
	}
}

// A server whose writer cannot write says so instead of failing vaguely.
func TestMixWithoutAWriter(t *testing.T) {
	l := newLabHarness(t, &mock.Judge{})
	l.makePro(ann)
	var refused apiError
	if code := l.do(ann, http.MethodPost, "/v1/mix", mixBody, &refused); code != 503 || refused.Error != "no_writer" {
		t.Errorf("mix with the mock = %d %+v", code, refused)
	}
}

// The shipped lab: section and trends.yaml load, with the mix fields from fields.yaml.
func TestShippedLabConfig(t *testing.T) {
	raw, _ := configs.Defaults().Read(ConfigFile)
	c, err := ParseConfig(raw, env(map[string]string{"SPARKJUDGE_TEAM_ID": "ABCDE12345"}))
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := ideacheck.DefaultSettings("mock", "")
	l := &Lab{Files: configs.Defaults(), Settings: settings}
	if err := l.Load(c.Lab); err != nil {
		t.Fatal(err)
	}
	if len(l.fields) != 4 || l.fields[0].Name != "title" || len(l.trends.Sources) < 3 || l.trends.Refresh != 24*time.Hour {
		t.Errorf("lab = %+v %+v", l.fields, l.trends)
	}
	c.Lab.Mix.Fields = []string{"title", "vibes"}
	if err := l.Load(c.Lab); err == nil || !strings.Contains(err.Error(), `"vibes"`) {
		t.Errorf("an unknown mix field: %v", err)
	}
}
