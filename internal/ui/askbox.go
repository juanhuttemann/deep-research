package ui

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Ask reads a question about a report in the ask ▸ box, drawn under the
// report as the start screen draws its own: the label between rules, the
// command menu or the keys under it, back saying what an empty line does.
// The box is read a key at a time, so /resume and /model complete as they
// do on the start screen. It returns "" for an empty line and for Esc,
// which go back; ok is false for Ctrl-C and Ctrl-D, which end the
// questions as they did when the terminal read the line.
func Ask(stdin io.Reader, w io.Writer, noColor bool, back string) (q string, ok bool) {
	sig := catchSignals(true)
	in := NewInput(stdin, nil)
	r := NewRenderer(w, Theme{Enabled: !noColor})
	defer guardTerminal(sig, in, r)()
	if !in.Raw() || r.plain {
		// A terminal that will not go raw reads the line itself.
		AskBox(w, noColor, back)
		line, err := in.NextLine()
		EndAsk(w)
		return strings.TrimSpace(strings.ReplaceAll(line, "\x1b", "")), err == nil || line != ""
	}
	keys := &keptInput{Input: in}
	return newAskBox(r, back, func() int { return cursorRow(keys, r.write) }).read(keys)
}

// keptInput is an Input with bytes put back ahead of it: the keys typed
// while the terminal was saying where the cursor is, which arrive before
// its answer and are the reader's, not the answer's.
// last is the byte read most recently, which says what ended a line.
type keptInput struct {
	Input
	kept []byte
	last byte
}

func (k *keptInput) Next() (byte, error) {
	if len(k.kept) > 0 {
		k.last, k.kept = k.kept[0], k.kept[1:]
		return k.last, nil
	}
	c, err := k.Input.Next()
	if err == nil {
		k.last = c
	}
	return c, err
}

func (k *keptInput) NextWithin(d time.Duration) (byte, error) {
	if len(k.kept) > 0 {
		return k.Next()
	}
	if t, ok := k.Input.(timedInput); ok {
		return t.NextWithin(d)
	}
	return 0, errNoKeyPending
}

// raw reads past what was put back, for the terminal's answer.
func (k *keptInput) raw(d time.Duration) (byte, error) {
	if t, ok := k.Input.(timedInput); ok {
		return t.NextWithin(d)
	}
	return 0, errNoKeyPending
}

// askBox is the ask ▸ box in the scrolling output under a report, drawn in
// a region of its own.
type askBox struct {
	r    *Renderer
	back string
	g    *region
}

// newAskBox is the box for r, locate asking the terminal where the cursor
// is (nil in tests).
func newAskBox(r *Renderer, back string, locate func() int) *askBox {
	return &askBox{r: r, back: back, g: newRegion(r.write, r.rows, r.width, locate)}
}

func (b *askBox) read(in Input) (string, bool) {
	keys, kept := in.(*keptInput)
	if !kept {
		keys = &keptInput{Input: in}
	}
	b.r.write(cursorHide)
	b.r.hidden = true
	e := &commandEditor{paint: func(text string, at, cursor int) { b.g.paint(b.lines(text, at, cursor)) }}
	q, ok := e.read(keys, false)
	if !ok {
		q = ""
	}
	// Esc goes back as an empty line does; anything else that ended the
	// line, Ctrl-C, Ctrl-D, the input, ends the questions.
	ok = ok || keys.last == 0x1b
	// What stays is the question under the upper rule, as the terminal's
	// own line left it. A command or going back leaves nothing: the box is
	// drawn again where it was when the command is done.
	var keep []string
	if q != "" && !strings.HasPrefix(q, "/") {
		keep = b.lines(q, 0, -1)[:2+len(b.prompt(q, -1))]
	}
	b.g.paint(keep)
	return q, ok
}

// lines is the box: a blank row, the label and the text typed between
// rules, wrapped, and what goes under them. cursor is the cursor's byte
// offset in text, -1 for none.
func (b *askBox) lines(text string, at, cursor int) []string {
	out := append([]string{"", b.r.rule()}, b.prompt(text, cursor)...)
	out = append(out, b.r.rule())
	return append(out, b.r.commandBlock(text, at, "[enter] ask   [empty enter] "+b.back, b.back)...)
}

// prompt is the label and text, wrapped under the text's first column: a
// question is often longer than a row, and the terminal's line wrapped it.
// The cursor, at byte offset cursor (-1 for none), is drawn in its cell
// after wrapping, so its colour codes are never cut by the wrap.
func (b *askBox) prompt(text string, cursor int) []string {
	label := "ask ▸ "
	room := max(b.r.width-2-dispWidth(label), 8)
	at, end := -1, cursor >= len(text) // the cursor's rune index; the end is a cell of its own
	if cursor >= 0 {
		at = utf8.RuneCountInString(text[:min(cursor, len(text))])
		if end {
			text += " "
		}
	}
	rows := wrapCells(text, room)
	for i, row := range rows {
		n := utf8.RuneCountInString(row)
		if at >= 0 && at < n {
			rows[i] = b.r.cursorAt(row, at, end)
		}
		at -= n
	}
	for i := range rows {
		lead := strings.Repeat(" ", 2+dispWidth(label))
		if i == 0 {
			lead = "  " + b.r.T.magenta(label)
		}
		rows[i] = lead + rows[i]
	}
	return rows
}

// wrapCells cuts s into rows of at most w columns, at any cell: the text
// being typed has no words to keep whole until it is done.
func wrapCells(s string, w int) []string {
	var out []string
	var cur strings.Builder
	n := 0
	forEachCell(s, func(c rune, cw int) bool {
		if n+cw > w && n > 0 {
			out = append(out, cur.String())
			cur.Reset()
			n = 0
		}
		cur.WriteRune(c)
		n += cw
		return true
	})
	return append(out, cur.String())
}

// dsrWait is how long the terminal has to say where the cursor is.
const dsrWait = 300 * time.Millisecond

// cursorRow asks the terminal which row the cursor is on (DSR 6, answered
// ESC [ row ; col R on the input); 0 when it does not answer in time. Keys
// typed before the answer are put back for the reader.
func cursorRow(in *keptInput, write func(string)) int {
	write("\x1b[6n")
	var reply []byte
	for end := time.Now().Add(dsrWait); time.Now().Before(end); {
		c, err := in.raw(time.Until(end))
		if errors.Is(err, errNoKeyPending) {
			continue
		}
		if err != nil {
			break
		}
		switch {
		case c == 0x1b && len(reply) > 0:
			// An Esc of the reader's, or a key's sequence, then the answer.
			in.kept = append(in.kept, reply...)
			reply = []byte{c}
		case len(reply) == 0 && c != 0x1b:
			in.kept = append(in.kept, c)
			continue
		default:
			reply = append(reply, c)
		}
		if c == 'R' {
			if row := parseCursorRow(string(reply)); row > 0 {
				return row
			}
			// A key's sequence that ended in R: the reader's.
			in.kept = append(in.kept, reply...)
			reply = nil
		}
	}
	in.kept = append(in.kept, reply...)
	return 0
}

// parseCursorRow is the row of a cursor position report, 0 for anything
// else.
func parseCursorRow(s string) int {
	s, ok := strings.CutPrefix(s, "\x1b[")
	if !ok {
		return 0
	}
	row, _, ok := strings.Cut(strings.TrimSuffix(s, "R"), ";")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(row)
	if err != nil {
		return 0
	}
	return n
}
