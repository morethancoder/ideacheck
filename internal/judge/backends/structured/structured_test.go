package structured

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/openai"
	"github.com/morethancoder/ideacheck/internal/prompt"
)

var (
	noul   = judge.Question{ID: "tarpit", Kind: judge.Noul, Instructions: "Tarpit."}
	score  = judge.Question{ID: "acuity", Kind: judge.Score, Instructions: "How acute?", Levels: []string{"a", "b", "c", "d"}}
	choice = judge.Question{ID: "idea_type", Kind: judge.Choice, Instructions: "Kind?", Options: map[string]string{"business": "b", "content": "c", "other": "o"}}
	state  = judge.State{"idea": map[string]any{"text": "An app"}}
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// scripted replies in order; "ERR" fails that sample.
type scripted struct {
	mu      sync.Mutex
	replies []string
	schemas []map[string]any
}

func (s *scripted) Complete(_ context.Context, _, _, _ string, schema map[string]any) (Completion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schemas = append(s.schemas, schema)
	r := s.replies[0]
	s.replies = s.replies[1:]
	if r == "ERR" {
		return Completion{}, &judge.RetryableError{Err: errors.New("503")}
	}
	return Completion{Text: r, Model: "m-1", TokensIn: 100, TokensOut: 10}, nil
}

func newJudge(t *testing.T, mode string, k int, llm Completer) *Judge {
	t.Helper()
	p, err := prompt.Load(config.NewFiles(""), "prompts")
	if err != nil {
		t.Fatal(err)
	}
	return &Judge{Backend: Name, Prompts: p, LLM: llm, Mode: mode, VoteK: k}
}

func TestVoteFrequenciesBecomeProbabilities(t *testing.T) {
	llm := &scripted{replies: []string{`{"yes":true}`, `{"yes":true}`, `{"yes":false}`, `{"yes":true}`, `{"yes":false}`}}
	a, err := newJudge(t, ModeVote, 5, llm).Evaluate(context.Background(), state, noul)
	if err != nil {
		t.Fatal(err)
	}
	if !near(a.Noul, 0.6) || !near(a.Confidence, 0.6) || a.Method != "vote:k=5" || a.Model != "m-1" {
		t.Errorf("answer = %+v", a)
	}
	if a.TokensIn != 500 || a.TokensOut != 50 {
		t.Errorf("usage must sum over all 5 samples: in=%d out=%d", a.TokensIn, a.TokensOut)
	}

	llm = &scripted{replies: []string{`{"level":2}`, `{"level":2}`, `{"level":3}`, `{"level":1}`}}
	a, _ = newJudge(t, ModeVote, 4, llm).Evaluate(context.Background(), state, score)
	if !near(a.Score, 2.0) || !near(a.Probabilities["2"], 0.5) || !near(a.Probabilities["0"], 0) || !near(a.Confidence, 0.5) {
		t.Errorf("score answer = %+v", a)
	}

	llm = &scripted{replies: []string{`{"choice":"content"}`, `{"choice":"business"}`, `{"choice":"content"}`}}
	a, _ = newJudge(t, ModeVote, 3, llm).Evaluate(context.Background(), state, choice)
	if a.Choice != "content" || !near(a.Probabilities["content"], 2.0/3) || len(a.Probabilities) != 3 {
		t.Errorf("choice answer = %+v", a)
	}
}

func TestVoteSurvivesFailedAndInvalidSamples(t *testing.T) {
	llm := &scripted{replies: []string{`{"yes":true}`, "ERR", `{"yes":false}`, `not json`, `{"yes":true}`}}
	a, err := newJudge(t, ModeVote, 5, llm).Evaluate(context.Background(), state, noul)
	if err != nil || a.Method != "vote:k=3" || !near(a.Noul, 2.0/3) {
		t.Errorf("answer = %+v err=%v (method must report the real sample count)", a, err)
	}
	llm = &scripted{replies: []string{`{"choice":"podcast"}`, `{"choice":"content"}`}}
	a, _ = newJudge(t, ModeVote, 2, llm).Evaluate(context.Background(), state, choice)
	if a.Method != "vote:k=1" || a.Choice != "content" {
		t.Errorf("an out-of-schema vote must be dropped: %+v", a)
	}
	llm = &scripted{replies: []string{"ERR", "ERR"}}
	_, err = newJudge(t, ModeVote, 2, llm).Evaluate(context.Background(), state, noul)
	var re *judge.RetryableError
	if !errors.As(err, &re) {
		t.Errorf("all samples failing must surface the retryable error, got %v", err)
	}
}

func TestVerbalizedNormalizesStatedProbabilities(t *testing.T) {
	llm := &scripted{replies: []string{`{"level":2,"probabilities":{"0":0.0,"1":0.22,"2":0.55,"3":0.33},"confidence":1.4}`}}
	a, err := newJudge(t, ModeVerbalized, 5, llm).Evaluate(context.Background(), state, score)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, p := range a.Probabilities {
		sum += p
	}
	// stated mass 1.1 → p = {0, 0.2, 0.5, 0.3}; E[level] = 0.2 + 1.0 + 0.9 = 2.1
	if !near(sum, 1) || !near(a.Score, 2.1) || a.Confidence != 1 || a.Method != "verbalized" {
		t.Errorf("answer = %+v (sum %v)", a, sum)
	}
	if len(llm.schemas) != 1 {
		t.Errorf("verbalized must make exactly one call, made %d", len(llm.schemas))
	}
	llm = &scripted{replies: []string{`{"probability":0.9}`}}
	a, _ = newJudge(t, ModeVerbalized, 1, llm).Evaluate(context.Background(), state, noul)
	if !near(a.Noul, 0.9) || math.Abs(a.Confidence-0.531004) > 1e-6 {
		t.Errorf("noul = %+v", a)
	}
	llm = &scripted{replies: []string{`{"choice":"content","probabilities":{"business":0,"content":0,"other":0},"confidence":0.5}`}}
	a, _ = newJudge(t, ModeVerbalized, 1, llm).Evaluate(context.Background(), state, choice)
	if !a.Failed() {
		t.Errorf("all-zero probabilities must fail the question, got %+v", a)
	}
}

func TestBatchRepliesAreKeyedByQuestionID(t *testing.T) {
	llm := &scripted{replies: []string{`{"tarpit":{"yes":true},"acuity":{"level":3},"idea_type":{"choice":"business"}}`}}
	got, err := newJudge(t, ModeVote, 1, llm).EvaluateBatch(context.Background(), state, []judge.Question{noul, score, choice})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != "tarpit" || got[0].Noul != 1 || got[1].Score != 3 || got[2].Choice != "business" {
		t.Errorf("answers = %+v", got)
	}
	if got[0].TokensIn != 100 || got[1].TokensIn != 0 {
		t.Errorf("batch usage must be booked once, not per question: %d, %d", got[0].TokensIn, got[1].TokensIn)
	}
	props := llm.schemas[0]["properties"].(map[string]any)
	if len(props) != 3 || props["acuity"] == nil {
		t.Errorf("batch schema = %v", llm.schemas[0])
	}
}

func TestSchemaShapes(t *testing.T) {
	vote := SchemaFor(choice, ModeVote)
	b, _ := json.Marshal(vote)
	if string(b) != `{"additionalProperties":false,"properties":{"choice":{"enum":["business","content","other"],"type":"string"}},"required":["choice"],"type":"object"}` {
		t.Errorf("vote choice schema = %s", b)
	}
	verbal := SchemaFor(score, ModeVerbalized)
	dist := verbal["properties"].(map[string]any)["probabilities"].(map[string]any)
	if len(dist["properties"].(map[string]any)) != 4 || len(verbal["required"].([]string)) != 3 {
		t.Errorf("verbalized score schema = %v", verbal)
	}
}

const anthropicReply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","stop_reason":"end_turn","stop_sequence":null,
"content":[{"type":"text","text":"{\"yes\":true}"}],
"usage":{"input_tokens":40,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":300}}`

func TestAnthropicWireFormat(t *testing.T) {
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicReply)
	}))
	defer srv.Close()

	llm := NewAnthropic("claude-sonnet-5", 1024, "disabled", "", option.WithBaseURL(srv.URL), option.WithAPIKey("test"))
	got, err := llm.Complete(context.Background(), "SYSTEM", "STATE", "QUESTION", SchemaFor(noul, ModeVote))
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != `{"yes":true}` || got.Model != "claude-sonnet-5" || got.TokensIn != 340 || got.TokensOut != 7 {
		t.Errorf("completion = %+v (input must include cached tokens)", got)
	}
	if path != "/v1/messages" || body["model"] != "claude-sonnet-5" || body["max_tokens"] != float64(1024) {
		t.Errorf("path=%s body=%v", path, body)
	}
	if _, sent := body["temperature"]; sent {
		t.Error("temperature must not be sent: current Claude models reject it")
	}
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"].(map[string]any)["type"] != "object" {
		t.Errorf("output_config.format = %v", format)
	}
	system := body["system"].([]any)
	stateBlock := system[1].(map[string]any)
	if len(system) != 2 || system[0].(map[string]any)["text"] != "SYSTEM" || stateBlock["text"] != "STATE" {
		t.Fatalf("system = %v", system)
	}
	if cc, _ := stateBlock["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("state block must carry the cache breakpoint: %v", stateBlock)
	}
	if _, cached := system[0].(map[string]any)["cache_control"]; cached {
		t.Error("only the state block should carry a breakpoint")
	}
	if body["thinking"].(map[string]any)["type"] != "disabled" {
		t.Errorf("thinking = %v", body["thinking"])
	}
}

func TestAnthropicErrorsAreClassifiedForRetry(t *testing.T) {
	var calls int
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()
	llm := NewAnthropic("m", 64, "adaptive", "low", option.WithBaseURL(srv.URL), option.WithAPIKey("test"))

	_, err := llm.Complete(context.Background(), "s", "st", "u", SchemaFor(noul, ModeVote))
	var re *judge.RetryableError
	if !errors.As(err, &re) || re.After != 3*time.Second {
		t.Errorf("429 → %v (After=%v), want retryable after 3s", err, re)
	}
	if calls != 1 {
		t.Errorf("SDK retried on its own (%d calls); the fan-out owns retries", calls)
	}
	status = http.StatusBadRequest
	if _, err = llm.Complete(context.Background(), "s", "st", "u", SchemaFor(noul, ModeVote)); err == nil || errors.As(err, &re) {
		t.Errorf("400 must be final, got %v", err)
	}
}

func TestOpenRouterWireFormat(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		body = nil // a fresh map per request: Unmarshal merges into an existing one
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, `{"model":"vendor/model","choices":[{"message":{"role":"assistant","content":"{\"level\":1}"}}],"usage":{"prompt_tokens":50,"completion_tokens":4}}`)
	}))
	defer srv.Close()
	llm := &Compat{Client: &openai.Client{BaseURL: srv.URL + "/", APIKey: "k"}, Model: "vendor/model", MaxTokens: 64}
	got, err := llm.Complete(context.Background(), "SYSTEM", "STATE", "Q", SchemaFor(score, ModeVote))
	if err != nil || got.Text != `{"level":1}` || got.TokensIn != 50 || got.Model != "vendor/model" {
		t.Fatalf("completion = %+v err=%v", got, err)
	}
	rf := body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if auth != "Bearer k" || rf["type"] != "json_schema" || js["strict"] != true || js["schema"] == nil {
		t.Errorf("auth=%q response_format=%v", auth, rf)
	}
	if body["max_tokens"] != float64(64) || body["max_completion_tokens"] != nil {
		t.Errorf("compatible servers expect max_tokens: %v", body)
	}
	llm.CompletionTokens = true
	if _, err := llm.Complete(context.Background(), "SYSTEM", "STATE", "Q", SchemaFor(score, ModeVote)); err != nil {
		t.Fatal(err)
	}
	if body["max_completion_tokens"] != float64(64) || body["max_tokens"] != nil {
		t.Errorf("OpenAI's own API expects max_completion_tokens: %v", body)
	}
	if msgs := body["messages"].([]any); len(msgs) != 3 || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "STATE") {
		t.Errorf("messages = %v", body["messages"])
	}
}

// Ollama Cloud has no structured outputs: the schema goes in the prompt and the
// JSON object is cut out of whatever the model wraps it in.
func TestCompatSchemaInPrompt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, `{"model":"gpt-oss:120b","choices":[{"message":{"role":"assistant","content":"Sure:\n`+"```json"+`\n{\"level\":2}\n`+"```"+`"}}],"usage":{"prompt_tokens":80,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":64}}}`)
	}))
	defer srv.Close()
	llm := &Compat{Client: &openai.Client{BaseURL: srv.URL}, Model: "gpt-oss:120b", MaxTokens: 64, Effort: "low", SchemaInPrompt: true}
	got, err := llm.Complete(context.Background(), "SYSTEM", "STATE", "Q", SchemaFor(score, ModeVote))
	if err != nil || got.Text != `{"level":2}` || got.TokensCached != 64 {
		t.Fatalf("completion = %+v err=%v", got, err)
	}
	if _, sent := body["response_format"]; sent {
		t.Error("response_format must not be sent when the schema is in the prompt")
	}
	user := body["messages"].([]any)[2].(map[string]any)["content"].(string)
	if !strings.HasPrefix(user, "Q\n") || !strings.Contains(user, `"level"`) || body["reasoning_effort"] != "low" {
		t.Errorf("user turn = %q reasoning_effort = %v", user, body["reasoning_effort"])
	}
}

func TestThinkingIsOnlyDisabledWhereTheModelAllowsIt(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		want          bool
	}{
		{"claude-sonnet-5", "max", true},
		{"claude-opus-5", "high", true},
		{"claude-opus-5", "xhigh", false},
		{"claude-fable-5-1", "", false},
	} {
		if got := canDisableThinking(c.model, c.effort); got != c.want {
			t.Errorf("%s @ %q = %v, want %v", c.model, c.effort, got, c.want)
		}
	}
}
