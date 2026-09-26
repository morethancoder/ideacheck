package store

import (
	"context"
	"database/sql"
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
		if err := s.Save(ctx, Local, in, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(ctx, Local, in, result("chk_A", "kill")); err == nil {
		t.Error("saving a duplicate id must fail, not overwrite history")
	}
	s.Close()

	s, err = Open(path) // reopen: data must survive, schema creation must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bySeq, err := s.Get(ctx, Local, "1")
	if err != nil || bySeq.ID != "chk_A" || bySeq.Verdict != "explore" || bySeq.TopRisks[0].ID != "tarpit" || bySeq.Rubric.Hash != "sha256:r" {
		t.Errorf("Get(1) = %+v, %v (the full result must round-trip)", bySeq, err)
	}
	if byID, err := s.Get(ctx, Local, "chk_B"); err != nil || byID.Verdict != "build" {
		t.Errorf("Get(chk_B) = %+v, %v", byID, err)
	}
	if last, err := s.Last(ctx, Local); err != nil || last.ID != "chk_B" {
		t.Errorf("Last = %+v, %v", last, err)
	}
	if _, err := s.Get(ctx, Local, "99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(99) = %v, want ErrNotFound", err)
	}
	rows, err := s.List(ctx, Local, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "chk_B" || rows[0].Seq != 2 || rows[0].Idea != "A payroll tool" || rows[0].Rubric != "business" {
		t.Errorf("List(1) = %+v, %v (newest first, idea collapsed to one line)", rows, err)
	}
}

// Each owner sees only their own checks, by id, by sequence number and in the
// list; someone else's check is simply not found.
func TestOwnersSeeOnlyTheirOwnChecks(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := ideacheck.Intake{Idea: "x"}
	if err := s.Save(ctx, "ann", in, result("chk_ANN", "build")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "bob", in, result("chk_BOB", "park")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"chk_ANN", "1"} {
		if _, err := s.Get(ctx, "bob", ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("bob Get(%s) = %v, want ErrNotFound", ref, err)
		}
	}
	if got, err := s.Get(ctx, "bob", "chk_BOB"); err != nil || got.Verdict != "park" {
		t.Errorf("bob Get(own) = %+v, %v", got, err)
	}
	if rows, err := s.List(ctx, "ann", 10); err != nil || len(rows) != 1 || rows[0].ID != "chk_ANN" {
		t.Errorf("ann List = %+v, %v", rows, err)
	}
	if last, err := s.Last(ctx, Local); !errors.Is(err, ErrNotFound) {
		t.Errorf("the local history saw a tenant's check: %+v, %v", last, err)
	}
}

// A history written before checks had owners opens, keeps its rows as the
// local user's, and takes owned rows from then on.
func TestOpenMigratesAHistoryWithoutOwners(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE checks (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL, status TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '', composite REAL NOT NULL DEFAULT 0,
		backend TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', rubric TEXT NOT NULL DEFAULT '', rubric_hash TEXT NOT NULL DEFAULT '',
		config_hash TEXT NOT NULL, idea TEXT NOT NULL, intake TEXT NOT NULL, result TEXT NOT NULL);
		INSERT INTO checks (id, created_at, status, backend, config_hash, idea, intake, result)
		VALUES ('chk_OLD', '2026-01-01T00:00:00Z', 'ok', 'mock', 'h', 'old idea', '{}', '{"id":"chk_OLD","status":"ok"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // a second open finds the column already there
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, _ := Open(path)
	defer s.Close()
	if got, err := s.Get(ctx, Local, "chk_OLD"); err != nil || got.ID != "chk_OLD" {
		t.Errorf("old row = %+v, %v; want it kept as the local user's", got, err)
	}
	if err := s.Save(ctx, "ann", ideacheck.Intake{Idea: "y"}, result("chk_NEW", "build")); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.List(ctx, "ann", 10); len(rows) != 1 {
		t.Errorf("ann rows after migration = %+v", rows)
	}
}

func TestLastOnEmptyStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Last(context.Background(), Local); !errors.Is(err, ErrNotFound) {
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
