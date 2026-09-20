// Package claudecli is a structured.Completer that shells out to `claude -p`.
// It is meant for human CLI mode only: each call starts a process and carries
// the CLI's own prompt overhead, so the backend defaults to batch mode.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/morethancoder/ideacheck/internal/judge/backends/structured"
)

const Name = "claude-cli"

// Runner executes the CLI; tests replace it.
type Runner func(ctx context.Context, stdin string, args ...string) ([]byte, error)

type CLI struct {
	Model  string
	Effort string // passed as --effort; "" = thinking off
	Run    Runner // nil = exec `claude`
}

// noThinking is passed as --settings. The CLI has no thinking flag; verified
// against claude 2.1.278 that MAX_THINKING_TOKENS=0 stops it (haiku answered a
// yes/no question in 4 output tokens instead of 128).
const noThinking = `{"env":{"MAX_THINKING_TOKENS":"0"}}`

// envelope is the subset of `claude -p --output-format json` we read (verified
// against the installed CLI: structured_output holds the schema-conforming object).
type envelope struct {
	IsError          bool            `json:"is_error"`
	TotalCostUSD     float64         `json:"total_cost_usd"` // API list price of the call, even on a subscription
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		CanonicalModel string `json:"canonicalModel"`
	} `json:"modelUsage"`
}

func (c *CLI) Complete(ctx context.Context, system, state, user string, schema map[string]any) (structured.Completion, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return structured.Completion{}, err
	}
	run := c.Run
	if run == nil {
		run = execClaude
	}
	// --tools "" disables every tool: this is a decision function, not an agent.
	// (--bare is NOT used: it drops the subscription login.)
	args := []string{"-p", "--output-format", "json", "--model", c.Model,
		"--system-prompt", system, "--json-schema", string(schemaJSON),
		"--tools", "", "--no-session-persistence"}
	// A narrow judgment needs no thinking: it is off unless an effort was chosen.
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	} else {
		args = append(args, "--settings", noThinking)
	}
	out, err := run(ctx, state+"\n\n"+user, args...)
	if err != nil {
		return structured.Completion{}, err
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return structured.Completion{}, fmt.Errorf("claude -p did not return JSON: %w", err)
	}
	if env.IsError {
		return structured.Completion{}, fmt.Errorf("claude -p: %s", env.Result)
	}
	if len(env.StructuredOutput) == 0 || string(env.StructuredOutput) == "null" {
		return structured.Completion{}, fmt.Errorf("claude -p returned no structured_output")
	}
	u := env.Usage
	return structured.Completion{
		Text:         string(env.StructuredOutput),
		Model:        c.model(env),
		TokensIn:     u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		TokensOut:    u.OutputTokens,
		TokensCached: u.CacheReadInputTokens,
		CostUSD:      env.TotalCostUSD,
	}, nil
}

// model reports the model the CLI used ("sonnet" is only an alias), preferring
// its canonical id ("claude-haiku-4-5" over "claude-haiku-4-5-20251001").
func (c *CLI) model(env envelope) string {
	if len(env.ModelUsage) == 1 {
		for id, u := range env.ModelUsage {
			if u.CanonicalModel != "" {
				return u.CanonicalModel
			}
			return id
		}
	}
	return c.Model
}

func execClaude(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = bytes.NewBufferString(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("run claude: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil // on a non-zero exit the JSON envelope still explains the error
}
