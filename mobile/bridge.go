package sparkcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
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

// judgeAdapter is a Swift Judge as a judge.Judge.
type judgeAdapter struct {
	swift   Judge
	name    string
	model   string
	prompts *prompt.Set
}

func (a *judgeAdapter) Name() string { return a.name }

// Capabilities: one question per call. Concurrency is Settings.MaxConcurrent.
func (a *judgeAdapter) Capabilities() judge.Capabilities { return judge.Capabilities{} }

func (a *judgeAdapter) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	stateText, err := a.prompts.State(state)
	if err != nil {
		return judge.Answer{}, err
	}
	// "vote" asks for the answer alone: one pick, the way an on-device model
	// answers best. The answer shape it names is the reply format above.
	ask, err := a.prompts.Questions([]judge.Question{q}, "vote")
	if err != nil {
		return judge.Answer{}, err
	}
	req, err := json.Marshal(request{Question: viewOf(q), State: state, System: a.prompts.System, Prompt: stateText + "\n\n" + ask})
	if err != nil {
		return judge.Answer{}, err
	}
	start := time.Now()
	out, err := call(ctx, func() (string, error) { return a.swift.Evaluate(string(req)) })
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
