package perf

import (
	"context"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
	"github.com/juanhuttemann/deep-research/internal/ui"
)

// stubAssistant answers every phase from the captured run, so a whole
// pipeline can be measured without a model or a network. What is left is the
// tool's own work: fanning the sub-agents out, merging and excerpting the
// sources, composing every prompt, governing the analysis by the fact-check,
// and exporting. Model answers come back as structs, so their parsing (see
// the agent package's benchmarks) is not in it.
type stubAssistant struct {
	findings []agent.Finding
	analysis *agent.Analysis
	check    *agent.FactCheckResult
	summary  *agent.Summary
	budget   int
	progress func(string)
	skip     func(string) bool
	domains  []string
	// tokens is written by the phase methods, which the driver may call from
	// its parallel sub-agent goroutines, so it is atomic like the real
	// assistant's counter.
	tokens atomic.Int64
}

func (s *stubAssistant) Plan(context.Context, string, int) (agent.Outline, error) {
	subs := make([]agent.SubTopic, 0, 6)
	for _, t := range []struct{ id, name, query string }{
		{"s1", "Iterator signatures", "range over func iterator signature yield"},
		{"s2", "Compiler desugaring", "range over func compiler desugaring loop"},
		{"s3", "Performance", "range over func benchmark inlining cost"},
		{"s4", "iter package", "iter package Pull Pull2 pull iterator"},
		{"s5", "GOEXPERIMENT", "GOEXPERIMENT rangefunc experiment flag"},
		{"s6", "Language spec", "Go 1.23 language spec range clause"},
	} {
		subs = append(subs, agent.SubTopic{ID: t.id, Name: t.name, Query: t.query,
			Terms: []string{"iterator", "yield", "range", "loop", "spec"}})
	}
	return agent.Outline{SubTopics: subs}, nil
}

func (s *stubAssistant) ResearchDetail(context.Context, string, []string) (*agent.ResearchDetail, error) {
	out := &agent.ResearchDetail{Findings: s.findings}
	for _, f := range s.findings {
		out.Signals = append(out.Signals, agent.SourceSignal{URL: f.URL, Status: "ok", Domain: tools.DomainOf(f.URL)})
	}
	return out, nil
}

func (s *stubAssistant) FollowUps(context.Context, string) ([]agent.FollowUpQuery, error) {
	return nil, nil
}

func (s *stubAssistant) Analyze(context.Context, string) (*agent.Analysis, error) {
	s.tokens.Add(24000)
	return s.analysis, nil
}

func (s *stubAssistant) FactCheck(context.Context, string) (*agent.FactCheckResult, error) {
	s.tokens.Add(26000)
	return s.check, nil
}

func (s *stubAssistant) Summarize(context.Context, string) (*agent.Summary, error) {
	s.tokens.Add(14000)
	return s.summary, nil
}

func (s *stubAssistant) SetSourceBudget(n int)              { s.budget = n }
func (s *stubAssistant) SkipSources(skip func(string) bool) { s.skip = skip }
func (s *stubAssistant) SetProgress(logf func(string))      { s.progress = logf }
func (s *stubAssistant) TokensUsed() int                    { return int(s.tokens.Load()) }
func (s *stubAssistant) ScopeTo(domains []string)           { s.domains = domains }
func (s *stubAssistant) CheckDomains(context.Context, []string) ([]agent.DomainCheck, error) {
	return nil, nil
}

// newStub builds the assistant over the captured run's data.
func newStub(tb testing.TB) *stubAssistant {
	tb.Helper()
	m, err := ui.LoadMeta(filepath.Join("..", "testdata", "perf", "report.json"))
	if err != nil {
		tb.Fatal(err)
	}
	s := &stubAssistant{analysis: m.Analysis, check: m.FactCheck}
	s.summary = &agent.Summary{Report: m.Report, Executive: m.ExecutiveSummary, Confidence: m.Confidence}
	for _, c := range m.Citations {
		s.findings = append(s.findings, agent.Finding{
			Query: c.Query, Title: c.Title, URL: c.URL,
			Content: m.Pages[c.URL], Confidence: c.Confidence, Status: c.Status,
		})
	}
	return s
}

// BenchmarkPipelineRun is one whole run with the network taken out: every
// model answer is the captured one, so what is measured is the pipeline's
// own CPU and garbage.
func BenchmarkPipelineRun(b *testing.B) {
	s := newStub(b)
	b.ReportAllocs()
	for b.Loop() {
		res, err := ui.Run(context.Background(), ui.Options{
			Question:        "How do Go 1.23 range-over-func iterators work?",
			Assistant:       s,
			DepthMode:       "standard",
			SourcesPerTopic: 5,
			Parallelism:     3,
			Quiet:           true,
			NoColor:         true,
			OutDir:          b.TempDir(),
			Stdout:          io.Discard,
			Stderr:          io.Discard,
		})
		if err != nil {
			b.Fatal(err)
		}
		if res.Report == nil || len(res.Report.Findings) == 0 {
			b.Fatal("no report")
		}
	}
}
