// Package pipeline runs a check: intake → gaps → route → score → aggregate →
// verdict. Every stage is a pure function of (input, config) except the judge call.
package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge"
)

// Intake is what the user gives us (§9). Every field is optional except that
// there must be some idea text or at least one field.
type Intake struct {
	Idea    string            `json:"idea" yaml:"idea"`
	Fields  map[string]string `json:"fields,omitempty" yaml:"fields,omitempty"`
	Profile map[string]string `json:"profile,omitempty" yaml:"profile,omitempty"`
}

var (
	ideaFields    = []string{"title", "problem", "audience", "solution", "why_now", "monetization", "competitors_known", "differentiation"}
	profileFields = []string{"skills", "domains", "network", "would_use_myself", "time_horizon", "background"}
)

// IdeaFields and ProfileFields are the accepted keys, in display order.
func IdeaFields() []string    { return append([]string(nil), ideaFields...) }
func ProfileFields() []string { return append([]string(nil), profileFields...) }

var ErrEmptyIdea = errors.New("no idea given: pass text, a file, or intake JSON")

// ParseIntake accepts the intake JSON object, or plain text as the idea.
func ParseIntake(b []byte) (Intake, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return Intake{Idea: string(b)}.checked()
	}
	var in Intake
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Intake{}, fmt.Errorf("intake JSON: %w", err)
	}
	return in.checked()
}

func (in Intake) checked() (Intake, error) {
	in.Idea = strings.TrimSpace(in.Idea)
	if err := unknownKey(in.Fields, ideaFields, "fields"); err != nil {
		return Intake{}, err
	}
	if err := unknownKey(in.Profile, profileFields, "profile"); err != nil {
		return Intake{}, err
	}
	if in.Idea == "" && len(nonEmpty(in.Fields)) == 0 {
		return Intake{}, ErrEmptyIdea
	}
	return in, nil
}

func unknownKey(m map[string]string, allowed []string, where string) error {
	for k := range m {
		if !contains(allowed, k) {
			return fmt.Errorf("%s.%s is not a known field (allowed: %s)", where, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func nonEmpty(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	return out
}

const profilePrefix = "profile."

// KnownField reports whether a gap's `fills` target names a real intake field.
func KnownField(fills string) bool {
	if name, ok := strings.CutPrefix(fills, profilePrefix); ok {
		return contains(profileFields, name)
	}
	return contains(ideaFields, fills)
}

// Has reports whether the field a gap `fills` already holds a non-blank value.
func (in Intake) Has(fills string) bool {
	if name, ok := strings.CutPrefix(fills, profilePrefix); ok {
		return strings.TrimSpace(in.Profile[name]) != ""
	}
	return strings.TrimSpace(in.Fields[fills]) != ""
}

// With returns a copy with a follow-up reply stored in the field a gap `fills`
// ("problem", or "profile.background").
func (in Intake) With(fills, value string) Intake {
	out := Intake{Idea: in.Idea, Fields: map[string]string{}, Profile: map[string]string{}}
	for k, v := range in.Fields {
		out.Fields[k] = v
	}
	for k, v := range in.Profile {
		out.Profile[k] = v
	}
	if name, ok := strings.CutPrefix(fills, profilePrefix); ok {
		out.Profile[name] = value
	} else {
		out.Fields[fills] = value
	}
	return out
}

// State is the v1 "normalize" step: no model call, just labelled fields so that
// questions read structure rather than pitch prose. Empty fields are omitted —
// material a question does not need lowers accuracy (context rot).
func (in Intake) State() judge.State {
	idea := nonEmpty(in.Fields)
	if in.Idea != "" {
		idea["text"] = in.Idea
	}
	s := judge.State{"idea": idea}
	if p := nonEmpty(in.Profile); len(p) > 0 {
		s["profile"] = p
	}
	return s
}
