package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
)

// Service names: each one's auth.json entry, and its engine or scraper name.
const (
	ServiceExa       = "exa"
	ServiceTavily    = "tavily"
	ServiceFirecrawl = "firecrawl"
)

// SearchSettings are the config.yaml keys `drsh setup` writes. Engine is
// searxng, exa, tavily or off; SearXNGURL "auto" or one or more URLs;
// Scraper none, firecrawl, exa or tavily, or "" when nothing chose one.
type SearchSettings struct {
	Engine, SearXNGURL, Scraper, FirecrawlURL string
}

// Search is the search a run on c uses, in the words setup writes. An empty
// scraper with a Firecrawl address is Firecrawl, as the run reads it.
func (c Config) Search() SearchSettings {
	s := SearchSettings{Engine: strings.ToLower(c.SearchEngine), SearXNGURL: c.SearXNGURL,
		Scraper: strings.ToLower(c.Scraper), FirecrawlURL: c.FirecrawlURL}
	if s.Engine == "" {
		s.Engine = "searxng"
	}
	// searxng_url: off is how search was turned off before search_engine.
	if s.Engine == "searxng" && s.SearXNGURL == "" {
		s.Engine, s.SearXNGURL = "off", "auto"
	}
	if s.Scraper == "" && s.FirecrawlURL != "" {
		s.Scraper = ServiceFirecrawl
	}
	return s
}

// searchSetting is one key setup writes, with the environment variables that
// override it, in the order a run reads them.
type searchSetting struct {
	key string
	env []string
	get func(SearchSettings) string
	// used reports whether the setting matters to s: a SEARXNG_URL left in
	// .env overrides nothing once Exa is the engine.
	used func(SearchSettings) bool
}

var searchSettings = []searchSetting{
	{"search_engine", nil, func(s SearchSettings) string { return s.Engine },
		func(SearchSettings) bool { return true }},
	{"searxng_url", []string{"SEARXNG_URL"}, func(s SearchSettings) string { return s.SearXNGURL },
		func(s SearchSettings) bool { return s.Engine == "searxng" }},
	{"scraper", nil, func(s SearchSettings) string { return s.Scraper },
		func(s SearchSettings) bool { return s.Engine != "off" }},
	{"firecrawl_url", []string{"FIRECRAWL_URL"}, func(s SearchSettings) string { return s.FirecrawlURL },
		func(s SearchSettings) bool { return s.Engine != "off" && s.Scraper == ServiceFirecrawl }},
}

// envNames is the variables that set key, in the order pick reads them.
func envNames(key string, names ...string) []string {
	return append(append([]string(nil), names...), EnvPrefix+"_"+strings.ToUpper(key))
}

// firstSet is the variable among names a run takes key's value from, if any.
func firstSet(key string, names ...string) (name, value string, ok bool) {
	for _, n := range envNames(key, names...) {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return n, v, true
		}
	}
	return "", "", false
}

// ServiceFallbackKey is the named service's key from the environment or
// config.yaml, the one used when no stored key is for its address.
func (c Config) ServiceFallbackKey(name string) string { return c.serviceFallback[name] }

// WithSearch is c as a run would resolve it once s is saved: a setting the
// environment sets keeps the environment's value, and one s leaves empty
// keeps c's. keys are keys typed in setup, by service, each with the address
// it was typed for; a service without one for the address it now has gets
// its stored key there, else the environment's or config.yaml's.
func (c Config) WithSearch(s SearchSettings, keys map[string]auth.Credential, store *auth.Store) (Config, error) {
	for _, st := range searchSettings {
		if _, _, ok := firstSet(st.key, st.env...); ok {
			continue
		}
		v := st.get(s)
		if v == "" {
			continue
		}
		switch st.key {
		case "search_engine":
			c.SearchEngine = v
		case "searxng_url":
			c.SearXNGURL = v
		case "scraper":
			c.Scraper = v
		case "firecrawl_url":
			c.FirecrawlURL = v
		}
	}
	if strings.EqualFold(c.SearXNGURL, "off") {
		c.SearXNGURL = ""
	}
	for _, sv := range c.serviceSlots() {
		*sv.dst = c.serviceFallback[sv.name]
		if !c.usesService(sv.name) {
			continue
		}
		key, err := draftKey(sv, keys[sv.name].Key, keys[sv.name].BaseURL, store)
		if err != nil {
			return c, err
		}
		if key != "" {
			*sv.dst = key
		}
	}
	return c, nil
}

// draftKey is the key a service gets in a draft: the one typed in setup when
// it was typed for the address the service now has, else the stored one for
// that address. A FIRECRAWL_URL in the environment keeps the address, and a
// hosted key typed in setup is not sent there. A "!command" is run, as a run
// would run it.
func draftKey(sv serviceSlot, typed, typedFor string, store *auth.Store) (string, error) {
	if typed != "" && auth.SameEndpoint(typedFor, sv.baseURL) {
		key, err := auth.Resolve(typed)
		if err != nil {
			return "", fmt.Errorf("%s key: %w", sv.name, err)
		}
		return key, nil
	}
	return store.ServiceKey(sv.name, sv.baseURL)
}

// serviceSlot is one search service's key in a Config, and the address it
// goes to.
type serviceSlot struct {
	name, baseURL string
	dst           *string
}

func (c *Config) serviceSlots() []serviceSlot {
	return []serviceSlot{
		{ServiceExa, agent.ExaURL, &c.ExaAPIKey},
		{ServiceTavily, agent.TavilyURL, &c.TavilyAPIKey},
		{ServiceFirecrawl, c.FirecrawlURL, &c.FirecrawlAPIKey},
	}
}

// usesService reports whether a run on c sends anything to the named
// service. A stored key of a service it does not use is never resolved: a
// password manager locked on the Tavily entry must not stop a run that
// searches with SearXNG.
func (c Config) usesService(name string) bool {
	s := c.Search()
	return s.Engine != "off" && (s.Engine == name || s.Scraper == name)
}

// Override is an environment variable that wins over what setup saves.
type Override struct {
	Setting, Var, Value string
	// Source is where the variable was set: "your shell", or the .env file.
	Source string
}

// SearchOverrides lists the variables that will override s once it is saved,
// each only when it sets a value s does not have.
func SearchOverrides(s SearchSettings) []Override {
	var out []Override
	for _, st := range searchSettings {
		if !st.used(s) {
			continue
		}
		name, v, ok := firstSet(st.key, st.env...)
		if ok && !strings.EqualFold(normalSetting(v), normalSetting(st.get(s))) {
			out = append(out, Override{Setting: st.key, Var: name, Value: v, Source: envSource(name)})
		}
	}
	return out
}

// ModelOverrides lists the variables that will override sel once it is
// saved: the provider, the endpoint or the model.
func ModelOverrides(sel auth.Selection) []Override {
	type want struct {
		key, value string
		env        []string
		same       func(a, b string) bool
	}
	eq := func(a, b string) bool { return strings.EqualFold(a, b) }
	same := func(a, b string) bool { return auth.SameEndpoint(a, b) }
	wants := []want{{"provider", sel.Transport, nil, eq}}
	if sel.Transport == "claude" {
		wants = append(wants, want{"claude_model", sel.Model, nil, eq},
			want{"anthropic_base_url", sel.BaseURL, []string{"ANTHROPIC_BASE_URL"}, same})
	} else {
		wants = append(wants, want{"openai_model", sel.Model, []string{"OPENAI_MODEL", "OPENROUTER_MODEL"}, eq},
			want{"openai_base_url", sel.BaseURL, []string{"OPENAI_BASE_URL", "OPENROUTER_BASE_URL"}, same})
	}
	var out []Override
	for _, w := range wants {
		if name, v, ok := firstSet(w.key, w.env...); ok && !w.same(v, w.value) {
			out = append(out, Override{Setting: w.key, Var: name, Value: v, Source: envSource(name)})
		}
	}
	return out
}

// normalSetting compares search settings the way a run reads them.
func normalSetting(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if strings.EqualFold(v, "none") {
		return "off"
	}
	return v
}

// startEnv is the environment the process started with, before a .env was
// loaded into it. Once loaded, a .env's variables look like the shell's, and
// setup has to say which file to edit.
var startEnv = func() map[string]bool {
	m := map[string]bool{}
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			m[k] = true
		}
	}
	return m
}()

// envSource says where the variable name was set.
func envSource(name string) string {
	if startEnv[name] {
		return "your shell"
	}
	for _, d := range Dirs() {
		p := filepath.Join(d, ".env")
		if vars, err := godotenv.Read(p); err == nil && vars[name] != "" {
			return p
		}
	}
	return "the environment"
}

// PendingFile is a file ready to be written: its whole content was built and
// checked before anything is written, so a setting that cannot be saved is
// found before another file has changed.
type PendingFile struct {
	Path string
	data []byte
	mode fs.FileMode
}

// Write replaces the file atomically, keeping its mode.
func (p PendingFile) Write() error {
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p.Path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	_, err = tmp.Write(p.data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), p.mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.Path)
}

// SearchFile is dir's config.yaml with s written into it. Only the keys
// setup owns change: every other line, comment and blank line is kept. A
// missing file starts from the default one, whose commented-out examples
// become the settings.
func SearchFile(dir string, s SearchSettings) (PendingFile, error) {
	p := PendingFile{Path: filepath.Join(dir, "config.yaml"), mode: 0o644}
	content := embeddedConfig("config.yaml")
	b, err := os.ReadFile(p.Path)
	switch {
	case err == nil:
		content = string(b)
		if fi, err := os.Stat(p.Path); err == nil {
			p.mode = fi.Mode().Perm()
		}
	case !errors.Is(err, fs.ErrNotExist):
		return p, err
	}
	if err := parseYAML(content); err != nil {
		return p, fmt.Errorf("%s does not parse (%w); fix it, then run drsh setup again", p.Path, err)
	}
	var kv [][2]string
	for _, st := range searchSettings {
		if v := st.get(s); v != "" {
			kv = append(kv, [2]string{st.key, v})
		}
	}
	out, err := patchYAML(content, kv)
	if err != nil {
		return p, fmt.Errorf("%s: %w", p.Path, err)
	}
	p.data = []byte(out)
	return p, nil
}

func parseYAML(content string) error {
	v := viper.New()
	v.SetConfigType("yaml")
	return v.ReadConfig(strings.NewReader(content))
}

// patchYAML sets each top-level key of kv in content: in place on the line
// that sets it, else on its commented-out example, else at the end. The
// result is read back, so a line this got wrong is an error, not a setting
// quietly lost.
func patchYAML(content string, kv [][2]string) (string, error) {
	lines := strings.Split(content, "\n")
	for _, p := range kv {
		// JSON strings are YAML double-quoted strings, so "off" stays a
		// string and a URL with a # in it stays whole.
		q, _ := json.Marshal(p[1])
		line := p[0] + ": " + string(q)
		switch i := keyLine(lines, p[0]); {
		case i >= 0:
			lines[i] = line
		default:
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			lines = append(lines, line, "")
		}
	}
	out := strings.Join(lines, "\n")
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(out)); err != nil {
		return "", fmt.Errorf("would not parse once changed: %w", err)
	}
	for _, p := range kv {
		if got := v.GetString(p[0]); got != p[1] {
			return "", fmt.Errorf("%s would read back as %q, not %q", p[0], got, p[1])
		}
	}
	return out, nil
}

// keyLine is the line that sets key at the top level, else the first
// commented-out example of it ("# searxng_url: ..."), else -1.
func keyLine(lines []string, key string) int {
	set := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*:`)
	example := regexp.MustCompile(`^#\s*` + regexp.QuoteMeta(key) + `\s*:`)
	for _, re := range []*regexp.Regexp{set, example} {
		for i, l := range lines {
			if re.MatchString(l) {
				return i
			}
		}
	}
	return -1
}
