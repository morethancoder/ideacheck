package sparkjudge

import (
	"context"
	"errors"
	"time"
)

// Accounts is everything the service remembers about its users besides their
// checks: attested keys, the challenges that precede them, entitlements from
// RevenueCat, the monthly ledger and the day's spend. SQLite implements it
// (OpenAccounts); a Postgres behind the same interface can replace it when one
// machine is no longer enough. Every method that decides money or access is a
// single atomic statement or transaction, so two machines could share it.
type Accounts interface {
	// PutChallenge remembers a one-time attestation challenge for user.
	PutChallenge(ctx context.Context, user string, challenge []byte, expires time.Time) error
	// TakeChallenge consumes a challenge: ErrNotFound when it was never
	// issued to user, was already used or has expired.
	TakeChallenge(ctx context.Context, user string, challenge []byte, now time.Time) error

	AddKey(ctx context.Context, k Key) error
	Key(ctx context.Context, id string) (Key, error)
	// Advance moves a key's counter to counter, only if that is higher than
	// the stored one; ErrStale otherwise (a replayed or overtaken assertion).
	Advance(ctx context.Context, id string, counter uint32) error

	Entitlement(ctx context.Context, user string) (Entitlement, error)
	// ApplyEvent records a webhook event and its changes together, once: a
	// second delivery of the same event id changes nothing and returns false.
	// A change older than the user's last applied change is skipped.
	ApplyEvent(ctx context.Context, eventID string, changes []Change) (bool, error)

	// Reserve takes one check from user's allowance for month, only while
	// fewer than limit are used; false when none is left.
	Reserve(ctx context.Context, user, month string, limit int) (bool, error)
	Refund(ctx context.Context, user, month string) error
	Used(ctx context.Context, user, month string) (int, error)

	AddSpend(ctx context.Context, day string, usd float64) error
	Spend(ctx context.Context, day string) (float64, error)

	Close() error
}

var (
	ErrNotFound = errors.New("not found")
	ErrStale    = errors.New("assertion counter did not increase")
)

// Key is an App Attest key: attested once, then used to sign every request.
type Key struct {
	ID        string // base64 of the key identifier the app got from DCAppAttestService
	User      string
	PublicKey []byte // uncompressed P-256 point
	Counter   uint32
	Receipt   []byte // Apple's receipt, kept for a later fraud-risk query
	Env       string // production | development
	CreatedAt time.Time
}

// Entitlement is what RevenueCat last said about a user's pro access.
type Entitlement struct {
	Expires time.Time // zero = never had one
	Product string
}

// Active reports whether the entitlement grants pro at now.
func (e Entitlement) Active(now time.Time) bool { return e.Expires.After(now) }

// Change sets one user's entitlement, as of the event's own time.
type Change struct {
	User    string
	Expires time.Time
	Product string
	At      time.Time // the event's timestamp: older changes never overwrite newer
}
