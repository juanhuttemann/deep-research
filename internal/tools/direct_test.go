package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectExtractionDoesNotRequireAService(t *testing.T) {
	s, err := NewScraper(Options{Scraper: "direct"}, 0)
	if err != nil || s == nil {
		t.Fatalf("built-in extraction unavailable: %v", err)
	}
}

func TestHTMLExtractionKeepsCorrectionAndTableContext(t *testing.T) {
	p, err := extractHTML(`<html><head><title>Write-Ahead Logging - SQLite</title></head><body><nav>Ignore navigation</nav><main><h2>Overview</h2><p><s>For transactions in excess of a gigabyte, WAL mode may fail.</s> Beginning with version 3.11.0, WAL mode works as efficiently with large transactions as rollback mode.</p><h2>Measurements</h2><table><tr><th>Region</th><th>Share</th></tr><tr><td>Europe</td><td>25%</td></tr></table><script>Ignore script</script></main></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Write-Ahead Logging - SQLite", "Superseded text:", "Beginning with version 3.11.0", "Region | Share", "Europe | 25%"} {
		if !strings.Contains(p.Content, want) {
			t.Errorf("missing %q: %s", want, p.Content)
		}
	}
	for _, bad := range []string{"Ignore navigation", "Ignore script"} {
		if strings.Contains(p.Content, bad) {
			t.Errorf("retained %s", bad)
		}
	}
}

func TestDirectPageReaderPreservesBodyAndRejectsPrivateTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Public documentation</title><main><p>Current guidance with its conditions.</p></main>`)
	}))
	defer server.Close()
	c := NewDirectClient(time.Second)
	// Only this test transport redirects the public hostname to the local double.
	c.HTTPClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	p, err := c.ScrapeURL(context.Background(), "http://public.example/doc")
	if err != nil || p == nil || !strings.Contains(p.Content, "Current guidance") {
		t.Fatalf("page=%+v err=%v", p, err)
	}
	if _, err := c.ScrapeURL(context.Background(), server.URL); !errors.Is(err, errBlockedTarget) {
		t.Fatalf("private URL accepted: %v", err)
	}
}

func TestHTMLChallengeIsNotSourceEvidence(t *testing.T) {
	_, err := extractHTML(`<title>Just a moment...</title><p>Checking your browser</p>`)
	if err == nil {
		t.Fatal("challenge treated as evidence")
	}
	// Its own code, so the site is taken for one that refuses readers and
	// the model is asked for pages elsewhere.
	if code := scrapeCode(err); code != "challenge" || !Refused(SourceSignal{Code: code}) {
		t.Errorf("code = %q, want a refusal", code)
	}
	for _, code := range []string{"404", "scrape_failed", "empty", "blocked"} {
		if Refused(SourceSignal{Code: code}) {
			t.Errorf("%s taken for a site that refuses readers", code)
		}
	}
}

func TestDirectRedirectCannotReadPrivateService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()
	c := NewDirectClient(time.Second)
	c.HTTPClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	if _, err := c.ScrapeURL(context.Background(), "http://public.example/redirect"); !errors.Is(err, errBlockedTarget) {
		t.Fatalf("private redirect accepted: %v", err)
	}
	if _, err := dialPublicPage(context.Background(), "tcp", "localhost:80"); !errors.Is(err, errBlockedTarget) {
		t.Fatalf("private DNS resolution accepted: %v", err)
	}
}

func TestExtractedCorrectionSurvivesQueryExcerpt(t *testing.T) {
	body := `<title>SQLite WAL</title><main>` + strings.Repeat(`<p>Unrelated introductory material about databases and applications.</p>`, 100) + `<p><s>For transactions in excess of a gigabyte, WAL mode may fail with an I/O or disk-full error.</s> Beginning with version 3.11.0, WAL mode works as efficiently with large transactions as does rollback mode.</p></main>`
	p, err := extractHTML(body)
	if err != nil {
		t.Fatal(err)
	}
	excerpt := ExcerptFor(p.Content, []string{"WAL transactions gigabyte fail"}, nil, 600)
	if !strings.Contains(excerpt, "Superseded text") || !strings.Contains(excerpt, "Beginning with version 3.11.0") {
		t.Fatalf("warning lost correction: %s", excerpt)
	}
}

// The built-in reader wrote a table's cells with no header row or separator,
// so the excerpts, which score a Markdown table by its header and answering
// rows, never saw a table on a page it read. It now writes Markdown: tables,
// nested lists and code as the page has them, and a link as its words alone.
func TestHTMLExtractionWritesMarkdown(t *testing.T) {
	p, err := extractHTML(`<title>Guide</title><main><p>See <a href="https://x.example/a">the guide</a> for C++ <img src="a.png" alt="chart"></p>` +
		`<ul><li>one<ul><li>nested</li></ul></li></ul><pre><code>go build ./...
  indented</code></pre>` +
		`<table><thead><tr><th>Region</th><th>Share</th></tr></thead><tbody><tr><td>Europe</td><td>25%</td></tr></tbody></table></main>`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"See the guide for C++", "- one\n", "  - nested", "```\ngo build ./...\n  indented\n```", "| Region | Share |\n|---|---|\n| Europe | 25% |"} {
		if !strings.Contains(p.Content, want) {
			t.Errorf("missing %q in:\n%s", want, p.Content)
		}
	}
	for _, bad := range []string{"x.example", "a.png", `C\+\+`} {
		if strings.Contains(p.Content, bad) {
			t.Errorf("kept %q in:\n%s", bad, p.Content)
		}
	}
}
