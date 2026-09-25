package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// newSearXNGServer serves a fake SearXNG API with the given results.
func newSearXNGServer(t *testing.T, results []map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"results": results}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	return server
}

// newFirecrawlServer serves a fake Firecrawl scrape endpoint (POST).
func newFirecrawlServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST scrape request, got %s", r.Method)
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode scrape request body: %v", err)
		}
		resp := map[string]any{
			"success": true,
			"data":    map[string]any{"markdown": "# Scraped Page\n\nFull content here.", "title": "Scraped Page"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearXNGClient_Search(t *testing.T) {
	server := newSearXNGServer(t, []map[string]any{
		{"title": "Test Result 1", "url": "https://example.com/1", "content": "Content 1", "engine": "google", "score": 1.0},
		{"title": "Test Result 2", "url": "https://example.com/2", "content": "Content 2", "engine": "bing", "score": 0.8},
	})

	client := NewSearXNGClient(server.URL, 0)
	results, err := client.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Test Result 1" {
		t.Errorf("expected 'Test Result 1', got %q", results[0].Title)
	}
}

func TestSearXNGClient_Search_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := NewSearXNGClient(server.URL, 0)
	if _, err := client.Search(context.Background(), "test query"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirecrawlClient_ScrapURL(t *testing.T) {
	server := newFirecrawlServer(t)

	client := NewFirecrawlClient(server.URL, 0)
	scraped, err := client.ScrapeURL(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("ScrapeURL() error: %v", err)
	}
	if scraped.Title != "Scraped Page" {
		t.Errorf("expected 'Scraped Page', got %q", scraped.Title)
	}
	if scraped.Content == "" {
		t.Error("expected content, got empty string")
	}
}

func TestSearchTools_Search(t *testing.T) {
	searxServer := newSearXNGServer(t, []map[string]any{
		{"title": "Test Result", "url": "https://example.com/test", "content": "a short answer to the test query", "engine": "google", "score": 1.0},
		{"title": "Long Test Result", "url": "https://example.com/long", "content": "test query " + strings.Repeat("x", 250), "engine": "bing", "score": 0.9},
	})
	// The handler runs on one goroutine per connection and the scrapes are
	// issued in parallel, so the counter has to be atomic: a bare int++ here
	// is a data race that `go test -race` fails on.
	var scrapeCalls atomic.Int64
	fcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST scrape request, got %s", r.Method)
		}
		scrapeCalls.Add(1)
		json.NewDecoder(r.Body).Decode(&struct {
			URL string `json:"url"`
		}{})
		resp := map[string]any{
			"success": true,
			"data":    map[string]any{"markdown": "# Scraped Page\n\nFull content here.", "title": "Scraped Page"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(fcServer.Close)

	tools := NewSearchTools(searxServer.URL, fcServer.URL, 0)
	results, err := tools.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(results.Findings))
	}
	// Every scrape succeeds, so titles/content come from the scraped page.
	if results.Findings[0].Title != "Scraped Page" || results.Findings[1].Title != "Scraped Page" {
		t.Errorf("expected scraped titles, got %q and %q", results.Findings[0].Title, results.Findings[1].Title)
	}
	if n := scrapeCalls.Load(); n != 2 {
		t.Errorf("expected a scrape per result, got %d calls", n)
	}
	if len(results.URLsFound) != 2 {
		t.Errorf("expected 2 URLs, got %v", results.URLsFound)
	}
}

// When scraping fails, the finding must survive with the search snippet.
func TestSearchTools_Search_ScrapeFailureFallback(t *testing.T) {
	searxServer := newSearXNGServer(t, []map[string]any{
		{"title": "Snippet Title", "url": "https://example.com/test", "content": "test query snippet text kept on failure", "engine": "google", "score": 1.0},
	})
	fcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(fcServer.Close)

	tools := NewSearchTools(searxServer.URL, fcServer.URL, 0)
	results, err := tools.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results.Findings) != 1 {
		t.Fatalf("expected the snippet-backed finding to survive, got %d", len(results.Findings))
	}
	f := results.Findings[0]
	if f.Title != "Snippet Title" || f.Content != "test query snippet text kept on failure" {
		t.Errorf("expected snippet fallback, got %+v", f)
	}
}

// TestSearchTools_ScrapesConcurrentlyInOrder checks the three properties the
// parallel scrape must preserve: results stay in search-result order, the
// per-query cap still applies, and scrapes actually overlap.
func TestSearchTools_ScrapesConcurrentlyInOrder(t *testing.T) {
	sxServer := newSearXNGServer(t, []map[string]any{
		{"title": "one", "url": "https://a.example/1", "content": "c1", "engine": "e"},
		{"title": "two", "url": "https://b.example/2", "content": "c2", "engine": "e"},
		{"title": "three", "url": "https://c.example/3", "content": "c3", "engine": "e"},
		{"title": "four", "url": "https://d.example/4", "content": "c4", "engine": "e"},
	})

	var mu sync.Mutex
	var inFlight, maxInFlight int
	fcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		time.Sleep(40 * time.Millisecond) // hold the slot so overlap is observable
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		mu.Lock()
		inFlight--
		mu.Unlock()

		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"markdown": "body of " + req.URL, "title": "T " + req.URL},
		})
	}))
	defer fcServer.Close()

	st := NewSearchTools(sxServer.URL, fcServer.URL, 5*time.Second)
	st.MaxURLsPerQuery = 3
	st.ScrapeParallelism = 3

	res, err := st.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(res.Findings) != 3 {
		t.Fatalf("MaxURLsPerQuery not applied: got %d findings, want 3", len(res.Findings))
	}
	want := []string{"https://a.example/1", "https://b.example/2", "https://c.example/3"}
	for i, w := range want {
		if res.Findings[i].URL != w {
			t.Errorf("finding %d = %q, want %q (order not preserved)", i, res.Findings[i].URL, w)
		}
		if res.Signals[i].URL != w {
			t.Errorf("signal %d = %q, want %q (signals not aligned to findings)", i, res.Signals[i].URL, w)
		}
	}
	if maxInFlight < 2 {
		t.Errorf("scrapes did not overlap: max in-flight was %d", maxInFlight)
	}
	if maxInFlight > 3 {
		t.Errorf("ScrapeParallelism exceeded: max in-flight was %d, limit 3", maxInFlight)
	}
}

func TestSearchTools_ScrapeParallelismIsBounded(t *testing.T) {
	results := make([]map[string]any, 8)
	for i := range results {
		results[i] = map[string]any{"title": "t", "url": fmt.Sprintf("https://e%d.example/", i), "content": "c", "engine": "e"}
	}
	sxServer := newSearXNGServer(t, results)

	var mu sync.Mutex
	var inFlight, maxInFlight int
	fcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"markdown": "m"}})
	}))
	defer fcServer.Close()

	st := NewSearchTools(sxServer.URL, fcServer.URL, 5*time.Second)
	st.MaxURLsPerQuery = 8
	st.ScrapeParallelism = 2

	if _, err := st.Search(context.Background(), "q"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if maxInFlight > 2 {
		t.Errorf("max in-flight %d exceeds ScrapeParallelism 2", maxInFlight)
	}
}

// TestSearchTools_SignalStatusVocabulary pins the three statuses the UI knows
// how to render. A scrape failure that still leaves a usable snippet is
// "degraded"; one that leaves nothing at all is "dropped", not a degraded
// source with an empty body pretending to be content.
func TestSearchTools_SignalStatusVocabulary(t *testing.T) {
	sxServer := newSearXNGServer(t, []map[string]any{
		{"title": "scrapes", "url": "https://ok.example/1", "content": "snippet", "engine": "e"},
		{"title": "snippet only", "url": "https://snip.example/2", "content": "snippet kept", "engine": "e"},
		{"title": "nothing", "url": "https://empty.example/3", "content": "", "engine": "e"},
	})
	fcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.URL, "ok.example") {
			json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"markdown": "full body", "title": "Full"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fcServer.Close()

	st := NewSearchTools(sxServer.URL, fcServer.URL, 5*time.Second)
	res, err := st.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Signals) != 3 || len(res.Findings) != 3 {
		t.Fatalf("want 3 findings and 3 signals, got %d/%d", len(res.Findings), len(res.Signals))
	}
	want := []string{"ok", "degraded", "dropped"}
	for i, w := range want {
		if res.Signals[i].Status != w {
			t.Errorf("signal %d (%s) status = %q, want %q",
				i, res.Signals[i].Domain, res.Signals[i].Status, w)
		}
	}
}

// TestDomainOf is the one host extractor the whole codebase shares; it used to
// exist three times over with three different sets of edge-case behaviour.
func TestDomainOf(t *testing.T) {
	cases := map[string]string{
		"https://www.nature.com/articles/1": "www.nature.com",
		"http://example.com":                "example.com",
		"https://example.com?q=1":           "example.com",
		"https://example.com#frag":          "example.com",
		"https://user:pw@example.com/x":     "example.com",
		"https://example.com:8443/x":        "example.com:8443",
		"example.com/path":                  "example.com",
		"example.com":                       "example.com",
		"":                                  "",
	}
	for in, want := range cases {
		if got := DomainOf(in); got != want {
			t.Errorf("DomainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// scrapeCode must read the status off the error structurally. Re-mining it
// from the message text classified a 403 whose body happened to contain "404"
// as a 404 — and, because the bare "404" pattern has no second field, panicked
// inside the scrape goroutine and took the whole run down with it.
func TestScrapeCodeReadsTheStatusStructurally(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"403 whose body mentions 404", &httpError{Code: 403, Body: "<h1>404 page not found</h1>"}, "403"},
		{"plain 404", &httpError{Code: 404}, "404"},
		{"429", &httpError{Code: 429}, "429"},
		{"wrapped", fmt.Errorf("scrape %s: %w", "https://x/404", &httpError{Code: 500}), "500"},
		{"transport failure", errors.New("dial tcp: connection refused"), "scrape_failed"},
		{"url containing 404", errors.New("scrape failed for https://x.example/404-page"), "scrape_failed"},
		{"nil", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrapeCode(tc.err); got != tc.want {
				t.Errorf("scrapeCode(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// The content cap is a byte offset into a UTF-8 string, so a cap landing
// mid-rune used to split it and leave invalid UTF-8 in the finding, which then
// flowed into the report and the exports as U+FFFD.
func TestScrapeTruncatesOnARuneBoundary(t *testing.T) {
	// "é" is two bytes, so a 5-byte cap lands inside the third rune.
	page := strings.Repeat("é", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"markdown": page},
		})
	}))
	t.Cleanup(server.Close)

	c := NewFirecrawlClient(server.URL, 0)
	c.ContentLimit = 5
	got, err := c.ScrapeURL(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("ScrapeURL: %v", err)
	}
	if !utf8.ValidString(got.Content) {
		t.Errorf("truncated content is not valid UTF-8: %q", got.Content)
	}
	if strings.ContainsRune(got.Content, utf8.RuneError) {
		t.Errorf("truncated content contains U+FFFD: %q", got.Content)
	}
}

// Hosted Firecrawl authenticates with a bearer token; without one every scrape
// 401s and every source silently degrades to "snippet only".
func TestScrapeSendsTheAPIKeyWhenOneIsSet(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"success": true, "data": map[string]any{"markdown": "body"},
		})
	}))
	t.Cleanup(server.Close)

	c := NewFirecrawlClient(server.URL, 0)
	if _, err := c.ScrapeURL(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("ScrapeURL: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("no key configured, but sent Authorization %q", gotAuth)
	}

	c.APIKey = "fc-secret"
	if _, err := c.ScrapeURL(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("ScrapeURL: %v", err)
	}
	if gotAuth != "Bearer fc-secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer fc-secret")
	}
}

// Firecrawl v2 reports the page title under data.metadata.title. Reading only
// data.title left every scraped finding titled by the search snippet instead
// of the page.
func TestScrapeReadsTheTitleFromMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Example Domain\n==============",
				"metadata": map[string]any{"title": "Example Domain"},
			},
		})
	}))
	t.Cleanup(server.Close)

	got, err := NewFirecrawlClient(server.URL, 0).ScrapeURL(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("ScrapeURL: %v", err)
	}
	if got.Title != "Example Domain" {
		t.Errorf("Title = %q, want %q", got.Title, "Example Domain")
	}
}

// SearXNG fans a query out to several engines and can return the same URL
// once per engine. Each duplicate used to be scraped again (cost) and cited
// again (redundant citations, inflated source counts).
func TestSearchDeduplicatesResultsByURL(t *testing.T) {
	searx := newSearXNGServer(t, []map[string]any{
		{"title": "Go", "url": "https://go.dev/", "content": "a", "engine": "bing"},
		{"title": "Go (dup)", "url": "https://go.dev/", "content": "b", "engine": "brave"},
		{"title": "Go (trailing slash dup)", "url": "https://go.dev", "content": "c", "engine": "ddg"},
		{"title": "Docs", "url": "https://go.dev/doc/", "content": "d", "engine": "bing"},
	})
	var scrapes atomic.Int64
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scrapes.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"success": true, "data": map[string]any{"markdown": "page"},
		})
	}))
	t.Cleanup(fc.Close)

	tools := NewSearchTools(searx.URL, fc.URL, 0)
	tools.MaxURLsPerQuery = 10
	res, err := tools.Search(context.Background(), "go")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Findings) != 2 {
		urls := make([]string, len(res.Findings))
		for i, f := range res.Findings {
			urls[i] = f.URL
		}
		t.Fatalf("expected 2 deduplicated findings, got %d: %v", len(res.Findings), urls)
	}
	if n := scrapes.Load(); n != 2 {
		t.Errorf("expected 2 scrapes, got %d", n)
	}
}
