package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// htmlResultsPage is SearXNG's own result markup (simple theme), trimmed.
const htmlResultsPage = `<!doctype html><html><body><div id="results"><div id="urls">
<article class="result result-default category-general"><div class="result_inner">
<a href="https://go.dev/" class="url_header"><div class="url_wrapper"><span>https://go.dev</span></div></a>
<h3><a href="https://go.dev/" target="_blank">The Go <span class="highlight">Programming</span> Language</a></h3>
<p class="content">
    Get Started Playground Tour &amp; more
  </p></div></article>
<article class="result result-default"><h3><a href="https://pkg.go.dev/sync">sync package</a></h3></article>
</div></div></body></html>`

// Public instances almost never enable format=json (it is off by default), and
// the client could only read JSON, so every public instance was unusable.
func TestSearXNGFallsBackToHTMLResults(t *testing.T) {
	var jsonAsks, htmlAsks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			jsonAsks.Add(1)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		htmlAsks.Add(1)
		if r.Header.Get("Accept-Language") == "" {
			t.Error("HTML request sent no Accept-Language; SearXNG's bot detection rejects that")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, htmlResultsPage)
	}))
	defer srv.Close()
	c := NewSearXNGClient(srv.URL, 0)
	for range 2 {
		got, err := c.Search(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("parsed %d results, want 2: %+v", len(got), got)
		}
		if got[0].URL != "https://go.dev/" || got[0].Title != "The Go Programming Language" ||
			got[0].Content != "Get Started Playground Tour & more" {
			t.Errorf("first result = %+v", got[0])
		}
		if got[1].URL != "https://pkg.go.dev/sync" || got[1].Content != "" {
			t.Errorf("second result = %+v", got[1])
		}
	}
	if jsonAsks.Load() != 1 || htmlAsks.Load() != 2 {
		t.Errorf("json asks=%d html asks=%d: an instance that refused JSON once should not be asked again",
			jsonAsks.Load(), htmlAsks.Load())
	}
}

// A bot-challenge page answers 200 text/html with no results markup. Read as
// "no results" it made a blocked instance look like an empty search.
func TestSearXNGChallengePageIsNotAnEmptySearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Making sure you're not a bot!</title></html>`)
	}))
	defer srv.Close()
	_, err := NewSearXNGClient(srv.URL, 0).Search(context.Background(), "q")
	if err == nil {
		t.Fatal("a challenge page was accepted as a search result")
	}
	if got := SearchStatus(err); got != "challenge" {
		t.Errorf("SearchStatus = %q, want challenge (err: %v)", got, err)
	}
}

// With several instances, one that rate-limits must not end the search, and
// the one that answered is asked first next time.
func TestSearXNGRotatesPastALimitedInstance(t *testing.T) {
	var limitedHits atomic.Int32
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitedHits.Add(1)
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	}))
	defer limited.Close()
	good := newSearXNGServer(t, []map[string]any{{"title": "T", "url": "https://a.example", "content": "c"}})

	c := NewSearXNGClient(limited.URL+", "+good.URL, 0)
	for range 3 {
		got, err := c.Search(context.Background(), "q")
		if err != nil || len(got) != 1 {
			t.Fatalf("results=%+v err=%v", got, err)
		}
	}
	// JSON then HTML on the limited instance, once; after that it is skipped.
	if n := limitedHits.Load(); n > 2 {
		t.Errorf("the limited instance was asked %d times, want it tried once per failure", n)
	}
}

// When every instance refuses, the error says how: a limiter is a state the
// run reports, not an info line.
func TestSearchStatusClassifiesRefusals(t *testing.T) {
	for code, want := range map[int]string{429: "rate-limited", 403: "blocked", 418: "challenge", 500: "unavailable"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		_, err := NewSearXNGClient(srv.URL, 0).Search(context.Background(), "q")
		srv.Close()
		if got := SearchStatus(err); got != want {
			t.Errorf("status %d: SearchStatus = %q, want %q (err: %v)", code, got, want, err)
		}
	}
	if got := SearchStatus(nil); got != "" {
		t.Errorf("SearchStatus(nil) = %q", got)
	}
}

// searx.space lists candidates, not working instances: only the normal-network,
// reachable, analytics-free ones with a search success rate are worth a probe.
func TestDiscoverInstancesFiltersTheCandidateList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"instances":{
"https://good.example/":{"network_type":"normal","http":{"status_code":200},"timing":{"search":{"success_percentage":90}}},
"https://best.example/":{"network_type":"normal","http":{"status_code":200},"timing":{"search":{"success_percentage":100}}},
"http://onion.example/":{"network_type":"tor","http":{"status_code":200},"timing":{"search":{"success_percentage":100}}},
"https://down.example/":{"network_type":"normal","http":{"status_code":502},"timing":{"search":{"success_percentage":100}}},
"https://never.example/":{"network_type":"normal","http":{"status_code":200},"timing":{"search":{"success_percentage":0}}},
"https://tracks.example/":{"network_type":"normal","http":{"status_code":200},"analytics":true,"timing":{"search":{"success_percentage":100}}}
}}`)
	}))
	defer srv.Close()
	got, err := DiscoverInstances(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "https://best.example,https://good.example" {
		t.Errorf("candidates = %q", got)
	}
}

// Web search needed both SearXNG and Firecrawl; with only SearXNG the run fell
// back to the model. Snippets alone are real search results, labelled as such.
func TestSearchWithoutAScraperKeepsSnippets(t *testing.T) {
	sx := newSearXNGServer(t, []map[string]any{
		{"title": "T", "url": "https://a.example/x", "content": "snippet"},
		{"title": "U", "url": "https://b.example/y", "content": ""},
	})
	got, err := NewSearchTools(sx.URL, "", 0).Search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Signals) != 2 || got.Signals[0].Status != "degraded" || got.Signals[0].Code != "noservice" {
		t.Errorf("signals = %+v, want snippet-only sources marked noservice", got.Signals)
	}
	if got.Signals[1].Status != "dropped" {
		t.Errorf("a result with no snippet and no scraper = %+v, want dropped", got.Signals[1])
	}
}
