package ui

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// liveRenderer returns a renderer wired for in-place painting at a fixed
// geometry, without needing a real terminal.
func liveRenderer(w *bytes.Buffer, rows, cols int) *Renderer {
	r := NewRenderer(w, Theme{Enabled: true})
	r.plain, r.width, r.rows = false, cols, rows
	return r
}

// paintedRows splits a captured frame into the rows it wrote, stripping the
// cursor-control escapes that separate them.
func paintedRows(out string) []string {
	i := strings.LastIndex(out, cursorHome)
	if i >= 0 {
		out = out[i+len(cursorHome):]
	}
	out = strings.ReplaceAll(out, eraseLine, "")
	return strings.Split(out, crlf)
}

func TestFrameNeverOverflowsTheViewport(t *testing.T) {
	// A run wide enough and named long enough to overflow a small terminal:
	// the frame must be clamped, not allowed to scroll the screen.
	const rows, cols = 20, 60
	var buf bytes.Buffer
	r := liveRenderer(&buf, rows, cols)

	r.Emit(Event{Type: Phase, Phase: "Research", Detail: strings.Repeat("long detail ", 20)})
	for i := range 12 {
		id := strconv.Itoa(i)
		name := "Sub-topic " + id + " " + strings.Repeat("wide 名前 ", 6)
		r.Emit(Event{Type: SubAgent, SubID: id, SubName: name, SubState: "running"})
		r.Emit(Event{Type: Read, SubID: id, SubName: name,
			Line: "read " + strings.Repeat("example.com/", 12)})
	}
	buf.Reset()
	r.mu.Lock()
	r.paintLocked(true)
	r.mu.Unlock()

	got := paintedRows(buf.String())
	if len(got) > rows-1 {
		t.Errorf("frame painted %d rows into a %d-row terminal", len(got), rows)
	}
	for i, line := range got {
		if w := dispWidth(line); w > cols {
			t.Errorf("row %d is %d columns wide, terminal is %d: %q", i, w, cols, line)
		}
	}
}

func TestFrameHeightIsStableAcrossEvents(t *testing.T) {
	// Row count must not drift as events arrive: a frame that grows is a frame
	// that eventually scrolls, and every scroll leaves a stale copy behind.
	const rows, cols = 24, 80
	var buf bytes.Buffer
	r := liveRenderer(&buf, rows, cols)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	for i := range 3 {
		r.Emit(Event{Type: SubAgent, SubID: strconv.Itoa(i), SubName: "topic", SubState: "running"})
	}

	var heights []int
	for i := range 40 {
		r.Emit(Event{Type: Read, SubID: strconv.Itoa(i % 3), Line: "read host" + strconv.Itoa(i) + ".com"})
		buf.Reset()
		r.mu.Lock()
		r.paintLocked(true)
		r.mu.Unlock()
		heights = append(heights, len(paintedRows(buf.String())))
	}
	for i, h := range heights {
		if h != heights[0] {
			t.Fatalf("frame height changed from %d to %d at event %d", heights[0], h, i)
		}
	}
}

func TestFrameRowsEndWithCarriageReturn(t *testing.T) {
	// Raw mode can leave the terminal without output post-processing, where a
	// bare newline drops a row without returning to column 0 and the frame
	// staircases off the right edge.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", SubState: "running"})

	out := buf.String()
	for i := 0; i < len(out); i++ {
		if out[i] == '\n' && (i == 0 || out[i-1] != '\r') {
			t.Fatalf("bare newline at offset %d in live frame: %q", i, out)
		}
	}
}

func TestLiveFrameRepaintsInPlace(t *testing.T) {
	// The viewport is cleared once; every later frame homes the cursor and
	// overwrites, so the transcript must not accumulate copies of the header.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	for i := range 20 {
		r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", SubState: "running",
			Line: "step " + strconv.Itoa(i)})
		r.mu.Lock()
		r.paintLocked(true)
		r.mu.Unlock()
	}
	if n := strings.Count(buf.String(), eraseScreen); n != 1 {
		t.Errorf("viewport cleared %d times, want exactly 1", n)
	}
	if n := strings.Count(buf.String(), cursorHide); n != 1 {
		t.Errorf("cursor hidden %d times, want exactly 1", n)
	}
	if n := strings.Count(buf.String(), cursorHome); n < 20 {
		t.Errorf("only %d in-place repaints for 20 frames", n)
	}
}

func TestCloseRestoresTheCursor(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	r.Close()
	if !strings.HasSuffix(buf.String(), cursorShow) {
		t.Error("Close must make the cursor visible again")
	}
	r.Close() // idempotent
}

func TestPlainRendererLogsOneLinePerEvent(t *testing.T) {
	// A pipe used to receive the entire frame once per event. It should now
	// get a single line, and no cursor control at all.
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	for i := range 10 {
		r.Emit(Event{Type: Read, SubID: "1", Line: "read host" + strconv.Itoa(i) + ".com"})
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("cursor control leaked to a pipe: %q", out)
	}
	if got, want := strings.Count(strings.TrimRight(out, "\n"), "\n")+1, 11; got != want {
		t.Errorf("wrote %d lines for 11 events, want %d:\n%s", got, want, out)
	}
}

func TestProgressReachesFullOnlyWhenDone(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", SubState: "running", Progress: 60})
	if got := r.nodes["1"].Progress; got != 60 {
		t.Errorf("progress = %d, want 60", got)
	}
	// A late, lower progress report must not walk the meter backwards.
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", Progress: 20})
	if got := r.nodes["1"].Progress; got != 60 {
		t.Errorf("progress went backwards to %d", got)
	}
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", SubState: "done"})
	if got := r.nodes["1"].Progress; got != 100 {
		t.Errorf("done node sits at %d%%, want 100", got)
	}
}

func TestQueryProgress(t *testing.T) {
	cases := []struct{ qi, n, done, total, want int }{
		{0, 1, 1, 4, 25},
		{0, 1, 4, 4, 99}, // capped: "done" is what reports 100%
		{1, 2, 1, 2, 75},
		{0, 0, 1, 0, 99}, // degenerate inputs must not divide by zero
	}
	for _, c := range cases {
		if got := queryProgress(c.qi, c.n, c.done, c.total); got != c.want {
			t.Errorf("queryProgress(%d,%d,%d,%d) = %d, want %d",
				c.qi, c.n, c.done, c.total, got, c.want)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"plain", 5},
		{"\x1b[1mbold\x1b[0m", 4}, // escapes occupy no columns
		{"名前", 4},                 // East-Asian wide
		{"⠋ ✓ ─ █ ░", 9},          // box drawing and braille are single width
		{"📊", 2},                  // emoji are double width
	}
	for _, c := range cases {
		if got := dispWidth(c.in); got != c.want {
			t.Errorf("dispWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClipKeepsColourBalanced(t *testing.T) {
	got := clip("\x1b[31mred and long\x1b[0m", 5)
	if dispWidth(got) > 5 {
		t.Errorf("clip returned %d columns: %q", dispWidth(got), got)
	}
	if !strings.HasSuffix(got, cReset) {
		t.Errorf("clip must close the open colour: %q", got)
	}
	if got := clip("名前名前", 3); dispWidth(got) > 3 {
		t.Errorf("clip split a wide rune: %q (%d cols)", got, dispWidth(got))
	}
}

func TestFormatCount(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 6120: "6.1k", 2_500_000: "2.5M"} {
		if got := formatCount(in); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestReportCardCarriesTotals(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.RenderReport(&agent.ResearchResult{
		Findings: []agent.Finding{{URL: "https://arxiv.org/abs/1"}},
	}, 9, 4200)
	out := buf.String()
	// The total is labelled "sources"; how they were obtained is stated
	// beside it rather than asserted by the word "verified".
	for _, want := range []string{"RESEARCH COMPLETE", "sources  9", "4.2k"} {
		if !strings.Contains(out, want) {
			t.Errorf("completion card missing %q:\n%s", want, out)
		}
	}
}

func TestNonResearchPhaseShowsWhatItIsDoing(t *testing.T) {
	// With research finished every sub-agent row reads done; the tree still
	// has to say the run is alive and what it is working on.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 100)
	r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "topic", SubState: "done"})
	r.Emit(Event{Type: Phase, Phase: "Analyze", Detail: "Synthesizing findings"})

	r.mu.Lock()
	rows := strings.Join(r.agentLines(), "\n")
	r.mu.Unlock()
	if !strings.Contains(rows, "Synthesizing findings") {
		t.Errorf("analyze phase does not say what it is doing:\n%s", rows)
	}

	// A finished run must not keep claiming work is in flight.
	r.Emit(Event{Type: Report})
	r.mu.Lock()
	rows = strings.Join(r.agentLines(), "\n")
	r.mu.Unlock()
	if strings.Contains(rows, "Synthesizing findings") {
		t.Errorf("finished run still shows in-flight work:\n%s", rows)
	}
}

// briefPlan builds a plan whose notes are long enough to need wrapping.
func briefPlan(n int) *Plan {
	subs := make([]SubTopic, n)
	for i := range subs {
		subs[i] = SubTopic{
			ID:   string(rune('a' + i)),
			Name: "Sub-topic " + strconv.Itoa(i+1),
			Notes: "Compare the neural network architecture, parameter count, and " +
				"training methodology used by each of the two models under study.",
		}
	}
	return &Plan{Question: "Compare two models", Depth: DepthModes[1], MaxSources: 4, SubTopics: subs}
}

func TestBriefWrapsNotesInsteadOfClippingThem(t *testing.T) {
	// A tall terminal has rows to spare: the note must be readable in full,
	// not cut at the right edge.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 40, 100)
	r.RenderBrief(briefPlan(6))

	out := buf.String()
	if strings.Contains(out, "…") {
		t.Errorf("note was truncated on a terminal with room to wrap:\n%s", out)
	}
	if !strings.Contains(out, "models under study.") {
		t.Errorf("end of the note never rendered:\n%s", out)
	}
	for _, line := range paintedRows(out) {
		if w := dispWidth(line); w > 100 {
			t.Errorf("wrapped row is %d columns wide: %q", w, line)
		}
	}
}

func TestBriefKeepsEverySubTopicBeforeItSpendsRowsOnNotes(t *testing.T) {
	// Rows are scarce here. Showing all six sub-topics with one note line each
	// beats showing three of them with full notes.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 20, 100)
	r.RenderBrief(briefPlan(6))

	out := buf.String()
	for i := 1; i <= 6; i++ {
		if !strings.Contains(out, "Sub-topic "+strconv.Itoa(i)) {
			t.Errorf("sub-topic %d hidden while rows were spent on notes:\n%s", i, out)
		}
	}
	if strings.Contains(out, "more") {
		t.Errorf("sub-topics were hidden unnecessarily:\n%s", out)
	}
}

func TestBriefAlwaysKeepsTheKeyHints(t *testing.T) {
	// The hints are the only thing that says how to launch the run, so they
	// survive even when the brief cannot fit its sub-topics.
	var buf bytes.Buffer
	r := liveRenderer(&buf, 12, 80)
	r.RenderBrief(briefPlan(8))

	out := buf.String()
	if !strings.Contains(out, "[enter]") {
		t.Errorf("key hints dropped from a cramped brief:\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a truncation marker on a cramped brief:\n%s", out)
	}
	if got := len(paintedRows(out)); got > 11 {
		t.Errorf("brief painted %d rows into a 12-row terminal", got)
	}
}

func TestWrapWords(t *testing.T) {
	got := wrapWords("one two three four five", 10)
	want := []string{"one two", "three four", "five"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapWords = %q, want %q", got, want)
	}
	// A word wider than the line is hard-broken rather than allowed to spill.
	for _, line := range wrapWords("short supercalifragilistic", 8) {
		if dispWidth(line) > 8 {
			t.Errorf("line %q exceeds the wrap width", line)
		}
	}
	if got := wrapWords("wide 名前 text", 6); len(got) == 0 {
		t.Error("wrapWords dropped wide-rune input")
	} else {
		for _, line := range got {
			if dispWidth(line) > 6 {
				t.Errorf("wide-rune line %q exceeds the wrap width (%d cols)", line, dispWidth(line))
			}
		}
	}
}

func TestClipAlwaysMarksATruncation(t *testing.T) {
	// A cut landing exactly on the boundary used to produce a string that
	// looked complete: "Inference Speed and Resource Usa" for a name ending
	// in "Usage". Every cut must be visible.
	cases := []struct {
		in string
		w  int
	}{
		{"Inference Speed and Resource Usage", 32},
		{"abcdef", 3},
		{"名前名前名前", 5},
		{"\x1b[2mcoloured and long\x1b[0m", 8},
	}
	for _, c := range cases {
		got := clip(c.in, c.w)
		if dispWidth(got) > c.w {
			t.Errorf("clip(%q, %d) is %d columns", c.in, c.w, dispWidth(got))
		}
		if !strings.Contains(got, "…") {
			t.Errorf("clip(%q, %d) = %q, truncation not marked", c.in, c.w, got)
		}
	}
	// A string that fits is returned untouched, ellipsis and all.
	if got := clip("exact", 5); got != "exact" {
		t.Errorf("clip added a marker to a string that fits: %q", got)
	}
}

func TestEscCancelsARunInFlight(t *testing.T) {
	// d.cancel used to be left nil, so the key reader had nothing to cancel
	// and Esc did nothing at all during a run.
	sink := &MultiSink{}
	d := NewDriver(&blockingAssistant{Assistant: &fakeAssistant{}},
		sink, NewByteReader([]byte{0x1b}), 1)
	plan := newTestPlan("standard", []agent.SubTopic{{ID: "1", Name: "S"}})

	done := make(chan error, 1)
	go func() { _, err := d.Run(context.Background(), plan); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Esc must abort the run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Esc did not cancel the run")
	}
}

// blockingAssistant stalls in ResearchDetail until its context is cancelled.
type blockingAssistant struct{ agent.Assistant }

func (b *blockingAssistant) ResearchDetail(ctx context.Context, _ string, _ []string) (*agent.ResearchDetail, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDetachReleasesTheTerminal(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})
	buf.Reset()

	r.Emit(Event{Type: Detach, Detail: "running detached"})
	out := buf.String()
	if !strings.Contains(out, "detached") {
		t.Errorf("detaching says nothing:\n%s", out)
	}
	if !strings.HasSuffix(out, cursorShow) {
		t.Error("detaching must give the cursor back")
	}

	// Nothing more may be drawn: the reader has their terminal back.
	buf.Reset()
	for range 5 {
		r.Emit(Event{Type: Read, SubID: "1", Line: "read example.com"})
		r.mu.Lock()
		r.paintLocked(true)
		r.mu.Unlock()
	}
	if buf.Len() != 0 {
		t.Errorf("kept drawing after detaching: %q", buf.String())
	}
}

// lineInput is a terminal that refused raw mode: keys arrive only with their
// trailing Enter.
type lineInput struct{ Input }

func (lineInput) Raw() bool { return false }

func TestDepthCyclesInLineMode(t *testing.T) {
	// Without treating a whole line as one keystroke, the Enter that delivers
	// "d" immediately launched the run, so changing depth looked like a no-op.
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	base := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "S"}})

	in := lineInput{NewByteReader([]byte("d\nd\n\n"))}
	got, action := waitBrief(in, r, base, (*Plan).WithDepth)
	if action != actionLaunch {
		t.Errorf("expected launch, got %d", action)
	}
	if got.Depth.Key != "deep" {
		t.Errorf("depth = %q, want deep: line-mode keys were not applied", got.Depth.Key)
	}
}

func TestDepthChangesTheSourceBudget(t *testing.T) {
	// The tier has to move a number the reader can see, or the key reads as
	// dead even when it fires.
	subs := make([]agent.SubTopic, 5)
	for i := range subs {
		subs[i] = agent.SubTopic{ID: strconv.Itoa(i), Name: "t"}
	}
	quick := newTestPlan("quick", subs).WithDepth(DepthModes[0])
	deep := quick.WithDepth(DepthModes[2])

	if quick.MaxSources >= deep.MaxSources {
		t.Errorf("deep (%d sources) must budget more than quick (%d)",
			deep.MaxSources, quick.MaxSources)
	}
	if got, want := quick.PerTopic(), DepthModes[0].MaxSources; got != want {
		t.Errorf("quick per sub-agent = %d, want %d", got, want)
	}
	if got, want := deep.PerTopic(), DepthModes[2].MaxSources; got != want {
		t.Errorf("deep per sub-agent = %d, want %d", got, want)
	}
}

// floodingDetail returns far more findings than any budget allows, so the
// budget is the only thing that can bound what a sub-agent keeps.
type floodingDetail struct{ agent.Assistant }

func (floodingDetail) ResearchDetail(context.Context, string, []string) (*agent.ResearchDetail, error) {
	out := &agent.ResearchDetail{}
	for i := range 20 { // far more than any budget
		out.Findings = append(out.Findings,
			agent.Finding{Title: "f", URL: "https://example.com/" + strconv.Itoa(i)})
	}
	return out, nil
}

func TestSubAgentHonoursItsSourceBudget(t *testing.T) {
	// perSource was computed, passed in, and then discarded with "_ =
	// perSource", so the depth tier had no effect on what a run gathered.
	plan := newTestPlan("quick", []agent.SubTopic{{ID: "1", Name: "S"}})
	plan, _ = plan.WithSourcesPerTopic(3)

	d := NewDriver(floodingDetail{&fakeAssistant{}}, &MultiSink{}, NewByteReader(nil), 1)
	got, _ := d.research(context.Background(), plan)
	if len(got) != 3 {
		t.Errorf("sub-agent gathered %d sources, want the budgeted 3", len(got))
	}
}

// budgetRecorder records the per-query source budget it was handed.
type budgetRecorder struct {
	agent.Assistant
	budget int
}

func (b *budgetRecorder) SetSourceBudget(n int)         { b.budget = n }
func (b *budgetRecorder) SkipSources(func(string) bool) {}

func TestDepthTierReachesTheSearchTool(t *testing.T) {
	// The search tool has its own fixed per-query URL limit. Unless the plan's
	// budget reaches it, a deeper tier cannot gather more than a shallow one
	// and the whole depth control is cosmetic.
	for _, tier := range DepthModes {
		rec := &budgetRecorder{Assistant: &fakeAssistant{}}
		d := NewDriver(rec, &MultiSink{}, NewByteReader(nil), 1)
		plan := newTestPlan(tier.Key, []agent.SubTopic{{ID: "1", Name: "S"}}).WithDepth(tier)
		d.research(context.Background(), plan)
		if rec.budget != tier.MaxSources {
			t.Errorf("%s: search budget = %d, want the tier's %d",
				tier.Key, rec.budget, tier.MaxSources)
		}
	}
}

// TestFooterShowsTokenCount: the footer's counters come from the Token events,
// so a lost or misread total shows up here rather than in a rendered frame.
func TestFooterShowsTokenCount(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.width, r.rows = 100, 40

	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	r.Emit(Event{Type: Token, Tokens: 1234, Sources: 3})

	foot := strings.Join(r.footerLines(), "\n")
	if !strings.Contains(foot, "1.2k tokens") {
		t.Errorf("footer lost the token count:\n%s", foot)
	}
	if !strings.Contains(foot, "3 sources") {
		t.Errorf("footer lost the source count:\n%s", foot)
	}
}

// TestFollowUpSourcesAreLabelled: a deep run over four sub-topics showed 29
// sources and three extra rows, with nothing saying the follow-up round
// added them on top of the plan's 20.
func TestFollowUpSourcesAreLabelled(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: false})
	r.width, r.rows = 100, 40

	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})
	for i, id := range []string{"1", "1", followUpPrefix + "1"} {
		r.Emit(Event{Type: Citation, SubID: id, SubName: "topic " + id, Sources: i + 1})
	}

	rows := r.agentLines()
	if len(rows) != 3 || !strings.Contains(rows[1], "follow-up") || !strings.Contains(rows[2], "topic f1") {
		t.Errorf("follow-up row is not set apart from the plan:\n%s", strings.Join(rows, "\n"))
	}
	if foot := strings.Join(r.footerLines(), "\n"); !strings.Contains(foot, "3 sources (2 planned + 1 follow-up)") {
		t.Errorf("footer total does not say where its sources came from:\n%s", foot)
	}
}

// TestRendererIgnoresOutputAfterClose: Run closes the input and the renderer,
// but the key-reader goroutine is still blocked in Next(). A key pressed in
// that window used to emit an event, repaint a released frame and re-hide the
// cursor Close had just restored.
func TestRendererIgnoresOutputAfterClose(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, Theme{Enabled: true})
	r.width, r.rows, r.plain = 80, 24, false // live-frame mode, as on a TTY
	r.Emit(Event{Type: Phase, Phase: "Research", Detail: "running"})

	r.Close()
	if strings.Contains(buf.String(), cursorHide) && !strings.HasSuffix(buf.String(), cursorShow) {
		t.Fatalf("Close did not restore the cursor")
	}
	buf.Reset()

	// Everything the key reader can still reach after Close.
	r.Emit(Event{Type: Detach, Detail: "late detach"})
	r.SetPrompt("prompt", "late")

	if buf.Len() > 0 {
		t.Errorf("renderer wrote %q after Close", buf.String())
	}
}

// The sub-agent row said "12 src", an abbreviation nobody reads at a glance.
func TestProgressBarSpellsOutSources(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, Theme{})
	for n, want := range map[int]string{1: "100%  1 source ", 12: "100% 12 sources"} {
		if got := r.progressBar(&Node{Progress: 100, Sources: n}); !strings.HasSuffix(got, want) {
			t.Errorf("progressBar with %d sources = %q, want it to end %q", n, got, want)
		}
	}
}

// The reasoning was quoted on the status row, clipped to a fragment too short
// to read. It is wrapped under the row instead, latest last, in rows the
// activity tail gives up — the frame still fits the terminal.
func TestThinkingIsWrappedUnderTheStatusRow(t *testing.T) {
	words := make([]string, 150)
	for i := range words {
		words[i] = "w" + strconv.Itoa(i)
	}
	for _, rows := range []int{40, 16} {
		var buf bytes.Buffer
		r := liveRenderer(&buf, rows, 60)
		r.Emit(Event{Type: SubAgent, SubID: "1", SubName: "Specs", SubState: "done"})
		r.Emit(Event{Type: Phase, Phase: "Fact-Check", Detail: "Verifying claims against sources"})
		r.Emit(Event{Type: Info, Transient: true, Detail: "fact-checking — thinking · 1:38\n" + strings.Join(words, " ")})
		r.mu.Lock()
		frame := r.frameLines()
		r.mu.Unlock()
		r.Close()

		at := slices.IndexFunc(frame, func(l string) bool { return strings.Contains(l, "fact-checking — thinking · 1:38") })
		if at < 0 || strings.Contains(frame[at], "w0") {
			t.Fatalf("%d rows: no status row holding the status line alone:\n%s", rows, strings.Join(frame, "\n"))
		}
		var shown []string
		for _, l := range frame[at+1:] {
			if !strings.HasPrefix(l, "    ") {
				break
			}
			shown = append(shown, l)
		}
		if len(shown) < 2 || len(shown) > maxThinkingRows || !strings.HasSuffix(shown[len(shown)-1], "w149"+cReset) {
			t.Errorf("%d rows: reasoning rows %q, want up to %d ending on the latest words", rows, shown, maxThinkingRows)
		}
		if len(frame) > rows-1 {
			t.Errorf("%d rows: frame is %d rows tall", rows, len(frame))
		}
		for _, l := range frame {
			if w := dispWidth(l); w > 60 {
				t.Errorf("%d rows: row is %d columns wide: %q", rows, w, l)
			}
		}
	}
}

// A page's read and how it went share one row: a spinner while the page is
// read, then a dot coloured by its status. They took a row each, so every
// source cost the tail two.
func TestReadSettlesInPlace(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	read := func(sub, domain string) {
		r.Emit(Event{Type: Read, SubID: sub, URL: "https://" + domain + "/p", Domain: domain, Line: "read " + domain})
	}
	verify := func(sub, domain, status, line string) {
		r.Emit(Event{Type: Verify, SubID: sub, URL: "https://" + domain + "/p", Domain: domain, Status: status, Line: line})
	}
	rows := func() []string {
		r.mu.Lock()
		defer r.mu.Unlock()
		var out []string
		for _, a := range r.acts {
			out = append(out, r.activityText(a))
		}
		return out
	}
	for _, d := range []string{"ok.example", "snip.example", "gone.example", "late.example"} {
		read("1", d)
	}
	spin := r.T.cyan(spinnerFrames[r.frame%len(spinnerFrames)])
	if got := rows(); len(got) != 4 || got[0] != spin+" ok.example/p" {
		t.Fatalf("reads in flight = %q, want four spinner rows", got)
	}

	verify("1", "ok.example", "ok", "✓ ok.example 200 OK")
	verify("1", "snip.example", "degraded", "! snip.example snippet only")
	verify("1", "gone.example", "dropped", "✗ 404 dropped")
	// Another branch reading the same page is its own row.
	read("2", "ok.example")
	verify("2", "ok.example", "unverified", "~ ok.example unverified")
	want := []string{
		r.T.green("●") + " " + r.T.link("https://ok.example/p", "ok.example/p"),
		r.T.yellow("●") + " " + r.T.link("https://snip.example/p", "snip.example/p"),
		r.T.red("●") + " gone.example/p",
		spin + " late.example/p",
		"~ ok.example unverified",
	}
	if got := rows(); !slices.Equal(got, want) {
		t.Errorf("settled rows = %q, want %q", got, want)
	}

	// Fetched past the budget, never verified: gone once the branch is done.
	r.Emit(Event{Type: SubAgent, SubID: "1", SubState: "done"})
	if got := rows(); !slices.Equal(got, slices.Delete(want, 3, 4)) {
		t.Errorf("rows after done = %q, want the unverified read dropped", got)
	}

	// Without colour the dots read alike, so each outcome keeps its glyph.
	r = liveRenderer(&buf, 24, 80)
	r.T.Enabled = false
	read("1", "snip.example")
	verify("1", "snip.example", "degraded", "! snip.example snippet only")
	if got := rows(); !slices.Equal(got, []string{"! snip.example/p"}) {
		t.Errorf("no-colour rows = %q", got)
	}

	// Two pages of one site are two rows that say so.
	r = liveRenderer(&buf, 24, 80)
	r.Emit(Event{Type: Read, SubID: "1", URL: "https://en.wikipedia.org/wiki/Asunci%C3%B3n", Domain: "en.wikipedia.org"})
	r.Emit(Event{Type: Read, SubID: "1", URL: "https://en.wikipedia.org/wiki/Juan_de_Salazar/", Domain: "en.wikipedia.org"})
	r.mu.Lock()
	got := []string{r.acts[0].text, r.acts[1].text}
	r.mu.Unlock()
	if !slices.Equal(got, []string{"en.wikipedia.org/wiki/Asunción", "en.wikipedia.org/wiki/Juan_de_Salazar"}) {
		t.Errorf("rows name %q, want each page", got)
	}
}

// A page that was read, in full or as a snippet, links to itself; one that
// was dropped or is still being read does not.
func TestReadRowsLinkToTheirPages(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	cases := []struct {
		domain, status string
		linked         bool
	}{{"ok.example", "ok", true}, {"snip.example", "degraded", true}, {"gone.example", "dropped", false}, {"late.example", "", false}}
	for _, c := range cases {
		u := "https://" + c.domain + "/p"
		r.Emit(Event{Type: Read, SubID: "1", URL: u, Domain: c.domain})
		if c.status != "" {
			r.Emit(Event{Type: Verify, SubID: "1", URL: u, Domain: c.domain, Status: c.status, Line: "x"})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range cases {
		row := r.activityText(r.acts[i])
		link := "\x1b]8;;https://" + c.domain + "/p\x1b\\" + c.domain + "/p" + linkClose
		if strings.Contains(row, link) != c.linked {
			t.Errorf("%s row %q: linked = %v, want %v", c.status, row, !c.linked, c.linked)
		}
	}
	r.T.Enabled = false
	if row := r.activityText(r.acts[0]); strings.Contains(row, "\x1b") {
		t.Errorf("without colour the row carries an escape: %q", row)
	}
}

// The URL comes from a search engine and goes into a terminal escape: only a
// clickable web address that cannot end the escape early is linked.
func TestLinkTargetTakesOnlySafeWebAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"https://a.example/p?q=1":     "https://a.example/p?q=1",
		"http://a.example":            "http://a.example",
		"javascript:alert(1)":         "",
		"file:///etc/passwd":          "",
		"https://a.example/\x1b\\":    "",
		"https://a.example/?q=\u009c": "",
	} {
		if got := linkTarget(raw); got != want {
			t.Errorf("linkTarget(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := linkTarget("https://a.example/%1b%07"); strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("an encoded control came back raw: %q", got)
	}
}

// A decoded path is printed as the row's text, so an encoded escape in a URL
// must not reach the terminal through it.
func TestReadRowDropsControlsFromThePath(t *testing.T) {
	var buf bytes.Buffer
	r := liveRenderer(&buf, 24, 80)
	r.Emit(Event{Type: Read, SubID: "1", URL: "https://a.example/%1b%5B2Jx", Domain: "a.example"})
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := r.acts[0].text; got != "a.example/[2Jx" {
		t.Errorf("row text = %q, want the controls dropped", got)
	}
}

// A link's URL takes no columns, and a row cut inside its text still closes
// it: open, it would make the rest of the frame part of the link.
func TestLinkedRowsMeasureAndClipAsText(t *testing.T) {
	th := Theme{Enabled: true}
	row := th.link("https://a.example/"+strings.Repeat("x", 200), "abcdefgh")
	if w := dispWidth(row); w != 8 {
		t.Errorf("dispWidth = %d, want 8, the text's", w)
	}
	cut := clip(row, 5)
	if !strings.HasSuffix(cut, linkClose) || dispWidth(cut) != 5 {
		t.Errorf("clip = %q (%d columns), want 5 columns ending on the link's close", cut, dispWidth(cut))
	}
}

// Each key stands out from what it does: the key in the terminal's own text
// colour, the action gray.
func TestKeyHintsPaintKeysApartFromActions(t *testing.T) {
	th := Theme{Enabled: true}
	want := "[enter]" + th.gray(" launch") + "   [b]" + th.gray(" release display")
	if got := th.hints("[enter] launch   [b] release display"); got != want {
		t.Errorf("hints = %q, want %q", got, want)
	}
	if got := (Theme{}).hints("[enter] launch   [q] cancel"); got != "[enter] launch   [q] cancel" {
		t.Errorf("without colour hints = %q, want the text unchanged", got)
	}
}
