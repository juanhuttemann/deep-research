package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaptinlin/jsonrepair"
	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/message"
	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"
)

func TestNew(t *testing.T) {
	assistant, err := New(Config{
		OpenAIModel:             "test",
		OpenAIBaseURL:           "https://test",
		OpenAIAPIKey:            "key",
		AnalyzerInstructions:    "analyze",
		FactCheckerInstructions: "check",
		SummarizerInstructions:  "summarize",
		SearchInstructions:      "search",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if assistant == nil {
		t.Fatal("expected non-nil assistant")
	}
}

func TestExtractExecutive(t *testing.T) {
	report := "Line 1\nLine 2\nLine 3\n\nLine 4\nLine 5"
	extracted := extractExecutive(report)
	if !strings.Contains(extracted, "Line 1") {
		t.Error("expected executive summary to contain Line 1")
	}
	if strings.Contains(extracted, "Line 4") {
		t.Errorf("executive summary ran past the opening paragraph: %q", extracted)
	}
}

func TestParseSearchResultsJSON(t *testing.T) {
	out := `{"findings":[{"title":"T1","content":"C1","url":"https://a"},{"title":"T2","content":"C2"}]}`
	findings := parseSearchResults(out, "q")
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Query != "q" || findings[0].Title != "T1" || findings[0].URL != "https://a" {
		t.Errorf("unexpected finding: %+v", findings[0])
	}
	if findings[1].Confidence != "medium" {
		t.Errorf("expected default confidence, got %q", findings[1].Confidence)
	}
}

func TestParseSearchResultsBareArray(t *testing.T) {
	out := `[{"title":"T1","content":"C1","url":"https://a"},{"title":"T2","content":"C2"}]`
	findings := parseSearchResults(out, "q")
	if len(findings) != 2 || findings[0].Title != "T1" {
		t.Errorf("unexpected findings: %+v", findings)
	}
}

func TestParseSearchResultsResultsKey(t *testing.T) {
	out := `{"results": [{"title":"T1","content":"C1","url":"https://a"}]}`
	findings := parseSearchResults(out, "q")
	if len(findings) != 1 || findings[0].Title != "T1" {
		t.Errorf("expected results key to be accepted, got %+v", findings)
	}
}

func TestParseSearchResultsJSONInProse(t *testing.T) {
	out := "Here are the results you asked for:\n" +
		`[{"title":"T1","content":"C1","url":"https://a"}]` +
		"\nHope that helps!"
	findings := parseSearchResults(out, "q")
	if len(findings) != 1 || findings[0].Title != "T1" || findings[0].URL != "https://a" {
		t.Errorf("expected JSON buried in prose to be parsed, got %+v", findings)
	}
}

func TestParseSearchResultsFallback(t *testing.T) {
	out := "first line of text\nshort\nsecond line of text"
	findings := parseSearchResults(out, "q")
	if len(findings) != 0 {
		t.Fatalf("prose is not evidence; got %d invented findings", len(findings))
	}
}

func TestParseAnalysisJSON(t *testing.T) {
	out := `{"answer":"A","gaps":["g1"],"confidence":"high","follow_up":["f1"],"topics":[{"name":"N","findings":["F"],"confidence":"low"}]}`
	a := parseAnalysis(out)
	if a.Answer != "A" || a.Confidence != "high" || len(a.Topics) != 1 || a.Topics[0].Name != "N" {
		t.Errorf("unexpected analysis: %+v", a)
	}
}

// Models write IDs as numbers and a single source as a bare string; reading
// only the documented shape dropped the decision the analysis had reached.
func TestParseAnalysisReadsTheDecision(t *testing.T) {
	a := parseAnalysis(`{"answer":"a","interpretation":"X vs Y",
		"claims":[{"id":1,"claim":"X scales itself","option":"X","sources":"https://x.example"},
		          {"claim":"Y is cheaper when steady","sources":["https://y.example"]},
		          {"id":"c9"}],
		"recommendations":[{"choose":"X","when":"traffic is spiky","claims":[1,"c2"]},{"when":"no option"}],
		"conflicts":[{"claims":["1","c2"],"resolution":"1 holds","why":"primary docs"}]}`)
	if a.Interpretation != "X vs Y" {
		t.Errorf("interpretation = %q", a.Interpretation)
	}
	if len(a.Claims) != 2 || a.Claims[0].ID != "1" || a.Claims[1].ID != "c2" || a.Claims[0].Sources[0] != "https://x.example" {
		t.Errorf("claims = %+v, want two, IDs 1 and c2, a bare source read as a list", a.Claims)
	}
	if len(a.Recommendations) != 1 || !slices.Equal(a.Recommendations[0].Claims, []string{"1", "c2"}) {
		t.Errorf("recommendations = %+v, want the one that names an option", a.Recommendations)
	}
	if len(a.Conflicts) != 1 || a.Conflicts[0].Resolution != "1 holds" {
		t.Errorf("conflicts = %+v", a.Conflicts)
	}
}

func TestParseAnalysisFencedJSON(t *testing.T) {
	out := "Sure, here is the analysis:\n```json\n{\"answer\":\"A\",\"gaps\":[],\"confidence\":\"high\"}\n```\nDone."
	a := parseAnalysis(out)
	if a.Answer != "A" || a.Confidence != "high" {
		t.Errorf("expected fence-wrapped JSON to parse, got: %+v", a)
	}
}

func TestParseAnalysisPlainText(t *testing.T) {
	a := parseAnalysis("just a plain answer")
	if a.Answer != "just a plain answer" {
		t.Errorf("expected plain text as answer, got %q", a.Answer)
	}
}

func TestParseFactCheck(t *testing.T) {
	out := `{"verified":[{"claim":"c1","verified":true,"evidence":"e1"}],"unverified":["c2"],"contradictions":[{"claim":"c3","sources":["s1","s2"]}]}`
	fc := parseFactCheck(out)
	if len(fc.Verified) != 1 || !fc.Verified[0].Verified || fc.Verified[0].Evidence != "e1" {
		t.Errorf("unexpected verified claims: %+v", fc.Verified)
	}
	if len(fc.Unverified) != 1 || fc.Unverified[0] != "c2" {
		t.Errorf("unexpected unverified: %+v", fc.Unverified)
	}
	if len(fc.Contradictions) != 1 || len(fc.Contradictions[0].Sources) != 2 {
		t.Errorf("unexpected contradictions: %+v", fc.Contradictions)
	}
}

func TestHumanizeTopicFixesIdentifierNames(t *testing.T) {
	cases := map[string]string{
		"model_overview_and_background":  "Model Overview and Background",
		"benchmark_performance":          "Benchmark Performance",
		"practical_usability_and_deploy": "Practical Usability and Deploy",
		"limitations-and-edge-cases":     "Limitations and Edge Cases",
		"Benchmark Performance":          "Benchmark Performance",
		"  spaced  out  ":                "Spaced Out",
		"":                               "",

		// Casing the model chose deliberately is left alone, and a hyphen
		// inside a real title is not a word separator.
		"GPT-4 vs Claude":          "GPT-4 vs Claude",
		"state-of-the-art results": "State-of-the-art Results",
		"LLM Architecture":         "LLM Architecture",
		"iOS deployment":           "iOS Deployment",
	}
	for in, want := range cases {
		if got := humanizeTopic(in); got != want {
			t.Errorf("humanizeTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePlanAssignsIDsOnEveryPath(t *testing.T) {
	// Sub-topics reaching the driver without IDs all report under the same
	// blank ID and collapse into a single node in the UI.
	for name, out := range map[string]string{
		"json":  `{"subtopics":[{"name":"first_topic"},{"name":"second_topic"}]}`,
		"array": `[{"name":"first_topic"},{"name":"second_topic"}]`,
	} {
		got := parsePlanAgent(out)
		if len(got) != 2 {
			t.Errorf("%s: parsed %d sub-topics, want 2: %+v", name, len(got), got)
			continue
		}
		if got[0].ID == "" || got[1].ID == "" || got[0].ID == got[1].ID {
			t.Errorf("%s: sub-topic IDs are %q and %q, want two distinct ids",
				name, got[0].ID, got[1].ID)
		}
		if strings.Contains(got[0].Name, "_") {
			t.Errorf("%s: identifier name survived: %q", name, got[0].Name)
		}
	}
}

// The planner's query and terms are what the branch searches and ranks by; a
// blank or repeated term would count toward a result's relevance.
func TestParsePlanKeepsQueryAndTerms(t *testing.T) {
	got := parsePlanAgent(`{"subtopics":[{"name":"Nuclear Advantages","query":" ventajas energía nuclear España ",
		"terms":["energía nuclear"," España ","","españa"]}]}`)
	if len(got) != 1 {
		t.Fatalf("parsed %+v", got)
	}
	if got[0].Query != "ventajas energía nuclear España" {
		t.Errorf("query = %q", got[0].Query)
	}
	if want := []string{"energía nuclear", "España"}; !slices.Equal(got[0].Terms, want) {
		t.Errorf("terms = %q, want %q", got[0].Terms, want)
	}
}

// TestNewRejectsMissingAPIKey keeps the CLI's missing-key error reachable:
// an online assistant built without credentials cannot make a single call, so
// New must say so at construction rather than failing mid-run at the planner.
func TestNewRejectsMissingAPIKey(t *testing.T) {
	if _, err := New(Config{OpenAIModel: "m"}); err == nil {
		t.Error("New with no API key returned no error; the CLI's missing-key error can never fire")
	}
	if _, err := New(Config{OpenAIAPIKey: "   ", OpenAIModel: "m"}); err == nil {
		t.Error("New with a blank API key returned no error")
	}
	if _, err := New(Config{OpenAIAPIKey: "sk-test", OpenAIModel: "m"}); err != nil {
		t.Errorf("New with a key failed: %v", err)
	}
}

// TestTokensUsedReportsModelUsage checks that the assistant surfaces the token
// usage the provider reports, so the UI's counters and the exported metadata
// carry the run's actual cost basis rather than a synthetic number.
func TestTokensUsedReportsModelUsage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "1",
			"object":  "chat.completion",
			"model":   "test",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"findings":[]}`}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 25, "total_tokens": 125},
		})
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.TokensUsed(); got != 0 {
		t.Errorf("fresh assistant reports %d tokens, want 0", got)
	}
	if _, err := a.ResearchDetail(context.Background(), "q", nil); err != nil {
		t.Fatalf("ResearchDetail: %v", err)
	}
	if got := a.TokensUsed(); got != 125 {
		t.Errorf("TokensUsed() = %d after one call reporting 125 tokens", got)
	}
	if _, err := a.Analyze(context.Background(), "p"); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := a.TokensUsed(); got != 250 {
		t.Errorf("TokensUsed() = %d after two calls, want the running total 250", got)
	}
}

// TestLLMSearchSignalsAreUnverified: with no SearXNG/Firecrawl configured the
// findings come from the model, not from a fetch. Marking them "ok" made the
// UI print "✓ domain 200 OK" for a page nobody ever requested.
func TestLLMSearchSignalsAreUnverified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "1", "object": "chat.completion", "model": "test",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{
				"role":    "assistant",
				"content": `{"findings":[{"title":"T","content":"c","url":"https://example.com/a"}]}`,
			}}},
		})
	}))
	defer srv.Close()

	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	det, err := a.ResearchDetail(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("ResearchDetail: %v", err)
	}
	if len(det.Signals) == 0 {
		t.Fatal("no signals")
	}
	for i, s := range det.Signals {
		if s.Status != "unverified" {
			t.Errorf("signal %d status = %q, want %q: nothing was fetched", i, s.Status, "unverified")
		}
	}
}

// TestExtractExecutiveKeepsWholeParagraph: the .md export's executive summary
// used to be a blind 5-line prefix, which cut a longer opening mid-sentence
// and swallowed the report's title line as if it were prose.
func TestExtractExecutiveKeepsWholeParagraph(t *testing.T) {
	report := "# Solid-State Batteries\n\n" +
		"Line one of the summary.\nLine two.\nLine three.\nLine four.\nLine five.\nLine six ends it.\n\n" +
		"## Detail\n\nBody text that is not part of the summary."

	got := extractExecutive(report)
	if strings.Contains(got, "# Solid-State Batteries") {
		t.Errorf("executive summary kept the report heading:\n%s", got)
	}
	if !strings.Contains(got, "Line six ends it.") {
		t.Errorf("executive summary cut the opening paragraph short:\n%s", got)
	}
	if strings.Contains(got, "Body text") || strings.Contains(got, "## Detail") {
		t.Errorf("executive summary ran past the opening paragraph:\n%s", got)
	}
}

// TestExtractExecutiveMarksTruncation: a runaway opening paragraph is still
// capped, but it says so instead of stopping mid-sentence.
func TestExtractExecutiveMarksTruncation(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("A sentence of the summary.\n")
	}
	got := extractExecutive(sb.String())
	if !strings.HasSuffix(strings.TrimSpace(got), "…") {
		t.Errorf("a truncated summary must be marked:\n%s", got)
	}
}

// FactCheck must send exactly the prompt it was given. It used to prepend
// "Verify these claims against the research findings:" on top of the identical
// sentence the driver had already written, so the model saw the instruction
// twice and the findings never.
func TestFactCheckSendsThePromptUnchanged(t *testing.T) {
	var got string
	a := &impl{factCheck: func(_ context.Context, prompt string) (string, error) {
		got = prompt
		return `{"verified":[]}`, nil
	}}
	if _, err := a.FactCheck(context.Background(), "CLAIMS\n\nFINDINGS"); err != nil {
		t.Fatalf("FactCheck: %v", err)
	}
	if got != "CLAIMS\n\nFINDINGS" {
		t.Errorf("prompt was rewritten: %q", got)
	}
}

// A fact-checker that answers with prose has verified nothing. Recording the
// raw dump as one verified claim put an unchecked paragraph into the report's
// citations under a "verified" label.
func TestParseFactCheckTreatsNonJSONAsUnverified(t *testing.T) {
	fc := parseFactCheck("I could not verify these claims.")
	if len(fc.Verified) != 0 {
		t.Errorf("prose output produced verified claims: %+v", fc.Verified)
	}
	if len(fc.Unverified) != 1 || !strings.Contains(fc.Unverified[0], "could not verify") {
		t.Errorf("expected the raw output recorded as unverified, got %+v", fc.Unverified)
	}
}

// The prompt asks for unverified entries as {"claim":...,"reason":...} objects,
// which decoded to a list of empty strings and reached the summarizer as
// "- unverified: ".
func TestParseFactCheckReadsUnverifiedObjects(t *testing.T) {
	fc := parseFactCheck(`{"verified":[],"unverified":[{"claim":"c1","reason":"no source"},"c2"]}`)
	if len(fc.Unverified) != 2 {
		t.Fatalf("expected 2 unverified entries, got %+v", fc.Unverified)
	}
	if !strings.Contains(fc.Unverified[0], "c1") || !strings.Contains(fc.Unverified[0], "no source") {
		t.Errorf("object entry lost its claim/reason: %q", fc.Unverified[0])
	}
	if fc.Unverified[1] != "c2" {
		t.Errorf("string entry mangled: %q", fc.Unverified[1])
	}
}

// The analyzer prompt asks for "follow_up_queries"; the parser only read
// "follow_up", so the field was silently always empty.
func TestParseAnalysisAcceptsBothFollowUpKeys(t *testing.T) {
	for _, key := range []string{"follow_up", "follow_up_queries"} {
		a := parseAnalysis(`{"answer":"A","` + key + `":["q1","q2"]}`)
		if len(a.FollowUp) != 2 || a.FollowUp[0] != "q1" {
			t.Errorf("%s: expected 2 follow-ups, got %+v", key, a.FollowUp)
		}
	}
}

// A model that opens a fence and never closes it used to deliver the report
// with a literal ```markdown line at the top, which then became the first line
// of the executive summary too.
func TestStripCodeFenceHandlesAnUnclosedFence(t *testing.T) {
	got := stripCodeFence("```markdown\n# Report\n\nBody text.")
	if strings.Contains(got, "```") {
		t.Errorf("opening fence survived: %q", got)
	}
	if !strings.HasPrefix(got, "# Report") {
		t.Errorf("report body was damaged: %q", got)
	}
}

// TestSlowCallIsRetriedWithALongerDeadline: the reported failure was a report
// that took longer than model_call_timeout and died with "error reading
// response body". The retry must give the next attempt more time, not repeat
// a deadline that was already too short.
func TestSlowCallIsRetriedWithALongerDeadline(t *testing.T) {
	// The handler runs on the server's goroutine while the test reads the
	// count, and an abandoned attempt is still in flight when it does.
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Slower than the first attempt's budget, faster than the second's.
			time.Sleep(150 * time.Millisecond)
		}
		// A real provider streams this phase, so the fixture does too:
		// against a non-streaming server the call would fall back to a second
		// request and the attempt count would no longer measure the retry.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"the report\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a, err := New(Config{
		OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test",
		ModelCallTimeout: 50 * time.Millisecond, ModelCallRetries: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := a.Summarize(context.Background(), "p")
	if err != nil {
		t.Fatalf("a slow first attempt must be retried, got: %v", err)
	}
	if got == nil || !strings.Contains(got.Report, "the report") {
		t.Errorf("retry returned %#v", got)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("want one timed-out attempt then one success, got %d calls", n)
	}
}

// TestCallGivesUpAfterItsRetries: retries are bounded, and the failure still
// names the agent that failed.
func TestCallGivesUpAfterItsRetries(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(80 * time.Millisecond)
	}))
	defer srv.Close()

	a, err := New(Config{
		OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test",
		ModelCallTimeout: 20 * time.Millisecond, ModelCallRetries: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.Summarize(context.Background(), "p"); err == nil {
		t.Fatal("expected the call to fail once its retries ran out")
	} else if !strings.Contains(err.Error(), "summarizer") {
		t.Errorf("error must name the failing agent, got %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("want 1 attempt + 1 retry = 2 calls, got %d", n)
	}
}

// "follows" counts only when plainly yes: a "partly" or a missing value is
// not a finding that the recommendation follows.
func TestParseInferences(t *testing.T) {
	fc := parseFactCheck(`{"verdicts":[],"recommendations":[
		{"id":"r1","follows":true},{"id":"r2","follows":"yes"},{"id":3,"follows":"partly"},{"id":"r4"}]}`)
	want := map[string]bool{"r1": true, "r2": true, "3": false, "r4": false}
	if len(fc.Inferences) != 4 {
		t.Fatalf("inferences = %+v", fc.Inferences)
	}
	for _, inf := range fc.Inferences {
		if inf.Follows != want[inf.ID] {
			t.Errorf("%s follows = %v, want %v", inf.ID, inf.Follows, want[inf.ID])
		}
	}
}

// Conclusions parse as statements; the checker's judgements are read from
// "inferences" and from the older "recommendations" key alike.
func TestParseConclusionsAndInferences(t *testing.T) {
	a := parseAnalysis(`{"answer":"a","conclusions":[{"statement":"S","claims":["c1"]},{"claims":["c2"]}]}`)
	if len(a.Conclusions) != 1 || a.Conclusions[0].Statement != "S" {
		t.Errorf("conclusions = %+v, want the one with a statement", a.Conclusions)
	}
	fc := parseFactCheck(`{"inferences":[{"id":"k1","follows":true}],"recommendations":[{"id":"r1","follows":true}]}`)
	if len(fc.Inferences) != 2 {
		t.Errorf("inferences = %+v, want both keys read", fc.Inferences)
	}
}

// A follow-up about a finished run is answered from the run's own pages, which
// the model reads through the tools it is given: the framework runs the call
// the model asks for and sends the result back. The conversation is carried as
// text, so a second question can lean on the first ("and the second one?").
func TestChatRunsToolsAndKeepsTurns(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		msg := map[string]any{"role": "assistant", "content": "It costs 5 dollars (https://a.example)."}
		reason := "stop"
		if len(bodies) == 1 {
			msg = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "read_source", "arguments": `{"url":"https://a.example","about":"price"}`},
			}}}
			reason = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "1", "object": "chat.completion", "model": "test",
			"choices": []any{map[string]any{"index": 0, "finish_reason": reason, "message": msg}},
		})
	}))
	defer srv.Close()

	type source struct {
		URL   string `json:"url"`
		About string `json:"about"`
	}
	var got source
	read := functool.MustNew(functool.Config{Name: "read_source", Description: "read a page"},
		func(_ context.Context, in source) (string, error) { got = in; return "PAGE SAYS 5 DOLLARS", nil })
	chat, err := NewChat(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test",
		ChatInstructions: "answer from the run"}, "Question: what does it cost?", []tool.Tool{read})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	answer, err := chat.Ask(context.Background(), "How much is it?")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got != (source{"https://a.example", "price"}) {
		t.Errorf("tool got %+v, want the arguments the model sent", got)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "PAGE SAYS 5 DOLLARS") {
		t.Fatalf("requests = %d; the second must carry the tool's result:\n%s", len(bodies), strings.Join(bodies, "\n"))
	}
	if !strings.Contains(bodies[0], "answer from the run") || !strings.Contains(bodies[0], "what does it cost?") {
		t.Errorf("the instructions and the run overview are not in the request:\n%s", bodies[0])
	}
	if !strings.Contains(answer, "5 dollars") {
		t.Errorf("answer = %q", answer)
	}

	if _, err := chat.Ask(context.Background(), "And in euros?"); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, "How much is it?") || !strings.Contains(last, "It costs 5 dollars") {
		t.Errorf("the second question does not carry the first turn:\n%s", last)
	}
}

// A model that cannot call tools refuses every question about a run, and each
// provider says so its own way. The refusal was retried, three requests for a
// certain failure, and reached the reader as a raw 404 telling them to "try
// disabling read_source": a tool they never chose. It now fails at once and
// says what to change.
func TestChatOnAModelWithoutToolsSaysWhatToChange(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		// OpenRouter, for a model none of whose endpoints take tools.
		"openrouter": {404, `{"error":{"message":"No endpoints found that support tool use. Try disabling \"read_source\".","code":404}}`},
		// vLLM started without --enable-auto-tool-choice.
		"vllm": {400, `{"error":{"message":"\"auto\" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set","type":"BadRequestError","code":400}}`},
		// Ollama, for a model whose template has no tools.
		"ollama": {400, `{"error":{"message":"registry.ollama.ai/library/gemma:2b does not support tools","type":"api_error"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			read := functool.MustNew(functool.Config{Name: "read_source", Description: "read a page"},
				func(context.Context, struct{}) (string, error) { return "", nil })
			chat, err := NewChat(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "plain-model",
				ModelCallRetries: 2}, "overview", []tool.Tool{read})
			if err != nil {
				t.Fatalf("NewChat: %v", err)
			}
			_, err = chat.Ask(context.Background(), "q")
			if err == nil || !strings.Contains(err.Error(), "plain-model cannot call tools") || !strings.Contains(err.Error(), "OPENAI_MODEL") {
				t.Errorf("err = %v; want it to name the model and say to set OPENAI_MODEL", err)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("%d requests for a refusal no retry can change, want 1", n)
			}
		})
	}
}

// TestCollectResponseProgressMatchesResponseText pins the streamed text that
// progress sees to what Response.String would say after the same updates.
// collectResponse keeps that text itself instead of asking Response for it on
// every token; the cases are where the two could part: a second message,
// a message with no text, reasoning with no text, and an empty first update.
func TestCollectResponseProgressMatchesResponseText(t *testing.T) {
	text := func(s string) message.Contents { return message.Contents{&message.TextContent{Text: s}} }
	updates := []*agent.ResponseUpdate{
		{Role: message.RoleAssistant},
		{Role: message.RoleAssistant, MessageID: "m1", Contents: message.Contents{&message.TextReasoningContent{Text: "hmm"}}},
		{Role: message.RoleAssistant, MessageID: "m1", Contents: text(`{"answer":`)},
		{Role: message.RoleAssistant, Contents: text(` "yes"}`)},
		{Role: message.RoleAssistant, MessageID: "m2"},
		{Role: message.RoleAssistant, MessageID: "m3", Contents: text("second")},
		{Role: message.RoleAssistant, MessageID: "m3", Contents: message.Contents{&message.TextContent{Text: ""}, &message.TextContent{Text: " part"}}},
		{Role: message.RoleTool, Contents: text("third")},
	}
	var want agent.Response
	var got []string
	stream := agent.ResponseStream(func(yield func(*agent.ResponseUpdate, error) bool) {
		for _, u := range updates {
			if !yield(u, nil) {
				return
			}
		}
	})
	resp, err := collectResponse(stream, func(p string) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	for i, u := range updates {
		want.Update(u)
		if got[i] != want.String() {
			t.Errorf("after update %d progress saw %q, Response.String is %q", i, got[i], want.String())
		}
	}
	if resp.String() != got[len(got)-1] {
		t.Errorf("collected %q, last progress %q", resp.String(), got[len(got)-1])
	}
}

// eagerJSONPayloads is jsonPayloads as it was before it became lazy, kept as
// the reference for the order and the set of candidates.
func eagerJSONPayloads(out string) []string {
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
	for _, p := range payloads {
		if fixed, err := jsonrepair.Repair(p); err == nil && fixed != p {
			payloads = append(payloads, fixed)
		}
	}
	return payloads
}

// jsonPayloads makes its repaired copies only when they are reached; what it
// yields, and in what order, must be what the eager list held, or a parse
// that used to find its payload would find another one first.
func TestJSONPayloadsYieldsTheEagerCandidatesInOrder(t *testing.T) {
	for _, out := range []string{
		`{"answer":"yes","claims":[{"id":"c1"}]}`,
		"Here it is:\n```json\n{\"answer\":\"yes\",\"claims\":[1,2,]}\n```\nDone.",
		`[{"title":"T","content":"x"},]`,
		`{"answer": "unterminated`,
		"prose with no JSON at all",
		`] backwards [`,
		"",
	} {
		got := slices.Collect(jsonPayloads(out))
		if want := eagerJSONPayloads(out); !slices.Equal(got, want) {
			t.Errorf("jsonPayloads(%q)\n got %q\nwant %q", out, got, want)
		}
	}
}

// fresh hands each piece of the reasoning out once, no more than once per
// interval while it grows, and the rest at the end of the call.
func TestThoughtsFreshSendsEachPieceOnce(t *testing.T) {
	var th thoughts
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	type got struct {
		text  string
		first bool
	}
	step := func(d time.Duration, last bool) got {
		text, first := th.fresh(t0.Add(d), last)
		return got{text, first}
	}
	if g := step(0, false); g != (got{}) {
		t.Errorf("before any reasoning: %+v, want nothing", g)
	}
	th.add("Weigh c3")
	if g := step(0, false); g != (got{"Weigh c3", true}) {
		t.Errorf("first piece: %+v", g)
	}
	th.add(" against")
	if g := step(500*time.Millisecond, false); g != (got{}) {
		t.Errorf("within the interval: %+v, want nothing", g)
	}
	th.add(" c5\n")
	if g := step(thinkingInterval, false); g != (got{" against c5\n", false}) {
		t.Errorf("after the interval: %+v", g)
	}
	th.add("Then c7.")
	if g := step(thinkingInterval+time.Millisecond, true); g != (got{"Then c7.", false}) {
		t.Errorf("at the end of the call: %+v", g)
	}
	if g := step(time.Hour, true); g != (got{}) {
		t.Errorf("after everything was sent: %+v, want nothing", g)
	}
}

// The thinking channel carries the reasoning whole, its line breaks kept,
// where a status carries only the last words of it run together.
func TestStreamedReasoningReachesTheThinkingChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning\":\"Weigh c3\\n\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\",\"reasoning\":\"against c5\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The report.\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m", ModelCallTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu     sync.Mutex
		pieces []string
		starts int
	)
	a.(interface {
		SetThinking(func(string, string, bool))
	}).SetThinking(func(text, _ string, first bool) {
		mu.Lock()
		defer mu.Unlock()
		pieces = append(pieces, text)
		if first {
			starts++
		}
	})
	if _, err := a.Summarize(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(pieces, ""); got != "Weigh c3\nagainst c5" {
		t.Errorf("reasoning sent = %q, want it whole", got)
	}
	if starts != 1 {
		t.Errorf("%d pieces marked first, want 1", starts)
	}
}

// The Driver runs the fact-check as several calls at once. Their reasoning
// and status interleave, and only the part each carries keeps one call's
// reasoning apart from another's.
func TestPartLabelsEachParallelCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning\":\"Weigh c3\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"verdicts\\\":[]}\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "m", ModelCallTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		parts    []string
		statuses []string
	)
	a.(interface {
		SetThinking(func(string, string, bool))
	}).SetThinking(func(_, part string, _ bool) {
		mu.Lock()
		defer mu.Unlock()
		parts = append(parts, part)
	})
	a.(interface{ SetStatus(func(string)) }).SetStatus(func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		statuses = append(statuses, msg)
	})
	if _, err := a.FactCheck(WithPart(context.Background(), "claims c1-c6"), "p"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(parts) == 0 || slices.ContainsFunc(parts, func(p string) bool { return p != "claims c1-c6" }) {
		t.Errorf("reasoning parts %q, want every piece labelled with its call", parts)
	}
	if len(statuses) == 0 || !strings.HasPrefix(statuses[0], "fact-checking claims c1-c6 — ") {
		t.Errorf("statuses %q, want the call's part in each", statuses)
	}
}

// effortServer is an OpenAI-compatible double that keeps every request body
// and refuses, as Gemini does "none", any request whose reasoning_effort is
// refuse.
type effortServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newEffortServer(t *testing.T, refuse string) *effortServer {
	t.Helper()
	s := &effortServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		if refuse != "" && body["reasoning_effort"] == refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Reasoning is mandatory for this endpoint and cannot be disabled.","code":400}}`)
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"{\\\"claims\\\":[]}\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "1", "object": "chat.completion", "model": "test",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"findings":[]}`}}},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *effortServer) body(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.bodies) {
		return nil
	}
	return s.bodies[i]
}

func (s *effortServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// The effort rides in the same request parameters as stream_options: the
// framework keeps only the last parameters option, so a separate one for the
// effort dropped include_usage and every streamed phase's tokens with it.
func TestEffortIsSentWithTheCallsOtherParameters(t *testing.T) {
	srv := newEffortServer(t, "")
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test",
		Effort: Effort{Analyze: "high", Search: "low"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := a.Analyze(ctx, "p"); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := a.Summarize(ctx, "p"); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if _, err := a.ResearchDetail(ctx, "q", nil); err != nil {
		t.Fatalf("ResearchDetail: %v", err)
	}
	usage := func(b map[string]any) any { so, _ := b["stream_options"].(map[string]any); return so["include_usage"] }
	if b := srv.body(0); b["reasoning_effort"] != "high" || usage(b) != true {
		t.Errorf("analyze sent effort %v, include_usage %v; want high and true", b["reasoning_effort"], usage(b))
	}
	if b := srv.body(1); b["reasoning_effort"] != nil || usage(b) != true {
		t.Errorf("summarize, left to the provider, sent effort %v, include_usage %v", b["reasoning_effort"], usage(b))
	}
	if b := srv.body(2); b["reasoning_effort"] != "low" || b["response_format"] == nil {
		t.Errorf("search sent effort %v, response_format %v; want low and JSON mode kept", b["reasoning_effort"], b["response_format"])
	}
}

// Gemini refuses "none" with a 400. The request is asked again without the
// effort, and once that worked the level is left out of every later request
// instead of being refused on each.
func TestRefusedEffortIsDroppedAndRemembered(t *testing.T) {
	srv := newEffortServer(t, "none")
	var mu sync.Mutex
	var lines []string
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test", ModelCallRetries: 0,
		Effort: Effort{Analyze: "none", FactCheck: "none"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetProgress(func(msg string) { mu.Lock(); lines = append(lines, msg); mu.Unlock() })
	ctx := context.Background()
	if _, err := a.Analyze(ctx, "p"); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if n := srv.requests(); n != 2 {
		t.Fatalf("want the refused request and one without the effort, got %d requests", n)
	}
	if b := srv.body(1); b["reasoning_effort"] != nil || b["stream"] != true {
		t.Errorf("the second request sent effort %v, stream %v", b["reasoning_effort"], b["stream"])
	}
	if _, err := a.FactCheck(ctx, "claims"); err != nil {
		t.Fatalf("FactCheck: %v", err)
	}
	if n := srv.requests(); n != 3 || srv.body(2)["reasoning_effort"] != nil {
		t.Errorf("a remembered refusal was sent again: %d requests, last effort %v", n, srv.body(2)["reasoning_effort"])
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"refused reasoning effort none (Reasoning is mandatory", "effort none refused by the endpoint"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q:\n%s", want, joined)
		}
	}
}

// A 400 about something else is not taken for a refused effort: it is not
// asked again without it, and the effort stays on later requests.
func TestOtherRefusalsKeepTheEffort(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model test does not exist"}}`)
	}))
	defer srv.Close()
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test", ModelCallRetries: 0,
		Effort: Effort{Analyze: "low"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.Analyze(context.Background(), "p"); err == nil {
		t.Fatal("want the 400 reported")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("an unrelated 400 was sent %d times", n)
	}
	if label := a.(*impl).effortLabel("low"); label != "effort low" {
		t.Errorf("label = %q, want the effort still sent", label)
	}
}

func TestParseEffort(t *testing.T) {
	for in, want := range map[string]string{"": "", "default": "", " High ": "high", "none": "none", "xhigh": "xhigh"} {
		if got, err := ParseEffort(in); err != nil || got != want {
			t.Errorf("ParseEffort(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseEffort("hgih"); err == nil || !strings.Contains(err.Error(), "want default, none") {
		t.Errorf("a typo: err = %v", err)
	}
}

// A fact-check answer with no verdict is logged with its opening, the only
// clue to why: a replay's check judged nothing and nothing it sent was kept.
func TestFactCheckWithNoVerdictsLogsWhatCameBack(t *testing.T) {
	srv := newEffortServer(t, "")
	a, err := New(Config{OpenAIAPIKey: "k", OpenAIBaseURL: srv.URL, OpenAIModel: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var lines []string
	a.SetProgress(func(m string) { lines = append(lines, m) })
	if _, err := a.FactCheck(context.Background(), "claims"); err != nil {
		t.Fatalf("FactCheck: %v", err)
	}
	if !slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, `the fact-check answered with no verdicts (13 characters): "{\"claims\":[]}"`)
	}) {
		t.Errorf("the empty answer was not logged:\n%s", strings.Join(lines, "\n"))
	}
}

// A streamed attempt that failed after its usage arrived returned nothing,
// and the tokens the provider billed for it never reached the run's total.
func TestFailedStreamKeepsItsUsage(t *testing.T) {
	stream := agent.ResponseStream(func(yield func(*agent.ResponseUpdate, error) bool) {
		if !yield(&agent.ResponseUpdate{Role: message.RoleAssistant, Contents: message.Contents{
			&message.UsageContent{Details: message.UsageDetails{InputTokenCount: 120, OutputTokenCount: 30}}}}, nil) {
			return
		}
		yield(nil, errors.New("stream cut"))
	})
	resp, err := collectResponse(stream, func(string) {})
	if err == nil {
		t.Fatal("the stream's error was lost")
	}
	a := &impl{}
	a.addUsage(resp)
	if got := a.tokens.Load(); got != 150 {
		t.Errorf("tokens = %d, want the failed attempt's 150", got)
	}
}
