package config

import (
	"path/filepath"

	"github.com/morethancoder/ideacheck/configs"
)

// Files resolves config-relative paths: the user's directory over the embedded
// defaults (configs.Files).
type Files = configs.Files

// NewFiles layers dir over the embedded defaults.
func NewFiles(dir string) Files { return configs.Over(dir) }

// UserDir is $XDG_CONFIG_HOME/ideacheck, falling back to ~/.config/ideacheck.
func UserDir(getenv func(string) string, home string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "ideacheck")
	}
	return filepath.Join(home, ".config", "ideacheck")
}
