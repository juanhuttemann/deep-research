package ui

import (
	"cmp"
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
	got := pickWith(in, r, pickSpec{title: title, label: label, rows: rows})
	if got.back {
		return -1
	}
	return got.row
}

// pickSpec is what a picker shows: its rows, the row it opens on, the text
// already typed and its key hints ("" for the default). free lets Enter take
// the typed text as the answer when no row holds it.
type pickSpec struct {
	// right is shown at the heading's end, lead under it.
	title, right, lead, label string
	rows                      []Choice
	at                        int
	text, hints               string
	free                      bool
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
	text, shown, cursor := sp.text, matching(sp.rows, sp.text), len(sp.text)
	at := 0
	if sp.text == "" {
		at = max(min(sp.at, len(shown)-1), 0)
	}
	paint := func() { r.RenderPick(sp, text, cursor, shown, at) }
	echo := func(t string, c int) {
		if t != text {
			text, shown, at = t, matching(sp.rows, t), 0
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
		_, ok := readPromptRaw(keys, []byte(text), nil, echo, arrow, nil)
		switch {
		case !ok:
			return picked{row: -1, back: true, stop: keys.last == byte(KeyCtrlC)}
		case len(shown) > 0:
			return picked{row: shown[at]}
		case sp.free && strings.TrimSpace(text) != "":
			return picked{row: -1, text: strings.TrimSpace(text)}
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
func (r *Renderer) RenderPick(sp pickSpec, text string, cursor int, shown []int, at int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.pickTitle, r.pickRows, r.pickShown, r.pickAt = "pick", sp.title, sp.rows, shown, at
	r.pickRight, r.pickLead = sp.right, sp.lead
	r.pickHints, r.pickFree = cmp.Or(sp.hints, "[enter] open   [↑↓] move   [esc] back"), sp.free
	example := "type to filter"
	if sp.free {
		example = "type to filter, or your own answer"
	}
	r.promptLabel, r.promptText, r.promptCursor, r.example = sp.label, text, cursor, example
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
	out = append(out, r.rule(), r.promptRow(), r.rule())
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
	switch {
	case len(r.pickShown) > 0:
	case r.pickFree && strings.TrimSpace(r.promptText) != "":
		out = append(out, "  "+r.T.magenta("▸ ")+"answer: "+r.T.bold(strings.TrimSpace(r.promptText)))
	default:
		out = append(out, "  "+r.T.dim("nothing matches"))
	}
	return append(out, r.rule(), "  "+r.T.hints(r.pickHints), "")
}
