package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Check is one line of `deep-research doctor`.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// diagnoseTimeout bounds each check: doctor is for finding out quickly.
const diagnoseTimeout = 20 * time.Second

// Diagnose makes one cheap request per dependency a run has — the model
// endpoint, the search backend, the scraper — and says what each answered. A
// run that produced nothing useful otherwise gave no way to tell which one was
// the cause. It spends no model tokens.
func Diagnose(ctx context.Context, cfg Config) []Check {
	return []Check{checkModel(ctx, cfg), checkSearch(ctx, cfg), checkScraper(ctx, cfg)}
}

// checkModel lists the provider's models with the key: it proves the host
// answers and, on providers that check keys there, that the key is accepted.
// OpenRouter lists models for anyone, so there the key endpoint is asked too.
func checkModel(ctx context.Context, cfg Config) Check {
	c := Check{Name: "model"}
	if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		c.Detail = "no API key — get a free one (no card): https://openrouter.ai/keys"
		return c
	}
	base := strings.TrimRight(cfg.OpenAIBaseURL, "/")
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = u.Hostname()
	}
	paths := []string{"/models"}
	if host == "openrouter.ai" {
		paths = append(paths, "/key")
	}
	var tier string
	for _, p := range paths {
		body, err := getJSON(ctx, base+p, cfg.OpenAIAPIKey)
		if err != nil {
			c.Detail = fmt.Sprintf("%s via %s: %v", cfg.OpenAIModel, host, err)
			return c
		}
		if p == "/key" {
			tier = keyTier(body)
		}
	}
	c.OK, c.Detail = true, fmt.Sprintf("%s via %s answers%s", cfg.OpenAIModel, host, tier)
	return c
}

// keyTier summarises OpenRouter's key record.
func keyTier(body []byte) string {
	var k struct {
		Data struct {
			FreeTier bool     `json:"is_free_tier"`
			Usage    float64  `json:"usage"`
			Limit    *float64 `json:"limit"`
			// FreeDaily is the free-model cap, counted in requests. It is
			// absent from records that do not carry it.
			FreeDaily *struct {
				Limit     int `json:"limit"`
				Remaining int `json:"remaining"`
				Used      int `json:"used"`
			} `json:"free_model_daily_requests"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &k) != nil {
		return ""
	}
	s := fmt.Sprintf("; key accepted, usage $%.2f", k.Data.Usage)
	if k.Data.Limit != nil {
		s += fmt.Sprintf(" of $%.2f", *k.Data.Limit)
	}
	if k.Data.FreeTier {
		s += ", free tier (free models are capped per minute and per UTC day)"
	}
	if f := k.Data.FreeDaily; f != nil {
		s += fmt.Sprintf("; free-model requests today: %d of %d used, %d left", f.Used, f.Limit, f.Remaining)
	}
	return s
}

func getJSON(ctx context.Context, u, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, diagnoseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := providerHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d %s", u, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	return raw, nil
}

// checkSearch runs one real query: searx.space's checker is whitelisted by
// most limiters, so only a query from here proves an instance answers this
// client.
func checkSearch(ctx context.Context, cfg Config) Check {
	c := Check{Name: "search"}
	if cfg.SearXNGURL == "" {
		c.OK, c.Detail = true, "off — the model supplies findings from memory; every source is unverified"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 2*diagnoseTimeout)
	defer cancel()
	client := tools.NewSearXNGClient(cfg.SearXNGURL, cfg.ModelCallTimeout)
	res, err := client.Search(ctx, "open source metasearch engine")
	if err != nil {
		c.Detail = fmt.Sprintf("%s: %v", tools.SearchStatus(err), err)
		return c
	}
	c.OK = true
	c.Detail = fmt.Sprintf("%s answered (%d %s)", client.Preferred(), len(res), pick(len(res), "result", "results"))
	if strings.EqualFold(cfg.SearXNGURL, "auto") {
		c.Detail += " — a public instance: every query goes to a third party you did not pick"
	}
	return c
}

// checkScraper scrapes one well-known page through Firecrawl.
func checkScraper(ctx context.Context, cfg Config) Check {
	c := Check{Name: "scraper"}
	if cfg.SearXNGURL == "" {
		c.OK, c.Detail = true, "not used — search is off"
		return c
	}
	if cfg.FirecrawlURL == "" {
		c.OK, c.Detail = true, "none — sources are search snippets, labelled snippet only"
		return c
	}
	fc := tools.NewFirecrawlClient(cfg.FirecrawlURL, diagnoseTimeout)
	fc.APIKey = cfg.FirecrawlAPIKey
	if _, err := fc.ScrapeURL(ctx, "https://example.com"); err != nil {
		c.Detail = fmt.Sprintf("%s: %v", cfg.FirecrawlURL, err)
		return c
	}
	c.OK, c.Detail = true, cfg.FirecrawlURL+" scraped a page"
	return c
}
