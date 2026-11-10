package ui

import (
	"fmt"
	"io"
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

// PrintDiff writes a diff for the terminal: removed lines red, added green,
// kept lines dim.
func PrintDiff(w io.Writer, d []DiffLine, noColor bool) {
	for _, l := range d {
		text := l.Op + " " + l.Text
		if !noColor {
			switch l.Op {
			case "-":
				text = "\x1b[31m" + text + "\x1b[0m"
			case "+":
				text = "\x1b[32m" + text + "\x1b[0m"
			default:
				text = "\x1b[2m" + text + "\x1b[0m"
			}
		}
		_, _ = fmt.Fprintln(w, text)
	}
}

// PrintOutcome shows what a turn did to the report: the diff of the edit
// proposed or saved, what happened, and with an edit pending, how to keep
// or drop it.
func PrintOutcome(w io.Writer, o Outcome, noColor bool) {
	if len(o.Diff) > 0 {
		_, _ = fmt.Fprintln(w)
		PrintDiff(w, o.Diff, noColor)
	}
	var lines []string
	if o.Note != "" {
		lines = append(lines, o.Note+".")
	}
	if o.Pending {
		lines = append(lines, `Not saved: say "save it" to keep it, "discard that" to drop it, or what to change.`)
	}
	for _, l := range lines {
		if !noColor {
			l = "\x1b[2m" + l + "\x1b[0m"
		}
		_, _ = fmt.Fprintln(w, l)
	}
	if len(lines) > 0 {
		_, _ = fmt.Fprintln(w)
	}
}
