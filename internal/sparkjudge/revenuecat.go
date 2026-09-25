package sparkjudge

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/morethancoder/ideacheck/server"
)

// RevenueCat webhooks, as documented at
// https://www.revenuecat.com/docs/integrations/webhooks and
// https://www.revenuecat.com/docs/integrations/webhooks/event-types-and-fields
// (read 2026-09-25). The body is {"api_version": "1.0", "event": {...}};
// RevenueCat sends the Authorization header value set in its dashboard
// verbatim, retries until it gets a 200, and reuses the event id on a retry.
type rcBody struct {
	Event rcEvent `json:"event"`
}

type rcEvent struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	AppUserID         string   `json:"app_user_id"`
	OriginalAppUserID string   `json:"original_app_user_id"`
	Aliases           []string `json:"aliases"`
	EntitlementIDs    []string `json:"entitlement_ids"` // may be null
	ProductID         string   `json:"product_id"`
	ExpirationAtMS    *int64   `json:"expiration_at_ms"`
	EventTimestampMS  int64    `json:"event_timestamp_ms"`
	TransferredFrom   []string `json:"transferred_from"`
	TransferredTo     []string `json:"transferred_to"`
	Environment       string   `json:"environment"` // SANDBOX | PRODUCTION
}

// users is everyone the event is about: the app's own id and every alias
// RevenueCat merged with it.
func (e rcEvent) users() []string {
	var out []string
	for _, u := range append([]string{e.AppUserID, e.OriginalAppUserID}, e.Aliases...) {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

func (e rcEvent) at() time.Time { return time.UnixMilli(e.EventTimestampMS).UTC() }

func (s *Service) revenuecat(w http.ResponseWriter, r *http.Request) {
	if s.WebhookAuth == "" {
		server.Fail(w, http.StatusServiceUnavailable, refuse(http.StatusServiceUnavailable, "not_configured", "REVENUECAT_WEBHOOK_AUTH is not set"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(s.WebhookAuth)) != 1 {
		server.Fail(w, http.StatusUnauthorized, refuse(http.StatusUnauthorized, "unauthorized", "wrong webhook authorization"))
		return
	}
	var body rcBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.Config.Limits.MaxBodyBytes)).Decode(&body); err != nil || body.Event.ID == "" {
		server.Fail(w, http.StatusBadRequest, refuse(http.StatusBadRequest, "bad_request", "not a RevenueCat event"))
		return
	}
	ev := body.Event
	changes, err := s.changes(r, ev)
	if err != nil {
		server.Fail(w, http.StatusInternalServerError, err)
		return
	}
	applied, err := s.Accounts.ApplyEvent(r.Context(), ev.ID, changes)
	if err != nil {
		server.Fail(w, http.StatusInternalServerError, err) // RevenueCat retries
		return
	}
	s.Log.Info("revenuecat event", "id", ev.ID, "type", ev.Type, "applied", applied, "changes", len(changes), "environment", ev.Environment)
	server.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "applied": applied})
}

// changes is what an event does to entitlements. Every event is recorded (so
// a retry is recognised); only these change anything:
//
//   - INITIAL_PURCHASE, RENEWAL, UNCANCELLATION: pro until expiration_at_ms.
//   - CANCELLATION: auto-renew is off, or the period was refunded; either way
//     access lasts until expiration_at_ms, which a refund moves to its own time.
//   - EXPIRATION: expiration_at_ms has passed: back to free.
//   - TRANSFER: sent to the destination; the source ids' entitlement moves to
//     transferred_to and the sources lose it.
//
// PRODUCT_CHANGE is recorded only: "the new subscription may not be in effect
// immediately", and the RENEWAL that starts it carries the new expiry. TEST
// and other types change nothing. An event about another entitlement is
// ignored, and an event older than a user's last change never overwrites it.
func (s *Service) changes(r *http.Request, ev rcEvent) ([]Change, error) {
	switch ev.Type {
	case "INITIAL_PURCHASE", "RENEWAL", "UNCANCELLATION", "CANCELLATION", "EXPIRATION":
		if !slices.Contains(ev.EntitlementIDs, s.Config.Entitlement) || ev.ExpirationAtMS == nil {
			return nil, nil
		}
		expires := time.UnixMilli(*ev.ExpirationAtMS).UTC()
		var out []Change
		for _, u := range ev.users() {
			out = append(out, Change{User: u, Expires: expires, Product: ev.ProductID, At: ev.at()})
		}
		return out, nil
	case "TRANSFER":
		var moved Entitlement
		for _, u := range ev.TransferredFrom {
			e, err := s.Accounts.Entitlement(r.Context(), u)
			if err != nil {
				return nil, err
			}
			if e.Expires.After(moved.Expires) {
				moved = e
			}
		}
		if moved.Expires.IsZero() {
			return nil, nil // nothing to move: the destination keeps what it has
		}
		var out []Change
		for _, u := range ev.TransferredFrom {
			out = append(out, Change{User: u, At: ev.at()})
		}
		for _, u := range ev.TransferredTo {
			out = append(out, Change{User: u, Expires: moved.Expires, Product: moved.Product, At: ev.at()})
		}
		return out, nil
	}
	return nil, nil
}
