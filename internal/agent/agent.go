package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/openaiprovider"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Config holds per-agent LLM settings. Pipeline-level settings
// (the per-sub-agent source budget) live in config.Config.
// When SearXNGURL and FirecrawlURL are set, research uses the real
// search+scrape tools; otherwise the search agent (LLM) is used.
type Config struct {
	AnalyzerInstructions    string
	FactCheckerInstructions string
	SummarizerInstructions  string
	SearchInstructions      string
	PlanningInstructions    string
	OpenAIAPIKey            string
	OpenAIBaseURL           string
	OpenAIModel             string
	ModelCallTimeout        time.Duration
	// ModelCallRetries is how many extra attempts a failed model call gets.
	// Zero means one attempt and no retry.
	ModelCallRetries int
	SearXNGURL       string
	FirecrawlURL     string
	FirecrawlAPIKey  string
}

// SubTopic is one branch of a research plan produced during the planning
// phase. A set of sub-topics seeds the parallel sub-agent tree.
type SubTopic struct {
	ID    string
	Name  string
	Notes string
}

// Finding is one unit of research evidence.
type Finding struct {
	Query      string `json:"query"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	URL        string `json:"url"`
	Confidence string `json:"confidence"`
	// Status is the source's SourceSignal.Status, carried on the finding so
	// the exports can state how each citation was obtained. The signals
	// themselves live in ResearchDetail, which the pipeline drops once the
	// findings are collected.
	Status string `json:"status,omitempty"`
}

// Analysis is the analyzer's structured output.
type Analysis struct {
	Answer     string   `json:"answer"`
	Topics     []Topic  `json:"topics"`
	Gaps       []string `json:"gaps"`
	Confidence string   `json:"confidence"`
	FollowUp   []string `json:"follow_up"`
}

// Topic is a topic cluster inside an analysis.
type Topic struct {
	Name       string   `json:"name"`
	Findings   []string `json:"findings"`
	Confidence string   `json:"confidence"`
}

// FactCheckResult is the fact-checker's output.
type FactCheckResult struct {
	Verified       []VerifiedClaim `json:"verified"`
	Unverified     []string        `json:"unverified"`
	Contradictions []Contradiction `json:"contradictions"`
}

// VerifiedClaim is a claim checked against the sources.
type VerifiedClaim struct {
	Claim    string `json:"claim"`
	Verified bool   `json:"verified"`
	Evidence string `json:"evidence"`
}

// Contradiction is a conflict between sources.
type Contradiction struct {
	Claim   string   `json:"claim"`
	Sources []string `json:"sources"`
}

// Assistant is a single LLM phase of the pipeline. ui.Driver sequences the
// four phases; each method is one model call.
type Assistant interface {
	// ResearchDetail runs a query and returns findings together with
	// per-source verification signals, so the UI can render source-quality
	// indicators. When real search+scrape is unavailable it synthesises a
	// best-effort signal set.
	ResearchDetail(ctx context.Context, query string) (*ResearchDetail, error)
	// Analyze synthesizes findings into an answer.
	Analyze(ctx context.Context, prompt string) (*Analysis, error)
	// FactCheck verifies claims against the findings. The prompt is composed
	// by the caller and sent verbatim: the fact-checker's whole job is to
	// compare two things, so the sources have to travel with the claims.
	FactCheck(ctx context.Context, claims string) (*FactCheckResult, error)
	// Summarize turns analysis and fact-check output into a report.
	Summarize(ctx context.Context, prompt string) (*Summary, error)
	// Plan breaks a question into subTopics ordered research sub-topics used
	// to seed the parallel sub-agent tree. Breadth is the caller's to choose:
	// the run's total effort is sources-per-branch times branches, so a depth
	// tier that set only the former left the model deciding half of it. It
	// returns an error only when the model call itself fails; a usable plan is
	// always derived as a fallback.
	Plan(ctx context.Context, question string, subTopics int) ([]SubTopic, error)
	// SetSourceBudget caps how many sources a single query may gather. It is
	// what gives the depth tier its effect: without it the search tool's own
	// fixed per-query limit bounds every run identically, and picking a deeper
	// tier changes the estimates but never the research.
	SetSourceBudget(perQuery int)
	// SetProgress wires the progress logger so each sub-step (the model call
	// and, when configured, each search/scrape) reports where and what it
	// is doing. A nil logger disables sub-step logging.
	SetProgress(logf func(string))
	// TokensUsed is the running total of tokens the provider has reported
	// across every model call made so far, so the UI and the exported trace
	// can show what the run actually consumed. It is safe to call while
	// sub-agents run in parallel.
	TokensUsed() int
}

// Summary is the final report.
type Summary struct {
	Report     string `json:"report"`
	Executive  string `json:"executive_summary"`
	Confidence string `json:"confidence"`
}

// ResearchDetail is the full, signal-enriched output of a single query.
type ResearchDetail struct {
	Findings []Finding      `json:"findings"`
	Signals  []SourceSignal `json:"signals"`
	// Skipped are results the search tools judged off-topic and never
	// fetched, carried so the run can report what it ignored.
	Skipped []SkippedSource `json:"skipped,omitempty"`
}

// SkippedSource is a search result rejected as off-topic before any fetch.
type SkippedSource = tools.SkippedSource

// SourceSignal is the quality/availability outcome of fetching a single URL.
type SourceSignal = tools.SourceSignal
type ResearchResult struct {
	Question  string           `json:"question"`
	Tokens    int              `json:"tokens"`
	Findings  []Finding        `json:"findings"`
	Analysis  *Analysis        `json:"analysis"`
	FactCheck *FactCheckResult `json:"fact_check"`
	Summary   *Summary         `json:"summary"`
	Timestamp time.Time        `json:"timestamp"`
	// Error is set when a late phase failed and the run was delivered from
	// what it had gathered; empty for a complete run.
	Error string `json:"error,omitempty"`
	// Model and Provider name what wrote the report: the configured model and
	// the endpoint's host. With a router alias the provider picks the model
	// per request, and without this two runs could not be compared; ServedBy
	// is what it picked.
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
	ServedBy []string `json:"served_by,omitempty"`
}

type runFunc func(ctx context.Context, prompt string) (string, error)

// New creates an Assistant backed by any OpenAI-compatible API.
//
// It fails when there is no API key: every phase of the pipeline is a model
// call, so a keyless online assistant cannot do anything except error at the
// first one. Reporting it here is what lets the CLI fall back to offline mode.
func New(cfg Config) (Assistant, error) {
	key := strings.TrimSpace(cfg.OpenAIAPIKey)
	if key == "" {
		return nil, errors.New("no API key (set OPENAI_API_KEY)")
	}
	timeout := cfg.ModelCallTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	impl := &impl{model: cfg.OpenAIModel, baseURL: cfg.OpenAIBaseURL,
		timeout: timeout, retries: max(cfg.ModelCallRetries, 0)}

	// The deadline is applied per call as a context timeout (see impl.call),
	// not as http.Client.Timeout: that one also caps reading the response
	// body, so a report the model was still streaming died mid-write with
	// "error reading response body" and no chance to retry.
	client := openai.NewClient(
		option.WithBaseURL(cfg.OpenAIBaseURL),
		option.WithAPIKey(key),
		option.WithHTTPClient(providerHTTPClient()),
		// The SDK retries twice by default, underneath impl.call's own loop.
		// Nested, the two multiply — up to nine HTTP attempts for one phase —
		// and the inner loop re-dials hosts the outer one has already judged
		// unreachable. Retry policy lives in impl.call, which knows the
		// difference between a server that never answered and one that did.
		option.WithMaxRetries(0),
		option.WithMiddleware(impl.sniffServedModel),
	)

	newAgent := func(name, instructions string) *agent.Agent {
		return openaiprovider.NewChatCompletionsAgent(client, openaiprovider.AgentConfig{
			Model:        cfg.OpenAIModel,
			Instructions: instructions,
			Config:       agent.Config{Name: name},
		})
	}

	// newRunJSON constrains the model to emit a JSON object
	// (response_format=json_object). Search/analysis/fact-check/planner all
	// parse JSON back out, so forcing structured output stops freeform models
	// from emitting prose that the parser cannot recover.
	newRunJSON := func(a *agent.Agent) runFunc {
		return func(ctx context.Context, prompt string) (string, error) {
			return impl.call(ctx, a, prompt,
				agent.WithResponseFormat(agent.ResponseFormat{Kind: "json"}))
		}
	}

	// The post-research phases are the long ones — each re-reads every finding
	// — and they used to print one line on starting and nothing again until
	// they finished or the deadline killed them. Streaming lets them report
	// how much of the answer has arrived. Search is left unstreamed: it runs in
	// parallel across sub-agents, where interleaved progress is noise.
	newRunStreaming := func(a *agent.Agent, label string, unit progressUnit, opts ...agent.Option) runFunc {
		return func(ctx context.Context, prompt string) (string, error) {
			return impl.callStreaming(ctx, a, prompt, impl.streamProgress(label, unit), opts...)
		}
	}
	jsonFormat := agent.WithResponseFormat(agent.ResponseFormat{Kind: "json"})

	if cfg.PlanningInstructions == "" {
		cfg.PlanningInstructions = defaultPlanningInstructions
	}
	impl.search = newRunJSON(newAgent("search", cfg.SearchInstructions))
	impl.analyzer = newRunStreaming(newAgent("analyzer", cfg.AnalyzerInstructions), "analyzing",
		entriesUnit("section so far", "sections so far", "answer", "name", "gaps", "follow_up", "follow_up_queries"), jsonFormat)
	impl.factCheck = newRunStreaming(newAgent("fact_checker", cfg.FactCheckerInstructions), "fact-checking",
		entriesUnit("claim checked", "claims checked", "claim"), jsonFormat)
	impl.summarizer = newRunStreaming(newAgent("summarizer", cfg.SummarizerInstructions), "writing the report", wordsUnit)
	// The planner streams: it is the only phase with nothing on screen behind
	// it, so the wait is dead air unless the run can say how much of the plan
	// has arrived.
	planner := newAgent("planner", cfg.PlanningInstructions)
	impl.plan = func(ctx context.Context, prompt string) (string, error) {
		reported := 0
		return impl.callStreaming(ctx, planner, prompt, func(partial string) {
			// Report on whole sub-topics, not on every chunk: a counter that
			// moves once per token is noise, and the name key is the first
			// thing complete enough to count.
			if n := strings.Count(partial, `"name"`); n > reported {
				reported = n
				impl.log(fmt.Sprintf("%d sub-topics so far", n))
			}
		}, agent.WithResponseFormat(agent.ResponseFormat{Kind: "json"}))
	}

	// SearXNG alone is real web search: without a scraper every source keeps
	// its snippet and is labelled snippet-only. Requiring Firecrawl too sent
	// every run that lacked it to the model for invented findings.
	if cfg.SearXNGURL != "" {
		impl.searchTools = tools.NewSearchTools(cfg.SearXNGURL, cfg.FirecrawlURL, timeout)
		if impl.searchTools.Firecrawl != nil {
			impl.searchTools.Firecrawl.APIKey = cfg.FirecrawlAPIKey
		}
	}
	return impl, nil
}

type impl struct {
	logf func(string)
	// statusf receives streamed-phase progress, which is status rather than
	// history: the live UI shows it in one row replaced in place. Unset, it
	// falls back to logf.
	statusf func(string)
	model   string
	// baseURL is kept so a transport failure can name the endpoint that was
	// actually tried, which is the one thing the reader has to check.
	baseURL string
	timeout time.Duration
	retries int
	// tokens is the provider-reported usage summed across every call. Model
	// calls run in parallel across sub-agents, so it is updated atomically.
	tokens      atomic.Int64
	search      runFunc
	analyzer    runFunc
	factCheck   runFunc
	summarizer  runFunc
	plan        runFunc
	searchTools *tools.SearchTools // non-nil when real search+scrape is available
	// served is every model the provider reported serving, which differs
	// from model when model is a router alias.
	servedMu sync.Mutex
	served   map[string]bool
}

// callStreaming is call with the response streamed and each partial reported
// through the progress logger. Planning is the one phase with nothing on
// screen behind it, so it is the one phase where watching the answer arrive is
// worth the extra request options.
//
// Usage is the catch: an OpenAI-compatible API reports tokens for a streamed
// call only when stream_options.include_usage is set, so streaming without it
// would quietly drop the planner's tokens from the run total.
func (a *impl) callStreaming(ctx context.Context, ag *agent.Agent, prompt string, progress func(partial string), opts ...agent.Option) (string, error) {
	streamOpts := append([]agent.Option{
		agent.Stream(true),
		openaiprovider.ChatCompletionNewParams(openai.ChatCompletionNewParams{
			StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
		}),
	}, opts...)
	report, stop := tickProgress(progress, progressTick)
	report("") // the wait starts now, before the first byte arrives
	out, err := a.callWith(ctx, ag, prompt, report, streamOpts...)
	stop()
	// Not every OpenAI-compatible server honours stream:true — proxies and
	// older local servers answer with a whole JSON body, the stream yields
	// nothing, and the phase would hand back an empty answer with no error at
	// all. Asking again without streaming costs one call on those servers and
	// nothing on the rest.
	if err == nil && strings.TrimSpace(out) == "" {
		a.log("no streamed response; asking again without streaming")
		return a.callWith(ctx, ag, prompt, nil, opts...)
	}
	return out, err
}

// call runs one model call, collecting the response in one step.
func (a *impl) call(ctx context.Context, ag *agent.Agent, prompt string, opts ...agent.Option) (string, error) {
	return a.callWith(ctx, ag, prompt, nil, opts...)
}

// callWith runs one model call under its own deadline, retrying a failure with
// a longer one. A timeout here is usually a model that needed more time than
// the budget — writing the report is the slow call — so each attempt doubles
// the budget rather than repeating a deadline that was already too short.
//
// When progress is non-nil the response stream is drained update by update so
// the partial text can be reported; otherwise it is collected in one step.
func (a *impl) callWith(ctx context.Context, ag *agent.Agent, prompt string, progress func(string), opts ...agent.Option) (string, error) {
	budget, err := a.timeout, error(nil)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			budget *= 2
			a.log(fmt.Sprintf("retrying %s in %s (attempt %d of %d): %v",
				ag.Name(), budget, attempt+1, a.retries+1, err))
			// Wait before trying again, and stay interruptible while waiting:
			// a cancelled run should not sit out a backoff it will not use.
			select {
			case <-time.After(retryWait(err, attempt)):
			case <-ctx.Done():
				return "", fmt.Errorf("agent %s call failed: %w", ag.Name(), err)
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, budget)
		out, callErr := collectResponse(ag.RunText(callCtx, prompt, opts...), progress)
		cancel()
		if callErr == nil {
			a.addUsage(out)
			return out.String(), nil
		}
		err = callErr
		// A request that never reached a server is not a request that needed
		// more time, and the retry budget here buys time: each attempt doubles
		// the deadline. Re-dialling a host that is not there just multiplies
		// the wait before saying so, which is how a typo in the provider URL
		// became a 30-second pause and a syscall.
		if unreachable(err) {
			return "", providerUnreachable(a.baseURL, err)
		}
		// A daily cap will not clear on a retry, only at the next UTC day;
		// retrying it spent the whole doubling budget on a certain failure.
		if strings.Contains(err.Error(), dailyLimitMarker) {
			return "", fmt.Errorf("the provider's daily request limit for %s is reached; it resets at 00:00 UTC"+
				" — add credits or set OPENAI_MODEL to a paid model: %w", a.model, err)
		}
		// The caller gave up (reader cancelled, or the whole run timed out):
		// retrying would only stall a run nobody is waiting for.
		if attempt >= a.retries || ctx.Err() != nil {
			return "", fmt.Errorf("agent %s call failed: %w", ag.Name(), err)
		}
	}
}

// dialTimeout bounds establishing the connection to the provider. Left to the
// operating system, a host that is simply not there is retried at the TCP
// level for tens of seconds before the error surfaces; a misconfigured URL
// should be reported roughly as fast as a refused one.
const dialTimeout = 5 * time.Second

// retryBackoff is the pause before the first retry, growing with each further
// attempt. A provider that answered with 429 or 500 is asking for a moment;
// retrying the instant the error arrives just spends the budget faster.
const retryBackoff = 500 * time.Millisecond

// dailyLimitMarker is how OpenRouter names the free-model daily cap, in the
// body of a 429 and in the error event that replaces one mid-stream. Matching
// provider text is fragile, but it is the only signal the two forms share.
const dailyLimitMarker = "free-models-per-day"

// maxRetryAfter bounds how long a provider's Retry-After can hold a retry.
const maxRetryAfter = time.Minute

// retryWait is the pause before retry attempt n: the growing backoff, or the
// provider's Retry-After when it asks for longer.
func retryWait(err error, attempt int) time.Duration {
	wait := time.Duration(attempt) * retryBackoff
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		if secs, convErr := strconv.Atoi(apiErr.Response.Header.Get("Retry-After")); convErr == nil {
			wait = max(wait, min(time.Duration(secs)*time.Second, maxRetryAfter))
		}
	}
	return wait
}

// providerHTTPClient is the HTTP client for provider calls. It bounds
// connection setup and nothing else: Client.Timeout is deliberately left zero
// because it also caps reading the response body, which would kill a report
// the model is still streaming. The transport is cloned from the default so
// proxy support, pooling and HTTP/2 survive.
func providerHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Transport: tr}
}

// unreachable reports whether err means the request never reached a server:
// the host refused it, had no route, or did not resolve. Errors from a server
// that did answer — a 500, a reset mid-response, a deadline the model blew —
// are not included: those can genuinely succeed on a second attempt.
func unreachable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// Op is "dial" only while the connection is being established, which is
	// exactly the window where no server was reached. This covers connection
	// refused, no route to host and network unreachable on every platform,
	// without naming platform-specific errno values.
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// providerUnreachable turns a transport failure into something the reader can
// act on. The raw error names a URL and a syscall; what matters is which
// endpoint was tried and that the setting pointing at it is the thing to fix.
func providerUnreachable(baseURL string, err error) error {
	reason := "connection failed"
	var opErr *net.OpError
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		reason = "host not found"
	case errors.As(err, &opErr) && opErr.Err != nil:
		// "connect: no route to host" -> "no route to host".
		reason = strings.TrimPrefix(opErr.Err.Error(), "connect: ")
	}
	if baseURL == "" {
		baseURL = "the configured provider"
	}
	return fmt.Errorf("cannot reach the model provider at %s (%s) — "+
		"check OPENAI_BASE_URL and that the server is running", baseURL, reason)
}

// progressInterval throttles streamed progress. The first update always
// reports — that is the one that proves the model has started answering —
// and later ones are spaced so a long phase does not flood the activity feed.
const progressInterval = 5 * time.Second

// progressTick is how often a silent stream is re-checked. It is finer than
// progressInterval so the reporter's own throttle, not the tick phase,
// decides when a line is due: at a 5s tick a 12s stall could pass unreported.
const progressTick = time.Second

// progressUnit measures a partial response in something a reader can judge.
// Characters were the old unit: comparable across nothing, and for the JSON
// phases mostly braces and keys.
//
// ponytail: words and entry counts are proxies. Real token counts arrive only
// in the final chunk (stream_options.include_usage), so none is invented here.
type progressUnit func(partial string) (n int, noun string)

// wordsUnit counts the words of prose — the report.
func wordsUnit(p string) (int, string) {
	n := len(strings.Fields(p))
	return n, pick(n, "word", "words")
}

// entriesUnit counts completed JSON entries by the keys that open them.
func entriesUnit(one, many string, keys ...string) progressUnit {
	return func(p string) (int, string) {
		n := 0
		for _, k := range keys {
			n += strings.Count(p, `"`+k+`"`)
		}
		return n, pick(n, one, many)
	}
}

// pick is the singular or plural noun for n.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// streamProgress reports a streamed phase on the status channel.
func (a *impl) streamProgress(label string, unit progressUnit) func(string) {
	return newStreamReporter(label, unit, time.Now, a.status)
}

// newStreamReporter turns partial responses into progress lines: a ticking
// wait for the first token, the count in the phase's unit with the delta
// since the last line, and a stall notice once the text stops growing. The
// stall line is the one that matters: without it a dead stream looked exactly
// like a slow one until the call deadline gave up minutes later.
//
// It is safe for concurrent use; tickProgress calls it from a ticker.
func newStreamReporter(label string, unit progressUnit, now func() time.Time, log func(string)) func(string) {
	var (
		mu                  sync.Mutex
		start               = now()
		lastLog, lastGrowth time.Time
		logged, texted      bool
		size, shown         int
	)
	return func(partial string) {
		mu.Lock()
		defer mu.Unlock()
		t := now()
		due := !logged || t.Sub(lastLog) >= progressInterval
		say := func(msg string) {
			log(label + " — " + msg)
			lastLog, logged = t, true
		}
		switch {
		case strings.TrimSpace(partial) == "":
			if due {
				say(fmt.Sprintf("waiting for the first token (%ds)", int(t.Sub(start).Seconds())))
			}
		case len(partial) > size:
			size, lastGrowth = len(partial), t
			n, noun := unit(partial)
			if texted && !due {
				return
			}
			delta := ""
			if texted && n > shown {
				delta = fmt.Sprintf(" (+%d in %ds)", n-shown, int(t.Sub(lastLog).Seconds()))
			}
			texted, shown = true, n
			say(fmt.Sprintf("%d %s%s · %s", n, noun, delta, clockTime(t.Sub(start))))
		case due && t.Sub(lastGrowth) >= 2*progressInterval:
			say(fmt.Sprintf("no new text for %ds · %s", int(t.Sub(lastGrowth).Seconds()), clockTime(t.Sub(start))))
		}
	}
}

// clockTime renders elapsed time as m:ss.
func clockTime(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// tickProgress re-reports the last partial every interval until stop. A model
// that sends nothing produces no stream updates at all, so without a ticker
// the wait line froze and the stall notice could never fire. No call to
// progress happens after stop returns.
func tickProgress(progress func(string), every time.Duration) (report func(string), stop func()) {
	var mu sync.Mutex
	last, done := "", make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				select {
				case <-done:
				default:
					progress(last)
				}
				mu.Unlock()
			}
		}
	}()
	var once sync.Once
	report = func(p string) {
		mu.Lock()
		defer mu.Unlock()
		last = p
		progress(p)
	}
	return report, func() { once.Do(func() { mu.Lock(); close(done); mu.Unlock() }) }
}

// collectResponse drains a response stream into a Response, reporting the text
// accumulated so far after each update. It mirrors ResponseStream.Collect,
// which offers no hook of its own.
func collectResponse(stream agent.ResponseStream, progress func(string)) (*agent.Response, error) {
	if progress == nil {
		return stream.Collect()
	}
	var resp agent.Response
	for update, err := range stream {
		if err != nil {
			return nil, err
		}
		resp.Update(update)
		progress(resp.String())
	}
	resp.Coalesce()
	return &resp, nil
}

// addUsage folds one response's provider-reported token usage into the running
// total. Providers that report no usage add nothing rather than an estimate.
func (a *impl) addUsage(resp *agent.Response) {
	u := resp.Usage()
	n := u.TotalTokenCount
	if n == 0 {
		// Some OpenAI-compatible backends omit total_tokens but still send the
		// two halves.
		n = u.InputTokenCount + u.OutputTokenCount
	}
	a.tokens.Add(n)
}

// ModelInfo names the configured model and the provider's host. The host is
// taken alone so credentials in the URL never reach an artifact. The models
// a router actually served are ServedModels.
func (a *impl) ModelInfo() (model, provider string) {
	if u, err := url.Parse(a.baseURL); err == nil {
		provider = u.Hostname()
	}
	return a.model, provider
}

// servedModelRE matches the top-level "model" field of a completion or of a
// stream chunk. Model output inside the content is JSON-escaped (\"model\"),
// so it cannot match.
var servedModelRE = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)

// maxSniff bounds how much of a response is searched for its model field,
// which providers put before the content.
const maxSniff = 8 << 10

// sniffServedModel is HTTP middleware that records the model a successful
// response names. The agent framework drops that field, and with a router
// alias it is the only record of which model wrote the run.
func (a *impl) sniffServedModel(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err == nil && resp != nil && resp.StatusCode == http.StatusOK && resp.Body != nil {
		resp.Body = &modelSniffer{ReadCloser: resp.Body, record: a.addServed}
	}
	return resp, err
}

func (a *impl) addServed(model string) {
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	if a.served == nil {
		a.served = map[string]bool{}
	}
	a.served[model] = true
}

// ServedModels lists, sorted, every model the provider reported serving.
func (a *impl) ServedModels() []string {
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	return slices.Sorted(maps.Keys(a.served))
}

// modelSniffer passes a body through, recording the first model field in its
// opening bytes.
type modelSniffer struct {
	io.ReadCloser
	head   []byte
	done   bool
	record func(string)
}

func (s *modelSniffer) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if !s.done && n > 0 {
		s.head = append(s.head, p[:n]...)
		if m := servedModelRE.FindSubmatch(s.head); m != nil {
			s.record(string(m[1]))
			s.done, s.head = true, nil
		} else if len(s.head) > maxSniff {
			s.done, s.head = true, nil
		}
	}
	return n, err
}

// TokensUsed reports the running provider-reported token total.
func (a *impl) TokensUsed() int { return int(a.tokens.Load()) }

// SetProgress wires the progress logger for every model call and, when real
// search+scrape is available, for each search/scrape sub-step.
func (a *impl) SetSourceBudget(perQuery int) {
	if a.searchTools != nil && perQuery > 0 {
		a.searchTools.MaxURLsPerQuery = perQuery
	}
}

func (a *impl) SetProgress(logf func(string)) {
	a.logf = logf
	if a.searchTools != nil {
		a.searchTools.SetLogger(logf)
	}
}

func (a *impl) log(msg string) {
	if a.logf != nil {
		a.logf(msg)
	}
}

// SetStatus wires the channel for transient progress. It is not part of
// Assistant: a caller that has no status row simply never sets it, and the
// lines reach the progress logger instead.
func (a *impl) SetStatus(f func(string)) { a.statusf = f }

func (a *impl) status(msg string) {
	if a.statusf != nil {
		a.statusf(msg)
		return
	}
	a.log(msg)
}

// ResearchDetail performs a web search and enriches results with scrape
// signals when search tools are configured; otherwise it uses the LLM search
// agent, whose findings are marked unverified because no URL in them was ever
// fetched.
//
// A configured search that fails is an error, not a cue to ask the model
// instead. The fallback turned every rate limit and outage into invented
// findings that counted as sources, and with public instances a refusal is
// routine: "it ran and produced a report" has to mean the same thing whether
// or not the search answered.
func (a *impl) ResearchDetail(ctx context.Context, query string) (*ResearchDetail, error) {
	if a.searchTools != nil {
		res, err := a.searchTools.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		det := &ResearchDetail{
			Findings: make([]Finding, len(res.Findings)),
			Signals:  res.Signals,
			Skipped:  res.Skipped,
		}
		for i, f := range res.Findings {
			det.Findings[i] = Finding{Query: query, Title: f.Title, Content: f.Content, URL: f.URL, Confidence: f.Confidence}
		}
		return det, nil
	}
	a.log("  llm agent searching")
	out, err := a.search(ctx, query)
	if err != nil {
		return nil, err
	}
	findings := parseSearchResults(out, query)
	signals := make([]SourceSignal, len(findings))
	for i, f := range findings {
		// Nothing was fetched here: the model supplied both the finding and
		// its URL. Claiming "ok" made the UI render "✓ domain 200 OK" for a
		// page that was never requested, and might not exist.
		signals[i] = SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
	}
	return &ResearchDetail{Findings: findings, Signals: signals}, nil
}

func (a *impl) Analyze(ctx context.Context, prompt string) (*Analysis, error) {
	a.log(fmt.Sprintf("%-10s  %s", "analyze", a.model))
	out, err := a.analyzer(ctx, prompt)
	if err != nil {
		return nil, err
	}
	return parseAnalysis(out), nil
}

func (a *impl) FactCheck(ctx context.Context, claims string) (*FactCheckResult, error) {
	a.log(fmt.Sprintf("%-10s  %s", "fact-check", a.model))
	// The prompt arrives fully composed: the caller is the only thing that
	// knows the answer AND the findings the claims must be checked against.
	out, err := a.factCheck(ctx, claims)
	if err != nil {
		return nil, err
	}
	return parseFactCheck(out), nil
}

func (a *impl) Summarize(ctx context.Context, prompt string) (*Summary, error) {
	a.log(fmt.Sprintf("%-10s  %s", "summarize", a.model))
	out, err := a.summarizer(ctx, prompt)
	if err != nil {
		return nil, err
	}
	out = stripCodeFence(strings.TrimSpace(out))
	return &Summary{Report: out, Executive: extractExecutive(out)}, nil
}

// Local returns an offline assistant that makes no API calls.
func Local() Assistant { return &local{} }

type local struct{}

func (l *local) Analyze(ctx context.Context, prompt string) (*Analysis, error) {
	return &Analysis{Answer: "offline mode: no analysis performed", Confidence: "low"}, nil
}

// FactCheck offline verifies nothing: no source was ever fetched and no model
// was ever asked. It says so instead of stamping the claims "verified", which
// the exports then presented as an established fact.
func (l *local) FactCheck(ctx context.Context, claims string) (*FactCheckResult, error) {
	return &FactCheckResult{
		Unverified: []string{"offline mode: no fact-check was performed"},
	}, nil
}

// Summarize offline writes no report. Returning the prompt made the composed
// summarizer input — question, findings, instructions — read as a real report
// in every artifact.
func (l *local) Summarize(ctx context.Context, prompt string) (*Summary, error) {
	return &Summary{
		Report:     "offline mode: no report was written; the pipeline ran against a stub assistant that makes no network calls.",
		Executive:  "offline mode",
		Confidence: "low",
	}, nil
}

func (l *local) Plan(ctx context.Context, question string, subTopics int) ([]SubTopic, error) {
	// Offline planning falls back to the same canonical set of sub-topics the
	// online planner uses when the model returns nothing usable.
	return defaultSubTopics(question), nil
}

func (l *local) ResearchDetail(ctx context.Context, query string) (*ResearchDetail, error) {
	return &ResearchDetail{
		Findings: []Finding{{Query: query, Title: "offline", Content: "offline mode: no API calls made", Confidence: "low"}},
		Signals:  []SourceSignal{{URL: "", Domain: "offline", Status: "unverified"}},
	}, nil
}

func (l *local) SetSourceBudget(int) {}

func (l *local) SetProgress(func(string)) {}

// TokensUsed is always zero offline: no model call is ever made, so there is
// no usage to report and none is invented.
func (l *local) TokensUsed() int { return 0 }

const defaultPlanningInstructions = `You are a research planner. Decompose the following question into exactly %d focused, non-overlapping research sub-topics that together answer it. For each sub-topic give a name and a single sentence describing what to investigate. The name is a short human-readable title of 2-5 words in Title Case ("Benchmark Performance"), never an identifier: no underscores, no snake_case, no camelCase. Write every name and note in English. Respond ONLY with JSON of the shape: {"subtopics":[{"name":"...","notes":"..."}]}` //nolint:lll

// planPrompt builds the planner prompt for a given plan breadth.
func planPrompt(question string, subTopics int) string {
	if subTopics < 1 {
		subTopics = defaultSubTopicCount
	}
	return fmt.Sprintf(defaultPlanningInstructions, subTopics) + "\n\nQuestion: " + question
}

// defaultSubTopicCount is the breadth used when a caller names none.
const defaultSubTopicCount = 4

// Plan implements the planning phase of the pipeline.
func (a *impl) Plan(ctx context.Context, question string, subTopics int) ([]SubTopic, error) {
	// The deadline is part of the answer to "how long will this sit here":
	// planning is one call with nothing on screen behind it.
	a.log(fmt.Sprintf("asking %s for sub-topics (up to %s)", a.model, a.timeout))
	out, err := a.plan(ctx, planPrompt(question, subTopics))
	if err != nil {
		return nil, err
	}
	topics := parsePlanAgent(out)
	if len(topics) == 0 {
		return defaultSubTopics(question), nil
	}
	// A requested count is a suggestion to a model, not a constraint. Running
	// every extra branch it volunteers would spend a budget the caller sized
	// for fewer of them.
	if subTopics > 0 && len(topics) > subTopics {
		topics = topics[:subTopics]
	}
	return topics, nil
}

// parsePlanAgent decodes the planner's JSON output. It tolerates a wrapped
// object, a bare array, or JSON surrounded by prose. Unstructured text is
// rejected so Plan can fall back to the original question.
func parsePlanAgent(out string) []SubTopic {
	out = strings.TrimSpace(out)
	payloads := []string{out}
	if i := strings.Index(out, "["); i >= 0 {
		if j := strings.LastIndex(out, "]"); j > i {
			payloads = append(payloads, out[i:j+1])
		}
	}
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			payloads = append(payloads, out[i:j+1])
		}
	}
	for _, payload := range payloads {
		var obj struct {
			Subtopics []SubTopic `json:"subtopics"`
			Topics    []SubTopic `json:"topics"`
			Items     []SubTopic `json:"items"`
		}
		if err := json.Unmarshal([]byte(payload), &obj); err == nil {
			if n := len(obj.Subtopics); n > 0 {
				return finalizeSubTopics(obj.Subtopics)
			}
			if n := len(obj.Topics); n > 0 {
				return finalizeSubTopics(obj.Topics)
			}
			if n := len(obj.Items); n > 0 {
				return finalizeSubTopics(obj.Items)
			}
		}
		var arr []SubTopic
		if err := json.Unmarshal([]byte(payload), &arr); err == nil && len(arr) > 0 {
			return finalizeSubTopics(arr)
		}
	}
	return nil
}

func finalizeSubTopics(in []SubTopic) []SubTopic {
	out := make([]SubTopic, 0, len(in))
	used := map[string]bool{}
	for _, t := range in {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		// A repeated ID does the same damage as a missing one: the live tree
		// and the exported per-branch notes are keyed by it, so two sub-agents
		// sharing an ID collapse into a single branch and the run reports
		// fewer sub-topics than it researched. Models repeat IDs freely.
		if t.ID == "" || used[t.ID] {
			t.ID = unusedTopicID(used, len(out)+1)
		}
		used[t.ID] = true
		t.Name = humanizeTopic(t.Name)
		t.Notes = strings.TrimSpace(t.Notes)
		out = append(out, t)
	}
	return out
}

// unusedTopicID returns the first positional ID at or after n that no earlier
// sub-topic claimed.
func unusedTopicID(used map[string]bool, n int) string {
	for ; ; n++ {
		if id := strconv.Itoa(n); !used[id] {
			return id
		}
	}
}

// titleLowerWords stay lowercase inside a title unless they lead it.
var titleLowerWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "but": true,
	"by": true, "for": true, "in": true, "of": true, "on": true, "or": true,
	"the": true, "to": true, "vs": true, "with": true,
}

// humanizeTopic turns a planner sub-topic name into a readable title.
//
// Models asked for JSON routinely answer with identifier-style names
// ("model_overview_and_background"). That is not only ugly in the brief: the
// name is appended to the research question as the search-query facet, and
// search backends tokenize an underscored blob far worse than they do words.
func humanizeTopic(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// An underscore is never part of a real topic label, so it always splits.
	// A hyphen usually is ("state-of-the-art", "GPT-4"), so it only splits an
	// unambiguous kebab-case identifier: all lowercase, no spaces, no
	// underscores, and hyphenated more than once.
	s = strings.ReplaceAll(s, "_", " ")
	if !strings.ContainsAny(s, " ") && strings.Count(s, "-") > 1 &&
		s == strings.ToLower(s) {
		s = strings.ReplaceAll(s, "-", " ")
	}

	words := strings.Fields(s)
	for i, w := range words {
		// Anything already carrying capitals is the model's own casing —
		// an acronym or a product name — and is left exactly as it is.
		if w != strings.ToLower(w) {
			continue
		}
		if i > 0 && titleLowerWords[w] {
			continue
		}
		words[i] = upperFirst(w)
	}
	return strings.Join(words, " ")
}

// upperFirst capitalises the first character of w.
//
// Slicing the first byte instead splits any multi-byte rune down the middle:
// "\u00e9lan" became mojibake in the brief, the tree and the exported notes,
// because strings.ToUpper over half a rune yields U+FFFD and the orphaned
// continuation byte stayed behind. The planner is asked for English, so this
// is rare — but corrupting a name is worse than leaving it uncapitalised.
func upperFirst(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	if r == utf8.RuneError {
		return w
	}
	return strings.ToUpper(string(r)) + w[n:]
}

// defaultSubTopics is the fallback used when the planner returns nothing
// usable. Sub-topic names are fed straight to the search backend as queries,
// so the fallback is anchored on the question itself: question-independent
// headings would search for the heading text and never for what was asked.
func defaultSubTopics(question string) []SubTopic {
	q := strings.TrimSpace(question)
	if q == "" {
		return nil
	}
	return []SubTopic{{ID: "1", Name: q}}
}

// parseSearchResults extracts findings from the search agent's output.
// Models are inconsistent about the wrapper: a bare array, {"findings":...},
// {"results":...}, or JSON buried in prose — so several shapes are tried
// before returning no findings. Prose and error messages are not evidence.
func parseSearchResults(out string, query string) []Finding {
	for _, payload := range jsonPayloads(out) {
		if findings := tryUnmarshalFindings(payload, query); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

// jsonPayloads returns the whole output plus its outermost JSON array and
// object substrings, so a payload wrapped in prose is still found.
func jsonPayloads(out string) []string {
	payloads := []string{out}
	if i := strings.Index(out, "["); i >= 0 {
		if j := strings.LastIndex(out, "]"); j > i {
			payloads = append(payloads, out[i:j+1])
		}
	}
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			payloads = append(payloads, out[i:j+1])
		}
	}
	return payloads
}

// tryUnmarshalFindings decodes a payload as a findings array directly or as
// an object wrapping the array under a common key.
func tryUnmarshalFindings(payload, query string) []Finding {
	var direct []Finding
	if err := json.Unmarshal([]byte(payload), &direct); err == nil && len(direct) > 0 {
		return finalizeFindings(direct, query)
	}
	var wrapped struct {
		Findings []Finding `json:"findings"`
		Results  []Finding `json:"results"`
	}
	if err := json.Unmarshal([]byte(payload), &wrapped); err == nil {
		if len(wrapped.Findings) > 0 {
			return finalizeFindings(wrapped.Findings, query)
		}
		if len(wrapped.Results) > 0 {
			return finalizeFindings(wrapped.Results, query)
		}
	}
	return nil
}

// finalizeFindings stamps each finding with its query and drops the ones with
// no text. A title and a URL are not evidence: accepting {"title":"T"} counted
// a source for free and sent a blank entry to every later phase.
func finalizeFindings(findings []Finding, query string) []Finding {
	out := findings[:0]
	for _, f := range findings {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		f.Query = query
		if f.Confidence == "" {
			f.Confidence = "medium"
		}
		out = append(out, f)
	}
	return out
}

// parseAnalysis tolerates non-JSON model output by treating it as the answer.
func parseAnalysis(out string) *Analysis {
	m, ok := parseJSONObject(out)
	if !ok {
		return &Analysis{Answer: strings.TrimSpace(out), Gaps: []string{}, Confidence: "medium"}
	}
	return &Analysis{
		Answer:     getString(m, "answer", strings.TrimSpace(out)),
		Gaps:       getStringSlice(m, "gaps", []string{}),
		Confidence: getString(m, "confidence", "medium"),
		FollowUp:   followUps(m),
		Topics:     parseTopics(m["topics"]),
	}
}

// Structured phases may wrap their JSON in explanatory prose. Decode those
// payloads separately from report fence cleanup, which must preserve prose.
func parseJSONObject(out string) (map[string]any, bool) {
	for _, payload := range jsonPayloads(out) {
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err == nil {
			return m, true
		}
	}
	return nil, false
}

// followUps reads the analyzer's suggested next queries. The prompt asks for
// "follow_up_queries" and the struct is tagged "follow_up", so both spellings
// are accepted rather than silently yielding an empty list for the one the
// model was actually told to use.
func followUps(m map[string]any) []string {
	if v := getStringSlice(m, "follow_up", nil); len(v) > 0 {
		return v
	}
	return getStringSlice(m, "follow_up_queries", []string{})
}

// stripCodeFence removes a markdown code fence (```lang ... ```) when a
// model wraps its whole output in one.
func stripCodeFence(out string) string {
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "```") {
		return out
	}
	opener, body, ok := strings.Cut(out, "\n")
	if !ok {
		return out
	}
	fence := opener[:len(opener)-len(strings.TrimLeft(opener, "`"))]
	// Only unwrap an outer fence. Reports often contain fenced examples;
	// searching for the first fence anywhere discards the rest of the report.
	last := strings.LastIndex(body, "\n") + 1
	if strings.TrimSpace(body[last:]) == fence {
		return strings.TrimSpace(body[:last])
	}
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == fence {
			// A leading example followed by prose is part of the report.
			return out
		}
	}
	// A model that opened a fence and never closed it still meant everything
	// after the opener as the body. Returning the input unchanged shipped the
	// literal ```markdown line at the top of the report and of the executive
	// summary extracted from it.
	return strings.TrimSpace(body)
}

func parseFactCheck(out string) *FactCheckResult {
	m, ok := parseJSONObject(out)
	if !ok {
		// The verification pass produced something that is not a verdict.
		// Nothing in it was checked, so it is recorded as unverified: calling
		// a raw model dump one big "verified" claim is the opposite of what
		// this phase exists to establish.
		text := strings.TrimSpace(out)
		if text == "" {
			return &FactCheckResult{}
		}
		return &FactCheckResult{Unverified: []string{text}}
	}
	return &FactCheckResult{
		Verified:       parseVerified(m["verified"]),
		Unverified:     parseUnverified(m["unverified"]),
		Contradictions: parseContradictions(m["contradictions"]),
	}
}

// parseUnverified reads the unverified list, which models return either as
// plain strings or as {"claim":..., "reason":...} objects — the shape the
// fact-checker prompt itself asks for. Decoding it as strings only turned
// every object into an empty entry.
func parseUnverified(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		switch t := item.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			claim := strings.TrimSpace(getString(t, "claim", ""))
			reason := strings.TrimSpace(getString(t, "reason", ""))
			switch {
			case claim != "" && reason != "":
				out = append(out, claim+" — "+reason)
			case claim != "":
				out = append(out, claim)
			case reason != "":
				out = append(out, reason)
			}
		}
	}
	return out
}

// IsEmphasizedHeading reports whether line is a section heading written as
// emphasis ("**Executive Summary**") rather than as a Markdown heading. Models
// write headings both ways, and the difference decides whether the report's
// opening line is the summary or merely the label above it. A wrapped line
// that reads as a sentence — many words, or sentence-ending punctuation — is
// prose that happens to be bold, so it stays prose.
func IsEmphasizedHeading(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) <= 4 || !strings.HasSuffix(line, line[:2]) {
		return false
	}
	if !strings.HasPrefix(line, "**") && !strings.HasPrefix(line, "__") {
		return false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(line[2:len(line)-2], ":"))
	if inner == "" || strings.ContainsAny(inner, "*_") || len(strings.Fields(inner)) > 6 {
		return false
	}
	return !strings.ContainsAny(inner[len(inner)-1:], ".!?")
}

// extractExecutive pulls the report's opening paragraph as its executive
// summary. It stops at a paragraph break rather than a fixed line count, so a
// six-line opening is not cut mid-sentence, and it skips the leading Markdown
// heading, which is the report's title and not part of the summary.
func extractExecutive(report string) string {
	// A summary long enough to need a cap is no longer a summary; the ellipsis
	// says the reader is looking at a prefix.
	const maxLines = 12

	var out []string
	for _, ln := range strings.Split(report, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || IsEmphasizedHeading(t) {
			if len(out) > 0 {
				return strings.Join(out, "\n")
			}
			continue // still in the leading blank lines / title
		}
		if len(out) == maxLines {
			return strings.Join(out, "\n") + "\n…"
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func getString(m map[string]any, key, fallback string) string {
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return fallback
}

func getStringSlice(m map[string]any, key string, fallback []string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return fallback
	}
	result := make([]string, len(arr))
	for i, item := range arr {
		result[i], _ = item.(string)
	}
	return result
}

func parseTopics(v any) []Topic {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var topics []Topic
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			topics = append(topics, Topic{
				Name:       getString(m, "name", ""),
				Findings:   getStringSlice(m, "findings", nil),
				Confidence: getString(m, "confidence", "medium"),
			})
		}
	}
	return topics
}

func parseVerified(v any) []VerifiedClaim {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var claims []VerifiedClaim
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			claims = append(claims, VerifiedClaim{
				Claim:    getString(m, "claim", ""),
				Verified: getBool(m, "verified", false),
				Evidence: getString(m, "evidence", ""),
			})
		}
	}
	return claims
}

func parseContradictions(v any) []Contradiction {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []Contradiction
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, Contradiction{
				Claim:   getString(m, "claim", ""),
				Sources: getStringSlice(m, "sources", nil),
			})
		}
	}
	return out
}

func getBool(m map[string]any, key string, fallback bool) bool {
	if b, ok := m[key].(bool); ok {
		return b
	}
	return fallback
}
