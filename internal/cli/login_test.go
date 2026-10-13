package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/catalog"
	"github.com/juanhuttemann/deep-research/internal/setup"
)

// loginEnv isolates login from the developer's config, environment and
// Catwalk, and serves a custom OpenAI-compatible endpoint that accepts key.
func loginEnv(t *testing.T, key string) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", dir)
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "OPENROUTER_API_KEY",
		"OPENROUTER_BASE_URL", "OPENROUTER_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
		"DEEP_RESEARCH_PROVIDER", "DEEP_RESEARCH_CLAUDE_MODEL"} {
		t.Setenv(k, "")
	}
	old := loadCatalog
	loadCatalog = func(context.Context) []catalog.Provider { return catalog.Embedded() }
	t.Cleanup(func() { loadCatalog = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen"}]}`))
	}))
	t.Cleanup(srv.Close)
	return dir, srv.URL + "/v1"
}

func runLoginCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := New(func() (Deps, error) { t.Fatal("login loaded the run config"); return Deps{}, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestScriptedLogin(t *testing.T) {
	dir, base := loginEnv(t, "sk-good")
	out, err := runLoginCLI(t, "sk-good\n", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if strings.Contains(out, "sk-good") {
		t.Errorf("output shows the key:\n%s", out)
	}
	store, _ := auth.Load(dir, nil)
	if c, ok := store.Get("custom"); !ok || c.Key != "sk-good" || c.BaseURL != base {
		t.Errorf("stored = %+v, %v", c, ok)
	}
	sel, ok, _ := auth.LoadSelection(dir)
	if !ok || sel.Model != "qwen" || sel.Transport != "openai" {
		t.Errorf("selection = %+v", sel)
	}
	if fi, err := os.Stat(filepath.Join(dir, auth.AuthFile)); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode %v", fi.Mode().Perm())
	}
}

// TestRejectedKeyIsNotSaved: the check runs before anything is written.
func TestRejectedKeyIsNotSaved(t *testing.T) {
	dir, base := loginEnv(t, "sk-good")
	_, err := runLoginCLI(t, "sk-bad\n", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin")
	if err == nil || strings.Contains(err.Error(), "sk-bad") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, auth.AuthFile)); err == nil {
		t.Error("a rejected key was stored")
	}
}

func TestLoginWarnsWhenTheEnvironmentOverrides(t *testing.T) {
	_, base := loginEnv(t, "sk-good")
	t.Setenv("OPENAI_MODEL", "from-env")
	out, err := runLoginCLI(t, "sk-good", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin")
	if err != nil || !strings.Contains(out, "overrides") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
}

func TestLogout(t *testing.T) {
	dir, base := loginEnv(t, "k")
	if _, err := runLoginCLI(t, "k", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := runLoginCLI(t, "", "logout")
	if err != nil || !strings.Contains(out, "removed the custom key") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	store, _ := auth.Load(dir, nil)
	if len(store.Providers()) != 0 {
		t.Error("key still stored")
	}
}

func TestPickerNeedsATerminal(t *testing.T) {
	loginEnv(t, "k")
	_, err := runLoginCLI(t, "", "login")
	if err == nil || !strings.Contains(err.Error(), "--key-stdin") {
		t.Errorf("err = %v", err)
	}
}

// TestScriptedLoginResolvesACommandKey: the "!command" form is checked with
// the key it prints, not sent to the provider as typed.
func TestScriptedLoginResolvesACommandKey(t *testing.T) {
	dir, base := loginEnv(t, "sk-resolved")
	out, err := runLoginCLI(t, "!printf sk-resolved", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	store, _ := auth.Load(dir, nil)
	if c, _ := store.Get("custom"); c.Key != "!printf sk-resolved" {
		t.Errorf("stored %q, want the command itself", c.Key)
	}
}

// TestScriptedKeylessCustomLogin: the documented way to script a local
// server that takes no key.
func TestScriptedKeylessCustomLogin(t *testing.T) {
	dir, base := loginEnv(t, setup.NoKey)
	out, err := runLoginCLI(t, "\n", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	store, _ := auth.Load(dir, nil)
	if c, _ := store.Get("custom"); c.Key != setup.NoKey {
		t.Errorf("stored %q", c.Key)
	}
	if _, err := runLoginCLI(t, "", "login", "--provider", "openrouter", "--model", "x", "--key-stdin"); err == nil {
		t.Error("an empty key was accepted for a provider that needs one")
	}
}

// TestScriptedSwitchUsesTheStoredKey: switching models by script needs no
// key on stdin when one is stored; with none anywhere the check refuses.
func TestScriptedSwitchUsesTheStoredKey(t *testing.T) {
	dir, base := loginEnv(t, "sk-good")
	if _, err := runLoginCLI(t, "sk-good", "login", "--provider", "custom", "--base-url", base, "--model", "qwen", "--key-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := runLoginCLI(t, "", "login", "--provider", "custom", "--base-url", base, "--model", "other")
	if err != nil {
		t.Fatalf("switch: %v\n%s", err, out)
	}
	if sel, _, _ := auth.LoadSelection(dir); sel.Model != "other" {
		t.Errorf("selection = %+v", sel)
	}
	_, err = runLoginCLI(t, "", "login", "--provider", "deepseek", "--model", "deepseek-v4-pro")
	if err == nil || !strings.Contains(err.Error(), "nothing saved") {
		t.Errorf("no key anywhere: err = %v", err)
	}
}
