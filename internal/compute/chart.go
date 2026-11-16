package compute

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Chart kinds. A bar chart compares magnitudes; a line shows one quantity
// over an ordered axis, usually time.
const (
	KindBar  = "bar"
	KindLine = "line"
)

// MaxPoints is the most points a chart draws: past it the labels no longer
// fit, and a table says it better.
const MaxPoints = 40

// Chart is one series of values in one unit, ready to draw.
type Chart struct {
	Title  string
	Kind   string
	Unit   string
	Points []Point
}

// Point is a labelled value; Text is the value as the report writes it.
type Point struct {
	Label string
	Value float64
	Text  string
}

// Shape kinds and the roles they are drawn in. SVG and PDF draw the same
// shapes, each in its own colours, so the chart a reader prints is the one
// on the page.
const (
	ShapeRect = "rect"
	ShapeLine = "line"
	ShapePath = "polyline"
	ShapeDot  = "dot"
	ShapeText = "text"

	RoleTitle = "title"
	RoleGrid  = "grid"
	RoleAxis  = "axis"
	RoleMark  = "mark"
	RoleLabel = "label"
	RoleValue = "value"
	RoleUnit  = "unit"
)

// Shape is one drawn element, in a frame with its origin at the top left.
// A rect's Y is its top; Up says which end is the data end (rounded in the
// SVG), true for a positive bar.
type Shape struct {
	Kind, Role string
	X, Y, W, H float64
	X2, Y2     float64
	Points     [][2]float64
	Text       string
	Anchor     string // start, middle, end
	Size       float64
	Up         bool
}

// Layout draws c into a w by h frame.
func Layout(c Chart, w, h float64) []Shape {
	const left, right, bottom = 64.0, 16.0, 40.0
	out, top := chartHeader(c, w)
	if len(c.Points) == 0 {
		return out
	}
	plotW, plotH := w-left-right, h-top-bottom
	// A bar's length is its value, so its axis starts at zero. A line shows
	// change, and spans its data: from zero, a population's 4% growth over
	// five years was drawn as a flat line.
	lo, hi := 0.0, 0.0
	if c.Kind == KindLine {
		lo, hi = c.Points[0].Value, c.Points[0].Value
	}
	for _, p := range c.Points {
		lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
	}
	ticks := niceTicks(lo, hi, 5)
	lo, hi = ticks[0], ticks[len(ticks)-1]
	y := func(v float64) float64 { return top + plotH*(hi-v)/(hi-lo) }
	step := ticks[1] - ticks[0]
	for _, t := range ticks {
		role := RoleGrid
		if t == 0 && c.Kind != KindLine {
			role = RoleAxis
		}
		out = append(out,
			Shape{Kind: ShapeLine, Role: role, X: left, Y: y(t), X2: left + plotW, Y2: y(t)},
			Shape{Kind: ShapeText, Role: RoleLabel, X: left - 6, Y: y(t) + 3.5, Text: tickText(t, step), Anchor: "end", Size: 10})
	}
	band := plotW / float64(len(c.Points))
	every := labelEvery(c.Points, band)
	fit := max(int(band*float64(every)/6), 3)
	for i, p := range c.Points {
		cx := left + band*(float64(i)+0.5)
		if i%every == 0 {
			out = append(out, Shape{Kind: ShapeText, Role: RoleLabel, X: cx, Y: top + plotH + 16,
				Text: clip(p.Label, fit), Anchor: "middle", Size: 10})
		}
	}
	if c.Kind == KindLine {
		return append(out, lineMarks(c.Points, band, left, y)...)
	}
	return append(out, barMarks(c.Points, band, left, y)...)
}

// Header lines reserve plot space in both vector formats. A scope-heavy
// model title must not extend beyond the page or overlap the unit/axis.
func chartHeader(c Chart, width float64) ([]Shape, float64) {
	lines := chartTitleLines(c.Title, width-16)
	var out []Shape
	for i, line := range lines {
		out = append(out, Shape{Kind: ShapeText, Role: RoleTitle, X: 0, Y: 18 + 18*float64(i), Text: line, Anchor: "start", Size: 14})
	}
	last := 18 + 18*float64(len(lines)-1)
	if c.Unit != "" {
		out = append(out, Shape{Kind: ShapeText, Role: RoleUnit, X: 0, Y: last + 16, Text: c.Unit, Anchor: "start", Size: 10})
	}
	return out, last + 26
}

func chartTitleLines(title string, width float64) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(title) {
		word = clip(word, max(1, int(width/14)))
		candidate := strings.TrimSpace(line + " " + word)
		if line != "" && chartTitleWidth(candidate) > width {
			lines = append(lines, line)
			line = word
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	if len(lines) > 5 {
		lines = lines[:5]
		lines[4] += "…"
	}
	return lines
}

func chartTitleWidth(s string) float64 {
	width := 0.0
	for _, r := range s {
		switch {
		case r == ' ':
			width += 0.4
		case r > 127 || strings.ContainsRune("MWmw@", r):
			width += 1
		default:
			width += 0.75
		}
	}
	return width * 14
}

func barMarks(pts []Point, band, left float64, y func(float64) float64) []Shape {
	var out []Shape
	bw := math.Min(24, band*0.7)
	for i, p := range pts {
		cx := left + band*(float64(i)+0.5)
		y0, y1 := y(0), y(p.Value)
		out = append(out, Shape{Kind: ShapeRect, Role: RoleMark, X: cx - bw/2, Y: math.Min(y0, y1), W: bw,
			H: math.Abs(y0 - y1), Up: p.Value >= 0})
		if len(pts) <= 12 {
			ty := y1 - 5
			if p.Value < 0 {
				ty = y1 + 13
			}
			out = append(out, Shape{Kind: ShapeText, Role: RoleValue, X: cx, Y: ty, Text: p.Text, Anchor: "middle", Size: 10})
		}
	}
	return out
}

func lineMarks(pts []Point, band, left float64, y func(float64) float64) []Shape {
	path := Shape{Kind: ShapePath, Role: RoleMark}
	var dots []Shape
	for i, p := range pts {
		x := left + band*(float64(i)+0.5)
		path.Points = append(path.Points, [2]float64{x, y(p.Value)})
		dots = append(dots, Shape{Kind: ShapeDot, Role: RoleMark, X: x, Y: y(p.Value), W: 4})
	}
	last := pts[len(pts)-1]
	end := path.Points[len(path.Points)-1]
	value := Shape{Kind: ShapeText, Role: RoleValue, X: end[0], Y: end[1] - 10, Text: last.Text, Anchor: "middle", Size: 10}
	return append(append([]Shape{path}, dots...), value)
}

// labelEvery labels every n-th point so the labels do not run into each
// other: a label needs about six points a character.
func labelEvery(pts []Point, band float64) int {
	longest := 1
	for _, p := range pts {
		longest = max(longest, min(utf8.RuneCountInString(p.Label), 14))
	}
	return max(1, int(math.Ceil(float64(longest)*6/band)))
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:max(n-1, 1)]) + "…"
}

// maxDrawable bounds a drawn value: the axis's span and step must stay
// finite floats, and a figure past it is better read in the table.
const maxDrawable = 1e15

// Drawable reports whether a chart can draw v.
func Drawable(v float64) bool { return !math.IsNaN(v) && math.Abs(v) <= maxDrawable }

// niceTicks spans lo..hi in about n steps of 1, 2 or 5 times a power of ten.
// lo and hi are
// Drawable; a span too small for a step is drawn as zero to one.
func niceTicks(lo, hi float64, n int) []float64 {
	// A span too narrow for its magnitude has no step a float can count:
	// near 1e15, adding 0.05 changes nothing. It is widened to a millionth
	// of the values, at least one.
	if tiny := math.Max(1, math.Max(math.Abs(lo), math.Abs(hi))*1e-6); hi-lo < tiny {
		mid := lo + (hi-lo)/2
		lo, hi = mid-tiny, mid+tiny
	}
	raw := (hi - lo) / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag * 10
	for _, m := range []float64{1, 2, 5, 10} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	var out []float64
	for k := math.Floor(lo / step); k*step <= hi+step*1e-9 && len(out) <= 4*n; k++ {
		out = append(out, k*step)
	}
	if out[len(out)-1] < hi {
		out = append(out, out[len(out)-1]+step)
	}
	if len(out) < 2 || !(out[len(out)-1] > out[0]) {
		return []float64{lo, hi}
	}
	return out
}

// tickText writes a tick with the places its step needs, grouped.
func tickText(v, step float64) string {
	dec := 0
	if step < 1 {
		dec = int(math.Ceil(-math.Log10(step)))
	}
	return group(strconv.FormatFloat(v, 'f', dec, 64))
}

// svgStyle is the chart's ink: one series hue on the chart surface, text in
// the text inks, a recessive grid. The dark set is chosen for the dark
// surface, not inverted, and follows the reader's system setting, which is
// all an image can see.
const svgStyle = `.bg{fill:#fcfcfb}.t{fill:#0b0b0b}.s{fill:#52514e}.m{fill:#898781}` +
	`.g{stroke:#e1e0d9;stroke-width:1}.a{stroke:#c3c2b7;stroke-width:1}` +
	`.bar{fill:#2a78d6}.ln{fill:none;stroke:#2a78d6;stroke-width:2;stroke-linejoin:round;stroke-linecap:round}` +
	`.dot{fill:#2a78d6;stroke:#fcfcfb;stroke-width:2}` +
	`@media (prefers-color-scheme:dark){.bg{fill:#1a1a19}.t{fill:#f0efec}.s{fill:#c3c2b7}` +
	`.g{stroke:#2c2c2a}.a{stroke:#383835}.bar{fill:#3987e5}.ln{stroke:#3987e5}.dot{fill:#3987e5;stroke:#1a1a19}}` +
	`text{font-family:system-ui,-apple-system,"Segoe UI",Helvetica,Arial,sans-serif}`

// SVG is the chart as a standalone SVG document: no script, no external
// reference, every text escaped. Its bytes depend only on the chart, so a
// file named by their digest is the same chart wherever it is linked.
func SVG(c Chart) []byte {
	const w, h = 640.0, 320.0
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s" role="img">`,
		num(w), num(h), num(w), num(h))
	fmt.Fprintf(&b, "<title>%s</title><style>%s</style>", esc(c.Title), svgStyle)
	fmt.Fprintf(&b, `<rect class="bg" x="0" y="0" width="%s" height="%s"/>`, num(w), num(h))
	b.WriteString(`<g transform="translate(12 6)">`)
	for _, s := range Layout(c, w-24, h-12) {
		writeShape(&b, s)
	}
	b.WriteString("</g></svg>\n")
	return b.Bytes()
}

func writeShape(b *bytes.Buffer, s Shape) {
	switch s.Kind {
	case ShapeText:
		class := map[string]string{RoleTitle: "t", RoleValue: "s", RoleUnit: "m"}[s.Role]
		if class == "" {
			class = "m"
		}
		weight := ""
		if s.Role == RoleTitle {
			weight = ` font-weight="600"`
		}
		fmt.Fprintf(b, `<text class="%s" x="%s" y="%s" font-size="%s" text-anchor="%s"%s>%s</text>`,
			class, num(s.X), num(s.Y), num(s.Size), s.Anchor, weight, esc(s.Text))
	case ShapeLine:
		class := "g"
		if s.Role == RoleAxis {
			class = "a"
		}
		fmt.Fprintf(b, `<line class="%s" x1="%s" y1="%s" x2="%s" y2="%s"/>`, class, num(s.X), num(s.Y), num(s.X2), num(s.Y2))
	case ShapeRect:
		fmt.Fprintf(b, `<path class="bar" d="%s"/>`, barPath(s))
	case ShapePath:
		pts := make([]string, len(s.Points))
		for i, p := range s.Points {
			pts[i] = num(p[0]) + "," + num(p[1])
		}
		fmt.Fprintf(b, `<polyline class="ln" points="%s"/>`, strings.Join(pts, " "))
	case ShapeDot:
		fmt.Fprintf(b, `<circle class="dot" cx="%s" cy="%s" r="%s"/>`, num(s.X), num(s.Y), num(s.W))
	}
}

// barPath is a bar with its data end rounded and its baseline end square.
func barPath(s Shape) string {
	r := math.Min(4, math.Min(s.W/2, s.H))
	x0, x1, top, bot := s.X, s.X+s.W, s.Y, s.Y+s.H
	if s.Up {
		return fmt.Sprintf("M%s %sV%sQ%s %s %s %sH%sQ%s %s %s %sV%sZ",
			num(x0), num(bot), num(top+r), num(x0), num(top), num(x0+r), num(top),
			num(x1-r), num(x1), num(top), num(x1), num(top+r), num(bot))
	}
	return fmt.Sprintf("M%s %sV%sQ%s %s %s %sH%sQ%s %s %s %sV%sZ",
		num(x0), num(top), num(bot-r), num(x0), num(bot), num(x0+r), num(bot),
		num(x1-r), num(x1), num(bot), num(x1), num(bot-r), num(top))
}

func num(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

func esc(s string) string { return html.EscapeString(s) }
