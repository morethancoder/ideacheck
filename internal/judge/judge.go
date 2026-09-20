// Package judge defines the single interface through which all model access
// happens, plus the typed questions and answers exchanged over it.
package judge

import (
	"context"
	"errors"
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
	Options      map[string]string `yaml:"options,omitempty" json:"options,omitempty"` // choice: key -> description
	Levels       []string          `yaml:"levels,omitempty" json:"levels,omitempty"`   // score: ordered, index 0 first
	// Rubric-only metadata (ignored by backends):
	Weight   float64            `yaml:"weight,omitempty" json:"weight,omitempty"`
	Polarity int                `yaml:"polarity,omitempty" json:"polarity,omitempty"` // +1 good-when-high, -1 bad-when-high, 0 informational
	Uses     []string           `yaml:"uses,omitempty" json:"uses,omitempty"`         // which state fields to include: ["idea","profile"]
	Requires []string           `yaml:"requires,omitempty" json:"requires,omitempty"` // state this question cannot be judged without; absent → not asked, not scored
	Ask      string             `yaml:"ask,omitempty" json:"ask,omitempty"`           // gaps only: follow-up question shown to the user
	Fills    string             `yaml:"fills,omitempty" json:"fills,omitempty"`       // gaps only: intake field the follow-up reply is stored in
	Values   map[string]float64 `yaml:"values,omitempty" json:"values,omitempty"`     // choice only: option key -> value in [0,1] for aggregation
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

// Sub returns only the named top-level fields: the context-rot rule says a
// question must not see state it does not need. Unknown names are skipped.
func (s State) Sub(uses []string) State {
	out := make(State, len(uses))
	for _, k := range uses {
		if v, ok := s[k]; ok {
			out[k] = v
		}
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
