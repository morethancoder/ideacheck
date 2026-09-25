package sparkjudge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/server"
)

const timeFormat = time.RFC3339

// Service is the hosted API's own part: identity, plans and quotas.
type Service struct {
	Config   Config
	Accounts Accounts
	Verifier Verifier
	// WebhookAuth is the Authorization header RevenueCat sends with every
	// webhook (REVENUECAT_WEBHOOK_AUTH); "" refuses every webhook.
	WebhookAuth string
	Log         *slog.Logger     // nil = discard
	Now         func() time.Time // nil = time.Now

	requests, checks *limiter
}

// Init prepares the rate limiters; call it once before serving.
func (s *Service) Init() {
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	l := s.Config.Limits
	s.requests = newLimiter(l.RequestsPerMinute, time.Minute, s.now)
	s.checks = newLimiter(l.ChecksPerHour, time.Hour, s.now)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func month(t time.Time) string { return t.Format("2006-01") }
func day(t time.Time) string   { return t.Format("2006-01-02") }

// resets is when the month after t begins, UTC.
func resets(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// plan is the user's plan now, and what RevenueCat last said about them.
func (s *Service) plan(ctx context.Context, user string) (string, Entitlement, error) {
	e, err := s.Accounts.Entitlement(ctx, user)
	if err != nil {
		return "", e, err
	}
	if e.Active(s.now()) {
		return PlanPro, e, nil
	}
	return PlanFree, e, nil
}

// Meter (server.Server.Meter) takes one check from the user's month before any
// model runs, and gives it back when the check produced no score: an error
// before a result, or a result with status error. Its cost counts toward the
// day's spend either way.
func (s *Service) Meter(ctx context.Context, user string) (func(*ideacheck.Result, error), error) {
	now := s.now()
	if !s.checks.allow(user) {
		return nil, refuse(http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("at most %d checks an hour; try again later", s.Config.Limits.ChecksPerHour))
	}
	spent, err := s.Accounts.Spend(ctx, day(now))
	if err != nil {
		return nil, err
	}
	if spent >= s.Config.Limits.DailySpendUSD {
		s.Log.Error("daily spend cap reached; refusing checks until midnight UTC", "spent_usd", spent)
		return nil, refuse(http.StatusServiceUnavailable, "daily_cap", "hosted checks are paused until 00:00 UTC; try again then")
	}
	plan, _, err := s.plan(ctx, user)
	if err != nil {
		return nil, err
	}
	m := month(now)
	ok, err := s.Accounts.Reserve(ctx, user, m, s.Config.Plans[plan].ChecksPerMonth)
	if err != nil {
		return nil, err
	}
	if !ok {
		msg := "no hosted checks left this month; upgrade to Pro for more"
		if plan == PlanPro {
			msg = "no hosted checks left this month; they come back on " + resets(now).Format("January 2")
		}
		return nil, refuse(http.StatusPaymentRequired, "quota_exceeded", msg)
	}
	return func(res *ideacheck.Result, err error) {
		// The request may be gone (an async check outlives it): the books are
		// kept regardless.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if res != nil && res.Cost.USD > 0 {
			if serr := s.Accounts.AddSpend(ctx, day(s.now()), res.Cost.USD); serr != nil {
				s.Log.Error("spend not recorded", "error", serr, "usd", res.Cost.USD)
			}
		}
		if err != nil || res == nil || res.Status == ideacheck.StatusError {
			if rerr := s.Accounts.Refund(ctx, user, m); rerr != nil {
				s.Log.Error("check not refunded", "error", rerr, "user", user)
			}
		}
	}, nil
}

// Me is what GET /v1/me answers: the plan and how much of it is left.
type Me struct {
	User     string `json:"user"`
	Plan     string `json:"plan"`
	Used     int    `json:"used"`
	Limit    int    `json:"limit"`
	ResetsAt string `json:"resets_at"`
	// ProUntil is when the Pro entitlement ends (or ended); absent when the
	// user never had one. It renews while the subscription does.
	ProUntil string `json:"pro_until,omitempty"`
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	user := server.Tenant(r.Context())
	plan, e, err := s.plan(r.Context(), user)
	if err != nil {
		server.Fail(w, http.StatusInternalServerError, err)
		return
	}
	now := s.now()
	used, err := s.Accounts.Used(r.Context(), user, month(now))
	if err != nil {
		server.Fail(w, http.StatusInternalServerError, err)
		return
	}
	me := Me{User: user, Plan: plan, Used: used, Limit: s.Config.Plans[plan].ChecksPerMonth, ResetsAt: resets(now).Format(timeFormat)}
	if !e.Expires.IsZero() {
		me.ProUntil = e.Expires.Format(timeFormat)
	}
	server.WriteJSON(w, http.StatusOK, me)
}

// limiter is a token bucket per key: capacity tokens, refilled evenly over
// per. It lives in memory, so it limits one machine; the monthly ledger and
// the spend cap are the limits that hold across restarts.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	per      time.Duration
	now      func() time.Time
	buckets  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// maxBuckets bounds memory: past it, buckets that have refilled are dropped
// (a full bucket is the same as none).
const maxBuckets = 100_000

func newLimiter(capacity int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{capacity: float64(capacity), per: per, now: now, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.prune(now)
		}
		b = &bucket{tokens: l.capacity, at: now}
		l.buckets[key] = b
	}
	b.tokens = l.refilled(b, now)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) refilled(b *bucket, now time.Time) float64 {
	return min(l.capacity, b.tokens+now.Sub(b.at).Seconds()*l.capacity/l.per.Seconds())
}

func (l *limiter) prune(now time.Time) {
	for k, b := range l.buckets {
		if l.refilled(b, now) >= l.capacity {
			delete(l.buckets, k)
		}
	}
}
