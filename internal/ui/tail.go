package ui

import "fmt"

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
func (r *Renderer) tailItems() []tailItem {
	var items []tailItem
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
// of them are topic headers, for a click. Each item takes a row; what rows
// are left go to the open groups' latest rows.
func (r *Renderer) activityLines(n int) []string {
	r.tailHeads = map[int]string{}
	if n <= 0 {
		return nil
	}
	items := fitItems(r.tailItems(), n)
	shares := r.openShares(items, n-len(items))
	col := r.topicCol()
	out := make([]string, 0, n)
	for i, it := range items {
		if it.sub == "" {
			out = append(out, r.runRow(it.act))
			continue
		}
		r.tailHeads[len(out)] = it.sub
		out = append(out, r.topicHead(it, col, shares[i]))
		out = append(out, r.topicRows(it.rows, shares[i])...)
	}
	// Keep the section a constant height: without the blank filler the rows
	// below it would march up the screen as the tail fills.
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

// fitItems keeps the items that fit in n rows, one row each. The topics
// go last: a topic is where a click or its number opens it, and one that
// started early but is still busy must not be pushed out by the phases that
// started after it. Of the rest the newest stay, and only when the topics
// alone overflow do the oldest of them give way.
func fitItems(items []tailItem, n int) []tailItem {
	if len(items) <= n {
		return items
	}
	groups := 0
	for _, it := range items {
		if it.sub != "" {
			groups++
		}
	}
	runs := max(n-groups, 0)       // run rows kept, the newest
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

// topicCol is the width of a header's name: the longest name, cut to leave
// the latest row room beside it.
func (r *Renderer) topicCol() int {
	w := 0
	for _, id := range r.order {
		w = max(w, dispWidth(r.nodes[id].Name))
	}
	return max(min(w, maxTopicCol, r.width/3), 1)
}

// maxTopicCol bounds a header's name column.
const maxTopicCol = 32

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
	if !r.showActivity {
		r.showActivity = true
		r.opened[sub] = true
	} else {
		r.opened[sub] = !r.opened[sub]
	}
	r.repaintTreeLocked()
}

// Click opens or shuts the group whose header is at screen row y (from 1).
// A click anywhere else does nothing.
func (r *Renderer) Click(_, y int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The live frame is drawn from the top of the screen, its row i on
	// screen row i+1; nothing drawn in place has headers to click.
	if r.mode != "tree" || r.inline != nil || !r.showActivity {
		return
	}
	sub, ok := r.tailHeads[y-1-r.tailTop]
	if !ok {
		return
	}
	r.opened[sub] = !r.opened[sub]
	r.repaintTreeLocked()
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
