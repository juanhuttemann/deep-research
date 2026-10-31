package tools

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
			got, err := newSearchTools(sx.URL, fc.URL, 0).Search(context.Background(), "q", nil)
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
	tools := newSearchTools(sx.URL, fc.URL, 0)
	tools.MaxURLsPerQuery = 2

	got, err := tools.Search(context.Background(), "What are the trade-offs between sync.Mutex and sync.RWMutex in Go?",
		[]string{"Mutex", "RWMutex"})
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
		// Short and octal dotted forms: not an IP to net.ParseIP, but the
		// scraper's URL parser normalises each to a private address.
		"http://127.1/",
		"http://127.0.1/",
		"http://10.1/",
		"http://192.168.1/",
		"http://0177.0.0.1/",
		"http://127.0.0.0x1/",
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

// Firecrawl answers success:true with empty markdown for JS-only pages, PDFs
// and paywalls. The empty body replaced the search snippet and the source was
// labelled a clean fetch.
func TestBlankScrapeKeepsTheSnippet(t *testing.T) {
	sx := newSearXNGServer(t, []map[string]any{{"title": "T", "url": "https://example.com/js", "content": "Search snippet"}})
	defer sx.Close()
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"  \n","metadata":{"title":"Loading…","statusCode":200}}}`)
	}))
	defer fc.Close()
	got, err := newSearchTools(sx.URL, fc.URL, 0).Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Content != "Search snippet" || got.Findings[0].Title != "T" {
		t.Errorf("blank scrape replaced the snippet: %+v", got.Findings)
	}
	if len(got.Signals) != 1 || got.Signals[0].Status != "degraded" || got.Signals[0].Code != "empty" {
		t.Errorf("blank scrape signal = %+v, want degraded/empty", got.Signals)
	}
}

// Relevance was scored on the query's own words, split by an ASCII pattern and
// filtered through an English stopword list. A Japanese question glued to an
// English facet was judged on the facet's words alone, so every Japanese page
// was off-topic and the run came back empty; a query in Japanese alone yielded
// no terms and was never judged at all. Planner terms need no word boundaries.
func TestRelevanceJudgesCJKResults(t *testing.T) {
	in := []SearchResult{
		{Title: "東京の天気予報", URL: "https://weather.example/tokyo", Content: "今日は晴れ、明日は雨の予報です。"},
		{Title: "量子誤り訂正の現状", URL: "https://qc.example/qec", Content: "表面符号のしきい値と実機での課題を解説します。"},
	}
	kept, skipped := rankByRelevance(in, []string{"量子誤り訂正", "表面符号"})
	if len(kept) != 1 || kept[0].URL != "https://qc.example/qec" {
		t.Errorf("kept %+v, want only the error-correction page", kept)
	}
	if len(skipped) != 1 || skipped[0].URL != "https://weather.example/tokyo" {
		t.Errorf("skipped %+v, want the weather page", skipped)
	}
}

// The ASCII pattern split "energía" into "energ" and "España" into "espa",
// and page titles routinely drop accents altogether: a Spanish question
// matched its own sources on fragments, or not at all.
func TestRelevanceMatchesAcrossAccents(t *testing.T) {
	terms := []string{"energía nuclear", "España"}
	for _, title := range []string{
		"Cuales Son Las Ventajas De La Energia Nuclear En Espana",
		"ENERGÍA NUCLEAR EN ESPAÑA",
	} {
		if got := relevance(foldAll(terms), title, ""); got != 1 {
			t.Errorf("%q scored %.2f against %q, want 1", title, got, terms)
		}
	}
}

// A documentation page scraped to Markdown opened with ~5 KB of cookie dialog
// and navigation, and the prompt kept the page's opening: the model got the
// consent text and never the section that answered the query.
func TestExcerptReachesPastAPagesOpeningBoilerplate(t *testing.T) {
	cookie := "We use essential cookies and similar tools that are necessary to provide our site and services.\n\n"
	answer := "WidgetDB integrates with Application Auto Scaling to add and remove nodes automatically."
	page := strings.Repeat(cookie, 60) + "## Scaling\n\n" + answer + "\n\n" + strings.Repeat("See also the release notes.\n\n", 40)

	got := ExcerptFor(page, []string{"WidgetDB automatic node scaling"}, nil, 1500)
	if !strings.Contains(got, answer) {
		t.Errorf("excerpt missed the answering passage:\n%s", got)
	}
	if strings.Contains(got, "cookies") {
		t.Errorf("excerpt kept the cookie dialog:\n%s", got)
	}
	if len(got) > 1500 {
		t.Errorf("excerpt is %d bytes, over the 1500 limit", len(got))
	}
}

// A price from a pricing page's durability section reached the model as a
// price for every engine: the section heading ("Durability") and the sentence
// under it that names the one engine it applies to were cut away, and the
// report quoted it as a cost of another engine.
func TestExcerptKeepsThePassagesSectionScope(t *testing.T) {
	filler := strings.Repeat("General notes about the service and its console.\n\n", 30)
	page := filler +
		"On-demand nodes\n---------------\n\nEngineB nodes are billed per node-hour.\n\n" + filler +
		"Durability\n----------\n\n" +
		"Durability is a feature available with EngineA 9.0, allowing you to use the cache as a persistent" +
		" data store with microsecond read latency, and it is described at length in the guide for" +
		" operators who plan capacity for write-heavy workloads across several availability zones.\n\n" +
		"**Pricing**  \nSynchronous writes are priced at 18% on top of the standard node-hour price.\n\n" + filler

	got := ExcerptFor(page, []string{"EngineB node pricing synchronous writes"}, nil, 1500)
	if !strings.Contains(got, "18%") {
		t.Fatalf("excerpt missed the priced passage:\n%s", got)
	}
	if !strings.Contains(got, "Durability is a feature available with EngineA 9.0") {
		t.Errorf("the 18%% price came without the sentence naming its engine:\n%s", got)
	}
	if len(got) > 1500 {
		t.Errorf("excerpt is %d bytes, over the limit", len(got))
	}
}

// A page another search had already read was scraped again and took one of
// this search's slots, so the search came back one new page short.
func TestSearchSkipsPagesTheRunHasBeforeTheBudget(t *testing.T) {
	sx := newSearXNGServer(t, []map[string]any{
		{"url": "https://a.example/1", "title": "A", "content": "a"},
		{"url": "https://b.example/2", "title": "B", "content": "b"},
		{"url": "https://c.example/3", "title": "C", "content": "c"},
	})
	var mu sync.Mutex
	var scraped []string
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		scraped = append(scraped, req.URL)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"markdown": "body", "title": "T"}})
	}))
	defer fc.Close()

	st := newSearchTools(sx.URL, fc.URL, 5*time.Second)
	st.MaxURLsPerQuery = 2
	st.Skip = func(url string) bool { return url == "https://a.example/1" }
	res, err := st.Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(scraped)
	if !slices.Equal(scraped, []string{"https://b.example/2", "https://c.example/3"}) {
		t.Errorf("scraped %v, want the two pages the run does not have", scraped)
	}
	if len(res.Known) != 1 || res.Known[0].URL != "https://a.example/1" {
		t.Errorf("known = %+v, want the skipped page reported", res.Known)
	}
}

func TestTransientDiscoveryFailureDoesNotPoisonLaterQueries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"instances":{"https://search.example":{"network_type":"normal","http":{"status_code":200},"timing":{"search":{"success_percentage":100}}}}}`)
	}))
	defer srv.Close()
	c := NewSearXNGClient("auto", time.Second)
	c.discover = func(ctx context.Context) ([]string, error) { return DiscoverInstances(ctx, c.HTTPClient, srv.URL) }
	if _, err := c.candidates(context.Background()); err == nil {
		t.Fatalf("first discovery should fail")
	}
	for range 2 {
		got, err := c.candidates(context.Background())
		if err != nil || len(got) != 1 || got[0].url != "https://search.example" {
			t.Fatalf("later discovery: %v, %v", got, err)
		}
	}
	if attempts != 2 {
		t.Fatalf("discovery attempts = %d, want 2 with success cached", attempts)
	}
}

func TestCancelledDiscoveryDoesNotPoisonLaterQueries(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		c := NewSearXNGClient("auto", time.Second)
		attempts := 0
		c.discover = func(context.Context) ([]string, error) {
			attempts++
			if attempts == 1 {
				return nil, cause
			}
			return []string{"https://search.example"}, nil
		}
		if _, err := c.candidates(context.Background()); !errors.Is(err, cause) {
			t.Fatalf("lost discovery cancellation: %v", err)
		}
		if _, err := c.candidates(context.Background()); err != nil {
			t.Fatalf("later query still failed: %v", err)
		}
	}
}

func TestCanonicalURLDoesNotCountDefaultPortsAsDifferentSources(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTP://EXAMPLE.COM:80/x", "http://example.com/x"},
		{"HTTPS://EXAMPLE.COM:443/x", "https://example.com/x"},
		{"http://example.com:443/x", "http://example.com:443/x"},
		{"https://example.com:8443/x", "https://example.com:8443/x"},
		{"http://[2001:db8::1]:80/x", "http://[2001:db8::1]/x"},
		{"https://[2001:db8::1]:443/x", "https://[2001:db8::1]/x"},
		{"https://[2001:db8::1]:8443/x", "https://[2001:db8::1]:8443/x"},
	} {
		if got := CanonicalURL(tc.raw); got != tc.want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestFirecrawlTrailingSlashDoesNotProduceADoubledAPIPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/scrape" {
			t.Errorf("scrape path = %q, want /v2/scrape", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"page text","metadata":{"statusCode":200}}}`)
	}))
	defer srv.Close()
	for _, suffix := range []string{"", "/", "///"} {
		c := NewFirecrawlClient(srv.URL+suffix, time.Second)
		got, err := c.ScrapeURL(context.Background(), "https://example.com")
		if err != nil || got.Content != "page text" {
			t.Fatalf("suffix %q scrape: %+v, %v", suffix, got, err)
		}
	}
}

type queryDeadlineTransport func(*http.Request) (*http.Response, error)

func (f queryDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchFallbackDoesNotResetTheWholeQueryDeadline(t *testing.T) {
	c := NewSearXNGClient("https://first.example,https://second.example", time.Second)
	var deadlines []time.Time
	c.HTTPClient = &http.Client{Transport: queryDeadlineTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatalf("search request has no whole-query deadline")
		}
		deadlines = append(deadlines, deadline)
		status, body := http.StatusServiceUnavailable, "unavailable"
		if len(deadlines) == 2 {
			status, body = http.StatusOK, `{"results":[{"title":"Result","url":"https://source.example","content":"text"}]}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	got, err := c.Search(context.Background(), "q")
	if err != nil || len(got) != 1 {
		t.Fatalf("fallback search: %v, %v", got, err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("fallback reset query deadline: %v", deadlines)
	}
	if time.Until(deadlines[0]) > maxQueryTimeout {
		t.Fatalf("query exceeded its total budget: %v", deadlines[0])
	}
}

func TestSearchDiscoveryUsesTheShorterCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	c := NewSearXNGClient("auto", time.Second)
	c.discover = func(ctx context.Context) ([]string, error) {
		if got, ok := ctx.Deadline(); !ok || !got.Equal(want) {
			t.Fatalf("discovery deadline = %v, want caller deadline %v", got, want)
		}
		return nil, context.DeadlineExceeded
	}
	if _, err := c.Search(ctx, "q"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery lost timeout: %v", err)
	}
}

// truncateUTF8Bare was the one truncation helper without a guard on its
// limit, so the exported ExcerptFor sliced with a negative index and panicked
// instead of returning an empty excerpt.
func TestExcerptForNonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if got := ExcerptFor("", nil, nil, limit); got != "" {
			t.Errorf("ExcerptFor(limit %d) = %q, want empty", limit, got)
		}
	}
}

// allocated is how many bytes f allocates on the heap.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The fact-check excerpts every page by every claim. Each claim re-folded
// every block of the page, so building one prompt for a wide run (fifty
// 40 KB pages, thirty-six claims) took twelve seconds of CPU before the
// model call even started, silent and deaf to Esc. The page is folded once
// per excerpt, whatever the number of claims.
func TestExcerptFoldsThePageOncePerCall(t *testing.T) {
	var page strings.Builder
	for i := range 400 {
		fmt.Fprintf(&page, "Paragraph %d about Café latency, throughput and the replication lag of node %d.\n\n", i, i%7)
	}
	var claims []string
	for i := range 36 {
		claims = append(claims, fmt.Sprintf("Claim %d: replication lag on node %d stays under %d ms", i, i%7, i*10))
	}
	content := page.String()
	one := allocated(func() { ExcerptFor(content, claims[:1], nil, 1500) })
	all := allocated(func() { ExcerptFor(content, claims[:2], claims[2:], 1500) })
	// Folding per claim made 36 claims cost 36 times one; folded once per
	// excerpt, the claims add only their own scores.
	if all > 4*one {
		t.Errorf("an excerpt for %d claims allocated %d bytes, %.1f times one claim's %d",
			len(claims), all, float64(all)/float64(one), one)
	}
}

// gatedDiscovery is a discovery that signals started when an attempt
// begins and ends it with result once release is closed or its context
// ends. It counts its attempts.
type gatedDiscovery struct {
	attempts atomic.Int32
	started  chan struct{}
	release  chan struct{}
	result   func(n int32) ([]string, error)
}

func newGatedDiscovery(result func(n int32) ([]string, error)) *gatedDiscovery {
	return &gatedDiscovery{started: make(chan struct{}, 8), release: make(chan struct{}), result: result}
}

func (g *gatedDiscovery) discover(ctx context.Context) ([]string, error) {
	n := g.attempts.Add(1)
	g.started <- struct{}{}
	select {
	case <-g.release:
		return g.result(n)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// waitFor fails the test unless ch delivers within a second.
func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// candidates ran the searx.space discovery while holding the client's mutex,
// so concurrent sub-agent searches queued behind it and, when it failed,
// each ran its own discovery in turn: N queries cost N discoveries, back to
// back. Concurrent queries share one attempt and its outcome.
func TestConcurrentQueriesShareOneDiscovery(t *testing.T) {
	g := newGatedDiscovery(func(int32) ([]string, error) { return nil, errors.New("searx.space unavailable") })
	c := NewSearXNGClient("auto", time.Second)
	c.discover = g.discover
	errs := make(chan error, 3)
	search := func() { _, err := c.Search(context.Background(), "q"); errs <- err }
	go search()
	waitFor(t, g.started, "the first discovery")
	go search()
	go search()
	// The other two have nothing to do but reach the wait; give them the
	// time before the attempt they would share ends.
	time.Sleep(100 * time.Millisecond)
	close(g.release)
	for range 3 {
		if err := waitFor(t, errs, "a query"); err == nil {
			t.Error("search succeeded with no instance")
		}
	}
	if n := g.attempts.Load(); n != 1 {
		t.Errorf("3 concurrent queries ran %d discoveries, one after another; want 1 shared", n)
	}
}

// Sharing must not hand one query's cancellation to the others: a query
// whose own deadline ends its discovery leaves the queries waiting on it to
// discover again.
func TestACancelledDiscoveryIsNotSharedWithItsWaiters(t *testing.T) {
	g := newGatedDiscovery(func(int32) ([]string, error) { return []string{"https://search.example"}, nil })
	c := NewSearXNGClient("auto", time.Second)
	c.discover = g.discover
	short, cancel := context.WithCancel(context.Background())
	go func() { _, _ = c.candidates(short) }()
	waitFor(t, g.started, "the short query's discovery")
	got := make(chan error, 1)
	go func() { _, err := c.candidates(context.Background()); got <- err }()
	time.Sleep(50 * time.Millisecond) // the waiter reaches the wait
	cancel()
	waitFor(t, g.started, "the waiter's own discovery")
	close(g.release)
	if err := waitFor(t, got, "the waiting query"); err != nil {
		t.Errorf("the waiting query inherited another's cancellation: %v", err)
	}
}

// A query that waits for another's discovery waited on a sync.Mutex, which
// no deadline reaches: its own one-minute budget ran out while it was
// blocked. Waiting ends with the waiter's own context.
func TestAQueryWaitingOnDiscoveryKeepsItsOwnDeadline(t *testing.T) {
	g := newGatedDiscovery(func(int32) ([]string, error) { return nil, errors.New("released") })
	defer close(g.release)
	c := NewSearXNGClient("auto", time.Second)
	c.discover = g.discover
	go func() { _, _ = c.Search(context.Background(), "first") }()
	waitFor(t, g.started, "the first discovery")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Search(ctx, "second"); done <- err }()
	if err := waitFor(t, done, "the waiting query to keep its deadline"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting query ended with %v, want its own deadline", err)
	}
}

// The scraper was handed the model-call timeout, two minutes by default and
// more when configured, so a Firecrawl that accepts the connection and never
// answers held every scrape that long. Firecrawl is told how long it has,
// and the client gives up a little after it, whatever the model timeout.
func TestScrapeDeadlineDoesNotFollowTheModelCallTimeout(t *testing.T) {
	var body struct {
		Timeout int `json:"timeout"`
	}
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"page text","metadata":{"statusCode":200}}}`)
	}))
	defer fc.Close()
	var client time.Duration
	for _, model := range []time.Duration{2 * time.Minute, 10 * time.Minute} {
		got := newSearchTools("http://searx.invalid", fc.URL, model).Scraper.(*FirecrawlClient).HTTPClient.Timeout
		if got >= 2*time.Minute || (client != 0 && got != client) {
			t.Errorf("model timeout %v gave the scraper %v", model, got)
		}
		client = got
	}
	if _, err := NewFirecrawlClient(fc.URL, time.Hour).ScrapeURL(context.Background(), "https://example.com/page"); err != nil {
		t.Fatal(err)
	}
	// Firecrawl's default proxy mode reads exactly 30000 as "unset" and
	// raises it to two minutes.
	if body.Timeout == 30000 {
		t.Error("Firecrawl rewrites a 30000 ms timeout to 120000 under its default proxy")
	}
	if limit := time.Duration(body.Timeout) * time.Millisecond; limit <= 0 || limit >= client {
		t.Errorf("Firecrawl was given %v to scrape and the client %v: the server must give up first", limit, client)
	}
}

// challengeServer refuses every query the way public instances refused a
// real run: a challenge page, or ctq.ro's 418 with a cookie script.
func challengeServer(t *testing.T, teapot bool, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if teapot {
			w.WriteHeader(http.StatusTeapot)
			fmt.Fprint(w, `<script>document.cookie = "Yogsototh_opens_the_door=1; Path=/;"; window.location.reload();</script>`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Making sure you're not a bot!</title></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Once the public instances started challenging, every query walked all ten
// again and spent 37 seconds learning what the last query had: a sub-agent
// stalled for its whole deadline per search. An instance that refused now
// rests, and a query whose every instance is resting fails at once.
func TestRefusingInstancesAreNotAskedAgainAtOnce(t *testing.T) {
	var hits atomic.Int32
	a, b := challengeServer(t, false, &hits), challengeServer(t, true, &hits)
	now := time.Unix(0, 0)
	c := NewSearXNGClient(a+","+b, 0)
	c.now = func() time.Time { return now }

	if _, err := c.Search(context.Background(), "q"); err == nil {
		t.Fatal("a pool of challenges answered")
	}
	asked := hits.Load()
	_, err := c.Search(context.Background(), "q")
	if err == nil || hits.Load() != asked {
		t.Fatalf("second query asked %d more times (err %v), want resting instances skipped", hits.Load()-asked, err)
	}
	if SearchStatus(err) != "challenge" || !SearchResting(err) || !strings.HasPrefix(err.Error(), "not searched") {
		t.Errorf("a query of resting instances lost why they rest, or read as fresh failures: %v", err)
	}

	now = now.Add(refusalRest + time.Second)
	_, _ = c.Search(context.Background(), "q")
	if hits.Load() == asked {
		t.Error("an instance was never asked again after its rest")
	}
}

// The reader's own server has no other instance to move to: benching it
// would turn a restart into minutes of searches that never ask it.
func TestASingleInstanceNeverRests(t *testing.T) {
	var hits atomic.Int32
	c := NewSearXNGClient(challengeServer(t, false, &hits), 0)
	for range 2 {
		_, _ = c.Search(context.Background(), "q")
	}
	if hits.Load() < 2 {
		t.Errorf("a lone instance was asked %d times in two queries", hits.Load())
	}
}

// Ten instances' errors joined made one event line of URLs, challenge
// scripts and timeouts. The run's line counts the failures by kind instead,
// and only the public pool's refusal reads as public.
func TestPoolFailureIsCountedNotDumped(t *testing.T) {
	var hits atomic.Int32
	a, b, d := challengeServer(t, false, &hits), challengeServer(t, false, &hits), challengeServer(t, true, &hits)

	listed := NewSearXNGClient(a+","+b+","+d, 0)
	_, err := listed.Search(context.Background(), "q")
	if err == nil {
		t.Fatal("a pool of challenges answered")
	}
	msg := err.Error()
	if msg != "all 3 SearXNG instances failed: 3 challenged" {
		t.Errorf("pool error = %q", msg)
	}
	if PublicSearchRefused(err) {
		t.Error("the reader's own instances were reported as the public pool")
	}

	auto := NewSearXNGClient("auto", 0)
	auto.discover = func(context.Context) ([]string, error) { return []string{a, b, d}, nil }
	_, err = auto.Search(context.Background(), "q")
	if !PublicSearchRefused(err) || !strings.Contains(err.Error(), "public SearXNG") {
		t.Errorf("public pool refusal not recognised: %v", err)
	}
	if strings.Contains(err.Error(), "<script") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("pool error still dumps each instance: %q", err)
	}
}

// resultsServer answers every query with one SearXNG result page titled
// title, counting the queries it gets.
func resultsServer(t *testing.T, title string, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><div id="urls"><article class="result"><h3><a href="https://%s.example/">%s</a></h3>`+
			`<p class="content">%s</p></article></div></body></html>`, strings.ReplaceAll(title, " ", ""), title, title)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// One public instance answered every query with results for its first word
// alone — ruby hotels for "Ruby on Rails …", chatbot homepages for "AI …" —
// and, counted as an answer, was asked first every time. An answer with
// nothing on the subject now rests the instance like a refusal, and the
// search's line names the instance, so junk can be traced to where it came
// from.
func TestAnInstanceAnsweringOffTopicIsRested(t *testing.T) {
	var junkHits, goodHits atomic.Int32
	junk := resultsServer(t, "Ruby Hotels Berlin", &junkHits)
	good := resultsServer(t, "Rails 8 release notes", &goodHits)
	var lines []string
	st := &SearchTools{Engine: NewSearXNGClient(junk+","+good, 0), MaxURLsPerQuery: 5, ScrapeParallelism: 1}
	st.SetLogger(func(s string) { lines = append(lines, s) })

	res, err := st.Search(context.Background(), "Ruby on Rails 2026", []string{"Rails", "2026"})
	if err != nil || len(res.Findings) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("first search: findings %d skipped %d err %v", len(res.Findings), len(res.Skipped), err)
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "1 results from "+DomainOf(junk)) {
		t.Errorf("search line does not name the instance: %q", lines)
	}
	asked := junkHits.Load()
	res, err = st.Search(context.Background(), "Ruby on Rails 2026", []string{"Rails", "2026"})
	if err != nil || len(res.Findings) != 1 {
		t.Fatalf("second search: findings %v err %v", res, err)
	}
	if junkHits.Load() != asked {
		t.Errorf("the off-topic instance was asked again (%d more requests)", junkHits.Load()-asked)
	}
}

// A query whose deadline ended the walk said "all 10 SearXNG instances
// failed" over the few it had asked.
func TestPoolFailureCutShortSaysHowManyWereAsked(t *testing.T) {
	e := &poolError{total: 10, errs: []error{errChallenge, errChallenge, context.DeadlineExceeded}}
	if got := e.Error(); !strings.HasPrefix(got, "3 of 10 SearXNG instances failed before the query's deadline") {
		t.Errorf("pool error = %q", got)
	}
}

// Exa answered the probe for pkg.go.dev with its home page first, 165
// characters of navigation, and the site was dropped as unreadable although
// every other page on it was text. A site is judged on several of its pages.
func TestDomainCheckTriesMoreThanTheFirstPage(t *testing.T) {
	eng := &recordingEngine{results: []SearchResult{
		{URL: "https://pkg.go.dev/", Body: "Search packages"},
		{URL: "https://pkg.go.dev/fmt", Body: strings.Repeat("Package fmt implements formatted I/O. ", 20)},
	}}
	got := (&SearchTools{Engine: eng}).CheckDomains(context.Background(), []string{"pkg.go.dev"})[0]
	if got.Status != DomainOK || got.Sample != "https://pkg.go.dev/fmt" {
		t.Errorf("check = %+v, want ok on the second page", got)
	}
}

// Tavily sent one site's results without their page text, and the check
// said "no scraper configured", which on Tavily reads as a setup mistake: it
// needs none. It names the engine that sent no text.
func TestSnippetOnlyOnAHostedEngineBlamesTheEngine(t *testing.T) {
	eng := &recordingEngine{name: EngineTavily, results: []SearchResult{{URL: "https://research.swtch.com/x", Content: "snippet"}}}
	got := (&SearchTools{Engine: eng}).CheckDomains(context.Background(), []string{"research.swtch.com"})[0]
	if got.Status != DomainSnippetOnly || !strings.Contains(got.Detail, "tavily sent no page text") {
		t.Errorf("check = %+v", got)
	}
}

// flakyEngine answers its first query empty, as SearXNG does when an
// upstream engine throttles a burst, and every later one with results.
type flakyEngine struct {
	mu    sync.Mutex
	calls int
	res   []SearchResult
}

func (f *flakyEngine) Name() string { return EngineSearXNG }

func (f *flakyEngine) Search(context.Context, string) ([]SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return nil, nil
	}
	return f.res, nil
}

// Three probes at once had Google CSE throttle one, SearXNG answered it with
// no results, and pkg.go.dev was dropped as unindexed. An empty probe asks
// again before the site is judged.
func TestEmptyProbeIsAskedAgainBeforeTheSiteIsUnindexed(t *testing.T) {
	defer func(w time.Duration) { probeRetryWait = w }(probeRetryWait)
	probeRetryWait = 0
	eng := &flakyEngine{res: []SearchResult{{URL: "https://pkg.go.dev/fmt", Content: "snippet"}}}
	got := (&SearchTools{Engine: eng}).CheckDomains(context.Background(), []string{"pkg.go.dev"})[0]
	if got.Status != DomainSnippetOnly || eng.calls != 2 {
		t.Errorf("check = %+v after %d searches, want it found on the second", got, eng.calls)
	}
}

// After a burst of searches got its Google engine suspended, a local SearXNG
// answered every query with a 200, no results and the engines that failed.
// Taken as an empty answer, it had every site of a --domains list reported
// as one no engine indexes. It is a search failure that names the engines.
func TestSearXNGEmptyAnswerWithFailedEnginesIsAnError(t *testing.T) {
	body := `{"results":[],"unresponsive_engines":[["google cse","Suspended: too many requests"],["duckduckgo","CAPTCHA"]]}`
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(sv.Close)
	_, err := NewSearXNGClient(sv.URL, 0).Search(context.Background(), "go.dev site:go.dev")
	if err == nil || !strings.Contains(err.Error(), "google cse (Suspended: too many requests)") {
		t.Fatalf("err = %v, want the failed engines named", err)
	}
	if s := SearchStatus(err); s != "rate-limited" {
		t.Errorf("status = %q, want rate-limited", s)
	}
	st := newSearchTools(sv.URL, "", 0)
	if c := st.CheckDomains(context.Background(), []string{"go.dev"})[0]; c.Status != DomainSearchFail {
		t.Errorf("domain check = %+v, want search-failed, not unindexed", c)
	}

	// No results with every engine answering is an empty answer, as before.
	body = `{"results":[],"unresponsive_engines":[]}`
	if res, err := NewSearXNGClient(sv.URL, 0).Search(context.Background(), "q"); err != nil || len(res) != 0 {
		t.Errorf("an honest empty answer = %v, %v", res, err)
	}
}

// A snippet kept a site in the check only when no scraper was configured, so
// adding Firecrawl dropped every site it was refused on, although a run keeps
// a refused page's snippet as a degraded source.
func TestScraperRefusalKeepsASiteWithSnippets(t *testing.T) {
	eng := &recordingEngine{results: []SearchResult{{URL: "https://walled.com/a", Content: "snippet"}}}
	sc := pageScraper{"https://walled.com/a": &httpError{Code: http.StatusForbidden}}
	got := (&SearchTools{Engine: eng, Scraper: sc}).CheckDomains(context.Background(), []string{"walled.com"})[0]
	if got.Status != DomainSnippetOnly || !strings.Contains(got.Detail, "403") {
		t.Errorf("check = %+v, want snippet-only naming the refusal", got)
	}
}

// Public instances are read through their HTML page, which says its engines
// failed in the engines_msg table rather than in JSON. Only the JSON form was
// read, so an instance whose Google was suspended passed doctor with "0
// results" and answered a run with nothing. The page is SearXNG's simple
// theme with its engines down: the empty urls list, the no-results dialog
// and one failed and one answering engine.
func TestSearXNGEmptyPageWithFailedEnginesIsAnError(t *testing.T) {
	page := `<html><body><div id="results"><div id="sidebar"><div id="engines_msg"><details open>` +
		`<table class="engine-stats" id="engines_msg-table">` +
		`<tr><td class="engine-name"><a href="/stats?engine=google">google</a></td><td class="response-error">Suspended: too many requests</td></tr>` +
		`<tr><td class="engine-name"><a href="/stats?engine=wikipedia">wikipedia</a></td><td class="response-time"><div class="bar-chart-value">0.4</div></td></tr>` +
		`</table></details></div></div><div id="urls" role="main"><div class="dialog-error-block" role="alert">` +
		`<p><strong>Sorry!</strong></p><p>No results were found.</p></div></div></div></body></html>`
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			w.WriteHeader(http.StatusTooManyRequests) // as public instances answer JSON
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(sv.Close)
	_, err := NewSearXNGClient(sv.URL, 0).Search(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "google (Suspended: too many requests)") || strings.Contains(err.Error(), "wikipedia") {
		t.Fatalf("err = %v, want only the failed engine named", err)
	}
	if s := SearchStatus(err); s != "rate-limited" {
		t.Errorf("status = %q, want rate-limited", s)
	}

	// The same page with every engine answering is an honest empty answer.
	page = strings.Replace(page, `<td class="response-error">Suspended: too many requests</td>`, `<td class="response-time">0.2</td>`, 1)
	if res, err := NewSearXNGClient(sv.URL, 0).Search(context.Background(), "q"); err != nil || len(res) != 0 {
		t.Errorf("an honest empty page = %v, %v", res, err)
	}
}

// Each page's outcome was logged here as well as reported by the run, so the
// activity tail showed "ok en.wikipedia.org" under the row that already said
// so, and a page fetched past the budget, never used, got a line too. The
// search's own line is the only one left.
func TestSearchLogsNoLinePerPage(t *testing.T) {
	sx := newSearXNGServer(t, []map[string]any{
		{"title": "A", "url": "https://a.example/test", "content": "test query a", "engine": "google", "score": 1.0},
		{"title": "B", "url": "https://b.example/test", "content": "test query b", "engine": "bing", "score": 0.9},
		{"title": "C", "url": "https://c.example/known", "content": "test query c", "engine": "bing", "score": 0.8},
	})
	st := newSearchTools(sx.URL, newFirecrawlServer(t).URL, 0)
	st.Skip = func(url string) bool { return strings.Contains(url, "known") }
	var mu sync.Mutex
	var lines []string
	st.SetLogger(func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, msg)
	})
	if _, err := st.Search(context.Background(), "test query", nil); err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	for _, l := range lines {
		if strings.Contains(l, ".example") {
			t.Errorf("logged a page: %q", l)
		}
	}
}

// An answer with no results was a success: the instance was preferred and
// asked first for every later query, and a healthy instance behind it was
// never asked, so a whole run found nothing.
func TestEmptyAnswerDoesNotWinThePool(t *testing.T) {
	empty := newSearXNGServer(t, nil)
	var goodHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"url": "https://a.example/", "title": "A", "content": "c"}}})
	}))
	t.Cleanup(good.Close)
	c := NewSearXNGClient(empty.URL+","+good.URL, 0)
	for range 3 {
		res, err := c.Search(context.Background(), "q")
		if err != nil || len(res) != 1 {
			t.Fatalf("Search = %d results, %v; want the healthy instance's one", len(res), err)
		}
	}
	if c.Preferred() != good.URL {
		t.Errorf("preferred %q, want the instance with results", c.Preferred())
	}
	alone := NewSearXNGClient(empty.URL+","+empty.URL+"/x", 0)
	if res, err := alone.Search(context.Background(), "q"); err != nil || len(res) != 0 {
		t.Errorf("all empty = %d results, %v; want an empty answer", len(res), err)
	}
}

// A model writes URLs without their scheme, and url.Parse files all of it
// under Path: "go.dev/doc/x" was its own identity beside the fetched
// https://go.dev/doc/x, and a claim citing it lost its source.
func TestCanonicalURLReadsASchemelessURLAsTheFetchedPage(t *testing.T) {
	want := CanonicalURL("https://go.dev/doc/x")
	for _, raw := range []string{"go.dev/doc/x", "go.dev/doc/x/", "//go.dev/doc/x", " GO.dev/doc/x"} {
		if got := CanonicalURL(raw); got != want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"not a url", "/relative/path", ""} {
		if got := CanonicalURL(raw); got != strings.TrimRight(raw, "/") {
			t.Errorf("CanonicalURL(%q) = %q, want it as written", raw, got)
		}
	}
}

// An international name was kept as typed, and engines return it as
// punycode: no page of bücher.de was ever in its own scope.
func TestInternationalDomainMatchesItsPunycodePages(t *testing.T) {
	ds, err := ParseDomains([]string{"bücher.de"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://xn--bcher-kva.de/x", "https://www.bücher.de/y"} {
		if !InDomains(u, ds) {
			t.Errorf("InDomains(%q, %q) = false", u, ds)
		}
	}
	if InDomains("https://example.de/", ds) {
		t.Error("another site matched")
	}
}

// Whether to read an instance through HTML was looked at again after its
// JSON answer: a refusal another query got meanwhile threw this query's
// good JSON answer away for an HTML request, which could fail it.
func TestJSONAnswerSurvivesAConcurrentRefusal(t *testing.T) {
	release := make(chan struct{})
	var htmlForA atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, asJSON := r.URL.Query().Get("q"), r.URL.Query().Get("format") == "json"
		switch {
		case q == "b" && asJSON:
			w.WriteHeader(http.StatusForbidden)
		case q == "a" && asJSON:
			<-release
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"url": "https://a.example/", "title": "A", "content": "c"}}})
		default:
			if q == "a" {
				htmlForA.Add(1)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewSearXNGClient(srv.URL, 0)
	done := make(chan error, 1)
	go func() {
		res, err := c.Search(context.Background(), "a")
		if err == nil && len(res) != 1 {
			err = fmt.Errorf("%d results", len(res))
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_, _ = c.Search(context.Background(), "b") // refuses JSON: the instance is read through HTML from now on
	close(release)
	if err := <-done; err != nil || htmlForA.Load() != 0 {
		t.Errorf("query a: %v after %d HTML requests; want its JSON answer", err, htmlForA.Load())
	}
}

// A body past the read limit failed the decode whole, though each page is
// cut to its limit once decoded: one huge page lost a search all its
// results. Strings are cut on the way in, and the document stays whole.
func TestOversizedPageTextIsCutNotFatal(t *testing.T) {
	huge := strings.Repeat("é\\\"x", 1<<20)
	doc := `{"results":[{"url":"https://a.example/","raw_content":"` + huge + `"},{"url":"https://b.example/","raw_content":"short"}]}`
	var out struct {
		Results []struct {
			URL        string `json:"url"`
			RawContent string `json:"raw_content"`
		} `json:"results"`
	}
	// The limit is far below the document, as a body past it was.
	if err := json.NewDecoder(cappedJSON(strings.NewReader(doc), 1000, 4096)).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 2 || out.Results[1].RawContent != "short" || len(out.Results[0].RawContent) > 1000 {
		t.Errorf("results = %d, first %d bytes, second %q", len(out.Results), len(out.Results[0].RawContent), out.Results[1].RawContent)
	}
	if !strings.HasPrefix(out.Results[0].RawContent, "é\"x") {
		t.Errorf("cut text starts %q", out.Results[0].RawContent[:10])
	}
}

// IsPrivate leaves out shared address space, where carrier NAT and a
// tailnet's machines sit: a model-supplied http://100.101.102.103/ was
// scraped though only a public address may be.
func TestScrapeTargetRefusesSharedAndReservedAddresses(t *testing.T) {
	for _, u := range []string{"http://100.101.102.103/", "http://192.0.0.8/", "http://198.18.0.1/", "http://240.0.0.1/", "http://[::ffff:100.64.0.1]/"} {
		if err := checkScrapeTarget(u); err == nil {
			t.Errorf("checkScrapeTarget(%q) passed", u)
		}
	}
	if err := checkScrapeTarget("http://100.0.0.1/"); err != nil {
		t.Errorf("a public address was refused: %v", err)
	}
}

// Searching a run's pages built a transformer per page per question; the
// scorer built once for the question judges every page as before.
func TestPageScorerJudgesAsASearchResult(t *testing.T) {
	sc := NewPageScorer("énergie nucléaire coûts")
	for _, page := range []string{"Les coûts de l'énergie nucléaire — en hausse", "nothing here", "ENERGIE nucleaire couts", ""} {
		if got, want := sc.Score(page), relevance(queryWords(newFolder(), "énergie nucléaire coûts"), "", page); got != want {
			t.Errorf("Score(%q) = %v, want %v", page, got, want)
		}
	}
}
