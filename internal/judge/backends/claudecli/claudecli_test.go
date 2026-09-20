package claudecli

import (
	"context"
	"strings"
	"testing"
)

func TestCompleteReadsTheEnvelope(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	cli := &CLI{Model: "sonnet", Effort: "low", Run: func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		gotArgs, gotStdin = args, stdin
		return []byte(`{"is_error":false,"result":"{\"yes\":false}","structured_output":{"yes":false},"total_cost_usd":0.0712,
			"usage":{"input_tokens":2,"cache_creation_input_tokens":11195,"cache_read_input_tokens":3,"output_tokens":52},
			"modelUsage":{"claude-sonnet-5-20260601":{"canonicalModel":"claude-sonnet-5"}}}`), nil
	}}
	got, err := cli.Complete(context.Background(), "SYS", "STATE", "QUESTION", map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != `{"yes":false}` || got.Model != "claude-sonnet-5" || got.TokensIn != 11200 || got.TokensOut != 52 || got.TokensCached != 3 || got.CostUSD != 0.0712 {
		t.Errorf("completion = %+v", got)
	}
	args := strings.Join(gotArgs, "\x00")
	for _, want := range []string{"-p", "--output-format\x00json", "--model\x00sonnet", "--system-prompt\x00SYS", `--json-schema` + "\x00" + `{"type":"object"}`, "--tools\x00\x00--no-session-persistence", "--effort\x00low"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %q", want, gotArgs)
		}
	}
	if strings.Contains(args, "--bare") {
		t.Error("--bare drops the subscription login and must not be used")
	}
	if gotStdin != "STATE\n\nQUESTION" {
		t.Errorf("stdin = %q", gotStdin)
	}
}

func TestCompleteSurfacesCLIErrors(t *testing.T) {
	for name, out := range map[string]string{
		"not logged in":        `{"is_error":true,"result":"Not logged in · Please run /login","structured_output":null}`,
		"no structured output": `{"is_error":false,"result":"sure!","structured_output":null}`,
		"not json":             `Error: something`,
	} {
		cli := &CLI{Model: "sonnet", Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }}
		if _, err := cli.Complete(context.Background(), "s", "st", "u", nil); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if name == "not logged in" && !strings.Contains(err.Error(), "Not logged in") {
			t.Errorf("%s: error must carry the CLI's message, got %v", name, err)
		}
	}
}

// Thinking multiplies the wait for a one-word judgment, so it is off unless the
// user chose an effort; the two must never be sent together.
func TestThinkingIsOffUnlessAnEffortIsChosen(t *testing.T) {
	for effort, want := range map[string]string{"": "--settings\x00" + noThinking, "high": "--effort\x00high"} {
		var args string
		cli := &CLI{Model: "haiku", Effort: effort, Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
			args = strings.Join(a, "\x00")
			return []byte(`{"structured_output":{"yes":true}}`), nil
		}}
		if _, err := cli.Complete(context.Background(), "SYS", "STATE", "Q", nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(args, want) {
			t.Errorf("effort %q: args missing %q: %q", effort, want, args)
		}
		if strings.Contains(args, "--effort") == strings.Contains(args, "--settings") {
			t.Errorf("effort %q: want exactly one of --effort / --settings: %q", effort, args)
		}
	}
}
