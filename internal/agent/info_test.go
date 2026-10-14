package agent

import "testing"

// The page names the model by provider host and the search by engine and page
// source; a base URL's credentials never reach it.
func TestDescribe(t *testing.T) {
	cases := []struct {
		cfg  Config
		want Setup
	}{
		{Config{OpenAIModel: "openrouter/free", OpenAIBaseURL: "https://u:p@openrouter.ai/api/v1", SearXNGURL: "auto"},
			Setup{"openrouter/free", "openrouter.ai", "searxng (public instances)", "snippets only"}},
		{Config{OpenAIModel: "m", OpenAIBaseURL: "http://localhost:8000/v1", SearXNGURL: "http://a:8888, http://b",
			FirecrawlURL: "http://localhost:3002"},
			Setup{"m", "localhost:8000", "searxng (a:8888, b)", "firecrawl (localhost:3002)"}},
		{Config{Provider: "claude", ClaudeModel: "claude-opus-5-5", AnthropicBaseURL: "https://api.anthropic.com",
			SearchEngine: "exa", ExaAPIKey: "k"},
			Setup{"claude-opus-5-5", "Claude Code, else api.anthropic.com", "exa", "page text included"}},
		{Config{Provider: "claude", ClaudeAPIOnly: true, ClaudeModel: "c", AnthropicBaseURL: "https://api.anthropic.com",
			SearchEngine: "tavily", TavilyAPIKey: "k", Scraper: "firecrawl", FirecrawlURL: "https://api.firecrawl.dev"},
			Setup{"c", "api.anthropic.com", "tavily", "firecrawl (api.firecrawl.dev)"}},
		{Config{OpenAIModel: "m", OpenAIBaseURL: "https://x.example", SearchEngine: "searxng", SearXNGURL: "auto", Scraper: "exa", ExaAPIKey: "k"},
			Setup{"m", "x.example", "searxng (public instances)", "exa"}},
		{Config{OpenAIModel: "m", OpenAIBaseURL: "https://x.example", SearchEngine: "off", SearXNGURL: "auto"},
			Setup{"m", "x.example", "off (model memory, unverified)", "none"}},
		{Config{OpenAIModel: "m", OpenAIBaseURL: "https://x.example", SearchEngine: "bing"},
			Setup{"m", "x.example", `not usable: search setting: search_engine: unknown "bing" (want searxng, exa, tavily or off)`, "none"}},
		{Config{OpenAIModel: "m", OpenAIBaseURL: "https://x.example", SearXNGURL: "auto", Scraper: "firecrawl"},
			Setup{"m", "x.example", "searxng (public instances)", "not usable: search setting: scraper firecrawl: set FIRECRAWL_URL"}},
	}
	for i, c := range cases {
		if got := c.cfg.Describe(); got != c.want {
			t.Errorf("case %d: got %+v, want %+v", i, got, c.want)
		}
	}
}
