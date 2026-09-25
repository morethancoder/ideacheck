package search

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
)

func serve(t *testing.T, status int, body string, seen **http.Request, sent *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Clone(context.Background())
		b, _ := io.ReadAll(r.Body)
		*sent = string(b)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearXNGWireFormat(t *testing.T) {
	var req *http.Request
	var sent string
	srv := serve(t, 200, `{"query":"q","results":[{"title":"Weave","url":"https://w.example/","content":"Texts patients.","engine":"google"},{"title":"B","url":"https://b.example","content":""},{"title":"C","url":"https://c.example","content":""}]}`, &req, &sent)
	got, err := (&SearX{BaseURL: srv.URL + "/", Client: srv.Client()}).Search(context.Background(), "dental recall software", 2)
	if err != nil || len(got) != 2 || got[0] != (Result{Title: "Weave", URL: "https://w.example/", Snippet: "Texts patients."}) {
		t.Fatalf("results = %+v, %v", got, err)
	}
	if q := req.URL.Query(); req.URL.Path != "/search" || q.Get("q") != "dental recall software" || q.Get("format") != "json" {
		t.Errorf("request = %s", req.URL)
	}

	// JSON output is off by default in SearXNG: the error has to say how to turn it on.
	srv = serve(t, 403, "Forbidden", &req, &sent)
	if _, err := (&SearX{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "q", 5); err == nil || !strings.Contains(err.Error(), "search.formats") {
		t.Errorf("a 403 must explain the settings.yml fix, got %v", err)
	}
}

func TestTavilyWireFormat(t *testing.T) {
	var req *http.Request
	var sent string
	srv := serve(t, 200, `{"results":[{"title":"Adit","url":"https://adit.example","content":"Recall system.","score":0.9}]}`, &req, &sent)
	got, err := (&TavilyAPI{BaseURL: srv.URL, APIKey: "tvly-k", Client: srv.Client()}).Search(context.Background(), "q", 5)
	if err != nil || len(got) != 1 || got[0].Snippet != "Recall system." {
		t.Fatalf("results = %+v, %v", got, err)
	}
	if req.Method != "POST" || req.URL.Path != "/search" || req.Header.Get("Authorization") != "Bearer tvly-k" ||
		!strings.Contains(sent, `"max_results":5`) || !strings.Contains(sent, `"search_depth":"basic"`) {
		t.Errorf("request = %s %s auth=%q body=%s", req.Method, req.URL, req.Header.Get("Authorization"), sent)
	}
}

func TestBraveWireFormat(t *testing.T) {
	var req *http.Request
	var sent string
	srv := serve(t, 200, `{"web":{"results":[{"title":"Promptly","url":"https://p.example","description":"Texts   overdue\n patients."}]}}`, &req, &sent)
	got, err := (&BraveAPI{BaseURL: srv.URL, APIKey: "bk", Client: srv.Client()}).Search(context.Background(), "q", 3)
	if err != nil || len(got) != 1 || got[0].Snippet != "Texts overdue\npatients." {
		t.Fatalf("results = %+v, %v", got, err)
	}
	if req.URL.Path != "/res/v1/web/search" || req.URL.Query().Get("count") != "3" || req.Header.Get("X-Subscription-Token") != "bk" {
		t.Errorf("request = %s token=%q", req.URL, req.Header.Get("X-Subscription-Token"))
	}
}

// auto prefers the free local service, then whatever has a key, then nothing —
// and nothing is not an error: the writer may search, or nobody does.
func TestNewPicksWhatIsThere(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("the probe must not spend a search: %s", r.URL)
		}
		io.WriteString(w, "OK")
	}))
	defer up.Close()
	none := func(string) string { return "" }
	tavily := func(env string) string { return map[string]string{TavilyKey: "k"}[env] }
	r := config.Research{Search: config.SearchAuto, Endpoints: map[string]string{SearXNG: up.URL, Tavily: "https://t.example"}}

	if p, err := New(context.Background(), r, tavily); err != nil || p.Name() != SearXNG {
		t.Errorf("a SearXNG that answers wins: %v %v", p, err)
	}
	r.Endpoints[SearXNG] = "http://127.0.0.1:1" // nothing listens here
	if p, err := New(context.Background(), r, tavily); err != nil || p.Name() != Tavily {
		t.Errorf("then a service with a key: %v %v", p, err)
	}
	if p, err := New(context.Background(), r, none); err != nil || p != nil {
		t.Errorf("then nothing, quietly: %v %v", p, err)
	}
	r.Search = Brave
	if _, err := New(context.Background(), r, none); err == nil || !strings.Contains(err.Error(), BraveKey) {
		t.Errorf("a service asked for by name without its key is a mistake worth naming: %v", err)
	}
	r.Search = config.SearchLLM
	if p, err := New(context.Background(), r, tavily); err != nil || p != nil {
		t.Errorf("llm leaves the searching to the writer: %v %v", p, err)
	}
}

const articlePage = `<html><head><title>Dental recall, explained</title><script>track()</script></head><body>
<nav><a href="/">Home</a><a href="/pricing">Pricing</a><a href="/login">Log in</a></nav>
<article><h1>Dental recall, explained</h1>
<p>Recall software texts patients who are overdue for a hygiene visit and offers them an open slot. Practices that automate it recover revenue the front desk has no time to chase, which is why every practice-management vendor now ships some version of it.</p>
<p>The hard part is not sending the message. It is reading the schedule out of Dentrix or Eaglesoft reliably, because neither exposes a clean interface and both change it without notice between releases of the desktop client.</p>
<p>Pricing per booked appointment aligns the vendor with the practice, but it needs attribution the practice trusts, or every invoice becomes an argument about which bookings the texts really caused.</p></article>
<footer>© 2026 Example Inc. <a href="/privacy">Privacy</a> Cookie settings</footer></body></html>`

const landingPage = `<html><head><title>Promptly — dental texting</title><style>.x{}</style></head><body>
<header>Promptly Login Sign up</header><div class="hero"><h1>Fill your hygiene schedule</h1><div>Texts overdue patients automatically</div>
<div>Works with Dentrix</div></div><footer>Cookie settings</footer></body></html>`

func TestDigestKeepsTheContentAndDropsTheChrome(t *testing.T) {
	u, _ := url.Parse("https://blog.example/recall")
	text := Digest([]byte(articlePage), u)
	for _, want := range []string{"Dental recall, explained", "reading the schedule out of Dentrix", "every invoice becomes an argument"} {
		if !strings.Contains(text, want) {
			t.Errorf("article text lacks %q:\n%s", want, text)
		}
	}
	for _, noise := range []string{"Log in", "Cookie settings", "track()", "Pricing\n"} {
		if strings.Contains(text, noise) {
			t.Errorf("article text still carries %q:\n%s", noise, text)
		}
	}
	if strings.Contains(text, "\n\n") || strings.Contains(text, "  ") {
		t.Errorf("whitespace must be collapsed: %q", text)
	}

	// A landing page is no article: the extractor finds little, so the visible
	// body is used — still without scripts, styles, header and footer.
	text = Digest([]byte(landingPage), u)
	if !strings.Contains(text, "Texts overdue patients automatically") || !strings.Contains(text, "Works with Dentrix") {
		t.Errorf("landing page text = %q", text)
	}
	if strings.Contains(text, "Cookie settings") || strings.Contains(text, ".x{}") || strings.Contains(text, "Sign up") {
		t.Errorf("landing page text still carries chrome: %q", text)
	}
}

func TestReadClipsAndRefusesWhatIsNotAPublicWebPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, articlePage)
	}))
	defer srv.Close()

	// The test server is on 127.0.0.1, so it is read through a plain client; the
	// guarded one must refuse that very address.
	open := &Reader{Client: srv.Client()}
	text, err := open.Read(context.Background(), srv.URL, 80)
	if err != nil || len([]rune(text)) > 81 || !strings.HasSuffix(text, "…") {
		t.Errorf("clipped read = %q, %v", text, err)
	}
	guarded := NewReader(2 * time.Second)
	if _, err := guarded.Read(context.Background(), srv.URL, 80); err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Errorf("a search result must never be a way to read a local service: %v", err)
	}
	for _, bad := range []string{"file:///etc/passwd", "ftp://x.example/a", "not a url", ""} {
		if _, err := guarded.Read(context.Background(), bad, 80); err == nil {
			t.Errorf("%q is not a web page and must be refused", bad)
		}
	}
}

// Engines that turn SearXNG away are not "no results": that would be scored,
// and cached, as "searched, found none".
func TestSearXNGSaysWhenItsEnginesTurnedItAway(t *testing.T) {
	var req *http.Request
	var sent string
	srv := serve(t, 200, `{"results":[],"unresponsive_engines":[["brave","Suspended: too many requests"],["duckduckgo","CAPTCHA"]]}`, &req, &sent)
	_, err := (&SearX{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "q", 5)
	var engines *EnginesError
	if !errors.As(err, &engines) || len(engines.Engines) != 2 || !strings.Contains(err.Error(), "brave: Suspended: too many requests") {
		t.Fatalf("err = %v", err)
	}
	// Results from the engines that did answer are results.
	srv = serve(t, 200, `{"results":[{"title":"A","url":"https://a.example"}],"unresponsive_engines":[["brave","timeout"]]}`, &req, &sent)
	if got, err := (&SearX{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "q", 5); err != nil || len(got) != 1 {
		t.Errorf("got %v, %v", got, err)
	}
}
