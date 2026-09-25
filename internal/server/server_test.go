package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/store"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	files := config.NewFiles("")
	cfg, err := config.Load(files, config.LoadOptions{Environ: func() []string { return nil }, Overrides: map[string]any{"backend": "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Engine: &pipeline.Engine{Config: cfg, Files: files, Judge: &mock.Judge{Seed: 1}}, Store: st, Files: files, RubricsDir: cfg.RubricsDir, Log: zerolog.Nop()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, into any) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_ = json.NewDecoder(res.Body).Decode(into)
	return res.StatusCode
}

func TestCheckIsSavedAndRetrievable(t *testing.T) {
	srv := newServer(t)
	res, err := http.Post(srv.URL+"/v1/check?rubric=business", "application/json", strings.NewReader(`{"idea":"A payroll compliance tool"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got pipeline.Result
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if res.StatusCode != 200 || got.Status != pipeline.StatusOK || got.Verdict == "" || got.Rubric.Name != "business" {
		t.Fatalf("POST /v1/check = %d %+v", res.StatusCode, got)
	}
	var again pipeline.Result
	if code := getJSON(t, srv.URL+"/v1/checks/"+got.ID, &again); code != 200 || again.Composite != got.Composite {
		t.Errorf("GET by id = %d %+v", code, again)
	}
	var list struct{ Checks []store.Row }
	if getJSON(t, srv.URL+"/v1/checks", &list); len(list.Checks) != 1 || list.Checks[0].ID != got.ID {
		t.Errorf("list = %+v", list)
	}
	if code := getJSON(t, srv.URL+"/v1/checks/chk_missing", &again); code != 404 {
		t.Errorf("missing id = %d, want 404", code)
	}
}

func TestBadRequests(t *testing.T) {
	srv := newServer(t)
	for name, body := range map[string]string{"empty": "", "unknown field": `{"idea":"x","fields":{"pitch":"y"}}`, "broken json": `{"idea":`} {
		res, _ := http.Post(srv.URL+"/v1/check", "application/json", strings.NewReader(body))
		if res.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", name, res.StatusCode)
		}
		res.Body.Close()
	}
}

// Async check + SSE: a subscriber sees every question start and land, then the result.
func TestEventsStreamProgressThenResult(t *testing.T) {
	srv := newServer(t)
	res, _ := http.Post(srv.URL+"/v1/check?async=1&rubric=creative", "application/json", strings.NewReader(`{"idea":"A puzzle game"}`))
	var accepted struct{ ID, Events string }
	_ = json.NewDecoder(res.Body).Decode(&accepted)
	res.Body.Close()
	if res.StatusCode != 202 || accepted.ID == "" {
		t.Fatalf("async POST = %d %+v", res.StatusCode, accepted)
	}
	stream, err := http.Get(srv.URL + accepted.Events)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if ct := stream.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q", ct)
	}
	counts := map[string]int{}
	var final pipeline.Result
	sc := bufio.NewScanner(stream.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			counts[event]++
			if event == "result" {
				_ = json.Unmarshal([]byte(data), &final)
			}
		}
	}
	// 6 gaps (the rubric is forced, so no router) + 3 of the 4 creative
	// questions that need no profile + the summary, each started + answered;
	// founder context and audience_named are derived, shown answered only.
	if counts["progress"] != 2*(6+3+1)+2 || counts["result"] != 1 || final.ID != accepted.ID || final.Verdict == "" {
		t.Errorf("events = %v final = %+v", counts, final)
	}
	if code := getJSON(t, srv.URL+"/v1/checks/chk_nope/events", &struct{}{}); code != 404 {
		t.Errorf("events for unknown id = %d", code)
	}
}

func TestCORSIsLimitedToLocalhost(t *testing.T) {
	srv := newServer(t)
	for origin, allowed := range map[string]bool{"http://localhost:5173": true, "http://127.0.0.1:3000": true, "https://evil.example": false, "http://localhost.evil.example": false} {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/check", nil)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin") == origin; got != allowed {
			t.Errorf("origin %s allowed = %v, want %v", origin, got, allowed)
		}
	}
}

func TestRubricsAndHealth(t *testing.T) {
	srv := newServer(t)
	var rubrics struct {
		Rubrics []struct {
			Name      string
			Questions []struct{ ID string }
		}
	}
	if code := getJSON(t, srv.URL+"/v1/rubrics", &rubrics); code != 200 || len(rubrics.Rubrics) != 5 || len(rubrics.Rubrics[0].Questions) == 0 {
		t.Errorf("rubrics = %d %+v", code, rubrics)
	}
	var health map[string]string
	if code := getJSON(t, srv.URL+"/v1/healthz", &health); code != 200 || health["status"] != "ok" {
		t.Errorf("healthz = %d %v", code, health)
	}
}
