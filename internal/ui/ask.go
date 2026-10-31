package ui

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// LoadMeta reads a finished run's .json sidecar, the record a follow-up
// question is answered from.
func LoadMeta(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s is not a run's .json: %w", path, err)
	}
	return &m, nil
}

// RunOverview is what a follow-up conversation knows of the run before it
// reads a page: the question, the report, every claim with what the
// fact-check made of it, and the sources. It is small; the pages, which are
// not, are read through SourceTools.
func RunOverview(m *Meta) string {
	var sb strings.Builder
	sb.WriteString("Research question: ")
	sb.WriteString(m.Question)
	sb.WriteString(agent.ClarifiedBlock(m.Clarifications))
	sb.WriteString("\n\n")
	if a := m.Analysis; a != nil && a.Interpretation != "" {
		sb.WriteString("How it was read: ")
		sb.WriteString(a.Interpretation)
		sb.WriteString("\n\n")
	}
	sb.WriteString("The report:\n")
	sb.WriteString(m.Report)
	sb.WriteString("\n\n")
	if a := m.Analysis; a != nil && len(a.Claims) > 0 {
		sb.WriteString("Claims, with the fact-check's status (only \"supported\" was established):\n")
		for _, c := range a.Claims {
			fmt.Fprintf(&sb, "  %s [%s] %s %v\n", c.ID, cmp.Or(c.Status, "unchecked"), c.Text, c.Sources)
		}
		for _, g := range a.Gaps {
			sb.WriteString("  open question: ")
			sb.WriteString(g)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Sources (fetched = page read in full, snippet only = only the search result's snippet was read):\n")
	for i, c := range m.Citations {
		fmt.Fprintf(&sb, "  %d. %s (%s) %s\n", i+1, c.Title, c.URL, pageWords(c.Status))
	}
	return sb.String()
}

// pageWords is a source's status in the words the page marks it with: told
// "degraded", the model answered with a word the reader could find nowhere
// on the page.
func pageWords(status string) string {
	if status == "ok" {
		return "fetched"
	}
	return sourceNote(cmp.Or(status, "unverified"))
}

// readLimit is how much of one page read_source returns: several times a
// run prompt's excerpt, since a follow-up reads few pages and wants the
// passage in its context.
const readLimit = 8000

// SourceTools are the tools a follow-up conversation reads the run's pages
// with: one page's passages about something, and which pages mention it.
func SourceTools(m *Meta) []tool.Tool {
	type readArgs struct {
		URL   string `json:"url" jsonschema:"the source's URL, as the run lists it"`
		About string `json:"about" jsonschema:"what to look for in the page"`
	}
	read := functool.MustNew(functool.Config{Name: "read_source",
		Description: "Read the passages of one of the run's sources about something."},
		func(_ context.Context, in readArgs) (string, error) {
			if len(m.Pages) == 0 {
				return noPages, nil
			}
			page, ok := pageFor(m, in.URL)
			if !ok {
				return "Not a page this run read. Its pages: " + strings.Join(slices.Sorted(maps.Keys(m.Pages)), ", "), nil
			}
			return tools.ExcerptFor(page, []string{in.About}, nil, readLimit), nil
		})
	type searchArgs struct {
		Query string `json:"query" jsonschema:"words the passage would contain"`
	}
	search := functool.MustNew(functool.Config{Name: "search_sources",
		Description: "Find which of the run's sources mention something, with the passage that does."},
		func(_ context.Context, in searchArgs) (string, error) { return searchPages(m, in.Query), nil })
	return []tool.Tool{read, search}
}

// noPages answers a tool on a run that kept no page text: one written
// before the .json stored pages.
const noPages = "This run saved no page text, only its report, claims and source list: answer from those."

// pageFor is the stored text of url, matched as the run matched pages.
func pageFor(m *Meta, url string) (string, bool) {
	want := tools.CanonicalURL(url)
	for u, page := range m.Pages {
		if tools.CanonicalURL(u) == want {
			return page, true
		}
	}
	return "", false
}

// searchHits and searchPassage bound search_sources' answer: a pointer to
// the pages worth reading, not the pages themselves.
const (
	searchHits    = 5
	searchPassage = 600
)

// searchPages lists the pages that mention query's words, best first.
func searchPages(m *Meta, query string) string {
	if len(m.Pages) == 0 {
		return noPages
	}
	type hit struct {
		url   string
		score float64
	}
	var hits []hit
	score := tools.NewPageScorer(query)
	for u, page := range m.Pages {
		if s := score.Score(page); s > 0 {
			hits = append(hits, hit{u, s})
		}
	}
	if len(hits) == 0 {
		return "No page of this run mentions that."
	}
	slices.SortFunc(hits, func(a, b hit) int { return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.url, b.url)) })
	var sb strings.Builder
	for _, h := range hits[:min(len(hits), searchHits)] {
		fmt.Fprintf(&sb, "%s\n%s\n\n", h.url, tools.ExcerptFor(m.Pages[h.url], []string{query}, nil, searchPassage))
	}
	return sb.String()
}

// ChatLog is where the questions about the run whose .json is at path are
// kept: beside it, one JSON line per question and its answer, appended, so
// a session can be resumed and the run's own files are never rewritten.
func ChatLog(path string) string { return strings.TrimSuffix(path, ".json") + ".chat.jsonl" }

// AppendTurn adds t to the run's chat log.
func AppendTurn(path string, t agent.Turn) error {
	line, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(ChatLog(path), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	// A line a crash cut short is ended first, or this turn would be glued
	// to it and lost with it.
	last := make([]byte, 1)
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// LoadTurns reads the run's chat log; a run never asked about has none. A
// line that does not parse, cut short by a crash, is skipped.
func LoadTurns(path string) ([]agent.Turn, error) {
	data, err := os.ReadFile(ChatLog(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var turns []agent.Turn
	for line := range bytes.Lines(data) {
		var t agent.Turn
		if json.Unmarshal(line, &t) == nil && t.Question != "" {
			turns = append(turns, t)
		}
	}
	return turns, nil
}

// Session is a run in the reports directory: a finished one, asked about
// by resuming it, or an interrupted one's checkpoint, resumed by running it.
type Session struct {
	Path        string
	Title       string
	Modified    time.Time
	Interrupted bool
}

// Sessions lists the runs in dir, the last run or asked about first. Traces
// hold no report, and a replay's runs are under dir/replay, not in dir.
func Sessions(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".trace.json") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		s := Session{Path: path, Modified: info.ModTime(), Interrupted: strings.HasSuffix(name, ".partial.json")}
		if fi, err := os.Stat(ChatLog(path)); err == nil && fi.ModTime().After(s.Modified) {
			s.Modified = fi.ModTime()
		}
		s.Title = cmp.Or(questionOf(path), strings.TrimSuffix(name, ".json"))
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Session) int { return b.Modified.Compare(a.Modified) })
	return out, nil
}

// questionOf is the question a run's .json or checkpoint was asked, read
// from the head of the file: it is among the first keys of both, and a
// finished run's pages, megabytes of them, come long after.
func questionOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(bufio.NewReader(f))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ""
		}
		var v json.RawMessage
		if key != "question" {
			if dec.Decode(&v) != nil {
				return ""
			}
			continue
		}
		var q string
		_ = dec.Decode(&q)
		return q
	}
	return ""
}
