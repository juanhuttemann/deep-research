package ui

import (
	"bytes"
	"strings"
	"testing"
)

// phasedRun is a live frame that took clicks, past its plan and research
// and into the fact-check, whose two parts streamed their reasoning
// interleaved, as parallel calls do.
func phasedRun(t *testing.T) (*Renderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := liveRenderer(&buf, 40, 120)
	plan := &Plan{Question: "q", MaxSources: 8, SubTopics: []SubTopic{
		{ID: "1", Name: "Land Signs", Notes: "birds and clouds that point to land", Query: "wayfinding land signs"},
		{ID: "2", Name: "Star Paths", Terms: []string{"star compass"}},
	}}
	r.RenderBrief(plan)
	r.Emit(Event{Type: PlanReady, Plan: plan})
	r.TrackClicks(true)
	r.Emit(Event{Type: Phase, Phase: "Plan", Detail: "Planning research scope"})
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "Land Signs", SubState: "running"})
	r.Emit(Event{Type: SubAgent, SubID: "2", SubName: "Star Paths", SubState: "running"})
	r.Emit(Event{Type: Search, SubID: "1", Line: "Searching: birds clouds land"})
	r.Emit(Event{Type: Search, SubID: "2", Line: "Searching: star compass"})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubState: "done"})
	r.Emit(Event{Type: SubAgent, SubID: "2", SubState: "done"})
	r.Emit(Event{Type: Phase, Phase: "Analyze", Detail: "Synthesizing findings into an answer"})
	r.Emit(Event{Type: Thinking, Status: "start", Detail: "The sources agree on birds."})
	r.Emit(Event{Type: Info, Detail: "analyze   some-model"})
	r.Emit(Event{Type: Phase, Phase: "Fact-Check", Detail: "Checking claims"})
	r.Emit(Event{Type: Thinking, Status: "start", Part: "claims 1-2", Detail: "Claim one is "})
	r.Emit(Event{Type: Thinking, Status: "start", Part: "claims 3-4", Detail: "Claim three lacks a quote."})
	r.Emit(Event{Type: Thinking, Part: "claims 1-2", Detail: "supported.\n\nClaim two too."})
	return r, &buf
}

// shown paints the frame and returns the screen it leaves.
func shown(t *testing.T, r *Renderer, buf *bytes.Buffer) *screen {
	t.Helper()
	r.mu.Lock()
	r.paintLocked(true)
	r.mu.Unlock()
	scr := newScreen(r.rows, r.width)
	scr.write(buf.String())
	return scr
}

// clickPhase clicks phase's name where the screen shows it on the strip.
func clickPhase(t *testing.T, r *Renderer, scr *screen, phase string) {
	t.Helper()
	y := scr.rowOf("Fact-Check")
	if y == 0 {
		t.Fatalf("no phase strip on screen:\n%s", dump(scr))
	}
	row := string(scr.cell[y-1])
	x := strings.Index(row, phase)
	if x < 0 {
		t.Fatalf("%s is not on the strip:\n%s", phase, dump(scr))
	}
	// The screen's cells are runes; the strip's marks are wider in bytes.
	r.Click(len([]rune(row[:x]))+1, y)
}

// Once the research was done the frame said only which phase ran: the
// plan, each topic's searches and the calls' reasoning were off the screen
// for good. A click on a phase the run reached shows what it did, where
// the screen shows it; again shuts it.
func TestClickAPhaseShowsWhatItDid(t *testing.T) {
	cases := []struct {
		phase string
		want  []string
	}{
		{"Plan", []string{"Land Signs", "birds and clouds that point to land", "wayfinding land signs", "star compass"}},
		{"Research", []string{"Running sub-agents", "search  birds clouds land", "search  star compass"}},
		{"Analyze", []string{"analyze   some-model", "reasoning", "The sources agree on birds."}},
		{"Fact-Check", []string{"claims 1-2", "Claim one is supported.", "Claim two too.", "claims 3-4", "Claim three lacks a quote."}},
	}
	for _, c := range cases {
		r, buf := phasedRun(t)
		clickPhase(t, r, shown(t, r, buf), c.phase)
		if r.view.phase != c.phase {
			t.Fatalf("a click on %s opened %q", c.phase, r.view.phase)
		}
		scr := shown(t, r, buf)
		for _, w := range c.want {
			if scr.rowOf(w) == 0 {
				t.Errorf("%s's view does not show %q:\n%s", c.phase, w, dump(scr))
			}
		}
		clickPhase(t, r, scr, c.phase)
		if r.view.phase != "" {
			t.Errorf("a second click on %s left %q open", c.phase, r.view.phase)
		}
	}
}

// The fact-check's parts stream at once; each part's reasoning stays whole
// under its own name rather than cut into the other's.
func TestPartsKeepTheirOwnReasoning(t *testing.T) {
	r, _ := phasedRun(t)
	got := r.phases["Fact-Check"].thoughts
	if len(got) != 2 || got[0].text.String() != "Claim one is supported.\n\nClaim two too." ||
		got[1].text.String() != "Claim three lacks a quote." {
		t.Fatalf("thoughts = %+v", got)
	}
}

// A phase not reached did nothing: its name takes no click.
func TestAPhaseNotReachedTakesNoClick(t *testing.T) {
	r, buf := phasedRun(t)
	clickPhase(t, r, shown(t, r, buf), "Summarize")
	if r.view.phase != "" {
		t.Errorf("a click on a phase not reached opened %q", r.view.phase)
	}
}

// Esc pressed to go back from a phase's view must not cancel the run
// behind it: the first closes the view, the next cancels.
func TestEscClosesAPhaseBeforeItCancels(t *testing.T) {
	r, _ := phasedRun(t)
	r.mu.Lock()
	r.view = phaseView{phase: "Plan"}
	r.mu.Unlock()
	cancelled := 0
	d := &Driver{Input: NewByteReader([]byte{27}), frame: r, cancel: func() { cancelled++ }}
	d.startKeyReader()
	<-d.keysDone
	if cancelled != 0 || r.view.phase != "" {
		t.Fatalf("Esc with a view open: cancelled %d times, view %q", cancelled, r.view.phase)
	}
	d = &Driver{Input: NewByteReader([]byte{27}), frame: r, cancel: func() { cancelled++ }}
	d.startKeyReader()
	<-d.keysDone
	if cancelled != 1 {
		t.Errorf("Esc with no view open did not cancel")
	}
}

// A view taller than the frame scrolls, and the running phase's follows its
// newest rows until scrolled up.
func TestAPhaseViewScrolls(t *testing.T) {
	r, buf := phasedRun(t)
	for range 60 {
		r.Emit(Event{Type: Thinking, Part: "claims 3-4", Detail: "\nmore reasoning"})
	}
	r.Emit(Event{Type: Thinking, Part: "claims 3-4", Detail: "\nthe newest line"})
	clickPhase(t, r, shown(t, r, buf), "Fact-Check")
	if scr := shown(t, r, buf); scr.rowOf("the newest line") == 0 {
		t.Fatalf("the running phase's view does not follow its newest row:\n%s", dump(scr))
	}
	r.ScrollPage(-1)
	r.Scroll(-100)
	scr := shown(t, r, buf)
	if scr.rowOf("the newest line") != 0 || scr.rowOf("Checking claims") == 0 || scr.rowOf("rows 1-") == 0 {
		t.Errorf("scrolled to the top, the view does not show its start:\n%s", dump(scr))
	}
	r.Emit(Event{Type: Thinking, Part: "claims 3-4", Detail: "\nnewer still"})
	if scr := shown(t, r, buf); scr.rowOf("Checking claims") == 0 {
		t.Errorf("a view scrolled up moved with a new row:\n%s", dump(scr))
	}
}

// The arrows, the page keys and the wheel scroll; until they did, every
// escape sequence but a click read as nothing.
func TestScrollKeys(t *testing.T) {
	for in, want := range map[string]Key{
		"\x1b[A": KeyUp, "\x1bOB": KeyDown, "\x1b[5~": KeyPageUp, "\x1b[6~": KeyPageDown,
		"\x1b[<64;10;5M": KeyWheelUp, "\x1b[<65;10;5M": KeyWheelDown, "\x1b[1;5A": KeyUnknown,
	} {
		k, err := readKey(NewByteReader([]byte(in)))
		if err != nil || k.Key != want {
			t.Errorf("%q read as %v, %v; want %v", in, k.Key, err, want)
		}
	}
}
