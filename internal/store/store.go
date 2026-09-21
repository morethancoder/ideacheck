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

	"github.com/morethancoder/ideacheck/internal/pipeline"
)

const schema = `
CREATE TABLE IF NOT EXISTS checks (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  id          TEXT NOT NULL UNIQUE,
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

var ErrNotFound = errors.New("no such check")

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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("history store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Save records the full result plus the intake that produced it.
func (s *Store) Save(ctx context.Context, in pipeline.Intake, res *pipeline.Result) error {
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
		(id, created_at, status, verdict, composite, backend, model, rubric, rubric_hash, config_hash, idea, intake, result)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		res.ID, res.CreatedAt, res.Status, res.Verdict, res.Composite, res.Backend, res.Model,
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

// summary is the one-line idea shown in history.
func summary(in pipeline.Intake) string {
	text := in.Fields["title"]
	if text == "" {
		text = in.Idea
	}
	return strings.Join(strings.Fields(text), " ")
}

// Get finds a check by sequence number ("12") or id ("chk_01J…").
func (s *Store) Get(ctx context.Context, ref string) (*pipeline.Result, error) {
	where, arg := "id = ?", any(ref)
	if seq, err := strconv.ParseInt(ref, 10, 64); err == nil {
		where, arg = "seq = ?", seq
	}
	return s.one(ctx, "SELECT result FROM checks WHERE "+where, arg)
}

// Last returns the most recent check.
func (s *Store) Last(ctx context.Context) (*pipeline.Result, error) {
	return s.one(ctx, "SELECT result FROM checks ORDER BY seq DESC LIMIT 1")
}

func (s *Store) one(ctx context.Context, query string, args ...any) (*pipeline.Result, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var res pipeline.Result
	return &res, json.Unmarshal([]byte(raw), &res)
}

// List returns the newest checks first.
func (s *Store) List(ctx context.Context, limit int) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, id, created_at, status, verdict, composite, backend, model, rubric, idea
		FROM checks ORDER BY seq DESC LIMIT ?`, limit)
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
