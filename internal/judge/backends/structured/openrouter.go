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
}

func (o *Compat) Complete(ctx context.Context, system, state, user string, schema map[string]any) (Completion, error) {
	req := openai.Request{
		Model: o.Model,
		Messages: []openai.Message{
			{Role: "system", Content: system},
			{Role: "system", Content: state},
			{Role: "user", Content: user},
		},
		ResponseFormat: &openai.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: &openai.JSONSchema{Name: "answer", Strict: true, Schema: schema},
		},
		ReasoningEffort: o.Effort,
	}
	if o.SchemaInPrompt {
		b, err := json.Marshal(schema)
		if err != nil {
			return Completion{}, err
		}
		req.ResponseFormat = nil
		req.Messages[2].Content = user + "\n\nThe reply must be one JSON object matching this JSON Schema, with nothing before or after it:\n" + string(b)
	}
	if o.CompletionTokens {
		req.MaxCompletionTokens = o.MaxTokens
	} else {
		req.MaxTokens = o.MaxTokens
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

// firstObject cuts the outermost {...} out of a reply that may wrap it in prose
// or a code fence. A reply without one is returned unchanged for decode to reject.
func firstObject(s string) string {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}
