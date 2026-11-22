package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/openai/openai-go/v3/option"
)

// outputLimit is what the run learned about the output limit openAIParams
// sends: unsupported once an endpoint refused the parameter itself, which
// every later request would hit too, and warned once the run has said that a
// request did not fit, so a long run says it once.
type outputLimit struct {
	unsupported atomic.Bool
	warned      atomic.Bool
}

// limitRefusal is why an endpoint refused a request's output limit.
type limitRefusal int

const (
	limitAccepted limitRefusal = iota
	// limitTooLarge: the prompt and the limit together exceed the context
	// window. It is about this request: a shorter prompt still fits the limit.
	limitTooLarge
	// limitOverBudget: OpenRouter priced the request at its limit and the
	// estimate exceeds the account's per-request budget. Also this request's.
	limitOverBudget
	// limitUnsupported: the endpoint knows no max_completion_tokens.
	limitUnsupported
)

// outputLimitFallback is HTTP middleware for a request whose output limit the
// endpoint refuses before anything streams. A request too large for the
// context window goes again with the largest limit the error says fits, or
// without a limit when it does not say; one over OpenRouter's budget goes
// again without it, as every call did before the limit was added. Neither is
// remembered, since the next request may well fit. Only an endpoint that
// refuses the parameter itself has it left out for the rest of the run.
func (a *impl) outputLimitFallback(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	if req.Body == nil || req.Method != http.MethodPost {
		return next(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	fields, ok := outputLimitFields(body)
	if !ok {
		return next(withBody(req, body))
	}
	without := withOutputLimit(fields, body, 0)
	if a.outputLimit.unsupported.Load() {
		return next(withBody(req, without))
	}
	resp, err := next(withBody(req, body))
	kind, reason, fits := refusesOutputLimit(resp, err)
	if kind == limitAccepted {
		return resp, err
	}
	_ = resp.Body.Close()
	retry := without
	if kind == limitTooLarge && fits > 0 {
		retry = withOutputLimit(fields, body, fits)
	}
	resp, err = next(withBody(req, retry))
	if err == nil && resp.StatusCode < http.StatusMultipleChoices {
		a.noteLimitRefusal(kind, reason, fits)
	}
	return resp, err
}

// noteLimitRefusal remembers and reports a refusal the retry got past.
func (a *impl) noteLimitRefusal(kind limitRefusal, reason string, fits int) {
	if kind == limitUnsupported {
		a.outputLimit.unsupported.Store(true)
		a.log("the endpoint refused the output limit (" + reason + "); asking without it from now on")
		return
	}
	if a.outputLimit.warned.Swap(true) {
		return
	}
	what := "without an output limit"
	if kind == limitTooLarge && fits > 0 {
		what = "for the " + strconv.Itoa(fits) + " output tokens that fit"
	}
	a.log("the endpoint refused a call's output limit (" + reason + "); asked again " + what +
		", and calls that fit keep the limit")
}

// outputLimitFields is an OpenAI-compatible request body's fields, when it
// carries max_completion_tokens.
func outputLimitFields(body []byte) (map[string]json.RawMessage, bool) {
	if !strings.Contains(string(body), "max_completion_tokens") {
		return nil, false
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || req["max_completion_tokens"] == nil {
		return nil, false
	}
	return req, true
}

// withOutputLimit is the body with max_completion_tokens set to limit, or
// without it when limit is 0. fields is left as it was.
func withOutputLimit(fields map[string]json.RawMessage, orig []byte, limit int) []byte {
	req := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		req[k] = v
	}
	delete(req, "max_completion_tokens")
	if limit > 0 {
		req["max_completion_tokens"] = json.RawMessage(strconv.Itoa(limit))
	}
	return remarshal(req, orig)
}

// refusesOutputLimit reports whether resp refused the request over its output
// limit, how, the provider's reason, and for a request too large for the
// context window the reply tokens that fit (0 when the error does not say).
// The body is put back for the caller when it is not such a refusal.
func refusesOutputLimit(resp *http.Response, err error) (limitRefusal, string, int) {
	if err == nil && resp != nil && resp.StatusCode == http.StatusPaymentRequired {
		return refusesOverBudget(resp)
	}
	head, ok := peekRefusal(resp, err)
	if !ok {
		return limitAccepted, "", 0
	}
	msg := strings.ToLower(string(head))
	reason := clip(errorMessage(head), 160)
	switch {
	case strings.Contains(msg, "context length") || strings.Contains(msg, "context_length") ||
		strings.Contains(msg, "context window"):
		return limitTooLarge, reason, replyTokensThatFit(string(head))
	case strings.Contains(msg, "max_completion_tokens"):
		return limitUnsupported, reason, 0
	}
	return limitAccepted, "", 0
}

// refusesOverBudget reads a 402: OpenRouter's weight_exceeds_budget, a single
// request priced at its output limit above the account's in-flight budget, is
// a refusal of the limit. Any other 402 (credits run out, the in-flight budget
// momentarily full) is not, and is put back for the caller.
func refusesOverBudget(resp *http.Response) (limitRefusal, string, int) {
	if resp.Body == nil {
		return limitAccepted, "", 0
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, maxRefusal))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(strings.NewReader(string(head)), resp.Body), resp.Body}
	var e struct {
		Error struct {
			Metadata struct {
				Reason string `json:"reason"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if json.Unmarshal(head, &e) != nil || e.Error.Metadata.Reason != "weight_exceeds_budget" {
		return limitAccepted, "", 0
	}
	return limitOverBudget, clip(errorMessage(head), 160), 0
}

// The ways a context-window refusal says what fits: a gateway in front of
// vLLM names the reply tokens; vLLM names the window and the prompt, in one
// of two wordings.
var (
	fitsReply   = regexp.MustCompile(`request at most (\d+) reply tokens`)
	fitsWindow  = regexp.MustCompile(`maximum context length is (\d+) tokens`)
	fitsPrompt  = regexp.MustCompile(`\((\d+) in the messages`)
	fitsPrompt2 = regexp.MustCompile(`your request has (\d+) input tokens`)
)

// replyTokensThatFit is the largest output limit a context-window refusal
// says the request can have, or 0 when it does not say or nothing fits.
func replyTokensThatFit(msg string) int {
	if n := firstInt(fitsReply, msg); n > 0 {
		return n
	}
	window := firstInt(fitsWindow, msg)
	prompt := firstInt(fitsPrompt, msg)
	if prompt == 0 {
		prompt = firstInt(fitsPrompt2, msg)
	}
	if window == 0 || prompt == 0 || window <= prompt {
		return 0
	}
	return window - prompt
}

func firstInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
