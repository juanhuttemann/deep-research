package tools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TavilyURL is Tavily's API.
const TavilyURL = "https://api.tavily.com"

// TavilyClient searches and extracts through Tavily. A search asks for each
// result's page as markdown too, so a run on Tavily needs no scraper.
type TavilyClient struct {
	hostedClient
	ResultLimit  int
	ContentLimit int
}

// NewTavilyClient creates a client for Tavily at baseURL with apiKey.
func NewTavilyClient(baseURL, apiKey string, timeout time.Duration) *TavilyClient {
	return &TavilyClient{
		hostedClient: newHostedClient("Tavily", baseURL, apiKey, timeout, func(r *http.Request, key string) {
			r.Header.Set("Authorization", "Bearer "+key)
		}),
		ResultLimit:  10,
		ContentLimit: hostedContentLimit,
	}
}

// Name is "tavily".
func (c *TavilyClient) Name() string { return EngineTavily }

// Search runs query and returns each result with its page as Body.
func (c *TavilyClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var data struct {
		Results []struct {
			URL        string  `json:"url"`
			Title      string  `json:"title"`
			Content    string  `json:"content"`
			RawContent string  `json:"raw_content"`
			Score      float64 `json:"score"`
		} `json:"results"`
	}
	// Basic depth is one credit a query; advanced costs two for chunks the
	// relevance ranker and ExcerptFor already pick out of the full page.
	err := c.post(ctx, "/search", map[string]any{
		"query":               query,
		"max_results":         c.ResultLimit,
		"search_depth":        "basic",
		"include_raw_content": "markdown",
	}, &data)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(data.Results))
	for _, r := range data.Results {
		out = append(out, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Content: r.Content,
			Engine:  "tavily",
			Score:   r.Score,
			Body:    truncateUTF8(r.RawContent, c.ContentLimit),
		})
	}
	return out, nil
}

// ScrapeURL extracts one page through Tavily's /extract.
func (c *TavilyClient) ScrapeURL(ctx context.Context, fetchURL string) (*ScrapedContent, error) {
	if err := checkScrapeTarget(fetchURL); err != nil {
		return nil, err
	}
	var data struct {
		Results []struct {
			RawContent string `json:"raw_content"`
		} `json:"results"`
		Failed []struct {
			Error string `json:"error"`
		} `json:"failed_results"`
	}
	err := c.post(ctx, "/extract", map[string]any{
		"urls": []string{fetchURL}, "format": "markdown",
	}, &data)
	if err != nil {
		return nil, err
	}
	if len(data.Failed) > 0 {
		return nil, tavilyFailure(data.Failed[0].Error)
	}
	if len(data.Results) == 0 {
		return nil, errEmptyScrape
	}
	return &ScrapedContent{URL: fetchURL, Content: truncateUTF8(data.Results[0].RawContent, c.ContentLimit)}, nil
}

// tavilyFailure turns a failed extract's message into an error. Tavily says
// why in prose ("404 page not found", "Failed to fetch url"); a leading
// status is kept as one, so a missing page reads as a 404 like Firecrawl's.
func tavilyFailure(msg string) error {
	first, _, _ := strings.Cut(strings.TrimSpace(msg), " ")
	if code, err := strconv.Atoi(first); err == nil && code >= http.StatusBadRequest && code < 600 {
		return fmt.Errorf("target page failed: %w", &httpError{Code: code, Body: msg})
	}
	return fmt.Errorf("tavily: %s", msg)
}
