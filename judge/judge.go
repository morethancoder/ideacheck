// Package judge defines the single interface through which all model access
// happens, plus the typed questions and answers exchanged over it.
package judge

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

type Kind string

const (
	Choice Kind = "choice"
	Score  Kind = "score"
	Noul   Kind = "noul"
)

type Question struct {
	ID           string            `yaml:"id" json:"id"`
	Kind         Kind              `yaml:"kind" json:"kind"`
	Instructions string            `yaml:"instructions" json:"instructions"`
	Options      map[string]string `yaml:"options,omitempty" json:"options,omitempty"`   // choice: key -> description
	Levels       []string          `yaml:"levels,omitempty" json:"levels,omitempty"`     // score: ordered, index 0 first
	Criteria     *NoulCriteria     `yaml:"criteria,omitempty" json:"criteria,omitempty"` // noul only, optional: the boundary between yes and no
	// Rubric-only metadata (ignored by backends):
	Weight   float64            `yaml:"weight,omitempty" json:"weight,omitempty"`
	Polarity int                `yaml:"polarity,omitempty" json:"polarity,omitempty"` // +1 good-when-high, -1 bad-when-high, 0 informational
	Uses     []string           `yaml:"uses,omitempty" json:"uses,omitempty"`         // which state fields to include: ["idea","profile"]
	Requires []string           `yaml:"requires,omitempty" json:"requires,omitempty"` // state this question cannot be judged without; absent → not asked, not scored
	Ask      string             `yaml:"ask,omitempty" json:"ask,omitempty"`           // gaps only: follow-up question shown to the user
	Fills    string             `yaml:"fills,omitempty" json:"fills,omitempty"`       // gaps only: intake field the follow-up reply is stored in
	Values   map[string]float64 `yaml:"values,omitempty" json:"values,omitempty"`     // choice only: option key -> value in [0,1] for aggregation
	Derive   *Derive            `yaml:"derive,omitempty" json:"derive,omitempty"`     // noul only: answer without the judge when this rule can
}

// Derive answers a noul from what the check already knows, so a judgment
// simple code can make never costs a model call. The rule is config; Go only
// runs it. Exactly one field is set. When the rule cannot decide, the judge is
// asked as usual.
type Derive struct {
	// Present is 1 when any of these intake fields ("profile.skills") has a value, else 0.
	Present []string `yaml:"present,omitempty" json:"present,omitempty"`
	// SameAs reuses the answer the judge gave that gap question in this check.
	SameAs string `yaml:"same_as,omitempty" json:"same_as,omitempty"`
	// Evidence is 1 when a finding in the topic was typed with the relation,
	// and 0 when the topic was searched and every finding in it typed otherwise.
	Evidence *EvidenceRule `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	// ZeroWhenUnstated is 0 when this intake field is empty and its gap
	// question found the description does not state it either.
	ZeroWhenUnstated string `yaml:"zero_when_unstated,omitempty" json:"zero_when_unstated,omitempty"`
}

// EvidenceRule names a research topic and a relation from rubrics/_evidence.yaml.
type EvidenceRule struct {
	Topic    string `yaml:"topic" json:"topic"`
	Relation string `yaml:"relation" json:"relation"`
}

// Rule names the one rule set, "" for none.
func (d *Derive) Rule() string {
	switch {
	case d == nil:
		return ""
	case len(d.Present) > 0:
		return "present"
	case d.SameAs != "":
		return "same_as"
	case d.Evidence != nil:
		return "evidence"
	case d.ZeroWhenUnstated != "":
		return "zero_when_unstated"
	}
	return ""
}

// Rules counts the rules set; a valid Derive sets exactly one.
func (d *Derive) Rules() int {
	n := 0
	for _, set := range []bool{len(d.Present) > 0, d.SameAs != "", d.Evidence != nil, d.ZeroWhenUnstated != ""} {
		if set {
			n++
		}
	}
	return n
}

// NoulCriteria says what counts as a yes and what counts as a no, for a noul
// whose boundary the statement alone leaves open. The YAML keys are yes/no
// because a bare `true:` key parses as a boolean.
type NoulCriteria struct {
	Yes string `yaml:"yes" json:"yes"`
	No  string `yaml:"no" json:"no"`
}

type Answer struct {
	ID            string             `json:"id"`
	Kind          Kind               `json:"kind"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"` // fractional level index
	Noul          float64            `json:"noul,omitempty"`
	Probabilities map[string]float64 `json:"probabilities"` // option key or level index → p
	Confidence    float64            `json:"confidence"`    // 0..1, backend-defined
	Method        string             `json:"method"`        // "jev" | "logprob" | "verbalized" | "vote:k=5" | "mock"
	Model         string             `json:"model"`         // exact model id that answered
	LatencyMS     int64              `json:"latency_ms"`
	TokensIn      int                `json:"tokens_in,omitempty"`
	TokensOut     int                `json:"tokens_out,omitempty"`
	TokensCached  int                `json:"tokens_cached,omitempty"` // part of tokens_in read from a prompt cache
	CostUSD       float64            `json:"cost_usd,omitempty"`      // cost the provider itself reported; 0 = not reported
	OrderBias     float64            `json:"order_bias,omitempty"`    // logprob: mean |Δp| between shuffled runs
	Err           string             `json:"error,omitempty"`         // per-question failure; never fails the batch
}

// Failed reports whether the question went unanswered.
func (a Answer) Failed() bool { return a.Err != "" }

// State is whatever the question needs. Backends serialize it as JSON (Jev accepts
// string | object | array). Keep it minimal per question (see Question.Uses).
type State map[string]any

// Sub returns only the named fields: the context-rot rule says a question must
// not see state it does not need. "evidence.market" is one key of an object
// field, kept under its parent. Unknown names are skipped.
func (s State) Sub(uses []string) State {
	out := make(State, len(uses))
	for _, k := range uses {
		parent, key, dotted := strings.Cut(k, ".")
		if !dotted {
			if v, ok := s[k]; ok {
				out[k] = v
			}
			continue
		}
		if slices.Contains(uses, parent) {
			continue // the whole field is asked for anyway
		}
		from, _ := s[parent].(map[string]any)
		v, ok := from[key]
		if !ok {
			continue
		}
		part, _ := out[parent].(map[string]any)
		if part == nil {
			part = map[string]any{}
			out[parent] = part
		}
		part[key] = v
	}
	return out
}

type Judge interface {
	Name() string
	// Evaluate answers ONE question. Called concurrently by the fan-out.
	Evaluate(ctx context.Context, state State, q Question) (Answer, error)
	// Capabilities lets the fan-out pick batch vs per-question strategy.
	Capabilities() Capabilities
}

type Capabilities struct {
	NativeBatch   bool // backend can answer many questions in one request (jev, structured)
	RealLogprobs  bool
	MaxConcurrent int // 0 = use config default
}

// Narrator is optionally implemented by backends that can also write prose:
// the plain-language summary of a result. It is not a judgment — nothing is
// computed from its text.
type Narrator interface {
	Narrate(ctx context.Context, system, brief string) (Narration, error)
}

// Extractor is optionally implemented by backends that can pull named fields
// out of a document. Like Narrate it is not a judgment: the values only fill
// intake fields the user left empty, and nothing is scored from the call.
type Extractor interface {
	Extract(ctx context.Context, system, user string, fields []string) (Extraction, error)
}

// Researcher is optionally implemented by backends that can look things up on
// the web. Like Extract it is not a judgment: it returns findings with their
// sources, and every number still comes from a typed question asked afterwards.
type Researcher interface {
	// CanResearch reports whether this backend, as configured, can reach the web.
	CanResearch() bool
	Research(ctx context.Context, system, user string, topics []string) (Research, error)
}

// Planner and Digester are the writer's two jobs when ideacheck does the
// searching itself (package search): say what to search for, then say what
// the results amount to. Neither touches the web, so any chat model can do
// them — including a local one that could never research on its own.
type Planner interface {
	Plan(ctx context.Context, system, user string, topics []string) (Plan, error)
}

type Digester interface {
	Digest(ctx context.Context, system, user string, topics []string) (Research, error)
}

// Query is one search to run for a topic.
type Query struct {
	Topic string `json:"topic"`
	Query string `json:"query"`
}

// Plan is the searches worth running, plus what the call cost.
type Plan struct {
	Queries      []Query
	Model        string
	TokensIn     int
	TokensOut    int
	TokensCached int
	CostUSD      float64
}

// Finding is one thing the research turned up, with where it came from.
type Finding struct {
	Topic   string `json:"topic"` // a topic id from research.yaml
	Title   string `json:"title"`
	Summary string `json:"summary"`
	URL     string `json:"url"`
}

// Research is the findings, plus what the call cost.
type Research struct {
	Findings     []Finding
	Model        string
	TokensIn     int
	TokensOut    int
	TokensCached int
	CostUSD      float64
}

// Extraction is the values read from the document, plus what the call cost.
type Extraction struct {
	Values       map[string]string
	Model        string
	TokensIn     int
	TokensOut    int
	TokensCached int
	CostUSD      float64
}

type Narration struct {
	Text         string
	Model        string
	TokensIn     int
	TokensOut    int
	TokensCached int
	CostUSD      float64
}

// BatchJudge is optionally implemented by backends with NativeBatch.
type BatchJudge interface {
	EvaluateBatch(ctx context.Context, state State, qs []Question) ([]Answer, error)
}

// errNoAnswer is the per-question failure for a batch reply that skipped a question.
var errNoAnswer = errors.New("backend returned no answer for this question")

// RetryableError marks a failure the fan-out may retry (408/429/5xx/timeouts).
// After carries a server-provided Retry-After; zero means "use backoff".
type RetryableError struct {
	Err   error
	After time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// retryAfter reports whether err is retryable and any server-requested delay.
func retryAfter(err error) (time.Duration, bool) {
	var re *RetryableError
	if errors.As(err, &re) {
		return re.After, true
	}
	return 0, errors.Is(err, context.DeadlineExceeded)
}
