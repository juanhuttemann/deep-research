package setup

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// item is one row of a picker list.
type item struct {
	id     string
	title  string
	detail string // dimmed, after the title
	mark   string // e.g. "✓ key stored"
	// keywords are searched but not shown: a model's display name, so
	// "v4.1 flash" finds deepseek-v4.1-flash and "opus" finds claude-opus-5.
	keywords string
}

// list is a filterable list: typing narrows it, the arrows move, Enter picks.
// A provider can list hundreds of models, so the search is the main way
// through it.
type list struct {
	items    []item
	filter   textinput.Model
	shown    []int // indexes into items that pass the filter
	cursor   int   // index into shown
	height   int   // rows visible at once
	column   int   // the widest title with a detail: details start after it
	freeText bool  // Enter with no match picks the typed text
	// plain lists are a handful of choices: no search box, and typing does
	// nothing but move.
	plain bool
}

// maxColumn is the widest a list's title column grows.
const maxColumn = 36

// newInput is a text input drawn in the picker's own colours. The defaults
// are fixed grays, the prompt and cursor light and the placeholder dark, so
// on one terminal background or the other the search box all but vanished.
func newInput() textinput.Model {
	in := textinput.New()
	accent := lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true)
	st := textinput.StyleState{
		Prompt:      accent,
		Text:        lipgloss.NewStyle(),
		Placeholder: lipgloss.NewStyle().Faint(true),
		Suggestion:  lipgloss.NewStyle().Faint(true),
	}
	styles := in.Styles()
	styles.Focused, styles.Blurred = st, st
	styles.Cursor.Color = lipgloss.Cyan
	in.SetStyles(styles)
	return in
}

func newList(items []item, placeholder string, freeText bool) list {
	f := newInput()
	f.Prompt = "search: "
	f.Placeholder = placeholder
	f.SetWidth(inputWidth)
	f.Focus()
	l := list{items: items, filter: f, height: 18, freeText: freeText}
	for _, it := range items {
		if it.detail != "" {
			l.column = max(l.column, lipgloss.Width(it.title))
		}
	}
	// One very long model ID would push every row's detail off a narrow
	// terminal; past this it overflows on its own row only.
	l.column = min(l.column, maxColumn)
	l.refilter()
	return l
}

// matches reports whether every space-separated term of q is in s. Terms, not
// a subsequence: "opus 5" finds Opus 5 without dragging in every model whose
// name happens to contain an o, a p, a u and an s in order.
func matches(s, q string) bool {
	s = strings.ToLower(s)
	for _, term := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(s, term) {
			return false
		}
	}
	return true
}

// refilter keeps the rows matching the search, best first: rows holding it
// as one phrase ("glm 5" puts GLM 5 above GLM 4.5), then rows holding each
// of its words.
func (l *list) refilter() {
	q := strings.ToLower(strings.TrimSpace(l.filter.Value()))
	var tiers [2][]int
	for i, it := range l.items {
		if t := tier(it, q); t >= 0 {
			tiers[t] = append(tiers[t], i)
		}
	}
	l.shown = append(tiers[0], tiers[1]...)
	l.cursor = min(l.cursor, max(len(l.shown)-1, 0))
}

// tier ranks it for q: 0 best, -1 hidden.
func tier(it item, q string) int {
	text := strings.ToLower(it.id + " " + it.title + " " + it.detail + " " + it.keywords)
	switch {
	case q == "":
		return 1
	case strings.Contains(text, q):
		return 0
	case matches(text, q):
		return 1
	}
	return -1
}

// selected is the highlighted row, or for a free-text list with nothing
// matching, the typed text as an item.
func (l *list) selected() (item, bool) {
	if len(l.shown) > 0 {
		return l.items[l.shown[l.cursor]], true
	}
	if v := strings.TrimSpace(l.filter.Value()); l.freeText && v != "" {
		return item{id: v, title: v}, true
	}
	return item{}, false
}

// selectID moves the cursor to the row with id, when it is shown.
func (l *list) selectID(id string) {
	for i, idx := range l.shown {
		if l.items[idx].id == id {
			l.cursor = i
			return
		}
	}
}

// newPlainList is a short list of choices, read top to bottom.
func newPlainList(items []item) list {
	l := newList(items, "", false)
	l.plain = true
	return l
}

func (l list) update(msg tea.Msg) (list, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up", "ctrl+p":
			l.cursor = max(l.cursor-1, 0)
			return l, nil
		case "down", "ctrl+n":
			l.cursor = min(l.cursor+1, max(len(l.shown)-1, 0))
			return l, nil
		case "pgup":
			l.cursor = max(l.cursor-l.height, 0)
			return l, nil
		case "pgdown":
			l.cursor = min(l.cursor+l.height, max(len(l.shown)-1, 0))
			return l, nil
		}
	}
	if l.plain {
		return l, nil
	}
	before := l.filter.Value()
	var cmd tea.Cmd
	l.filter, cmd = l.filter.Update(msg)
	if l.filter.Value() != before {
		l.cursor = 0
		l.refilter()
	}
	return l, cmd
}

func (l list) view(st styles) string {
	var b strings.Builder
	if !l.plain {
		b.WriteString(l.filter.View())
		b.WriteString("\n\n")
	}
	if len(l.shown) == 0 {
		msg := "  no match"
		if l.freeText && strings.TrimSpace(l.filter.Value()) != "" {
			msg = "  no match — Enter uses " + fmt.Sprintf("%q", strings.TrimSpace(l.filter.Value()))
		}
		b.WriteString(st.dim.Render(msg))
		b.WriteString("\n")
		return b.String()
	}
	lines, start, end := l.window(st)
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")
	if start > 0 || end < len(l.shown) {
		b.WriteString(st.dim.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(l.shown))))
		b.WriteString("\n")
	}
	return b.String()
}

// window renders at most l.height rows around the cursor.
func (l list) window(st styles) (lines []string, start, end int) {
	start = max(0, min(l.cursor-l.height/3, len(l.shown)-l.height))
	for end = start; end < len(l.shown) && len(lines) < l.height; end++ {
		lines = append(lines, l.row(st, l.items[l.shown[end]], end == l.cursor))
	}
	return lines, start, end
}

func (l list) row(st styles, it item, current bool) string {
	prefix, title := "  ", it.title
	if current {
		prefix, title = st.cursor.Render("› "), st.selected.Render(it.title)
	}
	line := prefix + title
	if l.plain {
		// A choice's detail is a sentence on what it means, on its own line.
		if it.mark != "" {
			line += "  " + st.ok.Render(it.mark)
		}
		if it.detail != "" {
			line += "\n    " + st.dim.Render(it.detail)
		}
		return line
	}
	if it.detail != "" || it.mark != "" {
		// Padded to the widest title, so the details and marks are columns
		// rather than a ragged edge.
		line += strings.Repeat(" ", max(l.column-lipgloss.Width(it.title), 0))
	}
	if it.detail != "" {
		line += "  " + st.dim.Render(it.detail)
	}
	if it.mark != "" {
		line += "  " + st.ok.Render(it.mark)
	}
	return line
}
