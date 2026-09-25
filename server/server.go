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

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/rubric"
	"github.com/morethancoder/ideacheck/store"
)

const maxBody = 1 << 20

// Checker is the slice of ideacheck.Engine the server uses.
type Checker interface {
	Check(ctx context.Context, in ideacheck.Intake, o ideacheck.CheckOptions) (*ideacheck.Result, error)
}

// Store keeps finished checks. *store.Store is the local SQLite history; a
// hosted API brings its own. Get returns store.ErrNotFound for an unknown ref.
type Store interface {
	Save(ctx context.Context, in ideacheck.Intake, res *ideacheck.Result) error
	Get(ctx context.Context, ref string) (*ideacheck.Result, error)
	List(ctx context.Context, limit int) ([]store.Row, error)
}

type Server struct {
	Engine     Checker
	Store      Store
	Files      rubric.Reader
	RubricsDir string
	Log        *slog.Logger // nil = discard
	// Auth, when set, admits each request (health checks aside) or refuses it
	// with 401. The tenant it names rides on the request's context (Tenant),
	// so a Store can keep each caller's checks apart. nil = no auth: the local
	// API, which answers only this machine.
	Auth func(*http.Request) (tenant string, err error)

	runs *registry
}

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
	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return cors(s.admit(mux))
}

type tenantKey struct{}

// Tenant is who Auth admitted the request as; "" without Auth.
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

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"status": ideacheck.StatusError, "error": err.Error()})
}

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
	live := s.runs.start(opts.ID)
	if q.Get("async") == "1" {
		// The check outlives this request, so it must not inherit its context.
		go s.execute(context.WithoutCancel(r.Context()), in, opts, live)
		writeJSON(w, http.StatusAccepted, map[string]string{"id": opts.ID, "events": "/v1/checks/" + opts.ID + "/events"})
		return
	}
	res, err := s.execute(r.Context(), in, opts, live)
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
		if serr := s.Store.Save(ctx, in, res); serr != nil {
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
	live, ok := s.runs.get(r.PathValue("id"))
	flusher, canFlush := w.(http.Flusher)
	if !ok || !canFlush {
		fail(w, http.StatusNotFound, fmt.Errorf("no live check %q (finished checks are at /v1/checks/{id})", r.PathValue("id")))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	wake, unsubscribe := live.subscribe()
	defer unsubscribe()
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
	rows, err := s.Store.List(r.Context(), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": rows})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.Get(r.Context(), r.PathValue("id"))
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
