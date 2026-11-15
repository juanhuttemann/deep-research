package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

const (
	logURL    = "https://portofvelmar.example/operations/log-2025"
	briefURL  = "https://velmar-business-review.example/port-2025"
	logPage   = "Port of Velmar operations log, calendar year 2025. Containers handled by quarter: Q1 2025: 61,200. Q2 2025: 58,900. Q3 2025: 63,400. Q4 2025: 64,500."
	briefPage = "The Port of Velmar handled 412,000 containers in 2025, its busiest year yet."
)

func velmarFindings() []agent.Finding {
	return []agent.Finding{
		{URL: logURL, Title: "Operations log 2025", Content: logPage, Status: "ok"},
		{URL: briefURL, Title: "Port of Velmar: 2025 in review", Content: briefPage, Status: "ok"},
	}
}

// velmarAnalysis is the quarterly log as four numeric claims, their sum,
// and two claims stating a total: the sum's, and the brief's.
func velmarAnalysis() *agent.Analysis {
	q := func(id, quarter, v string) agent.Claim {
		return agent.Claim{ID: id, Text: "The port handled " + v + " containers in " + quarter + " 2025.",
			Value: v, Unit: "containers", Scope: quarter + " 2025", Sources: []string{logURL}}
	}
	return &agent.Analysis{
		Claims: []agent.Claim{
			{ID: "c1", Text: "The port handled 412,000 containers in 2025.", Sources: []string{briefURL},
				Value: "412,000", Unit: "containers", Computed: "m1"},
			q("c2", "Q1", "61,200"), q("c3", "Q2", "58,900"), q("c4", "Q3", "63,400"), q("c5", "Q4", "64,500"),
			{ID: "c6", Text: "The port handled 248,000 containers in 2025, the sum of its quarters.",
				Value: "248,000", Unit: "containers", Computed: "m1"},
		},
		Computations: []agent.Computation{{ID: "m1", Label: "2025 total", Op: "sum", Inputs: []string{"c2", "c3", "c4", "c5"}}},
		Charts: []agent.Chart{{ID: "g1", Title: "Containers by quarter", Points: []agent.ChartPoint{
			{Label: "Q1", Ref: "c2"}, {Label: "Q2", Ref: "c3"}, {Label: "Q3", Ref: "c4"}, {Label: "Q4", Ref: "c5"}}}},
		Conclusions: []agent.Recommendation{{Statement: "The port handled 248,000 containers in 2025.", Claims: []string{"c6"}}},
	}
}

func quarterVerdicts(quotes map[string]string) *agent.FactCheckResult {
	fc := &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k1", Follows: true}}}
	for _, id := range []string{"c2", "c3", "c4", "c5"} {
		q := quotes[id]
		fc.Verdicts = append(fc.Verdicts, agent.Verdict{ID: id, Status: statusSupported,
			Evidence: []agent.Evidence{{Source: logURL, Quote: q}}})
	}
	return fc
}

var quarterQuotes = map[string]string{"c2": "Q1 2025: 61,200", "c3": "Q2 2025: 58,900", "c4": "Q3 2025: 63,400", "c5": "Q4 2025: 64,500"}

func claimByID(a *agent.Analysis, id string) agent.Claim {
	for _, c := range a.Claims {
		if c.ID == id {
			return c
		}
	}
	return agent.Claim{}
}

func TestComputedClaimsAreDecidedByTheArithmetic(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	m := a.Computations[0]
	if !m.Valid || m.Result != "248000" || m.Formula != "c2 + c3 + c4 + c5" || m.Unit != "containers" {
		t.Fatalf("m1 = %+v, want a valid 248000 containers", m)
	}
	if c := claimByID(a, "c6"); c.Status != statusSupported || !strings.Contains(c.Note, "248,000") {
		t.Errorf("c6 (states the sum) = %s: %s, want supported", c.Status, c.Note)
	}
	// The brief's total, however exactly quoted, is not the records' sum.
	if c := claimByID(a, "c1"); c.Status != statusContradicted || !strings.Contains(c.Note, "412,000") {
		t.Errorf("c1 (states 412,000) = %s: %s, want contradicted", c.Status, c.Note)
	}
	if got := a.Conclusions[0].Blocked; got != "" {
		t.Errorf("the conclusion on c6 was blocked: %s", got)
	}
	if ch := a.Charts[0]; ch.File == "" || ch.Unit != "containers" || ch.Points[0].Value != "61200" {
		t.Errorf("chart = %+v, want it drawn from the quarters", ch)
	}
	// Its sources are its inputs': the citations and the audit link them.
	if c := claimByID(a, "c6"); len(c.Sources) != 1 || c.Sources[0] != logURL {
		t.Errorf("c6 sources = %v, want the log's page", c.Sources)
	}
}

func TestAValueNoQuoteStatesIsNoInput(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	// The model's figure, not the page's, in the claim and its value alike.
	a.Claims[1].Value, a.Claims[1].Text = "61,300", "The port handled 61,300 containers in Q1 2025."
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "do not state its figure 61,300") {
		t.Errorf("m1 = %+v, want it invalid on c2's unquoted value", m)
	}
	if c := claimByID(a, "c6"); c.Status != statusInsufficient {
		t.Errorf("c6 = %s, want insufficient: its computation has no valid input", c.Status)
	}
	if a.Conclusions[0].Blocked == "" {
		t.Error("the conclusion on c6 stands on arithmetic over an unquoted figure")
	}
	if p := a.Charts[0].Points[0]; p.Value != "" || !strings.Contains(p.Reason, "61,300") {
		t.Errorf("chart drew an unquoted figure: %+v", a.Charts[0])
	}
}

func TestAnInputTheCheckDidNotSupportInvalidatesTheComputation(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	fc := quarterVerdicts(quarterQuotes)
	fc.Verdicts[3].Status = statusPartial
	governAll(a, fc, nil, findings)
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "c5 did not pass the fact-check (partial)") {
		t.Errorf("m1 = %+v, want it invalid on c5", m)
	}
	// No check at all approves no arithmetic either.
	b := velmarAnalysis()
	checkAnalysis(b, findings)
	governAll(b, nil, nil, findings)
	if b.Computations[0].Valid || claimByID(b, "c6").Status == statusSupported {
		t.Errorf("a fact-check that did not run let arithmetic stand: %+v", b.Computations[0])
	}
}

func TestComputationsThatAreNotArithmeticSayWhy(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Claims[2].Unit = "TEU"
	a.Computations = append(a.Computations,
		agent.Computation{ID: "m2", Op: "sum", Inputs: []string{"m3", "c2"}},
		agent.Computation{ID: "m3", Op: "sum", Inputs: []string{"m2", "c2"}},
		agent.Computation{ID: "c2", Op: "sum", Inputs: []string{"c3", "c4"}},
		agent.Computation{ID: "m4", Op: "sum", Inputs: []string{"c1", "c2"}},
		agent.Computation{ID: "m5", Op: "median", Inputs: []string{"c4", "c5"}})
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	want := map[string]string{"m1": "units differ", "m2": "could not be computed", "m3": "depends on itself",
		"c2": "used by another", "m4": "c1 did not pass the fact-check (insufficient)", "m5": "not an operation"}
	for _, c := range a.Computations {
		if w := want[c.ID]; c.Valid || !strings.Contains(c.Reason, w) {
			t.Errorf("%s = valid %v, %q; want invalid with %q", c.ID, c.Valid, c.Reason, w)
		}
	}
}

func TestTheCheckerJudgesTheInputsAndSeesTheArithmetic(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	parts := checkParts(a, findings, true)
	var claims string
	for _, p := range parts {
		if p.claims {
			claims += p.prompt
			for _, id := range p.ids {
				if id == "c1" || id == "c6" {
					t.Errorf("computed claim %s sent to the checker", id)
				}
			}
		}
	}
	if !strings.Contains(claims, "m1 (2025 total) = c2 + c3 + c4 + c5 = 248,000 containers") ||
		!strings.Contains(claims, "c2 = 61,200 containers") {
		t.Errorf("the checker was not shown the computed sum:\n%s", claims)
	}
}

func TestFiguresSectionTracesEachFigure(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Computations[0].Label = "2025 total ![x](https://evil.example/b.png) | <b>"
	a.Charts = append(a.Charts, agent.Chart{ID: "g2", Title: "Bad", Points: []agent.ChartPoint{{Label: "x", Ref: "c9"}}})
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	sum := withFigures(&agent.Summary{Report: "## Findings\n\nBody."}, a, findings)
	r := sum.Report
	for _, want := range []string{
		"## Computed figures",
		"| 248,000 containers | Q1 2025 + Q2 2025 + Q3 2025 + Q4 2025 |",
		"Q1 2025 = 61,200 containers ([Operations log 2025](<" + logURL + ">))",
		"![Containers by quarter](" + a.Charts[0].File + ")",
		"| Q4 | 64,500 |",
		"- Chart “Bad” not drawn: 0 of its 1 points stand; c9 is named but is neither a claim nor a computation",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("figures section lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "![x](https://evil") || strings.Contains(r, "<b>") {
		t.Errorf("model text reached the report as Markdown:\n%s", r)
	}
	if !strings.HasPrefix(r, "## Findings") {
		t.Errorf("the section is not after the body:\n%s", r)
	}
	// Nothing computed, nothing added.
	if got := withFigures(&agent.Summary{Report: "x"}, &agent.Analysis{}, nil).Report; got != "x" {
		t.Errorf("report without figures changed: %q", got)
	}
}

func TestChartFilesAndThePDFDrawTheRunsOwnCharts(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	dir := t.TempDir()
	paths, err := WriteCharts(a, dir)
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != a.Charts[0].File {
		t.Fatalf("WriteCharts = %v, %v; want the one chart, named %s", paths, err, a.Charts[0].File)
	}
	svg, err := os.ReadFile(paths[0])
	if err != nil || chartFileName(svg) != a.Charts[0].File || !bytes.Contains(svg, []byte("Containers by quarter")) {
		t.Fatalf("chart file does not digest to its name or lost its title: %v", err)
	}
	md := "Body.\n\n![Containers](" + a.Charts[0].File + ")\n\n![other](chart-0000000000000000.svg)\n"
	pdf := BuildPDF(md, drawnCharts(a))
	if !bytes.Contains(pdf, []byte(pdfChartSeries+" rg")) {
		t.Error("the PDF did not draw the run's chart")
	}
	if bytes.Contains(BuildPDF(md, nil), []byte(pdfChartSeries+" rg")) {
		t.Error("the PDF drew a chart the run did not give it")
	}
}

// The figure judged is the one the claim's words state: a value beside a
// text that says another number would have let the text stand on it.
func TestAClaimsTextMustStateItsValue(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Claims[5].Text = "The port handled 412,000 containers in 2025." // value still 248,000
	a.Claims[2].Text = "The port handled about sixty thousand containers in Q2 2025."
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "c3 does not itself state the figure") {
		t.Errorf("m1 = %+v, want it invalid on c3's text", m)
	}
	b := velmarAnalysis()
	b.Claims[5].Text = "The port handled 412,000 containers in 2025."
	checkAnalysis(b, findings)
	governAll(b, quarterVerdicts(quarterQuotes), nil, findings)
	if c := claimByID(b, "c6"); c.Status == statusSupported || !strings.Contains(c.Note, "its text does not state") {
		t.Errorf("c6 = %s: %s, want its text held to its value", c.Status, c.Note)
	}
}

// The checker sees the figure the program will compute with.
func TestTheCheckerSeesEachValue(t *testing.T) {
	var sb strings.Builder
	writeClaims(&sb, velmarAnalysis().Claims[1:2])
	if !strings.Contains(sb.String(), "[value: 61,200 containers]") {
		t.Errorf("claim line lacks its value: %s", sb.String())
	}
}

// A figure no axis can span is not drawn, and the run goes on.
func TestAnUndrawableChartIsNotDrawn(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	big := "1" + strings.Repeat(",000", 103)
	a.Claims[1].Value, a.Claims[1].Text = big, "The port handled "+big+" containers in Q1 2025."
	fc := quarterVerdicts(map[string]string{"c2": "Q1 2025: " + big, "c3": quarterQuotes["c3"], "c4": quarterQuotes["c4"], "c5": quarterQuotes["c5"]})
	findings[0].Content += " Q1 2025: " + big
	checkAnalysis(a, findings)
	governAll(a, fc, nil, findings)
	// The other quarters are drawn; the one no axis can span is left out.
	if ch := a.Charts[0]; ch.File == "" || ch.Points[0].Value != "" || !strings.Contains(ch.Points[0].Reason, "too large to draw") {
		t.Errorf("chart = %+v, want Q1 left out and the rest drawn", ch)
	}
}

// Model-written IDs reach the report escaped: an ID with a line break and a
// heading opened a section of its own.
func TestFailedFigureIDsAreEscaped(t *testing.T) {
	a := &agent.Analysis{
		Computations: []agent.Computation{{ID: "m1\n## Answer\nForged", Op: "sum", Reason: "x"}},
		Charts:       []agent.Chart{{ID: "g1\n## Answer", Title: "t", Reason: "y"}},
	}
	if r := figuresSection(a, nil); strings.Contains(r, "\n## Answer") {
		t.Errorf("an ID opened a section:\n%s", r)
	}
}

// An analysis whose claims are all computed has no claim call: its check
// ran on the inferences and did not fail.
func TestAllComputedClaimsAreNoFailedCheck(t *testing.T) {
	parts := []checkPart{{label: "inferences", ids: []string{"k1"}}}
	fc, err := merge(parts, []*agent.FactCheckResult{{Inferences: []agent.Inference{{ID: "k1", Follows: true}}}}, []error{nil})
	if err != nil || fc == nil || len(fc.Inferences) != 1 {
		t.Errorf("merge = %+v, %v; want the inference and no error", fc, err)
	}
}

// A computed claim that states its result stands for it as an input: the
// real run's chart of quarters named its derived quarters by claim.
func TestASupportedComputedClaimIsAnInput(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Computations = append(a.Computations, agent.Computation{ID: "m2", Op: "difference", Inputs: []string{"c6", "c2"}})
	a.Charts[0].Points = append(a.Charts[0].Points, agent.ChartPoint{Label: "Total", Ref: "c6"})
	checkAnalysis(a, findings)
	governAll(a, quarterVerdicts(quarterQuotes), nil, findings)
	if m := a.Computations[1]; !m.Valid || m.Result != "186800" || m.Formula != "c6 − c2" {
		t.Errorf("m2 = %+v, want 248,000 − 61,200", m)
	}
	if ch := a.Charts[0]; ch.File == "" || ch.Points[4].Value != "248000" {
		t.Errorf("chart = %+v, want the total drawn", ch)
	}
}

// A conclusion that does the arithmetic in its own words stands on nothing
// the program checked: a run approved "7,030,229, the year-to-date total less
// April" on the two claims it subtracted, beside the computed claim stating
// the same figure, which had failed.
func TestAStatementAddingAFigureIsBlocked(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Conclusions = []agent.Recommendation{
		{Statement: "The port handled 120,100 containers in the first half of 2025, Q1 plus Q2.", Claims: []string{"c2", "c3"}},
		{Statement: "The port handled 61,200 containers in Q1 2025.", Claims: []string{"c2"}},
	}
	checkAnalysis(a, findings)
	fc := quarterVerdicts(quarterQuotes)
	fc.Inferences = []agent.Inference{{ID: "k1", Follows: true}, {ID: "k2", Follows: true}}
	governAll(a, fc, nil, findings)
	if got := a.Conclusions[0].Blocked; !strings.Contains(got, "it states 120,100, which none of its claims states") {
		t.Errorf("k1 blocked = %q, want it blocked on its unstated figure", got)
	}
	if got := a.Conclusions[1].Blocked; got != "" {
		t.Errorf("k2 restates its claim's figure and the year in its scope, blocked: %s", got)
	}
}

// A revision from the repair pass is held to its figures too: one that
// restated the arithmetic it was blocked for passed on the inference alone.
func TestARevisionAddingAFigureIsBlocked(t *testing.T) {
	a := &agent.Analysis{
		Claims: []agent.Claim{{ID: "c2", Text: "The port handled 61,200 containers in Q1 2025.", Status: statusSupported}},
		Conclusions: []agent.Recommendation{
			{Statement: "Q1 was 61,200 plus 1,000.", Claims: []string{"c2"}, Blocked: "it states 1,000"},
			{Statement: "The port handled 62,200 containers in Q1 2025.", Claims: []string{"c2"}, Revises: "k1"},
		},
	}
	judgeRevisions(a, []string{"k2"}, nil, &agent.FactCheckResult{Inferences: []agent.Inference{{ID: "k2", Follows: true}}})
	if got := a.Conclusions[1].Blocked; !strings.Contains(got, "it states 62,200") {
		t.Errorf("revision blocked = %q, want it blocked on 62,200", got)
	}
}

// A run derived Q4 as the year's total less January to September, then
// "checked" that the quarters add up to the year's total, which they do by
// construction, whatever the sources say.
func TestAComputationThatOnlyRestatesAnInputChecksNothing(t *testing.T) {
	const url = "https://stats.example/airport-2024"
	page := "Passengers January to March 2024 were 7,030,229. January to September 2024 were 26,889,799. Total passengers for 2024 34,634,007."
	findings := []agent.Finding{{URL: url, Title: "Airport statistics 2024", Content: page, Status: "ok"}}
	claim := func(id, text, v string) agent.Claim {
		return agent.Claim{ID: id, Text: text, Value: v, Unit: "passengers", Sources: []string{url}}
	}
	a := &agent.Analysis{
		Claims: []agent.Claim{
			claim("c1", "Passengers in January to March 2024 were 7,030,229.", "7,030,229"),
			claim("c2", "Passengers in January to September 2024 were 26,889,799.", "26,889,799"),
			claim("c3", "Passengers in 2024 were 34,634,007.", "34,634,007"),
			{ID: "c6", Text: "Q2 and Q3 2024 together had 19,859,570 passengers.", Value: "19,859,570", Unit: "passengers", Computed: "m1"},
			{ID: "c7", Text: "Q4 2024 had 7,744,208 passengers.", Value: "7,744,208", Unit: "passengers", Computed: "m2"},
			{ID: "c8", Text: "The quarters sum to 34,634,007, the 2024 total.", Value: "34,634,007", Unit: "passengers", Computed: "m3"},
		},
		Computations: []agent.Computation{
			{ID: "m1", Op: "difference", Inputs: []string{"c2", "c1"}},
			{ID: "m2", Op: "difference", Inputs: []string{"c3", "c2"}},
			{ID: "m3", Op: "sum", Inputs: []string{"c1", "c6", "c7"}},
			{ID: "m4", Op: "max", Inputs: []string{"c1", "c7"}}, // returns an input by design
		},
	}
	fc := &agent.FactCheckResult{}
	for id, q := range map[string]string{"c1": "January to March 2024 were 7,030,229", "c2": "January to September 2024 were 26,889,799",
		"c3": "Total passengers for 2024 34,634,007"} {
		fc.Verdicts = append(fc.Verdicts, agent.Verdict{ID: id, Status: statusSupported, Evidence: []agent.Evidence{{Source: url, Quote: q}}})
	}
	checkAnalysis(a, findings)
	governAll(a, fc, nil, findings)
	for _, m := range a.Computations[:2] {
		if !m.Valid {
			t.Errorf("%s = %+v, want valid: a difference of two sources is a real figure", m.ID, m)
		}
	}
	if m := a.Computations[2]; m.Valid || !strings.Contains(m.Reason, "it is c3 by construction") {
		t.Errorf("m3 = %+v, want it refused as c3 restated", m)
	}
	if c := claimByID(a, "c8"); c.Status != statusInsufficient || !strings.Contains(c.Note, "by construction") {
		t.Errorf("c8 = %s: %s, want insufficient: it checks nothing", c.Status, c.Note)
	}
	if !a.Computations[3].Valid {
		t.Errorf("m4 = %+v, want max kept: it returns an input by design", a.Computations[3])
	}
}

// The check is an identity only for linear arithmetic: a product of 0 and 1
// moves one for one with its first input at those values, and is a real
// figure; a mean of one claim with itself is that claim restated.
func TestOnlyLinearArithmeticIsAskedWhetherItRestatesAnInput(t *testing.T) {
	const url = "https://stats.example/x"
	findings := []agent.Finding{{URL: url, Content: "Faults: 0. Units: 1. Units: 7.", Status: "ok"}}
	a := &agent.Analysis{
		Claims: []agent.Claim{
			{ID: "c1", Text: "There were 0 faults.", Value: "0", Unit: "", Sources: []string{url}},
			{ID: "c2", Text: "There was 1 unit.", Value: "1", Unit: "", Sources: []string{url}},
			{ID: "c3", Text: "There were 7 units.", Value: "7", Unit: "", Sources: []string{url}},
		},
		Computations: []agent.Computation{
			{ID: "m1", Op: "product", Inputs: []string{"c1", "c2"}},
			{ID: "m2", Op: "ratio", Inputs: []string{"c1", "c2"}},
			{ID: "m3", Op: "mean", Inputs: []string{"c3", "c3"}},
			{ID: "m4", Op: "sum", Inputs: []string{"c3", "c3"}},
		},
	}
	fc := &agent.FactCheckResult{}
	for id, q := range map[string]string{"c1": "Faults: 0.", "c2": "Units: 1.", "c3": "Units: 7."} {
		fc.Verdicts = append(fc.Verdicts, agent.Verdict{ID: id, Status: statusSupported, Evidence: []agent.Evidence{{Source: url, Quote: q}}})
	}
	checkAnalysis(a, findings)
	governAll(a, fc, nil, findings)
	want := map[string]bool{"m1": true, "m2": true, "m3": false, "m4": true}
	for _, m := range a.Computations {
		if m.Valid != want[m.ID] {
			t.Errorf("%s valid = %v (%s), want %v", m.ID, m.Valid, m.Reason, want[m.ID])
		}
	}
}

// One point a source does not state no longer hides the series: the rest
// are drawn, and the report says which point was left out and why.
func TestAChartDrawsThePointsThatStand(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	fc := quarterVerdicts(quarterQuotes)
	fc.Verdicts[0].Status = statusInsufficient // Q1
	governAll(a, fc, nil, findings)
	ch := a.Charts[0]
	if ch.File == "" || ch.Points[0].Value != "" || ch.Points[0].Reason != "c2 did not pass the fact-check (insufficient)" || ch.Points[1].Value != "58900" {
		t.Fatalf("chart = %+v, want Q2-Q4 drawn and Q1 left out", ch)
	}
	r := figuresSection(a, findings)
	if !strings.Contains(r, "- Chart “Containers by quarter”, point “Q1” left out: “The port handled 61,200 containers in Q1 2025” did not pass the fact-check (insufficient)") ||
		strings.Contains(r, "| Q1 |") {
		t.Errorf("section does not say Q1 was left out, or lists it:\n%s", r)
	}
	// Fewer than two standing points is no chart.
	fc.Verdicts[1].Status, fc.Verdicts[2].Status = statusInsufficient, statusInsufficient
	b := velmarAnalysis()
	checkAnalysis(b, findings)
	governAll(b, fc, nil, findings)
	if ch := b.Charts[0]; ch.File != "" || !strings.HasPrefix(ch.Reason, "1 of its 4 points stand") {
		t.Errorf("chart = %+v, want it not drawn", ch)
	}
}

func medalAnalysis() (*agent.Analysis, []agent.Finding, *agent.FactCheckResult) {
	const url = "https://medals.example/paris-2024"
	page := "Paris 2024 medal table. United States 40 gold. China 40 gold. Japan 20 gold. Australia 18 gold. France 16 gold."
	findings := []agent.Finding{{URL: url, Title: "Medal table", Content: page, Status: "ok"}}
	a := &agent.Analysis{}
	fc := &agent.FactCheckResult{}
	for i, row := range [][2]string{{"United States", "40"}, {"China", "40"}, {"Japan", "20"}, {"Australia", "18"}, {"France", "16"}} {
		id := "c" + strconv.Itoa(i+1)
		// As a model wrote them: no value, no unit; the figure is in the text.
		a.Claims = append(a.Claims, agent.Claim{ID: id, Text: row[0] + " won " + row[1] + " gold medals at the Paris 2024 Olympics",
			Option: row[0], Criterion: "Gold Medals", Scope: "Paris 2024 Olympics", Sources: []string{url}})
		fc.Verdicts = append(fc.Verdicts, agent.Verdict{ID: id, Status: statusSupported,
			Evidence: []agent.Evidence{{Source: url, Quote: row[0] + " " + row[1] + " gold."}}})
	}
	return a, findings, fc
}

// A model named every computation over five medal counts and gave no claim
// a value, and asked for no chart: nothing was computed or drawn. The
// numbers were in the claims' own words, and charting them is the
// program's job, not the model's to remember.
func TestFiguresInTheClaimsAreComputedAndChartedUnasked(t *testing.T) {
	a, findings, fc := medalAnalysis()
	a.Computations = []agent.Computation{{ID: "m1", Op: "sum", Inputs: []string{"c1", "c2", "c3", "c4", "c5"}}}
	checkAnalysis(a, findings)
	governAll(a, fc, nil, findings)
	// Japan's 20 is not mistaken for part of the scope's 2024.
	if c := claimByID(a, "c3"); c.Value != "20" {
		t.Errorf("c3 value = %q, want 20 read from its text", c.Value)
	}
	if m := a.Computations[0]; !m.Valid || m.Result != "134" {
		t.Errorf("m1 = %+v, want 134", m)
	}
	if len(a.Charts) != 1 || !a.Charts[0].Auto || a.Charts[0].File == "" || a.Charts[0].Kind != "bar" {
		t.Fatalf("charts = %+v, want one bar chart drawn unasked", a.Charts)
	}
	ch := a.Charts[0]
	if ch.Title != "Gold Medals, Paris 2024 Olympics" || ch.Points[2].Label != "Japan" || ch.Points[2].Value != "20" {
		t.Errorf("chart = %+v", ch)
	}
	// Governing again rebuilds it rather than adding a second one.
	governAll(a, fc, nil, findings)
	if len(a.Charts) != 1 {
		t.Errorf("a second govern added charts: %d", len(a.Charts))
	}
}

func TestNoFigureIsGuessedFromAmbiguousText(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{
		{ID: "c1", Text: "In 2024 the US won 40 golds and 126 medals in all."},
		{ID: "c2", Text: "The 2024 Games were held in Paris."},
		{ID: "c3", Text: "Japan won 20 golds at Paris 2024.", Scope: "Paris 2024"},
	}}
	inferValues(a)
	if a.Claims[0].Value != "" || a.Claims[1].Value != "" || a.Claims[2].Value != "20" {
		t.Errorf("values = %q %q %q, want none, none, 20", a.Claims[0].Value, a.Claims[1].Value, a.Claims[2].Value)
	}
}

func TestAutoChartsGroupLikeFiguresAndLabelThem(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{
		// One subject over years: labelled by the year in its scope, a line.
		{ID: "c1", Option: "Germany", Criterion: "GDP growth", Scope: "Germany, calendar year 2023", Value: "-0.87", Unit: "%"},
		{ID: "c2", Option: "Germany", Criterion: "GDP growth", Scope: "Germany, calendar year 2024", Value: "-0.50", Unit: "%"},
		{ID: "c3", Option: "Germany", Criterion: "GDP growth", Scope: "2024, base year 2015", Text: "growth in 2024 was -0.50%", Value: "-0.50", Unit: "%"},
		// A lone figure of another quantity, and one with nothing to group by.
		{ID: "c4", Option: "Germany", Criterion: "Population", Value: "84,000,000"},
		{ID: "c5", Value: "7"}, {ID: "c6", Value: "8"},
	}}
	got := autoCharts(a)
	if len(got) != 1 {
		t.Fatalf("auto charts = %+v, want the one GDP series", got)
	}
	ch := got[0]
	if ch.Title != "Germany: GDP growth" {
		t.Errorf("title = %q, want the one subject named", ch.Title)
	}
	if ch.Kind != "line" || len(ch.Points) != 2 || ch.Points[0].Label != "2023" || ch.Points[1].Label != "2024" {
		t.Errorf("chart = %+v, want a line over 2023 and 2024, the second 2024 once", ch)
	}
	// What the analyzer charted itself is not charted again.
	a.Charts = []agent.Chart{{ID: "g1", Points: []agent.ChartPoint{{Ref: "c1"}, {Ref: "c2"}, {Ref: "c3"}}}}
	if got := autoCharts(a); len(got) != 0 {
		t.Errorf("charted again: %+v", got)
	}
}

func TestOnlyTheCauseOfAFailureIsListed(t *testing.T) {
	a := &agent.Analysis{Computations: []agent.Computation{
		{ID: "m1", Label: "Total", Op: "sum", Reason: "c2 is insufficient"},
		{ID: "m2", Label: "Share", Op: "ratio", Reason: "m1 could not be computed"},
	}}
	r := figuresSection(a, nil)
	if !strings.Contains(r, "- Total: c2 is insufficient") || strings.Contains(r, "Share") {
		t.Errorf("section:\n%s", r)
	}
}

// A model numbered its rankings c1-c5 and then its heights c1-c4. The
// copies were renamed (c1.6, c4.9) and its "c1 - c4" went to the two
// rankings, sentences with no number in them, so nothing was computed.
func TestAReusedIDMeansTheCopyWithAFigure(t *testing.T) {
	const url = "https://towers.example/tallest"
	page := "Burj Khalifa is the tallest building in the world. Ping An Finance Center ranks fifth. " +
		"Burj Khalifa: 828 metres. Ping An Finance Center: 599.1 metres."
	findings := []agent.Finding{{URL: url, Content: page, Status: "ok"}}
	a := &agent.Analysis{
		Claims: []agent.Claim{
			{ID: "c1", Text: "Burj Khalifa is the tallest building in the world", Sources: []string{url}},
			{ID: "c4", Text: "Ping An Finance Center ranks fifth", Sources: []string{url}},
			{ID: "c1", Text: "Burj Khalifa has an architectural height of 828 metres", Option: "Burj Khalifa", Criterion: "Height", Sources: []string{url}},
			{ID: "c4", Text: "Ping An Finance Center has an architectural height of 599.1 metres", Option: "Ping An Finance Center", Criterion: "Height", Sources: []string{url}},
		},
		Computations: []agent.Computation{{ID: "m1", Op: "difference", Inputs: []string{"c1", "c4"}}},
	}
	fc := &agent.FactCheckResult{}
	checkAnalysis(a, findings)
	for _, c := range a.Claims {
		q := map[string]string{"Burj Khalifa is the tallest": "Burj Khalifa is the tallest building in the world.",
			"Ping An Finance Center ranks": "Ping An Finance Center ranks fifth.",
			"Burj Khalifa has":             "Burj Khalifa: 828 metres.", "Ping An Finance Center has": "Ping An Finance Center: 599.1 metres."}
		for prefix, quote := range q {
			if strings.HasPrefix(c.Text, prefix) {
				fc.Verdicts = append(fc.Verdicts, agent.Verdict{ID: c.ID, Status: statusSupported,
					Evidence: []agent.Evidence{{Source: url, Quote: quote}}})
			}
		}
	}
	governAll(a, fc, nil, findings)
	if m := a.Computations[0]; !m.Valid || m.Result != "228.9" {
		t.Errorf("m1 = %+v, want 828 − 599.1 = 228.9 over the height claims", m)
	}
}

// The report is read without the run's audit: no claim or computation ID,
// and no "finding 5" from the checker's prompt, may reach it unexplained.
// A PDF said "m1 (Difference between …): c1 states no value" and "c5: …
// The ranking list in finding 5 is truncated".
func TestTheReportNamesWhatItCitesWithoutIDs(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	a.Computations = append(a.Computations, agent.Computation{ID: "m2", Label: "Q1 less Q9", Op: "difference", Inputs: []string{"c2", "c99"}})
	checkAnalysis(a, findings)
	fc := quarterVerdicts(quarterQuotes)
	fc.Verdicts[3].Status, fc.Verdicts[3].Reason = statusInsufficient, "The table in finding 1 stops before Q4; finding 2 gives only the year."
	governAll(a, fc, nil, findings)
	sum := withFigures(withAnswer(&agent.Summary{Report: "## Findings\n\nBody."}, a), a, findings)
	report := MarkdownReport(&agent.ResearchResult{Question: "q", Findings: findings, Analysis: a, FactCheck: fc, Summary: sum})
	ids := regexp.MustCompile(`\b(?:c[0-9]|m[0-9]|k[0-9]|g[0-9]|auto[0-9])`)
	for _, ln := range strings.Split(report, "\n") {
		if strings.Contains(ln, "c99") {
			continue // a dangling reference the analyzer made: there is nothing to name it by
		}
		if ids.MatchString(ln) || regexp.MustCompile(`(?i)\bfindings? \d`).MatchString(ln) {
			t.Errorf("report line cites an ID or a finding number:\n%s", ln)
		}
	}
	for _, want := range []string{"“Operations log 2025”", "Q4 2025", "did not pass the fact-check"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestNamesReplaceIDsOnlyAsWholeWords(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{
		{ID: "c1", Text: "Burj Khalifa is the tallest building", Option: "Burj Khalifa", Criterion: "Rank"},
		{ID: "c1.6", Text: "Burj Khalifa is 828 m tall", Option: "Burj Khalifa", Criterion: "Height"},
		{ID: "c12", Text: "Other", Option: "Other tower"},
	}}
	n := newNamer(a)
	got := n.say("c1.6 − c12 (not c1x, but c1)", false)
	if got != "Burj Khalifa Height − Other tower (not c1x, but Burj Khalifa Rank)" {
		t.Errorf("say = %q", got)
	}
	if got := n.say("c1 states no figure", true); got != "“Burj Khalifa is the tallest building” states no figure" {
		t.Errorf("full = %q", got)
	}
	f := []agent.Finding{{Title: "Tallest list"}, {Title: "Atlas"}}
	if got := nameFindings("finding 1 is cut; findings 1 and 2 agree; finding 9 is none", f); got != "“Tallest list” is cut; “Tallest list” and “Atlas” agree; finding 9 is none" {
		t.Errorf("nameFindings = %q", got)
	}
}

func TestNamesLeaveURLsUnitsAndQuotedWordsAlone(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{{ID: "c1", Text: "x", Option: "Tower"}},
		Computations: []agent.Computation{{ID: "m2", Label: "Area"}}}
	n := newNamer(a)
	for in, want := range map[string]string{
		"see https://example.com/c1?ref=m2": "see https://example.com/c1?ref=m2",
		"[page](https://example.com/c1)":    "[page](https://example.com/c1)",
		"a flat of 10 m2 and c1":            "a flat of 10 m2 and Tower",
		"m2 failed: c1 is out":              "Area failed: Tower is out",
	} {
		if got := n.say(in, false); got != want {
			t.Errorf("say(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Q1 was 61,200 (c2). The total (m1, c3) is set [c4] (finding 3).": "Q1 was 61,200. The total is set.",
		"Measured in (cm) and [k]":                                        "Measured in (cm) and [k]",
	} {
		if got := readerText(in); got != want {
			t.Errorf("readerText(%q) = %q, want %q", in, got, want)
		}
	}
}

// A claim the check did not confirm is listed with what the check found, in
// words: "? unverified" left the reader to guess at the "?", and did not
// tell a contradicted claim from one no passage settled.
func TestTheFactCheckSaysWhatItFoundInWords(t *testing.T) {
	a := &agent.Analysis{Claims: []agent.Claim{
		{ID: "c1", Text: "A is 5", Status: statusSupported},
		{ID: "c2", Text: "B is 6", Status: statusPartial, Note: "only for 2023"},
		{ID: "c3", Text: "C is 7", Status: statusContradicted, Note: "the table gives 8"},
		{ID: "c4", Text: "D is 9", Status: statusInsufficient, Note: "quote_not_located: no passage names D"},
		{ID: "c5", Text: "E is 1", Status: statusInsufficient, Note: "fact_check_unavailable: timeout"},
	}}
	fc := &agent.FactCheckResult{}
	fillVerdictLists(fc, claimEntries(a.Claims))
	var sb strings.Builder
	writeFactCheck(&sb, fc, a)
	got := sb.String()
	for _, want := range []string{
		"Confirmed against their sources: 1 · not confirmed: 3 · contradicted or disputed: 1",
		"- **Partly supported:** “B is 6” — only for 2023",
		"- **Contradicted:** “C is 7” — the table gives 8",
		"- **Not established:** “D is 9” — no passage names D",
		"- **Not checked:** “E is 1” — timeout",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fact-check section lacks %q:\n%s", want, got)
		}
	}
	for _, glyph := range []string{"?", "✗", "⚠", "quote_not_located", "c2"} {
		if strings.Contains(got, glyph) {
			t.Errorf("fact-check section has %q:\n%s", glyph, got)
		}
	}
}
