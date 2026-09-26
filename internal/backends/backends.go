// Package backends constructs the configured judge. Model names and endpoints
// come from config; API keys come from the environment only.
package backends

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/claudecli"
	"github.com/morethancoder/ideacheck/internal/judge/codexcli"
	"github.com/morethancoder/ideacheck/internal/judge/laya"
	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/jev"
	"github.com/morethancoder/ideacheck/judge/logprob"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/judge/openai"
	"github.com/morethancoder/ideacheck/judge/structured"
	"github.com/morethancoder/ideacheck/prompt"
)

// Names lists every backend the CLI accepts for -b.
var Names = []string{jev.Name, laya.Name, logprob.Name, structured.Name, claudecli.Name, codexcli.Name, mock.Name}

// Deps are what model-backed judges need beyond config.
type Deps struct {
	Files prompt.Reader
	// Secret resolves an API key by its env-var name: environment first, then the
	// credentials file written by `ideacheck setup`.
	Secret func(string) string
}

// HumanOnly reports backends that spend a personal CLI login and must not be
// served over HTTP without an explicit opt-in.
func HumanOnly(backend string) bool { return backend == claudecli.Name || backend == codexcli.Name }

func requireCLI(name, hint string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("the %q command is not on your PATH (%s); run `ideacheck setup` to pick another provider", name, hint)
	}
	return nil
}

// New builds the judge selected by cfg.Backend.
func New(cfg config.Config, d Deps) (judge.Judge, error) {
	b := cfg.Active()
	switch cfg.Backend {
	case mock.Name:
		return &mock.Judge{Seed: b.Seed, FixturesDir: b.FixturesDir}, nil
	case structured.Name:
		llm, err := structuredProvider(b, d)
		if err != nil {
			return nil, err
		}
		return newStructured(cfg, d, llm)
	case claudecli.Name:
		if err := requireCLI("claude", "https://claude.com/claude-code"); err != nil {
			return nil, err
		}
		return newStructured(cfg, d, &claudecli.CLI{Model: b.Model, Effort: b.Effort})
	case codexcli.Name:
		if err := requireCLI("codex", "https://developers.openai.com/codex/cli"); err != nil {
			return nil, err
		}
		return newStructured(cfg, d, &codexcli.CLI{Model: b.Model, Effort: b.Effort})
	case logprob.Name:
		if ollamaCloud(b.BaseURL) {
			return nil, fmt.Errorf("the Ollama Cloud API returns no logprobs; use the structured backend with provider ollama (run `ideacheck setup` → Ollama Cloud)")
		}
		prompts, err := prompt.Load(d.Files, cfg.PromptsDir)
		if err != nil {
			return nil, err
		}
		client := &openai.Client{BaseURL: b.BaseURL, APIKey: endpointKey(b.BaseURL, d)}
		return &logprob.Judge{Client: client, Prompts: prompts, Model: b.Model, TopLogprobs: b.TopLogprobs, ShuffleRuns: b.ShuffleRuns, MaxConcurrent: b.MaxConcurrent, Effort: b.Effort}, nil
	case jev.Name:
		key := d.Secret("TYPESAFE_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("TYPESAFE_API_KEY is not set; get one at https://typesafe.ai and run `ideacheck setup` → TypeSafe Jev, or export it")
		}
		return &jev.Judge{BaseURL: b.BaseURL, APIKey: key, Model: b.Model, MaxConcurrent: b.MaxConcurrent}, nil
	case laya.Name:
		if err := laya.Supported(); err != nil {
			return nil, err
		}
		if _, err := laya.Interpreter(b.Python); err != nil {
			return nil, err
		}
		return &laya.Judge{Model: b.Model, Python: b.Python, MaxConcurrent: b.MaxConcurrent}, nil
	}
	return nil, fmt.Errorf("unknown backend %q (want one of %v)", cfg.Backend, Names)
}

// NewWriter builds the backend that reads, researches and writes when that is
// not the judging backend; nil means the judge does it all. A writer has to be
// able to write: naming a classifier here is a configuration mistake.
func NewWriter(cfg config.Config, d Deps) (judge.Judge, error) {
	if !cfg.Split() {
		return nil, nil
	}
	wcfg := cfg
	wcfg.Backend = cfg.Writer
	w, err := New(wcfg, d)
	if err != nil {
		return nil, fmt.Errorf("writer %s: %w", cfg.Writer, err)
	}
	if _, ok := w.(judge.Narrator); !ok {
		return nil, fmt.Errorf("writer %q only judges: it cannot read, research or write. Pick a chat model (claude-cli, codex-cli, structured, logprob) as the writer", cfg.Writer)
	}
	return w, nil
}

// endpointKey sends each hosted service only its own key; local servers get none.
func endpointKey(baseURL string, d Deps) string {
	switch {
	case strings.Contains(baseURL, "openrouter.ai"):
		return d.Secret("OPENROUTER_API_KEY")
	case ollamaCloud(baseURL):
		return d.Secret("OLLAMA_API_KEY")
	}
	return ""
}

// ollamaCloud is ollama.com's hosted API, which (unlike a local Ollama) has no
// structured outputs and returns no logprobs (verified 2026-09 against
// docs.ollama.com/capabilities/structured-outputs and ollama issue #13638).
func ollamaCloud(baseURL string) bool { return strings.Contains(baseURL, "ollama.com") }

func newStructured(cfg config.Config, d Deps, llm structured.Completer) (judge.Judge, error) {
	b := cfg.Active()
	if b.Mode != structured.ModeVote && b.Mode != structured.ModeVerbalized {
		return nil, fmt.Errorf("backends.%s.mode %q is not one of vote, verbalized", cfg.Backend, b.Mode)
	}
	prompts, err := prompt.Load(d.Files, cfg.PromptsDir)
	if err != nil {
		return nil, err
	}
	return &structured.Judge{Backend: cfg.Backend, Prompts: prompts, LLM: llm, Mode: b.Mode, VoteK: b.VoteK, MaxConcurrent: b.MaxConcurrent,
		Search: structured.SearchLimits{MaxSearches: cfg.Research.MaxSearches, MaxTokens: cfg.Research.MaxTokens}}, nil
}

func structuredProvider(b config.Backend, d Deps) (structured.Completer, error) {
	switch b.Provider {
	case "anthropic":
		// No key is required here: without one the SDK still resolves
		// ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile by itself.
		var opts []option.RequestOption
		if key := d.Secret("ANTHROPIC_API_KEY"); key != "" {
			opts = append(opts, option.WithAPIKey(key))
		}
		return structured.NewAnthropic(b.Model, b.MaxTokens, b.Thinking, b.Effort, opts...), nil
	case "openai", "openrouter", "ollama":
		if b.BaseURL == "" {
			return nil, fmt.Errorf("backends.structured.base_url is empty for provider %q; run `ideacheck setup`", b.Provider)
		}
		env := map[string]string{"openai": "OPENAI_API_KEY", "openrouter": "OPENROUTER_API_KEY", "ollama": "OLLAMA_API_KEY"}[b.Provider]
		key := d.Secret(env)
		// A local Ollama needs no key; every hosted service does.
		if key == "" && (b.Provider != "ollama" || ollamaCloud(b.BaseURL)) {
			return nil, fmt.Errorf("%s is not set; run `ideacheck setup` to enter it or pick another provider", env)
		}
		return &structured.Compat{Client: &openai.Client{BaseURL: b.BaseURL, APIKey: key}, Model: b.Model, MaxTokens: b.MaxTokens,
			CompletionTokens: b.Provider == "openai", Effort: b.Effort, SchemaInPrompt: ollamaCloud(b.BaseURL),
			WebPlugin: b.Provider == "openrouter"}, nil
	}
	return nil, fmt.Errorf("backends.structured.provider %q is not one of anthropic, openai, openrouter, ollama", b.Provider)
}
