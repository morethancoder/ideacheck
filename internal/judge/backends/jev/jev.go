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

// The System One wire format is shared with the laya backend: Laya keeps
// upstream's question and answer schema, which mirrors this one.

// WireQuestion is the wire form. criteria is a label→description map for
// choice, an ordered list for score, and for noul an optional {"true", "false"} pair.
type WireQuestion struct {
	Type         judge.Kind `json:"type"`
	Instructions string     `json:"instructions"`
	Criteria     any        `json:"criteria,omitempty"`
}

type Request struct {
	State     judge.State             `json:"state"`
	Questions map[string]WireQuestion `json:"questions"`
	Model     string                  `json:"model,omitempty"`
}

type WireAnswer struct {
	Type          judge.Kind         `json:"type"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Noul          float64            `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Response struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Answers Answers `json:"answers"`
}

// NewRequest puts every question in one request over state.
func NewRequest(state judge.State, model string, qs []judge.Question) Request {
	req := Request{State: state, Model: model, Questions: make(map[string]WireQuestion, len(qs))}
	for _, q := range qs {
		req.Questions[q.ID] = Wire(q)
	}
	return req
}

// Wire strips a question down to what the model sees: rubric metadata stays home.
func Wire(q judge.Question) WireQuestion {
	w := WireQuestion{Type: q.Kind, Instructions: q.Instructions}
	switch q.Kind {
	case judge.Choice:
		w.Criteria = q.Options
	case judge.Score:
		w.Criteria = q.Levels
	case judge.Noul:
		if c := q.Criteria; c != nil {
			w.Criteria = map[string]string{"true": c.Yes, "false": c.No}
		}
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
	res, err := j.post(ctx, NewRequest(state, j.Model, qs))
	if err != nil {
		return nil, err
	}
	logging.From(ctx).Debug().Str("model", res.Model).Int("tokens_in", res.Usage.InputTokens).Int("questions", len(qs)).Msg("jev response")
	return res.Answers.Convert(qs, method, res.Model, res.Usage.InputTokens, res.Usage.OutputTokens), nil
}

// Answers is a reply's answers by question id.
type Answers map[string]WireAnswer

// Convert turns the answers into ideacheck's, in question order, skipping a
// question the reply left out or answered as another kind: the fan-out reports
// that as a per-question failure. Usage belongs to the request, so it is booked
// once, on the first answer.
func (as Answers) Convert(qs []judge.Question, method, model string, tokensIn, tokensOut int) []judge.Answer {
	var out []judge.Answer
	for _, q := range qs {
		a, ok := as[q.ID]
		if !ok || a.Type != q.Kind {
			continue
		}
		out = append(out, convert(q, a, method, model))
	}
	if len(out) > 0 {
		out[0].TokensIn, out[0].TokensOut = tokensIn, tokensOut
	}
	return out
}

func convert(q judge.Question, a WireAnswer, method, model string) judge.Answer {
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

func (j *Judge) post(ctx context.Context, body Request) (*Response, error) {
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
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode jev response: %w", err)
	}
	return &out, nil
}
