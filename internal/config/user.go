package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const credentialsFile = "credentials.yaml"

// HasUserConfig reports whether setup has been done: a config.yaml exists in dir.
func HasUserConfig(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, mainFile))
	return err == nil
}

// SaveChoice records a setup choice in dir/config.yaml, keeping every other key
// the user already has there. (Comments in that file are not preserved.)
// An empty effort clears any earlier one, so the model's own default applies.
func SaveChoice(dir string, p Provider, model, effort string) error {
	path := filepath.Join(dir, mainFile)
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	backend := map[string]any{"model": model, "effort": effort}
	if p.Billing != "" {
		backend["billing"] = p.Billing
	}
	if p.Provider != "" {
		backend["provider"] = p.Provider
	}
	if p.BaseURL != "" {
		backend["base_url"] = p.BaseURL
	}
	backends, _ := doc["backends"].(map[string]any)
	if backends == nil {
		backends = map[string]any{}
	}
	existing, _ := backends[p.Backend].(map[string]any)
	for k, v := range existing {
		if _, set := backend[k]; !set {
			backend[k] = v
		}
	}
	backends[p.Backend] = backend
	doc["backend"], doc["backends"] = p.Backend, backends
	return writeYAML(path, doc, 0o644)
}

// Secrets resolves API keys: the environment first, then credentials.yaml.
type Secrets struct {
	Getenv func(string) string
	Dir    string
}

func (s Secrets) Get(name string) string {
	if v := s.Getenv(name); v != "" {
		return v
	}
	return s.file()[name]
}

func (s Secrets) file() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(s.Dir, credentialsFile)); err == nil {
		_ = yaml.Unmarshal(b, &out)
	}
	return out
}

// Set stores a key in credentials.yaml, readable by the owner only.
func (s Secrets) Set(name, value string) error {
	all := s.file()
	all[name] = value
	return writeYAML(filepath.Join(s.Dir, credentialsFile), all, 0o600)
}

func writeYAML(path string, v any, mode os.FileMode) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode) // WriteFile keeps the old mode of an existing file
}
