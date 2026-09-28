//go:build eval

package ui

// The checker eval: the live fact-check and the governance code run on
// frozen analyses (testdata/checker) whose right outcome is known, so a
// change to the checker's prompt, the model or the governance is measured on
// its own. A replay of a recorded run re-runs the analysis too, and two
// replays then compare different claims and recommendations as much as two
// checkers. It spends one model request per case and repeat, and runs only
// through `make eval-replay`, never in `make verify`.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
)

// evalScore counts outcomes against expectations. A false approval (a
// recommendation approved that should be blocked) and a false accept (a
// claim supported that should not be) are what reach a reader as fact.
type evalScore struct {
	claimsRight, falseAccepts, falseRejects int
	recsRight, falseApprovals, falseBlocks  int
	// rawWrong counts inference judgements that were wrong whatever the
	// governed outcome: a recommendation that should not follow, judged to
	// follow and blocked only because a premise failed, is still a checker
	// that got the inference wrong.
	rawWrong int
	errors   int
}

func (s evalScore) String() string {
	return fmt.Sprintf("claims right %d, false accepts %d, false rejects %d; recommendations right %d, false approvals %d, false blocks %d; wrong inference judgements %d; errors %d",
		s.claimsRight, s.falseAccepts, s.falseRejects, s.recsRight, s.falseApprovals, s.falseBlocks, s.rawWrong, s.errors)
}

func TestEvalChecker(t *testing.T) {
	cases := loadCheckerCases(t)
	repeat, _ := strconv.Atoi(os.Getenv("EVAL_REPEAT"))
	repeat = max(repeat, 1)
	t.Chdir("../..") // config and .env are found from the repository root
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	asst, err := agent.New(cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	asst.SetProgress(func(string) {})

	var total evalScore
	for _, c := range cases {
		// EVAL_CASE runs the cases whose name contains it, to measure one
		// failure without paying for the rest.
		if only := os.Getenv("EVAL_CASE"); only != "" && !strings.Contains(c.Name, only) {
			continue
		}
		for run := range repeat {
			s, detail := evalCase(t.Context(), asst, c)
			total = addScore(total, s)
			t.Logf("%-20s run %d: %s%s", c.Name, run+1, s, detail)
		}
	}
	t.Logf("TOTAL over %d cases x %d: %s", len(cases), repeat, total)
}

// evalCase runs the fact-check and governance on one case and scores them.
func evalCase(ctx context.Context, asst agent.Assistant, c checkerCase) (evalScore, string) {
	a := &agent.Analysis{Claims: slices.Clone(c.Claims), Recommendations: slices.Clone(c.Recommendations)}
	checkAnalysis(a, c.Findings)
	fc, err := asst.FactCheck(ctx, factCheckPrompt(claimsToCheck(a), c.Findings, true, a.Claims))
	if err != nil {
		return evalScore{errors: 1}, " (" + err.Error() + ")"
	}
	govern(a, fc, nil, c.Findings)
	var s evalScore
	misses := scoreClaims(&s, c, a)
	misses = append(misses, scoreInferences(&s, c, fc)...)
	misses = append(misses, scoreRecommendations(&s, c, a)...)
	if len(misses) == 0 {
		return s, ""
	}
	return s, "\n    " + strings.Join(misses, "\n    ")
}

func scoreClaims(s *evalScore, c checkerCase, a *agent.Analysis) (misses []string) {
	status := map[string]string{}
	for _, cl := range a.Claims {
		status[cl.ID] = cl.Status
	}
	for _, id := range c.Expect.Supported {
		if status[id] == statusSupported {
			s.claimsRight++
		} else {
			s.falseRejects++
			misses = append(misses, id+" rejected ("+status[id]+")")
		}
	}
	for _, id := range c.Expect.NotSupported {
		if status[id] == statusSupported {
			s.falseAccepts++
			misses = append(misses, id+" accepted")
		} else {
			s.claimsRight++
		}
	}
	return misses
}

// scoreInferences scores the checker's raw "follows", whatever governance
// then made of it.
func scoreInferences(s *evalScore, c checkerCase, fc *agent.FactCheckResult) (misses []string) {
	follows := map[string]bool{}
	for _, inf := range fc.Inferences {
		follows[inf.ID] = inf.Follows
	}
	for _, id := range c.Expect.Approved {
		if !follows[id] {
			s.rawWrong++
			misses = append(misses, id+" judged not to follow")
		}
	}
	for _, id := range c.Expect.Blocked {
		if follows[id] && inferenceOnly(c, id) {
			s.rawWrong++
			misses = append(misses, id+" judged to follow")
		}
	}
	return misses
}

func scoreRecommendations(s *evalScore, c checkerCase, a *agent.Analysis) (misses []string) {
	for _, id := range c.Expect.Approved {
		if b := a.Recommendations[mustIndex(id)].Blocked; b == "" {
			s.recsRight++
		} else {
			s.falseBlocks++
			misses = append(misses, id+" blocked: "+b)
		}
	}
	for _, id := range c.Expect.Blocked {
		if a.Recommendations[mustIndex(id)].Blocked == "" {
			s.falseApprovals++
			misses = append(misses, id+" approved")
		} else {
			s.recsRight++
		}
	}
	return misses
}

// inferenceOnly reports whether an expected block rests on the inference: a
// recommendation expected blocked because a premise fails may well follow
// from its premises.
func inferenceOnly(c checkerCase, id string) bool {
	for _, cl := range c.Recommendations[mustIndex(id)].Claims {
		if slices.Contains(c.Expect.NotSupported, cl) {
			return false
		}
	}
	return true
}

func mustIndex(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "r"))
	return n - 1
}

func addScore(a, b evalScore) evalScore {
	return evalScore{
		claimsRight: a.claimsRight + b.claimsRight, falseAccepts: a.falseAccepts + b.falseAccepts,
		falseRejects: a.falseRejects + b.falseRejects, recsRight: a.recsRight + b.recsRight,
		falseApprovals: a.falseApprovals + b.falseApprovals, falseBlocks: a.falseBlocks + b.falseBlocks,
		rawWrong: a.rawWrong + b.rawWrong, errors: a.errors + b.errors,
	}
}
