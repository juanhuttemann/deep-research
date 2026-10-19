package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/auth"
)

// setupEnv is loginEnv with the search settings cleared too, and a SearXNG
// that answers every query.
func setupEnv(t *testing.T) (dir, searxng string) {
	t.Helper()
	dir, _ = loginEnv(t, "sk-good")
	for _, k := range []string{"SEARXNG_URL", "FIRECRAWL_URL", "FIRECRAWL_API_KEY", "EXA_API_KEY", "TAVILY_API_KEY",
		"DEEP_RESEARCH_SEARCH_ENGINE", "DEEP_RESEARCH_SCRAPER", "DEEP_RESEARCH_SEARXNG_URL", "DEEP_RESEARCH_FIRECRAWL_URL"} {
		t.Setenv(k, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"url": "https://a.example/x", "title": "Metasearch", "content": "an open source metasearch engine"}}})
	}))
	t.Cleanup(srv.Close)
	return dir, srv.URL
}

func readConfig(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestScriptedSetupSearch(t *testing.T) {
	dir, searxng := setupEnv(t)
	out, err := runLoginCLI(t, "", "setup", "search", "--engine", "searxng", "--url", searxng)
	if err != nil {
		t.Fatalf("setup search: %v\n%s", err, out)
	}
	cfg := readConfig(t, dir)
	for _, want := range []string{"search_engine: \"searxng\"", "searxng_url: \"" + searxng + "\""} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.yaml lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "\nscraper:") {
		t.Error("setup search wrote the scraper, which it did not ask about")
	}
	if !strings.Contains(out, "A run now uses:") {
		t.Errorf("no summary:\n%s", out)
	}
}

// TestFailedSetupCheckSavesNothing: a Firecrawl that does not answer is not
// saved as the scraper, unless the script says --no-check.
func TestFailedSetupCheckSavesNothing(t *testing.T) {
	dir, searxng := setupEnv(t)
	t.Setenv("SEARXNG_URL", searxng)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	out, err := runLoginCLI(t, "", "setup", "scraper", "--scraper", "firecrawl", "--url", dead.URL)
	if err == nil || !strings.Contains(err.Error(), "nothing saved") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if cfg := readConfig(t, dir); cfg != "" {
		t.Errorf("config.yaml written after a failed check:\n%s", cfg)
	}
	if _, err := runLoginCLI(t, "", "setup", "scraper", "--scraper", "firecrawl", "--url", dead.URL, "--no-check"); err != nil {
		t.Fatalf("--no-check: %v", err)
	}
	if cfg := readConfig(t, dir); !strings.Contains(cfg, "firecrawl_url: \""+dead.URL+"\"") {
		t.Errorf("--no-check did not save:\n%s", cfg)
	}
}

// TestScriptedHostedEngineNeedsAKey: Exa with no key anywhere fails before
// any request, and a key on stdin is stored for Exa's address alone.
func TestScriptedHostedEngineNeedsAKey(t *testing.T) {
	dir, _ := setupEnv(t)
	if _, err := runLoginCLI(t, "", "setup", "search", "--engine", "exa"); err == nil || !strings.Contains(err.Error(), "needs a key") {
		t.Fatalf("err = %v", err)
	}
	if _, err := runLoginCLI(t, "exa-1\n", "setup", "search", "--engine", "exa", "--key-stdin", "--no-check"); err != nil {
		t.Fatal(err)
	}
	store, err := auth.Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := store.Service("exa"); !ok || c.Key != "exa-1" || c.BaseURL != "https://api.exa.ai" {
		t.Errorf("stored %+v, %v", c, ok)
	}
	out, err := runLoginCLI(t, "", "logout", "exa")
	if err != nil || !strings.Contains(out, "EXA_API_KEY") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	store, _ = auth.Load(dir, nil)
	if _, ok := store.Service("exa"); ok {
		t.Error("the Exa key is still stored")
	}
}

func TestScraperNeedsSearch(t *testing.T) {
	setupEnv(t)
	t.Setenv("DEEP_RESEARCH_SEARCH_ENGINE", "off")
	if _, err := runLoginCLI(t, "", "setup", "scraper", "--scraper", "none"); err == nil || !strings.Contains(err.Error(), "search is off") {
		t.Errorf("err = %v", err)
	}
}

func TestSetupWithoutATerminalNamesTheScripts(t *testing.T) {
	setupEnv(t)
	_, err := runLoginCLI(t, "", "setup")
	if err == nil || !strings.Contains(err.Error(), "drsh setup search --engine") || !strings.Contains(err.Error(), "drsh login --provider") {
		t.Errorf("err = %v", err)
	}
}
