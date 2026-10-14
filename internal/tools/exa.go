package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ExaURL is Exa's API.
const ExaURL = "https://api.exa.ai"

// ExaClient searches and extracts through Exa. A search asks for each
// result's text as well, so a run on Exa needs no scraper.
type ExaClient struct {
	hostedClient
	ResultLimit  int
	ContentLimit int
}

// NewExaClient creates a client for Exa at baseURL with apiKey.
func NewExaClient(baseURL, apiKey string, timeout time.Duration) *ExaClient {
	return &ExaClient{
		hostedClient: newHostedClient("Exa", baseURL, apiKey, timeout, func(r *http.Request, key string) {
			r.Header.Set("x-api-key", key)
		}),
		ResultLimit:  10,
		ContentLimit: hostedContentLimit,
	}
}

type exaResult struct {
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	Highlights []string `json:"highlights"`
}

// Name is "exa".
func (c *ExaClient) Name() string { return EngineExa }

// Search runs query and returns each result with its text as Body.
func (c *ExaClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var data struct {
		Results []exaResult `json:"results"`
	}
	err := c.post(ctx, "/search", map[string]any{
		"query":      query,
		"type":       "auto",
		"numResults": c.ResultLimit,
		"contents": map[string]any{
			"text":       map[string]any{"maxCharacters": c.ContentLimit},
			"highlights": true,
		},
	}, &data)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(data.Results))
	for _, r := range data.Results {
		out = append(out, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Content: exaSnippet(r),
			Engine:  "exa",
			Body:    truncateUTF8(r.Text, c.ContentLimit),
		})
	}
	return out, nil
}

// exaSnippet is what the relevance ranker scores and what survives as the
// source when the text is empty. Exa's highlights are the passages it
// matched to the query, which is what a search snippet is; a result without
// them falls back to the opening of its text.
func exaSnippet(r exaResult) string {
	if s := strings.TrimSpace(strings.Join(r.Highlights, " … ")); s != "" {
		return s
	}
	return truncateUTF8(strings.TrimSpace(r.Text), 500)
}

// ScrapeURL extracts one page through Exa's /contents.
func (c *ExaClient) ScrapeURL(ctx context.Context, fetchURL string) (*ScrapedContent, error) {
	if err := checkScrapeTarget(fetchURL); err != nil {
		return nil, err
	}
	var data struct {
		Results  []exaResult `json:"results"`
		Statuses []struct {
			Status string `json:"status"`
			Error  struct {
				HTTPStatusCode int    `json:"httpStatusCode"`
				Tag            string `json:"tag"`
			} `json:"error"`
		} `json:"statuses"`
	}
	// "fallback" crawls live only when Exa has no cached copy, which is most
	// of what a search result links to and cheaper than always crawling.
	err := c.post(ctx, "/contents", map[string]any{
		"urls": []string{fetchURL}, "text": true, "livecrawl": "fallback",
	}, &data)
	if err != nil {
		return nil, err
	}
	// The call succeeds while the page fails: the page's own status is in
	// statuses, and a 404 has to read as one rather than as an empty page.
	if len(data.Statuses) > 0 && data.Statuses[0].Status == "error" {
		e := data.Statuses[0].Error
		if e.HTTPStatusCode >= http.StatusBadRequest {
			return nil, fmt.Errorf("target page failed: %w", &httpError{Code: e.HTTPStatusCode, Body: e.Tag})
		}
		return nil, fmt.Errorf("exa could not read %s: %s", fetchURL, e.Tag)
	}
	if len(data.Results) == 0 {
		return nil, errEmptyScrape
	}
	r := data.Results[0]
	return &ScrapedContent{URL: fetchURL, Title: r.Title, Content: truncateUTF8(r.Text, c.ContentLimit)}, nil
}
