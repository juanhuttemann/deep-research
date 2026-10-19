package setup

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/juanhuttemann/deep-research/internal/auth"
)

func driveSvc(t *testing.T, m ServicesModel, msgs ...tea.Msg) ServicesModel {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(ServicesModel)
	}
	return m
}

func svcOpts(cur Services, search, scraper bool) ServiceOptions {
	return ServiceOptions{Current: cur, Search: search, Scraper: scraper,
		LocalSearXNG: "http://localhost:8888", ExaURL: "https://api.exa.ai", TavilyURL: "https://api.tavily.com",
		FirecrawlURL: "https://api.firecrawl.dev"}
}

// moveTo presses down until the highlighted row is id.
func moveTo(t *testing.T, m ServicesModel, id string) ServicesModel {
	t.Helper()
	for range 10 {
		if it, ok := m.list.selected(); ok && it.id == id {
			return m
		}
		m = driveSvc(t, m, downKey)
	}
	t.Fatalf("no row %q", id)
	return m
}

var public = Services{Engine: "searxng", SearXNGURL: "auto"}

// TestSearchStartsOnTheCurrentChoice: a rerun is Enter to keep.
func TestSearchStartsOnTheCurrentChoice(t *testing.T) {
	m := NewServices(svcOpts(Services{Engine: "searxng", SearXNGURL: "http://box:8888"}, true, false))
	if it, _ := m.list.selected(); it.id != "own" {
		t.Errorf("highlighted %q, want own", it.id)
	}
	m = driveSvc(t, m, enterKey)
	if m.step != svcSearXNGURL || m.input.Value() != "http://box:8888" {
		t.Fatalf("step %d, address %q", m.step, m.input.Value())
	}
	m = driveSvc(t, m, enterKey)
	s, ok := m.Result()
	if !ok || s.SearXNGURL != "http://box:8888" || !s.SearchAsked || s.ScraperAsked {
		t.Errorf("result %+v, %v", s, ok)
	}
}

// TestHostedEngineOffersItsOwnScraper: switching search to Exa with nothing
// reading pages asks for the key once and goes on to page extraction, with
// Exa highlighted and its key not asked again.
func TestHostedEngineOffersItsOwnScraper(t *testing.T) {
	m := NewServices(svcOpts(public, true, false))
	m = driveSvc(t, moveTo(t, m, "exa"), enterKey)
	if m.step != svcKey || m.keyFor != "exa" {
		t.Fatalf("step %d for %q, want Exa's key", m.step, m.keyFor)
	}
	m = driveSvc(t, m, enterKey)
	if m.err == nil {
		t.Error("an empty key was taken for a service with none")
	}
	m = driveSvc(t, m, append(typed("exa-1"), enterKey)...)
	if m.step != svcScraper {
		t.Fatalf("step %d, want the scraper", m.step)
	}
	if it, _ := m.list.selected(); it.id != "exa" {
		t.Errorf("highlighted %q, want exa", it.id)
	}
	m = driveSvc(t, m, enterKey)
	s, ok := m.Result()
	if !ok || s.Engine != "exa" || s.Scraper != "exa" || s.Keys["exa"] != "exa-1" || !s.ScraperAsked {
		t.Errorf("result %+v, %v", s, ok)
	}
}

// TestSearchLeavesAWorkingScraperAlone: a scraper that still works is not
// asked about when only search changes.
func TestSearchLeavesAWorkingScraperAlone(t *testing.T) {
	cur := Services{Engine: "searxng", SearXNGURL: "auto", Scraper: "firecrawl", FirecrawlURL: "http://fc:3002"}
	m := driveSvc(t, moveTo(t, NewServices(svcOpts(cur, true, false)), "docker"), enterKey)
	s, ok := m.Result()
	if !ok || !s.Docker || s.SearXNGURL != "http://localhost:8888" || s.ScraperAsked || s.Scraper != "firecrawl" {
		t.Errorf("result %+v, %v", s, ok)
	}
}

// TestSearchOffSkipsTheScraper: with no search nothing reads pages, so the
// full setup does not ask, and the scraper setting is left as it was.
func TestSearchOffSkipsTheScraper(t *testing.T) {
	cur := Services{Engine: "searxng", SearXNGURL: "auto", Scraper: "firecrawl", FirecrawlURL: "http://fc:3002"}
	m := driveSvc(t, moveTo(t, NewServices(svcOpts(cur, true, true)), "off"), enterKey)
	s, ok := m.Result()
	if !ok || s.Engine != "off" || s.ScraperAsked || s.FirecrawlURL != "http://fc:3002" {
		t.Errorf("result %+v, %v", s, ok)
	}
}

// TestStoredKeyIsKeptWithEnter: a service with a key stored is chosen
// without typing it again.
func TestStoredKeyIsKeptWithEnter(t *testing.T) {
	opts := svcOpts(public, false, true)
	opts.KeyMark = func(service, _ string) string {
		if service == "tavily" {
			return MarkStored
		}
		return ""
	}
	m := driveSvc(t, moveTo(t, NewServices(opts), "tavily"), enterKey, enterKey)
	s, ok := m.Result()
	if !ok || s.Scraper != "tavily" || len(s.Keys) != 0 {
		t.Errorf("result %+v, %v", s, ok)
	}
}

func TestQuitChoosesNothing(t *testing.T) {
	m := driveSvc(t, NewServices(svcOpts(public, true, true)), tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := m.Result(); ok {
		t.Error("esc returned a choice")
	}
}

// TestKeepRowKeepsTheModel: setup's picker opens on the model runs use now,
// and Enter keeps it without a check or a save.
func TestKeepRowKeepsTheModel(t *testing.T) {
	checked := false
	opts := Options{Providers: providers, Keep: "localhost · qwen"}
	opts.Check = func(ctx context.Context, sel auth.Selection, key string) error { checked = true; return nil }
	m := drive(t, New(opts), enterKey)
	res, ok := m.Result()
	if !ok || !res.Keep || res.Selection.Model != "" || checked {
		t.Errorf("result %+v, %v, checked %v", res, ok, checked)
	}
}
