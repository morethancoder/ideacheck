// Package pipeline runs a check: intake → gaps → route → score → aggregate →
// verdict. Every stage is a pure function of (input, config) except the judge call.
package ideacheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/judge"
)

// Intake is what the user gives us. Every field is optional except that there
// must be some idea text or at least one field.
type Intake struct {
	Idea string `json:"idea" yaml:"idea"`
	// Context is supporting material about the idea that is not the pitch
	// itself: what already exists, where it came from, who it is for. It rides
	// inside the idea state, so every question that uses the idea sees it.
	Context string            `json:"context,omitempty" yaml:"context,omitempty"`
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

var ErrEmptyIdea = errors.New("no idea given: pass text, a file, intake JSON, or --answer field=value")

// Empty reports whether there is nothing to check yet. A profile alone is not an
// idea; fields or context alone are, which is how a caller with no document
// gives ideacheck exactly what it needs and nothing else.
func (in Intake) Empty() bool {
	return strings.TrimSpace(in.Idea) == "" && strings.TrimSpace(in.Context) == "" && len(nonEmpty(in.Fields)) == 0
}

// ParseIntake accepts the intake JSON object, a document with YAML frontmatter,
// or plain text as the idea.
func ParseIntake(b []byte) (Intake, error) {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var in Intake
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return Intake{}, fmt.Errorf("intake JSON: %w", err)
		}
		return in.checked()
	}
	in, body, err := frontmatter(b)
	if err != nil {
		return Intake{}, err
	}
	in.Idea = string(body)
	return in.checked()
}

const fence = "---"

// frontmatter reads an optional leading YAML block, so a document can carry the
// facts a check would otherwise have to ask for:
//
//	---
//	context: what exists today ...
//	fields: {why_now: "..."}
//	profile: {background: "..."}
//	---
//	the idea itself
//
// The block may not set `idea`: the body is the idea.
func frontmatter(b []byte) (Intake, []byte, error) {
	line, rest, ok := bytes.Cut(b, []byte("\n"))
	if !ok || strings.TrimSpace(string(line)) != fence {
		return Intake{}, b, nil
	}
	block, body, ok := cutFence(rest)
	if !ok {
		return Intake{}, nil, fmt.Errorf("frontmatter: no closing %q line", fence)
	}
	var in Intake
	dec := yaml.NewDecoder(bytes.NewReader(block))
	dec.KnownFields(true)
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return Intake{}, nil, fmt.Errorf("frontmatter: %w", err)
	}
	if in.Idea != "" {
		return Intake{}, nil, errors.New("frontmatter: `idea` belongs in the body, not the block")
	}
	return in, bytes.TrimSpace(body), nil
}

// cutFence splits at the first line that is exactly the closing fence.
func cutFence(b []byte) (block, rest []byte, ok bool) {
	for start := 0; start <= len(b); {
		line, tail, more := bytes.Cut(b[start:], []byte("\n"))
		if strings.TrimSpace(string(line)) == fence {
			return b[:start], tail, true
		}
		if !more {
			return nil, nil, false
		}
		start = len(b) - len(tail)
	}
	return nil, nil, false
}

func (in Intake) checked() (Intake, error) {
	in.Idea, in.Context = strings.TrimSpace(in.Idea), strings.TrimSpace(in.Context)
	if err := unknownKey(in.Fields, ideaFields, "fields"); err != nil {
		return Intake{}, err
	}
	if err := unknownKey(in.Profile, profileFields, "profile"); err != nil {
		return Intake{}, err
	}
	if in.Idea == "" && in.Context == "" && len(nonEmpty(in.Fields)) == 0 {
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
	out := Intake{Idea: in.Idea, Context: in.Context, Fields: map[string]string{}, Profile: map[string]string{}}
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
	if in.Context != "" {
		idea["context"] = in.Context
	}
	s := judge.State{"idea": idea}
	if p := nonEmpty(in.Profile); len(p) > 0 {
		s["profile"] = p
	}
	return s
}
