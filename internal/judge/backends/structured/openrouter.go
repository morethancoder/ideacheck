package structured

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge/backends/openai"
)

// Compat serves chat models over an OpenAI-compatible API (OpenAI, OpenRouter,
// Ollama)
// with a strict json_schema response format.
type Compat struct {
	Client    *openai.Client
	Model     string
	MaxTokens int
	// CompletionTokens sends the limit as max_completion_tokens (OpenAI's own
	// API) instead of max_tokens (OpenRouter and other compatible servers).
	CompletionTokens bool
	Effort           string // sent as reasoning_effort; "" = the model's default
	// SchemaInPrompt states the schema in the prompt instead of response_format,
	// for servers without structured outputs (Ollama Cloud). The reply is then
	// unconstrained, so the first JSON object in it is taken as the answer.
	SchemaInPrompt bool
	// WebPlugin marks a server with OpenRouter's `web` plugin: research can run.
	WebPlugin bool
}

func (o *Compat) Complete(ctx context.Context, system, state, user string, schema map[string]any) (Completion, error) {
	return o.chat(ctx, system, state, user, schema, SearchLimits{}, false)
}

// CanSearch is true for OpenRouter, whose `web` plugin gives any model it serves
// live search results. OpenAI's own chat API and Ollama have no equivalent.
func (o *Compat) CanSearch() bool { return o.WebPlugin }

// Search is the same chat call with OpenRouter's web plugin on.
func (o *Compat) Search(ctx context.Context, system, user string, schema map[string]any, limits SearchLimits) (Completion, error) {
	return o.chat(ctx, system, "", user, schema, limits, true)
}

func (o *Compat) chat(ctx context.Context, system, state, user string, schema map[string]any, limits SearchLimits, web bool) (Completion, error) {
	messages := []openai.Message{{Role: "system", Content: system}}
	if state != "" {
		messages = append(messages, openai.Message{Role: "system", Content: state})
	}
	req := openai.Request{
		Model:    o.Model,
		Messages: append(messages, openai.Message{Role: "user", Content: user}),
		ResponseFormat: &openai.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: &openai.JSONSchema{Name: "answer", Strict: true, Schema: schema},
		},
		ReasoningEffort: o.Effort,
	}
	if o.SchemaInPrompt {
		stated, err := withSchema(user, schema)
		if err != nil {
			return Completion{}, err
		}
		req.ResponseFormat, req.Messages[len(req.Messages)-1].Content = nil, stated
	}
	maxTokens := o.MaxTokens
	if web {
		req.Plugins = []openai.Plugin{{ID: "web", MaxResults: limits.MaxSearches}}
		maxTokens = max(maxTokens, limits.MaxTokens)
	}
	if o.CompletionTokens {
		req.MaxCompletionTokens = maxTokens
	} else {
		req.MaxTokens = maxTokens
	}
	res, err := o.Client.Chat(ctx, req)
	if err != nil {
		return Completion{}, err
	}
	text := res.Choices[0].Message.Content
	if o.SchemaInPrompt {
		text = firstObject(text)
	}
	return Completion{
		Text:         text,
		Model:        res.Model,
		TokensIn:     res.Usage.PromptTokens,
		TokensOut:    res.Usage.CompletionTokens,
		TokensCached: res.Usage.PromptTokensDetails.CachedTokens,
	}, nil
}

// withSchema states the schema in the prompt, for a call whose reply cannot be
// constrained by the API itself.
func withSchema(user string, schema map[string]any) (string, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return user + "\n\nThe reply must be one JSON object matching this JSON Schema, with nothing before or after it:\n" + string(b), nil
}

// firstObject cuts the outermost {...} out of a reply that may wrap it in prose
// or a code fence. A reply without one is returned unchanged for decode to reject.
func firstObject(s string) string {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}
