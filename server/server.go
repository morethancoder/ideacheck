// Package server is the HTTP API: the local one `ideacheck serve` runs, and the
// base of a hosted one (Store, Auth). It speaks the same JSON contract as
// `ideacheck -o json` (schemas/check_result.schema.json).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/rubric"
	"github.com/morethancoder/ideacheck/store"
)

const maxBody = 1 << 20

// Checker is the slice of ideacheck.Engine the server uses.
type Checker interface {
	Check(ctx context.Context, in ideacheck.Intake, o ideacheck.CheckOptions) (*ideacheck.Result, error)
}

// Store keeps finished checks, each as its owner's: the tenant Auth admitted
// the request as (store.Local without Auth). *store.Store is the SQLite
// history; a hosted API may bring another. Get returns store.ErrNotFound for
// an unknown ref and for another owner's check alike.
type Store interface {
	Save(ctx context.Context, owner string, in ideacheck.Intake, res *ideacheck.Result) error
	Get(ctx context.Context, owner, ref string) (*ideacheck.Result, error)
	List(ctx context.Context, owner string, limit int) ([]store.Row, error)
}

type Server struct {
	Engine     Checker
	Store      Store
	Files      rubric.Reader
	RubricsDir string
	Log        *slog.Logger // nil = discard
	// Auth, when set, admits each request (health checks aside) or refuses it:
	// with the status of an *Error it returns, else 401. The tenant it names
	// rides on the request's context (Tenant) and owns every check the request
	// makes or reads. nil = no auth: the local API, which answers only this
	// machine.
	Auth func(*http.Request) (tenant string, err error)
	// Meter, when set, is asked before a check runs whether the tenant may run
	// it — an error refuses the check, with the status of an *Error — and the
	// settle func it returns is told how the check ended. Quotas live here.
	Meter func(ctx context.Context, tenant string) (settle func(*ideacheck.Result, error), err error)
	// Mount adds a host's own routes to the mux, behind the same Auth.
	Mount func(*http.ServeMux)
	// KeepAlive is how often an event stream with nothing to say sends an SSE
	// comment, so a proxy that drops idle connections (research can be quiet
	// for a minute) keeps it open; 0 = 15s.
	KeepAlive time.Duration

	runs *registry
}

// Error is a refusal with the status and the machine-readable code the client
// gets: {"status":"error","error":code,"message":message}.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Handler builds the routes. Progress streams from GET /v1/checks/{id}/events;
// to get an id before the check ends, POST /v1/check?async=1.
func (s *Server) Handler() http.Handler {
	s.runs = newRegistry()
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/check", s.check)
	mux.HandleFunc("GET /v1/checks", s.list)
	mux.HandleFunc("GET /v1/checks/{id}", s.get)
	mux.HandleFunc("GET /v1/checks/{id}/events", s.events)
	mux.HandleFunc("GET /v1/rubrics", s.rubrics)
	mux.HandleFunc("GET /v1/fields", s.fields)
	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	if s.Mount != nil {
		s.Mount(mux)
	}
	return cors(s.admit(mux))
}

type tenantKey struct{}

// Tenant is who Auth admitted the request as; store.Local without Auth.
func Tenant(ctx context.Context) string {
	t, _ := ctx.Value(tenantKey{}).(string)
	return t
}

// admit runs Auth before every route but the health check.
func (s *Server) admit(next http.Handler) http.Handler {
	if s.Auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		tenant, err := s.Auth(r)
		if err != nil {
			fail(w, http.StatusUnauthorized, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenantKey{}, tenant)))
	})
}

// cors allows browser frontends served from localhost only; this API runs checks
// that cost money, so arbitrary web pages must not be able to call it.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); localOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Fail writes err as the API's error body: an *Error with its own status and
// code, anything else with status and a code named after it.
func Fail(w http.ResponseWriter, status int, err error) {
	code := strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
	var e *Error
	if errors.As(err, &e) {
		status, code = e.Status, e.Code
	}
	writeJSON(w, status, map[string]string{"status": ideacheck.StatusError, "error": code, "message": err.Error()})
}

func fail(w http.ResponseWriter, status int, err error) { Fail(w, status, err) }

// WriteJSON writes v with status, as every route here does.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	in, err := ideacheck.ParseIntake(body)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	// Nothing on this side can answer a follow-up, so missing facts are reported
	// beside the scores rather than instead of them; ?strict=1 asks for the old
	// needs_input behavior.
	opts := ideacheck.CheckOptions{ID: ideacheck.NewID(), Rubric: q.Get("rubric"), Proceed: q.Get("strict") != "1"}
	tenant := Tenant(r.Context())
	settle := func(*ideacheck.Result, error) {}
	if s.Meter != nil {
		if settle, err = s.Meter(r.Context(), tenant); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
	}
	live := s.runs.start(tenant, opts.ID)
	if q.Get("async") == "1" {
		// The check outlives this request, so it must not inherit its context.
		go func() { settle(s.execute(context.WithoutCancel(r.Context()), in, opts, live)) }()
		writeJSON(w, http.StatusAccepted, map[string]string{"id": opts.ID, "events": "/v1/checks/" + opts.ID + "/events"})
		return
	}
	res, err := s.execute(r.Context(), in, opts, live)
	settle(res, err)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// execute runs one check, feeding its events into the run and saving the result.
func (s *Server) execute(ctx context.Context, in ideacheck.Intake, opts ideacheck.CheckOptions, live *run) (*ideacheck.Result, error) {
	opts.OnEvent = live.add
	res, err := s.Engine.Check(ctx, in, opts)
	if err == nil && s.Store != nil {
		if serr := s.Store.Save(ctx, Tenant(ctx), in, res); serr != nil {
			s.Log.Warn("result was not saved to history", "error", serr, "request_id", res.ID)
		}
	}
	if err != nil {
		s.Log.Error("check failed", "error", err, "request_id", opts.ID)
	}
	live.finish(res, err)
	return res, err
}

// events streams progress as Server-Sent Events: one "progress" event per
// question start/answer, then a final "result" (or "error") event.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	live, ok := s.runs.get(Tenant(r.Context()), r.PathValue("id"))
	flusher, canFlush := w.(http.Flusher)
	if !ok || !canFlush {
		fail(w, http.StatusNotFound, fmt.Errorf("no live check %q (finished checks are at /v1/checks/{id})", r.PathValue("id")))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	wake, unsubscribe := live.subscribe()
	defer unsubscribe()
	keepAlive := s.KeepAlive
	if keepAlive <= 0 {
		keepAlive = 15 * time.Second
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	sent := 0
	for {
		batch, res, err, done := live.since(sent)
		for _, e := range batch {
			sse(w, "progress", e)
		}
		sent += len(batch)
		if done {
			if err != nil {
				sse(w, "error", map[string]string{"error": err.Error()})
			} else {
				sse(w, "result", res)
			}
			flusher.Flush()
			return
		}
		flusher.Flush()
		select {
		case <-wake:
		case <-tick.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case <-r.Context().Done():
			return
		}
	}
}

func sse(w io.Writer, event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 500 {
		limit = 50
	}
	rows, err := s.Store.List(r.Context(), Tenant(r.Context()), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": rows})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.Get(r.Context(), Tenant(r.Context()), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) rubrics(w http.ResponseWriter, _ *http.Request) {
	names, err := rubric.Names(s.Files, s.RubricsDir)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]*rubric.Rubric, 0, len(names))
	for _, name := range names {
		rb, err := rubric.Load(s.Files, s.RubricsDir, name)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, rb)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rubrics": out})
}

// fields is the catalogue of what a caller can send about an idea
// (fields.yaml): what a form asks for, in the words it asks with.
func (s *Server) fields(w http.ResponseWriter, _ *http.Request) {
	f, err := ideacheck.LoadFields(s.Files)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}
