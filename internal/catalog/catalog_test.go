package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const fixture = `[
 {"id":"anthropic","name":"Anthropic","type":"anthropic","api_endpoint":"$ANTHROPIC_API_ENDPOINT","api_key":"$ANTHROPIC_API_KEY",
  "default_large_model_id":"claude-opus-5-5","models":[{"id":"claude-opus-5-5","name":"Claude Opus 5.5","cost_per_1m_in":4,"cost_per_1m_out":20,"context_window":1000000}]},
 {"id":"openai","name":"OpenAI","type":"openai","api_endpoint":"$OPENAI_API_ENDPOINT","api_key":"$OPENAI_API_KEY","models":[{"id":"gpt-x"}]},
 {"id":"openrouter","name":"OpenRouter","type":"openrouter","api_endpoint":"https://openrouter.ai/api/v1","api_key":"$OPENROUTER_API_KEY","models":[{"id":"a/b"}]},
 {"id":"kimi","name":"Kimi","type":"anthropic","api_endpoint":"https://api.kimi.com/coding","api_key":"$KIMI_API_KEY","models":[{"id":"k3"}]},
 {"id":"gemini","name":"Gemini","type":"google","api_endpoint":"$GEMINI_API_ENDPOINT","api_key":"$GEMINI_API_KEY","models":[{"id":"g"}]},
 {"id":"copilot","name":"Copilot","type":"openai-compat","api_endpoint":"https://api.githubcopilot.com","models":[{"id":"c"}]}
]`

func TestParse(t *testing.T) {
	ps, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	want := []string{ClaudeSubscription, "anthropic", "openai", "openrouter", "kimi", Custom}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	checks := []struct{ id, transport, base string }{
		{"anthropic", TransportClaude, AnthropicBaseURL},
		{"openai", TransportOpenAI, OpenAIBaseURL},
		{"kimi", TransportClaude, "https://api.kimi.com/coding"},
	}
	for _, c := range checks {
		p, _ := Find(ps, c.id)
		if p.Transport != c.transport || p.BaseURL != c.base {
			t.Errorf("%s = %s %s, want %s %s", c.id, p.Transport, p.BaseURL, c.transport, c.base)
		}
	}
	or, _ := Find(ps, "openrouter")
	if or.Models[0].ID != "openrouter/free" || or.DefaultModel != "openrouter/free" {
		t.Errorf("openrouter free router not first: %+v", or.Models[0])
	}
}

func TestEmbeddedSnapshotParses(t *testing.T) {
	ps := Embedded()
	if len(ps) < 10 {
		t.Fatalf("snapshot has %d providers", len(ps))
	}
	if len(ps[0].Models) == 0 {
		t.Error("Claude subscription entry has no models")
	}
	for _, p := range ps {
		if p.ID != Custom && p.BaseURL == "" {
			t.Errorf("%s has no base URL", p.ID)
		}
	}
}

func TestLoadRevalidatesAndFallsBack(t *testing.T) {
	var hits, status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if s := int(status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()
	cache := t.TempDir()
	ctx := context.Background()

	if ps := Load(ctx, srv.Client(), srv.URL, cache); len(ps) != 6 {
		t.Fatalf("live load: %d providers", len(ps))
	}
	if ps := Load(ctx, srv.Client(), srv.URL, cache); len(ps) != 6 {
		t.Fatalf("304 load: %d providers", len(ps))
	}
	status.Store(http.StatusInternalServerError)
	if ps := Load(ctx, srv.Client(), srv.URL, cache); len(ps) != 6 {
		t.Fatalf("cached load: %d providers", len(ps))
	}
	if ps := Load(ctx, srv.Client(), srv.URL, t.TempDir()); len(ps) != len(Embedded()) {
		t.Fatalf("no cache, server down: %d providers, want the snapshot", len(ps))
	}
	if hits.Load() != 4 {
		t.Errorf("server hit %d times", hits.Load())
	}
}

// With no user cache directory the cache fell back to a fixed directory in
// the shared temp dir, where another user could plant a catalog whose
// endpoints login sends a pasted key to. It is now no cache at all.
func TestNoUserCacheDirMeansNoCache(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("LocalAppData", "")
	if d := CacheDir(); d != "" && strings.HasPrefix(d, os.TempDir()) {
		t.Errorf("CacheDir() = %q, in the shared temp dir", d)
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile(cacheFile, []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b := readCache("", cacheFile); b != nil {
		t.Errorf("no cache read %q from the working directory", b)
	}
}
