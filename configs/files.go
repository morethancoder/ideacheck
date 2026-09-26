package configs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Files resolves config-relative paths: a file in the override directory replaces
// the embedded one; anything missing there falls back to the embedded default.
type Files struct {
	Dir      string // override directory; "" = embedded defaults only
	Embedded fs.FS
}

// Defaults is the embedded defaults alone.
func Defaults() Files { return Files{Embedded: FS} }

// Over layers dir over the embedded defaults.
func Over(dir string) Files { return Files{Dir: dir, Embedded: FS} }

// Read returns the override file when present, else the embedded default.
// name is slash-separated and relative, e.g. "rubrics/business.yaml".
func (f Files) Read(name string) ([]byte, error) {
	if b, ok := f.Override(name); ok {
		return b, nil
	}
	return fs.ReadFile(f.Embedded, name)
}

// Override reads name from the override directory only.
func (f Files) Override(name string) ([]byte, bool) {
	if f.Dir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(f.Dir, filepath.FromSlash(name)))
	return b, err == nil
}

// List returns the sorted union of file names in dir across both layers.
func (f Files) List(dir string) ([]string, error) {
	seen := map[string]bool{}
	layers := []fs.FS{f.Embedded}
	if f.Dir != "" {
		layers = append(layers, os.DirFS(f.Dir))
	}
	for _, layer := range layers {
		if err := collect(layer, dir, seen); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// collect adds the regular files of dir to seen; a missing dir is not an error.
func collect(layer fs.FS, dir string, seen map[string]bool) error {
	entries, err := fs.ReadDir(layer, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			seen[e.Name()] = true
		}
	}
	return nil
}

// All returns every embedded-or-overridden file path, for `config dump`.
func (f Files) All() ([]string, error) {
	var out []string
	err := fs.WalkDir(f.Embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return err
	})
	return out, err
}
