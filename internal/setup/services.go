package setup

import (
	"context"
	"errors"
	"maps"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/juanhuttemann/deep-research/internal/auth"
)

// Services is what setup asks about web search: the engine and the page
// scraper, in config.yaml's words, plus what config.yaml does not hold.
type Services struct {
	Engine     string // searxng, exa, tavily or off
	SearXNGURL string // "auto", or one or more URLs
	// Docker is a local SearXNG that setup writes the Docker files for.
	Docker       bool
	Scraper      string // none, firecrawl, exa or tavily; "" when nothing chose one
	FirecrawlURL string
	// Keys are the keys typed here, by service ("exa"), each with the
	// address it was typed for: a hosted Firecrawl key typed before an edit
	// moved Firecrawl to another server is not that server's key. A service
	// whose stored or environment key was kept has none.
	Keys map[string]auth.Credential
	// SearchAsked and ScraperAsked say which steps were shown: only what was
	// asked is saved, so a setting nobody looked at is not rewritten.
	SearchAsked, ScraperAsked bool
}

// ServiceOptions configure one run of the search steps.
type ServiceOptions struct {
	Current Services
	// Search and Scraper are the steps to ask. Search alone goes on to the
	// scraper when the new engine leaves it nothing that works, or when a
	// hosted engine could read the pages too and nothing reads them now.
	Search, Scraper bool
	// KeyMark says where a service's key for baseURL comes from now
	// ("✓ key stored"), or "" when there is none.
	KeyMark func(service, baseURL string) string
	// The addresses the choices stand for.
	LocalSearXNG, ExaURL, TavilyURL, FirecrawlURL string
}

type svcStep int

const (
	svcEngine svcStep = iota
	svcSearXNGURL
	svcScraper
	svcFirecrawlURL
	svcKey
	svcDone
)

// ServicesModel is the Bubble Tea model of the search steps. Exported for
// tests that drive it with key messages; callers use RunServices.
type ServicesModel struct {
	opts      ServiceOptions
	st        styles
	s         Services
	step      svcStep
	list      list
	input     textinput.Model
	keyFor    string // the service whose key is asked
	inScraper bool   // past the engine: a finished step ends the run
	err       error
	quit      bool
}

// NewServices returns the search steps, on the first one asked.
func NewServices(opts ServiceOptions) ServicesModel {
	if opts.KeyMark == nil {
		opts.KeyMark = func(string, string) string { return "" }
	}
	m := ServicesModel{opts: opts, st: newStyles(), s: opts.Current}
	// Keys typed before the review sent the reader back to edit stay typed.
	m.s.Keys = maps.Clone(opts.Current.Keys)
	if m.s.Keys == nil {
		m.s.Keys = map[string]auth.Credential{}
	}
	if opts.Search {
		return m.engineStep()
	}
	next, _ := m.scraperStep()
	return next.(ServicesModel)
}

// Result is the finished choice; ok is false when the user quit.
func (m ServicesModel) Result() (Services, bool) {
	return m.s, m.step == svcDone && !m.quit
}

// inline reports whether an engine brings each result's page text.
func inline(engine string) bool { return engine == "exa" || engine == "tavily" }

func serviceName(id string) string {
	switch id {
	case "exa":
		return "Exa"
	case "tavily":
		return "Tavily"
	}
	return "Firecrawl"
}

// baseURL is the address a service's key goes to.
func (m ServicesModel) baseURL(service string) string {
	switch service {
	case "exa":
		return m.opts.ExaURL
	case "tavily":
		return m.opts.TavilyURL
	}
	return m.s.FirecrawlURL
}

// typedKey reports whether a key was typed here for service at the address
// it now has.
func (m ServicesModel) typedKey(service string) bool {
	c, ok := m.s.Keys[service]
	return ok && auth.SameEndpoint(c.BaseURL, m.baseURL(service))
}

func (m ServicesModel) hasKey(service string) bool {
	return m.typedKey(service) || m.opts.KeyMark(service, m.baseURL(service)) != ""
}

func (m ServicesModel) engineItems() []item {
	return []item{
		{id: "public", title: "public SearXNG instances",
			detail: "free; third parties see the queries, and often refuse a run partway"},
		{id: "docker", title: "local SearXNG in Docker",
			detail: "free and private; setup writes the files, you start it with one command"},
		{id: "own", title: "my own SearXNG", detail: "one address, or several separated by commas"},
		{id: "exa", title: "Exa", mark: m.opts.KeyMark("exa", m.opts.ExaURL),
			detail: "hosted, about $0.007 a search, page text included; needs a key"},
		{id: "tavily", title: "Tavily", mark: m.opts.KeyMark("tavily", m.opts.TavilyURL),
			detail: "hosted, one credit a search, page text included; needs a key"},
		{id: "off", title: "no web search", detail: "the model answers from memory; every source is unverified"},
	}
}

func (m ServicesModel) currentEngine() string {
	switch {
	case m.s.Engine != "searxng":
		return m.s.Engine
	case m.s.Docker:
		return "docker"
	case strings.EqualFold(m.s.SearXNGURL, "auto"):
		return "public"
	}
	return "own"
}

func (m ServicesModel) engineStep() ServicesModel {
	m.step, m.inScraper, m.s.SearchAsked = svcEngine, false, true
	m.list = newPlainList(m.engineItems())
	m.list.selectID(m.currentEngine())
	return m
}

func (m ServicesModel) engineChosen(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "public":
		m.s.Engine, m.s.SearXNGURL, m.s.Docker = "searxng", "auto", false
	case "docker":
		m.s.Engine, m.s.SearXNGURL, m.s.Docker = "searxng", m.opts.LocalSearXNG, true
	case "own":
		prefill := m.s.SearXNGURL
		if strings.EqualFold(prefill, "auto") || m.s.Engine != "searxng" {
			prefill = ""
		}
		return m.ask(svcSearXNGURL, "address: ", "http://localhost:8888", false, prefill)
	case "off":
		m.s.Engine, m.s.Docker = "off", false
		return m.finish()
	default:
		// A hosted engine: a local SearXNG chosen before is no longer used,
		// and its Docker files are not written.
		m.s.Engine, m.s.Docker = id, false
		return m.askKey(id)
	}
	return m.afterEngine()
}

// afterEngine goes on to the scraper when it is asked for, or when the new
// engine needs it.
func (m ServicesModel) afterEngine() (tea.Model, tea.Cmd) {
	if m.needScraper() {
		return m.scraperStep()
	}
	return m.finish()
}

func (m ServicesModel) needScraper() bool {
	if m.s.Engine == "off" {
		return false
	}
	if m.opts.Scraper {
		return true
	}
	switch m.s.Scraper {
	case "", "none":
		return inline(m.s.Engine)
	case "firecrawl":
		// A self-hosted Firecrawl needs no key; the hosted one does.
		return m.s.FirecrawlURL == "" || (m.hostedFirecrawl() && !m.hasKey("firecrawl"))
	}
	return !m.hasKey(m.s.Scraper)
}

func (m ServicesModel) hostedFirecrawl() bool {
	return m.s.FirecrawlURL != "" && strings.EqualFold(strings.TrimRight(m.s.FirecrawlURL, "/"), m.opts.FirecrawlURL)
}

func (m ServicesModel) scraperItems() []item {
	none := item{id: "none", title: "no page extraction",
		detail: "claims are checked against search snippets, never the full page"}
	if inline(m.s.Engine) {
		none.title = "no additional page extraction"
		none.detail = serviceName(m.s.Engine) + "'s results bring their pages' text; one without it stays a snippet"
	}
	return []item{
		none,
		{id: "firecrawl", title: "Firecrawl, self-hosted",
			detail: "free, full page text; runs from its own repository (docs/services.md)"},
		{id: "firecrawl-hosted", title: "Firecrawl, hosted", mark: m.opts.KeyMark("firecrawl", m.opts.FirecrawlURL),
			detail: "firecrawl.dev; needs a key"},
		{id: "exa", title: "Exa", mark: m.opts.KeyMark("exa", m.opts.ExaURL),
			detail: "hosted, about $0.001 a page; needs a key"},
		{id: "tavily", title: "Tavily", mark: m.opts.KeyMark("tavily", m.opts.TavilyURL),
			detail: "hosted, one credit per five pages; needs a key"},
	}
}

// currentScraper is the row to highlight: what reads pages now, or with
// nothing chosen and a hosted engine, that engine.
func (m ServicesModel) currentScraper() string {
	switch m.s.Scraper {
	case "":
		if inline(m.s.Engine) {
			return m.s.Engine
		}
		return "none"
	case "firecrawl":
		if m.hostedFirecrawl() {
			return "firecrawl-hosted"
		}
	}
	return m.s.Scraper
}

func (m ServicesModel) scraperStep() (tea.Model, tea.Cmd) {
	m.step, m.inScraper, m.s.ScraperAsked = svcScraper, true, true
	m.list = newPlainList(m.scraperItems())
	m.list.selectID(m.currentScraper())
	return m, nil
}

func (m ServicesModel) scraperChosen(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "none":
		m.s.Scraper = "none"
		return m.finish()
	case "firecrawl":
		prefill := m.s.FirecrawlURL
		if m.currentScraper() == "firecrawl-hosted" {
			prefill = ""
		}
		m.s.Scraper = "firecrawl"
		return m.ask(svcFirecrawlURL, "address: ", "http://localhost:3002", false, prefill)
	case "firecrawl-hosted":
		m.s.Scraper, m.s.FirecrawlURL = "firecrawl", m.opts.FirecrawlURL
		return m.askKey("firecrawl")
	}
	m.s.Scraper = id
	return m.askKey(id)
}

// askKey asks for a service's key, unless one was typed for it already
// (Exa as engine, then as scraper). A stored or environment key is kept by
// pressing Enter; a new one replaces it.
func (m ServicesModel) askKey(service string) (tea.Model, tea.Cmd) {
	if m.typedKey(service) {
		return m.next()
	}
	m.keyFor = service
	hint := "paste your " + serviceName(service) + " API key"
	switch mark := m.opts.KeyMark(service, m.baseURL(service)); mark {
	case "":
	case MarkEnv:
		hint = "Enter uses the key from your environment, or paste a new one"
	default:
		hint = "Enter keeps the stored key, or paste a new one"
	}
	return m.ask(svcKey, "key: ", hint, true, "")
}

func (m ServicesModel) ask(s svcStep, prompt, placeholder string, secret bool, value string) (tea.Model, tea.Cmd) {
	in := newInput()
	in.Prompt, in.Placeholder = prompt, placeholder
	in.SetWidth(inputWidth)
	if secret {
		in.EchoMode, in.EchoCharacter = textinput.EchoPassword, '*'
	}
	in.SetValue(value)
	m.step, m.input, m.err = s, in, nil
	return m, m.input.Focus()
}

// next is the step after a finished one.
func (m ServicesModel) next() (tea.Model, tea.Cmd) {
	if m.inScraper {
		return m.finish()
	}
	return m.afterEngine()
}

func (m ServicesModel) finish() (tea.Model, tea.Cmd) {
	m.step = svcDone
	return m, tea.Quit
}

// Init implements tea.Model.
func (m ServicesModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m ServicesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "ctrl+c" || k.String() == "esc") {
		m.quit = true
		return m, tea.Quit
	}
	switch m.step {
	case svcEngine, svcScraper:
		if !enter(msg) {
			var cmd tea.Cmd
			m.list, cmd = m.list.update(msg)
			return m, cmd
		}
		it, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		if m.step == svcEngine {
			return m.engineChosen(it.id)
		}
		return m.scraperChosen(it.id)
	case svcSearXNGURL, svcFirecrawlURL, svcKey:
		if !enter(msg) {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m.entered(strings.TrimSpace(m.input.Value()))
	}
	return m, nil
}

func (m ServicesModel) entered(v string) (tea.Model, tea.Cmd) {
	switch m.step {
	case svcSearXNGURL:
		if err := checkURLs(v); err != nil {
			m.err = err
			return m, nil
		}
		m.s.Engine, m.s.SearXNGURL, m.s.Docker = "searxng", v, false
		return m.afterEngine()
	case svcFirecrawlURL:
		if err := checkURLs(v); err != nil || strings.Contains(v, ",") {
			m.err = errors.New("want one http:// or https:// address")
			return m, nil
		}
		m.s.FirecrawlURL = strings.TrimRight(v, "/")
		return m.finish()
	}
	if v == "" && !m.hasKey(m.keyFor) {
		m.err = errors.New(serviceName(m.keyFor) + " needs a key")
		return m, nil
	}
	if v != "" {
		m.s.Keys[m.keyFor] = auth.Credential{Key: v, BaseURL: m.baseURL(m.keyFor)}
	}
	return m.next()
}

// checkURLs accepts one http(s) address or several separated by commas.
func checkURLs(v string) error {
	if v == "" {
		return errors.New("want an http:// or https:// address")
	}
	for u := range strings.SplitSeq(v, ",") {
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return errors.New("want http:// or https:// addresses, separated by commas")
		}
	}
	return nil
}

// View implements tea.Model.
func (m ServicesModel) View() tea.View {
	if m.step == svcDone || m.quit {
		return tea.NewView("")
	}
	var b strings.Builder
	title, about := m.heading()
	b.WriteString(m.st.title.Render("deep-research · "+title) + "\n")
	if about != "" {
		b.WriteString(m.st.dim.Render(about) + "\n")
	}
	b.WriteString("\n")
	switch m.step {
	case svcEngine, svcScraper:
		b.WriteString(m.list.view(m.st))
	case svcKey:
		b.WriteString(m.input.View() + "\n" + m.st.dim.Render("  saved to auth.json, readable only by you") + "\n")
	default:
		b.WriteString(m.input.View() + "\n")
	}
	if m.err != nil {
		b.WriteString("\n" + m.st.err.Render(m.err.Error()) + "\n")
	}
	help := "enter confirm · esc quit"
	if m.step == svcEngine || m.step == svcScraper {
		help = "↑/↓ move · enter select · esc quit"
	}
	b.WriteString("\n" + m.st.dim.Render(help) + "\n")
	return tea.NewView(b.String())
}

func (m ServicesModel) heading() (title, about string) {
	switch m.step {
	case svcEngine:
		return "web search", "Where research looks for sources."
	case svcScraper:
		return "page extraction", "What reads each search result's page, so a claim is checked against the page itself."
	case svcSearXNGURL:
		return "your SearXNG", "Its JSON API should be on; an instance without it is read through its result page."
	case svcFirecrawlURL:
		return "your Firecrawl", ""
	}
	return serviceName(m.keyFor) + " API key", ""
}

// RunServices shows the search steps on the terminal and returns what was
// chosen; ok is false when the user quit.
func RunServices(ctx context.Context, opts ServiceOptions) (Services, bool, error) {
	final, err := tea.NewProgram(NewServices(opts), tea.WithContext(ctx)).Run()
	if err != nil {
		return Services{}, false, err
	}
	s, ok := final.(ServicesModel).Result()
	return s, ok, nil
}
