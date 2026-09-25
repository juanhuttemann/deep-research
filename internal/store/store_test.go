package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "nested", "research.jsonl"))
}

// TestSaveAppendsAndCreatesDir: the data file lives under ~/.deep-research,
// which may not exist yet, and every run appends one line to it.
func TestSaveAppendsAndCreatesDir(t *testing.T) {
	s := tempStore(t)
	for i, q := range []string{"first question", "second question"} {
		if err := s.Save(&ResearchResult{
			ID: NewID(), ResearchResult: agent.ResearchResult{Question: q, Timestamp: time.Now(),
				Summary:  &agent.Summary{Confidence: "high"},
				Findings: []agent.Finding{{Query: q, Title: "t", URL: "https://e.example/" + q}}},
		}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	got, _, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d runs, want 2", len(got))
	}
	if got[0].Question != "first question" || got[1].Question != "second question" {
		t.Errorf("runs out of order: %q, %q", got[0].Question, got[1].Question)
	}
	if len(got[0].Findings) != 1 || got[0].Findings[0].URL == "" {
		t.Errorf("findings did not round-trip: %+v", got[0].Findings)
	}
}

// TestListSkipsCorruptLines: an append interrupted mid-write leaves a partial
// line. That must cost the reader that one run, not the whole history.
func TestListSkipsCorruptLines(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(&ResearchResult{ID: "1", ResearchResult: agent.ResearchResult{Question: "good one"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.WriteString("{\"id\":\"2\",\"question\":\"trunc\n\n   \n")
	f.Close()
	if err := s.Save(&ResearchResult{ID: "3", ResearchResult: agent.ResearchResult{Question: "good two"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d runs, want the 2 intact ones", len(got))
	}
	if got[0].Question != "good one" || got[1].Question != "good two" {
		t.Errorf("wrong runs survived: %q, %q", got[0].Question, got[1].Question)
	}
}

// TestListMissingFileIsEmpty: `list` before the first run is not an error.
func TestListMissingFileIsEmpty(t *testing.T) {
	got, _, err := tempStore(t).List()
	if err != nil {
		t.Fatalf("List on a missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no runs, got %d", len(got))
	}
}

// TestNewIDIsUnique: findings and runs are keyed by it.
func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewID()
		if id == "" || seen[id] {
			t.Fatalf("NewID returned a duplicate or empty id: %q", id)
		}
		seen[id] = true
	}
}
