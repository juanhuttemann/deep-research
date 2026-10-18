package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// scopingAssistant is a fakeAssistant whose search can be confined to a
// domain list. statuses scripts the preflight per domain; an absent domain
// passes.
type scopingAssistant struct {
	*fakeAssistant
	statuses map[string]string
	// searchErr is the search's error on the domains scripted search-failed.
	searchErr error
	checked   []string
	scoped    []string
	planned   int
}

func (s *scopingAssistant) CheckDomains(_ context.Context, domains []string) ([]agent.DomainCheck, error) {
	s.checked = domains
	out := make([]agent.DomainCheck, len(domains))
	for i, d := range domains {
		st := s.statuses[d]
		if st == "" {
			st = tools.DomainOK
		}
		out[i] = agent.DomainCheck{Domain: d, Status: st, Pages: 3, Chars: 900, Sample: "https://" + d + "/", Detail: "scripted"}
		if st == tools.DomainSearchFail && s.searchErr != nil {
			out[i] = tools.SearchFailedCheck(d, s.searchErr)
		}
	}
	return out, nil
}

func (s *scopingAssistant) ScopeTo(domains []string) { s.scoped = domains }

func (s *scopingAssistant) Plan(ctx context.Context, q string, n int) ([]agent.SubTopic, error) {
	s.planned++
	return s.fakeAssistant.Plan(ctx, q, n)
}

func runWithDomains(t *testing.T, a agent.Assistant, domains []string) (RunResult, []Event, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	res, err := Run(context.Background(), Options{
		Question: "q", Assistant: a, DepthMode: "quick", OutDir: t.TempDir(),
		JSONL: true, Stdout: &stdout, Stderr: &stderr, Domains: domains,
	})
	var events []Event
	for ln := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		var e Event
		if json.Unmarshal([]byte(ln), &e) == nil {
			events = append(events, e)
		}
	}
	return res, events, err
}

func TestFailingDomainsAreDroppedAndTheRestScopeTheRun(t *testing.T) {
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}, statuses: map[string]string{
		"down.com": tools.DomainUnreadable, "thin.com": tools.DomainSnippetOnly,
	}}
	res, events, err := runWithDomains(t, a, []string{"good.com", "down.com", "thin.com"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"good.com", "thin.com"}; !slices.Equal(a.scoped, want) {
		t.Errorf("scoped to %v, want %v", a.scoped, want)
	}
	if !slices.Equal(res.Report.Domains, a.scoped) {
		t.Errorf("result records %v, want the domains used %v", res.Report.Domains, a.scoped)
	}
	// The plan keeps the list as asked, so a resume checks every one again.
	if len(res.Plan.Domains) != 3 {
		t.Errorf("plan domains = %v, want all three asked for", res.Plan.Domains)
	}
	var checks []string
	for _, e := range events {
		if e.Type == SourceCheck {
			checks = append(checks, e.Check.Domain+":"+e.Check.Status)
		}
	}
	if want := []string{"good.com:ok", "down.com:unreadable", "thin.com:snippet-only"}; !slices.Equal(checks, want) {
		t.Errorf("source events = %v, want %v", checks, want)
	}
}

// A list with nothing usable stops before the planner: a report from no
// sources is not worth a model call.
func TestNoUsableDomainStopsBeforePlanning(t *testing.T) {
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}, statuses: map[string]string{"down.com": tools.DomainUnindexed}}
	_, _, err := runWithDomains(t, a, []string{"down.com"})
	if !errors.Is(err, ErrNoUsableSources) {
		t.Fatalf("err = %v, want ErrNoUsableSources", err)
	}
	if a.planned != 0 {
		t.Errorf("planner called %d times; the run should stop before it", a.planned)
	}
	if !strings.Contains(err.Error(), "down.com unindexed") {
		t.Errorf("err = %q, want each domain and why named", err)
	}
}

func TestDomainsNeedAnAssistantThatCanScope(t *testing.T) {
	_, _, err := runWithDomains(t, &fakeAssistant{}, []string{"go.dev"})
	if !errors.Is(err, agent.ErrNoScopedSearch) {
		t.Fatalf("err = %v, want ErrNoScopedSearch", err)
	}
}

func TestAHandedPlanBringsItsDomains(t *testing.T) {
	a := &scopingAssistant{fakeAssistant: &fakeAssistant{}}
	plan := PlanFor("q", depthMode("quick"), []agent.SubTopic{{Name: "Alpha"}}, 0)
	plan.Domains = []string{"go.dev"}
	var stdout bytes.Buffer
	if _, err := Run(context.Background(), Options{
		Assistant: a, Plan: plan, OutDir: t.TempDir(), JSONL: true, Stdout: &stdout, Stderr: &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(a.checked, []string{"go.dev"}) || !slices.Equal(a.scoped, []string{"go.dev"}) {
		t.Errorf("checked %v, scoped %v; want the plan's go.dev", a.checked, a.scoped)
	}
}

func TestReadPlanCarriesAndVetsDomains(t *testing.T) {
	p, err := ReadPlan(strings.NewReader(`{"question":"q","depth":{"key":"quick"},"sub_topics":[{"name":"A"}],"domains":["https://www.Go.dev/doc"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Domains, []string{"go.dev"}) {
		t.Errorf("domains = %v", p.Domains)
	}
	if _, err := ReadPlan(strings.NewReader(`{"question":"q","depth":{"key":"quick"},"sub_topics":[{"name":"A"}],"domains":["localhost"]}`)); err == nil {
		t.Error("an edited plan naming localhost was accepted")
	}
}

func TestSourceTableNamesEveryDomain(t *testing.T) {
	out := sourceTable([]agent.DomainCheck{
		{Domain: "go.dev", Status: tools.DomainOK, Pages: 4, Chars: 1200, Sample: "https://go.dev/doc"},
		{Domain: "x.com", Status: tools.DomainUnindexed, Detail: "searxng returned no pages on it"},
	}, 1)
	for _, want := range []string{"✓ go.dev", "4 pages found", "✗ x.com", "unindexed — searxng returned no pages", "Researching 1 of 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

// A report from chosen sites says so: its .md (and the PDF typeset from it)
// and the .json the audit view reads.
func TestReportNamesTheSitesItWasLimitedTo(t *testing.T) {
	res := &agent.ResearchResult{Question: "q", Domains: []string{"go.dev", "pkg.go.dev"}, Summary: &agent.Summary{Report: "r"}}
	if md := MarkdownReport(res); !strings.Contains(md, "**Sources limited to:** go.dev, pkg.go.dev") {
		t.Errorf("markdown does not name the sites:\n%s", md)
	}
	if m := BuildMeta(res, nil, "quick", 0, 0); !slices.Equal(m.Domains, res.Domains) {
		t.Errorf("meta domains = %v", m.Domains)
	}
	if md := MarkdownReport(&agent.ResearchResult{Question: "q", Summary: &agent.Summary{Report: "r"}}); strings.Contains(md, "limited to") {
		t.Error("an open-web report claims a site list")
	}
}
