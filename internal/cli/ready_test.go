package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/tui"
)

func TestReadyCatchesAnUnreachableOrMissingLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"name":"llama3.2:3b"},{"name":"qwen3:latest"}]}`)
	}))
	defer srv.Close()
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[]}`)
	}))
	defer empty.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // a closed port: Ollama not running

	cfg := func(backend, provider, baseURL, model string) config.Config {
		return config.Config{Backend: backend, Backends: map[string]config.Backend{backend: {BaseURL: baseURL, Model: model, Provider: provider}}}
	}
	cases := []struct {
		name string
		cfg  config.Config
		want string // "" = ready
	}{
		{"installed", cfg("logprob", "", srv.URL+"/v1", "llama3.2:3b"), ""},
		{"untagged name means :latest", cfg("logprob", "", srv.URL+"/v1", "qwen3"), ""},
		{"not pulled", cfg("logprob", "", srv.URL+"/v1", "qwen3:8b"), "ollama pull qwen3:8b`, or pick one you have: llama3.2:3b, qwen3:latest"},
		{"no models at all", cfg("logprob", "", empty.URL+"/v1", "qwen3:8b"), "no models yet. Run `ollama pull qwen3:8b`"},
		{"server down", cfg("logprob", "", down.URL+"/v1", "qwen3:8b"), "Ollama is not answering"},
		{"structured via local ollama", cfg("structured", "ollama", down.URL+"/v1", "x"), "Ollama is not answering"},
		{"ollama cloud is not probed", cfg("structured", "ollama", "https://ollama.com/v1", "x"), ""},
		{"other backends are not probed", cfg("claude-cli", "", "", "sonnet"), ""},
	}
	for _, c := range cases {
		err := ready(context.Background(), c.cfg)
		var nr *tui.NotReady
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: want ready, got %v", c.name, err)
		case c.want != "" && (!errors.As(err, &nr) || !strings.Contains(nr.Reason, c.want)):
			t.Errorf("%s: got %v, want a NotReady containing %q", c.name, err, c.want)
		}
	}
}
