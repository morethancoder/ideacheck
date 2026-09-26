package sparkjudge

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func event(id, typ, user string, expires, at time.Time, extra string) string {
	return fmt.Sprintf(`{"id":%q,"type":%q,"app_user_id":%q,"entitlement_ids":["pro"],"product_id":"pro_monthly","expiration_at_ms":%d,"event_timestamp_ms":%d%s}`,
		id, typ, user, expires.UnixMilli(), at.UnixMilli(), extra)
}

func TestWebhookLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	now := h.clock.now()
	month := now.Add(30 * 24 * time.Hour)

	if code, _ := h.webhook(event("e1", "INITIAL_PURCHASE", ann, month, now, "")); code != 200 || h.me(ann).Plan != PlanPro {
		t.Fatalf("purchase = %d, plan %s", code, h.me(ann).Plan)
	}
	// RevenueCat retries with the same id: recorded once.
	if _, out := h.webhook(event("e1", "INITIAL_PURCHASE", ann, month, now, "")); out["applied"] != false {
		t.Errorf("retry applied = %v, want false", out["applied"])
	}
	// Cancelling keeps access until the period ends.
	h.webhook(event("e2", "CANCELLATION", ann, month, now.Add(time.Minute), ""))
	if h.me(ann).Plan != PlanPro {
		t.Error("a cancelled subscription lost access before it expired")
	}
	// An older event arriving late never undoes a newer one.
	h.webhook(event("e0", "EXPIRATION", ann, now.Add(-time.Hour), now.Add(-time.Hour), ""))
	if h.me(ann).Plan != PlanPro {
		t.Error("a stale EXPIRATION overwrote a newer purchase")
	}
	// An event about another entitlement changes nothing.
	h.webhook(strings.Replace(event("e3", "EXPIRATION", ann, now, now.Add(2*time.Minute), ""), `["pro"]`, `["stickers"]`, 1))
	if h.me(ann).Plan != PlanPro {
		t.Error("another entitlement's expiry ended pro")
	}
	// Expiration ends it.
	h.webhook(event("e4", "EXPIRATION", ann, now.Add(-time.Second), now.Add(3*time.Minute), ""))
	if me := h.me(ann); me.Plan != PlanFree || me.Limit != 3 || me.ProUntil == "" {
		t.Errorf("after expiration = %+v", me)
	}
	// A lapsed user resubscribes.
	h.webhook(event("e5", "RENEWAL", ann, month, now.Add(4*time.Minute), ""))
	if h.me(ann).Plan != PlanPro {
		t.Error("renewal did not restore pro")
	}
	// A restore on a new install moves the subscription to the new id.
	h.webhook(fmt.Sprintf(`{"id":"e6","type":"TRANSFER","transferred_from":[%q],"transferred_to":[%q],"event_timestamp_ms":%d}`, ann, bob, now.Add(5*time.Minute).UnixMilli()))
	if h.me(ann).Plan != PlanFree || h.me(bob).Plan != PlanPro {
		t.Errorf("after transfer: ann %s, bob %s", h.me(ann).Plan, h.me(bob).Plan)
	}
}

func TestWebhookRefusesTheWrongSecret(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/webhooks/revenuecat",
		strings.NewReader(`{"event":`+event("x", "INITIAL_PURCHASE", ann, h.clock.now().Add(time.Hour), h.clock.now(), "")+`}`))
	req.Header.Set("Authorization", "Bearer guessed")
	if code := h.send(req, nil); code != 401 || h.me(ann).Plan != PlanFree {
		t.Errorf("wrong secret = %d, plan %s", code, h.me(ann).Plan)
	}
	h.svc.WebhookAuth = ""
	req, _ = http.NewRequest(http.MethodPost, h.url+"/v1/webhooks/revenuecat", strings.NewReader(`{}`))
	if code := h.send(req, nil); code != 503 {
		t.Errorf("no secret configured = %d, want 503", code)
	}
}

// Many checks racing for the last of a month's allowance: exactly the limit
// get through.
func TestReserveNeverOverspends(t *testing.T) {
	acc, err := OpenAccounts(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for range 20 {
		wg.Go(func() {
			ok, err := acc.Reserve(ctx, ann, "2026-09", 5)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if used, _ := acc.Used(ctx, ann, "2026-09"); granted != 5 || used != 5 {
		t.Errorf("granted %d, used %d; want 5 and 5", granted, used)
	}
	if ok, _ := acc.Reserve(ctx, ann, "2026-09", 0); ok {
		t.Error("a zero limit granted a check")
	}
}
