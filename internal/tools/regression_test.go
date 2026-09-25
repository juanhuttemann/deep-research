package tools

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Live Firecrawl returned HTTP 200 and success:true for a target page that
// returned 404. The target's metadata.statusCode must govern source quality.
func TestFirecrawlTargetErrorsKeepOnlySearchSnippet(t *testing.T) {
	for _, code := range []int{403, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			sx := newSearXNGServer(t, []map[string]any{{"title": "Original result", "url": "https://example.com/missing", "content": "Search snippet"}})
			defer sx.Close()
			fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"success":true,"data":{"markdown":"Error page","metadata":{"title":"Error","statusCode":%d,"error":"Target failed"}}}`, code)
			}))
			defer fc.Close()
			got, err := NewSearchTools(sx.URL, fc.URL, 0).Search(context.Background(), "q")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Signals) != 1 || got.Signals[0].Status != "degraded" || got.Signals[0].Code != fmt.Sprint(code) {
				t.Errorf("target failure counted as fetched: %+v", got.Signals)
			}
			if len(got.Findings) != 1 || got.Findings[0].Content != "Search snippet" || got.Findings[0].Title != "Original result" {
				t.Errorf("error page replaced search evidence: %+v", got.Findings)
			}
		})
	}
}

// The per-query budget used to be applied to the engines' own ranking, so a
// run whose top results were noise spent every slot before reaching the pages
// that answered the question — and then scraped, counted and cited the noise.
func TestSearchSpendsBudgetOnRelevantResults(t *testing.T) {
	sx := newSearXNGServer(t, []map[string]any{
		{"title": "Wholesale nursery price list", "url": "https://viveros.example/1", "content": "Average wholesale prices for cut stems, updated weekly"},
		{"title": "Woodworking channel uploads", "url": "https://clips.example/2", "content": "Short builds filmed in a small garage workshop"},
		{"title": "The sync package, end to end", "url": "https://gosnippets.example/3", "content": "sync.Mutex and sync.RWMutex explained"},
		{"title": "Locking in Go, with examples", "url": "https://gophernotes.example/4", "content": "sync.RWMutex allows many readers"},
	})
	fc := newFirecrawlServer(t)
	tools := NewSearchTools(sx.URL, fc.URL, 0)
	tools.MaxURLsPerQuery = 2

	got, err := tools.Search(context.Background(), "What are the trade-offs between sync.Mutex and sync.RWMutex in Go?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("fetched %d sources, want the 2-source budget: %+v", len(got.Findings), got.Findings)
	}
	for _, f := range got.Findings {
		if f.URL != "https://gosnippets.example/3" && f.URL != "https://gophernotes.example/4" {
			t.Errorf("budget spent on off-topic source %s", f.URL)
		}
	}
	// The noise must be reported, not silently dropped: a run that quietly
	// discards most of what it found looks identical to one that found little.
	if len(got.Skipped) != 2 {
		t.Fatalf("skipped %d off-topic results, want 2: %+v", len(got.Skipped), got.Skipped)
	}
	for _, s := range got.Skipped {
		if s.URL != "https://viveros.example/1" && s.URL != "https://clips.example/2" {
			t.Errorf("relevant source reported as off-topic: %s", s.URL)
		}
		if s.Domain == "" {
			t.Errorf("skipped source has no domain: %+v", s)
		}
	}
	if len(got.Signals) != len(got.Findings) {
		t.Errorf("signals no longer align with findings: %d vs %d", len(got.Signals), len(got.Findings))
	}
}

// Firecrawl fetches whatever URL a search result — or, in LLM-search mode, a
// model — hands it, from inside the network that runs it. Without a check on
// the target, search results are an input channel to the scraper's own
// loopback interface, its LAN and the cloud metadata service.
func TestScraperRefusesNonPublicTargets(t *testing.T) {
	var requested int
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested++
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"secret"}}`)
	}))
	defer fc.Close()
	client := &FirecrawlClient{BaseURL: fc.URL, HTTPClient: fc.Client(), ContentLimit: 1000}

	for _, target := range []string{
		"http://127.0.0.1/admin",
		"http://localhost:8080/",
		"http://[::1]/",
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://10.0.0.5/internal",
		"http://192.168.1.1/",
		"file:///etc/passwd",
		"gopher://example.com/",
		// Integer-encoded 127.0.0.1, and an intranet name a search engine
		// could never have returned.
		"http://2130706433/",
		"http://0x7f000001/",
		"http://jenkins/job/deploy",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := client.ScrapeURL(context.Background(), target)
			if err == nil {
				t.Fatalf("scraped %s", target)
			}
			if code := scrapeCode(err); code != "blocked" {
				t.Errorf("signal code %q, want \"blocked\" — a target that never reached the network has no HTTP status", code)
			}
		})
	}
	if requested != 0 {
		t.Errorf("%d blocked target(s) still reached the scraper", requested)
	}
}

// The filter must not touch ordinary pages: a public URL is scraped exactly
// as before.
func TestScraperStillFetchesPublicTargets(t *testing.T) {
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"page text","metadata":{"title":"Page"}}}`)
	}))
	defer fc.Close()
	client := &FirecrawlClient{BaseURL: fc.URL, HTTPClient: fc.Client(), ContentLimit: 1000}
	got, err := client.ScrapeURL(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatalf("public target refused: %v", err)
	}
	if got.Content != "page text" || got.Title != "Page" {
		t.Errorf("unexpected scrape result: %+v", got)
	}
}
