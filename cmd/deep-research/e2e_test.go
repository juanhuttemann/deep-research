//go:build e2e

package main

// End-to-end suite: the built binary against the real provider and search.
// It spends model requests and depends on public services, so it sits behind
// the e2e build tag and runs only through `make e2e`, never in `make verify`
// or CI. It pins the --jsonl contract a calling program depends on, decoded
// into its own struct so a renamed JSON field fails here and not in a caller.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// event is the part of a --jsonl line a caller reads.
type event struct {
	Type      string   `json:"type"`
	Phase     string   `json:"phase"`
	Status    string   `json:"status"`
	Detail    string   `json:"detail"`
	Transient bool     `json:"transient"`
	Artifacts []string `json:"artifacts"`
}

// runCLI builds the binary and runs it in dir, so neither the repo's .env nor
// the real history is touched. env entries override the inherited ones.
func runCLI(t *testing.T, dir string, env []string, args ...string) (events []event, stderr string, code int) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "deep-research")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEEP_RESEARCH_DATA_FILE="+filepath.Join(dir, "runs.jsonl"))
	cmd.Env = append(cmd.Env, env...)
	var stdout, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &errOut
	var exit *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, ln := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var e event
		if err := json.Unmarshal([]byte(ln), &e); err != nil || e.Type == "" {
			t.Fatalf("stdout line is not an event: %q\nstderr:\n%s", ln, errOut.String())
		}
		events = append(events, e)
	}
	return events, errOut.String(), code
}

func TestE2EResearchRun(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set")
	}
	dir := t.TempDir()
	events, stderr, code := runCLI(t, dir, nil,
		"-p", "What is SearXNG?", "--mode", "quick", "--jsonl", "--reports", filepath.Join(dir, "reports"))
	done := events[len(events)-1]
	t.Logf("final done: status=%s artifacts=%v", done.Status, done.Artifacts)
	if done.Type != "done" {
		t.Fatalf("last event is %q, not done\nstderr:\n%s", done.Type, stderr)
	}
	// A search backend that is down or refusing (public instances do,
	// routinely) is the environment failing, not the code.
	if done.Status == "failed" && strings.Contains(done.Detail, "every search failed") {
		t.Skipf("no search answered; check SEARXNG_URL: %s", done.Detail)
	}
	if done.Status != "complete" || code != 0 {
		t.Fatalf("done = %+v, exit %d\nstderr:\n%s", done, code, stderr)
	}
	if !slices.ContainsFunc(events, func(e event) bool { return e.Type == "info" && e.Transient }) {
		t.Error("no transient status line: a watcher cannot tell a slow model call from a hung one")
	}
	if len(done.Artifacts) != 3 {
		t.Errorf("artifacts = %v, want .md, .pdf and .json", done.Artifacts)
	}
	for _, p := range done.Artifacts {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("artifact %s missing or empty: %v", p, err)
		}
	}
	history, err := os.ReadFile(filepath.Join(dir, "runs.jsonl"))
	if n := bytes.Count(history, []byte("\n")); err != nil || n != 1 {
		t.Errorf("history has %d records (%v), want 1", n, err)
	}
}

// A rejected key needs no valid key to test, and a 401 counts against no cap.
func TestE2ERejectedKey(t *testing.T) {
	dir := t.TempDir()
	events, stderr, code := runCLI(t, dir,
		[]string{"OPENAI_BASE_URL=https://openrouter.ai/api/v1", "OPENAI_API_KEY=sk-or-v1-invalid"},
		"-p", "q", "--mode", "quick", "--jsonl", "--reports", dir)
	done := events[len(events)-1]
	t.Logf("final done: status=%s artifacts=%v", done.Status, done.Artifacts)
	if done.Type != "done" || done.Status != "failed" || !strings.Contains(done.Detail, "OPENAI_API_KEY") {
		t.Errorf("last event = %+v, want done/failed naming OPENAI_API_KEY\nstderr:\n%s", done, stderr)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}
