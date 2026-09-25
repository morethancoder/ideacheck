package cli

import (
	"context"
	"os/exec"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/backends/laya"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/search"
	"github.com/morethancoder/ideacheck/internal/store"
	"github.com/morethancoder/ideacheck/internal/tui"
)

// host connects the TUI app to config, credentials, the engine and history.
// Everything is re-read on demand, so a change made in Settings applies to the
// very next check without restarting.
type host struct {
	app   *app
	flags *checkFlags
	ctx   context.Context

	prices     map[string]config.Price // live model prices, read once while choosing a model
	pricesRead bool
}

var _ tui.Host = (*host)(nil)

func (h *host) config() (config.Config, error) { return h.app.loadConfig(h.flags) }

func (h *host) Providers() []config.Provider {
	cfg, _ := h.config()
	return cfg.Setup.Providers
}

func (h *host) Current() (string, string, string) {
	cfg, err := h.config()
	if err != nil || (h.app.needsSetup(h.flags)) {
		return "not set up", "", ""
	}
	return cfg.Backend, cfg.Active().Model, cfg.Active().Effort
}

// Models lists what the provider offers, each label carrying its price. When
// discovery fails it returns the priced preset list along with the error.
func (h *host) Models(p config.Provider, key string) ([]config.ModelChoice, error) {
	cfg, err := h.config()
	if err != nil {
		return nil, err
	}
	if key == "" && p.KeyEnv != "" {
		key = h.app.secrets().Get(p.KeyEnv)
	}
	ms, err := discoverModels(h.ctx, p, key)
	if err != nil {
		ms = p.Models
	}
	var live map[string]config.Price
	if p.LivePrices && p.Billing != "local" {
		live = h.livePrices(cfg.Setup.PricesURL)
	}
	return withPrices(p.Billing, ms, func(model string) (config.Price, bool) { return modelPrice(cfg, live, model) }), err
}

// livePrices fetches the current price list once per session. Failing is
// quiet: the pricing table still labels every model it knows.
func (h *host) livePrices(url string) map[string]config.Price {
	if url != "" && !h.pricesRead {
		h.pricesRead = true
		h.prices, _ = livePrices(h.ctx, url)
	}
	return h.prices
}

// MissingModel is true when p runs models locally and model is not downloaded.
// An Ollama that is not answering says false: the readiness check explains that.
func (h *host) MissingModel(p config.Provider, model string) bool {
	if model == "" {
		return false
	}
	if p.Backend == laya.Name {
		return !laya.Downloaded(model)
	}
	if !localOllama(p) {
		return false
	}
	ctx, cancel := context.WithTimeout(h.ctx, readyTimeout)
	defer cancel()
	ms, err := ollamaModels(ctx, p.BaseURL, "")
	return err == nil && !hasModel(ms, model)
}

func (h *host) Download(ctx context.Context, p config.Provider, model string, progress func(tui.Progress)) error {
	if p.Backend == laya.Name {
		cfg, _ := h.config()
		j := &laya.Judge{Model: model, Python: cfg.Backends[laya.Name].Python}
		return j.Download(ctx, func(status string, done, total int64) {
			progress(tui.Progress{Status: status, Done: done, Total: total})
		})
	}
	return pullModel(ctx, p.BaseURL, model, progress)
}

// Search is what a check would search with now, and whether Docker is there
// to start a search engine with when the answer is "nothing".
func (h *host) Search() tui.SearchState {
	local, cfg, err := h.app.localSearch()
	if err != nil {
		return tui.SearchState{}
	}
	s := tui.SearchState{Enabled: cfg.Research.Enabled}
	if provider, err := search.New(h.ctx, cfg.Research, h.app.secrets().Get); err == nil && provider != nil {
		s.With = provider.Name()
		return s
	}
	_, s.DockerErr = local.Ready(h.ctx)
	return s
}

func (h *host) StartSearch(ctx context.Context, progress func(tui.Progress)) error {
	local, _, err := h.app.localSearch()
	if err != nil {
		return err
	}
	return local.Up(ctx, func(s search.Step) {
		if !s.Done && !s.Warn {
			progress(tui.Progress{Status: s.Text})
		}
	})
}

func (h *host) Key(env string) tui.Key {
	s := h.app.secrets()
	if v := s.Getenv(env); v != "" { // the shell, or a .env it loaded: this one wins
		return tui.Key{From: "your environment", Tail: keyTail(v)}
	}
	if v := s.Get(env); v != "" {
		return tui.Key{From: "credentials.yaml", Tail: keyTail(v)}
	}
	return tui.Key{}
}

// keyTail is the last four characters of a key, and only of one long enough
// that four characters give nothing away.
func keyTail(v string) string {
	if r := []rune(v); len(r) >= 16 {
		return string(r[len(r)-4:])
	}
	return ""
}

func (h *host) HasCLI(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (h *host) SaveSetup(p config.Provider, model, effort, key string) error {
	if key != "" && p.KeyEnv != "" {
		if err := h.app.secrets().Set(p.KeyEnv, key); err != nil {
			return err
		}
	}
	return config.SaveChoice(h.app.files().Dir, p, model, effort)
}

func (h *host) SaveRoles(judge, writer config.Provider) error {
	return config.SaveRoles(h.app.files().Dir, judge, writer)
}

func (h *host) Writer() (string, string, string) {
	cfg, err := h.config()
	if err != nil || !cfg.Split() || h.app.needsSetup(h.flags) {
		return "", "", ""
	}
	w := cfg.Backends[cfg.Writer]
	return cfg.Writer, w.Model, w.Effort
}

// Engine is only handed out once the model can be reached; a *tui.NotReady
// error sends the app to Settings with the reason instead of failing every question.
func (h *host) Engine() (*pipeline.Engine, error) {
	engine, cfg, err := h.app.engine(h.flags)
	if err != nil {
		return nil, err
	}
	return engine, ready(h.ctx, cfg)
}

func (h *host) Profile() map[string]string {
	in, err := h.app.withProfile(pipeline.Intake{}, h.flags.profile)
	if err != nil {
		return nil
	}
	return in.Profile
}

func (h *host) SaveProfile(p map[string]string) error {
	if _, err := yaml.Marshal(p); err != nil {
		return err
	}
	return h.app.saveProfile(p)
}

func (h *host) store() (*store.Store, error) { return h.app.openStore() }

func (h *host) History(limit int) ([]store.Row, error) {
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.List(h.ctx, limit)
}

func (h *host) Stored(ref string) (*pipeline.Result, error) {
	s, err := h.store()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Get(h.ctx, ref)
}

// Persist is best-effort: history must never cost the user a result.
func (h *host) Persist(in pipeline.Intake, res *pipeline.Result) {
	if cfg, err := h.config(); err == nil {
		_ = h.app.persist(h.ctx, cfg.Store.Path, in, res)
	}
}
