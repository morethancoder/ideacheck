package sparkcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/prompt"
)

// Judge answers one typed question. The app implements it in Swift; the core
// calls Evaluate from several threads at once (up to max_concurrent), so an
// implementation must be safe for that — with Foundation Models, one
// LanguageModelSession per call.
//
// requestJSON is
//
//	{"question": {"id", "kind": "choice"|"score"|"noul", "instructions",
//	              "options": [{"key", "description"}],   // choice, sorted by key
//	              "levels": ["…", …],                      // score, lowest first
//	              "criteria": {"yes", "no"}},              // noul, optional
//	 "state":  {…},       // only what the question reads
//	 "system": "…",       // configs/prompts/judge_system.md
//	 "prompt": "…"}       // state + question, rendered by question_structured.tmpl
//
// and the answer is one of
//
//	{"choice": "<option key>"}
//	{"level": <level index>}
//	{"probability": <0..1 that the statement is true>}   // noul; or {"yes": true|false}
//
// optionally with "probabilities" (option key or level index → p),
// "confidence" (0..1), "model", "method", "tokens_in", "tokens_out". The
// prompts live in the core's configs, so the app sends them as they are.
type Judge interface {
	Name() string
	Evaluate(requestJSON string) (answerJSON string, err error)
}

// BatchJudge is a Judge that can also answer several questions over one state
// in one call — a generative model answering a whole group in one response
// rather than one by one. Build its engine with NewBatchEngine. The core
// groups questions by the state they read (`uses`), so a batch never shows a
// question state it does not read.
//
// requestJSON is
//
//	{"questions": [<question>, …],  // as in Judge's request, in the core's order
//	 "state":  {…},                 // what every one of them reads
//	 "system": "…",
//	 "prompt": "…"}                 // state + every question, one "Answer shape" each
//
// and the answer is {"answers": {"<question id>": <an answer as Evaluate
// returns it>, …}}. An error — say the questions do not fit the model's
// context — sends each question on its own through Evaluate instead, and so
// does an answer that is missing or does not fit its question: a batch is
// only ever a shortcut.
type BatchJudge interface {
	Name() string
	Evaluate(requestJSON string) (answerJSON string, err error)
	EvaluateBatch(requestJSON string) (answersJSON string, err error)
}

// Writer does the two jobs that have no option list: Extract reads the stated
// facts out of the document — fieldsJSON is ["problem", …], the answer
// {"<field>": "<value>", …} with only the fields the document states — and
// Narrate writes the plain-language paragraph on why the idea got its
// verdict. Both are optional per engine (settings "extract", "explain"); a
// Swift method that cannot do its job returns an error, which costs the check
// that step and nothing else.
type Writer interface {
	Name() string
	Extract(system, user, fieldsJSON string) (valuesJSON string, err error)
	Narrate(system, brief string) (text string, err error)
}

// Listener receives the check's progress. eventJSON is {"type":
// "started"|"answered"|"failed", "stage", "question", "answer", "value"}; the
// calls come one at a time from a background thread.
type Listener interface {
	OnEvent(eventJSON string)
}

// request is what Judge.Evaluate receives.
type request struct {
	Question questionView `json:"question"`
	State    judge.State  `json:"state"`
	System   string       `json:"system"`
	Prompt   string       `json:"prompt"`
}

// questionView is the part of a question a judge needs: no weights, no
// polarity, options in a fixed order a Swift schema can take as they come.
type questionView struct {
	ID           string              `json:"id"`
	Kind         judge.Kind          `json:"kind"`
	Instructions string              `json:"instructions"`
	Options      []option            `json:"options,omitempty"`
	Levels       []string            `json:"levels,omitempty"`
	Criteria     *judge.NoulCriteria `json:"criteria,omitempty"`
}

type option struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

func viewOf(q judge.Question) questionView {
	v := questionView{ID: q.ID, Kind: q.Kind, Instructions: q.Instructions, Levels: q.Levels, Criteria: q.Criteria}
	for k, d := range q.Options {
		v.Options = append(v.Options, option{k, d})
	}
	sort.Slice(v.Options, func(i, j int) bool { return v.Options[i].Key < v.Options[j].Key })
	return v
}

// reply is what Judge.Evaluate returns.
type reply struct {
	Choice        string             `json:"choice"`
	Level         *float64           `json:"level"`
	Probability   *float64           `json:"probability"`
	Yes           *bool              `json:"yes"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    *float64           `json:"confidence"`
	Model         string             `json:"model"`
	Method        string             `json:"method"`
	TokensIn      int                `json:"tokens_in"`
	TokensOut     int                `json:"tokens_out"`
}

// batchRequest is what BatchJudge.EvaluateBatch receives.
type batchRequest struct {
	Questions []questionView `json:"questions"`
	State     judge.State    `json:"state"`
	System    string         `json:"system"`
	Prompt    string         `json:"prompt"`
}

// batchReply is what BatchJudge.EvaluateBatch returns: each answer raw, so
// one that does not fit its question costs that question alone.
type batchReply struct {
	Answers map[string]json.RawMessage `json:"answers"`
}

// judgeAdapter is a Swift Judge as a judge.Judge; with batch set, also a
// judge.BatchJudge.
type judgeAdapter struct {
	swift   Judge
	batch   BatchJudge // nil: one question per call
	name    string
	model   string
	prompts *prompt.Set
	// question bounds one question asked on its own inside a batch (the
	// fan-out's own per-question timeout does not reach into a batch).
	question time.Duration
	// slots holds one token per Swift call in flight: the fan-out limits
	// single questions to max_concurrent, but runs every batch at once.
	slots chan struct{}
	// rounds gathers the groups of one stage that see the same state.
	rounds rounds
}

// rounds merges batches. The core groups a stage's questions by the state
// they declare (`uses`) and asks every group at once; on the phone nothing is
// searched, so [idea] and [idea, evidence.market] both come out as {idea}.
// Groups that arrive within gatherWindow with the same state share one call:
// each question still sees exactly the state it reads.
type rounds struct {
	mu   sync.Mutex
	open map[string]*round
}

type round struct {
	qs      []judge.Question
	answers []judge.Answer
	err     error
	done    chan struct{}
}

// gatherWindow is how long the first group of a round waits for the others:
// the fan-out starts them together, so microseconds apart.
const gatherWindow = 20 * time.Millisecond

// swiftCall runs one call to the Swift judge within its slot.
func (a *judgeAdapter) swiftCall(ctx context.Context, f func() (string, error)) (string, error) {
	if a.slots != nil {
		select {
		case a.slots <- struct{}{}:
			defer func() { <-a.slots }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return call(ctx, f)
}

func (a *judgeAdapter) Name() string { return a.name }

// Capabilities: one question per call, unless the Swift judge takes batches.
// Concurrency is Settings.MaxConcurrent.
func (a *judgeAdapter) Capabilities() judge.Capabilities {
	return judge.Capabilities{NativeBatch: a.batch != nil}
}

// prompt renders the state and the questions as the configs' structured
// template has them. "vote" asks for the answer alone: one pick, the way an
// on-device model answers best. The answer shape it names is the reply format.
func (a *judgeAdapter) prompt(state judge.State, qs []judge.Question) (string, error) {
	stateText, err := a.prompts.State(state)
	if err != nil {
		return "", err
	}
	ask, err := a.prompts.Questions(qs, "vote")
	if err != nil {
		return "", err
	}
	return stateText + "\n\n" + ask, nil
}

func (a *judgeAdapter) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	text, err := a.prompt(state, []judge.Question{q})
	if err != nil {
		return judge.Answer{}, err
	}
	req, err := json.Marshal(request{Question: viewOf(q), State: state, System: a.prompts.System, Prompt: text})
	if err != nil {
		return judge.Answer{}, err
	}
	start := time.Now()
	out, err := a.swiftCall(ctx, func() (string, error) { return a.swift.Evaluate(string(req)) })
	if err != nil {
		return judge.Answer{}, err
	}
	ans, err := a.answer(q, out)
	if err != nil {
		return judge.Answer{}, fmt.Errorf("%s answered %s badly: %w", a.name, q.ID, err)
	}
	ans.LatencyMS = time.Since(start).Milliseconds()
	return ans, nil
}

// EvaluateBatch answers one group of questions, in one round with every other
// group of the stage that sees the same state (rounds).
func (a *judgeAdapter) EvaluateBatch(ctx context.Context, state judge.State, qs []judge.Question) ([]judge.Answer, error) {
	key, err := json.Marshal(state)
	if err != nil {
		return a.answerGroup(ctx, state, qs)
	}
	a.rounds.mu.Lock()
	if a.rounds.open == nil {
		a.rounds.open = map[string]*round{}
	}
	r, joined := a.rounds.open[string(key)]
	if !joined {
		r = &round{done: make(chan struct{})}
		a.rounds.open[string(key)] = r
	}
	from := len(r.qs)
	r.qs = append(r.qs, qs...)
	a.rounds.mu.Unlock()

	if joined {
		select {
		case <-r.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		t := time.NewTimer(gatherWindow)
		select {
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
		a.rounds.mu.Lock()
		delete(a.rounds.open, string(key))
		all := r.qs
		a.rounds.mu.Unlock()
		r.answers, r.err = a.answerGroup(ctx, state, all)
		close(r.done)
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.answers[from : from+len(qs)], nil
}

// answerGroup asks the Swift judge every question of a group in one call.
// A question the batch leaves out or answers badly, or every question when
// the batch call fails, is asked on its own; only a check that is over
// (cancelled, or out of time) fails the group.
func (a *judgeAdapter) answerGroup(ctx context.Context, state judge.State, qs []judge.Question) ([]judge.Answer, error) {
	answers := make([]judge.Answer, len(qs))
	done := make([]bool, len(qs))
	if a.batch != nil && len(qs) > 1 {
		a.tryBatch(ctx, state, qs, answers, done)
	}
	for i, q := range qs {
		if done[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		qctx, cancel := ctx, context.CancelFunc(func() {})
		if a.question > 0 {
			qctx, cancel = context.WithTimeout(ctx, a.question)
		}
		ans, err := a.Evaluate(qctx, state, q)
		cancel()
		if err != nil {
			ans = judge.Answer{Err: err.Error()}
		}
		ans.ID, ans.Kind = q.ID, q.Kind
		answers[i] = ans
	}
	return answers, nil
}

// tryBatch fills answers (and done) from one batch call, where it can.
func (a *judgeAdapter) tryBatch(ctx context.Context, state judge.State, qs []judge.Question, answers []judge.Answer, done []bool) {
	text, err := a.prompt(state, qs)
	if err != nil {
		return
	}
	req := batchRequest{State: state, System: a.prompts.System, Prompt: text}
	for _, q := range qs {
		req.Questions = append(req.Questions, viewOf(q))
	}
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	start := time.Now()
	out, err := a.swiftCall(ctx, func() (string, error) { return a.batch.EvaluateBatch(string(body)) })
	if err != nil {
		return
	}
	var r batchReply
	if json.Unmarshal([]byte(out), &r) != nil {
		return
	}
	took := time.Since(start).Milliseconds()
	for i, q := range qs {
		raw, ok := r.Answers[q.ID]
		if !ok {
			continue
		}
		ans, err := a.answer(q, string(raw))
		if err != nil {
			continue
		}
		ans.ID, ans.LatencyMS = q.ID, took
		answers[i], done[i] = ans, true
	}
}

// answer checks the reply against the question and fills what the core needs:
// probabilities (a single pick carries all the mass) and a confidence.
func (a *judgeAdapter) answer(q judge.Question, raw string) (judge.Answer, error) {
	var r reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return judge.Answer{}, err
	}
	ans := judge.Answer{ID: q.ID, Kind: q.Kind, Probabilities: r.Probabilities, Model: or(r.Model, a.model),
		Method: or(r.Method, a.name), TokensIn: r.TokensIn, TokensOut: r.TokensOut}
	switch q.Kind {
	case judge.Choice:
		if _, ok := q.Options[r.Choice]; !ok {
			return judge.Answer{}, fmt.Errorf("choice %q is not an option", r.Choice)
		}
		ans.Choice = r.Choice
		if len(ans.Probabilities) == 0 {
			ans.Probabilities = map[string]float64{r.Choice: 1}
		}
	case judge.Score:
		top := float64(len(q.Levels) - 1)
		if r.Level == nil || *r.Level < 0 || *r.Level > top || math.IsNaN(*r.Level) {
			return judge.Answer{}, fmt.Errorf("level must be 0..%d", len(q.Levels)-1)
		}
		ans.Score = *r.Level
		if len(ans.Probabilities) == 0 {
			ans.Probabilities = map[string]float64{strconv.Itoa(int(math.Round(*r.Level))): 1}
		}
	case judge.Noul:
		if r.Probability == nil && r.Yes != nil {
			p := 0.0
			if *r.Yes {
				p = 1
			}
			r.Probability = &p
		}
		if r.Probability == nil || *r.Probability < 0 || *r.Probability > 1 {
			return judge.Answer{}, errors.New("probability must be 0..1")
		}
		ans.Noul = *r.Probability
		ans.Confidence = judge.NoulConfidence(ans.Noul)
	default:
		return judge.Answer{}, fmt.Errorf("unknown question kind %q", q.Kind)
	}
	if q.Kind != judge.Noul {
		for _, p := range ans.Probabilities {
			ans.Confidence = max(ans.Confidence, p)
		}
	}
	if r.Confidence != nil {
		ans.Confidence = min(max(*r.Confidence, 0), 1)
	}
	return ans, nil
}

// writerAdapter is a Swift Writer as a judge.Judge that writes: it extracts and
// narrates, and is never asked a typed question.
type writerAdapter struct {
	swift Writer
	name  string
	model string
}

func (w *writerAdapter) Name() string                     { return w.name }
func (w *writerAdapter) Capabilities() judge.Capabilities { return judge.Capabilities{} }

func (w *writerAdapter) Evaluate(context.Context, judge.State, judge.Question) (judge.Answer, error) {
	return judge.Answer{}, errors.New("sparkcore: the writer does not judge")
}

func (w *writerAdapter) Extract(ctx context.Context, system, user string, fields []string) (judge.Extraction, error) {
	names, err := json.Marshal(fields)
	if err != nil {
		return judge.Extraction{}, err
	}
	out, err := call(ctx, func() (string, error) { return w.swift.Extract(system, user, string(names)) })
	if err != nil {
		return judge.Extraction{Model: w.model}, err
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		return judge.Extraction{Model: w.model}, fmt.Errorf("%s extracted badly: %w", w.name, err)
	}
	return judge.Extraction{Values: values, Model: w.model}, nil
}

func (w *writerAdapter) Narrate(ctx context.Context, system, brief string) (judge.Narration, error) {
	text, err := call(ctx, func() (string, error) { return w.swift.Narrate(system, brief) })
	return judge.Narration{Text: text, Model: w.model}, err
}

// call runs one Swift call and gives up on it when ctx ends. Swift cannot be
// interrupted from here, so a call past its deadline runs on and its answer is
// dropped; the check itself moves on at once. A panic in the call (a Go fake
// in tests; gomobile turns Swift failures into errors) is an error.
func call(ctx context.Context, f func() (string, error)) (string, error) {
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r.err = fmt.Errorf("panic: %v", p)
			}
			done <- r
		}()
		r.out, r.err = f()
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// notify hands one event to the app; a listener that panics loses the event,
// not the check.
func notify(l Listener, event string) {
	defer func() { _ = recover() }()
	l.OnEvent(event)
}
