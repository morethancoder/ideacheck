package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/internal/judge"
)

var qs = []judge.Question{
	{ID: "idea_type", Kind: judge.Choice, Instructions: "Kind?", Options: map[string]string{"business": "Makes money", "other": "None"}, Weight: 9},
	{ID: "acuity", Kind: judge.Score, Instructions: "How acute?", Levels: []string{"none", "mild", "painful"}},
	{ID: "tarpit", Kind: judge.Noul, Instructions: "This is a tarpit."},
	{ID: "skipped", Kind: judge.Noul, Instructions: "Server omits this."},
}

const reply = `{"model":"jev-1.13.0","usage":{"input_tokens":812,"output_tokens":0},"answers":{
 "idea_type":{"type":"choice","choice":"business","confidence":0.91,"probabilities":{"business":0.91,"other":0.09}},
 "acuity":{"type":"score","score":1.7,"confidence":0.6,"legend":{"0":"none"},"probabilities":{"0":0.1,"1":0.2,"2":0.7}},
 "tarpit":{"type":"noul","noul":0.9}}}`

func TestWireFormat(t *testing.T) {
	var body map[string]any
	var auth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	j := &Judge{BaseURL: srv.URL + "/", APIKey: "k", Model: "jev-1.13.0"}
	got, err := j.EvaluateBatch(context.Background(), judge.State{"idea": map[string]any{"text": "x"}}, qs)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/systemone" || auth != "Bearer k" || body["model"] != "jev-1.13.0" {
		t.Errorf("path=%q auth=%q model=%v", gotPath, auth, body["model"])
	}
	sent := body["questions"].(map[string]any)
	choice := sent["idea_type"].(map[string]any)
	if choice["type"] != "choice" || choice["criteria"].(map[string]any)["business"] != "Makes money" {
		t.Errorf("choice question = %v (criteria must be a label→description map)", choice)
	}
	if _, leaked := choice["weight"]; leaked {
		t.Error("rubric metadata must not be sent to the model")
	}
	if levels := sent["acuity"].(map[string]any)["criteria"].([]any); len(levels) != 3 || levels[2] != "painful" {
		t.Errorf("score criteria must be the ordered level list: %v", levels)
	}
	if _, has := sent["tarpit"].(map[string]any)["criteria"]; has {
		t.Error("noul must send no criteria")
	}

	if len(got) != 3 {
		t.Fatalf("answers = %+v (the omitted question must be left for the fan-out to fail)", got)
	}
	c, s, n := got[0], got[1], got[2]
	if c.Choice != "business" || c.Confidence != 0.91 || c.Model != "jev-1.13.0" || c.Method != "jev" || c.TokensIn != 812 {
		t.Errorf("choice = %+v", c)
	}
	if s.Score != 1.7 || s.Probabilities["2"] != 0.7 || s.TokensIn != 0 {
		t.Errorf("score = %+v (usage is booked once)", s)
	}
	if n.Noul != 0.9 || math.Abs(n.Confidence-0.531004) > 1e-6 {
		t.Errorf("noul = %+v (confidence is derived: Jev sends none)", n)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("retry-after-ms", "1500")
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	j := &Judge{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := j.Evaluate(context.Background(), judge.State{}, qs[2])
	var re *judge.RetryableError
	if !errors.As(err, &re) || re.After != 1500*time.Millisecond {
		t.Errorf("429 → %v After=%v; retry-after-ms must win over Retry-After", err, re)
	}
	status = http.StatusUnauthorized
	if _, err = j.Evaluate(context.Background(), judge.State{}, qs[2]); err == nil || errors.As(err, &re) {
		t.Errorf("401 must be final, got %v", err)
	}
}
