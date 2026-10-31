package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/anthropics/anthropic-sdk-go"
	aoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/anthropicprovider"
	"github.com/microsoft/agent-framework-go/tool"
)

// connectClaude picks how provider claude reaches the model: the user's own
// Claude Code sign-in first, ANTHROPIC_API_KEY second. withTools rules the
// subscription out: deep-research's tools are Go functions a claude -p
// process cannot call.
func (a *impl) connectClaude(cfg Config, withTools bool) (newAgentFunc, error) {
	a.model, a.baseURL = cfg.ClaudeModel, cfg.AnthropicBaseURL
	a.keyEnv, a.urlEnv = "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"
	// No JSON mode: the framework turns a schemaless one into an output format
	// without a schema, which the Messages API rejects. The prompts ask for
	// JSON and jsonPayloads repairs it, as it does for the analysis.
	a.params, a.mapEffort = claudeParams, claudeEffort
	var api newAgentFunc
	if key := strings.TrimSpace(cfg.AnthropicAPIKey); key != "" {
		api = a.anthropicAgents(cfg, key)
	}
	if withTools {
		if api == nil {
			return nil, errors.New("questions about a run need ANTHROPIC_API_KEY with provider claude:" +
				" Claude Code cannot call deep-research's tools")
		}
		return api, nil
	}
	if useClaudeCode(cfg) {
		bin, _, err := claudeCodeLogin(context.Background())
		if err != nil && api != nil {
			a.log(fmt.Sprintf("Claude Code's sign-in could not be read (%v); using the API key instead", err))
		}
		if bin != "" {
			a.via = "claude-code"
			cc := &claudeCode{bin: bin, model: cfg.ClaudeModel, impl: a, api: api}
			return cc.newAgent, nil
		}
	}
	if api == nil && !useClaudeCode(cfg) {
		return nil, fmt.Errorf("no API key for %s (run `drsh setup`, or set ANTHROPIC_API_KEY)", SafeEndpoint(cfg.AnthropicBaseURL))
	}
	if api == nil {
		return nil, errors.New("provider claude needs Claude Code signed in to a Claude subscription" +
			" (run `claude auth login`) or ANTHROPIC_API_KEY")
	}
	return api, nil
}

// useClaudeCode reports whether a run may go through the user's Claude Code
// sign-in: not when the API was chosen over the subscription, nor for an
// Anthropic-compatible provider (Kimi, MiniMax) — claude -p talks to
// Anthropic, so a Kimi model picked by name would be answered by Claude, or
// refused. `drsh login` sets ClaudeAPIOnly for both.
func useClaudeCode(cfg Config) bool { return !cfg.ClaudeAPIOnly }

// unstreamedMaxTokens and streamedMaxTokens replace the framework's 4096,
// which cut a report off. An unstreamed call stays below what the SDK sends
// without streaming at all (about 21k tokens, its 10-minute guard).
const (
	unstreamedMaxTokens = 16000
	streamedMaxTokens   = 64000
)

// anthropicAgents builds agents on the Messages API, paid with key.
func (a *impl) anthropicAgents(cfg Config, key string) newAgentFunc {
	opts := []aoption.RequestOption{
		aoption.WithAPIKey(key),
		aoption.WithBaseURL(cfg.AnthropicBaseURL),
		aoption.WithHTTPClient(providerHTTPClient()),
		// Retry policy lives in impl.call; see connect.
		aoption.WithMaxRetries(0),
		aoption.WithMiddleware(a.effortFallback, a.sniffServedModel),
	}
	if refusalFallback(cfg.AnthropicBaseURL, cfg.ClaudeModel) {
		opts = append(opts, refusalFallbackOpts()...)
	}
	client := anthropic.NewClient(opts...)
	unstreamed := anthropicprovider.MessageNewParams(anthropic.MessageNewParams{MaxTokens: unstreamedMaxTokens})
	return func(name, instructions string, tools []tool.Tool) *agent.Agent {
		return anthropicprovider.NewAgent(client, anthropicprovider.AgentConfig{
			Model:        cfg.ClaudeModel,
			Instructions: instructions,
			Config:       agent.Config{Name: name, Tools: tools, RunOptions: []agent.Option{unstreamed}},
		})
	}
}

// fallbackBeta is the beta that takes fallbacks: "default".
const fallbackBeta = "server-side-fallback-2026-07-01"

// refusalFallbackOpts ask for server-side refusal fallback: a request a safety
// classifier declines is answered by another model within the same call,
// instead of costing the phase.
func refusalFallbackOpts() []aoption.RequestOption {
	var refused atomic.Bool
	return []aoption.RequestOption{
		aoption.WithHeaderAdd("anthropic-beta", fallbackBeta),
		aoption.WithJSONSet("fallbacks", "default"),
		aoption.WithMiddleware(func(req *http.Request, next aoption.MiddlewareNext) (*http.Response, error) {
			return dropRefusedFallback(&refused, req, next)
		}),
	}
}

// dropRefusedFallback sends req without the fallback once the API has refused
// it. An account with no default fallback configured, or a model or beta that
// does not take it, answers every request with a 400 that a retry repeats, so
// what was meant to save a declined phase failed every phase. Claude Code
// drops it the same way.
func dropRefusedFallback(refused *atomic.Bool, req *http.Request, next aoption.MiddlewareNext) (*http.Response, error) {
	if !refused.Load() {
		resp, err := next(req)
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			return resp, err
		}
		msg, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(msg))
		if !bytes.Contains(msg, []byte("fallback")) {
			return resp, nil
		}
		refused.Store(true)
	}
	if err := withoutFallback(req); err != nil {
		return nil, err
	}
	return next(req)
}

// withoutFallback takes the fallbacks field and its beta out of req.
func withoutFallback(req *http.Request) error {
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&fields); err != nil {
		return err
	}
	delete(fields, "fallbacks")
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	req.Body, _ = req.GetBody()
	req.ContentLength = int64(len(raw))
	betas := slices.DeleteFunc(req.Header.Values("anthropic-beta"), func(b string) bool { return b == fallbackBeta })
	req.Header.Del("anthropic-beta")
	for _, b := range betas {
		req.Header.Add("anthropic-beta", b)
	}
	return nil
}

// fallbackModels take server-side refusal fallback.
//
// ponytail: listed by hand; a model released later needs adding here.
var fallbackModels = []string{"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5", "claude-fable-5-1"}

// refusalFallback reports whether requests may ask for it: on the Claude API
// only, and for those models only; anywhere else the field is an error.
func refusalFallback(baseURL, model string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && u.Hostname() == "api.anthropic.com" && slices.Contains(fallbackModels, model)
}
