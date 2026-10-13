package setup

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/catalog"
)

var providers = []catalog.Provider{
	{ID: catalog.ClaudeSubscription, Name: "Claude subscription", Transport: catalog.TransportClaude,
		BaseURL: catalog.AnthropicBaseURL, NoKey: true, Models: []catalog.Model{{ID: "claude-opus-5-5"}}},
	{ID: "openrouter", Name: "OpenRouter", Transport: catalog.TransportOpenAI, BaseURL: "https://openrouter.ai/api/v1",
		DefaultModel: "openrouter/free",
		Models: []catalog.Model{{ID: "openrouter/free"}, {ID: "z-ai/glm-5", Name: "GLM 5", ContextWindow: 200000, CostIn: 1, CostOut: 3},
			{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek: DeepSeek V4.1 Flash"}}},
	{ID: "deepseek", Name: "DeepSeek", Transport: catalog.TransportOpenAI, BaseURL: "https://api.deepseek.com/v1",
		Models: []catalog.Model{{ID: "deepseek-v4-flash"}, {ID: "deepseek-v4-pro"}}},
	{ID: catalog.Custom, Name: "Custom endpoint", Transport: catalog.TransportOpenAI},
}

// drive feeds msgs to m. The commands of the two asynchronous steps — the
// model listing and the check — are run and their message fed back, as the
// program loop would; others (cursor blinks, quit) are dropped.
func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		m = next.(Model)
		for cmd != nil && (m.step == stepLoadModels || m.step == stepCheck) {
			next, cmd = m.Update(cmd())
			m = next.(Model)
		}
	}
	return m
}

func typed(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

var (
	enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}
	downKey  = tea.KeyPressMsg{Code: tea.KeyDown}
)

func steps(parts ...[]tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func one(m tea.Msg) []tea.Msg { return []tea.Msg{m} }

// shownRows is the visible rows' "group · title", in order.
func shownRows(m Model) []string {
	var rows []string
	for _, i := range m.list.shown {
		rows = append(rows, m.list.items[i].group+" · "+m.list.items[i].title)
	}
	return rows
}

// TestSearchShowsEachModelWithItsProvider: typing "deep" lists DeepSeek's
// own models first, each row naming its provider, then a row for a model
// DeepSeek's catalog entry lacks, then DeepSeek models other providers serve.
func TestSearchShowsEachModelWithItsProvider(t *testing.T) {
	m := drive(t, New(Options{Providers: providers}), typed("deep")...)
	rows := shownRows(m)
	want := []string{"DeepSeek · deepseek-v4-flash", "DeepSeek · deepseek-v4-pro", "DeepSeek · other model ID…",
		"OpenRouter · deepseek/deepseek-v4.1-flash"}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	if view := m.render(); !strings.Contains(view, "DeepSeek") || !strings.Contains(view, "deepseek-v4-pro") {
		t.Errorf("view lacks the provider column:\n%s", view)
	}
	// The other-model row is not in the unsearched list.
	for _, r := range shownRows(New(Options{Providers: providers})) {
		if strings.Contains(r, "other model ID") {
			t.Errorf("unsearched list shows %q", r)
		}
	}
}

// TestModelThenMaskedKey: the model is picked from the list, then the key
// is asked for, shown only as asterisks.
func TestModelThenMaskedKey(t *testing.T) {
	var checkedKey string
	m := New(Options{Providers: providers, Check: func(_ context.Context, _ auth.Selection, key string) error {
		checkedKey = key
		return nil
	}})
	m = drive(t, m, steps(typed("deep pro"), one(enterKey), typed("sk-ds-secret"))...)
	if m.step != stepKey {
		t.Fatalf("step = %d, want the key step", m.step)
	}
	view := m.render()
	if strings.Contains(view, "sk-ds-secret") || !strings.Contains(view, strings.Repeat("*", len("sk-ds-secret"))) {
		t.Fatalf("key not masked:\n%s", view)
	}
	if !strings.Contains(view, "DeepSeek API key, for deepseek-v4-pro") {
		t.Errorf("key step does not say what it is for:\n%s", view)
	}
	m = drive(t, m, enterKey)
	res, ok := m.Result()
	want := auth.Selection{Provider: "deepseek", Transport: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-pro"}
	if !ok || res.Selection != want || res.Key != "sk-ds-secret" || checkedKey != "sk-ds-secret" {
		t.Errorf("result = %+v key %q, ok %v, checked %q", res.Selection, res.Key, ok, checkedKey)
	}
}

// TestOtherModelID: a model the catalog does not list yet is typed in.
func TestOtherModelID(t *testing.T) {
	m := New(Options{Providers: providers, StoredEndpoint: storedAt("deepseek", "https://api.deepseek.com/v1")})
	m = drive(t, m, steps(typed("deepseek"), one(downKey), one(downKey), one(enterKey))...)
	if m.step != stepModelID {
		t.Fatalf("step = %d, want the model ID input", m.step)
	}
	m = drive(t, m, steps(typed("deepseek-v4.1-flash"), one(enterKey))...)
	if res, ok := m.Result(); !ok || res.Selection.Provider != "deepseek" || res.Selection.Model != "deepseek-v4.1-flash" || res.Key != "" {
		t.Errorf("result = %+v, %v", res, ok)
	}
}

func TestStoredOrEnvKeyIsNotAskedFor(t *testing.T) {
	or := "https://openrouter.ai/api/v1"
	for name, opts := range map[string]Options{
		"stored": {Providers: providers, StoredEndpoint: storedAt("openrouter", or)},
		"env":    {Providers: providers, EnvEndpoint: func(tr string) (string, bool) { return or, tr == "openai" }},
	} {
		m := drive(t, New(opts), steps(typed("glm"), one(enterKey))...)
		if res, ok := m.Result(); !ok || res.Key != "" || res.Selection.Model != "z-ai/glm-5" {
			t.Errorf("%s: result = %+v, %v (step %d)", name, res, ok, m.step)
		}
		if !strings.Contains(strings.Join(rowMarks(New(opts)), ","), "✓") {
			t.Errorf("%s: no key mark", name)
		}
	}
	// An environment key for OpenRouter is no key for DeepSeek.
	m := New(Options{Providers: providers, EnvEndpoint: func(string) (string, bool) { return or, true }})
	m = drive(t, m, steps(typed("deep pro"), one(enterKey))...)
	if m.step != stepKey {
		t.Errorf("DeepSeek with only an OpenRouter env key: step %d, want the key prompt", m.step)
	}
}

func rowMarks(m Model) []string {
	var marks []string
	for _, it := range m.list.items {
		marks = append(marks, it.mark)
	}
	return marks
}

// storedAt is a StoredEndpoint with one key, for id at base.
func storedAt(id, base string) func(string) (string, bool) {
	return func(p string) (string, bool) { return base, p == id }
}

func TestCustomEndpointListsItsModels(t *testing.T) {
	m := New(Options{Providers: providers, ListModels: func(_ context.Context, base, key string) ([]string, error) {
		if base != "http://gpu:8000/v1" || key != NoKey {
			return nil, errors.New("wrong endpoint or key")
		}
		return []string{"qwen3.8-27b"}, nil
	}})
	m = drive(t, m, steps(typed("custom"), one(enterKey), typed("http://gpu:8000/v1/"), one(enterKey), one(enterKey), one(enterKey))...)
	res, ok := m.Result()
	if !ok || res.Selection.BaseURL != "http://gpu:8000/v1" || res.Selection.Model != "qwen3.8-27b" || res.Key != NoKey {
		t.Errorf("result = %+v %q, %v (err %v)", res.Selection, res.Key, ok, m.checkErr)
	}
}

// TestMovedCustomEndpointAsksForAKey: the stored custom key is bound to the
// URL it was entered for; a new URL needs its own key, or the selection
// would point at an endpoint with none.
func TestMovedCustomEndpointAsksForAKey(t *testing.T) {
	opts := Options{Providers: providers, StoredEndpoint: storedAt(catalog.Custom, "http://old:8000/v1")}
	m := drive(t, New(opts), steps(typed("custom"), one(enterKey))...)
	if m.input.Value() != "http://old:8000/v1" {
		t.Errorf("base URL not prefilled with the stored one: %q", m.input.Value())
	}
	m.input.SetValue("")
	m = drive(t, m, steps(typed("http://new:9000/v1"), one(enterKey))...)
	if m.step != stepKey {
		t.Fatalf("step = %d, want a key prompt for the new endpoint", m.step)
	}
	m = drive(t, New(opts), steps(typed("custom"), one(enterKey), one(enterKey))...)
	if m.step == stepKey || m.baseURL != "http://old:8000/v1" {
		t.Errorf("stored endpoint asked for a key again (step %d, %s)", m.step, m.baseURL)
	}
}

func TestFailedCheckOffersRetry(t *testing.T) {
	fail := true
	m := New(Options{Providers: providers, Check: func(context.Context, auth.Selection, string) error {
		if fail {
			return errors.New("401 Unauthorized")
		}
		return nil
	}})
	m = drive(t, m, steps(typed("glm"), one(enterKey), typed("bad"), one(enterKey))...)
	if m.step != stepCheckFailed || !strings.Contains(m.render(), "401") {
		t.Fatalf("step = %d:\n%s", m.step, m.render())
	}
	fail = false
	m = drive(t, m, steps(typed("k"), typed("good"), one(enterKey))...)
	if res, ok := m.Result(); !ok || res.Key != "good" {
		t.Errorf("result = %+v, %v", res, ok)
	}
}

func TestCurrentSelectionIsPreselected(t *testing.T) {
	m := New(Options{Providers: providers, Current: auth.Selection{Provider: "openrouter", Model: "z-ai/glm-5"}})
	if it, _ := m.list.selected(); it.id != rowID("openrouter", "z-ai/glm-5") {
		t.Errorf("cursor on %q", it.id)
	}
	if !strings.Contains(m.render(), "now: openrouter · z-ai/glm-5") {
		t.Errorf("current model not shown:\n%s", m.render())
	}
}

func TestEscQuits(t *testing.T) {
	m := drive(t, New(Options{Providers: providers}), tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := m.Result(); ok {
		t.Error("esc produced a result")
	}
}

func TestListFilterAndScroll(t *testing.T) {
	var items []item
	for i := range 30 {
		items = append(items, item{id: string(rune('a'+i%26)) + "x", title: "model"})
	}
	l := newList(items, "", false)
	for range 40 {
		l, _ = l.update(downKey)
	}
	if l.cursor != 29 {
		t.Errorf("cursor = %d, want clamped at 29", l.cursor)
	}
	ms := newList([]item{{id: "glm-4.5", title: "GLM 4.5"}, {id: "glm-5", title: "GLM 5"}}, "", false)
	for _, m := range typed("glm 5") {
		ms, _ = ms.update(m)
	}
	if it, _ := ms.selected(); it.id != "glm-5" {
		t.Errorf("first match = %s, want the whole phrase's", it.id)
	}
	if tokens(1048576) != "1M" || tokens(200000) != "200k" || tokens(1_500_000) != "1.5M" || price(0.0438) != "0.0438" || price(0.44785) != "0.448" {
		t.Errorf("formatting: %s %s %s %s %s", tokens(1048576), tokens(200000), tokens(1_500_000), price(0.0438), price(0.44785))
	}
	if !matches("Claude Opus 5.5", "opus 5") || matches("Claude Sonnet", "opus") {
		t.Error("term matching wrong")
	}
}

// TestKeyDoesNotFollowToAnotherProvider: after a failed check, picking a
// model of another provider asks for that provider's key; the one typed for
// the first must not be checked against, or saved for, the second.
func TestKeyDoesNotFollowToAnotherProvider(t *testing.T) {
	var checked []string
	m := New(Options{Providers: providers, Check: func(_ context.Context, sel auth.Selection, key string) error {
		checked = append(checked, sel.Provider+"="+key)
		return errors.New("401 Unauthorized")
	}})
	m = drive(t, m, steps(typed("glm"), one(enterKey), typed("sk-or"), one(enterKey), typed("m"), typed("deep pro"), one(enterKey))...)
	if m.step != stepKey {
		t.Fatalf("step = %d after switching to DeepSeek, want its key prompt (checked %v)", m.step, checked)
	}
	if len(checked) != 1 {
		t.Errorf("checks = %v, want only OpenRouter's", checked)
	}
}
