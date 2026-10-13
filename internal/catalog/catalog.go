// Package catalog lists the providers and models `deep-research login` offers.
//
// The data is Catwalk's (https://github.com/charmbracelet/catwalk, MIT), the
// community catalog Crush reads: endpoints, key variable names, models with
// their context window and price. A snapshot filtered to the providers
// deep-research can drive is embedded, so the picker works offline; Refresh
// replaces it with the live list when the network allows. `make catalog`
// updates the snapshot.
package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// URL is Catwalk's provider list.
const URL = "https://catwalk.charm.land/v2/providers"

//go:embed catwalk.json
var snapshot []byte

// Transports deep-research has a client for.
const (
	TransportOpenAI = "openai"
	TransportClaude = "claude"
)

// IDs of the entries this package adds to Catwalk's.
const (
	ClaudeSubscription = "claude-subscription"
	Custom             = "custom"
	AnthropicID        = "anthropic"
	OpenRouterID       = "openrouter"
)

// Endpoints Catwalk leaves as "$..._ENDPOINT" placeholders.
const (
	OpenAIBaseURL    = "https://api.openai.com/v1"
	AnthropicBaseURL = "https://api.anthropic.com"
)

// Provider is one entry in the picker.
type Provider struct {
	ID           string
	Name         string
	Transport    string
	BaseURL      string
	DefaultModel string
	Models       []Model
	// NoKey marks an entry that runs without a key (Claude Code).
	NoKey bool
}

// Model is one model a provider serves. Prices are dollars per million
// tokens; zero is free or unknown, which Catwalk does not distinguish.
type Model struct {
	ID            string
	Name          string
	ContextWindow int
	CostIn        float64
	CostOut       float64
}

// catwalkProvider is the subset of Catwalk's schema this package reads.
type catwalkProvider struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	APIEndpoint  string         `json:"api_endpoint"`
	APIKey       *string        `json:"api_key"`
	DefaultModel string         `json:"default_large_model_id"`
	Models       []catwalkModel `json:"models"`
}

type catwalkModel struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	CostIn        float64 `json:"cost_per_1m_in"`
	CostOut       float64 `json:"cost_per_1m_out"`
	ContextWindow int     `json:"context_window"`
}

// transportOf maps a Catwalk provider type to the client that speaks it.
// Google, Azure, Bedrock and Vertex have APIs of their own; they are left out
// rather than listed and failing on the first call.
func transportOf(typ string) string {
	switch typ {
	case "openai", "openai-compat", "openrouter", "vercel":
		return TransportOpenAI
	case "anthropic":
		return TransportClaude
	}
	return ""
}

// Parse reads Catwalk's JSON into the providers deep-research can drive,
// with this package's own entries first.
func Parse(b []byte) ([]Provider, error) {
	var raw []catwalkProvider
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	out := []Provider{claudeSubscription()}
	for _, p := range raw {
		if pr, ok := convert(p); ok {
			out = append(out, pr)
		}
	}
	return append(out, custom()), nil
}

func convert(p catwalkProvider) (Provider, bool) {
	t := transportOf(p.Type)
	// No api_key is an OAuth-only provider (Copilot): no key to paste.
	if t == "" || p.APIKey == nil || len(p.Models) == 0 {
		return Provider{}, false
	}
	pr := Provider{
		ID: p.ID, Name: p.Name, Transport: t,
		BaseURL:      endpoint(p.APIEndpoint, t),
		DefaultModel: p.DefaultModel,
	}
	for _, m := range p.Models {
		pr.Models = append(pr.Models, Model{ID: m.ID, Name: m.Name, ContextWindow: m.ContextWindow, CostIn: m.CostIn, CostOut: m.CostOut})
	}
	if p.ID == OpenRouterID {
		// The free router is deep-research's default: a first run that costs
		// nothing. Catwalk lists paid models only.
		pr.Models = append([]Model{{ID: "openrouter/free", Name: "OpenRouter Free (routes to a free model)"}}, pr.Models...)
		pr.DefaultModel = "openrouter/free"
	}
	return pr, true
}

// endpoint resolves Catwalk's "$OPENAI_API_ENDPOINT"-style placeholders,
// which Crush fills from the environment, to the provider's public URL.
func endpoint(e, transport string) string {
	if e != "" && !strings.HasPrefix(e, "$") {
		return e
	}
	if transport == TransportClaude {
		return AnthropicBaseURL
	}
	return OpenAIBaseURL
}

func claudeSubscription() Provider {
	return Provider{
		ID: ClaudeSubscription, Name: "Claude subscription",
		Transport: TransportClaude, BaseURL: AnthropicBaseURL, NoKey: true,
		DefaultModel: "claude-opus-5-5",
	}
}

func custom() Provider {
	return Provider{
		ID: Custom, Name: "Custom endpoint",
		Transport: TransportOpenAI,
	}
}

// Find returns the provider with id.
func Find(ps []Provider, id string) (Provider, bool) {
	i := slices.IndexFunc(ps, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		return Provider{}, false
	}
	return ps[i], true
}

// Embedded is the snapshot built into the binary.
func Embedded() []Provider {
	ps, err := Parse(snapshot)
	if err != nil {
		panic(err) // the snapshot is tested; a bad one is a build error
	}
	// The subscription's models are Anthropic's.
	if a, ok := Find(ps, AnthropicID); ok {
		ps[0].Models = a.Models
	}
	return ps
}

// fetchTimeout keeps an unreachable catalog from holding up the picker.
const fetchTimeout = 3 * time.Second

// Load returns the freshest catalog it can: Catwalk's live list (cached in
// cacheDir and revalidated by ETag), the cached copy when Catwalk does not
// answer, the embedded snapshot when there is no cache. It never fails.
func Load(ctx context.Context, client *http.Client, url, cacheDir string) []Provider {
	b, err := refresh(ctx, client, url, cacheDir)
	if err != nil {
		b, _ = os.ReadFile(filepath.Join(cacheDir, cacheFile))
	}
	if len(b) == 0 {
		return Embedded()
	}
	ps, err := Parse(b)
	if err != nil || len(ps) <= 2 {
		return Embedded()
	}
	if a, ok := Find(ps, AnthropicID); ok {
		ps[0].Models = a.Models
	}
	return ps
}

const (
	cacheFile = "catalog.json"
	etagFile  = "catalog.etag"
)

// refresh fetches url, sending the cached ETag; a 304 returns the cached
// body. A fresh body is written to the cache.
func refresh(ctx context.Context, client *http.Client, url, cacheDir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	cached, _ := os.ReadFile(filepath.Join(cacheDir, cacheFile))
	if etag, _ := os.ReadFile(filepath.Join(cacheDir, etagFile)); len(etag) > 0 && len(cached) > 0 {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return cached, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("catalog: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if _, err := Parse(b); err != nil {
		return nil, errors.New("catalog: unreadable response")
	}
	if os.MkdirAll(cacheDir, 0o755) == nil {
		_ = os.WriteFile(filepath.Join(cacheDir, cacheFile), b, 0o644)
		_ = os.WriteFile(filepath.Join(cacheDir, etagFile), []byte(resp.Header.Get("ETag")), 0o644)
	}
	return b, nil
}

// CacheDir is where Load keeps the live catalog.
func CacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "deep-research")
	}
	return filepath.Join(d, "deep-research")
}
