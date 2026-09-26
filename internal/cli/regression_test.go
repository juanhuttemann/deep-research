package cli

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

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
)

func TestHistoryPreservesResearchAndVerification(t *testing.T) {
	// Decode through the public JSON shape so this covers the CLI/store
	// boundary as well as the on-disk record.
	input := `{"question":"q","tokens":321,"findings":[{"title":"Fetched","status":"ok"},{"title":"Snippet","status":"degraded"},{"title":"Unknown","status":"unverified"}],"analysis":{"answer":"answer","topics":[{"name":"topic"}],"gaps":["gap"],"follow_up":["next query"]},"fact_check":{"verified":[{"claim":"checked","verified":true,"evidence":"source"}],"unverified":["unknown"],"contradictions":[{"claim":"conflict","sources":["a","b"]}]},"summary":{"report":"report","executive_summary":"summary","confidence":"low"}}`
	var result agent.ResearchResult
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		t.Fatal(err)
	}
	d := Deps{Config: config.Config{DataFile: filepath.Join(t.TempDir(), "runs.jsonl")}}
	if err := saveRun(d, &result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(d.Config.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tokens", "findings", "analysis", "fact_check", "summary"} {
		if !jsonContains(got[key], want[key]) {
			t.Errorf("history lost %s: got %#v, want %#v", key, got[key], want[key])
		}
	}
	cmd := New(func() (Deps, error) { return d, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Fetched: 1/3", "Tokens: 321"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("list missing %q: %s", text, &out)
		}
	}
}

func jsonContains(got, want any) bool {
	switch want := want.(type) {
	case map[string]any:
		m, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range want {
			if !jsonContains(m[key], value) {
				return false
			}
		}
		return true
	case []any:
		a, ok := got.([]any)
		if !ok || len(a) != len(want) {
			return false
		}
		for i, value := range want {
			if !jsonContains(a[i], value) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}

// `list` is the only view onto the run history, so a record it cannot read is
// a run that has silently disappeared. It must say so, on stderr, while still
// listing everything that did survive.
func TestListWarnsAboutUnreadableHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	good := `{"id":"kept","question":"a readable question","summary":{"confidence":"high"}}`
	torn := `{"id":"lost","question":"killed mid-w`
	if err := os.WriteFile(path, []byte(good+"\n"+torn), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := New(func() (Deps, error) { return Deps{Config: config.Config{DataFile: path}}, nil })
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "a readable question") {
		t.Errorf("readable run missing from list: %s", &out)
	}
	if !strings.Contains(errOut.String(), "1 unreadable record") {
		t.Errorf("history silently under-reported; stderr said: %q", errOut.String())
	}
}

// -o wrote Summary.Report while the .md artifact for the same run held the
// full MarkdownReport, so one run produced two different documents and the
// flagged one silently dropped the title, the confidence line and every
// citation.
func TestOutputFileHoldsTheSameReportAsTheArtifact(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.md")
	deps := Deps{
		Assistant: always(stub{}),
		Config: config.Config{DataFile: filepath.Join(dir, "runs.jsonl"),
			Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "why is the sky blue", "--silent", "--reports", dir, "-o", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run: %v", err)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read -o file: %v", err)
	}
	artifacts, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(artifacts) == 0 {
		t.Fatalf("no .md artifact written: %v", err)
	}
	artifact, err := os.ReadFile(artifacts[0])
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(written) != string(artifact) {
		t.Errorf("-o wrote a different document from the .md artifact:\n--- -o ---\n%s\n--- artifact ---\n%s",
			written, artifact)
	}
	if !strings.Contains(string(written), "# why is the sky blue") {
		t.Errorf("-o output lost the report title:\n%s", written)
	}
}

// Both flags write to stdout. Together, the report was rendered into the
// middle of the event stream, so the machine-readable output did not parse;
// the combination is rejected rather than guessed at.
func TestSilentAndJSONLAreMutuallyExclusive(t *testing.T) {
	deps := Deps{
		Assistant: always(stub{}),
		Config: config.Config{DataFile: filepath.Join(t.TempDir(), "r.jsonl"),
			Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "q", "--silent", "--jsonl", "--reports", t.TempDir()})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--silent --jsonl was accepted")
	}
	if !strings.Contains(err.Error(), "silent") || !strings.Contains(err.Error(), "jsonl") {
		t.Errorf("error should name both flags: %v", err)
	}
}

// --detach used to be reachable only from a test: Options.Detach was set by
// nothing the CLI could pass.
func TestDetachFlagIsAccepted(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Assistant: always(stub{}),
		Config: config.Config{DataFile: filepath.Join(dir, "r.jsonl"),
			Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "q", "--detach", "--reports", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--detach: %v", err)
	}
}

// `-p ""` planned nothing and still spent three model calls, wrote three
// artifacts and a history record about nothing.
func TestEmptyQuestionIsRejected(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Assistant: func() (agent.Assistant, error) {
			t.Error("assistant built for an empty question")
			return stub{}, nil
		},
		Config: config.Config{DataFile: filepath.Join(dir, "r.jsonl"), Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	for _, q := range []string{"", "   \t"} {
		cmd := New(func() (Deps, error) { return deps, nil })
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs([]string{"-p", q, "--silent", "--reports", dir})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("-p %q: err = %v, want an empty-question error", q, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a rejected run wrote files: %v", entries)
	}
}

// failingSummary is the stub assistant with a summarizer that errors.
type failingSummary struct{ agent.Assistant }

func (failingSummary) Summarize(context.Context, string) (*agent.Summary, error) {
	return nil, errors.New("provider 500")
}

// A run whose report call failed is saved and exported from what it gathered,
// but the exit status still says it did not finish.
func TestIncompleteRunIsSavedAndFails(t *testing.T) {
	dir := t.TempDir()
	history := filepath.Join(dir, "r.jsonl")
	deps := Deps{
		Assistant: always(failingSummary{stub{}}),
		Config: config.Config{DataFile: history,
			Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cmd := New(func() (Deps, error) { return deps, nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"-p", "q", "--silent", "--reports", dir})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "provider 500") {
		t.Errorf("err = %v, want the incomplete-run error", err)
	}
	if _, statErr := os.Stat(history); statErr != nil {
		t.Errorf("incomplete run was not saved to history: %v", statErr)
	}
	if mds, _ := filepath.Glob(filepath.Join(dir, "*.md")); len(mds) != 1 {
		t.Errorf("incomplete run wrote %d .md artifacts, want 1", len(mds))
	}
}

// doctor names what is wrong and exits non-zero, so a run that produced
// nothing useful can be traced to its cause.
func TestDoctorReportsProblemsAndBudget(t *testing.T) {
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", t.TempDir())
	deps := Deps{Config: config.Config{Config: agent.Config{OpenAIModel: "m", ModelCallTimeout: time.Second}}}
	cmd := New(func() (Deps, error) { return deps, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"doctor"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "1 problem") {
		t.Errorf("err = %v, want one problem (the missing key)", err)
	}
	for _, want := range []string{"config", "embedded defaults", "✗ model", "openrouter.ai/keys", "✓ search", "budget", "quick 10", "deep 16"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestInitDockerSaysWhatToRun(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", t.TempDir())
	t.Chdir(cwd)
	cmd := New(func() (Deps, error) { return Deps{}, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"init", "--docker"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docker-compose.yml", "docker compose up -d", "SEARXNG_URL=http://localhost:8888", "docs/services.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("init --docker output lacks %q:\n%s", want, out.String())
		}
	}
}
