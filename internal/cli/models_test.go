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
