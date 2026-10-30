package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
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

	// The only thing init should report is the .env beside them.
	if len(created) != 1 {
		t.Fatalf("expected only 1 created file, got %d: %v", len(created), created)
	}
	if want := filepath.Join(cfgDir, ".env"); created[0] != want {
		t.Errorf("expected created[0] == %q, got %q (created = %v)", want, created[0], created)
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
		filepath.Join(cfgDir, ".env"):        false,
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
		"EXA_API_KEY", "TAVILY_API_KEY", "DEEP_RESEARCH_SEARCH_ENGINE", "DEEP_RESEARCH_SCRAPER",
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
		"DEEP_RESEARCH_PROVIDER", "DEEP_RESEARCH_CLAUDE_MODEL", "DEEP_RESEARCH_REASONING_EFFORT",
		"DEEP_RESEARCH_REASONING_EFFORT_PLAN", "DEEP_RESEARCH_REASONING_EFFORT_ANALYZE",
		"DEEP_RESEARCH_REASONING_EFFORT_FACT_CHECK", "DEEP_RESEARCH_REASONING_EFFORT_SUMMARIZE",
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

// Claude is opt-in: a checkout that names no provider keeps the
// OpenAI-compatible endpoint it always had.
func TestProviderDefaultsToOpenAI(t *testing.T) {
	cfg := loadIn(t, "")
	if cfg.Provider != "openai" {
		t.Errorf("provider: got %q, want openai", cfg.Provider)
	}
	if cfg.ClaudeModel != "claude-opus-5-5" || cfg.AnthropicBaseURL != "https://api.anthropic.com" {
		t.Errorf("claude defaults: model=%q base=%q", cfg.ClaudeModel, cfg.AnthropicBaseURL)
	}
}

// An unknown provider ran as OpenAI: "provider: anthropic" asked for
// OPENAI_API_KEY with an OpenRouter link instead of naming the typo.
func TestUnknownProviderIsAnError(t *testing.T) {
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "provider: anthropic\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "openai or claude") {
		t.Errorf("err = %v, want the providers there are", err)
	}
}

// The Claude settings resolve like every other key: the file holds them, the
// environment overrides them.
func TestClaudeSettingsComeFromFileAndEnv(t *testing.T) {
	cfg := loadIn(t, "provider: claude\nclaude_model: claude-sonnet-5-5\n"+
		"anthropic_api_key: sk-file\nanthropic_base_url: http://from-file\n")
	if cfg.Provider != "claude" || cfg.ClaudeModel != "claude-sonnet-5-5" ||
		cfg.AnthropicAPIKey != "sk-file" || cfg.AnthropicBaseURL != "http://from-file" {
		t.Errorf("from file: %+v", cfg.Config)
	}
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "provider: openai\nclaude_model: from-file\n")
	t.Setenv("DEEP_RESEARCH_PROVIDER", "claude")
	t.Setenv("DEEP_RESEARCH_CLAUDE_MODEL", "claude-haiku-5-5")
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	t.Setenv("ANTHROPIC_BASE_URL", "http://from-env")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "claude" || cfg.ClaudeModel != "claude-haiku-5-5" ||
		cfg.AnthropicAPIKey != "sk-env" || cfg.AnthropicBaseURL != "http://from-env" {
		t.Errorf("env did not win: %+v", cfg.Config)
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

// Exa and Tavily are chosen by search_engine and scraper and take their keys
// from the usual three places; nothing changes for a config that names neither.
func TestHostedSearchSettings(t *testing.T) {
	def := loadIn(t, "model_call_retries: 2\n")
	if def.SearchEngine != "searxng" || def.Scraper != "" || !def.SearchEnabled() {
		t.Errorf("defaults changed: engine %q scraper %q enabled %v", def.SearchEngine, def.Scraper, def.SearchEnabled())
	}
	cfg := loadIn(t, "search_engine: Exa\nscraper: tavily\nexa_api_key: e-file\ntavily_api_key: t-file\n")
	if cfg.SearchEngine != "exa" || cfg.Scraper != "tavily" || cfg.ExaAPIKey != "e-file" || cfg.TavilyAPIKey != "t-file" {
		t.Errorf("file settings not read: %+v", cfg.Config)
	}
	for _, off := range []string{"search_engine: off\n", "search_engine: none\n", "searxng_url: off\n"} {
		if loadIn(t, off).SearchEnabled() {
			t.Errorf("%q left search on", off)
		}
	}

	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "search_engine: exa\nexa_api_key: e-file\n")
	t.Setenv("DEEP_RESEARCH_SEARCH_ENGINE", "tavily")
	t.Setenv("EXA_API_KEY", "e-env")
	t.Setenv("DEEP_RESEARCH_TAVILY_API_KEY", "t-env")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SearchEngine != "tavily" || cfg.ExaAPIKey != "e-env" || cfg.TavilyAPIKey != "t-env" {
		t.Errorf("the environment should win: %+v", cfg.Config)
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

// Settings were read from the working directory's config/ and the nearest
// .env above it, so a run inside a project took that project's
// config/config.yaml as its own and its .env's OPENAI_API_KEY over the key
// deep-research was set up with. Only the config directory is read now, and
// a project's files that hold deep-research settings are named, not dropped
// in silence.
func TestSettingsComeOnlyFromTheConfigDir(t *testing.T) {
	// Set, then unset, so the test restores it while .env can still fill it
	// in: godotenv never overrides a variable that exists, even empty.
	t.Setenv("OPENAI_MODEL", "")
	_ = os.Unsetenv("OPENAI_MODEL")
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	project := t.TempDir()
	t.Chdir(project)
	writeFile(t, filepath.Join(project, ".env"), "OPENAI_MODEL=from-the-project\nSEARXNG_URL=http://project:8888\n")
	writeFile(t, filepath.Join(project, "config", "config.yaml"), "sources_per_topic: 99\n")
	writeFile(t, filepath.Join(cfgDir, ".env"), "OPENAI_MODEL=from-the-config-dir\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIModel != "from-the-config-dir" {
		t.Errorf("model = %q, want the config dir's", cfg.OpenAIModel)
	}
	if cfg.SourcesPerTopic == 99 {
		t.Error("the project's config/config.yaml was read")
	}
	if got := StrayFiles(); len(got) != 2 {
		t.Errorf("StrayFiles() = %v, want the project's config.yaml and .env", got)
	}
}

// A project's own .env (an API key, a database URL) is the project's, and
// must not be reported as misplaced deep-research settings.
func TestAProjectsOwnEnvIsNotStray(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, ".env", "OPENAI_API_KEY=sk-project\nDATABASE_URL=postgres://x\n# SEARXNG_URL=commented\n")
	// The deep-research repo's config/config.yaml is the embedded defaults'
	// source; running from a checkout must not warn about it.
	writeFile(t, filepath.Join("config", "config.yaml"), embeddedConfig("config.yaml"))
	if got := StrayFiles(); len(got) != 0 {
		t.Errorf("StrayFiles() = %v, want none", got)
	}
}

// init --docker left the reader to add SEARXNG_URL to a .env by hand. It is
// written for them, once, and a URL they chose is left alone.
func TestUseLocalSearXNG(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "OPENAI_MODEL=m")
	for i, want := range []bool{true, false} {
		wrote, err := UseLocalSearXNG(dir)
		if err != nil || wrote != want {
			t.Fatalf("call %d: wrote=%v err=%v, want %v", i+1, wrote, err, want)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(b) != "OPENAI_MODEL=m\nSEARXNG_URL="+LocalSearXNG+"\n" {
		t.Errorf(".env = %q", b)
	}
}

// Reasoning effort lowers only the phases accuracy does not rest on, unless
// told otherwise: the analysis, the fact-check and the report keep the
// provider's default.
func TestReasoningEffortDefaults(t *testing.T) {
	got := loadIn(t, "").Effort
	want := agent.Effort{Plan: "low", Clarify: "low", Chat: "medium"}
	if got != want {
		t.Errorf("effort = %+v, want %+v", got, want)
	}
}

// Each phase is set on its own, from the file or the environment, and a file
// naming one phase keeps the others' defaults: a config.yaml on disk replaces
// the embedded one whole.
func TestReasoningEffortComesFromFileAndEnv(t *testing.T) {
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"),
		"reasoning_effort:\n  analyze: Medium\n  fact_check: high\n  summarize: low\n")
	t.Setenv("DEEP_RESEARCH_REASONING_EFFORT_FACT_CHECK", "low")
	t.Setenv("DEEP_RESEARCH_REASONING_EFFORT_PLAN", "default")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := agent.Effort{Clarify: "low", Analyze: "medium", FactCheck: "low", Summarize: "low", Chat: "medium"}
	if cfg.Effort != want {
		t.Errorf("effort = %+v, want %+v", cfg.Effort, want)
	}
}

// A level or a phase the tool does not know is an error naming it, not a
// setting quietly ignored; so is one level for every phase.
func TestReasoningEffortRejectsWhatItCannotUse(t *testing.T) {
	for _, c := range []struct{ yaml, env, want string }{
		{yaml: "reasoning_effort:\n  analyze: hgih\n", want: `reasoning_effort.analyze (DEEP_RESEARCH_REASONING_EFFORT_ANALYZE): "hgih"`},
		{yaml: "reasoning_effort:\n  analyse: low\n", want: "reasoning_effort.analyse: no such phase"},
		{yaml: "reasoning_effort: low\n", want: "give a level per phase"},
		{env: "low", want: "give a level per phase"},
		{yaml: "reasoning_effort:\n  analyze: [high]\n", want: "reasoning_effort.analyze"},
		{yaml: "reasoning_effort:\n  analyze: 2\n", want: "reasoning_effort.analyze"},
		{yaml: "reasoning_effort: false\n", want: "want a level per phase"},
	} {
		clearProviderEnv(t)
		cfgDir := t.TempDir()
		t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
		writeFile(t, filepath.Join(cfgDir, "config.yaml"), c.yaml)
		t.Setenv("DEEP_RESEARCH_REASONING_EFFORT", c.env)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("yaml %q env %q: err = %v, want %q", c.yaml, c.env, err, c.want)
		}
	}
}

// sub_topics and follow_ups shape the plan, so they take all three channels
// every key does: the embedded default, config.yaml and the environment.
func TestBreadthAndFollowUpKeys(t *testing.T) {
	cfg := loadIn(t, "model_call_retries: 2\n")
	if cfg.SubTopics != 0 || cfg.NoFollowUps {
		t.Errorf("defaults: sub_topics %d, follow-ups off=%v; want 0 and on", cfg.SubTopics, cfg.NoFollowUps)
	}
	cfg = loadIn(t, "sub_topics: 5\nfollow_ups: false\n")
	if cfg.SubTopics != 5 || !cfg.NoFollowUps {
		t.Errorf("config.yaml: sub_topics %d, follow-ups off=%v; want 5 and off", cfg.SubTopics, cfg.NoFollowUps)
	}
	if got := loadIn(t, "sub_topics: -3\n").SubTopics; got != 0 {
		t.Errorf("negative sub_topics: got %d, want 0 (the planner chooses)", got)
	}
	t.Setenv("DEEP_RESEARCH_SUB_TOPICS", "2")
	t.Setenv("DEEP_RESEARCH_FOLLOW_UPS", "true")
	cfg = loadIn(t, "sub_topics: 5\nfollow_ups: false\n")
	if cfg.SubTopics != 2 || cfg.NoFollowUps {
		t.Errorf("env: sub_topics %d, follow-ups off=%v; want 2 and on", cfg.SubTopics, cfg.NoFollowUps)
	}
}

// The report keys take the three channels too, are stored normalized (the
// standard report is the zero spec whichever channel named it), and a
// misspelt format fails the load rather than a run.
func TestReportKeys(t *testing.T) {
	if got := loadIn(t, "model_call_retries: 2\n").Report; got != (agent.ReportSpec{}) {
		t.Errorf("defaults: report %+v, want the zero (standard) spec", got)
	}
	want := agent.ReportSpec{Format: "brief", Words: 400, Tables: "none"}
	if got := loadIn(t, "report_format: Brief\nreport_words: 400\nreport_tables: none\n").Report; got != want {
		t.Errorf("config.yaml: report %+v, want %+v", got, want)
	}
	t.Setenv("DEEP_RESEARCH_REPORT_FORMAT", "table")
	t.Setenv("DEEP_RESEARCH_REPORT_TABLES", "auto")
	if got := loadIn(t, "report_format: brief\nreport_tables: none\n").Report; got != (agent.ReportSpec{Format: "table"}) {
		t.Errorf("env: report %+v, want table with the format's tables", got)
	}
	t.Setenv("DEEP_RESEARCH_REPORT_FORMAT", "breif")
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "model_call_retries: 2\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "breif") {
		t.Errorf("a misspelt format loaded: %v", err)
	}
}

// instant is a config key like the others: false by default, set in the
// file, overridden from the environment; its answer call has its own
// reasoning effort.
func TestInstantComesFromFileAndEnv(t *testing.T) {
	if loadIn(t, "").Instant {
		t.Error("instant is on by default")
	}
	if !loadIn(t, "instant: true\n").Instant {
		t.Error("instant: true in config.yaml was not read")
	}
	clearProviderEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"), "instant: true\n")
	t.Setenv("DEEP_RESEARCH_INSTANT", "false")
	t.Setenv("DEEP_RESEARCH_REASONING_EFFORT_ANSWER", "low")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Instant || cfg.Effort.Answer != "low" {
		t.Errorf("env did not win: instant %v, answer effort %q", cfg.Instant, cfg.Effort.Answer)
	}
	if cfg.AnswererInstructions == "" {
		t.Error("the answerer has no instructions")
	}
}
