package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// An empty probe's wait for a throttled engine is real time; no test double
// throttles.
func init() { probeRetryWait = 0 }

func TestParseDomainsNormalisesEachEntry(t *testing.T) {
	got, err := ParseDomains([]string{"Go.dev, https://www.pkg.go.dev/std?x=1", "go.dev\nexample.com/docs/", "  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go.dev", "pkg.go.dev", "example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("ParseDomains = %v, want %v", got, want)
	}
	if got, err := ParseDomains(nil); err != nil || got != nil {
		t.Errorf("an empty list = %v, %v; want nothing and no error", got, err)
	}
}

// The scraper behind the list may sit inside a private network, so the list
// is held to the rule a scrape target is.
func TestParseDomainsRefusesWhatIsNotAPublicName(t *testing.T) {
	for _, in := range []string{"localhost", "127.0.0.1", "10.0.0.5", "jenkins", "metadata.google.internal", "http://127.1/", "ftp://", "[::1]"} {
		if _, err := ParseDomains([]string{in}); !errors.Is(err, ErrSearchConfig) {
			t.Errorf("ParseDomains(%q) err = %v, want a search setting error", in, err)
		}
	}
}

func TestParseDomainsCapsTheList(t *testing.T) {
	var many []string
	for i := range maxDomains + 1 {
		many = append(many, strings.Repeat("a", i+1)+".com")
	}
	if _, err := ParseDomains(many); err == nil {
		t.Errorf("%d domains accepted; SearXNG would scope to only the first few", len(many))
	}
}

func TestInDomainsMatchesSubdomainsNotLookAlikes(t *testing.T) {
	d := []string{"example.com"}
	for u, want := range map[string]bool{
		"https://example.com/a":         true,
		"https://docs.example.com/a":    true,
		"https://EXAMPLE.com:8443/":     true,
		"https://notexample.com/":       false,
		"https://example.com.evil.net/": false,
		"not a url":                     false,
		"https://evil.net/?example.com": false,
	} {
		if got := InDomains(u, d); got != want {
			t.Errorf("InDomains(%q) = %v, want %v", u, got, want)
		}
	}
}

// recordingEngine answers every query with the same results and keeps the
// queries it was sent.
type recordingEngine struct {
	name    string // searxng when empty
	mu      sync.Mutex
	queries []string
	results []SearchResult
	err     error
}

func (e *recordingEngine) Name() string {
	if e.name != "" {
		return e.name
	}
	return "searxng"
}

func (e *recordingEngine) Search(_ context.Context, q string) ([]SearchResult, error) {
	e.mu.Lock()
	e.queries = append(e.queries, q)
	e.mu.Unlock()
	return e.results, e.err
}

func TestSearchScopesTheQueryAndDropsResultsOffTheList(t *testing.T) {
	eng := &recordingEngine{results: []SearchResult{
		{Title: "Generics tutorial", URL: "https://go.dev/doc/tutorial/generics", Content: "generics type parameters"},
		{Title: "Generics in Go", URL: "https://blog.example.net/generics", Content: "generics type parameters"},
		{Title: "Constraints", URL: "https://pkg.go.dev/golang.org/x/exp/constraints", Content: "generics type parameters"},
	}}
	st := &SearchTools{Engine: eng, MaxURLsPerQuery: 5, Domains: []string{"go.dev"}}
	res, err := st.Search(context.Background(), "go generics", []string{"generics"})
	if err != nil {
		t.Fatal(err)
	}
	if q := eng.queries[0]; q != "go generics site:go.dev" {
		t.Errorf("query = %q, want the site: operator appended", q)
	}
	for _, f := range res.Findings {
		if !InDomains(f.URL, st.Domains) {
			t.Errorf("finding %s is off the domain list", f.URL)
		}
	}
	if len(res.Findings) != 2 {
		t.Errorf("findings = %d, want the two on go.dev and its subdomain", len(res.Findings))
	}
	// Off the list is not off-topic: reporting it so would rest a healthy
	// SearXNG instance for answering the open web.
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", res.Skipped)
	}
}

func TestSiteFilterGroupsSeveralDomains(t *testing.T) {
	if got := siteFilter([]string{"a.com", "b.org"}); got != "(site:a.com OR site:b.org)" {
		t.Errorf("siteFilter = %q", got)
	}
}

func TestHostedEnginesScopeThroughTheirOwnParameter(t *testing.T) {
	for name, tc := range map[string]struct {
		search func(url string) DomainSearcher
		key    string
	}{
		"exa":    {func(u string) DomainSearcher { return NewExaClient(u, "k", 0) }, "includeDomains"},
		"tavily": {func(u string) DomainSearcher { return NewTavilyClient(u, "k", 0) }, "include_domains"},
	} {
		sv, bodies, _ := newHostedServer(t, http.StatusOK, map[string]string{"/search": `{"results":[]}`})
		if _, err := tc.search(sv.URL).SearchIn(context.Background(), "q", []string{"go.dev"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := json.Marshal((*bodies)[0][tc.key])
		if string(got) != `["go.dev"]` {
			t.Errorf("%s: %s = %s, want [\"go.dev\"]", name, tc.key, got)
		}
		if q := (*bodies)[0]["query"]; q != "q" {
			t.Errorf("%s: query = %v, want it untouched", name, q)
		}
	}
}

// pageScraper answers each URL from a table: text, or an error.
type pageScraper map[string]any

func (p pageScraper) Name() string { return "pages" }

func (p pageScraper) ScrapeURL(_ context.Context, u string) (*ScrapedContent, error) {
	switch v := p[u].(type) {
	case string:
		return &ScrapedContent{URL: u, Content: v}, nil
	case error:
		return nil, v
	}
	return nil, &httpError{Code: http.StatusNotFound}
}

func TestCheckDomainsClassifiesEachDomain(t *testing.T) {
	long := strings.Repeat("documentation ", 40)
	eng := &recordingEngine{results: []SearchResult{
		{URL: "https://good.com/a", Content: "snippet"},
		{URL: "https://walled.com/a", Content: "snippet"},
		{URL: "https://thin.com/a"},
	}}
	sc := pageScraper{
		"https://good.com/a":   long,
		"https://walled.com/a": &httpError{Code: http.StatusForbidden},
		"https://thin.com/a":   "Please enable JavaScript",
		"https://unseen.com/":  long,
	}
	st := &SearchTools{Engine: eng, Scraper: sc, ScrapeParallelism: 2}
	got := st.CheckDomains(context.Background(), []string{"good.com", "walled.com", "thin.com", "unseen.com"})
	// walled.com refuses the scraper but its snippet survives, as a run keeps
	// it; thin.com has no snippet to fall back on.
	want := []string{DomainOK, DomainSnippetOnly, DomainUnreadable, DomainUnindexed}
	for i, c := range got {
		if c.Status != want[i] {
			t.Errorf("%s: status %q (%s), want %q", c.Domain, c.Status, c.Detail, want[i])
		}
	}
	if !got[0].Usable() || got[0].Pages != 1 || got[0].Sample != "https://good.com/a" || got[0].Chars < minExtracted {
		t.Errorf("good.com = %+v", got[0])
	}
	if !strings.Contains(got[1].Detail, "403") {
		t.Errorf("walled.com detail = %q, want the status", got[1].Detail)
	}
	if !strings.Contains(got[3].Detail, "home page loads") {
		t.Errorf("unseen.com detail = %q, want it told apart from a site that is down", got[3].Detail)
	}
	for _, q := range eng.queries {
		if !strings.Contains(q, "site:") {
			t.Errorf("probe %q was not scoped to its domain", q)
		}
	}
}

func TestCheckDomainsWithoutAScraperIsSnippetOnly(t *testing.T) {
	eng := &recordingEngine{results: []SearchResult{{URL: "https://go.dev/doc", Content: "snippet"}}}
	got := (&SearchTools{Engine: eng}).CheckDomains(context.Background(), []string{"go.dev"})
	if got[0].Status != DomainSnippetOnly || !got[0].Usable() {
		t.Errorf("check = %+v, want a usable snippet-only domain", got[0])
	}
}

func TestCheckDomainsReportsAFailedSearch(t *testing.T) {
	eng := &recordingEngine{err: &httpError{Code: http.StatusTooManyRequests}}
	got := (&SearchTools{Engine: eng}).CheckDomains(context.Background(), []string{"go.dev"})
	if got[0].Status != DomainSearchFail || got[0].Usable() {
		t.Errorf("check = %+v, want an unusable search failure", got[0])
	}
}

// A real SearXNG receives the scoped query in q.
func TestSearXNGReceivesTheSiteOperator(t *testing.T) {
	var q string
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(sv.Close)
	st := newSearchTools(sv.URL, "", 0)
	st.Domains = []string{"a.com", "b.org"}
	if _, err := st.Search(context.Background(), "heat pumps", nil); err != nil {
		t.Fatal(err)
	}
	if q != "heat pumps (site:a.com OR site:b.org)" {
		t.Errorf("q = %q", q)
	}
}
