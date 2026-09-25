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
)

// Action constants returned from the brief confirmation loop.
const (
	actionLaunch = iota
	actionCancel
)

// Options configures a UI-driven research run.
type Options struct {
	Question        string
	Assistant       agent.Assistant
	DepthMode       string    // "quick" | "standard" | "deep" | "" (standard)
	SourcesPerTopic int       // sources per sub-agent; 0 => derived from depth
	Parallelism     int       // sub-agents searching concurrently; 0 => 3
	JSONL           bool      // emit structured JSONL events
	Quiet           bool      // minimal, non-interactive output
	NoColor         bool      // disable ANSI colour
	Detach          bool      // start in detached / background state
	OutDir          string    // report output dir (default "reports")
	Input           io.Reader // interactive stdin; nil => non-interactive
	Stdout          io.Writer
	Stderr          io.Writer
	Bell            func(string)     // OS/notification hook
	Now             func() time.Time // clock (test seam)
	KeyScript       string           // scripted key sequence for tests (non-TTY)
}

// RunResult carries the artifacts produced by a run.
type RunResult struct {
	Report    *agent.ResearchResult
	MDPath    string
	PDFPath   string
	JSONPath  string
	Cancelled bool
}

// Run executes the full interactive (or headless) research experience.
func Run(ctx context.Context, opts Options) (RunResult, error) {
	o := opts.withDefaults()

	plan, err := o.buildPlan(ctx)
	if err != nil {
		return RunResult{}, err
	}

	renderer := NewRenderer(o.Stdout, Theme{Enabled: !o.NoColor && isTTYWriter(o.Stdout)})
	// The renderer hides the cursor and runs an animation goroutine while the
	// live frame is up. Both are released here so an error path cannot leave
	// the terminal without a cursor.
	defer renderer.Close()

	interactive := isTTYReader(o.Input) && isTTYWriter(o.Stdout) && !o.Quiet && !o.JSONL
	sink := o.buildSink(renderer, interactive)

	res := RunResult{}

	input := o.openInput(interactive)
	defer input.Close()

	// Phase 1: brief confirmation.
	var cancelled bool
	if plan, cancelled = o.confirmBrief(input, renderer, plan, interactive); cancelled {
		res.Cancelled = true
		return res, nil
	}

	// Phase 2: live research via the driver.
	driver := NewDriver(o.Assistant, sink, input, o.Parallelism)
	driver.now = o.Now
	driver.detached = o.Detach
	if interactive {
		// A retried model call is the assistant's own progress line. Without
		// routing it into the activity feed, a retry that takes minutes looks
		// like a frozen spinner. Non-interactive runs keep the stderr logger
		// the CLI installed.
		o.Assistant.SetProgress(func(msg string) { driver.emit(Event{Type: Info, Detail: msg}) })
		// Streamed-phase progress is status: one row replaced in place, not
		// an activity line every five seconds.
		if s, ok := o.Assistant.(interface{ SetStatus(func(string)) }); ok {
			s.SetStatus(func(msg string) { driver.emit(Event{Type: Info, Detail: msg, Transient: true}) })
		}
	}

	result, err := driver.Run(ctx, plan)
	if err != nil {
		// Esc cancels the run. That is a deliberate stop, so it reports as a
		// cancellation rather than a failure; a timeout still fails, because
		// a deadline that expired is not something the reader asked for.
		if errors.Is(err, context.Canceled) && ctx.Err() != context.DeadlineExceeded {
			res.Cancelled = true
			return res, nil
		}
		return RunResult{}, err
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
	return res, nil
}

// buildPlan runs the planning phase and applies the configured source budget.
func (o Options) buildPlan(ctx context.Context) (*Plan, error) {
	// Planning is one model call with nothing on screen behind it, so it is
	// the whole of the dead air between launching the CLI and the brief.
	stopStatus := func() {}
	if !o.Quiet && !o.JSONL {
		var setStatus func(string)
		setStatus, stopStatus = statusSpinner(o.Stderr, "Planning research")
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
	plan, err := NewPlanner(o.Assistant, depthMode(o.DepthMode)).Plan(ctx, o.Question)
	stopStatus()
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
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
func (o Options) confirmBrief(input Input, r *Renderer, plan *Plan, interactive bool) (*Plan, bool) {
	if !interactive && (o.KeyScript == "" || o.JSONL) {
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
	plan, action := waitBrief(input, r, plan, (*Plan).WithDepth)
	return plan, action == actionCancel
}

// buildSink fans events to the machine stream, the live UI, or both.
func (o Options) buildSink(renderer *Renderer, interactive bool) *MultiSink {
	var sinks []Sink
	if o.JSONL {
		// The machine stream takes stdout unless a TUI is also drawing, in
		// which case the TUI keeps stdout and the JSONL goes to stderr.
		to := o.Stderr
		if !interactive {
			to = o.Stdout
		}
		sinks = append(sinks, JSONL{W: to})
	}
	if interactive {
		sinks = append(sinks, renderer)
	}
	return &MultiSink{Sinks: sinks}
}

// openInput returns the single Input used for both the brief and the run.
// Acquiring it twice would put the terminal in raw mode, then capture that
// already-raw state as the "original" to restore, leaving the shell without
// echo after the run.
func (o Options) openInput(interactive bool) Input {
	switch {
	case o.KeyScript != "":
		return NewByteReader([]byte(o.KeyScript))
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
	// The key reader can still be alive here: a `b` pressed between the
	// driver returning and this write emits a Detach event, which appends to
	// the same timeline BuildMeta ranges over. Snapshot it under the sink's
	// lock so the export never reads a slice mid-append.
	meta = keep(WriteMetadata(
		BuildMeta(result, sink.TimelineSnapshot(), plan.Depth.Key, driver.tokens, driver.sources), o.OutDir))
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
		_, _ = fmt.Fprintf(o.Stdout, "%s\n", FormatMarkdown(o.Stdout, result.Summary.Report, o.NoColor))
	default:
		if o.Bell != nil {
			o.Bell(fmt.Sprintf("Research complete: %s", o.Question))
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
// the action: actionLaunch or actionCancel. rebudget re-derives a plan at a
// new depth tier.
func waitBrief(in Input, r *Renderer, plan *Plan, rebudget func(*Plan, DepthMode) *Plan) (*Plan, int) {
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
		case KeyD:
			plan = rebudget(plan, cycleDepth(plan.Depth))
			r.RenderBrief(plan)
		case KeyE:
			// In line mode the topic was typed on the same line as the key, so
			// it is already in hand and there is no second line to wait for.
			name, ok := readPromptSeeded(in, k.Rest, func(text string) {
				r.SetPrompt("add sub-topic", text)
			})
			r.SetPrompt("", "")
			if ok && name != "" {
				plan.SubTopics = append(plan.SubTopics, SubTopic{
					Name:  truncate(name, 60),
					Notes: "added by you",
				})
				plan = renumbered(plan, rebudget)
			}
			r.RenderBrief(plan)
		case KeyR:
			renameTopic(in, r, plan, k.Rest)
			r.RenderBrief(plan)
		case KeyX:
			plan = deleteTopic(in, r, plan, k.Rest, rebudget)
			r.RenderBrief(plan)
		default:
			// ignore unknown keys
		}
	}
}

// renameTopic edits one sub-topic's name in place. The prompt opens seeded
// with the current name, so a small correction does not mean retyping it.
func renameTopic(in Input, r *Renderer, plan *Plan, seed string) {
	i, ok := pickTopic(in, r, plan, "rename which sub-topic", seed)
	if !ok {
		return
	}
	name, done := readPromptEdit(in, plan.SubTopics[i].Name, func(text string) {
		r.SetPrompt("rename sub-topic", text)
	})
	r.SetPrompt("", "")
	if done && name != "" {
		plan.SubTopics[i].Name = truncate(name, 60)
	}
}

// deleteTopic drops one sub-topic. The last one is not deletable: a plan with
// no branches has nothing to research.
func deleteTopic(in Input, r *Renderer, plan *Plan, seed string, rebudget func(*Plan, DepthMode) *Plan) *Plan {
	i, ok := pickTopic(in, r, plan, "delete which sub-topic", seed)
	if !ok || len(plan.SubTopics) < 2 {
		return plan
	}
	plan.SubTopics = slices.Delete(plan.SubTopics, i, i+1)
	return renumbered(plan, rebudget)
}

// pickTopic asks which sub-topic a key applies to and returns its index. An
// answer that is not a listed number is treated as "never mind", so a typo
// cannot delete the wrong branch.
func pickTopic(in Input, r *Renderer, plan *Plan, label, seed string) (int, bool) {
	answer, ok := readPromptSeeded(in, seed, func(text string) {
		r.SetPrompt(fmt.Sprintf("%s (1-%d)", label, len(plan.SubTopics)), text)
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
func renumbered(plan *Plan, rebudget func(*Plan, DepthMode) *Plan) *Plan {
	for i := range plan.SubTopics {
		plan.SubTopics[i].ID = strconv.Itoa(i + 1)
	}
	var np *Plan
	if pin := plan.PinnedPerTopic; pin > 0 {
		np, _ = plan.WithSourcesPerTopic(pin)
	} else {
		np = rebudget(plan, plan.Depth)
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

// cycleDepth returns the next depth tier after d.
func cycleDepth(d DepthMode) DepthMode {
	for i, m := range DepthModes {
		if m.Key == d.Key {
			return DepthModes[(i+1)%len(DepthModes)]
		}
	}
	return DepthModes[1]
}

// notify emits an OS notification when possible and always rings the bell.
func notify(msg string) {
	if path, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(path, "Deep Research", msg).Start()
	}
	_, _ = os.Stderr.Write([]byte{0x07})
}

// reportFiles lists the artifacts that were actually written. Printing the
// "Saved to" header unconditionally told the reader their report had been
// saved even when every write had failed and the list below it was empty.
func reportFiles(w io.Writer, dir string, res RunResult) {
	paths := make([]string, 0, 3)
	for _, p := range []string{res.MDPath, res.PDFPath, res.JSONPath} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		_, _ = fmt.Fprintf(w, "\nno report files were written (see the warnings above)\n")
		return
	}
	_, _ = fmt.Fprintf(w, "\nSaved to %s:\n", dir)
	for _, p := range paths {
		_, _ = fmt.Fprintf(w, "  • %s\n", p)
	}
}
