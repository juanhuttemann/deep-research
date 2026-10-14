package tools

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Engine and scraper names, as the search_engine and scraper settings spell
// them.
const (
	EngineSearXNG    = "searxng"
	EngineExa        = "exa"
	EngineTavily     = "tavily"
	ScraperFirecrawl = "firecrawl"
	ScraperNone      = "none"
)

// Options selects the search engine and the scraper, and carries what each
// needs to reach its service.
type Options struct {
	// Engine is searxng (the default when empty), exa or tavily.
	Engine     string
	SearXNGURL string
	// Scraper is firecrawl, exa, tavily or none. Empty means Firecrawl when
	// FirecrawlURL is set, else none.
	Scraper         string
	FirecrawlURL    string
	FirecrawlAPIKey string
	ExaAPIKey       string
	TavilyAPIKey    string
}

// ErrSearchConfig marks a search setting that cannot work as written: an
// unknown engine or scraper, or one chosen without what it needs. It is the
// reader's search setting to fix, not the model's key.
var ErrSearchConfig = errors.New("search setting")

// errNoKey marks a hosted service chosen without its key. Exa and Tavily
// both answer every request with 401 then, so it is said before any is made.
var errNoKey = errors.New("no API key")

// NewEngine builds the configured search engine. timeout bounds one SearXNG
// request; the hosted engines use their own.
func NewEngine(o Options, timeout time.Duration) (Searcher, error) {
	switch name := strings.ToLower(strings.TrimSpace(o.Engine)); name {
	case "", EngineSearXNG:
		return NewSearXNGClient(o.SearXNGURL, timeout), nil
	case EngineExa:
		if o.ExaAPIKey == "" {
			return nil, fmt.Errorf("%w: search_engine exa: %w: set EXA_API_KEY", ErrSearchConfig, errNoKey)
		}
		return NewExaClient(ExaURL, o.ExaAPIKey, 0), nil
	case EngineTavily:
		if o.TavilyAPIKey == "" {
			return nil, fmt.Errorf("%w: search_engine tavily: %w: set TAVILY_API_KEY", ErrSearchConfig, errNoKey)
		}
		return NewTavilyClient(TavilyURL, o.TavilyAPIKey, 0), nil
	default:
		return nil, fmt.Errorf("%w: search_engine: unknown %q (want searxng, exa, tavily or off)", ErrSearchConfig, name)
	}
}

// NewScraper builds the configured scraper, or nil for none.
func NewScraper(o Options, timeout time.Duration) (Scraper, error) {
	name := strings.ToLower(strings.TrimSpace(o.Scraper))
	if name == "" {
		name = ScraperNone
		if strings.TrimSpace(o.FirecrawlURL) != "" {
			name = ScraperFirecrawl
		}
	}
	switch name {
	case ScraperNone, "off":
		return nil, nil
	case ScraperFirecrawl:
		if strings.TrimSpace(o.FirecrawlURL) == "" {
			return nil, fmt.Errorf("%w: scraper firecrawl: set FIRECRAWL_URL", ErrSearchConfig)
		}
		fc := NewFirecrawlClient(o.FirecrawlURL, timeout)
		fc.APIKey = o.FirecrawlAPIKey
		return fc, nil
	case EngineExa:
		if o.ExaAPIKey == "" {
			return nil, fmt.Errorf("%w: scraper exa: %w: set EXA_API_KEY", ErrSearchConfig, errNoKey)
		}
		return NewExaClient(ExaURL, o.ExaAPIKey, timeout), nil
	case EngineTavily:
		if o.TavilyAPIKey == "" {
			return nil, fmt.Errorf("%w: scraper tavily: %w: set TAVILY_API_KEY", ErrSearchConfig, errNoKey)
		}
		return NewTavilyClient(TavilyURL, o.TavilyAPIKey, timeout), nil
	default:
		return nil, fmt.Errorf("%w: scraper: unknown %q (want firecrawl, exa, tavily or none)", ErrSearchConfig, name)
	}
}

// InlineText reports whether the engine returns each result's page text with
// the search, which leaves a scraper nothing to do but cover a result that
// came back without it.
func InlineText(engine string) bool {
	e := strings.ToLower(strings.TrimSpace(engine))
	return e == EngineExa || e == EngineTavily
}

// NewSearchToolsFrom builds SearchTools from o. SearXNG.URL is one instance,
// a comma-separated list, or "auto" (see NewSearXNGClient). With no scraper
// every result without a Body keeps its search snippet and is labelled
// snippet-only, which is still real search.
func NewSearchToolsFrom(o Options, timeout time.Duration) (*SearchTools, error) {
	engine, err := NewEngine(o, timeout)
	if err != nil {
		return nil, err
	}
	scraper, err := NewScraper(o, scrapeWait)
	if err != nil {
		return nil, err
	}
	return &SearchTools{Engine: engine, Scraper: scraper, MaxURLsPerQuery: 3, ScrapeParallelism: 4}, nil
}
