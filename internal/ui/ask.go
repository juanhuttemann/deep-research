package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"

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
	sb.WriteString("Research question: " + m.Question + "\n\n")
	if a := m.Analysis; a != nil && a.Interpretation != "" {
		sb.WriteString("How it was read: " + a.Interpretation + "\n\n")
	}
	sb.WriteString("The report:\n" + m.Report + "\n\n")
	if a := m.Analysis; a != nil && len(a.Claims) > 0 {
		sb.WriteString("Claims, with the fact-check's status (only \"supported\" was established):\n")
		for _, c := range a.Claims {
			fmt.Fprintf(&sb, "  %s [%s] %s %v\n", c.ID, cmp.Or(c.Status, "unchecked"), c.Text, c.Sources)
		}
		for _, g := range a.Gaps {
			sb.WriteString("  open question: " + g + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Sources (status: ok = page read in full, degraded = search snippet only):\n")
	for i, c := range m.Citations {
		fmt.Fprintf(&sb, "  %d. %s (%s) %s\n", i+1, c.Title, c.URL, c.Status)
	}
	return sb.String()
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
	for u, page := range m.Pages {
		if s := tools.PageRelevance(query, page); s > 0 {
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
