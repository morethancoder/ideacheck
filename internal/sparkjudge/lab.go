package sparkjudge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/rubric"
	"github.com/morethancoder/ideacheck/server"
)

// LabConfig is sparkjudge.yaml's lab: section: the routes around checks that
// are not checks, POST /v1/mix and GET /v1/trends.
type LabConfig struct {
	Mix MixConfig `yaml:"mix"`
	// Trends names the trends file (trends.yaml), read like any config file.
	Trends string `yaml:"trends"`
}

// MixConfig is who may mix on the server, how often, and what a mix returns.
type MixConfig struct {
	Plans    []string `yaml:"plans"`
	PerHour  int      `yaml:"per_hour"`
	MaxIdeas int      `yaml:"max_ideas"`
	// Fields are fields.yaml idea fields: the keys of the writer's reply and of
	// the draft the app makes from it.
	Fields []string `yaml:"fields"`
}

// The mix prompt: the writer's system prompt and the template it reads.
const (
	mixSystemFile = "mix_system.md"
	mixFile       = "mix.tmpl"
)

// Lab serves the Lab routes. It holds the engine's two roles: the judge types
// trend items (a typed judgment, the router's own question) and the writer
// mixes ideas and writes the day's sparks (free text). Load it before serving.
type Lab struct {
	Judge judge.Judge
	// Writer must be a judge.Extractor to mix or write sparks; without one,
	// mixing answers 503 and trends come without sparks.
	Writer   judge.Judge
	Files    rubric.Reader
	Settings ideacheck.Settings // the engine's: rubric and prompt dirs, timeouts, retries
	// Search is the research search service, for trends sources of kind
	// search; nil skips them.
	Search ideacheck.Searcher
	// Cache keeps the day's trends (the checks database); nil builds them on
	// every request, which only a test wants.
	Cache  ideacheck.ResearchCache
	Client *http.Client        // fetches trends sources; nil = one with the source timeout
	Getenv func(string) string // for a source's token_env; nil = no tokens

	config      LabConfig
	trends      TrendsConfig
	fields      []ideacheck.Field // the mix's output fields, described
	known       []string          // every idea field, in fields.yaml's order
	mixSystem   string
	mix         *template.Template
	sparkSystem string
	spark       *template.Template
	mixes       *limiter
	building    sync.Mutex
}

// Load reads what the routes need from the config files and checks it, so a
// bad lab: section stops the server at start rather than failing a request.
func (l *Lab) Load(c LabConfig) error {
	m := c.Mix
	if m.PerHour < 1 || m.MaxIdeas < 2 {
		return fmt.Errorf("lab.mix: per_hour must be positive and max_ideas at least 2")
	}
	for _, p := range m.Plans {
		if p != PlanFree && p != PlanPro {
			return fmt.Errorf("lab.mix.plans: %q is not one of free, pro", p)
		}
	}
	catalogue, err := ideacheck.LoadFields(l.Files)
	if err != nil {
		return err
	}
	l.fields, l.known = nil, nil
	for _, f := range catalogue.Idea {
		l.known = append(l.known, f.Name)
	}
	for _, name := range m.Fields {
		i := slices.IndexFunc(catalogue.Idea, func(f ideacheck.Field) bool { return f.Name == name })
		if i < 0 {
			return fmt.Errorf("lab.mix.fields: %q is not an idea field in %s", name, ideacheck.FieldsFile)
		}
		l.fields = append(l.fields, catalogue.Idea[i])
	}
	if len(l.fields) == 0 {
		return fmt.Errorf("lab.mix.fields names no field")
	}
	sys, err := l.Files.Read(path.Join(l.Settings.PromptsDir, mixSystemFile))
	if err != nil {
		return fmt.Errorf("prompt %s: %w", mixSystemFile, err)
	}
	l.mixSystem = string(bytes.TrimSpace(sys))
	if l.mix, err = loadTemplate(l.Files, l.Settings.PromptsDir, mixFile); err != nil {
		return err
	}
	if l.trends, err = LoadTrends(l.Files, c.Trends); err != nil {
		return err
	}
	l.config = c
	return l.loadSpark()
}

func loadTemplate(files rubric.Reader, dir, name string) (*template.Template, error) {
	b, err := files.Read(path.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", name, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("prompt %s: %w", name, err)
	}
	return t, nil
}

// mountLab adds the Lab routes behind the service's Auth.
func (s *Service) mountLab(mux *http.ServeMux) {
	s.Lab.mixes = newLimiter(s.Lab.config.Mix.PerHour, time.Hour, s.now)
	mux.HandleFunc("POST /v1/mix", s.mix)
	mux.HandleFunc("GET /v1/trends", s.trends)
}

// MixIdea is one idea going into a mix, as the app has it.
type MixIdea struct {
	Title  string            `json:"title,omitempty"`
	Idea   string            `json:"idea"`
	Fields map[string]string `json:"fields,omitempty"`
}

// MixRequest is POST /v1/mix's body.
type MixRequest struct {
	Ideas []MixIdea `json:"ideas"`
}

// Mix is the new idea: lab.mix.fields, filled by the writer.
type Mix struct {
	Fields  map[string]string `json:"fields"`
	Model   string            `json:"model,omitempty"`
	CostUSD float64           `json:"cost_usd,omitempty"`
}

func (s *Service) mix(w http.ResponseWriter, r *http.Request) {
	l := s.Lab
	var req MixRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.Config.Limits.MaxBodyBytes)).Decode(&req); err != nil {
		server.Fail(w, http.StatusBadRequest, refuse(http.StatusBadRequest, "bad_request", "the body is not a mix request: "+err.Error()))
		return
	}
	if err := l.validMix(req); err != nil {
		server.Fail(w, http.StatusBadRequest, refuse(http.StatusBadRequest, "bad_request", err.Error()))
		return
	}
	user := server.Tenant(r.Context())
	plan, _, err := s.plan(r.Context(), user)
	if err != nil {
		server.Fail(w, http.StatusInternalServerError, err)
		return
	}
	if !slices.Contains(l.config.Mix.Plans, plan) {
		server.Fail(w, http.StatusPaymentRequired, refuse(http.StatusPaymentRequired, "pro_required", "mixing on the server is part of Pro; the app can mix on your phone"))
		return
	}
	if !l.mixes.allow(user) {
		server.Fail(w, http.StatusTooManyRequests, refuse(http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("at most %d mixes an hour; try again later", l.config.Mix.PerHour)))
		return
	}
	if err := s.underCap(r.Context()); err != nil {
		server.Fail(w, http.StatusServiceUnavailable, err)
		return
	}
	m, err := l.Mix(r.Context(), req)
	if m.CostUSD > 0 {
		if serr := s.Accounts.AddSpend(context.WithoutCancel(r.Context()), day(s.now()), m.CostUSD); serr != nil {
			s.Log.Error("spend not recorded", "error", serr, "usd", m.CostUSD)
		}
	}
	switch {
	case errors.Is(err, errNoWriter):
		server.Fail(w, http.StatusServiceUnavailable, refuse(http.StatusServiceUnavailable, "no_writer", err.Error()))
	case err != nil:
		s.Log.Error("mix failed", "error", err, "user", user)
		server.Fail(w, http.StatusBadGateway, refuse(http.StatusBadGateway, "writer_failed", "the writer could not mix these ideas; try again"))
	default:
		server.WriteJSON(w, http.StatusOK, m)
	}
}

// underCap refuses once the day's spend has reached the cap, as checks are.
func (s *Service) underCap(ctx context.Context) error {
	spent, err := s.Accounts.Spend(ctx, day(s.now()))
	if err != nil {
		return err
	}
	if spent >= s.Config.Limits.DailySpendUSD {
		return refuse(http.StatusServiceUnavailable, "daily_cap", "the server is paused until 00:00 UTC; try again then")
	}
	return nil
}

func (l *Lab) validMix(req MixRequest) error {
	n := len(req.Ideas)
	if n < 2 || n > l.config.Mix.MaxIdeas {
		return fmt.Errorf("mix 2 to %d ideas, not %d", l.config.Mix.MaxIdeas, n)
	}
	for i, idea := range req.Ideas {
		if strings.TrimSpace(idea.Idea) == "" && strings.TrimSpace(idea.Title) == "" {
			return fmt.Errorf("idea %d has neither text nor a title", i+1)
		}
	}
	return nil
}

var errNoWriter = errors.New("this server's writer cannot write a mix")

// Mix asks the writer for one idea combining req's. It is a single Extract
// call: the fields to fill are lab.mix.fields, described by fields.yaml.
func (l *Lab) Mix(ctx context.Context, req MixRequest) (Mix, error) {
	writer, ok := l.Writer.(judge.Extractor)
	if !ok {
		return Mix{}, errNoWriter
	}
	user, err := l.mixPrompt(req)
	if err != nil {
		return Mix{}, err
	}
	names := make([]string, len(l.fields))
	for i, f := range l.fields {
		names[i] = f.Name
	}
	if t := l.Settings.Timeouts.Batch; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	out, err := writer.Extract(ctx, l.mixSystem, user, names)
	m := Mix{Fields: map[string]string{}, Model: out.Model, CostUSD: out.CostUSD}
	if err != nil {
		return m, err
	}
	for _, name := range names {
		if v := strings.TrimSpace(out.Values[name]); v != "" {
			m.Fields[name] = v
		}
	}
	if len(m.Fields) == 0 {
		return m, errors.New("the writer returned an empty mix")
	}
	return m, nil
}

// mixPrompt renders mix.tmpl. Only fields.yaml's idea fields are passed on,
// in its order, so the prompt is the same whatever order the app sent.
func (l *Lab) mixPrompt(req MixRequest) (string, error) {
	type value struct{ Name, Value string }
	type idea struct {
		N           int
		Title, Idea string
		Fields      []value
	}
	data := struct {
		Ideas  []idea
		Fields []ideacheck.Field
	}{Fields: l.fields}
	for i, in := range req.Ideas {
		it := idea{N: i + 1, Title: strings.TrimSpace(in.Title), Idea: strings.TrimSpace(in.Idea)}
		for _, name := range l.known {
			if v := strings.TrimSpace(in.Fields[name]); v != "" && name != "title" {
				it.Fields = append(it.Fields, value{name, v})
			}
		}
		data.Ideas = append(data.Ideas, it)
	}
	var b strings.Builder
	if err := l.mix.Execute(&b, data); err != nil {
		return "", fmt.Errorf("prompt %s: %w", mixFile, err)
	}
	return b.String(), nil
}
