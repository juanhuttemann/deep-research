package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/web"
)

// TestInitReportsCreatedFiles checks that `init` reports every artifact it
// creates (the config dir *and* the .env secrets file), rather than only the
// config dir.
func TestInitReportsCreatedFiles(t *testing.T) {
	cfgDir := t.TempDir()
	cwd := t.TempDir()

	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	cmd := New(func() (Deps, error) { return Deps{}, nil })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute init: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, cfgDir) {
		t.Errorf("output should mention the config dir %q; got:\n%s", cfgDir, out)
	}
	if !strings.Contains(out, ".env") {
		t.Errorf("output should report the .env file that was created; got:\n%s", out)
	}
	// The first run needs a key; init names the one place to get a free one.
	if !strings.Contains(out, "https://openrouter.ai/keys") {
		t.Errorf("init does not say where to get a key; got:\n%s", out)
	}
	env, err := os.ReadFile(filepath.Join(cwd, ".env"))
	if err != nil || !strings.Contains(string(env), "OPENAI_MODEL=openrouter/free") {
		t.Errorf(".env template does not default to the free model: %v\n%s", err, env)
	}
}

// A missing key used to print a warning and fall back to offline mode, so the
// reader got a RESEARCH COMPLETE card, three artifacts and a history record
// for a run that researched nothing. It is now an error that says how to fix
// it, and writes nothing.
func TestMissingKeyIsAHardError(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Assistant: func() (agent.Assistant, error) { return nil, errors.New("no API key (set OPENAI_API_KEY)") },
		Config:    config.Config{DataFile: filepath.Join(dir, "runs.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "why is the sky blue", "--silent", "--reports", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a run with no API key succeeded")
	}
	for _, want := range []string{"https://openrouter.ai/keys"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q: %v", want, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed run wrote files: %v", entries)
	}
}

// TestSourcesFlagAndDeprecatedDepthAlias: the flag budgets sources per
// sub-agent, so it is named --sources. --depth still works for scripts and
// docs written against the old name.
func TestSourcesFlagAndDeprecatedDepthAlias(t *testing.T) {
	for _, flag := range []string{"--sources", "--depth"} {
		var got int
		deps := Deps{
			Assistant: always(newRecording(&got)),
			Config:    config.Config{DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
		}
		cmd := New(func() (Deps, error) { return deps, nil })
		dir := t.TempDir()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs([]string{"-p", "q", "--silent", "--reports", dir, flag, "6"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if got != 6 {
			t.Errorf("%s 6 gave a per-sub-agent budget of %d", flag, got)
		}
	}
}

// recordingAssistant is the stub assistant plus a record of the source
// budget the CLI handed down.
type recordingAssistant struct {
	agent.Assistant
	sources *int
}

func newRecording(sources *int) *recordingAssistant {
	return &recordingAssistant{Assistant: stub{}, sources: sources}
}

func (r *recordingAssistant) SetSourceBudget(n int)         { *r.sources = n }
func (r *recordingAssistant) SkipSources(func(string) bool) {}

// A typo in --mode used to be accepted silently: anything that was not quick
// or deep mapped to standard, so "--mode deeep" ran a standard-depth study
// while the reader believed they had asked for a deep one.
func TestUnknownModeIsRejected(t *testing.T) {
	deps := Deps{
		Assistant: always(stub{}),
		Config:    config.Config{DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	var errBuf bytes.Buffer
	cmd.SetOut(io.Discard)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "q", "--silent", "--reports", t.TempDir(), "--mode", "deeep"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("--mode deeep was accepted")
	}
	if !strings.Contains(err.Error(), "deeep") || !strings.Contains(err.Error(), "deep") {
		t.Errorf("error should name the bad value and the valid ones: %v", err)
	}
}

// Every valid tier still runs.
func TestKnownModesAreAccepted(t *testing.T) {
	for _, mode := range []string{"quick", "standard", "deep"} {
		deps := Deps{
			Assistant: always(stub{}),
			Config:    config.Config{DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
		}
		cmd := New(func() (Deps, error) { return deps, nil })
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs([]string{"-p", "q", "--silent", "--reports", t.TempDir(), "--mode", mode})
		if err := cmd.Execute(); err != nil {
			t.Errorf("--mode %s: %v", mode, err)
		}
	}
}

// The whole-run deadline used to be 10x the per-model-call timeout, a number
// unrelated to the searching and scraping that dominate a run's wall clock.
// It is now its own setting, and a run must be given that budget.
func TestRunDeadlineComesFromRunTimeout(t *testing.T) {
	rec := &deadlineRecorder{Assistant: stub{}}
	deps := Deps{
		Assistant: always(rec),
		Config: config.Config{
			DataFile:   filepath.Join(t.TempDir(), "r.jsonl"),
			Config:     agent.Config{ModelCallTimeout: time.Second},
			RunTimeout: 25 * time.Minute,
		},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "q", "--silent", "--reports", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rec.budget < 20*time.Minute {
		t.Errorf("run deadline was %v, want roughly the configured 25m", rec.budget)
	}
}

// deadlineRecorder records how much time the run's context was given.
type deadlineRecorder struct {
	agent.Assistant
	budget time.Duration
}

func (d *deadlineRecorder) Plan(ctx context.Context, q string, subTopics int) ([]agent.SubTopic, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.budget = time.Until(dl)
	}
	return d.Assistant.Plan(ctx, q, subTopics)
}

// stub is an assistant that makes no network call, so a test can drive the
// whole pipeline through the CLI.
type stub struct{}

func (stub) Analyze(context.Context, string) (*agent.Analysis, error) {
	return &agent.Analysis{Answer: "stub", Confidence: "low"}, nil
}

func (stub) FactCheck(context.Context, string) (*agent.FactCheckResult, error) {
	return &agent.FactCheckResult{}, nil
}

func (stub) Summarize(context.Context, string) (*agent.Summary, error) {
	return &agent.Summary{Report: "stub report", Executive: "stub", Confidence: "low"}, nil
}

func (stub) Plan(_ context.Context, question string, _ int) ([]agent.SubTopic, error) {
	return []agent.SubTopic{{ID: "1", Name: question}}, nil
}

func (stub) ResearchDetail(_ context.Context, query string, _ []string) (*agent.ResearchDetail, error) {
	return &agent.ResearchDetail{Findings: []agent.Finding{{Query: query, Title: "stub", Content: "stub", Confidence: "low"}}}, nil
}

func (stub) SetSourceBudget(int)           {}
func (stub) SkipSources(func(string) bool) {}
func (stub) SetProgress(func(string))      {}
func (stub) TokensUsed() int               { return 0 }

func always(a agent.Assistant) func() (agent.Assistant, error) {
	return func() (agent.Assistant, error) { return a, nil }
}

// searchCounter is the stub assistant counting the searches it runs.
type searchCounter struct {
	stub
	searches *int
}

func (s searchCounter) ResearchDetail(ctx context.Context, q string, terms []string) (*agent.ResearchDetail, error) {
	*s.searches++
	return s.stub.ResearchDetail(ctx, q, terms)
}

// A prompt or model change could not be told apart from a change in what the
// web returned: every live run searched again and planned differently. A
// traced run is replayed on its recorded plan and searches, without
// searching, into its own directory, and is not saved to the history twice.
func TestTracedRunReplaysWithoutSearching(t *testing.T) {
	dir := t.TempDir()
	history := filepath.Join(dir, "r.jsonl")
	var searches int
	deps := Deps{
		Assistant: always(searchCounter{searches: &searches}),
		Config:    config.Config{DataFile: history, Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	run := func(args ...string) {
		t.Helper()
		cmd := New(func() (Deps, error) { return deps, nil })
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("-p", "q", "--silent", "--reports", dir, "--trace", "--mode", "quick")
	traces, _ := filepath.Glob(filepath.Join(dir, "*.trace.json"))
	if len(traces) != 1 || searches == 0 {
		t.Fatalf("traced run wrote %v after %d searches", traces, searches)
	}
	saved, _ := os.ReadFile(history)

	searches = 0
	run("--replay", traces[0], "--silent", "--reports", dir)
	if searches != 0 {
		t.Errorf("the replay searched %d times", searches)
	}
	if mds, _ := filepath.Glob(filepath.Join(dir, "replay", "*.md")); len(mds) != 1 {
		t.Errorf("replay wrote %v, want one report under replay/", mds)
	}
	if after, _ := os.ReadFile(history); len(after) != len(saved) {
		t.Error("the replay was saved to the history")
	}
}

// blockingPlan holds the plan call until the run is cancelled.
type blockingPlan struct {
	stub
	started chan struct{}
}

func (b blockingPlan) Plan(ctx context.Context, _ string, _ int) ([]agent.SubTopic, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A launch from the page must end on a done line like any --jsonl run, and
// the page's cancel button can press while the plan call is still out: that
// is a cancellation, which ui.Run reported as a failed plan.
func TestWebRunEndsOnDone(t *testing.T) {
	for _, status := range []string{"complete", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var a agent.Assistant = stub{}
			if status == "cancelled" {
				started := make(chan struct{})
				a = blockingPlan{started: started}
				go func() { <-started; cancel() }()
			}
			dir := t.TempDir()
			deps := Deps{Assistant: always(a), Config: config.Config{DataFile: filepath.Join(dir, "r.jsonl"),
				Config: agent.Config{ModelCallTimeout: time.Second}}}
			var out bytes.Buffer
			// A question that starts with a dash is still the question.
			run := webRun(func() (Deps, error) { return deps, nil }, dir, io.Discard)
			run(ctx, web.Request{Question: "-why is the sky blue", Mode: "quick"}, &out)
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var done struct{ Type, Status, Detail string }
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil || done.Type != "done" {
				t.Fatalf("last line is not a done event: %q", lines[len(lines)-1])
			}
			if done.Status != status {
				t.Errorf("done = %+v, want %s", done, status)
			}
		})
	}
}
