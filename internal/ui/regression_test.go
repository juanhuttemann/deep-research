package ui

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

func TestFinalResultRetainsReportedUsage(t *testing.T) {
	fa := &fakeAssistant{tokensPerCall: 40}
	sink := &MultiSink{}
	d := NewDriver(fa, sink, nil, 1)
	res, err := d.Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var usage struct {
		Tokens int `json:"tokens"`
	}
	if err := json.Unmarshal(data, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.Tokens != fa.TokensUsed() {
		t.Errorf("result tokens = %d, provider reported %d", usage.Tokens, fa.TokensUsed())
	}
	last := sink.Timeline[len(sink.Timeline)-1]
	if last.Type != Report || last.Tokens != usage.Tokens || last.Sources != len(res.Findings) {
		t.Errorf("final event differs from result: %+v", last)
	}
}

func TestCompletedBranchesExportFullProgress(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, nil, 1)
	res, err := d.Run(context.Background(), newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Basics"}}))
	if err != nil {
		t.Fatal(err)
	}
	meta := BuildMeta(res, sink.Timeline, "standard", res.Tokens, len(res.Findings))
	if len(meta.Notes) != 1 || meta.Notes[0].State != "done" || meta.Notes[0].Progress != 100 {
		t.Errorf("completed branch exports unfinished progress: %+v", meta.Notes)
	}
}

func TestArtifactNamesSurviveOldHashCollision(t *testing.T) {
	// Both questions have the same 80-character stem. Find a collision in
	// the old 24-bit suffix, then exercise all three artifact writers.
	seen := map[[3]byte]string{}
	var questions []string
	for i := 0; i < 100000; i++ {
		q := strings.Repeat("a", 80) + fmt.Sprint(i)
		h := sha256.Sum256([]byte(q))
		key := [3]byte{h[0], h[1], h[2]}
		if prev, ok := seen[key]; ok {
			questions = []string{prev, q}
			break
		}
		seen[key] = q
	}
	if len(questions) != 2 {
		t.Fatal("collision fixture not found")
	}
	dir := t.TempDir()
	paths := map[string]bool{}
	for _, q := range questions {
		res := &agent.ResearchResult{Question: q, Summary: &agent.Summary{Report: q}}
		for _, write := range []func(*agent.ResearchResult, string) (string, error){WriteMarkdown, WritePDF,
			func(r *agent.ResearchResult, dir string) (string, error) {
				return WriteMetadata(BuildMeta(r, nil, "quick", 0, 0), dir)
			},
		} {
			path, err := write(res, dir)
			if err != nil {
				t.Fatal(err)
			}
			if paths[path] {
				t.Errorf("different questions overwrite %s", path)
			}
			paths[path] = true
		}
	}
}

func TestSourceCategoriesRespectURLBoundaries(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/org/repo":                       "",
		"https://gitlab.com/user":                           "",
		"https://github.com/org/repo/issues/1":              "",
		"https://example.com/?url=https://docs.example.org": "",
		"https://example.com/github.com/docs.fake":          "",
		"https://notarxiv.org/paper":                        "",
		"https://arxiv.org.evil.example/paper":              "",
		"https://investor.anything.com":                     "",
		"https://my-news.example.com":                       "",
		"https://sci-hub.se/paper":                          "",
		"https://www.mit.edu/research":                      "Education",
		"https://cs.stanford.edu":                           "Education",
		"https://plato.stanford.edu/entries/logic":          "Reference",
		"https://docs.python.org/3":                         "Documentation",
		"https://developer.mozilla.org/en-US":               "Documentation",
		"https://go.dev/doc/effective_go":                   "Documentation",
		"https://example.com/docs":                          "Documentation",
		"https://example.com/docstrings":                    "",
		"https://NEWS.YCOMBINATOR.COM:443/item?id=1":        "Forums / Q&A",
		"https://news.example.com/story":                    "News / Media",
		"https://www.bbc.co.uk/news":                        "News / Media",
		"https://www.sec.gov/filing":                        "Financial / Filings",
		"https://pubmed.ncbi.nlm.nih.gov/123":               "Academic / Pre-prints",
		"https://www.energy.gov":                            "Government",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := categorize(raw); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// The tree keys nodes by sub-agent ID, so the ID invented for an empty one
// must not land on an ID a real sub-agent already uses: the two branches would
// merge and the run would report fewer sub-agents than it ran.
func TestSyntheticSubAgentIDsNeverCollide(t *testing.T) {
	r := NewRenderer(discard{}, Theme{})
	r.apply(Event{Type: SubAgent, SubID: "n2", SubName: "Real", SubState: "running"})
	r.apply(Event{Type: SubAgent, SubID: "", SubName: "Anonymous", SubState: "running"})
	r.apply(Event{Type: SubAgent, SubID: "", SubName: "Another", SubState: "running"})
	if len(r.order) != 3 || len(r.nodes) != 3 {
		t.Fatalf("sub-agents merged: %v", r.order)
	}
	names := map[string]bool{}
	for _, n := range r.nodes {
		names[n.Name] = true
	}
	for _, want := range []string{"Real", "Anonymous", "Another"} {
		if !names[want] {
			t.Errorf("lost sub-agent %q: %v", want, names)
		}
	}
}

// Unverified findings are model-written, and a model routinely writes a bare
// "example.com/page". DomainOf already resolves those for the citation list,
// so the category card must not file them all under "Other".
func TestSourceCategoriesAcceptSchemelessURLs(t *testing.T) {
	for raw, want := range map[string]string{
		"www.example-university.edu/research": "Education",
		"docs.example.org/guide":              "Documentation",
		"//news.example.com/story":            "News / Media",
		"mailto:someone@example.com":          "",
		"not a url":                           "",
	} {
		if got := categorize(raw); got != want {
			t.Errorf("categorize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDisplayWidthDoesNotAllocate(t *testing.T) {
	s := strings.Repeat("\x1b[31m名字 café\x1b[0m ", 50)
	if n := testing.AllocsPerRun(100, func() { dispWidth(s) }); n != 0 {
		t.Errorf("width scan allocated %g times", n)
	}
}

func TestQueryBudgetUsesUnicodeColumns(t *testing.T) {
	q, facet := strings.Repeat("界", 30), strings.Repeat("研", 30)
	got := subQueries(q, agent.SubTopic{Name: facet})[0]
	want := q + " " + strings.Repeat("研", 19)
	if got != want || !utf8.ValidString(got) || dispWidth(got) > maxQueryLen {
		t.Errorf("query = %q (%d columns), want %q", got, dispWidth(got), want)
	}
}

func TestExportsDoNotRepeatOpeningSummary(t *testing.T) {
	for _, report := range []string{
		"# A Model-Written Title\n\n## Executive Summary\n\nOpening paragraph.\n\n## Details\n\nMore evidence.",
		"# Research Report: Question\n\n## Executive Summary\n\nOpening paragraph.\n\n## Details\n\nMore evidence.",
		"Opening paragraph.\n\n## Details\n\nMore evidence.",
		"# Question\n\n## Executive Summary\n\nOpening paragraph.\n\nMore summary context.\n\n## Details\n\nMore evidence.",
	} {
		res := &agent.ResearchResult{Question: "Question", Summary: &agent.Summary{Executive: "Opening paragraph.", Report: report}}
		md := MarkdownReport(res)
		if strings.Count(md, "Opening paragraph.") != 1 || strings.Count(md, "## Executive Summary") != 1 {
			t.Errorf("duplicated executive summary:\n%s", md)
		}
		if strings.Count(md, "# Question\n") != 1 || strings.Contains(md, "# Research Report:") {
			t.Errorf("duplicate title:\n%s", md)
		}
		if strings.Contains(report, "More summary context.") && !strings.Contains(md, "More summary context.") {
			t.Error("lost summary continuation")
		}
		path, err := WritePDF(res, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		pdf, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(pdf), "Opening paragraph.") != 1 {
			t.Error("PDF repeats summary")
		}
	}
}

// The same duplication as the Markdown-heading case, for the bold heading
// models write just as often: the export must not add its own "## Executive
// Summary" above a report that already opens with one.
func TestExportsKeepEmphasizedExecutiveHeadingOnce(t *testing.T) {
	for _, heading := range []string{"**Executive Summary**", "__Resumen ejecutivo__"} {
		res := &agent.ResearchResult{Question: "Question", Summary: &agent.Summary{
			Executive: "Opening paragraph.",
			Report:    heading + "\n\nOpening paragraph.\n\n## Details\n\nMore evidence.",
		}}
		md := MarkdownReport(res)
		if strings.Count(md, "Opening paragraph.") != 1 {
			t.Errorf("%s: duplicated executive summary:\n%s", heading, md)
		}
		if !strings.Contains(md, heading) {
			t.Errorf("%s: lost the report's own heading:\n%s", heading, md)
		}
		if strings.Contains(md, "## Executive Summary") {
			t.Errorf("%s: added a second executive heading:\n%s", heading, md)
		}
	}
}

func TestPDFRendersMarkdownAsReadableText(t *testing.T) {
	res := &agent.ResearchResult{Question: "Question", Summary: &agent.Summary{Report: "# Heading\n\nA **bold** and *italic* [source](https://example.com).\n\n- `code_value`\n\n```go\nx := 2 * 3\n```"}}
	path, err := WritePDF(res, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pdf := string(data)
	for _, raw := range []string{"# Heading", "**bold**", "*italic*", "[source]", "`code_value`", "```"} {
		if strings.Contains(pdf, raw) {
			t.Errorf("PDF contains Markdown syntax %q", raw)
		}
	}
	for _, text := range []string{"Heading", "bold", "italic", "source", "https://example.com", "code_value", "x := 2 * 3"} {
		if !strings.Contains(pdf, text) {
			t.Errorf("PDF lost %q", text)
		}
	}
}

func TestMarkdownTablesRenderWithoutSourceDelimiters(t *testing.T) {
	var drawn []string
	for _, tx := range pdfTexts(t, BuildPDF("| Read:Write Ratio | Choice |\n| --- | --- |\n| 1:1 | **Mutex** |\n| 10:1 | RWMutex |\n")) {
		drawn = append(drawn, tx.text)
	}
	got := strings.Join(drawn, " ")
	if strings.ContainsAny(got, "|*") || strings.Contains(got, "---") {
		t.Errorf("table contains Markdown source delimiters: %q", got)
	}
	for _, want := range []string{"Read:Write Ratio", "Choice", "1:1", "10:1", "Mutex", "RWMutex"} {
		if !strings.Contains(got, want) {
			t.Errorf("table lost %q: %q", want, got)
		}
	}
}

func TestExportsRetainLocalizedExecutiveSummaryOnce(t *testing.T) {
	summary := "La elección depende de la carga."
	res := &agent.ResearchResult{Question: "¿Cuándo elegir Mutex?", Summary: &agent.Summary{
		Executive: summary,
		Report:    "# Reporte de investigación\n\n## Resumen ejecutivo\n\n" + summary + "\n\n## Detalles\n\nMedir antes de elegir.",
	}}
	got := MarkdownReport(res)
	if strings.Count(got, summary) != 1 {
		t.Errorf("localized summary duplicated:\n%s", got)
	}
	if !strings.Contains(got, "## Resumen ejecutivo") {
		t.Errorf("localized heading lost:\n%s", got)
	}
}

// Results rejected as off-topic must be visible in the run, and must not be
// counted, cited or charged against the source budget. A filter nobody can
// see is indistinguishable from a search that found nothing.
func TestOffTopicResultsAreReportedButNotCounted(t *testing.T) {
	topics := []agent.SubTopic{{ID: "1", Name: "Alpha"}}
	plan := newTestPlan("quick", topics)
	q := subQueries(plan.Question, topics[0])[0]
	fa := &fakeAssistant{
		planTopics: topics,
		skipped: map[string][]agent.SkippedSource{
			q: {{URL: "https://viveros.example/1", Domain: "viveros.example", Title: "Wholesale nursery price list", Score: 0.05}},
		},
	}
	sink := &MultiSink{}
	d := NewDriver(fa, sink, nil, 1)
	res, err := d.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Findings {
		if f.URL == "https://viveros.example/1" {
			t.Error("off-topic source reached the findings")
		}
	}
	var reported, line Event
	for _, e := range sink.Timeline {
		if e.Type == Verify && e.Status == "offtopic" {
			reported = e
		}
		if e.Type == Info && e.SubID == "1" && strings.Contains(e.Detail, "off-topic") {
			line = e
		}
		if e.Type == Citation && e.SourceURL == "https://viveros.example/1" {
			t.Error("off-topic source was cited")
		}
	}
	if reported.URL != "https://viveros.example/1" {
		t.Fatalf("off-topic source never reported: %+v", sink.Timeline)
	}
	// One line per search names them; a line per result buried the log.
	if !strings.Contains(line.Detail, "1 of 2 results off-topic: viveros.example") {
		t.Errorf("off-topic results have no readable line: %q", line.Detail)
	}
	last := sink.Timeline[len(sink.Timeline)-1]
	if last.Sources != len(res.Findings) {
		t.Errorf("source count %d includes off-topic results (%d findings)", last.Sources, len(res.Findings))
	}
}

// A live run retrieved 22 sources, the report cited 7, and the export listed
// all 22 under "Citations" — presenting pages the report never used, noise
// included, as the evidence behind it. What was cited and what was merely
// retrieved are different claims and must read differently.
func TestExportSeparatesCitedSourcesFromRetrieved(t *testing.T) {
	res := &agent.ResearchResult{
		Question: "Question",
		Summary:  &agent.Summary{Report: "## Executive Summary\n\nBody cites [one](https://cited.example/a) source."},
		Findings: []agent.Finding{
			{Title: "Cited", URL: "https://cited.example/a", Status: "ok"},
			{Title: "Unused", URL: "https://unused.example/b", Status: "ok"},
			{Title: "Noise", URL: "https://noise.example/c", Status: "ok"},
		},
	}
	md := MarkdownReport(res)
	if !strings.Contains(md, "## Citations (1)") {
		t.Errorf("citation count claims sources the report never used:\n%s", md)
	}
	if !strings.Contains(md, "## Other Sources Retrieved (2)") {
		t.Errorf("retrieved-but-unused sources not reported separately:\n%s", md)
	}
	// Compare the two list sections, not the whole document: the cited URL
	// also appears earlier, inside the report body that links to it.
	citedList, retrievedList, found := strings.Cut(md[strings.Index(md, "## Citations (1)"):], "## Other Sources Retrieved (2)")
	if !found {
		t.Fatalf("sections not laid out as expected:\n%s", md)
	}
	if !strings.Contains(citedList, "https://cited.example/a") {
		t.Errorf("cited source missing from the citations section:\n%s", citedList)
	}
	for _, url := range []string{"https://unused.example/b", "https://noise.example/c"} {
		if strings.Contains(citedList, url) {
			t.Errorf("%s listed as a citation", url)
		}
		if !strings.Contains(retrievedList, url) {
			t.Errorf("%s missing from the retrieved list", url)
		}
	}

	meta := BuildMeta(res, nil, "quick", 0, 3)
	byURL := map[string]bool{}
	for _, c := range meta.Citations {
		byURL[c.URL] = c.Cited
	}
	if !byURL["https://cited.example/a"] || byURL["https://unused.example/b"] {
		t.Errorf("sidecar does not record which sources the report cited: %+v", meta.Citations)
	}
}

// When the report embeds no links at all, nothing distinguishes cited from
// retrieved, so the export must not claim the report cited nothing.
func TestExportKeepsOneListWhenReportCitesNothing(t *testing.T) {
	res := &agent.ResearchResult{
		Question: "Question",
		Summary:  &agent.Summary{Report: "## Executive Summary\n\nA report with no inline links."},
		Findings: []agent.Finding{{Title: "One", URL: "https://a.example/1", Status: "ok"}},
	}
	md := MarkdownReport(res)
	if !strings.Contains(md, "## Citations (1)") || strings.Contains(md, "Other Sources Retrieved") {
		t.Errorf("split a report that cites nothing:\n%s", md)
	}
}

// strings.Contains(body, "") is always true, so a finding with no URL was
// marked cited whatever the report said — and that one "cited" source was
// enough to defeat the fallback above, demoting every real source.
func TestURLlessFindingIsNotCited(t *testing.T) {
	res := &agent.ResearchResult{
		Question: "Question",
		Summary:  &agent.Summary{Report: "## Executive Summary\n\nA report with no inline links."},
		Findings: []agent.Finding{
			{Title: "One", URL: "https://a.example/1", Status: "ok"},
			{Title: "Model recollection"},
		},
	}
	for _, c := range citations(res, res.Summary.Report) {
		if c.Cited {
			t.Errorf("%q marked cited by a report that links nothing", c.Title)
		}
	}
	if md := MarkdownReport(res); !strings.Contains(md, "## Citations (2)") || strings.Contains(md, "Other Sources Retrieved") {
		t.Errorf("a URL-less finding split a report that cites nothing:\n%s", md)
	}
}

// A branch can finish having contributed nothing — its results were all noise,
// or all pages another branch had already claimed. Reporting "complete" for
// those exactly as for a branch that gathered five sources lets a plan lose a
// whole facet while every branch still reads as done.
type contributionRun struct {
	fa            *fakeAssistant
	sink          *MultiSink
	res           *agent.ResearchResult
	winner, loser string
	names         map[string]string
}

// runContributionScenario runs three branches: two race for the same page, so
// one contributes it and the other finds it already cited, and a third has
// every result rejected as off-topic.
func runContributionScenario(t *testing.T) contributionRun {
	t.Helper()
	shared := "https://shared.example/page"
	topics := []agent.SubTopic{{ID: "1", Name: "Alpha"}, {ID: "2", Name: "Beta"}, {ID: "3", Name: "Gamma"}}
	plan := newTestPlan("quick", topics)
	qa, qb, qc := subQueries(plan.Question, topics[0])[0], subQueries(plan.Question, topics[1])[0], subQueries(plan.Question, topics[2])[0]
	fa := &fakeAssistant{
		planTopics: topics,
		findings: map[string][]agent.Finding{
			qa: {{Title: "Only source", URL: shared}},
			qb: {{Title: "Same page again", URL: shared}},
			qc: {},
		},
		signals: map[string][]agent.SourceSignal{
			qa: {{URL: shared, Domain: "shared.example", Status: "ok"}},
			qb: {{URL: shared, Domain: "shared.example", Status: "ok"}},
		},
		skipped: map[string][]agent.SkippedSource{
			qc: {{URL: "https://viveros.example/1", Domain: "viveros.example", Score: 0.04}},
		},
	}
	sink := &MultiSink{}
	res, err := NewDriver(fa, sink, nil, 1).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// Either branch may win the race for the shared page; the winner is
	// whichever one emitted the citation.
	run := contributionRun{fa: fa, sink: sink, res: res, winner: "1", loser: "2",
		names: map[string]string{"1": "Alpha", "2": "Beta", "3": "Gamma"}}
	for _, e := range sink.Timeline {
		if e.Type == Citation && e.SubID == "2" {
			run.winner, run.loser = "2", "1"
		}
	}
	return run
}

func TestBranchesReportWhatTheyContributed(t *testing.T) {
	run := runContributionScenario(t)
	lines := map[string]string{}
	for _, e := range run.sink.Timeline {
		if e.Type == SubAgent && e.SubState == "done" {
			lines[e.SubID] = e.Line
		}
	}
	if lines[run.winner] != "complete" {
		t.Errorf("contributing branch reported %q", lines[run.winner])
	}
	if !strings.Contains(lines[run.loser], "already cited") {
		t.Errorf("all-duplicate branch reported %q", lines[run.loser])
	}
	if !strings.Contains(lines["3"], "no relevant sources") {
		t.Errorf("all-off-topic branch reported %q", lines["3"])
	}
}

func TestExportedNotesRecordBranchContribution(t *testing.T) {
	run := runContributionScenario(t)
	meta := BuildMeta(run.res, run.sink.Timeline, "quick", run.res.Tokens, len(run.res.Findings))
	got := map[string]int{}
	for _, n := range meta.Notes {
		got[n.SubID] = n.Sources
	}
	if got[run.winner] != 1 || got[run.loser] != 0 || got["3"] != 0 {
		t.Errorf("exported notes do not record contribution: %+v", meta.Notes)
	}
}

// The analysis can only report a facet that came back empty if it is told
// which ones did: the findings list shows what was gathered, never what was
// planned and missed.
func TestAnalysisIsToldWhichSubTopicsFoundNothing(t *testing.T) {
	run := runContributionScenario(t)
	for _, id := range []string{run.loser, "3"} {
		if !strings.Contains(run.fa.analyzed, run.names[id]) {
			t.Errorf("analyze prompt never names uncovered sub-topic %q:\n%s", run.names[id], run.fa.analyzed)
		}
	}
	if strings.Contains(run.fa.analyzed, "  - "+run.names[run.winner]) {
		t.Errorf("analyze prompt lists the contributing sub-topic as uncovered:\n%s", run.fa.analyzed)
	}
}

// Planning is one model call with nothing on screen behind it. The spinner
// showed only elapsed time, so a 30-second wait gave no clue what was being
// waited on — or that it had been retried.
func TestSpinnerShowsWhatPlanningIsWaitingOn(t *testing.T) {
	// The spinner repaints from its own goroutine, so the test's reads have to
	// be synchronised with them — a plain bytes.Buffer is a data race here.
	buf := &syncBuffer{}
	set, stop := spin(buf, "Planning research")
	set("asking a-model for sub-topics")
	// The frame is repainted on a ticker; wait for one carrying the status.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "asking a-model") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	out := buf.String()
	if !strings.Contains(out, "Planning research") {
		t.Errorf("spinner lost its message: %q", out)
	}
	if !strings.Contains(out, "asking a-model for sub-topics") {
		t.Errorf("spinner never showed the live status: %q", out)
	}
	if !strings.HasSuffix(out, "\r"+eraseLine) {
		t.Errorf("spinner left its row on screen: %q", out)
	}
}

// The assistant reports which model it is asking, and reports a retry. Both
// happen during planning, and both used to go nowhere: the progress logger was
// only wired afterwards, for the research phase.
func TestPlanningSurfacesAssistantProgress(t *testing.T) {
	fa := &progressDuringPlan{fakeAssistant: &fakeAssistant{}, msg: "asking test-model for sub-topics"}
	var stderr bytes.Buffer
	o := Options{Question: "q", Assistant: fa, Stderr: &stderr, Stdout: &stderr, DepthMode: "quick"}.withDefaults()
	if _, err := o.buildPlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "asking test-model") {
		t.Errorf("planning hid the assistant's progress: %q", stderr.String())
	}
}

// progressDuringPlan reports a progress message from inside Plan, the way the
// real assistant does when it starts the call and when it retries.
type progressDuringPlan struct {
	*fakeAssistant
	msg  string
	logf func(string)
}

func (p *progressDuringPlan) SetProgress(logf func(string)) { p.logf = logf }

func (p *progressDuringPlan) Plan(ctx context.Context, question string, subTopics int) (agent.Outline, error) {
	if p.logf != nil {
		p.logf(p.msg)
	}
	return p.fakeAssistant.Plan(ctx, question, subTopics)
}

// syncBuffer is a bytes.Buffer safe to read while a goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The planner was asked for "3-6 sub-topics" whatever the depth tier, and the
// run's total budget is per-branch sources times however many it returned. So
// the tier set depth-per-branch while the model set breadth, and their product
// could invert the tiers: quick over six sub-topics gathered more than deep
// over three. Breadth is part of what "deep" means and belongs to the tier.
func TestDepthTierSetsPlanBreadth(t *testing.T) {
	seen := map[string]int{}
	for _, tier := range DepthModes {
		fa := &breadthRecorder{fakeAssistant: &fakeAssistant{}}
		plan, err := NewPlanner(fa, tier).Plan(context.Background(), "q")
		if err != nil {
			t.Fatal(err)
		}
		if fa.asked != tier.SubTopics || tier.SubTopics == 0 {
			t.Errorf("%s: planner asked for %d sub-topics, want the tier's %d", tier.Key, fa.asked, tier.SubTopics)
		}
		seen[tier.Key] = plan.MaxSources
	}
	// Whole-run budget must rise with the tier, which it cannot do while the
	// model decides breadth.
	if seen["quick"] >= seen["standard"] || seen["standard"] >= seen["deep"] {
		t.Errorf("depth tiers are not monotonic: %+v", seen)
	}
}

// Models treat a requested count as a suggestion. A tier that asked for three
// and got eight would silently run eight sub-agents.
func TestPlanBreadthIsCappedAtTheTier(t *testing.T) {
	tier := DepthModes[0] // quick
	fa := &breadthRecorder{fakeAssistant: &fakeAssistant{}, over: 8}
	plan, err := NewPlanner(fa, tier).Plan(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SubTopics) != tier.SubTopics {
		t.Errorf("planner returned %d sub-topics, want them capped at %d", len(plan.SubTopics), tier.SubTopics)
	}
}

// breadthRecorder records the sub-topic count it was asked for, and can return
// more than that to stand in for a model ignoring the request.
type breadthRecorder struct {
	*fakeAssistant
	asked int
	over  int
}

func (b *breadthRecorder) Plan(ctx context.Context, question string, subTopics int) (agent.Outline, error) {
	b.asked = subTopics
	n := subTopics
	if b.over > 0 {
		n = b.over
	}
	out := make([]agent.SubTopic, n)
	for i := range out {
		out[i] = agent.SubTopic{ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("Topic %d", i+1)}
	}
	return agent.Outline{SubTopics: out}, nil
}

// Analyze, fact-check and summarize each re-embed every finding's content, so
// the prompt — and the call — grow with the whole run. A live run hit the
// 2-minute model deadline during analyze and retried at 4 minutes, discarding
// the two minutes already spent. Per-finding content is now bounded, which
// bounds the phase.
func TestPostResearchPromptsBoundFindingContent(t *testing.T) {
	long := strings.Repeat("é", 4000) // multi-byte: truncation must not split a rune
	findings := make([]agent.Finding, 20)
	for i := range findings {
		findings[i] = agent.Finding{Title: "T", URL: "https://a.example/x", Content: long}
	}
	analysis := &agent.Analysis{Answer: "answer"}
	for name, prompt := range map[string]string{
		"analyze":    analyzePrompt("q", findings, nil),
		"fact-check": factCheckPrompt("claims", findings, true, nil),
		"summarize":  summarizePrompt("q", analysis, nil, findings, true),
	} {
		t.Run(name, func(t *testing.T) {
			if !utf8.ValidString(prompt) {
				t.Error("truncation split a UTF-8 rune")
			}
			if n := len([]rune(long)); strings.Contains(prompt, long[:n]) {
				t.Error("a finding's full content reached the prompt")
			}
			// Twenty sources must not carry twenty times the per-source cap.
			if got, limit := len(prompt), 20*maxPromptFindingChars*2; got > limit {
				t.Errorf("prompt is %d bytes, want it bounded under %d", got, limit)
			}
			if !strings.Contains(prompt, "truncated") {
				t.Error("truncation is not marked, so the model cannot tell the source was cut")
			}
		})
	}
}

// Short findings must survive untouched: the cap exists to bound a runaway
// prompt, not to trim every source.
func TestShortFindingsAreNotTruncated(t *testing.T) {
	content := "a short but complete source excerpt"
	prompt := analyzePrompt("q", []agent.Finding{{Title: "T", URL: "u", Content: content}}, nil)
	if !strings.Contains(prompt, content) || strings.Contains(prompt, "truncated") {
		t.Errorf("short content was altered:\n%s", prompt)
	}
}

// terminalSpy is an Input that records whether the terminal was handed back
// and keeps serving keys, so a key reader that did not stop is visible.
type terminalSpy struct {
	keys   chan byte
	closed chan struct{}
	once   sync.Once
	reads  atomic.Int64
}

func newTerminalSpy() *terminalSpy {
	return &terminalSpy{keys: make(chan byte, 8), closed: make(chan struct{})}
}

func (s *terminalSpy) Next() (byte, error) {
	s.reads.Add(1)
	b, ok := <-s.keys
	if !ok {
		return 0, io.EOF
	}
	return b, nil
}

func (s *terminalSpy) NextLine() (string, error) { return "", io.EOF }
func (s *terminalSpy) Raw() bool                 { return true }
func (s *terminalSpy) Close()                    { s.once.Do(func() { close(s.closed) }) }

func (s *terminalSpy) wasClosed(d time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(d):
		return false
	}
}

// Pressing "b" only stopped the renderer from painting. The terminal stayed in
// raw mode and the key reader kept eating every keystroke, so typed input was
// invisible and swallowed — and Esc could still kill a run the reader believed
// they had put in the background.
func TestDetachHandsTheTerminalBackAndStopsReadingKeys(t *testing.T) {
	spy := newTerminalSpy()
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, spy, 1)
	cancelled := make(chan struct{})
	d.cancel = func() { close(cancelled) }
	d.startKeyReader()

	spy.keys <- byte(KeyB)
	if !spy.wasClosed(2 * time.Second) {
		t.Fatal("detaching left the terminal in raw mode")
	}

	// Anything typed after detaching belongs to the reader, not to the run.
	// Esc is the sharpest case: a reader who stepped away must not come back
	// to a run their own typing cancelled.
	spy.keys <- byte(KeyEsc)
	select {
	case <-cancelled:
		t.Error("the key reader kept consuming keys after the terminal was released")
	case <-time.After(200 * time.Millisecond):
	}
	if n := spy.reads.Load(); n != 1 {
		t.Errorf("read %d keys after detaching, want 1 (the detach key itself)", n)
	}
}

// Detaching twice must restore the terminal once and announce itself once:
// Ctrl-Z and "b" are the same directive, and the deferred Close in ui.Run
// still runs over the same Input afterwards.
func TestDetachIsIdempotent(t *testing.T) {
	spy := newTerminalSpy()
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, spy, 1)
	d.detach()
	d.detach()
	spy.Close() // as ui.Run's deferred close does
	detaches := 0
	for _, e := range sink.Timeline {
		if e.Type == Detach {
			detaches++
		}
	}
	if detaches != 1 {
		t.Errorf("emitted %d detach events, want 1", detaches)
	}
}

// --detach starts a run with no display to release later and no directive to
// accept, so holding the terminal in raw mode with a reader on it swallows the
// reader's input for nothing.
func TestARunStartedDetachedNeverTakesTheTerminal(t *testing.T) {
	spy := newTerminalSpy()
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, spy, 1)
	d.detached = true
	if _, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "S"}})); err != nil {
		t.Fatal(err)
	}
	if !spy.wasClosed(time.Second) {
		t.Error("a run started detached kept the terminal in raw mode")
	}
	if n := spy.reads.Load(); n != 0 {
		t.Errorf("read %d keys with no display to drive, want 0", n)
	}
}

// Sub-agent parallelism was a literal 3 in ui.Run, so the budget a caller
// asked for was dropped on the floor: a deep plan always searched
// three-at-a-time, however many sub-topics it had. It is a configured budget
// now, and it has to survive the trip from Options to the driver.
func TestOptionsCarryParallelismIntoTheRun(t *testing.T) {
	// Four sub-topics with a budget of four: a run still capped at three
	// could never reach a peak of four.
	ca := &countingAssistant{Assistant: &fakeAssistant{
		planTopics: []agent.SubTopic{
			{ID: "1", Name: "Alpha"}, {ID: "2", Name: "Beta"},
			{ID: "3", Name: "Gamma"}, {ID: "4", Name: "Delta"},
		},
	}}
	var out, errOut bytes.Buffer
	if _, err := Run(context.Background(), Options{
		Question:    "does the budget reach the fan-out?",
		Assistant:   ca,
		Parallelism: 4,
		Quiet:       true,
		OutDir:      t.TempDir(),
		Stdout:      &out,
		Stderr:      &errOut,
		Bell:        func(string) {},
	}); err != nil {
		t.Fatal(err)
	}
	if peak := ca.peak(); peak != 4 {
		t.Errorf("peak concurrency = %d, want 4: Options.Parallelism never reached the driver", peak)
	}
}

// ---- --jsonl --silent -----------------------------------------------------

// announce checked Quiet before JSONL, so `--jsonl --silent` wrote a rendered
// Markdown report into the middle of the event stream and the stream that is
// documented as machine-readable no longer parsed.
func TestJSONLStreamStaysParseableUnderSilent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := Run(context.Background(), Options{
		Question:  "How will solid-state batteries commercialize?",
		Assistant: &fakeAssistant{summary: "# A report\n\nWith prose in it."},
		DepthMode: "quick",
		JSONL:     true,
		Quiet:     true,
		OutDir:    t.TempDir(),
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected an event stream, got %q", stdout.String())
	}
	for i, ln := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("stdout line %d is not an event: %q", i+1, ln)
		}
	}
}

// ---- event phases ---------------------------------------------------------

// phaseProbe emits a phase-less progress line from inside each post-research
// phase, exactly as the assistant's progress logger does when a model call
// reports which model it is asking or that it is retrying.
type phaseProbe struct {
	*fakeAssistant
	emit func(Event)
}

func (p phaseProbe) Analyze(ctx context.Context, prompt string) (*agent.Analysis, error) {
	p.emit(Event{Type: Info, Detail: "analyze progress"})
	return p.fakeAssistant.Analyze(ctx, prompt)
}

func (p phaseProbe) FactCheck(ctx context.Context, claims string) (*agent.FactCheckResult, error) {
	p.emit(Event{Type: Info, Detail: "fact-check progress"})
	return p.fakeAssistant.FactCheck(ctx, claims)
}

func (p phaseProbe) Summarize(ctx context.Context, prompt string) (*agent.Summary, error) {
	p.emit(Event{Type: Info, Detail: "summarize progress"})
	return p.fakeAssistant.Summarize(ctx, prompt)
}

// Every event that named no phase was stamped "Research", so a retry during
// Analyze, Fact-Check or Summarize was filed in the JSONL timeline under a
// phase that had already finished.
func TestPhaselessEventsCarryTheRunningPhase(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(nil, sink, nil, 1)
	d.Agent = phaseProbe{fakeAssistant: &fakeAssistant{}, emit: d.emit}
	if _, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}})); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"analyze progress":    "Analyze",
		"fact-check progress": "Fact-Check",
		"summarize progress":  "Summarize",
	}
	seen := map[string]string{}
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Info {
			seen[e.Detail] = e.Phase
		}
	}
	for detail, phase := range want {
		if seen[detail] != phase {
			t.Errorf("%q recorded in phase %q, want %q", detail, seen[detail], phase)
		}
	}
}

// ---- timeline export ------------------------------------------------------

// writeArtifacts ranged over the live timeline while the key reader could
// still emit a late detach into it.
func TestTimelineSnapshotIsSafeWhileEmitting(t *testing.T) {
	sink := &MultiSink{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			sink.Emit(Event{Type: Detach, Detail: "b"})
		}
	}()
	for i := 0; i < 200; i++ {
		for _, e := range sink.TimelineSnapshot() {
			_ = e.Detail
		}
	}
	<-done
}

// ---- the --sources pin ----------------------------------------------------

// Editing the sub-topic list re-derived the budget from the depth tier, so
// pressing "e" or "x" after --sources 9 silently reverted every branch to the
// tier default — the reduction --sources exists to report.
func TestEditingSubTopicsKeepsTheSourcesPin(t *testing.T) {
	base := &Plan{
		Question: "q", Depth: depthMode("quick"),
		SubTopics: []SubTopic{{ID: "1", Name: "One"}, {ID: "2", Name: "Two"}},
	}
	pinned, capped := base.WithSourcesPerTopic(9)
	if capped || pinned.PerTopic() != 9 {
		t.Fatalf("pin not applied: per-topic %d, capped %v", pinned.PerTopic(), capped)
	}

	added := *pinned
	added.SubTopics = append(slices.Clone(pinned.SubTopics), SubTopic{Name: "Three"})
	if got := renumbered(&added, (*Plan).WithDepth).PerTopic(); got != 9 {
		t.Errorf("after adding a sub-topic: %d sources per sub-agent, want the pinned 9", got)
	}

	deleted := *pinned
	deleted.SubTopics = slices.Clone(pinned.SubTopics)[:1]
	if got := renumbered(&deleted, (*Plan).WithDepth).PerTopic(); got != 9 {
		t.Errorf("after deleting a sub-topic: %d sources per sub-agent, want the pinned 9", got)
	}
}

// Choosing a depth tier in the brief is a later, explicit choice by the
// reader, so it — and only it — drops the pin the flag set.
func TestDepthKeyOverridesTheSourcesPin(t *testing.T) {
	base := &Plan{Question: "q", Depth: depthMode("quick"),
		SubTopics: []SubTopic{{ID: "1", Name: "One"}, {ID: "2", Name: "Two"}}}
	pinned, _ := base.WithSourcesPerTopic(9)
	tiered := pinned.WithDepth(depthMode("standard"))
	if tiered.PinnedPerTopic != 0 || tiered.PerTopic() != DepthModes[1].MaxSources {
		t.Errorf("depth tier did not win: pin %d, per-topic %d",
			tiered.PinnedPerTopic, tiered.PerTopic())
	}
}

// ---- fact-check honesty ---------------------------------------------------

// With no search backend the model supplies the findings, the URLs and the
// page text, so "verifying claims against sources" checks the model against
// its own recollection. The pass still runs; it just no longer claims to be
// verification.
func TestFactCheckSaysSoWhenNothingWasRetrieved(t *testing.T) {
	unretrieved := []agent.Finding{{Title: "A", URL: "https://a.example", Status: "unverified"}}
	retrieved := []agent.Finding{
		{Title: "A", URL: "https://a.example", Status: "unverified"},
		{Title: "B", URL: "https://b.example", Status: "degraded"},
	}
	if sourcesRetrieved(unretrieved) {
		t.Error("a run that fetched nothing reported retrieved sources")
	}
	if !sourcesRetrieved(retrieved) {
		t.Error("a snippet from a search backend is a retrieved source")
	}

	self := factCheckPrompt("claim", unretrieved, false, nil)
	if !strings.Contains(self, "not fetched") || !strings.Contains(self, "unverified") {
		t.Errorf("self-consistency prompt does not say what the material is:\n%s", self)
	}
	if strings.HasPrefix(self, "Verify these claims") {
		t.Error("self-consistency prompt still asks for verification")
	}
	if got := factCheckPrompt("claim", retrieved, true, nil); !strings.HasPrefix(got, "Verify these claims") {
		t.Errorf("retrieved-source prompt changed:\n%s", got)
	}

	if got := factCheckDetail(unretrieved); !strings.Contains(got, "self-consistency") {
		t.Errorf("phase line %q does not say what is being checked", got)
	}
	if got := summarizePrompt("q", &agent.Analysis{Answer: "a"}, nil, unretrieved, false); !strings.Contains(got, "No source was retrieved") {
		t.Errorf("summarizer was not told the sources were never fetched:\n%s", got)
	}
}

// ---- follow-up query ------------------------------------------------------

// A sub-agent issued exactly one query, ever: a branch whose search came back
// with nothing usable reported nothing and the run lost the whole facet.
func TestSubAgentRetriesWhenTheFirstQueryYieldsNothing(t *testing.T) {
	const question = "q"
	sub := agent.SubTopic{ID: "1", Name: "Facet", Query: "perovskite tandem degradation"}
	queries := subQueries(question, sub)
	if len(queries) != 2 {
		t.Fatalf("expected a follow-up query, got %q", queries)
	}
	fa := &fakeAssistant{
		planTopics: []agent.SubTopic{sub},
		// The first query's only result is dropped: nothing was retrieved, so
		// it spends no budget and the branch is still empty.
		findings: map[string][]agent.Finding{
			queries[0]: {{Title: "Dead link", URL: "https://dead.example"}},
			queries[1]: {{Title: "Real page", URL: "https://real.example"}},
		},
		signals: map[string][]agent.SourceSignal{
			queries[0]: {{URL: "https://dead.example", Domain: "dead.example", Status: "dropped", Code: "404"}},
			queries[1]: {{URL: "https://real.example", Domain: "real.example", Status: "ok"}},
		},
	}
	sink := &MultiSink{}
	d := NewDriver(fa, sink, nil, 1)
	res, err := d.Run(context.Background(), &Plan{
		Question: question, Depth: depthMode("quick"), MaxSources: 3, SubTopics: []SubTopic{sub},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].URL != "https://real.example" {
		t.Fatalf("the follow-up query's source is missing: %+v", res.Findings)
	}
	var searched []string
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Search {
			searched = append(searched, e.Query)
		}
	}
	if len(searched) != 2 {
		t.Errorf("queries issued: %q, want both the primary and the follow-up", searched)
	}
}

// A branch that fills its budget on the first query never issues the second:
// the follow-up is a re-formulation, not a doubling of every run.
func TestSubAgentStopsAtOneQueryWhenTheFirstSucceeds(t *testing.T) {
	sub := agent.SubTopic{ID: "1", Name: "Facet", Query: "perovskite tandem degradation"}
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{planTopics: []agent.SubTopic{sub}}, sink, nil, 1)
	if _, err := d.Run(context.Background(), &Plan{
		Question: "q", Depth: depthMode("quick"), MaxSources: 1, SubTopics: []SubTopic{sub},
	}); err != nil {
		t.Fatal(err)
	}
	searches := 0
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Search {
			searches++
		}
	}
	if searches != 1 {
		t.Errorf("%d searches for a branch that filled its budget, want 1", searches)
	}
}

// ---- progress lines are not errors ----------------------------------------

// Interactive runs routed "llm agent searching" and "retrying…" through the
// Error event, so the activity feed rendered ordinary progress with the
// failure glyph and a healthy run read as a stream of errors.
func TestProgressLinesRenderWithoutTheErrorGlyph(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	r.Emit(Event{Type: Info, Detail: "llm agent searching"})
	r.Emit(Event{Type: Error, Detail: "search failed: timeout"})
	out := buf.String()
	if !strings.Contains(out, "llm agent searching") {
		t.Fatalf("progress line missing from the feed: %q", out)
	}
	if strings.Contains(out, "! llm agent searching") {
		t.Errorf("progress rendered as an error: %q", out)
	}
	if !strings.Contains(out, "! search failed: timeout") {
		t.Errorf("a real failure lost its glyph: %q", out)
	}
}

// ---- Options.Detach -------------------------------------------------------

// Detach was set only by tests: the CLI had no way to reach it. It is now
// --detach, and it must release the display at the start of the run.
func TestDetachOptionReleasesTheDisplay(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, NewByteReader(nil), 1)
	d.detached = true
	if _, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}})); err != nil {
		t.Fatal(err)
	}
	var detached bool
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Detach {
			detached = true
		}
	}
	if !detached {
		t.Error("a run started detached never reported the display released")
	}
}

// ---- review pass: fact-check verdicts, mixed retrieval, query shape --------

// The fact-checker may put a claim in the "verified" array with
// "verified": false. The summarizer was told "verified: <claim>" regardless,
// so a claim the checker rejected reached the report as checked.
func TestRejectedClaimIsNotPresentedAsVerified(t *testing.T) {
	fc := &agent.FactCheckResult{Verified: []agent.VerifiedClaim{
		{Claim: "the moon is cheese", Verified: false, Evidence: "no source says so"},
		{Claim: "water is wet", Verified: true, Evidence: "source 1"},
	}}
	got := summarizePrompt("q", &agent.Analysis{Answer: "a"}, fc, nil, true)
	if strings.Contains(got, "verified: the moon is cheese") && !strings.Contains(got, "unverified: the moon is cheese") {
		t.Errorf("a rejected claim was presented as verified:\n%s", got)
	}
	if !strings.Contains(got, "unverified: the moon is cheese (no source says so)") {
		t.Errorf("the rejected claim and its evidence are missing:\n%s", got)
	}
	if !strings.Contains(got, "  - verified: water is wet (source 1)") {
		t.Errorf("a confirmed claim lost its verdict:\n%s", got)
	}
}

// A search outage on one branch falls back per query, so one run can hold
// fetched findings and model-invented ones. One fetched finding flipped the
// whole run to "verified against sources", invented ones included.
func TestMixedRunMarksFindingsThatWereNeverFetched(t *testing.T) {
	mixed := []agent.Finding{
		{Title: "Real", URL: "https://real.example", Content: "fetched", Status: "ok"},
		{Title: "Invented", URL: "https://invented.example", Content: "recalled", Status: "unverified"},
	}
	for name, prompt := range map[string]string{
		"analyze":    analyzePrompt("q", mixed, nil),
		"fact-check": factCheckPrompt("claim", mixed, true, nil),
		"summarize":  summarizePrompt("q", &agent.Analysis{Answer: "a"}, nil, mixed, true),
	} {
		if !strings.Contains(prompt, "Invented (https://invented.example) [never fetched]") {
			t.Errorf("%s prompt does not mark the unfetched finding:\n%s", name, prompt)
		}
		if strings.Contains(prompt, "Real (https://real.example) [never fetched]") {
			t.Errorf("%s prompt marks a fetched finding as unfetched:\n%s", name, prompt)
		}
	}
	if got := factCheckDetail(mixed); !strings.Contains(got, "1 of 2 never fetched") {
		t.Errorf("phase line %q hides that part of the evidence was never fetched", got)
	}
	if got := factCheckDetail(mixed[:1]); got != "Verifying claims against sources" {
		t.Errorf("fully fetched run phase line = %q", got)
	}
	if got := factCheckDetail(mixed[1:]); !strings.Contains(got, "self-consistency") {
		t.Errorf("unfetched run phase line = %q", got)
	}
}

// The planner fallback names its only sub-topic after the question, and the
// query builder glued the two together: "What is X? What is X?".
func TestFallbackPlanDoesNotRepeatTheQuestion(t *testing.T) {
	got := subQueries("What is X?", agent.SubTopic{ID: "1", Name: "what is x?"})
	if len(got) != 1 || got[0] != "What is X?" {
		t.Errorf("subQueries = %q, want the question once", got)
	}
}

// A question wider than the query cap dropped every facet, so every branch
// issued the same query and all but the first found only duplicates.
func TestLongQuestionKeepsBranchesDistinct(t *testing.T) {
	q := strings.Repeat("solid state battery commercialization ", 4) // ~150 columns
	a := anchoredQuery(q, "Manufacturing Cost")
	b := anchoredQuery(q, "Regulatory Hurdles")
	if a == b {
		t.Fatalf("two facets produced the same query %q", a)
	}
	for _, got := range []string{a, b} {
		if dispWidth(got) > maxQueryLen {
			t.Errorf("query %q is %d columns, over the %d cap", got, dispWidth(got), maxQueryLen)
		}
		if !strings.HasPrefix(got, "solid state battery") {
			t.Errorf("query %q lost the subject", got)
		}
	}
	if !strings.HasSuffix(a, "Manufacturing Cost") {
		t.Errorf("query %q lost its facet", a)
	}
}

// ---- review pass: partial runs, detach ticker, export completeness --------

// failingPhase answers every phase normally except the one named.
type failingPhase struct {
	*fakeAssistant
	phase string
}

func (f failingPhase) Analyze(ctx context.Context, p string) (*agent.Analysis, error) {
	if f.phase == "analyze" {
		return nil, errors.New("provider 500")
	}
	return f.fakeAssistant.Analyze(ctx, p)
}

func (f failingPhase) Summarize(ctx context.Context, p string) (*agent.Summary, error) {
	if f.phase == "summarize" {
		return nil, errors.New("provider 500")
	}
	return f.fakeAssistant.Summarize(ctx, p)
}

// A failed analyze or summarize returned no result at all, so every search,
// scrape and token the run had already spent was thrown away: no artifact, no
// history record, one line on stderr.
func TestFailedLatePhaseStillDeliversTheRun(t *testing.T) {
	for _, phase := range []string{"analyze", "summarize"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			var stdout, stderr bytes.Buffer
			res, err := Run(context.Background(), Options{
				Question:  "q",
				Assistant: failingPhase{&fakeAssistant{}, phase},
				OutDir:    dir, Stdout: &stdout, Stderr: &stderr, Quiet: true,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Report == nil || len(res.Report.Findings) == 0 {
				t.Fatalf("the gathered findings were discarded: %+v", res.Report)
			}
			if !strings.Contains(res.Report.Error, "provider 500") {
				t.Errorf("result does not record why it is incomplete: %q", res.Report.Error)
			}
			if res.Report.Summary == nil || !strings.Contains(res.Report.Summary.Report, "could not be written") {
				t.Errorf("report does not say it is incomplete: %+v", res.Report.Summary)
			}
			if res.MDPath == "" || res.JSONPath == "" {
				t.Errorf("artifacts were not written: %+v", res)
			}
		})
	}
}

// Esc during a late phase is still a cancellation, not a partial report.
func TestCancelDuringLatePhaseIsNotAPartialRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := NewDriver(cancellingAnalyzer{&fakeAssistant{}, cancel}, &MultiSink{}, nil, 1)
	if _, err := d.Run(ctx, newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}})); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

type cancellingAnalyzer struct {
	*fakeAssistant
	cancel context.CancelFunc
}

func (c cancellingAnalyzer) Analyze(ctx context.Context, p string) (*agent.Analysis, error) {
	c.cancel()
	return nil, ctx.Err()
}

// Detaching stopped the animation ticker, and the very next event started a
// new one that woke 8×/s for the rest of the run to paint nothing.
func TestDetachedRendererDoesNotRestartTheTicker(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	r.plain = false
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Emit(Event{Type: Detach, Detail: "released"})
	r.Emit(Event{Type: Info, Detail: "still working"})
	r.mu.Lock()
	running := r.stop != nil
	r.mu.Unlock()
	r.Close()
	if running {
		t.Error("an event after detach restarted the animation ticker")
	}
}

// A tab counted zero columns, so a row holding one was not really clipped:
// it wrapped, the frame scrolled and the repaint left stale frames behind.
func TestTabsCannotOverflowTheFrame(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	r.plain, r.width, r.rows = false, 40, 30
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "go"})
	r.Emit(Event{Type: Info, Detail: "provider said:\t\t\t\t\t\terror\tdetail\there"})
	r.mu.Lock()
	lines := r.frameLines()
	r.mu.Unlock()
	r.Close()
	for _, l := range lines {
		if strings.Contains(l, "\t") {
			t.Errorf("a tab reached the terminal: %q", l)
		}
		if w := dispWidth(l); w > 40 {
			t.Errorf("row is %d columns on a 40-column terminal: %q", w, l)
		}
	}
}

// The analyzer's per-topic evidence and confidence, and the fact-check
// verdicts, were parsed and stored and never reached either artifact. Then
// the Markdown appended all of them after a body written from the same
// material, repeating every finding: it now states the verdict counts and
// lists only what the check did not confirm, and the sidecar keeps the rest.
func TestArtifactsCarryTopicsAndFactCheck(t *testing.T) {
	res := testResult()
	res.Analysis.Topics = []agent.Topic{{Name: "Cost", Findings: []string{"cells cost $80/kWh"}, Confidence: "medium"}}
	res.FactCheck = &agent.FactCheckResult{
		Verified:       []agent.VerifiedClaim{{Claim: "ok claim", Verified: true, Evidence: "src"}, {Claim: "bad claim", Verified: false, Evidence: "none"}},
		Unverified:     []string{"loose claim"},
		Contradictions: []agent.Contradiction{{Claim: "launch year", Sources: []string{"a", "b"}}},
	}
	md := MarkdownReport(res)
	for _, want := range []string{"Confirmed against their sources: 1 · not confirmed: 2 · contradictions: 1", "bad claim", "loose claim", "launch year"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	for _, repeated := range []string{"cells cost $80/kWh", "ok claim"} {
		if strings.Contains(md, repeated) {
			t.Errorf("markdown repeats %q after the body:\n%s", repeated, md)
		}
	}
	if strings.Contains(md, "- ✓ bad claim") {
		t.Error("a rejected claim is listed as verified")
	}
	m := BuildMeta(res, nil, "standard", 0, 0)
	if m.FactCheck == nil || len(m.Topics) != 1 {
		t.Errorf("sidecar lacks fact-check or topics: %+v %+v", m.FactCheck, m.Topics)
	}
	if !strings.Contains(summarizePrompt("q", res.Analysis, nil, nil, true), "cells cost $80/kWh") {
		t.Error("the summarizer never sees the per-topic evidence")
	}
}

// Citation events carry the source only in SourceTitle/SourceURL, which the
// timeline export dropped: every exported citation named no source.
func TestExportedCitationsNameTheirSource(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, nil, 1)
	res, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}}))
	if err != nil {
		t.Fatal(err)
	}
	m := BuildMeta(res, sink.TimelineSnapshot(), "quick", 0, 0)
	var n int
	for _, e := range m.Timeline {
		if e.Type == "citation" {
			n++
			if e.URL == "" || e.Detail == "" {
				t.Errorf("exported citation names no source: %+v", e)
			}
		}
	}
	if n == 0 {
		t.Fatal("no citation in the timeline")
	}
}

// A URL-less finding was counted as a source but left out of the citation
// list, so "sources 5" came with a list of two.
func TestURLlessFindingsAreListed(t *testing.T) {
	res := testResult()
	res.Findings = append(res.Findings, agent.Finding{Title: "Model recollection", Content: "c", Status: "unverified"})
	if got := len(citations(res, "")); got != len(res.Findings) {
		t.Errorf("%d citations for %d counted sources", got, len(res.Findings))
	}
	if md := MarkdownReport(res); !strings.Contains(md, "Model recollection") || !strings.Contains(md, "no URL") {
		t.Errorf("URL-less source missing from the report or not marked:\n%s", md)
	}
}

// A plan with no sub-topics researched nothing and still spent three model
// calls on an empty findings list.
func TestPlanWithNoSubTopicsIsAnError(t *testing.T) {
	_, err := NewPlanner(&fakeAssistant{planTopics: []agent.SubTopic{}}, depthMode("quick")).Plan(context.Background(), "  ")
	if err == nil {
		t.Error("an empty plan was accepted")
	}
}

// Every 5-second progress line was appended to the 64-entry activity tail, so
// a long summarize evicted every read/verify/citation line — the only part of
// the frame that says what the run found. Progress is status: one row,
// replaced in place, and still recorded in the timeline.
func TestTransientProgressDoesNotEvictTheActivityTail(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	r.plain, r.width, r.rows = false, 100, 40
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "go"})
	r.Emit(Event{Type: Verify, SubID: "1", Line: "✓ keep.example 200 OK"})
	r.Emit(Event{Type: Phase, Phase: "Summarize", Detail: "Writing the report"})
	for i := range 100 {
		r.Emit(Event{Type: Info, Transient: true, Detail: fmt.Sprintf("writing the report — %d words", i)})
	}
	r.mu.Lock()
	var tail []string
	for _, a := range r.acts {
		tail = append(tail, a.text)
	}
	frame := strings.Join(r.frameLines(), "\n")
	r.mu.Unlock()
	r.Close()
	joined := strings.Join(tail, "\n")
	if strings.Contains(joined, "words") {
		t.Errorf("progress lines were pushed into the activity tail:\n%s", joined)
	}
	if !strings.Contains(joined, "keep.example") {
		t.Errorf("the source line was evicted:\n%s", joined)
	}
	if !strings.Contains(frame, "writing the report — 99 words") || strings.Contains(frame, "98 words") {
		t.Errorf("the frame does not show exactly the latest status:\n%s", frame)
	}
}

// failingSearch refuses every query the way a rate-limited instance does.
type failingSearch struct{ *fakeAssistant }

func (failingSearch) ResearchDetail(ctx context.Context, q string, _ []string) (*agent.ResearchDetail, error) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := tools.NewSearXNGClient(srv.URL, 0).Search(ctx, q)
	return nil, err
}

// When every search was refused the run analysed nothing, spent three model
// calls and wrote a report about nothing. It now fails, naming why, and each
// refusal carries its status on the event.
func TestRunFailsWhenEverySearchFailed(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(failingSearch{&fakeAssistant{}}, sink, nil, 2)
	_, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}))
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("err = %v, want a failure naming the rate limit", err)
	}
	var tagged bool
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Error && e.Status == "rate-limited" {
			tagged = true
		}
		if e.Type == Phase && e.Phase == "Analyze" {
			t.Error("analysis ran on no evidence")
		}
	}
	if !tagged {
		t.Error("the refused search carried no status on its event")
	}
}

// modelled reports which model and provider it used.
type modelled struct{ *fakeAssistant }

func (modelled) ModelInfo() (string, string) { return "openrouter/free", "openrouter.ai" }
func (modelled) ServedModels() []string      { return []string{"google/gemma:free"} }

// Nothing recorded which model wrote a report; with a free router that picks
// a model per request, two runs of one question were incomparable with no way
// to tell why.
func TestArtifactsRecordTheModel(t *testing.T) {
	sink := &MultiSink{}
	res, err := NewDriver(modelled{&fakeAssistant{}}, sink, nil, 1).Run(context.Background(),
		newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "openrouter/free" || res.Provider != "openrouter.ai" {
		t.Errorf("result model=%q provider=%q", res.Model, res.Provider)
	}
	if m := BuildMeta(res, nil, "quick", 0, 0); m.Model != "openrouter/free" || m.Provider != "openrouter.ai" {
		t.Errorf("sidecar model=%q provider=%q", m.Model, m.Provider)
	}
	if m := BuildMeta(res, nil, "quick", 0, 0); len(m.ServedBy) != 1 {
		t.Errorf("sidecar served_by = %q", m.ServedBy)
	}
	if md := MarkdownReport(res); !strings.Contains(md, "openrouter/free") || !strings.Contains(md, "served by google/gemma:free") {
		t.Errorf("report does not name its model:\n%s", md)
	}
}

// ignoringInput is scripted input that reports keys discarded before the brief.
type ignoringInput struct{ Input }

func (ignoringInput) Ignored() int { return 1 }

// An Enter pressed while planning was dropped, and the brief then waited with
// nothing to say why; it reads as a hang. The brief now says the key was
// ignored and what to press.
func TestBriefSaysKeysTypedDuringPlanningWereIgnored(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{})
	o := Options{KeyScript: "\r"}.withDefaults()
	plan := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}})
	if _, cancelled := o.confirmBrief(ignoringInput{NewByteReader([]byte("\r"))}, r, plan, true); cancelled {
		t.Fatal("brief cancelled")
	}
	if !strings.Contains(buf.String(), "typed while planning") {
		t.Errorf("brief does not mention the ignored key:\n%s", buf.String())
	}
}

// ---- --jsonl progress -----------------------------------------------------

// Under --jsonl the assistant's progress went nowhere: the CLI silenced its
// stderr logger and ui.Run routed it into events only for the live UI. The
// plan call, every retry and a stalled stream left the stream silent, so a
// caller watching it could not tell a slow run from a hung one.
func TestJSONLCarriesPlanningProgress(t *testing.T) {
	fa := &progressDuringPlan{fakeAssistant: &fakeAssistant{}, msg: "asking test-model for sub-topics"}
	var stdout bytes.Buffer
	if _, err := Run(context.Background(), Options{Question: "q", Assistant: fa, DepthMode: "quick",
		JSONL: true, OutDir: t.TempDir(), Stdout: &stdout, Stderr: io.Discard}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, ok := findEvent(t, stdout.String(), func(e Event) bool { return e.Type == Info && e.Detail == fa.msg })
	if !ok {
		t.Fatalf("planning progress missing from the stream:\n%s", stdout.String())
	}
	if ev.Phase != "Plan" {
		t.Errorf("planning progress filed under %q, want Plan", ev.Phase)
	}
}

// statusDuringSummarize reports streamed-phase status from inside Summarize,
// the way the real assistant does while the report streams in.
type statusDuringSummarize struct {
	*fakeAssistant
	status func(string)
}

func (s *statusDuringSummarize) SetStatus(f func(string)) { s.status = f }

func (s *statusDuringSummarize) Summarize(ctx context.Context, prompt string) (*agent.Summary, error) {
	if s.status != nil {
		s.status("writing the report — 120 words")
	}
	return s.fakeAssistant.Summarize(ctx, prompt)
}

func TestJSONLCarriesStreamStatus(t *testing.T) {
	fa := &statusDuringSummarize{fakeAssistant: &fakeAssistant{}}
	var stdout bytes.Buffer
	if _, err := Run(context.Background(), Options{Question: "q", Assistant: fa, DepthMode: "quick",
		JSONL: true, OutDir: t.TempDir(), Stdout: &stdout, Stderr: io.Discard}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, ok := findEvent(t, stdout.String(), func(e Event) bool { return e.Type == Info && e.Transient })
	if !ok {
		t.Fatalf("stream status missing from the stream:\n%s", stdout.String())
	}
	if ev.Phase != "Summarize" || !strings.Contains(ev.Detail, "120 words") {
		t.Errorf("status event = %+v, want the Summarize status line", ev)
	}
}

// findEvent decodes a JSONL stream and returns the first event match accepts.
func findEvent(t *testing.T, stream string, match func(Event) bool) (Event, bool) {
	t.Helper()
	for _, ln := range strings.Split(strings.TrimSpace(stream), "\n") {
		var ev Event
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("stream line is not an event: %q: %v", ln, err)
		}
		if match(ev) {
			return ev, true
		}
	}
	return Event{}, false
}

// followUpAnalyzer names open follow-up queries, or fails to, and records
// every prompt the analysis was sent.
type followUpAnalyzer struct {
	*fakeAssistant
	followUp    []agent.FollowUpQuery
	failFollow  bool
	prompts     []string
	followCalls int
}

func (f *followUpAnalyzer) FollowUps(context.Context, string) ([]agent.FollowUpQuery, error) {
	f.followCalls++
	if f.failFollow {
		return nil, errors.New("provider unavailable")
	}
	return f.followUp, nil
}

func (f *followUpAnalyzer) Analyze(ctx context.Context, p string) (*agent.Analysis, error) {
	f.prompts = append(f.prompts, p)
	return &agent.Analysis{Answer: "analysed", Confidence: "high"}, nil
}

func queries(qs ...string) []agent.FollowUpQuery {
	out := make([]agent.FollowUpQuery, len(qs))
	for i, q := range qs {
		out[i] = agent.FollowUpQuery{Query: q}
	}
	return out
}

// The analysis named the search that would settle its own gap, and the
// pipeline printed it under the report as a "suggested search" instead of
// running it; the report then answered "not established" where one search
// would have answered.
func TestFollowUpQueriesAreSearchedBeforeTheReport(t *testing.T) {
	fq := "widgetdb serverless engine support"
	fa := &followUpAnalyzer{fakeAssistant: &fakeAssistant{}, followUp: queries(fq)}
	res, err := NewDriver(fa, &MultiSink{}, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	// One analysis, after the follow-up: a first analysis only to name the
	// searches, then replaced by a second, put two full analyses on the
	// critical path of nine runs in ten.
	if len(fa.prompts) != 1 {
		t.Fatalf("analyze ran %d times, want once, after the follow-up", len(fa.prompts))
	}
	if !strings.Contains(fa.prompts[0], "Finding for "+fq) {
		t.Errorf("the analysis never saw the follow-up evidence:\n%s", fa.prompts[0])
	}
	if res.Analysis.Answer != "analysed" {
		t.Errorf("report built on analysis %q", res.Analysis.Answer)
	}
}

// A follow-up that finds a page the first round already cited adds no source,
// but its query must reach the page: the excerpt the model sees is chosen by
// the queries that found the page, and dropping the duplicate kept the
// passage tuned to the first question only.
func TestFollowUpOnAKnownPageSteersItsExcerptWithoutCountingIt(t *testing.T) {
	topics := []agent.SubTopic{{ID: "1", Name: "Alpha"}}
	plan := newTestPlan("quick", topics)
	q1, fq := subQueries(plan.Question, topics[0])[0], "widgetdb serverless engine support"
	shared := "https://docs.example/widgetdb"
	fa := &followUpAnalyzer{followUp: queries(fq), fakeAssistant: &fakeAssistant{
		planTopics: topics,
		findings: map[string][]agent.Finding{
			q1: {{Query: q1, Title: "WidgetDB docs", URL: shared, Content: "text"}},
			fq: {{Query: fq, Title: "WidgetDB docs", URL: shared, Content: "text"}},
			// Finding only a known page leaves the follow-up empty-handed, so
			// it tries its fallback query too; that one finds nothing here.
			anchoredQuery(plan.Question, fq): {},
		},
		signals: map[string][]agent.SourceSignal{
			q1: {{URL: shared, Domain: "docs.example", Status: "ok"}},
			fq: {{URL: shared, Domain: "docs.example", Status: "ok"}},
		},
	}}
	sink := &MultiSink{}
	res, err := NewDriver(fa, sink, nil, 1).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("one page became %d findings", len(res.Findings))
	}
	if !slices.Contains(res.Findings[0].AlsoFoundBy, fq) {
		t.Errorf("follow-up query not carried onto the page: %+v", res.Findings[0])
	}
	if last := sink.Timeline[len(sink.Timeline)-1]; last.Sources != 1 {
		t.Errorf("sources = %d, want 1: a page found twice is one source", last.Sources)
	}
}

// Follow-up rows were named after their search query, up to 80 characters,
// and cut off at the 40-column name column beside the same query in full.
func TestFollowUpRowsTakeTheirGivenName(t *testing.T) {
	named, unnamed := "Intel Arc Pro B70 vs AMD Radeon AI PRO R9700 benchmark LLM inference", "widgetdb limits"
	fa := &followUpAnalyzer{fakeAssistant: &fakeAssistant{},
		followUp: []agent.FollowUpQuery{{Name: "LLM inference benchmarks", Query: named}, {Query: unnamed}}}
	sink := &MultiSink{}
	plan := newTestPlan("quick", nil)
	if _, err := NewDriver(fa, sink, nil, 1).Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	var searched []string
	for _, e := range sink.Timeline {
		if e.Type == SubAgent && strings.HasPrefix(e.SubID, "f") {
			rows[e.SubID] = e.SubName
		}
		if e.Type == Search {
			searched = append(searched, e.Query)
		}
	}
	if rows["f1"] != "LLM inference benchmarks" || rows["f2"] != unnamed {
		t.Errorf("follow-up rows = %v, want the given name, or the query when it has none", rows)
	}
	// The follow-up's own query only: the question with the name glued on
	// asked nothing the query had not, clipped mid-phrase.
	if !slices.Contains(searched, named) || slices.Contains(searched, anchoredQuery(plan.Question, "LLM inference benchmarks")) {
		t.Errorf("searched %q, want the query itself and no question-plus-name fallback", searched)
	}
}

// The follow-up round is a refinement: when choosing its searches fails, the
// report is analysed from what the first round found, instead of the run
// failing after everything was spent.
func TestFailedFollowUpCallStillAnalyses(t *testing.T) {
	fa := &followUpAnalyzer{fakeAssistant: &fakeAssistant{}, failFollow: true}
	sink := &MultiSink{}
	res, err := NewDriver(fa, sink, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Analysis == nil || res.Analysis.Answer != "analysed" || len(fa.prompts) != 1 {
		t.Errorf("analysis = %+v after %d analyses, want one, on the first round's findings", res.Analysis, len(fa.prompts))
	}
	if !slices.ContainsFunc(sink.TimelineSnapshot(), func(e Event) bool {
		return e.Type == Error && strings.Contains(e.Detail, "follow-up searches not chosen")
	}) {
		t.Error("the failed follow-up call was not reported")
	}
}

// Search refusing queries left a follow-up round with nothing to ask; the
// call choosing its searches is not spent either.
func TestRefusedSearchSkipsTheFollowUpCall(t *testing.T) {
	fa := &followUpAnalyzer{fakeAssistant: &fakeAssistant{}, followUp: queries("widgetdb limits")}
	d := NewDriver(fa, &MultiSink{}, nil, 1)
	d.searchDown = true
	findings, _ := d.followUp(context.Background(), newTestPlan("quick", nil), nil, nil)
	if fa.followCalls != 0 || len(findings) != 0 {
		t.Errorf("follow-up call made %d times with search down, want none", fa.followCalls)
	}
}

// The analyzer flagged "not found" as a gap, but the summarizer never saw the
// gaps and wrote a conclusion of "no" over them.
func TestSummarizerSeesTheAnalysisGaps(t *testing.T) {
	a := &agent.Analysis{Answer: "a", Gaps: []string{"whether WidgetDB runs serverless"}}
	if p := summarizePrompt("q", a, nil, nil, true); !strings.Contains(p, "whether WidgetDB runs serverless") {
		t.Errorf("summarize prompt lacks the analysis gaps:\n%s", p)
	}
}

// repairingAssistant scripts a run whose first check blocks a recommendation
// for not following from its claim, and whose repair pass narrows it.
type repairingAssistant struct {
	*fakeAssistant
	mu     sync.Mutex
	checks int
}

func (r *repairingAssistant) Analyze(_ context.Context, p string) (*agent.Analysis, error) {
	if strings.Contains(p, "were not established") {
		return &agent.Analysis{Recommendations: []agent.Recommendation{
			{Revises: "r1", Choose: "Dual-fuel", When: "the load is high and the climate is severe", Claims: []string{"c1"}},
		}}, nil
	}
	return &agent.Analysis{Answer: "a", Confidence: "medium",
		Claims: []agent.Claim{{ID: "c1", Text: "Dual-fuel suits high loads in severe climates", Sources: []string{"https://h.example/guide"}}},
		Recommendations: []agent.Recommendation{
			{Choose: "Dual-fuel", When: "the load is high or the climate is severe", Claims: []string{"c1"}},
		}}, nil
}

// FactCheck answers what each call asks: the claims, the first inference, or
// the revision's. The check's calls run at once, in no fixed order.
func (r *repairingAssistant) FactCheck(_ context.Context, p string) (*agent.FactCheckResult, error) {
	r.mu.Lock()
	r.checks++
	r.mu.Unlock()
	switch {
	case !isInferenceCheck(p):
		return &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c1", Status: "supported", Evidence: []agent.Evidence{
			{Source: "https://h.example/guide", Quote: "Dual-fuel suits high loads in severe climates"}}}}}, nil
	case strings.Contains(p, "  - r2:"):
		return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "r2", Follows: true}}}, nil
	}
	return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "r1", Follows: false, Reason: "the claim says and, not or"}}}, nil
}

func isInferenceCheck(prompt string) bool { return strings.HasPrefix(prompt, "Judge only whether") }

// A heating question lost its dual-fuel option because the recommendation
// said "or" where its claim said "and": the check was right to block it,
// and the answer had nothing in its place although the claim supported a
// narrower recommendation. The repair pass writes and checks that one.
func TestRepairPassRestoresAnOptionTheClaimsSupport(t *testing.T) {
	topics := []agent.SubTopic{{ID: "1", Name: "Heating"}}
	plan := newTestPlan("quick", topics)
	q := subQueries(plan.Question, topics[0])[0]
	fa := &repairingAssistant{fakeAssistant: &fakeAssistant{
		planTopics: topics,
		findings: map[string][]agent.Finding{q: {{Query: q, Title: "Guide", URL: "https://h.example/guide",
			Content: "Dual-fuel suits high loads in severe climates, the guide says."}}},
		signals: map[string][]agent.SourceSignal{q: {{URL: "https://h.example/guide", Domain: "h.example", Status: "ok"}}},
	}}
	res, err := NewDriver(fa, &MultiSink{}, nil, 1).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if fa.checks != 3 {
		t.Errorf("fact-check ran %d times, want 3 (claims and inference at once, then the revision)", fa.checks)
	}
	if r := res.Summary.Report; !strings.Contains(r, "- **Dual-fuel**: the load is high and the climate is severe") ||
		strings.Contains(r, "Not established") {
		t.Errorf("report does not answer with the checked revision:\n%s", r)
	}
}

// scriptedAnalyzer answers Analyze from a list, in order.
type scriptedAnalyzer struct {
	*fakeAssistant
	answers []*agent.Analysis
	calls   int
}

func (s *scriptedAnalyzer) Analyze(context.Context, string) (*agent.Analysis, error) {
	a := s.answers[min(s.calls, len(s.answers)-1)]
	s.calls++
	return a, nil
}

var (
	placeholder = &agent.Analysis{Answer: `{"/": "placeholder"}`}
	structured  = &agent.Analysis{Answer: "a", Claims: []agent.Claim{{ID: "c1", Text: "t"}},
		Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1"}}}}
)

// A history question's analysis came back as {"/": "placeholder"} and the
// report fell back to prose no check had seen. A malformed analysis is
// asked for once more.
func TestMalformedAnalysisIsAskedAgain(t *testing.T) {
	sa := &scriptedAnalyzer{fakeAssistant: &fakeAssistant{}, answers: []*agent.Analysis{placeholder, structured}}
	res, err := NewDriver(sa, &MultiSink{}, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if sa.calls != 2 || len(res.Analysis.Claims) != 1 {
		t.Errorf("analyze ran %d times and kept %d claims, want 2 and the second answer", sa.calls, len(res.Analysis.Claims))
	}
}

// Every sub-topic of "intel arc pro b70 vs amd radeon ai pro r9700" was named
// "Intel Arc Pro B70 vs AMD Radeon AI Pro R9700 …": the rows read the same,
// cut off before the part that differs, and the anchored search sent the
// subject twice and lost the facet to the length limit.
func TestSharedSubjectIsTrimmedFromSubTopicNames(t *testing.T) {
	const question = "intel arc pro b70 vs amd radeon ai pro r9700"
	for _, tc := range []struct{ names, want []string }{
		{
			[]string{"Intel Arc Pro B70 vs AMD Radeon AI Pro R9700: gaming benchmarks", "Intel Arc Pro B70 vs AMD Radeon AI Pro R9700 — Pricing", "Intel Arc Pro B70 vs AMD Radeon AI Pro R9700 AI inference"},
			[]string{"Gaming benchmarks", "Pricing", "AI inference"},
		},
		// Each card named on its own: nothing is shared, nothing is dropped.
		{[]string{"Intel Arc Pro B70 architecture", "AMD Radeon AI Pro R9700 architecture"}, []string{"Intel Arc Pro B70 architecture", "AMD Radeon AI Pro R9700 architecture"}},
		// Shared, but not the question's words: it says something.
		{[]string{"Pricing in Europe", "Pricing in the US"}, []string{"Pricing in Europe", "Pricing in the US"}},
		// A name that is only the subject keeps a word of its own.
		{[]string{"Intel Arc Pro B70", "Intel Arc Pro B70 drivers"}, []string{"B70", "B70 drivers"}},
	} {
		topics := make([]SubTopic, len(tc.names))
		for i, n := range tc.names {
			topics[i] = SubTopic{Name: n}
		}
		trimSharedSubject(question, topics)
		for i, want := range tc.want {
			if topics[i].Name != want {
				t.Errorf("%q -> %q, want %q", tc.names[i], topics[i].Name, want)
			}
		}
	}
	if q := anchoredQuery(question, "Gaming benchmarks"); q != question+" Gaming benchmarks" {
		t.Errorf("anchored query = %q, want the question and the facet once each", q)
	}
}

// skippingSearch answers every query with the pages of its round, and asks
// the run's skip check first, as the search tool does.
type skippingSearch struct {
	*fakeAssistant
	skip    func(string) bool
	pages   []agent.Finding
	signals []agent.SourceSignal
	offTop  []agent.SkippedSource
	fetched []string
}

func (s *skippingSearch) SkipSources(skip func(string) bool) { s.skip = skip }

func (s *skippingSearch) ResearchDetail(_ context.Context, query string, _ []string) (*agent.ResearchDetail, error) {
	det := &agent.ResearchDetail{Skipped: s.offTop}
	for i, f := range s.pages {
		if s.skip != nil && s.skip(f.URL) {
			det.Known = append(det.Known, agent.SkippedSource{URL: f.URL, Domain: s.signals[i].Domain})
			continue
		}
		s.fetched = append(s.fetched, f.URL)
		f.Query = query
		det.Findings = append(det.Findings, f)
		det.Signals = append(det.Signals, s.signals[i])
	}
	return det, nil
}

// A page already cited was downloaded again by every later search that found
// it and took one of that search's slots. Only a page the run has in full is
// skipped: one another sub-agent judged off-topic, or cited from its snippet
// alone, is still fetched by a search it suits.
func TestLaterSearchSkipsOnlyPagesTheRunHasInFull(t *testing.T) {
	a := agent.Finding{Title: "A", Content: "full text", URL: "https://a.example/1"}
	b := agent.Finding{Title: "B", Content: "snippet", URL: "https://b.example/2"}
	c := agent.Finding{Title: "C", Content: "full text", URL: "https://c.example/3"}
	search := &skippingSearch{fakeAssistant: &fakeAssistant{},
		pages: []agent.Finding{a, b}, offTop: []agent.SkippedSource{{URL: c.URL, Domain: "c.example"}},
		signals: []agent.SourceSignal{{URL: a.URL, Domain: "a.example", Status: "ok"}, {URL: b.URL, Domain: "b.example", Status: "degraded"}}}
	sink := &MultiSink{}
	d := NewDriver(search, sink, nil, 1)
	round := func(name string) []agent.Finding {
		plan, _ := newTestPlan("quick", []agent.SubTopic{{ID: name, Name: name, Query: name}}).WithSourcesPerTopic(3)
		found, _ := d.research(context.Background(), plan)
		return found
	}
	finance := round("finance")

	fullB := b
	fullB.Content = "full text"
	search.pages, search.offTop, search.fetched = []agent.Finding{a, fullB, c}, nil, nil
	search.signals = []agent.SourceSignal{{URL: a.URL, Domain: "a.example", Status: "ok"},
		{URL: b.URL, Domain: "b.example", Status: "ok"}, {URL: c.URL, Domain: "c.example", Status: "ok"}}
	gaming := round("gaming")

	if !slices.Contains(search.fetched, b.URL) || !slices.Contains(search.fetched, c.URL) || slices.Contains(search.fetched, a.URL) {
		t.Errorf("second round fetched %v, want the snippet-only and the off-topic page, not the one read in full", search.fetched)
	}
	if !slices.ContainsFunc(gaming, func(f agent.Finding) bool { return f.URL == c.URL }) {
		t.Errorf("the page finance judged off-topic was not cited for gaming: %+v", gaming)
	}
	if !slices.ContainsFunc(sink.Timeline, func(e Event) bool { return e.URL == a.URL && e.Status == "duplicate" && e.SubID == "gaming" }) {
		t.Error("the skipped page was not reported as already cited")
	}
	all := d.withFoundAgain(mergeFindings(append(finance, gaming...)))
	for _, f := range all {
		if f.URL == a.URL && !slices.Contains(f.AlsoFoundBy, "gaming") {
			t.Errorf("page A lost the query that found it again: %+v", f)
		}
		if f.URL == b.URL && (f.Content != "full text" || f.Status != "ok") {
			t.Errorf("page B = %+v, want the full text the second fetch got", f)
		}
	}
}

func TestRepairDoesNotAcceptTwoReplacementsForOneBlockedStatement(t *testing.T) {
	a := &agent.Analysis{
		Claims:          []agent.Claim{{ID: "c1", Status: statusSupported}},
		Recommendations: []agent.Recommendation{{Choose: "original", Blocked: "unsupported"}},
		Conclusions:     []agent.Recommendation{{Statement: "original conclusion", Blocked: "unsupported"}},
	}
	rev := &agent.Analysis{
		Recommendations: []agent.Recommendation{
			{Revises: "r1", Choose: "invalid", Claims: []string{"missing"}},
			{Revises: "r1", Choose: "first", Claims: []string{"c1"}},
			{Revises: "r1", Choose: "second", Claims: []string{"c1"}},
		},
		Conclusions: []agent.Recommendation{
			{Revises: "k1", Statement: "first conclusion", Claims: []string{"c1"}},
			{Revises: "k1", Statement: "second conclusion", Claims: []string{"c1"}},
		},
	}
	added := acceptRevisions(a, rev)
	if !slices.Equal(added, []string{"r2", "k2"}) {
		t.Fatalf("accepted IDs = %v", added)
	}
	if len(a.Recommendations) != 2 || a.Recommendations[1].Choose != "first" {
		t.Fatalf("recommendations = %+v", a.Recommendations)
	}
	if len(a.Conclusions) != 2 || a.Conclusions[1].Statement != "first conclusion" {
		t.Fatalf("conclusions = %+v", a.Conclusions)
	}
}

func TestPDFExportDoesNotSilentlyDropNonLatinText(t *testing.T) {
	for _, text := range []string{"Привет мир", "日本語", "Ελληνικά", "עברית", "العربية"} {
		var warnings bytes.Buffer
		res := testResult()
		res.Summary.Report = text
		o := Options{OutDir: t.TempDir(), Stderr: &warnings}
		_, pdf, _ := o.writeArtifacts(res, &MultiSink{}, &Driver{}, newTestPlan("quick", nil))
		if pdf == "" {
			t.Fatalf("PDF was not written: %s", warnings.String())
		}
		if !strings.Contains(warnings.String(), "PDF export replaced unsupported characters") {
			t.Fatalf("%q silently lost text: %s", text, warnings.String())
		}
	}
}

// A run warned that its PDF lost text over one emoji in a source's title:
// decoration, not text the reader needs the Markdown for. Only emoji are
// dropped, from parsed text: the Markdown's syntax and link targets stay as
// written, and a symbol that means something still warns.
func TestPDFDropsEmojiWithoutWarning(t *testing.T) {
	report := "$580.0M (2025) → $1,813.0M (2034); value ≠ ranking; " +
		"[Top 20 Raspberry Pi Projects of 2025🤯!](https://example.com/🤯) 👍🏽 ⭐️ ✅ 🇦🇷"
	if pdfDropsText(report) {
		t.Errorf("warned for emoji: %+v", parsePDFBlocks(report))
	}
	pdf := string(BuildPDF(report))
	for _, want := range []string{"-> $1,813.0M", "value != ranking", "of 2025!", "example.com/%F0%9F%A4%AF"} {
		if !strings.Contains(pdf, want) {
			t.Errorf("PDF lacks %q", want)
		}
	}
	for _, text := range []string{"Привет мир", "C♯ versus C♭", "★ rating"} {
		if !pdfDropsText(text) {
			t.Errorf("%q stopped warning", text)
		}
	}
	if b := parsePDFBlocks("− 5 percent"); len(b) != 1 || b[0].kind != blockPara {
		t.Errorf("a minus sign became list syntax: %+v", b)
	}
}

func TestPDFExportDoesNotWarnForSupportedLatinText(t *testing.T) {
	var warnings bytes.Buffer
	res := testResult()
	res.Summary.Report = "café — €100… “quoted”"
	o := Options{OutDir: t.TempDir(), Stderr: &warnings}
	_, pdf, _ := o.writeArtifacts(res, &MultiSink{}, &Driver{}, newTestPlan("quick", nil))
	if pdf == "" || warnings.Len() != 0 {
		t.Fatalf("supported PDF text: path=%q warnings=%s", pdf, warnings.String())
	}
}

func TestClipWordsDoesNotSplitWordsAfterLeadingWhitespace(t *testing.T) {
	for _, tc := range []struct {
		input string
		width int
		want  string
	}{
		{"  ab cd ef", 6, "ab cd"},
		{"  ab cd ef", 4, "ab"},
		{"\t  ab cd ef  \n", 20, "ab cd ef"},
		{"  café monde encore", 8, "café"},
	} {
		if got := clipWords(tc.input, tc.width); got != tc.want {
			t.Errorf("clipWords(%q, %d) = %q, want %q", tc.input, tc.width, got, tc.want)
		}
	}
}

// slowSearch delays one query's search, so its sub-agent finishes last.
type slowSearch struct {
	*fakeAssistant
	slow string
}

func (s *slowSearch) ResearchDetail(ctx context.Context, query string, terms []string) (*agent.ResearchDetail, error) {
	if query == s.slow {
		time.Sleep(30 * time.Millisecond)
	}
	return s.fakeAssistant.ResearchDetail(ctx, query, terms)
}

// Parallel sub-agents appended their findings as each finished, so the same
// evidence reached the analysis prompt in a different order from run to run,
// renumbering every source.
func TestFindingsFollowPlanOrder(t *testing.T) {
	topics := []agent.SubTopic{{ID: "1", Name: "Alpha"}, {ID: "2", Name: "Beta"}}
	plan := newTestPlan("quick", topics)
	first := subQueries(plan.Question, topics[0])[0]
	fa := &slowSearch{fakeAssistant: &fakeAssistant{planTopics: topics}, slow: first}
	findings, _ := NewDriver(fa, &MultiSink{}, nil, 2).research(context.Background(), plan)
	if len(findings) == 0 || findings[0].Query != first {
		t.Errorf("findings open with %+v, want the first sub-topic's %q", findings, first)
	}
}

// The renderer was a sink only for an interactive run, which needs a terminal
// stdout, so its pipe log could never run: a piped run printed the completion
// card and none of the per-source activity the docs promise.
func TestPipedRunLogsEachEvent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if _, err := Run(context.Background(), Options{
		Question:  "q",
		Assistant: &fakeAssistant{},
		DepthMode: "quick",
		OutDir:    t.TempDir(),
		Stdout:    &stdout,
		Stderr:    &stderr,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"Searching: ", "read example.com", "RESEARCH COMPLETE"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("piped output lacks %q:\n%s", want, stdout.String())
		}
	}
}

// locate normalised the whole page again for every quote it checked, and
// twice more for the hyphen-joined variants: a 40 KB scraped page cost
// 80 ms a quote, some six seconds of CPU on a wide fact-check, all of it
// recomputing a pure function of the page. A pass over the claims
// normalises each page once, so forty quotes cost about what one does.
func TestLocatingQuotesNormalisesEachPageOnce(t *testing.T) {
	var page strings.Builder
	for i := range 500 {
		fmt.Fprintf(&page, "**Node %d** reports [replication lag](https://example.com/%d) of _%d ms_ under load.\n\n", i, i, i*3)
	}
	findings := []agent.Finding{{URL: "https://example.com/page", Content: page.String(), Status: "ok"}}
	pages := fetchedPages(findings)
	check := func(n int) uint64 {
		return allocated(func() {
			for i := range n {
				ev := []agent.Evidence{{Source: "https://example.com/page", Quote: fmt.Sprintf("a passage the page never had, number %d", i)}}
				if quotesLocated(ev, findings, pages) {
					t.Fatal("located a quote the page does not hold")
				}
			}
		})
	}
	one := check(1)
	pages = fetchedPages(findings)
	if forty := check(40); forty > 3*one {
		t.Errorf("40 quotes over one page allocated %d bytes, %.1f times one quote's %d", forty, float64(forty)/float64(one), one)
	}
}

// allocated is how many bytes f allocates on the heap.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A run that gave the terminal back, with --detach or by pressing b, must say
// so: the CLI offers questions about the report on the terminal that watched
// it, and reading that terminal after a detach sent the lines typed at it
// since, meant for the shell, to the model as questions.
func TestRunReportsThatItDetached(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detach bool
		keys   string
		want   bool
	}{
		{"watched", false, "\r", false},
		{"--detach", true, "\r", true},
		{"pressed b", false, "\rb", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(context.Background(), Options{
				Question: "q", Assistant: &fakeAssistant{summary: "s"}, OutDir: t.TempDir(),
				Stdout: io.Discard, Stderr: io.Discard, KeyScript: tc.keys, Detach: tc.detach,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Detached != tc.want {
				t.Errorf("Detached = %v, want %v", res.Detached, tc.want)
			}
		})
	}
}

// Public SearXNG instances refusing the run left the reader with a wall of
// per-instance errors and no word on what to do. The advice now comes once
// per run, however many sub-agents hit the same wall, and again in the
// run's error when nothing answered at all.
func TestPublicSearchRefusalIsExplainedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Making sure you're not a bot!</title></html>`)
	}))
	defer srv.Close()
	c := tools.NewSearXNGClient(srv.URL+","+srv.URL+"/x", 0)
	c.Public = true
	_, searchErr := c.Search(context.Background(), "q")
	if !tools.PublicSearchRefused(searchErr) {
		t.Fatalf("not a public refusal: %v", searchErr)
	}

	sink := &MultiSink{}
	d := NewDriver(nil, sink, nil, 1)
	for range 3 {
		d.noteSearch(searchErr, nil)
	}
	hints := 0
	for _, e := range sink.TimelineSnapshot() {
		if strings.Contains(e.Detail, "docs/services.md") {
			hints++
		}
	}
	if hints != 1 {
		t.Errorf("the refusal advice was given %d times, want once", hints)
	}
	if got := d.searchFailure(nil).Error(); !strings.Contains(got, "drsh setup search") {
		t.Errorf("run error gives no way out: %q", got)
	}
}

// refusedPool answers its first answered queries and sends every later one
// to a pool of public instances that all challenge: the shape of a run
// whose public SearXNG instances turned on it early.
type refusedPool struct {
	*fakeAssistant
	pool     *tools.SearXNGClient
	answered int32
	queries  atomic.Int32
}

func newRefusedPool(t *testing.T, answered int32) *refusedPool {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Making sure you're not a bot!</title></html>`)
	}))
	t.Cleanup(srv.Close)
	pool := tools.NewSearXNGClient(srv.URL+","+srv.URL+"/x", 0)
	pool.Public = true
	return &refusedPool{fakeAssistant: &fakeAssistant{}, pool: pool, answered: answered}
}

func (r *refusedPool) ResearchDetail(ctx context.Context, q string, terms []string) (*agent.ResearchDetail, error) {
	if r.queries.Add(1) <= r.answered {
		return r.fakeAssistant.ResearchDetail(ctx, q, terms)
	}
	_, err := r.pool.Search(ctx, q)
	return nil, err
}

func (r *refusedPool) FollowUps(context.Context, string) ([]agent.FollowUpQuery, error) {
	return queries("what the first search missed"), nil
}

var refusedTopics = []agent.SubTopic{{ID: "1", Name: "A", Query: "a"}, {ID: "2", Name: "B", Query: "b"}, {ID: "3", Name: "C", Query: "c"}}

// Public instances refused a run after its first search. Every remaining
// query still went out to fail without asking anything, and then five model
// calls and a minute went on analysing, checking, revising and writing up
// the one off-topic snippet left. The run now stops searching once nothing
// would be asked, and stops before analysis, saying why and what to do.
func TestRunOnARefusingPoolStopsBeforeAnalysis(t *testing.T) {
	a := newRefusedPool(t, 1)
	sink := &MultiSink{}
	_, err := NewDriver(a, sink, nil, 1).Run(context.Background(), newTestPlan("quick", refusedTopics))
	if err == nil || !strings.Contains(err.Error(), "too little evidence") || !strings.Contains(err.Error(), "docs/services.md") {
		t.Fatalf("err = %v, want the run stopped for too little evidence, with the way out", err)
	}
	// Two queries a sub-agent: one answer and one refusal that sets the pool
	// resting, then one query per remaining sub-agent that finds it resting
	// and stops that sub-agent.
	if n := a.queries.Load(); n > 4 {
		t.Errorf("%d queries sent, want searching to stop once no instance would be asked", n)
	}
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Phase && e.Phase == "Analyze" {
			t.Error("analysis ran on one source from a refused search")
		}
	}
}

// With enough sources to analyse but most searches failed, the report used
// to read like any finished one. It is now delivered marked incomplete, and
// a follow-up round that would ask no instance is skipped.
func TestRunOnMostlyFailedSearchIsReportedIncomplete(t *testing.T) {
	a := newRefusedPool(t, 3)
	sink := &MultiSink{}
	d := NewDriver(a, sink, nil, 1)
	d.noteSearch(errors.New("refused"), nil)
	d.noteSearch(errors.New("refused"), nil)
	res, err := d.Run(context.Background(), newTestPlan("quick", refusedTopics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(res.Error, "incomplete research:") {
		t.Errorf("result error = %q, want the run marked incomplete", res.Error)
	}
	if !strings.HasPrefix(res.Summary.Report, "_Incomplete research:") || res.Summary.Confidence != "low" {
		t.Errorf("report does not say it is incomplete (confidence %q):\n%s", res.Summary.Confidence, res.Summary.Report)
	}
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Phase && strings.HasPrefix(e.Detail, "Following up") {
			t.Error("a follow-up round ran with no search instance answering")
		}
	}
}

// A search answered with nothing on the subject delivered as little as a
// refused one. Counted as an answer, two such searches beside one refusal
// read as a healthy run.
func TestSearchWithOnlyOffTopicResultsCountsAsGivingNothing(t *testing.T) {
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1)
	empty := &agent.ResearchDetail{Skipped: []agent.SkippedSource{{URL: "https://ruby-hotels.example", Domain: "ruby-hotels.example"}}}
	d.noteSearch(nil, empty)
	d.noteSearch(nil, empty)
	d.noteSearch(errors.New("refused"), nil)
	if why := d.thinSearch(); !strings.Contains(why, "3 of 3 searches gave nothing") || !strings.Contains(why, "2 found nothing on the subject") {
		t.Errorf("thinSearch = %q", why)
	}
}

// A run where most searches answered is not marked: the mark is for a
// report the search failed to deliver evidence for, not for one refusal.
func TestRunWithAFewFailedSearchesIsComplete(t *testing.T) {
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1)
	d.noteSearch(errors.New("one refusal"), nil)
	res, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}}))
	if err != nil || res.Error != "" {
		t.Errorf("err=%v result error=%q, want a complete run", err, res.Error)
	}
}

// The fallback query was the question clipped with the facet glued on and
// cut mid-phrase: "Is Ruby on Rails still a good framework pick as of 2026
// with all the AI Ruby on Rails 2026 Health and". With the planner's terms
// it is those keywords, which name the subject by instruction.
func TestFallbackQueryIsThePlannersTerms(t *testing.T) {
	question := "Is Ruby on Rails still a good framework pick as of 2026 with all the AI coding tools around now?"
	sub := agent.SubTopic{Name: "Ruby on Rails 2026 Health and Maintenance", Query: "Rails 8 adoption 2026",
		Terms: []string{"Ruby on Rails", "Rails 8", "maintenance"}}
	got := subQueries(question, sub)
	if len(got) != 2 || got[1] != "Ruby on Rails Rails 8 maintenance" {
		t.Errorf("queries = %q, want the planner's query then its terms", got)
	}
	sub.Terms = nil
	if got := subQueries(question, sub); len(got) != 2 || !strings.HasPrefix(got[1], "Is Ruby on Rails") {
		t.Errorf("without terms the fallback should stay the anchored question, got %q", got)
	}
}

// pdfText is one string the PDF draws: the font slot it uses (1 Helvetica,
// 2 Bold, 3 Oblique, 4 BoldOblique, 5 Courier), its size, where its baseline
// starts, and the page it is on.
type pdfText struct {
	page       int
	font       int
	size, x, y float64
	text       string
}

var pdfTextOp = regexp.MustCompile(`/F(\d) ([\d.]+) Tf [\d. ]+ rg 1 0 0 1 ([\d.]+) ([\d.]+) Tm \(((?:[^()\\]|\\.)*)\) Tj`)

// pdfTexts parses every text-drawing operation out of a BuildPDF document.
func pdfTexts(t *testing.T, pdf []byte) []pdfText {
	t.Helper()
	var out []pdfText
	for page, stream := range strings.Split(string(pdf), ">>\nstream\n")[1:] {
		stream, _, _ = strings.Cut(stream, "\nendstream")
		for _, m := range pdfTextOp.FindAllStringSubmatch(stream, -1) {
			font, _ := strconv.Atoi(m[1])
			size, _ := strconv.ParseFloat(m[2], 64)
			x, _ := strconv.ParseFloat(m[3], 64)
			y, _ := strconv.ParseFloat(m[4], 64)
			out = append(out, pdfText{page, font, size, x, y, unescapePDF(m[5])})
		}
	}
	if len(out) == 0 {
		t.Fatal("no text operations in the PDF")
	}
	return out
}

// unescapePDF undoes escapePDF, giving each WinAnsi byte back as the rune it
// stands for, so tests match "•" and pdfWidth measures the glyph drawn.
func unescapePDF(s string) string {
	fromWinAnsi := map[byte]rune{}
	for r, b := range winAnsi {
		if b >= 0x80 && b < 0xa0 {
			fromWinAnsi[b] = r
		}
	}
	decode := func(b byte) rune {
		if r, ok := fromWinAnsi[b]; ok {
			return r
		}
		return rune(b)
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			sb.WriteByte(s[i])
			continue
		}
		if i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
			n, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			sb.WriteRune(decode(byte(n)))
			i += 3
			continue
		}
		i++
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// findPDFText is the first drawn string containing text. A string holds a
// run of words in one style, so a word is found inside its line.
func findPDFText(texts []pdfText, text string) (pdfText, bool) {
	for _, tx := range texts {
		if strings.Contains(tx.text, text) {
			return tx, true
		}
	}
	return pdfText{}, false
}

// The exported PDF drew every heading in the body's font and size, so the
// report read as one undifferentiated wall of text.
func TestPDFHeadingsStandOutFromBody(t *testing.T) {
	texts := pdfTexts(t, BuildPDF("# Title\n\n## Section\n\nBody text here.\n\n### Detail\n\nMore body."))
	body, _ := findPDFText(texts, "Body")
	for _, h := range []string{"Title", "Section", "Detail"} {
		got, ok := findPDFText(texts, h)
		if !ok {
			t.Fatalf("heading %q missing: %+v", h, texts)
		}
		if got.font != 2 || got.size <= body.size {
			t.Errorf("heading %q drawn in font F%d at %.1fpt, body F%d at %.1fpt", h, got.font, got.size, body.font, body.size)
		}
	}
}

// List items were separated by three blank lines (the item and its paragraph
// each ended with a paragraph break), and their wrapped lines ran back under
// the bullet instead of under the text.
func TestPDFListItemsAreTightWithHangingIndent(t *testing.T) {
	long := strings.Repeat("evidence supports the conditional answer ", 6)
	texts := pdfTexts(t, BuildPDF("- first item\n- second item\n- "+long+"\n- last item"))
	first, _ := findPDFText(texts, "first")
	second, _ := findPDFText(texts, "second")
	if gap := first.y - second.y; gap <= 0 || gap > 2*first.size {
		t.Errorf("items %.1fpt apart at %.1fpt text, want a single line step", gap, first.size)
	}
	bullet, ok := findPDFText(texts, "•")
	if !ok {
		t.Fatalf("no bullet drawn: %+v", texts)
	}
	last, _ := findPDFText(texts, "last")
	var starts []float64
	for _, tx := range texts {
		if tx.y < second.y && tx.y > last.y && tx.text != "•" {
			starts = append(starts, tx.x)
		}
	}
	if len(starts) < 2 {
		t.Fatalf("long item did not wrap: %+v", texts)
	}
	for _, x := range starts {
		if x <= bullet.x || x != first.x {
			t.Errorf("wrapped line starts at %.1f, bullet at %.1f, item text at %.1f", x, bullet.x, first.x)
		}
	}
}

// Ordered lists lost their numbers: every item became "- ", so the
// numbered citations no longer matched the numbers the report refers to.
func TestPDFOrderedListsKeepTheirNumbers(t *testing.T) {
	texts := pdfTexts(t, BuildPDF("3. third\n4. fourth"))
	for _, n := range []string{"3.", "4."} {
		if _, ok := findPDFText(texts, n); !ok {
			t.Errorf("marker %q missing: %+v", n, texts)
		}
	}
}

// Lines were wrapped at 78 characters of a proportional font, which filled
// about two thirds of the page and left a ragged empty right margin.
func TestPDFFillsTheMeasuredLineWidth(t *testing.T) {
	texts := pdfTexts(t, BuildPDF(strings.Repeat("The residential evidence is narrower than the commercial record. ", 20)))
	lines := map[float64][]pdfText{}
	for _, tx := range texts {
		if tx.size > 8 { // skip the page-number footer
			lines[tx.y] = append(lines[tx.y], tx)
		}
	}
	var rights []float64
	for _, words := range lines {
		last := words[len(words)-1]
		rights = append(rights, last.x+pdfWidth(last.text, pdfFont(last.font-1), last.size))
	}
	slices.Sort(rights)
	if len(rights) < 4 {
		t.Fatalf("paragraph wrapped onto %d lines", len(rights))
	}
	if right := rights[len(rights)-1]; right > pdfPageWidth-pdfMargin+0.01 {
		t.Errorf("a line overflows the right margin: ends at %.1f", right)
	}
	// Every line but the paragraph's last must reach close to the margin.
	if short := rights[1]; short < pdfPageWidth-pdfMargin-60 {
		t.Errorf("a full line ends at %.1f, %.1fpt short of the margin", short, pdfPageWidth-pdfMargin-short)
	}
}

// A word wrapped at a rune count was split in half ("su" / "pply-chain");
// the measured wrap must keep words whole, styled pieces included.
func TestPDFWrapKeepsWordsWhole(t *testing.T) {
	src := strings.Repeat("sup**ply**-chain security versus manufacturing maturity ", 12)
	texts := pdfTexts(t, BuildPDF(src))
	for i := 1; i < len(texts); i++ {
		prev, cur := texts[i-1], texts[i]
		if strings.HasSuffix(prev.text, "sup") && (cur.text != "ply" || cur.y != prev.y) {
			t.Fatalf("styled word split across lines: %+v then %+v", prev, cur)
		}
	}
	if got, _ := findPDFText(texts, "ply"); got.font != 2 {
		t.Errorf("bold piece drawn in F%d", got.font)
	}
}

// Tables were flattened into "cell — cell — cell" runs of text with no
// columns at all.
func TestPDFTablesRenderAsColumns(t *testing.T) {
	pdf := BuildPDF("| Dimension | LFP | Sodium-ion |\n|---|---|---|\n| Safety | Proven | Assessed |\n| Cost | Lower | Higher |\n")
	texts := pdfTexts(t, pdf)
	if strings.Contains(string(pdf), "—") || strings.Contains(string(pdf), `\227`) || strings.Contains(string(pdf), "|") {
		t.Error("table cells are still joined by delimiters")
	}
	header, _ := findPDFText(texts, "Dimension")
	if header.font != 2 {
		t.Errorf("header row drawn in F%d, want bold", header.font)
	}
	row := []string{"Safety", "Proven", "Assessed"}
	prev, _ := findPDFText(texts, row[0])
	for _, cell := range row[1:] {
		got, ok := findPDFText(texts, cell)
		if !ok || got.y != prev.y || got.x <= prev.x {
			t.Errorf("cell %q at (%.1f, %.1f), previous cell at (%.1f, %.1f)", cell, got.x, got.y, prev.x, prev.y)
		}
		prev = got
	}
	if cost, _ := findPDFText(texts, "Cost"); cost.y >= prev.y {
		t.Error("second row is not below the first")
	}
}

// A row taller than a page must continue on the next one rather than run
// off the bottom of the first, and nothing in it may be lost.
func TestPDFSplitsOversizedTableRows(t *testing.T) {
	long := strings.Repeat("word ", 1500) + "LASTWORD"
	texts := pdfTexts(t, BuildPDF("| A | B |\n|---|---|\n| "+long+" | short |\n"))
	last, ok := findPDFText(texts, "LASTWORD")
	if !ok {
		t.Fatal("oversized row lost its tail")
	}
	if last.page == 0 {
		t.Error("oversized row did not continue on a later page")
	}
	for _, tx := range texts {
		if tx.y < pdfMargin-1 && tx.size > 8 {
			t.Fatalf("text drawn below the bottom margin: %+v", tx)
		}
	}
}

// Links must stay clickable and printed: the link text, an annotation that
// opens the URL, and the URL itself for a paper copy.
func TestPDFLinksAreClickableAndKeepTheURL(t *testing.T) {
	pdf := string(BuildPDF("See [the guide](https://example.com/a_(b)/c) and https://example.org/x."))
	if !strings.Contains(pdf, `/S /URI /URI (https://example.com/a_\(b\)/c)`) {
		t.Errorf("no URI annotation for the link:\n%s", pdf)
	}
	if !strings.Contains(pdf, "/Annots [") || !strings.Contains(pdf, "/Subtype /Link") {
		t.Error("page carries no link annotations")
	}
	texts := pdfTexts(t, []byte(pdf))
	var all strings.Builder
	for _, tx := range texts {
		all.WriteString(tx.text)
	}
	for _, want := range []string{"guide", "https://example.com/a_(b)/c", "https://example.org/x"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("printed text lost %q: %q", want, all.String())
		}
	}
}

// A long URL broke mid-word at an arbitrary character and could overflow
// the line; it must break at its own separators and stay inside the margin.
func TestPDFLongURLsStayInsideTheMargin(t *testing.T) {
	url := "https://www.downgraf.com/general/sodium-ion-battery-for-home-storage-best-brands-buyers-guide-2026-with-a-very-long-tail/"
	texts := pdfTexts(t, BuildPDF("Source: ["+url+"]("+url+") and ["+url+"]("+url+")"))
	for _, tx := range texts {
		if right := tx.x + pdfWidth(tx.text, pdfFont(tx.font-1), tx.size); right > pdfPageWidth-pdfMargin+0.01 {
			t.Errorf("%q ends at %.1f, past the margin", tx.text, right)
		}
	}
}

// The fact-check list marks unverified claims with ✗ and contradictions with
// ⚠; both printed as "?" and set off the lost-characters warning on every
// run that left a claim unconfirmed.
func TestPDFPrintsFactCheckMarks(t *testing.T) {
	if pdfHasUnsupportedText("✗ not verified · ⚠ contradiction") {
		t.Error("fact-check marks still count as unsupported text")
	}
	pdf := string(BuildPDF("- ✗ not verified: c3\n- ⚠ contradiction on c4"))
	if strings.Contains(pdf, "(?)") || strings.Contains(pdf, "?") {
		t.Errorf("a mark printed as ?:\n%s", pdf)
	}
}

// A twelve-page export had no page numbers.
func TestPDFNumbersPages(t *testing.T) {
	texts := pdfTexts(t, BuildPDF(strings.Repeat("A paragraph of report text.\n\n", 120)))
	pages := texts[len(texts)-1].page + 1
	if pages < 2 {
		t.Fatalf("120 paragraphs fit on %d page", pages)
	}
	for p := 1; p <= pages; p++ {
		if _, ok := findPDFText(texts, fmt.Sprintf("%d / %d", p, pages)); !ok {
			t.Errorf("page %d has no number", p)
		}
	}
}

// Every link printed its destination inline, so a table cell citing three
// sources carried three URLs and grew a page tall. Each destination is
// printed once, at its last link: the citation list closing the report.
func TestPDFPrintsEachLinkDestinationOnce(t *testing.T) {
	pdf := BuildPDF("Body cites [Guide](https://example.com/guide).\n\n1. [Guide: full title](https://example.com/guide) — ok")
	var drawn []string
	for _, tx := range pdfTexts(t, pdf) {
		drawn = append(drawn, tx.text)
	}
	all := strings.Join(drawn, "\n")
	if n := strings.Count(all, "https://example.com/guide"); n != 1 {
		t.Errorf("destination printed %d times, want once:\n%s", n, all)
	}
	if body, _ := findPDFText(pdfTexts(t, pdf), "Body cites"); !strings.Contains(all, "full title\n(https://example.com/guide)") {
		t.Errorf("destination not printed at the citation (body at y=%.1f):\n%s", body.y, all)
	}
	if n := strings.Count(string(pdf), "/URI (https://example.com/guide)"); n != 2 {
		t.Errorf("%d links open the destination, want both", n)
	}
}

// A URL's closing parenthesis was a break point of its own and could start
// the next line alone.
func TestPDFURLKeepsItsClosingParenthesis(t *testing.T) {
	for _, piece := range splitURL("(https://homepowermatrix.com/sodium-ion-batteries-home-backup/)") {
		if piece == ")" {
			t.Fatal("closing parenthesis split from the URL")
		}
	}
}

// Found in review of the typeset PDF: a heading longer than a page was drawn
// after a single room check and ran off the bottom.
func TestPDFLongHeadingStaysOnThePage(t *testing.T) {
	for _, tx := range pdfTexts(t, BuildPDF("# "+strings.Repeat("Wide ", 400))) {
		if tx.y < pdfMargin-1 && tx.size > 8 {
			t.Fatalf("heading drawn below the bottom margin: %+v", tx)
		}
	}
}

// goldmark keeps a link destination as written, so a Markdown escape or an
// entity reached the clickable URI instead of the URL it spells.
func TestPDFLinkDestinationIsUnescaped(t *testing.T) {
	pdf := string(BuildPDF(`[x](https://example.com/a\_b?x=1&amp;y=2)`))
	if !strings.Contains(pdf, "/URI (https://example.com/a_b?x=1&y=2)") {
		t.Errorf("URI keeps Markdown escapes:\n%s", pdf)
	}
}

// Two columns of unbreakable words claimed the whole width and squeezed the
// third below its own padding, so its text ran past the right margin.
func TestPDFTableColumnsStayInsideTheMargin(t *testing.T) {
	long := strings.Repeat("W", 100)
	texts := pdfTexts(t, BuildPDF("| A | B | W |\n|---|---|---|\n| "+long+" | "+long+" | W |\n"))
	for _, tx := range texts {
		if right := tx.x + pdfWidth(tx.text, pdfFont(tx.font-1), tx.size); right > pdfPageWidth-pdfMargin+0.01 {
			t.Errorf("%q ends at %.2f, past the margin", tx.text, right)
		}
	}
}

// The lost-characters warning must judge what is drawn: a carriage return
// kept in a code block printed as "?" unwarned, and an em space, drawn as a
// plain space, warned.
func TestPDFWarningMatchesWhatIsDrawn(t *testing.T) {
	if pdf := string(BuildPDF("```\r\nalpha\r\n```\r\n")); strings.Contains(pdf, "alpha?") || strings.Contains(pdf, `alpha\015`) {
		t.Error("carriage return printed in a code block")
	}
	if pdfDropsText("A B") {
		t.Error("an em space, drawn as a space, set off the warning")
	}
}

// Found in an end-to-end run: "string → string" printed as "string ? string"
// and set off the lost-characters warning. Arrows and comparison signs have
// no WinAnsi glyph but a plain-text spelling.
func TestPDFSpellsArrowsAndComparisons(t *testing.T) {
	src := "`string → string` and x ≤ y ≥ z ≠ w ≈ v, a ⇒ b, −3"
	if pdfDropsText(src) {
		t.Error("arrows and comparisons still count as lost text")
	}
	var drawn []string
	for _, tx := range pdfTexts(t, BuildPDF(src)) {
		drawn = append(drawn, tx.text)
	}
	got := strings.Join(drawn, "")
	for _, want := range []string{"string -> string", "x <= y >= z != w ~ v", "a => b", "-3"} {
		if !strings.Contains(got, want) {
			t.Errorf("printed %q, want %q in it", got, want)
		}
	}
}

// checkSources returned early on an empty list without clearing the scope,
// so an assistant reused for a second run stayed confined to the first run's
// sites while the second run's record said it searched the open web.
func TestAnOpenWebRunClearsAnEarlierScope(t *testing.T) {
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}}
	if _, _, err := runWithDomains(t, a, []string{"go.dev"}); err != nil {
		t.Fatal(err)
	}
	res, _, err := runWithDomains(t, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.scoped != nil || res.Report.Domains != nil {
		t.Errorf("second run scoped to %v, record says %v; want the open web on both", a.scoped, res.Report.Domains)
	}
}

// Options.Domains reached the search unparsed from any caller but the CLI
// flag, so a library caller's "GO.DEV" matched no result.
func TestRunParsesTheDomainsItIsGiven(t *testing.T) {
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}}
	if _, _, err := runWithDomains(t, a, []string{"https://www.GO.dev/doc"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.scoped, []string{"go.dev"}) {
		t.Errorf("scoped to %v, want go.dev", a.scoped)
	}
	if _, _, err := runWithDomains(t, a, []string{"localhost"}); !errors.Is(err, tools.ErrSearchConfig) {
		t.Errorf("localhost: err = %v, want a search setting error", err)
	}
}

// A single SearXNG never rests, so after its upstream engines suspended it a
// run went on asking: each sub-agent sent its fallback query, then the whole
// follow-up round ran, seven more queries into the same limit. A refused
// search now ends its sub-agent's queries.
func TestRefusedSearchStopsTheSubAgentsQueries(t *testing.T) {
	sink := &MultiSink{}
	d := NewDriver(failingSearch{&fakeAssistant{}}, sink, nil, 1)
	sub := agent.SubTopic{ID: "1", Name: "A", Query: "widgetdb limits", Terms: []string{"widgetdb", "quota"}}
	if n := len(subQueries("q", sub)); n != 2 {
		t.Fatalf("the sub-topic plans %d queries; the test needs a fallback to skip", n)
	}
	_, _ = d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{sub}))
	searches := 0
	for _, e := range sink.TimelineSnapshot() {
		if e.Type == Search {
			searches++
		}
	}
	if searches != 1 {
		t.Errorf("sent %d queries after a rate limit, want 1", searches)
	}
	if !d.isSearchDown() {
		t.Error("a rate-limited search left the run thinking search answers, so a follow-up round would run")
	}
}

// Every source in the report's lists ended in "— ok", the fetch status of a
// page read in full: on nearly every line, it said nothing, and the sources
// that were not read in full did not stand out.
func TestSourceListsMarkOnlySourcesNotReadInFull(t *testing.T) {
	res := testResult()
	res.Findings = []agent.Finding{
		{Title: "Read", URL: "https://a.example/read", Content: "c", Status: "ok"},
		{Title: "Rated", URL: "https://a.example/rated", Content: "c", Status: "ok", Confidence: "high"},
		{Title: "Snippet", URL: "https://b.example/snippet", Content: "c", Status: "degraded"},
		{Title: "Rated snippet", URL: "https://b.example/rated", Content: "c", Status: "degraded", Confidence: "low"},
		{Title: "Recollection", Content: "c", Status: "unverified"},
		{Title: "Unknown", URL: "https://c.example/x", Content: "c"},
	}
	md := MarkdownReport(res)
	for _, want := range []string{
		"[Read](https://a.example/read)\n",
		"[Rated](https://a.example/rated) — _high_\n",
		"[Snippet](https://b.example/snippet) — snippet only\n",
		"[Rated snippet](https://b.example/rated) — _low_ (snippet only)\n",
		"Recollection — never fetched, no URL\n",
		"[Unknown](https://c.example/x) — never fetched\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("source list lacks %q:\n%s", want, md)
		}
	}
	for _, bad := range []string{"— ok", "(ok)", "degraded", "unverified"} {
		if strings.Contains(md, bad) {
			t.Errorf("source list says %q:\n%s", bad, md)
		}
	}
}

// With SearXNG not running, a run confined to wikipedia.org stopped on "None
// of the listed domains can be researched: wikipedia.org search-failed",
// which blamed the site. No site was asked about: the search was down, and
// the run now says so and what to check, as a run with no domain list does.
func TestSiteCheckWithSearchDownBlamesTheSearch(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := "http://" + ln.Addr().String()
	_ = ln.Close()
	_, searchErr := tools.NewSearXNGClient(addr, time.Second).Search(context.Background(), "q")

	down := map[string]string{"wikipedia.org": tools.DomainSearchFail, "britannica.com": tools.DomainSearchFail}
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}, statuses: down, searchErr: searchErr}
	_, _, err = runWithDomains(t, a, []string{"wikipedia.org", "britannica.com"})
	if err == nil {
		t.Fatal("the run went on with no search")
	}
	for _, want := range []string{"no search answered while checking the listed sites", "cannot reach SearXNG at " + addr, "SEARXNG_URL", "doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("run error %q does not mention %q", err, want)
		}
	}
	if errors.Is(err, ErrNoUsableSources) || strings.Contains(err.Error(), "wikipedia.org") {
		t.Errorf("run error %q still blames the sites", err)
	}
	if a.planned != 0 {
		t.Errorf("planner called %d times; the run should stop before it", a.planned)
	}

	// A site the search did answer about is the list's fault, not the
	// search's: the run names each site and why.
	mixed := map[string]string{"wikipedia.org": tools.DomainSearchFail, "nowhere.example": tools.DomainUnindexed}
	a = &scopingAssistant{fakeAssistant: &fakeAssistant{}, statuses: mixed, searchErr: searchErr}
	_, _, err = runWithDomains(t, a, []string{"wikipedia.org", "nowhere.example"})
	if !errors.Is(err, ErrNoUsableSources) || !strings.Contains(err.Error(), "nowhere.example unindexed") {
		t.Errorf("mixed failures: err = %v, want each site and why named", err)
	}
}

// Sources are marked in the page's words: told "degraded", the model
// answered with a label the reader could not find on the page.
func TestRunOverviewMarksSourcesAsThePageDoes(t *testing.T) {
	o := RunOverview(askMeta())
	for _, want := range []string{"/acme) fetched\n", "b.example/) snippet only\n"} {
		if !strings.Contains(o, want) {
			t.Errorf("overview lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "degraded") {
		t.Errorf("overview says degraded:\n%s", o)
	}
}

// scriptedChecker answers FactCheck from a list, in order, over a structured
// analysis. The claim calls and the inference calls each run down the list
// on their own: the check makes both at once. calls counts the claim calls.
type scriptedChecker struct {
	*scriptedAnalyzer
	checks                []*agent.FactCheckResult
	mu                    sync.Mutex
	calls, inferenceCalls int
}

func (s *scriptedChecker) FactCheck(_ context.Context, prompt string) (*agent.FactCheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := &s.calls
	if isInferenceCheck(prompt) {
		n = &s.inferenceCalls
	}
	fc := s.checks[min(*n, len(s.checks)-1)]
	*n++
	return fc, nil
}

func newScriptedChecker(checks ...*agent.FactCheckResult) *scriptedChecker {
	return &scriptedChecker{scriptedAnalyzer: &scriptedAnalyzer{fakeAssistant: &fakeAssistant{},
		answers: []*agent.Analysis{structured}}, checks: checks}
}

// A replay's fact-check reasoned through all twenty claims and then answered
// with nothing that parsed as a verdict. Governed as it was, every claim was
// insufficient and the run said "0 of 20 claims supported" with no error, as
// if the sources had failed. A check that judged nothing is asked once more.
func TestFactCheckThatJudgedNothingIsAskedAgain(t *testing.T) {
	judged := &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c1", Status: "insufficient"}}}
	sc := newScriptedChecker(&agent.FactCheckResult{}, judged)
	sink := &MultiSink{}
	res, err := NewDriver(sc, sink, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if sc.calls != 2 || res.FactCheck == nil || len(res.FactCheck.Verdicts) != 1 {
		t.Errorf("fact-check ran %d times, result %+v; want the second, judged answer", sc.calls, res.FactCheck)
	}
	if !slices.ContainsFunc(sink.TimelineSnapshot(), func(e Event) bool {
		return strings.Contains(e.Detail, "the fact-check returned no verdicts; asking once more")
	}) {
		t.Error("the second ask was not reported")
	}
}

// Empty twice, the check did not run, and the run says so: unverified, at low
// confidence, instead of a verdict of insufficient on every claim.
func TestFactCheckEmptyTwiceIsReportedUnavailable(t *testing.T) {
	sc := newScriptedChecker(&agent.FactCheckResult{})
	sink := &MultiSink{}
	res, err := NewDriver(sc, sink, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if sc.calls != 2 || res.FactCheck != nil || res.Summary.Confidence != "low" {
		t.Errorf("calls %d, fact-check %+v, confidence %q; want 2, none, low", sc.calls, res.FactCheck, res.Summary.Confidence)
	}
	if !slices.ContainsFunc(sink.TimelineSnapshot(), func(e Event) bool {
		return e.Type == Error && strings.HasPrefix(e.Detail, "fact-check unavailable, reporting unverified: ") &&
			strings.Contains(e.Detail, "claims: it returned no verdicts, twice")
	}) {
		t.Error("the run did not report the check as unavailable")
	}
}

// fixingPlanner answers the plan with a corrected question, as the planner
// does for a typo.
type fixingPlanner struct {
	*fakeAssistant
	fixed string
}

func (f *fixingPlanner) Plan(ctx context.Context, question string, subTopics int) (agent.Outline, error) {
	out, err := f.fakeAssistant.Plan(ctx, question, subTopics)
	out.Question = f.fixed
	return out, err
}

// The question as typed ("overalll", over two lines) titled the report and
// named its files. The planner's fix is what the run reports under, and the
// brief keeps what was typed so the change is seen before launch.
func TestFixedQuestionTitlesTheRun(t *testing.T) {
	a := &fixingPlanner{fakeAssistant: &fakeAssistant{}, fixed: "Best cloud storage overall"}
	var stdout, stderr bytes.Buffer
	res, err := Run(context.Background(), Options{
		Question: "Best cloud storage\noveralll", Assistant: a, DepthMode: "quick", OutDir: t.TempDir(),
		JSONL: true, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Question != a.fixed || strings.Contains(res.MDPath, "overalll") {
		t.Errorf("report question %q at %s, want it titled %q", res.Report.Question, res.MDPath, a.fixed)
	}
	if res.Plan.Asked != "Best cloud storage overalll" {
		t.Errorf("plan.Asked = %q, want the typed question on one line", res.Plan.Asked)
	}
	r := NewRenderer(discard{}, Theme{})
	r.plan = res.Plan
	if head := strings.Join(r.briefHead(), "\n"); !strings.Contains(head, "typed as: Best cloud storage overalll") {
		t.Errorf("brief does not show what was typed:\n%s", head)
	}
}

// The browser reviews the plan and hands it back to run; the typed question
// went with it only as far as the page, so a run of the reviewed plan lost
// what the planner had corrected.
func TestReadPlanKeepsTheTypedQuestion(t *testing.T) {
	plan, err := ReadPlan(strings.NewReader(`{"question":"cloud storage overall","asked":"cloud storage overalll",
		"depth":{"key":"quick"},"sub_topics":[{"name":"Pricing"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Asked != "cloud storage overalll" {
		t.Errorf("plan.Asked = %q, want the typed question kept", plan.Asked)
	}
}
