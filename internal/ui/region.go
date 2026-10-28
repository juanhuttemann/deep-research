package ui

import (
	"fmt"
	"strings"
)

// region is rows held in the scrolling output, at the cursor, where the
// ask ▸ box, the start screen and the picker are drawn in place: under what
// came before them, as the ask box is under its report, so the three sit
// alike. The picker drew on the alternate screen and the start screen from
// the top of the screen, and in Warp, which shows a command's output at the
// foot of the window, the input jumped between the foot and the top.
//
// Each row is drawn at its own position, found by asking the terminal where
// the cursor is: moving back up by a count of rows landed a row short in
// Warp after a screen had been painted, and the ask box's label was drawn a
// second time over its lower rule. A terminal that does not answer gets the
// relative moves.
type region struct {
	write func(string)
	// rows and width are the terminal's.
	rows, width int
	// top is the screen row, from 1, of the region's first row; 0 when the
	// terminal did not say, and moves are relative to row.
	top    int
	height int // rows held, the cursor's line the first
	row    int // the row the cursor is on, from the region's first
	// locate asks the terminal for the cursor's row; nil in tests and once
	// a terminal has not answered.
	locate func() int
}

func newRegion(write func(string), rows, width int, locate func() int) *region {
	return &region{write: write, rows: rows, width: width, height: 1, locate: locate}
}

// paint draws lines over the rows the region holds, taking more first and
// giving back the ones it no longer needs. Given back, the cursor is left
// at the start of the first of them, where what follows is written; with
// none, the first row is kept, blank. The region never moves to a row it
// does not hold: at the screen's foot the terminal would stop the cursor
// short, and the rows counted from it would be one off.
func (g *region) paint(lines []string) {
	g.hold(len(lines))
	var sb strings.Builder
	for i, l := range lines {
		sb.WriteString(g.move(i))
		sb.WriteString(clip(l, g.width))
		sb.WriteString(eraseLine)
	}
	if len(lines) < g.height {
		sb.WriteString(g.move(len(lines)))
		sb.WriteString(eraseToEnd)
	}
	g.height = max(len(lines), 1)
	g.write(sb.String())
}

// hold takes rows under the region until it has n, the screen scrolling
// when they run past its foot, and asks again where the region now starts.
func (g *region) hold(n int) {
	if n <= g.height {
		return
	}
	g.write(g.move(g.height-1) + strings.Repeat("\n", n-g.height))
	g.row, g.height = n-1, n
	// Rows the screen had room for leave the region where it was.
	if g.top > 0 && g.top+n-1 <= g.rows {
		return
	}
	if g.locate != nil {
		if y := g.locate(); y > 0 {
			g.top = y - g.row
			return
		}
		// One terminal that did not answer will not the next time either.
		g.locate = nil
	}
	g.top = 0
}

// move is the sequence that puts the cursor at the start of the region's
// row i.
func (g *region) move(i int) string {
	d := i - g.row
	g.row = i
	switch {
	case g.top > 0:
		return fmt.Sprintf("\x1b[%d;1H", g.top+i)
	case d < 0:
		return fmt.Sprintf("\r\x1b[%dA", -d)
	case d > 0:
		return fmt.Sprintf("\r\x1b[%dB", d)
	}
	return "\r"
}
