package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/rubric"
)

// Idea is one line of the dataset. Labels hold only what is obvious: 0/1 for a
// noul, a level index for a score, an option key for a choice.
type Idea struct {
	ID            string            `json:"id"`
	Idea          string            `json:"idea"`
	Fields        map[string]string `json:"fields,omitempty"`
	Profile       map[string]string `json:"profile,omitempty"`
	Labels        map[string]any    `json:"labels,omitempty"`
	ExpectMissing []string          `json:"expect_missing,omitempty"`
	Note          string            `json:"note,omitempty"`
}

func (i Idea) Intake() pipeline.Intake {
	return pipeline.Intake{Idea: i.Idea, Fields: i.Fields, Profile: i.Profile}
}

// Catalog is every question across every rubric file, by id.
func Catalog(files rubric.Reader, dir string) (map[string]judge.Question, error) {
	names, err := rubric.Names(files, dir)
	if err != nil {
		return nil, err
	}
	out := map[string]judge.Question{}
	for _, name := range append(names, rubric.GapsName, rubric.RouterName) {
		rb, err := rubric.Load(files, dir, name)
		if err != nil {
			return nil, err
		}
		for _, q := range rb.Questions {
			if _, dup := out[q.ID]; !dup {
				out[q.ID] = q
			}
		}
	}
	return out, nil
}

// Load reads a JSONL dataset and rejects labels that could never match: an
// unknown question id or a value outside the question's range silently scores
// as "no data", which would hide a typo forever.
func Load(path string, catalog map[string]judge.Question) ([]Idea, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}
	var ideas []Idea
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var idea Idea
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&idea); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if idea.ID == "" || idea.Idea == "" || seen[idea.ID] {
			return nil, fmt.Errorf("%s:%d: id and idea are required and ids must be unique (id %q)", path, line, idea.ID)
		}
		seen[idea.ID] = true
		if err := idea.validate(catalog); err != nil {
			return nil, fmt.Errorf("%s:%d (%s): %w", path, line, idea.ID, err)
		}
		ideas = append(ideas, idea)
	}
	if len(ideas) == 0 {
		return nil, fmt.Errorf("%s: no ideas", path)
	}
	return ideas, sc.Err()
}

func (i Idea) validate(catalog map[string]judge.Question) error {
	for id, v := range i.Labels {
		q, ok := catalog[id]
		if !ok {
			return fmt.Errorf("label %q is not a question in any rubric", id)
		}
		if err := validLabel(q, v); err != nil {
			return fmt.Errorf("label %s: %w", id, err)
		}
	}
	for _, id := range i.ExpectMissing {
		if q, ok := catalog[id]; !ok || q.Ask == "" {
			return fmt.Errorf("expect_missing %q is not a gap question", id)
		}
	}
	return nil
}

func validLabel(q judge.Question, v any) error {
	switch q.Kind {
	case judge.Choice:
		if s, ok := v.(string); !ok || q.Options[s] == "" {
			return fmt.Errorf("%v is not one of the options", v)
		}
	case judge.Score:
		if n, ok := v.(float64); !ok || n < 0 || n > float64(len(q.Levels)-1) || n != float64(int(n)) {
			return fmt.Errorf("%v is not a level index 0-%d", v, len(q.Levels)-1)
		}
	default:
		if n, ok := v.(float64); !ok || (n != 0 && n != 1) {
			return fmt.Errorf("%v is not 0 or 1", v)
		}
	}
	return nil
}
