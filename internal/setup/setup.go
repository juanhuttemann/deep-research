// Package setup is the terminal side of `drsh setup`, `drsh login` and
// `drsh model`. The model picker is one searchable list of every model of
// every provider, each row naming its provider; then the key, behind a mask
// of asterisks, when that provider has none; then one free request to check
// it.
//
// It decides nothing about where a key is stored; the caller gets a Result
// and saves it. The single list across providers is Crush's model picker,
// written for this tool.
package setup

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/catalog"
)

// Options configure one picker run.
type Options struct {
	Providers []catalog.Provider
	// StoredEndpoint returns the endpoint a provider's stored key was
	// entered for; ok is false when no key is stored. A key counts only for
	// that endpoint: a custom URL is typed in, and a key for the old one is
	// no key for a new one.
	StoredEndpoint func(id string) (baseURL string, ok bool)
	// EnvEndpoint returns the endpoint the environment's key for a transport
	// ("openai", "claude") goes to; ok is false when there is no such key.
	// OPENAI_API_KEY set for OpenRouter is no key for DeepSeek.
	EnvEndpoint func(transport string) (baseURL string, ok bool)
	// Check validates a selection and key with one request; nil skips it.
	Check func(ctx context.Context, sel auth.Selection, key string) error
	// ListModels lists a custom endpoint's models.
	ListModels func(ctx context.Context, baseURL, key string) ([]string, error)
	// NewKey asks for a key even when one is stored or in the environment
	// (login).
	NewKey bool
	// Current is the saved selection, highlighted when the picker opens.
	Current auth.Selection
	// Keep, when set, names the model runs use now: a first row keeps it as
	// it is, wherever it was set, and is highlighted instead of Current. The
	// catalog has no row for a custom endpoint or a model set in the
	// environment, so highlighting alone could not offer to keep those.
	Keep string
}

// Result is what the user picked. Key is empty when no new key was entered
// (a stored or environment one is used, or the provider needs none). Keep is
// the Keep row: nothing changes, and Selection is empty.
type Result struct {
	Selection auth.Selection
	Key       string
	Keep      bool
}

type step int

const (
	stepPick       step = iota // the list of every provider's models
	stepModelID                // a model ID the catalog does not list
	stepBaseURL                // the custom endpoint's URL
	stepKey                    // the API key
	stepLoadModels             // listing a custom endpoint's models
	stepModel                  // a custom endpoint's models
	stepCheck                  // the check request in flight
	stepCheckFailed
	stepDone
)

// inputWidth fits a placeholder and a long key: with no width, the
// textinput shows only the placeholder's first letter.
const inputWidth = 60

// NoKey is stored for a custom endpoint that takes no key. vLLM, LM Studio
// and llama.cpp ignore the Authorization header unless started with one, and
// the OpenAI client will not send a request without some key.
const NoKey = "none"

// Where a provider's key comes from, shown on its heading. Empty is a
// provider that needs one: it is asked for when a model is picked.
const (
	MarkStored = "✓ key stored"
	MarkEnv    = "✓ key from your environment"
	markNoKey  = "✓ no key: runs through Claude Code, billed to your plan"
)

// Model is the Bubble Tea model. Exported for tests that drive it with key
// messages; callers use Run.
type Model struct {
	opts     Options
	st       styles
	step     step
	provider catalog.Provider
	baseURL  string
	key      string
	model    string
	list     list
	input    textinput.Model
	checkErr error
	quit     bool
	keep     bool
}

// keepID is the Keep row's id; no provider is called that.
const keepID = "\x1fkeep"

// New returns the picker on the list of every model.
func New(opts Options) Model {
	if opts.StoredEndpoint == nil {
		opts.StoredEndpoint = func(string) (string, bool) { return "", false }
	}
	if opts.EnvEndpoint == nil {
		opts.EnvEndpoint = func(string) (string, bool) { return "", false }
	}
	m := Model{opts: opts, st: newStyles()}
	m.list = newList(m.pickItems(), "search a model or a provider", false)
	m.list.selectID(rowID(opts.Current.Provider, opts.Current.Model))
	if opts.Keep != "" {
		m.list.selectID(keepID)
	}
	return m
}

// rowID identifies a row of the pick list.
func rowID(provider, model string) string { return provider + "\x1f" + model }

// otherModel is the model half of a provider's "other model ID" row.
const otherModel = "\x1fother"

// pickItems is every model of every provider, the providers with a key
// first, each followed by its row for a model the catalog does not list.
func (m Model) pickItems() []item {
	var ready, rest []item
	if m.opts.Keep != "" {
		ready = append(ready, item{id: keepID, group: "Now", title: "keep " + m.opts.Keep, mark: "✓ what runs use now"})
	}
	for _, p := range m.opts.Providers {
		mark := m.keyMark(p)
		rows := m.providerRows(p, mark)
		if mark != "" {
			ready = append(ready, rows...)
		} else {
			rest = append(rest, rows...)
		}
	}
	return append(ready, rest...)
}

func (m Model) providerRows(p catalog.Provider, mark string) []item {
	if p.ID == catalog.Custom {
		return []item{{id: rowID(p.ID, ""), group: "Custom endpoint",
			title: "your own server: vLLM, LM Studio, llama.cpp, Ollama…", mark: mark}}
	}
	rows := make([]item, 0, len(p.Models)+1)
	for _, md := range p.Models {
		detail := modelDetail(md)
		if p.NoKey {
			// API prices are not what a subscription costs.
			detail = contextDetail(md)
		}
		rows = append(rows, item{id: rowID(p.ID, md.ID), group: p.Name, title: md.ID,
			keywords: md.Name, detail: detail, mark: mark})
	}
	// Catalogs lag behind providers: a model released last week is typed in.
	return append(rows, item{id: rowID(p.ID, otherModel), group: p.Name,
		title: "other model ID…", mark: mark, searchOnly: true})
}

// keyMark says whether p can run without a new key, and why.
func (m Model) keyMark(p catalog.Provider) string {
	switch {
	case p.NoKey:
		return markNoKey
	case p.ID == catalog.Custom:
		// The custom URL is not known yet: any stored key marks it.
		if _, ok := m.opts.StoredEndpoint(p.ID); ok {
			return MarkStored
		}
		return ""
	}
	return m.keyAt(p.ID, p.Transport, p.BaseURL)
}

// keyAt is the key mark for provider id at baseURL: a stored key entered
// for that endpoint, else an environment key that goes there.
func (m Model) keyAt(id, transport, baseURL string) string {
	if ep, ok := m.opts.StoredEndpoint(id); ok && auth.SameEndpoint(ep, baseURL) {
		return MarkStored
	}
	if ep, ok := m.opts.EnvEndpoint(transport); ok && auth.SameEndpoint(ep, baseURL) {
		return MarkEnv
	}
	return ""
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Result is the finished selection; ok is false when the user quit.
func (m Model) Result() (Result, bool) {
	if m.step != stepDone || m.quit {
		return Result{}, false
	}
	if m.keep {
		return Result{Keep: true}, true
	}
	return Result{Selection: m.selection(), Key: m.key}, true
}

func (m Model) selection() auth.Selection {
	return auth.Selection{Provider: m.provider.ID, Transport: m.provider.Transport, BaseURL: m.baseURL, Model: m.model}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "ctrl+c" || k.String() == "esc") {
		m.quit = true
		return m, tea.Quit
	}
	switch msg := msg.(type) {
	case modelsMsg:
		return m.gotModels(msg)
	case checkedMsg:
		return m.checked(msg)
	}
	switch m.step {
	case stepPick, stepModel:
		return m.updateList(msg)
	case stepModelID, stepBaseURL, stepKey:
		return m.updateInput(msg)
	case stepCheckFailed:
		return m.updateCheckFailed(msg)
	}
	return m, nil
}

func enter(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyPressMsg)
	return ok && k.String() == "enter"
}

func (m Model) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !enter(msg) {
		var cmd tea.Cmd
		m.list, cmd = m.list.update(msg)
		return m, cmd
	}
	it, ok := m.list.selected()
	if !ok {
		return m, nil
	}
	if m.step == stepModel {
		return m.modelChosen(it.id)
	}
	if it.id == keepID {
		m.keep, m.step = true, stepDone
		return m, tea.Quit
	}
	providerID, modelID, _ := strings.Cut(it.id, "\x1f")
	p, _ := catalog.Find(m.opts.Providers, providerID)
	if p.ID != m.provider.ID {
		// A key typed for the provider of a failed check is not this one's:
		// kept, it would be checked against, and saved for, another host.
		m.key = ""
	}
	m.provider, m.baseURL = p, p.BaseURL
	switch {
	case p.ID == catalog.Custom:
		ep, _ := m.opts.StoredEndpoint(p.ID)
		return m.ask(stepBaseURL, "base URL: ", "http://localhost:8000/v1", false, ep)
	case modelID == otherModel:
		return m.ask(stepModelID, "model ID: ", "as the provider names it, e.g. deepseek-v4.1-flash", false, "")
	}
	return m.modelChosen(modelID)
}

// modelChosen asks for the key when the provider has none, else checks.
func (m Model) modelChosen(id string) (tea.Model, tea.Cmd) {
	m.model = id
	if m.needKey() && m.key == "" {
		return m.askKey()
	}
	return m.check()
}

// needKey reports whether the provider needs a key it does not have here.
func (m Model) needKey() bool {
	if m.provider.NoKey {
		return false
	}
	return m.opts.NewKey || m.keyAt(m.provider.ID, m.provider.Transport, m.baseURL) == ""
}

func (m Model) askKey() (tea.Model, tea.Cmd) {
	hint := "paste your API key"
	if m.provider.ID == catalog.Custom {
		hint = "API key (Enter for none)"
	}
	return m.ask(stepKey, "key: ", hint, true, "")
}

func (m Model) ask(s step, prompt, placeholder string, secret bool, value string) (tea.Model, tea.Cmd) {
	in := newInput()
	in.Prompt, in.Placeholder = prompt, placeholder
	in.SetWidth(inputWidth)
	if secret {
		in.EchoMode, in.EchoCharacter = textinput.EchoPassword, '*'
	}
	in.SetValue(value)
	m.step, m.input = s, in
	return m, m.input.Focus()
}

func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !enter(msg) {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	v := strings.TrimSpace(m.input.Value())
	switch m.step {
	case stepModelID:
		if v == "" {
			return m, nil
		}
		return m.modelChosen(v)
	case stepBaseURL:
		return m.baseURLEntered(v)
	}
	return m.keyEntered(v)
}

// baseURLEntered takes the custom endpoint's URL. Its key comes before its
// models, because listing them needs it.
func (m Model) baseURLEntered(v string) (tea.Model, tea.Cmd) {
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		m.checkErr = fmt.Errorf("want an http:// or https:// URL")
		return m, nil
	}
	m.checkErr, m.baseURL = nil, strings.TrimRight(v, "/")
	if m.needKey() {
		return m.askKey()
	}
	return m.customModels()
}

func (m Model) keyEntered(v string) (tea.Model, tea.Cmd) {
	if v == "" && m.provider.ID != catalog.Custom {
		return m, nil
	}
	if v == "" {
		v = NoKey
	}
	m.key = v
	if m.provider.ID == catalog.Custom && m.model == "" {
		return m.customModels()
	}
	return m.check()
}

// customModels lists what a custom endpoint serves.
func (m Model) customModels() (tea.Model, tea.Cmd) {
	if m.opts.ListModels == nil {
		return m.gotModels(modelsMsg{})
	}
	m.step = stepLoadModels
	list, base, key := m.opts.ListModels, m.baseURL, m.key
	return m, func() tea.Msg {
		ids, err := list(context.Background(), base, key)
		return modelsMsg{ids: ids, err: err}
	}
}

type modelsMsg struct {
	ids []string
	err error
}

// gotModels shows a custom endpoint's models; when it lists none, the list
// is empty and the typed ID is used.
func (m Model) gotModels(msg modelsMsg) (tea.Model, tea.Cmd) {
	items := make([]item, 0, len(msg.ids))
	for _, id := range msg.ids {
		items = append(items, item{id: id, title: id})
	}
	m.step, m.checkErr = stepModel, msg.err
	m.list = newList(items, "search, or type a model ID", true)
	return m, nil
}

func contextDetail(md catalog.Model) string {
	if md.ContextWindow > 0 {
		return tokens(md.ContextWindow) + " ctx"
	}
	return ""
}

// modelDetail is the context window and price, where the catalog knows them.
func modelDetail(md catalog.Model) string {
	var parts []string
	if c := contextDetail(md); c != "" {
		parts = append(parts, c)
	}
	if md.CostIn > 0 || md.CostOut > 0 {
		parts = append(parts, "$"+price(md.CostIn)+"/$"+price(md.CostOut)+" per M")
	}
	return strings.Join(parts, " · ")
}

// tokens is a context window in round numbers: 1048576 is "1M", not
// "1.048576M".
func tokens(n int) string {
	if n >= 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

// price is dollars per million tokens to three significant digits.
func price(v float64) string { return fmt.Sprintf("%.3g", v) }

type checkedMsg struct{ err error }

// check validates the pick with one request before anything is saved.
func (m Model) check() (tea.Model, tea.Cmd) {
	if m.opts.Check == nil {
		m.step = stepDone
		return m, tea.Quit
	}
	m.step = stepCheck
	check, sel, key := m.opts.Check, m.selection(), m.key
	return m, func() tea.Msg { return checkedMsg{err: check(context.Background(), sel, key)} }
}

func (m Model) checked(msg checkedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.step, m.checkErr = stepCheckFailed, msg.err
		return m, nil
	}
	m.step, m.checkErr = stepDone, nil
	return m, tea.Quit
}

func (m Model) updateCheckFailed(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "s":
		m.step = stepDone
		return m, tea.Quit
	case "m":
		// Back to the whole list; a new key stays for the same provider.
		m.checkErr, m.model = nil, ""
		if m.provider.ID == catalog.Custom {
			return m.customModels()
		}
		m.step = stepPick
		m.list = newList(m.pickItems(), "search a model or a provider", false)
		return m, nil
	case "k":
		if !m.provider.NoKey {
			m.checkErr, m.opts.NewKey = nil, true
			return m.askKey()
		}
	case "q":
		m.quit = true
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() tea.View {
	return tea.NewView(m.render())
}

func (m Model) render() string {
	// The last frame stays on screen; the caller prints what was saved.
	if m.step == stepDone || m.quit {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.st.title.Render("deep-research · "+m.heading()) + "\n")
	if cur := m.opts.Current; cur.Model != "" && m.step == stepPick {
		b.WriteString(m.st.dim.Render("now: "+cur.Provider+" · "+cur.Model) + "\n")
	}
	b.WriteString("\n" + m.body())
	if m.checkErr != nil && m.step != stepCheckFailed {
		b.WriteString("\n" + m.st.err.Render(m.checkErr.Error()) + "\n")
	}
	b.WriteString("\n" + m.st.dim.Render(m.help()) + "\n")
	return b.String()
}

func (m Model) body() string {
	switch m.step {
	case stepPick, stepModel:
		return m.list.view(m.st)
	case stepKey:
		return m.input.View() + "\n" + m.st.dim.Render("  saved to auth.json, readable only by you") + "\n"
	case stepModelID, stepBaseURL:
		return m.input.View() + "\n"
	case stepLoadModels:
		return "listing models at " + m.baseURL + "…\n"
	case stepCheck:
		return "checking " + m.provider.Name + " · " + m.model + " with one request…\n"
	case stepCheckFailed:
		return m.st.err.Render("check failed: "+m.checkErr.Error()) + "\n"
	}
	return ""
}

func (m Model) heading() string {
	switch m.step {
	case stepPick:
		return "choose a model"
	case stepBaseURL:
		return "custom endpoint"
	case stepModelID:
		return m.provider.Name + " · model ID"
	case stepModel:
		return m.baseURL + " · choose a model"
	case stepKey:
		if m.model != "" {
			return m.provider.Name + " API key, for " + m.model
		}
		return m.provider.Name + " API key"
	}
	return m.provider.Name + " · " + m.model
}

func (m Model) help() string {
	switch m.step {
	case stepPick:
		return "type to search · ↑/↓ move · enter select · esc quit"
	case stepModel:
		return "type to search · ↑/↓ move · enter select · esc quit"
	case stepCheckFailed:
		h := "s save anyway · m pick another model · "
		if !m.provider.NoKey {
			h += "k enter the key again · "
		}
		return h + "q quit"
	}
	return "enter confirm · esc quit"
}

type styles struct {
	title, dim, group, cursor, selected, ok, err lipgloss.Style
}

func newStyles() styles {
	return styles{
		title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan),
		dim:      lipgloss.NewStyle().Faint(true),
		group:    lipgloss.NewStyle().Foreground(lipgloss.Magenta),
		cursor:   lipgloss.NewStyle().Foreground(lipgloss.Cyan),
		selected: lipgloss.NewStyle().Bold(true),
		ok:       lipgloss.NewStyle().Foreground(lipgloss.Green),
		err:      lipgloss.NewStyle().Foreground(lipgloss.Red),
	}
}

// Run shows the picker on the terminal and returns what was picked; ok is
// false when the user quit.
func Run(ctx context.Context, opts Options) (Result, bool, error) {
	final, err := tea.NewProgram(New(opts), tea.WithContext(ctx)).Run()
	if err != nil {
		return Result{}, false, err
	}
	res, ok := final.(Model).Result()
	return res, ok, nil
}
