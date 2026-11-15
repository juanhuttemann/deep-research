package ui

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/compute"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// The analysis's arithmetic, done by the program. The analyzer marks the
// claims that state a figure (value, unit), names the operations to run
// over them (computations) and the series to draw (charts); code evaluates
// them and decides what may stand:
//
//   - an input is a claim the fact-check supported, whose stated value is a
//     number in one of the passages it was supported on;
//   - a computation is valid when every input is, all the way down;
//   - a claim that states a computation's result (computed) is decided here,
//     not by the checker: supported when its figure is the result at its own
//     precision and the computation is valid, contradicted when it differs.
//
// Before the check the same evaluation runs over the stated values alone,
// so the checker sees what the figures add up to: the arithmetic needs no
// verdict, only its inputs' standing does.

// evaluated is a computation's outcome: its result, or why there is none.
type evaluated struct {
	res compute.Result
	err string
}

// evaluator works out the analysis's computations, each once.
type evaluator struct {
	claims map[string]*agent.Claim
	comps  map[string]*agent.Computation
	// usable says why a claim's stated value may not be used yet, "" when
	// it may: before the check any value that parses, after it only one the
	// check supported.
	usable func(*agent.Claim) string
	done   map[string]evaluated
	busy   map[string]bool
	// nudged is set on a probe (see echo): the claim whose value is taken
	// one more than it states. A probe asks only how a result moves, so it
	// takes computed claims as their computations without deciding them.
	nudged string
	probe  bool
}

func newEvaluator(a *agent.Analysis, usable func(*agent.Claim) string) *evaluator {
	e := &evaluator{claims: map[string]*agent.Claim{}, comps: map[string]*agent.Computation{}, usable: usable,
		done: map[string]evaluated{}, busy: map[string]bool{}}
	for i := range a.Claims {
		if _, ok := e.claims[a.Claims[i].ID]; !ok {
			e.claims[a.Claims[i].ID] = &a.Claims[i]
		}
	}
	for i := range a.Computations {
		c := &a.Computations[i]
		if _, clash := e.claims[c.ID]; !clash && e.comps[c.ID] == nil {
			e.comps[c.ID] = c
		}
	}
	return e
}

// input is ref as an operand, or why it cannot be one.
func (e *evaluator) input(ref string) (compute.Input, string) {
	if c := e.comps[ref]; c != nil {
		if e.busy[ref] {
			return compute.Input{}, ref + " depends on itself"
		}
		r := e.eval(c)
		if r.err != "" {
			return compute.Input{}, ref + " is not computed"
		}
		return compute.Input{ID: ref, Value: r.res.Value, Unit: r.res.Unit}, ""
	}
	c := e.claims[ref]
	switch {
	case c == nil:
		return compute.Input{}, ref + " is neither a claim nor a computation"
	case c.Computed != "":
		// It stands for its computation's exact result, once its own figure is
		// found to state it: models name the claim ("Q2" in a chart of
		// quarters) more often than the computation behind it.
		if status, note := e.decide(c); !e.probe && status != statusSupported {
			return compute.Input{}, ref + " is " + status + ": " + note
		}
		x, why := e.input(c.Computed)
		x.ID = ref
		return x, why
	case strings.TrimSpace(c.Value) == "":
		return compute.Input{}, ref + " states no value"
	}
	n, ok := compute.ParseValue(c.Value)
	if !ok {
		return compute.Input{}, fmt.Sprintf("%s's value %q is not a number", ref, c.Value)
	}
	if why := e.usable(c); why != "" {
		return compute.Input{}, why
	}
	if ref == e.nudged {
		n = n.PlusOne()
	}
	return compute.Input{ID: ref, Value: n, Unit: c.Unit}, ""
}

func (e *evaluator) eval(c *agent.Computation) evaluated {
	if r, ok := e.done[c.ID]; ok {
		return r
	}
	if e.busy[c.ID] {
		return evaluated{err: c.ID + " depends on itself"}
	}
	e.busy[c.ID] = true
	defer delete(e.busy, c.ID)
	var in []compute.Input
	var r evaluated
	for _, ref := range c.Inputs {
		x, why := e.input(ref)
		if why != "" {
			r.err = why
			break
		}
		in = append(in, x)
	}
	if r.err == "" {
		res, err := compute.Apply(c.Op, in, c.Periods)
		r.res = res
		if err != nil {
			r.err = err.Error()
		} else if x := e.echo(c, res.Value); x != "" {
			r.err = "it is " + x + " by construction: the inputs other than " + x + " cancel out, so it checks nothing"
		}
	}
	e.done[c.ID] = r
	return r
}

// echo is the input claim a computation's result merely reproduces, "" when
// there is none. A run derived Q4 as the year's total less January to
// September, then "checked" that the quarters add up to the total: true by
// construction, whatever the sources say. Each input claim is nudged by one
// in turn and the computation done again: the result is an input X in
// disguise when it moves by exactly one with X and not at all with any
// other input.
//
// It is asked only of a computation made of sums, differences and means
// throughout. Those are linear, so a nudge of one moves the result by that
// input's coefficient exactly, at any values: the test is an identity. A
// product or a ratio moves by an amount that depends on the values, and
// product(0, 1) would have looked like its first input. min and max return
// an input by design.
func (e *evaluator) echo(c *agent.Computation, base compute.Number) string {
	if e.probe || !e.linear(c.ID, map[string]bool{}) {
		return ""
	}
	leaves := e.leafClaims(c.ID, map[string]bool{})
	moved := func(id string) (*big.Rat, bool) {
		p := &evaluator{claims: e.claims, comps: e.comps, usable: func(*agent.Claim) string { return "" },
			done: map[string]evaluated{}, busy: map[string]bool{}, nudged: id, probe: true}
		r := p.eval(c)
		if r.err != "" {
			return nil, false
		}
		return new(big.Rat).Sub(r.res.Value.Rat(), base.Rat()), true
	}
	one := big.NewRat(1, 1)
	for _, x := range leaves {
		if d, ok := moved(x); !ok || d.Cmp(one) != 0 {
			continue
		}
		if !slices.ContainsFunc(leaves, func(y string) bool {
			d, ok := moved(y)
			return y != x && (!ok || d.Sign() != 0)
		}) {
			return x
		}
	}
	return ""
}

// linear reports whether ref is a sum, difference or mean of claims and of
// computations that are, all the way down.
func (e *evaluator) linear(ref string, seen map[string]bool) bool {
	if seen[ref] {
		return true
	}
	seen[ref] = true
	if c := e.claims[ref]; c != nil {
		return c.Computed == "" || e.linear(c.Computed, seen)
	}
	c := e.comps[ref]
	if c == nil || c.Op != compute.OpSum && c.Op != compute.OpDifference && c.Op != compute.OpMean {
		return false
	}
	for _, in := range c.Inputs {
		if !e.linear(in, seen) {
			return false
		}
	}
	return true
}

// leafClaims are the stated-value claims ref rests on, through computations
// and the computed claims that stand for them, each once.
func (e *evaluator) leafClaims(ref string, seen map[string]bool) []string {
	if seen[ref] {
		return nil
	}
	seen[ref] = true
	if c := e.claims[ref]; c != nil {
		if c.Computed != "" {
			return e.leafClaims(c.Computed, seen)
		}
		return []string{ref}
	}
	var out []string
	if c := e.comps[ref]; c != nil {
		for _, in := range c.Inputs {
			out = append(out, e.leafClaims(in, seen)...)
		}
	}
	return out
}

// record writes each computation's outcome onto it. checked marks the pass
// after the fact-check, the only one that can make a computation valid.
func (e *evaluator) record(a *agent.Analysis, checked bool) {
	for i := range a.Computations {
		c := &a.Computations[i]
		c.Result, c.Unit, c.Formula, c.Valid, c.Reason = "", "", "", false, ""
		if e.comps[c.ID] != c {
			c.Reason = "its ID " + c.ID + " is used by another claim or computation"
			continue
		}
		r := e.eval(c)
		if r.err != "" {
			c.Reason = r.err
			continue
		}
		c.Result, c.Unit, c.Formula = r.res.Value.Plain(), r.res.Unit, r.res.Formula
		c.Valid = checked
	}
}

// prepareFigures runs with checkAnalysis, before the fact-check: a computed
// claim cites its inputs' sources, and the computations are evaluated over
// the stated values, for the checker to see.
func prepareFigures(a *agent.Analysis) {
	inferValues(a)
	if len(a.Computations) == 0 && len(a.Charts) == 0 && !hasComputedClaim(a) {
		return
	}
	e := newEvaluator(a, func(*agent.Claim) string { return "" })
	for i := range a.Claims {
		if c := &a.Claims[i]; c.Computed != "" {
			c.Sources = e.leafSources(c.Computed, map[string]bool{})
			c.Status, c.Note = "", ""
		}
	}
	e.record(a, false)
}

func hasComputedClaim(a *agent.Analysis) bool {
	for _, c := range a.Claims {
		if c.Computed != "" {
			return true
		}
	}
	return false
}

// leafSources are the sources of the claims ref rests on, in order.
func (e *evaluator) leafSources(ref string, seen map[string]bool) []string {
	if seen[ref] {
		return nil
	}
	seen[ref] = true
	if c := e.claims[ref]; c != nil {
		if c.Computed != "" {
			return e.leafSources(c.Computed, seen)
		}
		return c.Sources
	}
	var out []string
	if c := e.comps[ref]; c != nil {
		for _, in := range c.Inputs {
			for _, s := range e.leafSources(in, seen) {
				if !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// checkedClaims are the claims the fact-check judges: all but the computed
// ones, which the program decides.
func checkedClaims(claims []agent.Claim) []agent.Claim {
	var out []agent.Claim
	for _, c := range claims {
		if c.Computed == "" {
			out = append(out, c)
		}
	}
	return out
}

// settleFigures runs in govern once the claims are judged: the computations
// are evaluated again over supported inputs only, the computed claims are
// decided against them, and the charts are drawn from what stands.
func settleFigures(a *agent.Analysis, verdicts map[string][]agent.Verdict, findings []agent.Finding,
	pages map[string]*pageText) {
	a.Charts = slices.DeleteFunc(a.Charts, func(ch agent.Chart) bool { return ch.Auto })
	a.Charts = append(a.Charts, autoCharts(a)...)
	if len(a.Computations) == 0 && len(a.Charts) == 0 && !hasComputedClaim(a) {
		return
	}
	e := newEvaluator(a, func(c *agent.Claim) string { return quotedValue(c, verdicts[c.ID], findings, pages) })
	e.record(a, true)
	for i := range a.Claims {
		if c := &a.Claims[i]; c.Computed != "" {
			c.Status, c.Note = e.decide(c)
		}
	}
	for i := range a.Charts {
		drawChart(&a.Charts[i], e)
	}
}

// quotedValue says why a claim's value cannot be an input, "" when it can:
// the claim passed the check, and its figure is written in a passage it was
// supported on. A claim can be supported on a passage that gives the figure
// another way, or not at all; then the value it was given is the model's.
func quotedValue(c *agent.Claim, vs []agent.Verdict, findings []agent.Finding, pages map[string]*pageText) string {
	if c.Status != statusSupported {
		return c.ID + " is " + c.Status
	}
	n, _ := compute.ParseValue(c.Value)
	if !compute.InText(c.Text, n) {
		return c.ID + "'s text does not state its value " + c.Value
	}
	if len(vs) == 1 {
		for _, ev := range vs[0].Evidence {
			page := pages[tools.CanonicalURL(resolveSource(ev.Source, findings))]
			if page != nil && page.locate(ev.Quote) && compute.InText(ev.Quote, n) {
				return ""
			}
		}
	}
	return c.ID + "'s value " + c.Value + " is in none of the passages it was supported on"
}

// decide is a computed claim's status and why.
func (e *evaluator) decide(c *agent.Claim) (string, string) {
	comp := e.comps[c.Computed]
	if comp == nil {
		return statusInsufficient, "it names no computation " + c.Computed
	}
	r := e.eval(comp)
	if r.err != "" {
		return statusInsufficient, comp.ID + " is not computed: " + r.err
	}
	got := comp.ID + " = " + r.res.Formula + " = " + withUnit(r.res.Value.String(), r.res.Unit)
	stated, ok := compute.ParseValue(c.Value)
	switch {
	case strings.TrimSpace(c.Value) == "":
		return statusInsufficient, "it states no figure to compare with " + got
	case !ok:
		return statusInsufficient, fmt.Sprintf("its value %q is not a number", c.Value)
	case !compute.InText(c.Text, stated):
		// The claim is what the answer and the inference check read, so the
		// figure judged must be the one its words state.
		return statusInsufficient, "its text does not state its value " + stated.String() + "; the program computed " + got
	case c.Unit != "" && compute.UnitKey(c.Unit) != compute.UnitKey(r.res.Unit):
		return statusContradicted, "it is in " + c.Unit + "; the program computed " + got
	case !compute.Matches(stated, r.res.Value):
		return statusContradicted, "it states " + withUnit(stated.String(), c.Unit) + "; the program computed " + got
	}
	return statusSupported, "computed by the program: " + got
}

func withUnit(v, unit string) string {
	switch u := strings.TrimSpace(unit); {
	case u == "":
		return v
	case u == "%" || u == "×" || strings.HasPrefix(u, "% "):
		return v + u
	}
	return v + " " + strings.TrimSpace(unit)
}

// drawChart resolves a chart's points and names the file it is drawn in,
// or says why it is not drawn. A point that is not a supported figure is left
// out, with why, and the rest are drawn when at least two stand: one year a
// source does not state made a run's whole series disappear. Points in
// another unit than the first that stands are left out too, since one axis
// cannot show both.
func drawChart(ch *agent.Chart, e *evaluator) {
	ch.Unit, ch.File, ch.Reason = "", "", ""
	if ch.Kind != compute.KindLine {
		ch.Kind = compute.KindBar
	}
	if len(ch.Points) > compute.MaxPoints {
		ch.Reason = fmt.Sprintf("it has %d points, more than %d", len(ch.Points), compute.MaxPoints)
		return
	}
	standing, first := 0, ""
	for j := range ch.Points {
		p := &ch.Points[j]
		p.Reason = pointReason(p, e, ch)
		if p.Reason != "" {
			first = cmp.Or(first, p.Reason)
			continue
		}
		standing++
	}
	if standing < 2 {
		ch.Unit, ch.Reason = "", fmt.Sprintf("%d of its %d points stand", standing, len(ch.Points))
		if first != "" {
			ch.Reason += "; " + first
		}
		return
	}
	ch.File = chartFileName(compute.SVG(chartOf(*ch)))
}

// pointReason resolves one point, setting its value and the chart's unit,
// and says why it is left out, "" when it is drawn.
func pointReason(p *agent.ChartPoint, e *evaluator, ch *agent.Chart) string {
	p.Value = ""
	in, why := e.input(p.Ref)
	switch {
	case why != "":
		return why
	case !compute.Drawable(in.Value.Float()):
		return p.Ref + " is too large to draw"
	case ch.Unit == "" && in.Unit != "":
		ch.Unit = in.Unit
	case compute.UnitKey(in.Unit) != compute.UnitKey(ch.Unit):
		return p.Ref + " is in " + cmp.Or(in.Unit, "no unit") + ", the chart in " + ch.Unit
	}
	p.Value = in.Value.Plain()
	return ""
}

// chartFileName names a chart's SVG by its content: the same chart is the
// same file, and a later run of the question never redraws the chart an
// earlier report or revision links to.
func chartFileName(svg []byte) string {
	sum := sha256.Sum256(svg)
	return "chart-" + hex.EncodeToString(sum[:8]) + ".svg"
}

// chartOf is a drawn chart in the compute package's terms.
func chartOf(ch agent.Chart) compute.Chart {
	c := compute.Chart{Title: ch.Title, Kind: ch.Kind, Unit: ch.Unit}
	for _, p := range ch.Points {
		if p.Value == "" {
			continue // left out (see drawChart)
		}
		n, _ := compute.ParseValue(p.Value)
		c.Points = append(c.Points, compute.Point{Label: p.Label, Value: n.Float(), Text: n.String()})
	}
	return c
}

// drawnCharts are the analysis's charts that were drawn, by file name: the
// one list the exports draw from, so no file a report names is read unless
// the run drew it.
func drawnCharts(a *agent.Analysis) map[string]compute.Chart {
	if a == nil {
		return nil
	}
	out := map[string]compute.Chart{}
	for _, ch := range a.Charts {
		if ch.File != "" {
			out[ch.File] = chartOf(ch)
		}
	}
	return out
}

// WriteCharts writes each drawn chart's SVG into dir and returns their
// paths. The bytes are made again from the analysis, never read from disk,
// so the file always is the chart its name digests.
func WriteCharts(a *agent.Analysis, dir string) ([]string, error) {
	var paths []string
	for name, c := range drawnCharts(a) {
		svg := compute.SVG(c)
		if chartFileName(svg) != name {
			return paths, fmt.Errorf("chart %s does not match its drawing", name)
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return paths, err
		}
		if err := writeAtomic(path, svg); err != nil {
			return paths, err
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

// computedBlock is what the checker is told the figures add up to, before
// any of them is checked.
func computedBlock(a *agent.Analysis) string {
	var lines []string
	for _, c := range a.Computations {
		if c.Result == "" {
			continue
		}
		n, _ := compute.ParseValue(c.Result)
		lines = append(lines, fmt.Sprintf("  - %s (%s) = %s = %s%s", c.ID, oneLine(c.Label), c.Formula,
			withUnit(n.String(), c.Unit), inputValues(a, c)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Computed by the program, exactly, from the values the claims state; each holds only if its" +
		" inputs do. Use them as you would a source's own arithmetic: a claim whose figure one of them" +
		" contradicts is not supported by a page that states it.\n" + strings.Join(lines, "\n") + "\n\n"
}

// inputValues is ", where c1 = 61,200 TEU, …" for a computation's inputs.
func inputValues(a *agent.Analysis, c agent.Computation) string {
	var parts []string
	for _, ref := range c.Inputs {
		if v := refValue(a, ref); v != "" {
			parts = append(parts, ref+" = "+v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ", where " + strings.Join(parts, ", ")
}

// refValue is the figure a claim or computation gives, with its unit.
func refValue(a *agent.Analysis, ref string) string {
	for _, c := range a.Claims {
		if c.ID == ref {
			if n, ok := compute.ParseValue(c.Value); ok {
				return withUnit(n.String(), c.Unit)
			}
			return ""
		}
	}
	for _, c := range a.Computations {
		if c.ID == ref && c.Result != "" {
			n, _ := compute.ParseValue(c.Result)
			return withUnit(n.String(), c.Unit)
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// withFigures closes the report with the computed figures, written by the
// program from the governed analysis like the answer section: the
// summarizer is told not to do arithmetic, and the figures it could state
// are these, each with what it was computed from and where those values
// were read.
func withFigures(sum *agent.Summary, a *agent.Analysis, findings []agent.Finding) *agent.Summary {
	if section := figuresSection(a, findings); section != "" {
		sum.Report = strings.TrimSpace(sum.Report) + "\n\n" + section
	}
	return sum
}

// figuresSection is the report's computed figures: a table of the valid
// computations, each drawn chart with its data as a table (the chart a
// terminal can show), and what was not established, with why.
func figuresSection(a *agent.Analysis, findings []agent.Finding) string {
	if a == nil || len(a.Computations) == 0 && len(a.Charts) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s\n\n_%s_\n\n", label(a, "computed"), label(a, "computed_note"))
	rows, failed := computationRows(a, findings)
	if len(rows) > 0 {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n| --- | ---: | --- | --- |\n%s\n\n", label(a, "figure"),
			label(a, "value"), label(a, "formula"), label(a, "inputs"), strings.Join(rows, "\n"))
	}
	failed = append(failed, writeCharts(&sb, a)...)
	if len(rows) == 0 && len(failed) > 0 && !anyDrawn(a) {
		sb.Reset()
		fmt.Fprintf(&sb, "## %s\n\n", label(a, "computed"))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "%s:\n\n%s\n", label(a, "not_established"), strings.Join(failed, "\n"))
	}
	return strings.TrimSpace(sb.String())
}

// computationRows are the table rows of the valid computations, and a line
// for each that failed for a cause of its own.
func computationRows(a *agent.Analysis, findings []agent.Finding) (rows, failed []string) {
	for _, c := range a.Computations {
		if !c.Valid {
			// One that failed only because an earlier one did adds nothing:
			// the earlier one is listed with the cause.
			if !knockOn(a, c.Reason) {
				failed = append(failed, fmt.Sprintf("- %s%s: %s", mdText(c.ID), labelled(c.Label), mdText(c.Reason)))
			}
			continue
		}
		n, _ := compute.ParseValue(c.Result)
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s |", mdText(firstNonEmpty(c.Label, c.ID)),
			mdText(withUnit(n.String(), c.Unit)), mdText(c.ID+" = "+c.Formula), inputCell(a, c, findings)))
	}
	return rows, failed
}

// writeCharts writes the drawn charts and returns a line for each chart
// not drawn and each point left out.
func writeCharts(sb *strings.Builder, a *agent.Analysis) (failed []string) {
	for _, ch := range a.Charts {
		if ch.File == "" {
			failed = append(failed, fmt.Sprintf("- %s%s: %s", mdText(ch.ID), labelled(ch.Title), mdText(ch.Reason)))
			continue
		}
		writeChart(sb, ch)
		for _, p := range ch.Points {
			if p.Reason != "" {
				failed = append(failed, fmt.Sprintf("- %s%s, point %s: %s", mdText(ch.ID), labelled(ch.Title),
					mdText(firstNonEmpty(p.Label, p.Ref)), mdText(p.Reason)))
			}
		}
	}
	return failed
}

func anyDrawn(a *agent.Analysis) bool {
	return slices.ContainsFunc(a.Charts, func(ch agent.Chart) bool { return ch.File != "" })
}

func labelled(s string) string {
	if s = oneLine(s); s != "" {
		return " (" + mdText(s) + ")"
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return oneLine(s)
		}
	}
	return ""
}

// writeChart is a drawn chart, linked as an image beside the report, and
// its points as a table.
func writeChart(sb *strings.Builder, ch agent.Chart) {
	title := mdText(firstNonEmpty(ch.Title, ch.ID))
	fmt.Fprintf(sb, "![%s](%s)\n\n| %s | %s |\n| --- | ---: |\n", title, ch.File, title, mdText(firstNonEmpty(ch.Unit, " ")))
	for _, p := range ch.Points {
		if p.Value == "" {
			continue
		}
		n, _ := compute.ParseValue(p.Value)
		fmt.Fprintf(sb, "| %s | %s |\n", mdText(firstNonEmpty(p.Label, p.Ref)), n.String())
	}
	sb.WriteString("\n")
}

// inputCell lists a computation's inputs: each claim's figure and the page
// it was read from, linked; an earlier computation by its ID.
func inputCell(a *agent.Analysis, c agent.Computation, findings []agent.Finding) string {
	var parts []string
	for _, ref := range c.Inputs {
		part := mdText(ref + " = " + refValue(a, ref))
		for _, cl := range a.Claims {
			if cl.ID == ref && len(cl.Sources) > 0 {
				part += " (" + sourceLink(cl.Sources[0], findings) + ")"
				break
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// sourceLink is a link to a page by its title.
func sourceLink(url string, findings []agent.Finding) string {
	title := url
	for _, f := range findings {
		if f.URL == url && strings.TrimSpace(f.Title) != "" {
			title = oneLine(f.Title)
			break
		}
	}
	return "[" + mdText(title) + "](<" + strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(url) + ">)"
}

// mdText is model or web text made literal in Markdown: on one line, with
// the characters that would open a link, an image, emphasis, code, HTML or
// a table cell escaped.
func mdText(s string) string {
	s = oneLine(s)
	var sb strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_[]<>!|#~", r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// inferValues gives a claim with no value the one number its text states
// (compute.SoleFigure), skipping years and what its scope already writes.
// Models skip the value field: a run named every computation over five
// medal counts and gave no claim a value, so nothing was computed or drawn.
// An inferred value is held to the same checks as a stated one: the claim
// supported, the number in a passage it was supported on.
func inferValues(a *agent.Analysis) {
	for i := range a.Claims {
		c := &a.Claims[i]
		if c.Computed != "" || strings.TrimSpace(c.Value) != "" {
			continue
		}
		if n, ok := compute.SoleFigure(c.Text, c.Scope+" "+c.Option); ok {
			c.Value = n.String()
		}
	}
}

// maxAutoCharts bounds the charts drawn unasked.
const maxAutoCharts = 3

// autoCharts are the charts the program draws of the analysis's figures
// when they make a series the analyzer did not chart: two or more claims
// with a value measuring the same thing (the same criterion and unit),
// labelled by what each is about. Charts are the program's to draw when
// the numbers are there, not only when a model remembers to ask; the claims
// that do not pass the check are left out of them with why (drawChart).
func autoCharts(a *agent.Analysis) []agent.Chart {
	charted := map[string]bool{}
	for _, ch := range a.Charts {
		for _, p := range ch.Points {
			charted[p.Ref] = true
		}
	}
	groups, order := map[string][]agent.Claim{}, []string{}
	for _, c := range a.Claims {
		if c.Computed != "" || charted[c.ID] || strings.TrimSpace(c.Criterion) == "" && strings.TrimSpace(c.Unit) == "" {
			continue
		}
		if _, ok := compute.ParseValue(c.Value); !ok {
			continue
		}
		key := strings.ToLower(oneLine(c.Criterion)) + "|" + compute.UnitKey(c.Unit)
		if groups[key] == nil {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}
	var out []agent.Chart
	for _, key := range order {
		if ch, ok := autoChart(groups[key], len(out)+1); ok && len(out) < maxAutoCharts {
			out = append(out, ch)
		}
	}
	return out
}

// autoChart is one group's chart, when its claims have two or more
// distinct labels: what each is about (its option, else its scope), each
// label once. Years in order make a line, anything else bars.
func autoChart(cs []agent.Claim, n int) (agent.Chart, bool) {
	ch := agent.Chart{ID: "auto" + strconv.Itoa(n), Auto: true, Title: oneLine(cs[0].Criterion)}
	seen := map[string]bool{}
	years := true
	prev := -1
	byScope := !distinct(cs, func(c agent.Claim) string { return c.Option })
	for _, c := range cs {
		label := firstNonEmpty(c.Option, c.Scope)
		if byScope {
			// A scope is a sentence ("Germany, calendar year 2023; …"); the
			// year in it is the label a reader needs.
			label = firstNonEmpty(compute.SoleYear(c.Scope), compute.SoleYear(c.Text), c.Scope, c.Option)
		}
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		ch.Points = append(ch.Points, agent.ChartPoint{Label: label, Ref: c.ID})
		y, err := strconv.Atoi(label)
		years = years && err == nil && y > prev
		prev = y
	}
	if len(ch.Points) < 2 {
		return agent.Chart{}, false
	}
	if ch.Title == "" {
		ch.Title = cs[0].Unit
	}
	ch.Title = capitalized(ch.Title)
	// One subject over a series ("Tesla" each year) is named in the title,
	// since the labels are years.
	if subject := oneLine(cs[0].Option); byScope && subject != "" && distinctCount(cs, func(c agent.Claim) string { return c.Option }) == 1 {
		ch.Title = subject + ": " + ch.Title
	}
	if scope := sharedScope(cs); scope != "" && !strings.Contains(ch.Title, scope) {
		ch.Title += ", " + scope
	}
	ch.Kind = compute.KindBar
	if years {
		ch.Kind = compute.KindLine
	}
	return ch, true
}

// sharedScope is the scope every claim gives, "" when they differ.
func sharedScope(cs []agent.Claim) string {
	s := oneLine(cs[0].Scope)
	for _, c := range cs[1:] {
		if oneLine(c.Scope) != s {
			return ""
		}
	}
	return s
}

// distinct reports whether field gives every claim its own non-empty value.
func distinct(cs []agent.Claim, field func(agent.Claim) string) bool {
	seen := map[string]bool{}
	for _, c := range cs {
		v := oneLine(field(c))
		if v == "" || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// knockOn reports whether reason is only that another computation of a
// failed ("m1 is not computed").
func knockOn(a *agent.Analysis, reason string) bool {
	id, ok := strings.CutSuffix(reason, " is not computed")
	return ok && slices.ContainsFunc(a.Computations, func(c agent.Computation) bool { return c.ID == id })
}

// distinctCount is how many different values field gives the claims.
func distinctCount(cs []agent.Claim, field func(agent.Claim) string) int {
	seen := map[string]bool{}
	for _, c := range cs {
		seen[oneLine(field(c))] = true
	}
	return len(seen)
}

// capitalized is s with its first letter upper case.
func capitalized(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
