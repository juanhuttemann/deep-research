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
		Use:          "drsh",
		Short:        "AI-powered deep research agent",
		SilenceUsage: true,
	}

	// A question is a flag on the root, not a subcommand: `drsh -p "..."`.
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if !slices.ContainsFunc([]string{"prompt", "replay", "plan", "plan-only", "clarify-only", "resume"}, cmd.Flags().Changed) {
			// A script piping a bare drsh still gets the help it always did.
			silent, _ := cmd.Flags().GetBool("silent")
			jsonl, _ := cmd.Flags().GetBool("jsonl")
			if !drawsUI(cmd, silent, jsonl) {
				return cmd.Help()
			}
			return interactive(cmd, load)
		}
		res, err := research(cmd, load)
		if res.Cancelled {
			fmt.Fprintln(cmd.ErrOrStderr(), "cancelled")
		}
		if jsonl, _ := cmd.Flags().GetBool("jsonl"); jsonl {
			ui.JSONL{W: cmd.OutOrStdout()}.Emit(doneEvent(res, err))
		}
		if err != nil || res.Cancelled {
			return err
		}
		// A run watched in the terminal then takes questions about it.
		d, _ := load()
		silent, _ := cmd.Flags().GetBool("silent")
		jsonl, _ := cmd.Flags().GetBool("jsonl")
		return askAfter(cmd, d, res.JSONPath, drawsUI(cmd, silent, jsonl) && !res.Detached)
	}
	root.Flags().StringP("prompt", "p", "", "the question to research")
	root.Flags().StringP("output", "o", "", "write report to file")
	root.Flags().Int("sources", 0, "sources to gather per sub-agent (0 = --mode tier default)")
	// The flag was called --depth when the value capped a run's total sources.
	// Scripts and older docs still pass that name, so it keeps working.
	root.Flags().Int("depth", 0, "deprecated alias for --sources")
	_ = root.Flags().MarkDeprecated("depth", "use --sources")
	root.Flags().String("mode", "standard", "research depth: quick | standard | deep")
	root.Flags().Int("topics", 0, "plan exactly this many sub-topics, 1-8 (0 = the planner chooses, up to the --mode tier's)")
	root.Flags().Bool("no-follow-ups", false, "skip the follow-up round: analyse what the planned searches found")
	root.Flags().String("format", "standard", "report format: standard | brief | detailed | table")
	root.Flags().Int("words", 0, "the report's explanation in about this many words (0 = the --format's)")
	root.Flags().String("tables", "auto", "how much of the report is in tables: auto (the --format's) | none | some | many")
	root.Flags().String("clarify", "auto", "before planning, ask what the question leaves open: auto (only when it does) | off")
	root.Flags().Bool("clarify-only", false, "ask the clarifying questions, print them (as the clarify event under --jsonl) and stop before planning")
	root.Flags().String("answers", "", "plan for these answers instead of asking: the JSON --clarify-only printed, with \"answers\" filled in")
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
	for _, other := range []string{"prompt", "replay", "mode", "sources", "depth", "plan-only", "topics", "no-follow-ups",
		"format", "words", "tables", "clarify", "clarify-only", "answers"} {
		root.MarkFlagsMutuallyExclusive("plan", other)
	}
	root.MarkFlagsMutuallyExclusive("plan-only", "replay")
	// The questions come before the plan, so a run that stops at them makes
	// none; with answers in hand there is nothing left to ask.
	for _, other := range []string{"plan-only", "replay", "answers"} {
		root.MarkFlagsMutuallyExclusive("clarify-only", other)
	}
	// The report flags are not among them: a replay writes the report again,
	// live, and the recorded evidence can be written up in another shape.
	for _, other := range []string{"topics", "no-follow-ups", "clarify", "answers"} {
		root.MarkFlagsMutuallyExclusive("replay", other)
	}
	root.Flags().StringSlice("domains", nil, "research only these sites (comma-separated); each is checked before planning")
	// A replay searches nothing to confine, and a plan or a checkpoint
	// carries the list it was made with.
	for _, other := range []string{"replay", "plan"} {
		root.MarkFlagsMutuallyExclusive("domains", other)
	}
	root.Flags().String("resume", "", "continue an interrupted run from its checkpoint (<reports>/*.partial.json), without searching again what it searched")
	// A checkpoint carries the question, the confirmed plan, its tier and
	// budget, and the searches already made.
	for _, other := range []string{"prompt", "replay", "plan", "plan-only", "mode", "sources", "depth", "domains",
		"topics", "no-follow-ups", "format", "words", "tables", "clarify", "clarify-only", "answers"} {
		root.MarkFlagsMutuallyExclusive("resume", other)
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "write default config files (to set everything up: drsh setup)",
		RunE:  runInit,
	}
	initCmd.Flags().Bool("docker", false, "also write a docker-compose.yml for a local SearXNG")

	root.AddCommand(setupCmd(),
		initCmd,
		doctorCmd(load),
		loginCmd(),
		modelCmd(),
		logoutCmd(),
		serveCmd(load),
		askCmd(load),
		updateCmd(),
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

// LoadDeps loads the configuration commands run on. main loads it once
// per process; a session's /model loads it again.
func LoadDeps() (Deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return Deps{}, err
	}
	// A fresh assistant per call: it accumulates token usage and served
	// models, and `serve` launches one run after another in one process.
	return Deps{
		Assistant: func() (agent.Assistant, error) { return agent.New(cfg.Config) },
		Config:    cfg,
	}, nil
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
		// Each file is listed by its absolute path, so the reader can open
		// it from wherever they are.
		fmt.Fprintln(out, "wrote:")
		for _, f := range created {
			if abs, err := filepath.Abs(f); err == nil {
				f = abs
			}
			fmt.Fprintf(out, "  - %s\n", f)
		}
	}
	if docker, _ := cmd.Flags().GetBool("docker"); docker {
		if err := initDocker(out, dir); err != nil {
			return err
		}
	}
	// The key comes last: it is the step every first run needs, so it is
	// the one left on screen.
	if envFile := filepath.Join(dir, ".env"); slices.Contains(created, envFile) {
		fmt.Fprintln(out, "\nSet up the model, web search and page extraction: drsh setup")
		fmt.Fprintln(out, "(a free OpenRouter key, no card: "+keyURL+")")
		fmt.Fprintln(out, "then run: drsh -p \"your question\"")
	}
	return nil
}

// initDocker writes the local SearXNG setup into the config dir, points the
// config's .env at it, and says the one command left. It wrote into the
// working directory and left the reader to edit a .env by hand, so a
// project gained a docker-compose.yml and runs elsewhere still searched
// the public instances.
func initDocker(out io.Writer, dir string) error {
	created, err := config.InitDocker(dir)
	if err != nil {
		return err
	}
	for _, f := range created {
		fmt.Fprintf(out, "  - %s\n", f)
	}
	pointed, err := config.UseLocalSearXNG(dir)
	if err != nil {
		return err
	}
	env := filepath.Join(dir, ".env")
	if pointed {
		fmt.Fprintf(out, "  - %s: SEARXNG_URL=%s\n", env, config.LocalSearXNG)
	} else {
		fmt.Fprintf(out, "  - %s already sets SEARXNG_URL; left as it is\n", env)
	}
	fmt.Fprintln(out, "\nStart the local search; Docker restarts it from then on:")
	fmt.Fprintf(out, "  docker compose -f %s up -d\n", filepath.Join(dir, "docker-compose.yml"))
	fmt.Fprintln(out, "then check it: drsh doctor")
	fmt.Fprintln(out, "For full page text add Firecrawl, which runs from its own repository: "+agent.ServicesDoc)
	return nil
}

// doctorCmd checks what a run depends on; --domains adds a source list's
// preflight, the proof a list works before any run is started on it.
func doctorCmd(load func() (Deps, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "check the model endpoint, search and scraper a run depends on",
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, _ := cmd.Flags().GetStringSlice("domains")
			domains, err := agent.ParseDomains(raw)
			if err != nil {
				return err
			}
			d, err := load()
			if err != nil {
				return err
			}
			return runDoctor(cmd, d, domains)
		},
	}
	cmd.Flags().StringSlice("domains", nil, "also check that these sites can be searched and read (comma-separated)")
	return cmd
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
			var ready func(string)
			if noOpen, _ := cmd.Flags().GetBool("no-open"); !noOpen {
				ready = func(url string) { openPage(url, cmd.ErrOrStderr()) }
			}
			return web.Serve(cmd.Context(), addr, reports, webRun(load, reports, cmd.ErrOrStderr()), webAsk(load, reports), webInfo(load), webPlan,
				cmd.ErrOrStderr(), ready)
		},
	}
	cmd.Flags().String("addr", "127.0.0.1:7777", "address to listen on")
	cmd.Flags().String("reports", "reports", "directory for the .md / .pdf / .json artifacts, served read-only")
	cmd.Flags().Bool("no-open", false, "do not open the page in the default browser")
	return cmd
}

// openPage opens the page in the browser. Failing to is not serve failing:
// the URL is on the line above, for the reader to open by hand.
func openPage(url string, log io.Writer) {
	if opened, err := openBrowser(url); err != nil {
		_, _ = fmt.Fprintf(log, "could not open a browser (%v); open %s\n", err, url)
	} else if !opened {
		_, _ = fmt.Fprintf(log, "no display to open a browser on; open %s\n", url)
	}
}

// webInfo describes the setup from the same load a launch makes. serve loads
// the configuration once, so this is the setup it started with, which is what
// every run it launches uses.
func webInfo(load func() (Deps, error)) web.InfoFunc {
	return func() (any, error) {
		d, err := load()
		if err != nil {
			return nil, err
		}
		// The settings go with the setup: the page draws its controls from
		// them, at the values a run started now would take.
		return struct {
			agent.Setup
			Settings []ui.Setting `json:"settings"`
		}{d.Config.Describe(), ui.Settings(defaultSettings(d.Config))}, nil
	}
}

// webPlan is the page review's edit: the plan read as --plan reads it, the
// setting applied as the terminal brief's key applies it, and the budget
// per sub-agent the brief shows.
func webPlan(raw json.RawMessage, key, value string) (any, error) {
	p, err := ui.ReadPlan(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if key != "" {
		if p, err = ui.ApplyToPlan(p, key, value); err != nil {
			return nil, err
		}
	}
	return struct {
		*ui.Plan
		PerTopic int          `json:"per_topic"`
		Settings []ui.Setting `json:"settings"`
	}{p, p.PerTopic(), ui.ReviewSettings(p)}, nil
}

// webRun is a launch from the page: the same command as
// `drsh --jsonl -p ...`, run in-process with the stream going to the
// page. Reusing the command keeps one path for validation, the run deadline,
// the history record and the done line on every outcome.
func webRun(load func() (Deps, error), reports string, stderr io.Writer) web.RunFunc {
	return func(ctx context.Context, r web.Request, w io.Writer) {
		args, in, err := webArgs(r, reports)
		if err != nil {
			ui.JSONL{W: w}.Emit(doneEvent(ui.RunResult{}, err))
			return
		}
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
func webArgs(r web.Request, reports string) ([]string, io.Reader, error) {
	if r.Resume != "" {
		// web accepts only a checkpoint's bare name in the reports directory.
		return []string{"--jsonl", "--resume=" + filepath.Join(reports, r.Resume), "--reports=" + reports}, strings.NewReader(""), nil
	}
	if len(r.Plan) > 0 {
		return []string{"--jsonl", "--plan=-", "--reports=" + reports}, bytes.NewReader(r.Plan), nil
	}
	// --flag=value: a question that starts with "-" is still the value.
	args := []string{"--jsonl", "--prompt=" + r.Question, "--reports=" + reports}
	// Each setting becomes its flag through the one mapping the terminal's
	// /commands use, so the page cannot offer a choice the CLI reads
	// differently.
	for _, s := range ui.Settings(ui.RunSettings{}) {
		v, ok := r.Settings[s.Key]
		if !ok {
			continue
		}
		name, val, err := s.Flag(v)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--"+name+"="+val)
	}
	for k := range r.Settings {
		if _, ok := ui.SettingFor(ui.Settings(ui.RunSettings{}), k); !ok {
			return nil, nil, fmt.Errorf("unknown setting %q", k)
		}
	}
	if len(r.Domains) > 0 {
		args = append(args, "--domains="+strings.Join(r.Domains, ","))
	}
	if r.PlanOnly {
		args = append(args, "--plan-only")
	}
	if r.ClarifyOnly {
		args = append(args, "--clarify-only")
	}
	if len(r.Answers) > 0 {
		args = append(args, "--answers="+string(r.Answers))
	}
	return args, strings.NewReader(""), nil
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
//
// The hint leads with `drsh setup`, which stores the key; the .env
// line stays for those who keep keys in the environment.
const keyURL = "https://openrouter.ai/keys"

// pickAssistant builds the assistant. One that cannot be built is an error
// that says how to get a key, never a stub run that researched nothing.
func pickAssistant(d Deps) (agent.Assistant, error) {
	a, err := d.Assistant()
	if err != nil {
		return nil, keyHint(d, err)
	}
	return a, nil
}

// keyHint says how to get a key, under the error of a model that could not
// be reached without one. Provider claude's error names its own ways in.
func keyHint(d Deps, err error) error {
	// A search setting that cannot work is not a model key problem, and the
	// login hint under it sent the reader to fix a key that already worked.
	if d.Config.Provider == "claude" || errors.Is(err, agent.ErrSearchConfig) {
		return err
	}
	return fmt.Errorf("%w\n  Pick a provider and store its key: drsh setup\n"+
		"  (free OpenRouter key, no card: %s; or set OPENAI_API_KEY in .env)", err, keyURL)
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

// subTopics is the exact sub-topic count asked for: the config value unless
// --topics overrides it, 0 for the planner's choice.
func subTopics(cmd *cobra.Command, d Deps) int {
	if cmd.Flags().Changed("topics") {
		n, _ := cmd.Flags().GetInt("topics")
		return max(n, 0)
	}
	return d.Config.SubTopics
}

// noFollowUps reports whether the follow-up round is off for this run: the
// flag when given, either way, else the config's.
func noFollowUps(cmd *cobra.Command, d Deps) bool {
	if cmd.Flags().Changed("no-follow-ups") {
		off, _ := cmd.Flags().GetBool("no-follow-ups")
		return off
	}
	return d.Config.NoFollowUps
}

// reportSpec is the run's report shape: base (the config's, or a trace's)
// with each report flag given over its own field, so a replay asked for
// --words keeps the format it recorded.
func reportSpec(cmd *cobra.Command, base ui.ReportSpec) (ui.ReportSpec, error) {
	if cmd.Flags().Changed("format") {
		base.Format, _ = cmd.Flags().GetString("format")
	}
	if cmd.Flags().Changed("words") {
		base.Words, _ = cmd.Flags().GetInt("words")
	}
	if cmd.Flags().Changed("tables") {
		base.Tables, _ = cmd.Flags().GetString("tables")
	}
	return base.Normalized()
}

// runSettings are the run's settings: the config's, with the flags over
// them. The terminal's start screen shows them and ui.Options takes them. A
// report flag that does not parse is runResearch's to report; here it
// leaves the config's shape.
func runSettings(cmd *cobra.Command, d Deps) ui.RunSettings {
	mode, _ := cmd.Flags().GetString("mode")
	report, err := reportSpec(cmd, d.Config.Report)
	if err != nil {
		report = d.Config.Report
	}
	return ui.RunSettings{Mode: mode, SubTopics: subTopics(cmd, d), Sources: sourceBudget(cmd, d), NoFollowUps: noFollowUps(cmd, d),
		Report: report, NoClarify: noClarify(cmd, d)}
}

// noClarify is whether the run asks no clarifying questions: the flag's
// choice, else the config's.
func noClarify(cmd *cobra.Command, d Deps) bool {
	if cmd.Flags().Changed("clarify") {
		v, _ := cmd.Flags().GetString("clarify")
		return strings.EqualFold(strings.TrimSpace(v), "off")
	}
	return d.Config.NoClarify
}

// answers are the --answers handed in, nil when none were.
func answers(cmd *cobra.Command) (*ui.Clarified, error) {
	if !cmd.Flags().Changed("answers") {
		return nil, nil
	}
	raw, _ := cmd.Flags().GetString("answers")
	c, err := ui.ParseAnswers(raw)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// defaultSettings are the settings a run started now with no flags takes,
// the ones the serve page opens on.
func defaultSettings(c config.Config) ui.RunSettings {
	return ui.RunSettings{Mode: "standard", SubTopics: c.SubTopics, Sources: c.SourcesPerTopic, NoFollowUps: c.NoFollowUps,
		Report: c.Report, NoClarify: c.NoClarify}
}

// setSetting sets a setting chosen on the start screen as its flag, so every
// run the session starts after takes it, as a flag given on the command
// line would.
func setSetting(cmd *cobra.Command, key, value string) error {
	s, ok := ui.SettingFor(ui.Settings(ui.RunSettings{}), key)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	name, val, err := s.Flag(value)
	if err != nil {
		return err
	}
	return cmd.Flags().Set(name, val)
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

// checkRun refuses a question or a setting the run cannot take. A misspelt
// tier used to fall through to standard without a word, so "--mode deeep"
// ran a study the reader never asked for.
func checkRun(cmd *cobra.Command, d Deps, question string) error {
	// An empty question plans nothing, and the run would still spend its
	// model calls writing a report about nothing.
	if strings.TrimSpace(question) == "" {
		return errors.New("the question is empty")
	}
	mode, _ := cmd.Flags().GetString("mode")
	if _, err := ui.ParseDepthMode(mode); err != nil {
		return err
	}
	if n := subTopics(cmd, d); n > ui.MaxSubTopics {
		return fmt.Errorf("%d sub-topics asked for: at most %d", n, ui.MaxSubTopics)
	}
	if v, _ := cmd.Flags().GetString("clarify"); !slices.Contains([]string{"auto", "off"}, strings.ToLower(strings.TrimSpace(v))) {
		return fmt.Errorf("--clarify %q: want auto or off", v)
	}
	if _, err := answers(cmd); err != nil {
		return err
	}
	_, err := reportSpec(cmd, d.Config.Report)
	return err
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
	// Checked before the assistant, so a typo is reported before a missing
	// key.
	if err := checkRun(cmd, d, question); err != nil {
		return ui.RunResult{}, err
	}
	mode, _ := cmd.Flags().GetString("mode")
	rawDomains, _ := cmd.Flags().GetStringSlice("domains")
	domains, err := agent.ParseDomains(rawDomains)
	if err != nil {
		return ui.RunResult{}, err
	}
	assistant, err := pickAssistant(d)
	if err != nil {
		return ui.RunResult{}, err
	}
	planOnly, _ := cmd.Flags().GetBool("plan-only")
	clarifyOnly, _ := cmd.Flags().GetBool("clarify-only")
	given, _ := answers(cmd) // checkRun refused what does not parse
	assistant, rec := instrument(cmd, assistant, st, planOnly || clarifyOnly, mode, sourceBudget(cmd, d))

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

	rs := runSettings(cmd, d)
	res, err := ui.Run(ctx, ui.Options{
		Question:        question,
		Assistant:       assistant,
		DepthMode:       rs.Mode,
		SourcesPerTopic: rs.Sources,
		SubTopics:       rs.SubTopics,
		NoFollowUps:     rs.NoFollowUps,
		Report:          rs.Report,
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
		Domains:         domains,
		Clarify:         !rs.NoClarify,
		ClarifyOnly:     clarifyOnly,
		Answers:         given,
	})
	if err != nil {
		return res, fmt.Errorf("research failed: %w", err)
	}
	if res.Cancelled {
		return res, nil
	}
	if clarifyOnly || planOnly {
		return res, printStage(cmd, res, jsonl)
	}
	writeTrace(cmd, rec, &res)
	return res, finish(cmd, d, rec, res, !st.replay, silent, jsonl)
}

// finish delivers a run that ended and, once it finished in full, removes its
// checkpoint: nothing is left to resume. A cancelled, failed or incomplete
// run keeps it.
func finish(cmd *cobra.Command, d Deps, rec *replay.Recorder, res ui.RunResult, save, silent, jsonl bool) error {
	if err := deliver(cmd, d, res.Report, save, silent || jsonl); err != nil {
		// An incomplete run keeps its checkpoint, and nothing said so: the
		// reader re-ran the whole question, paying again for every search
		// that had worked. Searches refused by a limit are retried by a
		// resume; the limit itself lifts in minutes.
		if p := rec.CheckpointPath(); p != "" && res.Report != nil && res.Report.Error != "" {
			return fmt.Errorf("%w. Retry only what failed with: drsh --resume %s"+
				" (if searches were rate-limited, wait a few minutes first)", err, p)
		}
		return err
	}
	if err := rec.Discard(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove the checkpoint: %v\n", err)
	}
	return nil
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
		plan := ui.PlanFor(tr.Question, depth, tr.Plan, sourceBudget(cmd, d))
		plan.NoFollowUps, plan.Scope = tr.NoFollowUps, tr.Scope
		plan.Questions, plan.Clarifications = tr.Questions, tr.Clarifications
		// Normalized here as a plan file is in ReadPlan: a hand-edited
		// trace's format is input too.
		if plan.Report, err = reportSpec(cmd, tr.Report); err != nil {
			return nil, "", err
		}
		// A resumed run searches live for what it lacks, on the sites it was
		// confined to; a replay searches nothing.
		if replaying, _ := cmd.Flags().GetString("replay"); replaying == "" {
			plan.Domains = tr.Domains
		}
		return plan, tr.Question, nil
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

// printStage writes what a --clarify-only or --plan-only run stopped at.
func printStage(cmd *cobra.Command, res ui.RunResult, jsonl bool) error {
	if res.Clarified != nil {
		return printJSON(cmd, res.Clarified, jsonl)
	}
	return printJSON(cmd, res.Plan, jsonl)
}

// printJSON writes what a stage stopped at as JSON: a plan-only run's plan,
// the input --plan reads back, or a clarify-only run's questions, the input
// --answers does. To -o when given, as it does a report, else to stdout;
// under --jsonl its event already carried it there.
func printJSON[T any](cmd *cobra.Command, v *T, jsonl bool) error {
	if v == nil {
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
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
	return func(p *ui.Plan) {
		rec.SetDomains(p.Domains)
		rec.SetPlan(p.SubTopics, tracedSettings(p))
	}
}

// tracedSettings are what the trace records of plan beside its sub-topics.
func tracedSettings(p *ui.Plan) replay.PlanSettings {
	return replay.PlanSettings{Mode: p.Depth.Key, Sources: p.PinnedPerTopic, NoFollowUps: p.NoFollowUps, Scope: p.Scope, Report: p.Report,
		Questions: p.Questions, Answers: p.Clarifications}
}

// writeTrace saves a recorded run next to its report, named after it.
func writeTrace(cmd *cobra.Command, rec *replay.Recorder, res *ui.RunResult) {
	// Every run that searches records, for its checkpoint; only --trace
	// writes the trace.
	if on, _ := cmd.Flags().GetBool("trace"); !on || rec == nil || res.MDPath == "" {
		return
	}
	if p := res.Plan; p != nil {
		rec.SetPlan(p.SubTopics, tracedSettings(p))
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
	body := ui.WithReading(result.Summary.Report, result.Clarifications)
	fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", ui.FormatMarkdown(cmd.OutOrStdout(), body, noColor))
	return nil
}

// runDoctor prints one line per dependency and fails when any is broken.
// With domains, it also runs the source-list preflight a run would.
func runDoctor(cmd *cobra.Command, d Deps, domains []string) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  config   %s\n", configSource())
	warnings := 0
	if dirs := config.Dirs(); len(dirs) > 0 {
		for _, f := range config.StrayFiles() {
			warnings++
			fmt.Fprintf(out, "! config   %s is not read: settings come only from %s\n", f, dirs[0])
			fmt.Fprintf(out, "  %-8s → move its deep-research settings to %s\n", "", filepath.Join(dirs[0], filepath.Base(f)))
		}
	}
	problems := 0
	checks := agent.Diagnose(cmd.Context(), d.Config.Config)
	if len(domains) > 0 {
		checks = append(checks, agent.DiagnoseDomains(cmd.Context(), d.Config.Config, domains)...)
	}
	for _, c := range checks {
		mark := "✓"
		switch {
		case !c.OK:
			mark, problems = "✗", problems+1
		case c.Warn:
			mark, warnings = "!", warnings+1
		}
		fmt.Fprintf(out, "%s %-8s %s\n", mark, c.Name, c.Detail)
		if c.Fix != "" && (!c.OK || c.Warn) {
			fmt.Fprintf(out, "  %-8s → %s\n", "", c.Fix)
		}
	}
	fmt.Fprintf(out, "  budget   %s\n", callBudget(d.Config.SearchEnabled()))
	if problems > 0 {
		return fmt.Errorf("doctor found %d problem(s)", problems)
	}
	if warnings > 0 {
		fmt.Fprintf(out, "ready to run, with %d warning(s) above\n", warnings)
		return nil
	}
	fmt.Fprintln(out, "ready to run")
	return nil
}

// configSource names the config.yaml a run reads, or the embedded defaults
// and where init would write one.
func configSource() string {
	dirs := config.Dirs()
	for _, dir := range dirs {
		if p := filepath.Join(dir, "config.yaml"); fileExists(p) {
			return p
		}
	}
	if len(dirs) > 0 {
		return "embedded defaults (no config.yaml found; `drsh init` writes one to " + dirs[0] + ")"
	}
	return "embedded defaults (no config.yaml found)"
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// callBudget is how many model requests one run spends per --mode tier, which
// is what a free model's per-day cap is counted in. With web search a run is
// plan, follow-ups, analyze, fact-check (up to ui.MaxCheckCalls calls at
// once) and summarize, plus an analyze and a fact-check when the repair pass
// revises a blocked recommendation; LLM search adds up to two calls per
// sub-topic and per follow-up.
func callBudget(webSearch bool) string {
	parts := make([]string, len(ui.DepthModes))
	for i, m := range ui.DepthModes {
		n := 6 + ui.MaxCheckCalls
		if !webSearch {
			n += 2 * (m.SubTopics + ui.MaxFollowUps)
		}
		parts[i] = fmt.Sprintf("%s %d", m.Key, n)
	}
	// With web search every tier spends the same: the tiers differ in
	// searches, not model calls. "quick 7, standard 7, deep 7" read as a
	// bug, so a uniform budget is said once.
	if webSearch {
		return fmt.Sprintf("model requests per run: at most %d, in every --mode (the modes differ in searches)", 6+ui.MaxCheckCalls)
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
