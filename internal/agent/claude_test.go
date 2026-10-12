package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"
)

// anthropicServer is a Messages API double. reply gives the content blocks
// and stop reason for the nth request; every body is kept for the assertions.
type anthropicServer struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
}

type anthropicReply struct {
	status     int
	blocks     []map[string]any
	stopReason string
}

func newAnthropicServer(t *testing.T, reply func(n int) anthropicReply) *anthropicServer {
	t.Helper()
	s := &anthropicServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		n := len(s.bodies)
		s.bodies = append(s.bodies, body)
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		rep := reply(n)
		if rep.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rep.status)
			fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		if body["stream"] == true {
			writeAnthropicStream(w, rep)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-served",
			"content": rep.blocks, "stop_reason": rep.stopReason,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func writeAnthropicStream(w http.ResponseWriter, rep anthropicReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(v map[string]any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", v["type"], b)
	}
	event(map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-served", "content": []any{},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 1}}})
	for i, b := range rep.blocks {
		switch b["type"] {
		case "thinking":
			event(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			event(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "thinking_delta", "thinking": b["thinking"]}})
			event(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "signature_delta", "signature": "sig"}})
		default:
			event(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			event(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": b["text"]}})
		}
		event(map[string]any{"type": "content_block_stop", "index": i})
	}
	event(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": rep.stopReason}, "usage": map[string]any{"output_tokens": 20}})
	event(map[string]any{"type": "message_stop"})
}

func (s *anthropicServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func text(s string) anthropicReply {
	return anthropicReply{blocks: []map[string]any{{"type": "text", "text": s}}, stopReason: "end_turn"}
}

// noClaudeCode hides any real claude binary, so the API transport is the one
// under test.
func noClaudeCode(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// fakeClaudeCode puts a claude script on PATH that answers `auth status` with
// auth and every -p call with out, recording its arguments, environment and
// stdin. It returns the directory those records are written to.
func fakeClaudeCode(t *testing.T, auth, out string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("auth.json", auth, 0o644)
	write("out.ndjson", out, 0o644)
	write("claude", `#!/bin/sh
d=$(dirname "$0")
if [ "$1" = auth ]; then cat "$d/auth.json"; exit 0; fi
echo call >> "$d/calls"
printf '%s\n' "$@" > "$d/args"
env > "$d/env"
cat > "$d/stdin"
cat "$d/out.ndjson"
`, 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const subscribed = `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"pro"}`

// claudeCodeRun is a -p stream: one thinking delta, the answer in two text
// deltas, the result with its usage.
func claudeCodeRun(answer string) string {
	half := len(answer) / 2
	return strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-5-5"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-served-sub"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Weigh c3 against c5"}}}`,
		fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":%q}}}`, answer[:half]),
		fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":%q}}}`, answer[half:]),
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"result":%q,"usage":{"input_tokens":3,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"output_tokens":7}}`, answer),
	}, "\n") + "\n"
}

// A Claude subscription is used through the unmodified claude binary, signed
// in by the user, and before any API key: the key in the environment would
// otherwise be what Claude Code bills, so the child never sees it.
func TestClaudeUsesTheSubscriptionFirst(t *testing.T) {
	dir := fakeClaudeCode(t, subscribed, claudeCodeRun("The report."))
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-reach-claude")
	api := newAnthropicServer(t, func(int) anthropicReply { return text("from the API") })

	a, err := New(Config{Provider: "claude", ClaudeModel: "claude-opus-5-5",
		AnthropicAPIKey: "sk", AnthropicBaseURL: api.URL,
		SummarizerInstructions: "write the report", ModelCallTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	a.(interface{ SetStatus(func(string)) }).SetStatus(func(msg string) { mu.Lock(); lines = append(lines, msg); mu.Unlock() })

	sum, err := a.Summarize(context.Background(), "the findings")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Report != "The report." {
		t.Errorf("report = %q", sum.Report)
	}
	if api.requests() != 0 {
		t.Errorf("the API was called %d times while the subscription was signed in", api.requests())
	}
	args := strings.Split(readFile(t, filepath.Join(dir, "args")), "\n")
	for _, want := range [][]string{{"-p"}, {"--safe-mode"}, {"--tools", ""}, {"--model", "claude-opus-5-5"},
		{"--system-prompt", "write the report"}, {"--output-format", "stream-json"}} {
		if i := slices.Index(args, want[0]); i < 0 || !slices.Equal(args[i:i+len(want)], want) {
			t.Errorf("claude args lack %q:\n%s", want, strings.Join(args, "\n"))
		}
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); !strings.Contains(got, "the findings") {
		t.Errorf("stdin = %q, want the prompt", got)
	}
	if env := readFile(t, filepath.Join(dir, "env")); strings.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Error("claude was given ANTHROPIC_API_KEY, which it would bill instead of the subscription")
	}
	if got := a.TokensUsed(); got != 160 {
		t.Errorf("TokensUsed = %d, want 160 (input, both cache counts and output)", got)
	}
	m := a.(interface {
		ModelInfo() (string, string)
		ServedModels() []string
	})
	if model, via := m.ModelInfo(); model != "claude-opus-5-5" || via != "claude-code" {
		t.Errorf("ModelInfo = %q, %q", model, via)
	}
	if got := m.ServedModels(); !slices.Equal(got, []string{"claude-served-sub"}) {
		t.Errorf("ServedModels = %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "Weigh c3 against c5") }) {
		t.Errorf("the thinking never reached the status line:\n%s", strings.Join(lines, "\n"))
	}
}

// limitRun is a -p run refused by the plan's limit: Claude Code reports it as
// a rate_limit_event, then fails the result.
const limitRun = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791784800,"rateLimitType":"five_hour"}}
{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit"}
`

// The API key is the fallback: a run that exhausts the subscription carries on
// with the key instead of losing its remaining phases, and stays there rather
// than spending a doomed claude call on every later phase.
func TestClaudeSubscriptionLimitFallsBackToTheAPIKey(t *testing.T) {
	dir := fakeClaudeCode(t, subscribed, limitRun)
	api := newAnthropicServer(t, func(int) anthropicReply { return text("from the API") })
	a, err := New(Config{Provider: "claude", ClaudeModel: "claude-opus-5-5",
		AnthropicAPIKey: "sk", AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	a.SetProgress(func(m string) { logged = append(logged, m) })
	for range 2 {
		sum, err := a.Summarize(context.Background(), "p")
		if err != nil {
			t.Fatal(err)
		}
		if sum.Report != "from the API" {
			t.Errorf("report = %q, want the API's answer", sum.Report)
		}
	}
	if calls := strings.Count(readFile(t, filepath.Join(dir, "calls")), "call"); calls != 1 {
		t.Errorf("claude ran %d times, want 1: the limit holds for the rest of the run", calls)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "continuing on ANTHROPIC_API_KEY") }) {
		t.Errorf("the switch to the API key was not reported:\n%s", strings.Join(logged, "\n"))
	}
}

// A limit that cuts an answer off midway cannot be continued on the API, but
// the retry can: with a key it is not the final "set ANTHROPIC_API_KEY".
func TestClaudeSubscriptionLimitMidAnswerRetriesOnTheAPI(t *testing.T) {
	midway := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The rep"}}}
` + limitRun
	dir := fakeClaudeCode(t, subscribed, midway)
	api := newAnthropicServer(t, func(int) anthropicReply { return text("from the API") })
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", AnthropicAPIKey: "sk",
		AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second, ModelCallRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := a.Summarize(context.Background(), "p")
	if err != nil || sum.Report != "from the API" {
		t.Fatalf("report = %v, %v; want the API's answer on the retry", sum, err)
	}
	if calls := strings.Count(readFile(t, filepath.Join(dir, "calls")), "call"); calls != 1 {
		t.Errorf("claude ran %d times, want 1", calls)
	}
}

// Without a key the limit is final until it resets; retrying it spends the
// whole doubling budget on a certain failure.
func TestClaudeSubscriptionLimitWithoutKeyIsNotRetried(t *testing.T) {
	dir := fakeClaudeCode(t, subscribed, limitRun)
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", ModelCallTimeout: 10 * time.Second, ModelCallRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Summarize(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "usage limit") || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("err = %v, want the limit and how to continue", err)
	}
	if reset := time.Unix(1791784800, 0).Format("15:04 Jan 2"); !strings.Contains(err.Error(), "resets at "+reset) {
		t.Errorf("err = %v, want the reset time %s", err, reset)
	}
	if calls := strings.Count(readFile(t, filepath.Join(dir, "calls")), "call"); calls != 1 {
		t.Errorf("claude ran %d times, want 1", calls)
	}
}

// The call deadline kills claude; the retry log has to say that, not "signal:
// killed", or a slow model reads as a crash.
func TestClaudeCodeDeadlineIsReportedAsOne(t *testing.T) {
	dir := fakeClaudeCode(t, subscribed, "")
	slow := "#!/bin/sh\nif [ \"$1\" = auth ]; then cat \"$(dirname \"$0\")/auth.json\"; exit 0; fi\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", ModelCallTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Summarize(context.Background(), "p")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
}

// Killing claude at the deadline is not enough when something it started
// still holds its stdout: the read waited for that instead of the deadline.
func TestClaudeCodeDeadlineDoesNotWaitForItsChildren(t *testing.T) {
	dir := fakeClaudeCode(t, subscribed, "")
	slow := "#!/bin/sh\nif [ \"$1\" = auth ]; then cat \"$(dirname \"$0\")/auth.json\"; exit 0; fi\nsleep 30 &\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", ModelCallTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = a.Summarize(context.Background(), "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the call took %s past a 200ms deadline", elapsed)
	}
}

// A consumer that stops at the answer must not be handed the usage after it:
// range-over-func panics on a yield after false.
func TestClaudeCodeStopsYieldingWhenTheConsumerStops(t *testing.T) {
	n := 0
	s := &ccStream{impl: &impl{}, yield: func(*agent.ResponseUpdate, error) bool { n++; return false }}
	s.result = &ccLine{Result: "the whole answer"}
	if err := s.finish(nil, ""); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("yielded %d times after the consumer stopped at the first", n)
	}
}

// Claude Code installed but signed out (or signed in to API billing) is not a
// subscription: the key is used.
func TestClaudeUsesTheKeyWhenClaudeCodeIsNotSubscribed(t *testing.T) {
	for name, auth := range map[string]string{
		"logged out": `{"loggedIn":false,"authMethod":"none"}`,
		"console":    `{"loggedIn":true,"authMethod":"api_key"}`,
	} {
		t.Run(name, func(t *testing.T) {
			fakeClaudeCode(t, auth, claudeCodeRun("from claude"))
			api := newAnthropicServer(t, func(int) anthropicReply { return text("from the API") })
			a, err := New(Config{Provider: "claude", ClaudeModel: "m", AnthropicAPIKey: "sk",
				AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			sum, err := a.Summarize(context.Background(), "p")
			if err != nil || sum.Report != "from the API" {
				t.Errorf("report = %v, %v; want the API's answer", sum, err)
			}
		})
	}
}

func TestClaudeWithoutLoginOrKeyIsAnError(t *testing.T) {
	noClaudeCode(t)
	_, err := New(Config{Provider: "claude", ClaudeModel: "m"})
	if err == nil || !strings.Contains(err.Error(), "claude auth login") || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("err = %v, want both ways to get a model", err)
	}
}

// The Messages API rejects a JSON output format without a schema, and its
// default max_tokens (4096) cuts a report off; thinking is only shown when
// asked for.
func TestClaudeAPIRequestShape(t *testing.T) {
	noClaudeCode(t)
	api := newAnthropicServer(t, func(int) anthropicReply {
		return text(`{"subtopics":[{"name":"Core","query":"q","terms":["a","b"]}]}`)
	})
	a, err := New(Config{Provider: "claude", ClaudeModel: "claude-opus-5-5", AnthropicAPIKey: "sk",
		AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Plan(context.Background(), "question", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResearchDetail(context.Background(), "query", nil); err != nil {
		t.Fatal(err)
	}
	if api.requests() != 2 {
		t.Fatalf("requests = %d, want the plan and the search", api.requests())
	}
	plan, search := api.bodies[0], api.bodies[1]
	if api.headers[0].Get("X-Api-Key") != "sk" {
		t.Errorf("x-api-key = %q", api.headers[0].Get("X-Api-Key"))
	}
	noFieldsOutsideTheClaudeAPI(t, plan, search)
	if plan["stream"] != true || plan["max_tokens"] != float64(64000) {
		t.Errorf("plan: stream=%v max_tokens=%v, want a streamed 64000", plan["stream"], plan["max_tokens"])
	}
	if th, _ := plan["thinking"].(map[string]any); th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Errorf("plan thinking = %v, want adaptive and summarized", plan["thinking"])
	}
	if search["stream"] == true || search["max_tokens"] != float64(16000) {
		t.Errorf("search: stream=%v max_tokens=%v, want an unstreamed 16000", search["stream"], search["max_tokens"])
	}
	if got := api.headers[0].Get("Anthropic-Beta"); strings.Contains(got, "fallback") {
		t.Errorf("anthropic-beta = %q on a host that is not the Claude API", got)
	}
}

// noFieldsOutsideTheClaudeAPI checks requests carry neither a JSON output
// format nor the refusal fallback, which only api.anthropic.com accepts.
func noFieldsOutsideTheClaudeAPI(t *testing.T, bodies ...map[string]any) {
	t.Helper()
	for _, b := range bodies {
		if _, ok := b["output_config"]; ok {
			t.Errorf("output_config sent: %v", b["output_config"])
		}
		if _, ok := b["fallbacks"]; ok {
			t.Error("fallbacks sent to a host that is not the Claude API")
		}
	}
}

func TestRefusalFallbackOnlyWhereItIsAccepted(t *testing.T) {
	for _, c := range []struct {
		base, model string
		want        bool
	}{
		{"https://api.anthropic.com", "claude-opus-5-5", true},
		{"https://api.anthropic.com/", "claude-sonnet-5-5", true},
		{"https://api.anthropic.com", "claude-haiku-5-5", false},
		{"https://proxy.example", "claude-opus-5-5", false},
	} {
		if got := refusalFallback(c.base, c.model); got != c.want {
			t.Errorf("refusalFallback(%q, %q) = %v, want %v", c.base, c.model, got, c.want)
		}
	}
}

func TestClaudeRejectedKeyNamesTheAnthropicKey(t *testing.T) {
	noClaudeCode(t)
	api := newAnthropicServer(t, func(int) anthropicReply { return anthropicReply{status: http.StatusUnauthorized} })
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", AnthropicAPIKey: "sk",
		AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second, ModelCallRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Summarize(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") || strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("err = %v, want the Anthropic key named and no OpenRouter hint", err)
	}
	if api.requests() != 1 {
		t.Errorf("a rejected key was retried: %d requests", api.requests())
	}
}

// A declined request comes back as a normal answer with no text; reading it
// as one handed the phase an empty report.
func TestDeclinedAnswerIsAnError(t *testing.T) {
	noClaudeCode(t)
	api := newAnthropicServer(t, func(int) anthropicReply { return anthropicReply{stopReason: "refusal"} })
	a, err := New(Config{Provider: "claude", ClaudeModel: "m", AnthropicAPIKey: "sk",
		AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second, ModelCallRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Summarize(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("err = %v, want the refusal reported", err)
	}
	if api.requests() != 1 {
		t.Errorf("a refusal was retried: %d requests", api.requests())
	}
}

// Claude always thinks, and a tool loop must send that thinking back with the
// tool call it led to; the framework once dropped it.
func TestClaudeChatReplaysThinkingInTheToolLoop(t *testing.T) {
	noClaudeCode(t)
	api := newAnthropicServer(t, func(n int) anthropicReply {
		if n == 0 {
			return anthropicReply{stopReason: "tool_use", blocks: []map[string]any{
				{"type": "thinking", "thinking": "I should read the page", "signature": "sig"},
				{"type": "tool_use", "id": "toolu_1", "name": "read_source", "input": map[string]any{"url": "https://a.example"}},
			}}
		}
		return text("It costs 5 dollars.")
	})
	read := functool.MustNew(functool.Config{Name: "read_source", Description: "read a page"},
		func(_ context.Context, in struct {
			URL string `json:"url"`
		}) (string, error) {
			return "PAGE SAYS 5 DOLLARS", nil
		})
	chat, err := NewChat(Config{Provider: "claude", ClaudeModel: "m", AnthropicAPIKey: "sk",
		AnthropicBaseURL: api.URL, ModelCallTimeout: 10 * time.Second}, "overview", []tool.Tool{read})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(context.Background(), "How much?"); err != nil {
		t.Fatal(err)
	}
	if api.requests() != 2 {
		t.Fatalf("requests = %d, want the tool call and the answer", api.requests())
	}
	msgs, _ := api.bodies[1]["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("second request messages = %v", msgs)
	}
	assistant, _ := msgs[1].(map[string]any)
	content, _ := assistant["content"].([]any)
	first, _ := content[0].(map[string]any)
	if first["type"] != "thinking" {
		t.Errorf("the replayed tool call does not start with its thinking: %v", content)
	}
}

// Questions about a run call deep-research's own tools, which claude -p
// cannot reach, so they need the key even with a subscription signed in.
func TestClaudeChatNeedsTheAPIKey(t *testing.T) {
	fakeClaudeCode(t, subscribed, claudeCodeRun("x"))
	_, err := NewChat(Config{Provider: "claude", ClaudeModel: "m"}, "overview", nil)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("err = %v, want the key asked for", err)
	}
}

func TestStreamedReasoningReadsAnthropicThinking(t *testing.T) {
	line := `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Weigh c3"}}`
	if got := streamedReasoning([]byte(line)); got != "Weigh c3" {
		t.Errorf("streamedReasoning = %q, want the thinking delta", got)
	}
}
