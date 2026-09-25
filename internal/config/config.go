// Package config loads the effective configuration:
// flags > env (IDEACHECK_*) > user config dir > embedded defaults.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// delim never occurs in a config key, so keys such as "jev-1.13.0" or
// "anthropic/claude-sonnet-5" (pricing) survive without being split.
const delim = "::"

const (
	envPrefix = "IDEACHECK_"
	mainFile  = "config.yaml"
)

type Config struct {
	Backend string `koanf:"backend" json:"backend"`
	// Writer names the backend that reads, researches and writes prose (extract,
	// research, explain); "" = the judging backend does it all. It lets a
	// classifier that cannot write (Jev) judge next to a model that can.
	Writer      string             `koanf:"writer" json:"writer,omitempty"`
	Timeouts    Timeouts           `koanf:"timeouts" json:"timeouts"`
	Retries     Retries            `koanf:"retries" json:"retries"`
	Concurrency Concurrency        `koanf:"concurrency" json:"concurrency"`
	Backends    map[string]Backend `koanf:"backends" json:"backends"`
	Pricing     map[string]Price   `koanf:"pricing" json:"pricing"`
	Explain     bool               `koanf:"explain" json:"explain"` // add a plain-language summary to every result
	Extract     bool               `koanf:"extract" json:"extract"` // read stated facts out of the document before looking for gaps
	Research    Research           `koanf:"research" json:"research"`
	Setup       Setup              `koanf:"setup" json:"-"` // wizard presets: not part of a result's config hash
	RubricsDir  string             `koanf:"rubrics_dir" json:"rubrics_dir"`
	PromptsDir  string             `koanf:"prompts_dir" json:"prompts_dir"`
	Store       Store              `koanf:"store" json:"store"`
	Log         Log                `koanf:"log" json:"log"`
}

// Research is the stage that looks the idea up on the web before it is scored.
// What is looked up lives in research.yaml; this is only how hard and how long.
type Research struct {
	Enabled bool `koanf:"enabled" json:"enabled"` // runs only when the writer can search the web
	// Sift is whether the judge types each finding (rubrics/_evidence.yaml) and
	// drops the unrelated ones: auto = only when judge and writer are different
	// backends, always, never.
	Sift string `koanf:"sift" json:"sift"`
	// Search is who does the searching: a search service ideacheck queries
	// itself (searxng, tavily, brave), the writer's own web tool (llm), or auto —
	// a SearXNG that answers, else a service whose key is set, else llm.
	Search          string            `koanf:"search" json:"search"`
	Endpoints       map[string]string `koanf:"endpoints" json:"endpoints"`
	SearXNG         LocalSearXNG      `koanf:"searxng" json:"-"` // how `search up` runs one: not part of a result's config hash
	QueriesPerTopic int               `koanf:"queries_per_topic" json:"queries_per_topic"`
	ResultsPerQuery int               `koanf:"results_per_query" json:"results_per_query"`
	ReadPages       int               `koanf:"read_pages" json:"read_pages"` // per topic; 0 = snippets only
	PageChars       int               `koanf:"page_chars" json:"page_chars"`
	PageTimeout     time.Duration     `koanf:"page_timeout" json:"page_timeout"`
	// The rest bound the writer's own web tool (search: llm).
	MaxSearches int           `koanf:"max_searches" json:"max_searches"`
	MaxTokens   int           `koanf:"max_tokens" json:"max_tokens"`
	Timeout     time.Duration `koanf:"timeout" json:"timeout"`
	CacheTTL    time.Duration `koanf:"cache_ttl" json:"cache_ttl"` // 0 = never reuse findings
}

// LocalSearXNG is the SearXNG `ideacheck search up` runs in Docker. It answers
// at Endpoints["searxng"], which must then be a port on this machine.
type LocalSearXNG struct {
	Image     string `koanf:"image"`
	Container string `koanf:"container"`
}

// SearchLLM leaves the searching to the writer's own web tool; SearchAuto picks.
const (
	SearchAuto = "auto"
	SearchLLM  = "llm"
)

// Sift modes.
const (
	SiftAuto   = "auto"
	SiftAlways = "always"
	SiftNever  = "never"
)

type Timeouts struct {
	Question time.Duration `koanf:"question" json:"question"`
	Batch    time.Duration `koanf:"batch" json:"batch"`
}

type Retries struct {
	MaxAttempts int           `koanf:"max_attempts" json:"max_attempts"`
	BaseBackoff time.Duration `koanf:"base_backoff" json:"base_backoff"`
	MaxBackoff  time.Duration `koanf:"max_backoff" json:"max_backoff"`
}

type Concurrency struct {
	DefaultMax int `koanf:"default_max" json:"default_max"`
}

// Backend is the union of every backend's settings; each backend reads its own.
type Backend struct {
	BaseURL       string `koanf:"base_url" json:"base_url,omitempty"`
	Model         string `koanf:"model" json:"model,omitempty"`
	Batch         bool   `koanf:"batch" json:"batch,omitempty"`
	MaxConcurrent int    `koanf:"max_concurrent" json:"max_concurrent,omitempty"`
	TopLogprobs   int    `koanf:"top_logprobs" json:"top_logprobs,omitempty"`
	ShuffleRuns   int    `koanf:"shuffle_runs" json:"shuffle_runs,omitempty"`
	Provider      string `koanf:"provider" json:"provider,omitempty"`
	Mode          string `koanf:"mode" json:"mode,omitempty"`
	VoteK         int    `koanf:"vote_k" json:"vote_k,omitempty"`
	MaxTokens     int    `koanf:"max_tokens" json:"max_tokens,omitempty"`
	Thinking      string `koanf:"thinking" json:"thinking,omitempty"`
	Effort        string `koanf:"effort" json:"effort,omitempty"`   // low | medium | high | xhigh | max; "" = the model's default
	Billing       string `koanf:"billing" json:"billing,omitempty"` // api (default) | subscription | local: how the cost line reads
	Python        string `koanf:"python" json:"python,omitempty"`   // laya: the interpreter that has laya-mlx; "" = find one
	Seed          int64  `koanf:"seed" json:"seed,omitempty"`
	FixturesDir   string `koanf:"fixtures_dir" json:"fixtures_dir,omitempty"`
}

type Setup struct {
	Providers []Provider `koanf:"providers"`
	// PricesURL is a public model list (OpenRouter's shape) read for current
	// prices while choosing a model; "" = use only the pricing table.
	PricesURL string `koanf:"prices_url"`
}

// Provider is one choice in `ideacheck setup`.
type Provider struct {
	ID    string `koanf:"id"`
	Label string `koanf:"label"`
	// NeedsWriter marks a provider that only judges (Jev, Laya): setup then asks
	// which other provider reads, researches and writes next to it.
	NeedsWriter bool   `koanf:"needs_writer"`
	Backend     string `koanf:"backend"`
	Provider    string `koanf:"provider"`
	BaseURL     string `koanf:"base_url"`
	Model       string `koanf:"model"`
	KeyEnv      string `koanf:"key_env"`   // "" = no API key needed
	KeyURL      string `koanf:"key_url"`   // where to get one
	NeedsCLI    string `koanf:"needs_cli"` // executable that must be on PATH
	Billing     string `koanf:"billing"`   // copied to backends.<backend>.billing
	// Discover names a live model list to offer instead of Models, so setup
	// follows what the provider serves today: codex (`codex debug models`),
	// claude (the aliases `claude --help` advertises), ollama (/api/tags),
	// openai (GET <base_url>/models; OpenRouter's richer shape included),
	// anthropic (GET /v1/models), typesafe (GET /v1/models) or huggingface
	// (a Hub search, DiscoverURL). Models then only lend their labels.
	Discover      string        `koanf:"discover"`
	DiscoverURL   string        `koanf:"discover_url"`   // the list to read instead of the default for Discover
	DiscoverMatch string        `koanf:"discover_match"` // regexp a listed id must match to be offered
	DiscoverSkip  string        `koanf:"discover_skip"`  // regexp a listed id must not match
	DiscoverNeeds []string      `koanf:"discover_needs"` // supported_parameters a listed model must have (OpenRouter)
	Models        []ModelChoice `koanf:"models"`         // offered in setup; "Other" lets the user type any id
	Efforts       []string      `koanf:"efforts"`        // effort levels for models that do not list their own
	// LivePrices labels models with prices from Setup.PricesURL, falling back
	// to the pricing table. Off for a provider that bills at its own rates.
	LivePrices bool `koanf:"live_prices"`
}

// ModelChoice is one model offered by setup.
type ModelChoice struct {
	ID       string   `koanf:"id"`
	Label    string   `koanf:"label"`
	Efforts  []string `koanf:"efforts"`   // empty = the provider's efforts
	NoEffort bool     `koanf:"no_effort"` // the model takes no effort setting
}

// Price is USD per million tokens. CachedIn prices input read from a prompt
// cache; 0 means cached input costs the same as In.
type Price struct {
	In       float64 `koanf:"in" json:"in"`
	Out      float64 `koanf:"out" json:"out"`
	CachedIn float64 `koanf:"cached_in" json:"cached_in,omitempty"`
}

// PriceFor finds a model's price: the exact id, then without a provider prefix
// ("anthropic/claude-sonnet-5"), then the longest priced id it starts with
// ("claude-haiku-4-5-20251001" → "claude-haiku-4-5").
func (c Config) PriceFor(model string) (Price, bool) {
	if p, ok := c.Pricing[model]; ok {
		return p, true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return c.PriceFor(model[i+1:])
	}
	best := ""
	for id := range c.Pricing {
		if strings.HasPrefix(model, id+"-") && len(id) > len(best) {
			best = id
		}
	}
	p, ok := c.Pricing[best]
	return p, ok && best != ""
}

type Store struct {
	Path string `koanf:"path" json:"path"`
}

type Log struct {
	Level  string `koanf:"level" json:"level"`
	Format string `koanf:"format" json:"format"`
}

// LoadOptions are the non-file layers. Overrides keys use "." for nesting
// ("backends.structured.model"); only set keys the user actually passed as flags.
type LoadOptions struct {
	Environ   func() []string
	Overrides map[string]any
}

// Load merges every layer and validates the result.
func Load(files Files, o LoadOptions) (Config, error) {
	k := koanf.New(delim)
	for _, layer := range fileLayers(files) {
		if err := k.Load(rawbytes.Provider(layer), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", mainFile, err)
		}
	}
	envOpt := env.Opt{Prefix: envPrefix, TransformFunc: envKey, EnvironFunc: o.Environ}
	if err := k.Load(env.Provider(delim, envOpt), nil); err != nil {
		return Config{}, fmt.Errorf("read environment: %w", err)
	}
	if err := k.Load(confmap.Provider(redelimit(o.Overrides), delim), nil); err != nil {
		return Config{}, fmt.Errorf("apply flags: %w", err)
	}
	var c Config
	if err := k.Unmarshal("", &c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return c, c.Validate()
}

// fileLayers returns the embedded default followed by the user override, if any.
func fileLayers(files Files) [][]byte {
	var layers [][]byte
	if b, err := (Files{Embedded: files.Embedded}).Read(mainFile); err == nil {
		layers = append(layers, b)
	}
	if b, ok := files.override(mainFile); ok {
		layers = append(layers, b)
	}
	return layers
}

// envKey maps IDEACHECK_BACKENDS__JEV__MODEL to backends::jev::model. A double
// underscore nests; single underscores are part of the key (max_concurrent).
func envKey(k, v string) (string, any) {
	k = strings.ToLower(strings.TrimPrefix(k, envPrefix))
	return strings.ReplaceAll(k, "__", delim), v
}

func redelimit(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[strings.ReplaceAll(k, ".", delim)] = v
	}
	return out
}

var (
	logLevels  = map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true}
	logFormats = map[string]bool{"console": true, "json": true}
)

// Validate rejects configurations the pipeline cannot run with.
func (c Config) Validate() error {
	if _, ok := c.Backends[c.Backend]; !ok {
		return fmt.Errorf("backend %q has no entry under backends:", c.Backend)
	}
	if c.Writer != "" {
		if _, ok := c.Backends[c.Writer]; !ok {
			return fmt.Errorf("writer %q has no entry under backends:", c.Writer)
		}
	}
	if s := c.Research.Sift; s != SiftAuto && s != SiftAlways && s != SiftNever {
		return fmt.Errorf("research.sift %q is not one of auto, always, never", s)
	}
	if r := c.Research; r.Enabled && (r.Timeout <= 0 || r.PageTimeout <= 0 || r.QueriesPerTopic < 1 || r.ResultsPerQuery < 1 || r.ReadPages < 0) {
		return fmt.Errorf("research: timeout, page_timeout, queries_per_topic and results_per_query must be positive, read_pages not negative")
	}
	if c.Timeouts.Question <= 0 || c.Timeouts.Batch <= 0 {
		return fmt.Errorf("timeouts.question and timeouts.batch must be positive")
	}
	if c.Retries.MaxAttempts < 1 {
		return fmt.Errorf("retries.max_attempts must be at least 1")
	}
	if c.Concurrency.DefaultMax < 1 {
		return fmt.Errorf("concurrency.default_max must be at least 1")
	}
	if !logLevels[c.Log.Level] {
		return fmt.Errorf("log.level %q is not one of trace, debug, info, warn, error", c.Log.Level)
	}
	if !logFormats[c.Log.Format] {
		return fmt.Errorf("log.format %q is not one of console, json", c.Log.Format)
	}
	return nil
}

// Active returns the selected backend's settings.
func (c Config) Active() Backend { return c.Backends[c.Backend] }

// WriterName is the backend that reads, researches and writes: the writer when
// one is set, else the judging backend.
func (c Config) WriterName() string {
	if c.Writer != "" {
		return c.Writer
	}
	return c.Backend
}

// Split reports whether judging and writing are done by different backends.
func (c Config) Split() bool { return c.WriterName() != c.Backend }

// MaxConcurrent is the backend's limit, or the global default when unset.
func (c Config) MaxConcurrent() int {
	if n := c.Active().MaxConcurrent; n > 0 {
		return n
	}
	return c.Concurrency.DefaultMax
}

// Hash identifies the effective configuration a result was produced under.
func (c Config) Hash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
