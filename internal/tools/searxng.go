package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
)

// SearxSpaceURL is the public SearXNG instance list "auto" discovers from.
const SearxSpaceURL = "https://searx.space/data/instances.json"

// maxAutoInstances bounds how many discovered instances a query may try.
// Public instances are donated capacity; a query that fails everywhere should
// give up rather than walk the whole list.
const maxAutoInstances = 10

// maxSearchTimeout caps one request to one instance. SearXNG answers within
// its own engine timeouts or not at all, and with several instances to try, a
// dead one must not hold a query for the model call's two minutes.
const maxSearchTimeout = 30 * time.Second

// errChallenge marks an instance that answered with a bot challenge instead
// of results.
var errChallenge = errors.New("bot challenge instead of results")

// errNotJSON marks a JSON request answered with something else.
var errNotJSON = errors.New("response is not JSON")

// SearXNGClient searches one or more SearXNG instances. With several, a query
// goes to the instance that answered last and moves on when one refuses; an
// instance that refuses the JSON API is read through its HTML page instead.
type SearXNGClient struct {
	HTTPClient  *http.Client
	ResultLimit int

	mu        sync.Mutex
	instances []*searxInstance
	// discover fills instances on first use for "auto"; nil once it has run.
	discover    func(ctx context.Context) ([]string, error)
	discoverErr error
}

type searxInstance struct {
	url string
	// html is set once the instance refused format=json. The JSON API is off
	// by default and most public instances never enable it, but their HTML
	// page serves the same results.
	html atomic.Bool
}

// SearXNGResult represents a single search result from SearXNG
type SearXNGResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Engine  string  `json:"engine"`
	Score   float64 `json:"score"`
}

// NewSearXNGClient creates a client for baseURL: one instance, a
// comma-separated list, or "auto" for public instances listed on searx.space.
func NewSearXNGClient(baseURL string, timeout time.Duration) *SearXNGClient {
	if timeout <= 0 || timeout > maxSearchTimeout {
		timeout = maxSearchTimeout
	}
	c := &SearXNGClient{HTTPClient: &http.Client{Timeout: timeout}, ResultLimit: 10}
	if strings.EqualFold(strings.TrimSpace(baseURL), "auto") {
		c.discover = func(ctx context.Context) ([]string, error) {
			return DiscoverInstances(ctx, c.HTTPClient, SearxSpaceURL)
		}
		return c
	}
	for u := range strings.SplitSeq(baseURL, ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			c.instances = append(c.instances, &searxInstance{url: u})
		}
	}
	return c
}

// Search runs query on the first instance that answers.
func (c *SearXNGClient) Search(ctx context.Context, query string) ([]SearXNGResult, error) {
	insts, err := c.candidates(ctx)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, in := range insts {
		res, err := c.searchOne(ctx, in, query)
		if err == nil {
			c.prefer(in)
			return res, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", in.url, err))
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, fmt.Errorf("all %d SearXNG instances failed: %w", len(insts), errors.Join(errs...))
}

// candidates is the instance list in the order to try, discovering it first
// for "auto".
func (c *SearXNGClient) candidates(ctx context.Context) ([]*searxInstance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discover != nil {
		urls, err := c.discover(ctx)
		c.discover, c.discoverErr = nil, err
		for _, u := range urls {
			// Public instances are read through HTML from the start: measured,
			// almost none serve JSON, and asking first doubles every query.
			in := &searxInstance{url: u}
			in.html.Store(true)
			c.instances = append(c.instances, in)
		}
	}
	if c.discoverErr != nil {
		return nil, fmt.Errorf("discover public SearXNG instances: %w", c.discoverErr)
	}
	if len(c.instances) == 0 {
		return nil, errors.New("no SearXNG instance configured")
	}
	return slices.Clone(c.instances), nil
}

// Preferred is the instance the next query goes to first: after a success,
// the one that answered.
func (c *SearXNGClient) Preferred() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.instances) == 0 {
		return ""
	}
	return c.instances[0].url
}

// prefer moves the instance that just answered to the front, so the next
// query skips the ones that refused.
func (c *SearXNGClient) prefer(in *searxInstance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.instances, in); i > 0 {
		c.instances = slices.Insert(slices.Delete(c.instances, i, i+1), 0, in)
	}
}

// searchOne queries one instance, through JSON unless it has refused it.
func (c *SearXNGClient) searchOne(ctx context.Context, in *searxInstance, query string) ([]SearXNGResult, error) {
	if !in.html.Load() {
		res, err := c.searchJSON(ctx, in.url, query)
		if !refusedJSON(err) {
			return res, err
		}
		in.html.Store(true)
	}
	return c.searchHTML(ctx, in.url, query)
}

// refusedJSON reports whether err is an instance declining the JSON API or
// its limiter refusing it, either of which its HTML page may still serve.
func refusedJSON(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.Code == http.StatusForbidden || he.Code == http.StatusTooManyRequests || he.Code == http.StatusTeapot
	}
	return errors.Is(err, errNotJSON)
}

func (c *SearXNGClient) get(ctx context.Context, base, query string, asJSON bool) (*http.Response, error) {
	u := base + "/search?q=" + url.QueryEscape(query) + "&categories=general"
	if asJSON {
		u += "&format=json"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	// An honest, stable User-Agent; the Accept headers are what SearXNG's bot
	// detection checks a browser-like request for.
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if asJSON {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/html")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, &httpError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

func (c *SearXNGClient) searchJSON(ctx context.Context, base, query string) ([]SearXNGResult, error) {
	resp, err := c.get(ctx, base, query, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Some instances ignore format=json and serve their HTML page with 200.
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return nil, errNotJSON
	}
	var data struct {
		Results []SearXNGResult `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	return c.limit(data.Results), nil
}

func (c *SearXNGClient) searchHTML(ctx context.Context, base, query string) ([]SearXNGResult, error) {
	resp, err := c.get(ctx, base, query, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	res, err := parseResultsHTML(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	return c.limit(res), nil
}

func (c *SearXNGClient) limit(res []SearXNGResult) []SearXNGResult {
	if len(res) > c.ResultLimit {
		return res[:c.ResultLimit]
	}
	return res
}

// parseResultsHTML reads SearXNG's own result markup: each article.result
// holds an h3 > a[href] with the title and a p.content with the snippet. A
// page with no results list at all (id="urls") is not a search that found
// nothing — it is a challenge or an error page.
func parseResultsHTML(r io.Reader) ([]SearXNGResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse search page: %w", err)
	}
	var out []SearXNGResult
	found := false
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if attr(n, "id") == "urls" {
			found = true
		}
		if n.Data == "article" && hasClass(n, "result") {
			if res, ok := parseArticle(n); ok {
				out = append(out, res)
			}
		}
	}
	if !found {
		return nil, errChallenge
	}
	return out, nil
}

func parseArticle(article *html.Node) (SearXNGResult, bool) {
	var res SearXNGResult
	for n := range article.Descendants() {
		switch {
		case n.Type != html.ElementNode:
		case n.Data == "a" && res.URL == "" && n.Parent != nil && n.Parent.Data == "h3":
			res.URL, res.Title = attr(n, "href"), text(n)
		case n.Data == "p" && hasClass(n, "content") && res.Content == "":
			res.Content = text(n)
		}
	}
	return res, res.URL != ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attr(n, "class")), class)
}

// text is n's visible text with whitespace collapsed.
func text(n *html.Node) string {
	var sb strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			sb.WriteString(d.Data)
			sb.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// SearchStatus names how a search failed, for the run's events: a limiter or
// a challenge is a state to report, not an info line.
func SearchStatus(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errChallenge) {
		return "challenge"
	}
	var he *httpError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusTooManyRequests:
			return "rate-limited"
		case http.StatusForbidden:
			return "blocked"
		case http.StatusTeapot, http.StatusAccepted:
			return "challenge"
		}
	}
	return "unavailable"
}

// DiscoverInstances reads the searx.space list and returns the instances worth
// a probe: normal network, reachable, no analytics, some search success —
// best first. It is a candidate list, not a working one: searx.space's own
// checker is whitelisted by most limiters, so only a real query proves an
// instance will answer this client.
func DiscoverInstances(ctx context.Context, client *http.Client, listURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{Code: resp.StatusCode}
	}
	var data struct {
		Instances map[string]struct {
			NetworkType string `json:"network_type"`
			Analytics   any    `json:"analytics"`
			HTTP        struct {
				StatusCode int `json:"status_code"`
			} `json:"http"`
			Timing struct {
				Search struct {
					SuccessPercentage float64 `json:"success_percentage"`
				} `json:"search"`
			} `json:"timing"`
		} `json:"instances"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode instance list: %w", err)
	}
	type cand struct {
		url     string
		success float64
	}
	var cands []cand
	for u, in := range data.Instances {
		if in.NetworkType != "normal" || in.HTTP.StatusCode != http.StatusOK ||
			in.Timing.Search.SuccessPercentage <= 0 || (in.Analytics != nil && in.Analytics != false) {
			continue
		}
		cands = append(cands, cand{strings.TrimRight(u, "/"), in.Timing.Search.SuccessPercentage})
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b.success, a.success), strings.Compare(a.url, b.url))
	})
	out := make([]string, 0, min(len(cands), maxAutoInstances))
	for _, c := range cands[:min(len(cands), maxAutoInstances)] {
		out = append(out, c.url)
	}
	if len(out) == 0 {
		return nil, errors.New("searx.space lists no usable instance")
	}
	return out, nil
}
