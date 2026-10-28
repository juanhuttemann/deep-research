package ui

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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
	b := &askBox{r: r, back: back, height: 1, locate: func() int { return cursorRow(keys, r.write) }}
	return b.read(keys)
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

// askBox is the ask ▸ box in the scrolling output under a report. The
// rows it holds are found by asking the terminal where the cursor is, and
// each is drawn at its own position: moving back up by a count of rows
// landed a row short in Warp after the start screen had painted, and the
// label was drawn a second time over the lower rule. A terminal that does
// not answer gets the relative moves.
type askBox struct {
	r    *Renderer
	back string
	// top is the screen row, from 1, of the box's first row; 0 when the
	// terminal did not say, and moves are relative to row.
	top    int
	height int // rows held, the cursor's line the first
	row    int // the row the cursor is on, from the box's first
	// locate asks the terminal for the cursor's row; nil in tests.
	locate func() int
}

func (b *askBox) read(in Input) (string, bool) {
	keys, kept := in.(*keptInput)
	if !kept {
		keys = &keptInput{Input: in}
	}
	b.r.write(cursorHide)
	b.r.hidden = true
	e := &commandEditor{paint: func(text string, at int) { b.paint(b.lines(text, at, true)) }}
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
		keep = b.lines(q, 0, false)[:2+len(b.prompt(q, false))]
	}
	b.paint(keep)
	b.moveTo(b.height)
	b.r.write(eraseToEnd)
	return q, ok
}

// lines is the box: a blank row, the label and the text typed between
// rules, wrapped, and what goes under them.
func (b *askBox) lines(text string, at int, cursor bool) []string {
	out := append([]string{"", b.r.rule()}, b.prompt(text, cursor)...)
	out = append(out, b.r.rule())
	return append(out, b.r.commandBlock(text, at, "[enter] ask   [empty enter] "+b.back, b.back)...)
}

// prompt is the label and text, wrapped under the text's first column: a
// question is often longer than a row, and the terminal's line wrapped it.
func (b *askBox) prompt(text string, cursor bool) []string {
	label := "ask ▸ "
	room := max(b.r.width-2-dispWidth(label), 8)
	if cursor {
		text += "█"
	}
	rows := wrapCells(text, room)
	if cursor {
		last := rows[len(rows)-1]
		rows[len(rows)-1] = strings.TrimSuffix(last, "█") + b.r.T.cyan("█")
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

// paint draws lines over the rows the box holds, taking more first and
// giving back the ones it no longer needs.
func (b *askBox) paint(lines []string) {
	b.hold(len(lines))
	var sb strings.Builder
	for i, l := range lines {
		sb.WriteString(b.move(i))
		sb.WriteString(clip(l, b.r.width))
		sb.WriteString(eraseLine)
	}
	if len(lines) < b.height {
		sb.WriteString(b.move(len(lines)))
		sb.WriteString(eraseToEnd)
		b.height = len(lines)
	}
	b.r.write(sb.String())
}

// hold takes rows under the box until it has n, the screen scrolling when
// they run past its foot, and asks again where the box now starts.
func (b *askBox) hold(n int) {
	if n <= b.height {
		return
	}
	b.r.write(b.move(b.height-1) + strings.Repeat("\n", n-b.height))
	b.row, b.height = n-1, n
	// Rows the screen had room for leave the box where it was.
	if b.top > 0 && b.top+n-1 <= b.r.rows {
		return
	}
	if b.locate != nil {
		if y := b.locate(); y > 0 {
			b.top = y - b.row
			return
		}
		// One terminal that did not answer will not the next time either.
		b.locate = nil
	}
	b.top = 0
}

func (b *askBox) moveTo(i int) { b.r.write(b.move(i)) }

// move is the sequence that puts the cursor at the start of the box's row i.
func (b *askBox) move(i int) string {
	d := i - b.row
	b.row = i
	switch {
	case b.top > 0:
		return fmt.Sprintf("\x1b[%d;1H", b.top+i)
	case d < 0:
		return fmt.Sprintf("\r\x1b[%dA", -d)
	case d > 0:
		return fmt.Sprintf("\r\x1b[%dB", d)
	}
	return "\r"
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
