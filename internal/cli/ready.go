package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/laya"
	"github.com/morethancoder/ideacheck/internal/tui"
	"github.com/morethancoder/ideacheck/judge/logprob"
	"github.com/morethancoder/ideacheck/judge/structured"
)

const readyTimeout = 3 * time.Second

// ready checks, before any question is sent, that the configured model can be
// reached. It catches what would otherwise fail every question at once — a
// local Ollama that is not running, or a model that was never pulled — and
// says how to fix it in one calm sentence instead of a wall of HTTP errors.
func ready(ctx context.Context, cfg config.Config) error {
	if err := reachable(ctx, cfg, cfg.Backend); err != nil || !cfg.Split() {
		return err
	}
	return reachable(ctx, cfg, cfg.Writer)
}

// reachable checks one backend: the judge, or the writer beside it.
func reachable(ctx context.Context, cfg config.Config, backend string) error {
	b := cfg.Backends[backend]
	if backend == laya.Name {
		// The worker would download it, but inside a question's timeout: 850 MB
		// does not fit in 90 s, and the whole check would fail for it.
		if !laya.Downloaded(b.Model) {
			return &tui.NotReady{Reason: fmt.Sprintf("The Laya model %s is not downloaded yet. Choose Laya in Settings and it is fetched (about 850 MB, once).", b.Model)}
		}
		return nil
	}
	local := backend == logprob.Name || (backend == structured.Name && b.Provider == "ollama")
	if !local || strings.Contains(b.BaseURL, "ollama.com") {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	ms, err := ollamaModels(ctx, b.BaseURL, "")
	if err != nil {
		return &tui.NotReady{Reason: fmt.Sprintf("Ollama is not answering at %s. Start it (open the Ollama app, or run `ollama serve`) and try again — or pick another provider.", ollamaHost(b.BaseURL))}
	}
	if len(ms) == 0 {
		return &tui.NotReady{Reason: fmt.Sprintf("Ollama is running but has no models yet. Run `ollama pull %s`, then try again.", orDefault(b.Model, "qwen3:8b"))}
	}
	if b.Model == "" || hasModel(ms, b.Model) {
		return nil
	}
	var have []string
	for _, m := range ms {
		have = append(have, m.ID)
	}
	return &tui.NotReady{Reason: fmt.Sprintf("The model %s is not downloaded. Run `ollama pull %s`, or pick one you have: %s.", b.Model, b.Model, strings.Join(have, ", "))}
}

// hasModel matches Ollama's naming: a name without a tag means ":latest".
func hasModel(ms []config.ModelChoice, want string) bool {
	for _, m := range ms {
		if m.ID == want || m.ID == want+":latest" {
			return true
		}
	}
	return false
}

func ollamaHost(baseURL string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ollamaRoot(baseURL), "http://"), "https://")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
