package config

import (
	"path/filepath"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
)

// loginIn loads a config from a temp dir holding what `deep-research login`
// saves, and an empty config.yaml. Callers clear the provider environment
// first, then set what the test needs.
func loginIn(t *testing.T, sel *auth.Selection, creds map[string]auth.Credential) Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", dir)
	writeFile(t, filepath.Join(dir, "config.yaml"), "")
	store, err := auth.Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, c := range creds {
		if err := store.Set(id, c); err != nil {
			t.Fatal(err)
		}
	}
	if sel != nil {
		if err := auth.SaveSelection(dir, *sel); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

var (
	orSel  = &auth.Selection{Provider: "openrouter", Transport: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "a/b"}
	orCred = map[string]auth.Credential{"openrouter": {Key: "sk-stored", BaseURL: "https://openrouter.ai/api/v1"}}
)

func TestSavedSelectionAndKey(t *testing.T) {
	clearProviderEnv(t)
	cfg := loginIn(t, orSel, orCred)
	if cfg.Provider != "openai" || cfg.OpenAIModel != "a/b" || cfg.OpenAIAPIKey != "sk-stored" || cfg.KeySource != agent.KeyFromLogin {
		t.Errorf("cfg = %s %s %q %s", cfg.Provider, cfg.OpenAIModel, cfg.OpenAIAPIKey, cfg.KeySource)
	}
}

// TestStoredKeyBeatsEnvKey: the key login stored is the one used; the
// environment's is the fallback.
func TestStoredKeyBeatsEnvKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	cfg := loginIn(t, orSel, orCred)
	if cfg.OpenAIAPIKey != "sk-stored" {
		t.Errorf("key = %q, want the stored one", cfg.OpenAIAPIKey)
	}
}

func TestEnvKeyIsTheFallback(t *testing.T) {
	clearProviderEnv(t)
	cfg := loginIn(t, orSel, nil)
	if cfg.OpenAIAPIKey != "" || cfg.KeySource != "" {
		t.Fatalf("key = %q from %q with nothing set", cfg.OpenAIAPIKey, cfg.KeySource)
	}
	t.Setenv("OPENAI_API_KEY", "sk-env")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAIAPIKey != "sk-env" || cfg.KeySource != agent.KeyFromEnv {
		t.Errorf("key = %q from %q", cfg.OpenAIAPIKey, cfg.KeySource)
	}
}

// TestEnvSettingsOverrideTheSavedSelection: a one-off OPENAI_MODEL=... run
// still works after login.
func TestEnvSettingsOverrideTheSavedSelection(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_MODEL", "env/model")
	cfg := loginIn(t, orSel, orCred)
	if cfg.OpenAIModel != "env/model" {
		t.Errorf("model = %q, want the environment's", cfg.OpenAIModel)
	}
	t.Setenv("DEEP_RESEARCH_PROVIDER", "claude")
	cfg, _ = Load()
	if cfg.Provider != "claude" || cfg.AnthropicAPIKey != "" {
		t.Errorf("provider %s with key %q", cfg.Provider, cfg.AnthropicAPIKey)
	}
}

// TestStoredKeyIsNotSentToAnotherEndpoint: with the base URL overridden for a
// run, the OpenRouter key must not go to that host.
func TestStoredKeyIsNotSentToAnotherEndpoint(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_BASE_URL", "http://elsewhere.example/v1")
	cfg := loginIn(t, orSel, orCred)
	if cfg.OpenAIAPIKey != "" {
		t.Errorf("stored key sent to %s", cfg.OpenAIBaseURL)
	}
}

func TestSavedClaudeSelection(t *testing.T) {
	clearProviderEnv(t)
	ant := map[string]auth.Credential{"anthropic": {Key: "sk-ant", BaseURL: "https://api.anthropic.com"}}
	sel := &auth.Selection{Provider: "anthropic", Transport: "claude", BaseURL: "https://api.anthropic.com", Model: "claude-x"}
	cfg := loginIn(t, sel, ant)
	if cfg.Provider != "claude" || cfg.ClaudeModel != "claude-x" || cfg.AnthropicAPIKey != "sk-ant" || !cfg.ClaudeAPIOnly {
		t.Errorf("cfg = %s %s %q apiOnly=%v", cfg.Provider, cfg.ClaudeModel, cfg.AnthropicAPIKey, cfg.ClaudeAPIOnly)
	}
	// The subscription keeps Claude Code first, the stored key as fallback.
	sel.Provider = "claude-subscription"
	cfg = loginIn(t, sel, ant)
	if cfg.ClaudeAPIOnly || cfg.AnthropicAPIKey != "sk-ant" {
		t.Errorf("subscription: apiOnly=%v key=%q", cfg.ClaudeAPIOnly, cfg.AnthropicAPIKey)
	}
	// The OpenAI settings are not taken from a Claude selection.
	if cfg.OpenAIModel != DefaultModel {
		t.Errorf("openai model = %q", cfg.OpenAIModel)
	}
}

// TestNoLoginBehavesAsBefore: without auth.json or model.json, the file and
// environment decide, as they always have.
func TestNoLoginBehavesAsBefore(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	cfg := loginIn(t, nil, nil)
	if cfg.Provider != "openai" || cfg.OpenAIModel != DefaultModel || cfg.OpenAIAPIKey != "sk-env" {
		t.Errorf("cfg = %s %s %q", cfg.Provider, cfg.OpenAIModel, cfg.OpenAIAPIKey)
	}
}

// TestBaseURLOverrideKeepsTheAPIChoice: ANTHROPIC_BASE_URL pointing a run at
// a proxy left ClaudeAPIOnly false, and the run went to Anthropic through
// claude -p, which ignores that URL, instead of the endpoint chosen.
func TestBaseURLOverrideKeepsTheAPIChoice(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "http://proxy.example")
	t.Setenv("ANTHROPIC_API_KEY", "sk-proxy")
	sel := &auth.Selection{Provider: "anthropic", Transport: "claude", BaseURL: "https://api.anthropic.com", Model: "claude-x"}
	cfg := loginIn(t, sel, nil)
	if !cfg.ClaudeAPIOnly || cfg.AnthropicBaseURL != "http://proxy.example" || cfg.AnthropicAPIKey != "sk-proxy" {
		t.Errorf("apiOnly=%v base=%s key=%q", cfg.ClaudeAPIOnly, cfg.AnthropicBaseURL, cfg.AnthropicAPIKey)
	}
}

// TestEnvKeyGoesOnlyToItsEndpoint: OPENAI_API_KEY, set for OpenRouter (the
// default endpoint), is not sent to the DeepSeek login chose; login asks for
// DeepSeek's key instead.
func TestEnvKeyGoesOnlyToItsEndpoint(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-or-env")
	ds := &auth.Selection{Provider: "deepseek", Transport: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-pro"}
	cfg := loginIn(t, ds, nil)
	if cfg.OpenAIAPIKey != "" {
		t.Errorf("OpenRouter key sent to DeepSeek: %q", cfg.OpenAIAPIKey)
	}
	if base, key := cfg.FallbackKey("openai"); key != "sk-or-env" || base != "https://openrouter.ai/api/v1" {
		t.Errorf("FallbackKey = %s %q", base, key)
	}
	// The same key with OpenRouter chosen is used.
	cfg = loginIn(t, orSel, nil)
	if cfg.OpenAIAPIKey != "sk-or-env" {
		t.Errorf("key = %q", cfg.OpenAIAPIKey)
	}
}
