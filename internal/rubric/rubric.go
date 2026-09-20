// Package rubric loads, validates and hashes the YAML rubric files. Everything a
// rubric says — questions, weights, gates, thresholds — is data, never Go source.
package rubric

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/internal/judge"
)

const (
	GapsName   = "_gaps"
	RouterName = "_router"
	// Fallback is the rubric used when the router answers "other".
	Fallback = "business"
	Other    = "other"
)

// Reader is the slice of config.Files a rubric needs.
type Reader interface {
	Read(name string) ([]byte, error)
	List(dir string) ([]string, error)
}

type Rubric struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description" json:"description,omitempty"`
	Threshold   float64 `yaml:"threshold" json:"threshold,omitempty"` // _gaps only
	// ConfidencePenalty is _gaps only: how hard unstated facts discount a
	// result's confidence. 0 = no discount, 1 = every gap open leaves none.
	ConfidencePenalty float64          `yaml:"confidence_penalty" json:"confidence_penalty,omitempty"`
	Questions         []judge.Question `yaml:"questions" json:"questions"`
	Verdict           Verdict          `yaml:"verdict" json:"verdict"`
	Hash              string           `yaml:"-" json:"hash"`
}

type Verdict struct {
	Gates         []Gate     `yaml:"gates" json:"gates,omitempty"`
	Thresholds    Thresholds `yaml:"thresholds" json:"thresholds"`
	MinConfidence float64    `yaml:"min_confidence" json:"min_confidence"`
}

// Thresholds are lower bounds on the composite in [0,1]; below Park is "kill".
type Thresholds struct {
	Build   float64 `yaml:"build" json:"build"`
	Explore float64 `yaml:"explore" json:"explore"`
	Park    float64 `yaml:"park" json:"park"`
}

// Load reads <dir>/<name>.yaml, rejecting unknown keys so typos fail loudly.
func Load(r Reader, dir, name string) (*Rubric, error) {
	file := path.Join(dir, name+".yaml")
	b, err := r.Read(file)
	if err != nil {
		return nil, fmt.Errorf("rubric %q: %w", name, err)
	}
	var rb Rubric
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&rb); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if rb.Name == "" {
		rb.Name = name
	}
	sum := sha256.Sum256(b)
	rb.Hash = "sha256:" + hex.EncodeToString(sum[:])
	if err := rb.validateFor(name); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &rb, nil
}

// validateFor applies the common rules plus the ones for the file's role.
func (rb *Rubric) validateFor(name string) error {
	if err := rb.Validate(); err != nil {
		return err
	}
	switch name {
	case GapsName:
		return rb.validateGaps()
	case RouterName:
		return rb.validateRouter()
	}
	return rb.validateScoring()
}

// Names lists the scoring rubrics (files not starting with "_").
func Names(r Reader, dir string) ([]string, error) {
	files, err := r.List(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if name, ok := strings.CutSuffix(f, ".yaml"); ok && !strings.HasPrefix(name, "_") {
			out = append(out, name)
		}
	}
	return out, nil
}

// Question returns the question with the given id.
func (rb *Rubric) Question(id string) (judge.Question, bool) {
	for _, q := range rb.Questions {
		if q.ID == id {
			return q, true
		}
	}
	return judge.Question{}, false
}
