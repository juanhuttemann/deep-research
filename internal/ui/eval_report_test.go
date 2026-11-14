//go:build eval

package ui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type evalJudgment struct {
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Correct  bool   `json:"correct"`
}

type evalRun struct {
	Trace           *evalTrace              `json:"trace,omitempty"`
	WrongInferences int                     `json:"wrong_inferences"`
	Case            string                  `json:"case"`
	Repeat          int                     `json:"repeat"`
	Judgments       map[string]evalJudgment `json:"judgments"`
	Errors          int                     `json:"errors"`
	Detail          string                  `json:"detail,omitempty"`
	Seconds         float64                 `json:"seconds"`
	Tokens          int                     `json:"tokens"`
}

type evalReport struct {
	SourceSHA256 string        `json:"source_sha256"`
	Dirty        bool          `json:"dirty_working_tree"`
	Cases        []checkerCase `json:"cases"`
	Provider     string        `json:"provider"`
	Effort       string        `json:"fact_check_effort"`
	Model        string        `json:"model"`
	PromptSHA256 string        `json:"prompt_sha256"`
	Version      int           `json:"version"`
	Revision     string        `json:"revision"`
	Dataset      string        `json:"dataset_sha256"`
	Created      time.Time     `json:"created"`
	Runs         []evalRun     `json:"runs"`
}

func newEvalReport(t *testing.T, cases []checkerCase) *evalReport {
	t.Helper()
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	status, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := evalSourceHash()
	if err != nil {
		t.Fatal(err)
	}
	return &evalReport{SourceSHA256: fingerprint, Dirty: len(status) > 0, Version: 3, Cases: cases, Revision: string(revision[:len(revision)-1]), Dataset: fmt.Sprintf("%x", sha256.Sum256(data)), Created: time.Now().UTC()}
}

func expectedJudgments(c checkerCase) map[string]string {
	out := map[string]string{}
	for _, id := range c.Expect.Supported {
		out[id] = "supported"
	}
	for _, id := range c.Expect.NotSupported {
		out[id] = "not_supported"
	}
	for _, id := range c.Expect.Approved {
		out[id] = "approved"
	}
	for _, id := range c.Expect.Blocked {
		out[id] = "blocked"
	}
	return out
}

func (r *evalReport) add(c checkerCase, repeat int, got map[string]string, score evalScore, detail string, elapsed time.Duration, tokens int, trace *evalTrace) {
	run := evalRun{Trace: trace, Case: c.Name, Repeat: repeat, Judgments: map[string]evalJudgment{}, Errors: score.errors, WrongInferences: score.rawWrong, Detail: detail, Seconds: elapsed.Seconds(), Tokens: tokens}
	for id, expected := range expectedJudgments(c) {
		actual := got[id]
		correct := actual == expected
		if expected == "not_supported" {
			correct = actual != "" && actual != "error" && actual != "supported"
		}
		run.Judgments[id] = evalJudgment{expected, actual, correct}
	}
	r.Runs = append(r.Runs, run)
}

func (r *evalReport) counts() (right, total, errors int) {
	for _, run := range r.Runs {
		errors += run.Errors
		for _, j := range run.Judgments {
			total++
			if j.Correct {
				right++
			}
		}
	}
	return
}

func (r *evalReport) finish(t *testing.T) {
	t.Helper()
	right, total, errors := r.counts()
	t.Logf("SCORE: %d/%d expected judgments correct; %d case errors", right, total, errors)
	if path := os.Getenv("EVAL_OUTPUT"); path != "" {
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("EVAL_BASELINE"); path != "" {
		r.compare(t, path)
	}
	if os.Getenv("EVAL_STRICT") == "1" && r.failed() {
		t.Error("checker evaluation failed: see scored judgments")
	}
}

// Comparisons aggregate repeated judgments; repetition numbers do not pair
// independent model samples or establish a causal improvement.
func (r *evalReport) compare(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before evalReport
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	if before.Model != r.Model || before.Provider != r.Provider || before.Effort != r.Effort || before.PromptSHA256 != r.PromptSHA256 {
		t.Log("COMPARE: model/provider/effort/instructions changed; results include those differences")
	}
	if before.SourceSHA256 != r.SourceSHA256 {
		t.Logf("COMPARE source: %s → %s", before.SourceSHA256, r.SourceSHA256)
	}
	rows, err := compareEvalReports(before, *r)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Logf("COMPARE %s/%s: %d/%d → %d/%d correct; %s", row.Case, row.ID, row.Before.Correct, row.Before.Total, row.After.Correct, row.After.Total, row.Change)
	}
}

func TestEvalReportScoring(t *testing.T) {
	var c checkerCase
	c.Name = "scope"
	c.Expect.Supported = []string{"c1"}
	c.Expect.NotSupported = []string{"c2"}
	r := &evalReport{}
	r.add(c, 1, map[string]string{"c1": "supported", "c2": "error"}, evalScore{errors: 1}, "", time.Second, 10, nil)
	right, total, errors := r.counts()
	if right != 1 || total != 2 || errors != 1 {
		t.Fatalf("got %d/%d errors %d", right, total, errors)
	}
	r.add(c, 2, map[string]string{"c1": "insufficient", "c2": "contradicted"}, evalScore{}, "", 0, 0, nil)
	right, total, errors = r.counts()
	if right != 2 || total != 4 || errors != 1 {
		t.Fatalf("got %d/%d errors %d", right, total, errors)
	}
}

func (r *evalReport) failed() bool {
	right, total, errors := r.counts()
	if right != total || errors != 0 {
		return true
	}
	for _, run := range r.Runs {
		if run.WrongInferences != 0 {
			return true
		}
	}
	return false
}

func TestEvalReportDetectsMaskedInferenceFailure(t *testing.T) {
	r := &evalReport{Runs: []evalRun{{WrongInferences: 1, Judgments: map[string]evalJudgment{"r1": {Expected: "blocked", Actual: "blocked", Correct: true}}}}}
	if !r.failed() {
		t.Fatal("raw inference error masked by governance passed strict evaluation")
	}
	r.Runs[0].WrongInferences = 0
	if r.failed() {
		t.Fatal("correct evaluation failed")
	}
	r.Runs[0].Errors = 1
	if !r.failed() {
		t.Fatal("provider error passed strict evaluation")
	}
}

// Hash source inputs, including untracked tests and fixtures, rather than
// treating two dirty trees at the same HEAD as the same code variant.
func evalSourceHash() (string, error) {
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(rootBytes))
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "internal", "config", "go.mod", "go.sum")
	cmd.Dir = root
	names, err := cmd.Output()
	if err != nil {
		return "", err
	}
	paths := strings.Split(strings.TrimRight(string(names), "\x00"), "\x00")
	slices.Sort(paths)
	h := sha256.New()
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if os.IsNotExist(err) {
			data = []byte("deleted")
		} else if err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(fmt.Sprintf("%s\x00%d\x00", path, len(data))))
		_, _ = h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
