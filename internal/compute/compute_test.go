package compute

import (
	"bytes"
	"strings"
	"testing"
)

func mustValue(t *testing.T, s string) Number {
	t.Helper()
	n, ok := ParseValue(s)
	if !ok {
		t.Fatalf("ParseValue(%q) failed", s)
	}
	return n
}

func TestParseValueReadsOneForm(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"61,200", "61,200", true},
		{"61200", "61,200", true},
		{"-3.5", "-3.5", true},
		{"−3.5", "-3.5", true},
		{"4.5%", "4.5", true},
		{"12,400,000", "12,400,000", true},
		{"0.0012", "0.0012", true},
		{"1,5", "", false}, // a decimal comma is ambiguous in a value field
		{"1.234,5", "", false},
		{"61,20", "", false}, // a group of two
		{"about 5", "", false},
		{"", "", false},
		{"5e3", "", false},
	} {
		n, ok := ParseValue(tc.in)
		if ok != tc.ok {
			t.Errorf("ParseValue(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && n.String() != tc.want {
			t.Errorf("ParseValue(%q) = %s, want %s", tc.in, n, tc.want)
		}
	}
}

func TestInTextMatchesWholeTokensOnly(t *testing.T) {
	for _, tc := range []struct {
		text, value string
		want        bool
	}{
		{"Q1 2025: 61,200. Q2 2025: 58,900.", "61,200", true},
		{"Q1 2025: 61,200. Q2 2025: 58,900.", "61200", true},
		{"Q1 2025: 61,200.", "1,200", false}, // part of a token
		{"Q1 2025: 61,200.", "1", false},     // the 1 of Q1 is a name
		{"Q1 2025: 61,200.", "620,000", false},
		{"handled 61 200 boxes", "61,200", true},
		{"handled 61\u202f200 boxes", "61,200", true},
		{"in 2025 61,200 boxes", "61,200", true}, // numbers side by side
		{"in Q1 2025 the port", "2025", true},    // a quarter's name keeps no year
		{"handled 61 200 500 boxes", "61,200,500", true},
		{"2025 123 456", "123", true}, // space-separated table cells
		{"2025 123 456", "123,456", true},
		{"GPT-4 scored higher", "4", false},
		{"in the 1990s", "1990", false},
		{"the 1st quarter", "1", false},
		{"COVID-19 cases", "-19", false},
		{"Umsatz: 1.234,5 Mio.", "1234.5", true},
		{"growth of 2,5 %", "2.5", true},
		{"fees of $12,400,000 in total", "12,400,000", true},
		{"fees of $12.4 million", "12.4", true},
		{"a fall of -3.5 points", "-3.5", true},
		{"a fall of -3.5 points", "3.5", false}, // the sign is part of the figure
		{"pages 10-20", "20", true},             // a hyphen between numbers is no minus
		{"pages 10-20", "-20", false},
		{"rate 4.50%", "4.5", true},
		{"v3.2 released", "3.2", false},
		{"about 3e6 tonnes", "3", false}, // an exponent's mantissa is not the figure
		{"a rate of 1e-3", "3", false},   // nor its exponent
		{"a rate of 1e+6", "6", false},
		{"1e-3 and 7 more", "7", true},
		{"a loss of $-3.5 million", "-3.5", true},
		{"a loss of $-3.5 million", "3.5", false},
	} {
		if got := InText(tc.text, mustValue(t, tc.value)); got != tc.want {
			t.Errorf("InText(%q, %s) = %v, want %v", tc.text, tc.value, got, tc.want)
		}
	}
}

func TestMatchesAtTheStatedPrecision(t *testing.T) {
	res, err := Apply(OpPercentChange, []Input{{"c1", mustValue(t, "100"), "TEU"}, {"c2", mustValue(t, "102.4618"), "TEU"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stated string
		want   bool
	}{{"2.5", true}, {"2.46", true}, {"2", true}, {"2.4", false}, {"3", false}} {
		if got := Matches(mustValue(t, tc.stated), res.Value); got != tc.want {
			t.Errorf("Matches(%s, %s) = %v, want %v", tc.stated, res.Value.Plain(), got, tc.want)
		}
	}
}

func inputs(t *testing.T, unit string, vs ...string) []Input {
	t.Helper()
	out := make([]Input, len(vs))
	for i, v := range vs {
		out[i] = Input{ID: "c" + string(rune('1'+i)), Value: mustValue(t, v), Unit: unit}
	}
	return out
}

func TestApplyIsExact(t *testing.T) {
	for _, tc := range []struct {
		op      string
		vs      []string
		periods int
		value   string
		unit    string
		formula string
	}{
		{OpSum, []string{"61,200", "58,900", "63,400", "64,500"}, 0, "248,000", "TEU", "c1 + c2 + c3 + c4"},
		{OpSum, []string{"0.1", "0.2"}, 0, "0.3", "TEU", "c1 + c2"},
		{OpDifference, []string{"64,500", "61,200"}, 0, "3,300", "TEU", "c1 − c2"},
		{OpMean, []string{"1", "2"}, 0, "1.50", "TEU", "mean(c1, c2)"},
		{OpMin, []string{"5", "3", "9"}, 0, "3", "TEU", "min(c1, c2, c3)"},
		{OpMax, []string{"5", "3", "9"}, 0, "9", "TEU", "max(c1, c2, c3)"},
		{OpRatio, []string{"248,000", "62,000"}, 0, "4.000", "×", "c1 ÷ c2"},
		{OpShare, []string{"61,200", "248,000"}, 0, "24.68", "%", "c1 ÷ c2 × 100"},
		{OpPercentChange, []string{"200", "250"}, 0, "25.00", "%", "(c2 − c1) ÷ c1 × 100"},
		{OpCAGR, []string{"100", "121"}, 2, "10.00", "% a period", "((c2 ÷ c1)^(1/2) − 1) × 100"},
	} {
		res, err := Apply(tc.op, inputs(t, "TEU", tc.vs...), tc.periods)
		if err != nil {
			t.Errorf("%s: %v", tc.op, err)
			continue
		}
		if res.Value.String() != tc.value || res.Unit != tc.unit || res.Formula != tc.formula {
			t.Errorf("%s = %s %q %q, want %s %q %q", tc.op, res.Value, res.Unit, res.Formula, tc.value, tc.unit, tc.formula)
		}
	}
	p, err := Apply(OpProduct, []Input{{"c1", mustValue(t, "50"), "USD per container"}, {"c2", mustValue(t, "248,000"), "containers"}}, 0)
	if err != nil || p.Value.String() != "12,400,000" || p.Unit != "USD per container × containers" {
		t.Errorf("product = %s %q, %v", p.Value, p.Unit, err)
	}
}

func TestApplyRefusesWhatIsNotArithmetic(t *testing.T) {
	for _, tc := range []struct {
		op      string
		in      []Input
		periods int
		want    string
	}{
		{OpSum, []Input{{"c1", mustValue(t, "1"), "TEU"}, {"c2", mustValue(t, "2"), "containers"}}, 0, "units differ"},
		{OpSum, []Input{{"c1", mustValue(t, "1"), "TEU"}}, 0, "at least 2"},
		{OpDifference, inputs(t, "", "1", "2", "3"), 0, "takes 2"},
		{OpRatio, inputs(t, "", "1", "0"), 0, "c2 is zero"},
		{OpPercentChange, inputs(t, "", "0", "5"), 0, "c1 is zero"},
		{OpCAGR, inputs(t, "", "-1", "5"), 3, "both positive"},
		{OpCAGR, inputs(t, "", "1", "5"), 0, "number of periods"},
		{"median", inputs(t, "", "1", "5"), 0, "not an operation"},
	} {
		_, err := Apply(tc.op, tc.in, tc.periods)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.op, err, tc.want)
		}
	}
}

func TestSVGEscapesAndIsDeterministic(t *testing.T) {
	c := Chart{Title: `Throughput <script>alert(1)</script> & "more"`, Kind: KindBar, Unit: "TEU",
		Points: []Point{{"Q1 <b>", 61200, "61,200"}, {"Q2", 58900, "58,900"}, {"Q3", -100, "-100"}}}
	a, b := SVG(c), SVG(c)
	if !bytes.Equal(a, b) {
		t.Fatal("the same chart drew two different SVGs")
	}
	s := string(a)
	for _, bad := range []string{"<script", "<b>", "href", "http://", "url("} {
		if strings.Contains(s[strings.Index(s, "</style>"):], bad) {
			t.Errorf("SVG contains %q:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "&lt;script&gt;") || !strings.Contains(s, "61,200") {
		t.Errorf("SVG lost the escaped title or a value:\n%s", s)
	}
	line := SVG(Chart{Title: "t", Kind: KindLine, Points: c.Points})
	if !bytes.Contains(line, []byte("<polyline")) {
		t.Errorf("a line chart drew no line:\n%s", line)
	}
}

func TestNiceTicksSpanTheDataFromZero(t *testing.T) {
	for _, tc := range []struct{ lo, hi float64 }{{0, 64500}, {-100, 64500}, {0, 0.03}, {0, 0}, {-5, -1}} {
		ticks := niceTicks(tc.lo, tc.hi, 5)
		if ticks[0] > tc.lo || ticks[len(ticks)-1] < tc.hi || len(ticks) < 2 {
			t.Errorf("niceTicks(%v, %v) = %v does not span the data", tc.lo, tc.hi, ticks)
		}
	}
}

func TestCAGRStaysAccurateAtTheEdgesOfFloatRange(t *testing.T) {
	big, _ := ParseValue("1" + strings.Repeat("0", 300))
	small, _ := ParseValue("0." + strings.Repeat("0", 299) + "1")
	res, err := Apply(OpCAGR, []Input{{"c1", big, ""}, {"c2", small, ""}}, 1000)
	if err != nil || res.Value.String() != "-74.88" {
		t.Errorf("cagr = %s, %v; want -74.88 (the ratio underflows; the logs do not)", res.Value, err)
	}
}

func TestNiceTicksStayBounded(t *testing.T) {
	for _, tc := range []struct{ lo, hi float64 }{{-1e15, 1e15}, {0, 1e-300}, {0, 5e-324},
		{-1e15, -999999999999999.9}, {5, 5}, {-7, -3}} {
		if ticks := niceTicks(tc.lo, tc.hi, 5); len(ticks) < 2 || len(ticks) > 21 || !(ticks[len(ticks)-1] > ticks[0]) ||
			ticks[0] > tc.lo || ticks[len(ticks)-1] < tc.hi {
			t.Errorf("niceTicks(%v, %v) = %d ticks", tc.lo, tc.hi, len(ticks))
		}
	}
}

func TestPercentSpellingsAreOneUnit(t *testing.T) {
	for _, u := range []string{"percent", "Per cent", "pct", "%"} {
		if UnitKey(u) != UnitKey("%") {
			t.Errorf("UnitKey(%q) = %q, want %%", u, UnitKey(u))
		}
	}
	if UnitKey("percentage points") == UnitKey("%") {
		t.Error("percentage points read as percent")
	}
}

func TestALineSpansItsDataAndABarStartsAtZero(t *testing.T) {
	pts := []Point{{"2020", 5367580, ""}, {"2025", 5594340, ""}}
	lowest := func(kind string) string {
		for _, s := range Layout(Chart{Kind: kind, Points: pts}, 600, 300) {
			if s.Kind == ShapeText && s.Role == RoleLabel && s.Anchor == "end" {
				return s.Text
			}
		}
		return ""
	}
	if got := lowest(KindBar); got != "0" {
		t.Errorf("bar axis starts at %s, want 0", got)
	}
	if got := lowest(KindLine); got == "0" || got == "" {
		t.Errorf("line axis starts at %q, want near its data", got)
	}
}

func TestANarrowLineDrawsFiniteCoordinates(t *testing.T) {
	for _, pts := range [][]Point{{{"a", -1e15, ""}, {"b", -999999999999999.9, ""}}, {{"a", 5, ""}, {"b", 5, ""}}} {
		if svg := string(SVG(Chart{Title: "t", Kind: KindLine, Points: pts})); strings.Contains(svg, "NaN") || strings.Contains(svg, "Inf") {
			t.Errorf("chart of %v drew non-finite coordinates", pts)
		}
	}
}

func TestUnstatedNamesTheFiguresTheSourcesLack(t *testing.T) {
	got := Unstated("7,030,229 is 9,896,347 less 2,866,118 in Q1 2024", "9 896 347 passengers in 2024", "2,866,118 in April")
	if len(got) != 1 || got[0] != "7,030,229" {
		t.Errorf("Unstated = %v, want [7,030,229]", got)
	}
}

func TestUnstatedAcceptsAVerbatimIdentifier(t *testing.T) {
	if got := Unstated("Use version 1.2.3 of it", "Version 1.2.3 fixed it"); len(got) != 0 {
		t.Errorf("Unstated = %v, want a version its premise writes accepted", got)
	}
	if got := Unstated("The improvement is 4%", "GPT-4 performs better"); len(got) != 1 {
		t.Errorf("Unstated = %v, want the 4%% unstated: GPT-4 is a name", got)
	}
}

// Table cells run together by PDF extraction are read as the cells: a
// daa statistics table came out as below, and the year-to-date total in it
// was refused as part of a malformed number.
func TestGluedTableCellsReadAsTheirCells(t *testing.T) {
	row := "Total Passengers 2,866,1182,834,4721%9,896,3479,493,5644%"
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"2,866,118", true}, {"2,834,472", true}, {"9,896,347", true}, {"9,493,564", true},
		{"28,661,182", false}, {"866,118", false}, {"3,479", false},
	} {
		if got := InText(row, mustValue(t, tc.value)); got != tc.want {
			t.Errorf("InText(row, %s) = %v, want %v", tc.value, got, tc.want)
		}
	}
	// A run that is no row of grouped numbers is not split into one: a
	// typo, an identifier, point groups, a lone leftover cell.
	for _, s := range []string{"1,23456789", "1234,567", "1,2,3", "1,000,0001", "12.3456.7890"} {
		if got := glued(s); got != nil {
			t.Errorf("glued(%q) = %v, want nothing", s, got)
		}
	}
	for _, v := range []string{"1,000,000", "1"} {
		if InText("a typo: 1,000,0001 units", mustValue(t, v)) {
			t.Errorf("a typo supplied %s", v)
		}
	}
	if InText(row, mustValue(t, "1")) || InText(row, mustValue(t, "4")) {
		t.Error("a leftover ungrouped cell was offered")
	}
	// A well-formed number keeps its one reading.
	if InText("61,200", mustValue(t, "61")) || InText("61,200", mustValue(t, "200")) {
		t.Error("a grouped number was read as cells")
	}
}
