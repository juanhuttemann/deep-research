package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/anthropicprovider"
	"github.com/microsoft/agent-framework-go/provider/openaiprovider"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// Effort is how hard each phase's model reasons before it answers, one level
// per phase; "" sends none and leaves it to the provider. Reasoning is most of
// a run's wall time (about 80% of every long call in the runs measured), and
// the phases do not need it alike: the analysis and the fact-check are what
// the answer rests on, the plan only writes search queries.
type Effort struct {
	Plan, Clarify, Search, Analyze, FactCheck, Summarize, Answer, Chat string
}

// EffortLevels are the levels a phase takes besides "default". Not every
// provider has every one: Gemini refuses "none" ("reasoning is mandatory"),
// Claude has no "none" or "minimal", and a model without reasoning ignores
// the field. What a provider refuses is handled per request (effortFallback).
var EffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ParseEffort normalizes a configured level: "" or "default" is "", which
// sends nothing.
func ParseEffort(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "default" {
		return "", nil
	}
	if !slices.Contains(EffortLevels, s) {
		return "", fmt.Errorf("%q is not an effort level (want default, %s)", s, strings.Join(EffortLevels, ", "))
	}
	return s, nil
}

// openAIParams are an OpenAI-compatible call's request parameters at effort.
// They are one option because the framework keeps only the last of a kind: a
// second ChatCompletionNewParams for the effort would drop include_usage and,
// with it, every streamed phase's tokens from the run total.
func openAIParams(effort string, streamed bool) []agent.Option {
	if !streamed && effort == "" {
		return nil
	}
	p := openai.ChatCompletionNewParams{ReasoningEffort: shared.ReasoningEffort(effort)}
	if streamed {
		// An OpenAI-compatible API reports tokens for a streamed call only when
		// stream_options.include_usage is set, so streaming without it would
		// quietly drop the streamed phases' tokens from the run total.
		p.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	}
	return []agent.Option{openaiprovider.ChatCompletionNewParams(p)}
}

// claudeParams are a Claude call's request parameters at effort, for the
// Messages API and for claude -p alike (claudeCode reads the effort from
// them), so a call handed to the API mid-run keeps its effort. A streamed
// call gets room for the report and the thinking before it, and asks for the
// thinking's text: current models omit it unless asked, and the long wait
// then shows nothing. An unstreamed one replaces the agent's own parameters,
// so it carries their output limit.
//
// Adaptive thinking is what 4.6 and later models accept; earlier ones (Haiku
// 4.5) and some compatible endpoints refuse it, and every streamed phase
// failed at its first call. effortFallback sends such a request again
// without it, as it does a refused effort.
func claudeParams(effort string, streamed bool) []agent.Option {
	if !streamed && effort == "" {
		return nil
	}
	p := anthropic.MessageNewParams{MaxTokens: unstreamedMaxTokens}
	if streamed {
		p.MaxTokens = streamedMaxTokens
		p.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
		}}
	}
	p.OutputConfig.Effort = anthropic.OutputConfigEffort(claudeEffort(effort))
	return []agent.Option{anthropicprovider.MessageNewParams(p)}
}

// claudeEffort is the level Claude is sent for effort. Current Claude models
// cannot turn thinking off (Opus 5.5 refuses it at every level), so the levels
// below low ask for the least it has.
func claudeEffort(effort string) string {
	if effort == "none" || effort == "minimal" {
		return "low"
	}
	return effort
}

// claudeCodeEffort is the --effort a claude -p call takes from its options,
// "" when they set none.
func claudeCodeEffort(opts []agent.Option) string {
	p, _ := agent.GetOption(opts, anthropicprovider.MessageNewParams)
	return string(p.OutputConfig.Effort)
}

// effortLabel is what a phase's line says it asks for: the level as sent, or
// that the endpoint already refused it.
func (a *impl) effortLabel(effort string) string {
	if a.mapEffort != nil {
		effort = a.mapEffort(effort)
	}
	switch {
	case effort == "":
		return "provider's default effort"
	case a.via == "claude-code" && a.noEffortFlag.Load():
		return "effort " + effort + " not sent: this claude has no --effort"
	case a.refused.has(effort):
		return "effort " + effort + " refused by the endpoint, provider's default"
	}
	return "effort " + effort
}

// refusedEfforts are the levels an endpoint refused and then answered without.
type refusedEfforts struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *refusedEfforts) has(level string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[level]
}

func (r *refusedEfforts) add(level string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	r.seen[level] = true
}

// maxRefusal bounds how much of a refused request's error body is read.
const maxRefusal = 16 << 10

// effortFallback is HTTP middleware for an endpoint that refuses the effort it
// is sent: Gemini answers "none" with a 400, and an OpenAI-compatible server
// may know no such field. A refusal comes back before anything streams, so
// the same request is sent once more without the effort, and when that one is
// answered the level is remembered and left out of every later request.
// Working here, per request, keeps a chat's tool loop from starting over, and
// a refusal that was about something else costs one request that fails the
// same way; a level is remembered only once the request without it worked.
func (a *impl) effortFallback(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	if req.Body == nil || req.Method != http.MethodPost {
		return next(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	without, level := body, ""
	if bytes.Contains(body, []byte("effort")) || bytes.Contains(body, []byte("thinking")) {
		without, level = stripEffort(body)
	}
	if level == "" {
		return next(withBody(req, body))
	}
	if a.refused.has(level) {
		return next(withBody(req, without))
	}
	resp, err := next(withBody(req, body))
	reason, refused := refusesEffort(resp, err)
	if !refused {
		return resp, err
	}
	_ = resp.Body.Close()
	resp, err = next(withBody(req, without))
	if err == nil && resp.StatusCode < http.StatusMultipleChoices {
		a.refused.add(level)
		what := "reasoning effort " + level
		if level == "default" {
			what = "adaptive thinking"
		}
		a.log(fmt.Sprintf("the endpoint refused %s (%s); asking without it from now on", what, reason))
	}
	return resp, err
}

// withBody is req carrying body, readable again by whatever sends it.
func withBody(req *http.Request, body []byte) *http.Request {
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	r.ContentLength = int64(len(body))
	return r
}

// stripEffort is a request body without its reasoning settings, and the
// level it had: reasoning_effort on an OpenAI-compatible request,
// output_config.effort and thinking on a Messages API one. A Messages API
// body with thinking and no effort has level "default". level is "" when the
// body sets neither.
func stripEffort(body []byte) (without []byte, level string) {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body, ""
	}
	if raw, ok := req["reasoning_effort"]; ok {
		delete(req, "reasoning_effort")
		return remarshal(req, body), unquote(raw)
	}
	var cfg map[string]json.RawMessage
	if json.Unmarshal(req["output_config"], &cfg) == nil && cfg["effort"] != nil {
		level = unquote(cfg["effort"])
		delete(cfg, "effort")
		if len(cfg) == 0 {
			delete(req, "output_config")
		} else {
			req["output_config"] = remarshal(cfg, req["output_config"])
		}
	}
	if _, ok := req["thinking"]; ok {
		delete(req, "thinking")
		level = cmp.Or(level, "default")
	}
	if level == "" {
		return body, ""
	}
	return remarshal(req, body), level
}

func remarshal(v any, orig []byte) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return orig
	}
	return b
}

func unquote(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// refusesEffort reports whether resp refused the request over its reasoning
// settings, with the provider's reason. The body is put back for the caller
// when it is not.
func refusesEffort(resp *http.Response, err error) (reason string, refused bool) {
	if err != nil || resp == nil || resp.Body == nil ||
		resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
		return "", false
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, maxRefusal))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	msg := strings.ToLower(string(head))
	if !strings.Contains(msg, "reasoning") && !strings.Contains(msg, "effort") && !strings.Contains(msg, "thinking") {
		return "", false
	}
	return clip(errorMessage(head), 160), true
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// errorMessage is the provider's message in an error body, or the body.
func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(body))
}
