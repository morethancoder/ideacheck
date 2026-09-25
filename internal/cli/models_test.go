package cli

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
)

const tags = `{"models":[
	{"name":"llama3.2:3b","details":{"parameter_size":"3.2B"}},
	{"name":"gpt-oss:120b-cloud","remote_host":"https://ollama.com:443"}]}`

func TestOllamaDiscoveryDropsCloudProxiesLocally(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		io.WriteString(w, tags)
	}))
	defer srv.Close()

	ms, err := ollamaModels(context.Background(), srv.URL+"/v1", "")
	if err != nil || path != "/api/tags" || auth != "" {
		t.Fatalf("err=%v path=%q auth=%q", err, path, auth)
	}
	if len(ms) != 1 || ms[0].ID != "llama3.2:3b" || ms[0].Label != "llama3.2:3b (3.2B)" || !ms[0].NoEffort {
		t.Errorf("local list = %+v, want only the local model (a :cloud proxy returns no logprobs)", ms)
	}
}

func TestDiscoverySendsTheKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, `{"models":[{"name":"gpt-oss:120b"},{"name":"new-model"}]}`)
	}))
	defer srv.Close()
	p := config.Provider{Discover: "ollama", BaseURL: srv.URL + "/v1", Billing: "api"}
	ms, err := discoverModels(context.Background(), p, "k")
	if err != nil || auth != "Bearer k" || len(ms) != 2 {
		t.Fatalf("ms=%+v err=%v auth=%q", ms, err, auth)
	}
	if ms, _ := discoverModels(context.Background(), config.Provider{Models: []config.ModelChoice{{ID: "x"}}}, ""); len(ms) != 1 {
		t.Error("a provider without discovery offers its preset list")
	}
}

func TestEveryModelLabelSaysWhatItCosts(t *testing.T) {
	table := map[string]config.Price{"gpt-oss:120b": {In: 0.15, Out: 0.6}, "jev": {In: 0.042}}
	price := func(m string) (config.Price, bool) { p, ok := table[m]; return p, ok }
	ms := []config.ModelChoice{{ID: "gpt-oss:120b", Label: "gpt-oss:120b"}, {ID: "jev", Label: "jev"}, {ID: "new", Label: "new"}}

	got := withPrices("api", ms, price)
	want := []string{
		"gpt-oss:120b · $0.15 in / $0.60 out per 1M tokens",
		"jev · $0.042 in / $0.00 out per 1M tokens",
		"new · price not listed",
	}
	for i := range want {
		if got[i].Label != want[i] {
			t.Errorf("label %d = %q, want %q", i, got[i].Label, want[i])
		}
	}
	if l := withPrices("local", ms, price)[0].Label; l != "gpt-oss:120b · free, runs on this machine" {
		t.Errorf("local label = %q", l)
	}
	if ms[0].Label != "gpt-oss:120b" {
		t.Error("withPrices must not change the list it was given (it is the config's preset)")
	}
}

// Trimmed from a real https://openrouter.ai/api/v1/models response.
const openrouterModels = `{"data":[
	{"id":"anthropic/claude-haiku-4.5","pricing":{"prompt":"0.000001","completion":"0.000005","input_cache_read":"0.0000001"}},
	{"id":"anthropic/claude-haiku-4.5:batch","pricing":{"prompt":"0.0000005","completion":"0.0000025"}},
	{"id":"openai/gpt-5.6-terra","pricing":{"prompt":"0.000002","completion":"0.000012"}},
	{"id":"openrouter/auto","pricing":{"prompt":"-1","completion":"-1"}}]}`

func TestLivePricesMatchOurModelIDs(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, openrouterModels)
	}))
	defer srv.Close()
	live, err := livePrices(context.Background(), srv.URL)
	if err != nil || auth != "" {
		t.Fatalf("err=%v auth=%q (the list is public: never send a key)", err, auth)
	}
	cfg := config.Config{Pricing: map[string]config.Price{"sonnet": {In: 2, Out: 10}, "claude-haiku-4-5": {In: 9, Out: 9}}}
	haiku := config.Price{In: 1, Out: 5, CachedIn: 0.1}
	for model, want := range map[string]config.Price{
		"claude-haiku-4-5":           haiku, // dashes for OpenRouter's dots; the live price wins over the table
		"claude-haiku-4-5-20251001":  haiku, // dated id
		"anthropic/claude-haiku-4-5": haiku,
		"gpt-5.6-terra":              {In: 2, Out: 12},
		"sonnet":                     {In: 2, Out: 10}, // a CLI alias OpenRouter does not list: the table
	} {
		got, ok := modelPrice(cfg, live, model)
		if !ok || math.Abs(got.In-want.In) > 1e-9 || math.Abs(got.Out-want.Out) > 1e-9 || math.Abs(got.CachedIn-want.CachedIn) > 1e-9 {
			t.Errorf("%s = %+v %v, want %+v", model, got, ok, want)
		}
	}
	if _, ok := live["openrouter/auto"]; ok {
		t.Error("a router model has no fixed price (-1) and must be skipped")
	}
	if _, ok := modelPrice(cfg, nil, "unknown"); ok {
		t.Error("an unknown model has no price")
	}
}

func serve(t *testing.T, body string, seen *http.Header) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func ids(ms []config.ModelChoice) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestOpenRouterListOffersNewestModelsThatCanAnswer(t *testing.T) {
	url := serve(t, `{"data":[
		{"id":"old/model","created":100,"supported_parameters":["structured_outputs"]},
		{"id":"new/thinker","created":300,"supported_parameters":["structured_outputs","reasoning"],
		 "reasoning":{"supported_efforts":["max","high","low"]}},
		{"id":"new/thinker:free","created":300,"supported_parameters":["structured_outputs"]},
		{"id":"no/schema","created":400,"supported_parameters":["tools"]}]}`, nil)
	p := config.Provider{Discover: "openai", DiscoverURL: url, DiscoverNeeds: []string{"structured_outputs"},
		Models: []config.ModelChoice{{ID: "old/model", Label: "old — kept label"}, {ID: "gone/model"}}}

	ms, err := discoverModels(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ms); len(got) != 2 || got[0] != "new/thinker" || got[1] != "old/model" {
		t.Fatalf("ids = %v, want newest first, no variant, no model without structured output, no retired preset", got)
	}
	if e := ms[0].Efforts; len(e) != 3 || e[0] != "low" || e[2] != "max" {
		t.Errorf("efforts = %v, want the model's own, least thinking first", e)
	}
	if !ms[1].NoEffort || ms[1].Label != "old — kept label" {
		t.Errorf("old = %+v, want no effort step and the preset label", ms[1])
	}
}

func TestOpenAIListIsFilteredByTheConfiguredPatterns(t *testing.T) {
	var h http.Header
	url := serve(t, `{"data":[{"id":"gpt-6","created":2},{"id":"gpt-6-realtime","created":3},{"id":"text-embedding-4","created":4}]}`, &h)
	p := config.Provider{Discover: "openai", BaseURL: url, DiscoverMatch: `^gpt-\d`, DiscoverSkip: `realtime`}
	ms, err := discoverModels(context.Background(), p, "k")
	if err != nil || h.Get("Authorization") != "Bearer k" {
		t.Fatalf("err=%v auth=%q", err, h.Get("Authorization"))
	}
	if got := ids(ms); len(got) != 1 || got[0] != "gpt-6" || ms[0].NoEffort {
		t.Errorf("ids = %v (%+v), want gpt-6 with the provider's efforts", got, ms)
	}
}

func TestAnthropicListSaysWhichEffortsEachModelTakes(t *testing.T) {
	var h http.Header
	url := serve(t, `{"data":[
		{"id":"claude-small","created_at":"2025-10-01T00:00:00Z","capabilities":{"effort":{"supported":false}}},
		{"id":"claude-big","created_at":"2026-09-01T00:00:00Z","capabilities":{"effort":{"supported":true,
		 "low":{"supported":true},"high":{"supported":true},"max":{"supported":true}}}}]}`, &h)
	p := config.Provider{Discover: "anthropic", DiscoverURL: url}
	ms, err := discoverModels(context.Background(), p, "k")
	if err != nil || h.Get("x-api-key") != "k" || h.Get("anthropic-version") == "" {
		t.Fatalf("err=%v headers=%v", err, h)
	}
	if got := ids(ms); got[0] != "claude-big" || !ms[1].NoEffort {
		t.Errorf("ms = %+v, want newest first and no effort step for a model that takes none", ms)
	}
	if e := ms[0].Efforts; len(e) != 3 || e[0] != "low" || e[1] != "high" || e[2] != "max" {
		t.Errorf("efforts = %v", e)
	}
}

func TestAListThatNeedsAKeyWaitsForIt(t *testing.T) {
	presets := []config.ModelChoice{{ID: "jev-1"}}
	for _, p := range []config.Provider{
		{Discover: "typesafe", BaseURL: "http://127.0.0.1:1", Models: presets},
		{Discover: "anthropic", DiscoverURL: "http://127.0.0.1:1", Models: presets},
		{Discover: "openai", BaseURL: serveStatus(t, http.StatusUnauthorized), Models: presets},
	} {
		if ms, err := discoverModels(context.Background(), p, ""); err != nil || len(ms) != 1 {
			t.Errorf("%s without a key: ms=%v err=%v, want the presets and no error", p.Discover, ms, err)
		}
	}
}

func serveStatus(t *testing.T, code int) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTypeSafeAndHubListsNewestFirst(t *testing.T) {
	var h http.Header
	url := serve(t, `{"models":[{"name":"jev-latest","release_date":"2026-09-10T18:38:01.391457+00:00"},
		{"name":"jev-preview","release_date":"2026-09-10T18:39:06+00:00"}]}`, &h)
	ms, err := discoverModels(context.Background(), config.Provider{Discover: "typesafe", DiscoverURL: url}, "k")
	if err != nil || h.Get("Authorization") != "Bearer k" || ids(ms)[0] != "jev-preview" {
		t.Fatalf("ms=%v err=%v", ids(ms), err)
	}

	url = serve(t, `[{"id":"a/laya-mlx","createdAt":"2026-09-19T14:11:30.000Z"},{"id":"a/laya-coreml","createdAt":"2026-09-20T03:22:22.000Z"}]`, nil)
	ms, err = discoverModels(context.Background(), config.Provider{Discover: "huggingface", DiscoverURL: url, DiscoverMatch: "-mlx$"}, "")
	if err != nil || len(ms) != 1 || ms[0].ID != "a/laya-mlx" {
		t.Errorf("hub = %v err=%v, want only the checkpoints the backend loads", ids(ms), err)
	}
}

func TestClaudeAliasesComeFromTheInstalledHelp(t *testing.T) {
	help := `  --fallback-model <model>   Enable automatic fallback
  --model <model>                       Model for the current session. Provide
                                        an alias for the latest model (e.g.
                                        'fable', 'opus', or 'sonnet') or a
                                        model's full name (e.g.
                                        'claude-fable-5').
  -n, --name <name>                     Set a display name ('not-a-model')`
	if got := modelAliases(help); len(got) != 3 || got[0] != "fable" || got[2] != "sonnet" {
		t.Errorf("aliases = %v, want the versionless names under --model only", got)
	}
}
