package ui

import (
	"bufio"
	"errors"
	"io"
	"os"
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
	r := NewRenderer(w, Theme{Enabled: !noColor})
	in := askInput(stdin, r.plain)
	defer guardTerminal(sig, in, r)()
	if !in.Raw() || r.plain {
		// A terminal that will not go raw reads the line itself.
		AskBox(w, noColor, back)
		line, err := in.NextLine()
		EndAsk(w)
		// Raw input marks a paste even when the output is not drawn on;
		// the markers are not the question.
		line = strings.NewReplacer(pasteStart, "", pasteEnd, "", "\x1b", "").Replace(line)
		return strings.TrimSpace(line), err == nil || line != ""
	}
	keys := &keptInput{Input: in}
	return newAskBox(r, back, func() int { return cursorRow(keys, r.write) }).read(keys)
}

// askInput is the input the ask box reads. With the box not drawn (w is no
// terminal, as with 2>file) a terminal stdin is left cooked: put raw, it read
// the line blind, with no echo and no editing, and stored backspaces and
// arrow keys in the question as bytes.
func askInput(stdin io.Reader, plain bool) Input {
	if f, ok := stdin.(*os.File); ok && plain && IsTTYFile(f) {
		return &lineReader{scan: bufio.NewReader(f)}
	}
	return NewInput(stdin, nil)
}

// keptInput is an Input with bytes put back ahead of it: the keys typed
// while the terminal was saying where the cursor is, which arrive before
// its answer and are the reader's, not the answer's.
// last is the byte read most recently, which says what ended a line.
type keptInput struct {
	Input
	kept []byte
	last byte
	// ended is set once the input has no more keys: a reader that loops
	// until a key gives it something must stop.
	ended bool
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
	k.ended = err != nil
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
	e := &commandEditor{paint: func(text string, at, cursor int) { b.g.paint(b.lines(text, at, cursor)) },
		quit: b.r.armQuit("press Ctrl-C again to exit")}
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
	// It is written below the region, into the scrolling output, whole: a
	// region holds no more rows than the screen has, and kept in it a tall
	// question lost its last lines, the ones just read before sending it.
	b.g.paint(nil)
	if q != "" && !strings.HasPrefix(q, "/") {
		b.r.writeBlock(append([]string{"", b.r.rule()}, b.prompt(q, -1)...))
	}
	return q, ok
}

// lines is the box: a blank row, the label and the text typed between
// rules, wrapped, and what goes under them. cursor is the cursor's byte
// offset in text, -1 for none.
func (b *askBox) lines(text string, at, cursor int) []string {
	out := append([]string{"", b.r.rule()}, b.prompt(text, cursor)...)
	out = append(out, b.r.rule())
	return append(out, b.r.commandBlock(text, at, "[enter] ask   [ctrl+j] new line   [empty enter] "+b.back, b.back)...)
}

// prompt is the label and text, wrapped under the text's first column: a
// question is often longer than a row, and the terminal's line wrapped it.
// The cursor is at byte offset cursor, -1 for none.
func (b *askBox) prompt(text string, cursor int) []string {
	return b.r.labelledRows("ask ▸ ", text, cursor)
}

// labelledRows is a box's label and its text under it, in rows of the
// frame's width: each of the text's lines wrapped under its first column,
// the label on the first row.
func (r *Renderer) labelledRows(label, text string, cursor int) []string {
	// One column short of the row: a frame row never writes the last. On a
	// screen too narrow to leave the text a few columns beside its label,
	// the label goes: forcing those columns made the row wider than the
	// screen, and the clip took the cursor with the row's end.
	room := r.width - 3 - dispWidth(label)
	if room < minTextCols {
		label, room = "", max(r.width-3, 1)
	}
	rows, at := r.editRows(text, cursor, room)
	// While it is edited, at most half the screen, the rows up to the
	// cursor's, and room left for the six rows around a box (blank,
	// heading, rules, keys, blank): a taller box pushed the cursor and the
	// keys under it off the screen, and the ask box's region cannot hold
	// rows the screen does not have. Without a cursor it is a question sent,
	// written to the scrolling output whole.
	if limit := max(min(r.rows/2, r.rows-6), 1); cursor >= 0 && len(rows) > limit {
		from := min(max(at-limit+1, 0), len(rows)-limit)
		rows = rows[from : from+limit]
	}
	for i := range rows {
		lead := strings.Repeat(" ", 2+dispWidth(label))
		if i == 0 {
			lead = "  " + r.T.magenta(label)
		}
		rows[i] = lead + rows[i]
	}
	return rows
}

// minTextCols is the fewest columns a box gives its text beside the label.
const minTextCols = 8

// editRows lays out text being typed in rows of room columns: a row per
// line it holds, each wrapped at any cell. The cursor, at byte offset
// cursor (-1 for none), is drawn in its cell after wrapping, so its colour
// codes are never cut by the wrap; at a line's end it is a cell of its own.
// at is the row the cursor is on, 0 with none.
func (r *Renderer) editRows(text string, cursor, room int) (rows []string, at int) {
	// drawn: without colour the block stands in front of the character, a
	// column of its own. Put in after the wrap, it made that row one column
	// too wide and the region clipped its last character; so it goes in
	// first, and the cursor only says which row it is on.
	drawn := cursor >= 0 && cursor < len(text) && text[cursor] != '\n' && !r.T.Enabled
	if drawn {
		text = text[:cursor] + "█" + text[cursor:]
	}
	off := 0 // the line's byte offset in text
	for line := range strings.SplitSeq(text, "\n") {
		lr, i := r.lineRows(line, cursor-off, room, drawn)
		if i >= 0 {
			at = len(rows) + i
		}
		rows = append(rows, lr...)
		off += len(line) + 1
	}
	return rows, at
}

// lineRows is one line of editRows, the cursor at byte offset cursor in
// it, or not in it when out of range; at is the cursor's row, -1 for none.
// drawn says the cursor's block is in the line already.
func (r *Renderer) lineRows(line string, cursor, room int, drawn bool) (rows []string, at int) {
	idx, end := -1, cursor == len(line) // the cursor's rune index
	if cursor >= 0 && cursor <= len(line) {
		idx = utf8.RuneCountInString(line[:cursor])
		if end {
			line += " "
		}
	}
	at = -1
	rows = wrapCells(line, room)
	for i, row := range rows {
		n := utf8.RuneCountInString(row)
		if idx >= 0 && idx < n {
			at = i
			if !drawn {
				rows[i] = r.cursorAt(row, idx, end)
			}
		}
		idx -= n
	}
	return rows, at
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
