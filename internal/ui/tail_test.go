package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// groupedRun is a live renderer with two topics whose rows interleave, as
// parallel sub-agents' do, and the tail shown.
func groupedRun(t *testing.T) (*Renderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := liveRenderer(&buf, 40, 120)
	r.T.Enabled = false
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "Land Signs", SubState: "running"})
	r.Emit(Event{Type: SubAgent, SubID: "2", SubName: "Star Paths", SubState: "running"})
	r.Emit(Event{Type: Search, SubID: "1", Line: "Searching: birds clouds land"})
	r.Emit(Event{Type: Search, SubID: "2", Line: "Searching: star compass"})
	r.Emit(Event{Type: Info, SubID: "2", SubName: "Star Paths", Detail: "searxng   10 results from localhost"})
	r.Emit(Event{Type: Read, SubID: "1", URL: "https://hokulea.com/wayfinding", Domain: "hokulea.com"})
	r.ToggleActivity()
	return r, &buf
}

func tail(r *Renderer) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.treeLines()
	return r.activityLines(12)
}

// Every row named its topic, so a run of two topics read as the same two
// names down the whole tail. Each topic is named once, its rows shut under
// it, its latest row beside its name.
func TestTailGroupsRowsUnderTheirTopic(t *testing.T) {
	r, _ := groupedRun(t)
	rows := tail(r)
	joined := strings.Join(rows, "\n")
	if strings.Count(joined, "Land Signs") != 1 || strings.Count(joined, "Star Paths") != 1 {
		t.Fatalf("want each topic named once:\n%s", joined)
	}
	if strings.Contains(joined, "birds clouds land") || strings.Contains(joined, "star compass") {
		t.Errorf("a shut group shows rows other than its latest:\n%s", joined)
	}
	if !strings.Contains(rows[0], "Running sub-agents") {
		t.Errorf("the phase row is not first: %q", rows[0])
	}
	if !strings.Contains(rows[1], "1 ▹ Land Signs") || !strings.Contains(rows[1], "hokulea.com/wayfinding") {
		t.Errorf("header = %q, want its number, name and latest row", rows[1])
	}
	if !strings.Contains(rows[2], "2 ▹ Star Paths") || !strings.Contains(rows[2], "searxng   10 results") {
		t.Errorf("header = %q, want the engine line filed under its topic", rows[2])
	}
	// Tagged with its topic, the engine's line took over the topic's tree
	// row, which says what the topic is doing.
	if strings.Contains(r.nodes["2"].Line, "searxng") {
		t.Errorf("tree row = %q, want the engine line kept out of it", r.nodes["2"].Line)
	}
}

// A digit opens its topic's group, and again shuts it; with the tail
// hidden it shows the tail with that topic open.
func TestDigitOpensATopic(t *testing.T) {
	r, _ := groupedRun(t)
	r.OpenTopic(2)
	rows := tail(r)
	if !strings.Contains(rows[2], "2 ▿ Star Paths") || !strings.Contains(rows[3], "search  star compass") ||
		!strings.Contains(rows[4], "searxng   10 results") || strings.Contains(rows[2], "searxng") {
		t.Fatalf("topic 2 is not open under its header:\n%s", strings.Join(rows, "\n"))
	}
	r.OpenTopic(2)
	if strings.Contains(strings.Join(tail(r), "\n"), "star compass") {
		t.Errorf("a second 2 did not shut it")
	}
	r.ToggleActivity()
	r.OpenTopic(1)
	if !r.showActivity || !r.opened["1"] {
		t.Errorf("1 with the tail hidden did not show it with topic 1 open")
	}
	r.OpenTopic(9) // no ninth topic: nothing
}

// A click on a header opens its group; a click on any other row does
// nothing.
func TestClickOpensTheHeaderUnderIt(t *testing.T) {
	r, buf := groupedRun(t)
	r.mu.Lock()
	r.paintLocked(true)
	var at int
	for row, sub := range r.tail.heads {
		if sub == "2" {
			at = r.tail.top + row + 1 // screen rows count from 1
		}
	}
	r.mu.Unlock()
	if at == 0 {
		t.Fatal("no header recorded for topic 2")
	}
	if got := paintedRows(buf.String())[at-1]; !strings.Contains(got, "Star Paths") {
		t.Fatalf("screen row %d = %q, want topic 2's header", at, got)
	}
	r.Click(5, at+1) // the row under it: topic 2's, with 1 shut, nothing
	if r.opened["2"] || r.opened["1"] {
		t.Fatalf("a click off a header opened a topic")
	}
	r.Click(5, at)
	if !r.opened["2"] {
		t.Fatalf("a click on topic 2's header did not open it")
	}
	if got := strings.Join(paintedRows(buf.String()), "\n"); !strings.Contains(got, "star compass") {
		t.Errorf("the repaint does not show the opened rows:\n%s", got)
	}
}

// An open group too long for the tail shows its latest rows and counts the
// rest, and the others still get their header.
func TestOpenGroupKeepsItsLatestRows(t *testing.T) {
	r, _ := groupedRun(t)
	for i := range 30 {
		r.Emit(Event{Type: Search, SubID: "2", Line: "Searching: q" + string(rune('a'+i%26))})
	}
	r.OpenTopic(2)
	rows := tail(r)
	joined := strings.Join(rows, "\n")
	if len(rows) != 12 || !strings.Contains(joined, "Land Signs") || !strings.Contains(joined, "earlier") ||
		!strings.Contains(rows[len(rows)-1], "search  qd") {
		t.Errorf("want 12 rows with the other header, a count of the earlier and the latest last:\n%s", joined)
	}
}

// The terminal reports clicks only while the live frame is up, and only
// for a run with a key reader to take them.
func TestClicksReportedOnlyDuringTheFrame(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 30, 100)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	if strings.Contains(buf.String(), mouseOn) {
		t.Fatal("a run without a key reader asked for clicks")
	}
	buf.Reset()
	r = liveRenderer(&buf, 30, 100)
	r.TrackClicks(true)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	if strings.Count(buf.String(), mouseOn) != 1 {
		t.Fatal("the live frame did not ask for clicks")
	}
	r.Close()
	if out := buf.String(); strings.LastIndex(out, mouseOff) < strings.LastIndex(out, mouseOn) {
		t.Fatal("closing the frame left the terminal reporting clicks")
	}
}

func TestReadKeyDecodesClicksAndDigits(t *testing.T) {
	cases := []struct {
		in   string
		want Keystroke
	}{
		{"\x1b[<0;12;7M", Keystroke{Key: KeyClick, X: 12, Y: 7}},
		{"\x1b[<4;3;250M", Keystroke{Key: KeyClick, X: 3, Y: 250}}, // shift-click
		{"\x1b[<0;12;7m", Keystroke{Key: KeyUnknown}},              // release
		{"\x1b[<2;12;7M", Keystroke{Key: KeyUnknown}},              // right button
		{"\x1b[<64;12;7M", Keystroke{Key: KeyWheelUp}},             // wheel: scrolls, not a click
		{"\x1b[<32;12;7M", Keystroke{Key: KeyUnknown}},             // drag
		{"3", Keystroke{Key: '3'}},
		{"0", Keystroke{Key: KeyUnknown}},
	}
	for _, c := range cases {
		got, err := readKey(NewByteReader([]byte(c.in)))
		if err != nil || got != c.want {
			t.Errorf("readKey(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
}

// A tail too short for everything gave way oldest first, so a topic that
// started early was pushed out by the phases after it, while still busy,
// and nothing could bring it back. The topics stay; the run's rows go.
func TestShortTailKeepsTheTopics(t *testing.T) {
	r, _ := groupedRun(t)
	for _, p := range []string{"Analyze", "Fact-Check", "Summarize"} {
		r.Emit(Event{Type: Phase, Phase: p, Detail: p + " step"})
	}
	r.mu.Lock()
	rows := r.activityLines(3)
	r.mu.Unlock()
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Land Signs") || !strings.Contains(joined, "Star Paths") ||
		!strings.Contains(rows[2], "Summarize step") {
		t.Errorf("want both topics and the newest phase:\n%s", joined)
	}
}

// Opened with one spare row, a group spent it on its count of earlier rows
// and, its header no longer showing the latest, showed less than shut.
func TestOpenGroupWithOneRowShowsTheLatest(t *testing.T) {
	r, _ := groupedRun(t)
	r.OpenTopic(2)
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := r.activityLines(4) // phase, two headers, one spare
	if !strings.Contains(rows[3], "searxng   10 results") || strings.Contains(strings.Join(rows, "\n"), "earlier") {
		t.Errorf("want the latest row under the header:\n%s", strings.Join(rows, "\n"))
	}
	rows = r.activityLines(3) // no spare: the header keeps it
	if !strings.Contains(rows[2], "searxng   10 results") {
		t.Errorf("want the header to keep the latest with no room under it:\n%s", strings.Join(rows, "\n"))
	}
}

// An open topic grows a row with each page it reads, moving the headers
// under it down one, while the frame repaints several times a second: a
// click aimed at a header landed on the row that had just taken its place.
// A header there a moment ago still takes the click; one there longer ago
// does not, and a click on an open topic's rows shuts it.
func TestClickFindsAHeaderThatJustMoved(t *testing.T) {
	r, _ := groupedRun(t)
	now := time.Now()
	r.clock = func() time.Time { return now }
	r.OpenTopic(1)
	header2 := func() int {
		r.mu.Lock()
		defer r.mu.Unlock()
		l := r.shown[len(r.shown)-1]
		for row, sub := range l.heads {
			if sub == "2" {
				return l.top + row + 1
			}
		}
		t.Fatal("no header for topic 2")
		return 0
	}
	seen := header2()
	now = now.Add(100 * time.Millisecond)
	r.Emit(Event{Type: Info, SubID: "1", SubName: "Land Signs", Detail: "searxng   9 results"})
	if header2() != seen+1 {
		t.Fatalf("topic 1's new row did not move topic 2's header")
	}
	now = now.Add(200 * time.Millisecond)
	r.Click(1, seen) // now topic 1's last row, a moment ago topic 2's header
	if !r.opened["2"] || !r.opened["1"] {
		t.Fatalf("the click did not open the header seen there: opened = %v", r.opened)
	}

	r.OpenTopic(2) // shut it again
	seen = header2()
	now = now.Add(time.Second)
	r.Emit(Event{Type: Info, SubID: "1", SubName: "Land Signs", Detail: "searxng   8 results"})
	now = now.Add(clickGrace + time.Millisecond)
	r.Click(1, seen) // long since topic 1's row: it shuts topic 1
	if r.opened["2"] || r.opened["1"] {
		t.Errorf("want a click on topic 1's row to shut it, and nothing else: opened = %v", r.opened)
	}
}

// The tail's rows no longer carry a name each, so a header shows its
// topic's name whole where the row has room.
func TestHeaderShowsTheWholeName(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 30, 130)
	r.T.Enabled = false
	name := "Reading Land Signs and the Voyaging Revival"
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: name, SubState: "running"})
	r.Emit(Event{Type: Search, SubID: "1", Line: "Searching: birds"})
	r.ToggleActivity()
	if rows := tail(r); !strings.Contains(strings.Join(rows, "\n"), name+" ") {
		t.Errorf("the name is cut:\n%s", strings.Join(rows, "\n"))
	}
}

// A tail just hidden, or squeezed out by a resize, still took a click on a
// header it had shown a moment before.
func TestNoClickWithoutATail(t *testing.T) {
	r, _ := groupedRun(t)
	r.mu.Lock()
	r.paintLocked(true)
	l := r.shown[len(r.shown)-1]
	var at int
	for row := range l.heads {
		at = l.top + row + 1
	}
	r.rows = 8 // too short for a tail
	r.paintLocked(true)
	r.mu.Unlock()
	r.Click(1, at)
	if len(r.opened) != 0 && (r.opened["1"] || r.opened["2"]) {
		t.Errorf("a click with no tail on screen opened a topic: %v", r.opened)
	}
}

// On Warp's main screen a program's output is a block that grows, and a
// click is reported by its row in that block: rows drawn by position and
// the rows clicks named came apart. The live frame of a run that takes
// clicks is on the alternate screen, left before the report and on close.
func TestLiveFrameOnTheAlternateScreen(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 30, 100)
	r.TrackClicks(true)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	out := buf.String()
	if !strings.Contains(out, altOn) || strings.Index(out, altOn) > strings.Index(out, eraseScreen) {
		t.Fatalf("the live frame is not drawn on the alternate screen: %q", out)
	}
	r.RenderReport(nil, 0, 0)
	out = buf.String()
	if strings.Count(out, altOff) != 1 || strings.LastIndex(out, mouseOff) > strings.Index(out, altOff) {
		t.Errorf("the report did not leave the alternate screen, clicks off first: %q", out)
	}
	r.Close()
	if strings.Count(buf.String(), altOff) != 1 {
		t.Errorf("left the alternate screen twice")
	}

	buf.Reset()
	r = liveRenderer(&buf, 30, 100)
	r.TrackClicks(true)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Close() // a cancel or an error
	if !strings.Contains(buf.String(), altOff) {
		t.Errorf("closing the frame left the terminal on the alternate screen")
	}

	buf.Reset()
	r = liveRenderer(&buf, 30, 100) // no key reader: as before
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Close()
	if strings.Contains(buf.String(), altOn) {
		t.Errorf("a run without clicks took the alternate screen")
	}
}

// A run paints before its frame: the brief, and the plan's event once it is
// confirmed. The frame took the alternate screen only on the screen's
// first clear, which those had already done, so in a real run it never did
// and clicks in Warp still landed rows off; a fresh renderer, as the tests
// had, always did.
func TestFrameTakesTheAlternateScreenAfterTheBrief(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 30, 100)
	plan := &Plan{Question: "q", SubTopics: []SubTopic{{ID: "1", Name: "A"}}}
	r.RenderBrief(plan)
	before := buf.Len()
	r.Emit(Event{Type: PlanReady, Plan: plan})
	if buf.Len() != before {
		t.Errorf("the plan's event drew the brief again: %q", buf.String()[before:])
	}
	r.TrackClicks(true)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.mu.Lock()
	r.paintLocked(true) // the ticker's, should the event's have been throttled
	r.mu.Unlock()
	r.Close()
	out := buf.String()[before:]
	if !strings.Contains(out, altOn) {
		t.Fatalf("the run's frame is not on the alternate screen")
	}
	if i := strings.Index(out, altOn); !strings.Contains(out[i:], eraseScreen) || !strings.Contains(out, altOff) {
		t.Errorf("want the alternate screen cleared once entered, and left on close: %q", out)
	}
}

// Once the phases and the run's notes filled the tail, the rows left for an
// open topic were none: it showed open with nothing under it. An open
// topic's rows go before the run's older rows; the newest run row stays.
func TestOpenTopicOutranksRunNotes(t *testing.T) {
	r, _ := groupedRun(t)
	r.OpenTopic(2)
	for _, p := range []string{"Analyze", "Fact-Check", "Summarize"} {
		r.Emit(Event{Type: Phase, Phase: p, Detail: p + " step"})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := r.activityLines(5)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "star compass") || !strings.Contains(joined, "Land Signs") ||
		!strings.Contains(joined, "Summarize step") {
		t.Errorf("want topic 2's rows, both headers and the newest phase:\n%s", joined)
	}
	if strings.Contains(joined, "Analyze step") {
		t.Errorf("an older phase kept a row topic 2 needed:\n%s", joined)
	}
}
