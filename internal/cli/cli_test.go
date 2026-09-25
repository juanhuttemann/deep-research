package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
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
}

// TestRunFallsBackToOfflineWhenAssistantUnavailable exercises the fallback the
// README promises: when the online assistant cannot be built, the run warns
// and completes offline instead of failing.
func TestRunFallsBackToOfflineWhenAssistantUnavailable(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Assistant: func() (agent.Assistant, error) { return nil, errors.New("no API key") },
		Raw:       agent.Local(),
		Config:    config.Config{DataFile: filepath.Join(dir, "runs.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"run", "why is the sky blue", "--silent", "--reports", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run should have fallen back to offline, got: %v", err)
	}
	if !strings.Contains(errBuf.String(), "falling back to offline") {
		t.Errorf("no fallback warning on stderr; got:\n%s", errBuf.String())
	}
}

// TestSourcesFlagAndDeprecatedDepthAlias: the flag budgets sources per
// sub-agent, so it is named --sources. --depth still works for scripts and
// docs written against the old name.
func TestSourcesFlagAndDeprecatedDepthAlias(t *testing.T) {
	for _, flag := range []string{"--sources", "--depth"} {
		var got int
		deps := Deps{
			Raw:    newRecording(&got),
			Config: config.Config{Offline: true, DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
		}
		cmd := New(func() (Deps, error) { return deps, nil })
		dir := t.TempDir()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs([]string{"run", "q", "--silent", "--reports", dir, flag, "6"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if got != 6 {
			t.Errorf("%s 6 gave a per-sub-agent budget of %d", flag, got)
		}
	}
}

// recordingAssistant is the offline assistant plus a record of the source
// budget the CLI handed down.
type recordingAssistant struct {
	agent.Assistant
	sources *int
}

func newRecording(sources *int) *recordingAssistant {
	return &recordingAssistant{Assistant: agent.Local(), sources: sources}
}

func (r *recordingAssistant) SetSourceBudget(n int) { *r.sources = n }

// A typo in --mode used to be accepted silently: anything that was not quick
// or deep mapped to standard, so "--mode deeep" ran a standard-depth study
// while the reader believed they had asked for a deep one.
func TestUnknownModeIsRejected(t *testing.T) {
	deps := Deps{
		Raw:    agent.Local(),
		Config: config.Config{Offline: true, DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	var errBuf bytes.Buffer
	cmd.SetOut(io.Discard)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"run", "q", "--silent", "--reports", t.TempDir(), "--mode", "deeep"})

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
			Raw:    agent.Local(),
			Config: config.Config{Offline: true, DataFile: filepath.Join(t.TempDir(), "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
		}
		cmd := New(func() (Deps, error) { return deps, nil })
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs([]string{"run", "q", "--silent", "--reports", t.TempDir(), "--mode", mode})
		if err := cmd.Execute(); err != nil {
			t.Errorf("--mode %s: %v", mode, err)
		}
	}
}

// The whole-run deadline used to be 10x the per-model-call timeout, a number
// unrelated to the searching and scraping that dominate a run's wall clock.
// It is now its own setting, and a run must be given that budget.
func TestRunDeadlineComesFromRunTimeout(t *testing.T) {
	rec := &deadlineRecorder{Assistant: agent.Local()}
	deps := Deps{
		Raw: rec,
		Config: config.Config{
			Offline:    true,
			DataFile:   filepath.Join(t.TempDir(), "r.jsonl"),
			Config:     agent.Config{ModelCallTimeout: time.Second},
			RunTimeout: 25 * time.Minute,
		},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"run", "q", "--silent", "--reports", t.TempDir()})
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
