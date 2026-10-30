package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Action constants returned from the brief confirmation loop.
const (
	actionLaunch = iota
	actionCancel
	// actionClarify asks the plan's questions again; ui.Run, not the brief,
	// makes the new plan.
	actionClarify
)

// Options configures a UI-driven research run.
type Options struct {
	Question        string
	Assistant       agent.Assistant
	DepthMode       string     // "quick" | "standard" | "deep" | "" (standard)
	SourcesPerTopic int        // sources per sub-agent; 0 => derived from depth
	SubTopics       int        // exact sub-topic count; 0 => the planner chooses up to the tier's
	NoFollowUps     bool       // skip the follow-up round
	Instant         bool       // answer in one call after the search, unchecked (instant mode)
	Report          ReportSpec // the report's shape; the zero value for standard
	Parallelism     int        // sub-agents searching concurrently; 0 => 3
	JSONL           bool       // emit structured JSONL events
	Quiet           bool       // minimal, non-interactive output
	NoColor         bool       // disable ANSI colour
	OutDir          string     // report output dir (default "reports")
	Input           io.Reader  // interactive stdin; nil => non-interactive
	Stdout          io.Writer
	Stderr          io.Writer
	Bell            func(string)     // OS/notification hook
	Now             func() time.Time // clock (test seam)
	KeyScript       string           // scripted key sequence for tests (non-TTY)
	// Plan, when set, is run as it is: no planning call and no brief, since
	// it was confirmed where it was edited. DepthMode and SourcesPerTopic do
	// not apply to it.
	Plan *Plan
	// PlanOnly stops once the plan is made and emitted, before any search.
	PlanOnly bool
	// OnPlan, when set, is given the plan once it is confirmed, before the
	// first search: the plan a resumed run must run again.
	OnPlan func(*Plan)
	// Domains confines the search to these sites, each checked before the
	// run plans. A Plan handed in brings its own.
	Domains []string
	// Clarify asks what the question leaves open before planning, when
	// there is someone to ask: the terminal, or --clarify-only's caller.
	Clarify bool
	// ClarifyOnly stops once the questions are asked, before planning,
	// with the questions in the result and on the clarify event.
	ClarifyOnly bool
	// Answers, when set, are what the reader already clarified (--answers):
	// the run asks nothing and plans for them.
	Answers *Clarified
}

// RunResult carries the artifacts produced by a run.
type RunResult struct {
	// Plan is the plan the run used, set once planning succeeds.
	Plan *Plan
	// Clarified is a --clarify-only run's questions.
	Clarified *Clarified
	Report    *agent.ResearchResult
	MDPath    string
	PDFPath   string
	JSONPath  string
	// TracePath is the --trace file, written by the CLI after the run.
	TracePath string
	Cancelled bool
	// KeysHeld is set when the run could not stop reading keys from the
	// terminal: what is typed at it next goes to that reader, not to a
	// prompt opened after the run.
	KeysHeld bool
}

// Paths lists the artifacts that were actually written; a failed write left
// its path empty.
func (r RunResult) Paths() []string {
	var paths []string
	for _, p := range []string{r.MDPath, r.PDFPath, r.JSONPath, r.TracePath} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// Run executes the full interactive (or headless) research experience.
func Run(ctx context.Context, opts Options) (RunResult, error) {
	o := opts.withDefaults()

	renderer := NewRenderer(o.Stdout, Theme{Enabled: !o.NoColor && isTTYWriter(o.Stdout)})
	// The renderer hides the cursor and runs an animation goroutine while the
	// live frame is up. Both are released here so an error path cannot leave
	// the terminal without a cursor.
	defer renderer.Close()

	interactive := isTTYReader(o.Input) && isTTYWriter(o.Stdout) && !o.Quiet && !o.JSONL
	sink := o.buildSink(renderer, interactive)
	// Planning runs before the driver exists, and under --jsonl nothing else
	// is on the stream while it does: a slow or retried plan call looked
	// exactly like a hung process to whatever was watching.
	if o.JSONL {
		wireProgress(o.Assistant, func(e Event) { e.Phase = "Plan"; sink.Emit(e) })
	}

	script := o.scriptInput()
	plan, usable, err := o.scopedPlan(ctx, sink, o.asker(renderer, interactive, script))
	if err != nil {
		return endedBeforePlan(ctx, err)
	}

	sig := catchSignals(interactive)
	input := o.openInput(interactive, script)
	defer input.Close()
	defer guardTerminal(sig, input, renderer)()

	// Phase 1: brief confirmation.
	var cancelled bool
	if plan, cancelled = o.confirmBrief(ctx, input, renderer, plan, interactive); cancelled {
		return RunResult{Cancelled: true}, nil
	}
	if o.OnPlan != nil {
		o.OnPlan(plan)
	}
	sink.Emit(Event{Type: PlanReady, Plan: plan})
	res := RunResult{Plan: plan}
	if o.PlanOnly {
		return res, nil
	}

	// Phase 2: live research via the driver.
	driver := NewDriver(o.Assistant, sink, input, o.Parallelism)
	driver.now = o.Now
	driver.domains = usable
	driver.toggleActivity = renderer.ToggleActivity
	if interactive || o.JSONL {
		// A retried model call is the assistant's own progress line. Without
		// routing it into the events, a retry that takes minutes looks like a
		// frozen spinner on the frame and like silence on the stream. Other
		// non-interactive runs keep the stderr logger the CLI installed.
		wireProgress(o.Assistant, driver.emit)
	}

	result, err := driver.Run(ctx, plan)
	if err != nil {
		return ended(ctx, res, err)
	}
	res.Report = result
	res.Cancelled = false

	// Phase 3: report + exports.
	// In JSONL mode stdout carries only the structured stream, so the human
	// report is rendered only when we are not emitting JSONL.
	if !o.JSONL && !o.Quiet {
		renderer.RenderReport(result, driver.sources, driver.tokens)
	}
	res.MDPath, res.PDFPath, res.JSONPath = o.writeArtifacts(result, sink, driver, plan)
	o.announce(result, res)
	res.KeysHeld = driver.stopKeys()
	return res, nil
}

// endedBeforePlan reports a run that stopped before it had a plan: at the
// questions, by the reader's Ctrl-C or with a --clarify-only run's
// questions, or on err.
func endedBeforePlan(ctx context.Context, err error) (RunResult, error) {
	var stopped errStopped
	switch {
	case errors.As(err, &stopped) && stopped.c != nil:
		return RunResult{Clarified: stopped.c}, nil
	case errors.As(err, &stopped):
		return RunResult{Cancelled: true}, nil
	}
	// A cancel can arrive while the plan call is still out (the browser's
	// cancel button can press it there); it is the same stop as Esc later.
	return ended(ctx, RunResult{}, err)
}

// ended reports a run that stopped on err. Esc or the cancel button is a
// deliberate stop, so it reports as a cancellation rather than a failure; a
// timeout still fails, because a deadline that expired is not something the
// reader asked for.
func ended(ctx context.Context, res RunResult, err error) (RunResult, error) {
	if errors.Is(err, context.Canceled) && ctx.Err() != context.DeadlineExceeded {
		res.Cancelled = true
		return res, nil
	}
	return RunResult{}, err
}

// errStopped ends a run before its plan: the reader pressed Ctrl-C at a
// question, or c holds the questions a --clarify-only run asked.
type errStopped struct{ c *Clarified }

func (errStopped) Error() string { return "stopped before planning" }

// scopedPlan checks the run's domain list, asks what the question leaves
// open, then plans: a list with nothing usable on it ends the run before any
// model call. usable is what the search was confined to; the plan keeps the
// list as asked.
func (o Options) scopedPlan(ctx context.Context, sink *MultiSink, ask askFunc) (plan *Plan, usable []string, err error) {
	if o.ClarifyOnly {
		// The sites are the plan's to check: the stage after this one runs
		// the same check, and fetched twice each would take twice as long.
		return nil, nil, o.clarifyOnly(ctx, sink)
	}
	domains := o.Domains
	if o.Plan != nil && len(domains) == 0 {
		domains = o.Plan.Domains
	}
	// Parsed here, where every run passes, and not only where a list is
	// typed: a resumed checkpoint's list went straight to the search, so
	// "localhost" was searched and "GO.DEV" matched no result at all.
	if domains, err = tools.ParseDomains(domains); err != nil {
		return nil, nil, err
	}
	if usable, err = o.checkSources(ctx, sink, domains); err != nil {
		return nil, nil, err
	}
	c, stop, err := o.clarified(ctx, ask)
	switch {
	case err != nil:
		return nil, nil, err
	case stop:
		return nil, nil, errStopped{}
	}
	if plan, err = o.plan(ctx, c); err != nil {
		return nil, nil, err
	}
	plan.Domains = domains
	return plan, usable, nil
}

// clarifyOnly asks the clarifier and sends what it asked, none when it
// asked nothing or clarify is off: the caller moves on to the plan the same
// way either way.
func (o Options) clarifyOnly(ctx context.Context, sink *MultiSink) error {
	c, _, err := o.clarified(ctx, nil)
	if err != nil {
		return err
	}
	qs := c.Questions
	if qs == nil {
		qs = []Question{}
	}
	sink.Emit(Event{Type: ClarifyReady, Questions: &qs})
	return errStopped{c: &Clarified{Questions: qs, Answers: []Clarification{}}}
}

// plan is the plan to run: the one handed in, or the planner's for the
// reading c.
func (o Options) plan(ctx context.Context, c Clarified) (*Plan, error) {
	if o.Plan != nil {
		return o.Plan, nil
	}
	return o.buildPlan(ctx, c)
}

// asker is who answers the clarifier's questions: the terminal, the scripted
// keys, or nobody.
func (o Options) asker(r *Renderer, interactive bool, script Input) askFunc {
	switch {
	case script != nil && !o.JSONL:
		return askScripted(script, r)
	case interactive:
		return o.askOnTerminal()
	}
	return nil
}

// buildPlan runs the planning phase for the reading c and applies the
// configured source budget.
func (o Options) buildPlan(ctx context.Context, c Clarified) (*Plan, error) {
	// Planning is one model call with nothing on screen behind it, so it is
	// the whole of the dead air between launching the CLI and the brief.
	stopStatus := func() {}
	if !o.Quiet && !o.JSONL {
		var setStatus func(string)
		setStatus, stopStatus = StatusSpinner(o.Stderr, "Planning research")
		// The assistant reports which model it is asking and when it retries.
		// Both happen inside this call, and the progress logger was only wired
		// after it, so the one phase with nothing on screen behind it was also
		// the one phase that explained nothing.
		o.Assistant.SetProgress(setStatus)
		// Hand the logger back once the spinner is gone: it writes to a row
		// that no longer exists, and the CLI's own stderr logger is what the
		// non-interactive phases expect.
		defer o.Assistant.SetProgress(func(msg string) { _, _ = fmt.Fprintf(o.Stderr, "%s\n", msg) })
	}
	// One sub-topic is the question itself: a planner call to rephrase it
	// would take longer than the search it plans.
	if o.Instant && o.SubTopics == 1 {
		plan := InstantPlan(o.Question, depthMode(o.DepthMode), o.SourcesPerTopic)
		plan.NoFollowUps, plan.Report = o.NoFollowUps, o.Report
		plan.Questions, plan.Clarifications = c.Questions, c.Answers
		return plan, nil
	}
	planner := NewPlanner(o.Assistant, depthMode(o.DepthMode))
	planner.SubTopics, planner.Reading = o.SubTopics, c.Answers
	plan, err := planner.Plan(ctx, o.Question)
	stopStatus()
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	plan.NoFollowUps, plan.Report, plan.Instant = o.NoFollowUps, o.Report, o.Instant
	plan.Questions, plan.Clarifications = c.Questions, c.Answers
	if o.SourcesPerTopic <= 0 {
		return plan, nil
	}
	// A request the run budget cannot honour is reported, not silently
	// reduced: --sources 20 across four sub-agents used to yield 12.
	plan, capped := plan.WithSourcesPerTopic(o.SourcesPerTopic)
	if capped {
		_, _ = fmt.Fprintf(o.Stderr,
			"warning: %d sources per sub-agent exceeds the %d-source run budget; using %d per sub-agent\n",
			o.SourcesPerTopic, maxRunSources, plan.PerTopic())
	}
	return plan, nil
}

// confirmBrief runs the plan-confirmation loop and reports whether the reader
// cancelled. It is skipped under --jsonl: stdout is the machine stream there,
// so drawing a brief would corrupt it, and there is no human present to
// confirm a plan.
//
// A plan handed in was confirmed where it was edited, a plan-only run
// stops before anyone could act on a confirmation, and an instant run has
// no brief: waiting on one would take longer than the run.
func (o Options) confirmBrief(ctx context.Context, input Input, r *Renderer, plan *Plan, interactive bool) (*Plan, bool) {
	if o.Plan != nil || o.PlanOnly || plan.Instant || (!interactive && (o.KeyScript == "" || o.JSONL)) {
		return plan, false
	}
	// --depth / max_depth set the starting budget; pressing "d" is a later,
	// explicit choice by the reader, so the tier it selects wins outright.
	// Re-applying the flag here is what made the key look dead: it cycled the
	// label while pinning every visible number to the configured value.
	// Keys typed while planning are discarded — a plan cannot be confirmed
	// before it is shown — but a dropped Enter with no word read as a hang.
	if in, ok := input.(interface{ Ignored() int }); ok && in.Ignored() > 0 {
		keys := "1 key typed while planning was"
		if n := in.Ignored(); n > 1 {
			keys = fmt.Sprintf("%d keys typed while planning were", n)
		}
		r.SetBriefNote(keys + " ignored — review the plan, then press Enter")
	}
	// Drawn in place, as the start screen it follows is, and cleared after:
	// drawn from the top of the screen, a cancelled brief brought the start
	// screen back at the top, not where it had been. A launch clears it
	// before the live frame takes the screen.
	keys := &keptInput{Input: input}
	if interactive {
		defer r.Inline(keys)()
	}
	for {
		var action int
		plan, action = waitBrief(keys, r, plan)
		if action != actionClarify {
			return plan, action == actionCancel
		}
		var stop bool
		if plan, stop = o.reclarify(ctx, keys, r, plan); stop {
			return plan, true
		}
	}
}

// buildSink sends events to the machine stream, the live UI or, on a pipe,
// the renderer's one-line-per-event log. --jsonl never draws either (the
// stream owns stdout), and a terminal stdout with a piped stdin gets neither:
// it is not plain, so it would draw a live frame with no key reader.
func (o Options) buildSink(renderer *Renderer, interactive bool) *MultiSink {
	var sinks []Sink
	if o.JSONL {
		sinks = append(sinks, JSONL{W: o.Stdout})
	}
	if interactive || (renderer.plain && !o.Quiet && !o.JSONL) {
		sinks = append(sinks, renderer)
	}
	return &MultiSink{Sinks: sinks}
}

// wireProgress routes the assistant's progress into events: which model is
// asked and each retry as Info, streamed-phase status as transient Info — one
// row replaced in place on the frame, not an activity line every five seconds
// — and the reasoning behind it as Thinking, which only the page shows.
func wireProgress(a agent.Assistant, emit func(Event)) {
	a.SetProgress(func(msg string) { emit(Event{Type: Info, Detail: msg}) })
	if s, ok := a.(interface{ SetStatus(func(string)) }); ok {
		s.SetStatus(func(msg string) { emit(Event{Type: Info, Detail: msg, Transient: true}) })
	}
	if s, ok := a.(interface {
		SetThinking(func(string, string, bool))
	}); ok {
		s.SetThinking(func(text, part string, first bool) {
			e := Event{Type: Thinking, Detail: text, Part: part}
			if first {
				e.Status = "start"
			}
			emit(e)
		})
	}
}

// scriptInput is the scripted keys, read once for the questions and the
// brief alike; nil without a script.
func (o Options) scriptInput() Input {
	if o.KeyScript == "" {
		return nil
	}
	return NewByteReader([]byte(o.KeyScript))
}

// openInput returns the single Input used for both the brief and the run.
// Acquiring it twice would put the terminal in raw mode, then capture that
// already-raw state as the "original" to restore, leaving the shell without
// echo after the run.
func (o Options) openInput(interactive bool, script Input) Input {
	switch {
	case script != nil:
		return script
	case interactive:
		return NewInput(o.Input, func(msg string) {
			_, _ = fmt.Fprintf(o.Stderr, "warning: %s\n", msg)
		})
	default:
		return NewByteReader(nil)
	}
}

// writeArtifacts persists the markdown, PDF and metadata exports. A write that
// fails is reported on stderr and yields an empty path, so a missing artifact
// is visible instead of quietly absent from the "Saved to" list.
func (o Options) writeArtifacts(result *agent.ResearchResult, sink *MultiSink, driver *Driver, plan *Plan) (md, pdf, meta string) {
	keep := func(path string, err error) string {
		if err != nil {
			_, _ = fmt.Fprintf(o.Stderr, "warning: could not write %s: %v\n", path, err)
			return ""
		}
		return path
	}
	md = keep(WriteMarkdown(result, o.OutDir))
	pdf = keep(WritePDF(result, o.OutDir))
	if pdf != "" && pdfDropsText(MarkdownReport(result)) {
		_, _ = fmt.Fprintln(o.Stderr, "warning: PDF export replaced unsupported characters with ?; use the Markdown or JSON artifact for the full Unicode text")
	}
	// Snapshot the timeline under the sink's lock so the export never reads
	// a slice mid-append.
	m := BuildMeta(result, sink.TimelineSnapshot(), plan.Depth.Key, driver.tokens, driver.sources)
	m.ReportSpec = plan.Report
	m.Clarifications = plan.Clarifications
	meta = keep(WriteMetadata(m, o.OutDir))
	return md, pdf, meta
}

// announce closes the run out for a human: the report on --silent, otherwise
// a notification and the list of saved artifacts.
//
// JSONL is checked first because it is the only mode with a second writer on
// stdout. With --jsonl --silent the Quiet case used to win and wrote a
// rendered Markdown report into the middle of the event stream, so the stream
// documented as machine-readable did not parse.
func (o Options) announce(result *agent.ResearchResult, res RunResult) {
	switch {
	case o.JSONL:
		// The event stream owns the writer; nothing human-readable joins it.
	case o.Quiet:
		_, _ = fmt.Fprintf(o.Stdout, "%s\n", FormatMarkdown(o.Stdout, WithReading(result.Summary.Report, result.Clarifications), o.NoColor))
	default:
		if o.Bell != nil {
			o.Bell(fmt.Sprintf("Research complete: %s", result.Question))
		}
		reportFiles(o.Stdout, o.OutDir, res)
	}
}

func (o Options) withDefaults() Options {
	if o.OutDir == "" {
		o.OutDir = "reports"
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Bell == nil {
		o.Bell = notify
	}
	if o.DepthMode == "" {
		o.DepthMode = "standard"
	}
	return o
}

// waitBrief drives the brief confirmation loop. It returns the final plan and
// the action: actionLaunch or actionCancel.
func waitBrief(in Input, r *Renderer, plan *Plan) (*Plan, int) {
	// Draw the brief before blocking on a key. Without this the loop waits on
	// an empty screen, with nothing to say a plan exists or that Enter runs it.
	r.RenderBrief(plan)
	for {
		k, err := readKey(in)
		if err != nil {
			return plan, actionLaunch // EOF => launch
		}
		switch k.Key {
		case 0x0d, 0x0a, 0x00: // Enter / CR / NUL
			return plan, actionLaunch
		case KeyQ, KeyEsc, KeyCtrlC:
			return plan, actionCancel
		case KeyC:
			if len(plan.Questions) > 0 {
				return plan, actionClarify
			}
		case KeyE:
			// In line mode the topic was typed on the same line as the key, so
			// it is already in hand and there is no second line to wait for.
			name, ok := readPromptSeeded(in, k.Rest, func(text string, cursor int) {
				r.SetPromptAt("add sub-topic", text, cursor)
			})
			r.SetPrompt("", "")
			if ok && name != "" {
				plan.SubTopics = append(plan.SubTopics, SubTopic{
					Name:  truncate(name, 60),
					Notes: "added by you",
				})
				plan = renumbered(plan)
			}
			r.RenderBrief(plan)
		case KeyR:
			renameTopic(in, r, plan, k.Rest)
			r.RenderBrief(plan)
		case KeyX:
			plan = deleteTopic(in, r, plan, k.Rest)
			r.RenderBrief(plan)
		default:
			// A review setting's key moves it to its next choice, through the
			// same ApplyToPlan the serve page's review uses; other keys do
			// nothing.
			if s, ok := briefSetting(k.Key); ok {
				if np, err := ApplyToPlan(plan, s.Key, s.Next(PlanValue(plan, s.Key))); err == nil {
					plan = np
				}
				r.RenderBrief(plan)
			}
		}
	}
}

// briefSetting is the review setting key cycles in the brief.
func briefSetting(k Key) (Setting, bool) {
	for _, s := range Settings(RunSettings{}) {
		if s.Review && s.BriefKey != 0 && s.BriefKey == k {
			return s, true
		}
	}
	return Setting{}, false
}

// renameTopic edits one sub-topic's name in place. The prompt opens seeded
// with the current name, so a small correction does not mean retyping it.
func renameTopic(in Input, r *Renderer, plan *Plan, seed string) {
	i, ok := pickTopic(in, r, plan, "rename which sub-topic", seed)
	if !ok {
		return
	}
	name, done := readPromptEdit(in, plan.SubTopics[i].Name, func(text string, cursor int) {
		r.SetPromptAt("rename sub-topic", text, cursor)
	})
	r.SetPrompt("", "")
	if name = truncate(name, 60); done && name != "" && name != plan.SubTopics[i].Name {
		// The planner's query and terms were written for the old name; kept,
		// the branch would search and rank for a facet the reader replaced.
		plan.SubTopics[i] = SubTopic{ID: plan.SubTopics[i].ID, Name: name, Notes: plan.SubTopics[i].Notes}
	}
}

// deleteTopic drops one sub-topic. The last one is not deletable: a plan with
// no branches has nothing to research.
func deleteTopic(in Input, r *Renderer, plan *Plan, seed string) *Plan {
	i, ok := pickTopic(in, r, plan, "delete which sub-topic", seed)
	if !ok || len(plan.SubTopics) < 2 {
		return plan
	}
	plan.SubTopics = slices.Delete(plan.SubTopics, i, i+1)
	return renumbered(plan)
}

// pickTopic asks which sub-topic a key applies to and returns its index. An
// answer that is not a listed number is treated as "never mind", so a typo
// cannot delete the wrong branch.
func pickTopic(in Input, r *Renderer, plan *Plan, label, seed string) (int, bool) {
	answer, ok := readPromptSeeded(in, seed, func(text string, cursor int) {
		r.SetPromptAt(fmt.Sprintf("%s (1-%d)", label, len(plan.SubTopics)), text, cursor)
	})
	r.SetPrompt("", "")
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if !ok || err != nil || n < 1 || n > len(plan.SubTopics) {
		return 0, false
	}
	return n - 1, true
}

// renumbered re-derives the plan after the sub-topic list changed: IDs stay
// unique and in listed order, and the source budget follows the new count so
// the brief never shows a ceiling for topics that are no longer there.
//
// An explicit --sources request is re-applied rather than re-derived from the
// depth tier: the reader asked for a number of sources per sub-agent, and
// adding or deleting a branch does not withdraw that request.
func renumbered(plan *Plan) *Plan {
	for i := range plan.SubTopics {
		plan.SubTopics[i].ID = strconv.Itoa(i + 1)
	}
	var np *Plan
	if pin := plan.PinnedPerTopic; pin > 0 {
		np, _ = plan.WithSourcesPerTopic(pin)
	} else {
		np = plan.WithDepth(plan.Depth)
	}
	np.SubTopics = plan.SubTopics
	return np
}

// depthMode maps a config string to a DepthMode, defaulting to standard.
func depthMode(s string) DepthMode {
	d, _ := ParseDepthMode(s)
	return d
}

// ParseDepthMode resolves a --mode value. An unrecognised name is an error
// rather than a silent fall back to standard: "--mode deeep" used to run a
// standard-depth study while the reader believed they had asked for a deep one.
func ParseDepthMode(s string) (DepthMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quick":
		return DepthModes[0], nil
	case "", "standard":
		return DepthModes[1], nil
	case "deep":
		return DepthModes[2], nil
	default:
		names := make([]string, len(DepthModes))
		for i, m := range DepthModes {
			names[i] = m.Key
		}
		return DepthModes[1], fmt.Errorf("unknown mode %q (want %s)", s, strings.Join(names, " | "))
	}
}

// notify emits an OS notification when possible and always rings the bell.
func notify(msg string) {
	if path, err := exec.LookPath("notify-send"); err == nil {
		// Waited on in the background: unreaped, each finished run left a
		// zombie, and waiting inline would hold completion on the desktop.
		if cmd := exec.Command(path, "Deep Research", msg); cmd.Start() == nil {
			go func() { _ = cmd.Wait() }()
		}
	}
	_, _ = os.Stderr.Write([]byte{0x07})
}

// reportFiles lists the artifacts that were actually written. Printing the
// "Saved to" header unconditionally told the reader their report had been
// saved even when every write had failed and the list below it was empty.
func reportFiles(w io.Writer, dir string, res RunResult) {
	paths := res.Paths()
	if len(paths) == 0 {
		_, _ = fmt.Fprintf(w, "\nno report files were written (see the warnings above)\n")
		return
	}
	_, _ = fmt.Fprintf(w, "\nSaved to %s:\n", dir)
	for _, p := range paths {
		_, _ = fmt.Fprintf(w, "  • %s\n", p)
	}
}
