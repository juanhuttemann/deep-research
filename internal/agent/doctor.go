package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	aoption "github.com/anthropics/anthropic-sdk-go/option"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Check is one line of `deep-research doctor`.
type Check struct {
	Name   string
	OK     bool
	Detail string
	// Warn marks a check that passed on a setup that costs the run quality,
	// such as no scraper: nothing to fail on, but worth fixing.
	Warn bool
	// Fix is the next step when the check failed or warned: the setting to
	// change or the command to run. Before it, a failed search printed what
	// broke and left the reader to work out the way out.
	Fix string
}

// ServicesDoc is where running SearXNG and Firecrawl is explained.
const ServicesDoc = tools.ServicesDoc

// diagnoseTimeout bounds each check: doctor is for finding out quickly.
const diagnoseTimeout = 20 * time.Second

// Diagnose makes one cheap request per dependency a run has — the model
// endpoint, the search backend, the scraper — and says what each answered. A
// run that produced nothing useful otherwise gave no way to tell which one was
// the cause. It spends no model tokens.
func Diagnose(ctx context.Context, cfg Config) []Check {
	return []Check{checkModel(ctx, cfg), checkSearch(ctx, cfg), checkScraper(ctx, cfg)}
}

// DiagnoseDomains runs a run's source-list preflight on its own: one line per
// domain, saying whether a run could search and read it. It spends no model
// tokens, so a list can be proved before any run is started on it.
func DiagnoseDomains(ctx context.Context, cfg Config, domains []string) []Check {
	if !cfg.SearchEnabled() {
		return []Check{{Name: "domains", Detail: ErrNoScopedSearch.Error() + ", and search is off"}}
	}
	st, err := tools.NewSearchToolsFrom(cfg.SearchOptions(), cfg.ModelCallTimeout)
	if err != nil {
		return []Check{{Name: "domains", Detail: err.Error()}}
	}
	var out []Check
	for _, c := range st.CheckDomains(ctx, domains) {
		out = append(out, Check{Name: "domain", OK: c.Usable(), Detail: c.Domain + "  " + c.Line()})
	}
	return out
}

// checkModel lists the provider's models with the key: it proves the host
// answers and, on providers that check keys there, that the key is accepted.
// OpenRouter lists models for anyone, so there the key endpoint is asked too.
func checkModel(ctx context.Context, cfg Config) Check {
	if cfg.Provider == "claude" {
		return checkClaude(ctx, cfg)
	}
	c := Check{Name: "model"}
	if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		c.Detail = "no API key — run `deep-research login`, or get a free one (no card): https://openrouter.ai/keys"
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
	c.OK, c.Detail = true, fmt.Sprintf("%s via %s answers%s%s", cfg.OpenAIModel, host, tier, keyFrom(cfg))
	return c
}

// checkClaude reports the transport a run on provider claude would use: the
// Claude Code subscription when signed in, else the key, which it checks by
// looking the model up. That proves the key and the model name together, and
// is free.
func checkClaude(ctx context.Context, cfg Config) Check {
	c := Check{Name: "model"}
	if !useClaudeCode(cfg) {
		// Chosen over the subscription, or not Anthropic at all: the key is
		// what a run would use, so the key is what to check.
	} else if _, plan := claudeCodeLogin(ctx); plan != "" {
		c.OK, c.Detail = true, fmt.Sprintf("%s via Claude Code (%s subscription)", cfg.ClaudeModel, plan)
		return c
	}
	key := strings.TrimSpace(cfg.AnthropicAPIKey)
	if key == "" {
		c.Detail = "no API key — run `deep-research login`, or set ANTHROPIC_API_KEY"
		if useClaudeCode(cfg) {
			c.Detail = "Claude Code is not signed in to a subscription (run `claude auth login`)," +
				" and there is no API key (`deep-research login`, or ANTHROPIC_API_KEY)"
		}
		return c
	}
	host := cfg.AnthropicBaseURL
	if u, err := url.Parse(host); err == nil {
		host = u.Hostname()
	}
	ctx, cancel := context.WithTimeout(ctx, diagnoseTimeout)
	defer cancel()
	client := anthropic.NewClient(aoption.WithAPIKey(key), aoption.WithBaseURL(cfg.AnthropicBaseURL),
		aoption.WithHTTPClient(providerHTTPClient()), aoption.WithMaxRetries(0))
	if err := lookUpClaudeModel(ctx, client, cfg.ClaudeModel); err != nil {
		// The SDK's error text is the URL and the raw body; the status says it.
		if status, _ := apiResponse(err); status != 0 {
			err = fmt.Errorf("%d %s", status, http.StatusText(status))
		}
		c.Detail = fmt.Sprintf("%s via %s: %v", cfg.ClaudeModel, host, endpointError(cfg.AnthropicBaseURL, err))
		return c
	}
	c.OK, c.Detail = true, fmt.Sprintf("%s via %s answers%s", cfg.ClaudeModel, host, keyFrom(cfg))
	return c
}

// lookUpClaudeModel asks for the model by ID. Anthropic-compatible providers
// need not serve that route — Kimi answers 404 there, while its model list
// and its Messages endpoint work — so a 404 falls back to the list, which
// still proves the endpoint and the key.
func lookUpClaudeModel(ctx context.Context, client anthropic.Client, model string) error {
	_, err := client.Models.Get(ctx, model, anthropic.ModelGetParams{})
	if status, _ := apiResponse(err); status != http.StatusNotFound {
		return err
	}
	_, err = client.Models.List(ctx, anthropic.ModelListParams{})
	return err
}

// keyFrom names where the key came from, for doctor's line.
func keyFrom(cfg Config) string {
	if cfg.KeySource == "" {
		return ""
	}
	return " (key from " + cfg.KeySource + ")"
}

// CheckModel is doctor's model check alone: one free request that proves the
// endpoint answers and accepts the key. `deep-research login` runs it on a
// pasted key before saving it.
func CheckModel(ctx context.Context, cfg Config) Check { return checkModel(ctx, cfg) }

// ListModels returns the model IDs an OpenAI-compatible endpoint serves, from
// its /models list: the choices for a custom endpoint, which no catalog knows.
func ListModels(ctx context.Context, baseURL, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, diagnoseTimeout)
	defer cancel()
	body, err := getJSON(ctx, strings.TrimRight(baseURL, "/")+"/models", key)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("unreadable model list: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
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
		return nil, endpointError(u, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := providerHTTPClient().Do(req)
	if err != nil {
		return nil, endpointError(u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d %s", safeEndpoint(u), resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("GET %s: %w", safeEndpoint(u), endpointError(u, err))
	}
	return raw, nil
}

// checkSearch runs one real query: searx.space's checker is whitelisted by
// most limiters, so only a query from here proves an instance answers this
// client, and only a query proves a hosted engine takes the key.
func checkSearch(ctx context.Context, cfg Config) Check {
	c := Check{Name: "search"}
	if !cfg.SearchEnabled() {
		c.OK, c.Detail = true, "off — the model supplies findings from memory; every source is unverified"
		return c
	}
	engine, err := tools.NewEngine(cfg.SearchOptions(), cfg.ModelCallTimeout)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 2*diagnoseTimeout)
	defer cancel()
	res, err := engine.Search(ctx, "open source metasearch engine")
	if err != nil {
		c.Detail = fmt.Sprintf("%s: %v", tools.SearchStatus(err), err)
		c.Fix = searchFix(err)
		return c
	}
	c.OK = true
	c.Detail = fmt.Sprintf("%s answered (%d %s)", searchEndpoint(engine), len(res), pick(len(res), "result", "results"))
	if len(res) == 0 {
		// Nothing for a query every index has pages on is a backend that
		// answers and does not search; it passed as ✓ and the run then
		// found nothing.
		c.Warn = true
		c.Fix = "a search for a common subject came back empty, so a run may find nothing; " + tools.SearchFix
	}
	switch {
	case strings.EqualFold(cfg.SearXNGURL, "auto") && !tools.InlineText(cfg.SearchEngine):
		c.Detail += " — a public instance: every query goes to a third party you did not pick"
	case tools.InlineText(cfg.SearchEngine):
		c.Detail += " — a hosted service: every query goes to it and spends credit"
	}
	return c
}

// searchFix is the next step after a failed search check, by how it failed.
// It matches the advice a run gives after the same failure.
func searchFix(err error) string {
	switch status := tools.SearchStatus(err); {
	case tools.PublicSearchRefused(err):
		return "public instances refuse automated queries; retrying will not help. " + upperFirst(tools.SearchFix)
	case status == "unreachable" && tools.UnreachableService(err) != "SearXNG":
		return "check this machine's network: the hosted engine did not answer"
	case status == "unreachable":
		return "check that SEARXNG_URL is right and that SearXNG is running there"
	case status == "unauthorized" || status == "quota":
		return "check the engine's API key (EXA_API_KEY or TAVILY_API_KEY) and its remaining credit"
	}
	return ""
}

// searchEndpoint names who answered: the SearXNG instance, or the hosted API.
func searchEndpoint(e tools.Searcher) string {
	switch e := e.(type) {
	case *tools.SearXNGClient:
		return e.Preferred()
	case *tools.ExaClient:
		return e.BaseURL
	case *tools.TavilyClient:
		return e.BaseURL
	}
	return "search"
}

// checkScraper reads one well-known page through the configured scraper.
func checkScraper(ctx context.Context, cfg Config) Check {
	c := Check{Name: "scraper"}
	if !cfg.SearchEnabled() {
		c.OK, c.Detail = true, "not used — search is off"
		return c
	}
	scraper, err := tools.NewScraper(cfg.SearchOptions(), diagnoseTimeout)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	if scraper == nil {
		c.OK = true
		if tools.InlineText(cfg.SearchEngine) {
			c.Detail = "none needed — " + strings.ToLower(cfg.SearchEngine) + " returns page text with each result"
			return c
		}
		// Not a failure, but every claim is then checked against a search
		// snippet of a page, never the page: the default setup said ✓ for
		// the weakest evidence a run can have.
		c.Warn = true
		c.Detail = "none — sources are search snippets only, never the full page"
		c.Fix = "for full page text set FIRECRAWL_URL (" + ServicesDoc + ")," +
			" or DEEP_RESEARCH_SCRAPER=exa with EXA_API_KEY, or tavily with TAVILY_API_KEY"
		return c
	}
	where := scraperName(cfg, scraper)
	if _, err := scraper.ScrapeURL(ctx, "https://example.com"); err != nil {
		c.Detail = fmt.Sprintf("%s: %v", where, err)
		return c
	}
	c.OK, c.Detail = true, where+" scraped a page"
	return c
}

// scraperName is the scraper's name, or for Firecrawl, which runs anywhere,
// its address.
func scraperName(cfg Config, s tools.Scraper) string {
	if s.Name() == tools.ScraperFirecrawl {
		return cfg.FirecrawlURL
	}
	return s.Name()
}
