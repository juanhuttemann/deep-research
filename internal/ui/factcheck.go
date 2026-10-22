package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// The fact-check is the longest call of a run, and nearly all of it is the
// model reasoning before its first verdict: 213 seconds of a 514-second run,
// 37 thousand of its 41 thousand output tokens reasoning, for 27 claims and
// eight recommendations and conclusions in one call. The time grows with what
// the call is asked to judge, not with the evidence it reads (a 2 KB recheck
// took 76 seconds, the 43 KB report 20), so the check is split into calls that
// judge disjoint parts and run at once.
//
// claimsPerCheck is the fewest claims that earn a call of their own, and
// maxClaimChecks how many calls the claims are split into at most: each is a
// model request, which is what free tiers cap per minute and per day.
//
// The recommendations and conclusions split the same way, each judged
// against its own premises: in one call they were the slowest part of the
// check in seven runs of eleven, 165 seconds for fifteen of them while every
// claim call ended within 17. inferencesPerCheck is the most one call is
// given while there are calls to spare.
const (
	claimsPerCheck     = 6
	maxClaimChecks     = 4
	inferencesPerCheck = 4
	maxInferenceChecks = 3
)

// MaxCheckCalls is the most model requests one fact-check makes before
// retries: the claim calls and the calls that judge the inferences.
const MaxCheckCalls = maxClaimChecks + maxInferenceChecks

// checkPart is one call of the fact-check: a share of the claims, judged
// against every finding, or the recommendations and conclusions, judged
// against their premises.
type checkPart struct {
	label, prompt string
	// claims marks a part that judges claims; the inference part has none.
	claims bool
	// ids are what the part was asked to judge: only its judgements of
	// these are kept, and a failure leaves its claims among them unchecked.
	// The one call over prose has none, and keeps all it judged.
	ids []string
}

// checkParts splits the check of a's claims into calls of their own. Each
// claim call still reads every finding: a claim is contradicted by whatever
// applicable page says otherwise, not only by the pages it cites. The
// inferences need none of it — the checker judges each against its premises
// alone, assumed true — so they are a call without findings.
//
// An analysis with no claims is one call as before: the checker splits its
// prose into claims itself, and there is nothing to divide.
func checkParts(a *agent.Analysis, findings []agent.Finding, retrieved bool) []checkPart {
	if len(a.Claims) == 0 {
		return []checkPart{{prompt: factCheckPrompt(claimsToCheck(a), findings, retrieved, nil), claims: true}}
	}
	groups := claimGroups(a.Claims)
	var parts []checkPart
	for _, g := range groups {
		var sb strings.Builder
		sb.WriteString("Claims:\n")
		writeClaims(&sb, g)
		label := "claims"
		if len(groups) > 1 {
			label = "claims " + g[0].ID + "-" + g[len(g)-1].ID
		}
		// Every page is excerpted by all the claims, as in one call: a passage
		// another call's claim matched can contradict one of these, and an
		// excerpt chosen by this call's claims alone left it out.
		parts = append(parts, checkPart{label: label, prompt: factCheckPrompt(sb.String(), findings, retrieved, a.Claims),
			claims: true, ids: claimIDs(g)})
	}
	us := units(a)
	n := min(maxInferenceChecks, (len(us)+inferencesPerCheck-1)/inferencesPerCheck)
	for i := range n {
		g := us[i*len(us)/n : (i+1)*len(us)/n]
		ids := make([]string, len(g))
		for j, u := range g {
			ids[j] = u.id
		}
		label := "inferences"
		if n > 1 {
			label += " " + ids[0] + "-" + ids[len(ids)-1]
		}
		parts = append(parts, checkPart{label: label, prompt: inferencePrompt(a, ids), ids: ids})
	}
	return parts
}

func claimIDs(claims []agent.Claim) []string {
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	return ids
}

// claimGroups divides claims into at most maxClaimChecks runs of at least
// claimsPerCheck, as even as they come, in the analysis's order.
func claimGroups(claims []agent.Claim) [][]agent.Claim {
	n := min(maxClaimChecks, max(1, len(claims)/claimsPerCheck))
	var out [][]agent.Claim
	for i := range n {
		out = append(out, claims[i*len(claims)/n:(i+1)*len(claims)/n])
	}
	return out
}

// inferencePrompt asks only whether each recommendation and conclusion
// follows from the premises it names; only limits it to those IDs. It carries
// no claims to verify: given claims, the checker verified them again, and a
// revision's check spent a minute re-judging claims whose verdicts it already
// had, to answer one inference.
func inferencePrompt(a *agent.Analysis, only []string) string {
	var sb strings.Builder
	sb.WriteString("Judge only whether each recommendation and conclusion below follows from" +
		" the premises listed under it. There are no claims to verify and no findings:" +
		" return an \"inferences\" entry for each ID and an empty \"verdicts\" list.\n\n")
	writePremisePackets(&sb, a, only)
	return sb.String()
}

// checkAll runs the parts at once through check and merges what they judged:
// from each call, only its judgements of the IDs it was asked about. A
// verdict a call gave for another call's claim was taken as that claim's, and
// with the other call failed, a claim nothing had checked could stand as
// supported. A part that failed leaves its share unjudged. With no claim call
// answered the check did not run, whatever the inference calls said: their
// judgements count only for premises that passed. The error names each part
// that failed.
func checkAll(ctx context.Context, parts []checkPart,
	check func(context.Context, string) (*agent.FactCheckResult, error)) (*agent.FactCheckResult, error) {
	results := make([]*agent.FactCheckResult, len(parts))
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for i, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx := ctx
			if p.label != "" {
				pctx = agent.WithPart(ctx, p.label)
			}
			results[i], errs[i] = check(pctx, p.prompt)
		}()
	}
	wg.Wait()
	if len(parts) == 1 && parts[0].ids == nil {
		if errs[0] != nil {
			return nil, errs[0]
		}
		return results[0], nil
	}
	return merge(parts, results, errs)
}

// merge combines the parts' results; see checkAll.
func merge(parts []checkPart, results []*agent.FactCheckResult, errs []error) (*agent.FactCheckResult, error) {
	merged, claimsJudged := &agent.FactCheckResult{}, false
	var failed, unchecked []string
	for i, p := range parts {
		if errs[i] != nil {
			failed = append(failed, cmp.Or(p.label, "claims")+": "+errs[i].Error())
			if p.claims {
				unchecked = append(unchecked, p.ids...)
			}
			continue
		}
		if p.claims {
			claimsJudged = true
			merged.Verdicts = append(merged.Verdicts, asked(results[i].Verdicts, p.ids, verdictID)...)
		} else {
			merged.Inferences = append(merged.Inferences, asked(results[i].Inferences, p.ids, inferenceID)...)
		}
	}
	// One line, as every other error the run reports.
	switch {
	case !claimsJudged:
		return nil, errors.New(strings.Join(failed, "; "))
	case len(failed) > 0:
		return merged, &partialCheck{claims: unchecked, err: errors.New(strings.Join(failed, "; "))}
	}
	return merged, nil
}

func verdictID(v agent.Verdict) string     { return v.ID }
func inferenceID(i agent.Inference) string { return i.ID }

// asked keeps the judgements of the IDs a call was given.
func asked[T any](judged []T, ids []string, id func(T) string) []T {
	var out []T
	for _, j := range judged {
		if slices.Contains(ids, id(j)) {
			out = append(out, j)
		}
	}
	return out
}

// partialCheck is the error of a fact-check some of whose calls failed: the
// claims those calls were given, and why.
type partialCheck struct {
	claims []string
	err    error
}

func (p *partialCheck) Error() string { return p.err.Error() }
func (p *partialCheck) Unwrap() error { return p.err }

// uncheckedBy is why claim id went unjudged: the check's error when the whole
// check failed or id was in a call that did, and nil when its call answered.
func uncheckedBy(err error, id string) error {
	var p *partialCheck
	if errors.As(err, &p) && !slices.Contains(p.claims, id) {
		return nil
	}
	return err
}

// factCheckAll runs the fact-check of a as parallel calls (see checkParts)
// and says when it failed. A call that failed leaves its share unverified and
// the rest stands: discarding every verdict for one failed call would report
// a whole run unverified for a quarter of its check.
func (d *Driver) factCheckAll(ctx context.Context, a *agent.Analysis, findings []agent.Finding,
	retrieved bool) (*agent.FactCheckResult, error) {
	parts := checkParts(a, findings, retrieved)
	if len(parts) > 1 {
		d.emit(Event{Type: Info, Phase: "Fact-Check", Detail: fmt.Sprintf(
			"checking %d claims and %d recommendations and conclusions in %d calls at once",
			len(a.Claims), len(units(a)), len(parts))})
	}
	claims := len(a.Claims) > 0
	fc, err := checkAll(ctx, parts, func(ctx context.Context, prompt string) (*agent.FactCheckResult, error) {
		return d.factCheck(ctx, prompt, claims)
	})
	switch {
	case err != nil && fc == nil:
		d.emit(Event{Type: Error, Phase: "Fact-Check",
			Detail: "fact-check unavailable, reporting unverified: " + err.Error()})
	case err != nil:
		d.emit(Event{Type: Error, Phase: "Fact-Check",
			Detail: "part of the fact-check failed, its share reported unverified: " + err.Error()})
	}
	return fc, err
}
