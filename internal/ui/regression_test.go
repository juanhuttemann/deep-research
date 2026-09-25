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
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
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
	got := markdownText("| Read:Write Ratio | Choice |\n| --- | --- |\n| 1:1 | **Mutex** |\n| 10:1 | RWMutex |\n")
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
	var reported Event
	for _, e := range sink.Timeline {
		if e.Type == Verify && e.Status == "offtopic" {
			reported = e
		}
		if e.Type == Citation && e.SourceURL == "https://viveros.example/1" {
			t.Error("off-topic source was cited")
		}
	}
	if reported.URL != "https://viveros.example/1" {
		t.Fatalf("off-topic source never reported: %+v", sink.Timeline)
	}
	if reported.Line == "" || !strings.Contains(reported.Line, "viveros.example") {
		t.Errorf("off-topic event has no readable line: %q", reported.Line)
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

func (p *progressDuringPlan) Plan(ctx context.Context, question string, subTopics int) ([]agent.SubTopic, error) {
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

func (b *breadthRecorder) Plan(ctx context.Context, question string, subTopics int) ([]agent.SubTopic, error) {
	b.asked = subTopics
	n := subTopics
	if b.over > 0 {
		n = b.over
	}
	out := make([]agent.SubTopic, n)
	for i := range out {
		out[i] = agent.SubTopic{ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("Topic %d", i+1)}
	}
	return out, nil
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
		"fact-check": factCheckPrompt("claims", findings, true),
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

	self := factCheckPrompt("claim", unretrieved, false)
	if !strings.Contains(self, "not fetched") || !strings.Contains(self, "unverified") {
		t.Errorf("self-consistency prompt does not say what the material is:\n%s", self)
	}
	if strings.HasPrefix(self, "Verify these claims") {
		t.Error("self-consistency prompt still asks for verification")
	}
	if got := factCheckPrompt("claim", retrieved, true); !strings.HasPrefix(got, "Verify these claims") {
		t.Errorf("retrieved-source prompt changed:\n%s", got)
	}

	if got := factCheckDetail(false); !strings.Contains(got, "self-consistency") {
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
	sub := agent.SubTopic{ID: "1", Name: "Facet", Notes: "Examine perovskite tandem degradation."}
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
	sub := agent.SubTopic{ID: "1", Name: "Facet", Notes: "Examine perovskite tandem degradation."}
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
