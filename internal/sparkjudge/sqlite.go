package sparkjudge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const accountsSchema = `
CREATE TABLE IF NOT EXISTS challenges (
  user       TEXT NOT NULL,
  challenge  BLOB NOT NULL,
  expires_ms INTEGER NOT NULL,
  PRIMARY KEY (user, challenge)
);
CREATE TABLE IF NOT EXISTS keys (
  id         TEXT PRIMARY KEY,
  user       TEXT NOT NULL,
  public_key BLOB NOT NULL,
  counter    INTEGER NOT NULL,
  receipt    BLOB NOT NULL,
  env        TEXT NOT NULL,
  created_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS keys_user ON keys (user);
CREATE TABLE IF NOT EXISTS entitlements (
  user       TEXT PRIMARY KEY,
  expires_ms INTEGER NOT NULL,
  product    TEXT NOT NULL,
  updated_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS webhook_events (
  id          TEXT PRIMARY KEY,
  received_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ledger (
  user  TEXT NOT NULL,
  month TEXT NOT NULL,
  used  INTEGER NOT NULL,
  PRIMARY KEY (user, month)
);
CREATE TABLE IF NOT EXISTS spend (
  day TEXT PRIMARY KEY,
  usd REAL NOT NULL
);`

type sqliteAccounts struct{ db *sql.DB }

// OpenAccounts opens (creating when needed) the accounts database at path.
func OpenAccounts(path string) (Accounts, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	if _, err := db.Exec(accountsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("accounts %s: %w", path, err)
	}
	return &sqliteAccounts{db: db}, nil
}

func (s *sqliteAccounts) Close() error { return s.db.Close() }

func (s *sqliteAccounts) PutChallenge(ctx context.Context, user string, challenge []byte, expires time.Time) error {
	// Expired challenges are swept as new ones arrive; nothing else reads them.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM challenges WHERE expires_ms < ?", time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO challenges (user, challenge, expires_ms) VALUES (?,?,?)", user, challenge, expires.UnixMilli())
	return err
}

func (s *sqliteAccounts) TakeChallenge(ctx context.Context, user string, challenge []byte, now time.Time) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM challenges WHERE user = ? AND challenge = ? AND expires_ms > ?", user, challenge, now.UnixMilli())
	return one(res, err, ErrNotFound)
}

func (s *sqliteAccounts) AddKey(ctx context.Context, k Key) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO keys (id, user, public_key, counter, receipt, env, created_ms) VALUES (?,?,?,?,?,?,?)`,
		k.ID, k.User, k.PublicKey, k.Counter, k.Receipt, k.Env, k.CreatedAt.UnixMilli())
	return err
}

func (s *sqliteAccounts) Key(ctx context.Context, id string) (Key, error) {
	var k Key
	var created int64
	err := s.db.QueryRowContext(ctx, "SELECT id, user, public_key, counter, receipt, env, created_ms FROM keys WHERE id = ?", id).
		Scan(&k.ID, &k.User, &k.PublicKey, &k.Counter, &k.Receipt, &k.Env, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	k.CreatedAt = time.UnixMilli(created).UTC()
	return k, err
}

func (s *sqliteAccounts) Advance(ctx context.Context, id string, counter uint32) error {
	res, err := s.db.ExecContext(ctx, "UPDATE keys SET counter = ? WHERE id = ? AND counter < ?", counter, id, counter)
	return one(res, err, ErrStale)
}

func (s *sqliteAccounts) Entitlement(ctx context.Context, user string) (Entitlement, error) {
	var e Entitlement
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT expires_ms, product FROM entitlements WHERE user = ?", user).Scan(&expires, &e.Product)
	if errors.Is(err, sql.ErrNoRows) {
		return Entitlement{}, nil
	}
	if expires > 0 {
		e.Expires = time.UnixMilli(expires).UTC()
	}
	return e, err
}

func (s *sqliteAccounts) ApplyEvent(ctx context.Context, eventID string, changes []Change) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO webhook_events (id, received_ms) VALUES (?,?) ON CONFLICT(id) DO NOTHING", eventID, time.Now().UnixMilli())
	if err := one(res, err, ErrNotFound); errors.Is(err, ErrNotFound) {
		return false, nil // delivered before
	} else if err != nil {
		return false, err
	}
	for _, c := range changes {
		var expires int64
		if !c.Expires.IsZero() {
			expires = c.Expires.UnixMilli()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entitlements (user, expires_ms, product, updated_ms) VALUES (?,?,?,?)
			ON CONFLICT(user) DO UPDATE SET expires_ms = excluded.expires_ms, product = excluded.product, updated_ms = excluded.updated_ms
			WHERE excluded.updated_ms >= entitlements.updated_ms`, c.User, expires, c.Product, c.At.UnixMilli()); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *sqliteAccounts) Reserve(ctx context.Context, user, month string, limit int) (bool, error) {
	if limit < 1 {
		return false, nil
	}
	// One statement: the row is created at 1 or incremented only while under
	// the limit, so two checks racing for the last one cannot both get it.
	res, err := s.db.ExecContext(ctx, `INSERT INTO ledger (user, month, used) VALUES (?,?,1)
		ON CONFLICT(user, month) DO UPDATE SET used = used + 1 WHERE ledger.used < ?`, user, month, limit)
	if err := one(res, err, ErrNotFound); errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (s *sqliteAccounts) Refund(ctx context.Context, user, month string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE ledger SET used = used - 1 WHERE user = ? AND month = ? AND used > 0", user, month)
	return err
}

func (s *sqliteAccounts) Used(ctx context.Context, user, month string) (int, error) {
	var used int
	err := s.db.QueryRowContext(ctx, "SELECT used FROM ledger WHERE user = ? AND month = ?", user, month).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return used, err
}

func (s *sqliteAccounts) AddSpend(ctx context.Context, day string, usd float64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO spend (day, usd) VALUES (?,?)
		ON CONFLICT(day) DO UPDATE SET usd = usd + excluded.usd`, day, usd)
	return err
}

func (s *sqliteAccounts) Spend(ctx context.Context, day string) (float64, error) {
	var usd float64
	err := s.db.QueryRowContext(ctx, "SELECT usd FROM spend WHERE day = ?", day).Scan(&usd)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return usd, err
}

// one turns a statement that changed no row into none.
func one(res sql.Result, err error, none error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return none
	}
	return nil
}
