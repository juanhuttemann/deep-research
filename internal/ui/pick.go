package ui

import (
	"cmp"
	"io"
	"slices"
	"strings"
)

// Choice is one row of a picker. own marks the row that answers with the
// text typed in the box, not with its title.
type Choice struct {
	Title, Note string
	own         bool
}

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
	got := pickWith(in, r, pickSpec{title: title, label: label, rows: rows})
	if got.back {
		return -1
	}
	return got.row
}

// pickSpec is what a picker shows: its rows, the row it opens on, the text
// already typed and its key hints ("" for the default).
//
// A picker with an own row is answered, not searched: it has no filter box,
// every row stays in view, and what is typed is written in the own row,
// which the highlight moves to. Text typed before opens on that row.
type pickSpec struct {
	// right is shown at the heading's end, lead under it.
	title, right, lead, label string
	rows                      []Choice
	at                        int
	text, hints               string
}

// picked is how a picker ended: a row, typed text (row -1), or back. Ctrl-C
// is told apart from Esc and the end of input, which go back: it stops the
// run where Esc only leaves the picker.
type picked struct {
	row        int
	text       string
	back, stop bool
}

func pickWith(in Input, r *Renderer, sp pickSpec) picked {
	keys, ok := in.(*keptInput)
	if !ok {
		keys = &keptInput{Input: in}
	}
	own := slices.IndexFunc(sp.rows, func(c Choice) bool { return c.own })
	text, shown, cursor := sp.text, pickRows(sp.rows, sp.text), len(sp.text)
	at := max(min(sp.at, len(shown)-1), 0)
	if sp.text != "" {
		at = max(own, 0)
	}
	paint := func() { r.RenderPick(sp, text, cursor, shown, at) }
	echo := func(t string, c int) {
		if t != text {
			text, shown, at = t, pickRows(sp.rows, t), max(own, 0)
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
		_, ok := readPromptRaw(keys, []byte(text), echo, promptOpts{arrow: arrow})
		switch {
		case !ok:
			return picked{row: -1, back: true, stop: keys.last == byte(KeyCtrlC)}
		case len(shown) == 0:
		case !sp.rows[shown[at]].own:
			return picked{row: shown[at]}
		case strings.TrimSpace(text) != "":
			return picked{row: -1, text: strings.TrimSpace(text)}
		}
		// Enter on the own row with nothing typed, or on a filter that
		// matches nothing, waits. The end of input waits for nothing: read
		// again, it ended the same way, forever.
		if keys.ended {
			return picked{row: -1, back: true}
		}
	}
}

// pickRows is the rows a picker shows for what is typed: all of them when
// the text is the own row's answer, else the ones it filters to.
func pickRows(rows []Choice, text string) []int {
	if !slices.ContainsFunc(rows, func(c Choice) bool { return c.own }) {
		return matching(rows, text)
	}
	out := make([]int, len(rows))
	for i := range out {
		out[i] = i
	}
	return out
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
func (r *Renderer) RenderPick(sp pickSpec, text string, cursor int, shown []int, at int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.pickTitle, r.pickRows, r.pickShown, r.pickAt = "pick", sp.title, sp.rows, shown, at
	r.pickRight, r.pickLead = sp.right, sp.lead
	r.pickHints = cmp.Or(sp.hints, "[enter] open   [↑↓] move   [esc] back")
	r.promptLabel, r.promptText, r.promptCursor, r.example = sp.label, text, cursor, "type to filter"
	if !r.plain {
		r.paintLocked(true)
	}
}

// pickLines is the picker, its rows scrolled to keep the highlighted one in
// view.
func (r *Renderer) pickLines() []string {
	out := []string{r.heading(r.pickTitle, r.pickRight)}
	if r.pickLead != "" {
		out = append(out, "")
		for _, l := range r.wrapNote(r.pickLead, r.width-4) {
			out = append(out, "  "+r.T.bold(l))
		}
	}
	out = append(out, r.rule())
	// The own row is where the typing shows: a filter box over it would read
	// as a search.
	if !slices.ContainsFunc(r.pickRows, func(c Choice) bool { return c.own }) {
		out = append(out, r.promptRow(), r.rule())
	}
	// The rule, the keys and the blank row under them, as the start screen
	// ends, are kept below the rows.
	room := max(r.rows-1-len(out)-3, 1)
	from := max(r.pickAt-room+1, 0)
	for i := from; i < len(r.pickShown) && i < from+room; i++ {
		out = append(out, r.pickRow(r.pickRows[r.pickShown[i]], i == r.pickAt))
	}
	if len(r.pickShown) == 0 {
		out = append(out, "  "+r.T.dim("nothing matches"))
	}
	return append(out, r.rule(), "  "+r.T.hints(r.pickHints), "")
}

// pickRow is one row, highlighted when at. The own row shows the text typed
// in it, and the cursor while it is highlighted.
func (r *Renderer) pickRow(c Choice, at bool) string {
	switch {
	case c.own && at:
		return "  " + r.T.magenta("▸ ") + r.T.bold(c.Title+": ") + r.withCursor(r.promptText, r.promptCursor)
	case c.own && r.promptText != "":
		return "    " + c.Title + ": " + r.promptText
	case at:
		row := "  " + r.T.magenta("▸ ") + r.T.bold(c.Title)
		if c.Note != "" {
			row += "  " + r.T.gray(c.Note)
		}
		return row
	}
	row := "    " + c.Title
	if c.Note != "" {
		row += "  " + r.T.gray(c.Note)
	}
	return row
}
