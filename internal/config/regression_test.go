package config

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"os"
	"path/filepath"
	"runtime"
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
	want := []string{"config.yaml", ".env"}
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

func TestStalePromptFilesDoNotSilentlyOverrideCurrentSchemas(t *testing.T) {
	complete := "analyzer_instructions: custom analysis\nfact_checker_instructions: legacy checker\nsummarizer_instructions: custom summary\nsearch_instructions: custom search\nplanner_instructions: custom planner\n"
	for _, tc := range []struct {
		name, yaml string
		warn       bool
	}{
		{"missing version", complete, true},
		{"old version", "prompts_version: -1\n" + complete, true},
		// A missing phase falls back to the current prompt, so it is not stale.
		{"missing phase", "prompts_version: 1\n" + strings.ReplaceAll(complete, "planner_instructions: custom planner\n", ""), false},
		{"current custom prompts", "prompts_version: 1\n" + complete, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, warning := loadWithPromptWarning(t, tc.yaml)
			if strings.Contains(warning, "is not prompts_version") != tc.warn {
				t.Fatalf("warning = %q, expected warning %v", warning, tc.warn)
			}
			if cfg.FactCheckerInstructions != "legacy checker" {
				t.Fatalf("custom prompt overwritten: %q", cfg.FactCheckerInstructions)
			}
			if cfg.PlanningInstructions == "" {
				t.Fatalf("missing phase did not fall back")
			}
			// Every agent.yaml written before the chat existed lacks its prompt.
			if cfg.ChatInstructions == "" {
				t.Fatalf("an agent.yaml without chat_instructions left the chat with no prompt")
			}
		})
	}
}

func loadWithPromptWarning(t *testing.T, yaml string) (Config, string) {
	t.Helper()
	dir := chdirTemp(t)
	writeFile(t, filepath.Join(dir, "agent.yaml"), yaml)
	var cfg Config
	warning := captureStderr(t, func() {
		var err error
		if cfg, err = Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	return cfg, warning
}

// captureStderr returns what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	defer out.Close()
	previous := os.Stderr
	os.Stderr = out
	defer func() { os.Stderr = previous }()
	fn()
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(b)
}

// The stale-prompt warning said only "agent.yaml", and three directories can
// hold the one that is read. A user who ran it from their home directory went
// looking in the repo's config/, found the current version there, and could
// not tell which file the warning meant or what to do about it.
func TestStalePromptWarningNamesTheFileAndTheFix(t *testing.T) {
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", "cfg")
	t.Chdir(t.TempDir())
	writeFile(t, filepath.Join("cfg", "agent.yaml"), "planner_instructions: old planner\n")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	warning := captureStderr(t, func() {
		if _, err := Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if want := filepath.Join(cwd, "cfg", "agent.yaml"); !strings.Contains(warning, want) {
		t.Errorf("warning = %q, want it to name %s", warning, want)
	}
	if !strings.Contains(warning, "move it aside") {
		t.Errorf("warning = %q, want it to say how to get the current prompts", warning)
	}
}

// init copied every prompt into the config dir, where the copy froze: prompt
// changes in later releases never reached that install, and once the schema
// moved, every run warned while init said "already configured". The prompts
// are built into the binary; agent.yaml is only for the ones a user changes.
func TestInitLeavesPromptsToTheBinary(t *testing.T) {
	dir := chdirTemp(t)
	if _, _, err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.yaml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote agent.yaml (stat err %v); the prompts belong to the binary", err)
	}
	var cfg Config
	warning := captureStderr(t, func() {
		var err error
		if cfg, err = Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if warning != "" {
		t.Errorf("a fresh init warned: %s", warning)
	}
	if cfg.AnalyzerInstructions == "" || cfg.ChatInstructions == "" {
		t.Errorf("a fresh init left phases without the built-in prompts")
	}
}

func TestInitDoesNotCreateWorldReadableSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not supported on Windows")
	}
	dir := chdirTemp(t)
	if _, _, err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dockerDir := t.TempDir()
	if _, err := InitDocker(dockerDir); err != nil {
		t.Fatalf("InitDocker: %v", err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(dir, ".env"), 0o600},
		{filepath.Join(dockerDir, "searxng", "settings.yml"), 0o600},
		{filepath.Join(dir, "config.yaml"), 0o644},
		{filepath.Join(dockerDir, "docker-compose.yml"), 0o644},
	} {
		fi, err := os.Stat(tc.path)
		if err != nil {
			t.Fatalf("stat %s: %v", tc.path, err)
		}
		if fi.Mode().Perm() != tc.mode {
			t.Errorf("%s permissions = %o, want %o", tc.path, fi.Mode().Perm(), tc.mode)
		}
	}
}

// A bare number is nanoseconds to viper: run_timeout: 1800, meant as
// seconds, was 1.8µs, and every run failed with a deadline naming no key.
func TestBareNumberDurationIsRefusedNamingItsKey(t *testing.T) {
	for _, tc := range []struct{ yaml, env, key string }{
		{yaml: "run_timeout: 1800\n", key: "run_timeout"},
		{yaml: "model_call_timeout: 300\n", key: "model_call_timeout"},
		{env: "1800", key: "run_timeout"},
	} {
		cfgDir := chdirTemp(t)
		writeFile(t, filepath.Join(cfgDir, "config.yaml"), tc.yaml+"model_call_retries: 2\n")
		if tc.env != "" {
			t.Setenv("DEEP_RESEARCH_RUN_TIMEOUT", tc.env)
		}
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%q / env %q: Load = %v, want an error naming %s", tc.yaml, tc.env, err, tc.key)
		}
	}
}

// max_depth is an alias, and viper looks an alias's env var up under the
// new name only: DEEP_RESEARCH_MAX_DEPTH, the documented older spelling,
// was silently ignored.
func TestLegacyMaxDepthEnvApplies(t *testing.T) {
	cfgDir := chdirTemp(t)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "max_depth: 5\n")
	t.Setenv("DEEP_RESEARCH_MAX_DEPTH", "7")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcesPerTopic != 7 {
		t.Errorf("SourcesPerTopic = %d, want the env's 7", cfg.SourcesPerTopic)
	}
	t.Setenv("DEEP_RESEARCH_SOURCES_PER_TOPIC", "3")
	if cfg, _ = Load(); cfg.SourcesPerTopic != 3 {
		t.Errorf("SourcesPerTopic = %d, want the new name's 3 over the old", cfg.SourcesPerTopic)
	}
}
