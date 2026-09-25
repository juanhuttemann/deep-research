package ui

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// spinnerFrames are cycled while a sub-agent is running.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// pipeline is the fixed phase order shown in the progress strip, so the run
// always says how far along it is rather than only what it is doing now.
var pipeline = []string{"Plan", "Research", "Analyze", "Fact-Check", "Summarize"}

const (
	// paintInterval throttles live repaints. A single source produces five
	// events in a burst; repainting each one costs a full frame of terminal
	// traffic and shows nothing a coalesced frame would not.
	paintInterval = 60 * time.Millisecond
	// tickInterval animates the spinner and the elapsed clock between events.
	// Analyze, fact-check and summarize are single model calls that emit
	// nothing for a minute or more; without a tick the UI looks hung.
	tickInterval = 120 * time.Millisecond
	// activityKeep bounds the retained event tail.
	activityKeep = 64
	// minNameCol / maxNameCol bound the sub-agent name column so the tree
	// stays aligned without crowding out the status text beside it.
	minNameCol = 14
	maxNameCol = 40
	// minNoteCol is the narrowest column worth wrapping a note into.
	minNoteCol = 24
	// maxNoteLines caps how tall one sub-topic's note may grow in the brief.
	maxNoteLines = 6
)

// Node is one sub-agent in the supervisor tree. Each node occupies exactly one
// row of the frame: its status text is replaced, never appended to, so rows
// never shift under the reader's eye while a run is in flight.
type Node struct {
	ID       string
	Name     string
	State    string // running | queued | done | error
	Progress int
	Line     string // current activity, replaced on every event
	Sources  int
}

// activity is one entry in the scrolling event tail.
type activity struct {
	at   time.Duration
	sub  string
	text string
}

// Renderer paints the live run. On a terminal it keeps one frame pinned to the
// viewport and repaints it in place; the frame is measured in display columns
// and clamped to the terminal's rows so it can never wrap or scroll, which is
// what would otherwise leave stale copies of earlier frames on screen.
//
// On a non-terminal writer it degrades to a sequential log — one line per
// event — so pipes, captured stdout and CI logs stay readable and free of
// cursor control.
type Renderer struct {
	W io.Writer
	T Theme

	// mu guards every field below. Events arrive from the driver's parallel
	// sub-agents (serialised by MultiSink) and the animation ticker paints
	// from its own goroutine, so both meet here.
	mu    sync.Mutex
	clock func() time.Time

	tty   *os.File // non-nil when W is an interactive terminal
	plain bool     // true when output is a pipe/file: log instead of repaint
	width int
	rows  int

	mode     string // brief | tree | report
	plan     *Plan
	nodes    map[string]*Node
	order    []string
	phase    string
	detail   string
	question string

	tokens  int
	sources int
	acts    []activity

	// promptLabel is non-empty while the reader is typing a line; promptText
	// is what they have typed so far. Raw mode echoes nothing, so the frame
	// has to draw it.
	promptLabel string
	promptText  string

	startTime time.Time
	detached  bool
	done      bool
	frame     int
	result    *agent.ResearchResult

	painted   int  // rows written by the previous frame
	cleared   bool // viewport has been cleared for the live frame
	released  bool // detached: the frame has handed the terminal back
	hidden    bool // cursor is currently hidden
	closed    bool // Close has released the terminal; writes are ignored
	lastPaint time.Time
	stop      chan struct{}
}

// NewRenderer builds a renderer for w. It probes the terminal size when w is a
// *os.File connected to a TTY; otherwise it falls back to log mode.
func NewRenderer(w io.Writer, theme Theme) *Renderer {
	r := &Renderer{
		W:         w,
		T:         theme,
		width:     80,
		rows:      24,
		clock:     time.Now,
		nodes:     map[string]*Node{},
		startTime: time.Now(),
		plain:     true,
	}
	if f, ok := w.(*os.File); ok {
		if rows, cols := termSize(f); cols > 0 {
			r.tty, r.width, r.rows, r.plain = f, cols, rows, false
		}
	}
	return r
}

// RenderBrief shows the research plan and switches to brief mode.
func (r *Renderer) RenderBrief(plan *Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan = plan
	r.question = plan.Question
	r.result = nil
	r.detached = false
	r.mode = "brief"
	r.phase = ""
	r.frame = 0
	if r.plain {
		r.logBrief(plan)
		return
	}
	r.paintLocked(true)
}

// RenderReport draws the completion card. The totals are passed in because the
// renderer is only wired as an event sink when it is drawing the live UI;
// headless runs would otherwise report zero sources and tokens.
func (r *Renderer) RenderReport(res *agent.ResearchResult, sources, tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopTickerLocked()
	r.sources, r.tokens = sources, tokens
	r.result = res
	r.mode = "report"
	r.done = true
	if r.plain {
		r.writeBlock(r.reportLines())
		return
	}
	// The live frame owned the viewport; release it and let the completion
	// card scroll normally so the report printed after it reads as one
	// continuous transcript.
	r.write(cursorHome + eraseToEnd)
	r.writeBlock(r.reportLines())
	r.showCursorLocked()
	r.painted, r.cleared = 0, false
}

// Close releases the terminal: it stops the animation ticker and makes the
// cursor visible again. It is safe to call more than once.
//
// After it returns the renderer writes nothing more. The key-reader goroutine
// is still blocked in Input.Next() when Run's deferred Closes fire, so a key
// pressed in that window would otherwise repaint a frame that has already been
// handed back and re-hide the cursor Close just restored.
func (r *Renderer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.stopTickerLocked()
	// A live frame still owns the viewport on the cancel and error paths, and
	// the cursor sits at the end of its last row. Whatever the caller prints
	// next would land on that row, so the frame is closed off first.
	if r.painted > 0 {
		r.write(crlf)
		r.painted = 0
	}
	r.showCursorLocked()
	r.closed = true
}

// Emit folds an event into the live state and schedules a repaint.
func (r *Renderer) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.apply(e)

	if r.plain {
		r.logEvent(e)
		return
	}
	if r.mode == "tree" {
		r.startTickerLocked()
	}
	r.paintLocked(e.Type == Detach)
}

// SetPrompt draws the line the reader is typing. label empty closes it.
func (r *Renderer) SetPrompt(label, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promptLabel, r.promptText = label, text
	if r.plain {
		return
	}
	r.paintLocked(true)
}

// promptRow renders the in-progress line with a block cursor.
func (r *Renderer) promptRow() string {
	return "  " + r.T.magenta(r.promptLabel+" ▸ ") + r.promptText + r.T.cyan("█")
}

// apply folds one event into the live state. It must be called with mu held.
func (r *Renderer) apply(e Event) {
	switch e.Type {
	case Phase:
		r.phase, r.detail = e.Phase, e.Detail
		if r.mode == "brief" || r.mode == "" {
			r.mode = "tree"
		}
		r.pushActivity("", "▸ "+e.Detail)
	case Search, Read, Verify, Citation:
		r.applySourceEvent(e)
	case Token:
		r.tokens = e.Tokens
		r.sources = max(r.sources, e.Sources)
	case SubAgent:
		r.applySubAgent(e)
	case Detach:
		r.detached = true
	case Info:
		// Progress, not failure: the same line without the error glyph.
		if e.SubID != "" {
			r.node(e.SubID, e.SubName).Line = e.Detail
		}
		r.pushActivity(e.SubID, e.Detail)
	case Error:
		if e.SubID != "" {
			r.node(e.SubID, e.SubName).Line = e.Detail
		}
		r.pushActivity(e.SubID, "! "+e.Detail)
	case Report:
		r.done = true
	}
}

// applySourceEvent handles the per-source events a sub-agent emits while it
// searches, reads and verifies.
func (r *Renderer) applySourceEvent(e Event) {
	if e.SubID != "" {
		n := r.node(e.SubID, e.SubName)
		if n.State != "done" && n.State != "error" {
			n.State = "running"
		}
		if e.Line != "" {
			n.Line = e.Line
		}
		if e.Type == Citation {
			n.Sources++
		}
	}
	r.pushActivity(e.SubID, e.Line)
	r.sources = max(r.sources, e.Sources)
}

// applySubAgent updates a node's lifecycle state. Progress only ever advances:
// sub-agents run in parallel and their events can land out of order, and a
// meter that walks backwards reads as a stall.
func (r *Renderer) applySubAgent(e Event) {
	n := r.node(e.SubID, e.SubName)
	if e.SubState != "" {
		n.State = e.SubState
	}
	if e.Progress > n.Progress {
		n.Progress = e.Progress
	}
	if e.SubState == "done" {
		n.Progress = 100
	}
	if e.Line != "" {
		n.Line = e.Line
	}
}

// pushActivity appends to the bounded event tail shown at the foot of the
// frame.
func (r *Renderer) pushActivity(sub, text string) {
	if text == "" {
		return
	}
	r.acts = append(r.acts, activity{at: r.clock().Sub(r.startTime), sub: sub, text: text})
	if len(r.acts) > activityKeep {
		r.acts = r.acts[len(r.acts)-activityKeep:]
	}
}

func (r *Renderer) node(id, name string) *Node {
	if id == "" {
		// The invented ID must not land on one a real sub-agent already uses:
		// the node map is keyed by it, so a collision merges two branches into
		// one and the tree shows fewer sub-agents than the run started.
		for n := len(r.order) + 1; ; n++ {
			if candidate := fmt.Sprintf("n%d", n); r.nodes[candidate] == nil {
				id = candidate
				break
			}
		}
	}
	if n, ok := r.nodes[id]; ok {
		if name != "" {
			n.Name = name
		}
		return n
	}
	n := &Node{ID: id, Name: name, State: "queued"}
	r.nodes[id] = n
	r.order = append(r.order, id)
	return n
}

// ---- live painting --------------------------------------------------------

// startTickerLocked launches the animation goroutine once per run.
func (r *Renderer) startTickerLocked() {
	if r.stop != nil {
		return
	}
	r.stop = make(chan struct{})
	go func(stop chan struct{}) {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.mu.Lock()
				if r.mode == "tree" && !r.done {
					r.paintLocked(true)
				}
				r.mu.Unlock()
			}
		}
	}(r.stop)
}

func (r *Renderer) stopTickerLocked() {
	if r.stop != nil {
		close(r.stop)
		r.stop = nil
	}
}

// paintLocked repaints the frame in place. force bypasses the throttle; event
// driven paints are coalesced so a burst of five events per source costs one
// frame instead of five.
func (r *Renderer) paintLocked(force bool) {
	now := r.clock()
	if !force && now.Sub(r.lastPaint) < paintInterval {
		return
	}
	r.lastPaint = now
	r.frame++

	// Detached: hand the terminal back and stop drawing. The run itself keeps
	// going and still prints its report. There is no second process to hand
	// the work to, so what is released is the display and the terminal mode —
	// the process keeps the foreground until the report is written, and the
	// notice says so rather than implying a shell prompt is coming back.
	if r.detached {
		if !r.released {
			r.stopTickerLocked()
			r.write(cursorHome + eraseToEnd)
			r.writeBlock([]string{r.T.yellow(
				"  ● detached — display released; the run still holds this terminal")})
			r.showCursorLocked()
			r.painted, r.cleared, r.released = 0, false, true
		}
		return
	}

	// Re-probe on every frame: a terminal resized mid-run must not keep
	// painting to the old geometry, which is a guaranteed wrap-and-scroll.
	if r.tty != nil {
		if rows, cols := termSize(r.tty); cols > 0 {
			if cols != r.width || rows != r.rows {
				r.width, r.rows, r.painted = cols, rows, 0
				r.write(eraseScreen)
			}
		}
	}

	lines := r.frameLines()

	var sb strings.Builder
	if !r.hidden {
		sb.WriteString(cursorHide)
		r.hidden = true
	}
	if !r.cleared {
		sb.WriteString(eraseScreen)
		r.cleared = true
	}
	sb.WriteString(cursorHome)
	for i, l := range lines {
		if i > 0 {
			sb.WriteString(crlf)
		}
		sb.WriteString(l)
		sb.WriteString(eraseLine)
	}
	// Wipe rows the previous, taller frame left behind.
	for i := len(lines); i < r.painted; i++ {
		sb.WriteString(crlf)
		sb.WriteString(eraseLine)
	}
	r.painted = len(lines)
	r.write(sb.String())
}

// frameLines builds the whole frame, already clipped to the terminal width and
// clamped to its height. The returned slice is never longer than rows-1, so
// painting it cannot scroll the viewport.
func (r *Renderer) frameLines() []string {
	var lines []string
	switch r.mode {
	case "report":
		lines = r.reportLines()
	case "brief":
		lines = r.briefLines()
	default:
		lines = r.treeLines()
	}
	limit := r.rows - 1
	if limit < 1 {
		limit = 1
	}
	for i, l := range lines {
		lines[i] = clip(l, r.width)
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

// ---- frame sections -------------------------------------------------------

func (r *Renderer) briefLines() []string {
	if r.plan == nil {
		return nil
	}
	head, foot := r.briefHead(), r.briefFoot()
	subs := r.plan.SubTopics

	// Names are listed with their number, because the rename and delete keys
	// ask for one; the column is sized to the numbered label so numbering
	// never eats into the name itself.
	labels := make([]string, len(subs))
	nameCol := minNameCol
	for i, sub := range subs {
		labels[i] = fmt.Sprintf("%d. %s", i+1, sub.Name)
		nameCol = max(nameCol, dispWidth(labels[i]))
	}
	nameCol = min(nameCol, r.nameColCap())

	// Every row is "  ├─ <name>  <note>"; continuations align under the note.
	// The prefix is measured in columns, not bytes: the connector is a
	// three-byte rune two columns wide.
	indent := dispWidth("  ├─ ") + nameCol + dispWidth("  ")
	notes := make([][]string, len(subs))
	for i, sub := range subs {
		notes[i] = r.wrapNote(sub.Notes, r.width-indent)
	}

	// A note the reader cannot finish is worse than a tall brief, so the
	// sub-topic block spends every row the fixed chrome leaves it on more of
	// the note text. Only when even one row each will not fit does it start
	// hiding sub-topics.
	perTopic, hidden := len(notes), 0
	if !r.plain {
		room := r.rows - 1 - len(head) - len(foot)
		if perTopic = fitNoteLines(notes, room); perTopic == 0 {
			perTopic = 1
			keep := max(min(room-1, len(subs)), 0)
			subs, notes, hidden = subs[:keep], notes[:keep], len(subs)-keep
		}
	}

	out := head
	for i := range subs {
		conn, guide := "├─", "│ "
		if i == len(subs)-1 && hidden == 0 {
			conn, guide = "└─", "  "
		}
		shown := notes[i]
		if len(shown) > perTopic {
			shown = append([]string{}, shown[:perTopic]...)
			shown[perTopic-1] += "…"
		}
		out = append(out, fmt.Sprintf("  %s %s  %s",
			r.T.dim(conn), pad(labels[i], nameCol), r.T.dim(first(shown))))
		for _, l := range rest(shown) {
			out = append(out, fmt.Sprintf("  %s %s  %s",
				r.T.dim(guide), strings.Repeat(" ", nameCol), r.T.dim(l)))
		}
	}
	if hidden > 0 {
		out = append(out, "  "+r.T.dim(fmt.Sprintf("└─ … %d more", hidden)))
	}
	return append(out, foot...)
}

// nameColCap bounds the sub-topic name column. Names now share their row with
// text that wraps, so a wide terminal can afford a wide name column; the
// fraction is what stops a narrow one from spending most of the row on it.
func (r *Renderer) nameColCap() int {
	return min(max(r.width*2/5, minNameCol), maxNameCol)
}

func (r *Renderer) briefHead() []string {
	return []string{
		r.heading("RESEARCH BRIEF", ""),
		r.rule(),
		"  " + r.T.bold(r.plan.Question),
		"",
		// The per-sub-agent figure is what the reader asked for with --sources
		// and what each branch actually gets, so a run budget that reduced the
		// request is visible in the brief instead of only in the run.
		fmt.Sprintf("  %s  %s   %s  %d   %s  %d",
			r.T.dim("depth"), r.plan.Depth.Label,
			r.T.dim("max sources"), r.plan.MaxSources,
			r.T.dim("per sub-agent"), r.plan.PerTopic()),
		"",
		"  " + r.T.dim("planned sub-topics"),
	}
}

// briefFoot carries the key hints, the only thing that says how to start the
// run, so it is never what gets dropped when the brief is too tall.
func (r *Renderer) briefFoot() []string {
	if r.promptLabel != "" {
		return []string{
			r.rule(),
			r.promptRow(),
			"  " + r.T.gray("[enter] confirm   [esc] cancel"),
		}
	}
	return []string{
		r.rule(),
		"  " + r.T.gray("[enter] launch   [e] add   [r] rename   [x] delete   [d] depth   [q] cancel"),
	}
}

// wrapNote lays a sub-topic note out in the column beside its name. Below
// minNoteCol there is no room to wrap usefully, so the note stays on one line
// and the frame's own clip trims it.
func (r *Renderer) wrapNote(note string, width int) []string {
	switch {
	case strings.TrimSpace(note) == "":
		return nil
	case width < minNoteCol:
		return []string{note}
	default:
		return wrapWords(note, width)
	}
}

// fitNoteLines is the most note lines each sub-topic can show while the whole
// block still fits in room rows. It returns 0 when not even one row per
// sub-topic fits, which is the caller's signal to start hiding sub-topics.
func fitNoteLines(notes [][]string, room int) int {
	if len(notes) > room {
		return 0
	}
	best := 1
	for k := 2; k <= maxNoteLines; k++ {
		total := 0
		for _, n := range notes {
			total += max(min(k, len(n)), 1)
		}
		if total > room {
			break
		}
		best = k
	}
	return best
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func rest(s []string) []string {
	if len(s) < 2 {
		return nil
	}
	return s[1:]
}

func (r *Renderer) treeLines() []string {
	head := []string{
		r.heading("DEEP RESEARCH", formatDuration(r.clock().Sub(r.startTime))),
		"  " + r.T.dim(clip(r.question, r.width-2)),
		r.rule(),
		r.phaseStrip(),
		r.rule(),
	}
	agents := r.agentLines()
	foot := r.footerLines()

	// The activity tail is the elastic section: it takes whatever rows the
	// fixed sections leave, so the frame height is constant and the header,
	// tree and footer never move.
	budget := r.rows - 1 - len(head) - len(agents) - len(foot) - 1
	var tail []string
	if budget >= 2 {
		tail = append(tail, r.rule())
		tail = append(tail, r.activityLines(budget-1)...)
	}

	out := append([]string{}, head...)
	out = append(out, agents...)
	out = append(out, tail...)
	out = append(out, r.rule())
	return append(out, foot...)
}

// agentLines renders one fixed row per sub-agent, plus an overflow marker when
// the plan is wider than the space available.
func (r *Renderer) agentLines() []string {
	if len(r.order) == 0 {
		return []string{r.T.dim("  waiting for the planner…")}
	}
	nameCol := minNameCol
	for _, id := range r.order {
		if w := dispWidth(r.nodes[id].Name); w > nameCol {
			nameCol = w
		}
	}
	nameCol = min(nameCol, r.nameColCap())

	// Cap the tree at half the viewport so a wide plan cannot squeeze the
	// header and footer off the screen.
	shown, hidden := r.order, 0
	if cap := max(r.rows/2, 3); len(shown) > cap {
		shown, hidden = shown[:cap-1], len(shown)-(cap-1)
	}

	out := make([]string, 0, len(shown)+1)
	for _, id := range shown {
		n := r.nodes[id]
		out = append(out, fmt.Sprintf("  %s %s %s %s",
			r.stateMark(n),
			pad(n.Name, nameCol),
			r.progressBar(n),
			r.T.dim(n.Line)))
	}
	if hidden > 0 {
		out = append(out, r.T.dim(fmt.Sprintf("  … %d more sub-agents", hidden)))
	}
	// Once research is done every row reads ✓ and the tree stops moving, while
	// analyze / fact-check / summarize are single model calls that can run for
	// a minute. This row is what says the run is still working, and on what.
	if r.detail != "" && !strings.EqualFold(r.phase, "Research") && !r.done {
		out = append(out, fmt.Sprintf("  %s %s",
			r.T.cyan(spinnerFrames[r.frame%len(spinnerFrames)]), r.detail))
	}
	return out
}

func (r *Renderer) stateMark(n *Node) string {
	switch n.State {
	case "done":
		return r.T.green("✓")
	case "error":
		return r.T.red("✗")
	case "running":
		return r.T.cyan(spinnerFrames[r.frame%len(spinnerFrames)])
	default:
		return r.T.dim("·")
	}
}

// progressBar renders a fixed-width meter so rows stay aligned as work lands.
func (r *Renderer) progressBar(n *Node) string {
	const cells = 10
	p := min(max(n.Progress, 0), 100)
	full := p * cells / 100
	bar := strings.Repeat("█", full) + strings.Repeat("░", cells-full)
	switch n.State {
	case "done":
		bar = r.T.green(bar)
	case "running":
		bar = r.T.cyan(bar)
	default:
		bar = r.T.dim(bar)
	}
	label := fmt.Sprintf("%3d%%", p)
	if n.Sources > 0 {
		label = fmt.Sprintf("%3d%% %2d src", p, n.Sources)
	}
	return bar + " " + r.T.dim(label)
}

// phaseStrip shows the whole pipeline with the current stage marked, so the
// run reads as progress through a known sequence rather than a status string
// that changes without context.
func (r *Renderer) phaseStrip() string {
	cur := -1
	for i, p := range pipeline {
		if strings.EqualFold(p, r.phase) {
			cur = i
		}
	}
	parts := make([]string, 0, len(pipeline))
	for i, p := range pipeline {
		switch {
		case r.done, i < cur:
			parts = append(parts, r.T.green("✓ "+p))
		case i == cur:
			parts = append(parts, r.T.bold(r.T.cyan("▸ "+p)))
		default:
			parts = append(parts, r.T.dim("· "+p))
		}
	}
	return "  " + strings.Join(parts, r.T.dim("  ─  "))
}

// activityLines renders the last n entries of the event tail, oldest first.
func (r *Renderer) activityLines(n int) []string {
	if n <= 0 {
		return nil
	}
	acts := r.acts
	if len(acts) > n {
		acts = acts[len(acts)-n:]
	}
	out := make([]string, 0, n)
	for _, a := range acts {
		sub := "  "
		if a.sub != "" {
			sub = clip(a.sub, 2)
		}
		out = append(out, fmt.Sprintf("  %s %s %s",
			r.T.dim(formatDuration(a.at)), r.T.gray(pad(sub, 2)), a.text))
	}
	// Keep the section a constant height: without the blank filler the rows
	// below it would march up the screen as the tail fills.
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

func (r *Renderer) footerLines() []string {
	counters := fmt.Sprintf("  %s   %s",
		r.T.bold(fmt.Sprintf("%d sources", r.sources)),
		r.T.dim(fmt.Sprintf("%s tokens", formatCount(r.tokens))))
	out := []string{counters}
	switch {
	case r.promptLabel != "":
		out = append(out, r.promptRow(), r.T.gray("  [enter] send   [esc] cancel"))
	case !r.done:
		out = append(out, r.T.gray("  [b] release display   [esc] cancel"))
	}
	return out
}

func (r *Renderer) reportLines() []string {
	if r.result == nil {
		return nil
	}
	// "sources verified" counted degraded, dropped and unverified sources
	// too. The total is the total; how each one was obtained is spelled out
	// beside it, which is the distinction the rest of the UI already draws.
	counters := fmt.Sprintf("  %s  %d%s      %s  %s",
		r.T.dim("sources"), r.sources, r.T.dim(sourceQuality(r.result)),
		r.T.dim("tokens"), formatCount(r.tokens))
	out := []string{
		r.heading("RESEARCH COMPLETE", formatDuration(r.clock().Sub(r.startTime))),
		r.rule(),
		counters,
		"",
		r.T.bold("  Source Breakdown"),
	}
	for _, b := range sourceBreakdown(r.result) {
		out = append(out, fmt.Sprintf("    %s %s: %d",
			r.T.dim("·"), b.label, b.count))
	}
	return append(out, r.rule())
}

// ---- shared chrome --------------------------------------------------------

// heading renders a title row with an optional right-aligned status.
func (r *Renderer) heading(title, right string) string {
	left := "  " + r.T.bold(r.T.cyan(title))
	if right == "" {
		return left
	}
	gap := r.width - dispWidth(left) - dispWidth(right) - 2
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + r.T.dim(right)
}

func (r *Renderer) rule() string {
	w := r.width - 2
	if w < 4 {
		w = 4
	}
	return "  " + r.T.dim(strings.Repeat("─", w))
}

// ---- log mode (non-terminal writers) --------------------------------------

// logBrief prints the plan once as plain text.
func (r *Renderer) logBrief(plan *Plan) {
	r.writeBlock(r.briefLines())
	_ = plan
}

// logEvent appends a single line per event. A pipe gets a readable transcript
// instead of one full frame repeated for every event.
func (r *Renderer) logEvent(e Event) {
	var line string
	switch e.Type {
	case Phase:
		line = "▸ " + e.Phase + ": " + e.Detail
	case SubAgent:
		if e.SubState == "" {
			return
		}
		line = fmt.Sprintf("  [%s] %s — %s", e.SubID, e.SubName, e.SubState)
	case Search, Read, Verify, Citation:
		if e.Line == "" {
			return
		}
		line = fmt.Sprintf("  [%s] %s", e.SubID, e.Line)
	case Info:
		line = "  " + e.Detail
	case Error:
		line = "! " + e.Detail
	default:
		return
	}
	r.write(line + "\n")
}

// writeBlock emits lines as a plain block, using CRLF on a terminal so the
// rows start at column 0 even while the tty is in raw mode.
func (r *Renderer) writeBlock(lines []string) {
	eol := "\n"
	if !r.plain {
		eol = crlf
	}
	var sb strings.Builder
	for _, l := range lines {
		if !r.plain {
			l = clip(l, r.width)
		}
		sb.WriteString(l)
		sb.WriteString(eol)
	}
	r.write(sb.String())
}

func (r *Renderer) showCursorLocked() {
	if r.hidden {
		r.write(cursorShow)
		r.hidden = false
	}
}

// write is the renderer's single exit to the terminal, so the post-Close guard
// lives here and covers every path into it.
func (r *Renderer) write(s string) {
	if r.closed {
		return
	}
	_, _ = io.WriteString(r.W, s)
}

// formatCount abbreviates large counts (6120 -> "6.1k").
func formatCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

type breakdown struct {
	label string
	count int
}

// sourceQuality summarises how the run's sources were obtained, e.g.
// " (2 fetched, 1 snippet only, 1 unverified)". It is what keeps the source
// total from reading as a verification claim it cannot support.
func sourceQuality(res *agent.ResearchResult) string {
	if res == nil {
		return ""
	}
	counts := map[string]int{}
	for _, f := range res.Findings {
		counts[citationStatus(f)]++
	}
	var parts []string
	for _, s := range []struct{ status, label string }{
		{"ok", "fetched"},
		{"degraded", "snippet only"},
		{"unverified", "unverified"},
		{"dropped", "dropped"},
	} {
		if n := counts[s.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s.label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// sourceCategories lists domain suffixes in priority order. Host and path
// conventions are explicit rules, never substrings of the entire URL.
var sourceCategories = []struct {
	label string
	hosts []string
	match func(host, path string) bool
}{
	{"Academic / Pre-prints", []string{"arxiv.org", "nature.com", "science.org",
		"semanticscholar.org", "springer.com", "sciencedirect.com", "ieee.org",
		"acm.org", "ncbi.nlm.nih.gov", "biorxiv.org", "ssrn.com"}, nil},
	// Filings come before the broad .gov bucket: an SEC filing is a financial
	// source that happens to be hosted by a government.
	{"Financial / Filings", []string{"sec.gov", "reuters.com",
		"bloomberg.com", "ft.com", "wsj.com", "marketwatch.com"}, nil},
	{"Government", []string{"gov", "gov.uk", "europa.eu", "who.int", "un.org"}, nil},
	{"Reference", []string{"wikipedia.org", "britannica.com", "wiktionary.org",
		"wikidata.org"}, func(host, path string) bool {
		return domainMatches(host, "plato.stanford.edu") && pathMatches(path, "/entries")
	}},
	{"Education", []string{"edu", "edu.au", "ac.uk"}, nil},
	{"Forums / Q&A", []string{"stackoverflow.com", "stackexchange.com", "reddit.com",
		"news.ycombinator.com", "quora.com"}, func(host, _ string) bool {
		return strings.HasPrefix(host, "discourse.")
	}},
	{"Documentation", []string{"readthedocs.io", "readthedocs.org", "godoc.org",
		"pkg.go.dev", "man7.org", "rfc-editor.org"}, func(host, path string) bool {
		return strings.HasPrefix(host, "docs.") || strings.HasPrefix(host, "developer.") ||
			pathMatches(path, "/docs") || pathMatches(path, "/doc")
	}},
	{"News / Media", []string{"techcrunch.com", "theverge.com", "wired.com", "bbc.com", "bbc.co.uk",
		"nytimes.com", "theguardian.com", "cnbc.com", "arstechnica.com"}, func(host, _ string) bool {
		return strings.HasPrefix(host, "news.")
	}},
}

// sourceBreakdown buckets findings by source category for the summary view.
// Only non-empty buckets are returned: a card meant to be read at a glance
// gains nothing from a list of zeroes.
func sourceBreakdown(res *agent.ResearchResult) []breakdown {
	counts := map[string]int{}
	other := 0
	for _, f := range res.Findings {
		if label := categorize(f.URL); label != "" {
			counts[label]++
		} else {
			other++
		}
	}
	var out []breakdown
	for _, c := range sourceCategories {
		if n := counts[c.label]; n > 0 {
			out = append(out, breakdown{c.label, n})
		}
	}
	if other > 0 {
		out = append(out, breakdown{"Other", other})
	}
	return out
}

// categorize returns the bucket a URL belongs to, or "" when nothing matches.
func categorize(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Hostname() == "" && u.Scheme == "" {
		// Unverified findings are model-written and routinely carry a bare
		// "example.com/page". DomainOf already resolves those for the citation
		// list, so filing every one of them under "Other" was a disagreement
		// between two views of the same source.
		if u, err = url.Parse("https://" + strings.TrimPrefix(rawURL, "//")); err != nil {
			return ""
		}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return ""
	}
	for _, c := range sourceCategories {
		for _, domain := range c.hosts {
			if domainMatches(host, domain) {
				return c.label
			}
		}
		if c.match != nil && c.match(host, u.Path) {
			return c.label
		}
	}
	return ""
}

func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func pathMatches(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
