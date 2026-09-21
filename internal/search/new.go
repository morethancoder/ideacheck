package search

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
)

// Keys are the environment names of the hosted services' API keys.
const (
	TavilyKey = "TAVILY_API_KEY"
	BraveKey  = "BRAVE_API_KEY"
)

const requestTimeout = 15 * time.Second

// New picks the search service research.search names. nil with no error means
// ideacheck will not search itself: the setting is `llm`, or it is `auto` and
// nothing is there to search with — no SearXNG answering, no key set.
func New(ctx context.Context, r config.Research, secret func(string) string) (Provider, error) {
	client := &http.Client{Timeout: requestTimeout}
	searx := &SearX{BaseURL: r.Endpoints[SearXNG], Client: client}
	keyed := func(name, env string) (Provider, error) {
		key := secret(env)
		if key == "" {
			return nil, fmt.Errorf("research.search is %s but %s is not set", name, env)
		}
		if name == Brave {
			return &BraveAPI{BaseURL: r.Endpoints[Brave], APIKey: key, Client: client}, nil
		}
		return &TavilyAPI{BaseURL: r.Endpoints[Tavily], APIKey: key, Client: client}, nil
	}
	switch r.Search {
	case config.SearchLLM:
		return nil, nil
	case SearXNG:
		if searx.BaseURL == "" {
			return nil, fmt.Errorf("research.search is searxng but research.endpoints.searxng is empty")
		}
		return searx, nil
	case Tavily:
		return keyed(Tavily, TavilyKey)
	case Brave:
		return keyed(Brave, BraveKey)
	case config.SearchAuto:
		if searx.BaseURL != "" && searx.Reachable(ctx) {
			return searx, nil
		}
		for _, s := range [][2]string{{Tavily, TavilyKey}, {Brave, BraveKey}} {
			if secret(s[1]) != "" {
				return keyed(s[0], s[1])
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("research.search %q is not one of auto, searxng, tavily, brave, llm", r.Search)
}
