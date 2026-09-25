package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Driver executes a Plan through the assistant, emitting structured events to
// a MultiSink. It runs the research sub-topics in parallel, tracks running
// token / source counters, and surfaces human-in-the-loop directives.
type Driver struct {
	Agent       agent.Assistant
	Sink        *MultiSink
	Input       Input
	Parallelism int

	now func() time.Time

	detached  bool
	cancel    context.CancelFunc
	startTime time.Time

	// mu guards the running counters and the current phase below, which the
	// parallel sub-agent goroutines update and read concurrently.
	mu sync.Mutex
	// phase is the pipeline phase most recently announced; every event that
	// names no phase of its own is stamped with it.
	phase string
	// running counters surfaced in the status line.
	tokens  int
	sources int
	// citedURLs is every source already taken by some sub-agent. Each
	// sub-agent anchors its query on the same research question, so the search
	// backend hands most of them the same pages; without this the run counts
	// one page once per sub-agent and reports far more sources than it read.
	citedURLs map[string]bool
	// searchesOK and searchErr tell a run that found nothing apart from a run
	// whose every search was refused: only the second is a failure.
	searchesOK int
	searchErr  error
}

func NewDriver(a agent.Assistant, sink *MultiSink, input Input, parallelism int) *Driver {
	if parallelism <= 0 {
		parallelism = 3
	}
	return &Driver{
		Agent:       a,
		Sink:        sink,
		Input:       input,
		Parallelism: parallelism,
		now:         time.Now,
	}
}

// emit stamps an event with the phase the pipeline is in and fans it out.
//
// The phase is tracked rather than hardcoded: the assistant's progress logger
// and the detach key emit phase-less events, and defaulting them all to
// "Research" filed every retry line from Analyze, Fact-Check and Summarize
// under a phase that had already finished.
func (d *Driver) emit(e Event) {
	if e.Type == Phase && e.Phase != "" {
		d.setPhase(e.Phase)
	}
	if e.Phase == "" {
		e.Phase = d.currentPhase()
	}
	if d.Sink != nil {
		d.Sink.Emit(e)
	}
}

// setPhase records the phase the pipeline has entered. Sub-agents emit in
// parallel, so the field is guarded like the other running state.
func (d *Driver) setPhase(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.phase = name
}

// currentPhase is the phase to stamp on an event that names none. A run that
// has not announced a phase yet is still planning.
func (d *Driver) currentPhase() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.phase == "" {
		return "Plan"
	}
	return d.phase
}

// record counts one more source and re-reads the assistant's provider-reported
// token total, returning a snapshot of the resulting counters. Sub-agents run
// in parallel and all call it, so the counters are updated under mu and the
// caller emits the returned snapshot (captured under the same lock) to keep
// every field consistent and free of data races.
func (d *Driver) record() (sources int, tokens int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources++
	return d.sources, d.syncUsageLocked()
}

// syncUsageLocked pulls the running token total from the assistant. It must be
// called with mu held.
func (d *Driver) syncUsageLocked() int {
	d.tokens = d.Agent.TokensUsed()
	return d.tokens
}

// syncUsage refreshes the counters after the phases that run outside the
// per-source loop (analyze, fact-check, summarize), so the completion card and
// the exported metadata account for every call the run made.
func (d *Driver) syncUsage() (sources int, tokens int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sources, d.syncUsageLocked()
}

// startKeyReader launches the background goroutine that reads keys and applies
// them. Each directive takes effect where it is read rather than being queued
// for the pipeline to notice: a queued cancel cannot interrupt the model call
// it is meant to abort.
func (d *Driver) startKeyReader() {
	go func() {
		if d.Input == nil {
			return
		}
		for {
			k, err := readKey(d.Input)
			if err != nil {
				return
			}
			switch k.Key {
			case KeyEsc, KeyCtrlC:
				if d.cancel != nil {
					d.cancel()
				}
			case KeyCtrlZ, KeyB:
				d.detach()
				// Detaching gave the terminal back, so there are no more
				// keys to read here. Staying in the loop would keep eating
				// every keystroke the reader types at their shell prompt,
				// and would leave Esc able to kill a run they already sent
				// to the background.
				return
			}
		}
	}()
}

// claimSource reserves a URL for the caller, reporting false when another
// sub-agent has already taken it. An empty URL is never deduplicated: there is
// no identity to compare, so the finding is kept on its content.
func (d *Driver) claimSource(url string) bool {
	if strings.TrimSpace(url) == "" {
		return true
	}
	key := tools.CanonicalURL(url)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.citedURLs == nil {
		d.citedURLs = map[string]bool{}
	}
	if d.citedURLs[key] {
		return false
	}
	d.citedURLs[key] = true
	return true
}

// detach stops the live display and hands the terminal back.
//
// There is no second process to hand the work to, so the run keeps the
// foreground until it prints its report. What detaching can honestly give
// back is the terminal itself: closing the Input restores the mode it was
// found in, so typing is echoed again and the keys are no longer swallowed.
func (d *Driver) detach() {
	d.mu.Lock()
	already := d.detached
	d.detached = true
	d.mu.Unlock()
	if already {
		return
	}
	d.releaseTerminal()
	d.emit(Event{Type: Detach, Detail: detachDetail})
}

// detachDetail is what the run says about itself once the display is released.
// It does not claim the process was backgrounded, because it was not.
const detachDetail = "display released; the run keeps this terminal until the report is written"

// releaseTerminal restores the terminal mode the Input was opened over. It is
// safe to call more than once, and ui.Run closes the same Input again when the
// run ends.
func (d *Driver) releaseTerminal() {
	if d.Input != nil {
		d.Input.Close()
	}
}

// Run drives the full pipeline for plan and returns the final research result.
func (d *Driver) Run(ctx context.Context, plan *Plan) (*agent.ResearchResult, error) {
	d.startTime = d.now()
	// Esc cancels the run, which needs a context the key reader can cancel.
	// Without deriving one here d.cancel stays nil and the key does nothing.
	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	defer cancel()
	// A run that starts detached never reads a key: it has no display to
	// release later and no directive to accept, so leaving the terminal in
	// raw mode with a reader on it would swallow input for nothing.
	if d.detached {
		d.releaseTerminal()
		d.emit(Event{Type: Detach, Detail: detachDetail})
	} else if d.Input != nil {
		d.startKeyReader()
	}
	d.emit(Event{Type: Phase, Phase: "Plan", Detail: "Planning research scope"})
	d.emit(Event{Type: Phase, Phase: "Research", Detail: "Running sub-agents"})

	findings, uncovered := d.research(ctx, plan)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Every search was refused: there is no evidence to analyse, and three
	// model calls would only write a report about nothing.
	if err := d.searchFailure(findings); err != nil {
		return nil, err
	}

	d.emit(Event{Type: Phase, Phase: "Analyze", Detail: "Synthesizing findings into an answer"})
	analysis, err := d.Agent.Analyze(ctx, analyzePrompt(plan.Question, findings, uncovered))
	if err != nil {
		return d.partial(ctx, plan, findings, nil, nil, "analysis failed", err)
	}

	// What this phase can honestly claim depends on where the "sources" came
	// from. With no search backend configured the model supplied the findings,
	// the URLs and the page text, so checking an answer against them is a
	// self-consistency pass with no contact with the world — and saying
	// "verified" for it would be the pipeline's single most misleading word.
	retrieved := sourcesRetrieved(findings)
	d.emit(Event{Type: Phase, Phase: "Fact-Check", Detail: factCheckDetail(findings)})
	// Fact-checking is a verification pass over an answer that already
	// exists. If it fails there is still a complete, citable report to
	// deliver, so the failure is surfaced and the run continues rather than
	// discarding every search and the analysis behind it.
	fc, err := d.Agent.FactCheck(ctx, factCheckPrompt(analysis.Answer, findings, retrieved))
	if err != nil {
		fc = nil
		d.emit(Event{Type: Error, Phase: "Fact-Check",
			Detail: "fact-check unavailable, reporting unverified: " + err.Error()})
	}

	d.emit(Event{Type: Phase, Phase: "Summarize", Detail: "Writing the report"})
	prompt := summarizePrompt(plan.Question, analysis, fc, findings, retrieved)
	summary, err := d.Agent.Summarize(ctx, prompt)
	if err != nil {
		return d.partial(ctx, plan, findings, analysis, fc, "report writing failed", err)
	}
	// The summarizer writes prose, so it reports no confidence of its own. The
	// analyzer's is the run's, and without carrying it over the report prints
	// a bare "Confidence:" line and the exports record an empty string.
	if summary.Confidence == "" {
		summary.Confidence = analysis.Confidence
	}
	return d.finish(&agent.ResearchResult{
		Question: plan.Question, Findings: findings, Analysis: analysis, FactCheck: fc, Summary: summary,
	}), nil
}

// finish stamps a result with the run's final counters and announces it.
func (d *Driver) finish(result *agent.ResearchResult) *agent.ResearchResult {
	sources, tokens := d.syncUsage()
	result.Tokens, result.Timestamp = tokens, d.now()
	if m, ok := d.Agent.(interface{ ModelInfo() (string, string) }); ok {
		result.Model, result.Provider = m.ModelInfo()
	}
	if m, ok := d.Agent.(interface{ ServedModels() []string }); ok {
		result.ServedBy = m.ServedModels()
	}
	d.emit(Event{Type: Report, Phase: "Report", Detail: "Research complete",
		Tokens: tokens, Sources: sources})
	return result
}

// partial delivers a run whose analyze or summarize call failed. Those calls
// come last, after every search, scrape and token has been spent; returning
// only the error threw all of it away — no artifact, no history record. A
// deadline or a provider that gave up mid-report still leaves the sources and
// possibly the analysis, so they are delivered, marked incomplete.
//
// A cancellation is the reader stopping the run, not a failure to recover
// from, so it is passed through.
func (d *Driver) partial(ctx context.Context, plan *Plan, findings []agent.Finding,
	analysis *agent.Analysis, fc *agent.FactCheckResult, what string, err error) (*agent.ResearchResult, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, err
	}
	reason := what + ": " + err.Error()
	d.emit(Event{Type: Error, Detail: reason + " — delivering what the run gathered"})
	report := "_The report could not be written (" + reason + ")._\n\n"
	confidence := "unknown"
	if analysis != nil && strings.TrimSpace(analysis.Answer) != "" {
		report += "The analysis it would have been written from:\n\n" + analysis.Answer
		confidence = analysis.Confidence
	} else {
		report += "Only the sources the run gathered remain; the saved report lists them under its citations."
	}
	return d.finish(&agent.ResearchResult{
		Question: plan.Question, Findings: findings, Analysis: analysis, FactCheck: fc,
		Summary: &agent.Summary{Report: report, Confidence: confidence}, Error: reason,
	}), nil
}

// research runs each sub-topic as a parallel sub-agent and collects findings.
// A sub-agent that fails reports it as an event and the run continues on what
// the others found, so there is no run-level error to return here.
func (d *Driver) research(ctx context.Context, plan *Plan) (findings []agent.Finding, uncovered []string) {
	var (
		mu          sync.Mutex
		all         []agent.Finding
		contributed = map[string]int{}
		wg          sync.WaitGroup
	)
	n := len(plan.SubTopics)
	if n == 0 {
		return nil, nil
	}
	perSource := plan.PerTopic()
	// A sub-agent issues one query, so its whole budget is that query's URL
	// allowance. Without this the search tool keeps its own fixed limit and
	// the depth tier cannot widen a run, only narrow it.
	d.Agent.SetSourceBudget(perSource)

	// Parallelism bounds how many sub-agents search at once. Without the
	// semaphore a wide plan opens one connection per sub-topic to SearXNG and
	// the scraper simultaneously, which is what the field exists to prevent.
	limit := d.Parallelism
	if limit <= 0 {
		limit = 1
	}
	sem := make(chan struct{}, limit)

	for _, sub := range plan.SubTopics {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(sub agent.SubTopic) {
			defer wg.Done()
			// Announce the sub-agent before queueing so the tree shows the
			// whole plan up front, with waiting branches marked queued rather
			// than missing until a slot frees up.
			d.emit(Event{Type: SubAgent, SubID: sub.ID, SubName: sub.Name, SubState: "queued", Line: "queued"})
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				d.emit(Event{Type: SubAgent, SubID: sub.ID, SubName: sub.Name, SubState: "done", Line: "aborted"})
				return
			}
			defer func() { <-sem }()
			kept := d.runSubAgent(ctx, plan.Question, sub, perSource, &mu, &all)
			mu.Lock()
			contributed[sub.ID] = kept
			mu.Unlock()
		}(sub)
	}
	wg.Wait()
	// Reported in plan order rather than completion order: sub-agents finish
	// in whatever order they finish, and the analysis prompt should not vary
	// between two runs that gathered the same evidence.
	for _, sub := range plan.SubTopics {
		if contributed[sub.ID] == 0 {
			uncovered = append(uncovered, sub.Name)
		}
	}
	return all, uncovered
}

// runSubAgent drives a single sub-agent to completion, emitting status,
// search, read, verify, citation and token events.
func (d *Driver) runSubAgent(ctx context.Context, question string, sub agent.SubTopic, perSource int, mu *sync.Mutex, all *[]agent.Finding) int {
	id := sub.ID
	kept, duplicates, offTopic := 0, 0, 0
	d.emit(Event{Type: SubAgent, SubID: id, SubName: sub.Name, SubState: "running", Line: "starting"})

	queries := subQueries(question, sub)
	for qi, q := range queries {
		if ctx.Err() != nil {
			d.emit(Event{Type: SubAgent, SubID: id, SubName: sub.Name, SubState: "done", Line: "aborted"})
			return kept
		}
		if kept >= perSource {
			break
		}
		d.emit(Event{Type: Search, Query: q, SubID: id, SubName: sub.Name, Line: "Searching: " + q})

		det, err := d.Agent.ResearchDetail(ctx, q)
		d.noteSearch(err)
		if err != nil {
			// A limiter or a challenge is a state of the run, so it travels on
			// the event rather than only inside the message text.
			status := tools.SearchStatus(err)
			d.emit(Event{Type: Error, Detail: "search failed: " + err.Error(), SubID: id,
				Status: status, Line: "search " + status})
			continue
		}
		// Results the search judged off-topic were never fetched, so they cost
		// no budget and are never cited. They are still reported: a run that
		// silently discarded most of what the engines returned would look
		// exactly like one whose search found almost nothing.
		offTopic += len(det.Skipped)
		for _, s := range det.Skipped {
			d.emit(Event{Type: Verify, URL: s.URL, Domain: s.Domain, Status: "offtopic", SubID: id,
				Line: verifyLabel(agent.SourceSignal{URL: s.URL, Domain: s.Domain, Status: "offtopic"})})
		}
		for si, f := range det.Findings {
			// The depth tier's whole effect is this budget: stop once the
			// sub-agent has taken its share of sources.
			if kept >= perSource {
				break
			}
			// A finding with no signal was never confirmed fetched. Defaulting
			// to "ok" claimed a clean 200 for a page nothing ever requested.
			signal := agent.SourceSignal{URL: f.URL, Domain: tools.DomainOf(f.URL), Status: "unverified"}
			if si < len(det.Signals) {
				signal = det.Signals[si]
			}
			// Carry the signal onto the finding: it is the only thing that
			// survives into the report and the exports.
			f.Status = signal.Status
			d.emit(Event{Type: Read, URL: f.URL, Domain: signal.Domain, SubID: id, Line: "read " + signal.Domain})
			d.emit(Event{Type: Verify, URL: f.URL, Domain: signal.Domain, Status: signal.Status, Code: signal.Code, SubID: id, Line: verifyLabel(signal)})

			// A dropped source retrieved nothing at all: no page text, not even
			// a search snippet. It is reported above so the reader sees what
			// failed, but it must not spend a slot of the budget, reach the
			// model as a blank finding, or appear in the citation list.
			if signal.Status == "dropped" {
				continue
			}
			// Another sub-agent already took this page. It is reported so the
			// duplication is visible, but counting and citing it again would
			// inflate the source total over the number of pages actually read.
			if !d.claimSource(f.URL) {
				duplicates++
				d.emit(Event{Type: Verify, URL: f.URL, Domain: signal.Domain,
					Status: "duplicate", SubID: id, Line: "= " + signal.Domain + " already cited"})
				continue
			}
			kept++

			sources, tokens := d.record()
			d.emit(Event{Type: Token, Tokens: tokens, Sources: sources})
			// URL and Detail repeat the source's identity in the fields the
			// timeline export keeps; SourceTitle/SourceURL alone left every
			// exported citation naming no source.
			d.emit(Event{Type: Citation, SourceTitle: f.Title, SourceURL: f.URL, URL: f.URL, Detail: f.Title,
				SubID: id, Sources: sources})

			// Report progress per source, not per query. A sub-topic usually
			// issues a single query, so query-granular progress would leave the
			// meter at 0% for the whole search and then jump straight to 100%.
			d.emit(Event{Type: SubAgent, SubID: id, SubName: sub.Name,
				Progress: queryProgress(qi, len(queries), kept, perSource)})

			mu.Lock()
			*all = append(*all, f)
			mu.Unlock()
		}
	}
	d.emit(Event{Type: SubAgent, SubID: id, SubName: sub.Name, SubState: "done", Progress: 100,
		Line: contributionLine(kept, duplicates, offTopic)})
	return kept
}

// noteSearch records the outcome of one search.
func (d *Driver) noteSearch(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.searchErr = err
	} else {
		d.searchesOK++
	}
}

// searchFailure is the run's error when it gathered nothing because no search
// ever answered.
func (d *Driver) searchFailure(findings []agent.Finding) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(findings) > 0 || d.searchesOK > 0 || d.searchErr == nil {
		return nil
	}
	return fmt.Errorf("every search failed (%s): %w", tools.SearchStatus(d.searchErr), d.searchErr)
}

// contributionLine says what a finished sub-agent actually added to the run.
// Every branch used to end on "complete", so one that gathered five sources
// and one that gathered none were indistinguishable in the live tree and in
// the exported notes — and a plan could quietly lose a whole facet, to noise
// or to another branch having claimed the same pages first.
func contributionLine(kept, duplicates, offTopic int) string {
	switch {
	case kept > 0:
		return "complete"
	case duplicates > 0:
		return "no new sources — already cited"
	case offTopic > 0:
		return "no relevant sources found"
	default:
		return "no sources found"
	}
}

// queryProgress is the completion percentage of a sub-agent that has finished
// done of total sources on query qi of n.
func queryProgress(qi, n, done, total int) int {
	n, total = max(n, 1), max(total, 1)
	p := (qi*total + done) * 100 / (n * total)
	return min(p, 99) // 100% is reserved for the terminal "done" event
}

// verifyLabel maps a signal status to a friendly status line.
func verifyLabel(s agent.SourceSignal) string {
	switch s.Status {
	case "ok":
		return "✓ " + s.Domain + " 200 OK"
	case "degraded":
		return "! " + s.Domain + " snippet only"
	case "dropped":
		return "✗ " + s.Code + " dropped"
	case "unverified":
		// LLM-search and offline modes never fetch anything, so the URL is
		// only as good as the model that produced it. Say so.
		return "~ " + s.Domain + " unverified"
	case "offtopic":
		// The engines returned this page for the query, but nothing in it
		// matched what was asked, so it was never fetched.
		return "≠ " + s.Domain + " off-topic"
	case "duplicate":
		// Another sub-agent already cited this page; it is shown so the
		// overlap between sub-topics is visible, and counted once.
		return "= " + s.Domain + " already cited"
	default:
		return s.Domain
	}
}

// maxQueryLen caps a generated search query. Search backends degrade sharply
// on long queries, so the facet is trimmed rather than the question.
const maxQueryLen = 100

// minFacetCols is the room a facet keeps even beside a question that fills the
// query cap by itself. Dropping the facet there made every branch issue the
// same query, so the plan collapsed into one search and a pile of duplicates.
const minFacetCols = 30

// subQueries is the list of search queries a sub-agent issues.
//
// Planner sub-topic names are facets of the question ("Performance
// Benchmarks"), not searchable questions in their own right, so every query is
// anchored on the research question. Searching a bare facet returns results
// with no connection to what was asked — "Training Data and Methodology"
// retrieves generic articles about training data, never the model under study.
//
// Notes is never searched as written: it is a full descriptive sentence, and
// backends degrade badly on prose. What it contributes is the follow-up — a
// few distilled terms, anchored on the question like the first query — issued
// only if the first query leaves the branch's budget unspent.
func subQueries(question string, sub agent.SubTopic) []string {
	primary := anchoredQuery(question, sub.Name)
	if primary == "" {
		return nil
	}
	// The second query is a re-formulation, not a second search: runSubAgent
	// stops issuing queries as soon as the branch has taken its share of
	// sources, so this one runs only when the first came back short. Without
	// it a sub-agent that found nothing simply reported nothing, and the
	// planner's own description of what to look for was never searched at all.
	facet := notesFacet(sub.Notes, question+" "+sub.Name)
	if facet == "" {
		return []string{primary}
	}
	if alt := anchoredQuery(question, facet); alt != "" && alt != primary {
		return []string{primary, alt}
	}
	return []string{primary}
}

// anchoredQuery joins the research question to one facet, trimmed to the
// backends' comfortable query length. The facet loses the trailing words
// rather than the question: a query that drops the subject retrieves generic
// articles about the facet and nothing about what was asked. Only a question
// too long to leave the facet minFacetCols gives up its own tail.
func anchoredQuery(question, facet string) string {
	q, facet := strings.TrimSpace(question), strings.TrimSpace(facet)
	switch {
	case q == "" && facet == "":
		return ""
	case q == "":
		return facet
	// The planner fallback names its one sub-topic after the question.
	case facet == "", strings.EqualFold(q, facet):
		return clipWords(q, maxQueryLen)
	}
	keep := min(dispWidth(facet), minFacetCols)
	q = clipWords(q, maxQueryLen-1-keep)
	facet = clipWords(facet, maxQueryLen-1-dispWidth(q))
	if facet == "" {
		return q
	}
	return q + " " + facet
}

// clipWords trims s to at most width columns, cutting at a word boundary when
// it has to cut at all. A single word wider than the budget is cut mid-word:
// some prefix of the subject beats none.
func clipWords(s string, width int) string {
	if dispWidth(s) <= width {
		return s
	}
	cut := strings.TrimSpace(queryPrefix(s, width))
	if i := strings.LastIndex(cut, " "); i > 0 && !strings.HasPrefix(s[len(cut):], " ") {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

// maxNotesTerms bounds the re-formulation: a handful of distinctive words
// from the planner's note, not the prose sentence it wrote. Backends degrade
// badly on prose, which is why Notes is never searched verbatim.
const maxNotesTerms = 4

// queryStopWords are the connective and instructional words a planner note is
// mostly made of ("investigate the current state of…"). Searching them adds
// nothing and crowds out the terms that actually narrow the query.
var queryStopWords = map[string]bool{
	"about": true, "accepted": true, "account": true, "across": true, "added": true,
	"analysis": true, "aspects": true, "assess": true, "available": true,
	"been": true, "both": true, "compare": true, "covering": true, "current": true,
	"detailed": true, "findings": true, "focus": true, "general": true,
	"overview": true, "primary": true, "relevant": true, "specific": true,
	"topic": true, "topics": true,
	"describe": true, "detail": true, "determine": true, "different": true,
	"documented": true, "evaluate": true, "examine": true, "explore": true,
	"from": true, "have": true, "identify": true, "include": true, "including": true,
	"information": true, "investigate": true, "into": true, "its": true, "look": true,
	"more": true, "most": true, "over": true, "provide": true, "recent": true,
	"related": true, "research": true, "review": true, "should": true, "such": true,
	"that": true, "their": true, "them": true, "these": true, "they": true,
	"this": true, "those": true, "understand": true, "used": true, "using": true,
	"various": true, "what": true, "when": true, "where": true, "which": true,
	"while": true, "with": true, "within": true, "your": true,
}

// notesFacet distils a planner note into a short list of distinctive terms.
// Words already in the question or the sub-topic name are dropped: repeating
// them would re-issue the query that already came back short. Fewer than two
// terms is not a different query, so it yields nothing.
func notesFacet(notes, exclude string) string {
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(exclude)) {
		seen[trimTerm(w)] = true
	}
	var terms []string
	for _, w := range strings.Fields(strings.ToLower(notes)) {
		w = trimTerm(w)
		if len([]rune(w)) < 4 || queryStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
		if len(terms) == maxNotesTerms {
			break
		}
	}
	if len(terms) < 2 {
		return ""
	}
	return strings.Join(terms, " ")
}

// trimTerm strips the punctuation a word carries inside a sentence.
func trimTerm(w string) string { return strings.Trim(w, ".,;:!?()[]\"'`—–-") }

// queryPrefix keeps complete runes within a display-column budget, without
// adding the ellipsis intended for UI labels.
func queryPrefix(s string, width int) string {
	used := 0
	for i, r := range s {
		used += runeWidth(r)
		if used > width {
			return s[:i]
		}
	}
	return s
}

// Prompt builders live here: the driver is the only thing that sequences the
// pipeline, so this is the one place that composes what each phase is asked.

// maxPromptFindingChars bounds how much of one source reaches a post-research
// prompt. Analyze, fact-check and summarize each re-embed every finding, so
// without a cap the prompt — and the call — grow with the whole run: a wide
// run put a quarter of a megabyte in front of the model, blew the two-minute
// call deadline mid-analysis and retried at four minutes, throwing away the
// two already spent. A source's opening is the part that says what it covers;
// the cap keeps that and drops the tail.
const maxPromptFindingChars = 1500

// writeFindings lists findings as numbered, content-bounded prompt entries.
//
// A finding no page backs is marked on its own line. One run can mix both
// kinds — the search falls back to the model per query — and a run-level flag
// let one fetched page vouch for every invented one beside it.
func writeFindings(sb *strings.Builder, findings []agent.Finding) {
	for i, f := range findings {
		mark := ""
		if !fetched(f) {
			mark = " " + neverFetched
		}
		sb.WriteString("  " + strconv.Itoa(i+1) + ". " + f.Title + " (" + f.URL + ")" + mark + "\n    ")
		sb.WriteString(clipPromptContent(f.Content))
		sb.WriteString("\n")
	}
	if n := countFetched(findings); n > 0 && n < len(findings) {
		sb.WriteString("  Entries marked " + neverFetched + " came from a language model's memory," +
			" not from a retrieved page: treat them as unverified.\n")
	}
}

// clipPromptContent caps one source at maxPromptFindingChars, marking the cut
// so the model reads a source as excerpted rather than as complete. The cut
// lands on a rune boundary: half a rune is not evidence.
func clipPromptContent(s string) string {
	if len(s) <= maxPromptFindingChars {
		return s
	}
	cut := maxPromptFindingChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …[truncated]"
}

func analyzePrompt(question string, findings []agent.Finding, uncovered []string) string {
	var sb strings.Builder
	sb.WriteString("Question: " + question + "\n\nFindings:\n")
	writeFindings(&sb, findings)
	// Naming the facets that came back empty is the only way the analysis can
	// report them: the findings list says what was gathered and never what was
	// planned and missed. Without it, a gap in the plan reaches the reader only
	// if the model happens to notice an absence.
	if len(uncovered) > 0 {
		sb.WriteString("\nPlanned sub-topics with no evidence gathered:\n")
		for _, name := range uncovered {
			sb.WriteString("  - " + name + "\n")
		}
	}
	return sb.String()
}

// factCheckPrompt asks the fact-checker to compare the analysis against the
// evidence. Both halves have to be here: sending the answer alone left the
// strictest phase of the pipeline checking the answer against itself.
func factCheckPrompt(answer string, findings []agent.Finding, retrieved bool) string {
	var sb strings.Builder
	if retrieved {
		sb.WriteString("Verify these claims against the research findings below.\n\nClaims:\n")
	} else {
		// Telling the checker what the material actually is stops it reporting
		// agreement with the model's own recollection as verification.
		sb.WriteString("No page was retrieved for this run: the findings below were" +
			" produced by a language model from memory, not fetched from their URLs." +
			" Check the claims for consistency with that material and with each other," +
			" and treat every claim as unverified — nothing here is evidence.\n\nClaims:\n")
	}
	sb.WriteString(answer)
	sb.WriteString("\n\nResearch findings (title, URL, source material):\n")
	writeFindings(&sb, findings)
	return sb.String()
}

// writeTopicEvidence gives the summarizer the analyzer's per-topic evidence
// and confidence, the structured half of the analysis the answer text flattens.
func writeTopicEvidence(sb *strings.Builder, topics []agent.Topic) {
	if len(topics) == 0 {
		return
	}
	sb.WriteString("Evidence by topic (with the analyzer's confidence):\n")
	for _, t := range topics {
		sb.WriteString("  - " + t.Name + " (" + t.Confidence + ")\n")
		for _, f := range t.Findings {
			sb.WriteString("      " + f + "\n")
		}
	}
	sb.WriteString("\n")
}

// neverFetched marks a prompt entry whose URL and text came from the model.
const neverFetched = "[never fetched]"

// fetched reports whether a finding came from a page the run actually
// retrieved. "ok" is a scraped page and "degraded" a search snippet; both
// reached the run from a search backend. "unverified" is the LLM-search path,
// where the model supplied the URL and the text behind it.
func fetched(f agent.Finding) bool { return f.Status == "ok" || f.Status == "degraded" }

// countFetched is how many findings a page actually backs.
func countFetched(findings []agent.Finding) int {
	n := 0
	for _, f := range findings {
		if fetched(f) {
			n++
		}
	}
	return n
}

// sourcesRetrieved reports whether any finding came from a fetched page. It
// picks which pass the fact-checker runs; the per-finding marks in the
// prompts are what keep a mixed run from vouching for its unfetched half.
func sourcesRetrieved(findings []agent.Finding) bool { return countFetched(findings) > 0 }

// factCheckDetail is the phase line the reader sees: which of the two passes
// is running, and how much of the evidence no page backs.
func factCheckDetail(findings []agent.Finding) string {
	n := countFetched(findings)
	switch {
	case n == 0:
		return "Checking self-consistency (no page was retrieved)"
	case n < len(findings):
		return fmt.Sprintf("Verifying claims against sources (%d of %d never fetched)", len(findings)-n, len(findings))
	}
	return "Verifying claims against sources"
}

func summarizePrompt(question string, analysis *agent.Analysis, fc *agent.FactCheckResult, findings []agent.Finding, retrieved bool) string {
	var sb strings.Builder
	sb.WriteString("Question: " + question + "\n\nSynthesized answer:\n" + analysis.Answer + "\n\n")
	writeTopicEvidence(&sb, analysis.Topics)
	sb.WriteString("Sources (title, URL, content) — link and quote these:\n")
	writeFindings(&sb, findings)
	// The reader cannot tell the two modes apart from the finished report, so
	// the report has to say which one produced it.
	if !retrieved {
		sb.WriteString("\nNo source was retrieved: every URL and its content above came" +
			" from the model's own memory and none was fetched. Say so in the report," +
			" present the findings as unverified recollection rather than evidence," +
			" and do not describe any claim as verified.\n")
	}
	// The fact-check verdicts are what let the report separate verified claims
	// from unsupported ones, so they belong in the summarizer's context.
	if fc != nil {
		sb.WriteString("\nFact-check results:\n")
		// The verdict is the flag, not the array: models echo the schema and
		// file a claim they rejected under "verified" with verified:false.
		for _, c := range fc.Verified {
			verdict := "unverified"
			if c.Verified {
				verdict = "verified"
			}
			sb.WriteString("  - " + verdict + ": " + c.Claim + " (" + c.Evidence + ")\n")
		}
		for _, c := range fc.Unverified {
			sb.WriteString("  - unverified: " + c + "\n")
		}
		for _, c := range fc.Contradictions {
			sb.WriteString("  - contradiction on " + c.Claim + " between: " + strings.Join(c.Sources, "; ") + "\n")
		}
	} else {
		sb.WriteString("\nFact-check did not run: present claims as unverified.\n")
	}
	return sb.String()
}
