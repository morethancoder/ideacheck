package rubric

import (
	"errors"
	"fmt"

	"github.com/morethancoder/ideacheck/internal/judge"
)

const (
	minLevels = 2
	maxLevels = 10
	// maxOptions is Jev's documented limit for a choice question.
	maxOptions = 255
)

var stateFields = map[string]bool{"idea": true, "profile": true}

// Validate enforces the question-writing rules shared by every rubric file.
// All problems are reported at once so a rubric author fixes them in one pass.
func (rb *Rubric) Validate() error {
	if len(rb.Questions) == 0 {
		return errors.New("no questions")
	}
	var errs []error
	seen := map[string]bool{}
	for i, q := range rb.Questions {
		if q.ID == "" {
			errs = append(errs, fmt.Errorf("question %d: missing id", i))
			continue
		}
		if seen[q.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", q.ID))
		}
		seen[q.ID] = true
		for _, err := range validateQuestion(q) {
			errs = append(errs, fmt.Errorf("%s: %w", q.ID, err))
		}
	}
	return errors.Join(errs...)
}

func validateQuestion(q judge.Question) []error {
	var errs []error
	if q.Instructions == "" {
		errs = append(errs, errors.New("missing instructions"))
	}
	errs = append(errs, validateKind(q)...)
	errs = append(errs, validateWeight(q)...)
	errs = append(errs, validateUses(q.Uses)...)
	errs = append(errs, validateRequires(q)...)
	return errs
}

func validateKind(q judge.Question) []error {
	switch q.Kind {
	case judge.Choice:
		return validateChoice(q)
	case judge.Score:
		return validateScore(q)
	case judge.Noul:
		return validateNoul(q)
	}
	return []error{fmt.Errorf("kind %q is not one of choice, score, noul", q.Kind)}
}

func validateChoice(q judge.Question) []error {
	var errs []error
	if _, ok := q.Options[Other]; !ok {
		errs = append(errs, fmt.Errorf("choice must include an explicit %q option", Other))
	}
	if n := len(q.Options); n < 2 || n > maxOptions {
		errs = append(errs, fmt.Errorf("choice has %d options, want 2-%d", n, maxOptions))
	}
	if len(q.Levels) > 0 {
		errs = append(errs, errors.New("choice must not set levels"))
	}
	for key, v := range q.Values {
		if _, ok := q.Options[key]; !ok {
			errs = append(errs, fmt.Errorf("values key %q is not an option", key))
		}
		if v < 0 || v > 1 {
			errs = append(errs, fmt.Errorf("values[%s] = %v, want within [0,1]", key, v))
		}
	}
	return errs
}

func validateScore(q judge.Question) []error {
	var errs []error
	if n := len(q.Levels); n < minLevels || n > maxLevels {
		errs = append(errs, fmt.Errorf("score has %d levels, want %d-%d", n, minLevels, maxLevels))
	}
	for i, l := range q.Levels {
		if l == "" {
			errs = append(errs, fmt.Errorf("level %d is empty", i))
		}
	}
	if len(q.Options) > 0 || len(q.Values) > 0 {
		errs = append(errs, errors.New("score must not set options or values"))
	}
	return errs
}

func validateNoul(q judge.Question) []error {
	if len(q.Options) > 0 || len(q.Levels) > 0 || len(q.Values) > 0 {
		return []error{errors.New("noul must not set options, levels or values")}
	}
	return nil
}

// validateWeight: weights are never negative, and anything that counts toward
// the composite must say which direction is good.
func validateWeight(q judge.Question) []error {
	var errs []error
	if q.Weight < 0 {
		errs = append(errs, fmt.Errorf("weight %v is negative", q.Weight))
	}
	if q.Polarity < -1 || q.Polarity > 1 {
		errs = append(errs, fmt.Errorf("polarity %d is not one of -1, 0, 1", q.Polarity))
	}
	if q.Weight > 0 && q.Polarity == 0 {
		errs = append(errs, errors.New("weighted question must set polarity to 1 or -1"))
	}
	if q.Weight > 0 && q.Kind == judge.Choice && len(q.Values) == 0 {
		errs = append(errs, errors.New("weighted choice must map option keys to values"))
	}
	return errs
}

func validateUses(uses []string) []error {
	if len(uses) == 0 {
		return []error{errors.New("uses must list the state fields the question needs")}
	}
	var errs []error
	for _, u := range uses {
		if !stateFields[u] {
			errs = append(errs, fmt.Errorf("uses %q is not one of idea, profile", u))
		}
	}
	return errs
}

// validateRequires: a question cannot require state it does not even look at,
// and requiring everything would make the question unanswerable in every run.
func validateRequires(q judge.Question) []error {
	var errs []error
	for _, r := range q.Requires {
		if !stateFields[r] {
			errs = append(errs, fmt.Errorf("requires %q is not one of idea, profile", r))
			continue
		}
		if !contains(q.Uses, r) {
			errs = append(errs, fmt.Errorf("requires %q but does not use it", r))
		}
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (rb *Rubric) validateGaps() error {
	if rb.Threshold <= 0 || rb.Threshold >= 1 {
		return fmt.Errorf("threshold %v must be strictly between 0 and 1", rb.Threshold)
	}
	if rb.ConfidencePenalty < 0 || rb.ConfidencePenalty > 1 {
		return fmt.Errorf("confidence_penalty %v must be within [0,1]", rb.ConfidencePenalty)
	}
	for _, q := range rb.Questions {
		if q.Kind != judge.Noul {
			return fmt.Errorf("%s: gap questions must be noul", q.ID)
		}
		if q.Ask == "" || q.Fills == "" {
			return fmt.Errorf("%s: gap questions must set ask and fills", q.ID)
		}
	}
	return nil
}

func (rb *Rubric) validateRouter() error {
	if len(rb.Questions) != 1 || rb.Questions[0].Kind != judge.Choice {
		return errors.New("router must contain exactly one choice question")
	}
	return nil
}

func (rb *Rubric) validateScoring() error {
	if rb.ConfidencePenalty != 0 {
		return errors.New("confidence_penalty belongs to _gaps.yaml only")
	}
	var total float64
	for _, q := range rb.Questions {
		total += q.Weight
	}
	if total <= 0 {
		return errors.New("no weighted questions; the composite would be undefined")
	}
	return rb.Verdict.validate(rb.Questions)
}
