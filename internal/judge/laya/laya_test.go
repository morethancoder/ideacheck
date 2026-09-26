package laya

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/judge"
)

// The worker is a Python process; the tests run this binary in its place
// (TestMain) and speak the same JSON lines, so nothing here needs Python.
func TestMain(m *testing.M) {
	if os.Getenv("LAYA_FAKE_WORKER") == "1" {
		os.Exit(fakeWorker(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "LAYA_FAKE_WORKER=1")
	return cmd
}

// fakeWorker answers like worker.py. The model name picks the behaviour: one
// containing "nomodel" fails to load; one whose path does not exist yet is
// created and the process dies on its first request (once).
func fakeWorker(args []string) int {
	out := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	if len(args) != 2 {
		return 2
	}
	mode, model := args[0], args[1]
	if strings.Contains(model, "nomodel") {
		out(map[string]any{"error": "FileNotFoundError: no such checkpoint"})
		return 1
	}
	if mode == "download" {
		out(map[string]any{"status": "downloading " + model, "done": 0, "total": 100})
		out(map[string]any{"status": "downloading " + model, "done": 60, "total": 100})
		out(map[string]any{"status": "done", "done": 100, "total": 100})
		return 0
	}
	out(map[string]any{"ready": true, "model": model, "max_len": 512})
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if strings.HasSuffix(model, "/crash") {
			if _, err := os.Stat(model); err != nil {
				_ = os.WriteFile(model, nil, 0o644)
				fmt.Fprintln(os.Stderr, "boom")
				return 3
			}
		}
		var req struct {
			ID        int64
			State     map[string]any
			Questions map[string]struct {
				Type     string
				Criteria any
			}
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			out(map[string]any{"error": err.Error()})
			continue
		}
		answers, atLimit := map[string]any{}, []string{}
		for id, q := range req.Questions {
			a := map[string]any{"type": q.Type, "confidence": 0.5}
			switch q.Type {
			case "choice":
				var keys []string
				for k := range q.Criteria.(map[string]any) {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				a["choice"], a["probabilities"] = keys[0], map[string]float64{keys[0]: 0.8}
			case "score":
				a["score"], a["probabilities"] = 1.0, map[string]float64{"1": 1}
			default:
				a["noul"] = 0.7
			}
			answers[id] = a
			if _, long := req.State["long"]; long {
				atLimit = append(atLimit, id)
			}
		}
		out(map[string]any{"id": req.ID, "model": model, "answers": answers, "usage": map[string]int{"input_tokens": 42}, "at_limit": atLimit})
	}
	return 0
}

var qs = []judge.Question{
	{ID: "kind", Kind: judge.Choice, Instructions: "Kind?", Options: map[string]string{"business": "money", "other": "none"}, Weight: 3},
	{ID: "acuity", Kind: judge.Score, Instructions: "How acute?", Levels: []string{"none", "mild", "painful"}},
	{ID: "stated", Kind: judge.Noul, Instructions: "The customer is named.", Criteria: &judge.NoulCriteria{Yes: "named", No: "implied"}},
}

func TestAnswersComeBackTyped(t *testing.T) {
	j := &Judge{Model: "fake/ok", Command: fakeCommand}
	got, err := j.EvaluateBatch(context.Background(), judge.State{"idea": "x"}, qs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("answers = %+v", got)
	}
	c, s, n := got[0], got[1], got[2]
	if c.Choice != "business" || c.Method != "laya" || c.Model != "fake/ok" || c.TokensIn != 42 || c.Probabilities["business"] != 0.8 {
		t.Errorf("choice = %+v", c)
	}
	if s.Score != 1 || s.TokensIn != 0 {
		t.Errorf("score = %+v (usage is booked once)", s)
	}
	if n.Noul != 0.7 || n.Confidence != judge.NoulConfidence(0.7) {
		t.Errorf("noul = %+v", n)
	}
	one, err := j.Evaluate(context.Background(), judge.State{"long": true}, qs[2])
	if err != nil || one.Noul != 0.7 {
		t.Errorf("Evaluate = %+v, %v", one, err)
	}
}

// One process per model, however many engines are built on it: the weights
// are a gigabyte, and the TUI builds an engine for every check.
func TestJudgesOnTheSameModelShareOneWorker(t *testing.T) {
	starts := 0
	counting := func(args ...string) *exec.Cmd { starts++; return fakeCommand(args...) }
	a, b := &Judge{Model: "fake/shared", Command: counting}, &Judge{Model: "fake/shared", Command: counting}
	for _, j := range []*Judge{a, b, a} {
		if _, err := j.EvaluateBatch(context.Background(), judge.State{}, qs); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Errorf("worker started %d times, want once", starts)
	}
}

// A worker that dies mid-check is a retryable failure, and the retry gets a
// fresh one: the fan-out's retry policy is what restarts it.
func TestADeadWorkerIsReplacedOnRetry(t *testing.T) {
	j := &Judge{Model: filepath.Join(t.TempDir(), "crash"), Command: fakeCommand}
	_, err := j.EvaluateBatch(context.Background(), judge.State{}, qs)
	var re *judge.RetryableError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want a retryable error carrying the worker's last words", err)
	}
	if _, err := j.EvaluateBatch(context.Background(), judge.State{}, qs); err != nil {
		t.Errorf("second attempt: %v", err)
	}
}

func TestAModelThatCannotLoadIsAFinalError(t *testing.T) {
	j := &Judge{Model: "fake/nomodel", Command: fakeCommand}
	_, err := j.EvaluateBatch(context.Background(), judge.State{}, qs)
	var re *judge.RetryableError
	if err == nil || errors.As(err, &re) || !strings.Contains(err.Error(), "no such checkpoint") {
		t.Errorf("err = %v, want the loader's own words, not a retry", err)
	}
}

func TestACancelledRequestReturnsAtOnce(t *testing.T) {
	j := &Judge{Model: "fake/cancel", Command: fakeCommand}
	if _, err := j.EvaluateBatch(context.Background(), judge.State{}, qs); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := j.EvaluateBatch(ctx, judge.State{}, qs); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

// Downloaded looks where huggingface_hub puts snapshots, so setup can say
// whether the first check will download 850 MB without starting Python.
func TestDownloadedReadsTheHubCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)
	if Downloaded("aac6fef/laya-mlx") {
		t.Error("an empty cache has no model")
	}
	snap := filepath.Join(cache, "models--aac6fef--laya-mlx", "snapshots", "abc123")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(snap, "model.safetensors"), []byte("w"), 0o644)
	if !Downloaded("aac6fef/laya-mlx") {
		t.Error("a snapshot with weights is downloaded")
	}
	if !Downloaded(cache) || Downloaded(filepath.Join(cache, "nowhere")) {
		t.Error("a local checkpoint is a directory that exists")
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	var seen []string
	j := &Judge{Model: "fake/dl", Command: fakeCommand}
	err := j.Download(context.Background(), func(status string, done, total int64) {
		seen = append(seen, fmt.Sprintf("%s %d/%d", status, done, total))
	})
	if err != nil || len(seen) != 3 || seen[1] != "downloading fake/dl 60/100" || seen[2] != "done 100/100" {
		t.Errorf("err=%v seen=%v", err, seen)
	}
	err = (&Judge{Model: "fake/nomodel", Command: fakeCommand}).Download(context.Background(), func(string, int64, int64) {})
	if err == nil || !strings.Contains(err.Error(), "no such checkpoint") {
		t.Errorf("a failed download carries the worker's reason: %v", err)
	}
}

func TestInterpreterHonoursTheConfiguredPython(t *testing.T) {
	got, err := Interpreter("~/venv/bin/python")
	home, _ := os.UserHomeDir()
	if err != nil || len(got) != 1 || got[0] != filepath.Join(home, "venv/bin/python") {
		t.Errorf("got %v, %v", got, err)
	}
}
