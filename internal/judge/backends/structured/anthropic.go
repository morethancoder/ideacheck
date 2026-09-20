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
