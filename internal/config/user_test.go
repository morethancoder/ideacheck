package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveChoiceMergesAndIsLoadable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yaml", "log: {level: debug}\nbackends:\n  structured:\n    vote_k: 3\n")
	p := Provider{Backend: "structured", Provider: "openai", BaseURL: "https://api.openai.com/v1"}
	if err := SaveChoice(dir, p, "gpt-x", "low"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(NewFiles(dir), LoadOptions{Environ: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	a := c.Active()
	if c.Backend != "structured" || a.Provider != "openai" || a.Model != "gpt-x" || a.BaseURL != "https://api.openai.com/v1" || a.Effort != "low" {
		t.Errorf("active = %q %+v", c.Backend, a)
	}
	if a.VoteK != 3 || c.Log.Level != "debug" || a.Mode != "vote" {
		t.Errorf("existing user keys and embedded defaults must survive: vote_k=%d level=%q mode=%q", a.VoteK, c.Log.Level, a.Mode)
	}
	if !HasUserConfig(dir) || HasUserConfig(t.TempDir()) {
		t.Error("HasUserConfig")
	}
}

func TestSecretsPreferEnvAndStayPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ideacheck")
	env := map[string]string{}
	s := Secrets{Getenv: func(k string) string { return env[k] }, Dir: dir}
	if s.Get("OPENAI_API_KEY") != "" {
		t.Error("unset key")
	}
	if err := s.Set("OPENAI_API_KEY", "from-file"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("OTHER", "x"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("OPENAI_API_KEY"); got != "from-file" {
		t.Errorf("file key = %q (a second Set must not drop the first)", got)
	}
	env["OPENAI_API_KEY"] = "from-env"
	if got := s.Get("OPENAI_API_KEY"); got != "from-env" {
		t.Errorf("env must win, got %q", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "credentials.yaml"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials.yaml mode = %v, want 0600 (%v)", fi.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); len(b) != 0 {
		t.Error("keys must never be written to config.yaml")
	}
}

func TestSetupPresetsAreUsable(t *testing.T) {
	c, _ := Load(NewFiles(""), LoadOptions{Environ: noEnv})
	if len(c.Setup.Providers) != 8 {
		t.Fatalf("providers = %d", len(c.Setup.Providers))
	}
	for _, p := range c.Setup.Providers {
		if _, ok := c.Backends[p.Backend]; !ok || p.ID == "" || p.Label == "" {
			t.Errorf("preset %+v names no configured backend", p)
		}
		for _, m := range p.Models {
			if m.ID == "" || m.Label == "" {
				t.Errorf("preset %s has an incomplete model %+v", p.ID, m)
			}
		}
	}
}

func TestSaveRoles(t *testing.T) {
	dir := t.TempDir()
	jev := Provider{Backend: "jev", Model: "jev-1", BaseURL: "https://api.typesafe.ai", Billing: "api"}
	claude := Provider{Backend: "claude-cli", Model: "sonnet", Billing: "subscription"}
	if err := SaveChoice(dir, claude, "opus", "low"); err != nil {
		t.Fatal(err)
	}
	if err := SaveRoles(dir, jev, claude); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(NewFiles(dir), LoadOptions{Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != "jev" || cfg.Writer != "claude-cli" || !cfg.Split() || cfg.Backends["jev"].Model != "jev-1" {
		t.Errorf("backend=%q writer=%q jev=%+v", cfg.Backend, cfg.Writer, cfg.Backends["jev"])
	}
	if m := cfg.Backends["claude-cli"]; m.Model != "opus" || m.Effort != "low" {
		t.Errorf("the writer setup just configured must keep its model: %+v", m)
	}
	if err := SaveRoles(dir, claude, Provider{}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(NewFiles(dir), LoadOptions{Environ: func() []string { return nil }})
	if cfg.Backend != "claude-cli" || cfg.Writer != "" || cfg.Split() {
		t.Errorf("one model for everything: backend=%q writer=%q", cfg.Backend, cfg.Writer)
	}
}
