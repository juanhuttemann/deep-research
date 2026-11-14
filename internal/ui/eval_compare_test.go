//go:build eval

package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

type evalTally struct {
	Correct, Total, Errors int
	Expected               string
}
type evalChange struct {
	Case, ID, Change string
	Before, After    evalTally
}

func tallyEval(r evalReport) (map[string]evalTally, error) {
	out := map[string]evalTally{}
	seen := map[string]bool{}
	for _, run := range r.Runs {
		sample := fmt.Sprintf("%s/%d", run.Case, run.Repeat)
		if seen[sample] {
			return nil, fmt.Errorf("duplicate case/repetition %s", sample)
		}
		seen[sample] = true
		for id, j := range run.Judgments {
			key := run.Case + "\x00" + id
			t := out[key]
			if t.Total > 0 && t.Expected != j.Expected {
				return nil, fmt.Errorf("inconsistent expected label for %s/%s", run.Case, id)
			}
			t.Expected = j.Expected
			t.Total++
			if j.Correct {
				t.Correct++
			}
			if j.Actual == "error" {
				t.Errors++
			}
			out[key] = t
		}
	}
	return out, nil
}
func compareEvalReports(before, after evalReport) ([]evalChange, error) {
	if before.Version != after.Version || before.Dataset == "" || before.Dataset != after.Dataset {
		return nil, fmt.Errorf("baseline schema or selected evidence/labels differ; rerun both variants on the same cases")
	}
	b, err := tallyEval(before)
	if err != nil {
		return nil, err
	}
	a, err := tallyEval(after)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) != len(a) {
		return nil, fmt.Errorf("missing judgments in a comparison")
	}
	keys := make([]string, 0, len(b))
	for key := range b {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]evalChange, 0, len(keys))
	for _, key := range keys {
		at, ok := a[key]
		bt := b[key]
		if !ok || at.Expected != bt.Expected {
			return nil, fmt.Errorf("expected judgment changed for %q", key)
		}
		name, id, _ := strings.Cut(key, "\x00")
		out = append(out, evalChange{name, id, classifyEvalChange(bt, at), bt, at})
	}
	return out, nil
}
func classifyEvalChange(b, a evalTally) string {
	switch {
	case b.Errors+a.Errors > 0:
		return "provider_error"
	case b.Total < 3 || a.Total < 3:
		return "insufficient_repeats"
	case b.Correct == 0 && a.Correct == a.Total:
		return "consistent_improvement_on_this_case"
	case b.Correct == b.Total && a.Correct == 0:
		return "consistent_regression_on_this_case"
	case b.Correct == 0 && a.Correct == 0:
		return "unchanged_failure"
	case b.Correct == b.Total && a.Correct == a.Total:
		return "unchanged_pass"
	default:
		return "unstable"
	}
}

func TestEvalComparisonDoesNotPairIndependentSamples(t *testing.T) {
	tests := []struct {
		name string
		b, a evalTally
		want string
	}{
		{"one observation", evalTally{Total: 1}, evalTally{Correct: 1, Total: 1}, "insufficient_repeats"},
		{"mixed baseline", evalTally{Correct: 1, Total: 3}, evalTally{Correct: 3, Total: 3}, "unstable"},
		{"consistent recovery", evalTally{Total: 3}, evalTally{Correct: 3, Total: 3}, "consistent_improvement_on_this_case"},
		{"consistent regression", evalTally{Correct: 3, Total: 3}, evalTally{Total: 3}, "consistent_regression_on_this_case"},
		{"different repeat counts", evalTally{Correct: 3, Total: 3}, evalTally{Correct: 5, Total: 5}, "unchanged_pass"},
		{"provider errors", evalTally{Total: 3, Errors: 1}, evalTally{Correct: 3, Total: 3}, "provider_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyEvalChange(tt.b, tt.a); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}
func TestEvalComparisonRejectsChangedLabelsAndDuplicateSamples(t *testing.T) {
	r := evalReport{Version: 3, Dataset: "same", Runs: []evalRun{{Case: "scope", Repeat: 1, Judgments: map[string]evalJudgment{"c1": {Expected: "supported", Actual: "supported", Correct: true}}}}}
	other := r
	other.Dataset = "changed"
	if _, err := compareEvalReports(r, other); err == nil {
		t.Fatal("changed dataset accepted")
	}
	other = r
	other.Runs = []evalRun{{Case: "scope", Repeat: 1, Judgments: map[string]evalJudgment{"c1": {Expected: "not_supported", Actual: "supported"}}}}
	if _, err := compareEvalReports(r, other); err == nil {
		t.Fatal("changed expected label accepted")
	}
	other = r
	other.Runs = append(slices.Clone(r.Runs), r.Runs[0])
	if _, err := compareEvalReports(r, other); err == nil {
		t.Fatal("duplicate sample accepted")
	}
}
