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
)

// Config holds all configuration for the deep research agent. The embedded
// agent.Config is the LLM/search half, passed straight to agent.New.
type Config struct {
	agent.Config
	DataFile string
	Offline  bool
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
}

// EnvTemplate is the default contents written to a root .env file by `init`.
// The entries are commented out on purpose so users fill in their own values,
// or export the matching environment variables for a session.
const EnvTemplate = `# Secrets / machine-specific overrides for deep-research.
# Fill in your values here, or export the matching env var for the session.
# Get a free OpenRouter key (no card): https://openrouter.ai/keys
# OPENAI_API_KEY=
# OPENAI_BASE_URL=https://openrouter.ai/api/v1
# OPENAI_MODEL=openrouter/free
# SEARXNG_URL=http://localhost:8888
# FIRECRAWL_URL=http://localhost:3002
# FIRECRAWL_API_KEY=
`

// DefaultModel is the model a fresh checkout uses.
const DefaultModel = "openrouter/free"

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

	// config.yaml / agent.yaml live in the config dir and are user-editable;
	// only write them when missing so we never clobber existing config.
	//
	// The list is a slice, not a map: ranging a map gave "wrote 2 file(s)" a
	// different order on every run, so two identical inits disagreed about
	// what they had done.
	for _, f := range []struct{ name, content string }{
		{"config.yaml", embeddedConfig("config.yaml")},
		{"agent.yaml", embeddedConfig("agent.yaml")},
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
	} else if werr := os.WriteFile(envPath, []byte(EnvTemplate), 0o644); werr != nil {
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
	for _, f := range []struct{ path, content string }{
		{filepath.Join(dir, "docker-compose.yml"), dockerCompose},
		{filepath.Join(dir, "searxng", "settings.yml"), fmt.Sprintf(searxngSettings, hex.EncodeToString(secret))},
	} {
		if _, err := os.Stat(f.path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return created, err
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return created, err
		}
		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
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
// environment variables win over the config file, built-in defaults win
// over everything.
func applyEnvDefaults(cfg *Config, v *viper.Viper) {
	// Every setting resolves the same way: the config file holds it, the
	// environment overrides it, a built-in default fills the gap. The provider
	// settings used to skip the file entirely, so an openai_model: written in
	// the one place every other setting lives was silently ignored.
	pick := func(key, fallback string, envNames ...string) string {
		if v := strings.TrimSpace(firstEnv(envNames...)); v != "" {
			return v
		}
		if v := strings.TrimSpace(v.GetString(key)); v != "" {
			return v
		}
		return fallback
	}
	// The API key is a secret and belongs in .env or the environment; it is
	// still read from the file for symmetry, with no default.
	cfg.OpenAIAPIKey = pick("openai_api_key", "", "OPENAI_API_KEY", "OPENROUTER_API_KEY")
	cfg.OpenAIBaseURL = pick("openai_base_url", "https://openrouter.ai/api/v1",
		"OPENAI_BASE_URL", "OPENROUTER_BASE_URL")
	// OpenRouter's free router, not a pinned ":free" id: pinned free models
	// are retired regularly, and the first run should cost nothing.
	cfg.OpenAIModel = pick("openai_model", DefaultModel,
		"OPENAI_MODEL", "OPENROUTER_MODEL")
	// Public instances out of the box ("auto"); "off" is the model-as-search
	// mode that used to be the default, kept for runs that want no search.
	cfg.SearXNGURL = pick("searxng_url", "auto", "SEARXNG_URL")
	if strings.EqualFold(cfg.SearXNGURL, "off") || strings.EqualFold(cfg.SearXNGURL, "none") {
		cfg.SearXNGURL = ""
	}
	cfg.FirecrawlURL = pick("firecrawl_url", "", "FIRECRAWL_URL")
	cfg.FirecrawlAPIKey = pick("firecrawl_api_key", "", "FIRECRAWL_API_KEY")
}

// Load resolves configuration via the Dirs chain.
func Load() (Config, error) {
	dirs := Dirs()
	if err := loadDotEnv(dirs); err != nil {
		return Config{}, err
	}

	cfgYAML, err := readFile(dirs, "config.yaml", embeddedConfig("config.yaml"))
	if err != nil {
		return Config{}, err
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(cfgYAML)); err != nil {
		return Config{}, fmt.Errorf("read config.yaml: %w", err)
	}
	// Every config.yaml key can also be set as DEEP_RESEARCH_<KEY>, e.g.
	// DEEP_RESEARCH_OFFLINE=true or DEEP_RESEARCH_SOURCES_PER_TOPIC=9. This
	// is documented in the README's environment table and in config.yaml:
	// an override channel nobody has written down is indistinguishable from
	// the tool ignoring its own config file.
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("data_file", "~/.deep-research/research.jsonl")
	v.SetDefault("offline", false)
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
		Offline:         v.GetBool("offline"),
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

	applyEnvDefaults(&cfg, v)

	agentYAML, err := readFile(dirs, "agent.yaml", embeddedConfig("agent.yaml"))
	if err != nil {
		return Config{}, err
	}
	a := viper.New()
	a.SetConfigType("yaml")
	if err := a.ReadConfig(strings.NewReader(agentYAML)); err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}
	if err := loadAgentConfig(&cfg, a); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func loadAgentConfig(cfg *Config, a *viper.Viper) error {
	embedded := viper.New()
	embedded.SetConfigType("yaml")
	if err := embedded.ReadConfig(strings.NewReader(embeddedConfig("agent.yaml"))); err != nil {
		return fmt.Errorf("read embedded agent.yaml: %w", err)
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

func readFile(dirs []string, name, embedded string) (string, error) {
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, name))
		if err == nil {
			return string(b), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return embedded, nil
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
