package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newHostedServer serves fixed JSON answers by path, recording each request
// body and the headers the client sent.
func newHostedServer(t *testing.T, status int, answers map[string]string) (*httptest.Server, *[]map[string]any, *http.Header) {
	t.Helper()
	var bodies []map[string]any
	var hdr http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		hdr = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, answers[r.URL.Path])
	}))
	t.Cleanup(server.Close)
	return server, &bodies, &hdr
}

func TestExaSearchCarriesTextAsBody(t *testing.T) {
	sv, bodies, hdr := newHostedServer(t, http.StatusOK, map[string]string{"/search": `{"results":[
		{"url":"https://go.dev/blog/range-functions","title":"Range","text":"full page","highlights":["a","b"]},
		{"url":"https://x.example","title":"X","text":"only text here"}]}`})
	got, err := NewExaClient(sv.URL, "k", 0).Search(context.Background(), "range funcs")
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("x-api-key") != "k" {
		t.Errorf("key not sent in x-api-key: %v", *hdr)
	}
	if q := (*bodies)[0]["query"]; q != "range funcs" {
		t.Errorf("query = %v", q)
	}
	if len(got) != 2 || got[0].Body != "full page" || got[0].Content != "a … b" || got[0].Engine != "exa" {
		t.Fatalf("results = %+v", got)
	}
	// Without highlights the ranker still needs something to score.
	if got[1].Content != "only text here" {
		t.Errorf("snippet fallback = %q", got[1].Content)
	}
}

func TestTavilySearchCarriesRawContentAsBody(t *testing.T) {
	sv, bodies, hdr := newHostedServer(t, http.StatusOK, map[string]string{"/search": `{"results":[
		{"url":"https://a.example","title":"A","content":"snippet","raw_content":"# A\npage","score":0.8}]}`})
	c := NewTavilyClient(sv.URL, "tvly-k", 0)
	c.ContentLimit = 4
	got, err := c.Search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("Authorization") != "Bearer tvly-k" {
		t.Errorf("Authorization = %q", hdr.Get("Authorization"))
	}
	if (*bodies)[0]["include_raw_content"] != "markdown" || (*bodies)[0]["search_depth"] != "basic" {
		t.Errorf("request = %v", (*bodies)[0])
	}
	if len(got) != 1 || got[0].Content != "snippet" || got[0].Body != "# A\n..." || got[0].Score != 0.8 {
		t.Fatalf("results = %+v", got)
	}
}

// A wrong key is a state the run reports by name: retrying, or trying
// another engine, will not fix it.
func TestHostedRejectedKeyIsUnauthorized(t *testing.T) {
	sv, _, _ := newHostedServer(t, http.StatusUnauthorized, map[string]string{"/search": `{"error":"Invalid API key"}`})
	for _, e := range []Searcher{NewExaClient(sv.URL, "bad", 0), NewTavilyClient(sv.URL, "bad", 0)} {
		_, err := e.Search(context.Background(), "q")
		if SearchStatus(err) != "unauthorized" {
			t.Errorf("%T: status %q for %v", e, SearchStatus(err), err)
		}
	}
	sv2, _, _ := newHostedServer(t, tavilyPlanLimit, nil)
	if _, err := NewTavilyClient(sv2.URL, "k", 0).Search(context.Background(), "q"); SearchStatus(err) != "quota" {
		t.Errorf("Tavily's plan limit read as %q", SearchStatus(err))
	}
}

// Both APIs answer 200 when the page itself failed; a 404 has to reach the
// source signal as a 404, the way Firecrawl's target status does.
func TestHostedExtractReportsThePageStatus(t *testing.T) {
	exa, _, _ := newHostedServer(t, http.StatusOK, map[string]string{"/contents": `{"results":[],
		"statuses":[{"id":"u","status":"error","error":{"httpStatusCode":404,"tag":"CRAWL_NOT_FOUND"}}]}`})
	tav, _, _ := newHostedServer(t, http.StatusOK, map[string]string{"/extract": `{"results":[],
		"failed_results":[{"url":"u","error":"404 page not found"}]}`})
	for _, s := range []Scraper{NewExaClient(exa.URL, "k", 0), NewTavilyClient(tav.URL, "k", 0)} {
		_, err := s.ScrapeURL(context.Background(), "https://a.example/gone")
		if scrapeCode(err) != "404" {
			t.Errorf("%T: code %q for %v", s, scrapeCode(err), err)
		}
	}
	tav2, _, _ := newHostedServer(t, http.StatusOK, map[string]string{"/extract": `{"results":[],
		"failed_results":[{"url":"u","error":"Failed to fetch url"}]}`})
	if _, err := NewTavilyClient(tav2.URL, "k", 0).ScrapeURL(context.Background(), "https://a.example/"); scrapeCode(err) != "scrape_failed" {
		t.Errorf("an unreadable page = %q", scrapeCode(err))
	}
}

func TestHostedExtractReadsThePage(t *testing.T) {
	exa, _, _ := newHostedServer(t, http.StatusOK, map[string]string{"/contents": `{"results":[{"url":"u","title":"T","text":"exa text"}],"statuses":[{"status":"success"}]}`})
	tav, _, _ := newHostedServer(t, http.StatusOK, map[string]string{"/extract": `{"results":[{"url":"u","raw_content":"tavily text"}]}`})
	for s, want := range map[Scraper]string{NewExaClient(exa.URL, "k", 0): "exa text", NewTavilyClient(tav.URL, "k", 0): "tavily text"} {
		got, err := s.ScrapeURL(context.Background(), "https://a.example/")
		if err != nil || got.Content != want {
			t.Errorf("%T: %+v, %v", s, got, err)
		}
	}
}

// A hosted extractor is outside the user's network, but the target check is
// cheap and the same scraper interface may front a self-hosted service.
func TestHostedExtractRefusesPrivateTargets(t *testing.T) {
	var calls atomic.Int32
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer sv.Close()
	for _, s := range []Scraper{NewExaClient(sv.URL, "k", 0), NewTavilyClient(sv.URL, "k", 0)} {
		if _, err := s.ScrapeURL(context.Background(), "http://169.254.169.254/latest/meta-data"); !errors.Is(err, errBlockedTarget) {
			t.Errorf("%T: %v", s, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("a blocked target reached the service %d times", calls.Load())
	}
}

// countingScraper records how often it is asked.
type countingScraper struct{ calls atomic.Int32 }

func (c *countingScraper) Name() string { return "counting" }

func (c *countingScraper) ScrapeURL(_ context.Context, u string) (*ScrapedContent, error) {
	c.calls.Add(1)
	return &ScrapedContent{URL: u, Content: "scraped"}, nil
}

type fixedEngine []SearchResult

func (f fixedEngine) Name() string { return "fixed" }

func (f fixedEngine) Search(context.Context, string) ([]SearchResult, error) { return f, nil }

// A SearXNG instance behind basic auth answers 401 as well; it has no key of
// the reader's, so it is not reported as one.
func TestSearXNG401IsNotAKeyProblem(t *testing.T) {
	sx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer sx.Close()
	_, err := NewSearXNGClient(sx.URL, 0).Search(context.Background(), "q")
	if st := SearchStatus(err); st == "unauthorized" || st == "quota" {
		t.Errorf("a SearXNG 401 read as %q", st)
	}
}

// Go keeps a custom header like Exa's x-api-key on a redirect to another
// host, so no redirect is followed with the key on it.
func TestHostedKeyIsNotSentThroughARedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("x-api-key") != "")
	}))
	defer other.Close()
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/search", http.StatusTemporaryRedirect)
	}))
	defer sv.Close()
	if _, err := NewExaClient(sv.URL, "secret", 0).Search(context.Background(), "q"); err == nil {
		t.Error("a redirect read as a search")
	}
	if leaked.Load() {
		t.Error("the key followed the redirect")
	}
}

// The point of inline text: a result that brought its page is not fetched
// again, and one that did not still goes to the scraper.
func TestInlineBodySkipsTheScraper(t *testing.T) {
	sc := &countingScraper{}
	st := &SearchTools{Engine: fixedEngine{
		{URL: "https://a.example/1", Title: "one", Content: "snip", Body: "inline page"},
		{URL: "https://b.example/2", Title: "two", Content: "snip"},
	}, Scraper: sc, MaxURLsPerQuery: 5, ScrapeParallelism: 2}
	res, err := st.Search(context.Background(), "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sc.calls.Load() != 1 {
		t.Errorf("scraper called %d times, want 1", sc.calls.Load())
	}
	want := map[string]string{"https://a.example/1": "inline page", "https://b.example/2": "scraped"}
	for i, f := range res.Findings {
		if f.Content != want[f.URL] || res.Signals[i].Status != "ok" {
			t.Errorf("%s: content %q status %q", f.URL, f.Content, res.Signals[i].Status)
		}
	}
}

func TestEngineNeedsItsKey(t *testing.T) {
	for _, o := range []Options{{Engine: "exa"}, {Engine: "tavily"}} {
		if _, err := NewEngine(o, 0); !errors.Is(err, errNoKey) {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if _, err := NewScraper(Options{Scraper: "exa"}, 0); !errors.Is(err, errNoKey) {
		t.Errorf("exa scraper without a key: %v", err)
	}
	if _, err := NewEngine(Options{Engine: "bing"}, 0); err == nil || !strings.Contains(err.Error(), "bing") {
		t.Errorf("unknown engine: %v", err)
	}
	if s, err := NewScraper(Options{}, 0); s != nil || err != nil {
		t.Errorf("no scraper configured: %v, %v", s, err)
	}
}
