package pipeline

import (
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
)

const (
	StatusOK         = "ok"
	StatusNeedsInput = "needs_input"
	StatusError      = "error"
)

// Result is the stable output contract for agent mode and the server;
// schemas/check_result.schema.json is generated from this type, and a test fails
// when the two drift.
type Result struct {
	Status   string    `json:"status"`
	ID       string    `json:"id"`
	Backend  string    `json:"backend"`
	Model    string    `json:"model"`
	Method   string    `json:"method"`
	IdeaType *IdeaType `json:"idea_type,omitempty"`
	Missing  []Missing `json:"missing"`
	// Partial is true when the idea was scored although facts in missing[] were
	// never stated. The scores stand; the confidence is discounted.
	Partial bool `json:"partial,omitempty"`
	// Extracted names the intake fields read out of the document by the extract
	// stage rather than given by the user.
	Extracted           []string       `json:"extracted,omitempty"`
	Answers             []judge.Answer `json:"answers"`
	Composite           float64        `json:"composite"`
	CompositeConfidence float64        `json:"composite_confidence"`
	Verdict             string         `json:"verdict,omitempty"`
	VerdictReason       string         `json:"verdict_reason,omitempty"`
	Dimensions          []Dimension    `json:"dimensions"`
	TopStrengths        []Contribution `json:"top_strengths"`
	TopRisks            []Contribution `json:"top_risks"`
	Timing              Timing         `json:"timing"`
	CostEstimateUSD     float64        `json:"cost_estimate_usd"`
	Cost                Cost           `json:"cost"`
	Summary             string         `json:"summary,omitempty"` // plain-language why, written by the model after scoring
	Rubric              *RubricRef     `json:"rubric,omitempty"`
	ConfigHash          string         `json:"config_hash"`
	Warnings            []string       `json:"warnings,omitempty"`
	Error               string         `json:"error,omitempty"`
	CreatedAt           string         `json:"created_at"`
}

// Cost is what this check spent, with the working shown.
type Cost struct {
	USD          float64 `json:"usd"`
	Basis        string  `json:"basis"` // reported | priced | free | unpriced (see Cost* constants)
	Model        string  `json:"model,omitempty"`
	TokensIn     int     `json:"tokens_in"` // cached included
	TokensCached int     `json:"tokens_cached,omitempty"`
	TokensOut    int     `json:"tokens_out"`
	// Price is the per-million-token rate used for tokens the provider did not
	// cost itself; nil when no price is known or none applies.
	Price *config.Price `json:"price_per_mtok,omitempty"`
	Note  string        `json:"note,omitempty"`
}

const (
	CostReported = "reported" // the provider stated the cost (the Claude CLI does)
	CostPriced   = "priced"   // tokens × the pricing table in config
	CostFree     = "free"     // a local model: no per-token charge
	CostUnpriced = "unpriced" // the model has no entry in pricing:
)

type IdeaType struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

type Missing struct {
	ID          string  `json:"id"`
	Probability float64 `json:"probability"`
	Ask         string  `json:"ask"`
	Fills       string  `json:"fills"`
}

type Timing struct {
	TotalMS         int64  `json:"total_ms"`
	SlowestQuestion string `json:"slowest_question,omitempty"`
}

type RubricRef struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// Dimension is one rubric question's score: the per-dimension scores are the
// explanation. Value is normalized to [0,1] BEFORE polarity (for a Polarity -1
// question, high is bad). Nil Value means unanswered or not valued.
type Dimension struct {
	ID         string   `json:"id"`
	Value      *float64 `json:"value" jsonschema:"nullable"` // null when the question went unanswered
	Weight     float64  `json:"weight"`
	Polarity   int      `json:"polarity"`
	Confidence float64  `json:"confidence"`
	Error      string   `json:"error,omitempty"`
}
