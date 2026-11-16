package ui

import (
	"errors"
	"strings"
)

// Failure is a run's error the reader can act on, its parts kept apart:
// what stopped the run, the error under it, why that happens, and what to
// do. Joined into one sentence chain, a refused search read as "research
// failed: no search answered (challenge): not searched: every one of 10
// …. Public SearXNG … Without Docker, choose a hosted engine there: Exa or
// Tavily" on the start screen, one paragraph with the fix buried at its
// end. Error keeps that one-line form for stderr, JSONL and saved results;
// FailureNote lays the parts out.
type Failure struct {
	Problem string // what stopped the run: "no search answered"
	Status  string // the search status, when it names the failure
	Cause   error
	Reason  string   // why it happens, when that is not the cause's to say
	Fix     []string // what to do, a sentence each
	Doc     string   // where the fix is explained
}

func (f *Failure) Error() string {
	var b strings.Builder
	b.WriteString(f.Problem)
	if f.Status != "" {
		b.WriteString(" (" + f.Status + ")")
	}
	b.WriteString(": " + f.Cause.Error())
	for _, s := range append([]string{f.Reason}, f.Fix...) {
		if s != "" {
			b.WriteString(". " + s)
		}
	}
	if f.Doc != "" {
		b.WriteString(" (" + f.Doc + ")")
	}
	return b.String()
}

func (f *Failure) Unwrap() error { return f.Cause }

// FailureNote is err as the start screen shows it, a line per part: the
// "error: " headline, its detail indented, and each fix after an arrow,
// apart from the detail. The prefixes are the layout, so the same text
// printed to stderr outside the start screen still reads in that order.
func FailureNote(err error) string {
	f, ok := errors.AsType[*Failure](err)
	if !ok {
		return "error: " + err.Error()
	}
	lines := []string{"error: " + upperFirst(f.Problem), "  " + sentence(f.Cause.Error())}
	if f.Reason != "" {
		lines = append(lines, "  "+sentence(f.Reason))
	}
	if len(f.Fix) > 0 {
		lines = append(lines, "")
	}
	for _, s := range f.Fix {
		lines = append(lines, "→ "+sentence(s))
	}
	if f.Doc != "" {
		lines = append(lines, "  "+f.Doc)
	}
	return strings.Join(lines, "\n")
}

// sentence is s capitalised and ended with a full stop.
func sentence(s string) string {
	s = upperFirst(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// noteLines is one line of a start-screen note, wrapped to width and
// coloured by its prefix (see FailureNote); any other line is a notice,
// in yellow as before.
func (r *Renderer) noteLines(para string, width int) []string {
	var lead, cont string
	paint := r.T.yellow
	switch {
	case para == "":
		return []string{""}
	case strings.HasPrefix(para, "error: "):
		para, lead, cont, paint = strings.TrimPrefix(para, "error: "), r.T.red("✗ "), "  ", r.T.bold
	case strings.HasPrefix(para, "→ "):
		para, lead, cont, paint = strings.TrimPrefix(para, "→ "), r.T.green("→ "), "  ", noPaint
	case strings.HasPrefix(para, "  "):
		para, lead, cont, paint = strings.TrimPrefix(para, "  "), "  ", "  ", r.T.dim
	}
	var out []string
	for i, l := range wrapWords(para, width-len([]rune(cont))) {
		pre := cont
		if i == 0 && lead != "" {
			pre = lead
		}
		out = append(out, pre+paint(l))
	}
	return out
}

func noPaint(s string) string { return s }
