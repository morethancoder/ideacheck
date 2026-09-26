// Package mock is the fixture-driven backend behind every pipeline test and
// `bench --dry-run`. It never touches the network.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/morethancoder/ideacheck/judge"
)

const (
	Name   = "mock"
	method = "mock"
)

type Judge struct {
	Seed        int64
	FixturesDir string // <question_id>.json holding an Answer; "" = seeded values only
}

func (j *Judge) Name() string                     { return Name }
func (j *Judge) Capabilities() judge.Capabilities { return judge.Capabilities{} }

// Evaluate returns the fixture for q.ID when one exists, else a value derived
// deterministically from (Seed, state, q.ID).
func (j *Judge) Evaluate(_ context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	a, err := j.fixture(q.ID)
	if errors.Is(err, fs.ErrNotExist) {
		a, err = j.seeded(state, q), nil
	}
	if err != nil {
		return judge.Answer{}, err
	}
	a.Method, a.Model = method, Name
	return a, nil
}

func (j *Judge) fixture(id string) (judge.Answer, error) {
	if j.FixturesDir == "" {
		return judge.Answer{}, fs.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(j.FixturesDir, id+".json"))
	if err != nil {
		return judge.Answer{}, err
	}
	var a judge.Answer
	if err := json.Unmarshal(b, &a); err != nil {
		return judge.Answer{}, fmt.Errorf("fixture %s.json: %w", id, err)
	}
	return a, nil
}

// frac maps (seed, state, id) to a stable number in [0,1): the same idea always
// gets the same answers, and different ideas get different ones, so a dry-run
// bench exercises the metrics with varied data.
func (j *Judge) frac(state judge.State, id string) float64 {
	h := fnv.New64a()
	b, _ := json.Marshal(state) // map keys are sorted: deterministic
	fmt.Fprintf(h, "%d/%s/", j.Seed, id)
	h.Write(b)
	return float64(h.Sum64()%10000) / 10000
}

func (j *Judge) seeded(state judge.State, q judge.Question) judge.Answer {
	f := j.frac(state, q.ID)
	switch q.Kind {
	case judge.Choice:
		return seededChoice(q, f)
	case judge.Score:
		return seededScore(q, f)
	}
	// Seeded nouls stay above 0.5 so gap detection passes unless a fixture says otherwise.
	p := 0.55 + 0.4*f
	return judge.Answer{Noul: p, Confidence: judge.NoulConfidence(p)}
}

// seededChoice picks a non-"other" option and gives it 0.8 of the mass.
func seededChoice(q judge.Question, f float64) judge.Answer {
	keys := make([]string, 0, len(q.Options))
	for k := range q.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	named := keys[:0:0]
	for _, k := range keys {
		if k != "other" {
			named = append(named, k)
		}
	}
	pick := named[int(f*float64(len(named)))]
	probs := make(map[string]float64, len(keys))
	for _, k := range keys {
		probs[k] = 0.2 / float64(len(keys)-1)
	}
	probs[pick] = 0.8
	return judge.Answer{Choice: pick, Probabilities: probs, Confidence: 0.8}
}

// seededScore spreads mass over the two levels around a fractional position.
func seededScore(q judge.Question, f float64) judge.Answer {
	pos := f * float64(len(q.Levels)-1)
	lo := math.Floor(pos)
	probs := map[string]float64{strconv.Itoa(int(lo)): 1 - (pos - lo)}
	if pos > lo {
		probs[strconv.Itoa(int(lo)+1)] = pos - lo
	}
	return judge.Answer{Score: pos, Probabilities: probs, Confidence: 0.7}
}

// researchFixture is the file of findings a test or an offline demo supplies.
const researchFixture = "research.json"

// CanResearch is true only with a findings fixture: the mock never invents a
// competitor, so without one a mock run is exactly what it was before research.
func (j *Judge) CanResearch() bool {
	if j.FixturesDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(j.FixturesDir, researchFixture))
	return err == nil
}

// Research returns the fixture's findings, keeping the topics asked about.
func (j *Judge) Research(_ context.Context, _, _ string, topics []string) (judge.Research, error) {
	b, err := os.ReadFile(filepath.Join(j.FixturesDir, researchFixture))
	if err != nil {
		return judge.Research{}, err
	}
	var all []judge.Finding
	if err := json.Unmarshal(b, &all); err != nil {
		return judge.Research{}, fmt.Errorf("fixture %s: %w", researchFixture, err)
	}
	out := judge.Research{Model: Name}
	for _, f := range all {
		if slices.Contains(topics, f.Topic) {
			out.Findings = append(out.Findings, f)
		}
	}
	return out, nil
}

// Narrate returns a fixed paragraph: the mock never writes real prose.
func (j *Judge) Narrate(context.Context, string, string) (judge.Narration, error) {
	return judge.Narration{Text: "Mock summary: the scores above were produced by the mock backend, not by a model.", Model: Name}, nil
}
