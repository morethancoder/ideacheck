package codexcli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/judge/structured"
)

// fake plays codex: it checks the schema file exists and writes the -o file.
func fake(t *testing.T, answer string, seen *[]string, stdin *string) Runner {
	return func(_ context.Context, in string, args ...string) ([]byte, error) {
		*seen, *stdin = args, in
		for i, a := range args {
			if a == "--output-schema" {
				if b, err := os.ReadFile(args[i+1]); err != nil || !strings.Contains(string(b), `"object"`) {
					t.Errorf("schema file: %q %v", b, err)
				}
			}
			if a == "-o" && answer != "" {
				_ = os.WriteFile(args[i+1], []byte(answer+"\n"), 0o600)
			}
		}
		return []byte(`{"type":"thread.started"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":12134,"cached_input_tokens":1408,"output_tokens":40}}` + "\n"), nil
	}
}

func TestCompleteReadsTheAnswerFile(t *testing.T) {
	var args []string
	var stdin string
	cli := &CLI{Model: "gpt-x", Effort: "low", Run: fake(t, `{"yes":false}`, &args, &stdin)}
	got, err := cli.Complete(context.Background(), "SYS", "STATE", "Q", map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != `{"yes":false}` || got.Model != "gpt-x" || got.TokensIn != 12134 || got.TokensCached != 1408 || got.TokensOut != 40 {
		t.Errorf("completion = %+v", got)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "-s read-only", "-m gpt-x", "-c model_reasoning_effort=low"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != "-" || stdin != "SYS\n\nSTATE\n\nQ" {
		t.Errorf("prompt must go via stdin: last arg %q stdin %q", args[len(args)-1], stdin)
	}
}

func TestDefaultModelAndMissingAnswer(t *testing.T) {
	var args []string
	var stdin string
	home := t.TempDir()
	cli := &CLI{Home: home, Run: fake(t, `{"yes":true}`, &args, &stdin)}
	got, _ := cli.Complete(context.Background(), "s", "st", "q", map[string]any{"type": "object"})
	if got.Model != "codex-default" || strings.Contains(strings.Join(args, " "), "-m ") {
		t.Errorf("no model configured: model=%q args=%v", got.Model, args)
	}
	_ = os.WriteFile(home+"/config.toml", []byte("approval = \"never\"\nmodel = \"gpt-5.5\"\n"), 0o600)
	if got, _ = cli.Complete(context.Background(), "s", "st", "q", map[string]any{"type": "object"}); got.Model != "gpt-5.5" {
		t.Errorf("default model should be read from codex's config.toml, got %q", got.Model)
	}
	cli = &CLI{Run: fake(t, "", &args, &stdin)}
	if _, err := cli.Complete(context.Background(), "s", "st", "q", map[string]any{"type": "object"}); err == nil {
		t.Error("an empty answer file must be an error")
	}
}

func TestFailureCarriesTheAPIMessage(t *testing.T) {
	events := `{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"The 'x' model is not supported\"}}"}}`
	cli := &CLI{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(events), errors.New("exit status 1")
	}}
	_, err := cli.Complete(context.Background(), "s", "st", "q", map[string]any{"type": "object"})
	if err == nil || !strings.Contains(err.Error(), "The 'x' model is not supported") {
		t.Errorf("err = %v", err)
	}
}

// With no effort codex would inherit the user's config.toml (often "high"),
// so reasoning is switched off explicitly.
func TestNoEffortMeansNoReasoning(t *testing.T) {
	var args []string
	var stdin string
	cli := &CLI{Model: "gpt-x", Run: fake(t, `{"yes":true}`, &args, &stdin)}
	if _, err := cli.Complete(context.Background(), "SYS", "STATE", "Q", map[string]any{"type": "object"}); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(args, " "); !strings.Contains(joined, "-c model_reasoning_effort=none") {
		t.Errorf("args = %v", args)
	}
}

// --search belongs to `codex`, not to `exec`: it has to come first.
func TestSearchTurnsOnLiveWebSearch(t *testing.T) {
	var args []string
	var stdin string
	cli := &CLI{Model: "gpt-x", Run: fake(t, `{"findings":[]}`, &args, &stdin)}
	got, err := cli.Search(context.Background(), "SYS", "BRIEF", map[string]any{"type": "object"}, structured.SearchLimits{})
	if err != nil || got.Text != `{"findings":[]}` {
		t.Fatalf("search = %+v, %v", got, err)
	}
	if args[0] != "--search" || args[1] != "exec" || !strings.Contains(strings.Join(args, " "), "-s read-only") || stdin != "SYS\n\nBRIEF" {
		t.Errorf("args=%v stdin=%q", args, stdin)
	}
}
