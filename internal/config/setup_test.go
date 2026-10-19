package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
)

// clearSearchEnv unsets every variable that sets a search setting, so .env
// files in the test take effect: godotenv leaves a variable that is set, even
// to "", alone.
func clearSearchEnv(t *testing.T) {
	t.Helper()
	clearProviderEnv(t)
	for _, k := range []string{"SEARXNG_URL", "FIRECRAWL_URL", "FIRECRAWL_API_KEY", "EXA_API_KEY", "TAVILY_API_KEY",
		"DEEP_RESEARCH_SEARCH_ENGINE", "DEEP_RESEARCH_SCRAPER", "DEEP_RESEARCH_SEARXNG_URL", "DEEP_RESEARCH_FIRECRAWL_URL",
		"DEEP_RESEARCH_EXA_API_KEY", "DEEP_RESEARCH_TAVILY_API_KEY", "DEEP_RESEARCH_FIRECRAWL_API_KEY"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	old := startEnv
	startEnv = map[string]bool{}
	t.Cleanup(func() { startEnv = old })
}

// TestPatchYAMLKeepsTheRest: setup changes the keys it owns and no other
// byte of a file the reader wrote and commented.
func TestPatchYAMLKeepsTheRest(t *testing.T) {
	in := "# my settings\nparallelism: 6\n\nsearxng_url: http://old:8888 # the old box\n" +
		"# scraper: firecrawl\nmodel_call_timeout: 300s\n"
	out, err := patchYAML(in, [][2]string{{"searxng_url", "auto"}, {"scraper", "none"}, {"search_engine", "off"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "# my settings\nparallelism: 6\n\nsearxng_url: \"auto\"\n" +
		"scraper: \"none\"\nmodel_call_timeout: 300s\nsearch_engine: \"off\"\n"
	if out != want {
		t.Errorf("patched:\n%s\nwant:\n%s", out, want)
	}
}

// TestSearchFileStartsFromTheDefaults: with no config.yaml, setup writes the
// default one with its examples turned into the settings chosen.
func TestSearchFileStartsFromTheDefaults(t *testing.T) {
	dir := t.TempDir()
	p, err := SearchFile(dir, SearchSettings{Engine: "searxng", SearXNGURL: "http://localhost:8888"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Write(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"\nsearxng_url: \"http://localhost:8888\"\n", "\nsearch_engine: \"searxng\"\n", "parallelism: 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml lacks %q", want)
		}
	}
	if strings.Contains(got, "# searxng_url:") {
		t.Error("the example line was kept beside the setting")
	}
}

func TestSearchFileRefusesABrokenFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.yaml"), "parallelism: [3\n")
	if _, err := SearchFile(dir, SearchSettings{Engine: "off"}); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("err = %v", err)
	}
}

// TestStoredServiceKeyBeatsTheEnvironment: a key setup stored is the one
// meant; EXA_API_KEY is the fallback, as OPENAI_API_KEY is for the model.
func TestStoredServiceKeyBeatsTheEnvironment(t *testing.T) {
	clearSearchEnv(t)
	t.Setenv("EXA_API_KEY", "exa-env")
	t.Setenv("TAVILY_API_KEY", "tavily-env")
	// Exa searches and Tavily reads pages, so both keys are used.
	t.Setenv("DEEP_RESEARCH_SEARCH_ENGINE", "exa")
	t.Setenv("DEEP_RESEARCH_SCRAPER", "tavily")
	cfg := loginIn(t, nil, map[string]auth.Credential{
		auth.ServiceID("exa"): {Type: auth.TypeService, Key: "exa-stored", BaseURL: agent.ExaURL}})
	if cfg.ExaAPIKey != "exa-stored" || cfg.TavilyAPIKey != "tavily-env" {
		t.Errorf("exa %q, tavily %q", cfg.ExaAPIKey, cfg.TavilyAPIKey)
	}
	if cfg.OpenAIAPIKey != "" {
		t.Errorf("a service key reached the model: %q", cfg.OpenAIAPIKey)
	}
}

// TestFirecrawlKeyStaysWithItsAddress: a hosted Firecrawl key is not sent to
// a FIRECRAWL_URL pointed at another server.
func TestFirecrawlKeyStaysWithItsAddress(t *testing.T) {
	clearSearchEnv(t)
	creds := map[string]auth.Credential{
		auth.ServiceID("firecrawl"): {Type: auth.TypeService, Key: "fc-stored", BaseURL: agent.FirecrawlURL}}
	t.Setenv("FIRECRAWL_URL", agent.FirecrawlURL)
	if cfg := loginIn(t, nil, creds); cfg.FirecrawlAPIKey != "fc-stored" {
		t.Errorf("hosted: key %q", cfg.FirecrawlAPIKey)
	}
	t.Setenv("FIRECRAWL_URL", "http://elsewhere:3002")
	cfg := loginIn(t, nil, creds)
	if cfg.FirecrawlAPIKey != "" {
		t.Errorf("the hosted key went to another server: %q", cfg.FirecrawlAPIKey)
	}
	// And the same holds for a draft that moves Firecrawl.
	store, _ := auth.Load(os.Getenv("DEEP_RESEARCH_CONFIG_DIR"), nil)
	_ = store.SetService("firecrawl", "fc-stored", agent.FirecrawlURL)
	t.Setenv("FIRECRAWL_URL", "")
	draft, err := cfg.WithSearch(SearchSettings{Engine: "searxng", SearXNGURL: "auto", Scraper: "firecrawl",
		FirecrawlURL: "http://mine:3002"}, nil, store)
	if err != nil || draft.FirecrawlAPIKey != "" || draft.FirecrawlURL != "http://mine:3002" {
		t.Errorf("draft: %q at %q, %v", draft.FirecrawlAPIKey, draft.FirecrawlURL, err)
	}
}

// TestWithSearchKeepsWhatTheEnvironmentSets: a draft is checked as a run
// would resolve it, so an override in the environment is what is checked.
func TestWithSearchKeepsWhatTheEnvironmentSets(t *testing.T) {
	clearSearchEnv(t)
	t.Setenv("SEARXNG_URL", "http://env:8888")
	cfg := loginIn(t, nil, nil)
	draft, err := cfg.WithSearch(SearchSettings{Engine: "searxng", SearXNGURL: "auto", Scraper: "exa"},
		map[string]auth.Credential{"exa": {Key: "typed", BaseURL: agent.ExaURL}}, auth.Empty())
	if err != nil {
		t.Fatal(err)
	}
	if draft.SearXNGURL != "http://env:8888" || draft.Scraper != "exa" || draft.ExaAPIKey != "typed" {
		t.Errorf("draft = %q %q %q", draft.SearXNGURL, draft.Scraper, draft.ExaAPIKey)
	}
}

// TestOverridesNameTheirFile: a SEARXNG_URL from the config dir's .env, as
// init --docker wrote it, is named with that file, so the reader knows which
// line to remove; one setting what was chosen is no override.
func TestOverridesNameTheirFile(t *testing.T) {
	clearSearchEnv(t)
	dir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", dir)
	writeFile(t, filepath.Join(dir, ".env"), "SEARXNG_URL=http://localhost:8888\n")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	got := SearchOverrides(SearchSettings{Engine: "searxng", SearXNGURL: "auto"})
	if len(got) != 1 || got[0].Var != "SEARXNG_URL" || got[0].Source != filepath.Join(dir, ".env") {
		t.Fatalf("overrides = %+v", got)
	}
	if got := SearchOverrides(SearchSettings{Engine: "searxng", SearXNGURL: "http://localhost:8888/"}); len(got) != 0 {
		t.Errorf("the same address counted as an override: %+v", got)
	}
	if got := SearchOverrides(SearchSettings{Engine: "exa"}); len(got) != 0 {
		t.Errorf("a SearXNG address overrides nothing once Exa searches: %+v", got)
	}
	t.Setenv("OPENAI_MODEL", "x/y")
	sel := auth.Selection{Provider: "openrouter", Transport: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "a/b"}
	if got := ModelOverrides(sel); len(got) != 1 || got[0].Var != "OPENAI_MODEL" || got[0].Source != "the environment" {
		t.Errorf("model overrides = %+v", got)
	}
}

// TestTypedKeyStaysWithItsAddress: a hosted Firecrawl key typed in setup is
// not sent, by setup's check, to a FIRECRAWL_URL the environment points
// elsewhere; a run would not send the stored key there either.
func TestTypedKeyStaysWithItsAddress(t *testing.T) {
	clearSearchEnv(t)
	t.Setenv("FIRECRAWL_URL", "http://elsewhere:3002")
	cfg := loginIn(t, nil, nil)
	draft, err := cfg.WithSearch(SearchSettings{Engine: "searxng", SearXNGURL: "auto", Scraper: "firecrawl",
		FirecrawlURL: agent.FirecrawlURL}, map[string]auth.Credential{"firecrawl": {Key: "fc-typed", BaseURL: agent.FirecrawlURL}},
		auth.Empty())
	if err != nil {
		t.Fatal(err)
	}
	if draft.FirecrawlURL != "http://elsewhere:3002" || draft.FirecrawlAPIKey == "fc-typed" {
		t.Errorf("the typed key went to %s: %q", draft.FirecrawlURL, draft.FirecrawlAPIKey)
	}
}

// TestTypedKeyCommandIsRun: a "!command" typed in setup is checked with what
// it prints, as a run uses it; sent as typed it is a certain 401.
func TestTypedKeyCommandIsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	clearSearchEnv(t)
	cfg := loginIn(t, nil, nil)
	draft, err := cfg.WithSearch(SearchSettings{Engine: "exa"}, map[string]auth.Credential{"exa": {Key: "!echo exa-1", BaseURL: agent.ExaURL}},
		auth.Empty())
	if err != nil || draft.ExaAPIKey != "exa-1" {
		t.Errorf("key %q, %v", draft.ExaAPIKey, err)
	}
}

// TestUnusedServiceKeyIsNotRun: a stored key whose command fails stops only
// a run that uses its service; one on SearXNG never runs it.
func TestUnusedServiceKeyIsNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	clearSearchEnv(t)
	creds := map[string]auth.Credential{
		auth.ServiceID("tavily"): {Type: auth.TypeService, Key: "!exit 1", BaseURL: agent.TavilyURL}}
	loginIn(t, nil, creds) // Fatal when Load fails
	t.Setenv("DEEP_RESEARCH_SEARCH_ENGINE", "tavily")
	if _, err := Load(); err == nil {
		t.Error("a run on Tavily loaded without its key")
	}
}
