package rubric

import (
	"errors"
	"fmt"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge"
)

const (
	minLevels = 2
	maxLevels = 10
	// maxOptions is Jev's documented limit for a choice question.
	maxOptions = 255
)

// stateFields are the names a question may read. `evidence` is what research
// found; `finding` is one of those findings, seen only by _evidence.yaml.
var stateFields = map[string]bool{"idea": true, "profile": true, "evidence": true, "finding": true}

const stateNames = "idea, profile, evidence, finding"

// evidencePrefix reads one research topic: evidence.competitors is the
// competitors topic alone, and only the topics a rubric reads are searched.
const evidencePrefix = "evidence."

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
	errs = append(errs, validateDerive(q)...)
	return errs
}

// validateDerive: one rule, on a noul, reading evidence the question reads.
// What the rule names in other files (gap ids, intake fields, relations) is
// checked by the pipeline, which loads those files.
func validateDerive(q judge.Question) []error {
	d := q.Derive
	if d == nil {
		return nil
	}
	var errs []error
	if d.Rules() != 1 {
		errs = append(errs, fmt.Errorf("derive sets %d rules, want exactly one of present, same_as, evidence, zero_when_unstated", d.Rules()))
	}
	if q.Kind != judge.Noul {
		errs = append(errs, errors.New("derive answers nouls only"))
	}
	for _, f := range d.Present {
		if f == "" {
			errs = append(errs, errors.New("derive present lists an empty field"))
		}
	}
	if ev := d.Evidence; ev != nil {
		if ev.Topic == "" || ev.Relation == "" {
			errs = append(errs, errors.New("derive evidence needs topic and relation"))
		} else if !contains(q.Uses, "evidence") && !contains(q.Uses, evidencePrefix+ev.Topic) {
			errs = append(errs, fmt.Errorf("derive reads evidence.%s but the question does not use it", ev.Topic))
		}
	}
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
	if len(q.Levels) > 0 || q.Criteria != nil {
		errs = append(errs, errors.New("choice must not set levels or criteria"))
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
	if len(q.Options) > 0 || len(q.Values) > 0 || q.Criteria != nil {
		errs = append(errs, errors.New("score must not set options, values or criteria"))
	}
	return errs
}

func validateNoul(q judge.Question) []error {
	var errs []error
	if len(q.Options) > 0 || len(q.Levels) > 0 || len(q.Values) > 0 {
		errs = append(errs, errors.New("noul must not set options, levels or values"))
	}
	// Criteria that describe only one side leave the other to a guess.
	if c := q.Criteria; c != nil && (c.Yes == "" || c.No == "") {
		errs = append(errs, errors.New("criteria must describe both yes and no"))
	}
	return errs
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
		if topic, ok := strings.CutPrefix(u, evidencePrefix); ok {
			if topic == "" || strings.Contains(topic, ".") {
				errs = append(errs, fmt.Errorf("uses %q must name one research topic, as evidence.<topic id>", u))
			}
			continue
		}
		if !stateFields[u] {
			errs = append(errs, fmt.Errorf("uses %q is not one of %s, or evidence.<topic id>", u, stateNames))
		}
	}
	return errs
}

// reads reports whether uses includes the field, whole or one key of it.
func reads(uses []string, field string) bool {
	for _, u := range uses {
		if u == field || strings.HasPrefix(u, field+".") {
			return true
		}
	}
	return false
}

// validateRequires: a question cannot require state it does not even look at,
// and requiring everything would make the question unanswerable in every run.
func validateRequires(q judge.Question) []error {
	var errs []error
	for _, r := range q.Requires {
		if !stateFields[r] {
			errs = append(errs, fmt.Errorf("requires %q is not one of %s", r, stateNames))
			continue
		}
		if !reads(q.Uses, r) {
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
	if rb.AskLimit < 0 {
		return fmt.Errorf("ask_limit %d must not be negative", rb.AskLimit)
	}
	for _, q := range rb.Questions {
		if q.Kind != judge.Noul {
			return fmt.Errorf("%s: gap questions must be noul", q.ID)
		}
		if q.Ask == "" || q.Fills == "" {
			return fmt.Errorf("%s: gap questions must set ask and fills", q.ID)
		}
		// Gaps are asked first: nothing else is known yet to derive from.
		if r := q.Derive.Rule(); r != "" && r != "present" {
			return fmt.Errorf("%s: a gap question can derive only from present fields, not %s", q.ID, r)
		}
	}
	return nil
}

// validateEvidence: one choice question per finding, and the options that drop
// a finding must be options of it.
func (rb *Rubric) validateEvidence() error {
	if len(rb.Questions) != 1 || rb.Questions[0].Kind != judge.Choice {
		return errors.New("_evidence must contain exactly one choice question")
	}
	q := rb.Questions[0]
	if !contains(q.Uses, "finding") {
		return fmt.Errorf("%s: must use `finding`, the item it is asked about", q.ID)
	}
	for _, d := range rb.Drop {
		if _, ok := q.Options[d]; !ok {
			return fmt.Errorf("drop %q is not an option of %s", d, q.ID)
		}
	}
	return nil
}

func (rb *Rubric) validateRouter() error {
	if len(rb.Questions) != 1 || rb.Questions[0].Kind != judge.Choice {
		return errors.New("router must contain exactly one choice question")
	}
	if rb.Fallback == "" {
		return errors.New("router must name a fallback rubric")
	}
	return nil
}

func (rb *Rubric) validateScoring() error {
	if rb.ConfidencePenalty != 0 || rb.AskLimit != 0 {
		return errors.New("confidence_penalty and ask_limit belong to _gaps.yaml only")
	}
	if len(rb.Drop) > 0 {
		return errors.New("drop belongs to _evidence.yaml only")
	}
	if rb.Fallback != "" {
		return errors.New("fallback belongs to _router.yaml only")
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
