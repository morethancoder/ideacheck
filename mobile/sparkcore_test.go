package sparkcore

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubJudge is what a Swift judge looks like from Go: JSON in, JSON out. It
// picks the first option, the top level and a 0.8 "true", every time.
type stubJudge struct {
	answer func(req request) string // nil = the stub's fixed answers
	mu     sync.Mutex
	seen   []request
}

func (s *stubJudge) Name() string { return "stub" }

func (s *stubJudge) Evaluate(requestJSON string) (string, error) {
	var req request
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.seen = append(s.seen, req)
	s.mu.Unlock()
	if s.answer != nil {
		return s.answer(req), nil
	}
	return fixed(req), nil
}

func fixed(req request) string {
	switch req.Question.Kind {
	case "choice":
		return `{"choice": "` + req.Question.Options[0].Key + `"}`
	case "score":
		b, _ := json.Marshal(map[string]int{"level": len(req.Question.Levels) - 1})
		return string(b)
	}
	return `{"probability": 0.8}`
}

type events struct {
	mu  sync.Mutex
	all []map[string]any
}

func (e *events) OnEvent(eventJSON string) {
	var ev map[string]any
	if err := json.Unmarshal([]byte(eventJSON), &ev); err != nil {
		panic(err) // fails the test through the missing event count
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.all = append(e.all, ev)
}

type result struct {
	Status  string `json:"status"`
	Backend string `json:"backend"`
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
	Answers []struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Error  string `json:"error"`
	} `json:"answers"`
	Extracted []string `json:"extracted"`
}

const idea = `{"idea": "A scheduling app for dental clinics that fills cancelled slots from a waitlist by text message.", "fields": {"audience": "independent dental clinics"}}`

func mustEngine(t *testing.T, settings string, j Judge, w Writer) *Engine {
	t.Helper()
	e, err := NewEngine(settings, j, w)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func run(t *testing.T, e *Engine, l Listener) result {
	t.Helper()
	out, err := e.Check(idea, l)
	if err != nil {
		t.Fatal(err)
	}
	var r result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCheckRunsWithASwiftShapedJudge(t *testing.T) {
	j := &stubJudge{}
	var got events
	r := run(t, mustEngine(t, `{"max_concurrent": 4}`, j, nil), &got)

	if r.Status != "ok" || r.Verdict == "" || r.Backend != "stub" {
		t.Fatalf("status %q verdict %q backend %q, want ok, a verdict, stub", r.Status, r.Verdict, r.Backend)
	}
	for _, a := range r.Answers {
		// No research and no profile: those questions are skipped, not guessed.
		skipped := a.Error == "no evidence given" || a.Error == "no profile given"
		if a.Error != "" && !skipped {
			t.Errorf("%s failed: %s", a.ID, a.Error)
		}
	}
	if len(got.all) == 0 {
		t.Fatal("no events")
	}
	for _, ev := range got.all {
		if ev["type"] == nil || ev["stage"] == nil {
			t.Fatalf("event without type or stage: %v", ev)
		}
	}
	// The judge gets the configs' prompt and only the state its question reads.
	for _, req := range j.seen {
		if req.System == "" || !strings.Contains(req.Prompt, "QUESTION id="+req.Question.ID) {
			t.Fatalf("%s: request carries no rendered prompt", req.Question.ID)
		}
		if _, ok := req.State["evidence"]; ok {
			t.Fatalf("%s: evidence sent, but nothing was searched", req.Question.ID)
		}
	}
}

func TestAMisbehavingJudgeCostsItsQuestionOnly(t *testing.T) {
	j := &stubJudge{answer: func(req request) string {
		switch req.Question.Kind {
		case "choice":
			if req.Question.ID != "idea_type" {
				return `{"choice": "not-an-option"}`
			}
		case "score":
			panic("the model fell over")
		}
		return fixed(req)
	}}
	r := run(t, mustEngine(t, "", j, nil), nil)

	if r.Status != "ok" {
		t.Fatalf("status %q, want ok: failed questions are excluded, not fatal", r.Status)
	}
	failed := 0
	for _, a := range r.Answers {
		if strings.Contains(a.Error, "not an option") || strings.Contains(a.Error, "fell over") {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("a bad choice and a panic were accepted as answers")
	}
}

func TestCancelStopsARunningCheck(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	j := &stubJudge{answer: func(req request) string { <-release; return fixed(req) }}
	c, err := mustEngine(t, "", j, nil).Prepare(idea, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Run(nil); done <- err }()
	time.Sleep(50 * time.Millisecond)
	c.Cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("Run returned %v, want a cancellation error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Cancel while the judge was still busy")
	}
	if _, err := c.Run(nil); err == nil {
		t.Fatal("a check ran twice")
	}
}

type stubWriter struct{}

func (stubWriter) Name() string { return "stub-writer" }
func (stubWriter) Extract(system, user, fieldsJSON string) (string, error) {
	var fields []string
	if err := json.Unmarshal([]byte(fieldsJSON), &fields); err != nil {
		return "", err
	}
	for _, f := range fields {
		if f == "problem" {
			return `{"problem": "cancelled slots sit empty"}`, nil
		}
	}
	return `{}`, nil
}
func (stubWriter) Narrate(system, brief string) (string, error) { return "Because.", nil }

func TestTheWriterExtractsAndNarrates(t *testing.T) {
	r := run(t, mustEngine(t, `{"extract": true, "explain": true}`, &stubJudge{}, stubWriter{}), nil)
	if len(r.Extracted) != 1 || r.Extracted[0] != "problem" {
		t.Fatalf("extracted %v, want [problem]", r.Extracted)
	}
	if r.Summary != "Because." {
		t.Fatalf("summary %q", r.Summary)
	}
}

func TestBadInputIsAnErrorNotACrash(t *testing.T) {
	if _, err := NewEngine(`{"modle": "x"}`, &stubJudge{}, nil); err == nil {
		t.Error("a misspelt setting was accepted")
	}
	if _, err := NewEngine("", nil, nil); err == nil {
		t.Error("an engine without a judge was built")
	}
	if _, err := mustEngine(t, "", &stubJudge{}, nil).Check(`{"idea": 3}`, nil); err == nil {
		t.Error("an intake of the wrong shape was accepted")
	}
}

func TestCataloguesForTheAppsForms(t *testing.T) {
	f, err := Fields()
	if err != nil {
		t.Fatal(err)
	}
	var fields struct{ Idea, Profile []struct{ Name string } }
	if err := json.Unmarshal([]byte(f), &fields); err != nil || len(fields.Idea) == 0 || len(fields.Profile) == 0 {
		t.Fatalf("fields %s: %v", f, err)
	}
	rb, err := Rubrics()
	if err != nil {
		t.Fatal(err)
	}
	var rubrics map[string]struct{ Questions []struct{ ID string } }
	if err := json.Unmarshal([]byte(rb), &rubrics); err != nil || len(rubrics["business"].Questions) == 0 {
		t.Fatalf("rubrics: %v", err)
	}
	if _, ok := rubrics["_gaps"]; ok {
		t.Fatal("the gaps rubric is not a scoring rubric")
	}
}

// stubBatch is a Swift judge that takes batches: it answers every question of
// a batch as stubJudge would, except where answer says otherwise.
type stubBatch struct {
	stubJudge
	batch   func(req batchRequest) (string, error) // nil = answer every question
	batches []batchRequest
}

func (s *stubBatch) EvaluateBatch(requestJSON string) (string, error) {
	var req batchRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.batches = append(s.batches, req)
	s.mu.Unlock()
	if s.batch != nil {
		return s.batch(req)
	}
	return allOf(req, nil), nil
}

// allOf answers every question of a batch with fixed, except those in skip.
func allOf(req batchRequest, skip map[string]string) string {
	out := map[string]json.RawMessage{}
	for _, q := range req.Questions {
		if raw, ok := skip[q.ID]; ok {
			if raw != "" {
				out[q.ID] = json.RawMessage(raw)
			}
			continue
		}
		out[q.ID] = json.RawMessage(fixed(request{Question: q}))
	}
	b, _ := json.Marshal(batchReply{Answers: out})
	return string(b)
}

func batchEngine(t *testing.T, settings string, j BatchJudge) *Engine {
	t.Helper()
	e, err := NewBatchEngine(settings, j, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestABatchJudgeAnswersEachStateInOneCall(t *testing.T) {
	j := &stubBatch{}
	r := run(t, batchEngine(t, `{"max_concurrent": 1}`, j), nil)
	perQuestion := run(t, mustEngine(t, `{"max_concurrent": 1}`, &stubJudge{}, nil), nil)

	if r.Status != "ok" || r.Verdict != perQuestion.Verdict {
		t.Fatalf("batched: status %q verdict %q; one at a time: verdict %q", r.Status, r.Verdict, perQuestion.Verdict)
	}
	if len(j.batches) == 0 {
		t.Fatal("no batch was asked")
	}
	asked := len(j.seen)
	for _, b := range j.batches {
		asked += len(b.Questions)
		if len(b.Questions) < 2 {
			t.Errorf("a batch of %d: a lone question goes through Evaluate", len(b.Questions))
		}
		// One state per batch, and every question in it reads that state.
		for _, q := range b.Questions {
			if !strings.Contains(b.Prompt, "QUESTION id="+q.ID) {
				t.Errorf("batch prompt lacks %s", q.ID)
			}
		}
		if _, ok := b.State["evidence"]; ok {
			t.Error("evidence sent, but nothing was searched")
		}
	}
	// Nothing is searched here, so every question of a stage sees {idea}:
	// one call for the gaps and router, one for the rubric.
	if len(j.batches) != 2 || len(j.seen) != 0 {
		t.Fatalf("%d batches and %d single calls for %d questions, want 2 and 0", len(j.batches), len(j.seen), asked)
	}
	for _, a := range r.Answers {
		skipped := a.Error == "no evidence given" || a.Error == "no profile given"
		if a.Error != "" && !skipped {
			t.Errorf("%s failed: %s", a.ID, a.Error)
		}
	}
}

func TestABatchThatFailsIsAskedOneByOne(t *testing.T) {
	j := &stubBatch{batch: func(batchRequest) (string, error) {
		return "", errors.New("the questions do not fit the context")
	}}
	r := run(t, batchEngine(t, "", j), nil)
	if r.Status != "ok" || len(j.batches) == 0 || len(j.seen) == 0 {
		t.Fatalf("status %q, %d batches, %d single calls: want ok, a batch tried, then singles", r.Status, len(j.batches), len(j.seen))
	}
	for _, a := range r.Answers {
		skipped := a.Error == "no evidence given" || a.Error == "no profile given"
		if a.Error != "" && !skipped {
			t.Errorf("%s failed: %s", a.ID, a.Error)
		}
	}
}

func TestAQuestionTheBatchMissesIsAskedAlone(t *testing.T) {
	var missed, bad string
	var mu sync.Mutex
	j := &stubBatch{}
	j.batch = func(req batchRequest) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if missed == "" && len(req.Questions) >= 2 {
			missed, bad = req.Questions[0].ID, req.Questions[1].ID
			return allOf(req, map[string]string{missed: "", bad: `{"choice": "not-an-option", "level": 99, "probability": 7}`}), nil
		}
		return allOf(req, nil), nil
	}
	r := run(t, batchEngine(t, "", j), nil)
	alone := map[string]bool{}
	for _, req := range j.seen {
		alone[req.Question.ID] = true
	}
	if !alone[missed] || !alone[bad] {
		t.Fatalf("asked alone %v; want %s (left out) and %s (answered badly)", alone, missed, bad)
	}
	for _, a := range r.Answers {
		if a.ID == missed || a.ID == bad {
			if a.Error != "" {
				t.Errorf("%s: %s", a.ID, a.Error)
			}
		}
	}
}

func TestBatchesCanBeTurnedOff(t *testing.T) {
	j := &stubBatch{}
	run(t, batchEngine(t, `{"batch": false}`, j), nil)
	if len(j.batches) != 0 || len(j.seen) == 0 {
		t.Fatalf("%d batches, %d single calls with batch off", len(j.batches), len(j.seen))
	}
}
