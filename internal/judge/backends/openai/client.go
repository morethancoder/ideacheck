// Package openai is a minimal client for OpenAI-compatible /chat/completions
// endpoints (Ollama, llama.cpp, vLLM, OpenRouter). It is NOT used for Claude on
// the Anthropic API, which goes through the official SDK.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge"
)

type Client struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string // optional; sent as a Bearer token, never logged
	HTTP    *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	// MaxCompletionTokens replaces max_tokens on OpenAI's own API, whose newer
	// models reject max_tokens. Other compatible servers still expect max_tokens.
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	Logprobs            bool            `json:"logprobs,omitempty"`
	TopLogprobs         int             `json:"top_logprobs,omitempty"`
	ResponseFormat      *ResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"` // reasoning models: low | medium | high ...
	Plugins             []Plugin        `json:"plugins,omitempty"`          // OpenRouter only
}

// Plugin is an OpenRouter request plugin; {id: "web"} adds live search results.
type Plugin struct {
	ID         string `json:"id"`
	MaxResults int    `json:"max_results,omitempty"`
}

type ResponseFormat struct {
	Type       string      `json:"type"` // "json_schema"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type JSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type Response struct {
	Model   string `json:"model"`
	Choices []struct {
		Message  Message `json:"message"`
		Logprobs *struct {
			Content []TokenLogprob `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type TokenLogprob struct {
	Token       string  `json:"token"`
	Logprob     float64 `json:"logprob"`
	TopLogprobs []struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
	} `json:"top_logprobs"`
}

// Chat posts one completion request. Failures are classified by judge.HTTPError
// so the fan-out knows what to retry.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(hreq)
	if err != nil {
		return nil, &judge.RetryableError{Err: fmt.Errorf("request to %s: %w", url, err)}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return nil, judge.HTTPError(res.StatusCode, res.Header, string(raw))
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices")
	}
	return &out, nil
}
