package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// governAll is govern on the findings' own fetched pages.
func governAll(a *agent.Analysis, fc *agent.FactCheckResult, fcErr error, findings []agent.Finding) []string {
	return govern(a, fc, fcErr, findings, fetchedPages(findings))
}

func claimsN(n int) []agent.Claim {
	out := make([]agent.Claim, n)
	for i := range out {
		out[i] = agent.Claim{ID: fmt.Sprintf("c%d", i+1), Text: fmt.Sprintf("claim %d", i+1),
			Sources: []string{"https://a.example/"}}
	}
	return out
}

func TestClaimGroupsSplitEvenlyWithinTheCallLimit(t *testing.T) {
	for _, tc := range []struct {
		claims int
		sizes  []int
	}{
		{1, []int{1}},
		{11, []int{11}},
		{12, []int{6, 6}},
		{27, []int{6, 7, 7, 7}},
		{60, []int{15, 15, 15, 15}},
	} {
		var got []int
		seen := 0
		for _, g := range claimGroups(claimsN(tc.claims)) {
			got = append(got, len(g))
			if g[0].ID != fmt.Sprintf("c%d", seen+1) {
				t.Errorf("%d claims: a group starts at %s, want c%d (in order, none skipped)", tc.claims, g[0].ID, seen+1)
			}
			seen += len(g)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.sizes) {
			t.Errorf("%d claims split %v, want %v", tc.claims, got, tc.sizes)
		}
	}
}

func TestCheckPartsGiveEachCallItsOwnShare(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(12),
		Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1", "c12"}}}}
	findings := []agent.Finding{{Title: "Page", URL: "https://a.example/", Content: "evidence text", Status: "ok"}}
	parts := checkParts(a, findings, true)
	if len(parts) != 3 {
		t.Fatalf("%d parts, want two claim calls and the inferences", len(parts))
	}
	first, second, inf := parts[0].prompt, parts[1].prompt, parts[2].prompt
	if !strings.Contains(first, "c6 ") || strings.Contains(first, "c7 ") || !strings.Contains(second, "c12 ") {
		t.Errorf("claims not split c1-c6 / c7-c12:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(first, "evidence text") || !strings.Contains(second, "evidence text") {
		t.Error("a claim call is missing the findings: a claim is judged against every page")
	}
	if strings.Contains(first, "premise") {
		t.Error("a claim call carries the inferences, which the inference call judges")
	}
	if !isInferenceCheck(inf) || !strings.Contains(inf, "premise c12: claim 12") || strings.Contains(inf, "evidence text") {
		t.Errorf("the inference call should name each premise and carry no findings:\n%s", inf)
	}
	if parts[0].label != "claims c1-c6" || parts[2].label != "inferences" {
		t.Errorf("labels %q, %q", parts[0].label, parts[2].label)
	}

	prose := checkParts(&agent.Analysis{Answer: "prose"}, findings, true)
	if len(prose) != 1 || prose[0].label != "" {
		t.Errorf("an analysis without claims is %d calls, want the one call the checker splits itself", len(prose))
	}
}

// The calls must run at once: that is the whole point of splitting the check.
// The fake answers only once every call has arrived.
func TestCheckAllRunsThePartsAtOnce(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(24), Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1"}}}}
	parts := checkParts(a, nil, true)
	var arrived sync.WaitGroup
	arrived.Add(len(parts))
	all := make(chan struct{})
	go func() { arrived.Wait(); close(all) }()
	fc, err := checkAll(context.Background(), parts, func(ctx context.Context, p string) (*agent.FactCheckResult, error) {
		arrived.Done()
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			return nil, errors.New("the other calls never started")
		}
		if isInferenceCheck(p) {
			return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}},
				Verdicts: []agent.Verdict{{ID: "p1", Status: "supported"}}}, nil
		}
		var vs []agent.Verdict
		for _, c := range a.Claims {
			if strings.Contains(p, c.ID+" [") {
				vs = append(vs, agent.Verdict{ID: c.ID, Status: "supported"})
			}
		}
		return &agent.FactCheckResult{Verdicts: vs, Inferences: []agent.Inference{{ID: "k9"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.Verdicts) != 24 || len(fc.Inferences) != 1 || fc.Inferences[0].ID != "k1" {
		t.Errorf("merged %d verdicts, inferences %+v; want each claim's once and only the inference call's inference",
			len(fc.Verdicts), fc.Inferences)
	}
}

// A call that fails leaves its claims unverified, named as unchecked, and the
// rest of the check stands: one failed call no longer costs a whole run its
// verdicts.
func TestFailedCheckCallLeavesOnlyItsClaimsUnchecked(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(12)}
	findings := []agent.Finding{{Title: "Page", URL: "https://a.example/", Content: "claim 1 claim 7", Status: "ok"}}
	fc, err := checkAll(context.Background(), checkParts(a, findings, true), func(_ context.Context, p string) (*agent.FactCheckResult, error) {
		if strings.Contains(p, "c7 [") {
			return nil, errors.New("provider timed out")
		}
		return &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c1", Status: "supported",
			Evidence: []agent.Evidence{{Source: "https://a.example/", Quote: "claim 1"}}}}}, nil
	})
	if fc == nil || err == nil || !strings.Contains(err.Error(), "claims c7-c12: provider timed out") {
		t.Fatalf("fc %v, err %v; want the first call's verdicts and the second call named in the error", fc, err)
	}
	governAll(a, fc, err, findings)
	if a.Claims[0].Status != statusSupported {
		t.Errorf("c1 = %s %q, want supported: its call answered", a.Claims[0].Status, a.Claims[0].Note)
	}
	if !strings.HasPrefix(a.Claims[1].Note, "the fact-check gave no verdict") {
		t.Errorf("c2 note %q: its call answered without it, which is not a failure", a.Claims[1].Note)
	}
	if !strings.HasPrefix(a.Claims[6].Note, "fact_check_unavailable: claims c7-c12: provider timed out") {
		t.Errorf("c7 note %q, want it unchecked because its call failed", a.Claims[6].Note)
	}
}

// The inferences split like the claims: in one call they were the slowest
// part of the check in seven runs of eleven. Each is judged in exactly one.
func TestInferencesSplitAcrossCalls(t *testing.T) {
	for _, tc := range []struct{ units, calls int }{{1, 1}, {4, 1}, {5, 2}, {13, 3}, {30, 3}} {
		a := &agent.Analysis{Claims: claimsN(1)}
		for range tc.units {
			a.Conclusions = append(a.Conclusions, agent.Recommendation{Statement: "s", Claims: []string{"c1"}})
		}
		judged := map[string]int{}
		calls := 0
		for _, p := range checkParts(a, nil, true) {
			if !isInferenceCheck(p.prompt) {
				continue
			}
			calls++
			for i := range tc.units {
				if strings.Contains(p.prompt, fmt.Sprintf("  - k%d: ", i+1)) {
					judged[fmt.Sprintf("k%d", i+1)]++
				}
			}
		}
		if calls != tc.calls || len(judged) != tc.units {
			t.Errorf("%d units: %d calls judging %d of them, want %d calls judging all", tc.units, calls, len(judged), tc.calls)
		}
		for id, n := range judged {
			if n != 1 {
				t.Errorf("%d units: %s judged %d times", tc.units, id, n)
			}
		}
	}
}

// A call answering for a claim or a conclusion it was not given was taken at
// its word: with the call that was given it failed, a claim nothing checked
// stood as supported, and a conclusion as following.
func TestStrayJudgementsFromAnotherCallAreIgnored(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(12)}
	for range 5 {
		a.Conclusions = append(a.Conclusions, agent.Recommendation{Statement: "s", Claims: []string{"c1"}})
	}
	findings := []agent.Finding{{Title: "Page", URL: "https://a.example/", Content: "claim 7", Status: "ok"}}
	fc, err := checkAll(context.Background(), checkParts(a, findings, true), func(_ context.Context, p string) (*agent.FactCheckResult, error) {
		switch {
		case strings.Contains(p, "c7 ["), strings.Contains(p, "  - k1: "):
			return nil, errors.New("provider timed out")
		case isInferenceCheck(p):
			return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}, {ID: "k4", Follows: true}}}, nil
		}
		return &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c7", Status: "supported",
			Evidence: []agent.Evidence{{Source: "https://a.example/", Quote: "claim 7"}}}}}, nil
	})
	if fc == nil || err == nil {
		t.Fatalf("fc %v, err %v; want the calls that answered and the failed ones named", fc, err)
	}
	for _, v := range fc.Verdicts {
		t.Errorf("kept verdict %+v from the call for c1-c6, which was not given c7", v)
	}
	if len(fc.Inferences) != 1 || fc.Inferences[0].ID != "k4" {
		t.Errorf("inferences %+v, want only k4, from the call that was given it", fc.Inferences)
	}
	governAll(a, fc, err, findings)
	if c := a.Claims[6]; c.Status == statusSupported || !strings.HasPrefix(c.Note, "fact_check_unavailable") {
		t.Errorf("c7 = %s %q, want unchecked: its own call failed", c.Status, c.Note)
	}
}

// With every claim call failed the check did not run, though the inference
// call answered: a result holding only inferences kept the run's confidence
// as if its claims had been checked.
func TestNoClaimCallAnsweredIsNoCheck(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(12), Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1"}}}}
	fc, err := checkAll(context.Background(), checkParts(a, nil, true), func(_ context.Context, p string) (*agent.FactCheckResult, error) {
		if isInferenceCheck(p) {
			return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}}}, nil
		}
		return nil, errors.New("provider timed out")
	})
	if fc != nil || err == nil {
		t.Errorf("fc %+v, err %v; want no check and the error", fc, err)
	}
}

// One call excerpted each long page by the claims it judged. A passage that
// matched another call's claim, the one that could contradict this call's,
// was cut from the excerpt; as one call, every page is excerpted by all the
// claims.
func TestEachClaimCallSeesPagesExcerptedByAllClaims(t *testing.T) {
	a := &agent.Analysis{Claims: claimsN(12)}
	a.Claims[0].Text = "widget prices stayed flat through 2024"
	page := strings.Repeat("Unrelated navigation text about the site and its menus.\n\n", 60) +
		"Widget prices stayed flat through 2024, the survey found.\n\n" +
		strings.Repeat("More unrelated footer text and links.\n\n", 60)
	findings := []agent.Finding{{Title: "Survey", URL: "https://a.example/", Content: page, Status: "ok"}}
	parts := checkParts(a, findings, true)
	if !strings.Contains(parts[1].prompt, "stayed flat through 2024") {
		t.Errorf("the call for c7-c12 lost the passage c1 matched:\n%s", parts[1].prompt)
	}
}

// partlyFailingChecker makes a confident analysis whose second claim call fails.
type partlyFailingChecker struct{ *fakeAssistant }

func (p *partlyFailingChecker) Analyze(context.Context, string) (*agent.Analysis, error) {
	return &agent.Analysis{Answer: "a", Confidence: "high", Claims: claimsN(12),
		Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1"}}}}, nil
}

func (p *partlyFailingChecker) FactCheck(_ context.Context, prompt string) (*agent.FactCheckResult, error) {
	switch {
	case strings.Contains(prompt, "c7 ["):
		return nil, errors.New("provider timed out")
	case isInferenceCheck(prompt):
		return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}}}, nil
	}
	return &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c1", Status: "insufficient"}}}, nil
}

// A check that left a share of the claims unverified kept the analyzer's
// "high" on the report: only a check that failed whole lowered it.
func TestPartlyFailedCheckLowersConfidence(t *testing.T) {
	res, err := NewDriver(&partlyFailingChecker{&fakeAssistant{}}, &MultiSink{}, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.FactCheck == nil || res.Summary.Confidence != "low" {
		t.Errorf("fact-check %v, confidence %q; want the answered share kept and confidence low", res.FactCheck != nil, res.Summary.Confidence)
	}
}

// blockingChecker makes a confident analysis whose one conclusion rests on a
// claim the check finds insufficient.
type blockingChecker struct{ *fakeAssistant }

func (b *blockingChecker) Analyze(context.Context, string) (*agent.Analysis, error) {
	return &agent.Analysis{Answer: "a", Confidence: "high", Claims: claimsN(1),
		Conclusions: []agent.Recommendation{{Statement: "s", Claims: []string{"c1"}}}}, nil
}

func (b *blockingChecker) FactCheck(_ context.Context, prompt string) (*agent.FactCheckResult, error) {
	if isInferenceCheck(prompt) {
		return &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}}}, nil
	}
	return &agent.FactCheckResult{Verdicts: []agent.Verdict{{ID: "c1", Status: "insufficient"}}}, nil
}

// A check that ran and blocked every conclusion left the analyzer's "high"
// on a report whose answer section says nothing was established.
func TestNothingApprovedLowersConfidence(t *testing.T) {
	res, err := NewDriver(&blockingChecker{&fakeAssistant{}}, &MultiSink{}, nil, 1).Run(context.Background(), newTestPlan("quick", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Confidence != "low" {
		t.Errorf("confidence %q with nothing approved; want low", res.Summary.Confidence)
	}
}
