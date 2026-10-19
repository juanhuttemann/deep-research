package setup

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
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
		help[i] = c.Help
	}
	return tea.NewView(strings.TrimRight(m.body, "\n") + "\n\n" + m.st.dim.Render(strings.Join(help, " · ")) + "\n")
}

// Title styles a prompt body's first line as the picker's titles are.
func Title(s string) string { return newStyles().title.Render("deep-research · " + s) }

// Dim styles a line as the pickers' hints are.
func Dim(s string) string { return newStyles().dim.Render(s) }

// Ask shows the prompt on the terminal and returns the key pressed, or ""
// when it was quit.
func Ask(ctx context.Context, body string, choices []Choice) (string, error) {
	final, err := tea.NewProgram(NewPrompt(body, choices), tea.WithContext(ctx)).Run()
	if err != nil {
		return "", err
	}
	return final.(PromptModel).Picked(), nil
}
