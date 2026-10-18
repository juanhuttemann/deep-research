package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxDomains bounds a source list. SearXNG gets the list as site: operators in
// the query text, and upstream engines truncate or reject a query past a few
// hundred characters, so a longer list would quietly scope to its first few.
const maxDomains = 20

// minExtracted is how much page text the preflight takes as proof a domain
// can be read. A consent wall, a JavaScript shell or a bot challenge comes
// back as a 200 with a line or two on it, which is not a page.
const minExtracted = 200

// ParseDomains reads a source list: hosts or URLs, separated by commas or
// whitespace, in any number of arguments. Each is reduced to its lower-case
// host without "www.", since a domain covers its subdomains anyway. A path is
// dropped: no engine scopes a search to one reliably.
//
// Only public names pass, by the same rule a scrape target does. The list is
// the reader's, but the pages under it are fetched by a scraper that may sit
// inside a private network.
func ParseDomains(raw []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, arg := range raw {
		for _, f := range strings.FieldsFunc(arg, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
			host, err := domainHost(f)
			if err != nil {
				return nil, fmt.Errorf("%w: domains: %q: %w", ErrSearchConfig, f, err)
			}
			if !seen[host] {
				seen[host] = true
				out = append(out, host)
			}
		}
	}
	if len(out) > maxDomains {
		return nil, fmt.Errorf("%w: domains: %d given, at most %d", ErrSearchConfig, len(out), maxDomains)
	}
	return out, nil
}

// domainHost is one source-list entry reduced to its host.
func domainHost(entry string) (string, error) {
	s := entry
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("not a domain")
	}
	host := strings.TrimPrefix(strings.ToLower(strings.Trim(u.Hostname(), ".")), "www.")
	if err := checkScrapeTarget("https://" + host); err != nil {
		// Said as what it is for this list: "blocked scrape target" names
		// the guard, not the reader's mistake.
		why := strings.TrimPrefix(err.Error(), errBlockedTarget.Error()+": ")
		return "", fmt.Errorf("not a public site (%s)", why)
	}
	return host, nil
}

// InDomains reports whether rawURL is on one of domains or a subdomain of
// one. A look-alike ("notexample.com" for "example.com") is not.
func InDomains(rawURL string, domains []string) bool {
	host := ""
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		host = u.Hostname()
	}
	host = strings.ToLower(strings.Trim(host, "."))
	if host == "" {
		return false
	}
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// DomainSearcher is an engine that restricts a search to a set of sites
// itself (Exa, Tavily). Any other engine is scoped through the query text.
type DomainSearcher interface {
	SearchIn(ctx context.Context, query string, domains []string) ([]SearchResult, error)
}

// searchIn runs query on e, restricted to domains when there are any.
// SearXNG has no parameter for it, so the restriction goes in the query as
// site: operators: Google, Bing, DuckDuckGo and Brave honour them, others
// ignore them, and the results are filtered either way (see Search).
func searchIn(ctx context.Context, e Searcher, query string, domains []string) ([]SearchResult, error) {
	if len(domains) == 0 {
		return e.Search(ctx, query)
	}
	if ds, ok := e.(DomainSearcher); ok {
		return ds.SearchIn(ctx, query, domains)
	}
	return e.Search(ctx, query+" "+siteFilter(domains))
}

// siteFilter is domains as one site: operator, or an OR group of them.
func siteFilter(domains []string) string {
	sites := make([]string, len(domains))
	for i, d := range domains {
		sites[i] = "site:" + d
	}
	if len(sites) == 1 {
		return sites[0]
	}
	return "(" + strings.Join(sites, " OR ") + ")"
}

// inScope keeps the results on s.Domains. The engines' own scoping is a
// request, not a guarantee: a SearXNG engine that ignores site: answers with
// the open web, and the reader asked for these sites only.
func (s *SearchTools) inScope(res []SearchResult) []SearchResult {
	if len(s.Domains) == 0 {
		return res
	}
	kept := res[:0:0]
	for _, r := range res {
		if InDomains(r.URL, s.Domains) {
			kept = append(kept, r)
		}
	}
	if out := len(res) - len(kept); out > 0 {
		s.log(fmt.Sprintf("    %d outside the domain list", out))
	}
	return kept
}

// Domain check statuses. Only ok and snippet-only domains are researched.
const (
	DomainOK          = "ok"
	DomainSnippetOnly = "snippet-only"
	DomainUnindexed   = "unindexed"
	DomainUnreadable  = "unreadable"
	DomainSearchFail  = "search-failed"
)

// DomainCheck is what the preflight found for one domain of a source list.
type DomainCheck struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	// Pages is how many results a search scoped to the domain returned on it.
	Pages int `json:"pages"`
	// Sample is the page the extraction was tried on, and Chars how much text
	// came back from it.
	Sample string `json:"sample,omitempty"`
	Chars  int    `json:"chars,omitempty"`
	// Detail says why a domain failed, in the reader's terms.
	Detail string `json:"detail,omitempty"`
	// searchErr is the search's own error on a search-failed check. When
	// every domain has one, the search is what is down, and the run says
	// that rather than blaming each site.
	searchErr error
}

// SearchFailedCheck is the check of a domain whose search did not answer.
func SearchFailedCheck(domain string, err error) DomainCheck {
	return DomainCheck{Domain: domain, Status: DomainSearchFail,
		Detail: fmt.Sprintf("%s: %v", SearchStatus(err), err), searchErr: err}
}

// SearchErr is the search's error behind a search-failed check, nil on any
// other.
func (c DomainCheck) SearchErr() error { return c.searchErr }

// Usable reports whether a run can research the domain: the engine finds
// pages on it and they can be read, in full or from their snippet.
func (c DomainCheck) Usable() bool {
	return c.Status == DomainOK || c.Status == DomainSnippetOnly
}

// Line is what the check found, in words: the run's table and doctor both
// print it after the domain.
func (c DomainCheck) Line() string {
	if c.Status == DomainOK {
		pages := "pages"
		if c.Pages == 1 {
			pages = "page"
		}
		return fmt.Sprintf("%d %s found; read %d characters from %s", c.Pages, pages, c.Chars, c.Sample)
	}
	return c.Status + " — " + c.Detail
}

// CheckDomains proves each domain works before a run spends a model call on
// it: a search scoped to the domain must find pages there, and the first of
// them must be readable through the scraper (or come with its text, on Exa
// and Tavily). The checks run in parallel; results keep the list's order.
func (s *SearchTools) CheckDomains(ctx context.Context, domains []string) []DomainCheck {
	out := make([]DomainCheck, len(domains))
	sem := make(chan struct{}, max(s.ScrapeParallelism, 1))
	var wg sync.WaitGroup
	for i, d := range domains {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.checkDomain(ctx, d)
		})
	}
	wg.Wait()
	return out
}

// probeRetryWait is how long an empty or rate-limited probe waits before it
// asks again. Three probes at once had Google CSE refuse one of them, and
// pkg.go.dev was reported unindexed. A throttled engine often answers again
// within seconds; a site it does not index stays empty.
var probeRetryWait = 3 * time.Second

// checkDomain is one domain's preflight.
func (s *SearchTools) checkDomain(ctx context.Context, domain string) DomainCheck {
	c := DomainCheck{Domain: domain}
	on, err := s.probe(ctx, domain)
	if (err == nil && len(on) == 0) || SearchStatus(err) == "rate-limited" {
		select {
		case <-time.After(probeRetryWait):
			on, err = s.probe(ctx, domain)
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		return SearchFailedCheck(domain, err)
	}
	c.Pages = len(on)
	if len(on) == 0 {
		c.Status = DomainUnindexed
		c.Detail = s.engineName() + " returned no pages on it, asked twice" + s.homepageNote(ctx, domain)
		return c
	}
	return s.checkExtract(ctx, c, on[:min(len(on), maxSamples)])
}

// probe searches the domain for itself and keeps the results on it.
func (s *SearchTools) probe(ctx context.Context, domain string) ([]SearchResult, error) {
	res, err := searchIn(ctx, s.Engine, domain, []string{domain})
	if err != nil {
		return nil, err
	}
	var on []SearchResult
	for _, r := range dedupeResults(res) {
		if InDomains(r.URL, []string{domain}) {
			on = append(on, r)
		}
	}
	return on, nil
}

// maxSamples is how many of a domain's pages the preflight tries to read. A
// site is judged by whether a run can read it, and a run reads several
// pages: Exa answers a bare "pkg.go.dev" with the home page first, 165
// characters of navigation, on a site whose other pages are all text.
const maxSamples = 3

// checkExtract reads the domain's results in order until one gives a page
// of text, the proof a run can read the site.
func (s *SearchTools) checkExtract(ctx context.Context, c DomainCheck, samples []SearchResult) DomainCheck {
	var first error
	snippet := false
	for _, r := range samples {
		chars, err := s.readSample(ctx, r)
		if err == nil {
			c.Status, c.Sample, c.Chars = DomainOK, r.URL, chars
			return c
		}
		if first == nil {
			first, c.Sample, c.Chars = err, r.URL, chars
		}
		// A run keeps a page's snippet when it cannot read the page, scraper
		// or not. Counting a snippet only without one meant configuring
		// Firecrawl dropped every site it was refused on.
		snippet = snippet || strings.TrimSpace(r.Content) != ""
	}
	if snippet {
		c.Status, c.Detail = DomainSnippetOnly, s.snippetDetail(first, c.Chars)
		return c
	}
	c.Status, c.Detail = DomainUnreadable, extractDetail(first, c.Chars)
	if len(samples) > 1 {
		c.Detail = fmt.Sprintf("none of %d pages tried could be read; the first: %s", len(samples), c.Detail)
	}
	return c
}

// readSample reads one result as a run would and says how much text came.
func (s *SearchTools) readSample(ctx context.Context, r SearchResult) (int, error) {
	page, err := s.body(ctx, r)
	if err != nil {
		return 0, err
	}
	chars := len(strings.TrimSpace(page.Content))
	if chars < minExtracted {
		return chars, errEmptyScrape
	}
	return chars, nil
}

// snippetDetail says why a domain's sources will be snippets. On Exa and
// Tavily no scraper is needed until the engine sends a result without its
// text, so "no scraper configured" there read as a setup mistake.
func (s *SearchTools) snippetDetail(err error, chars int) string {
	if !errors.Is(err, errNoScraper) {
		return "its search snippets are the sources; the pages could not be read: " + extractDetail(err, chars)
	}
	if InlineText(s.engineName()) {
		return s.engineName() + " sent no page text with its results and no scraper is configured; its search snippets are the sources"
	}
	return "no scraper configured; its search snippets are the sources"
}

// extractDetail says why a page could not be read.
func extractDetail(err error, chars int) string {
	switch code := scrapeCode(err); code {
	case "empty":
		return fmt.Sprintf("the page had %d characters of text (JavaScript-only, a consent wall or a bot check)", chars)
	case "noservice":
		return "no scraper configured and no snippet came back"
	case "blocked":
		return err.Error()
	default:
		return fmt.Sprintf("scrape %s: %v", code, err)
	}
}

// homepageNote tells a site the engine does not index apart from one that is
// down, by reading its home page. Without a scraper there is nothing to ask.
func (s *SearchTools) homepageNote(ctx context.Context, domain string) string {
	if s.Scraper == nil {
		return ""
	}
	if _, err := s.Scraper.ScrapeURL(ctx, "https://"+domain+"/"); err != nil {
		return fmt.Sprintf(", and its home page did not load (%s)", scrapeCode(err))
	}
	return ", though its home page loads"
}

// engineName is the engine as the setting spells it.
func (s *SearchTools) engineName() string {
	if s.Engine == nil {
		return "search"
	}
	return s.Engine.Name()
}
