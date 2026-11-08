package ui

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// screen is a small terminal for tests: rows of cells, the cursor, and the
// controls the frame writes. It wraps as soon as a cell is written in the
// last column, as a terminal without deferred wrap does, which is the
// harshest reading of a row written to the full width.
type screen struct {
	rows, cols int
	cell       [][]rune
	y, x       int
	// main is the main screen while the alternate one is up.
	main [][]rune
}

func newScreen(rows, cols int) *screen {
	s := &screen{rows: rows, cols: cols}
	for range rows {
		s.cell = append(s.cell, []rune(strings.Repeat(" ", cols)))
	}
	return s
}

var control = regexp.MustCompile(`^\x1b(\[[0-9;?<]*[ -/]*[@-~]|\][^\x1b\x07]*(\x1b\\|\x07))`)

func (s *screen) write(out string) {
	for out != "" {
		if m := control.FindString(out); m != "" {
			s.control(m)
			out = out[len(m):]
			continue
		}
		r, n := []rune(out[:min(len(out), 4)])[0], len(string([]rune(out[:min(len(out), 4)])[0]))
		out = out[n:]
		switch r {
		case '\r':
			s.x = 0
		case '\n':
			s.down()
		default:
			s.put(r)
		}
	}
}

func (s *screen) control(m string) {
	if !strings.HasPrefix(m, "\x1b[") {
		return // OSC: a link, no cells
	}
	body, final := m[2:len(m)-1], m[len(m)-1]
	if body == "?1049" {
		s.alternate(final == 'h')
		return
	}
	switch {
	case final == 'H':
		y, x := 1, 1
		if p := strings.Split(body, ";"); body != "" {
			y, _ = strconv.Atoi(p[0])
			if len(p) > 1 {
				x, _ = strconv.Atoi(p[1])
			}
		}
		s.y, s.x = y-1, x-1
	case final == 'K' && body == "2":
		s.cell[s.y] = []rune(strings.Repeat(" ", s.cols))
	case final == 'K':
		for i := s.x; i < s.cols; i++ {
			s.cell[s.y][i] = ' '
		}
	case final == 'J' && body == "2":
		for i := range s.cell {
			s.cell[i] = []rune(strings.Repeat(" ", s.cols))
		}
	}
}

// alternate enters (on) or leaves the alternate screen.
func (s *screen) alternate(on bool) {
	switch {
	case on && s.main == nil:
		s.main, s.cell = s.cell, nil
		for range s.rows {
			s.cell = append(s.cell, []rune(strings.Repeat(" ", s.cols)))
		}
	case !on && s.main != nil:
		s.cell, s.main = s.main, nil
	}
}

func (s *screen) down() {
	if s.y == s.rows-1 {
		s.cell = append(s.cell[1:], []rune(strings.Repeat(" ", s.cols)))
		return
	}
	s.y++
}

func (s *screen) put(r rune) {
	w := runeWidth(r)
	if w == 0 {
		return
	}
	s.cell[s.y][s.x] = r
	s.x += w
	if s.x >= s.cols { // immediate wrap
		s.x = 0
		s.down()
	}
}

// rowOf is the screen row, from 1, that holds text; 0 for none.
func (s *screen) rowOf(text string) int {
	for i, row := range s.cell {
		if strings.Contains(string(row), text) {
			return i + 1
		}
	}
	return 0
}

// A click on a header, where the terminal shows it, opens that topic. Rows
// written to the full width and chained by CR LF wrapped on a terminal
// that wraps at once, scrolling the frame up a row: the click had to land
// a row under the header.
func TestClickLandsOnTheHeaderAsShown(t *testing.T) {
	// As a run goes: the brief, the plan confirmed, then the frame, which
	// is what kept it off the alternate screen.
	var buf0 bytes.Buffer
	r := liveRenderer(&buf0, 40, 120)
	plan := &Plan{Question: "q", SubTopics: []SubTopic{{ID: "1", Name: "Land Signs"}}}
	r.RenderBrief(plan)
	r.Emit(Event{Type: PlanReady, Plan: plan})
	r.TrackClicks(true)
	buf := &buf0
	r.T.Enabled = false
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "Land Signs", SubState: "running"})
	r.Emit(Event{Type: SubAgent, SubID: "2", SubName: "Star Paths", SubState: "running"})
	r.Emit(Event{Type: Search, SubID: "1", Line: "Searching: birds clouds land"})
	r.Emit(Event{Type: Search, SubID: "2", Line: "Searching: star compass"})
	r.Emit(Event{Type: Info, SubID: "2", SubName: "Star Paths", Detail: "searxng   10 results from localhost"})
	r.ToggleActivity()
	long := "https://example.org/" + strings.Repeat("a-very-long-path/", 12)
	for range 3 {
		r.Emit(Event{Type: Read, SubID: "2", URL: long})
	}
	r.T.Enabled = true
	r.OpenTopic(2)
	r.OpenTopic(1)
	r.mu.Lock()
	r.paintLocked(true)
	r.mu.Unlock()
	scr := newScreen(r.rows, r.width)
	scr.write(buf.String())
	if scr.main == nil {
		t.Fatalf("the run's frame is not on the alternate screen")
	}
	if scr.rowOf("DEEP RESEARCH") != 1 {
		t.Fatalf("the frame does not start on the first row:\n%s", dump(scr))
	}
	for _, c := range []struct{ sub, name string }{{"1", "Land Signs"}, {"2", "Star Paths"}} {
		y := scr.rowOf("▿ " + c.name)
		if y == 0 {
			t.Fatalf("no header for %s on screen:\n%s", c.name, dump(scr))
		}
		r.Click(3, y)
		if r.opened[c.sub] {
			t.Errorf("a click on %s's header, on screen row %d, did not shut it:\n%s", c.name, y, dump(scr))
		}
	}
}

func dump(s *screen) string {
	var b strings.Builder
	for i, row := range s.cell {
		b.WriteString(strconv.Itoa(i+1) + "|" + strings.TrimRight(string(row), " ") + "\n")
	}
	return b.String()
}
