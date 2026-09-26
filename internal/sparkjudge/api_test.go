package sparkjudge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/server"
	"github.com/morethancoder/ideacheck/store"
)

const (
	ann = "0f4c8a4e-2b1d-4c3e-9a7f-1e2d3c4b5a61"
	bob = "7d9e1f20-3a4b-4c5d-8e6f-a1b2c3d4e5f6"
)

// clock is a settable now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t        *testing.T
	url      string
	svc      *Service
	accounts Accounts
	clock    *clock
}

// newHarness serves the API over the mock judge with the embedded
// sparkjudge.yaml, attest off unless dev is given, and a fixed clock.
func newHarness(t *testing.T, dev *device, with ...func(*Service, *server.Server)) *harness {
	t.Helper()
	files := configs.Defaults()
	raw, _ := files.Read(ConfigFile)
	mode := AttestOff
	if dev != nil {
		mode = AttestRequired
	}
	cfg, err := ParseConfig(raw, func(k string) string {
		return map[string]string{"SPARKJUDGE_ATTEST": mode, "SPARKJUDGE_TEAM_ID": "ABCDE12345"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ideacheck.DefaultSettings("mock", "")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := ideacheck.New(ideacheck.Options{Settings: settings, Files: files, Judge: &mock.Judge{Seed: 1}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	checks, err := store.Open(filepath.Join(dir, "checks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { checks.Close() })
	accounts, err := OpenAccounts(filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.Close() })
	c := &clock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	svc := &Service{Config: cfg, Accounts: accounts, WebhookAuth: "Bearer rc-secret", Now: c.now}
	if dev != nil {
		svc.Verifier = Verifier{Roots: dev.roots, AppID: testAppID}
	}
	api := &server.Server{Engine: engine, Store: checks, Files: files, RubricsDir: settings.RubricsDir}
	for _, f := range with {
		f(svc, api)
	}
	srv := httptest.NewServer(svc.Handler(api))
	t.Cleanup(srv.Close)
	return &harness{t: t, url: srv.URL, svc: svc, accounts: accounts, clock: c}
}

// do sends a request as user (attest off) and decodes a JSON answer into out.
func (h *harness) do(user, method, path, body string, out any) int {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.url+path, strings.NewReader(body))
	if user != "" {
		req.Header.Set(HeaderUser, user)
	}
	return h.send(req, out)
}

func (h *harness) send(req *http.Request, out any) int {
	h.t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	return res.StatusCode
}

func (h *harness) me(user string) Me {
	h.t.Helper()
	var me Me
	if code := h.do(user, http.MethodGet, "/v1/me", "", &me); code != 200 {
		h.t.Fatalf("GET /v1/me = %d", code)
	}
	return me
}

func (h *harness) webhook(event string) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/webhooks/revenuecat", strings.NewReader(`{"api_version":"1.0","event":`+event+`}`))
	req.Header.Set("Authorization", "Bearer rc-secret")
	var out map[string]any
	return h.send(req, &out), out
}

type apiError struct{ Error, Message string }

const idea = `{"idea":"A payroll compliance tool for small bakeries"}`

// The whole free-to-pro path: free checks are counted before they run, the
// fourth is refused with 402, a purchase lifts the limit, and each user sees
// only their own checks.
func TestQuotaFromFreeToPro(t *testing.T) {
	h := newHarness(t, nil)
	if me := h.me(ann); me.Plan != PlanFree || me.Used != 0 || me.Limit != 3 || me.ResetsAt != "2026-10-01T00:00:00Z" {
		t.Errorf("fresh user = %+v", me)
	}
	var first ideacheck.Result
	for i := range 3 {
		var res ideacheck.Result
		if code := h.do(ann, http.MethodPost, "/v1/check?rubric=business", idea, &res); code != 200 || res.Status != ideacheck.StatusOK {
			t.Fatalf("check %d = %d %+v", i+1, code, res)
		}
		if i == 0 {
			first = res
		}
	}
	if me := h.me(ann); me.Used != 3 {
		t.Errorf("after three checks used = %d", me.Used)
	}
	var refused apiError
	if code := h.do(ann, http.MethodPost, "/v1/check", idea, &refused); code != 402 || refused.Error != "quota_exceeded" {
		t.Errorf("fourth free check = %d %+v, want 402 quota_exceeded", code, refused)
	}

	expires := h.clock.now().Add(30 * 24 * time.Hour).UnixMilli()
	code, out := h.webhook(`{"id":"evt-1","type":"INITIAL_PURCHASE","app_user_id":"` + ann + `","entitlement_ids":["pro"],"product_id":"sparkjudge_pro_monthly","expiration_at_ms":` + itoa(expires) + `,"event_timestamp_ms":` + itoa(h.clock.now().UnixMilli()) + `}`)
	if code != 200 || out["applied"] != true {
		t.Fatalf("purchase webhook = %d %v", code, out)
	}
	if me := h.me(ann); me.Plan != PlanPro || me.Limit != 100 || me.Used != 3 || me.ProUntil == "" {
		t.Errorf("after purchase = %+v", me)
	}
	if code := h.do(ann, http.MethodPost, "/v1/check?rubric=business", idea, nil); code != 200 {
		t.Errorf("pro check = %d", code)
	}

	var list struct{ Checks []store.Row }
	if h.do(bob, http.MethodGet, "/v1/checks", "", &list); len(list.Checks) != 0 {
		t.Errorf("bob sees %d of ann's checks", len(list.Checks))
	}
	if h.do(ann, http.MethodGet, "/v1/checks", "", &list); len(list.Checks) != 4 {
		t.Errorf("ann sees %d checks, want 4", len(list.Checks))
	}
	if code := h.do(bob, http.MethodGet, "/v1/checks/"+first.ID, "", nil); code != 404 {
		t.Errorf("bob GET ann's check = %d, want 404", code)
	}

	// The month turns: the allowance comes back.
	h.clock.add(7 * 24 * time.Hour)
	if me := h.me(ann); me.Used != 0 || me.ResetsAt != "2026-11-01T00:00:00Z" {
		t.Errorf("next month = %+v", me)
	}
}

// failing is an engine whose checks never produce a result.
type failing struct{}

func (failing) Check(context.Context, ideacheck.Intake, ideacheck.CheckOptions) (*ideacheck.Result, error) {
	return nil, errors.New("rubric did not load")
}

func TestAFailedCheckIsRefunded(t *testing.T) {
	h := newHarness(t, nil, func(_ *Service, api *server.Server) { api.Engine = failing{} })
	if code := h.do(ann, http.MethodPost, "/v1/check", idea, nil); code != 500 {
		t.Fatalf("failing check = %d", code)
	}
	if me := h.me(ann); me.Used != 0 {
		t.Errorf("used after a failed check = %d, want 0", me.Used)
	}
}

func TestSpendCapAndRateLimits(t *testing.T) {
	h := newHarness(t, nil, func(s *Service, _ *server.Server) {
		s.Config.Limits.ChecksPerHour = 2
		s.Config.Limits.RequestsPerMinute = 8
	})
	var refused apiError
	if err := h.accounts.AddSpend(context.Background(), "2026-09-25", 25); err != nil {
		t.Fatal(err)
	}
	if code := h.do(ann, http.MethodPost, "/v1/check", idea, &refused); code != 503 || refused.Error != "daily_cap" {
		t.Errorf("over the daily cap = %d %+v", code, refused)
	}
	h.clock.add(24 * time.Hour) // a new day, a fresh budget and a full hour's allowance
	for range 2 {
		if code := h.do(ann, http.MethodPost, "/v1/check", idea, nil); code != 200 {
			t.Fatalf("check within the hour's limit = %d", code)
		}
	}
	if code := h.do(ann, http.MethodPost, "/v1/check", idea, &refused); code != 429 || refused.Error != "rate_limited" {
		t.Errorf("third check within the hour = %d %+v", code, refused)
	}
	for i := range 8 {
		if code := h.do(bob, http.MethodGet, "/v1/me", "", nil); code != 200 {
			t.Fatalf("request %d within the minute's limit = %d", i+1, code)
		}
	}
	if code := h.do(bob, http.MethodGet, "/v1/me", "", &refused); code != 429 || refused.Error != "rate_limited" {
		t.Errorf("request past the minute's limit = %d %+v", code, refused)
	}
	if code := h.do(ann, http.MethodGet, "/v1/me", "", nil); code != 200 {
		t.Errorf("ann is limited by bob's requests: %d", code)
	}
	h.clock.add(time.Minute)
	if code := h.do(bob, http.MethodGet, "/v1/me", "", nil); code != 200 {
		t.Errorf("a minute later = %d", code)
	}
}

func TestRequestsWithoutAUserAreRefused(t *testing.T) {
	h := newHarness(t, nil)
	var refused apiError
	for _, user := range []string{"", "not-a-uuid"} {
		if code := h.do(user, http.MethodGet, "/v1/me", "", &refused); code != 401 || refused.Error != "missing_user" {
			t.Errorf("user %q = %d %+v", user, code, refused)
		}
	}
	if code := h.do("", http.MethodGet, "/v1/healthz", "", nil); code != 200 {
		t.Errorf("healthz = %d", code)
	}
	var fields ideacheck.Fields
	if code := h.do(ann, http.MethodGet, "/v1/fields", "", &fields); code != 200 || len(fields.Idea) == 0 {
		t.Errorf("fields = %d", code)
	}
}

// attestKey runs the app's first launch: a challenge, then the attestation.
func (h *harness) attestKey(d *device, user string) (int, apiError) {
	h.t.Helper()
	var ch struct{ Challenge string }
	if code := h.do(user, http.MethodPost, "/v1/attest/challenge", "", &ch); code != 200 {
		h.t.Fatalf("challenge = %d", code)
	}
	challenge, _ := base64.StdEncoding.DecodeString(ch.Challenge)
	body, _ := json.Marshal(attestRequest{KeyID: d.keyIDString(), Attestation: base64.StdEncoding.EncodeToString(d.attest(challenge)), Challenge: ch.Challenge})
	var out apiError
	return h.do(user, http.MethodPost, "/v1/attest", string(body), &out), out
}

func (h *harness) signed(d *device, user, method, path, body string, out any) int {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.url+path, strings.NewReader(body))
	d.sign(req, user, []byte(body))
	return h.send(req, out)
}

// With attest required: a key is attested once, then every request carries an
// assertion over itself, used once.
func TestAttestedKeySignsEveryRequest(t *testing.T) {
	d := newDevice(t)
	h := newHarness(t, d)
	var refused apiError
	if code := h.do(ann, http.MethodGet, "/v1/me", "", &refused); code != 401 || refused.Error != "attestation_required" {
		t.Errorf("unsigned request = %d %+v", code, refused)
	}
	if code, out := h.attestKey(d, ann); code != 201 {
		t.Fatalf("attest = %d %+v", code, out)
	}
	var me Me
	if code := h.signed(d, ann, http.MethodGet, "/v1/me", "", &me); code != 200 || me.User != ann {
		t.Fatalf("signed /v1/me = %d %+v", code, me)
	}
	var res ideacheck.Result
	if code := h.signed(d, ann, http.MethodPost, "/v1/check?rubric=business", idea, &res); code != 200 || res.Status != ideacheck.StatusOK {
		t.Errorf("signed check = %d %+v", code, res.Status)
	}

	// Replaying a request that was already served.
	req, _ := http.NewRequest(http.MethodGet, h.url+"/v1/me", nil)
	d.sign(req, ann, nil)
	replay := req.Clone(context.Background())
	h.send(req, nil)
	if code := h.send(replay, &refused); code != 401 || refused.Error != "stale_assertion" {
		t.Errorf("replayed assertion = %d %+v", code, refused)
	}

	// An assertion lifted onto a different body.
	req, _ = http.NewRequest(http.MethodPost, h.url+"/v1/check", strings.NewReader(`{"idea":"something else"}`))
	d.sign(req, ann, []byte(idea))
	if code := h.send(req, &refused); code != 401 || refused.Error != "bad_assertion" {
		t.Errorf("assertion over another body = %d %+v", code, refused)
	}

	// Ann's key is not bob's.
	if code := h.signed(d, bob, http.MethodGet, "/v1/me", "", &refused); code != 401 || refused.Error != "unknown_key" {
		t.Errorf("ann's key as bob = %d %+v", code, refused)
	}
}

func TestAttestationsThatMustFail(t *testing.T) {
	d := newDevice(t)
	h := newHarness(t, d)

	// A challenge is single-use.
	var ch struct{ Challenge string }
	h.do(ann, http.MethodPost, "/v1/attest/challenge", "", &ch)
	challenge, _ := base64.StdEncoding.DecodeString(ch.Challenge)
	body, _ := json.Marshal(attestRequest{KeyID: d.keyIDString(), Attestation: base64.StdEncoding.EncodeToString(d.attest(challenge)), Challenge: ch.Challenge})
	if code := h.do(ann, http.MethodPost, "/v1/attest", string(body), nil); code != 201 {
		t.Fatalf("first use = %d", code)
	}
	var refused apiError
	if code := h.do(ann, http.MethodPost, "/v1/attest", string(body), &refused); code != 401 || refused.Error != "bad_challenge" {
		t.Errorf("challenge reused = %d %+v", code, refused)
	}

	for name, tamper := range map[string]func(*device){
		"another app":       func(d *device) { d.appID = "ZZZZZ99999.com.example.other" },
		"development build": func(d *device) { d.aaguid = "appattestdevelop" },
		"untrusted root":    func(d *device) { *d = *newDevice(t) },
	} {
		other := newDevice(t)
		tamper(other)
		if name != "untrusted root" {
			other.roots = d.roots // same CA as the server trusts
			other.caKey, other.caCert = d.caKey, d.caCert
		}
		if code, out := h.attestKey(other, bob); code != 401 || out.Error != "bad_attestation" {
			t.Errorf("%s: attest = %d %+v, want 401 bad_attestation", name, code, out)
		}
	}
}

// The root the server trusts is Apple's, by the fingerprint Apple publishes.
func TestEmbeddedRootIsApples(t *testing.T) {
	if got := rootFingerprint(AppleRootCA); got != "1cb9823ba28ba6ad2d33a006941de2ae4f513ef1d4e831b9f7e0fa7b6242c932" {
		t.Errorf("root fingerprint = %s", got)
	}
	AppleRoots() // parses
}

func itoa(n int64) string { return string(bytes.TrimSpace([]byte(jsonNumber(n)))) }

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }
