// Package codexcli is a structured.Completer that shells out to `codex exec`.
// Like claude-cli it is for human CLI mode only and batches by default.
// Flags verified against codex-cli 0.153: --output-schema FILE constrains the
// final message, -o FILE receives it, the prompt is read from stdin ("-"), and
// --json streams JSONL events on stdout whose turn.completed carries token usage.
package codexcli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge/backends/structured"
)

const Name = "codex-cli"

// noReasoning is not in `codex debug models`, but verified on codex-cli 0.153.4
// with gpt-5.5, gpt-5.6-luna and gpt-5.6-terra: 0 reasoning tokens. ("minimal"
// is rejected: codex always sends web_search, which that level forbids.)
const noReasoning = "none"

// Runner executes the CLI and returns stdout (the --json event stream); tests
// replace it.
type Runner func(ctx context.Context, stdin string, args ...string) (events []byte, err error)

type CLI struct {
	Model  string // "" = Codex's default
	Effort string // model_reasoning_effort; "" = none (no reasoning)
	Run    Runner // nil = exec `codex`
	Home   string // CODEX_HOME, for naming the default model; "" = ~/.codex
}

func (c *CLI) Complete(ctx context.Context, system, state, user string, schema map[string]any) (structured.Completion, error) {
	// codex has no system-prompt flag: the system text leads the prompt.
	return c.run(ctx, system+"\n\n"+state+"\n\n"+user, schema)
}

// Search is Complete with live web search on. --search is a flag of `codex`
// itself, not of `exec`, so it goes first (verified on codex-cli 0.153.4: the
// event stream shows web_search items and -o still receives schema-valid JSON).
// The sandbox stays read-only, and codex has no cap on searches to pass on.
func (c *CLI) Search(ctx context.Context, system, user string, schema map[string]any, _ structured.SearchLimits) (structured.Completion, error) {
	return c.run(ctx, system+"\n\n"+user, schema, "--search")
}

func (c *CLI) run(ctx context.Context, stdin string, schema map[string]any, global ...string) (structured.Completion, error) {
	dir, err := os.MkdirTemp("", "ideacheck-codex-")
	if err != nil {
		return structured.Completion{}, err
	}
	defer os.RemoveAll(dir)
	schemaPath, outPath := filepath.Join(dir, "schema.json"), filepath.Join(dir, "answer.json")
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return structured.Completion{}, err
	}
	if err := os.WriteFile(schemaPath, schemaJSON, 0o600); err != nil {
		return structured.Completion{}, err
	}
	// read-only sandbox + ephemeral: this is a decision function, not an agent.
	args := append(global, "exec", "--json", "--output-schema", schemaPath, "-o", outPath, "--skip-git-repo-check", "--ephemeral", "-s", "read-only")
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	// Always set: left out, codex falls back to the user's own config.toml, which
	// is tuned for coding ("high"), not for one narrow judgment.
	args = append(args, "-c", "model_reasoning_effort="+cmp.Or(c.Effort, noReasoning))
	run := c.Run
	if run == nil {
		run = execCodex
	}
	events, runErr := run(ctx, stdin, append(args, "-")...)
	usage, failure := readEvents(events)
	answer, err := os.ReadFile(outPath)
	if err != nil || len(bytes.TrimSpace(answer)) == 0 {
		switch {
		case failure != "":
			return structured.Completion{}, fmt.Errorf("codex exec: %s", failure)
		case runErr != nil:
			return structured.Completion{}, runErr
		}
		return structured.Completion{}, fmt.Errorf("codex exec wrote no answer")
	}
	return structured.Completion{
		Text:         string(bytes.TrimSpace(answer)),
		Model:        c.model(),
		TokensIn:     usage.InputTokens,
		TokensOut:    usage.OutputTokens,
		TokensCached: usage.CachedInputTokens,
	}, nil
}

type usage struct {
	InputTokens       int `json:"input_tokens"`        // cached included
	CachedInputTokens int `json:"cached_input_tokens"` // part of input_tokens
	OutputTokens      int `json:"output_tokens"`
}

// readEvents sums token usage over turn.completed events and returns the last
// failure message, unwrapping the API error JSON codex nests inside it.
func readEvents(b []byte) (total usage, failure string) {
	for _, line := range bytes.Split(b, []byte("\n")) {
		var e struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Usage   usage  `json:"usage"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "turn.completed":
			total.InputTokens += e.Usage.InputTokens
			total.CachedInputTokens += e.Usage.CachedInputTokens
			total.OutputTokens += e.Usage.OutputTokens
		case "turn.failed":
			failure = apiMessage(e.Error.Message)
		case "error":
			failure = apiMessage(e.Message)
		}
	}
	return total, failure
}

func apiMessage(s string) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(s), &nested) == nil && nested.Error.Message != "" {
		return nested.Error.Message
	}
	return s
}

var modelRE = regexp.MustCompile(`(?m)^\s*model\s*=\s*"([^"]+)"`)

// model names the model that answered: the configured one, else the default
// in codex's config.toml, so the cost can be priced.
func (c *CLI) model() string {
	if c.Model != "" {
		return c.Model
	}
	home := c.Home
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(dir, ".codex")
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
		if m := modelRE.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return "codex-default"
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// execCodex returns the event stream even on a non-zero exit: its
// turn.failed event explains the failure better than the exit status.
func execCodex(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("run codex: %w: %s", err, lastLine(stderr.Bytes()))
	}
	return out, nil
}
