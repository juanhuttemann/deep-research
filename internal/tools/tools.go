package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// FirecrawlClient wraps the Firecrawl API for webpage scraping
type FirecrawlClient struct {
	BaseURL    string
	HTTPClient *http.Client
	// APIKey authenticates against hosted Firecrawl, which rejects an
	// unauthenticated scrape with 401. A self-hosted instance needs no key, so
	// an empty value sends no Authorization header at all.
	APIKey       string
	ContentLimit int
}

// scrapeTimeout is how long Firecrawl is given for one page, said in each
// request so that it gives up first and says so. Not 30 s: Firecrawl's
// default proxy mode reads exactly 30000 as "unset" and raises it to two
// minutes. 60 s is what it allows a page when told nothing.
const scrapeTimeout = 60 * time.Second

// scrapeWait bounds one scrape request from this side, for a Firecrawl that
// accepts the connection and never answers. A run's scraper used to get the
// model-call timeout instead, two minutes by default and more when raised
// for a slow model, and held each stalled scrape that long.
const scrapeWait = scrapeTimeout + 10*time.Second

// Name is "firecrawl".
func (c *FirecrawlClient) Name() string { return ScraperFirecrawl }

// NewFirecrawlClient creates a new Firecrawl client
func NewFirecrawlClient(baseURL string, timeout time.Duration) *FirecrawlClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &FirecrawlClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: timeout},
		// Generous, because the prompts no longer take a page's opening: they
		// take the passages that match the query (see ExcerptFor), and the answer
		// to a query sits anywhere in the page. At 5000 bytes a documentation
		// page was cut before its first section.
		ContentLimit: 40000,
	}
}

// ScrapedContent is the result of scraping a URL
type ScrapedContent struct {
	URL     string
	Title   string
	Content string
}

// errBlockedTarget marks a URL the scraper is not allowed to fetch.
var errBlockedTarget = errors.New("blocked scrape target")

// errEmptyScrape marks a scrape that succeeded but returned no text.
var errEmptyScrape = errors.New("scrape returned no content")

// errNoScraper marks a search run with no scrape service configured: the
// search snippet is all there is, and the source is labelled that way.
var errNoScraper = errors.New("no scrape service configured")

// checkScrapeTarget vets a URL before it is handed to Firecrawl.
//
// The URLs come from search results — and, in LLM-search mode, from a model —
// so they are attacker-influenceable input to a server-side fetcher. A
// self-hosted Firecrawl sits inside the network that runs it, which makes an
// unfiltered scrape a way to reach anything that instance can reach: cloud
// metadata endpoints, admin ports on the loopback interface, hosts on the LAN.
// Only http(s) to a public address is scraped.
//
// Names are not resolved here. A resolution decided in this function is not
// the one the scraper's own DNS lookup will make, so it would buy a check the
// fetch can still slip (DNS rebinding) at the cost of a lookup per URL. This
// blocks the literal forms that reach a private address with no DNS at all,
// and the trust boundary is documented in the README for the rest.
func checkScrapeTarget(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: unparseable url", errBlockedTarget)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", errBlockedTarget, u.Scheme)
	}
	host := strings.ToLower(strings.Trim(u.Hostname(), "."))
	if host == "" {
		return fmt.Errorf("%w: no host", errBlockedTarget)
	}
	if blockedHosts[host] || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: host %q", errBlockedTarget, host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("%w: address %s", errBlockedTarget, ip)
		}
		return nil
	}
	// A single-label host is never a page on the public web. It is either an
	// intranet name the fetcher can resolve and a search engine cannot
	// ("jenkins", "gitlab"), or an integer-encoded address — http://2130706433/
	// and http://0x7f000001/ both reach 127.0.0.1 without parsing as an IP.
	if !strings.Contains(host, ".") {
		return fmt.Errorf("%w: host %q is not a public name", errBlockedTarget, host)
	}
	// The short and octal dotted forms do not parse as an IP either, yet the
	// fetcher reads them as one: http://127.1/, http://10.1/ and
	// http://0177.0.0.1/ all reach a private address. A URL parser takes a
	// host as IPv4 whenever its last label is a number, and no real TLD is.
	if numericLabel(host[strings.LastIndex(host, ".")+1:]) {
		return fmt.Errorf("%w: host %q is a numeric address", errBlockedTarget, host)
	}
	// Anything else is a name, left to the scraper's own resolver.
	return nil
}

// numericLabel reports whether a host label is a number as a URL parser reads
// one: decimal, octal with a leading 0, or hex with a leading 0x.
func numericLabel(s string) bool {
	digits := "0123456789"
	if hex, ok := strings.CutPrefix(s, "0x"); ok {
		s, digits = hex, "0123456789abcdef"
	}
	return strings.Trim(s, digits) == ""
}

// blockedHosts are the names that reach the fetcher's own host or the cloud
// metadata service without a public DNS answer.
var blockedHosts = map[string]bool{
	"localhost":                true,
	"metadata":                 true,
	"metadata.google.internal": true,
}

// ScrapeURL fetches and extracts content from a URL via Firecrawl.
// Firecrawl v2 exposes scrape as a POST endpoint.
func (c *FirecrawlClient) ScrapeURL(ctx context.Context, fetchURL string) (*ScrapedContent, error) {
	if err := checkScrapeTarget(fetchURL); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"url":     fetchURL,
		"formats": []string{"markdown"},
		"timeout": scrapeTimeout.Milliseconds(),
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v2/scrape", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create scrape request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("scrape failed: %w", &httpError{Code: resp.StatusCode, Body: string(b)})
	}

	// Firecrawl v2 reports the page title under data.metadata.title; older
	// responses put it at data.title. Both are read so neither shape loses it.
	var data struct {
		Success bool `json:"success"`
		Data    struct {
			Markdown string `json:"markdown"`
			Title    string `json:"title"`
			Metadata struct {
				Title      string `json:"title"`
				StatusCode int    `json:"statusCode"`
				Error      string `json:"error"`
			} `json:"metadata"`
		} `json:"data"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode scrape response: %w", err)
	}

	if !data.Success {
		return nil, fmt.Errorf("scrape failed for %s", fetchURL)
	}
	// The API call can succeed while the target page returns an error.
	// Preserve the target status so callers fall back to a search snippet
	// instead of counting a 404/error page as successfully fetched evidence.
	if status := data.Data.Metadata.StatusCode; status >= http.StatusBadRequest {
		return nil, fmt.Errorf("target page failed: %w", &httpError{Code: status, Body: data.Data.Metadata.Error})
	}

	title := data.Data.Title
	if title == "" {
		title = data.Data.Metadata.Title
	}

	return &ScrapedContent{
		URL:     fetchURL,
		Title:   title,
		Content: truncateUTF8(data.Data.Markdown, c.ContentLimit),
	}, nil
}

// truncateUTF8 caps s at limit bytes without splitting a rune. The cap is a
// byte offset into a UTF-8 string, so slicing it directly turned any
// multi-byte rune straddling the boundary into invalid UTF-8, which then
// reached the report and the exports as U+FFFD.
func truncateUTF8(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// SearchResult is one result a search engine returned.
type SearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Engine  string  `json:"engine"`
	Score   float64 `json:"score"`
	// Body is the page's full text when the engine returned it with the
	// result (Exa, Tavily). A result that has it is not scraped: the text is
	// already here, and a second fetch would cost another request per page.
	Body string `json:"-"`
	// instance is the SearXNG instance that answered, so a result set that
	// is all noise can be traced to it and the instance rested.
	instance string
}

// Searcher is a web search engine: SearXNG, Exa or Tavily.
type Searcher interface {
	Search(ctx context.Context, query string) ([]SearchResult, error)
	// Name is the engine as search_engine spells it.
	Name() string
}

// Scraper fetches one page's text: Firecrawl, Exa or Tavily.
type Scraper interface {
	ScrapeURL(ctx context.Context, url string) (*ScrapedContent, error)
	// Name is the scraper as the scraper setting spells it.
	Name() string
}

// SearchTools provides real web search and webpage scraping capabilities.
type SearchTools struct {
	Engine Searcher
	// Scraper is nil when none is configured, and every result without a Body
	// then keeps its search snippet.
	Scraper         Scraper
	MaxURLsPerQuery int
	// ScrapeParallelism bounds how many pages are scraped concurrently within
	// a single search. Zero or less means sequential.
	ScrapeParallelism int
	// logf, when set, receives a line per sub-step (the search query and each
	// scrape) so the runner can show exactly what each tool does.
	logf func(string)
	// Skip, when set, names pages the run already has in full. They are not
	// fetched again and take no slot of the budget: a page another search
	// already read cost a second download and left this search one new page
	// short.
	Skip func(url string) bool
	// Domains, when set, is the source list: every search is scoped to these
	// sites and anything the engine returns from elsewhere is dropped. It is
	// set before the run's searches start and not changed during them.
	Domains []string
}

// SetLogger wires the progress logger so each sub-step reports the source
// (where) and the query or URL (what).
func (s *SearchTools) SetLogger(logf func(string)) {
	s.logf = logf
}

func (s *SearchTools) log(msg string) {
	if s.logf != nil {
		s.logf(msg)
	}
}

// httpError is a non-2xx response from a service. The status travels on the
// error itself rather than inside its message: re-mining a code out of the
// text classified a 403 whose body happened to contain "404" as a 404, and any
// message carrying a bare "404" (a URL ending in /404-page, say) panicked the
// scrape goroutine and took the run down with it.
type httpError struct {
	Code int
	Body string
	// Keyed is set for a service that takes an API key (Exa, Tavily). Only
	// there is a 401 a key to fix or a 402 credit to buy: a public SearXNG
	// instance behind basic auth answers 401 too, and has no key of yours.
	Keyed bool
}

func (e *httpError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("status %d", e.Code)
	}
	return fmt.Sprintf("status %d: %s", e.Code, e.Body)
}

// scrapeCode extracts a short status token from a scrape error: the HTTP
// status when the failure carried one, and otherwise a generic classification.
func scrapeCode(err error) string {
	if err == nil {
		return ""
	}
	// A blocked target never reached the network, so it has no HTTP status and
	// saying "scrape_failed" would read as a page that would not load.
	if errors.Is(err, errBlockedTarget) {
		return "blocked"
	}
	if errors.Is(err, errEmptyScrape) {
		return "empty"
	}
	if errors.Is(err, errNoScraper) {
		return "noservice"
	}
	var he *httpError
	if errors.As(err, &he) {
		return strconv.Itoa(he.Code)
	}
	return "scrape_failed"
}

// DomainOf extracts the host of a URL for compact display in logs and for
// grouping citations. It is the one host extractor in the codebase: the agent
// and the UI both call it rather than keeping their own near-copies.
//
// Scheme-less input ("example.com/x") is common in model-supplied URLs, and
// url.Parse files all of it under Path, so that case is trimmed by hand.
func DomainOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	u = strings.TrimPrefix(u, "//")
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

// scrape fetches a page's text, or reports that there is no scraper.
func (s *SearchTools) scrape(ctx context.Context, url string) (*ScrapedContent, error) {
	if s.Scraper == nil {
		return nil, errNoScraper
	}
	return s.Scraper.ScrapeURL(ctx, url)
}

// SearchResults is the full result of a search query.
type SearchResults struct {
	Query     string
	Findings  []SearchFinding
	URLsFound []string
	// Signals records the source-quality outcome for each attempted fetch,
	// in the same order as Findings. The UI renders these as quality
	// indicators (fetched, degraded, dropped).
	Signals []SourceSignal
	// Skipped are results the engines returned that were judged off-topic and
	// never fetched. They are reported rather than discarded: a run that
	// quietly throws away most of what it found is indistinguishable from one
	// that found little, and the difference matters to whoever reads it.
	Skipped []SkippedSource
	// Known are relevant results that Skip said the run already has. They
	// were not fetched, but this query found them, which the run records.
	Known []SkippedSource
}

// SkippedSource is a search result rejected before it was fetched.
type SkippedSource struct {
	URL    string  `json:"url"`
	Domain string  `json:"domain"`
	Title  string  `json:"title,omitempty"`
	Score  float64 `json:"score"`
}

// SourceSignal is the quality/availability outcome of fetching a single URL.
type SourceSignal struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	// Status is one of: ok (fetched and scraped cleanly), degraded (the scrape
	// failed but the search snippet survives) or dropped (nothing was
	// retrieved at all).
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

// SearchFinding is a verified finding from web search + scraping.
type SearchFinding struct {
	Title      string
	Content    string
	URL        string
	Engine     string
	Confidence string
}

// dedupeResults keeps the first result for each distinct URL, in search order.
func dedupeResults(in []SearchResult) []SearchResult {
	seen := make(map[string]bool, len(in))
	out := make([]SearchResult, 0, len(in))
	for _, r := range in {
		key := CanonicalURL(r.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// CanonicalURL is the identity a URL is deduplicated under: scheme and host
// are case-insensitive and a bare trailing slash on the root path is not a
// distinct page, so "https://go.dev" and "https://go.dev/" are one source.
//
// It is exported because the same identity has to hold at two levels: within
// one search, where SearXNG returns a page once per engine, and across the
// run's sub-agents, which all search the same question and are handed the same
// pages back.
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.TrimSpace(raw), "/")
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	// A default port names the same page as no port; trimming the suffix
	// keeps an IPv6 host's brackets.
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// Search performs a real web search and enriches each result with the page's
// full text — the body the engine returned with it, or a scrape — so findings
// carry the actual page text (quotable content) rather than just a snippet. The snippet
// is kept as a fallback when scraping a URL fails, so a slow scraper
// never drops an otherwise good result. terms rank the results; see
// rankByRelevance.
func (s *SearchTools) Search(ctx context.Context, query string, terms []string) (*SearchResults, error) {
	// Returned as it is: every caller says "search failed" itself, and the
	// prefix here printed it twice in every error line.
	searchResults, err := searchIn(ctx, s.Engine, query, s.Domains)
	if err != nil {
		return nil, err
	}

	// SearXNG fans a query out to several engines, so the same page can come
	// back once per engine. Deduplicating before the budget is applied stops a
	// duplicate from being scraped twice, cited twice, and from consuming a
	// slot a distinct source could have taken.
	// Rank before budgeting. Applying the budget to the engines' own order
	// spent every slot on whatever they ranked first, which on a long
	// technical question is routinely unrelated to it; the pages that answer
	// the question are usually present but buried below them.
	ranked, offTopic := rankByRelevance(s.inScope(dedupeResults(searchResults)), terms)
	ranked, known := s.splitKnown(ranked)
	if len(ranked) > s.MaxURLsPerQuery {
		// Results past the budget were not rejected, only not reached, so they
		// are not reported as off-topic.
		ranked = ranked[:s.MaxURLsPerQuery]
	}
	candidates := make([]SearchResult, len(ranked))
	for i, r := range ranked {
		candidates[i] = r.SearchResult
	}
	var skipped []SkippedSource
	for _, r := range offTopic {
		skipped = append(skipped, SkippedSource{
			URL: r.URL, Domain: DomainOf(r.URL), Title: r.Title, Score: r.Score,
		})
	}
	s.logResults(searchResults)
	if len(candidates) == 0 && len(known) == 0 && len(offTopic) > 0 {
		// Nothing a page could answer from: an instance that does this was
		// seen answering every query with results for its first word alone.
		if r, ok := s.Engine.(offTopicRester); ok {
			r.restOffTopic(searchResults[0].instance)
		}
	}

	// Scraping dominates a search's wall-clock time, so pages are fetched
	// concurrently. Results are written into fixed slots and assembled after
	// the wait, which keeps findings and signals in search-result order and
	// index-aligned with each other regardless of completion order.
	findings := make([]SearchFinding, len(candidates))
	signals := make([]SourceSignal, len(candidates))
	urls := make([]string, len(candidates))

	limit := s.ScrapeParallelism
	if limit <= 0 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, r := range candidates {
		wg.Add(1)
		go func(i int, r SearchResult) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			urls[i] = r.URL
			findings[i], signals[i] = s.fetch(ctx, r)
		}(i, r)
	}
	wg.Wait()

	// A cancelled context leaves unfilled slots; drop them so no zero-valued
	// finding reaches the model.
	var fs []SearchFinding
	var sg []SourceSignal
	var us []string
	for i := range findings {
		if findings[i].URL == "" {
			continue
		}
		fs = append(fs, findings[i])
		sg = append(sg, signals[i])
		us = append(us, urls[i])
	}

	return &SearchResults{Query: query, Findings: fs, URLsFound: us, Signals: sg, Skipped: skipped, Known: known}, nil
}

// offTopicRester is an engine that can rest the instance whose answer was
// all off-topic, as it rests one that refused.
type offTopicRester interface {
	restOffTopic(instance string)
}

// logResults is the search's one line: how many results and from which
// instance, so a run's junk can be traced to where it came from. Off-topic
// results are the run's to report, once per search; each used to get a
// line here as well.
func (s *SearchTools) logResults(res []SearchResult) {
	line := fmt.Sprintf("  %-9s %d results", s.Engine.Name(), len(res))
	if len(res) > 0 && res[0].instance != "" {
		line += " from " + DomainOf(res[0].instance)
	}
	s.log(line)
}

// splitKnown separates the results Skip says the run already has, before the
// budget is applied, so the budget goes to pages the run has not read.
func (s *SearchTools) splitKnown(ranked []ScoredResult) (fresh []ScoredResult, known []SkippedSource) {
	if s.Skip == nil {
		return ranked, nil
	}
	for _, r := range ranked {
		if !s.Skip(r.URL) {
			fresh = append(fresh, r)
			continue
		}
		known = append(known, SkippedSource{URL: r.URL, Domain: DomainOf(r.URL), Title: r.Title, Score: r.Score})
		s.log(fmt.Sprintf("    known    %s", DomainOf(r.URL)))
	}
	return fresh, known
}

// body is the result's full text: the one the engine sent with it, else a
// scrape.
func (s *SearchTools) body(ctx context.Context, r SearchResult) (*ScrapedContent, error) {
	if strings.TrimSpace(r.Body) != "" {
		return &ScrapedContent{URL: r.URL, Content: r.Body}, nil
	}
	return s.scrape(ctx, r.URL)
}

// fetch scrapes one search result and labels how its content was obtained.
func (s *SearchTools) fetch(ctx context.Context, r SearchResult) (SearchFinding, SourceSignal) {
	title, content := r.Title, r.Content
	signal := SourceSignal{URL: r.URL, Domain: DomainOf(r.URL), Status: "ok"}
	scraped, err := s.body(ctx, r)
	// A 200 with an empty body is a page the scraper could not read (JS-only,
	// PDF, paywall), not a page with nothing on it. Taking it as a fetch threw
	// away the snippet and labelled a blank source a clean 200.
	if err == nil && strings.TrimSpace(scraped.Content) == "" {
		err = errEmptyScrape
	}
	switch {
	case err == nil:
		if scraped.Title != "" {
			title = scraped.Title
		}
		content = scraped.Content
		s.log(fmt.Sprintf("    ok       %s", DomainOf(r.URL)))
	case strings.TrimSpace(content) != "":
		// Scraping failed but the search snippet survives: keep it and flag
		// the source as degraded rather than dropping it.
		signal.Status, signal.Code = "degraded", scrapeCode(err)
		s.log(fmt.Sprintf("    snippet  %s", DomainOf(r.URL)))
	default:
		// Nothing was scraped and there is no snippet either, so this source
		// contributed no content at all. Saying "degraded" would claim a body
		// the finding does not have.
		signal.Status, signal.Code = "dropped", scrapeCode(err)
		s.log(fmt.Sprintf("    dropped  %s", DomainOf(r.URL)))
	}
	// No confidence is claimed: every fetched source was labelled "high", a
	// snippet-only one included, and the report printed it beside each
	// citation. How the source was obtained is the signal's status; how far
	// it can be trusted depends on the claim it is cited for.
	return SearchFinding{Title: title, Content: content, URL: r.URL, Engine: r.Engine}, signal
}
