package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The fixtures are one real run's artifacts: the pages it fetched and the
// sources it cited. Ranking and excerpting are measured on those, at the
// sizes a live run meets (a 40 KB page, a 250-byte engine snippet, 30
// results per query).

// perfMeta is the subset of the run sidecar these benchmarks need.
type perfMeta struct {
	Question  string            `json:"question"`
	Pages     map[string]string `json:"pages"`
	Citations []perfCitation    `json:"citations"`
}

type perfCitation struct {
	Query string `json:"query"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func perfFixture(tb testing.TB) perfMeta {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "perf", "report.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var m perfMeta
	if err := json.Unmarshal(b, &m); err != nil {
		tb.Fatal(err)
	}
	return m
}

// perfTerms are the distinctive terms a planner names for this question.
func perfTerms() []string { return []string{"iterator", "yield", "range", "desugar", "pull"} }

// perfResults is one query's result set: every cited source, plus the
// off-topic tail a metasearch engine returns, each with the page's opening
// as its snippet.
func perfResults(tb testing.TB, n int) []SearchResult {
	tb.Helper()
	m := perfFixture(tb)
	var out []SearchResult
	for _, c := range m.Citations {
		out = append(out, SearchResult{
			Title: c.Title, URL: c.URL, Engine: "searxng", Content: snippetOf(m.Pages[c.URL]),
		})
	}
	for i := 0; len(out) < n; i++ {
		out = append(out, SearchResult{
			Title: "unrelated listing " + string(rune('a'+i%26)),
			URL:   "https://example.com/listing/" + string(rune('a'+i%26)),
			Content: "A catalogue of products, opening hours and a cookie notice. " +
				"No technical content appears on this page.",
			Engine: "searxng",
		})
	}
	return out
}

func snippetOf(page string) string {
	if len(page) > 250 {
		return page[:250]
	}
	return page
}

func perfPage(tb testing.TB) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "perf", "page.txt"))
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// BenchmarkRelevance is one result scored against a query whose terms were
// folded once, as rankByRelevance does.
func BenchmarkRelevance(b *testing.B) {
	terms := foldAll(perfTerms())
	r := perfResults(b, 30)
	b.ReportAllocs()
	for b.Loop() {
		if s := relevance(terms, r[0].Title, r[0].Content); s < 0 || s > 1 {
			b.Fatalf("score %v out of range", s)
		}
	}
}

func BenchmarkRankByRelevance(b *testing.B) {
	terms := perfTerms()
	in := perfResults(b, 30)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for b.Loop() {
		kept, skipped := rankByRelevance(in, terms)
		if len(kept)+len(skipped) != len(in) {
			b.Fatal("results lost")
		}
	}
}

func BenchmarkFoldPage(b *testing.B) {
	page := perfPage(b)
	b.SetBytes(int64(len(page)))
	b.ReportAllocs()
	for b.Loop() {
		if fold(page) == "" {
			b.Fatal("empty")
		}
	}
}

func BenchmarkFoldWord(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if fold("Iterator") == "" {
			b.Fatal("empty")
		}
	}
}

// BenchmarkExcerptFor is the per-page cost of handing the model the passages
// that bear on a query, run once per source per phase.
func BenchmarkExcerptFor(b *testing.B) {
	page := perfPage(b)
	first := []string{"how do range over func iterators work in Go 1.23"}
	second := []string{"the compiler desugars the loop body", "iterator signatures"}
	b.SetBytes(int64(len(page)))
	b.ReportAllocs()
	for b.Loop() {
		if ExcerptFor(page, first, second, 8000) == "" {
			b.Fatal("empty excerpt")
		}
	}
}

func BenchmarkExcerptForClaims(b *testing.B) {
	page := perfPage(b)
	claims := slices.Repeat([]string{"Go 1.23 adds range over function types", "the iterator calls yield"}, 12)
	b.SetBytes(int64(len(page)))
	b.ReportAllocs()
	for b.Loop() {
		if ExcerptFor(page, claims, nil, 8000) == "" {
			b.Fatal("empty excerpt")
		}
	}
}

func BenchmarkPageRelevance(b *testing.B) {
	page := perfPage(b)
	b.SetBytes(int64(len(page)))
	b.ReportAllocs()
	for b.Loop() {
		if NewPageScorer("how do range over func iterators work").Score(page) < 0 {
			b.Fatal("negative")
		}
	}
}
