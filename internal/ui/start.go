package ui

import (
	"io"
	"math/rand/v2"
	"strings"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// examples are the start screen's example questions, the ones the browser
// page falls back on (web/index.html) when Stack Exchange does not answer.
var examples = []string{
	"how close are we to a room-temperature superconductor?",
	"is there life in the oceans of Europa or Enceladus?",
	"how did the Antikythera mechanism work?",
	"could a light sail reach Proxima Centauri within a lifetime?",
	"what killed the dinosaurs: the asteroid or the Deccan Traps?",
	"how do octopuses think with most of their neurons in their arms?",
	"when could a quantum computer break RSA-2048?",
	"what is inside a neutron star?",
	"how did Polynesian navigators find islands without instruments?",
	"can de-extinction bring back the woolly mammoth?",
}

// AskQuestion is the start screen of a bare drsh, the terminal's side of
// the browser page's: the model and search runs will use, and a box for the
// question with an example in it that Tab takes. It is drawn and read as the brief
// is, so the two look and answer keys alike. note, when set, says why the
// last step ended: the screen's first paint erases what was printed before
// it. It returns "" when the reader quit (Esc, Ctrl-C).
func AskQuestion(stdin io.Reader, stdout io.Writer, noColor bool, s agent.Setup, note string) string {
	sig := catchSignals(true)
	in := NewInput(stdin, nil)
	r := NewRenderer(stdout, Theme{Enabled: !noColor})
	defer guardTerminal(sig, in, r)()
	return askQuestion(in, r, s, examples[rand.IntN(len(examples))], note)
}

// commands are the session's (cli/session.go), offered in a menu under the
// box while a "/" is being typed.
var commands = []Choice{{Title: "/resume", Note: "past runs"}, {Title: "/model", Note: "change the model"}}

// completions is the commands text is the start of, while it can still be
// one: a "/" and no space.
func completions(text string) []Choice {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t") {
		return nil
	}
	var out []Choice
	for _, c := range commands {
		if strings.HasPrefix(c.Title, strings.ToLower(text)) {
			out = append(out, c)
		}
	}
	return out
}

func askQuestion(in Input, r *Renderer, s agent.Setup, example, note string) string {
	r.RenderStart(s, example, note)
	text, at := "", 0
	echo := func(t string) {
		if t != text {
			text, at = t, 0
		}
		r.SetCommand(text, at)
	}
	arrow := func(k byte) {
		switch k {
		case 'A':
			at = max(at-1, 0)
		case 'B':
			at = max(min(at+1, len(completions(text))-1), 0)
		}
		r.SetCommand(text, at)
	}
	// Tab takes the highlighted command over the example, and Enter on
	// the start of one runs it, rather than "unknown command /r".
	pick := func(t string) string {
		if m := completions(t); len(m) > 0 {
			return m[min(at, len(m)-1)].Title
		}
		return t
	}
	tab := func(t string) string {
		if t == "" {
			return example
		}
		return pick(t)
	}
	q, ok := "", true
	for ok && q == "" {
		q, ok = readPromptRaw(in, nil, tab, echo, arrow)
	}
	return pick(q)
}

// SetCommand is SetPrompt for the start screen, with the command menu's
// highlighted row.
func (r *Renderer) SetCommand(text string, at int) {
	r.mu.Lock()
	r.commandAt = at
	r.mu.Unlock()
	r.SetPrompt("research", text)
}

// RenderStart shows the start screen.
func (r *Renderer) RenderStart(s agent.Setup, example, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.setup, r.example, r.briefNote, r.commandAt = "start", s, example, note, 0
	r.promptLabel, r.promptText = "research", ""
	if !r.plain {
		r.paintLocked(true)
	}
}

// startLines is the start screen, headed as the live frame and the page's
// top bar are: the title, and the page's model and search chips.
func (r *Renderer) startLines() []string {
	chips := "model " + r.setup.Model + " · search " + searchChip(r.setup)
	out := []string{r.heading("DEEP RESEARCH", chips), r.rule(), r.promptRow(), r.rule()}
	if m := completions(r.promptText); len(m) > 0 {
		out = append(append(out, r.commandLines(m)...), "",
			"  "+r.T.hints("[tab] complete   [enter] run   [↑↓] move   [esc] quit"), "")
	} else {
		out = append(out, "  "+r.T.hints("[enter] research   [tab] use the example   [esc] quit"), "")
	}
	if r.briefNote != "" {
		for _, l := range wrapWords(r.briefNote, r.width-4) {
			out = append(out, "  "+r.T.yellow(l))
		}
		out = append(out, "")
	}
	return out
}

// commandLines is the command menu, the picker's rows: what is typed bold
// in each, the highlighted one marked.
func (r *Renderer) commandLines(m []Choice) []string {
	n := len(r.promptText)
	out := make([]string, 0, len(m))
	for i, c := range m {
		row := "    " + r.T.bold(c.Title[:n]) + r.T.gray(c.Title[n:])
		if i == min(r.commandAt, len(m)-1) {
			row = "  " + r.T.magenta("▸ ") + r.T.bold(c.Title[:n]) + c.Title[n:]
		}
		out = append(out, row+"  "+r.T.gray(c.Note))
	}
	return out
}

// AskPrompt is the label a question about a report is typed after, the
// page's for its box.
func AskPrompt(noColor bool) string {
	return "  " + Theme{Enabled: !noColor}.magenta("ask ▸ ")
}

// AskHeading heads the questions about a report, as the page's card is
// headed.
func AskHeading(noColor bool) string {
	t := Theme{Enabled: !noColor}
	return "\n  " + t.bold(t.cyan("Ask about this report"))
}

// AskBox draws the box a question about a report is typed in, as the start
// screen draws its own: the label between rules, the keys under it, back
// saying what an empty line does. The cursor is left after the label, where
// the terminal echoes and edits the line; EndAsk erases what is under it
// once it is read.
func AskBox(w io.Writer, noColor bool, back string) {
	r := NewRenderer(w, Theme{Enabled: !noColor})
	label := AskPrompt(noColor)
	_, _ = io.WriteString(w, "\n"+r.rule()+"\n"+label+"\n"+r.rule()+"\n  "+
		r.T.hints("[enter] ask   [empty enter] "+back+"   [/resume] past runs")+"\x1b[2A\r"+label)
}

// EndAsk erases the box's lower rule and keys, under the line just read.
func EndAsk(w io.Writer) { _, _ = io.WriteString(w, eraseToEnd) }

// searchChip names the engine and what reads the pages, without their hosts,
// as the page's chip of that name does (web/index.html).
func searchChip(s agent.Setup) string {
	bare := func(v string) string { return strings.TrimSpace(strings.Split(v, " (")[0]) }
	switch {
	case s.Pages == "" || s.Pages == "none" || s.Pages == "page text included":
		return bare(s.Search)
	case s.Pages == "snippets only":
		return bare(s.Search) + " · snippets only"
	case strings.HasPrefix(s.Pages, "not usable"):
		return bare(s.Search) + " · pages not usable"
	}
	return bare(s.Search) + " + " + bare(s.Pages)
}
