package sparkjudge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/rubric"
	"github.com/morethancoder/ideacheck/server"
)

// TrendsConfig is trends.yaml: where the day's items come from and how often
// they are rebuilt.
type TrendsConfig struct {
	Refresh      time.Duration `yaml:"refresh"`
	Timeout      time.Duration `yaml:"timeout"`
	MaxItems     int           `yaml:"max_items"`
	JudgeTimeout time.Duration `yaml:"judge_timeout"`
	Sources      []TrendSource `yaml:"sources"`
	Spark        bool          `yaml:"spark"`
}

// TrendSource is one place items come from. Kind says how to read its reply.
type TrendSource struct {
	ID       string        `yaml:"id"`
	Name     string        `yaml:"name"`
	Kind     string        `yaml:"kind"`
	URL      string        `yaml:"url"`
	Since    time.Duration `yaml:"since"`
	Limit    int           `yaml:"limit"`
	Queries  []string      `yaml:"queries"`
	TokenEnv string        `yaml:"token_env"`
	// ItemURL is where an item with no link of its own points (an Ask HN);
	// {id} is the item's id.
	ItemURL string `yaml:"item_url"`
	// StripPrefix is taken off the front of every title ("Show HN: ").
	StripPrefix string `yaml:"strip_prefix"`
}

// Source kinds.
const (
	KindHN     = "hn"
	KindGitHub = "github"
	KindSearch = "search"
)

// LoadTrends reads and checks the trends file.
func LoadTrends(files rubric.Reader, name string) (TrendsConfig, error) {
	var c TrendsConfig
	raw, err := files.Read(name)
	if err != nil {
		return c, fmt.Errorf("lab.trends: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	if c.Refresh <= 0 || c.Timeout <= 0 || c.JudgeTimeout <= 0 || c.MaxItems < 1 {
		return c, fmt.Errorf("%s: refresh, timeout, judge_timeout and max_items must be positive", name)
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		switch {
		case s.ID == "" || seen[s.ID]:
			return c, fmt.Errorf("%s: every source needs its own id (%q)", name, s.ID)
		case s.Limit < 1:
			return c, fmt.Errorf("%s: source %s: limit must be positive", name, s.ID)
		case (s.Kind == KindHN || s.Kind == KindGitHub) && s.URL == "":
			return c, fmt.Errorf("%s: source %s: a %s source needs a url", name, s.ID, s.Kind)
		case s.Kind == KindSearch && len(s.Queries) == 0:
			return c, fmt.Errorf("%s: source %s: a search source needs queries", name, s.ID)
		case s.Kind != KindHN && s.Kind != KindGitHub && s.Kind != KindSearch:
			return c, fmt.Errorf("%s: source %s: kind %q is not one of hn, github, search", name, s.ID, s.Kind)
		}
		seen[s.ID] = true
	}
	return c, nil
}

// TrendItem is one thing launched or talked about, typed into an idea type.
type TrendItem struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Source string `json:"source"` // the source's name
	// IdeaType is the router's choice (rubrics/_router.yaml): other when the
	// judge said so or could not answer.
	IdeaType string `json:"idea_type"`
	Summary  string `json:"summary,omitempty"`
	Points   int    `json:"points,omitempty"` // stars or points, where the source has them
}

// Spark is an inspiration prompt for one idea type.
type Spark struct {
	IdeaType string `json:"idea_type"`
	Prompt   string `json:"prompt"`
}

// TrendsReport is what GET /v1/trends answers.
type TrendsReport struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Items     []TrendItem `json:"items"`
	Sparks    []Spark     `json:"sparks"`
	Warnings  []string    `json:"warnings,omitempty"`
	// Stale is set when the rebuild failed and the last report is served again.
	Stale bool `json:"stale,omitempty"`
}

// The inspiration prompt, written once per build.
const (
	sparkSystemFile = "trends_spark_system.md"
	sparkFile       = "trends_spark.tmpl"
)

// trendsKey is the report's row in the cache.
const trendsKey = "sparkjudge/trends/v1"

// keptFor is how long the cache is asked to hold a report: far past refresh,
// so an old report can still stand in when a rebuild fails.
const keptFor = 30 * 24 * time.Hour

func (l *Lab) loadSpark() error {
	if !l.trends.Spark {
		return nil
	}
	sys, err := l.Files.Read(path.Join(l.Settings.PromptsDir, sparkSystemFile))
	if err != nil {
		return fmt.Errorf("prompt %s: %w", sparkSystemFile, err)
	}
	l.sparkSystem = string(bytes.TrimSpace(sys))
	l.spark, err = loadTemplate(l.Files, l.Settings.PromptsDir, sparkFile)
	return err
}

func (s *Service) trends(w http.ResponseWriter, r *http.Request) {
	report, cost, err := s.Lab.Trends(r.Context(), s.now())
	if cost > 0 {
		if serr := s.Accounts.AddSpend(context.WithoutCancel(r.Context()), day(s.now()), cost); serr != nil {
			s.Log.Error("spend not recorded", "error", serr, "usd", cost)
		}
	}
	if err != nil {
		s.Log.Error("trends not built", "error", err)
		server.Fail(w, http.StatusBadGateway, refuse(http.StatusBadGateway, "trends_unavailable", "no trends could be fetched; try again later"))
		return
	}
	for _, warning := range report.Warnings {
		s.Log.Warn("trends", "warning", warning)
	}
	server.WriteJSON(w, http.StatusOK, report)
}

// Trends is the day's report: the cached one while it is younger than
// refresh, else a new one, built by one request while any others wait for it.
// When a rebuild fails the last report is served again, marked stale. cost is
// what a build spent on models (0 when the cache answered).
func (l *Lab) Trends(ctx context.Context, now time.Time) (*TrendsReport, float64, error) {
	if r := l.cached(ctx); r != nil && now.Sub(r.FetchedAt) < l.trends.Refresh {
		return r, 0, nil
	}
	l.building.Lock()
	defer l.building.Unlock()
	last := l.cached(ctx)
	if last != nil && now.Sub(last.FetchedAt) < l.trends.Refresh {
		return last, 0, nil // built while this request waited
	}
	// A client that gives up must not waste the build others will read.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.trends.Timeout+l.trends.JudgeTimeout+l.Settings.Timeouts.Batch)
	defer cancel()
	r, cost, err := l.build(ctx, now)
	if err != nil {
		if last != nil {
			last.Stale = true
			last.Warnings = append(last.Warnings, "not rebuilt: "+err.Error())
			return last, cost, nil
		}
		return nil, cost, err
	}
	if l.Cache != nil {
		if b, err := json.Marshal(r); err == nil {
			_ = l.Cache.Put(ctx, trendsKey, b)
		}
	}
	return r, cost, nil
}

func (l *Lab) cached(ctx context.Context) *TrendsReport {
	if l.Cache == nil {
		return nil
	}
	b, ok := l.Cache.Get(ctx, trendsKey, keptFor)
	if !ok {
		return nil
	}
	var r TrendsReport
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// build fetches every source at once, keeps the first max_items distinct
// links, has the judge type each, and the writer write the sparks.
func (l *Lab) build(ctx context.Context, now time.Time) (*TrendsReport, float64, error) {
	r := &TrendsReport{FetchedAt: now.UTC(), Items: []TrendItem{}, Sparks: []Spark{}}
	got := make([][]TrendItem, len(l.trends.Sources))
	errs := make([]error, len(l.trends.Sources))
	var wg sync.WaitGroup
	for i, src := range l.trends.Sources {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, l.trends.Timeout)
			defer cancel()
			got[i], errs[i] = l.fetch(ctx, src, now)
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, items := range got {
		if errs[i] != nil && !errors.Is(errs[i], errNoSearch) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", l.trends.Sources[i].Name, errs[i]))
		}
		for _, it := range items {
			if seen[it.URL] || len(r.Items) == l.trends.MaxItems {
				continue
			}
			seen[it.URL] = true
			r.Items = append(r.Items, it)
		}
	}
	if len(r.Items) == 0 {
		return nil, 0, fmt.Errorf("no source returned an item: %w", errors.Join(errs...))
	}
	router, err := rubric.Load(l.Files, l.Settings.RubricsDir, rubric.RouterName)
	if err != nil {
		return nil, 0, err
	}
	q := router.Questions[0]
	cost, failed := l.typeItems(ctx, q, r.Items)
	if failed > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d of %d items could not be typed and are filed under other", failed, len(r.Items)))
	}
	sparks, spent, err := l.sparks(ctx, q, r.Items)
	cost += spent
	if err != nil {
		r.Warnings = append(r.Warnings, "no sparks: "+err.Error())
	}
	r.Sparks = append(r.Sparks, sparks...)
	return r, cost, nil
}

// typeItems asks the router's question about every item at once, one item
// per question so each sees only its own title: the same typed judgment a
// check makes about an idea. It returns what the answers cost and how many
// failed.
func (l *Lab) typeItems(ctx context.Context, q judge.Question, items []TrendItem) (float64, int) {
	ctx, cancel := context.WithTimeout(ctx, l.trends.JudgeTimeout)
	defer cancel()
	answers := make([]judge.Answer, len(items))
	slots := make(chan struct{}, max(l.Settings.MaxConcurrent, 1))
	opts := judge.Options{QuestionTimeout: l.Settings.Timeouts.Question, Retry: l.Settings.Retry}
	var wg sync.WaitGroup
	for i := range items {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			text := items[i].Title
			if items[i].Summary != "" {
				text += ". " + items[i].Summary
			}
			answers[i] = judge.Run(ctx, l.Judge, judge.State{"idea": text}, []judge.Question{q}, opts)[0]
		})
	}
	wg.Wait()
	cost, failed := 0.0, 0
	for i, a := range answers {
		cost += a.CostUSD
		items[i].IdeaType = "other"
		if _, known := q.Options[a.Choice]; a.Failed() || !known {
			failed += btoi(a.Failed())
			continue
		}
		items[i].IdeaType = a.Choice
	}
	return cost, failed
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sparks asks the writer, once, for a prompt per idea type that has items.
func (l *Lab) sparks(ctx context.Context, q judge.Question, items []TrendItem) ([]Spark, float64, error) {
	writer, ok := l.Writer.(judge.Extractor)
	if !l.trends.Spark || !ok {
		return nil, 0, nil
	}
	type group struct {
		Type, Meaning string
		Items         []TrendItem
	}
	byType := map[string]*group{}
	for _, it := range items {
		if it.IdeaType == "other" {
			continue
		}
		g := byType[it.IdeaType]
		if g == nil {
			g = &group{Type: it.IdeaType, Meaning: q.Options[it.IdeaType]}
			byType[it.IdeaType] = g
		}
		g.Items = append(g.Items, it)
	}
	if len(byType) == 0 {
		return nil, 0, nil
	}
	data := struct {
		Groups []*group
		Types  []string
	}{}
	for t := range byType {
		data.Types = append(data.Types, t)
	}
	sort.Strings(data.Types)
	for _, t := range data.Types {
		data.Groups = append(data.Groups, byType[t])
	}
	var user strings.Builder
	if err := l.spark.Execute(&user, data); err != nil {
		return nil, 0, fmt.Errorf("prompt %s: %w", sparkFile, err)
	}
	if t := l.Settings.Timeouts.Batch; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	out, err := writer.Extract(ctx, l.sparkSystem, user.String(), data.Types)
	if err != nil {
		return nil, out.CostUSD, err
	}
	var sparks []Spark
	for _, t := range data.Types {
		if p := strings.TrimSpace(out.Values[t]); p != "" {
			sparks = append(sparks, Spark{IdeaType: t, Prompt: p})
		}
	}
	return sparks, out.CostUSD, nil
}

// fetch reads one source's items, at most its limit.
func (l *Lab) fetch(ctx context.Context, src TrendSource, now time.Time) ([]TrendItem, error) {
	var items []TrendItem
	var err error
	switch src.Kind {
	case KindHN:
		items, err = l.fetchHN(ctx, src, now)
	case KindGitHub:
		items, err = l.fetchGitHub(ctx, src, now)
	case KindSearch:
		items, err = l.fetchSearch(ctx, src)
	}
	var kept []TrendItem
	for _, it := range items {
		it.Title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(it.Title), src.StripPrefix))
		it.Source = src.Name
		if it.Title == "" || !webURL(it.URL) || len(kept) == src.Limit {
			continue
		}
		kept = append(kept, it)
	}
	return kept, err
}

func webURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// expand fills a source url's {since_unix} and {since_date}.
func expand(src TrendSource, now time.Time) string {
	since := now.Add(-src.Since).UTC()
	return strings.NewReplacer("{since_unix}", strconv.FormatInt(since.Unix(), 10), "{since_date}", since.Format("2006-01-02")).Replace(src.URL)
}

func (l *Lab) getJSON(ctx context.Context, src TrendSource, raw string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sparkjudge-api")
	if src.TokenEnv != "" && l.Getenv != nil {
		if token := l.Getenv(src.TokenEnv); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: l.trends.Timeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", req.URL.Host, res.StatusCode)
	}
	return json.Unmarshal(body, into)
}

func (l *Lab) fetchHN(ctx context.Context, src TrendSource, now time.Time) ([]TrendItem, error) {
	var reply struct {
		Hits []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			ObjectID string `json:"objectID"`
			Points   int    `json:"points"`
		} `json:"hits"`
	}
	if err := l.getJSON(ctx, src, expand(src, now), &reply); err != nil {
		return nil, err
	}
	items := make([]TrendItem, 0, len(reply.Hits))
	for _, h := range reply.Hits {
		link := h.URL
		if link == "" && src.ItemURL != "" {
			link = strings.ReplaceAll(src.ItemURL, "{id}", url.QueryEscape(h.ObjectID))
		}
		items = append(items, TrendItem{Title: h.Title, URL: link, Points: h.Points})
	}
	return items, nil
}

func (l *Lab) fetchGitHub(ctx context.Context, src TrendSource, now time.Time) ([]TrendItem, error) {
	var reply struct {
		Items []struct {
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
			Stars       int    `json:"stargazers_count"`
		} `json:"items"`
	}
	if err := l.getJSON(ctx, src, expand(src, now), &reply); err != nil {
		return nil, err
	}
	items := make([]TrendItem, 0, len(reply.Items))
	for _, r := range reply.Items {
		items = append(items, TrendItem{Title: r.FullName, URL: r.HTMLURL, Summary: oneLine(r.Description, 200), Points: r.Stars})
	}
	return items, nil
}

// errNoSearch skips a search source on a server with no search service.
var errNoSearch = errors.New("no search service is configured")

func (l *Lab) fetchSearch(ctx context.Context, src TrendSource) ([]TrendItem, error) {
	if l.Search == nil {
		return nil, errNoSearch
	}
	var items []TrendItem
	var errs []error
	for _, q := range src.Queries {
		results, err := l.Search.Search(ctx, q, src.Limit)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range results {
			items = append(items, TrendItem{Title: r.Title, URL: r.URL, Summary: oneLine(r.Snippet, 200)})
		}
	}
	if len(items) == 0 {
		return nil, errors.Join(errs...)
	}
	return items, nil
}

// oneLine collapses whitespace and cuts s to at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
