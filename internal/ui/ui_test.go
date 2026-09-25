package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// ---- test doubles ---------------------------------------------------------

// fakeAssistant is a deterministic, offline assistant whose behaviour can be
// scripted per query. It drives the driver and renderer without any network.
type fakeAssistant struct {
	// planTopics are returned by Plan; nil => Local-style default.
	planTopics []agent.SubTopic
	// findings maps a query to the findings/signals to return. When a query is
	// absent, a single default finding with an "ok" signal is returned.
	findings map[string][]agent.Finding
	signals  map[string][]agent.SourceSignal
	// skipped maps a query to results the search tools rejected as off-topic.
	skipped map[string][]agent.SkippedSource
	summary string
	// analyzed captures the prompt Analyze was called with.
	analyzed string
	// tokensPerCall is added to the running usage total by every model call,
	// standing in for what a provider reports.
	tokensPerCall int
	tokens        atomic.Int64
}

func (f *fakeAssistant) Plan(ctx context.Context, question string, subTopics int) ([]agent.SubTopic, error) {
	if f.planTopics != nil {
		return f.planTopics, nil
	}
	return []agent.SubTopic{
		{ID: "1", Name: "Alpha", Notes: "first branch"},
		{ID: "2", Name: "Beta", Notes: "second branch"},
	}, nil
}

func (f *fakeAssistant) ResearchDetail(ctx context.Context, query string) (*agent.ResearchDetail, error) {
	f.call()
	// The default finding's URL carries the query: distinct queries return
	// distinct pages, as a real search does, so the run's cross-sub-agent
	// deduplication is not silently collapsing every sub-agent's result.
	url := "https://example.com/x?q=" + url.QueryEscape(query)
	fs := f.findings[query]
	if fs == nil {
		fs = []agent.Finding{{Query: query, Title: "Finding for " + query, Content: "content", URL: url}}
	}
	ss := f.signals[query]
	if ss == nil {
		ss = []agent.SourceSignal{{URL: url, Domain: "example.com", Status: "ok"}}
	}
	return &agent.ResearchDetail{Findings: fs, Signals: ss, Skipped: f.skipped[query]}, nil
}

func (f *fakeAssistant) Analyze(ctx context.Context, prompt string) (*agent.Analysis, error) {
	f.call()
	f.analyzed = prompt
	return &agent.Analysis{Answer: "synthesized answer", Confidence: "high"}, nil
}

func (f *fakeAssistant) FactCheck(ctx context.Context, claims string) (*agent.FactCheckResult, error) {
	f.call()
	return &agent.FactCheckResult{Verified: []agent.VerifiedClaim{{Claim: claims, Verified: true, Evidence: "e"}}}, nil
}

func (f *fakeAssistant) Summarize(ctx context.Context, prompt string) (*agent.Summary, error) {
	f.call()
	if f.summary != "" {
		return &agent.Summary{Report: f.summary, Executive: "exec"}, nil
	}
	return &agent.Summary{Report: prompt, Executive: "exec summary"}, nil
}

// Research delegates to ResearchDetail for interface completeness.
func (f *fakeAssistant) Research(ctx context.Context, query string) ([]agent.Finding, error) {
	det, err := f.ResearchDetail(ctx, query)
	if err != nil {
		return nil, err
	}
	return det.Findings, nil
}

func (f *fakeAssistant) SetSourceBudget(int)      {}
func (f *fakeAssistant) SetProgress(func(string)) {}
func (f *fakeAssistant) TokensUsed() int          { return int(f.tokens.Load()) }

// call records one model call's worth of reported usage.
func (f *fakeAssistant) call() { f.tokens.Add(int64(f.tokensPerCall)) }
func newTestPlan(depthDepth string, topics []agent.SubTopic) *Plan {
	depth := depthMode(depthDepth)
	return &Plan{
		Question:   "How will solid-state batteries commercialize by 2030?",
		Depth:      depth,
		MaxSources: 12,
		SubTopics:  topics,
	}
}

func testResult() *agent.ResearchResult {
	return &agent.ResearchResult{
		Question: "How will solid-state batteries commercialize by 2030?",
		Findings: []agent.Finding{
			{Query: "q", Title: "ArXiv Paper", Content: "c", URL: "https://arxiv.org/abs/1234", Confidence: "high"},
			{Query: "q", Title: "Nature Article", Content: "c", URL: "https://www.nature.com/articles/1", Confidence: "high"},
			{Query: "q", Title: "SEC Filing", Content: "c", URL: "https://www.sec.gov/edd/1", Confidence: "medium"},
			{Query: "q", Title: "TechCrunch", Content: "c", URL: "https://techcrunch.com/2026/01/01/x", Confidence: "low"},
		},
		Analysis: &agent.Analysis{Answer: "synthesized answer", Confidence: "high"},
		FactCheck: &agent.FactCheckResult{
			Verified: []agent.VerifiedClaim{{Claim: "c", Verified: true, Evidence: "e"}},
		},
		Summary:   &agent.Summary{Report: "# Report\n\nSolid-state batteries will commercialize.", Executive: "Exec summary", Confidence: "high"},
		Timestamp: time.Now(),
	}
}

// ---- renderer tests -------------------------------------------------------

func TestRenderBrief(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: true})
	plan := newTestPlan("deep", []agent.SubTopic{
		{ID: "1", Name: "Anode materials", Notes: "silicon vs graphite"},
		{ID: "2", Name: "Market timelines"},
	})
	r.RenderBrief(plan)

	out := buf.String()
	for _, want := range []string{"RESEARCH BRIEF", "How will solid-state", "Anode materials", "Market timelines", "[enter] launch"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI colour when theme enabled")
	}

	// With colour disabled the same frame must be free of ANSI escapes.
	var plain bytes.Buffer
	rp := NewRenderer(&plain, Theme{Enabled: false})
	rp.RenderBrief(plan)
	if strings.Contains(plain.String(), "\x1b[") {
		t.Error("did not expect ANSI when theme disabled")
	}
}

func TestRenderTreeFromEvents(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Chemistry", Notes: "n"}})
	r.RenderBrief(plan) // start in brief
	// emit events that move the renderer into the supervisor tree
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	r.Emit(Event{Type: Search, Query: "silicon anode", SubID: "1", Line: "Searching: silicon anode"})
	r.Emit(Event{Type: Read, URL: "https://example.com", Domain: "example.com", SubID: "1", Line: "Read example.com"})

	out := buf.String()
	for _, want := range []string{"Research", "Chemistry", "Searching: silicon anode", "Read example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderReport(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.RenderReport(testResult(), 7, 1200)

	out := buf.String()
	for _, want := range []string{"RESEARCH COMPLETE", "Source Breakdown", "Academic", "Financial", "News"} {
		if !strings.Contains(out, want) {
			t.Errorf("report output missing %q:\n%s", want, out)
		}
	}
	// counts: 2 academic (arXiv, Nature), 1 financial (SEC), 1 news
	if !strings.Contains(out, "Academic / Pre-prints: 2") {
		t.Errorf("expected academic count 2 in:\n%s", out)
	}
	if !strings.Contains(out, "Financial / Filings: 1") {
		t.Errorf("expected financial count 1 in:\n%s", out)
	}
	if !strings.Contains(out, "News / Media: 1") {
		t.Errorf("expected news count 1 in:\n%s", out)
	}
}

func TestVerifyLabels(t *testing.T) {
	cases := []struct {
		sig agent.SourceSignal
		out string
	}{
		{agent.SourceSignal{Domain: "nature.com", Status: "ok"}, "✓ nature.com 200 OK"},
		{agent.SourceSignal{Domain: "paywalled.com", Status: "degraded"}, "! paywalled.com snippet only"},
		{agent.SourceSignal{Code: "404", Status: "dropped"}, "✗ 404 dropped"},
		{agent.SourceSignal{Domain: "invented.example", Status: "unverified"}, "~ invented.example unverified"},
	}
	for _, c := range cases {
		if got := verifyLabel(c.sig); got != c.out {
			t.Errorf("verifyLabel(%+v) = %q, want %q", c.sig, got, c.out)
		}
	}
}

// ---- driver tests ---------------------------------------------------------

func TestDriverRunsEndToEnd(t *testing.T) {
	ctx := context.Background()
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, NewByteReader(nil), 3)
	d.now = func() time.Time { return time.Now() }

	plan := newTestPlan("standard", []agent.SubTopic{
		{ID: "1", Name: "Alpha", Notes: "alpha notes"},
		{ID: "2", Name: "Beta", Notes: "beta notes"},
	})
	res, err := d.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("expected findings")
	}
	// Each sub-topic searches its name (never its prose notes), so two
	// sub-topics yield one finding each from the fake assistant.
	if len(res.Findings) < 2 {
		t.Errorf("expected >=2 findings, got %d", len(res.Findings))
	}
	if res.Summary == nil || res.Summary.Report == "" {
		t.Error("expected a populated summary")
	}
	if len(sink.Timeline) == 0 {
		t.Error("expected a populated event timeline")
	}

	// The timeline must contain the phase markers and a report event.
	var sawPhase, sawReport bool
	for _, e := range sink.Timeline {
		if e.Type == Phase {
			sawPhase = true
		}
		if e.Type == Report {
			sawReport = true
		}
	}
	if !sawPhase || !sawReport {
		t.Errorf("timeline missing phase/report events: sawPhase=%v sawReport=%v", sawPhase, sawReport)
	}
}

func TestDriverTracksTokenAndSourceCounters(t *testing.T) {
	ctx := context.Background()
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{tokensPerCall: 120}, sink, NewByteReader(nil), 3)

	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Solo", Notes: "s"}})
	if _, err := d.Run(ctx, plan); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var lastToken Event
	var sawToken bool
	for _, e := range sink.Timeline {
		if e.Type == Token {
			lastToken, sawToken = e, true
		}
	}
	if !sawToken {
		t.Fatal("expected at least one Token event")
	}
	if lastToken.Tokens <= 0 {
		t.Errorf("expected positive token count, got %d", lastToken.Tokens)
	}
	if lastToken.Sources == 0 {
		t.Errorf("expected source count > 0, got %d", lastToken.Sources)
	}
}

func TestDriverCancelViaEsc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled => Run should short-circuit
	sink := &MultiSink{}
	d := NewDriver(&fakeAssistant{}, sink, NewByteReader(nil), 3)
	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})
	if _, err := d.Run(ctx, plan); err == nil {
		t.Error("expected an error when the context is already cancelled")
	}
}

// ---- confirmBrief tests ---------------------------------------------------

func TestConfirmBriefLaunchesOnEnter(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})
	_, action := waitBrief(NewByteReader([]byte("\r")), r, plan, rebudgetForTest)
	if action != actionLaunch {
		t.Errorf("expected launch action, got %d", action)
	}
}

func TestConfirmBriefCancels(t *testing.T) {
	for name, key := range map[string]string{"q": "q", "esc": "\x1b"} {
		var buf bytes.Buffer
		r := NewRenderer(&buf, Theme{Enabled: false})
		plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})
		_, action := waitBrief(NewByteReader([]byte(key)), r, plan, rebudgetForTest)
		if action != actionCancel {
			t.Errorf("%s: expected cancel action, got %d", name, action)
		}
	}
}

func rebudgetForTest(p *Plan, d DepthMode) *Plan { return p.WithDepth(d) }

func TestConfirmBriefCyclesDepth(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})

	// Two presses: quick -> standard -> deep, then launch.
	got, action := waitBrief(NewByteReader([]byte("dd\r")), r, base, rebudgetForTest)
	if action != actionLaunch {
		t.Errorf("expected launch after cycling, got %d", action)
	}
	if got.Depth.Key != "deep" {
		t.Errorf("expected depth cycled to deep, got %q", got.Depth.Key)
	}
	// Cycling re-budgets; it must not drop the decomposition or re-plan.
	if len(got.SubTopics) != len(base.SubTopics) {
		t.Errorf("cycling depth changed the sub-topics: %#v", got.SubTopics)
	}
	if quick := base.WithDepth(depthMode("quick")); got.MaxSources <= quick.MaxSources {
		t.Errorf("depth change did not re-budget: deep sources=%d, quick=%d",
			got.MaxSources, quick.MaxSources)
	}
}

func TestConfirmBriefEditsSubtopic(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})
	got, action := waitBrief(NewByteReader([]byte("eNew added topic\n\r")), r, base, rebudgetForTest)
	if action != actionLaunch {
		t.Errorf("expected launch, got %d", action)
	}
	found := false
	for _, s := range got.SubTopics {
		if s.Name == "New added topic" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected injected sub-topic, got %#v", got.SubTopics)
	}
	// The key that did it has to be advertised in the brief.
	if !strings.Contains(buf.String(), "[e] add") {
		t.Errorf("no add hint in the brief:\n%s", buf.String())
	}
}

func TestConfirmBriefRenamesSubtopic(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{
		{ID: "1", Name: "First", Notes: "n"}, {ID: "2", Name: "Second", Notes: "n"}})
	// "r" then "2" picks the second topic; the prompt starts seeded with the
	// current name, so six backspaces clear "Second" before the new one.
	got, _ := waitBrief(NewByteReader([]byte("r2\n\x7f\x7f\x7f\x7f\x7f\x7fRenamed\n\r")), r, base, rebudgetForTest)
	if got.SubTopics[1].Name != "Renamed" || got.SubTopics[0].Name != "First" {
		t.Errorf("rename hit the wrong topic: %#v", got.SubTopics)
	}
}

func TestConfirmBriefDeletesSubtopic(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{
		{ID: "1", Name: "First", Notes: "n"}, {ID: "2", Name: "Second", Notes: "n"}})
	got, _ := waitBrief(NewByteReader([]byte("x1\n\r")), r, base, rebudgetForTest)
	if len(got.SubTopics) != 1 || got.SubTopics[0].Name != "Second" {
		t.Errorf("delete removed the wrong topic: %#v", got.SubTopics)
	}
	if got.SubTopics[0].ID != "1" {
		t.Errorf("IDs must be renumbered after a delete: %#v", got.SubTopics)
	}
}

func TestConfirmBriefKeepsTheLastSubtopicAndIgnoresBadPicks(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Only", Notes: "n"}})
	got, _ := waitBrief(NewByteReader([]byte("x1\nx9\nrzz\n\r")), r, base, rebudgetForTest)
	if len(got.SubTopics) != 1 || got.SubTopics[0].Name != "Only" {
		t.Errorf("plan must keep one sub-topic: %#v", got.SubTopics)
	}
}

func TestConfirmBriefEscapeAbandonsAnEdit(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S", Notes: "n"}})
	got, _ := waitBrief(NewByteReader([]byte("ehalf typed\x1b\r")), r, base, rebudgetForTest)
	if len(got.SubTopics) != 1 {
		t.Errorf("Esc must abandon the edit, got %#v", got.SubTopics)
	}
}

func TestReadPromptEditing(t *testing.T) {
	// Backspace deletes a whole rune, not a byte.
	got, ok := readPromptSeeded(NewByteReader([]byte("ab名\x7f\x7fc\r")), "", nil)
	if !ok || got != "ac" {
		t.Errorf("readPrompt = %q (ok=%v), want \"ac\"", got, ok)
	}
	// Every keystroke is echoed so the reader sees what they are typing.
	var seen []string
	if _, _ = readPromptSeeded(NewByteReader([]byte("hi\r")), "", func(s string) {
		seen = append(seen, s)
	}); strings.Join(seen, "|") != "|h|hi" {
		t.Errorf("echo sequence = %q, want \"|h|hi\"", strings.Join(seen, "|"))
	}
}

// ---- end-to-end Run tests -------------------------------------------------

func TestRunEndToEndWritesArtifacts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	res, err := Run(context.Background(), Options{
		Question:  "How will solid-state batteries commercialize?",
		Assistant: &fakeAssistant{summary: "Solid-state batteries will dominate."},
		DepthMode: "standard",
		OutDir:    dir,
		Stdout:    &stdout,
		Stderr:    &stderr,
		KeyScript: "\r", // press Enter to launch
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Report == nil {
		t.Fatal("expected a report")
	}
	for _, p := range []string{res.MDPath, res.PDFPath, res.JSONPath} {
		if p == "" {
			t.Error("expected a generated artifact path")
			continue
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("artifact missing: %s: %v", p, err)
		}
	}
	// The markdown file must contain the report body.
	md, err := os.ReadFile(res.MDPath)
	if err != nil {
		t.Fatalf("read md: %v", err)
	}
	if !strings.Contains(string(md), "commercialize") {
		t.Errorf("markdown missing question term:\n%s", md)
	}
	// The JSON metadata must contain citations.
	metaData, err := os.ReadFile(res.JSONPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("json metadata invalid: %v", err)
	}
	if len(meta.Citations) == 0 {
		t.Error("expected citations in metadata")
	}
	if len(meta.Timeline) == 0 {
		t.Error("expected a populated timeline")
	}
}

func TestRunCancelSkipsReport(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	_, err := Run(context.Background(), Options{
		Question:  "q",
		Assistant: &fakeAssistant{},
		OutDir:    dir,
		Stdout:    &stdout,
		KeyScript: "q", // cancel at the brief
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("expected no artifacts for a cancelled run, got %d", len(entries))
	}
}

func TestRunJSONLStreamToStdout(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if _, err := Run(context.Background(), Options{
		Question:  "q",
		Assistant: &fakeAssistant{},
		DepthMode: "quick",
		OutDir:    dir,
		JSONL:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
		KeyScript: "\r",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 5 {
		t.Fatalf("expected multiple JSONL lines, got %d: %q", len(lines), stdout.String())
	}
	for _, ln := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("JSONL line not valid json: %q: %v", ln, err)
		}
		if ev.Type == "" {
			t.Errorf("JSONL event missing type: %q", ln)
		}
	}
}

func TestRunQuietPrintsReportToStdout(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	summary := "Quiet summary line."
	if _, err := Run(context.Background(), Options{
		Question:  "q",
		Assistant: &fakeAssistant{summary: summary},
		OutDir:    dir,
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
		KeyScript: "\r",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout.String(), summary) {
		t.Errorf("quiet stdout should carry the report:\n%s", stdout.String())
	}
}

// ---- pure helper tests ----------------------------------------------------

func TestDepthModeSelection(t *testing.T) {
	cases := map[string]string{
		"quick":    "quick",
		"deep":     "deep",
		"standard": "standard",
		"":         "standard",
		"bogus":    "standard",
	}
	for in, want := range cases {
		if got := depthMode(in); got.Key != want {
			t.Errorf("depthMode(%q) = %q, want %q", in, got.Key, want)
		}
	}
}

func TestCycleDepth(t *testing.T) {
	if got := cycleDepth(DepthModes[0]); got.Key != "standard" {
		t.Errorf("cycle quick = %q, want standard", got.Key)
	}
	if got := cycleDepth(DepthModes[2]); got.Key != "quick" {
		t.Errorf("cycle deep wraps to %q, want quick", got.Key)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{90 * time.Second, "1:30"},
		{3*time.Hour + 5*time.Minute + 9*time.Second, "3:05:09"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// ---- export / pdf tests ---------------------------------------------------

func TestMarkdownReportFormat(t *testing.T) {
	md := MarkdownReport(testResult())
	if !strings.Contains(md, "# How will") {
		t.Errorf("markdown missing title:\n%s", md)
	}
	if !strings.Contains(md, "Executive Summary") {
		t.Errorf("markdown missing executive summary:\n%s", md)
	}
	if !strings.Contains(md, "Citations (4)") {
		t.Errorf("markdown expected 4 citations:\n%s", md)
	}
	// Citations are deduplicated by URL and numbered.
	if !strings.Contains(md, "1. [ArXiv Paper](https://arxiv.org/abs/1234)") {
		t.Errorf("markdown missing citation #1:\n%s", md)
	}
}

func TestBuildMetaSortsCitationsAndCapturesTimeline(t *testing.T) {
	meta := BuildMeta(testResult(), []Event{
		{Type: SubAgent, SubID: "1", SubName: "Alpha", SubState: "done", Progress: 100, Line: "complete"},
		{Type: Search, Query: "q", Line: "Searching q"},
	}, "standard", 42, 8)
	if meta.Sources != 8 || meta.Tokens != 42 {
		t.Errorf("unexpected counters: %+v", meta)
	}
	if len(meta.Citations) != 4 {
		t.Errorf("expected 4 citations, got %d", len(meta.Citations))
	}
	// sorted ascending by URL
	if meta.Citations[0].URL != "https://arxiv.org/abs/1234" {
		t.Errorf("citations not sorted: %q", meta.Citations[0].URL)
	}
	if len(meta.Notes) != 1 || meta.Notes[0].SubName != "Alpha" {
		t.Errorf("expected sub-agent note, got %+v", meta.Notes)
	}
	if len(meta.Timeline) != 2 {
		t.Errorf("expected 2 timeline entries, got %d", len(meta.Timeline))
	}
}

func TestWriteExportsToTempDir(t *testing.T) {
	dir := t.TempDir()
	mdPath, err := WriteMarkdown(testResult(), dir)
	if err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	pdfPath, err := WritePDF(testResult(), dir)
	if err != nil {
		t.Fatalf("WritePDF: %v", err)
	}
	meta := BuildMeta(testResult(), nil, "standard", 10, 3)
	jsonPath, err := WriteMetadata(meta, dir)
	if err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}
	for _, p := range []string{mdPath, pdfPath, jsonPath} {
		if filepath.Ext(p) == "" {
			t.Errorf("expected an extension in %q", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if len(data) == 0 {
			t.Errorf("empty file: %s", p)
		}
	}
	// The PDF must start with the magic header and close with a trailer.
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("PDF missing header:\n%s", string(pdf))
	}
	if !strings.Contains(string(pdf), "trailer") {
		t.Errorf("PDF missing trailer:\n%s", string(pdf))
	}
}

func TestBuildPDFFormsValidStructure(t *testing.T) {
	pdf := BuildPDF([]string{"Title line", "Second paragraph that is a bit longer to exercise wrapping logic here.", "Third line"})
	s := string(pdf)
	if !strings.HasPrefix(s, "%PDF-1.4") {
		t.Fatalf("bad header: %q", s[:8])
	}
	for _, marker := range []string{
		"/Type /Catalog", "/Type /Pages", "/Type /Page ",
		"/BaseFont /Helvetica", "stream\n", "\nendstream", "endobj", "trailer", "startxref",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("PDF missing %q", marker)
		}
	}
	checkPDFXref(t, pdf)
}

// checkPDFXref walks the trailer -> startxref -> xref table and asserts that
// every object the table lists is actually present at the offset it names, and
// that /Root points at one of them. A PDF whose xref marks reserved-but-never-
// written objects as free is unreadable, which is exactly what this catches.
func checkPDFXref(t *testing.T, pdf []byte) {
	t.Helper()
	s := string(pdf)

	i := strings.LastIndex(s, "startxref")
	if i < 0 {
		t.Fatal("no startxref")
	}
	var xrefOff int
	if _, err := fmt.Sscanf(s[i:], "startxref\n%d", &xrefOff); err != nil {
		t.Fatalf("unparseable startxref: %v", err)
	}
	if xrefOff <= 0 || xrefOff >= len(s) {
		t.Fatalf("startxref %d out of range (len %d)", xrefOff, len(s))
	}

	var count int
	if _, err := fmt.Sscanf(s[xrefOff:], "xref\n0 %d", &count); err != nil {
		t.Fatalf("no xref table at %d: %v", xrefOff, err)
	}
	// Entries are fixed 20-byte records following the "xref\n0 N\n" preamble.
	body := s[xrefOff+len(fmt.Sprintf("xref\n0 %d\n", count)):]
	for n := 1; n < count; n++ {
		if len(body) < 20*(n+1) {
			t.Fatalf("xref truncated at entry %d", n)
		}
		entry := body[20*n : 20*n+20]
		var off int
		var gen int
		var kind string
		if _, err := fmt.Sscanf(entry, "%d %d %s", &off, &gen, &kind); err != nil {
			t.Fatalf("bad xref entry %d %q: %v", n, entry, err)
		}
		if kind != "n" {
			t.Errorf("object %d listed as free in the xref, but the trailer's document needs it", n)
			continue
		}
		if want := fmt.Sprintf("%d 0 obj", n); !strings.HasPrefix(s[off:], want) {
			t.Errorf("xref points object %d at %d, which holds %q", n, off, s[off:min(off+16, len(s))])
		}
	}

	var root int
	if _, err := fmt.Sscanf(s[strings.LastIndex(s, "/Root"):], "/Root %d 0 R", &root); err != nil {
		t.Fatalf("unparseable /Root: %v", err)
	}
	if root < 1 || root >= count {
		t.Fatalf("/Root %d is outside the xref (size %d)", root, count)
	}
}

// TestBuildPDFStreamLengthIsAccurate guards the one number a PDF reader trusts
// blindly: a /Length that disagrees with the bytes between stream and
// endstream makes the page render empty or the file fail to parse.
func TestBuildPDFStreamLengthIsAccurate(t *testing.T) {
	s := string(BuildPDF([]string{"alpha", "beta (with parens) and a \\ backslash"}))
	rest := s
	found := 0
	for {
		i := strings.Index(rest, "/Length ")
		if i < 0 {
			break
		}
		var declared int
		if _, err := fmt.Sscanf(rest[i:], "/Length %d", &declared); err != nil {
			t.Fatalf("unparseable /Length: %v", err)
		}
		start := strings.Index(rest[i:], "stream\n")
		end := strings.Index(rest[i:], "\nendstream")
		if start < 0 || end < 0 {
			t.Fatal("stream keywords missing around /Length")
		}
		if got := end - (start + len("stream\n")); got != declared {
			t.Errorf("/Length %d but stream holds %d bytes", declared, got)
		}
		found++
		rest = rest[i+end:]
	}
	if found == 0 {
		t.Fatal("no content stream in the PDF")
	}
}

// TestBuildPDFPaginates keeps a long report from being clipped: a single page
// holds ~48 lines, so 200 lines must produce several page objects and still
// contain every line.
func TestBuildPDFPaginates(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d", i)
	}
	s := string(BuildPDF(lines))
	if n := strings.Count(s, "/Type /Page "); n < 2 {
		t.Errorf("200 lines produced %d page objects, want several", n)
	}
	for _, ln := range []string{"line-000", "line-100", "line-199"} {
		if !strings.Contains(s, "("+ln+") Tj") {
			t.Errorf("line %q dropped from the PDF", ln)
		}
	}
	checkPDFXref(t, []byte(s))
}

// TestBuildPDFEncodesNonASCII keeps the report's typographic characters (…, —,
// •) from emitting raw UTF-8, which a WinAnsi-encoded font renders as mojibake.
func TestBuildPDFEncodesNonASCII(t *testing.T) {
	s := string(BuildPDF([]string{"an em—dash, an ellipsis… and a bullet •", "arrow → ok"}))
	body := s[strings.Index(s, "stream\n"):]
	for _, r := range body {
		if r > 0x7f {
			t.Fatalf("raw non-ASCII rune %q left in the content stream", r)
		}
	}
	if !strings.Contains(s, `\227`) { // em dash in WinAnsi is 0x97
		t.Error("em dash was not mapped to its WinAnsi slot")
	}
}

// TestWrapTextBreaksOnWords: the PDF export wraps the report body, and a wrap
// that cuts at an exact rune count splits words down the middle ("supply" ->
// "su" / "pply"), which is what the real exports were doing.
func TestWrapTextBreaksOnWords(t *testing.T) {
	src := "supply-chain security versus higher efficiency and manufacturing maturity"
	lines := wrapText(src, 24)
	for _, ln := range lines {
		if dispWidth(ln) > 24 {
			t.Errorf("line exceeds width: %q", ln)
		}
	}
	// Every word must survive intact across the wrap.
	for _, w := range strings.Fields(src) {
		found := false
		for _, ln := range lines {
			for _, got := range strings.Fields(ln) {
				if got == w {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("word %q was split across lines: %q", w, lines)
		}
	}
}

// TestWrapTextKeepsParagraphBreaks: blank lines separate the report's sections
// and must survive into the PDF.
func TestWrapTextKeepsParagraphBreaks(t *testing.T) {
	lines := wrapText("first para\n\nsecond para", 40)
	if len(lines) != 3 || lines[0] != "first para" || lines[1] != "" || lines[2] != "second para" {
		t.Errorf("paragraph break lost: %q", lines)
	}
}

// TestWrapTextHardBreaksLongWords: a URL longer than the line still must not
// overflow the page.
func TestWrapTextHardBreaksLongWords(t *testing.T) {
	lines := wrapText("see https://example.com/a/very/long/path/that/never/ends/at/all here", 20)
	for _, ln := range lines {
		if dispWidth(ln) > 20 {
			t.Errorf("long word overflowed: %q", ln)
		}
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText("one two three four five six seven", 10)
	if len(lines) < 3 {
		t.Errorf("expected wrapping to produce multiple lines, got %d: %v", len(lines), lines)
	}
	for _, ln := range lines {
		if len([]rune(ln)) > 10 {
			t.Errorf("line exceeds width: %q (%d)", ln, len([]rune(ln)))
		}
	}
}

func TestJSONLSinkProducesValidLines(t *testing.T) {
	var buf bytes.Buffer
	j := JSONL{W: &buf}
	j.Emit(Event{Type: Search, Query: "a b"})
	j.Emit(Event{Type: Token, Tokens: 5})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for _, ln := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("invalid jsonl: %v", err)
		}
	}
}

func TestSubQueriesAnchorOnQuestion(t *testing.T) {
	const q = "widget 2.5 vs gadget 1.5 model"
	got := subQueries(q, agent.SubTopic{Name: "Performance Benchmarks", Notes: "Compare scores."})
	want := []string{q + " Performance Benchmarks"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The planner's note is the one place that says what the branch is looking
// for, and it used to be searched never — a sub-agent that found nothing on
// its first query had no second thing to ask. It is still never searched as
// prose: backends degrade badly on a sentence, so the follow-up carries a few
// distilled terms and stays anchored on the question.
func TestSubQueriesFollowUpDistilsNotes(t *testing.T) {
	const q = "q"
	const notes = "Investigate the current perovskite tandem degradation data."
	got := subQueries(q, agent.SubTopic{Name: "Facet", Notes: notes})
	if len(got) != 2 {
		t.Fatalf("got %q, want a follow-up query", got)
	}
	for _, s := range got {
		if !strings.HasPrefix(s, q+" ") {
			t.Errorf("query %q is not anchored on the question", s)
		}
		if strings.Contains(s, notes) || strings.Contains(s, "the current") {
			t.Errorf("note prose leaked into a search query: %q", s)
		}
	}
	for _, want := range []string{"perovskite", "tandem", "degradation"} {
		if !strings.Contains(got[1], want) {
			t.Errorf("follow-up %q dropped the distinctive term %q", got[1], want)
		}
	}
}

// A note with nothing in it the first query did not already cover is not a
// second query: re-issuing the same search would spend the budget twice.
func TestSubQueriesSkipsUselessFollowUp(t *testing.T) {
	for name, sub := range map[string]agent.SubTopic{
		"no notes":       {Name: "Facet"},
		"only stopwords": {Name: "Facet", Notes: "Investigate these."},
		"echoes the name": {Name: "Performance Benchmarks",
			Notes: "Compare performance benchmarks."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := subQueries("q", sub); len(got) != 1 {
				t.Errorf("got %q, want one query", got)
			}
		})
	}
}

func TestSubQueriesCapsLength(t *testing.T) {
	q := strings.Repeat("a", 60)
	got := subQueries(q, agent.SubTopic{Name: strings.Repeat("facet ", 20)})
	if len(got) != 1 {
		t.Fatalf("got %q", got)
	}
	if len(got[0]) > maxQueryLen {
		t.Errorf("query %d chars, want <= %d: %q", len(got[0]), maxQueryLen, got[0])
	}
	if !strings.HasPrefix(got[0], q) {
		t.Errorf("question was trimmed instead of the facet: %q", got[0])
	}
}

// failingFactChecker answers every phase normally except FactCheck.
type failingFactChecker struct{ agent.Assistant }

func (f *failingFactChecker) FactCheck(context.Context, string) (*agent.FactCheckResult, error) {
	return nil, errors.New("boom")
}

func TestFactCheckFailureStillProducesReport(t *testing.T) {
	ctx := context.Background()
	sink := &MultiSink{}
	d := NewDriver(&failingFactChecker{&fakeAssistant{}}, sink, NewByteReader(nil), 3)
	d.now = time.Now

	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Alpha", Notes: "n"}})
	res, err := d.Run(ctx, plan)
	if err != nil {
		t.Fatalf("a fact-check failure must not sink the run: %v", err)
	}
	if res.Summary == nil || res.Summary.Report == "" {
		t.Error("expected a report despite the fact-check failure")
	}
	var sawError bool
	for _, e := range sink.Timeline {
		if e.Type == Error {
			sawError = true
		}
	}
	if !sawError {
		t.Error("the fact-check failure should be surfaced as an Error event")
	}
}

func TestSummarizePromptCarriesFactCheck(t *testing.T) {
	fc := &agent.FactCheckResult{
		Verified:   []agent.VerifiedClaim{{Claim: "sky is blue", Evidence: "src1"}},
		Unverified: []string{"grass is purple"},
	}
	got := summarizePrompt("q", &agent.Analysis{Answer: "a"}, fc, nil, true)
	for _, want := range []string{"sky is blue", "src1", "grass is purple"} {
		if !strings.Contains(got, want) {
			t.Errorf("fact-check result %q missing from the summarizer prompt", want)
		}
	}
}

func TestRendererEmitsNoEscapesToAPipe(t *testing.T) {
	// A pipe is an *os.File but not a terminal; it must get clean text.
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()

	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(rd)
		done <- b
	}()

	r := NewRenderer(wr, Theme{Enabled: false})
	r.RenderReport(testResult(), 7, 1200)
	wr.Close()

	out := string(<-done)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI escape leaked into piped output: %q", out)
	}
	if !strings.Contains(out, "sources  7") {
		t.Errorf("totals missing from the completion card: %q", out)
	}
}

// countingAssistant records how many ResearchDetail calls overlap.
type countingAssistant struct {
	agent.Assistant
	mu       sync.Mutex
	inFlight int
	maxSeen  int
}

func (c *countingAssistant) ResearchDetail(ctx context.Context, q string) (*agent.ResearchDetail, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return c.Assistant.ResearchDetail(ctx, q)
}

func (c *countingAssistant) peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxSeen
}

func TestDriverRespectsParallelism(t *testing.T) {
	subs := make([]agent.SubTopic, 6)
	for i := range subs {
		subs[i] = agent.SubTopic{ID: strconv.Itoa(i + 1), Name: "topic " + strconv.Itoa(i+1)}
	}

	ca := &countingAssistant{Assistant: &fakeAssistant{}}
	d := NewDriver(ca, &MultiSink{}, NewByteReader(nil), 2)
	d.now = time.Now

	if _, err := d.Run(context.Background(), newTestPlan("standard", subs)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if peak := ca.peak(); peak > 2 {
		t.Errorf("%d sub-agents searched at once, Parallelism is 2", peak)
	} else if peak < 2 {
		t.Errorf("sub-agents did not run in parallel: peak %d", peak)
	}
}

func TestWaitBriefDrawsBriefBeforeBlocking(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	plan := newTestPlan("standard", []agent.SubTopic{
		{ID: "1", Name: "Anode materials", Notes: "silicon vs graphite"},
	})

	// An immediate Enter: the brief must already have been drawn.
	got, action := waitBrief(NewByteReader([]byte("\r")), r, plan, rebudgetForTest)
	if action != actionLaunch {
		t.Errorf("action = %d, want actionLaunch", action)
	}
	if got != plan {
		t.Error("plan changed unexpectedly")
	}
	out := buf.String()
	for _, want := range []string{"RESEARCH BRIEF", "Anode materials", "[enter] launch"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q; drew:\n%s", want, out)
		}
	}
}

// TestDriverReportsRealTokenUsage: the counters the UI shows and the metadata
// exports must come from what the provider reported, not from a synthetic
// per-source formula. Two sub-topics x one source each is 2 search calls plus
// analyze + fact-check + summarize = 5 calls.
func TestDriverReportsRealTokenUsage(t *testing.T) {
	fa := &fakeAssistant{tokensPerCall: 40}
	sink := &MultiSink{}
	d := NewDriver(fa, sink, NewByteReader(nil), 1)

	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "Alpha"}, {ID: "2", Name: "Beta"}})
	plan.MaxSources = 2
	if _, err := d.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if want := fa.TokensUsed(); d.tokens != want {
		t.Errorf("driver tokens = %d, assistant reported %d", d.tokens, want)
	}
	if d.tokens != 200 {
		t.Errorf("tokens = %d, want 200 (5 calls x 40)", d.tokens)
	}

	// No Token event may claim a total the assistant never reported.
	for _, e := range sink.Timeline {
		if e.Type == Token && e.Tokens > 200 {
			t.Errorf("Token event reports %d tokens, more than the run used", e.Tokens)
		}
	}
}

// TestExportsCarryPerSourceStatus: a source the scraper only got a snippet
// from, or one the model invented, must not be exported as a cleanly fetched
// citation. Both the .md and the .json read the finding's own status.
func TestExportsCarryPerSourceStatus(t *testing.T) {
	res := testResult()
	res.Findings = []agent.Finding{
		{Query: "q", Title: "Scraped", URL: "https://a.example/1", Confidence: "high", Status: "ok"},
		{Query: "q", Title: "Snippet", URL: "https://b.example/2", Confidence: "low", Status: "degraded"},
		{Query: "q", Title: "Invented", URL: "https://c.example/3", Confidence: "low", Status: "unverified"},
		{Query: "q", Title: "Unlabelled", URL: "https://d.example/4", Confidence: "low"},
	}

	meta := BuildMeta(res, nil, "deep", 0, 0)
	want := map[string]string{
		"https://a.example/1": "ok",
		"https://b.example/2": "degraded",
		"https://c.example/3": "unverified",
		// A finding with no signal was never confirmed fetched either.
		"https://d.example/4": "unverified",
	}
	for _, c := range meta.Citations {
		if got := c.Status; got != want[c.URL] {
			t.Errorf("citation %s status = %q, want %q", c.URL, got, want[c.URL])
		}
	}

	md := MarkdownReport(res)
	if !strings.Contains(md, "degraded") || !strings.Contains(md, "unverified") {
		t.Errorf("markdown citations hide the source status:\n%s", md)
	}
}

// TestBuildMetaRecordsDepth: the depth tier is known at plan time and belongs
// in the trace; it used to be written as an empty string.
func TestBuildMetaRecordsDepth(t *testing.T) {
	if got := BuildMeta(testResult(), nil, "quick", 0, 0).Depth; got != "quick" {
		t.Errorf("Depth = %q, want %q", got, "quick")
	}
}

// TestWriteArtifactsReportsFailures: an export that cannot be written must say
// so. Silently dropping it from the "Saved to" list looks like success.
func TestWriteArtifactsReportsFailures(t *testing.T) {
	// A file where the reports directory should be makes every write fail.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var stderr bytes.Buffer
	o := Options{OutDir: blocked, Stderr: &stderr}.withDefaults()
	o.Stderr = &stderr

	d := NewDriver(&fakeAssistant{}, &MultiSink{}, NewByteReader(nil), 1)
	md, pdf, meta := o.writeArtifacts(testResult(), &MultiSink{}, d, newTestPlan("quick", nil))
	if md != "" || pdf != "" || meta != "" {
		t.Errorf("failed writes reported paths: md=%q pdf=%q meta=%q", md, pdf, meta)
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Errorf("no warning for failed exports; stderr was:\n%s", stderr.String())
	}
}

// TestDriverFillsSummaryConfidence: the summarizer writes prose, not JSON, so
// it reports no confidence of its own. Left empty the .md printed a bare
// "**Confidence:**" line and the .json/history carried "". The analyzer's
// confidence is the run's, so it is what carries through.
func TestDriverFillsSummaryConfidence(t *testing.T) {
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, NewByteReader(nil), 1)
	res, err := d.Run(context.Background(), newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "Alpha"}}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary.Confidence == "" {
		t.Error("summary confidence is empty; the report prints a blank Confidence line")
	}
	if res.Summary.Confidence != res.Analysis.Confidence {
		t.Errorf("summary confidence %q does not match the analysis' %q",
			res.Summary.Confidence, res.Analysis.Confidence)
	}
}

// TestSlugDistinguishesQuestions: slug() strips every non-alphanumeric rune, so
// "What is X?" and "What is X" produced identical filenames and the second run
// silently overwrote the first's .md/.pdf/.json. Distinct questions must get
// distinct names; the same question re-run keeps overwriting its own files.
func TestSlugDistinguishesQuestions(t *testing.T) {
	pairs := [][2]string{
		{"What is X?", "What is X"},
		{"a-b", "a b"},
		{"Cost: 2024", "Cost 2024"},
	}
	for _, p := range pairs {
		if slug(p[0]) == slug(p[1]) {
			t.Errorf("slug(%q) == slug(%q) == %q: one run overwrites the other",
				p[0], p[1], slug(p[0]))
		}
	}
	q := "What is X?"
	if slug(q) != slug("What is "+"X?") {
		t.Error("slug is not deterministic; a re-run would pile up files")
	}
	if !strings.HasPrefix(slug("What is X?"), "What-is-X") {
		t.Errorf("slug lost its readable stem: %q", slug("What is X?"))
	}
	if slug("") == "" {
		t.Error("an empty question still needs a filename")
	}
}
