package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Meta is the structured trace written to the .json sidecar: full citations,
// per-sub-agent research notes, and the full event timeline.
type Meta struct {
	Version          string    `json:"version"`
	Question         string    `json:"question"`
	Timestamp        time.Time `json:"timestamp"`
	Depth            string    `json:"depth"`
	Model            string    `json:"model,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	ServedBy         []string  `json:"served_by,omitempty"`
	Confidence       string    `json:"confidence"`
	Tokens           int       `json:"tokens"`
	Sources          int       `json:"sources"`
	ExecutiveSummary string    `json:"executive_summary"`
	Report           string    `json:"report"`
	// Error is set when a late phase failed and the report is incomplete.
	Error string `json:"error,omitempty"`
	// Topics is the analyzer's per-topic evidence with its own confidence.
	Topics []agent.Topic `json:"topics,omitempty"`
	// FactCheck is the verification pass's verdicts, so a consumer can see
	// which claims were checked rather than inferring it from the prose.
	FactCheck *agent.FactCheckResult `json:"fact_check,omitempty"`
	Citations []MetaCitation         `json:"citations"`
	Notes     []MetaNote             `json:"sub_agent_notes"`
	Timeline  []TimelineEntry        `json:"timeline"`
}

// MetaCitation is one cited source with its verification status.
type MetaCitation struct {
	Query      string `json:"query"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Confidence string `json:"confidence"`
	Status     string `json:"status"`
	Domain     string `json:"domain"`
	// Cited reports whether the report body actually references this source.
	// Retrieving a page and citing it are different claims: a run can gather
	// twenty sources and build its answer on three.
	Cited bool `json:"cited"`
}

// MetaNote records a sub-agent's final state and last status line.
type MetaNote struct {
	SubID   string `json:"sub_id"`
	SubName string `json:"sub_name"`
	State   string `json:"state"`
	// Sources is how many sources this branch contributed to the run. A branch
	// can finish having contributed none — its results were all off-topic, or
	// all pages another branch had already claimed — and without this the
	// trace shows only that it finished, exactly like one that did the work.
	Sources  int    `json:"sources"`
	Progress int    `json:"progress"`
	Line     string `json:"last_line"`
}

// TimelineEntry is one rendered event in the run timeline.
type TimelineEntry struct {
	Type   string    `json:"type"`
	Phase  string    `json:"phase,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Time   time.Time `json:"time"`
	Query  string    `json:"query,omitempty"`
	URL    string    `json:"url,omitempty"`
	SubID  string    `json:"sub_id,omitempty"`
	Line   string    `json:"line,omitempty"`
}

// MarkdownReport renders a self-contained Markdown document from a result,
// including an executive summary, the report body and a numbered citations
// list deduplicated by URL.
func MarkdownReport(res *agent.ResearchResult) string {
	var sb strings.Builder
	sum := res.Summary
	if sum == nil {
		sum = &agent.Summary{}
	}
	fmt.Fprintf(&sb, "# %s\n\n", res.Question)
	fmt.Fprintf(&sb, "**Confidence:** %s\n\n", sum.Confidence)
	if res.Model != "" {
		fmt.Fprintf(&sb, "**Model:** %s", res.Model)
		if res.Provider != "" {
			fmt.Fprintf(&sb, " via %s", res.Provider)
		}
		if len(res.ServedBy) > 0 && !slices.Equal(res.ServedBy, []string{res.Model}) {
			fmt.Fprintf(&sb, " (served by %s)", strings.Join(res.ServedBy, ", "))
		}
		sb.WriteString("\n\n")
	}
	if body := summaryBody(res.Question, sum); body != "" {
		sb.WriteString(body)
		sb.WriteString("\n\n")
	}

	if res.Analysis != nil {
		writeTopics(&sb, res.Analysis.Topics)
		writeOpenQuestions(&sb, res.Analysis)
	}
	writeFactCheck(&sb, res.FactCheck)

	// Judged against the report body alone, exactly as the JSON sidecar does:
	// a URL is cited when the text of the answer references it.
	cits := citations(res, condStr(res.Summary, "report"))
	cited, retrieved := splitCited(cits)
	// A report that embeds no links at all gives nothing to split on, so
	// everything stays under one heading rather than claiming the report
	// cited none of its sources.
	if len(cited) == 0 {
		cited, retrieved = cits, nil
	}
	writeCitationList(&sb, "Citations", cited)
	// Sources the run fetched but the report never used are still worth
	// listing — they are what the reader would check to see what was looked
	// at — but listing them as citations overstated the evidence behind the
	// answer, noise included.
	writeCitationList(&sb, "Other Sources Retrieved", retrieved)
	return sb.String()
}

// citations is the deduplicated, URL-ordered citation list for a result, with
// each entry marked according to whether the report body references it.
//
// A finding without a URL is listed under its title: it is counted as a
// source, and leaving it out made "sources 5" arrive with a list of two.
func citations(res *agent.ResearchResult, body string) []MetaCitation {
	seen := map[string]bool{}
	var cits []MetaCitation
	for _, f := range res.Findings {
		key := f.URL
		if key == "" {
			key = "title:" + f.Title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		cits = append(cits, MetaCitation{
			Query: f.Query, Title: f.Title, URL: f.URL,
			Confidence: f.Confidence, Status: citationStatus(f), Domain: tools.DomainOf(f.URL),
			Cited: strings.Contains(body, f.URL),
		})
	}
	sort.Slice(cits, func(i, j int) bool { return cits[i].URL < cits[j].URL })
	return cits
}

func splitCited(in []MetaCitation) (cited, retrieved []MetaCitation) {
	for _, c := range in {
		if c.Cited {
			cited = append(cited, c)
		} else {
			retrieved = append(retrieved, c)
		}
	}
	return cited, retrieved
}

func writeCitationList(sb *strings.Builder, heading string, cits []MetaCitation) {
	if len(cits) == 0 {
		return
	}
	fmt.Fprintf(sb, "## %s (%d)\n\n", heading, len(cits))
	for i, c := range cits {
		if c.URL == "" {
			fmt.Fprintf(sb, "%d. %s — _%s_ (%s, no URL)\n", i+1, c.Title, c.Confidence, c.Status)
			continue
		}
		fmt.Fprintf(sb, "%d. [%s](%s) — _%s_ (%s)\n", i+1, c.Title, c.URL, c.Confidence, c.Status)
	}
	sb.WriteString("\n")
}

// writeOpenQuestions records what the analysis could not settle: the gaps it
// identified and the follow-up queries it suggested. Both were parsed and then
// read by nothing, so the one part of the run that says what is still unknown
// never reached the reader.
func writeOpenQuestions(sb *strings.Builder, a *agent.Analysis) {
	if len(a.Gaps) == 0 && len(a.FollowUp) == 0 {
		return
	}
	sb.WriteString("## Open Questions\n\n")
	for _, g := range a.Gaps {
		if g = strings.TrimSpace(g); g != "" {
			fmt.Fprintf(sb, "- %s\n", g)
		}
	}
	for _, q := range a.FollowUp {
		if q = strings.TrimSpace(q); q != "" {
			fmt.Fprintf(sb, "- _suggested search:_ %s\n", q)
		}
	}
	sb.WriteString("\n")
}

// writeTopics lists the analyzer's per-topic evidence and confidence. It was
// parsed, stored in the history and read by nothing.
func writeTopics(sb *strings.Builder, topics []agent.Topic) {
	if len(topics) == 0 {
		return
	}
	sb.WriteString("## Evidence by Topic\n\n")
	for _, t := range topics {
		fmt.Fprintf(sb, "### %s\n\n_Confidence: %s_\n\n", t.Name, t.Confidence)
		for _, f := range t.Findings {
			if f = strings.TrimSpace(f); f != "" {
				fmt.Fprintf(sb, "- %s\n", f)
			}
		}
		sb.WriteString("\n")
	}
}

// writeFactCheck records what the verification pass concluded, claim by
// claim. Leaving it to the summarizer's prose meant a reader could not tell
// which claims the pipeline had actually checked. The verdict is the flag,
// not the array a claim arrived in.
func writeFactCheck(sb *strings.Builder, fc *agent.FactCheckResult) {
	if fc == nil || len(fc.Verified)+len(fc.Unverified)+len(fc.Contradictions) == 0 {
		return
	}
	sb.WriteString("## Fact-Check\n\n")
	for _, c := range fc.Verified {
		mark := "✓ verified"
		if !c.Verified {
			mark = "✗ not verified"
		}
		fmt.Fprintf(sb, "- %s: %s", mark, c.Claim)
		if c.Evidence != "" {
			fmt.Fprintf(sb, " — %s", c.Evidence)
		}
		sb.WriteString("\n")
	}
	for _, c := range fc.Unverified {
		fmt.Fprintf(sb, "- ? unverified: %s\n", c)
	}
	for _, c := range fc.Contradictions {
		fmt.Fprintf(sb, "- ⚠ contradiction on %s: %s\n", c.Claim, strings.Join(c.Sources, "; "))
	}
	sb.WriteString("\n")
}

// WriteMarkdown writes the Markdown report and returns the path written.
func WriteMarkdown(res *agent.ResearchResult, dir string) (string, error) {
	body := MarkdownReport(res)
	name := slug(res.Question) + ".md"
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return path, err
	}
	return path, nil
}

// WritePDF builds a PDF from the report and executive summary and writes it.
func WritePDF(res *agent.ResearchResult, dir string) (string, error) {
	body := markdownText(MarkdownReport(res))
	data := BuildPDF(wrapText(strings.ReplaceAll(body, "\t", "  "), 78))
	name := slug(res.Question) + ".pdf"
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return path, err
	}
	return path, nil
}

// summaryBody retains an executive section already present in the report.
// Executive is usually an extracted opening paragraph, sometimes truncated;
// copying it back into that report would repeat the same text.
func summaryBody(question string, sum *agent.Summary) string {
	body := strings.TrimSpace(sum.Report)
	first, rest, _ := strings.Cut(body, "\n")
	if strings.HasPrefix(first, "# ") {
		title := strings.TrimSpace(strings.TrimPrefix(first, "# "))
		if sum.Executive != "" || title == question || strings.EqualFold(title, "Research Report: "+question) {
			body = strings.TrimSpace(rest)
		}
	}
	first, _, _ = strings.Cut(body, "\n")
	// A report's own executive heading counts whether the model wrote it as a
	// Markdown heading or in bold; either way, adding a second one above it
	// repeats both the heading and the paragraph under it.
	heading := strings.HasPrefix(first, "#") || agent.IsEmphasizedHeading(first)
	if heading && strings.EqualFold(strings.Trim(first, "#*_ \t"), "Executive Summary") {
		return body
	}
	exec := strings.TrimSpace(sum.Executive)
	if exec == "" {
		return body
	}
	prefix := strings.TrimSuffix(exec, "\n…")
	if heading {
		_, rest, _ := strings.Cut(body, "\n")
		rest = strings.TrimSpace(rest)
		if rest == prefix || strings.HasPrefix(rest, prefix+"\n") {
			// Match the extracted opening text, not an English-only heading:
			// localized reports already have their own executive section.
			return body
		}
	}
	if body == prefix || strings.HasPrefix(body, prefix+"\n") {
		return "## Executive Summary\n\n" + body
	}
	return strings.TrimSpace("## Executive Summary\n\n" + exec + "\n\n" + body)
}

// citationStatus reports how a cited source was obtained. A finding with no
// signal was never confirmed fetched, so it counts as unverified rather than
// being promoted to a clean fetch.
func citationStatus(f agent.Finding) string {
	if f.Status == "" {
		return "unverified"
	}
	return f.Status
}

// BuildMeta assembles the trace metadata from a result and its event timeline.
func BuildMeta(res *agent.ResearchResult, timeline []Event, depth string, tokens int, sources int) Meta {
	m := Meta{
		Version:          "1",
		Question:         res.Question,
		Timestamp:        res.Timestamp,
		Depth:            depth,
		Confidence:       condStr(res.Summary, "confidence"),
		Tokens:           tokens,
		Sources:          sources,
		ExecutiveSummary: condStr(res.Summary, "executive"),
		Report:           condStr(res.Summary, "report"),
		Error:            res.Error,
		Model:            res.Model,
		Provider:         res.Provider,
		ServedBy:         res.ServedBy,
		FactCheck:        res.FactCheck,
	}
	if res.Analysis != nil {
		m.Topics = res.Analysis.Topics
	}
	// The sidecar records the same cited/retrieved distinction the Markdown
	// export draws, so a consumer of the trace can tell which sources the
	// answer actually rests on.
	m.Citations = citations(res, condStr(res.Summary, "report"))

	notes := map[string]MetaNote{}
	var order []string
	for _, e := range timeline {
		m.Timeline = append(m.Timeline, TimelineEntry{
			Type: string(e.Type), Phase: e.Phase, Detail: e.Detail, Time: e.Time,
			Query: e.Query, URL: e.URL, SubID: e.SubID, Line: e.Line,
		})
		if e.Type == "citation" && e.SubID != "" {
			n, ok := notes[e.SubID]
			if !ok {
				order = append(order, e.SubID)
				n = MetaNote{SubID: e.SubID}
			}
			n.Sources++
			notes[e.SubID] = n
			continue
		}
		if e.Type != "subagent" || e.SubID == "" {
			continue
		}
		// A note records a sub-agent's *final* state. Keeping the first event
		// per sub-agent kept the "queued" announcement the tree emits before
		// the work starts, so every exported note read queued no matter how
		// the run went. Fields absent from a later event (a progress-only
		// update carries no state) keep the value they already had.
		n, ok := notes[e.SubID]
		if !ok {
			order = append(order, e.SubID)
			n = MetaNote{SubID: e.SubID}
		}
		if e.SubName != "" {
			n.SubName = e.SubName
		}
		if e.SubState != "" {
			n.State = e.SubState
		}
		if e.Line != "" {
			n.Line = e.Line
		}
		if e.Progress > n.Progress {
			n.Progress = e.Progress
		}
		notes[e.SubID] = n
	}
	for _, id := range order {
		m.Notes = append(m.Notes, notes[id])
	}
	return m
}

// WriteMetadata marshals the trace metadata to JSON and writes it.
func WriteMetadata(m Meta, dir string) (string, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, slug(m.Question)+".json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return path, err
	}
	return path, nil
}

// condStr extracts a field from a Summary, tolerating a nil summary.
func condStr(s *agent.Summary, field string) string {
	if s == nil {
		return ""
	}
	switch field {
	case "confidence":
		return s.Confidence
	case "executive":
		return s.Executive
	case "report":
		return s.Report
	}
	return ""
}

// slug turns a question into an artifact filename: a readable stem plus a
// short digest of the full question.
//
// The stem alone is ambiguous — every non-alphanumeric rune collapses to "-"
// and the edges are trimmed, so "What is X?" and "What is X" reduce to the
// same name and the second run overwrites the first's .md, .pdf and .json.
// A 128-bit digest of the untouched question makes collisions negligible,
// while re-running the same question keeps replacing its own files.
func slug(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	stem := strings.Trim(sb.String(), "-")
	if stem == "" {
		stem = "research"
	}
	// Long questions make unwieldy filenames; the digest keeps the trimmed
	// stem unambiguous.
	if len(stem) > 80 {
		stem = stem[:80]
		stem = strings.Trim(stem, "-")
	}
	sum := sha256.Sum256([]byte(s))
	return stem + "-" + hex.EncodeToString(sum[:16])
}
