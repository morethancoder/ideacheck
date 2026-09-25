package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
)

const discoverTimeout = 10 * time.Second

// errNoKey is a list that needs the API key nobody has entered yet: setup then
// offers the preset list, quietly, rather than an error before the key step.
var errNoKey = errors.New("needs an API key")

// discoverModels lists the models a provider can actually use right now, for
// the setup wizard. key is the API key being entered (it may not be saved yet).
// A provider without discovery gets its preset list back as given; one with
// discovery gets what the provider lists today, with the preset labels.
func discoverModels(ctx context.Context, p config.Provider, key string) ([]config.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	var ms []config.ModelChoice
	var err error
	switch p.Discover {
	case "":
		return p.Models, nil
	case "codex":
		ms, err = codexModels(ctx)
	case "claude":
		ms, err = claudeModels(ctx, p.Models)
	case "ollama":
		ms, err = ollamaModels(ctx, p.BaseURL, key)
	case "openai":
		ms, err = openAIModels(ctx, cmp.Or(p.DiscoverURL, strings.TrimRight(p.BaseURL, "/")+"/models"), key, p.DiscoverNeeds)
	case "anthropic":
		ms, err = anthropicModels(ctx, cmp.Or(p.DiscoverURL, "https://api.anthropic.com/v1/models?limit=1000"), key)
	case "typesafe":
		ms, err = typesafeModels(ctx, cmp.Or(p.DiscoverURL, strings.TrimRight(p.BaseURL, "/")+"/v1/models"), key)
	case "huggingface":
		ms, err = huggingfaceModels(ctx, p.DiscoverURL)
	default:
		return nil, fmt.Errorf("unknown discover %q", p.Discover)
	}
	if errors.Is(err, errNoKey) {
		return p.Models, nil
	}
	if err != nil {
		return nil, err
	}
	if ms, err = keep(ms, p.DiscoverMatch, p.DiscoverSkip); err != nil {
		return nil, err
	}
	return withPresets(ms, p.Models), nil
}

// keep drops listed ids that do not match, or that match skip.
func keep(ms []config.ModelChoice, match, skip string) ([]config.ModelChoice, error) {
	var in, out *regexp.Regexp
	var err error
	if match != "" {
		if in, err = regexp.Compile(match); err != nil {
			return nil, fmt.Errorf("discover_match: %w", err)
		}
	}
	if skip != "" {
		if out, err = regexp.Compile(skip); err != nil {
			return nil, fmt.Errorf("discover_skip: %w", err)
		}
	}
	return slices.DeleteFunc(ms, func(m config.ModelChoice) bool {
		return (in != nil && !in.MatchString(m.ID)) || (out != nil && out.MatchString(m.ID))
	}), nil
}

// withPresets gives a listed model the preset's label, and its effort
// settings when the list said nothing about efforts. Presets the provider no
// longer lists are not offered.
func withPresets(ms, presets []config.ModelChoice) []config.ModelChoice {
	for i, m := range ms {
		for _, p := range presets {
			if p.ID != m.ID {
				continue
			}
			ms[i].Label = cmp.Or(p.Label, m.Label)
			if len(m.Efforts) == 0 && !m.NoEffort {
				ms[i].Efforts, ms[i].NoEffort = p.Efforts, p.NoEffort
			}
		}
	}
	return ms
}

// getJSON reads a model list. 401/403 without a key is errNoKey.
func getJSON(ctx context.Context, url, key string, header map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, val := range header {
		req.Header.Set(k, val)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("list models at %s: %w", url, err)
	}
	defer res.Body.Close()
	if key == "" && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
		return errNoKey
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("list models at %s: HTTP %d", url, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("list models at %s: %w", url, err)
	}
	return nil
}

func bearer(key string) map[string]string {
	if key == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + key}
}

// newestFirst orders a list by release time, newest on top: the models a
// provider shipped last are the ones worth seeing first.
func newestFirst(ms []config.ModelChoice, at []int64) []config.ModelChoice {
	idx := make([]int, len(ms))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(at[b], at[a]) })
	out := make([]config.ModelChoice, len(ms))
	for i, j := range idx {
		out[i] = ms[j]
	}
	return out
}

// effortRank orders effort names from least to most thinking; a provider may
// list them in any order.
var effortRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func byRank(efforts []string) []string {
	rank := func(e string) int {
		if i := slices.Index(effortRank, e); i >= 0 {
			return i
		}
		return len(effortRank)
	}
	slices.SortStableFunc(efforts, func(a, b string) int { return cmp.Compare(rank(a), rank(b)) })
	return efforts
}

// openAIModels reads an OpenAI-compatible GET /models. OpenRouter's list is
// public and richer: it says which parameters a model takes (needs filters on
// them) and which reasoning efforts it accepts. Variants (":free", ":batch")
// and moving "~" aliases are left out, as in the price list.
func openAIModels(ctx context.Context, url, key string, needs []string) ([]config.ModelChoice, error) {
	var doc struct {
		Data []struct {
			ID        string    `json:"id"`
			Created   int64     `json:"created"`
			Params    *[]string `json:"supported_parameters"`
			Reasoning *struct {
				Efforts []string `json:"supported_efforts"`
			} `json:"reasoning"`
		} `json:"data"`
	}
	if err := getJSON(ctx, url, key, bearer(key), &doc); err != nil {
		return nil, err
	}
	var ms []config.ModelChoice
	var at []int64
	for _, m := range doc.Data {
		if strings.Contains(m.ID, ":") || strings.HasPrefix(m.ID, "~") {
			continue
		}
		c := config.ModelChoice{ID: m.ID, Label: m.ID}
		if m.Params != nil { // OpenRouter's shape: it knows what the model takes
			if slices.ContainsFunc(needs, func(n string) bool { return !slices.Contains(*m.Params, n) }) {
				continue
			}
			if m.Reasoning != nil && len(m.Reasoning.Efforts) > 0 {
				c.Efforts = byRank(m.Reasoning.Efforts)
			} else {
				c.NoEffort = true
			}
		}
		ms, at = append(ms, c), append(at, m.Created)
	}
	return newestFirst(ms, at), nil
}

// anthropicModels reads GET /v1/models, which says per model which effort
// levels it takes.
func anthropicModels(ctx context.Context, url, key string) ([]config.ModelChoice, error) {
	if key == "" {
		return nil, errNoKey
	}
	type level struct {
		Supported bool `json:"supported"`
	}
	var doc struct {
		Data []struct {
			ID           string    `json:"id"`
			CreatedAt    time.Time `json:"created_at"`
			Capabilities struct {
				Effort struct {
					Supported bool  `json:"supported"`
					Low       level `json:"low"`
					Medium    level `json:"medium"`
					High      level `json:"high"`
					Xhigh     level `json:"xhigh"`
					Max       level `json:"max"`
				} `json:"effort"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	header := map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}
	if err := getJSON(ctx, url, key, header, &doc); err != nil {
		return nil, err
	}
	var ms []config.ModelChoice
	var at []int64
	for _, m := range doc.Data {
		c := config.ModelChoice{ID: m.ID, Label: m.ID}
		e := m.Capabilities.Effort
		for name, l := range map[string]level{"low": e.Low, "medium": e.Medium, "high": e.High, "xhigh": e.Xhigh, "max": e.Max} {
			if l.Supported {
				c.Efforts = append(c.Efforts, name)
			}
		}
		c.Efforts = byRank(c.Efforts)
		c.NoEffort = !e.Supported || len(c.Efforts) == 0
		ms, at = append(ms, c), append(at, m.CreatedAt.Unix())
	}
	return newestFirst(ms, at), nil
}

// typesafeModels reads TypeSafe's GET /v1/models (verified 2026-09-25: it
// needs the key and answers {"models":[{name, description, release_date}]}).
func typesafeModels(ctx context.Context, url, key string) ([]config.ModelChoice, error) {
	if key == "" {
		return nil, errNoKey
	}
	var doc struct {
		Models []struct {
			Name     string    `json:"name"`
			Released time.Time `json:"release_date"`
		} `json:"models"`
	}
	if err := getJSON(ctx, url, key, bearer(key), &doc); err != nil {
		return nil, err
	}
	var ms []config.ModelChoice
	var at []int64
	for _, m := range doc.Models {
		ms, at = append(ms, config.ModelChoice{ID: m.Name, Label: m.Name, NoEffort: true}), append(at, m.Released.Unix())
	}
	return newestFirst(ms, at), nil
}

// huggingfaceModels reads a Hub search (GET /api/models?author=…&search=…);
// discover_match picks the checkpoints the backend can load.
func huggingfaceModels(ctx context.Context, url string) ([]config.ModelChoice, error) {
	if url == "" {
		return nil, errors.New("discover: huggingface needs discover_url")
	}
	var doc []struct {
		ID      string    `json:"id"`
		Created time.Time `json:"createdAt"`
	}
	if err := getJSON(ctx, url, "", nil, &doc); err != nil {
		return nil, err
	}
	var ms []config.ModelChoice
	var at []int64
	for _, m := range doc {
		ms, at = append(ms, config.ModelChoice{ID: m.ID, Label: m.ID, NoEffort: true}), append(at, m.Created.Unix())
	}
	return newestFirst(ms, at), nil
}

// claudeModels reads the model aliases the installed claude CLI advertises
// under --model (verified on claude 2.1.282: "an alias for the latest model
// (e.g. 'fable', 'opus', or 'sonnet')"). Each alias resolves to the newest
// model of its family inside the CLI, so a CLI update moves it. The help names
// examples, not every alias, so presets it does not name stay offered after
// the ones it does.
func claudeModels(ctx context.Context, presets []config.ModelChoice) ([]config.ModelChoice, error) {
	out, err := exec.CommandContext(ctx, "claude", "--help").Output()
	if err != nil {
		return nil, fmt.Errorf("claude --help: %w", err)
	}
	aliases := modelAliases(string(out))
	if len(aliases) == 0 {
		return nil, errors.New("claude --help names no model aliases")
	}
	var ms []config.ModelChoice
	for _, a := range aliases {
		ms = append(ms, config.ModelChoice{ID: a, Label: a})
	}
	for _, p := range presets {
		if !slices.Contains(aliases, p.ID) {
			ms = append(ms, p)
		}
	}
	return ms, nil
}

var quoted = regexp.MustCompile(`'([a-z][a-z0-9.-]*)'`)

// modelAliases are the quoted names in the --model option's help that carry no
// version number ('sonnet', not 'claude-fable-5').
func modelAliases(help string) []string {
	var text strings.Builder
	in := false
	for _, line := range strings.Split(help, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--model ") {
			in = true
		} else if in && strings.HasPrefix(t, "-") {
			break
		}
		if in {
			text.WriteString(t + " ")
		}
	}
	var aliases []string
	for _, m := range quoted.FindAllStringSubmatch(text.String(), -1) {
		if !strings.ContainsAny(m[1], "0123456789") && !slices.Contains(aliases, m[1]) {
			aliases = append(aliases, m[1])
		}
	}
	return aliases
}

// withPrices appends each model's input and output price to its label, so
// every model list shows what a choice costs.
func withPrices(billing string, ms []config.ModelChoice, price func(model string) (config.Price, bool)) []config.ModelChoice {
	out := make([]config.ModelChoice, len(ms))
	for i, m := range ms {
		switch p, ok := price(m.ID); {
		case billing == "local":
			m.Label += " · free, runs on this machine"
		case ok:
			m.Label += fmt.Sprintf(" · %s in / %s out per 1M tokens", usd(p.In), usd(p.Out))
		default:
			m.Label += " · price not listed"
		}
		out[i] = m
	}
	return out
}

// usd shows cents, or more digits when the price has them ($0.042).
func usd(v float64) string {
	if math.Abs(v*100-math.Round(v*100)) < 1e-6 {
		return fmt.Sprintf("$%.2f", v)
	}
	return "$" + strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
}

// codexModels reads `codex debug models` (verified on codex-cli 0.153): the
// models this login may pick, each with the reasoning efforts it accepts.
func codexModels(ctx context.Context) ([]config.ModelChoice, error) {
	out, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("codex debug models: %w", err)
	}
	var doc struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("codex debug models: %w", err)
	}
	var ms []config.ModelChoice
	for _, m := range doc.Models {
		if m.Visibility != "list" {
			continue // internal models codex itself hides
		}
		c := config.ModelChoice{ID: m.Slug, Label: m.Slug}
		for _, l := range m.Levels {
			c.Efforts = append(c.Efforts, l.Effort)
		}
		ms = append(ms, c)
	}
	return ms, nil
}

// ollamaModels reads /api/tags. A local server also lists ":cloud" proxies;
// those return no logprobs, so the local (logprob) provider leaves them out.
func ollamaModels(ctx context.Context, baseURL, key string) ([]config.ModelChoice, error) {
	url := ollamaRoot(baseURL) + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models at %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models at %s: HTTP %d", url, res.StatusCode)
	}
	var doc struct {
		Models []struct {
			Name       string `json:"name"`
			RemoteHost string `json:"remote_host"`
			Details    struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("list models at %s: %w", url, err)
	}
	cloud := strings.Contains(baseURL, "ollama.com")
	var ms []config.ModelChoice
	for _, m := range doc.Models {
		if !cloud && m.RemoteHost != "" {
			continue
		}
		label := m.Name
		if m.Details.ParameterSize != "" && !cloud {
			label += " (" + m.Details.ParameterSize + ")"
		}
		ms = append(ms, config.ModelChoice{ID: m.Name, Label: label, NoEffort: true})
	}
	return ms, nil
}
