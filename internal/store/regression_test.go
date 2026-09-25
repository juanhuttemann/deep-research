package store

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

func TestLegacyHistoryStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	legacy := `{"id":"old-run","question":"old question","analysis":"old answer","gaps":["old gap"],"confidence":"low","findings":[{"id":"old-finding","title":"old source","url":"https://example.com"}]}`
	if err := os.WriteFile(path, []byte(legacy+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runs, _, err := Open(path).List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("old history unreadable: %+v, %v", runs, err)
	}
	raw, err := json.Marshal(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	var result agent.ResearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("legacy record not migrated to result schema: %v", err)
	}
	if result.Analysis == nil || result.Analysis.Answer != "old answer" || len(result.Analysis.Gaps) != 1 || result.Summary == nil || result.Summary.Confidence != "low" {
		t.Fatalf("legacy fields lost: %s", raw)
	}
	if len(result.Findings) != 1 || result.Findings[0].Status != "" || result.FactCheck != nil {
		t.Fatalf("invented verification for legacy result: %s", raw)
	}
}

// A run killed part-way through Save leaves a torn last line. That record can
// never be parsed again, and List used to drop it without a word, so the
// history under-reported with nothing to explain the missing run.
func TestListReportsUnreadableRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	good := `{"id":"complete","question":"answered"}`
	torn := `{"id":"killed-mid-write","question":"half a rec`
	if err := os.WriteFile(path, []byte(good+"\n"+torn), 0600); err != nil {
		t.Fatal(err)
	}
	runs, skipped, err := Open(path).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "complete" {
		t.Fatalf("readable record lost: %+v", runs)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1: a torn record must be reported, not swallowed", skipped)
	}
}

// Blank padding between records is not corruption — an empty trailing line is
// what every write leaves behind — so it must not be counted as skipped.
func TestListDoesNotCountBlankLinesAsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"a"}`+"\n\n"+`{"id":"b"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runs, skipped, err := Open(path).List()
	if err != nil || len(runs) != 2 || skipped != 0 {
		t.Fatalf("runs=%d skipped=%d err=%v, want 2/0/nil", len(runs), skipped, err)
	}
}

// Every run appended the full text of every source it read to one JSONL file
// that `list` reads back in its entirety, so the history grew without bound
// and the index that prints two lines per run paid for all of it. The full
// text stays in that run's artifacts under reports/.
func TestHistoryBoundsStoredSourceText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	long := strings.Repeat("é", maxStoredContentChars)
	result := &ResearchResult{ID: "1", ResearchResult: agent.ResearchResult{
		Question: "q",
		Findings: []agent.Finding{{Title: "T", URL: "https://a.example", Content: long}},
	}}
	if err := Open(path).Save(result); err != nil {
		t.Fatal(err)
	}
	// The caller still holds the full text: it has a report to write.
	if result.Findings[0].Content != long {
		t.Error("Save truncated the caller's own findings")
	}
	runs, skipped, err := Open(path).List()
	if err != nil || skipped != 0 || len(runs) != 1 {
		t.Fatalf("list: %v runs, %d skipped, %v", len(runs), skipped, err)
	}
	stored := runs[0].Findings[0].Content
	if len(stored) > maxStoredContentChars+len(" …[truncated]") {
		t.Errorf("stored content is %d bytes, want it bounded", len(stored))
	}
	if !strings.HasSuffix(stored, "…[truncated]") {
		t.Error("a truncated source does not say it was truncated")
	}
	if !utf8.ValidString(stored) {
		t.Error("truncation split a UTF-8 rune")
	}
}

// Content that fits is stored whole, and a torn final line is still counted
// rather than ending the read.
func TestHistoryKeepsShortSourcesAndCountsTornRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	st := Open(path)
	if err := st.Save(&ResearchResult{ID: "1", ResearchResult: agent.ResearchResult{
		Question: "q", Findings: []agent.Finding{{Title: "T", Content: "short"}},
	}}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A run killed mid-write leaves a final line with no newline at all.
	if _, err := f.WriteString(`{"id":"2","question":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runs, skipped, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || skipped != 1 {
		t.Fatalf("got %d runs and %d skipped, want 1 and 1", len(runs), skipped)
	}
	if runs[0].Findings[0].Content != "short" {
		t.Errorf("short content was altered: %q", runs[0].Findings[0].Content)
	}
}
