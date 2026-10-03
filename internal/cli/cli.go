package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/replay"
	"github.com/juanhuttemann/deep-research/internal/store"
	"github.com/juanhuttemann/deep-research/internal/ui"
	"github.com/juanhuttemann/deep-research/internal/web"
)

// Deps carries everything a command needs.
type Deps struct {
	Assistant func() (agent.Assistant, error) // built lazily, one per run
	Config    config.Config
}

// New builds the root CLI command.
func New(load func() (Deps, error)) *cobra.Command {
	root := &cobra.Command{
		Use:          "deep-research",
		Short:        "AI-powered deep research agent",
		SilenceUsage: true,
	}

	// A question is a flag on the root, not a subcommand: `deep-research -p "..."`.
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if !slices.ContainsFunc([]string{"prompt", "replay", "plan", "plan-only", "resume"}, cmd.Flags().Changed) {
			return cmd.Help()
		}
		res, err := research(cmd, load)
		if jsonl, _ := cmd.Flags().GetBool("jsonl"); jsonl {
			ui.JSONL{W: cmd.OutOrStdout()}.Emit(doneEvent(res, err))
		}
		return err
	}
	root.Flags().StringP("prompt", "p", "", "the question to research")
	root.Flags().StringP("output", "o", "", "write report to file")
	root.Flags().Int("sources", 0, "sources to gather per sub-agent (0 = --mode tier default)")
	// The flag was called --depth when the value capped a run's total sources.
	// Scripts and older docs still pass that name, so it keeps working.
	root.Flags().Int("depth", 0, "deprecated alias for --sources")
	_ = root.Flags().MarkDeprecated("depth", "use --sources")
	root.Flags().String("mode", "standard", "research depth: quick | standard | deep")
	root.Flags().String("reports", "reports", "directory for the .md / .pdf / .json artifacts")
	root.Flags().Bool("jsonl", false, "emit machine-readable JSONL events instead of the live UI")
	root.Flags().Bool("no-color", false, "disable ANSI colour")
	root.Flags().BoolP("silent", "s", false, "suppress the live UI; print only the report")
	// Same as pressing "b" during a run: the display is released at the first
	// event, the run keeps the terminal until it writes its report.
	root.Flags().Bool("detach", false, "release the live display as soon as the run starts")
	// Both write to stdout. Together they interleaved a rendered Markdown
	// report with the event stream, leaving the machine-readable output
	// unparseable, so the combination is rejected instead of guessed at.
	root.MarkFlagsMutuallyExclusive("silent", "jsonl")
	root.Flags().Bool("trace", false, "also write <report>.trace.json: the plan, every search result and each model phase's prompt and output")
	root.Flags().String("replay", "", "re-run the model phases on a trace's recorded plan and search results, without searching")
	// A replay's question is the recorded one; a second question would be
	// answered from evidence gathered for another.
	root.MarkFlagsMutuallyExclusive("prompt", "replay")
	root.Flags().Bool("plan-only", false, "make the plan, print it (as the plan event under --jsonl) and stop before searching")
	root.Flags().String("plan", "", "run a plan written by --plan-only, edited or not, instead of planning (- reads stdin)")
	// A plan carries its own question, depth and budget. Taking them from
	// flags as well would leave two answers to each, and a replay carries
	// its recorded plan.
	for _, other := range []string{"prompt", "replay", "mode", "sources", "depth", "plan-only"} {
		root.MarkFlagsMutuallyExclusive("plan", other)
	}
	root.MarkFlagsMutuallyExclusive("plan-only", "replay")
	root.Flags().String("resume", "", "continue an interrupted run from its checkpoint (<reports>/*.partial.json), without searching again what it searched")
	// A checkpoint carries the question, the confirmed plan, its tier and
	// budget, and the searches already made.
	for _, other := range []string{"prompt", "replay", "plan", "plan-only", "mode", "sources", "depth"} {
		root.MarkFlagsMutuallyExclusive("resume", other)
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "write default config files",
		RunE:  runInit,
	}
	initCmd.Flags().Bool("docker", false, "also write a docker-compose.yml for a local SearXNG")

	root.AddCommand(initCmd,
		&cobra.Command{
			Use:   "doctor",
			Short: "check the model endpoint, search and scraper a run depends on",
			RunE: func(cmd *cobra.Command, _ []string) error {
				d, err := load()
				if err != nil {
					return err
				}
				return runDoctor(cmd, d)
			},
		},
		serveCmd(load),
		askCmd(load),
		&cobra.Command{
			Use:   "list",
			Short: "list past research runs",
			RunE: func(cmd *cobra.Command, _ []string) error {
				d, err := load()
				if err != nil {
					return err
				}
				return runList(cmd, d)
			},
		},
	)
	return root
}

// runInit writes the config files and, with --docker, a local SearXNG setup.
func runInit(cmd *cobra.Command, _ []string) error {
	dir, created, err := config.Init()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(created) == 0 {
		fmt.Fprintf(out, "config dir %q already configured; nothing to write\n", dir)
	} else {
		// Each file is listed by its absolute path: the yaml files go to the
		// config dir and .env to the working directory, and a header naming
		// only the config dir said the .env was written there too.
		fmt.Fprintf(out, "wrote %d file(s):\n", len(created))
		for _, f := range created {
			if abs, err := filepath.Abs(f); err == nil {
				f = abs
			}
			fmt.Fprintf(out, "  - %s\n", f)
		}
		if slices.Contains(created, ".env") {
			fmt.Fprintln(out, "\nGet a free API key (no card): "+keyURL)
			fmt.Fprintln(out, "Add it to .env as OPENAI_API_KEY=..., then run:")
			fmt.Fprintln(out, "  ./deep-research -p \"your question\"")
		}
	}
	if docker, _ := cmd.Flags().GetBool("docker"); docker {
		return initDocker(out)
	}
	return nil
}

// initDocker writes the local SearXNG setup and says how to use it.
func initDocker(out io.Writer) error {
	created, err := config.InitDocker(".")
	if err != nil {
		return err
	}
	for _, f := range created {
		fmt.Fprintf(out, "  - %s\n", f)
	}
	fmt.Fprintln(out, "\nLocal search (docker-compose.yml, searxng/settings.yml):")
	fmt.Fprintln(out, "  docker compose up -d")
	fmt.Fprintln(out, "  echo 'SEARXNG_URL=http://localhost:8888' >> .env")
	fmt.Fprintln(out, "For full page text add Firecrawl, which runs from its own repository: docs/services.md")
	return nil
}

// serveCmd runs the browser UI on this machine.
func serveCmd(load func() (Deps, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "launch and audit runs from a page on localhost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A config that does not load fails here, not as every launch's
			// done line.
			if _, err := load(); err != nil {
				return err
			}
			addr, _ := cmd.Flags().GetString("addr")
			reports, _ := cmd.Flags().GetString("reports")
			return web.Serve(cmd.Context(), addr, reports, webRun(load, reports, cmd.ErrOrStderr()), webAsk(load, reports), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().String("addr", "127.0.0.1:7777", "address to listen on")
	cmd.Flags().String("reports", "reports", "directory for the .md / .pdf / .json artifacts, served read-only")
	return cmd
}

// webRun is a launch from the page: the same command as
// `deep-research --jsonl -p ...`, run in-process with the stream going to the
// page. Reusing the command keeps one path for validation, the run deadline,
// the history record and the done line on every outcome.
func webRun(load func() (Deps, error), reports string, stderr io.Writer) web.RunFunc {
	return func(ctx context.Context, r web.Request, w io.Writer) {
		args, in := webArgs(r, reports)
		out := &wroteTo{Writer: w}
		cmd := New(load)
		cmd.SetArgs(args)
		cmd.SetIn(in)
		cmd.SetOut(out)
		cmd.SetErr(stderr)
		// RunE ends every run it starts on a done line, which carries the
		// error to the page. A failure before RunE (a flag that does not
		// parse) writes nothing, and the page would wait on a run that never
		// began.
		cmd.SilenceErrors = true
		if err := cmd.ExecuteContext(ctx); err != nil && !out.wrote {
			ui.JSONL{W: w}.Emit(doneEvent(ui.RunResult{}, err))
		}
	}
}

// webArgs is the command line for a page's request. An edited plan goes in
// on stdin (--plan=-): it carries its own question, depth and budget.
func webArgs(r web.Request, reports string) ([]string, io.Reader) {
	if r.Resume != "" {
		// web accepts only a checkpoint's bare name in the reports directory.
		return []string{"--jsonl", "--resume=" + filepath.Join(reports, r.Resume), "--reports=" + reports}, strings.NewReader("")
	}
	if len(r.Plan) > 0 {
		return []string{"--jsonl", "--plan=-", "--reports=" + reports}, bytes.NewReader(r.Plan)
	}
	// --flag=value: a question that starts with "-" is still the value.
	args := []string{"--jsonl", "--prompt=" + r.Question, "--reports=" + reports}
	if r.Mode != "" {
		args = append(args, "--mode="+r.Mode)
	}
	if r.Sources > 0 {
		args = append(args, "--sources="+strconv.Itoa(r.Sources))
	}
	if r.PlanOnly {
		args = append(args, "--plan-only")
	}
	return args, strings.NewReader("")
}

// wroteTo records whether anything was written through it.
type wroteTo struct {
	io.Writer
	wrote bool
}

func (w *wroteTo) Write(p []byte) (int, error) {
	w.wrote = true
	return w.Writer.Write(p)
}

// keyURL is where a first-time user gets a free OpenRouter key.
const keyURL = "https://openrouter.ai/keys"

// pickAssistant builds the assistant. One that cannot be built is an error
// that says how to get a key, never a stub run that researched nothing.
func pickAssistant(d Deps) (agent.Assistant, error) {
	a, err := d.Assistant()
	if err != nil {
		return nil, keyHint(err)
	}
	return a, nil
}

// keyHint says how to get a key, under the error of a model that could not
// be reached without one.
func keyHint(err error) error {
	return fmt.Errorf("%w\n  Get a free key (no card): %s\n"+
		"  then: echo 'OPENAI_API_KEY=sk-or-...' >> .env && deep-research -p \"...\"", err, keyURL)
}

// sourceBudget is the per-sub-agent source budget: the config value unless a
// flag overrides it. --depth is the old name for --sources and still works.
func sourceBudget(cmd *cobra.Command, d Deps) int {
	sources := d.Config.SourcesPerTopic
	for _, name := range []string{"depth", "sources"} {
		if cmd.Flags().Changed(name) {
			sources, _ = cmd.Flags().GetInt(name)
		}
	}
	return sources
}

// runDeadline is the whole-run budget, defaulted when unconfigured.
func runDeadline(d Deps) time.Duration {
	if d.Config.RunTimeout > 0 {
		return d.Config.RunTimeout
	}
	return 30 * time.Minute
}

// research loads the config and runs the question. It is split from RunE so
// every way a run can end, a config that does not load included, reaches the
// done event.
func research(cmd *cobra.Command, load func() (Deps, error)) (ui.RunResult, error) {
	question, _ := cmd.Flags().GetString("prompt")
	d, err := load()
	if err != nil {
		return ui.RunResult{}, err
	}
	return runResearch(cmd, d, question)
}

// doneEvent is the last line of a --jsonl run: how it ended and the files it
// wrote. An incomplete run also returns an error, so it is told apart from a
// failed one by the report it still delivered.
func doneEvent(res ui.RunResult, err error) ui.Event {
	e := ui.Event{Type: ui.Done, Time: time.Now(), Status: "complete", Artifacts: res.Paths()}
	switch {
	case res.Report != nil && res.Report.Error != "":
		e.Status = "incomplete"
	case err != nil:
		e.Status = "failed"
	case res.Cancelled:
		e.Status = "cancelled"
	}
	if err != nil {
		e.Detail = err.Error()
	}
	return e
}

func runResearch(cmd *cobra.Command, d Deps, question string) (ui.RunResult, error) {
	st, err := runStart(cmd, d, question)
	if err != nil {
		return ui.RunResult{}, err
	}
	question = st.question
	// Sub-agents running at once race for the pages several searches return:
	// whichever claims a page first counts it, which decides whether another
	// falls short of its budget and runs its fallback query. A replay served
	// from memory loses nothing by running them one at a time, in plan order,
	// and two replays of one trace then see exactly the same evidence.
	if st.replay {
		d.Config.Parallelism = 1
	}
	// An empty question plans nothing, and the run would still spend its
	// model calls writing a report about nothing.
	if strings.TrimSpace(question) == "" {
		return ui.RunResult{}, errors.New("the question is empty")
	}
	// A misspelt tier used to fall through to standard without a word, so
	// "--mode deeep" ran a study the reader never asked for. Checked before
	// the assistant, so a typo is reported before a missing key.
	mode, _ := cmd.Flags().GetString("mode")
	if _, err := ui.ParseDepthMode(mode); err != nil {
		return ui.RunResult{}, err
	}
	assistant, err := pickAssistant(d)
	if err != nil {
		return ui.RunResult{}, err
	}
	planOnly, _ := cmd.Flags().GetBool("plan-only")
	assistant, rec := instrument(cmd, assistant, st, planOnly, mode, sourceBudget(cmd, d))

	silent, _ := cmd.Flags().GetBool("silent")
	jsonl, _ := cmd.Flags().GetBool("jsonl")
	noColor, _ := cmd.Flags().GetBool("no-color")
	detach, _ := cmd.Flags().GetBool("detach")
	outDir, _ := cmd.Flags().GetString("reports")

	// The agent's own progress lines are the fallback log for a plain pipe.
	// They are suppressed everywhere else: they would tear the live UI's
	// in-place repaint and defeat the point of --silent, and the UI and
	// --jsonl get them as events from ui.Run instead.
	if drawsUI(cmd, silent, jsonl) || silent || jsonl {
		assistant.SetProgress(func(string) {})
	} else {
		assistant.SetProgress(func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", msg) })
	}

	// The whole-run deadline is its own setting. Deriving it from the model
	// call timeout tied it to the one part of a run that is not the slow part:
	// search and scrape dominate the wall clock on a wide plan, so a deep run
	// could expire while every individual model call still had budget.
	ctx, cancel := context.WithTimeout(cmd.Context(), runDeadline(d))
	defer cancel()

	res, err := ui.Run(ctx, ui.Options{
		Question:        question,
		Assistant:       assistant,
		DepthMode:       mode,
		SourcesPerTopic: sourceBudget(cmd, d),
		Parallelism:     d.Config.Parallelism,
		JSONL:           jsonl,
		Quiet:           silent,
		NoColor:         noColor || os.Getenv("NO_COLOR") != "",
		Detach:          detach,
		OutDir:          outDir,
		Input:           cmd.InOrStdin(),
		Stdout:          cmd.OutOrStdout(),
		Stderr:          cmd.ErrOrStderr(),
		Plan:            st.plan,
		PlanOnly:        planOnly,
		OnPlan:          recordPlan(rec),
	})
	if err != nil {
		return res, fmt.Errorf("research failed: %w", err)
	}
	if res.Cancelled {
		fmt.Fprintln(cmd.ErrOrStderr(), "cancelled")
		return res, nil
	}
	if planOnly {
		return res, printPlan(cmd, res.Plan, jsonl)
	}
	writeTrace(cmd, rec, &res)
	return res, finish(cmd, d, rec, res, !st.replay, silent, jsonl)
}

// finish delivers a run that ended and, once it finished in full, removes its
// checkpoint: nothing is left to resume. A cancelled, failed or incomplete
// run keeps it. A run watched in the terminal then takes questions about it.
func finish(cmd *cobra.Command, d Deps, rec *replay.Recorder, res ui.RunResult, save, silent, jsonl bool) error {
	if err := deliver(cmd, d, res.Report, save, silent || jsonl); err != nil {
		return err
	}
	if err := rec.Discard(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove the checkpoint: %v\n", err)
	}
	return askAfter(cmd, d, res.JSONPath, drawsUI(cmd, silent, jsonl) && !res.Detached)
}

// deliver warns about an empty run, saves it to the history and prints it.
// A replay is not saved: it is an experiment on a run the history already
// holds, and saving it would list the same question twice.
func deliver(cmd *cobra.Command, d Deps, result *agent.ResearchResult, save, printed bool) error {
	if len(result.Findings) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: research produced no findings; the search agent returned nothing usable\n")
	}
	if save {
		if err := saveRun(d, result); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save result: %v\n", err)
		}
	}
	if err := printResult(cmd, result, printed); err != nil {
		return err
	}
	// The partial run is saved and printed, but it did not finish: scripts
	// deciding on the exit status must not read it as a complete report.
	if result.Error != "" {
		return fmt.Errorf("research incomplete (the partial report was saved): %s", result.Error)
	}
	return nil
}

// start is where a run comes from: a question, a --plan, a --replay trace
// or a --resume checkpoint.
type start struct {
	question string
	plan     *ui.Plan
	trace    *replay.Trace
	replay   bool   // trace is replayed: its searches only, nothing live
	resume   string // the checkpoint being resumed
}

// runStart loads what the run starts from. A trace gives the run its
// question and, unless the flags override them, its depth tier and source
// budget: the recorded results were cut at that budget, and a run at another
// one would not see the same evidence. A replay writes to <reports>/replay:
// its report is named after the same question, and in the reports directory
// itself it would overwrite the report it is meant to be compared against.
func runStart(cmd *cobra.Command, d Deps, question string) (start, error) {
	st := start{question: question}
	replayPath, _ := cmd.Flags().GetString("replay")
	st.resume, _ = cmd.Flags().GetString("resume")
	if path := cmp.Or(replayPath, st.resume); path != "" {
		t, err := replay.Load(path)
		if err != nil {
			return st, err
		}
		st.trace, st.replay = t, replayPath != ""
		for name, v := range map[string]string{"mode": t.Mode, "sources": strconv.Itoa(t.Sources)} {
			if !cmd.Flags().Changed(name) && v != "" {
				_ = cmd.Flags().Set(name, v)
			}
		}
	}
	if st.replay {
		reports, _ := cmd.Flags().GetString("reports")
		_ = cmd.Flags().Set("reports", filepath.Join(reports, "replay"))
	}
	var err error
	st.plan, st.question, err = givenPlan(cmd, d, st.trace, question)
	return st, err
}

// givenPlan is the plan the run is handed instead of planning, with the
// question it answers; nil plans afresh for question. A replay runs its
// recorded plan as recorded, at the depth and budget replayTrace put in the
// flags: through the planner it was cut to the tier's sub-topic count,
// dropping any the brief had added.
func givenPlan(cmd *cobra.Command, d Deps, tr *replay.Trace, question string) (*ui.Plan, string, error) {
	if tr != nil {
		mode, _ := cmd.Flags().GetString("mode")
		depth, err := ui.ParseDepthMode(mode)
		if err != nil {
			return nil, "", err
		}
		return ui.PlanFor(tr.Question, depth, tr.Plan, sourceBudget(cmd, d)), tr.Question, nil
	}
	path, _ := cmd.Flags().GetString("plan")
	if path == "" {
		return nil, question, nil
	}
	plan, err := readPlanFile(cmd, path)
	if err != nil {
		return nil, "", err
	}
	return plan, plan.Question, nil
}

// readPlanFile reads the plan at path, or stdin for "-".
func readPlanFile(cmd *cobra.Command, path string) (*ui.Plan, error) {
	if path == "-" {
		return ui.ReadPlan(cmd.InOrStdin())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ui.ReadPlan(f)
}

// printPlan writes a plan-only run's plan as JSON, the input --plan reads
// back: to -o when given, as it does a report, else to stdout. Under --jsonl
// the plan event already carried it to stdout.
func printPlan(cmd *cobra.Command, plan *ui.Plan, jsonl bool) error {
	if plan == nil {
		return nil
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if out, _ := cmd.Flags().GetString("output"); out != "" {
		return os.WriteFile(out, append(b, '\n'), 0o644)
	}
	if jsonl {
		return nil
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", b)
	return err
}

// instrument wraps the assistant for the run: the replay player or the
// resume wrapper over the live one, and the recorder that saves the run's
// checkpoint as it goes and writes --trace. A run that will search is
// checkpointed; a replay searches nothing and a plan-only run stops before
// searching. rec is nil when nothing records.
func instrument(cmd *cobra.Command, a agent.Assistant, st start, planOnly bool, mode string, sources int) (agent.Assistant, *replay.Recorder) {
	switch {
	case st.replay:
		a = replay.Play(a, st.trace)
	case st.resume != "":
		a = replay.Resume(a, st.trace)
	}
	trace, _ := cmd.Flags().GetBool("trace")
	checkpoint := !st.replay && !planOnly
	if !trace && !checkpoint {
		return a, nil
	}
	rec := replay.Record(a, st.question, mode, sources)
	if st.resume != "" {
		rec.Seed(st.trace)
	}
	if checkpoint {
		rec.Checkpoint(checkpointPath(cmd, st), func(err error) {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save the checkpoint, so this run cannot be resumed: %v\n", err)
		})
	}
	return rec, rec
}

// checkpointPath is the resumed checkpoint, or a new one beside the run's
// artifacts. The run ID keeps two runs of one question, at once or one
// after another, from writing over each other's progress.
func checkpointPath(cmd *cobra.Command, st start) string {
	if st.resume != "" {
		return st.resume
	}
	reports, _ := cmd.Flags().GetString("reports")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
	}
	id, _, _ := strings.Cut(store.NewID(), "-")
	return filepath.Join(reports, ui.Stem(st.question)+"."+id+".partial.json")
}

// recordPlan saves the confirmed plan into the recorder: the plan a resumed
// run runs again, with its tier and pinned budget.
func recordPlan(rec *replay.Recorder) func(*ui.Plan) {
	if rec == nil {
		return nil
	}
	return func(p *ui.Plan) { rec.SetPlan(p.SubTopics, p.Depth.Key, p.PinnedPerTopic) }
}

// writeTrace saves a recorded run next to its report, named after it.
func writeTrace(cmd *cobra.Command, rec *replay.Recorder, res *ui.RunResult) {
	// Every run that searches records, for its checkpoint; only --trace
	// writes the trace.
	if on, _ := cmd.Flags().GetBool("trace"); !on || rec == nil || res.MDPath == "" {
		return
	}
	if p := res.Plan; p != nil {
		rec.SetPlan(p.SubTopics, p.Depth.Key, p.PinnedPerTopic)
	}
	path := strings.TrimSuffix(res.MDPath, ".md") + ".trace.json"
	if err := rec.Write(path); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the trace: %v\n", err)
		return
	}
	res.TracePath = path
	fmt.Fprintf(cmd.ErrOrStderr(), "trace %s\n", path)
}

// drawsUI reports whether ui.Run will paint the live terminal UI, which it
// does only on an interactive TTY with neither --silent nor --jsonl set.
func drawsUI(cmd *cobra.Command, silent, jsonl bool) bool {
	if silent || jsonl {
		return false
	}
	out, ok := cmd.OutOrStdout().(*os.File)
	if !ok || !ui.IsTTYFile(out) {
		return false
	}
	in, ok := cmd.InOrStdin().(*os.File)
	return ok && ui.IsTTYFile(in)
}

func saveRun(d Deps, result *agent.ResearchResult) error {
	st := store.Open(d.Config.DataFile)
	return st.Save(&store.ResearchResult{
		ID:             store.NewID(),
		ResearchResult: *result,
	})
}

// printResult honours -o and otherwise echoes the report body to stdout. The
// UI already drew the completion card and the list of saved artifacts, but not
// the report text itself; printed is true when the caller (--silent) or the
// JSONL stream has already put the report on stdout.
func printResult(cmd *cobra.Command, result *agent.ResearchResult, printed bool) error {
	if out, _ := cmd.Flags().GetString("output"); out != "" {
		if dir := filepath.Dir(out); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("mkdir output dir: %w", err)
			}
		}
		// The same document the .md artifact holds. Writing only
		// Summary.Report here produced two different "reports" for one run:
		// -o silently dropped the title, the confidence line and every
		// citation the artifact carried.
		if err := os.WriteFile(out, []byte(ui.MarkdownReport(result)), 0o644); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", out)
		return nil
	}
	if printed {
		return nil
	}
	noColor, _ := cmd.Flags().GetBool("no-color")
	fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", ui.FormatMarkdown(cmd.OutOrStdout(), result.Summary.Report, noColor))
	return nil
}

// runDoctor prints one line per dependency and fails when any is broken.
func runDoctor(cmd *cobra.Command, d Deps) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  config   %s\n", configSource())
	problems := 0
	for _, c := range agent.Diagnose(cmd.Context(), d.Config.Config) {
		mark := "✓"
		if !c.OK {
			mark, problems = "✗", problems+1
		}
		fmt.Fprintf(out, "%s %-8s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintf(out, "  budget   %s\n", callBudget(d.Config.SearXNGURL != ""))
	if problems > 0 {
		return fmt.Errorf("doctor found %d problem(s)", problems)
	}
	return nil
}

// configSource names the config.yaml a run reads, or the embedded defaults.
func configSource() string {
	for _, dir := range config.Dirs() {
		if p := filepath.Join(dir, "config.yaml"); fileExists(p) {
			return p
		}
	}
	return "embedded defaults (no config.yaml found)"
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// callBudget is how many model requests one run spends per --mode tier, which
// is what a free model's per-day cap is counted in. With web search a run is
// plan, analyze, fact-check and summarize, plus a second analyze when the
// follow-up round finds evidence and an analyze and a fact-check when the
// repair pass revises a blocked recommendation; LLM search adds up to two
// calls per sub-topic.
func callBudget(webSearch bool) string {
	parts := make([]string, len(ui.DepthModes))
	for i, m := range ui.DepthModes {
		n := 7
		if !webSearch {
			n += 2 * (m.SubTopics + ui.MaxFollowUps)
		}
		parts[i] = fmt.Sprintf("%s %d", m.Key, n)
	}
	return "model requests per run: " + strings.Join(parts, ", ")
}

func runList(cmd *cobra.Command, d Deps) error {
	results, skipped, err := store.Open(d.Config.DataFile).List()
	if err != nil {
		return err
	}
	// An unreadable line is a run that will never appear again — most often
	// the torn last record of a run killed mid-write. Saying so is the only
	// way the history's total can be trusted.
	if skipped > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %d unreadable record(s) in %s were skipped\n", skipped, d.Config.DataFile)
	}
	if len(results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no research runs found")
		return nil
	}
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", r.Timestamp.Format("2006-01-02"), r.Question)
		confidence := "unknown"
		if r.Summary != nil {
			confidence = r.Summary.Confidence
		}
		fetched := 0
		for _, f := range r.Findings {
			if f.Status == "ok" {
				fetched++
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Confidence: %s | Findings: %d | Fetched: %d/%d | Tokens: %d\n",
			confidence, len(r.Findings), fetched, len(r.Findings), r.Tokens)
	}
	return nil
}
