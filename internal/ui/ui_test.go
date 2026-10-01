package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
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

func (f *fakeAssistant) ResearchDetail(ctx context.Context, query string, _ []string) (*agent.ResearchDetail, error) {
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
	det, err := f.ResearchDetail(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	return det.Findings, nil
}

func (f *fakeAssistant) SetSourceBudget(int)           {}
func (f *fakeAssistant) SkipSources(func(string) bool) {}
func (f *fakeAssistant) SetProgress(func(string))      {}
func (f *fakeAssistant) TokensUsed() int               { return int(f.tokens.Load()) }

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

// A renamed branch is the reader's facet, not the planner's: the planner's
// query and terms would keep searching and ranking for the name it replaced.
func TestConfirmBriefRenameDropsThePlannersQuery(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("standard", []agent.SubTopic{
		{ID: "1", Name: "Memory", Query: "sync.Mutex memory overhead", Terms: []string{"Mutex", "memory"}},
		{ID: "2", Name: "Same", Query: "kept query", Terms: []string{"kept", "terms"}}})
	// "Memory" is six backspaces; the second rename confirms the seeded name.
	got, _ := waitBrief(NewByteReader([]byte("r1\n\x7f\x7f\x7f\x7f\x7f\x7fFairness\nr2\n\n\r")), r, base, rebudgetForTest)
	if s := got.SubTopics[0]; s.Name != "Fairness" || s.Query != "" || s.Terms != nil {
		t.Errorf("renamed topic kept the planner's search: %#v", s)
	}
	if s := got.SubTopics[1]; s.Query != "kept query" || len(s.Terms) != 2 {
		t.Errorf("an unchanged name lost the planner's search: %#v", s)
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
		{Type: Search, Query: "q", Terms: []string{"Memcached"}, Line: "Searching q"},
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
		t.Fatalf("expected 2 timeline entries, got %d", len(meta.Timeline))
	}
	// The terms are what explains an off-topic verdict after the run.
	if got := meta.Timeline[1].Terms; len(got) != 1 || got[0] != "Memcached" {
		t.Errorf("search terms not exported: %v", got)
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

// A plan without a planner query — the fallback plan, or a model that
// skipped the field — still searches, anchored on the question.
func TestSubQueriesAnchorOnQuestion(t *testing.T) {
	const q = "widget 2.5 vs gadget 1.5 model"
	got := subQueries(q, agent.SubTopic{Name: "Performance Benchmarks", Notes: "Compare scores."})
	want := []string{q + " Performance Benchmarks"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The planner's query names the subject in the question's language, so it is
// searched first; the question anchored on the facet is the fallback. Notes
// is a sentence for the reader and is never searched.
func TestSubQueriesPlannerQueryFirst(t *testing.T) {
	const q = "¿Cuáles son las ventajas de la energía nuclear en España?"
	sub := agent.SubTopic{Name: "Nuclear Advantages", Notes: "Investigate the current advantages.",
		Query: "ventajas energía nuclear España"}
	got := subQueries(q, sub)
	want := []string{sub.Query, anchoredQuery(q, sub.Name)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, s := range got {
		if strings.Contains(s, "Investigate") {
			t.Errorf("note prose leaked into a search query: %q", s)
		}
	}
}

// A fallback identical to the planner's query is not a second query:
// re-issuing the same search would spend the budget twice.
func TestSubQueriesSkipsUselessFollowUp(t *testing.T) {
	got := subQueries("q", agent.SubTopic{Name: "Facet", Query: "Q facet"})
	if len(got) != 1 || got[0] != "Q facet" {
		t.Errorf("got %q, want the planner query once", got)
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

func (c *countingAssistant) ResearchDetail(ctx context.Context, q string, terms []string) (*agent.ResearchDetail, error) {
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
	return c.Assistant.ResearchDetail(ctx, q, terms)
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

// The model writes sources and claim IDs freely: an invented URL, a page the
// run never fetched or a repeated ID would reach the report looking sourced.
// A source given as the prompt's entry number is the finding at that number.
func TestCheckAnalysisHoldsReferencesToWhatWasRetrieved(t *testing.T) {
	findings := []agent.Finding{
		{URL: "https://a.example/doc", Status: "ok"},
		{URL: "https://b.example/pricing", Status: "degraded"},
		{URL: "https://model.example/recalled", Status: "unverified"},
		{URL: "/", Status: "ok"},
	}
	a := &agent.Analysis{
		Claims: []agent.Claim{
			// "//" names no page; neither does the hostless result above.
			{ID: "c1", Text: "t", Sources: []string{"https://a.example/doc/", "https://invented.example", "1", "//"}},
			{ID: "c2", Text: "t", Sources: []string{"2", "#9"}},
			{ID: "c3", Text: "t", Sources: []string{"https://model.example/recalled"}},
			{ID: "c1", Text: "a second claim reusing an ID"},
		},
		Conflicts: []agent.Conflict{{Claims: []string{"c2", "c8"}, Resolution: "c2 holds"}},
	}
	checkAnalysis(a, findings)
	// Once, under the URL it was fetched as: the model's spelling and the
	// entry number name the same page, which the citations list as fetched.
	if got := a.Claims[0].Sources; len(got) != 1 || got[0] != "https://a.example/doc" {
		t.Errorf("c1 sources = %v, want only the retrieved page, as it was fetched", got)
	}
	if got := a.Claims[1].Sources; len(got) != 1 || got[0] != "https://b.example/pricing" {
		t.Errorf("c2 sources = %v, want entry 2 resolved and entry 9 dropped", got)
	}
	if c := a.Claims[2]; len(c.Sources) != 0 || c.Status != statusUnsourced {
		t.Errorf("c3 = %+v, want unsourced: the run fetched pages, and this one it never did", c)
	}
	if a.Claims[3].ID == "c1" {
		t.Error("a repeated claim ID was kept, leaving every reference to it ambiguous")
	}
	if got := a.Conflicts[0].Claims; len(got) != 1 || got[0] != "c2" {
		t.Errorf("conflict claims = %v, want the dangling c8 dropped", got)
	}
}

// decisionFixture is a checked-evidence setup: one fetched page whose text
// holds the passages the verdicts quote.
func decisionFixture() ([]agent.Finding, *agent.Analysis) {
	page := "## Pricing\n\nServerless is **billed per GB-hour** of data stored.\n\n" +
		"Reserved nodes save up to 55% on a [3-year term](https://p.example/ri)."
	findings := []agent.Finding{{URL: "https://p.example/pricing", Status: "ok", Content: page}}
	a := &agent.Analysis{
		Answer: "UNCHECKED PROSE",
		Claims: []agent.Claim{
			{ID: "c1", Text: "Serverless bills per GB-hour", Sources: []string{"https://p.example/pricing"}},
			{ID: "c2", Text: "Reserved nodes save 90%", Sources: []string{"https://p.example/pricing"}},
			{ID: "c3", Text: "Serverless has no minimum", Sources: []string{"https://p.example/pricing"}},
		},
		Recommendations: []agent.Recommendation{
			{Choose: "Serverless", When: "traffic is spiky", Claims: []string{"c1"}},
			{Choose: "Node-based", When: "traffic is steady", Claims: []string{"c1", "c2"}},
			{Choose: "Serverless, small caches", When: "data is tiny", Claims: []string{"c3"}},
		},
	}
	return findings, a
}

// follows judges each named recommendation to follow from its claims.
func follows(ids ...string) []agent.Inference {
	var out []agent.Inference
	for _, id := range ids {
		out = append(out, agent.Inference{ID: id, Follows: true})
	}
	return out
}

func verdict(id, status, source, quote string) agent.Verdict {
	return agent.Verdict{ID: id, Status: status, Reason: "r", Evidence: []agent.Evidence{{Source: source, Quote: quote}}}
}

// Every claim a recommendation names is a premise: one that fails blocks the
// recommendation, even when another it named passed. Keeping it on what was
// left let an incidental fact carry a conclusion whose deciding claim failed.
// A "supported" verdict whose quote is not in the page does not count.
func TestRecommendationStandsOnlyOnSupportedPremises(t *testing.T) {
	findings, a := decisionFixture()
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "https://p.example/pricing", "Serverless is billed per GB-hour of data stored."),
		verdict("c2", "contradicted", "https://p.example/pricing", "save up to 55%"),
		verdict("c3", "supported", "https://p.example/pricing", "Serverless has no minimum charge."),
	}, Inferences: follows("r1", "r2", "r3")}
	govern(a, fc, nil, findings)
	if ok := approvedUnits(a); len(ok) != 1 || ok[0].Choose != "Serverless" {
		t.Fatalf("approved = %+v, want only the one resting on c1", ok)
	}
	if b := a.Recommendations[1].Blocked; !strings.Contains(b, "c2 contradicted") {
		t.Errorf("node-based blocked = %q, want it to name the failed c2", b)
	}
	if b := a.Recommendations[2].Blocked; !strings.Contains(b, "quote_not_located") {
		t.Errorf("c3's recommendation blocked = %q, want the unlocated quote named", b)
	}
	p := summarizePrompt("q", a, fc, findings, true)
	if strings.Contains(p, "UNCHECKED PROSE") {
		t.Error("the analyzer's unchecked answer prose reached the summarizer")
	}
	if !strings.Contains(p, "Not established") || !strings.Contains(p, "choose Node-based when traffic is steady") {
		t.Errorf("the blocked recommendation is not marked as not concluded:\n%s", p)
	}
	sum := withAnswer(&agent.Summary{Report: "# q\n\n## Answer\n\nNode-based wins.\n\n## Comparison\n\ntable"}, a)
	if !strings.HasPrefix(sum.Report, "## Answer\n\n- **Serverless**: traffic is spiky\n") ||
		strings.Contains(sum.Report, "Node-based wins") || strings.Contains(sum.Report, "# q") {
		t.Errorf("report = %q, want the rendered answer in place of the summarizer's", sum.Report)
	}
}

// The checker returned verdicts for claims it was not given, several for one
// claim, and none for another. None of those may support a claim.
func TestMalformedVerdictsNeverSupportAClaim(t *testing.T) {
	findings, a := decisionFixture()
	quote := "Serverless is billed per GB-hour of data stored."
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c2", "supported", "1", "save up to 55%"),
		verdict("c2", "contradicted", "1", "save up to 55%"),
		verdict("c9", "supported", "1", quote),
		verdict("c3", "true", "1", quote),
	}}
	unknown := govern(a, fc, nil, findings)
	if len(unknown) != 1 || unknown[0] != "c9" {
		t.Errorf("unknown = %v, want the invented c9", unknown)
	}
	for _, c := range a.Claims {
		if c.Status != statusInsufficient {
			t.Errorf("%s = %s (%s), want insufficient", c.ID, c.Status, c.Note)
		}
	}
	if len(approvedUnits(a)) != 0 {
		t.Error("a recommendation was approved on malformed verdicts")
	}
	if len(fc.Unverified) != 3 || len(fc.Verified) != 0 {
		t.Errorf("report lists = %d unverified, %d verified; want the three claims unverified", len(fc.Unverified), len(fc.Verified))
	}
}

// A fact-check that did not run approves nothing, and the summarizer is told
// so, without the candidates being called wrong.
func TestFailedFactCheckApprovesNothing(t *testing.T) {
	findings, a := decisionFixture()
	govern(a, nil, errors.New("deadline exceeded"), findings)
	if len(approvedUnits(a)) != 0 {
		t.Error("a recommendation was approved with no fact-check")
	}
	if !strings.Contains(a.Claims[0].Note, "fact_check_unavailable: deadline exceeded") {
		t.Errorf("note = %q, want the failure named", a.Claims[0].Note)
	}
	if p := summarizePrompt("q", a, nil, findings, true); !strings.Contains(p, "could not be completed") {
		t.Errorf("the summarizer is not told the check failed:\n%s", p)
	}
}

// A quote is located through Markdown presentation, but not through a
// paraphrase, a changed number or changed case.
func TestLocateQuoteThroughMarkdownOnly(t *testing.T) {
	page := "Reserved nodes save **up to 55%** on a [3-year term](https://x) — see `cache.r7g`."
	for quote, want := range map[string]bool{
		"save **up to 55%** on a": true,
		"Reserved nodes save up to 55% on a 3-year term - see cache.r7g.": true,
		"Reserved nodes save up to 60% on a 3-year term":                  false,
		"reserved nodes save up to 55%":                                   false,
		"Reserved nodes can save as much as 55%":                          false,
		"":                                                                false,
	} {
		if got := locate(page, quote); got != want {
			t.Errorf("locate(%q) = %v, want %v", quote, got, want)
		}
	}
}

// The report's own fallback printed the analyzer's answer prose when the
// summary could not be written, skipping the fact-check the summary is held
// to. A checked analysis falls back to its approved decision instead.
func TestFailedSummaryDeliversTheCheckedDecision(t *testing.T) {
	findings, a := decisionFixture()
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
	}, Inferences: follows("r1")}
	govern(a, fc, nil, findings)
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1)
	res, err := d.partial(context.Background(), newTestPlan("quick", nil), findings, a, fc, "report writing failed", errors.New("500"))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Summary.Report; strings.Contains(r, "UNCHECKED PROSE") || !strings.Contains(r, "- **Serverless**: traffic is spiky") {
		t.Errorf("fallback report = %q, want the approved decision and not the prose", r)
	}
}

// With search off nothing is fetched and every finding is the model's own;
// holding claims to fetched pages there would strip every source.
func TestCheckAnalysisKeepsModelFindingsWhenNothingWasFetched(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{{ID: "c1", Text: "t", Sources: []string{"https://model.example/a"}}}}
	checkAnalysis(a, []agent.Finding{{URL: "https://model.example/a", Status: "unverified"}})
	if len(a.Claims[0].Sources) != 1 {
		t.Errorf("sources = %v, want the model's finding kept", a.Claims[0].Sources)
	}
}

// ---- review of the verdict pass: each test names the hole it closes ------

// With every recommendation blocked, the summarizer's own answer section went
// through untouched, and it could state a blocked choice.
func TestNoApprovedRecommendationReplacesTheSummarizersAnswer(t *testing.T) {
	findings, a := decisionFixture()
	govern(a, &agent.FactCheckResult{}, nil, findings)
	sum := withAnswer(&agent.Summary{Report: "## Answer\n\nChoose Node-based.\n\n## Why\n\nx"}, a)
	if strings.Contains(sum.Report, "Choose Node-based") || !strings.Contains(sum.Report, englishLabels[noApprovalEvidence]) {
		t.Errorf("report = %q, want the no-approval answer in place of the summarizer's", sum.Report)
	}
}

// Recommendations with no claims skipped the check and counted as approved,
// and a prose verdict's invented quote was listed as confirmed.
func TestAnalysisWithoutClaimsApprovesNothing(t *testing.T) {
	findings, _ := decisionFixture()
	a := &agent.Analysis{Recommendations: []agent.Recommendation{{Choose: "X", When: "w"}}}
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		{ID: "p1", Claim: "X is cheap", Status: "supported", Evidence: []agent.Evidence{{Source: "1", Quote: "not on the page"}}},
	}}
	govern(a, fc, nil, findings)
	if len(approvedUnits(a)) != 0 {
		t.Error("a recommendation naming no claim was approved")
	}
	if len(fc.Verified) != 0 || len(fc.Unverified) != 1 {
		t.Errorf("verified %v, unverified %v: an invented quote was confirmed", fc.Verified, fc.Unverified)
	}
}

// With search off the findings are the model's own; a quote "located" in
// them verified the model against itself.
func TestModelFindingsCannotSupportAClaim(t *testing.T) {
	findings, a := decisionFixture()
	findings[0].Status = "unverified"
	checkAnalysis(a, findings)
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
	}}
	govern(a, fc, nil, findings)
	if a.Claims[0].Status == statusSupported || len(approvedUnits(a)) != 0 {
		t.Errorf("c1 = %s (%s): a claim was supported by the model's own recollection", a.Claims[0].Status, a.Claims[0].Note)
	}
}

// Deleting every underscore, asterisk and backslash matched quotes the page
// does not contain, and a link whose target held parentheses never matched.
func TestLocateRemovesOnlyMarkup(t *testing.T) {
	for _, tc := range []struct {
		page, quote string
		want        bool
	}{
		{"Use `cache_size` here.", "Use cachesize here.", false},
		{"Use `cache_size` here.", "Use cache_size here.", true},
		{"Price is 2*3 dollars.", "Price is 23 dollars.", false},
		{"See [pricing](https://example.com/a_(b)) for rates.", "See pricing for rates.", true},
		{"It is _very_ fast and **cheap**.", "It is very fast and cheap.", true},
		{`A literal \*star\* here.`, "A literal *star* here.", true},
	} {
		if got := locate(tc.page, tc.quote); got != tc.want {
			t.Errorf("locate(%q, %q) = %v, want %v", tc.page, tc.quote, got, tc.want)
		}
	}
}

// "## Answer ##", an indented heading and sub-headings inside the section all
// kept the summarizer's answer in the report.
func TestDropAnswerSectionVariants(t *testing.T) {
	for _, report := range []string{
		"## Answer ##\n\nChoose Blocked.\n\n## Why\n\nkept",
		"  ## Answer\n\nChoose Blocked.\n\n## Why\n\nkept",
		"## Answer\n\n### Detail\n\nChoose Blocked.\n\n## Why\n\nkept",
	} {
		if got := dropAnswerSection(report, "Answer"); strings.Contains(got, "Blocked") || !strings.Contains(got, "kept") {
			t.Errorf("dropAnswerSection(%q) = %q", report, got)
		}
	}
}

// A checker that answered in the older list shape left its "verified" list in
// the report beside claims this pass had rejected for having no verdict.
func TestOlderVerdictListsDoNotOutliveTheCheck(t *testing.T) {
	findings, a := decisionFixture()
	fc := &agent.FactCheckResult{Verified: []agent.VerifiedClaim{{Claim: "c1", Verified: true}}}
	govern(a, fc, nil, findings)
	if len(fc.Verified) != 0 {
		t.Errorf("verified = %v, want none: no claim had a verdict", fc.Verified)
	}
}

// One genuine quote beside an invented one left the verdict standing on the
// invented passage.
func TestEveryQuotedPassageMustBeLocated(t *testing.T) {
	findings, a := decisionFixture()
	v := verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored.")
	v.Evidence = append(v.Evidence, agent.Evidence{Source: "1", Quote: "and it is free under 1 GB"})
	govern(a, &agent.FactCheckResult{Verdicts: []agent.Verdict{v}}, nil, findings)
	if a.Claims[0].Status == statusSupported {
		t.Error("a verdict with an invented passage stood on the genuine one")
	}
}

// The fallback report kept the analyzer's high confidence after a fact-check
// that did not run.
func TestFallbackAfterFailedCheckIsLowConfidence(t *testing.T) {
	findings, a := decisionFixture()
	a.Confidence = "high"
	govern(a, nil, errors.New("timeout"), findings)
	d := NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1)
	res, _ := d.partial(context.Background(), newTestPlan("quick", nil), findings, a, nil, "report writing failed", errors.New("500"))
	if res.Summary.Confidence != "low" || !strings.Contains(res.Summary.Report, englishLabels[noApprovalUnchecked]) {
		t.Errorf("fallback = %q, %q; want low confidence and no approval", res.Summary.Confidence, res.Summary.Report)
	}
}

// A quote copied with a decomposed accent (e + combining acute) is the same
// text as the page's precomposed one.
func TestLocateIgnoresUnicodeNormalForm(t *testing.T) {
	if !locate("Le caf\u00e9 est ouvert.", "Le cafe\u0301 est ouvert.") {
		t.Error("a quote differing only in normal form was not located")
	}
}

// Research sources are often PDFs and wikis: a sentence split by a hyphen at
// a line end, a citation marker inside it, a ligature character. Each lost a
// correct claim on a history question.
func TestLocateThroughPDFAndWikiArtifacts(t *testing.T) {
	for _, tc := range []struct{ page, quote string }{
		{"put aside  capital  re-\nserves as a cushion", "put aside capital reserves as a cushion"},
		{"derivatives markets.[\\[43\\]](https://w.example/x#cite_note-43)\n These markets", "derivatives markets. These markets"},
		{"markets.[43] These", "markets. These"},
		{"the \ufb01nancial system", "the financial system"},
		{"a well-\nknown case", "a well-known case"},
		{"due to the securitization of _subprime_ _mortgages_ into *mortgage-backed* *securities*", "due to the securitization of subprime mortgages into mortgage-backed securities"},
		{"1.  Reply false if term &lt; currentTerm (§5.1)", "1. Reply false if term < currentTerm (§5.1)"},
		{"| HP Split Systems  <br>(Ducted) | ≥ 8.1 HSPF2 |", "| HP Split Systems \n(Ducted) | ≥ 8.1 HSPF2 |"},
	} {
		if !locate(tc.page, tc.quote) {
			t.Errorf("locate(%q, %q) = false", tc.page, tc.quote)
		}
	}
	// The number is what the quote is evidence of: text whose digits the PDF
	// did not encode readably cannot vouch for one.
	if locate("more than \uf653\uf644 billion", "more than 180 billion") {
		t.Error("a number the page does not show was located")
	}
}

// Supported premises do not make a recommendation follow: the check judges
// the inference too, and a recommendation it does not find following, or
// does not judge, is blocked.
func TestRecommendationMustFollowFromItsClaims(t *testing.T) {
	quote := "Serverless is billed per GB-hour of data stored."
	for _, tc := range []struct {
		name string
		inf  []agent.Inference
		want bool
	}{
		{"follows", follows("r1"), true},
		{"does not follow", []agent.Inference{{ID: "r1", Follows: false, Reason: "billing says nothing about traffic"}}, false},
		{"not judged", nil, false},
		{"judged twice", append(follows("r1"), agent.Inference{ID: "r1", Follows: false}), false},
	} {
		findings, a := decisionFixture()
		a.Recommendations = a.Recommendations[:1]
		fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{verdict("c1", "supported", "1", quote)}, Inferences: tc.inf}
		govern(a, fc, nil, findings)
		if got := len(approvedUnits(a)) == 1; got != tc.want {
			t.Errorf("%s: approved = %v (%s), want %v", tc.name, got, a.Recommendations[0].Blocked, tc.want)
		}
	}
	findings, a := decisionFixture()
	fc := &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "r9", Follows: true}}}
	if unknown := govern(a, fc, nil, findings); !slices.Contains(unknown, "r9") {
		t.Errorf("unknown = %v, want the invented recommendation r9", unknown)
	}
}

// With one option approved and the other blocked, the answer read as a win
// for the one; the other is now named as not established, with the claim
// that failed.
func TestAnswerNamesTheOptionsNotEstablished(t *testing.T) {
	findings, a := decisionFixture()
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
		verdict("c2", "partial", "1", "save up to 55%"),
	}, Inferences: follows("r1", "r2", "r3")}
	govern(a, fc, nil, findings)
	got := renderAnswer(a)
	if !strings.Contains(got, "Not established in this run") ||
		!strings.Contains(got, "- **Node-based**: traffic is steady (partial: \u201cReserved nodes save 90%\u201d)") {
		t.Errorf("answer does not name the blocked option and its failed claim:\n%s", got)
	}
	govern(a, nil, errors.New("timeout"), findings)
	if got := renderAnswer(a); strings.Contains(got, "Not established") {
		t.Errorf("with no fact-check there is no per-option reason to give:\n%s", got)
	}
}

// Told the program writes the answer, the summarizer still restated it in a
// paragraph above its first heading.
func TestRestatedAnswerAboveTheFirstHeadingIsDropped(t *testing.T) {
	findings, a := decisionFixture()
	govern(a, &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
	}, Inferences: follows("r1")}, nil, findings)
	sum := withAnswer(&agent.Summary{Report: "Serverless is the pick for everyone.\n\n## How billing works\n\nx"}, a)
	if strings.Contains(sum.Report, "pick for everyone") || !strings.Contains(sum.Report, "## How billing works") {
		t.Errorf("report = %q, want the restatement dropped and the explanation kept", sum.Report)
	}
	if got := dropLeadingProse("no headings at all"); got != "no headings at all" {
		t.Errorf("a report with no heading lost its text: %q", got)
	}
}

// A revision may cite only claims that passed and must revise a blocked
// recommendation; the pass cannot add more than maxRevisions.
func TestAcceptRevisionsOnlyOnSupportedClaims(t *testing.T) {
	findings, a := decisionFixture()
	govern(a, &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
		verdict("c2", "partial", "1", "save up to 55%"),
	}, Inferences: follows("r1", "r2", "r3")}, nil, findings)
	added := acceptRevisions(a, &agent.Analysis{Recommendations: []agent.Recommendation{
		{Revises: "r2", Choose: "Node-based", When: "billing by the hour suits you", Claims: []string{"c1"}},
		{Revises: "r2", Choose: "Node-based", When: "you want 90% off", Claims: []string{"c2"}},
		{Revises: "r1", Choose: "Serverless", When: "again", Claims: []string{"c1"}},
		{Revises: "r2", Choose: "Node-based", When: "no claim"},
	}})
	if u, _ := unitByID(a, added[0]); len(added) != 1 || u.r.When != "billing by the hour suits you" {
		t.Errorf("accepted %v, want only the revision of a blocked recommendation on supported claims", added)
	}
	_, fresh := decisionFixture()
	govern(fresh, &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
	}}, nil, findings)
	var many []agent.Recommendation
	for range 10 {
		fresh.Recommendations = append(fresh.Recommendations, agent.Recommendation{Choose: "x", Blocked: "unsupported"})
		many = append(many, agent.Recommendation{Revises: fmt.Sprintf("r%d", len(fresh.Recommendations)), Choose: "x", When: "y", Claims: []string{"c1"}})
	}
	if got := acceptRevisions(fresh, &agent.Analysis{Recommendations: many}); len(got) != maxRevisions {
		t.Errorf("accepted %d revisions, want the cap of %d", len(got), maxRevisions)
	}
}

// An approved revision takes its original's place in the answer; a revision
// that was blocked too leaves the original named as not established.
func TestApprovedRevisionReplacesItsOriginal(t *testing.T) {
	findings, a := decisionFixture()
	a.Recommendations = a.Recommendations[:2]
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
		verdict("c2", "partial", "1", "save up to 55%"),
	}, Inferences: follows("r1", "r2")}
	govern(a, fc, nil, findings)
	added := acceptRevisions(a, &agent.Analysis{Recommendations: []agent.Recommendation{{Revises: "r2", Choose: "Node-based", When: "hourly billing suits you", Claims: []string{"c1"}}}})
	judgeRevisions(a, added, fc, &agent.FactCheckResult{Inferences: follows("r3")})
	got := renderAnswer(a)
	if !strings.Contains(got, "- **Node-based**: hourly billing suits you") || strings.Contains(got, "Not established") {
		t.Errorf("answer does not show the revision in place of the original:\n%s", got)
	}
	a.Recommendations[2].Blocked = "it does not follow from its claims: r"
	if got := renderAnswer(a); !strings.Contains(got, "Not established") || !strings.Contains(got, "traffic is steady") {
		t.Errorf("with the revision blocked the original should be named as not established:\n%s", got)
	}
}

// A Spanish report read "Not established in this run" and "partial" between
// Spanish sentences. The analysis gives the program's words in the
// question's language; one that could break the report's Markdown is not
// used, and a missing one falls back to English.
func TestAnswerUsesTheAnalysisLabels(t *testing.T) {
	findings, a := decisionFixture()
	a.Labels = map[string]string{"answer": "Respuesta", "not_established": "No establecido en esta ejecuci\u00f3n",
		"partial": "parcial", "does_not_follow": "## injected"}
	govern(a, &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Serverless is billed per GB-hour of data stored."),
		verdict("c2", "partial", "1", "save up to 55%"),
	}, Inferences: follows("r1", "r2", "r3")}, nil, findings)
	got := renderAnswer(a)
	for _, want := range []string{"## Respuesta\n", "No establecido en esta ejecuci\u00f3n:", "(parcial: \u201c"} {
		if !strings.Contains(got, want) {
			t.Errorf("answer lacks %q:\n%s", want, got)
		}
	}
	if label(a, "does_not_follow") != englishLabels["does_not_follow"] {
		t.Error("a label carrying Markdown was used")
	}
	sum := withAnswer(&agent.Summary{Report: "## Respuesta\n\nPython gana.\n\n## Por qu\u00e9\n\nx"}, a)
	if strings.Contains(sum.Report, "Python gana") {
		t.Errorf("the summarizer's localized answer section was kept:\n%s", sum.Report)
	}
}

// ---- checker eval cases (testdata/checker) --------------------------------

// checkerCase is a frozen analysis with the outcome a correct fact-check and
// governance should reach. make eval-replay runs the live checker on them;
// here they are only held to being well formed.
type checkerCase struct {
	Name            string
	About           string                 `json:"about"`
	Findings        []agent.Finding        `json:"findings"`
	Claims          []agent.Claim          `json:"claims"`
	Recommendations []agent.Recommendation `json:"recommendations"`
	Conclusions     []agent.Recommendation `json:"conclusions"`
	Expect          struct {
		Supported    []string `json:"supported"`
		NotSupported []string `json:"not_supported"`
		Approved     []string `json:"approved"`
		Blocked      []string `json:"blocked"`
	} `json:"expect"`
}

func loadCheckerCases(t *testing.T) []checkerCase {
	t.Helper()
	paths, _ := filepath.Glob("testdata/checker/*.json")
	if len(paths) == 0 {
		t.Fatal("no checker cases in testdata/checker")
	}
	var out []checkerCase
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var c checkerCase
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		c.Name = strings.TrimSuffix(filepath.Base(p), ".json")
		for i := range c.Findings {
			c.Findings[i].Status = "ok"
		}
		out = append(out, c)
	}
	return out
}

// A case whose expectation names a claim or recommendation it does not have,
// or cites a page it does not hold, would score nonsense.
func TestCheckerCasesAreWellFormed(t *testing.T) {
	for _, c := range loadCheckerCases(t) {
		pages, claims := map[string]bool{}, map[string]bool{}
		for _, f := range c.Findings {
			pages[f.URL] = true
		}
		for _, cl := range c.Claims {
			claims[cl.ID] = true
			for _, s := range cl.Sources {
				if !pages[s] {
					t.Errorf("%s: %s cites %s, not one of its pages", c.Name, cl.ID, s)
				}
			}
		}
		for _, id := range append(c.Expect.Supported, c.Expect.NotSupported...) {
			if !claims[id] {
				t.Errorf("%s: expectation names claim %s it does not have", c.Name, id)
			}
		}
		a := &agent.Analysis{Recommendations: c.Recommendations, Conclusions: c.Conclusions}
		for _, id := range append(c.Expect.Approved, c.Expect.Blocked...) {
			if _, ok := unitByID(a, id); !ok {
				t.Errorf("%s: expectation names %s, which it does not have", c.Name, id)
			}
		}
		if c.About == "" || len(c.Expect.Supported)+len(c.Expect.Approved) == 0 {
			t.Errorf("%s: a case needs what it tests and a positive expectation", c.Name)
		}
	}
}

// ---- conclusions: the answer of a question that asks for no choice --------

// conclusionFixture is an explanatory answer on one fetched page.
func conclusionFixture() ([]agent.Finding, *agent.Analysis) {
	page := "Raft elects a leader when a follower's election timeout expires. A candidate needs votes from a majority of servers."
	findings := []agent.Finding{{URL: "https://raft.example/paper", Status: "ok", Content: page}}
	a := &agent.Analysis{
		Answer: "UNCHECKED PROSE",
		Claims: []agent.Claim{
			{ID: "c1", Text: "A follower starts an election when its election timeout expires.", Sources: []string{"https://raft.example/paper"}},
			{ID: "c2", Text: "A candidate needs votes from a majority of servers.", Sources: []string{"https://raft.example/paper"}},
			{ID: "c3", Text: "Elections always finish within one round.", Sources: []string{"https://raft.example/paper"}},
		},
		Conclusions: []agent.Recommendation{
			{Statement: "An election starts on a follower's timeout and is won with a majority of votes.", Claims: []string{"c1", "c2"}},
			{Statement: "An election always settles within a single round.", Claims: []string{"c3"}},
		},
	}
	return findings, a
}

// An explanatory answer was unchecked prose while a choice was held to its
// premises: conclusions are governed the same way, by k IDs.
func TestConclusionsAreGovernedLikeRecommendations(t *testing.T) {
	findings, a := conclusionFixture()
	govern(a, &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Raft elects a leader when a follower's election timeout expires."),
		verdict("c2", "supported", "1", "A candidate needs votes from a majority of servers."),
		verdict("c3", "insufficient", "1", "A candidate needs votes"),
	}, Inferences: follows("k1", "k2")}, nil, findings)
	got := renderAnswer(a)
	if !strings.Contains(got, "- An election starts on a follower's timeout and is won with a majority of votes.") {
		t.Errorf("the approved conclusion is not the answer:\n%s", got)
	}
	if !strings.Contains(got, "Not established in this run") || !strings.Contains(got, "always settles") {
		t.Errorf("the blocked conclusion is not named as not established:\n%s", got)
	}
	if p := claimsToCheck(a); !strings.Contains(p, "k1: conclude: An election starts") || !strings.Contains(p, "premise c2:") {
		t.Errorf("the fact-check is not given the conclusion with its premises:\n%s", p)
	}
	if p := summarizePrompt("q", a, &agent.FactCheckResult{}, findings, true); strings.Contains(p, "UNCHECKED PROSE") {
		t.Error("the analyzer's prose reached the summarizer beside its checked conclusions")
	}
}

// An analysis with claims and no conclusion at all used to get the
// summarizer's unchecked answer; the answer now says nothing was concluded.
func TestNoConclusionStatedIsSaid(t *testing.T) {
	findings, a := conclusionFixture()
	a.Conclusions = nil
	govern(a, &agent.FactCheckResult{}, nil, findings)
	sum := withAnswer(&agent.Summary{Report: "## Answer\n\nRaft is simple.\n\n## How it works\n\nx"}, a)
	if strings.Contains(sum.Report, "Raft is simple") || !strings.Contains(sum.Report, englishLabels["no_conclusion"]) {
		t.Errorf("report = %q, want the no-conclusion answer in place of the summarizer's", sum.Report)
	}
}

// The repair pass revises a blocked conclusion as it does a recommendation,
// and only as a conclusion.
func TestRepairRevisesAConclusion(t *testing.T) {
	findings, a := conclusionFixture()
	fc := &agent.FactCheckResult{Verdicts: []agent.Verdict{
		verdict("c1", "supported", "1", "Raft elects a leader when a follower's election timeout expires."),
		verdict("c2", "supported", "1", "A candidate needs votes from a majority of servers."),
	}, Inferences: []agent.Inference{{ID: "k1", Follows: false, Reason: "says nothing of ties"}, {ID: "k2", Follows: true}}}
	govern(a, fc, nil, findings)
	added := acceptRevisions(a, &agent.Analysis{
		Conclusions:     []agent.Recommendation{{Revises: "k1", Statement: "A follower whose timeout expires stands for election.", Claims: []string{"c1"}}},
		Recommendations: []agent.Recommendation{{Revises: "k1", Choose: "x", When: "y", Claims: []string{"c1"}}},
	})
	if len(added) != 1 || added[0] != "k3" {
		t.Fatalf("added %v, want only the conclusion revising k1, as k3", added)
	}
	judgeRevisions(a, added, fc, &agent.FactCheckResult{Inferences: follows("k3")})
	got := renderAnswer(a)
	if !strings.Contains(got, "- A follower whose timeout expires stands for election.") || strings.Contains(got, "won with a majority") {
		t.Errorf("the revision does not replace the original conclusion:\n%s", got)
	}
}

// Claim IDs the analyzer wrote into a statement reached the answer:
// "... two deployment options (c1), and ... (c3)".
func TestAnswerStatementsCarryNoClaimIDs(t *testing.T) {
	for in, want := range map[string]string{
		"two deployment options (c1), and fixed needs (c3)": "two deployment options, and fixed needs",
		"up to 300 nodes (c9, c10); scaling (c9 and c11)":   "up to 300 nodes; scaling",
		"a figure (in c1 units) stays":                      "a figure (in c1 units) stays",
	} {
		if got := readerText(in); got != want {
			t.Errorf("readerText(%q) = %q, want %q", in, got, want)
		}
	}
}

// The page shows a claim's quote inside the page text the check located it
// in, so the sidecar carries that text and resolves each quote's source the
// way governance does: "#1" is the first finding, a URL matches canonically,
// and a page the run only saw in a search result has no stored text.
func TestMetaResolvesQuotesToStoredPages(t *testing.T) {
	res := testResult()
	res.Findings = []agent.Finding{
		{Title: "A", URL: "https://example.com/a", Content: "The cell reached 400 Wh/kg in tests.", Status: "ok"},
		{Title: "B", URL: "https://example.com/b", Content: "snippet", Status: "unverified"},
		// Hostless: its canonical URL is "", the key a source naming no page
		// looks up, so it must back nothing.
		{Title: "C", URL: "/", Content: "hostless text", Status: "ok"},
	}
	res.FactCheck = &agent.FactCheckResult{Verdicts: []agent.Verdict{
		{ID: "c1", Status: "supported", Evidence: []agent.Evidence{
			{Source: "#1", Quote: "reached 400 Wh/kg"},
			{Source: "https://EXAMPLE.com/a/", Quote: "an invented sentence"},
		}},
		{ID: "c2", Status: "supported", Evidence: []agent.Evidence{{Source: "https://example.com/b", Quote: "snippet"}}},
		{ID: "c3", Status: "supported", Evidence: []agent.Evidence{{Source: "//", Quote: "hostless text"}}},
	}}
	m := BuildMeta(res, nil, "quick", 0, 0)
	if m.Pages["https://example.com/a"] != res.Findings[0].Content || len(m.Pages) != 1 {
		t.Errorf("pages = %v, want only the fetched page's text", m.Pages)
	}
	want := []MetaPassage{
		{Verdict: 0, Page: "https://example.com/a", Quote: "reached 400 Wh/kg", Located: true},
		{Verdict: 0, Page: "https://example.com/a", Quote: "an invented sentence"},
		{Verdict: 1, Quote: "snippet"},
		{Verdict: 2, Quote: "hostless text"},
	}
	if !slices.Equal(m.Passages, want) {
		t.Errorf("passages =\n%+v\nwant\n%+v", m.Passages, want)
	}
	// The sidecar reports what governance concluded, so governance must not
	// locate the quote either.
	if quotesLocated(res.FactCheck.Verdicts[2].Evidence, res.Findings, fetchedPages(res.Findings)) {
		t.Error("governance located a quote whose source names no page in a hostless page")
	}
}

// The browser page reads the sidecar by its JSON keys: a renamed struct tag
// would blank a panel with no test failing. Every key the page reads is here.
func TestMetaKeepsTheKeysTheAuditPageReads(t *testing.T) {
	res := testResult()
	res.Findings = []agent.Finding{{Title: "A", URL: "https://example.com/a", Content: "The cell reached 400 Wh/kg.", Status: "ok"}}
	unit := agent.Recommendation{Choose: "x", When: "y", Statement: "s", Claims: []string{"c1"}, Blocked: "b", Failed: []string{"c1"}, Revises: "r1"}
	res.Analysis = &agent.Analysis{
		Interpretation:  "i",
		Claims:          []agent.Claim{{ID: "c1", Text: "t", Option: "o", Criterion: "c", Scope: "s", Sources: []string{"https://example.com/a"}, Status: "supported", Note: "n"}},
		Conclusions:     []agent.Recommendation{unit},
		Recommendations: []agent.Recommendation{unit},
	}
	res.FactCheck = &agent.FactCheckResult{Verdicts: []agent.Verdict{
		{ID: "c1", Status: "supported", Reason: "r", Evidence: []agent.Evidence{{Source: "#1", Quote: "reached 400 Wh/kg"}}},
	}}
	res.Error = "e"
	b, err := json.Marshal(BuildMeta(res, nil, "quick", 10, 1))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	unitKeys := []string{"statement", "choose", "when", "claims", "blocked", "failed", "revises"}
	paths := []string{"question", "sources", "tokens", "depth", "timestamp", "error", "pages",
		"analysis.interpretation", "fact_check.verdicts.0.id", "fact_check.verdicts.0.status", "fact_check.verdicts.0.reason",
		"passages.0.verdict", "passages.0.page", "passages.0.quote", "passages.0.located",
		"citations.0.url", "citations.0.title", "citations.0.status", "citations.0.cited"}
	for _, k := range []string{"id", "claim", "status", "note", "option", "criterion", "scope", "sources"} {
		paths = append(paths, "analysis.claims.0."+k)
	}
	for _, k := range unitKeys {
		paths = append(paths, "analysis.conclusions.0."+k, "analysis.recommendations.0."+k)
	}
	for _, p := range paths {
		var v any = doc
		for _, k := range strings.Split(p, ".") {
			switch n := v.(type) {
			case map[string]any:
				v = n[k]
			case []any:
				i, _ := strconv.Atoi(k)
				if i >= len(n) {
					v = nil
					break
				}
				v = n[i]
			default:
				v = nil
			}
		}
		if v == nil {
			t.Errorf("sidecar has no %s, which the audit page reads", p)
		}
	}
}

// A run whose search server is down said only "every search failed
// (unavailable)" and a transport error; it now says what to check.
func TestSearchFailureSaysWhatToCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := "http://" + ln.Addr().String()
	_ = ln.Close()
	_, searchErr := tools.NewSearXNGClient(addr, time.Second).Search(context.Background(), "q")
	d := &Driver{searchErr: searchErr}
	got := d.searchFailure(nil).Error()
	for _, want := range []string{"no search answered", "cannot reach SearXNG at " + addr, "SEARXNG_URL", "doctor"} {
		if !strings.Contains(got, want) {
			t.Errorf("run error %q does not mention %q", got, want)
		}
	}
	if strings.Contains(got, "dial tcp") {
		t.Errorf("run error still carries the raw transport error: %q", got)
	}
}

// A plan handed back with --plan was written or edited by hand. It cannot
// ask for a tier that does not exist, run with no sub-topics, or claim a
// budget larger than its tier and sub-topic count allow.
func TestReadPlanMakesAnEditedPlanSafeToRun(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      "{",
		"no question":   `{"depth":{"key":"quick"},"sub_topics":[{"name":"a"}]}`,
		"unknown tier":  `{"question":"q","depth":{"key":"deeep"},"sub_topics":[{"name":"a"}]}`,
		"no sub-topics": `{"question":"q","sub_topics":[{"name":"  "}]}`,
	} {
		if _, err := ReadPlan(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	long := strings.Repeat("long sub-topic name ", 4)
	p, err := ReadPlan(strings.NewReader(`{"question":"q","depth":{"key":"standard","max_sources":99},
		"max_sources":999,"sub_topics":[{"id":"x","name":"a","query":"qa"},{"id":"x","name":"` + long + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// A planner name runs past the brief's cap, and the fallback search
	// sends it: it must come back whole.
	if p.SubTopics[1].Name != strings.TrimSpace(long) {
		t.Errorf("name = %q, want it whole", p.SubTopics[1].Name)
	}
	if p.SubTopics[0].ID != "1" || p.SubTopics[1].ID != "2" || p.SubTopics[0].Query != "qa" {
		t.Errorf("sub-topics = %+v, want renumbered 1, 2 with their queries kept", p.SubTopics)
	}
	if p.Depth.MaxSources != 4 || p.MaxSources != 8 {
		t.Errorf("budget = tier %d, run %d; want the standard tier's 4 per sub-topic, 8 in all", p.Depth.MaxSources, p.MaxSources)
	}
}

// The browser's plan review shows the per-topic budget from copies of the
// tiers and the run cap, and orders follow-up rows by their ID prefix. The
// page cannot import them, so a change here must fail until the page follows.
func TestBrowserPageCopiesMatch(t *testing.T) {
	page, err := os.ReadFile("../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	tiers := make([]string, len(DepthModes))
	for i, m := range DepthModes {
		tiers[i] = fmt.Sprintf("%s: %d", m.Key, m.MaxSources)
	}
	for _, want := range []string{
		"const TIER_SOURCES = { " + strings.Join(tiers, ", ") + " }, RUN_SOURCES = " + strconv.Itoa(maxRunSources) + ";",
		`id.startsWith("` + followUpPrefix + `")`,
	} {
		if !bytes.Contains(page, []byte(want)) {
			t.Errorf("index.html does not have %s", want)
		}
	}
}
