package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	embedcfg "github.com/juanhuttemann/deep-research/config"
	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
)

// Config holds all configuration for the deep research agent. The embedded
// agent.Config is the LLM/search half, passed straight to agent.New.
type Config struct {
	agent.Config
	DataFile string
	// RunTimeout is the deadline for a whole run. It covers searching and
	// scraping, which dominate wall-clock time and are not model calls, so it
	// is its own setting rather than a multiple of ModelCallTimeout.
	RunTimeout      time.Duration
	SourcesPerTopic int // sources per sub-agent; 0 => the --mode tier decides
	// Parallelism is how many sub-agents search at once. Every other budget
	// in the tool is configurable; this one used to be a literal 3 in the UI,
	// so a deep run with six sub-topics could not be widened or, on a rate-
	// limited provider, narrowed.
	Parallelism int
	// fallback is the key from the environment or config.yaml for each
	// transport, with the endpoint it goes with: the one the environment and
	// config.yaml name, before `deep-research login`'s choice. See
	// FallbackKey.
	fallback map[string]fallbackKey
}

type fallbackKey struct{ baseURL, key, source string }

// FallbackKey returns the environment's (or config.yaml's) key for a
// transport ("openai", "claude") and the endpoint it is for. That key is
// only ever sent there: OPENAI_API_KEY set for OpenRouter is no key for
// DeepSeek, which login then asks for.
func (c Config) FallbackKey(transport string) (baseURL, key string) {
	f := c.fallback[transport]
	return f.baseURL, f.key
}

// EnvTemplate is the default contents written to a root .env file by `init`.
// The entries are commented out on purpose so users fill in their own values,
// or export the matching environment variables for a session.
const EnvTemplate = `# Secrets / machine-specific overrides for deep-research.
# The easy way: run "deep-research login" to pick a provider and model and store
# the key (auth.json, readable only by you). The keys below are the fallback;
# the provider and model settings below override what login picked.
# Get a free OpenRouter key (no card): https://openrouter.ai/keys
# OPENAI_API_KEY=
# OPENAI_BASE_URL=https://openrouter.ai/api/v1
# OPENAI_MODEL=openrouter/free
# Or Claude: your subscription via Claude Code (claude auth login), with this
# key as the fallback.
# DEEP_RESEARCH_PROVIDER=claude
# ANTHROPIC_API_KEY=
# SEARXNG_URL=http://localhost:8888
# FIRECRAWL_URL=http://localhost:3002
# FIRECRAWL_API_KEY=
# Hosted search instead of SearXNG (page text comes with each result):
# DEEP_RESEARCH_SEARCH_ENGINE=exa
# EXA_API_KEY=
# DEEP_RESEARCH_SEARCH_ENGINE=tavily
# TAVILY_API_KEY=
`

// DefaultModel is the model a fresh checkout uses.
const DefaultModel = "openrouter/free"

// DefaultClaudeModel is the model provider claude uses unless told otherwise.
const DefaultClaudeModel = "claude-opus-5-5"

// EnvPrefix is the prefix for the per-key environment overrides: any
// config.yaml key resolves from DEEP_RESEARCH_<KEY> when that variable is set.
const EnvPrefix = "DEEP_RESEARCH"

// defaultParallelism is how many sub-agents search concurrently when nothing
// says otherwise. Three keeps a plan moving without flooding a search backend
// or a rate-limited model provider.
const defaultParallelism = 3

// Dirs returns the config directory chain.
func Dirs() []string {
	if d := os.Getenv("DEEP_RESEARCH_CONFIG_DIR"); d != "" {
		return []string{d}
	}
	dirs := []string{"config"}
	if ud, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(ud, "deep-research"))
	}
	return dirs
}

// Init writes the default config files. It returns the config directory and
// the list of files it actually created (relative to the current working
// directory), so callers can report honestly what happened.
func Init() (dir string, created []string, err error) {
	dir, err = writeDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}

	// config.yaml lives in the config dir and is user-editable; only write it
	// when missing so we never clobber existing config. agent.yaml is not
	// written: a copy froze the prompts at install time, so later releases'
	// prompt changes never reached it. The binary carries them, and an
	// agent.yaml a user creates overrides only the blocks it has.
	//
	// The list is a slice, not a map: ranging a map gave "wrote 2 file(s)" a
	// different order on every run, so two identical inits disagreed about
	// what they had done.
	for _, f := range []struct{ name, content string }{
		{"config.yaml", embeddedConfig("config.yaml")},
	} {
		p := filepath.Join(dir, f.name)
		if _, statErr := os.Stat(p); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", nil, statErr
		}
		if werr := os.WriteFile(p, []byte(f.content), 0o644); werr != nil {
			return "", nil, werr
		}
		created = append(created, p)
	}

	// The .env file holds secrets, so it is written into the working
	// directory (never inside the config dir).
	envPath := filepath.Join(".env")
	if _, statErr := os.Stat(envPath); statErr == nil {
		// existing .env left as-is
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", nil, statErr
	} else if werr := os.WriteFile(envPath, []byte(EnvTemplate), 0o600); werr != nil {
		return "", nil, werr
	} else {
		created = append(created, envPath)
	}

	return dir, created, nil
}

// dockerCompose runs SearXNG on localhost only: it is a search endpoint for
// this machine, not a public instance.
const dockerCompose = `# Local SearXNG for deep-research. Start it with: docker compose up -d
# Firecrawl (full page text) runs from its own repository: docs/services.md.
services:
  searxng:
    image: docker.io/searxng/searxng:latest
    container_name: searxng
    restart: unless-stopped
    ports:
      - "127.0.0.1:8888:8080"
    environment:
      # Without it the container takes ownership of ./searxng and later
      # edits to settings.yml need sudo.
      - FORCE_OWNERSHIP=false
    volumes:
      # :z relabels the mount for SELinux hosts (Fedora, RHEL), where the
      # container is otherwise denied its own settings file; hosts without
      # SELinux ignore it.
      - ./searxng:/etc/searxng:z
`

// searxngSettings enables the JSON API, which SearXNG ships disabled.
const searxngSettings = `use_default_settings: true

server:
  secret_key: "%s"

search:
  formats:
    - html
    - json
`

// InitDocker writes a docker-compose.yml and searxng/settings.yml into dir
// for a local SearXNG, returning the files it created. Existing files are
// left alone, so a second run changes nothing — including the secret.
func InitDocker(dir string) ([]string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	var created []string
	for _, f := range []struct {
		path, content string
		mode          fs.FileMode
	}{
		{filepath.Join(dir, "docker-compose.yml"), dockerCompose, 0o644},
		{filepath.Join(dir, "searxng", "settings.yml"), fmt.Sprintf(searxngSettings, hex.EncodeToString(secret)), 0o600},
	} {
		if _, err := os.Stat(f.path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return created, err
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return created, err
		}
		if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return created, err
		}
		created = append(created, f.path)
	}
	return created, nil
}

func writeDir() (string, error) {
	if dir := os.Getenv("DEEP_RESEARCH_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	for _, dir := range Dirs() {
		if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return UserDir()
}

// UserDir returns the per-user config directory.
func UserDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: user config dir: %w", err)
	}
	return filepath.Join(base, "deep-research"), nil
}

// firstEnv returns the first non-empty environment variable among names. The
// OPENAI_* names are canonical; the legacy OPENROUTER_* names are still read
// so existing .env files keep working.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// applyEnvDefaults fills in LLM provider and search-service settings:
// environment variables win over the selection saved by `deep-research
// login`, which wins over the config file; built-in defaults fill the gap.
func applyEnvDefaults(cfg *Config, v *viper.Viper, sel auth.Selection) {
	// Every setting resolves the same way: the environment, then the saved
	// selection, then the config file, then a built-in default. The provider
	// settings used to skip the file entirely, so an openai_model: written in
	// the one place every other setting lives was silently ignored.
	//
	// The environment is read here, not through viper: viper merges
	// DEEP_RESEARCH_<KEY> with the file, and the saved selection has to sit
	// between the two.
	pick := func(key, saved, fallback string, envNames ...string) string {
		envNames = append(envNames, EnvPrefix+"_"+strings.ToUpper(key))
		for _, s := range []string{firstEnv(envNames...), saved, v.GetString(key)} {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
		return fallback
	}
	openaiSel, claudeSel := savedFor(sel, "openai"), savedFor(sel, "claude")
	cfg.OpenAIBaseURL = pick("openai_base_url", openaiSel.BaseURL, "https://openrouter.ai/api/v1",
		"OPENAI_BASE_URL", "OPENROUTER_BASE_URL")
	// OpenRouter's free router, not a pinned ":free" id: pinned free models
	// are retired regularly, and the first run should cost nothing.
	cfg.OpenAIModel = pick("openai_model", openaiSel.Model, DefaultModel,
		"OPENAI_MODEL", "OPENROUTER_MODEL")
	// Claude is chosen, never inferred from which keys happen to be exported.
	// Its model has no bare ANTHROPIC_MODEL name: Claude Code reads that one,
	// often set to an alias the API does not know.
	cfg.Provider = strings.ToLower(pick("provider", sel.Transport, "openai"))
	cfg.ClaudeModel = pick("claude_model", claudeSel.Model, DefaultClaudeModel)
	cfg.AnthropicBaseURL = pick("anthropic_base_url", claudeSel.BaseURL, "https://api.anthropic.com", "ANTHROPIC_BASE_URL")
	// Where a key from the environment or config.yaml goes: the endpoint
	// they name, not the one login chose.
	cfg.fallback = map[string]fallbackKey{
		"openai": {baseURL: pick("openai_base_url", "", "https://openrouter.ai/api/v1", "OPENAI_BASE_URL", "OPENROUTER_BASE_URL")},
		"claude": {baseURL: pick("anthropic_base_url", "", "https://api.anthropic.com", "ANTHROPIC_BASE_URL")},
	}
	// Public instances out of the box ("auto"); "off" is the model-as-search
	// mode that used to be the default, kept for runs that want no search.
	cfg.SearXNGURL = pick("searxng_url", "", "auto", "SEARXNG_URL")
	if strings.EqualFold(cfg.SearXNGURL, "off") || strings.EqualFold(cfg.SearXNGURL, "none") {
		cfg.SearXNGURL = ""
	}
	cfg.FirecrawlURL = pick("firecrawl_url", "", "", "FIRECRAWL_URL")
	cfg.FirecrawlAPIKey = pick("firecrawl_api_key", "", "", "FIRECRAWL_API_KEY")
	// The engine is chosen, never inferred from an exported key: a key alone
	// would quietly send every query to a paid third party.
	cfg.SearchEngine = strings.ToLower(pick("search_engine", "", "searxng"))
	if cfg.SearchEngine == "none" {
		cfg.SearchEngine = "off"
	}
	cfg.Scraper = strings.ToLower(pick("scraper", "", ""))
	cfg.ExaAPIKey = pick("exa_api_key", "", "", "EXA_API_KEY")
	cfg.TavilyAPIKey = pick("tavily_api_key", "", "", "TAVILY_API_KEY")
}

// savedFor is the saved selection when it is for transport, else nothing:
// a saved Claude model is no OpenAI model.
func savedFor(sel auth.Selection, transport string) auth.Selection {
	if sel.Transport != transport {
		return auth.Selection{}
	}
	return sel
}

// applyKeys picks the key for the endpoint the settings resolved to: a key
// stored by `deep-research login` for that endpoint, else the environment's,
// else the config file's. Every key is bound to an endpoint: a stored one to
// the endpoint it was entered for, the environment's to the one the
// environment and config file name. So an OPENAI_BASE_URL pointed elsewhere
// for one run never receives the stored OpenRouter key, and a DeepSeek chosen
// in login never receives the OPENAI_API_KEY meant for OpenRouter.
func applyKeys(cfg *Config, v *viper.Viper, store *auth.Store, sel auth.Selection) error {
	cfg.fallback["openai"] = fallbackFor(cfg.fallback["openai"], v, "openai_api_key", "OPENAI_API_KEY", "OPENROUTER_API_KEY")
	cfg.fallback["claude"] = fallbackFor(cfg.fallback["claude"], v, "anthropic_api_key", "ANTHROPIC_API_KEY")
	base, transport := cfg.OpenAIBaseURL, "openai"
	if cfg.Provider == "claude" {
		base, transport = cfg.AnthropicBaseURL, "claude"
	}
	key, from, err := store.KeyFor(base, sel.Provider)
	if err != nil {
		return fmt.Errorf("stored key for %s in %s: %w", from, store.Path(), err)
	}
	source := agent.KeyFromLogin
	if fb := cfg.fallback[transport]; key == "" && auth.SameEndpoint(fb.baseURL, base) {
		key, source = fb.key, fb.source
	}
	if key == "" {
		source = ""
	}
	cfg.KeySource = source
	if cfg.Provider == "claude" {
		cfg.AnthropicAPIKey = key
		// The API was picked over the subscription: a run must not go
		// through Claude Code and bill the plan instead. The choice decides
		// it, not the endpoint: an ANTHROPIC_BASE_URL overriding it for a run
		// (a proxy) is still the API, and claude -p would ignore that URL.
		cfg.ClaudeAPIOnly = sel.Transport == "claude" && sel.Provider != catalogClaudeSubscription
		return nil
	}
	cfg.OpenAIAPIKey = key
	return nil
}

// fallbackFor fills in f's key: the environment's, else the config file's.
func fallbackFor(f fallbackKey, v *viper.Viper, keyName string, envNames ...string) fallbackKey {
	f.key, f.source = strings.TrimSpace(firstEnv(append(envNames, EnvPrefix+"_"+strings.ToUpper(keyName))...)), agent.KeyFromEnv
	if f.key == "" {
		// The API key is a secret and belongs in auth.json, .env or the
		// environment; it is still read from the file for symmetry.
		f.key, f.source = strings.TrimSpace(v.GetString(keyName)), agent.KeyFromConfig
	}
	return f
}

// catalogClaudeSubscription is catalog.ClaudeSubscription; config does not
// import the catalog for one string.
const catalogClaudeSubscription = "claude-subscription"

// AuthDir is where `deep-research login` keeps auth.json and model.json:
// DEEP_RESEARCH_CONFIG_DIR when set, else the per-user config directory.
// Never the project's config/ directory, which sits in a checkout.
func AuthDir() (string, error) {
	if d := os.Getenv("DEEP_RESEARCH_CONFIG_DIR"); d != "" {
		return d, nil
	}
	return UserDir()
}

// loadLogin reads what `deep-research login` saved. With no user config
// directory there is nothing saved, which is not an error.
func loadLogin() (*auth.Store, auth.Selection, error) {
	dir, err := AuthDir()
	if err != nil {
		return auth.Empty(), auth.Selection{}, nil
	}
	store, err := auth.Load(dir, os.Stderr)
	if err != nil {
		return nil, auth.Selection{}, err
	}
	sel, _, err := auth.LoadSelection(dir)
	return store, sel, err
}

// Load resolves configuration via the Dirs chain.
func Load() (Config, error) {
	dirs := Dirs()
	if err := loadDotEnv(dirs); err != nil {
		return Config{}, err
	}

	_, cfgYAML, err := readFile(dirs, "config.yaml", embeddedConfig("config.yaml"))
	if err != nil {
		return Config{}, err
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(cfgYAML)); err != nil {
		return Config{}, fmt.Errorf("read config.yaml: %w", err)
	}
	// Every config.yaml key can also be set as DEEP_RESEARCH_<KEY>, e.g.
	// DEEP_RESEARCH_SOURCES_PER_TOPIC=9. This is documented in the README's environment table and in config.yaml:
	// an override channel nobody has written down is indistinguishable from
	// the tool ignoring its own config file.
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("data_file", "~/.deep-research/research.jsonl")
	v.SetDefault("model_call_timeout", "120s")
	v.SetDefault("model_call_retries", 2)
	v.SetDefault("run_timeout", "30m")
	v.SetDefault("sources_per_topic", 0)
	v.SetDefault("parallelism", defaultParallelism)
	// The setting was originally spelled max_depth back when it capped a run's
	// total sources. Config files on disk still use that name, so it keeps
	// resolving to the same value.
	v.RegisterAlias("max_depth", "sources_per_topic")

	cfg := Config{
		DataFile:        v.GetString("data_file"),
		RunTimeout:      v.GetDuration("run_timeout"),
		SourcesPerTopic: v.GetInt("sources_per_topic"),
		Parallelism:     v.GetInt("parallelism"),
	}
	// A file or env var setting this to 0 or a negative number would
	// otherwise mean "no sub-agent runs at all".
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = defaultParallelism
	}
	cfg.ModelCallTimeout = v.GetDuration("model_call_timeout")
	cfg.ModelCallRetries = v.GetInt("model_call_retries")

	// Expand ~ in paths
	if strings.HasPrefix(cfg.DataFile, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("expand home: %w", err)
		}
		cfg.DataFile = filepath.Join(home, cfg.DataFile[1:])
	}

	store, sel, err := loadLogin()
	if err != nil {
		return Config{}, err
	}
	applyEnvDefaults(&cfg, v, sel)
	if err := applyKeys(&cfg, v, store, sel); err != nil {
		return Config{}, err
	}
	if cfg.Provider != "openai" && cfg.Provider != "claude" {
		return Config{}, fmt.Errorf("provider %q: want openai or claude (provider in config.yaml, or DEEP_RESEARCH_PROVIDER)", cfg.Provider)
	}

	agentPath, agentYAML, err := readFile(dirs, "agent.yaml", embeddedConfig("agent.yaml"))
	if err != nil {
		return Config{}, err
	}
	a := viper.New()
	a.SetConfigType("yaml")
	if err := a.ReadConfig(strings.NewReader(agentYAML)); err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}
	if err := loadAgentConfig(&cfg, a, agentPath); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadAgentConfig reads the prompts from a, the agent.yaml at path ("" for
// the embedded copy).
func loadAgentConfig(cfg *Config, a *viper.Viper, path string) error {
	embedded := viper.New()
	embedded.SetConfigType("yaml")
	if err := embedded.ReadConfig(strings.NewReader(embeddedConfig("agent.yaml"))); err != nil {
		return fmt.Errorf("read embedded agent.yaml: %w", err)
	}
	// The per-key fallback cannot catch a stale file: its old prompts are all
	// present, and they win. Only the version says they predate the parsers.
	// A missing key is not stale — it falls back to the current prompt.
	// The path is absolute because three directories can hold the file read.
	if v := embedded.GetInt("prompts_version"); a.GetInt("prompts_version") != v {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		_, _ = fmt.Fprintf(os.Stderr, "warning: %s is not prompts_version %d; its prompts may not produce what"+
			" this release parses — move it aside to use the built-in prompts, or update it from this"+
			" release's config/agent.yaml\n", path, v)
	}
	for _, in := range []struct {
		key string
		dst *string
	}{
		{"analyzer_instructions", &cfg.AnalyzerInstructions},
		{"fact_checker_instructions", &cfg.FactCheckerInstructions},
		{"summarizer_instructions", &cfg.SummarizerInstructions},
		{"search_instructions", &cfg.SearchInstructions},
		{"planner_instructions", &cfg.PlanningInstructions},
		{"chat_instructions", &cfg.ChatInstructions},
	} {
		s := strings.TrimRight(a.GetString(in.key), " \t\r\n")
		if s == "" {
			s = strings.TrimRight(embedded.GetString(in.key), " \t\r\n")
		}
		if s == "" {
			return fmt.Errorf("agent.yaml: missing required key %s", in.key)
		}
		*in.dst = s
	}
	return nil
}

// readFile returns the first dirs/name that exists, with its path so a
// warning about it can say which one was read; path is "" for embedded.
func readFile(dirs []string, name, embedded string) (path, content string, err error) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		b, err := os.ReadFile(p)
		if err == nil {
			return p, string(b), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
	}
	return "", embedded, nil
}

// embeddedConfig returns the bundled default config file by name.
func embeddedConfig(name string) string {
	b, err := embedcfg.ConfigFS.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

func loadDotEnv(dirs []string) error {
	paths := findDotEnv()
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, ".env"))
	}
	for _, p := range paths {
		if err := godotenv.Load(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("load %s: %w", p, err)
		}
	}
	return nil
}

// findDotEnv locates the project's .env by walking up from the working
// directory. It returns the first one it finds and never climbs past the
// project root (a directory holding .git or go.mod).
//
// Walking to the filesystem root instead loaded every ancestor's .env — an
// unrelated one in $HOME or / is common in development — and each could
// silently inject OPENAI_API_KEY, SEARXNG_URL and the rest into the run.
func findDotEnv() []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	for {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			return []string{p}
		}
		if isProjectRoot(dir) {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// isProjectRoot reports whether dir looks like the top of a checkout, which is
// as far up as .env discovery is allowed to go.
func isProjectRoot(dir string) bool {
	for _, marker := range []string{".git", "go.mod"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}
