// Package sparkcore is the check as a gomobile library: `gomobile bind` turns
// it into Sparkcore.xcframework for the Sparkjudge iOS app. It is a thin shell
// over package ideacheck that speaks only what gomobile can carry — strings,
// numbers, errors, and interfaces of those — so every value crosses as JSON.
//
// The app brings the models: a Judge (Apple's on-device Foundation Model, a
// Core ML Laya later) and optionally a Writer, both written in Swift. The core
// brings everything else from its embedded configs: rubrics, prompts, fields,
// the arithmetic and the verdict. Nothing is searched and nothing is saved
// here; a question that requires evidence is skipped, as in any check without
// research, and the app keeps its own history.
//
// Nothing may panic across the boundary — a Go panic kills the app — so every
// exported call recovers and returns the panic as an error.
package sparkcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/prompt"
	"github.com/morethancoder/ideacheck/rubric"
)

// settings is what NewEngine reads from its settingsJSON. Every field is
// optional; the rest comes from the embedded config.yaml.
type settings struct {
	// Model names the model behind the judge on every result ("" = the judge's Name).
	Model string `json:"model"`
	// WriterModel names the model behind the writer ("" = its Name).
	WriterModel string `json:"writer_model"`
	// Explain and Extract turn the writer's two jobs on or off (nil = config.yaml).
	Explain *bool `json:"explain"`
	Extract *bool `json:"extract"`
	// MaxConcurrent bounds the questions the judge is asked at once. An
	// on-device model answers one at a time anyway; 0 = config.yaml's default
	// for a local model.
	MaxConcurrent int `json:"max_concurrent"`
	// QuestionSeconds and WriterSeconds replace the timeouts for one question
	// and for one writer call (0 = config.yaml's). The core's writer timeout
	// also bounds every question of one stage together, so it must outlast a
	// whole scoring stage.
	QuestionSeconds float64 `json:"question_seconds"`
	WriterSeconds   float64 `json:"writer_seconds"`
	// Batch sends each group of questions sharing a state to a BatchJudge in
	// one call (NewBatchEngine only; nil = on). False asks one at a time.
	Batch *bool `json:"batch"`
}

// Engine runs checks with one judge and, optionally, one writer. It is safe to
// use from several threads.
type Engine struct {
	core *ideacheck.Engine
}

// NewEngine builds an engine around a Swift judge and an optional writer (nil
// = no extract and no summary: the judge only judges). settingsJSON may be ""
// or "{}".
//
// The verdict's cuts are chosen by the judge's Name(): a rubric's
// verdict.backends.<name> block, when there is one, else its default cuts.
// No rubric has cuts for an on-device judge yet — they are tuned on bench,
// never guessed.
func NewEngine(settingsJSON string, judge Judge, writer Writer) (e *Engine, err error) {
	defer recoverTo(&err)
	if judge == nil {
		return nil, errors.New("sparkcore: an engine needs a judge")
	}
	return newEngine(settingsJSON, judge, nil, writer)
}

// NewBatchEngine is NewEngine for a judge that can also answer a group of
// questions in one call (BatchJudge). Each group shares one state — the core
// groups questions by what they read — and falls back to one question per
// call wherever the batch cannot answer. Settings {"batch": false} turns
// batches off, to compare.
func NewBatchEngine(settingsJSON string, judge BatchJudge, writer Writer) (e *Engine, err error) {
	defer recoverTo(&err)
	if judge == nil {
		return nil, errors.New("sparkcore: an engine needs a judge")
	}
	return newEngine(settingsJSON, judge, judge, writer)
}

func newEngine(settingsJSON string, judge Judge, batch BatchJudge, writer Writer) (*Engine, error) {
	var s settings
	if settingsJSON != "" {
		if err := strictJSON(settingsJSON, &s); err != nil {
			return nil, fmt.Errorf("settings: %w", err)
		}
	}
	// config.yaml's own default backend is a local model: its timeouts,
	// retries and concurrency are the ones an on-device judge wants.
	base, err := ideacheck.DefaultSettings("", "")
	if err != nil {
		return nil, err
	}
	prompts, err := prompt.Load(configs.Defaults(), base.PromptsDir)
	if err != nil {
		return nil, err
	}
	judgeName := judge.Name()
	base.Judge = ideacheck.Role{Model: or(s.Model, judgeName), Billing: ideacheck.BillingLocal}
	base.Writer = base.Judge
	base.Batch = false
	base.Research.Enabled = false
	if s.Explain != nil {
		base.Explain = *s.Explain
	}
	if s.Extract != nil {
		base.Extract = *s.Extract
	}
	if s.MaxConcurrent > 0 {
		base.MaxConcurrent = s.MaxConcurrent
	}
	if s.QuestionSeconds > 0 {
		base.Timeouts.Question = seconds(s.QuestionSeconds)
	}
	if s.WriterSeconds > 0 {
		base.Timeouts.Batch = seconds(s.WriterSeconds)
	}
	adapter := &judgeAdapter{swift: judge, name: judgeName, model: base.Judge.Model, prompts: prompts, question: base.Timeouts.Question}
	if batch != nil && (s.Batch == nil || *s.Batch) {
		adapter.batch = batch
		base.Batch = true
		if base.MaxConcurrent > 0 {
			adapter.slots = make(chan struct{}, base.MaxConcurrent)
		}
	}
	opts := ideacheck.Options{Settings: base, Judge: adapter}
	if writer != nil {
		writerName := writer.Name()
		opts.Settings.Writer = ideacheck.Role{Model: or(s.WriterModel, writerName), Billing: ideacheck.BillingLocal}
		opts.Writer = &writerAdapter{swift: writer, name: writerName, model: opts.Settings.Writer.Model}
	}
	core, err := ideacheck.New(opts)
	if err != nil {
		return nil, err
	}
	return &Engine{core: core}, nil
}

// checkOptions is what Prepare reads from its optionsJSON.
type checkOptions struct {
	// Rubric forces a rubric instead of letting the router pick one.
	Rubric string `json:"rubric"`
	// Strict stops at the first unstated fact with status needs_input, so the
	// app can ask; the default scores what is known and lists missing[].
	Strict bool `json:"strict"`
	// Answered names fields the person was offered and left blank.
	Answered []string `json:"answered"`
	// Earlier is the needs_input result this check follows up: its gap and
	// router answers stand.
	Earlier *ideacheck.Result `json:"earlier"`
}

// Check is one check, made by Prepare and run once by Run. Cancel stops it
// from any thread.
type Check struct {
	engine *ideacheck.Engine
	intake ideacheck.Intake
	opts   checkOptions

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

// Prepare reads a check: intakeJSON is {"idea", "context", "fields",
// "profile"} (see Fields), optionsJSON may be "" or {"rubric", "strict",
// "answered", "earlier"}.
func (e *Engine) Prepare(intakeJSON, optionsJSON string) (c *Check, err error) {
	defer recoverTo(&err)
	in, err := ideacheck.ParseIntake([]byte(intakeJSON))
	if err != nil {
		return nil, err
	}
	var o checkOptions
	if optionsJSON != "" {
		if err := strictJSON(optionsJSON, &o); err != nil {
			return nil, fmt.Errorf("check options: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Check{engine: e.core, intake: in, opts: o, ctx: ctx, cancel: cancel}, nil
}

// Run runs the check and returns the result JSON (schemas/check_result.schema.json).
// It blocks until the check ends, so call it off the main thread. Each step is
// handed to listener (may be nil) as an event JSON, one at a time, before Run returns.
// A cancelled check returns an error.
func (c *Check) Run(listener Listener) (resultJSON string, err error) {
	defer recoverTo(&err)
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return "", errors.New("sparkcore: a check runs once; make another with Prepare")
	}
	c.started = true
	c.mu.Unlock()
	defer c.cancel()

	o := ideacheck.CheckOptions{Rubric: c.opts.Rubric, Proceed: !c.opts.Strict, Answered: c.opts.Answered, Earlier: c.opts.Earlier}
	if listener != nil {
		o.OnEvent = func(ev ideacheck.Event) {
			if c.ctx.Err() != nil {
				return // nothing more is drawn for a check the app has let go of
			}
			if b, err := json.Marshal(ev); err == nil {
				notify(listener, string(b))
			}
		}
	}
	res, err := c.engine.Check(c.ctx, c.intake, o)
	if err := c.ctx.Err(); err != nil {
		return "", fmt.Errorf("sparkcore: check cancelled: %w", err)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	return string(b), err
}

// Cancel stops the check: questions not yet answered are abandoned and Run
// returns an error. A Swift call already under way finishes on its own; its
// answer is dropped.
func (c *Check) Cancel() { c.cancel() }

// Check runs one check start to finish: Prepare(intakeJSON, "") then Run.
// Use Prepare when the check may need cancelling.
func (e *Engine) Check(intakeJSON string, listener Listener) (string, error) {
	c, err := e.Prepare(intakeJSON, "")
	if err != nil {
		return "", err
	}
	return c.Run(listener)
}

// Fields is the catalogue of what a check accepts — {"idea": [...],
// "profile": [...]}, each {"name", "description", "example"} — for the app's
// forms. It is configs/fields.yaml, the same one the CLI and server read.
func Fields() (fieldsJSON string, err error) {
	defer recoverTo(&err)
	f, err := ideacheck.LoadFields(configs.Defaults())
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(f)
	return string(b), err
}

// Rubrics is every scoring rubric, by name, with its questions and verdict
// cuts: what a result's dimensions and answers refer to.
func Rubrics() (rubricsJSON string, err error) {
	defer recoverTo(&err)
	files := configs.Defaults()
	base, err := ideacheck.DefaultSettings("", "")
	if err != nil {
		return "", err
	}
	names, err := rubric.Names(files, base.RubricsDir)
	if err != nil {
		return "", err
	}
	out := make(map[string]*rubric.Rubric, len(names))
	for _, name := range names {
		rb, err := rubric.Load(files, base.RubricsDir, name)
		if err != nil {
			return "", err
		}
		out[name] = rb
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// recoverTo turns a panic into the call's error, so it never reaches the app.
func recoverTo(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("sparkcore: internal error: %v\n%s", p, debug.Stack())
	}
}

func strictJSON(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
