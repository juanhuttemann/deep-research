// Command pipeline-diagram draws docs/images/pipeline.svg, the picture of a
// run in the README, from the tables below. Edit a table, then run
// `make diagram`. The test fails while the checked-in SVG differs from what
// this draws, so a hand edit to the SVG cannot drift from its source.
//
// Boxes are sized from their text and spread across their row, and a row
// that no longer fits is an error here rather than an overlap in the README.
// The counts that the pipeline's code fixes (follow-up searches, fact-check
// calls, sub-topics per depth) are read from internal/ui, so they cannot go
// stale while the code changes under them.
package main

import (
	"flag"
	"fmt"
	"html"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/ui"
)

// kind is what a box stands for, and so its colour (see style).
type kind string

const (
	model kind = "llm"  // a model call
	web   kind = "web"  // search, scraping or files
	rule  kind = "code" // a rule held in Go code
	user  kind = "you"
)

type box struct {
	kind  kind
	title string
	count string // model calls, printed beside the title
	lines []string
}

// The run as the README shows it: what a reader needs to follow one, no
// more. docs/architecture.md has the rest.
var (
	question = box{user, "Question", "", []string{"drsh -p, or", "drsh serve"}}
	plan     = box{model, "Plan", "1 call", []string{subTopicRange() + " sub-topics,", "query + terms each"}}
	group    = "Sub-agents, in parallel"
	lanes    = []string{"sub-topic 1", "sub-topic 2", "sub-topic n"}
	laneWork = "search · rank · read"
	followUp = box{model, "Follow-ups", "1 call",
		[]string{fmt.Sprintf("up to %d more searches", ui.MaxFollowUps), "for what is still open"}}
	handoff = "every page found, each one source"

	decide = []box{
		{model, "Analyze", "1 call", []string{"claims with sources,", "the answer's statements"}},
		{model, "Fact-check", fmt.Sprintf("1–%d calls", ui.MaxCheckCalls), []string{"claims vs. the pages;", "do statements follow?"}},
		{rule, "Keep what passed", "", []string{"quotes found on the page;", "the rest blocked, named"}},
		{model, "Summarize", "1 call", []string{"written from checked", "material only"}},
		{web, "Report", "", []string{".md · .pdf · .json", "+ run history"}},
	}
	// The repair pass runs from decide[repairFrom] back to decide[repairTo].
	repairFrom, repairTo = 2, 1
	repairNote           = []string{"a blocked statement is rewritten", "from passed claims and checked once (0–2 calls)"}

	legend = []struct {
		kind  kind
		label string
	}{{model, "model call"}, {web, "web or files"}, {rule, "rule in code"}, {user, "you"}}
)

// subTopicRange is the planner's sub-topic counts across the depth tiers,
// "3–6" for quick 3 and deep 6.
func subTopicRange() string {
	lo, hi := math.MaxInt, 0
	for _, m := range ui.DepthModes {
		lo, hi = min(lo, m.SubTopics), max(hi, m.SubTopics)
	}
	if lo == hi {
		return fmt.Sprint(lo)
	}
	return fmt.Sprintf("%d–%d", lo, hi)
}

// The canvas. Content spans left..right; the hand-off between the rows runs
// outside it, down the right margin and back up the left one, so it crosses
// no label on the way.
const (
	width, left, right = 960, 40, 936
	marginL, marginR   = 14, 948
	boxH, pad          = 64, 12
	minGap             = 26 // narrower and an arrow between two boxes is a stub
	laneH, laneStep    = 24, 32
	groupTop           = 24
)

// Advance widths per character, a little over the widest of the fonts the
// style names, so a box sized here holds its text in each of them.
const (
	titleCh  = 8.0 // 14px semibold
	detailCh = 6.0 // 12px
	laneCh   = 7.0 // 12px semibold
	countCh  = 6.7 // 11px monospace
	edgeCh   = 5.8 // 11.5px italic
	keyCh    = 5.9 // 11.5px
)

func textW(s string, ch float64) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) * ch))
}

func boxW(b box) int {
	w := textW(b.title, titleCh)
	if b.count != "" {
		w += 10 + textW(b.count, countCh)
	}
	for _, l := range b.lines {
		w = max(w, textW(l, detailCh))
	}
	return w + 2*pad
}

// spread places boxes of widths ws from left to right with equal gaps.
func spread(ws []int) ([]int, error) {
	sum := 0
	for _, w := range ws {
		sum += w
	}
	gap := (right - left - sum) / (len(ws) - 1)
	if gap < minGap {
		return nil, fmt.Errorf("a row is %dpx too wide: shorten a box's text", (minGap-gap)*(len(ws)-1))
	}
	xs := make([]int, len(ws))
	x := left
	for i, w := range ws {
		xs[i] = x
		x += w + gap
	}
	return xs, nil
}

type canvas struct{ strings.Builder }

func (c *canvas) printf(format string, a ...any) { _, _ = fmt.Fprintf(&c.Builder, format, a...) }

func (c *canvas) text(class string, x, y int, anchor, s string) {
	a := ""
	if anchor != "" {
		a = ` text-anchor="` + anchor + `"`
	}
	c.printf(`<text class="%s" x="%d" y="%d"%s>%s</text>`+"\n", class, x, y, a, html.EscapeString(s))
}

func (c *canvas) arrow(x1, y1, x2, y2 int) {
	c.printf(`<line class="a" x1="%d" y1="%d" x2="%d" y2="%d" marker-end="url(#ah)"/>`+"\n", x1, y1, x2, y2)
}

func (c *canvas) box(x, y, w int, b box) {
	c.printf(`<rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="8"/>`+"\n", b.kind, x, y, w, boxH)
	c.text("title", x+pad, y+23, "", b.title)
	if b.count != "" {
		c.text("count", x+w-pad, y+23, "end", b.count)
	}
	for i, l := range b.lines {
		c.text("detail", x+pad, y+42+i*15, "", l)
	}
}

// layout is where the rows landed, for what is drawn between them.
type layout struct {
	handoffX, handoffY int // the gather row's last box's right edge, mid-height
	joinY              int // the horizontal run of the hand-off
	decideY            int // top of the decide row
	repairY            int // bottom of the repair loop
}

func draw() (string, error) {
	var c canvas
	var l layout
	err := c.gather(&l)
	if err == nil {
		err = c.decide(&l)
	}
	if err != nil {
		return "", err
	}
	height := l.repairY + 74
	return header(height) + c.String() + "</svg>\n", nil
}

func (c *canvas) gather(l *layout) error {
	laneW := 0
	for _, n := range lanes {
		laneW = max(laneW, textW(n, laneCh)+8+textW(laneWork, detailCh))
	}
	laneW += 2 * pad
	groupW, groupH := laneW+2*14, 34+len(lanes)*laneStep+4
	nodes := []box{question, plan, {}, followUp}
	xs, err := spread([]int{boxW(question), boxW(plan), groupW, boxW(followUp)})
	if err != nil {
		return err
	}
	mid := groupTop + groupH/2
	for i, b := range nodes {
		if i != 2 {
			c.box(xs[i], mid-boxH/2, boxW(b), b)
		}
	}
	c.arrow(xs[0]+boxW(question), mid, xs[1]-2, mid)
	gx := xs[2]
	c.printf(`<rect class="group" x="%d" y="%d" width="%d" height="%d" rx="10"/>`+"\n", gx, groupTop, groupW, groupH)
	c.text("title", gx+14, groupTop+22, "", group)
	for i, n := range lanes {
		y := groupTop + 34 + i*laneStep
		c.printf(`<rect class="web" x="%d" y="%d" width="%d" height="%d" rx="6"/>`+"\n", gx+14, y, laneW, laneH)
		c.printf(`<text class="lane" x="%d" y="%d"><tspan font-weight="600">%s</tspan><tspan class="detail" dx="8">%s</tspan></text>`+"\n",
			gx+14+pad, y+16, html.EscapeString(n), html.EscapeString(laneWork))
		c.arrow(xs[1]+boxW(plan), mid, gx+12, y+laneH/2)
		c.arrow(gx+14+laneW, y+laneH/2, xs[3]-2, mid)
	}
	l.handoffX, l.handoffY = xs[3]+boxW(followUp), mid
	l.joinY = groupTop + groupH + 36
	l.decideY = l.joinY + 40
	return nil
}

func (c *canvas) decide(l *layout) error {
	ws := make([]int, len(decide))
	for i, b := range decide {
		ws[i] = boxW(b)
	}
	xs, err := spread(ws)
	if err != nil {
		return err
	}
	mid := l.decideY + boxH/2
	// Down the right margin, back along the gap between the rows, down the
	// left margin and into the first box from its left, the side every box
	// after it in the row is entered from.
	c.printf(`<path class="a" d="M%d,%d H%d Q%d,%d %d,%d V%d Q%d,%d %d,%d H%d Q%d,%d %d,%d V%d Q%d,%d %d,%d H%d" marker-end="url(#ah)"/>`+"\n",
		l.handoffX, l.handoffY, marginR-12, marginR, l.handoffY, marginR, l.handoffY+12,
		l.joinY-12, marginR, l.joinY, marginR-12, l.joinY,
		marginL+12, marginL, l.joinY, marginL, l.joinY+12,
		mid-12, marginL, mid, marginL+12, mid, left-2)
	c.text("edge", (left+right)/2, l.joinY-8, "middle", handoff)
	for i, b := range decide {
		c.box(xs[i], l.decideY, ws[i], b)
		if i > 0 {
			c.arrow(xs[i-1]+ws[i-1], mid, xs[i]-2, mid)
		}
	}
	return c.repair(l, xs, ws)
}

// repair draws the repair loop under the decide row, its note, and the
// legend beside the note.
func (c *canvas) repair(l *layout, xs, ws []int) error {
	from, to := xs[repairFrom]+ws[repairFrom]/2, xs[repairTo]+ws[repairTo]/2
	bottom := l.decideY + boxH
	l.repairY = bottom + 32
	c.printf(`<path class="loop" d="M%d,%d V%d Q%d,%d %d,%d H%d Q%d,%d %d,%d V%d" marker-end="url(#ahl)"/>`+"\n",
		from, bottom, l.repairY-10, from, l.repairY, from-10, l.repairY,
		to+10, to, l.repairY, to, l.repairY-10, bottom+2)
	noteX, noteEnd := (from+to)/2, 0
	for i, s := range repairNote {
		c.text("edge", noteX, l.repairY+18+i*15, "middle", s)
		noteEnd = max(noteEnd, noteX+textW(s, edgeCh)/2)
	}
	return c.legend(l.repairY+26, noteEnd)
}

// legend is right-aligned on the row of the repair note, and must clear it.
func (c *canvas) legend(y, after int) error {
	x := right
	xs := make([]int, len(legend))
	for i := len(legend) - 1; i >= 0; i-- {
		x -= 22 + textW(legend[i].label, keyCh)
		xs[i] = x
		x -= 18
	}
	if xs[0] < after+16 {
		return fmt.Errorf("the legend runs %dpx into the repair note: shorten either", after+16-xs[0])
	}
	for i, k := range legend {
		c.printf(`<rect class="%s" x="%d" y="%d" width="16" height="12" rx="3"/>`+"\n", k.kind, xs[i], y-10)
		c.text("key", xs[i]+22, y, "", k.label)
	}
	return nil
}

func header(height int) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">
<title>How a deep-research run works</title>
<style>%s</style>
<defs>
<marker id="ah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path class="ah" d="M0,0 L10,5 L0,10 z"/></marker>
<marker id="ahl" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path class="ahl" d="M0,0 L10,5 L0,10 z"/></marker>
</defs>
<rect class="bg" x="0" y="0" width="%d" height="%d" rx="12"/>
`, width, height, width, height, html.EscapeString(summary), style, width, height)
}

const summary = "A deep-research run: the question is planned into sub-topics, each researched by a parallel " +
	"sub-agent that searches, ranks by its terms and reads pages; one follow-up round adds a few searches. " +
	"The analysis states claims and the answer, the fact-check judges them against the pages, code keeps only " +
	"what passed and blocked statements get one repair, then the summary is written from the checked result " +
	"and saved as Markdown, PDF and JSON."

// style carries its own background and a dark variant: GitHub shows the SVG
// as an image, which cannot see the page's theme, only the viewer's.
const style = `
.bg{fill:#f6f8fa}
text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;fill:#1f2328}
.title{font-size:14px;font-weight:600}
.detail{font-size:12px;fill:#59636e}
.count{font-size:11px;font-weight:600;fill:#5a4bd1;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.edge{font-size:11.5px;fill:#59636e;font-style:italic}
.lane{font-size:12px}
.llm{fill:#efedff;stroke:#5a4bd1;stroke-width:1.5}
.web{fill:#e3f4f1;stroke:#0f766e;stroke-width:1.5}
.code{fill:#ffffff;stroke:#1f2328;stroke-width:1.5}
.you{fill:#fdf3dc;stroke:#9a6700;stroke-width:1.5}
.group{fill:none;stroke:#0f766e;stroke-width:1.2;stroke-dasharray:5 4}
.a{stroke:#59636e;stroke-width:1.5;fill:none}
.loop{stroke:#5a4bd1;stroke-width:1.5;fill:none;stroke-dasharray:5 4}
.ah{fill:#59636e}.ahl{fill:#5a4bd1}
.key{font-size:11.5px;fill:#59636e}
@media (prefers-color-scheme:dark){
.bg{fill:#151b23}
text{fill:#e6edf3}
.detail,.edge,.key{fill:#9198a1}
.count{fill:#a99fff}
.llm{fill:#26234a;stroke:#a99fff}
.web{fill:#0f2e2a;stroke:#3fbfae}
.code{fill:#151b23;stroke:#e6edf3}
.you{fill:#33280f;stroke:#e3b341}
.group{stroke:#3fbfae}
.a{stroke:#9198a1}.ah{fill:#9198a1}
.loop{stroke:#a99fff}.ahl{fill:#a99fff}
}
`

func main() {
	out := flag.String("o", "docs/images/pipeline.svg", "where to write the SVG")
	flag.Parse()
	svg, err := draw()
	if err == nil {
		err = os.WriteFile(*out, []byte(svg), 0o644)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "pipeline-diagram:", err)
		os.Exit(1)
	}
}
