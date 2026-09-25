package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juanhuttemann/deep-research/internal/agent"
)

type ResearchResult struct {
	ID string `json:"id"`
	agent.ResearchResult
}

// UnmarshalJSON accepts both full results and the original history schema,
// which stored analysis as a string and confidence/gaps at the top level.
// Missing verification stays unknown for those legacy records.
func (r *ResearchResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID string `json:"id"`
		agent.ResearchResult
		Analysis   json.RawMessage `json:"analysis"`
		Gaps       []string        `json:"gaps"`
		Confidence string          `json:"confidence"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = ResearchResult{ID: wire.ID, ResearchResult: wire.ResearchResult}
	if len(wire.Analysis) > 0 {
		if wire.Analysis[0] == '"' {
			var answer string
			if err := json.Unmarshal(wire.Analysis, &answer); err != nil {
				return err
			}
			r.Analysis = &agent.Analysis{Answer: answer, Gaps: wire.Gaps, Confidence: wire.Confidence}
		} else if err := json.Unmarshal(wire.Analysis, &r.Analysis); err != nil {
			return err
		}
	}
	if r.Summary == nil && wire.Confidence != "" {
		r.Summary = &agent.Summary{Confidence: wire.Confidence}
	}
	return nil
}

type Store struct {
	Path string
	mu   sync.Mutex
}

func Open(path string) *Store {
	return &Store{Path: path}
}

// NewID returns a random ID for a run.
func NewID() string { return uuid.NewString() }

// maxStoredContentChars bounds how much of one source's text the history
// keeps. The history is the run index `list` reads — every run appended the
// full text of every source it read to a single file that is read back whole,
// so a few hundred runs turn `list` into a multi-hundred-megabyte read. The
// complete text stays in that run's .md / .json artifacts under reports/,
// which is where a reader goes for the sources themselves.
const maxStoredContentChars = 4000

// trimForHistory returns r with each finding's content bounded. The findings
// slice is copied: the caller still prints and exports the same result, and
// truncating in place would shorten the report it has not written yet.
func trimForHistory(r *ResearchResult) *ResearchResult {
	out := *r
	out.Findings = make([]agent.Finding, len(r.Findings))
	copy(out.Findings, r.Findings)
	for i := range out.Findings {
		out.Findings[i].Content = clipStored(out.Findings[i].Content)
	}
	return &out
}

// clipStored cuts content to the stored limit on a rune boundary and marks
// the cut, so a reader of the history can tell an excerpt from a short page.
func clipStored(content string) string {
	if len(content) <= maxStoredContentChars {
		return content
	}
	cut := maxStoredContentChars
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + " …[truncated]"
}

func (s *Store) Save(r *ResearchResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", s.Path, err)
	}
	line, err := json.Marshal(trimForHistory(r))
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("marshal result: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", s.Path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", s.Path, err)
	}
	return nil
}

// List reads the run history. It also reports how many lines were unreadable:
// history is an append-only JSONL file, so a run killed part-way through Save
// leaves a torn final line, and that record can never be parsed again.
// Skipping it silently made the history under-report with nothing to say why,
// so the count comes back for the caller to surface.
func (s *Store) List() (results []ResearchResult, skipped int, err error) {
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	// Read a record at a time rather than the whole file: history only grows,
	// and `list` held every run ever made in memory at once to print two
	// lines about each of them.
	br := bufio.NewReader(f)
	for {
		line, readErr := br.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			var r ResearchResult
			if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
				skipped++
			} else {
				results = append(results, r)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return results, skipped, nil
			}
			return results, skipped, readErr
		}
	}
}
