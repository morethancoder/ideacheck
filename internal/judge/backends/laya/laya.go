// Package laya runs Laya, an open-weight typed-decision model, on this machine
// through laya-mlx (github.com/mizorewww/laya-mlx, an MLX port of
// github.com/NandhaKishorM/laya; Apple silicon only). Like Jev it answers a
// typed question as probabilities in one forward pass — no sampling, no text —
// and like Jev it can only judge. It keeps upstream's question and answer
// schema, which mirrors System One's, so the wire types are jev's.
//
// laya-mlx is a Python library with no server, and loading the 421M-parameter
// checkpoint takes seconds, so the binary runs one worker process per model
// (worker.py, embedded) that loads it once and answers one JSON line per
// request. The worker exits on its own after ten idle minutes; the next
// request starts another. Verified against laya-mlx 0.1.0 and 0.2.0.
package laya

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/jev"
	"github.com/morethancoder/ideacheck/internal/logging"
)

const (
	Name   = "laya"
	method = "laya"
	// pypi is what `uv run --with` installs when no interpreter has laya-mlx:
	// the versions whose predict() this worker was verified against.
	pypi = "laya-mlx>=0.1,<1"
)

//go:embed worker.py
var worker string

// Judge is one configured model. Every Judge for the same model and
// interpreter shares a worker process (see pool): the TUI builds an engine per
// check, and a gigabyte of weights must not be loaded again for each.
type Judge struct {
	Model         string // a Hugging Face repo id (aac6fef/laya-mlx) or a local checkpoint dir
	Python        string // interpreter with laya-mlx installed; "" = python3 if it has it, else uv
	MaxConcurrent int
	// Command builds the worker process from its arguments; nil = Interpreter.
	// Tests replace it.
	Command func(args ...string) *exec.Cmd
}

func (j *Judge) Name() string { return Name }
func (j *Judge) Capabilities() judge.Capabilities {
	return judge.Capabilities{NativeBatch: true, MaxConcurrent: j.MaxConcurrent}
}

// Supported says whether laya-mlx can run here at all: it needs MLX, which is
// Apple silicon only.
func Supported() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("laya runs through laya-mlx, which needs Apple silicon (MLX); pick another backend on this machine")
	}
	return nil
}

var probe struct {
	once sync.Once
	ok   bool
}

// Interpreter is the command that runs the worker: the configured interpreter;
// else python3 when it can import laya_mlx; else uv, which installs laya-mlx
// into a cached environment of its own the first time. Its error says what to
// install.
func Interpreter(python string) ([]string, error) {
	if python != "" {
		return []string{expandHome(python)}, nil
	}
	probe.once.Do(func() {
		probe.ok = exec.Command("python3", "-c", "import laya_mlx").Run() == nil
	})
	if probe.ok {
		return []string{"python3"}, nil
	}
	if uv, err := exec.LookPath("uv"); err == nil {
		return []string{uv, "run", "-q", "--no-project", "--with", pypi, "python3"}, nil
	}
	return nil, errors.New("nothing here can run laya-mlx: install uv (https://docs.astral.sh/uv/) and ideacheck sets it up itself, or set backends.laya.python to a Python that has it (pip install laya-mlx)")
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// command is the worker process for args (serve MODEL | download MODEL).
func (j *Judge) command(args ...string) (*exec.Cmd, error) {
	if j.Command != nil {
		return j.Command(args...), nil
	}
	py, err := Interpreter(j.Python)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(py[0], append(append(py[1:], "-c", worker), args...)...)
	cmd.Dir = os.TempDir() // never a project directory: uv would read its pyproject
	return cmd, nil
}

func (j *Judge) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	answers, err := j.EvaluateBatch(ctx, state, []judge.Question{q})
	if err != nil {
		return judge.Answer{}, err
	}
	if len(answers) == 0 {
		return judge.Answer{}, fmt.Errorf("laya returned no answer for %s", q.ID)
	}
	return answers[0], nil
}

// EvaluateBatch sends every question in one request; the worker runs them as
// one batch through the model.
func (j *Judge) EvaluateBatch(ctx context.Context, state judge.State, qs []judge.Question) ([]judge.Answer, error) {
	w, err := j.worker(ctx)
	if err != nil {
		return nil, err
	}
	res, err := w.call(ctx, jev.NewRequest(state, "", qs))
	if err != nil {
		return nil, err
	}
	log := logging.From(ctx)
	log.Debug().Str("model", res.Model).Int("tokens_in", res.Usage.InputTokens).Int("questions", len(qs)).Msg("laya response")
	if len(res.AtLimit) > 0 {
		// The model reads whatever fits and says nothing: without this the check
		// silently scores a shortened idea.
		log.Warn().Strs("questions", res.AtLimit).Int("max_len", w.maxLen).Msg("laya: the state filled the model's context and was cut to fit; a checkpoint with a longer context (aac6fef/laya-typed-decisions-mlx) reads more of it")
	}
	return res.Answers.Convert(qs, method, res.Model, res.Usage.InputTokens, res.Usage.OutputTokens), nil
}

// reply is one line from the worker: the handshake, an answer, or an error.
type reply struct {
	jev.Response
	ID      int64    `json:"id"`
	Ready   bool     `json:"ready"`
	MaxLen  int      `json:"max_len"`
	AtLimit []string `json:"at_limit"`
	Error   string   `json:"error"`
}

// pool is every worker running in this process, by model and interpreter.
var pool = struct {
	sync.Mutex
	workers map[string]*process
}{workers: map[string]*process{}}

// worker is the running process for this model, started when there is none
// (or the last one exited), and ready: it has loaded the model.
func (j *Judge) worker(ctx context.Context) (*process, error) {
	key := j.Model + "\x00" + j.Python
	pool.Lock()
	w := pool.workers[key]
	if w == nil || w.exited() {
		cmd, err := j.command("serve", j.Model)
		if err != nil {
			pool.Unlock()
			return nil, err
		}
		if w, err = start(cmd); err != nil {
			pool.Unlock()
			return nil, err
		}
		pool.workers[key] = w
	}
	pool.Unlock()
	select {
	case <-w.ready:
		return w, w.loadErr
	case <-w.done:
		return nil, w.exitError()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// process is one worker: requests go in by id under mu, replies come back on
// the channel registered for that id.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tail

	ready   chan struct{} // closed once the handshake arrived (loadErr says how it went)
	loadErr error
	maxLen  int
	done    chan struct{} // closed when the process has exited

	mu      sync.Mutex
	next    int64
	pending map[int64]chan reply
}

func start(cmd *exec.Cmd) (*process, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	w := &process{cmd: cmd, stdin: stdin, stderr: &tail{}, ready: make(chan struct{}), done: make(chan struct{}), pending: map[int64]chan reply{}}
	cmd.Stderr = w.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the laya worker: %w", err)
	}
	go w.read(stdout)
	return w, nil
}

// read dispatches every line the worker writes; when it stops, everyone
// waiting is told so.
func (w *process) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	handshake := false
	for sc.Scan() {
		var r reply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // not ours: a library printing to stdout
		}
		if !handshake {
			handshake = true
			w.maxLen = r.MaxLen
			if r.Error != "" {
				w.loadErr = fmt.Errorf("laya could not load the model: %s", r.Error)
			}
			close(w.ready)
			continue
		}
		w.mu.Lock()
		ch := w.pending[r.ID]
		delete(w.pending, r.ID)
		w.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
	_ = w.cmd.Wait()
	if !handshake {
		w.loadErr = w.exitError()
		close(w.ready)
	}
	close(w.done)
}

func (w *process) exited() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// exitError says the worker is gone, with its last words. It is retryable:
// the next attempt starts another.
func (w *process) exitError() error {
	msg := "the laya worker exited"
	if s := w.stderr.String(); s != "" {
		msg += ": " + s
	}
	return &judge.RetryableError{Err: errors.New(msg)}
}

func (w *process) call(ctx context.Context, req jev.Request) (*reply, error) {
	ch := make(chan reply, 1)
	w.mu.Lock()
	w.next++
	id := w.next
	w.pending[id] = ch
	line, err := json.Marshal(struct {
		ID int64 `json:"id"`
		jev.Request
	}{id, req})
	if err == nil {
		_, err = w.stdin.Write(append(line, '\n'))
	}
	w.mu.Unlock()
	if err != nil {
		w.forget(id)
		return nil, w.exitError()
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, fmt.Errorf("laya: %s", r.Error)
		}
		return &r, nil
	case <-w.done:
		w.forget(id)
		return nil, w.exitError()
	case <-ctx.Done():
		w.forget(id)
		return nil, ctx.Err()
	}
}

func (w *process) forget(id int64) {
	w.mu.Lock()
	delete(w.pending, id)
	w.mu.Unlock()
}

// tail keeps the last bytes a process wrote to stderr, for its error message.
type tail struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > 4096 {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-4096:]...))
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(t.buf.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Downloaded reports whether the checkpoint is on this machine: a local
// directory, or a complete snapshot in the Hugging Face cache (the same
// lookup laya-mlx makes, so what it finds, the worker finds).
func Downloaded(model string) bool {
	if info, err := os.Stat(expandHome(model)); err == nil && info.IsDir() {
		return true
	}
	repo := "models--" + strings.ReplaceAll(model, "/", "--")
	found, _ := filepath.Glob(filepath.Join(hubCache(), repo, "snapshots", "*", "model.safetensors"))
	return len(found) > 0
}

// hubCache is huggingface_hub's cache directory, resolved as it resolves it:
// HF_HUB_CACHE, else HF_HOME/hub, else $XDG_CACHE_HOME|~/.cache/huggingface/hub.
func hubCache() string {
	if v := os.Getenv("HF_HUB_CACHE"); v != "" {
		return expandHome(v)
	}
	if v := os.Getenv("HF_HOME"); v != "" {
		return filepath.Join(expandHome(v), "hub")
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, _ := os.UserHomeDir()
		cache = filepath.Join(home, ".cache")
	}
	return filepath.Join(cache, "huggingface", "hub")
}

// Download fetches the checkpoint into the cache the worker loads from,
// reporting bytes as they land. The first run through uv also installs
// laya-mlx, which is the quiet start before the first status.
func (j *Judge) Download(ctx context.Context, progress func(status string, done, total int64)) error {
	cmd, err := j.command("download", j.Model)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tail{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the laya worker: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer stop()
	sc := bufio.NewScanner(stdout)
	failed := ""
	for sc.Scan() {
		var line struct {
			Status, Error string
			Done, Total   int64
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			failed = line.Error
			continue
		}
		progress(line.Status, line.Done, line.Total)
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case failed != "":
		return fmt.Errorf("download %s: %s", j.Model, failed)
	case err != nil:
		return fmt.Errorf("download %s: %w: %s", j.Model, err, stderr.String())
	}
	return nil
}
