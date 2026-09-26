package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// write pre-creates files in dir so tests can simulate an existing setup.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestInitReportsOnlyCreatedFiles ensures `init` is honest: it must report
// exactly the files it actually created, and must not claim to have written
// files that already existed.
func TestInitReportsOnlyCreatedFiles(t *testing.T) {
	cfgDir := t.TempDir()
	cwd := t.TempDir()

	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// Simulate a setup where the config dir already has its yaml files.
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "model_call_retries: 2\n")
	writeFile(t, filepath.Join(cfgDir, "agent.yaml"), "researcher_instructions: x\n")

	_, created, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// The only thing init should report is the .env in the working dir.
	if len(created) != 1 {
		t.Fatalf("expected only 1 created file, got %d: %v", len(created), created)
	}
	if created[0] != filepath.Join(".env") {
		t.Errorf("expected created[0] == %q, got %q (created = %v)", filepath.Join(".env"), created[0], created)
	}
	// It must never claim to have written the already-present yaml files.
	for _, c := range created {
		if strings.HasSuffix(c, "config.yaml") || strings.HasSuffix(c, "agent.yaml") {
			t.Errorf("init must not report already-existing %q as newly created", c)
		}
	}
}

// TestInitReportsFreshCreation checks that on a clean setup `init` reports all
// the files it writes (config yaml files + .env).
func TestInitReportsFreshCreation(t *testing.T) {
	cfgDir := t.TempDir()
	cwd := t.TempDir()

	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	_, created, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	want := map[string]bool{
		filepath.Join(cfgDir, "config.yaml"): false,
		filepath.Join(cfgDir, "agent.yaml"):  false,
		filepath.Join(".env"):                false,
	}
	for _, c := range created {
		if _, ok := want[c]; ok {
			want[c] = true
		} else {
			t.Errorf("unexpected created file: %q", c)
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("expected init to create %q", p)
		}
	}
}

// clearProviderEnv isolates a test from the developer's own shell. These
// variables override the config file by design, so a machine that exports
// OPENAI_BASE_URL would otherwise fail every test about file-sourced settings.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
		"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "OPENROUTER_MODEL",
		"SEARXNG_URL", "FIRECRAWL_URL", "FIRECRAWL_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// loadIn loads a config from a temp dir holding the given config.yaml.
func loadIn(t *testing.T, yaml string) Config {
	t.Helper()
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), yaml)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// TestSourcesPerTopicKey: the setting has always meant "sources gathered per
// sub-agent", so the key says that. The old max_depth spelling keeps working
// for config files already on disk.
func TestSourcesPerTopicKey(t *testing.T) {
	if got := loadIn(t, "sources_per_topic: 7\n").SourcesPerTopic; got != 7 {
		t.Errorf("sources_per_topic: got %d, want 7", got)
	}
	if got := loadIn(t, "max_depth: 5\n").SourcesPerTopic; got != 5 {
		t.Errorf("legacy max_depth: got %d, want 5", got)
	}
	if got := loadIn(t, "model_call_retries: 2\n").SourcesPerTopic; got != 0 {
		t.Errorf("unset: got %d, want 0 (the --mode tier decides)", got)
	}
}

// The provider settings belong in config.yaml like every other setting.
// Reading them only from the environment meant a user who wrote openai_model:
// in the natural place was silently ignored.
func TestOpenAISettingsComeFromTheConfigFile(t *testing.T) {
	cfg := loadIn(t, "openai_base_url: http://localhost:18080/v1\nopenai_model: qwen3.8-27b\n")
	if cfg.OpenAIBaseURL != "http://localhost:18080/v1" {
		t.Errorf("openai_base_url: got %q", cfg.OpenAIBaseURL)
	}
	if cfg.OpenAIModel != "qwen3.8-27b" {
		t.Errorf("openai_model: got %q", cfg.OpenAIModel)
	}
}

// The environment still wins: it is where a machine-specific override, and the
// API key, belong.
func TestEnvironmentOverridesTheConfigFile(t *testing.T) {
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"),
		"openai_base_url: http://from-file/v1\nopenai_model: from-file\n")
	t.Setenv("OPENAI_BASE_URL", "http://from-env/v1")
	t.Setenv("OPENAI_MODEL", "from-env-model")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIBaseURL != "http://from-env/v1" || cfg.OpenAIModel != "from-env-model" {
		t.Errorf("env did not win: base=%q model=%q", cfg.OpenAIBaseURL, cfg.OpenAIModel)
	}
}

// Hosted Firecrawl needs a bearer token, and there was no way to supply one.
func TestFirecrawlAPIKeyIsConfigurable(t *testing.T) {
	if got := loadIn(t, "firecrawl_api_key: fc-from-file\n").FirecrawlAPIKey; got != "fc-from-file" {
		t.Errorf("firecrawl_api_key from file: got %q", got)
	}
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "firecrawl_api_key: fc-from-file\n")
	t.Setenv("FIRECRAWL_API_KEY", "fc-from-env")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FirecrawlAPIKey != "fc-from-env" {
		t.Errorf("FIRECRAWL_API_KEY should win: got %q", cfg.FirecrawlAPIKey)
	}
}

// The whole-run deadline used to be an arbitrary 10x the per-model-call
// timeout, a number unrelated to how long a plan's searches and scrapes take.
func TestRunTimeoutIsItsOwnSetting(t *testing.T) {
	if got := loadIn(t, "run_timeout: 12m\n").RunTimeout; got != 12*time.Minute {
		t.Errorf("run_timeout: got %v, want 12m", got)
	}
	if got := loadIn(t, "model_call_retries: 2\n").RunTimeout; got != 30*time.Minute {
		t.Errorf("default run_timeout: got %v, want 30m", got)
	}
}

// researcher_instructions was a required key that nothing ever read: agent.New
// builds search, analyzer, fact_checker, summarizer and planner agents only.
// A config file without it must load.
func TestAgentConfigDoesNotRequireTheDeadResearcherPrompt(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "model_call_retries: 2\n")
	writeFile(t, filepath.Join(cfgDir, "agent.yaml"), strings.Join([]string{
		"analyzer_instructions: analyze",
		"fact_checker_instructions: check",
		"summarizer_instructions: summarize",
		"search_instructions: search",
		"planner_instructions: plan",
	}, "\n")+"\n")
	if _, err := Load(); err != nil {
		t.Fatalf("Load without researcher_instructions: %v", err)
	}
}

// .env discovery used to walk from the working directory all the way to the
// filesystem root, so an unrelated .env in $HOME or / was loaded into the run
// and could inject an API key or a search URL nobody meant to use.
func TestDotEnvSearchStopsAtTheProjectRoot(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, ".env"), "OPENAI_MODEL=leaked-from-above\n")
	project := filepath.Join(outside, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sub := filepath.Join(project, "cmd", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(sub); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	got := findDotEnv()
	for _, p := range got {
		if strings.HasPrefix(p, outside) && !strings.HasPrefix(p, project) {
			t.Errorf("loaded a .env from above the project root: %q (found %v)", p, got)
		}
	}
}

// A .env inside the project is still found from a subdirectory.
func TestDotEnvIsFoundFromASubdirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(project, ".env"), "OPENAI_MODEL=from-project\n")
	sub := filepath.Join(project, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(sub); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	want := filepath.Join(project, ".env")
	if got := findDotEnv(); len(got) != 1 || got[0] != want {
		t.Errorf("findDotEnv() = %v, want [%s]", got, want)
	}
}
