package ui

import (
	"fmt"
	"maps"
	"time"
)

// The activity tail files each sub-agent's rows under one header per topic,
// shut until the reader opens it. Every row used to carry its topic's name,
// so a run of three topics read as the same three names down the whole
// tail, and the rows of one topic were scattered among the others'.

// tailItem is one entry of the tail: a row of the run's own (a phase
// starting, a model retry), or a topic's group, placed where its first row
// fell.
type tailItem struct {
	act  activity
	sub  string // the topic's sub-agent; "" for a run row
	rows []activity
}

// tailItems groups the retained rows: a row whose sub-agent the tree knows
// joins its topic's group, any other stays a row of its own.
//
// The grouping is kept until an event changes the rows (Emit drops it):
// the ticker repaints several times a second with nothing new, and grouped
// again each time, a thousand rows made a fresh copy of the history per
// paint. What changes between events (a spinner, the clock) is drawn from
// the rows, not kept in them.
func (r *Renderer) tailItems() []tailItem {
	if r.grouped != nil {
		return r.grouped
	}
	r.grouped = r.groupActs()
	return r.grouped
}

func (r *Renderer) groupActs() []tailItem {
	items := []tailItem{}
	at := map[string]int{}
	for _, a := range r.acts {
		if a.sub == "" || r.nodes[a.sub] == nil {
			items = append(items, tailItem{act: a})
			continue
		}
		i, ok := at[a.sub]
		if !ok {
			i = len(items)
			at[a.sub] = i
			items = append(items, tailItem{sub: a.sub})
		}
		items[i].rows = append(items[i].rows, a)
	}
	return items
}

// activityLines renders the tail in n rows, oldest first, and records which
// of them are topic headers, and rows of an open topic, for a click. Each
// item takes a row; what rows are left go to the open groups' latest rows.
func (r *Renderer) activityLines(n int) []string {
	r.tail.heads, r.tail.body = map[int]string{}, map[int]string{}
	if n <= 0 {
		return nil
	}
	all := r.tailItems()
	items := fitItems(all, n, r.openWant(all))
	shares := r.openShares(items, n-len(items))
	col := r.topicCol()
	out := make([]string, 0, n)
	for i, it := range items {
		if it.sub == "" {
			out = append(out, r.runRow(it.act))
			continue
		}
		r.tail.heads[len(out)] = it.sub
		out = append(out, r.topicHead(it, col, shares[i]))
		for _, row := range r.topicRows(it.rows, shares[i]) {
			r.tail.body[len(out)] = it.sub
			out = append(out, row)
		}
	}
	// Keep the section a constant height: without the blank filler the rows
	// below it would march up the screen as the tail fills.
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

// openWant is how many rows the open topics' groups would fill.
func (r *Renderer) openWant(items []tailItem) (n int) {
	for _, it := range items {
		if it.sub != "" && r.opened[it.sub] {
			n += len(it.rows)
		}
	}
	return n
}

// fitItems keeps the items that fit in n rows, one row each, with room
// left for open rows the open topics would fill. The topics go last: a
// topic is where a click or its number opens it, and one that started early
// but is still busy must not be pushed out by the phases that started after
// it. A topic opened goes before the run's rows: once the phases and notes
// filled the tail, a topic opened showed nothing of what it held. The
// newest run row, the phase the run is in, stays while it fits. Of the
// rest the newest stay, and only when the topics alone overflow do the
// oldest of them give way.
func fitItems(items []tailItem, n, open int) []tailItem {
	groups, runCount := 0, 0
	for _, it := range items {
		if it.sub != "" {
			groups++
		} else {
			runCount++
		}
	}
	if len(items)+open <= n {
		return items
	}
	// run rows kept, the newest
	runs := max(n-groups-open, min(runCount, 1, max(n-groups, 0)), 0)
	dropGroups := max(groups-n, 0) // groups dropped, the oldest
	keep := make([]bool, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].sub == "" && runs > 0 {
			keep[i], runs = true, runs-1
		}
	}
	for i, it := range items {
		if it.sub == "" {
			continue
		}
		if dropGroups > 0 {
			dropGroups--
			continue
		}
		keep[i] = true
	}
	var out []tailItem
	for i, it := range items {
		if keep[i] {
			out = append(out, it)
		}
	}
	return out
}

// openShares splits spare rows among the open groups, evenly, a group
// needing fewer giving the rest to the others: one long topic opened must
// not push another opened topic's rows off the tail.
func (r *Renderer) openShares(items []tailItem, spare int) []int {
	shares := make([]int, len(items))
	var open []int
	for i, it := range items {
		if it.sub != "" && r.opened[it.sub] {
			open = append(open, i)
		}
	}
	for spare > 0 && len(open) > 0 {
		each := max(spare/len(open), 1)
		var still []int
		for _, i := range open {
			give := min(each, len(items[i].rows)-shares[i], spare)
			shares[i] += give
			spare -= give
			if shares[i] < len(items[i].rows) {
				still = append(still, i)
			}
		}
		open = still
	}
	return shares
}

// runRow is a row of the run's own. A phase heads what follows it, from
// the column the topics start.
func (r *Renderer) runRow(a activity) string {
	return fmt.Sprintf("  %s  %s", r.T.dim(pad(formatDuration(a.at), 5)), r.activityText(a))
}

// topicHead is a group's one row: its number (the key that opens it), its
// name in the tree's colour, how many rows it holds and, unless they are
// shown under it, the latest of them, so a shut group still shows what its
// topic is doing. An open group the tail had no room for (share 0) keeps it
// too: opening it must not leave less on screen than before.
func (r *Renderer) topicHead(it tailItem, col, share int) string {
	last := it.rows[len(it.rows)-1]
	mark, latest := "▹", "  "+r.activityText(last)
	if r.opened[it.sub] {
		mark = "▿"
		if share > 0 {
			latest = ""
		}
	}
	num := " "
	if i := r.topicNumber(it.sub); i > 0 && i <= 9 {
		num = fmt.Sprint(i)
	}
	name := r.hue(it.sub, pad(clip(r.nodes[it.sub].Name, col), col))
	return fmt.Sprintf("  %s  %s %s %s  %s%s",
		r.T.dim(pad(formatDuration(last.at), 5)), r.T.dim(num), r.hue(it.sub, mark), name,
		r.T.dim(pad(fmt.Sprint(len(it.rows)), 3)), latest)
}

// topicRows are an open group's latest k rows, under its name, with a row
// counting those that did not fit. With one row there is no room for the
// count: the row goes to the latest, which the header no longer shows.
func (r *Renderer) topicRows(rows []activity, k int) []string {
	if k <= 0 {
		return nil
	}
	var out []string
	if k < len(rows) && k > 1 {
		k--
		out = append(out, fmt.Sprintf("  %s      %s", pad("", 5), r.T.dim(fmt.Sprintf("… %d earlier", len(rows)-k))))
	}
	for _, a := range rows[len(rows)-k:] {
		out = append(out, fmt.Sprintf("  %s      %s", r.T.dim(pad(formatDuration(a.at), 5)), r.activityText(a)))
	}
	return out
}

// topicCol is the width of a header's name: the longest name, whole, as
// long as it leaves the latest row room beside it. The tail's rows no
// longer carry a name each, so the header has the width a third of the
// row cut names to.
func (r *Renderer) topicCol() int {
	w := 0
	for _, id := range r.order {
		w = max(w, dispWidth(r.nodes[id].Name))
	}
	return max(min(w, r.width-latestRoom), 1)
}

// latestRoom is what a header keeps beside its name: the time, the number
// and mark, the count, and a start of the latest row.
const latestRoom = 40

// topicNumber is sub's place in the tree, from 1: the digit that opens it.
func (r *Renderer) topicNumber(sub string) int {
	for i, id := range r.order {
		if id == sub {
			return i + 1
		}
	}
	return 0
}

// OpenTopic opens or shuts the group of the tree's nth topic, a digit's
// key. With the tail hidden it shows it, that topic open: the digit asked
// to see it.
func (r *Renderer) OpenTopic(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < 1 || n > len(r.order) {
		return
	}
	sub := r.order[n-1]
	// A topic asked for is shown in the tail, which a phase's view hides.
	r.view = phaseView{}
	if !r.showActivity {
		r.showActivity = true
		r.opened[sub] = true
	} else {
		r.opened[sub] = !r.opened[sub]
	}
	r.repaintTreeLocked()
}

// tailLayout is where a laid-out tail's topics are: top is the frame row
// the tail starts on, heads its rows that head a topic, body the rows of
// an open topic's group. at is when it was painted.
type tailLayout struct {
	at    time.Time
	top   int
	heads map[int]string
	body  map[int]string
}

func (l tailLayout) same(o tailLayout) bool {
	return l.top == o.top && maps.Equal(l.heads, o.heads) && maps.Equal(l.body, o.body)
}

// clickGrace is how long a layout no longer on screen still takes a
// click. An open topic grows a row with each page it reads, moving every
// header under it down one, and the frame repaints several times a second:
// a click aimed at a header landed on the row that had just taken its
// place, a moment after the eye saw it.
const clickGrace = 400 * time.Millisecond

// noteShownLocked records the tail just painted, when it moved, and
// forgets the layouts replaced longer than clickGrace ago.
func (r *Renderer) noteShownLocked(now time.Time) {
	if n := len(r.shown); n == 0 || !r.shown[n-1].same(r.tail) {
		l := r.tail
		l.at = now
		r.shown = append(r.shown, l)
	}
	// Layout i was on screen until layout i+1 was painted.
	for len(r.shown) > 1 && now.Sub(r.shown[1].at) > clickGrace {
		r.shown = r.shown[1:]
	}
}

// Click opens the phase clicked on the strip (see clickStripLocked), or
// opens or shuts the topic whose header is at screen row y (from 1):
// on screen now, or there a moment ago (see clickGrace). A click on a row
// of an open topic shuts it. A click anywhere else does nothing.
func (r *Renderer) Click(x, y int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The live frame is drawn from the top of the screen, its row i on
	// screen row i+1; nothing drawn in place has headers to click.
	if r.mode != "tree" || r.inline != nil {
		return
	}
	if y-1 == r.strip.row {
		r.clickStripLocked(x)
		return
	}
	if r.view.phase != "" || !r.showActivity || len(r.shown) == 0 {
		return
	}
	// No tail on screen (one too short for it) has nothing to click, not
	// even what it showed a moment before.
	if len(r.shown[len(r.shown)-1].heads) == 0 {
		return
	}
	now := r.clock()
	for i := len(r.shown) - 1; i >= 0; i-- {
		l := r.shown[i]
		if i < len(r.shown)-1 && now.Sub(r.shown[i+1].at) > clickGrace {
			break
		}
		if sub, ok := l.heads[y-1-l.top]; ok {
			r.opened[sub] = !r.opened[sub]
			r.repaintTreeLocked()
			return
		}
	}
	cur := r.shown[len(r.shown)-1]
	if sub, ok := cur.body[y-1-cur.top]; ok {
		r.opened[sub] = false
		r.repaintTreeLocked()
	}
}

func (r *Renderer) repaintTreeLocked() {
	if r.plain || r.mode != "tree" {
		return
	}
	r.paintLocked(true)
}

// TrackClicks sets whether the live frame asks the terminal for mouse
// clicks, which open a topic's group. Only a run with a key reader may ask:
// without one the terminal's reports reach the shell as typed text.
func (r *Renderer) TrackClicks(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clicks = on
}

// mouseLocked turns the terminal's click reports on or off. They are on
// only while the live frame is: the start screen, the brief and the report
// keep the terminal's own selection and links.
func (r *Renderer) mouseLocked(on bool) {
	if r.plain || on == r.mouse || (on && !r.clicks) {
		return
	}
	r.mouse = on
	if on {
		r.write(mouseOn)
	} else {
		r.write(mouseOff)
	}
}
