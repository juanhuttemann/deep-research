package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// hostedTimeout bounds one request to a hosted search or extract API. A
// search with page text attached is one request for ten pages, and both
// services answer it in a few seconds; an extract that misses their cache
// crawls the page live, which Firecrawl is given a minute for.
const hostedTimeout = scrapeWait

// hostedContentLimit caps a page's text from a hosted engine, as
// FirecrawlClient.ContentLimit caps a scrape and for the same reason: the
// prompts take the passages that match the query, wherever they sit.
const hostedContentLimit = 40000

// hostedBodyLimit bounds a response read. A search that returns ten pages'
// text is well past the 2 MB a single scrape is allowed, and Tavily does not
// cap raw_content on its side.
const hostedBodyLimit = 16 * 1024 * 1024

// Tavily's own statuses for a key out of credits: 432 is the plan's limit,
// 433 the pay-as-you-go cap the account set.
const (
	tavilyPlanLimit = 432
	tavilyKeyLimit  = 433
)

// hostedClient is what Exa and Tavily share: a JSON POST with a key, whose
// failures are classified the same way as SearXNG's and Firecrawl's.
type hostedClient struct {
	service    string
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	setAuth    func(req *http.Request, key string)
}

func newHostedClient(service, baseURL, apiKey string, timeout time.Duration, setAuth func(*http.Request, string)) hostedClient {
	if timeout <= 0 {
		timeout = hostedTimeout
	}
	return hostedClient{
		service: service,
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  strings.TrimSpace(apiKey),
		// No redirect is followed. Go drops Authorization on a redirect to
		// another host but not Exa's x-api-key, and neither API redirects:
		// a 3xx here is a proxy or a portal, and reads as the failure it is.
		HTTPClient: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		setAuth: setAuth,
	}
}

// post sends body to path and decodes the answer into out.
func (c *hostedClient) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("create %s request: %w", c.service, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	c.setAuth(req, c.APIKey)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if reason, ok := DialFailure(err); ok {
			return &unreachableError{service: c.service, url: c.BaseURL, reason: reason, err: err}
		}
		return fmt.Errorf("%s request failed: %w", c.service, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %w", c.service, &httpError{Code: resp.StatusCode, Body: strings.TrimSpace(string(msg)), Keyed: true})
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, hostedBodyLimit)).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", c.service, err)
	}
	return nil
}
