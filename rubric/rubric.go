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

	"github.com/morethancoder/ideacheck/judge"
)

const (
	GapsName   = "_gaps"
	RouterName = "_router"
	// EvidenceName is the question the judge answers about each research
	// finding: how it relates to the idea. Optional — without the file, every
	// finding is kept as the researcher reported it.
	EvidenceName = "_evidence"
	Other        = "other"
)

// Reader is the slice of configs.Files a rubric needs.
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
	ConfidencePenalty float64 `yaml:"confidence_penalty" json:"confidence_penalty,omitempty"`
	// AskLimit is _gaps only: the most follow-ups a person is asked in one
	// check, taken in file order (most important first). 0 = ask them all.
	AskLimit int `yaml:"ask_limit" json:"ask_limit,omitempty"`
	// Fallback is _router only: the rubric scored when the router answers
	// "other", names a type with no rubric, or cannot answer at all.
	Fallback string `yaml:"fallback" json:"fallback,omitempty"`
	// Drop is _evidence only: the options that remove a finding from the evidence.
	Drop      []string         `yaml:"drop" json:"drop,omitempty"`
	Questions []judge.Question `yaml:"questions" json:"questions"`
	Verdict   Verdict          `yaml:"verdict" json:"verdict"`
	Hash      string           `yaml:"-" json:"hash"`
}

type Verdict struct {
	Gates         []Gate     `yaml:"gates" json:"gates,omitempty"`
	Thresholds    Thresholds `yaml:"thresholds" json:"thresholds"`
	MinConfidence float64    `yaml:"min_confidence" json:"min_confidence"`
	// Backends replaces parts of the verdict for one backend. Probabilities from
	// different backends are not on one scale — Jev's are calibrated, vote
	// frequencies come in steps of 1/k — so a cut tuned on one is not carried to
	// another. Set only what differs; the rest is inherited.
	Backends map[string]VerdictOverride `yaml:"backends" json:"backends,omitempty"`
}

// VerdictOverride is one backend's replacement for parts of a Verdict. Gates,
// when present, replace the whole list.
type VerdictOverride struct {
	Gates         []Gate      `yaml:"gates" json:"gates,omitempty"`
	Thresholds    *Thresholds `yaml:"thresholds" json:"thresholds,omitempty"`
	MinConfidence *float64    `yaml:"min_confidence" json:"min_confidence,omitempty"`
}

// For returns the verdict that applies to backend.
func (v Verdict) For(backend string) Verdict {
	o, ok := v.Backends[backend]
	if !ok {
		return v
	}
	out := v
	if o.Gates != nil {
		out.Gates = o.Gates
	}
	if o.Thresholds != nil {
		out.Thresholds = *o.Thresholds
	}
	if o.MinConfidence != nil {
		out.MinConfidence = *o.MinConfidence
	}
	return out
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
	case EvidenceName:
		return rb.validateEvidence()
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

// Evidence names the research topics the rubric's questions read: all of them
// when a question uses `evidence` whole, none when no question reads it.
func (rb *Rubric) Evidence() (all bool, topics []string) {
	for _, q := range rb.Questions {
		for _, u := range q.Uses {
			if u == "evidence" {
				all = true
			}
			if t, ok := strings.CutPrefix(u, evidencePrefix); ok && !contains(topics, t) {
				topics = append(topics, t)
			}
		}
	}
	return all, topics
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
