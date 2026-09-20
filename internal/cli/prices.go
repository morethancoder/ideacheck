package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/morethancoder/ideacheck/internal/config"
)

const pricesTimeout = 5 * time.Second

// livePrices reads a public model list in OpenRouter's shape (verified
// 2026-09-19 on https://openrouter.ai/api/v1/models, no key needed): each
// model's pricing is USD per token as decimal strings. Keys are normalized
// with priceKey, both as "vendor/model" and as "model".
func livePrices(ctx context.Context, url string) (map[string]config.Price, error) {
	ctx, cancel := context.WithTimeout(ctx, pricesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read prices at %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read prices at %s: HTTP %d", url, res.StatusCode)
	}
	var doc struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				CacheRead  string `json:"input_cache_read"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("read prices at %s: %w", url, err)
	}
	prices := map[string]config.Price{}
	for _, m := range doc.Data {
		// ":batch", ":free" and the like are variants at other rates; "~vendor/x-latest" are moving aliases.
		if strings.Contains(m.ID, ":") || strings.HasPrefix(m.ID, "~") {
			continue
		}
		in, errIn := strconv.ParseFloat(m.Pricing.Prompt, 64)
		out, errOut := strconv.ParseFloat(m.Pricing.Completion, 64)
		if errIn != nil || errOut != nil || in < 0 || out < 0 { // OpenRouter's router models price at -1
			continue
		}
		p := config.Price{In: in * 1e6, Out: out * 1e6}
		if c, err := strconv.ParseFloat(m.Pricing.CacheRead, 64); err == nil {
			p.CachedIn = c * 1e6
		}
		prices[priceKey(m.ID)] = p
		if _, short, ok := strings.Cut(m.ID, "/"); ok {
			if _, taken := prices[priceKey(short)]; !taken {
				prices[priceKey(short)] = p
			}
		}
	}
	return prices, nil
}

// priceKey matches our ids to OpenRouter's, which write a version with dots
// ("claude-haiku-4.5") where Anthropic's ids use dashes ("claude-haiku-4-5").
func priceKey(id string) string { return strings.ReplaceAll(id, ".", "-") }

// modelPrice looks a model up in the live list (exact, unprefixed, or dated
// id — the same rules as the pricing table), then in the pricing table.
func modelPrice(cfg config.Config, live map[string]config.Price, model string) (config.Price, bool) {
	if p, ok := (config.Config{Pricing: live}).PriceFor(priceKey(model)); ok {
		return p, true
	}
	return cfg.PriceFor(model)
}
