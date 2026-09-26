package config

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdirTemp points config discovery at fresh directories and returns the
// config dir, so a test never reads or writes the developer's real config.
func chdirTemp(t *testing.T) string {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	t.Chdir(t.TempDir())
	return cfgDir
}

// `init` listed the files it wrote by ranging a map, so two identical runs
// reported them in different orders — output that cannot be diffed, scripted
// against, or trusted to mean the same thing twice.
func TestInitListsCreatedFilesInAFixedOrder(t *testing.T) {
	want := []string{"config.yaml", "agent.yaml", ".env"}
	for range 20 {
		chdirTemp(t)
		_, created, err := Init()
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		if len(created) != len(want) {
			t.Fatalf("created %v, want %d files", created, len(want))
		}
		for i, name := range want {
			if filepath.Base(created[i]) != name {
				t.Fatalf("created = %v, want the order %v", created, want)
			}
		}
	}
}

// Sub-agent concurrency is a budget like every other one, and on a rate-limited
// provider it is the one that has to come down. It used to be a literal 3 in
// the UI with no way to reach it.
func TestParallelismIsConfigurable(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		env  string
		want int
	}{
		{name: "default when unset", yaml: "model_call_retries: 2\n", want: 3},
		{name: "from the config file", yaml: "parallelism: 6\n", want: 6},
		{name: "environment overrides the file", yaml: "parallelism: 6\n", env: "2", want: 2},
		// 0 or a negative number would otherwise mean "no sub-agent runs".
		{name: "zero falls back", yaml: "parallelism: 0\n", want: 3},
		{name: "negative falls back", yaml: "parallelism: -4\n", want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := chdirTemp(t)
			writeFile(t, filepath.Join(cfgDir, "config.yaml"), tc.yaml)
			if tc.env != "" {
				t.Setenv("DEEP_RESEARCH_PARALLELISM", tc.env)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Parallelism != tc.want {
				t.Errorf("Parallelism = %d, want %d", cfg.Parallelism, tc.want)
			}
		})
	}
}

// Every config.yaml key can be overridden as DEEP_RESEARCH_<KEY>. That is a
// documented channel now (README + config.yaml), so it is also a contract:
// these keys must keep resolving from the environment, and the environment
// must keep winning over the file.
func TestDocumentedEnvOverridesApply(t *testing.T) {
	cfgDir := chdirTemp(t)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"),
		"sources_per_topic: 2\nrun_timeout: 30m\n")
	t.Setenv("DEEP_RESEARCH_SOURCES_PER_TOPIC", "9")
	t.Setenv("DEEP_RESEARCH_RUN_TIMEOUT", "90s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourcesPerTopic != 9 {
		t.Errorf("SourcesPerTopic = %d, want 9", cfg.SourcesPerTopic)
	}
	if cfg.RunTimeout.String() != "1m30s" {
		t.Errorf("RunTimeout = %s, want 1m30s", cfg.RunTimeout)
	}
}

// The override prefix is what the README and config.yaml tell people to type.
// Renaming it would break every documented DEEP_RESEARCH_* variable at once.
func TestEnvPrefixMatchesTheDocumentedName(t *testing.T) {
	if EnvPrefix != "DEEP_RESEARCH" {
		t.Errorf("EnvPrefix = %q, but the docs promise DEEP_RESEARCH_<KEY>", EnvPrefix)
	}
}

// The shipped default pointed a fresh checkout at a paid model. The first run
// should cost nothing, so the default is OpenRouter's free router.
func TestDefaultModelIsFree(t *testing.T) {
	if got := loadIn(t, "model_call_retries: 2\n").OpenAIModel; got != "openrouter/free" {
		t.Errorf("default model = %q, want openrouter/free", got)
	}
	if !strings.Contains(EnvTemplate, "OPENAI_MODEL=openrouter/free") {
		t.Errorf(".env template does not name the free model:\n%s", EnvTemplate)
	}
}

// A fresh checkout had no search at all: the model invented its sources. The
// default is now public SearXNG instances; "off" keeps the old LLM search.
func TestSearchDefaultsToPublicInstances(t *testing.T) {
	if got := loadIn(t, "model_call_retries: 2\n").SearXNGURL; got != "auto" {
		t.Errorf("default searxng_url = %q, want auto", got)
	}
	for _, off := range []string{"off", "OFF", "none"} {
		if got := loadIn(t, "searxng_url: "+off+"\n").SearXNGURL; got != "" {
			t.Errorf("searxng_url: %s resolved to %q, want LLM search", off, got)
		}
	}
	if got := loadIn(t, "searxng_url: http://localhost:8888\n").SearXNGURL; got != "http://localhost:8888" {
		t.Errorf("a pinned instance became %q", got)
	}
}

// The Docker setup existed only as prose to copy. init --docker writes it:
// a compose file and a SearXNG settings file with the JSON API enabled and a
// fresh secret, never clobbering files already there.
func TestInitDockerWritesALocalSearXNG(t *testing.T) {
	dir := t.TempDir()
	created, err := InitDocker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created %v, want the compose file and the settings file", created)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	settings, _ := os.ReadFile(filepath.Join(dir, "searxng", "settings.yml"))
	if !strings.Contains(string(compose), "127.0.0.1:8888:8080") {
		t.Errorf("compose does not publish SearXNG on localhost only:\n%s", compose)
	}
	if !strings.Contains(string(settings), "- json") || strings.Contains(string(settings), "CHANGE") {
		t.Errorf("settings lack the JSON format or a generated secret:\n%s", settings)
	}
	if again, err := InitDocker(dir); err != nil || len(again) != 0 {
		t.Errorf("second init --docker: created=%v err=%v, want nothing overwritten", again, err)
	}
	if s2, _ := os.ReadFile(filepath.Join(dir, "searxng", "settings.yml")); string(s2) != string(settings) {
		t.Error("settings changed on a second run")
	}
}
