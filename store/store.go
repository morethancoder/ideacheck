// Package store persists every check to SQLite (pure Go, no cgo) so results can
// be listed, re-shown and compared later.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/morethancoder/ideacheck/ideacheck"
)

const schema = `
CREATE TABLE IF NOT EXISTS checks (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  id          TEXT NOT NULL UNIQUE,
  owner       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  status      TEXT NOT NULL,
  verdict     TEXT NOT NULL DEFAULT '',
  composite   REAL NOT NULL DEFAULT 0,
  backend     TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  rubric      TEXT NOT NULL DEFAULT '',
  rubric_hash TEXT NOT NULL DEFAULT '',
  config_hash TEXT NOT NULL,
  idea        TEXT NOT NULL,
  intake      TEXT NOT NULL,
  result      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS research (
  key        TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  findings   TEXT NOT NULL
);`

// migrations bring a database written by an older ideacheck up to schema,
// each guarded by the column it adds so it runs once.
var migrations = []struct{ table, column, ddl string }{
	{"checks", "owner", "ALTER TABLE checks ADD COLUMN owner TEXT NOT NULL DEFAULT ''"},
}

// indexes come after the migrations, since they may name a migrated column.
const indexes = `CREATE INDEX IF NOT EXISTS checks_owner ON checks (owner, seq);`

var ErrNotFound = errors.New("no such check")

// Local owns every check made on this machine: the CLI's history and the local
// API have one user. A hosted API passes each caller's own id instead.
const Local = ""

type Store struct{ db *sql.DB }

// Row is one line of `ideacheck history`.
type Row struct {
	Seq       int64   `json:"seq"`
	ID        string  `json:"id"`
	CreatedAt string  `json:"created_at"`
	Status    string  `json:"status"`
	Verdict   string  `json:"verdict"`
	Composite float64 `json:"composite"`
	Backend   string  `json:"backend"`
	Model     string  `json:"model"`
	Rubric    string  `json:"rubric"`
	Idea      string  `json:"idea"`
}

// ExpandHome resolves a leading "~/" against home.
func ExpandHome(path, home string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// Open creates the database (and its directory) when needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("history store: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("history store: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("history store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, m := range migrations {
		has, err := hasColumn(db, m.table, m.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("add %s.%s: %w", m.table, m.column, err)
			}
		}
	}
	_, err := db.Exec(indexes)
	return err
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// Save records the full result plus the intake that produced it, as owner's.
func (s *Store) Save(ctx context.Context, owner string, in ideacheck.Intake, res *ideacheck.Result) error {
	result, err := json.Marshal(res)
	if err != nil {
		return err
	}
	intake, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var rubric, rubricHash string
	if res.Rubric != nil {
		rubric, rubricHash = res.Rubric.Name, res.Rubric.Hash
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO checks
		(id, owner, created_at, status, verdict, composite, backend, model, rubric, rubric_hash, config_hash, idea, intake, result)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		res.ID, owner, res.CreatedAt, res.Status, res.Verdict, res.Composite, res.Backend, res.Model,
		rubric, rubricHash, res.ConfigHash, summary(in), string(intake), string(result))
	return err
}

// Findings and KeepFindings hold what a web search found
// for an idea, kept so the same idea is not searched (and paid for, and scored
// slightly differently) on every check.
func (s *Store) Findings(ctx context.Context, key string, maxAge time.Duration) ([]byte, bool) {
	var raw string
	var created int64
	err := s.db.QueryRowContext(ctx, "SELECT findings, created_at FROM research WHERE key = ?", key).Scan(&raw, &created)
	if err != nil || time.Since(time.Unix(created, 0)) > maxAge {
		return nil, false
	}
	return []byte(raw), true
}

func (s *Store) KeepFindings(ctx context.Context, key string, findings []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO research (key, created_at, findings) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET created_at = excluded.created_at, findings = excluded.findings`,
		key, time.Now().Unix(), string(findings))
	return err
}

// FindingsCache is an ideacheck.ResearchCache over the history database at
// Path. It opens the store per call, as the CLI saves a check: a check holds
// no database handle while a model is thinking, and a store that cannot be
// opened only costs a search.
type FindingsCache struct {
	Path string
}

func (c FindingsCache) Get(ctx context.Context, key string, maxAge time.Duration) ([]byte, bool) {
	s, err := Open(c.Path)
	if err != nil {
		return nil, false
	}
	defer s.Close()
	return s.Findings(ctx, key, maxAge)
}

func (c FindingsCache) Put(ctx context.Context, key string, findings []byte) error {
	s, err := Open(c.Path)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.KeepFindings(ctx, key, findings)
}

var _ ideacheck.ResearchCache = FindingsCache{}

// summary is the one-line idea shown in history.
func summary(in ideacheck.Intake) string {
	text := in.Fields["title"]
	if text == "" {
		text = in.Idea
	}
	return strings.Join(strings.Fields(text), " ")
}

// Get finds one of owner's checks by sequence number ("12") or id
// ("chk_01J…"). Another owner's check is ErrNotFound, never a different error:
// a caller learns nothing about ids that are not theirs.
func (s *Store) Get(ctx context.Context, owner, ref string) (*ideacheck.Result, error) {
	where, arg := "id = ?", any(ref)
	if seq, err := strconv.ParseInt(ref, 10, 64); err == nil {
		where, arg = "seq = ?", seq
	}
	return s.one(ctx, "SELECT result FROM checks WHERE owner = ? AND "+where, owner, arg)
}

// Last returns owner's most recent check.
func (s *Store) Last(ctx context.Context, owner string) (*ideacheck.Result, error) {
	return s.one(ctx, "SELECT result FROM checks WHERE owner = ? ORDER BY seq DESC LIMIT 1", owner)
}

func (s *Store) one(ctx context.Context, query string, args ...any) (*ideacheck.Result, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var res ideacheck.Result
	return &res, json.Unmarshal([]byte(raw), &res)
}

// List returns owner's checks, newest first.
func (s *Store) List(ctx context.Context, owner string, limit int) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, id, created_at, status, verdict, composite, backend, model, rubric, idea
		FROM checks WHERE owner = ? ORDER BY seq DESC LIMIT ?`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Seq, &r.ID, &r.CreatedAt, &r.Status, &r.Verdict, &r.Composite, &r.Backend, &r.Model, &r.Rubric, &r.Idea); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
