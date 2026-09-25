package ideacheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/judge"
)

// Settings are how an Engine runs a check: where the rubrics and prompts are,
// how long a question may take, how hard research looks and what tokens cost.
// A host fills them from wherever it keeps its configuration; the CLI reads
// config.yaml (internal/config), and DefaultSettings reads the embedded one.
type Settings struct {
	RubricsDir string
	PromptsDir string
	Explain    bool // add a plain-language summary to every result
	Extract    bool // read stated facts out of the document before looking for gaps
	// VerifyExtract has the judge check the writer's reading: a field extract
	// filled is still asked about (against the idea as given), and dropped
	// back to missing when the judge finds the document does not state it.
	// Off, extraction is trusted, as a strong writer deserves; on, a small
	// writer that fills fields the idea never states cannot hide a gap.
	VerifyExtract bool
	Timeouts      Timeouts
	Retry         judge.RetryPolicy
	// MaxConcurrent bounds the judge's questions in flight.
	MaxConcurrent int
	// Batch sends every question in one request when the judge can take them so.
	Batch    bool
	Research Research
	Pricing  Pricing
	// Judge and Writer are the model behind each role and how it bills; with
	// no separate writer, Writer describes the judge again.
	Judge  Role
	Writer Role
	// Hash identifies the configuration on every result (config_hash); "" = a
	// hash of these settings.
	Hash string
}

type Timeouts struct {
	Question time.Duration // one question
	Batch    time.Duration // every question of a stage, and each writer call
}

// Research is how hard the web lookup looks. What it looks up is research.yaml.
type Research struct {
	Enabled bool
	// Sift is whether the judge types each finding and drops the unrelated
	// ones: SiftAuto, SiftAlways or SiftNever.
	Sift            string
	QueriesPerTopic int
	ResultsPerQuery int
	ReadPages       int // per topic; 0 = snippets only
	PageChars       int
	PageTimeout     time.Duration
	Timeout         time.Duration // the whole lookup
	CacheTTL        time.Duration // 0 = never reuse findings
}

// Sift modes: auto = only when judge and writer are different backends.
const (
	SiftAuto   = "auto"
	SiftAlways = "always"
	SiftNever  = "never"
)

// Role is one backend's model and billing (BillingLocal, BillingSubscription,
// or "" / "api" for pay per token).
type Role struct {
	Model   string
	Billing string
}

// Price is USD per million tokens. CachedIn prices input read from a prompt
// cache; 0 means cached input costs the same as In.
type Price struct {
	In       float64 `json:"in" yaml:"in"`
	Out      float64 `json:"out" yaml:"out"`
	CachedIn float64 `json:"cached_in,omitempty" yaml:"cached_in"`
}

// Pricing is the price of each model, by id.
type Pricing map[string]Price

// Find is a model's price: the exact id, then without a provider prefix
// ("anthropic/claude-sonnet-5"), then the longest priced id it starts with
// ("claude-haiku-4-5-20251001" → "claude-haiku-4-5").
func (p Pricing) Find(model string) (Price, bool) {
	if price, ok := p[model]; ok {
		return price, true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return p.Find(model[i+1:])
	}
	best := ""
	for id := range p {
		if strings.HasPrefix(model, id+"-") && len(id) > len(best) {
			best = id
		}
	}
	price, ok := p[best]
	return price, ok && best != ""
}

// hash is Hash, or one computed from the settings when the host set none.
func (s Settings) hash() string {
	if s.Hash != "" {
		return s.Hash
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s Settings) validate() error {
	switch {
	case s.Timeouts.Question <= 0 || s.Timeouts.Batch <= 0:
		return fmt.Errorf("settings: both timeouts must be positive")
	case s.Retry.MaxAttempts < 1:
		return fmt.Errorf("settings: retry.max_attempts must be at least 1")
	case s.MaxConcurrent < 1:
		return fmt.Errorf("settings: max_concurrent must be at least 1")
	case s.Research.Sift != SiftAuto && s.Research.Sift != SiftAlways && s.Research.Sift != SiftNever:
		return fmt.Errorf("settings: research sift %q is not one of auto, always, never", s.Research.Sift)
	}
	return nil
}

// defaults is the part of config.yaml the settings come from.
type defaults struct {
	Backend  string `yaml:"backend"`
	Explain  bool   `yaml:"explain"`
	Extract  bool   `yaml:"extract"`
	Research struct {
		Enabled         bool          `yaml:"enabled"`
		Sift            string        `yaml:"sift"`
		QueriesPerTopic int           `yaml:"queries_per_topic"`
		ResultsPerQuery int           `yaml:"results_per_query"`
		ReadPages       int           `yaml:"read_pages"`
		PageChars       int           `yaml:"page_chars"`
		PageTimeout     time.Duration `yaml:"page_timeout"`
		Timeout         time.Duration `yaml:"timeout"`
		CacheTTL        time.Duration `yaml:"cache_ttl"`
	} `yaml:"research"`
	Timeouts struct {
		Question time.Duration `yaml:"question"`
		Batch    time.Duration `yaml:"batch"`
	} `yaml:"timeouts"`
	Retries struct {
		MaxAttempts int           `yaml:"max_attempts"`
		BaseBackoff time.Duration `yaml:"base_backoff"`
		MaxBackoff  time.Duration `yaml:"max_backoff"`
	} `yaml:"retries"`
	Concurrency struct {
		DefaultMax int `yaml:"default_max"`
	} `yaml:"concurrency"`
	Backends map[string]struct {
		Model         string `yaml:"model"`
		Billing       string `yaml:"billing"`
		Batch         bool   `yaml:"batch"`
		MaxConcurrent int    `yaml:"max_concurrent"`
	} `yaml:"backends"`
	Pricing    map[string]Price `yaml:"pricing"`
	RubricsDir string           `yaml:"rubrics_dir"`
	PromptsDir string           `yaml:"prompts_dir"`
}

// DefaultSettings are the embedded config.yaml's settings for the judge and
// writer backends it names ("" judge = config.yaml's own backend; "" writer =
// the judge writes too). They are what the CLI runs with before a user
// changes anything, so a host that has no configuration of its own starts
// from the same place.
func DefaultSettings(judgeBackend, writerBackend string) (Settings, error) {
	raw, err := configs.FS.ReadFile("config.yaml")
	if err != nil {
		return Settings{}, err
	}
	var d defaults
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return Settings{}, fmt.Errorf("config.yaml: %w", err)
	}
	if judgeBackend == "" {
		judgeBackend = d.Backend
	}
	if writerBackend == "" {
		writerBackend = judgeBackend
	}
	j, ok := d.Backends[judgeBackend]
	if !ok {
		return Settings{}, fmt.Errorf("config.yaml has no backend %q", judgeBackend)
	}
	w, ok := d.Backends[writerBackend]
	if !ok {
		return Settings{}, fmt.Errorf("config.yaml has no backend %q", writerBackend)
	}
	r := d.Research
	s := Settings{
		RubricsDir: d.RubricsDir, PromptsDir: d.PromptsDir, Explain: d.Explain, Extract: d.Extract,
		Timeouts:      Timeouts{Question: d.Timeouts.Question, Batch: d.Timeouts.Batch},
		Retry:         judge.RetryPolicy{MaxAttempts: d.Retries.MaxAttempts, Base: d.Retries.BaseBackoff, Max: d.Retries.MaxBackoff},
		MaxConcurrent: d.Concurrency.DefaultMax,
		Batch:         j.Batch,
		Research: Research{Enabled: r.Enabled, Sift: r.Sift, QueriesPerTopic: r.QueriesPerTopic, ResultsPerQuery: r.ResultsPerQuery,
			ReadPages: r.ReadPages, PageChars: r.PageChars, PageTimeout: r.PageTimeout, Timeout: r.Timeout, CacheTTL: r.CacheTTL},
		Pricing: d.Pricing,
		Judge:   Role{Model: j.Model, Billing: j.Billing},
		Writer:  Role{Model: w.Model, Billing: w.Billing},
	}
	if j.MaxConcurrent > 0 {
		s.MaxConcurrent = j.MaxConcurrent
	}
	return s, s.validate()
}
