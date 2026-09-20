package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
)

const discoverTimeout = 10 * time.Second

// discoverModels lists the models a provider can actually use right now, for
// the setup wizard. key is the API key being entered (it may not be saved yet).
// A provider without discovery gets its preset list back as given.
func discoverModels(ctx context.Context, p config.Provider, key string) ([]config.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	switch p.Discover {
	case "codex":
		return codexModels(ctx)
	case "ollama":
		return ollamaModels(ctx, p.BaseURL, key)
	}
	return p.Models, nil
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
