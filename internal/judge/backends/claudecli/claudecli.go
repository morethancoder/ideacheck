// Package claudecli is a structured.Completer that shells out to `claude -p`.
// It is meant for human CLI mode only: each call starts a process and carries
// the CLI's own prompt overhead, so the backend defaults to batch mode.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// lean keeps the caller's own Claude Code setup out of the call: their MCP
// servers, skills, settings files and project instructions all ride along on
// every `claude -p` otherwise, and a judgment needs none of them. Measured on
// claude 2.1.278 with one yes/no question: 12.2k input tokens without these
// flags, 0.9k with them — on every call of every check. The login survives
// (unlike with --bare). execClaude also runs outside any project directory, so
// no CLAUDE.md is picked up.
var lean = []string{"--strict-mcp-config", "--disable-slash-commands", "--setting-sources", ""}

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
	// --tools "" disables every tool: this is a decision function, not an agent.
	return c.run(ctx, system, state+"\n\n"+user, schema, "--tools", "")
}

// webTools is the only tool a research call may use. Verified against claude
// 2.1.278: with both flags set, `claude -p` searches without asking and still
// returns structured_output matching --json-schema. WebFetch is deliberately
// not allowed: every fetched page rides along on every later turn, and a check
// measured with it spent ~146k input tokens on research alone.
const webTools = "WebSearch"

// Search is Complete with the web tools on and nothing else: no shell, no files.
// The CLI has no cap on searches, so limits are left to the prompt.
func (c *CLI) Search(ctx context.Context, system, user string, schema map[string]any, _ structured.SearchLimits) (structured.Completion, error) {
	return c.run(ctx, system, user, schema, "--tools", webTools, "--allowedTools", webTools)
}

func (c *CLI) run(ctx context.Context, system, stdin string, schema map[string]any, tools ...string) (structured.Completion, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return structured.Completion{}, err
	}
	run := c.Run
	if run == nil {
		run = execClaude
	}
	// (--bare is NOT used: it drops the subscription login.)
	args := []string{"-p", "--output-format", "json", "--model", c.Model,
		"--system-prompt", system, "--json-schema", string(schemaJSON)}
	args = append(args, lean...)
	args = append(append(args, tools...), "--no-session-persistence")
	// A narrow judgment needs no thinking: it is off unless an effort was chosen.
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	} else {
		args = append(args, "--settings", noThinking)
	}
	out, err := run(ctx, stdin, args...)
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
	cmd.Dir = os.TempDir()
	cmd.Stdin = bytes.NewBufferString(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("run claude: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil // on a non-zero exit the JSON envelope still explains the error
}
