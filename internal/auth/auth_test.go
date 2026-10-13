package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	s, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if err := s.Set("openrouter", Credential{Key: "sk-or-1", BaseURL: "https://openrouter.ai/api/v1"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s2, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	c, ok := s2.Get("openrouter")
	if !ok || c.Key != "sk-or-1" || c.Type != "api_key" {
		t.Fatalf("Get = %+v, %v", c, ok)
	}
	if got, from, _ := s2.KeyFor("https://openrouter.ai/api/v1/", ""); got != "sk-or-1" || from != "openrouter" {
		t.Errorf("KeyFor same endpoint = %q", got)
	}
	if found, err := s2.Delete("openrouter"); !found || err != nil {
		t.Fatalf("Delete = %v, %v", found, err)
	}
	s3, _ := Load(dir, nil)
	if _, ok := s3.Get("openrouter"); ok {
		t.Error("key still stored after Delete")
	}
}

func TestFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix mode bits")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	s, _ := Load(dir, nil)
	if err := s.Set("x", Credential{Key: "k", BaseURL: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSelection(dir, Selection{Provider: "x", Transport: "openai", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, s.Path(), filepath.Join(dir, ModelFile)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if fi.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	// No temporary files left beside them.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("dir holds %d entries, want 2: %v", len(entries), entries)
	}
}

func TestLoadWarnsOnReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix mode bits")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, AuthFile)
	if err := os.WriteFile(p, []byte(`{"x":{"type":"api_key","key":"k"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	if _, err := Load(dir, &warn); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(warn.String(), "chmod 600") {
		t.Errorf("no permission warning: %q", warn.String())
	}
}

// TestInvalidFileDoesNotEchoKeys: a parse error that quoted the file would
// print the keys it holds.
func TestInvalidFileDoesNotEchoKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AuthFile), []byte(`{"x":{"key":"sk-secret"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, nil)
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("err = %v", err)
	}
}

// TestKeyBoundToEndpoint: a stored key goes only to the endpoint it was
// entered for, never to a base URL the environment swapped in.
func TestKeyBoundToEndpoint(t *testing.T) {
	s, _ := Load(t.TempDir(), nil)
	_ = s.Set("openrouter", Credential{Key: "sk-or", BaseURL: "https://openrouter.ai/api/v1"})
	if got, _, _ := s.KeyFor("https://evil.example/v1", "openrouter"); got != "" {
		t.Errorf("key sent to another endpoint: %q", got)
	}
	_ = s.Set("legacy", Credential{Key: "k"})
	if got, _, _ := s.KeyFor("", "legacy"); got != "" {
		t.Errorf("unbound key returned: %q", got)
	}
	// Two keys for one endpoint: the preferred provider's wins.
	_ = s.Set("anthropic", Credential{Key: "sk-ant", BaseURL: "https://api.anthropic.com"})
	_ = s.Set("claude-subscription", Credential{Key: "sk-sub", BaseURL: "https://api.anthropic.com"})
	if got, from, _ := s.KeyFor("https://api.anthropic.com", "claude-subscription"); got != "sk-sub" || from != "claude-subscription" {
		t.Errorf("preferred key = %q from %q", got, from)
	}
}

func TestResolveCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	if got, err := Resolve("plain"); got != "plain" || err != nil {
		t.Errorf("plain = %q, %v", got, err)
	}
	if got, err := Resolve("!printf ' sk-from-cmd\\n'"); got != "sk-from-cmd" || err != nil {
		t.Errorf("command = %q, %v", got, err)
	}
	if _, err := Resolve("!true"); err == nil {
		t.Error("empty output accepted")
	}
	_, err := Resolve("!echo sk-leak; exit 3")
	if err == nil || strings.Contains(err.Error(), "sk-leak") {
		t.Errorf("failing command err = %v", err)
	}
}

func TestSelectionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := LoadSelection(dir); ok || err != nil {
		t.Fatalf("missing selection = %v, %v", ok, err)
	}
	want := Selection{Provider: "anthropic", Transport: "claude", BaseURL: "https://api.anthropic.com", Model: "claude-opus-5-5"}
	if err := SaveSelection(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadSelection(dir)
	if !ok || err != nil || got != want {
		t.Fatalf("LoadSelection = %+v, %v, %v", got, ok, err)
	}
}

// TestNullFileIsAnEmptyStore: auth.json holding null decoded to a nil map,
// and the first Set — login's save — panicked.
func TestNullFileIsAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AuthFile), []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("x", Credential{Key: "k"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
}
