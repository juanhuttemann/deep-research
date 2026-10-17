package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/message"
)

// The fixtures are one real run's analysis and fact-check, as the phases'
// parsers receive them: a JSON object the size a model returns (16 KB).
func benchBlob(tb testing.TB, name string) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "perf", name))
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// benchProse is the same payload the way a model often wraps it, which is
// what jsonPayloads exists for: the whole answer fails, the object in it
// parses.
func benchProse(tb testing.TB) string {
	return "Here is the analysis you asked for.\n\n```json\n" + benchBlob(tb, "analysis.json") +
		"\n```\n\nLet me know if you need more detail."
}

func BenchmarkParseAnalysis(b *testing.B) {
	blob := benchBlob(b, "analysis.json")
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	for b.Loop() {
		if a := parseAnalysis(blob); a == nil {
			b.Fatal("nil analysis")
		}
	}
}

func BenchmarkParseAnalysisProse(b *testing.B) {
	blob := benchProse(b)
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	for b.Loop() {
		if a := parseAnalysis(blob); a == nil {
			b.Fatal("nil analysis")
		}
	}
}

func BenchmarkParseFactCheck(b *testing.B) {
	blob := benchBlob(b, "fact_check.json")
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	for b.Loop() {
		if f := parseFactCheck(blob); f == nil {
			b.Fatal("nil fact-check")
		}
	}
}

// benchFindingsOut is a search sub-agent's answer: n findings, each with an
// excerpt of a fetched page.
func benchFindingsOut(tb testing.TB, n, excerpt int) string {
	tb.Helper()
	page := benchBlob(tb, "page.txt")
	if len(page) > excerpt {
		page = page[:excerpt]
	}
	quoted, err := json.Marshal(page)
	if err != nil {
		tb.Fatal(err)
	}
	var items []string
	for i := range n {
		suffix := strconv.Itoa(i)
		items = append(items, `{"title":"Result `+suffix+`","url":"https://example.com/`+suffix+
			`","content":`+string(quoted)+`,"confidence":"high"}`)
	}
	return "```json\n[" + strings.Join(items, ",\n") + "]\n```"
}

// BenchmarkParseSearchResultsTypical is the search fallback's parse, the one
// used when no search backend is configured and the model supplies the
// findings: eight with the excerpt each was read from, about 10 KB.
func BenchmarkParseSearchResultsTypical(b *testing.B) {
	out := benchFindingsOut(b, 8, 1200)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		if len(parseSearchResults(out, "range over func")) == 0 {
			b.Fatal("no findings")
		}
	}
}

// benchStream is a streamed phase the way an OpenAI-compatible provider
// sends one: a token's worth of text per server-sent event, about four bytes,
// with the reasoning first. The framework yields an update for every event,
// a reasoning-only one with no text in it (the body sniffer hands its
// reasoning to thinking), so the reporter runs once per token throughout.
// The thinking is 48 KB, about 12,000 tokens: what a minute or two of a
// reasoning model's fact-check comes to.
func benchStream(tb testing.TB, think func(string)) agent.ResponseStream {
	tb.Helper()
	answer := benchBlob(tb, "analysis.json")
	thinking := strings.Repeat("The second source dates the release; the claim needs that quote. ", 740)
	return func(yield func(*agent.ResponseUpdate, error) bool) {
		for s := range slices.Values(chunks(thinking, 4)) {
			think(s)
			if !yield(&agent.ResponseUpdate{Role: message.RoleAssistant}, nil) {
				return
			}
		}
		for s := range slices.Values(chunks(answer, 4)) {
			if !yield(&agent.ResponseUpdate{Role: message.RoleAssistant, Contents: message.Contents{&message.TextContent{Text: s}}}, nil) {
				return
			}
		}
	}
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// BenchmarkStreamedPhase is one streamed phase's own work, network aside:
// draining the stream, collecting the answer and the reasoning, and the
// progress reporter that runs on every update, wired as callStreaming wires
// them.
func BenchmarkStreamedPhase(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var th thoughts
		progress := newStreamReporter("analyzing", analysisUnit, time.Now, func(string) {})
		report, stop := tickProgress(func(p string) { progress(p, th.String()) }, progressTick)
		resp, err := collectResponse(benchStream(b, th.add), report)
		stop()
		if err != nil || !strings.HasPrefix(resp.String(), "{") {
			b.Fatalf("collected %q, %v", resp.String()[:min(20, len(resp.String()))], err)
		}
	}
}
