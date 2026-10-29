package ui

import (
	"io"
	"strings"
)

// Choice is one row of a picker.
type Choice struct{ Title, Note string }

// Pick shows rows under a filter box, drawn and read as the start screen is:
// typing keeps the rows holding every word typed, the arrows move, Enter
// takes the highlighted row. It returns the row's index, or -1 when the
// reader went back (Esc, Ctrl-C).
func Pick(stdin io.Reader, stdout io.Writer, noColor bool, title, label string, rows []Choice) int {
	sig := catchSignals(true)
	in := NewInput(stdin, nil)
	r := NewRenderer(stdout, Theme{Enabled: !noColor})
	defer guardTerminal(sig, in, r)()
	// Drawn in place, as the boxes it opens from are, and cleared after, so
	// what was above it shows as it was. It opened on the alternate screen,
	// which Warp shows from the top of the window while the boxes sit at
	// its foot.
	keys := &keptInput{Input: in}
	defer r.Inline(keys)()
	return pick(keys, r, title, label, rows)
}

func pick(in Input, r *Renderer, title, label string, rows []Choice) int {
	text, shown, at, cursor := "", matching(rows, ""), 0, 0
	paint := func() { r.RenderPick(title, label, text, cursor, rows, shown, at) }
	echo := func(t string, c int) {
		if t != text {
			text, shown, at = t, matching(rows, t), 0
		}
		cursor = c
		paint()
	}
	arrow := func(k byte) {
		switch k {
		case 'A':
			at = max(at-1, 0)
		case 'B':
			at = max(min(at+1, len(shown)-1), 0)
		}
		paint()
	}
	for {
		_, ok := readPromptRaw(in, []byte(text), nil, echo, arrow)
		if !ok {
			return -1
		}
		if len(shown) > 0 {
			return shown[at]
		}
	}
}

// matching is the rows holding every word of filter, in their order.
func matching(rows []Choice, filter string) []int {
	words := strings.Fields(strings.ToLower(filter))
	var out []int
	for i, c := range rows {
		text := strings.ToLower(c.Title + " " + c.Note)
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(text, w)
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// RenderPick shows the picker: rows[shown[at]] is highlighted.
func (r *Renderer) RenderPick(title, label, text string, cursor int, rows []Choice, shown []int, at int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.pickTitle, r.pickRows, r.pickShown, r.pickAt = "pick", title, rows, shown, at
	r.promptLabel, r.promptText, r.promptCursor, r.example = label, text, cursor, "type to filter"
	if !r.plain {
		r.paintLocked(true)
	}
}

// pickLines is the picker, its rows scrolled to keep the highlighted one in
// view.
func (r *Renderer) pickLines() []string {
	out := []string{r.heading(r.pickTitle, ""), r.rule(), r.promptRow(), r.rule()}
	// The rule, the keys and the blank row under them, as the start screen
	// ends, are kept below the rows.
	room := max(r.rows-1-len(out)-3, 1)
	from := max(r.pickAt-room+1, 0)
	for i := from; i < len(r.pickShown) && i < from+room; i++ {
		c := r.pickRows[r.pickShown[i]]
		row := "    " + c.Title
		if i == r.pickAt {
			row = "  " + r.T.magenta("▸ ") + r.T.bold(c.Title)
		}
		if c.Note != "" {
			row += "  " + r.T.gray(c.Note)
		}
		out = append(out, row)
	}
	if len(r.pickShown) == 0 {
		out = append(out, "  "+r.T.dim("nothing matches"))
	}
	return append(out, r.rule(), "  "+r.T.hints("[enter] open   [↑↓] move   [esc] back"), "")
}
