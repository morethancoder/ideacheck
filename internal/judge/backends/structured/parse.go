package structured

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/morethancoder/ideacheck/internal/judge"
)

// reply is the union of every answer shape; unused fields stay nil.
type reply struct {
	Choice        *string            `json:"choice"`
	Level         *int               `json:"level"`
	Yes           *bool              `json:"yes"`
	Probability   *float64           `json:"probability"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    *float64           `json:"confidence"`
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// normalized rescales a stated distribution to sum to 1 over the allowed
// outcomes; models routinely state probabilities that sum to 0.9 or 1.1.
func normalized(stated map[string]float64, allowed []string) (map[string]float64, error) {
	out := make(map[string]float64, len(allowed))
	var sum float64
	for _, k := range allowed {
		p := stated[k]
		if p < 0 {
			p = 0
		}
		out[k] = p
		sum += p
	}
	if sum == 0 {
		return nil, fmt.Errorf("stated probabilities are all zero")
	}
	for k := range out {
		out[k] /= sum
	}
	return out, nil
}

// fromVerbalized converts one verbalized reply into an Answer.
func fromVerbalized(q judge.Question, r reply) (judge.Answer, error) {
	if q.Kind == judge.Noul {
		if r.Probability == nil {
			return judge.Answer{}, fmt.Errorf("reply has no probability")
		}
		p := clamp01(*r.Probability)
		return judge.Answer{Noul: p, Confidence: judge.NoulConfidence(p)}, nil
	}
	probs, err := normalized(r.Probabilities, outcomes(q))
	if err != nil {
		return judge.Answer{}, err
	}
	a := judge.Answer{Probabilities: probs}
	if r.Confidence != nil {
		a.Confidence = clamp01(*r.Confidence)
	}
	if q.Kind == judge.Score {
		a.Score = judge.ExpectedLevel(probs)
		return a, nil
	}
	a.Choice = judge.Argmax(probs)
	return a, nil
}

// vote is one sample's discrete answer: an option key, a level index, or "yes"/"no".
func vote(q judge.Question, r reply) (string, error) {
	switch {
	case q.Kind == judge.Noul && r.Yes != nil:
		if *r.Yes {
			return "yes", nil
		}
		return "no", nil
	case q.Kind == judge.Choice && r.Choice != nil:
		if _, ok := q.Options[*r.Choice]; ok {
			return *r.Choice, nil
		}
		return "", fmt.Errorf("choice %q is not an option", *r.Choice)
	case q.Kind == judge.Score && r.Level != nil:
		if *r.Level >= 0 && *r.Level < len(q.Levels) {
			return strconv.Itoa(*r.Level), nil
		}
		return "", fmt.Errorf("level %d is out of range", *r.Level)
	}
	return "", fmt.Errorf("reply does not answer a %s question", q.Kind)
}

// fromVotes turns k samples into frequencies. Confidence is the winner's share.
func fromVotes(q judge.Question, votes []string) judge.Answer {
	keys := outcomes(q)
	if q.Kind == judge.Noul {
		keys = []string{"yes", "no"}
	}
	probs := make(map[string]float64, len(keys))
	for _, k := range keys {
		probs[k] = 0
	}
	for _, v := range votes {
		probs[v] += 1 / float64(len(votes))
	}
	winner := judge.Argmax(probs)
	a := judge.Answer{Probabilities: probs, Confidence: probs[winner]}
	switch q.Kind {
	case judge.Noul:
		a.Noul = probs["yes"]
	case judge.Score:
		a.Score = judge.ExpectedLevel(probs)
	default:
		a.Choice = winner
	}
	return a
}

func decode(text string, into any) error {
	if err := json.Unmarshal([]byte(text), into); err != nil {
		return fmt.Errorf("model reply is not the requested JSON: %w", err)
	}
	return nil
}
