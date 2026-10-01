// Package replay records the evidence a run gathered and serves it back, so
// the model phases (analyze, fact-check, summarize) can be run again on the
// same evidence.
//
// A live run searches the web and the planner writes a different plan each
// time, so two runs never see the same pages: a change to a prompt or a model
// could not be told apart from a change in what was found. A trace freezes the
// plan and every search result, and a replay spends model calls only on the
// phases under test.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

// Trace is one recorded run.
type Trace struct {
	Version  int    `json:"version"`
	Question string `json:"question"`
	// Mode and Sources are the run's depth tier and per-sub-agent source
	// budget: a replay at another budget would cut the recorded results
	// differently and no longer see the same evidence.
	Mode     string           `json:"mode"`
	Sources  int              `json:"sources,omitempty"`
	Model    string           `json:"model,omitempty"`
	Plan     []agent.SubTopic `json:"plan"`
	Searches []Search         `json:"searches"`
	// Pages holds each distinct page text once, keyed by URL. Searches return
	// the same page over and over (a vendor's pricing page came back from five
	// searches in one run), and storing it with every result made the page
	// text most of the file several times over.
	Pages map[string]string `json:"pages,omitempty"`
	// Phases are each model phase's prompt and parsed output, in call order,
	// for comparing a replay against the run it came from.
	Phases []Phase `json:"phases"`
}

// Search is one recorded search and what it returned. In a written trace the
// findings' text is in Trace.Pages: PageKeys names each finding's page, in
// finding order.
type Search struct {
	Query    string                `json:"query"`
	Terms    []string              `json:"terms,omitempty"`
	Result   *agent.ResearchDetail `json:"result,omitempty"`
	PageKeys []string              `json:"page_keys,omitempty"`
	Error    string                `json:"error,omitempty"`
}

// Phase is one model phase's input and output. The output is serialized when
// the phase returns: the Driver goes on to edit the analysis it got back
// (checkAnalysis drops sources and blocks recommendations), and a pointer
// kept until the trace was written recorded those edits as the model's.
type Phase struct {
	Name   string          `json:"name"`
	Prompt string          `json:"prompt"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Recorder wraps an assistant and records what it was asked and answered.
type Recorder struct {
	agent.Assistant
	mu    sync.Mutex
	trace Trace
	// checkpoint, when set, is where the trace is saved after each step, so
	// a run that is killed can resume from it (see Resume). saveMu keeps one
	// save at a time: sub-agents record searches in parallel.
	checkpoint string
	saveMu     sync.Mutex
	warn       func(error)
	warned     bool
	// finished are the queries already recorded whole, so a search a resume
	// serves again from them is not recorded twice.
	finished map[string]bool
}

// Record starts recording a run of question at the given depth and budget.
func Record(a agent.Assistant, question, mode string, sources int) *Recorder {
	return &Recorder{Assistant: a, trace: Trace{Version: 1, Question: question, Mode: mode, Sources: sources}}
}

func (r *Recorder) Plan(ctx context.Context, question string, n int) ([]agent.SubTopic, error) {
	subs, err := r.Assistant.Plan(ctx, question, n)
	r.mu.Lock()
	r.trace.Plan = subs
	r.mu.Unlock()
	return subs, err
}

func (r *Recorder) ResearchDetail(ctx context.Context, query string, terms []string) (*agent.ResearchDetail, error) {
	det, err := r.Assistant.ResearchDetail(ctx, query, terms)
	s := Search{Query: query, Terms: terms, Result: det}
	switch {
	case err != nil:
		s.Error = err.Error()
	case ctx.Err() != nil:
		// A cancel mid-search returns the pages fetched so far and no error.
		// Recorded as a whole search, a resume would never finish it.
		s.Error = "cancelled before the search finished"
	}
	r.mu.Lock()
	if s.Error != "" || !r.finished[query] {
		r.trace.Searches = append(r.trace.Searches, s)
	}
	if s.Error == "" {
		if r.finished == nil {
			r.finished = map[string]bool{}
		}
		r.finished[query] = true
	}
	r.mu.Unlock()
	r.save()
	return det, err
}

// Seed starts the recording from what an interrupted run saved: its plan and
// finished searches. A resumed run that recorded from nothing saved over its
// checkpoint with the plan alone, and a second stop lost every search the
// first run had made.
func (r *Recorder) Seed(t *Trace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Plan, r.trace.Mode, r.trace.Sources = slices.Clone(t.Plan), t.Mode, t.Sources
	if r.finished == nil {
		r.finished = map[string]bool{}
	}
	for _, s := range t.Searches {
		if s.Error == "" && s.Result != nil && !r.finished[s.Query] {
			r.trace.Searches = append(r.trace.Searches, s)
			r.finished[s.Query] = true
		}
	}
}

func (r *Recorder) Analyze(ctx context.Context, prompt string) (*agent.Analysis, error) {
	out, err := r.Assistant.Analyze(ctx, prompt)
	r.phase("analyze", prompt, out, err)
	return out, err
}

func (r *Recorder) FactCheck(ctx context.Context, prompt string) (*agent.FactCheckResult, error) {
	out, err := r.Assistant.FactCheck(ctx, prompt)
	r.phase("fact-check", prompt, out, err)
	return out, err
}

func (r *Recorder) Summarize(ctx context.Context, prompt string) (*agent.Summary, error) {
	out, err := r.Assistant.Summarize(ctx, prompt)
	r.phase("summarize", prompt, out, err)
	return out, err
}

func (r *Recorder) phase(name, prompt string, out any, err error) {
	b, _ := json.Marshal(out)
	p := Phase{Name: name, Prompt: prompt, Output: b}
	if err != nil {
		p.Error = err.Error()
	}
	r.mu.Lock()
	r.trace.Phases = append(r.trace.Phases, p)
	r.mu.Unlock()
}

// SetPlan records the plan the run actually used, with its tier and pinned
// per-sub-agent budget. The planner's answer is not it: the brief can rename,
// add and delete sub-topics and change the tier, and a plan handed in with
// --plan never calls the planner at all, which left a trace with no plan that
// Load refused. The flags are not it either: --plan carries its own tier and
// budget, and a replay at the flags' tier cut the recorded results
// differently from the run it reproduces.
func (r *Recorder) SetPlan(subs []agent.SubTopic, mode string, sources int) {
	r.mu.Lock()
	r.trace.Plan = slices.Clone(subs)
	r.trace.Mode, r.trace.Sources = mode, sources
	r.mu.Unlock()
	r.save()
}

// Checkpoint saves the trace to path after each step from the plan on, and
// reports a save that fails to warn once: a run is not stopped because its
// checkpoint could not be written.
func (r *Recorder) Checkpoint(path string, warn func(error)) {
	r.saveMu.Lock()
	r.checkpoint, r.warn = path, warn
	r.saveMu.Unlock()
}

// Discard removes the checkpoint of a run that finished, and stops saving.
// A run that records nothing has a nil recorder and nothing to remove.
func (r *Recorder) Discard() error {
	if r == nil {
		return nil
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	path := r.checkpoint
	r.checkpoint = ""
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// save writes the checkpoint once there is a plan to resume.
func (r *Recorder) save() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	r.mu.Lock()
	planned := len(r.trace.Plan) > 0
	r.mu.Unlock()
	if r.checkpoint == "" || !planned {
		return
	}
	if err := r.Write(r.checkpoint); err != nil && !r.warned {
		r.warned = true
		if r.warn != nil {
			r.warn(err)
		}
	}
}

// Write saves the trace to path.
func (r *Recorder) Write(path string) error {
	r.mu.Lock()
	t := r.trace
	t.Searches = slices.Clone(t.Searches)
	r.mu.Unlock()
	t.Model, _ = modelInfo(r.Assistant)
	t.Pages = map[string]string{}
	for i, s := range t.Searches {
		if s.Result == nil {
			continue
		}
		// The recorded results are the ones the run holds: copy before
		// moving their text out.
		det := *s.Result
		det.Findings = slices.Clone(det.Findings)
		s.PageKeys = make([]string, len(det.Findings))
		for j, f := range det.Findings {
			s.PageKeys[j], det.Findings[j].Content = t.addPage(f.URL, f.Content), ""
		}
		s.Result = &det
		t.Searches[i] = s
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// writeAtomic replaces path with b whole: a checkpoint is rewritten while the
// run goes, and a kill mid-write must leave the previous one, not half of it.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// Load reads a trace written by Recorder.Write.
func Load(path string) (*Trace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Trace
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("read trace %s: %w", path, err)
	}
	if t.Question == "" || len(t.Plan) == 0 {
		return nil, fmt.Errorf("read trace %s: no question or plan recorded", path)
	}
	for _, s := range t.Searches {
		for j, key := range s.PageKeys {
			if s.Result != nil && j < len(s.Result.Findings) {
				s.Result.Findings[j].Content = t.Pages[key]
			}
		}
	}
	return &t, nil
}

// addPage stores a page's text under its URL and returns the key. The same
// URL can come back with different text (a search snippet once, the scraped
// page another time), and both are evidence the run held, so a second text
// gets a numbered key rather than replacing the first.
func (t *Trace) addPage(url, content string) string {
	for n := 1; ; n++ {
		key := url
		if n > 1 {
			key = url + " #" + strconv.Itoa(n)
		}
		old, taken := t.Pages[key]
		if !taken {
			t.Pages[key] = content
		}
		if !taken || old == content {
			return key
		}
	}
}

// Player serves a trace's plan and search results and passes the model
// phases to the live assistant it wraps.
type Player struct {
	agent.Assistant
	trace    *Trace
	byQuery  map[string]Search
	analyzed atomic.Bool
}

// Play replays t, calling live only for the model phases.
func Play(live agent.Assistant, t *Trace) *Player {
	p := &Player{Assistant: live, trace: t, byQuery: map[string]Search{}}
	for _, s := range t.Searches {
		if _, seen := p.byQuery[s.Query]; !seen {
			p.byQuery[s.Query] = s
		}
	}
	return p
}

func (p *Player) Plan(context.Context, string, int) ([]agent.SubTopic, error) {
	return p.trace.Plan, nil
}

// Analyze runs the live analysis. The first one's follow-up queries are
// replaced by the recorded first analysis's: the replayed analysis words its
// own, which the recorded run never searched, and the follow-up round would
// find nothing and end the replay on less evidence than the run it is
// compared against.
func (p *Player) Analyze(ctx context.Context, prompt string) (*agent.Analysis, error) {
	a, err := p.Assistant.Analyze(ctx, prompt)
	if err != nil || p.analyzed.Swap(true) {
		return a, err
	}
	if recorded := recordedFollowUps(p.trace); recorded != nil {
		a.FollowUp = recorded
	}
	return a, nil
}

// recordedFollowUps are the follow-up queries of the recorded run's first
// analysis, the ones its follow-up round searched.
func recordedFollowUps(t *Trace) []string {
	for _, ph := range t.Phases {
		if ph.Name != "analyze" {
			continue
		}
		var a agent.Analysis
		if json.Unmarshal(ph.Output, &a) != nil {
			return nil
		}
		return a.FollowUp
	}
	return nil
}

// ResearchDetail returns what the recorded run's search returned. A query the
// recorded run never issued — a follow-up the replayed analysis thought of —
// fails rather than searching live: the point of a replay is that the
// evidence does not change.
func (p *Player) ResearchDetail(_ context.Context, query string, _ []string) (*agent.ResearchDetail, error) {
	s, ok := p.byQuery[query]
	if !ok {
		return nil, fmt.Errorf("replay: %q was not searched in the recorded run", query)
	}
	if s.Error != "" {
		return nil, fmt.Errorf("replay: recorded search failed: %s", s.Error)
	}
	return s.Result, nil
}

// Resumer serves the searches a checkpoint recorded and goes live for the
// rest: a search the interrupted run never finished, or one that failed,
// often the failure being recovered from (a search server that was down).
// The model phases always run live: governance edits what they return, and
// the analysis retry depends on asking again.
type Resumer struct {
	agent.Assistant
	byQuery map[string]Search
}

// Resume continues t on live.
func Resume(live agent.Assistant, t *Trace) *Resumer {
	r := &Resumer{Assistant: live, byQuery: map[string]Search{}}
	for _, s := range t.Searches {
		if s.Error == "" && s.Result != nil {
			r.byQuery[s.Query] = s
		}
	}
	return r
}

func (r *Resumer) ResearchDetail(ctx context.Context, query string, terms []string) (*agent.ResearchDetail, error) {
	if s, ok := r.byQuery[query]; ok {
		return s.Result, nil
	}
	return r.Assistant.ResearchDetail(ctx, query, terms)
}

func (r *Resumer) ModelInfo() (string, string) { return modelInfo(r.Assistant) }
func (r *Resumer) ServedModels() []string      { return servedModels(r.Assistant) }
func (r *Resumer) SetStatus(f func(string))    { setStatus(r.Assistant, f) }

// The driver and UI look for these optional methods on the assistant. An
// embedded interface does not promote them, so each wrapper forwards them.

func (r *Recorder) ModelInfo() (string, string) { return modelInfo(r.Assistant) }
func (r *Recorder) ServedModels() []string      { return servedModels(r.Assistant) }
func (r *Recorder) SetStatus(f func(string))    { setStatus(r.Assistant, f) }
func (p *Player) ModelInfo() (string, string)   { return modelInfo(p.Assistant) }
func (p *Player) ServedModels() []string        { return servedModels(p.Assistant) }
func (p *Player) SetStatus(f func(string))      { setStatus(p.Assistant, f) }

func modelInfo(a agent.Assistant) (string, string) {
	if m, ok := a.(interface{ ModelInfo() (string, string) }); ok {
		return m.ModelInfo()
	}
	return "", ""
}

func servedModels(a agent.Assistant) []string {
	if m, ok := a.(interface{ ServedModels() []string }); ok {
		return m.ServedModels()
	}
	return nil
}

func setStatus(a agent.Assistant, f func(string)) {
	if s, ok := a.(interface{ SetStatus(func(string)) }); ok {
		s.SetStatus(f)
	}
}
