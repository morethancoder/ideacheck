package sparkcore

import (
	"encoding/json"
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
