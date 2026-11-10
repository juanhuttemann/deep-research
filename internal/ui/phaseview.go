package ui

import (
	"fmt"
	"strings"
	"time"
)

// A phase's view: a click on a phase the run has reached, on the strip,
// shows in place of the tree and the tail what that phase did. Once the
// research is done the frame only says which phase is running; what the
// plan was, what each topic searched and read, and what the analysis and
// the fact-check reasoned through and concluded was gone from the screen,
// and the reasoning was never on it (the status row shows its first line).

// phaseLog is what the frame keeps of one phase beyond its tail rows: when
// it started and the reasoning its calls streamed.
type phaseLog struct {
	at       time.Duration
	thoughts []*thought
}

// thought is one call's streamed reasoning; part names the call when the
// phase runs several at once (the fact-check's parts).
type thought struct {
	part string
	text strings.Builder
	cut  bool // its start was dropped to keep it under thoughtKeep
}

// thoughtKeep bounds what one call's reasoning keeps: a long fact-check
// reasons for tens of thousands of words, and the view is wrapped again on
// every repaint. Past it the oldest text gives way, as the tail's rows do.
const thoughtKeep = 64 << 10

// phaseView is the open view: the phase, its first row shown, whether it
// follows the newest rows as they arrive (the phase still running, scrolled
// to its end), and the rows it was given at its last paint, a page.
type phaseView struct {
	phase  string
	top    int
	follow bool
	height int
}

// stripLayout is where the strip's reachable phases were drawn: the frame
// row it is on, from 0, and each phase's columns, from 1.
type stripLayout struct {
	row   int
	spots []stripSpot
}

type stripSpot struct {
	phase    string
	from, to int
}

// phaseLog is phase's log, made when the phase first starts. The research
// starts again for the follow-up round; it is still the research.
func (r *Renderer) phaseLog(phase string) *phaseLog {
	if l, ok := r.phases[phase]; ok {
		return l
	}
	l := &phaseLog{at: r.clock().Sub(r.startTime)}
	r.phases[phase] = l
	return l
}

// think adds a Thinking event's text to its call's reasoning, under the
// phase the run is in: the first event of a call starts it, the rest go to
// the latest call of the same part, since parallel parts interleave.
func (r *Renderer) think(e Event) {
	if r.phase == "" || e.Detail == "" {
		return
	}
	l := r.phaseLog(r.phase)
	var t *thought
	for i := len(l.thoughts) - 1; i >= 0 && e.Status != "start"; i-- {
		if l.thoughts[i].part == e.Part {
			t = l.thoughts[i]
			break
		}
	}
	if t == nil {
		t = &thought{part: e.Part}
		l.thoughts = append(l.thoughts, t)
	}
	t.text.WriteString(e.Detail)
	if t.text.Len() > thoughtKeep {
		s := t.text.String()
		s = s[len(s)-thoughtKeep/2:]
		if i := strings.IndexAny(s, " \n"); i >= 0 {
			s = s[i+1:]
		}
		t.text.Reset()
		t.text.WriteString(s)
		t.cut = true
	}
}

// clickStripLocked opens the view of the phase drawn at column x, or shuts
// it when it is the one open. A phase the run has not reached takes no
// click: it did nothing yet.
func (r *Renderer) clickStripLocked(x int) {
	for _, s := range r.strip.spots {
		if x < s.from || x > s.to {
			continue
		}
		if r.view.phase == s.phase {
			r.view = phaseView{}
		} else {
			// The phase running follows its newest rows; a finished one
			// opens at its start, which is where reading it begins.
			r.view = phaseView{phase: s.phase, follow: strings.EqualFold(s.phase, r.phase)}
		}
		r.repaintTreeLocked()
		return
	}
}

// ClosePhase shuts an open phase's view and reports whether one was open:
// Esc goes back to the run before it cancels it.
func (r *Renderer) ClosePhase() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.view.phase == "" || r.mode != "tree" {
		return false
	}
	r.view = phaseView{}
	r.repaintTreeLocked()
	return true
}

// Scroll moves an open view by n rows, down when n is positive. Scrolled
// to its end it follows the rows still arriving.
func (r *Renderer) Scroll(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.view.phase == "" || r.mode != "tree" {
		return
	}
	r.view.top += n
	r.view.follow = false
	r.repaintTreeLocked()
}

// ScrollPage moves an open view by its height less a row, which stays on
// screen to keep the reader's place.
func (r *Renderer) ScrollPage(dir int) {
	r.mu.Lock()
	height := r.view.height
	r.mu.Unlock()
	r.Scroll(dir * max(height-1, 1))
}

// viewLines is the open view in n rows: its title, then the window of its
// content the scroll is at, held inside the content.
func (r *Renderer) viewLines(n int) []string {
	if n < 2 {
		return nil
	}
	body := r.viewContent()
	h := n - 1
	r.view.height = h
	last := max(len(body)-h, 0)
	if r.view.follow {
		r.view.top = last
	}
	r.view.top = min(max(r.view.top, 0), last)
	if r.view.top == last {
		r.view.follow = true
	}
	shown := body[r.view.top:min(r.view.top+h, len(body))]
	out := append([]string{r.viewTitle(len(body), len(shown))}, shown...)
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

// viewTitle names the phase, when it started and how long it took, and
// where in its content the window is when it does not fit.
func (r *Renderer) viewTitle(total, shown int) string {
	title := "  " + r.T.bold(r.view.phase)
	if l, ok := r.phases[r.view.phase]; ok {
		title += r.T.dim("   started " + formatDuration(l.at))
		if end, ok := r.phaseEnd(r.view.phase); ok {
			title += r.T.dim(" · took " + formatDuration(end-l.at))
		} else {
			title += r.T.dim(" · running")
		}
	}
	if shown < total {
		title += r.T.dim(fmt.Sprintf("   rows %d-%d of %d", r.view.top+1, r.view.top+shown, total))
	}
	return title
}

// phaseEnd is when the phase after phase started, which ended it.
func (r *Renderer) phaseEnd(phase string) (time.Duration, bool) {
	order := pipeline
	if r.instant {
		order = instantPipeline
	}
	for i, p := range order {
		if p != phase || i+1 >= len(order) {
			continue
		}
		if next, ok := r.phases[order[i+1]]; ok {
			return next.at, true
		}
	}
	return 0, false
}

func (r *Renderer) viewHints() string {
	return "[↑↓ pgup pgdn] scroll   [esc] back to the run   [click] another phase to see it"
}

// viewContent is everything the view has to show of its phase.
func (r *Renderer) viewContent() []string {
	var out []string
	switch r.view.phase {
	case "Plan":
		out = r.planView()
	case "Research":
		out = r.researchView()
	default:
		out = r.phaseRows()
	}
	out = append(out, r.reasoningView()...)
	if len(out) == 0 {
		out = []string{"  " + r.T.dim("nothing recorded for this phase yet")}
	}
	return out
}

// planView is the plan the run is following: the settings it was given,
// and each sub-topic with what it is for, the search it starts from and
// the words its pages must show.
func (r *Renderer) planView() []string {
	if r.plan == nil {
		return r.phaseRows()
	}
	p := r.plan
	var out []string
	if len(p.Clarifications) > 0 {
		out = append(out, r.readingRows(p.Clarifications)...)
	}
	out = append(out, r.briefSettingsRow())
	if len(p.Domains) > 0 {
		out = append(out, "  "+r.T.dim("only from  ")+strings.Join(p.Domains, ", "))
	}
	out = append(out, "", "  "+r.T.dim(fmt.Sprintf("%d sub-topics", len(p.SubTopics)))+r.scopeNote())
	w := max(r.width-10, minNoteCol)
	for i, s := range p.SubTopics {
		out = append(out, fmt.Sprintf("  %s %s", r.T.dim(fmt.Sprintf("%2d", i+1)), r.T.bold(s.Name)))
		for _, l := range wrapWords(s.Notes, w) {
			out = append(out, "       "+l)
		}
		out = append(out, r.labelled("search", s.Query, w)...)
		out = append(out, r.labelled("terms", strings.Join(s.Terms, ", "), w)...)
	}
	return out
}

// labelled is text wrapped under its label: clipped to one row, a long
// query or term list was cut where scrolling could not reach.
func (r *Renderer) labelled(label, text string, w int) []string {
	var out []string
	for i, l := range wrapWords(text, w-8) {
		lead := "        "
		if i == 0 {
			lead = r.T.dim(pad(label, 8))
		}
		out = append(out, "       "+lead+l)
	}
	return out
}

// researchView is the research's own rows, then every topic's rows under
// its name, all of them: the tail shows a topic's latest that fit, this
// is the whole of what it searched, read and kept.
func (r *Renderer) researchView() []string {
	out := r.phaseRows()
	for i, id := range r.order {
		n := r.nodes[id]
		out = append(out, "", fmt.Sprintf("  %s %s %s  %s",
			r.T.dim(fmt.Sprintf("%2d", i+1)), r.stateMark(n), r.hue(id, n.Name),
			r.T.dim(fmt.Sprintf("%d sources", n.Sources))))
		for _, a := range r.acts {
			if a.sub == id {
				out = append(out, fmt.Sprintf("  %s      %s", r.T.dim(pad(formatDuration(a.at), 5)), r.activityText(a)))
			}
		}
	}
	return out
}

// phaseRows are the rows the run logged of itself during the view's phase:
// it starting, the model asked, retries, counts and failures.
func (r *Renderer) phaseRows() []string {
	var out []string
	for _, a := range r.acts {
		if a.phase == r.view.phase && (a.sub == "" || r.nodes[a.sub] == nil) {
			out = append(out, r.runRow(a))
		}
	}
	return out
}

// reasoningView is the phase's calls' reasoning, whole, wrapped to the
// frame: each call under its part's name when the phase ran several.
func (r *Renderer) reasoningView() []string {
	l, ok := r.phases[r.view.phase]
	if !ok || len(l.thoughts) == 0 {
		return nil
	}
	out := []string{"", "  " + r.T.cyan("reasoning")}
	w := max(r.width-6, minNoteCol)
	for _, t := range l.thoughts {
		if t.part != "" {
			out = append(out, "", "  "+r.T.bold(t.part))
		}
		if t.cut {
			out = append(out, "    "+r.T.dim("… earlier reasoning dropped"))
		}
		out = append(out, r.wrapParagraphs(t.text.String(), w)...)
	}
	return out
}

// wrapParagraphs wraps text to w columns, keeping its paragraphs apart with
// one blank row: wrapWords alone ran every paragraph into one.
func (r *Renderer) wrapParagraphs(text string, w int) []string {
	var out []string
	blank := false
	for para := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(para) == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, "")
			blank = false
		}
		for _, l := range wrapWords(para, w) {
			out = append(out, "    "+l)
		}
	}
	return out
}
