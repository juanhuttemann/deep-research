package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/message"
	"github.com/microsoft/agent-framework-go/tool"
)

// claudeCode runs model calls through the claude binary, signed in to the
// user's own Claude subscription. Anthropic's terms let a tool run the
// unmodified binary under the user's sign-in; reading its credentials to call
// the API is not allowed, so this is the only way a subscription serves a run.
type claudeCode struct {
	bin, model string
	impl       *impl
	// api builds agents on ANTHROPIC_API_KEY; nil without a key.
	api newAgentFunc
	// limited is set once the subscription reports its usage limit. It holds
	// for hours, so every later call goes straight to the API.
	limited atomic.Bool
}

// errSubscriptionLimit is the subscription's usage limit. A retry does not
// clear it; the plan's window resetting does.
var errSubscriptionLimit = errors.New("the Claude subscription's usage limit is reached")

// claudeAuthTimeout bounds asking claude whether it is signed in.
const claudeAuthTimeout = 15 * time.Second

// claudeCodeLogin finds the claude binary and the subscription it is signed in
// to. It is empty when Claude Code is missing, signed out, or signed in to API
// billing, which is not a subscription.
func claudeCodeLogin(ctx context.Context) (bin, plan string) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, claudeAuthTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "auth", "status", "--json")
	cmd.Env = claudeCodeEnv()
	out, _ := cmd.Output() // signed out exits 1, with the JSON still on stdout
	var s struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &s) != nil || !s.LoggedIn || (s.AuthMethod != "claude.ai" && s.AuthMethod != "oauth_token") {
		return "", ""
	}
	return bin, cmp.Or(s.SubscriptionType, "claude.ai")
}

// claudeCodeEnv is this process's environment without the Anthropic API
// settings. Given ANTHROPIC_API_KEY, claude -p bills the key instead of the
// subscription, and the .env that holds one for the fallback loads it here.
func claudeCodeEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "ANTHROPIC_API_KEY" || k == "ANTHROPIC_AUTH_TOKEN" || k == "ANTHROPIC_BASE_URL"
	})
}

func (c *claudeCode) newAgent(name, instructions string, _ []tool.Tool) *agent.Agent {
	var twin *agent.Agent
	if c.api != nil {
		// No instructions of its own: the options it is handed carry them.
		twin = c.api(name, "", nil)
	}
	var runOpts []agent.Option
	if instructions != "" {
		runOpts = []agent.Option{agent.WithInstructions(instructions)}
	}
	run := func(ctx context.Context, msgs []*message.Message, opts ...agent.Option) iter.Seq2[*agent.ResponseUpdate, error] {
		return c.run(ctx, twin, msgs, opts)
	}
	return agent.New(agent.ProviderConfig{ProviderName: "claude-code", Run: run}, agent.Config{Name: name, RunOptions: runOpts})
}

// run answers on the subscription until it reports its limit, then on the API
// twin when there is one.
func (c *claudeCode) run(ctx context.Context, twin *agent.Agent, msgs []*message.Message, opts []agent.Option) iter.Seq2[*agent.ResponseUpdate, error] {
	return func(yield func(*agent.ResponseUpdate, error) bool) {
		if twin == nil || !c.limited.Load() {
			sent, err := c.call(ctx, msgs, opts, yield)
			if err == nil || twin == nil || !errors.Is(err, errSubscriptionLimit) {
				if err != nil {
					yield(nil, err)
				}
				return
			}
			if !c.limited.Swap(true) {
				c.impl.log("Claude subscription usage limit reached; continuing on ANTHROPIC_API_KEY")
			}
			// Part of an answer is already out and cannot be continued on the
			// API; the caller's retry starts it there instead, so this error
			// must stay retryable, unlike the limit itself.
			//
			// ponytail: with no retry left the phase fails. A limit rejects a
			// request before it streams, so this needs one landing mid-answer.
			if sent {
				yield(nil, errors.New("the Claude subscription's usage limit cut the answer off; retrying on ANTHROPIC_API_KEY"))
				return
			}
		}
		for u, err := range twin.Run(ctx, msgs, opts...) {
			if !yield(u, err) {
				return
			}
		}
	}
}

// call runs one claude -p, yielding the answer as it streams. sent reports
// whether any of it reached yield.
func (c *claudeCode) call(ctx context.Context, msgs []*message.Message, opts []agent.Option, yield func(*agent.ResponseUpdate, error) bool) (sent bool, err error) {
	args := []string{"-p",
		// The binary with nothing of the user's loaded: no CLAUDE.md, hooks,
		// MCP servers or plugins. --bare does the same but refuses the
		// subscription sign-in.
		"--safe-mode", "--strict-mcp-config", "--disable-slash-commands",
		// No tools, so a phase stays one model call.
		"--tools", "",
		"--no-session-persistence", "--permission-prompts", "none",
		"--model", c.model,
		"--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if sys := strings.Join(slices.Collect(agent.AllOptions(opts, agent.WithInstructions)), "\n"); sys != "" {
		args = append(args, "--system-prompt", sys)
	}
	procCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(procCtx, c.bin, args...)
	cmd.Env = claudeCodeEnv()
	// The prompt goes on stdin: the analysis carries every source, past what
	// an argument may hold.
	cmd.Stdin = strings.NewReader(promptOf(msgs))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Also bounds the read: a second after the deadline kills claude, exec
	// closes stdout, so a child still holding it cannot keep the call open.
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start claude: %w", err)
	}
	s := &ccStream{impl: c.impl, yield: yield}
	s.think, _ = ctx.Value(reasoningKey{}).(func(string))
	readErr := s.read(out)
	if readErr != nil || s.stopped {
		cancel()
	}
	waitErr := cmd.Wait()
	if s.stopped {
		return s.sent, nil
	}
	// The call's deadline killed it: say so, not "signal: killed".
	if ctx.Err() != nil {
		return s.sent, ctx.Err()
	}
	if readErr != nil {
		return s.sent, readErr
	}
	return s.sent, s.finish(waitErr, stderr.String())
}

func promptOf(msgs []*message.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.String())
	}
	return strings.Join(parts, "\n\n")
}

// ccLine is one line of claude -p's stream-json output, with the fields read.
type ccLine struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	// RateLimit is a rate_limit_event's: the plan's window, "rejected" once
	// its limit is reached.
	RateLimit struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
	Usage struct {
		Input      int64 `json:"input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		Output     int64 `json:"output_tokens"`
	} `json:"usage"`
	// Event is the Messages API stream event a stream_event line carries.
	Event struct {
		Type    string `json:"type"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			Thinking   string `json:"thinking"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	} `json:"event"`
}

// ccStream is one claude -p run's output as it is read.
type ccStream struct {
	impl          *impl
	think         func(string)
	yield         func(*agent.ResponseUpdate, error) bool
	sent, stopped bool
	stop          string // the model's stop_reason
	limitHit      bool   // a rate_limit_event said the plan's limit is reached
	limitResets   int64  // and when its window resets, when it said
	result        *ccLine
}

func (s *ccStream) read(out io.Reader) error {
	sc := bufio.NewScanner(out)
	// The result line holds the whole answer.
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for !s.stopped && sc.Scan() {
		var l ccLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			s.line(&l)
		}
	}
	return sc.Err()
}

func (s *ccStream) line(l *ccLine) {
	switch d := l.Event.Delta; {
	case l.Type == "result":
		s.result = l
	case l.RateLimit.Status == "rejected":
		s.limitHit, s.limitResets = true, l.RateLimit.ResetsAt
	case l.Event.Type == "message_start" && l.Event.Message.Model != "":
		s.impl.addServed(l.Event.Message.Model)
	case d.Type == "text_delta":
		s.text(d.Text)
	case d.Type == "thinking_delta":
		s.thinking(d.Thinking)
	case d.StopReason != "":
		s.stop = d.StopReason
	}
}

func (s *ccStream) text(t string) {
	s.sent = true
	if !s.yield(&agent.ResponseUpdate{Role: message.RoleAssistant, Contents: message.Contents{&message.TextContent{Text: t}}}, nil) {
		s.stopped = true
	}
}

// thinking hands the reasoning to the status line, and yields it so the
// progress reporter runs while no answer text has arrived yet.
func (s *ccStream) thinking(t string) {
	if s.think != nil {
		s.think(t)
	}
	if !s.yield(&agent.ResponseUpdate{Role: message.RoleAssistant, Contents: message.Contents{&message.TextReasoningContent{Text: t}}}, nil) {
		s.stopped = true
	}
}

// finish turns the run's result into the closing update, or the call's error.
func (s *ccStream) finish(waitErr error, stderr string) error {
	r := s.result
	if r == nil {
		return fmt.Errorf("claude ended without an answer: %v %s", waitErr, strings.TrimSpace(stderr))
	}
	if r.IsError {
		if s.limitHit || subscriptionLimited(r.Result) {
			return limitError(s.limitResets, r.Result)
		}
		return fmt.Errorf("claude: %s", r.Result)
	}
	// Without partial messages the answer arrives only here.
	if !s.sent && r.Result != "" {
		if s.text(r.Result); s.stopped {
			return nil
		}
	}
	u := r.Usage
	in := u.Input + u.CacheWrite + u.CacheRead
	s.yield(&agent.ResponseUpdate{Role: message.RoleAssistant, FinishReason: finishReason(s.stop),
		Contents: message.Contents{&message.UsageContent{Details: message.UsageDetails{
			InputTokenCount: in, OutputTokenCount: u.Output, TotalTokenCount: in + u.Output, CachedInputTokenCount: u.CacheRead,
		}}}}, nil)
	return nil
}

// finishReason maps a stop_reason to the framework's, as the API provider does.
func finishReason(stop string) string {
	switch stop {
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// subscriptionLimited reports whether a failed run hit the plan's usage limit
// when no rate_limit_event said so, by Claude Code's wording. A 429 it retried
// is not one: that is the API's rate limit, which clears in seconds.
//
// ponytail: a backstop for versions without the event; wording drifts.
func subscriptionLimited(result string) bool {
	r := strings.ToLower(result)
	return strings.Contains(r, "usage limit") || strings.Contains(r, "hit your limit")
}

// limitError is the subscription's limit with when it resets: from the
// rate_limit_event when it gave the time, else from the result's wording.
func limitError(resets int64, result string) error {
	if resets > 0 {
		return fmt.Errorf("%w; it resets at %s", errSubscriptionLimit, time.Unix(resets, 0).Format("15:04 Jan 2"))
	}
	return fmt.Errorf("%w: %s", errSubscriptionLimit, resetTime(result))
}

// resetTime rewrites the "message|unix-time" form of a limit message as the
// local time it resets at.
func resetTime(result string) string {
	msg, at, ok := strings.Cut(result, "|")
	sec, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil {
		return result
	}
	return msg + "; it resets at " + time.Unix(sec, 0).Format("15:04 Jan 2")
}
