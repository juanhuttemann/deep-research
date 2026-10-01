package replay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// live is a minimal assistant: it counts searches and answers every model
// phase with a fixed value.
type live struct{ searches int }

func (l *live) Plan(context.Context, string, int) ([]agent.SubTopic, error) {
	return []agent.SubTopic{{ID: "1", Name: "Alpha", Query: "alpha query"}}, nil
}
func (l *live) ResearchDetail(_ context.Context, q string, _ []string) (*agent.ResearchDetail, error) {
	l.searches++
	if q == "broken" {
		return nil, errors.New("engine down")
	}
	return &agent.ResearchDetail{Findings: []agent.Finding{{Query: q, URL: "https://a.example", Content: "page text"}}}, nil
}
func (l *live) Analyze(context.Context, string) (*agent.Analysis, error) {
	return &agent.Analysis{Answer: "live answer", FollowUp: []string{"the live model's own follow-up"}}, nil
}
func (l *live) FactCheck(context.Context, string) (*agent.FactCheckResult, error) {
	return &agent.FactCheckResult{}, nil
}
func (l *live) Summarize(context.Context, string) (*agent.Summary, error) {
	return &agent.Summary{Report: "r"}, nil
}
func (l *live) SetSourceBudget(int)           {}
func (l *live) SkipSources(func(string) bool) {}
func (l *live) SetProgress(func(string))      {}
func (l *live) TokensUsed() int               { return 0 }

func TestReplayServesTheRecordedEvidenceAndCallsTheModelLive(t *testing.T) {
	ctx := context.Background()
	rec := Record(&live{}, "q", "deep", 4)
	_, _ = rec.Plan(ctx, "q", 4)
	_, _ = rec.ResearchDetail(ctx, "alpha query", []string{"alpha"})
	_, _ = rec.ResearchDetail(ctx, "broken", nil)
	_, _ = rec.Analyze(ctx, "analyze prompt")
	path := filepath.Join(t.TempDir(), "run.trace.json")
	if err := rec.Write(path); err != nil {
		t.Fatal(err)
	}

	tr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Mode != "deep" || tr.Sources != 4 || len(tr.Phases) != 1 || tr.Phases[0].Prompt != "analyze prompt" {
		t.Errorf("trace = %+v", tr)
	}
	fresh := &live{}
	p := Play(fresh, tr)
	if plan, _ := p.Plan(ctx, "ignored", 9); len(plan) != 1 || plan[0].Query != "alpha query" {
		t.Errorf("plan = %+v, want the recorded one", plan)
	}
	det, err := p.ResearchDetail(ctx, "alpha query", nil)
	if err != nil || det.Findings[0].Content != "page text" {
		t.Errorf("recorded search = %+v, %v", det, err)
	}
	if _, err := p.ResearchDetail(ctx, "broken", nil); err == nil {
		t.Error("a search that failed in the recorded run succeeded in the replay")
	}
	if _, err := p.ResearchDetail(ctx, "a new follow-up", nil); err == nil {
		t.Error("a query the recorded run never issued was answered")
	}
	if fresh.searches != 0 {
		t.Errorf("the replay searched live %d times", fresh.searches)
	}
	if a, _ := p.Analyze(ctx, "x"); a.Answer != "live answer" {
		t.Errorf("analyze = %+v, want the live model's", a)
	}
}

// The replayed analysis words its own follow-ups, which the recorded run never
// searched; searching the recorded ones keeps the replay on the same evidence.
func TestReplaySearchesTheRecordedFollowUps(t *testing.T) {
	tr := &Trace{Question: "q", Plan: []agent.SubTopic{{ID: "1"}},
		Phases: []Phase{{Name: "analyze", Output: json.RawMessage(`{"follow_up":["recorded follow-up"]}`)}}}
	p := Play(&live{}, tr)
	first, _ := p.Analyze(context.Background(), "p")
	if len(first.FollowUp) != 1 || first.FollowUp[0] != "recorded follow-up" {
		t.Errorf("first analysis follow-ups = %v, want the recorded run's", first.FollowUp)
	}
	second, _ := p.Analyze(context.Background(), "p")
	if second.FollowUp[0] != "the live model's own follow-up" {
		t.Errorf("second analysis follow-ups = %v, want its own: only the first is replaced", second.FollowUp)
	}
}

// A page that several searches returned was stored once per search, which
// made the page text most of a trace several times over. Text is stored once
// per page; a second, different text for the same URL (the snippet one time,
// the scraped page another) is kept apart rather than merged into the first.
func TestTraceStoresEachPageTextOnce(t *testing.T) {
	page := "Pricing page. " + strings.Repeat("pricing table ", 500)
	found := func(content string) *agent.ResearchDetail {
		return &agent.ResearchDetail{Findings: []agent.Finding{{URL: "https://a.example/pricing", Content: content}}}
	}
	rec := Record(&live{}, "q", "standard", 0)
	rec.trace.Plan = []agent.SubTopic{{ID: "1"}}
	rec.trace.Searches = []Search{
		{Query: "one", Result: found(page)}, {Query: "two", Result: found(page)}, {Query: "three", Result: found("a snippet")},
	}
	path := filepath.Join(t.TempDir(), "run.trace.json")
	if err := rec.Write(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "Pricing page."); n != 1 {
		t.Errorf("the page text is in the trace %d times, want once", n)
	}
	if rec.trace.Searches[0].Result.Findings[0].Content != page {
		t.Error("writing the trace emptied the run's own findings")
	}
	tr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{page, page, "a snippet"} {
		if got := tr.Searches[i].Result.Findings[0].Content; got != want {
			t.Errorf("search %d restored %d bytes, want %d", i, len(got), len(want))
		}
	}
}

// The Driver edits the analysis it gets back; a trace that kept a pointer
// recorded those edits as the model's own output.
func TestTraceRecordsThePhaseOutputAsReturned(t *testing.T) {
	rec := Record(&live{}, "q", "standard", 0)
	rec.trace.Plan = []agent.SubTopic{{ID: "1"}}
	a, _ := rec.Analyze(context.Background(), "p")
	a.Answer = "edited after the phase returned"
	path := filepath.Join(t.TempDir(), "run.trace.json")
	if err := rec.Write(path); err != nil {
		t.Fatal(err)
	}
	tr, _ := Load(path)
	var got agent.Analysis
	if err := json.Unmarshal(tr.Phases[0].Output, &got); err != nil || got.Answer != "live answer" {
		t.Errorf("recorded answer = %q (%v), want the model's", got.Answer, err)
	}
}

// Sub-topics had no JSON tags, so traces spell their keys "ID" and "Name".
// The tags made them lowercase; a trace written before must still load.
func TestTraceWithCapitalizedSubTopicKeysLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.trace.json")
	old := `{"version":1,"question":"q","plan":[{"ID":"1","Name":"Alpha","Query":"alpha q","Terms":["a"]}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Plan[0]; got.ID != "1" || got.Name != "Alpha" || got.Query != "alpha q" || len(got.Terms) != 1 {
		t.Errorf("plan = %+v, want the capitalized keys read", got)
	}
}

// A run killed mid-way lost everything it had searched: the trace was written
// once, at the end. A checkpoint is saved after each step from the plan on,
// and a search cut short by a cancel, which returns what it had fetched and
// no error, is saved as unfinished so a resume does it again.
func TestCheckpointIsSavedAsTheRunGoes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.partial.json")
	rec := Record(&live{}, "q", "standard", 0)
	rec.Checkpoint(path, func(err error) { t.Errorf("save failed: %v", err) })
	if _, err := rec.ResearchDetail(context.Background(), "before the plan", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a checkpoint with no plan was saved; there is nothing to resume yet")
	}
	rec.SetPlan([]agent.SubTopic{{ID: "1", Name: "Alpha"}}, "deep", 5)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = rec.ResearchDetail(cancelled, "cut short", nil)

	tr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Mode != "deep" || tr.Sources != 5 || len(tr.Searches) != 2 {
		t.Fatalf("checkpoint = %+v, want the plan at deep/5 and both searches", tr)
	}
	if tr.Searches[0].Error != "" || tr.Searches[0].Result.Findings[0].Content != "page text" {
		t.Errorf("the finished search was not kept whole: %+v", tr.Searches[0])
	}
	if tr.Searches[1].Error == "" {
		t.Error("a search cut short by a cancel was saved as finished")
	}
	if err := rec.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("Discard left the checkpoint")
	}
}

// A resume serves what the interrupted run finished and searches the rest:
// the searches it never made and the ones that failed, often the failure
// being recovered from.
func TestResumeSearchesOnlyWhatIsMissing(t *testing.T) {
	l := &live{}
	r := Resume(l, &Trace{Searches: []Search{
		{Query: "done", Result: &agent.ResearchDetail{Findings: []agent.Finding{{URL: "https://kept.example"}}}},
		{Query: "failed", Error: "search server down"},
	}})
	got, err := r.ResearchDetail(context.Background(), "done", nil)
	if err != nil || l.searches != 0 || got.Findings[0].URL != "https://kept.example" {
		t.Errorf("a finished search was not served from the checkpoint: %v, %d live searches", err, l.searches)
	}
	for _, q := range []string{"failed", "never made"} {
		if _, err := r.ResearchDetail(context.Background(), q, nil); err != nil {
			t.Fatal(err)
		}
	}
	if l.searches != 2 {
		t.Errorf("live searches = %d, want the failed one and the one never made", l.searches)
	}
}

// A resumed run recorded from nothing: its first save, the plan, wrote over
// the checkpoint, and a second stop lost every search the first run had
// made. Seeded, the checkpoint keeps them, and a search the resume serves
// again is not recorded twice.
func TestResumedRecordingKeepsTheSavedSearches(t *testing.T) {
	saved := &Trace{Question: "q", Mode: "deep", Plan: []agent.SubTopic{{ID: "1", Name: "Alpha"}}, Searches: []Search{
		{Query: "done", Result: &agent.ResearchDetail{Findings: []agent.Finding{{URL: "https://kept.example", Content: "kept"}}}},
		{Query: "failed", Error: "down"},
	}}
	path := filepath.Join(t.TempDir(), "q.partial.json")
	l := &live{}
	rec := Record(Resume(l, saved), "q", "standard", 0)
	rec.Seed(saved)
	rec.Checkpoint(path, func(err error) { t.Errorf("save failed: %v", err) })
	rec.SetPlan(saved.Plan, "deep", 0)
	if _, err := rec.ResearchDetail(context.Background(), "done", nil); err != nil {
		t.Fatal(err)
	}
	tr, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Searches) != 1 || tr.Searches[0].Query != "done" || tr.Searches[0].Result.Findings[0].Content != "kept" {
		t.Errorf("checkpoint searches = %+v, want the saved one once, the failed one left to redo", tr.Searches)
	}
	if l.searches != 0 {
		t.Errorf("the saved search was searched again live %d times", l.searches)
	}
}
