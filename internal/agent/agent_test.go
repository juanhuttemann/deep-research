package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalRunResearchPhases(t *testing.T) {
	assistant := Local()
	ctx := context.Background()

	det, err := assistant.ResearchDetail(ctx, "search query")
	if err != nil {
		t.Fatalf("ResearchDetail() error: %v", err)
	}
	results := det.Findings
	if len(results) == 0 {
		t.Fatal("expected at least one finding")
	}
	if results[0].Query != "search query" {
		t.Errorf("expected query 'search query', got %q", results[0].Query)
	}

	analysis, err := assistant.Analyze(ctx, "prompt")
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	if analysis.Confidence != "low" {
		t.Errorf("expected low confidence offline, got %q", analysis.Confidence)
	}

	factCheck, err := assistant.FactCheck(ctx, "a claim")
	if err != nil {
		t.Fatalf("FactCheck() error: %v", err)
	}
	if len(factCheck.Verified) != 0 {
		t.Errorf("offline mode verified a claim it never checked: %+v", factCheck.Verified)
	}

	summary, err := assistant.Summarize(ctx, "prompt")
	if err != nil {
		t.Fatalf("Summarize() error: %v", err)
	}
	if summary.Report == "prompt" || !strings.Contains(summary.Report, "offline") {
		t.Errorf("offline report should be a stub, got %q", summary.Report)
	}
}

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

// TestNewRejectsMissingAPIKey keeps the documented offline fallback reachable:
// an online assistant built without credentials cannot make a single call, so
// New must say so at construction rather than failing mid-run at the planner.
func TestNewRejectsMissingAPIKey(t *testing.T) {
	if _, err := New(Config{OpenAIModel: "m"}); err == nil {
		t.Error("New with no API key returned no error; the CLI's offline fallback can never fire")
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
	if _, err := a.ResearchDetail(context.Background(), "q"); err != nil {
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

// TestLocalReportsNoTokens: the offline assistant makes no API calls, so it
// must not invent usage for the counters to display.
func TestLocalReportsNoTokens(t *testing.T) {
	if got := Local().TokensUsed(); got != 0 {
		t.Errorf("offline assistant reports %d tokens", got)
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
	det, err := a.ResearchDetail(context.Background(), "q")
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

// TestLocalSignalsAreUnverified: offline mode makes no network call either.
func TestLocalSignalsAreUnverified(t *testing.T) {
	det, err := Local().ResearchDetail(context.Background(), "q")
	if err != nil {
		t.Fatalf("ResearchDetail: %v", err)
	}
	if det.Signals[0].Status != "unverified" {
		t.Errorf("offline signal status = %q, want unverified", det.Signals[0].Status)
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

// Offline mode makes no API call, so it has verified nothing. Reporting the
// whole answer as one verified claim stated a fact the stub cannot know.
func TestLocalFactCheckVerifiesNothing(t *testing.T) {
	fc, err := Local().FactCheck(context.Background(), "a claim")
	if err != nil {
		t.Fatalf("FactCheck: %v", err)
	}
	if len(fc.Verified) != 0 {
		t.Errorf("offline mode reported verified claims: %+v", fc.Verified)
	}
	if len(fc.Unverified) == 0 {
		t.Error("expected offline mode to report the claims as unverified")
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
