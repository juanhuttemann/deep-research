package ui

import "strings"

// commands are the session's (cli/session.go), offered in a menu under the
// research ▸ and ask ▸ boxes while a "/" is being typed: its own, then one
// per start setting, from the list the serve page draws its controls from.
func commands() []Choice {
	out := []Choice{{Title: "/resume", Note: "past runs"}, {Title: "/model", Note: "change the model"}}
	for _, s := range Settings(RunSettings{}) {
		if s.Start {
			out = append(out, Choice{Title: "/" + s.Key, Note: "set the " + s.Label})
		}
	}
	return out
}

// CommandNames are the commands' names, for saying which there are.
func CommandNames() []string {
	var out []string
	for _, c := range commands() {
		out = append(out, c.Title)
	}
	return out
}

// completions is the commands text is the start of, while it can still be
// one: a "/" and no space. A command typed whole comes first, so it is the
// one highlighted and the one Enter runs: /mode is the start of /model,
// which is listed before it, and Enter on "/mode" opened the model picker.
func completions(text string) []Choice {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t") {
		return nil
	}
	typed := strings.ToLower(text)
	var out []Choice
	for _, c := range commands() {
		switch {
		case c.Title == typed:
			out = append([]Choice{c}, out...)
		case strings.HasPrefix(c.Title, typed):
			out = append(out, c)
		}
	}
	return out
}

// commandEditor is what a box with the command menu keeps between keys: the
// line typed and the menu's highlighted row. paint shows both; example is
// what Tab takes in an empty box, "" for none.
type commandEditor struct {
	text    string
	at      int
	cursor  int
	example string
	paint   func(text string, at, cursor int)
	// quit, when set, is told when a Ctrl-C has armed the next to leave
	// (see twice); without it one Ctrl-C leaves.
	quit func(armed bool)
}

// read takes a line, its keys handled as the menu needs; an empty line is
// waited past only when wait is set. ok is false when the reader went back
// (Esc, Ctrl-C) or the input ended.
func (e *commandEditor) read(in Input, wait bool) (string, bool) {
	var quit *twice
	if e.quit != nil {
		quit = &twice{show: e.quit}
	}
	q, ok := "", true
	for ok {
		if q, ok = readPromptRaw(in, nil, e.tab, e.echo, e.arrow, quit); q != "" || !wait {
			break
		}
	}
	return e.pick(q), ok
}

func (e *commandEditor) echo(t string, cursor int) {
	if t != e.text {
		e.text, e.at = t, 0
	}
	e.cursor = cursor
	e.paint(e.text, e.at, e.cursor)
}

func (e *commandEditor) arrow(k byte) {
	switch k {
	case 'A':
		e.at = max(e.at-1, 0)
	case 'B':
		e.at = max(min(e.at+1, len(completions(e.text))-1), 0)
	}
	e.paint(e.text, e.at, e.cursor)
}

// pick is t, or the highlighted command when t is the start of one: Enter
// on "/r" runs /resume rather than reporting an unknown command.
func (e *commandEditor) pick(t string) string {
	if m := completions(t); len(m) > 0 {
		return m[min(e.at, len(m)-1)].Title
	}
	return t
}

// tab takes the example in an empty box and the highlighted command over
// the start of one.
func (e *commandEditor) tab(t string) string {
	if t == "" && e.example != "" {
		return e.example
	}
	return e.pick(t)
}

// armQuit has the box's keys say what a second Ctrl-C does, note, while
// armed is set.
func (r *Renderer) armQuit(note string) func(armed bool) {
	return func(armed bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.quitNote = ""
		if armed {
			r.quitNote = note
		}
	}
}

// commandBlock is what a box shows under its lower rule, a blank row under
// it as every screen ends: the command menu while a command is typed, keys
// otherwise. esc is what Esc does there.
func (r *Renderer) commandBlock(text string, at int, keys, esc string) []string {
	m := completions(text)
	if r.quitNote != "" && text == "" {
		return []string{"  " + r.T.yellow(r.quitNote), ""}
	}
	if len(m) == 0 {
		return []string{"  " + r.T.hints(keys), ""}
	}
	n := len(text)
	out := make([]string, 0, len(m)+3)
	for i, c := range m {
		row := "    " + r.T.bold(c.Title[:n]) + r.T.gray(c.Title[n:])
		if i == min(at, len(m)-1) {
			row = "  " + r.T.magenta("▸ ") + r.T.bold(c.Title[:n]) + c.Title[n:]
		}
		out = append(out, row+"  "+r.T.gray(c.Note))
	}
	return append(out, "", "  "+r.T.hints("[tab] complete   [enter] run   [↑↓] move   [esc] "+esc), "")
}
