// Package search is the web lookup ideacheck runs itself: a query goes to a
// search service, results come back as title, URL and snippet, and pages worth
// reading are fetched and boiled down to their text. No model is in this loop —
// letting one drive the searching re-sends every earlier result on every turn,
// which is where the time and the tokens of an "agent that researches" go.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Result is one hit: enough to decide whether the page is worth reading.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Provider is a search service. Search returns at most n results.
type Provider interface {
	Name() string
	Search(ctx context.Context, query string, n int) ([]Result, error)
}

// Provider names, as research.search spells them.
const (
	SearXNG = "searxng"
	Tavily  = "tavily"
	Brave   = "brave"
)

const maxReply = 4 << 20

// getJSON runs one request and decodes the reply; a non-2xx status is an error
// that says what the service said.
func getJSON(client *http.Client, req *http.Request, into any) error {
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxReply))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", res.Status, clip(strings.TrimSpace(string(body)), 200))
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("reply is not JSON (%w): %s", err, clip(string(body), 120))
	}
	return nil
}

func first(rs []Result, n int) []Result {
	if n > 0 && len(rs) > n {
		return rs[:n]
	}
	return rs
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// SearX is a SearXNG instance: free, no key, self-hosted. Its JSON output is
// off by default — `search.formats: [html, json]` in its settings.yml turns it
// on; without that every request is a 403 (docs.searxng.org/dev/search_api,
// checked 2026-09-21).
type SearX struct {
	BaseURL string
	Client  *http.Client
}

func (s *SearX) Name() string { return SearXNG }

func (s *SearX) Search(ctx context.Context, query string, n int) ([]Result, error) {
	u := strings.TrimRight(s.BaseURL, "/") + "/search?" + url.Values{"q": {query}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
		Unresponsive [][]string `json:"unresponsive_engines"` // [engine, reason] pairs
	}
	if err := getJSON(s.Client, req, &reply); err != nil {
		if strings.HasPrefix(err.Error(), "403") {
			return nil, fmt.Errorf("searxng at %s refuses JSON: add `json` under search.formats in its settings.yml", s.BaseURL)
		}
		return nil, fmt.Errorf("searxng: %w", err)
	}
	// Nothing found because nothing was asked: the engines behind it turned the
	// instance away. Passing that on as "no results" would be scored, and cached,
	// as "searched, found none".
	if len(reply.Results) == 0 && len(reply.Unresponsive) > 0 {
		var engines []string
		for _, e := range reply.Unresponsive {
			engines = append(engines, strings.Join(e, ": "))
		}
		return nil, &EnginesError{Engines: engines}
	}
	out := make([]Result, 0, len(reply.Results))
	for _, r := range reply.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return first(out, n), nil
}

// EnginesError is a SearXNG that works while the search engines behind it do
// not answer it: they rate-limit an address that asks a lot, for minutes to hours.
type EnginesError struct {
	Engines []string // "brave: too many requests"
}

func (e *EnginesError) Error() string {
	return "searxng found nothing because its search engines turned it away (" + strings.Join(e.Engines, ", ") + "); they rate-limit, so try again in a while"
}

// Reachable reports whether a SearXNG answers at BaseURL, without spending a
// search: /healthz is its liveness endpoint.
func (s *SearX) Reachable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/healthz", nil)
	if err != nil {
		return false
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// TavilyAPI is api.tavily.com: a key, a free monthly allowance, one credit per
// basic search (docs.tavily.com, checked 2026-09-21).
type TavilyAPI struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (t *TavilyAPI) Name() string { return Tavily }

func (t *TavilyAPI) Search(ctx context.Context, query string, n int) ([]Result, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": n, "search_depth": "basic"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/search", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	var reply struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := getJSON(t.Client, req, &reply); err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	out := make([]Result, 0, len(reply.Results))
	for _, r := range reply.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return first(out, n), nil
}

// BraveAPI is api.search.brave.com: a key, its own index.
type BraveAPI struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (b *BraveAPI) Name() string { return Brave }

func (b *BraveAPI) Search(ctx context.Context, query string, n int) ([]Result, error) {
	u := strings.TrimRight(b.BaseURL, "/") + "/res/v1/web/search?" + url.Values{"q": {query}, "count": {fmt.Sprint(n)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)
	var reply struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := getJSON(b.Client, req, &reply); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	out := make([]Result, 0, len(reply.Web.Results))
	for _, r := range reply.Web.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: Text(r.Description)})
	}
	return first(out, n), nil
}
