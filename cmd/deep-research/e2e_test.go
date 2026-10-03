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

// binary builds the CLI into a temporary directory.
func binary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "deep-research")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// runCLI builds the binary and runs it in dir, so neither the repo's .env nor
// the real history is touched. env entries override the inherited ones.
func runCLI(t *testing.T, dir string, env []string, args ...string) (events []event, stderr string, code int) {
	t.Helper()
	bin := binary(t)
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

// A follow-up is answered from the run's saved pages, which the model reads
// through tools: the configured model has to support tool calls. The page
// holds a fact the report does not and no model could know, so an answer
// that states it read the page.
func TestE2EAskAboutTheRun(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set")
	}
	dir := t.TempDir()
	const page = "https://example.org/zorblat-9"
	sidecar, err := json.Marshal(map[string]any{
		"question":  "What is the Zorblat 9?",
		"report":    "## Answer\n\nThe Zorblat 9 is a handheld radio ([spec sheet](" + page + ")).",
		"citations": []any{map[string]any{"title": "Zorblat 9 spec sheet", "url": page, "status": "ok"}},
		"pages": map[string]string{page: "Zorblat 9 spec sheet.\n\nThe Zorblat 9 is a handheld radio." +
			"\n\n## Power\n\nThe Zorblat 9 ships with a 7,421 mAh battery that lasts 31 hours."},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "zorblat.json")
	if err := os.WriteFile(path, sidecar, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), "ask", path, "--no-color", "-p", "What battery capacity does it have?")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ask: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Logf("answer:\n%s", stdout.String())
	if a := stdout.String(); !strings.Contains(a, "7,421") && !strings.Contains(a, "7421") && !strings.Contains(a, "7 421") {
		t.Errorf("the answer does not state the battery the page gives: the model did not read it")
	}
}
