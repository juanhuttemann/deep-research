package ui

import (
	"bytes"
	"os"
	"path/filepath"
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
			Value: v, Unit: "containers", Sources: []string{logURL}}
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
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "c2's value 61,300") {
		t.Errorf("m1 = %+v, want it invalid on c2's unquoted value", m)
	}
	if c := claimByID(a, "c6"); c.Status != statusInsufficient {
		t.Errorf("c6 = %s, want insufficient: its computation has no valid input", c.Status)
	}
	if a.Conclusions[0].Blocked == "" {
		t.Error("the conclusion on c6 stands on arithmetic over an unquoted figure")
	}
	if a.Charts[0].File != "" {
		t.Errorf("chart drawn from an unquoted figure: %+v", a.Charts[0])
	}
}

func TestAnInputTheCheckDidNotSupportInvalidatesTheComputation(t *testing.T) {
	a, findings := velmarAnalysis(), velmarFindings()
	checkAnalysis(a, findings)
	fc := quarterVerdicts(quarterQuotes)
	fc.Verdicts[3].Status = statusPartial
	governAll(a, fc, nil, findings)
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "c5 is partial") {
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
	want := map[string]string{"m1": "units differ", "m2": "not computed", "m3": "depends on itself",
		"c2": "used by another", "m4": "c1 is insufficient", "m5": "not an operation"}
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
		"| 248,000 containers | m1 = c2 + c3 + c4 + c5 |",
		"c2 = 61,200 containers ([Operations log 2025](<" + logURL + ">))",
		"![Containers by quarter](" + a.Charts[0].File + ")",
		"| Q4 | 64,500 |",
		"- g2 (Bad): c9 is neither a claim nor a computation",
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
	if m := a.Computations[0]; m.Valid || !strings.Contains(m.Reason, "c3's text does not state its value") {
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
	if ch := a.Charts[0]; ch.File != "" || !strings.Contains(ch.Reason, "too large to draw") {
		t.Errorf("chart = %+v, want it not drawn", ch)
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
