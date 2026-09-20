package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/tui"
)

// Lines as Ollama 0.13.5 streams them from POST /api/pull.
const pullOK = `{"status":"pulling manifest"}
{"status":"pulling dde5aa3fc5ff","digest":"sha256:dde5","total":2000,"completed":500}
{"status":"pulling 966de95ca8a6","digest":"sha256:966d","total":1000,"completed":1000}
{"status":"pulling dde5aa3fc5ff","digest":"sha256:dde5","total":2000,"completed":2000}
{"status":"verifying sha256 digest"}
{"status":"success"}
`

func TestPullReportsProgressOverAllLayers(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, pullOK)
	}))
	defer srv.Close()

	var seen []tui.Progress
	err := pullModel(context.Background(), srv.URL+"/v1", "llama3.2:3b", func(p tui.Progress) { seen = append(seen, p) })
	if err != nil || path != "/api/pull" || got["model"] != "llama3.2:3b" || got["stream"] != true {
		t.Fatalf("err=%v path=%q body=%v", err, path, got)
	}
	if p := seen[2]; p.Done != 1500 || p.Total != 3000 {
		t.Errorf("after two layers = %+v, want 1500 of 3000 (summed over layers)", p)
	}
	if p := seen[len(seen)-1]; p.Status != "success" || p.Done != 3000 || p.Total != 3000 {
		t.Errorf("last = %+v", p)
	}
}

func TestPullFailuresSayWhatWentWrong(t *testing.T) {
	for body, want := range map[string]string{
		`{"status":"pulling manifest"}` + "\n" + `{"error":"pull model manifest: file does not exist"}`: "Ollama has no model called nope:1b; check the name at ollama.com/library",
		`{"error":"disk full"}`:                "download nope:1b: disk full",
		`{"status":"pulling manifest"}` + "\n": "download nope:1b: Ollama stopped before it finished",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		err := pullModel(context.Background(), srv.URL, "nope:1b", func(tui.Progress) {})
		srv.Close()
		if err == nil || err.Error() != want {
			t.Errorf("body %q: err = %v, want %q", body, err, want)
		}
	}
}

func TestOnlyLocalOllamaModelsAreDownloaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"name":"llama3.2:3b"},{"name":"qwen3:latest"}]}`)
	}))
	defer srv.Close()
	h := &host{ctx: context.Background()}
	local := config.Provider{Discover: "ollama", BaseURL: srv.URL + "/v1"}
	for model, want := range map[string]bool{"qwen3:8b": true, "llama3.2:3b": false, "qwen3": false, "": false} {
		if got := h.MissingModel(local, model); got != want {
			t.Errorf("MissingModel(%q) = %v, want %v", model, got, want)
		}
	}
	if h.MissingModel(config.Provider{Discover: "ollama", BaseURL: "https://ollama.com/v1"}, "x") ||
		h.MissingModel(config.Provider{Discover: "codex"}, "x") {
		t.Error("only a local Ollama downloads models")
	}
	down := config.Provider{Discover: "ollama", BaseURL: "http://127.0.0.1:1/v1"}
	if h.MissingModel(down, "x") {
		t.Error("an Ollama that is not running cannot download; the readiness check explains it")
	}
	if !strings.Contains(ollamaHost(local.BaseURL), "127.0.0.1") {
		t.Errorf("ollamaHost = %q", ollamaHost(local.BaseURL))
	}
}
