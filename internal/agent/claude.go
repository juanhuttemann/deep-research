package agent

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

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
	a.streamOpts = claudeStreamOpts
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
	if bin, _ := claudeCodeLogin(context.Background()); bin != "" {
		a.via = "claude-code"
		cc := &claudeCode{bin: bin, model: cfg.ClaudeModel, impl: a, api: api}
		return cc.newAgent, nil
	}
	if api == nil {
		return nil, errors.New("provider claude needs Claude Code signed in to a Claude subscription" +
			" (run `claude auth login`) or ANTHROPIC_API_KEY")
	}
	return api, nil
}

// unstreamedMaxTokens and streamedMaxTokens replace the framework's 4096,
// which cut a report off. An unstreamed call stays below what the SDK sends
// without streaming at all (about 21k tokens, its 10-minute guard).
const (
	unstreamedMaxTokens = 16000
	streamedMaxTokens   = 64000
)

// claudeStreamOpts give a streamed call room for the report and the thinking
// before it, and ask for the thinking's text: current models omit it unless
// asked, and the long wait then shows nothing.
//
// ponytail: adaptive thinking is what 4.6 and later models accept; earlier
// ones (Haiku 4.5) reject it, and need a model check to stream.
var claudeStreamOpts = []agent.Option{anthropicprovider.MessageNewParams(anthropic.MessageNewParams{
	MaxTokens: streamedMaxTokens,
	Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
		Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
	}},
})}

// anthropicAgents builds agents on the Messages API, paid with key.
func (a *impl) anthropicAgents(cfg Config, key string) newAgentFunc {
	opts := []aoption.RequestOption{
		aoption.WithAPIKey(key),
		aoption.WithBaseURL(cfg.AnthropicBaseURL),
		aoption.WithHTTPClient(providerHTTPClient()),
		// Retry policy lives in impl.call; see connect.
		aoption.WithMaxRetries(0),
		aoption.WithMiddleware(a.sniffServedModel),
	}
	if refusalFallback(cfg.AnthropicBaseURL, cfg.ClaudeModel) {
		// A request a safety classifier declines is answered by another model
		// within the same call, instead of costing the phase.
		opts = append(opts,
			aoption.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
			aoption.WithJSONSet("fallbacks", "default"))
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
