package structured

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/morethancoder/ideacheck/internal/judge"
)

// Anthropic calls the Messages API through the official SDK.
type Anthropic struct {
	client    anthropic.Client
	Model     string
	MaxTokens int
	Thinking  string // "disabled" | "adaptive"
	Effort    string // output_config.effort; "" = the model's default (Haiku 4.5 takes none)
}

// NewAnthropic builds the provider. With no explicit option the SDK resolves
// credentials itself (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, an `ant auth`
// profile); a key saved by `ideacheck setup` is passed in via option.WithAPIKey.
// SDK retries are off: the fan-out owns retrying, backoff and Retry-After.
func NewAnthropic(model string, maxTokens int, thinking, effort string, opts ...option.RequestOption) *Anthropic {
	opts = append([]option.RequestOption{option.WithMaxRetries(0)}, opts...)
	return &Anthropic{client: anthropic.NewClient(opts...), Model: model, MaxTokens: maxTokens, Thinking: thinking, Effort: effort}
}

func (a *Anthropic) Complete(ctx context.Context, system, state, user string, schema map[string]any) (Completion, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.Model),
		MaxTokens: int64(a.MaxTokens),
		// tools → system → messages is the cache prefix order: the state block is
		// shared by every question over the same state, so it carries the breakpoint.
		System: []anthropic.TextBlockParam{
			{Text: system},
			{Text: state, CacheControl: anthropic.NewCacheControlEphemeralParam()},
		},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: schema}, Effort: anthropic.OutputConfigEffort(a.Effort)},
		// No Temperature: current Claude models reject sampling parameters (HTTP 400).
	}
	if a.Thinking == "disabled" && canDisableThinking(a.Model, a.Effort) {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	}
	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Completion{}, classify(err)
	}
	if msg.StopReason == anthropic.StopReasonRefusal || msg.StopReason == anthropic.StopReasonMaxTokens {
		return Completion{}, fmt.Errorf("model stopped with %q before completing the JSON answer", msg.StopReason)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	u := msg.Usage
	return Completion{
		Text:         text.String(),
		Model:        string(msg.Model),
		TokensIn:     int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens),
		TokensOut:    int(u.OutputTokens),
		TokensCached: int(u.CacheReadInputTokens),
	}, nil
}

// maxPauses bounds how often a paused search turn is resumed.
const maxPauses = 5

// Search runs one request with the web search server tool on. The reply is not
// constrained with output_config: search replies carry citations, which the API
// rejects together with a JSON format, so the schema is stated in the prompt
// and the caller takes the first object out of the text. Thinking is left at
// the model's default: deciding what to look up is not a narrow judgment.
func (a *Anthropic) Search(ctx context.Context, system, user string, schema map[string]any, limits SearchLimits) (Completion, error) {
	stated, err := withSchema(user, schema)
	if err != nil {
		return Completion{}, err
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.Model),
		MaxTokens: int64(max(a.MaxTokens, limits.MaxTokens)),
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(stated))},
		Tools:     []anthropic.ToolUnionParam{searchTool(a.Model, limits.MaxSearches)},
	}
	var total Completion
	for range maxPauses {
		msg, err := a.client.Messages.New(ctx, params)
		if err != nil {
			return Completion{}, classify(err)
		}
		u := msg.Usage
		total.Model = string(msg.Model)
		total.TokensIn += int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens)
		total.TokensOut += int(u.OutputTokens)
		total.TokensCached += int(u.CacheReadInputTokens)
		// pause_turn: the server stopped a long search turn; sending the reply
		// back as it is lets the model carry on from there.
		if msg.StopReason == anthropic.StopReasonPauseTurn {
			params.Messages = append(params.Messages, msg.ToParam())
			continue
		}
		if msg.StopReason == anthropic.StopReasonRefusal || msg.StopReason == anthropic.StopReasonMaxTokens {
			return Completion{}, fmt.Errorf("model stopped with %q before reporting its findings", msg.StopReason)
		}
		var text strings.Builder
		for _, block := range msg.Content {
			if t, ok := block.AsAny().(anthropic.TextBlock); ok {
				text.WriteString(t.Text)
			}
		}
		total.Text = text.String()
		return total, nil
	}
	return Completion{}, fmt.Errorf("the search turn was still paused after %d resumes", maxPauses)
}

// searchTool picks the web search tool the model supports: Haiku 4.5 and older
// take the basic one; newer models take the one that filters results itself.
func searchTool(model string, maxUses int) anthropic.ToolUnionParam {
	uses := anthropic.Int(int64(maxUses))
	if strings.HasPrefix(model, "claude-haiku") {
		t := &anthropic.WebSearchTool20250305Param{}
		if maxUses > 0 {
			t.MaxUses = uses
		}
		return anthropic.ToolUnionParam{OfWebSearchTool20250305: t}
	}
	t := &anthropic.WebSearchTool20260209Param{}
	if maxUses > 0 {
		t.MaxUses = uses
	}
	return anthropic.ToolUnionParam{OfWebSearchTool20260209: t}
}

// canDisableThinking: Fable/Mythos reject disabled thinking outright, and Opus 5
// rejects it above high effort (both 400). Those run with their default instead.
func canDisableThinking(model, effort string) bool {
	switch {
	case strings.HasPrefix(model, "claude-fable"), strings.HasPrefix(model, "claude-mythos"):
		return false
	case strings.HasPrefix(model, "claude-opus-5"):
		return effort != "xhigh" && effort != "max"
	}
	return true
}

// classify maps SDK errors onto the fan-out's retry rules.
func classify(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		var header = map[string][]string{}
		if apierr.Response != nil {
			header = apierr.Response.Header
		}
		return judge.HTTPError(apierr.StatusCode, header, apierr.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &judge.RetryableError{Err: err} // connection-level failure
}
