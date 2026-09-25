package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// ---- fact-check prompt ----------------------------------------------------

// The fact-checker's whole job is to compare claims against sources, and it
// was only ever sent the answer — so "verify these claims against the research
// findings" meant checking the answer against itself.
func TestFactCheckPromptCarriesTheFindings(t *testing.T) {
	findings := []agent.Finding{
		{Title: "Solid State Review", URL: "https://example.com/a", Content: "Cells reached 400 Wh/kg."},
		{Title: "Pilot Line", URL: "https://example.com/b", Content: "Production starts in 2027."},
	}
	got := factCheckPrompt("Batteries hit 400 Wh/kg.", findings, true)

	for _, want := range []string{
		"Batteries hit 400 Wh/kg.",
		"Solid State Review", "https://example.com/a", "Cells reached 400 Wh/kg.",
		"Pilot Line", "https://example.com/b", "Production starts in 2027.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fact-check prompt is missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "Verify these claims"); n > 1 {
		t.Errorf("instruction line repeated %d times", n)
	}
}

// The driver must send the findings to the fact-checker, not just the answer.
func TestDriverFactChecksAgainstTheFindings(t *testing.T) {
	fake := &fakeAssistant{}
	spy := &factCheckSpy{Assistant: fake}
	d := NewDriver(spy, &MultiSink{}, nil, 2)
	d.now = time.Now
	plan := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}})

	if _, err := d.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(spy.claims, "https://example.com/x") {
		t.Errorf("fact-check prompt never saw a source URL:\n%s", spy.claims)
	}
}

type factCheckSpy struct {
	agent.Assistant
	claims string
}

func (s *factCheckSpy) FactCheck(ctx context.Context, claims string) (*agent.FactCheckResult, error) {
	s.claims = claims
	return s.Assistant.FactCheck(ctx, claims)
}

// ---- dropped sources ------------------------------------------------------

// A source whose scrape failed and whose snippet was empty carries no content
// at all. Keeping it spent a slot of the sub-agent's budget, put a blank entry
// in the analyzer and summarizer prompts, and listed it as a citation.
func TestDroppedSourcesAreNotCitedOrBudgeted(t *testing.T) {
	fake := &fakeAssistant{
		planTopics: []agent.SubTopic{{ID: "1", Name: "Alpha"}},
		findings: map[string][]agent.Finding{
			"How will solid-state batteries commercialize by 2030? Alpha": {
				{Title: "Gone", Content: "", URL: "https://dead.example/1"},
				{Title: "Good", Content: "real content", URL: "https://live.example/2"},
			},
		},
		signals: map[string][]agent.SourceSignal{
			"How will solid-state batteries commercialize by 2030? Alpha": {
				{URL: "https://dead.example/1", Domain: "dead.example", Status: "dropped", Code: "404"},
				{URL: "https://live.example/2", Domain: "live.example", Status: "ok"},
			},
		},
	}
	sink := &MultiSink{}
	d := NewDriver(fake, sink, nil, 1)
	d.now = time.Now

	res, err := d.Run(context.Background(), newTestPlan("quick", fake.planTopics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range res.Findings {
		if f.URL == "https://dead.example/1" {
			t.Errorf("a dropped source became a finding: %+v", f)
		}
	}
	if d.sources != 1 {
		t.Errorf("dropped source counted toward the source total: %d", d.sources)
	}
	var cited, verified int
	for _, e := range sink.Timeline {
		if e.Type == Citation {
			cited++
		}
		if e.Type == Verify && e.Status == "dropped" {
			verified++
		}
	}
	if cited != 1 {
		t.Errorf("expected 1 citation, got %d", cited)
	}
	if verified != 1 {
		t.Error("the dropped source should still be reported in the timeline")
	}
}

// A finding that arrives with no signal at all was never confirmed fetched.
func TestUnsignalledFindingsAreNotClaimedVerified(t *testing.T) {
	fake := &fakeAssistant{
		planTopics: []agent.SubTopic{{ID: "1", Name: "Alpha"}},
		findings: map[string][]agent.Finding{
			"How will solid-state batteries commercialize by 2030? Alpha": {
				{Title: "No signal", Content: "text", URL: "https://x.example/1"},
			},
		},
		signals: map[string][]agent.SourceSignal{
			"How will solid-state batteries commercialize by 2030? Alpha": {},
		},
	}
	d := NewDriver(fake, &MultiSink{}, nil, 1)
	d.now = time.Now
	res, err := d.Run(context.Background(), newTestPlan("quick", fake.planTopics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected the finding to survive, got %d", len(res.Findings))
	}
	if res.Findings[0].Status != "unverified" {
		t.Errorf("status = %q, want unverified", res.Findings[0].Status)
	}
}

// ---- keys -----------------------------------------------------------------

// Arrow and function keys arrive as ESC [ A. Reading the ESC alone as the
// cancel key meant any arrow press killed a run in flight, or abandoned a
// half-typed steering note.
func TestArrowKeysAreNotReadAsEscape(t *testing.T) {
	for _, seq := range []string{"\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D", "\x1bOP"} {
		in := NewByteReader([]byte(seq))
		k, err := readKey(in)
		if err != nil {
			t.Fatalf("%q: readKey: %v", seq, err)
		}
		if k.Key == KeyEsc {
			t.Errorf("%q was read as Esc", seq)
		}
		// The whole sequence must be consumed, not left for the next read.
		if _, err := in.Next(); err == nil {
			t.Errorf("%q left bytes unconsumed", seq)
		}
	}
}

// A lone Esc still cancels.
func TestLoneEscapeStillCancels(t *testing.T) {
	k, err := readKey(NewByteReader([]byte{0x1b}))
	if err != nil {
		t.Fatalf("readKey: %v", err)
	}
	if k.Key != KeyEsc {
		t.Errorf("lone Esc read as %v, want KeyEsc", k.Key)
	}
}

// Raw mode clears ISIG, so Ctrl-C is never delivered as a signal. It has to be
// handled as a key or it does nothing at all, which is not what anyone typing
// it expects.
func TestCtrlCCancels(t *testing.T) {
	k, err := readKey(NewByteReader([]byte{0x03}))
	if err != nil {
		t.Fatalf("readKey: %v", err)
	}
	if k.Key != KeyCtrlC {
		t.Errorf("Ctrl-C read as %v, want KeyCtrlC", k.Key)
	}
	plan, action := waitBrief(NewByteReader([]byte{0x03}), NewRenderer(discard{}, Theme{}),
		newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}}), (*Plan).WithDepth)
	if action != actionCancel {
		t.Errorf("Ctrl-C in the brief did not cancel (plan %v)", plan.Depth.Key)
	}
}

// In line mode a whole line arrives as one keystroke, so "e Performance"
// carried both the key and its argument. Discarding the remainder threw the
// typed topic away and then blocked for a second line.
func TestLineModeKeepsTheRestOfTheLine(t *testing.T) {
	in := &lineModeReader{lines: []string{"e Performance Benchmarks", ""}}
	plan := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "A"}})
	plan, action := waitBrief(in, NewRenderer(discard{}, Theme{}), plan, (*Plan).WithDepth)
	if action != actionLaunch {
		t.Fatalf("expected launch, got cancel")
	}
	if len(plan.SubTopics) != 2 {
		t.Fatalf("expected the sub-topic to be added, got %d", len(plan.SubTopics))
	}
	if plan.SubTopics[1].Name != "Performance Benchmarks" {
		t.Errorf("sub-topic name = %q, want %q", plan.SubTopics[1].Name, "Performance Benchmarks")
	}
}

// lineModeReader is an Input that delivers whole lines, as a terminal that
// could not be switched to raw mode does.
type lineModeReader struct{ lines []string }

func (l *lineModeReader) Next() (byte, error) {
	s, err := l.NextLine()
	if err != nil || s == "" {
		return '\r', err
	}
	return s[0], nil
}

func (l *lineModeReader) NextLine() (string, error) {
	if len(l.lines) == 0 {
		return "", io.EOF
	}
	s := l.lines[0]
	l.lines = l.lines[1:]
	return s, nil
}

func (l *lineModeReader) Close()    {}
func (l *lineModeReader) Raw() bool { return false }

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// ---- report card honesty --------------------------------------------------

// The completion card labelled its source total "sources verified" while
// counting degraded, dropped and unverified sources too.
func TestReportCardDoesNotOverstateVerification(t *testing.T) {
	res := testResult()
	res.Findings[0].Status = "ok"
	res.Findings[1].Status = "degraded"
	res.Findings[2].Status = "unverified"
	res.Findings[3].Status = "ok"

	var buf strings.Builder
	r := NewRenderer(&buf, Theme{})
	r.RenderReport(res, 4, 100)
	out := buf.String()

	if strings.Contains(out, "sources verified") {
		t.Errorf("card still claims every source was verified:\n%s", out)
	}
	if !strings.Contains(out, "2 fetched") {
		t.Errorf("card does not break the sources down by status:\n%s", out)
	}
}

// Everything that was not academic or financial was filed under "News",
// including Wikipedia, documentation, forums and government pages.
func TestSourceBreakdownDoesNotFileEverythingUnderNews(t *testing.T) {
	res := &agent.ResearchResult{Findings: []agent.Finding{
		{URL: "https://arxiv.org/abs/1"},
		{URL: "https://www.sec.gov/x"},
		{URL: "https://en.wikipedia.org/wiki/Battery"},
		{URL: "https://go.dev/doc/effective_go"},
		{URL: "https://stackoverflow.com/questions/1"},
		{URL: "https://www.energy.gov/report"},
		{URL: "https://techcrunch.com/2026/01/01/x"},
	}}
	got := map[string]int{}
	for _, b := range sourceBreakdown(res) {
		got[b.label] = b.count
	}
	for label, want := range map[string]int{
		"Academic / Pre-prints": 1,
		"Reference":             1,
		"Documentation":         1,
		"Forums / Q&A":          1,
		"Government":            1,
		"News / Media":          1,
	} {
		if got[label] != want {
			t.Errorf("%s: got %d, want %d (all buckets: %v)", label, got[label], want, got)
		}
	}
}

// Empty buckets are noise on a card that is meant to be read at a glance.
func TestSourceBreakdownOmitsEmptyBuckets(t *testing.T) {
	res := &agent.ResearchResult{Findings: []agent.Finding{{URL: "https://arxiv.org/abs/1"}}}
	got := sourceBreakdown(res)
	if len(got) != 1 || got[0].label != "Academic / Pre-prints" {
		t.Errorf("expected only the non-empty bucket, got %+v", got)
	}
}

// ---- budgets and exports --------------------------------------------------

// --sources asks for N sources per sub-agent. The whole-run cap could quietly
// reduce that to a third of what was asked, with no message at all.
func TestSourcesPerTopicReportsWhenItIsCapped(t *testing.T) {
	p := &Plan{
		Question:  "q",
		Depth:     depthMode("standard"),
		SubTopics: []SubTopic{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}},
	}
	got, capped := p.WithSourcesPerTopic(20)
	if !capped {
		t.Error("asking for 20 per sub-agent across 4 sub-agents must report the cap")
	}
	if got.PerTopic() >= 20 {
		t.Errorf("expected the budget to be capped, PerTopic() = %d", got.PerTopic())
	}
	if _, capped := p.WithSourcesPerTopic(5); capped {
		t.Error("a request inside the cap must not be reported as reduced")
	}
}

// WriteMarkdown guards a nil summary; WritePDF dereferenced it and panicked on
// exactly the same input.
func TestWritePDFToleratesAMissingSummary(t *testing.T) {
	dir := t.TempDir()
	res := &agent.ResearchResult{Question: "q", Timestamp: time.Now()}
	if _, err := WritePDF(res, dir); err != nil {
		t.Fatalf("WritePDF with no summary: %v", err)
	}
}

// "Saved to reports:" followed by nothing told the reader their artifacts were
// written when every single write had failed.
func TestReportFilesSaysNothingWasWritten(t *testing.T) {
	var buf strings.Builder
	reportFiles(&buf, "reports", RunResult{})
	if strings.Contains(buf.String(), "Saved to") {
		t.Errorf("claimed a save with no paths: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "no report files were written") {
		t.Errorf("expected an explicit failure line, got %q", buf.String())
	}

	buf.Reset()
	reportFiles(&buf, "reports", RunResult{MDPath: "reports/a.md"})
	if !strings.Contains(buf.String(), "Saved to reports:") || !strings.Contains(buf.String(), "a.md") {
		t.Errorf("expected the saved file listed, got %q", buf.String())
	}
}

// Follow-up queries were parsed and then read by nothing at all.
func TestMarkdownReportCarriesOpenQuestions(t *testing.T) {
	res := testResult()
	res.Analysis.Gaps = []string{"long-term cycle life"}
	res.Analysis.FollowUp = []string{"solid-state cycle life 2026"}
	md := MarkdownReport(res)
	if !strings.Contains(md, "long-term cycle life") {
		t.Error("markdown report drops the analyzer's gaps")
	}
	if !strings.Contains(md, "solid-state cycle life 2026") {
		t.Error("markdown report drops the analyzer's follow-up queries")
	}
}

// ---- found by the live end-to-end run -------------------------------------

// Every sub-agent anchors its query on the same research question, so the
// search backend returns largely the same pages to all of them. A live run
// gathered "16 sources" that were four distinct URLs: the completion card and
// the JSON sidecar counted each page once per sub-agent, while the Markdown
// citation list — which deduplicates — showed four.
func TestASourceIsCountedOnceAcrossSubAgents(t *testing.T) {
	same := []agent.Finding{
		{Title: "Go", Content: "text", URL: "https://go.dev/doc/x"},
		{Title: "Refs", Content: "text", URL: "https://example.com/refs"},
	}
	sig := []agent.SourceSignal{
		{URL: "https://go.dev/doc/x", Domain: "go.dev", Status: "ok"},
		{URL: "https://example.com/refs", Domain: "example.com", Status: "ok"},
	}
	topics := []agent.SubTopic{{ID: "1", Name: "Alpha"}, {ID: "2", Name: "Beta"}}
	q := "How will solid-state batteries commercialize by 2030? "
	fake := &fakeAssistant{
		planTopics: topics,
		findings:   map[string][]agent.Finding{q + "Alpha": same, q + "Beta": same},
		signals:    map[string][]agent.SourceSignal{q + "Alpha": sig, q + "Beta": sig},
	}
	d := NewDriver(fake, &MultiSink{}, nil, 2)
	d.now = time.Now

	res, err := d.Run(context.Background(), newTestPlan("standard", topics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) != 2 {
		urls := make([]string, len(res.Findings))
		for i, f := range res.Findings {
			urls[i] = f.URL
		}
		t.Errorf("the same two pages became %d findings: %v", len(res.Findings), urls)
	}
	if d.sources != 2 {
		t.Errorf("source counter = %d, want 2 distinct sources", d.sources)
	}
}

// MetaNote documents itself as "a sub-agent's final state and last status
// line", but BuildMeta kept the first event it saw for each sub-agent — always
// the "queued" announcement — so every exported note read queued, even for
// sub-agents that had finished.
func TestBuildMetaRecordsTheFinalSubAgentState(t *testing.T) {
	timeline := []Event{
		{Type: SubAgent, SubID: "1", SubName: "Alpha", SubState: "queued", Line: "queued"},
		{Type: SubAgent, SubID: "1", SubName: "Alpha", SubState: "running", Line: "starting"},
		{Type: SubAgent, SubID: "1", SubName: "Alpha", Progress: 60},
		{Type: SubAgent, SubID: "1", SubName: "Alpha", SubState: "done", Line: "complete"},
	}
	m := BuildMeta(testResult(), timeline, "standard", 0, 0)
	if len(m.Notes) != 1 {
		t.Fatalf("expected one note, got %+v", m.Notes)
	}
	n := m.Notes[0]
	if n.State != "done" || n.Line != "complete" {
		t.Errorf("note kept the first event, not the last: %+v", n)
	}
	if n.Progress != 60 {
		t.Errorf("note lost the progress it reached: %+v", n)
	}
	if n.SubName != "Alpha" {
		t.Errorf("note lost the sub-agent name: %+v", n)
	}
}
