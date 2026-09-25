package agent

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

func TestMalformedOutputDoesNotInventResearch(t *testing.T) {
	for _, out := range []string{"I cannot research this question.", "Here are the topics:\n- first topic\n- second topic", "{\n\"subtopics\": [\ninvalid output", "{\"error\":\"service unavailable\"}"} {
		if got := parsePlanAgent(out); len(got) != 0 {
			t.Errorf("%q invented topics: %+v", out, got)
		}
		if got := parseSearchResults(out, "q"); len(got) != 0 {
			t.Errorf("%q invented findings: %+v", out, got)
		}
		a := &impl{plan: func(context.Context, string) (string, error) { return out, nil }}
		got, err := a.Plan(context.Background(), "original question", 3)
		if err != nil || len(got) != 1 || got[0].Name != "original question" || got[0].ID == "" {
			t.Errorf("fallback must research original question: %+v, %v", got, err)
		}
	}
}

// Sub-topic IDs key the live tree and the exported per-branch notes, so a
// planner that repeats an ID — or leaves one blank — collapses two sub-agents
// into a single branch. Filling only the blanks left the repeats intact.
func TestPlannerSubTopicIDsAreUnique(t *testing.T) {
	for _, out := range []string{
		`[{"id":"1","name":"Alpha"},{"id":"1","name":"Beta"},{"name":"Gamma"}]`,
		`{"subtopics":[{"id":"2","name":"Alpha"},{"name":"Beta"},{"id":"2","name":"Gamma"}]}`,
	} {
		got := parsePlanAgent(out)
		if len(got) != 3 {
			t.Fatalf("%s: parsed %d sub-topics, want 3", out, len(got))
		}
		seen := map[string]bool{}
		for _, sub := range got {
			if sub.ID == "" || seen[sub.ID] {
				t.Errorf("%s: repeated or empty ID %q in %+v", out, sub.ID, got)
			}
			seen[sub.ID] = true
		}
	}
}

// Models write the executive heading as bold text about as often as they write
// it as a Markdown heading. Treating the bold line as prose made it the
// "executive summary" the card and the JSON sidecar reported.
func TestExecutiveSkipsEmphasizedHeading(t *testing.T) {
	for _, heading := range []string{"**Executive Summary**", "__Resumen ejecutivo__", "**Résumé**"} {
		report := "# Title\n\n" + heading + "\n\nOpening paragraph.\n\n## Details\n\nMore evidence."
		if got := extractExecutive(report); got != "Opening paragraph." {
			t.Errorf("%s: executive = %q, want the opening paragraph", heading, got)
		}
	}
	// A fully emphasized sentence is still the summary when nothing follows it
	// as a separate paragraph.
	if got := extractExecutive("**The whole summary is bold.**"); got != "**The whole summary is bold.**" {
		t.Errorf("dropped an emphasized opening paragraph: %q", got)
	}
}

// A real DeepSeek report contained a Go example. Fence cleanup extracted that
// first example and silently discarded every heading, conclusion and citation.
func TestSummarizePreservesCodeExamplesInsideReport(t *testing.T) {
	report := "# Mutex vs RWMutex\n\n## Executive Summary\n\nChoose based on measured contention.\n\n```go\ntype Mutex struct {\n    state int32\n}\n```\n\n## Conclusion\n\nBenchmark your workload ([source](https://go.dev/doc/))."
	for _, output := range []string{report, "```markdown\n" + report + "\n```", "````markdown\n" + report + "\n````"} {
		a := &impl{summarizer: func(context.Context, string) (string, error) { return output, nil }}
		got, err := a.Summarize(context.Background(), "prompt")
		if err != nil {
			t.Fatal(err)
		}
		if got.Report != report {
			t.Errorf("report lost text or code fences:\n%s", got.Report)
		}
		if got.Executive != "Choose based on measured contention." {
			t.Errorf("wrong executive: %q", got.Executive)
		}
	}
}

// Off-topic results the search tools rejected must reach the caller. The run
// reports what it ignored; dropping the record makes a filtered search
// indistinguishable from a search that found little.
func TestResearchDetailReportsSkippedSources(t *testing.T) {
	sx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[
		 {"title":"Wholesale nursery price list","url":"https://viveros.example/1","content":"average wholesale prices for cut stems"},
		 {"title":"The sync package, end to end","url":"https://gosnippets.example/2","content":"sync.Mutex and sync.RWMutex"}]}`)
	}))
	defer sx.Close()
	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{"markdown":"page text","metadata":{"title":"The sync package, end to end","statusCode":200}}}`)
	}))
	defer fc.Close()

	a := &impl{searchTools: tools.NewSearchTools(sx.URL, fc.URL, 0)}
	det, err := a.ResearchDetail(context.Background(), "sync.Mutex vs sync.RWMutex in Go")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Findings) != 1 || det.Findings[0].URL != "https://gosnippets.example/2" {
		t.Fatalf("off-topic result reached the model: %+v", det.Findings)
	}
	if len(det.Skipped) != 1 || det.Skipped[0].URL != "https://viveros.example/1" {
		t.Fatalf("skipped sources not reported: %+v", det.Skipped)
	}
}

// A model call that never reached a server is not a call that needed more
// time. Retrying it re-dials a host that is not there, and because each retry
// doubles the *timeout* it buys nothing but delay: an unreachable provider
// took ~30s to report "no route to host" three times over.
func TestUnreachableProviderFailsWithoutRetrying(t *testing.T) {
	for _, err := range []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: no route to host")},
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
		&net.DNSError{Err: "no such host", Name: "provider.invalid"},
		fmt.Errorf("Post %q: %w", "http://host:1234/v1/chat/completions",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: network is unreachable")}),
	} {
		if !unreachable(err) {
			t.Errorf("not treated as unreachable: %v", err)
		}
	}
	// A slow or failing model is a different thing: those keep their retries.
	for _, err := range []error{
		context.DeadlineExceeded,
		errors.New("500 Internal Server Error"),
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")},
	} {
		if unreachable(err) {
			t.Errorf("wrongly treated as unreachable: %v", err)
		}
	}
}

// The raw Go error names a URL and a syscall and says nothing about what to
// do. The endpoint is the one thing the reader has to check.
func TestUnreachableProviderErrorNamesTheEndpoint(t *testing.T) {
	err := providerUnreachable("http://192.0.2.10:1234",
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: no route to host")})
	msg := err.Error()
	for _, want := range []string{"http://192.0.2.10:1234", "OPENAI_BASE_URL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "dial tcp") || strings.Contains(msg, "Post \"") {
		t.Errorf("message still carries raw transport detail: %q", msg)
	}
}

// End to end through the real client: a dead endpoint must fail on the first
// attempt, with no retry logged, and report the endpoint.
func TestPlanAgainstDeadEndpointFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close() // nothing is listening now: connection refused

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: dead, OpenAIModel: "m",
		ModelCallTimeout: 30 * time.Second, ModelCallRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	a.SetProgress(func(msg string) { logged = append(logged, msg) })

	start := time.Now()
	_, err = a.Plan(context.Background(), "question", 3)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error against a dead endpoint")
	}
	for _, line := range logged {
		if strings.Contains(line, "retrying") {
			t.Errorf("retried an unreachable endpoint: %q", line)
		}
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s to report an unreachable endpoint", elapsed)
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("error does not name the endpoint: %v", err)
	}
}

// The provider client caps how long establishing a connection may take, so an
// unreachable host is reported in seconds rather than after the OS gives up.
// Client.Timeout must stay zero: it also bounds reading the response body, and
// a report the model is still streaming would die mid-write.
func TestProviderClientCapsDialWithoutCappingTheResponse(t *testing.T) {
	c := providerHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("client timeout %s would cut off a streaming response", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want a cloned *http.Transport", c.Transport)
	}
	if tr.DialContext == nil {
		t.Fatal("transport does not bound connection setup")
	}
	// Proxy support and pooling come from the default transport; a bare
	// &http.Transport{} would silently drop them.
	if tr.Proxy == nil {
		t.Error("transport lost proxy support")
	}
	start := time.Now()
	// TEST-NET-1 (RFC 5737) is not routable, so the dial hangs until it is cut off.
	_, err := tr.DialContext(context.Background(), "tcp", "192.0.2.1:1234")
	if err == nil {
		t.Fatal("expected a dial failure to an unroutable address")
	}
	if elapsed := time.Since(start); elapsed > dialTimeout+3*time.Second {
		t.Errorf("dial took %s, want it bounded near %s", elapsed, dialTimeout)
	}
}

// Retry policy belongs in one place. The provider SDK retries on its own by
// default, underneath impl.call's loop, so the two multiplied: a failing call
// could make nine HTTP attempts, and an unreachable host was re-dialled three
// times even once this layer stopped retrying it.
func TestRetriesAreNotMultipliedBySDK(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "upstream boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m",
		ModelCallTimeout: 10 * time.Second, ModelCallRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = a.Plan(context.Background(), "question", 3); err == nil {
		t.Fatal("expected an error from a failing provider")
	}
	// ModelCallRetries: 1 means exactly two attempts, not two times the SDK's.
	if got := attempts.Load(); got != 2 {
		t.Errorf("provider saw %d requests, want 2", got)
	}
	// A server-side failure is still worth retrying, but not instantly.
	if elapsed := time.Since(start); elapsed < retryBackoff {
		t.Errorf("retried after %s, want at least %s of backoff", elapsed, retryBackoff)
	}
}

// The dial cap must bound only connection setup. Writing a report takes far
// longer than any dial should, so a response that arrives well past
// dialTimeout has to survive intact — this is the failure the zero
// Client.Timeout exists to prevent, exercised through the real client.
func TestSlowResponseSurvivesTheDialCap(t *testing.T) {
	const want = "an answer that took its time"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(dialTimeout + time.Second)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, want)
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m",
		ModelCallTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Summarize(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("slow response failed: %v", err)
	}
	if !strings.Contains(got.Report, want) {
		t.Errorf("slow response truncated: %q", got.Report)
	}
}

// Planning is one call with nothing on screen behind it, so the wait is the
// whole of the dead air before the brief. Streaming it lets the run say how
// much of the plan has arrived — but only if usage survives: an
// OpenAI-compatible API reports tokens in a stream solely when asked to, and
// losing them would silently zero the run's token count.
func TestStreamedPlanReportsProgressAndKeepsUsage(t *testing.T) {
	var streamed, askedForUsage atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		streamed.Store(strings.Contains(string(body), `"stream":true`))
		askedForUsage.Store(strings.Contains(string(body), `"include_usage":true`))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"subtopics":[{"name":"Core Semantics","notes":"n"}`,
			`,{"name":"Performance","notes":"n"}`,
			`,{"name":"Pitfalls","notes":"n"}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22,\"total_tokens\":33}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m",
		ModelCallTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var progress []string
	a.SetProgress(func(msg string) { mu.Lock(); progress = append(progress, msg); mu.Unlock() })

	topics, err := a.Plan(context.Background(), "question", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !streamed.Load() {
		t.Error("planning call was not streamed, so nothing can be reported while it runs")
	}
	if !askedForUsage.Load() {
		t.Error("stream did not request usage, which is how the token count goes missing")
	}
	if len(topics) != 3 || topics[0].Name != "Core Semantics" {
		t.Fatalf("streamed plan not assembled: %+v", topics)
	}
	if got := a.TokensUsed(); got != 33 {
		t.Errorf("streamed call reported %d tokens, want the provider's 33", got)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawPartial bool
	for _, msg := range progress {
		if strings.Contains(msg, "sub-topics so far") {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Errorf("no progress reported while the plan streamed: %q", progress)
	}
}

// Analyze, fact-check and summarize are the long calls — a live analyze ran
// past the two-minute deadline — and each showed one line when it started and
// nothing until it finished or retried. Streaming them lets a slow phase prove
// it is alive, and usage has to survive the switch as it does for planning.
func TestPostResearchPhasesStreamProgress(t *testing.T) {
	for _, phase := range []string{"analyze", "fact-check", "summarize"} {
		t.Run(phase, func(t *testing.T) {
			var streamed, askedForUsage atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				streamed.Store(strings.Contains(string(body), `"stream":true`))
				askedForUsage.Store(strings.Contains(string(body), `"include_usage":true`))
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				for _, c := range []string{`{"answer":"an ans`, `wer","gaps":[],"confidence":"high"}`} {
					fmt.Fprintf(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\n", c)
					if flusher != nil {
						flusher.Flush()
					}
				}
				fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m",
				ModelCallTimeout: 20 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var progress []string
			a.SetProgress(func(msg string) { mu.Lock(); progress = append(progress, msg); mu.Unlock() })

			switch phase {
			case "analyze":
				_, err = a.Analyze(context.Background(), "prompt")
			case "fact-check":
				_, err = a.FactCheck(context.Background(), "claims")
			case "summarize":
				_, err = a.Summarize(context.Background(), "prompt")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !streamed.Load() || !askedForUsage.Load() {
				t.Errorf("streamed=%v usageRequested=%v, want both", streamed.Load(), askedForUsage.Load())
			}
			if got := a.TokensUsed(); got != 12 {
				t.Errorf("reported %d tokens, want the provider's 12", got)
			}
			mu.Lock()
			defer mu.Unlock()
			var sawProgress bool
			for _, msg := range progress {
				if strings.Contains(msg, "so far") {
					sawProgress = true
				}
			}
			if !sawProgress {
				t.Errorf("no progress reported while the phase streamed: %q", progress)
			}
		})
	}
}

// Not every OpenAI-compatible server honours stream:true — proxies and older
// local servers answer with a whole JSON body instead. The stream then yields
// nothing, and a phase that silently returns an empty answer would put an
// empty report on disk. A streamed call that comes back empty is retried
// without streaming.
func TestStreamedCallFallsBackWhenServerDoesNotStream(t *testing.T) {
	var streamedAttempts, plainAttempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			streamedAttempts.Add(1)
		} else {
			plainAttempts.Add(1)
		}
		// Always answer as a non-streaming server would.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"the whole answer"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m",
		ModelCallTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Summarize(context.Background(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Report, "the whole answer") {
		t.Errorf("lost the answer against a non-streaming server: %q", got.Report)
	}
	if streamedAttempts.Load() != 1 || plainAttempts.Load() != 1 {
		t.Errorf("attempts: streamed=%d plain=%d, want one of each",
			streamedAttempts.Load(), plainAttempts.Load())
	}
}

// A reasoning model streams for a long time before any answer text appears.
// The wait is worth saying once; saying "0 characters so far" every few
// seconds reads as a broken counter.
func TestStreamProgressReportsTheWaitOnceThenGrowth(t *testing.T) {
	var got []string
	a := &impl{logf: func(msg string) { got = append(got, msg) }}
	report := a.streamProgress("analyzing")
	for range 5 {
		report("") // the model is working, but nothing to show yet
	}
	report("hello")
	report("hello")  // no growth: nothing to say
	report("hello!") // throttled behind the previous report

	waits, counts := 0, 0
	for _, msg := range got {
		switch {
		case strings.Contains(msg, "no answer text yet"):
			waits++
		case strings.Contains(msg, "characters so far"):
			counts++
		}
	}
	if waits != 1 {
		t.Errorf("reported the wait %d times, want once: %q", waits, got)
	}
	if counts != 1 {
		t.Errorf("reported %d counts, want one for the first content: %q", counts, got)
	}
}

// humanizeTopic capitalised the first *byte* of a word. A leading multi-byte
// rune was split in half, and the name reached the brief, the live tree and
// the exported notes as mojibake.
func TestHumanizeTopicKeepsNonASCIINames(t *testing.T) {
	for name, want := range map[string]string{
		"élan vital":     "Élan Vital",
		"ökonomie":       "Ökonomie",
		"日本語 sources":    "日本語 Sources",
		"état_de_l_art":  "État De L Art",
		"model_overview": "Model Overview",
	} {
		if got := humanizeTopic(name); got != want {
			t.Errorf("humanizeTopic(%q) = %q, want %q", name, got, want)
		}
	}
}
