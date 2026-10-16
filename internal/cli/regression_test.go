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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/replay"
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
	for _, want := range []string{"config", "embedded defaults", "✗ model", "openrouter.ai/keys", "✓ search", "budget", "quick 19", "deep 25"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out.String())
		}
	}
}

// init --docker wrote its files into whatever folder it ran in and left
// SEARXNG_URL for the reader to add, so a run from anywhere else still went
// to the public instances. It sets up in the config dir and points the
// config at it, leaving one command to run.
func TestInitDockerSaysWhatToRun(t *testing.T) {
	cwd, cfgDir := t.TempDir(), t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	t.Chdir(cwd)
	cmd := New(func() (Deps, error) { return Deps{}, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"init", "--docker"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docker compose -f " + filepath.Join(cfgDir, "docker-compose.yml") + " up -d",
		filepath.Join(cfgDir, ".env") + ": SEARXNG_URL=http://localhost:8888", "docs/services.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("init --docker output lacks %q:\n%s", want, out.String())
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) > 0 {
		t.Errorf("init --docker wrote into the folder it ran in: %v", entries)
	}
}

// A --jsonl caller had no line saying the run was over or where its report
// was: the report event came before the files were written and named none,
// and a fatal error reached only stderr. Every run now ends on one done line.
func TestJSONLEndsOnDone(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Assistant: always(stub{}),
		Config: config.Config{DataFile: filepath.Join(dir, "r.jsonl"),
			Config: agent.Config{ModelCallTimeout: time.Second}},
	}
	cases := []struct {
		name, question, status, detail string
		files                          int
	}{
		{"complete", "q", "complete", "", 3},
		{"failed", "  ", "failed", "empty", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := New(func() (Deps, error) { return deps, nil })
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader(""))
			cmd.SetArgs([]string{"-p", tc.question, "--jsonl", "--reports", t.TempDir()})
			err := cmd.Execute()
			if (err != nil) != (tc.status != "complete") {
				t.Errorf("err = %v for a %s run", err, tc.status)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var done struct {
				Type, Status, Detail string
				Artifacts            []string
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &done); err != nil {
				t.Fatalf("last line is not an event: %q", lines[len(lines)-1])
			}
			if done.Type != "done" || done.Status != tc.status || !strings.Contains(done.Detail, tc.detail) {
				t.Errorf("last event = %+v, want done/%s mentioning %q", done, tc.status, tc.detail)
			}
			if len(done.Artifacts) != tc.files {
				t.Errorf("artifacts = %v, want %d", done.Artifacts, tc.files)
			}
			for _, p := range done.Artifacts {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("done names a file that is not there: %v", err)
				}
			}
		})
	}
}

// init printed "wrote 3 file(s) to <config dir>:" above a list that included
// the .env, which it wrote to the working directory, so the reader was sent
// looking for it in the wrong place. Both now go to the config dir, which is
// the only place a run reads them, and nothing is written where init ran.
func TestInitSaysWhereTheEnvWent(t *testing.T) {
	cfgDir, cwd := t.TempDir(), t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	t.Chdir(cwd)
	cmd := New(func() (Deps, error) { return Deps{}, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{filepath.Join(cfgDir, "config.yaml"), filepath.Join(cfgDir, ".env")} {
		if !strings.Contains(out.String(), "  - "+want+"\n") {
			t.Errorf("init output does not list %s:\n%s", want, out.String())
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) > 0 {
		t.Errorf("init wrote into the folder it ran in: %v", entries)
	}
}

// A missing Exa key failed the run under "Pick a provider and store its key:
// deep-research login", sending the reader to fix a model key that worked.
func TestSearchSettingErrorHasNoLoginHint(t *testing.T) {
	d := Deps{Assistant: func() (agent.Assistant, error) {
		return agent.New(agent.Config{OpenAIAPIKey: "k", OpenAIBaseURL: "http://127.0.0.1:1", SearchEngine: "exa"})
	}}
	_, err := pickAssistant(d)
	if err == nil || !strings.Contains(err.Error(), "EXA_API_KEY") || strings.Contains(err.Error(), "deep-research login") {
		t.Errorf("error = %v", err)
	}
}

// A checkpoint's domain list went straight to the search on --resume, while
// --plan parsed the same list: "localhost" was searched, and "GO.DEV" matched
// no result, so the run found nothing and said nothing. A resumed list is
// held to the rule a typed one is.
func TestResumedDomainListIsParsedLikeATypedOne(t *testing.T) {
	resume := func(domain string) ([]string, error) {
		dir := t.TempDir()
		cp := filepath.Join(dir, "q.abc.partial.json")
		b, _ := json.Marshal(replay.Trace{Version: 1, Question: "q", Mode: "quick", Plan: []agent.SubTopic{{ID: "1", Name: "A"}}, Domains: []string{domain}})
		if err := os.WriteFile(cp, b, 0o644); err != nil {
			t.Fatal(err)
		}
		var scoped []string
		_, err := runCLI(t, dir, scopingStub{scoped: &scoped}, "--resume", cp, "--silent", "--reports", dir)
		return scoped, err
	}
	if scoped, err := resume("localhost"); err == nil || scoped != nil {
		t.Errorf("checkpoint with localhost: err=%v scoped=%v, want refused before any search", err, scoped)
	}
	if scoped, err := resume("GO.DEV"); err != nil || !slices.Equal(scoped, []string{"go.dev"}) {
		t.Errorf("checkpoint with GO.DEV: err=%v scoped=%v, want it normalised to go.dev", err, scoped)
	}
}

// doctor printed what broke and stopped; the next step now follows a failed
// line, and a doctor that passes says the run is ready. The budget read
// "quick 7, standard 7, deep 7", which looked like a bug, so a budget that is
// the same in every mode is said once.
func TestDoctorSaysWhatToDoNext(t *testing.T) {
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", t.TempDir())
	deps := Deps{Config: config.Config{Config: agent.Config{OpenAIModel: "m", ModelCallTimeout: time.Second}}}
	cmd := New(func() (Deps, error) { return deps, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"doctor"})
	_ = cmd.Execute()
	if strings.Contains(out.String(), "ready to run") {
		t.Errorf("a doctor with a problem said ready:\n%s", out.String())
	}
	if got := callBudget(true); !strings.Contains(got, "at most 7, in every --mode") {
		t.Errorf("web-search budget = %q", got)
	}
	if got := configSource(); !strings.Contains(got, "deep-research init") {
		t.Errorf("no config found, and no word on how to make one: %q", got)
	}
}
