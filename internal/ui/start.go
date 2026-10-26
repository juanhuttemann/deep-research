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
// is, so the two look and answer keys alike. It returns "" when the reader
// quit (Esc, Ctrl-C).
func AskQuestion(stdin io.Reader, stdout io.Writer, noColor bool, s agent.Setup) string {
	sig := catchSignals(true)
	in := NewInput(stdin, nil)
	r := NewRenderer(stdout, Theme{Enabled: !noColor})
	defer guardTerminal(sig, in, r)()
	return askQuestion(in, r, s, examples[rand.IntN(len(examples))])
}

func askQuestion(in Input, r *Renderer, s agent.Setup, example string) string {
	r.RenderStart(s, example)
	echo := func(text string) { r.SetPrompt("research", text) }
	q, ok := "", true
	for ok && q == "" {
		q, ok = readPromptRaw(in, nil, example, echo)
	}
	return q
}

// RenderStart shows the start screen.
func (r *Renderer) RenderStart(s agent.Setup, example string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode, r.setup, r.example = "start", s, example
	r.promptLabel, r.promptText = "research", ""
	if !r.plain {
		r.paintLocked(true)
	}
}

// startLines is the start screen, headed as the live frame and the page's
// top bar are: the title, and the page's model and search chips.
func (r *Renderer) startLines() []string {
	chips := "model " + r.setup.Model + " · search " + searchChip(r.setup)
	return []string{r.heading("DEEP RESEARCH", chips), r.rule(), r.promptRow(), r.rule(),
		"  " + r.T.hints("[enter] research   [tab] use the example   [esc] quit")}
}

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
