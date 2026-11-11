package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// DiffLine is one line of a report edit's diff: Op is ' ' for a line both
// versions keep, '-' for one the edit removes and '+' for one it adds.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// diffContext is how many kept lines stand around each change. A report's
// lines are whole paragraphs, so one either side places the change.
const diffContext = 1

// Diff is the line diff from a to b, with only diffContext kept lines around
// each change and "…" standing for the kept lines between. Blank lines are
// left out: in Markdown they only separate paragraphs, and a diff full of
// them read as changes to nothing.
func Diff(a, b string) []DiffLine {
	all := lineDiff(nonBlank(a), nonBlank(b))
	keep := make([]bool, len(all))
	for i, d := range all {
		if d.Op == " " {
			continue
		}
		for j := max(0, i-diffContext); j <= min(len(all)-1, i+diffContext); j++ {
			keep[j] = true
		}
	}
	var out []DiffLine
	skipped := false
	for i, d := range all {
		if !keep[i] {
			skipped = true
			continue
		}
		if skipped && len(out) > 0 {
			out = append(out, DiffLine{" ", "…"})
		}
		skipped = false
		out = append(out, d)
	}
	return out
}

func nonBlank(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// lineDiff is the longest-common-subsequence diff of two line lists. A
// report is a few hundred lines at most, so the quadratic table is small.
func lineDiff(a, b []string) []DiffLine {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, DiffLine{" ", a[i]})
			i, j = i+1, j+1
		// A removed line goes ahead of the line that takes its place.
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, DiffLine{"-", a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", b[j]})
			j++
		}
	}
	return out
}

// DiffText is a diff as plain text, one line per line with its op ahead of
// it: what the model is shown of the edit it made.
func DiffText(d []DiffLine) string {
	var sb strings.Builder
	for _, l := range d {
		fmt.Fprintf(&sb, "%s %s\n", l.Op, l.Text)
	}
	return sb.String()
}

// diffGutter is the left margin the diff shares with the rendered reply, so
// the two read as one column; the marker sits in it.
const diffGutter = "  "

// PrintDiff writes a diff for the terminal. In colour on a terminal it reads
// as the report does: added text rendered as Markdown, its links and
// headings like the report's, under a green bar; a removed paragraph one
// struck-out row under a red one, the user having just read it; kept lines
// one dim row. Raw Markdown in red and green, a line per paragraph, read as
// a patch to review rather than a report to read. Elsewhere (a pipe, a file,
// --no-color) it is the plain line diff, every word kept.
func PrintDiff(w io.Writer, d []DiffLine, noColor bool) {
	width := 0
	if f, ok := w.(*os.File); ok {
		_, width = termSize(f)
	}
	if noColor || width < 30 || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		for _, l := range d {
			for _, row := range diffRows(l, width) {
				if !noColor {
					row = diffColor[l.Op] + row + cReset
				}
				_, _ = fmt.Fprintln(w, row)
			}
		}
		return
	}
	for _, row := range styledDiff(d, width) {
		_, _ = fmt.Fprintln(w, row)
	}
}

const (
	barAdd  = "\x1b[32m▌\x1b[0m"
	barDel  = "\x1b[31m▌\x1b[0m"
	barKeep = " "
)

// DiffBlocks is a diff as it is shown: each run of added lines one block
// of Markdown, to render whole (a list, a paragraph with links), and each
// removed or kept line its words alone (PlainMarkdown), shown as one row.
// The terminal and the serve page both lay a diff out from it.
func DiffBlocks(d []DiffLine) []DiffLine {
	var out []DiffLine
	for _, l := range d {
		switch {
		case l.Op != "+":
			out = append(out, DiffLine{l.Op, PlainMarkdown(l.Text)})
		case len(out) > 0 && out[len(out)-1].Op == "+":
			out[len(out)-1].Text += "\n\n" + l.Text
		default:
			out = append(out, l)
		}
	}
	return out
}

// styledDiff lays a diff out in width columns.
func styledDiff(d []DiffLine, width int) []string {
	var rows []string
	for _, b := range DiffBlocks(d) {
		switch b.Op {
		case "+":
			for _, r := range strings.Split(renderMarkdown(b.Text, width-1), "\n") {
				if strings.TrimSpace(stripANSI(r)) != "" {
					rows = append(rows, barAdd+r)
				}
			}
		case "-":
			rows = append(rows, barDel+diffGutter+"\x1b[2;9m"+clip(b.Text, width-1-len(diffGutter))+cReset)
		default:
			rows = append(rows, barKeep+diffGutter+"\x1b[2m"+clip(b.Text, width-1-len(diffGutter))+cReset)
		}
	}
	return rows
}

var (
	mdLinkText = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdMarks    = regexp.MustCompile(`^\s*(#+|[-*]|\d+\.)\s+|\*\*|__|\x60`)
	ansiSeq    = regexp.MustCompile(`\x1b(\[[0-9;?]*[a-zA-Z]|\]8;[^\x1b\x07]*(\x1b\\|\x07))`)
)

// PlainMarkdown is a Markdown line as the words it shows: link text without
// its URL, no emphasis or list marks. A removed line is one row, and its
// URLs filled it.
func PlainMarkdown(s string) string {
	s = mdLinkText.ReplaceAllString(strings.TrimSpace(s), "$1")
	s = strings.TrimSpace(mdMarks.ReplaceAllString(s, ""))
	// A line in italics, "_Instant answer…_", without its underscores.
	for _, m := range []string{"_", "*"} {
		if len(s) > 2 && strings.HasPrefix(s, m) && strings.HasSuffix(s, m) {
			s = s[1 : len(s)-1]
		}
	}
	return s
}

func stripANSI(s string) string { return ansiSeq.ReplaceAllString(s, "") }

var diffColor = map[string]string{"-": "\x1b[31m", "+": "\x1b[32m", " ": "\x1b[2m"}

// diffRows lays out one diff line in width columns, or as one line when the
// width is unknown (a pipe or a file, which keep every word).
func diffRows(l DiffLine, width int) []string {
	lead := diffGutter + l.Op + " "
	text := strings.TrimSpace(l.Text)
	room := width - dispWidth(lead)
	if width <= 0 || room < 20 {
		return []string{lead + text}
	}
	if l.Op != "+" {
		return []string{lead + clip(text, room)}
	}
	var rows []string
	for i, row := range wrapWords(text, room) {
		if i > 0 {
			lead = strings.Repeat(" ", dispWidth(lead))
		}
		rows = append(rows, lead+row)
	}
	return rows
}

// PrintOutcome shows the diff of what a turn did to the report. It comes
// ahead of the reply, which says what was changed, and PrintOutcomeNote
// after it.
func PrintOutcome(w io.Writer, o Outcome, noColor bool) {
	if len(o.Diff) > 0 {
		_, _ = fmt.Fprintln(w)
		PrintDiff(w, o.Diff, noColor)
	}
}

// PrintOutcomeNote says what a turn did and, with an edit pending, how to
// keep or drop it: one dim line under the reply. "Proposed an edit" is
// left out under a pending edit, which the line already says.
func PrintOutcomeNote(w io.Writer, o Outcome, noColor bool) {
	var parts []string
	for p := range strings.SplitSeq(o.Note, "; ") {
		said := o.Pending && (p == "proposed an edit" || p == "revised the proposed edit")
		if p != "" && !said {
			parts = append(parts, p)
		}
	}
	line := strings.Join(parts, "; ")
	if line != "" {
		line = strings.ToUpper(line[:1]) + line[1:] + "."
	}
	if o.Pending {
		line = strings.TrimSpace(line + ` Not saved yet: "save it" keeps it, "discard that" drops it, or say what to change.`)
	}
	if line == "" {
		return
	}
	for _, row := range diffWrap(line, w) {
		if !noColor {
			row = "\x1b[2m" + row + cReset
		}
		_, _ = fmt.Fprintln(w, row)
	}
	_, _ = fmt.Fprintln(w)
}

func diffWrap(s string, w io.Writer) []string {
	if f, ok := w.(*os.File); ok {
		if _, width := termSize(f); width > 20 {
			rows := wrapWords(s, width-len(diffGutter))
			for i := range rows {
				rows[i] = diffGutter + rows[i]
			}
			return rows
		}
	}
	return []string{diffGutter + s}
}
