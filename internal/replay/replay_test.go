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

func (l *live) Plan(context.Context, string, int) (agent.Outline, error) {
	return agent.Outline{SubTopics: []agent.SubTopic{{ID: "1", Name: "Alpha", Query: "alpha query"}}}, nil
}
func (l *live) ResearchDetail(_ context.Context, q string, _ []string) (*agent.ResearchDetail, error) {
	l.searches++
	if q == "broken" {
		return nil, errors.New("engine down")
	}
	return &agent.ResearchDetail{Findings: []agent.Finding{{Query: q, URL: "https://a.example", Content: "page text"}}}, nil
}
func (l *live) FollowUps(context.Context, string) ([]agent.FollowUpQuery, error) {
	return []agent.FollowUpQuery{{Query: "the live model's own follow-up"}}, nil
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
	if plan, _ := p.Plan(ctx, "ignored", 9); len(plan.SubTopics) != 1 || plan.SubTopics[0].Query != "alpha query" {
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

// A live follow-up call words its own searches, which the recorded run never
// made; serving the recorded ones keeps the replay on the same evidence. A
// trace from before the follow-up call holds them in its first analysis, and
// a query the recorded run never searched is not served from either.
func TestReplaySearchesTheRecordedFollowUps(t *testing.T) {
	searched := []Search{{Query: "recorded follow-up"}}
	for name, phases := range map[string][]Phase{
		"follow-up call": {{Name: "follow-ups", Output: json.RawMessage(
			`[{"name":"Settles it","query":"recorded follow-up"},{"query":"never searched"}]`)}},
		"older trace": {{Name: "analyze", Output: json.RawMessage(
			`{"follow_up":["recorded follow-up","never searched"],"follow_up_names":{"recorded follow-up":"Settles it"}}`)}},
	} {
		p := Play(&live{}, &Trace{Question: "q", Plan: []agent.SubTopic{{ID: "1"}}, Searches: searched, Phases: phases})
		qs, err := p.FollowUps(context.Background(), "p")
		if err != nil || len(qs) != 1 || qs[0] != (agent.FollowUpQuery{Name: "Settles it", Query: "recorded follow-up"}) {
			t.Errorf("%s: follow-ups = %+v (%v), want the one recorded and searched", name, qs, err)
		}
	}
}

// The Driver trims a follow-up and clips it to the search length before
// searching, so the recorded search is not always the query the call gave:
// matched exactly, a long follow-up was dropped from the replay and its
// evidence with it. A planned search that only begins the same words is not
// taken for one.
func TestReplayFindsFollowUpsTheDriverClipped(t *testing.T) {
	long := "go 1.23 range over func iterator benchmark versus a hand-written loop on amd64 with inlining and escape analysis"
	clipped := "go 1.23 range over func iterator benchmark versus a hand-written loop on amd64 with inlining and"
	tr := &Trace{Question: "q", Plan: []agent.SubTopic{{ID: "1"}},
		Searches: []Search{{Query: clipped}, {Query: "spaced follow-up"}, {Query: "go iterators", Terms: []string{"go", "iter"}}},
		Phases: []Phase{{Name: "follow-ups", Output: json.RawMessage(
			`[{"query":"` + long + `"},{"query":"  spaced follow-up "},{"query":"go iterators in depth"}]`)}}}
	qs, _ := Play(&live{}, tr).FollowUps(context.Background(), "p")
	if len(qs) != 2 || qs[0].Query != long || strings.TrimSpace(qs[1].Query) != "spaced follow-up" {
		t.Errorf("follow-ups = %+v, want the clipped and the spaced one, not the planned search's prefix", qs)
	}
}

// The recorder keeps the follow-up call, so a replay of the trace searches
// what the run searched.
func TestRecorderKeepsTheFollowUps(t *testing.T) {
	r := Record(&live{}, "q", "quick", 0)
	if _, err := r.FollowUps(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if got := recordedFollowUps(&r.trace); len(got) != 1 || got[0].Query != "the live model's own follow-up" {
		t.Errorf("recorded follow-ups = %+v", got)
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

// scopingLive is live with a search that can be confined to a domain list.
type scopingLive struct {
	live
	scoped []string
}

func (s *scopingLive) CheckDomains(_ context.Context, d []string) ([]agent.DomainCheck, error) {
	return []agent.DomainCheck{{Domain: d[0], Status: "ok"}}, nil
}
func (s *scopingLive) ScopeTo(d []string) { s.scoped = d }

// The wrappers embed an interface, which does not promote the scoping
// methods; without forwarding, a recorded or resumed run would refuse its
// --domains. A replay searches nothing and is not a scoper at all.
func TestWrappersForwardScopingButAPlayerHasNone(t *testing.T) {
	ctx := context.Background()
	inner := &scopingLive{}
	for name, a := range map[string]agent.Assistant{
		"recorder": Record(inner, "q", "quick", 0),
		"resumer":  Resume(inner, &Trace{}),
	} {
		sc, ok := a.(agent.SourceScoper)
		if !ok {
			t.Fatalf("%s is not a SourceScoper", name)
		}
		if got, err := sc.CheckDomains(ctx, []string{"go.dev"}); err != nil || got[0].Domain != "go.dev" {
			t.Errorf("%s: CheckDomains = %v, %v", name, got, err)
		}
		inner.scoped = nil
		sc.ScopeTo([]string{"go.dev"})
		if len(inner.scoped) != 1 {
			t.Errorf("%s: ScopeTo did not reach the live assistant", name)
		}
	}
	if _, err := Record(&live{}, "q", "quick", 0).CheckDomains(ctx, []string{"go.dev"}); !errors.Is(err, agent.ErrNoScopedSearch) {
		t.Errorf("recorder over an unscoped search: err = %v, want ErrNoScopedSearch", err)
	}
	if _, ok := any(Play(inner, &Trace{})).(agent.SourceScoper); ok {
		t.Error("a Player claims to scope a search it never makes")
	}
}

// The trace keeps the question the run planned and searched with, not the one
// typed: its fallback queries are built from it, and a question typed over
// two lines made every one of them a query the replay had never seen.
func TestTraceKeepsThePlannedQuestion(t *testing.T) {
	rec := Record(&live{}, "cloud\nstorage", "quick", 0)
	_, _ = rec.Plan(context.Background(), "cloud storage", 3)
	if rec.trace.Question != "cloud storage" {
		t.Errorf("trace question = %q, want the planned %q", rec.trace.Question, "cloud storage")
	}
}
