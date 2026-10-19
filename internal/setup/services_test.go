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

// moveTo goes to the top, then presses down until the highlighted row is id.
func moveTo(t *testing.T, m ServicesModel, id string) ServicesModel {
	t.Helper()
	for range 10 {
		m = driveSvc(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
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
	if !ok || s.Engine != "exa" || s.Scraper != "exa" || s.Keys["exa"].Key != "exa-1" || !s.ScraperAsked {
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

// TestHostedFirecrawlWithoutAKeyIsAsked: after `logout firecrawl` the hosted
// Firecrawl has no key, and setup search offers the step that takes one.
func TestHostedFirecrawlWithoutAKeyIsAsked(t *testing.T) {
	cur := Services{Engine: "searxng", SearXNGURL: "auto", Scraper: "firecrawl", FirecrawlURL: "https://api.firecrawl.dev"}
	m := driveSvc(t, NewServices(svcOpts(cur, true, false)), enterKey)
	if m.step != svcScraper {
		t.Errorf("step %d, want the scraper", m.step)
	}
	cur.FirecrawlURL = "http://localhost:3002"
	m = driveSvc(t, NewServices(svcOpts(cur, true, false)), enterKey)
	if _, ok := m.Result(); !ok {
		t.Error("a keyless self-hosted Firecrawl was asked about")
	}
}

// TestEditKeepsTypedKeys: going back from the review to edit does not lose
// a key typed the first time round.
func TestEditKeepsTypedKeys(t *testing.T) {
	cur := Services{Engine: "exa", Scraper: "exa", Keys: map[string]auth.Credential{"exa": {Key: "exa-1", BaseURL: "https://api.exa.ai"}},
		SearchAsked: true}
	m := driveSvc(t, NewServices(svcOpts(cur, true, false)), enterKey)
	s, ok := m.Result()
	if !ok || s.Keys["exa"].Key != "exa-1" {
		t.Errorf("result %+v, %v", s, ok)
	}
}

// TestHostedKeyIsNotMovedToAnotherServer: a hosted Firecrawl key typed
// before an edit stays bound to firecrawl.dev when the edit moves Firecrawl
// to a server of the reader's own, and choosing hosted again asks for no key.
func TestHostedKeyIsNotMovedToAnotherServer(t *testing.T) {
	m := NewServices(svcOpts(public, false, true))
	m = driveSvc(t, moveTo(t, m, "firecrawl-hosted"), enterKey)
	m = driveSvc(t, m, append(typed("fc-1"), enterKey)...)
	first, ok := m.Result()
	if !ok || first.Keys["firecrawl"].BaseURL != "https://api.firecrawl.dev" {
		t.Fatalf("first pass %+v, %v", first, ok)
	}
	// The review's edit: back to the scraper, now self-hosted.
	m = NewServices(svcOpts(first, false, true))
	m = driveSvc(t, moveTo(t, m, "firecrawl"), enterKey)
	m.input.SetValue("http://localhost:3002")
	m = driveSvc(t, m, enterKey)
	s, ok := m.Result()
	if !ok || s.FirecrawlURL != "http://localhost:3002" || s.Keys["firecrawl"].BaseURL != "https://api.firecrawl.dev" {
		t.Errorf("after edit %+v, %v", s, ok)
	}
	if m.typedKey("firecrawl") {
		t.Error("the hosted key counts as the self-hosted server's")
	}
}

// TestHostedEngineDropsTheLocalSearXNG: switching from a local SearXNG in
// Docker to Exa or to no search writes no Docker files.
func TestHostedEngineDropsTheLocalSearXNG(t *testing.T) {
	cur := Services{Engine: "searxng", SearXNGURL: "http://localhost:8888", Docker: true, Scraper: "none"}
	for _, id := range []string{"exa", "off"} {
		m := driveSvc(t, moveTo(t, NewServices(svcOpts(cur, true, false)), id), enterKey)
		if id == "exa" {
			m = driveSvc(t, m, append(typed("exa-1"), enterKey, enterKey)...)
		}
		if s, ok := m.Result(); !ok || s.Docker {
			t.Errorf("%s: %+v, %v", id, s, ok)
		}
	}
}
