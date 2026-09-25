package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/ideacheck"
)

func result(id, verdict string) *ideacheck.Result {
	return &ideacheck.Result{ID: id, Status: ideacheck.StatusOK, Verdict: verdict, Composite: 0.62, Backend: "mock",
		ConfigHash: "sha256:c", CreatedAt: "2026-09-19T12:00:00Z", Rubric: &ideacheck.RubricRef{Name: "business", Hash: "sha256:r"},
		TopRisks: []ideacheck.Contribution{{ID: "tarpit", Value: 0.2, Weight: 1.5}}}
}

func TestSaveThenFindBySeqIDAndLast(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "history.db") // directory must be created
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in := ideacheck.Intake{Idea: "  A payroll\n tool  ", Fields: map[string]string{"problem": "fines"}}
	for _, r := range []*ideacheck.Result{result("chk_A", "explore"), result("chk_B", "build")} {
		if err := s.Save(ctx, in, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(ctx, in, result("chk_A", "kill")); err == nil {
		t.Error("saving a duplicate id must fail, not overwrite history")
	}
	s.Close()

	s, err = Open(path) // reopen: data must survive, schema creation must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bySeq, err := s.Get(ctx, "1")
	if err != nil || bySeq.ID != "chk_A" || bySeq.Verdict != "explore" || bySeq.TopRisks[0].ID != "tarpit" || bySeq.Rubric.Hash != "sha256:r" {
		t.Errorf("Get(1) = %+v, %v (the full result must round-trip)", bySeq, err)
	}
	if byID, err := s.Get(ctx, "chk_B"); err != nil || byID.Verdict != "build" {
		t.Errorf("Get(chk_B) = %+v, %v", byID, err)
	}
	if last, err := s.Last(ctx); err != nil || last.ID != "chk_B" {
		t.Errorf("Last = %+v, %v", last, err)
	}
	if _, err := s.Get(ctx, "99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(99) = %v, want ErrNotFound", err)
	}
	rows, err := s.List(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "chk_B" || rows[0].Seq != 2 || rows[0].Idea != "A payroll tool" || rows[0].Rubric != "business" {
		t.Errorf("List(1) = %+v, %v (newest first, idea collapsed to one line)", rows, err)
	}
}

func TestLastOnEmptyStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Last(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Last on empty = %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/.local/share/x.db", "/home/u"); got != filepath.Join("/home/u", ".local/share/x.db") {
		t.Errorf("got %q", got)
	}
	if got := ExpandHome("/abs/x.db", "/home/u"); got != "/abs/x.db" {
		t.Errorf("got %q", got)
	}
}

// Findings a check kept come back for the next check of the same idea, until
// they are older than it will take.
func TestFindingsCacheKeepsFindingsUntilTheyAreTooOld(t *testing.T) {
	ctx := context.Background()
	var c ideacheck.ResearchCache = FindingsCache{Path: filepath.Join(t.TempDir(), "history.db")}
	if _, ok := c.Get(ctx, "k", time.Hour); ok {
		t.Fatal("an empty cache returned findings")
	}
	if err := c.Put(ctx, "k", []byte(`[{"title":"Weave"}]`)); err != nil {
		t.Fatal(err)
	}
	if b, ok := c.Get(ctx, "k", time.Hour); !ok || string(b) != `[{"title":"Weave"}]` {
		t.Errorf("Get = %s, %v", b, ok)
	}
	if _, ok := c.Get(ctx, "k", -time.Second); ok {
		t.Error("findings older than the limit were returned")
	}
}
