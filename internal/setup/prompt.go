package setup

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Choice is one key a prompt answers to, and what it does.
type Choice struct {
	Key  string // as Bubble Tea names it: "enter", "e", "esc"
	Help string
}

// PromptModel is a screen of text and the keys that answer it: setup's
// review and its failed-check question. Exported for tests.
type PromptModel struct {
	body    string
	choices []Choice
	picked  string
	st      styles
}

// NewPrompt returns the prompt.
func NewPrompt(body string, choices []Choice) PromptModel {
	return PromptModel{body: body, choices: choices, st: newStyles()}
}

// Picked is the key pressed, or "" when the prompt was quit.
func (m PromptModel) Picked() string { return m.picked }

// Init implements tea.Model.
func (m PromptModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m PromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	for _, c := range m.choices {
		if k.String() == c.Key {
			m.picked = c.Key
			return m, tea.Quit
		}
	}
	if k.String() == "ctrl+c" || k.String() == "esc" {
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model. The frame is cleared once answered; the
// caller says what came of it.
func (m PromptModel) View() tea.View {
	if m.picked != "" {
		return tea.NewView("")
	}
	help := make([]string, len(m.choices))
	for i, c := range m.choices {
		// The key in the text's own colour, what it does dim: all dim, the
		// keys were lost among the words around them.
		key, action, _ := strings.Cut(c.Help, " ")
		help[i] = key + " " + m.st.dim.Render(action)
	}
	return tea.NewView(strings.TrimRight(m.body, "\n") + "\n\n" + strings.Join(help, m.st.dim.Render("  ·  ")) + "\n")
}

// Title styles a prompt body's first line as the picker's titles are.
func Title(s string) string { return newStyles().title.Render("deep-research · " + s) }

// Dim styles a line as the pickers' hints are.
func Dim(s string) string { return newStyles().dim.Render(s) }

// Bold styles a check's name.
func Bold(s string) string { return newStyles().selected.Render(s) }

// Failed styles what says a check failed.
func Failed(s string) string { return newStyles().err.Render(s) }

// Mark colours a check's mark: ✓ green, ! yellow, ✗ red.
func Mark(m string) string {
	st := newStyles()
	switch m {
	case "✓":
		return st.ok.Render(m)
	case "!":
		return lipgloss.NewStyle().Foreground(lipgloss.Yellow).Render(m)
	default:
		return st.err.Render(m)
	}
}

// Hang wraps plain text to width columns for a row that starts indent
// columns in, every row after the first indented to line up under it.
// Words are never split: a link broken across rows is no longer one the
// terminal opens. The terminal's own wrap broke words and went back to
// the first column, which read as noise under a check's name.
func Hang(text string, width, indent int) string {
	room := max(width-indent, 20)
	var rows []string
	line, lineW := "", 0
	for _, w := range strings.Fields(text) {
		ww := lipgloss.Width(w)
		if lineW > 0 && lineW+1+ww > room {
			rows, line, lineW = append(rows, line), "", 0
		}
		if lineW > 0 {
			line, lineW = line+" ", lineW+1
		}
		line, lineW = line+w, lineW+ww
	}
	if line != "" {
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n"+strings.Repeat(" ", indent))
}

// Ask shows the prompt on the terminal and returns the key pressed, or ""
// when it was quit.
func Ask(ctx context.Context, body string, choices []Choice) (string, error) {
	final, err := tea.NewProgram(NewPrompt(body, choices), tea.WithContext(ctx)).Run()
	if err != nil {
		return "", err
	}
	return final.(PromptModel).Picked(), nil
}
