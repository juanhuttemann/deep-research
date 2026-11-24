package agent

import (
	"cmp"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Setup is what a run will use, as the browser page shows it. It is read from
// the configuration, not from a connection, so it never spends a request or
// starts Claude Code to find out.
type Setup struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Search   string `json:"search"`
	Pages    string `json:"pages"`
}

// ErrSearchConfig marks a search setting that cannot work as written, for
// callers that do not import tools: it is not the model's key to fix.
var ErrSearchConfig = tools.ErrSearchConfig

// Describe is the run's model, provider, search engine and page source.
func (c Config) Describe() Setup {
	s := Setup{}
	s.Model, s.Provider = c.describeModel()
	s.Search, s.Pages = c.describeSearch()
	return s
}

// describeModel names the provider by host, the way a run's record does
// (ModelInfo), never by the full URL: a base URL can carry credentials.
func (c Config) describeModel() (model, provider string) {
	if c.Provider == "claude" {
		if useClaudeCode(c) {
			// Which of the two answers is decided when a run starts.
			return c.ClaudeModel, "Claude Code, else " + host(c.AnthropicBaseURL)
		}
		return c.ClaudeModel, host(c.AnthropicBaseURL)
	}
	return c.OpenAIModel, host(c.OpenAIBaseURL)
}

// describeSearch names the engine and where each source's text comes from.
// A setting the run would refuse says so here too: the page showed a healthy
// setup for a search_engine it did not know, and every launch then failed.
func (c Config) describeSearch() (search, pages string) {
	o := c.SearchOptions()
	if !c.SearchEnabled() {
		// The model suggests URLs; the scraper fetches them, and
		// with none the built-in reader does.
		const off = "off (model suggests URLs)"
		switch scraper, err := tools.NewScraper(o, 0); {
		case err != nil:
			return off, "not usable: " + err.Error()
		case scraper == nil:
			return off, tools.ScraperDirect
		default:
			return off, shownScraper(c, scraper)
		}
	}
	engine, err := tools.NewEngine(o, 0)
	if err != nil {
		return "not usable: " + err.Error(), "none"
	}
	switch {
	case engine.Name() != tools.EngineSearXNG:
		search = engine.Name()
	case strings.EqualFold(c.SearXNGURL, "auto"):
		search = "searxng (public instances)"
	default:
		search = "searxng (" + hosts(c.SearXNGURL) + ")"
	}
	scraper, err := tools.NewScraper(o, 0)
	switch {
	case err != nil:
		pages = "not usable: " + err.Error()
	case scraper != nil:
		pages = shownScraper(c, scraper)
	case tools.InlineText(engine.Name()):
		pages = "page text included"
	default:
		pages = "snippets only"
	}
	return search, pages
}

// shownScraper is the scraper for display: Firecrawl, which runs anywhere,
// with its address.
func shownScraper(c Config, s tools.Scraper) string {
	if s.Name() == tools.ScraperFirecrawl {
		return "firecrawl (" + host(c.FirecrawlURL) + ")"
	}
	return s.Name()
}

// host is the address's host for display, never its userinfo.
func host(raw string) string {
	return cmp.Or(tools.DomainOf(strings.TrimSpace(raw)), "[unset]")
}

func hosts(list string) string {
	var out []string
	for u := range strings.SplitSeq(list, ",") {
		if strings.TrimSpace(u) != "" {
			out = append(out, host(u))
		}
	}
	return strings.Join(out, ", ")
}
