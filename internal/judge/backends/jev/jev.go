// Package jev is the client for TypeSafe's System One endpoint. There is no Go
// SDK; the wire format below was verified against @typesafe-ai/sdk 0.6.0 and
// typesafe-sdk 0.7.0 (the Python wire models mirror the OpenAPI schema).
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/logging"
)

const (
	Name   = "jev"
	method = "jev"
	path   = "/v1/systemone"
)

type Judge struct {
	BaseURL       string
	APIKey        string // from TYPESAFE_API_KEY; never logged
	Model         string // pinned (jev-1.13.0): jev-latest moves
	MaxConcurrent int
	HTTP          *http.Client
}

func (j *Judge) Name() string { return Name }
func (j *Judge) Capabilities() judge.Capabilities {
	return judge.Capabilities{NativeBatch: true, MaxConcurrent: j.MaxConcurrent}
}

// question is the wire form. criteria is a label→description map for choice, an
// ordered list for score, and absent for noul.
type question struct {
	Type         judge.Kind `json:"type"`
	Instructions string     `json:"instructions"`
	Criteria     any        `json:"criteria,omitempty"`
}

type request struct {
	State     judge.State         `json:"state"`
	Questions map[string]question `json:"questions"`
	Model     string              `json:"model"`
}

type answer struct {
	Type          judge.Kind         `json:"type"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Noul          float64            `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type response struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Answers map[string]answer `json:"answers"`
}

func wire(q judge.Question) question {
	w := question{Type: q.Kind, Instructions: q.Instructions}
	switch q.Kind {
	case judge.Choice:
		w.Criteria = q.Options
	case judge.Score:
		w.Criteria = q.Levels
	}
	return w
}

func (j *Judge) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	answers, err := j.EvaluateBatch(ctx, state, []judge.Question{q})
	if err != nil {
		return judge.Answer{}, err
	}
	if len(answers) == 0 {
		return judge.Answer{}, fmt.Errorf("jev returned no answer for %s", q.ID)
	}
	return answers[0], nil
}

// EvaluateBatch sends every question in one request; Jev evaluates them in parallel.
func (j *Judge) EvaluateBatch(ctx context.Context, state judge.State, qs []judge.Question) ([]judge.Answer, error) {
	req := request{State: state, Model: j.Model, Questions: make(map[string]question, len(qs))}
	for _, q := range qs {
		req.Questions[q.ID] = wire(q)
	}
	res, err := j.post(ctx, req)
	if err != nil {
		return nil, err
	}
	logging.From(ctx).Debug().Str("model", res.Model).Int("tokens_in", res.Usage.InputTokens).Int("questions", len(qs)).Msg("jev response")
	var out []judge.Answer
	for _, q := range qs {
		a, ok := res.Answers[q.ID]
		if !ok || a.Type != q.Kind {
			continue // the fan-out reports a missing answer as a per-question failure
		}
		out = append(out, convert(q, a, res.Model))
	}
	if len(out) > 0 { // usage belongs to the request: book it once
		out[0].TokensIn, out[0].TokensOut = res.Usage.InputTokens, res.Usage.OutputTokens
	}
	return out, nil
}

func convert(q judge.Question, a answer, model string) judge.Answer {
	out := judge.Answer{ID: q.ID, Method: method, Model: model, Probabilities: a.Probabilities, Confidence: a.Confidence}
	switch q.Kind {
	case judge.Choice:
		out.Choice = a.Choice
	case judge.Score:
		out.Score = a.Score
	default: // Jev nouls carry no confidence field; derive it the same way every backend does
		out.Noul, out.Confidence, out.Probabilities = a.Noul, judge.NoulConfidence(a.Noul), nil
	}
	return out
}

func (j *Judge) post(ctx context.Context, body request) (*response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(j.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+j.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := j.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, &judge.RetryableError{Err: fmt.Errorf("request to %s: %w", url, err)}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, judge.HTTPError(res.StatusCode, res.Header, string(data))
	}
	var out response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode jev response: %w", err)
	}
	return &out, nil
}
