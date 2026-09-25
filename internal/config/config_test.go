package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func noEnv() []string { return nil }

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedDefaultsMatchTheSpec(t *testing.T) {
	c, err := Load(NewFiles(""), LoadOptions{Environ: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != "logprob" || c.RubricsDir != "rubrics" || c.PromptsDir != "prompts" {
		t.Errorf("top level = %+v", c)
	}
	if c.Timeouts != (Timeouts{30 * time.Second, 90 * time.Second}) {
		t.Errorf("timeouts = %+v", c.Timeouts)
	}
	if c.Retries != (Retries{4, 500 * time.Millisecond, 5 * time.Second}) {
		t.Errorf("retries = %+v", c.Retries)
	}
	if c.Concurrency.DefaultMax != 16 {
		t.Errorf("default_max = %d", c.Concurrency.DefaultMax)
	}
	jev := c.Backends["jev"]
	if jev.Model != "jev-1.13.0" || !jev.Batch || jev.MaxConcurrent != 32 || jev.BaseURL != "https://api.typesafe.ai" {
		t.Errorf("jev = %+v", jev)
	}
	st := c.Backends["structured"]
	if st.Provider != "anthropic" || st.Mode != "vote" || st.VoteK != 5 || st.Batch || st.MaxConcurrent != 8 {
		t.Errorf("structured = %+v", st)
	}
	lp := c.Backends["logprob"]
	if lp.TopLogprobs != 20 || lp.ShuffleRuns != 2 || lp.MaxConcurrent != 1 { // 1: parallel local requests corrupt logits on Ollama 0.13.5
		t.Errorf("logprob = %+v", lp)
	}
	if c.Backends["claude-cli"].Model != "sonnet" || c.Backends["mock"].Seed != 1 {
		t.Errorf("claude-cli/mock = %+v %+v", c.Backends["claude-cli"], c.Backends["mock"])
	}
	// Keys containing dots must survive intact.
	if p := c.Pricing["jev-1.13.0"]; p != (Price{In: 0.042, Out: 0}) {
		t.Errorf("pricing[jev-1.13.0] = %+v (keys: %v)", p, c.Pricing)
	}
	if c.Log != (Log{"info", "console"}) || !strings.HasSuffix(c.Store.Path, "history.db") {
		t.Errorf("log/store = %+v %+v", c.Log, c.Store)
	}
}

func TestPrecedenceFlagsOverEnvOverUserDirOverDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yaml", "backend: jev\nbackends:\n  jev:\n    model: from-file\n    max_concurrent: 7\npricing:\n  vendor/model-2.5: {in: 1.5, out: 2}\n")
	env := func() []string {
		return []string{"IDEACHECK_BACKENDS__JEV__MODEL=from-env", "IDEACHECK_LOG__LEVEL=debug", "OTHER_BACKEND=ignored", "IDEACHECK_RETRIES__MAX_ATTEMPTS=9"}
	}

	c, err := Load(NewFiles(dir), LoadOptions{Environ: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != "jev" || c.Active().Model != "from-file" || c.MaxConcurrent() != 7 {
		t.Errorf("user dir layer: %+v", c.Active())
	}
	if !c.Active().Batch || c.Active().BaseURL == "" {
		t.Errorf("override file must merge into defaults, not replace them: %+v", c.Active())
	}
	if c.Pricing["vendor/model-2.5"] != (Price{In: 1.5, Out: 2}) || c.Pricing["jev-1.13.0"].In != 0.042 {
		t.Errorf("pricing = %+v", c.Pricing)
	}

	c, err = Load(NewFiles(dir), LoadOptions{Environ: env})
	if err != nil {
		t.Fatal(err)
	}
	if c.Active().Model != "from-env" || c.Log.Level != "debug" || c.Retries.MaxAttempts != 9 {
		t.Errorf("env layer: model=%q level=%q attempts=%d", c.Active().Model, c.Log.Level, c.Retries.MaxAttempts)
	}

	c, err = Load(NewFiles(dir), LoadOptions{Environ: env, Overrides: map[string]any{"backends.jev.model": "from-flag", "timeouts.question": "20s"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Active().Model != "from-flag" || c.Timeouts.Question != 20*time.Second {
		t.Errorf("flag layer: model=%q timeout=%v", c.Active().Model, c.Timeouts.Question)
	}
}

func TestMaxConcurrentFallsBackToDefault(t *testing.T) {
	c := Config{Backend: "x", Backends: map[string]Backend{"x": {}}, Concurrency: Concurrency{DefaultMax: 16}}
	if got := c.MaxConcurrent(); got != 16 {
		t.Errorf("unset = %d, want 16", got)
	}
	c.Backends["x"] = Backend{MaxConcurrent: 1}
	if got := c.MaxConcurrent(); got != 1 {
		t.Errorf("set = %d, want 1", got)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yaml", "backend: [unclosed")
	if _, err := Load(NewFiles(dir), LoadOptions{Environ: noEnv}); err == nil || !strings.Contains(err.Error(), "parse config.yaml") {
		t.Errorf("bad yaml: %v", err)
	}
	if _, err := Load(NewFiles(""), LoadOptions{Environ: noEnv, Overrides: map[string]any{"timeouts.question": "soon"}}); err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Errorf("bad duration: %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := func() Config {
		c, err := Load(NewFiles(""), LoadOptions{Environ: noEnv})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		name string
		edit func(*Config)
		want string // "" = valid
	}{
		{"defaults", func(*Config) {}, ""},
		{"unknown backend", func(c *Config) { c.Backend = "gpt" }, `backend "gpt"`},
		{"zero question timeout", func(c *Config) { c.Timeouts.Question = 0 }, "timeouts"},
		{"zero batch timeout", func(c *Config) { c.Timeouts.Batch = 0 }, "timeouts"},
		{"smallest timeouts", func(c *Config) { c.Timeouts = Timeouts{1, 1} }, ""},
		{"zero attempts", func(c *Config) { c.Retries.MaxAttempts = 0 }, "max_attempts"},
		{"one attempt", func(c *Config) { c.Retries.MaxAttempts = 1 }, ""},
		{"zero concurrency", func(c *Config) { c.Concurrency.DefaultMax = 0 }, "default_max"},
		{"one concurrency", func(c *Config) { c.Concurrency.DefaultMax = 1 }, ""},
		{"bad level", func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		{"bad format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
	}
	for _, tc := range cases {
		c := ok()
		tc.edit(&c)
		err := c.Validate()
		if tc.want == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestHashIsStableAndSensitive(t *testing.T) {
	a, _ := Load(NewFiles(""), LoadOptions{Environ: noEnv})
	b, _ := Load(NewFiles(""), LoadOptions{Environ: noEnv})
	if a.Hash() != b.Hash() || !strings.HasPrefix(a.Hash(), "sha256:") || len(a.Hash()) != 7+64 {
		t.Fatalf("hash unstable or malformed: %q vs %q", a.Hash(), b.Hash())
	}
	b.Backends["structured"] = Backend{Model: "other"}
	if a.Hash() == b.Hash() {
		t.Error("hash ignored a model change")
	}
}

func TestEnvKey(t *testing.T) {
	k, v := envKey("IDEACHECK_BACKENDS__CLAUDE-CLI__MAX_CONCURRENT", "3")
	if k != "backends::claude-cli::max_concurrent" || v != "3" {
		t.Errorf("envKey = %q, %v", k, v)
	}
}

func TestFilesReadPrefersOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "rubrics/business.yaml", "name: mine")
	f := Files{Dir: dir, Embedded: fstest.MapFS{
		"rubrics/business.yaml": {Data: []byte("name: default")},
		"rubrics/content.yaml":  {Data: []byte("name: content")},
	}}
	if b, err := f.Read("rubrics/business.yaml"); err != nil || string(b) != "name: mine" {
		t.Errorf("override: %q %v", b, err)
	}
	if b, err := f.Read("rubrics/content.yaml"); err != nil || string(b) != "name: content" {
		t.Errorf("fallback: %q %v", b, err)
	}
	if _, err := f.Read("rubrics/none.yaml"); err == nil {
		t.Error("missing file should error")
	}
	f.Dir = ""
	if b, _ := f.Read("rubrics/business.yaml"); string(b) != "name: default" {
		t.Errorf("no override dir: %q", b)
	}
}

func TestFilesListIsSortedUnion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "rubrics/zeta.yaml", "x")
	writeFile(t, dir, "rubrics/business.yaml", "x")
	writeFile(t, dir, "rubrics/sub/ignored.yaml", "x")
	f := Files{Dir: dir, Embedded: fstest.MapFS{
		"rubrics/business.yaml": {Data: []byte("x")},
		"rubrics/_gaps.yaml":    {Data: []byte("x")},
	}}
	got, err := f.List("rubrics")
	if want := []string{"_gaps.yaml", "business.yaml", "zeta.yaml"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, %v; want %v", got, err, want)
	}
	if got, err := f.List("absent"); err != nil || len(got) != 0 {
		t.Errorf("List(absent) = %v, %v", got, err)
	}
}

func TestEmbeddedShipsUnderscoreFilesAndAllSkipsGo(t *testing.T) {
	f := NewFiles("")
	names, err := f.List("rubrics")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"_evidence.yaml", "_gaps.yaml", "_router.yaml", "business.yaml", "content.yaml", "creative.yaml", "research.yaml", "side_project.yaml"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("embedded rubrics = %v, want %v", names, want)
	}
	all, err := f.All()
	if err != nil {
		t.Fatal(err)
	}
	if want := 3 + 8 + 13 + 1; len(all) != want { // config.yaml + fields.yaml + research.yaml, 8 rubrics, 13 prompts, searxng/settings.yml
		t.Errorf("All() = %d files %v, want %d", len(all), all, want)
	}
	for _, p := range all {
		if strings.HasSuffix(p, ".go") {
			t.Errorf("All() leaked %s", p)
		}
	}
}

func TestUserDir(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if got := UserDir(env("/x"), "/home/u"); got != filepath.Join("/x", "ideacheck") {
		t.Errorf("XDG = %q", got)
	}
	if got := UserDir(env(""), "/home/u"); got != filepath.Join("/home/u", ".config", "ideacheck") {
		t.Errorf("fallback = %q", got)
	}
}

func TestPriceForResolvesAliases(t *testing.T) {
	c := Config{Pricing: map[string]Price{"claude-haiku-4-5": {In: 1}, "gpt-5": {In: 2}, "gpt-5-mini": {In: 3}}}
	for model, want := range map[string]float64{
		"claude-haiku-4-5":           1,
		"claude-haiku-4-5-20251001":  1, // dated snapshot
		"anthropic/claude-haiku-4-5": 1, // provider prefix
		"gpt-5-mini-2026-01-01":      3, // longest match wins over gpt-5
		"gpt-5":                      2,
	} {
		if p, ok := c.PriceFor(model); !ok || p.In != want {
			t.Errorf("PriceFor(%q) = %v %v, want %v", model, p, ok, want)
		}
	}
	for _, model := range []string{"gpt-5.5", "claude-haiku", "unknown"} {
		if _, ok := c.PriceFor(model); ok {
			t.Errorf("PriceFor(%q) should be unpriced", model)
		}
	}
}

func TestSummaryIsOnByDefault(t *testing.T) {
	if c, err := Load(NewFiles(""), LoadOptions{Environ: noEnv}); err != nil || !c.Explain {
		t.Errorf("the plain-language summary should be on by default (err=%v)", err)
	}
}
