package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/store"
	"github.com/juanhuttemann/deep-research/internal/ui"
)

// Deps carries everything a command needs.
type Deps struct {
	Assistant func() (agent.Assistant, error) // built lazily
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
		if !cmd.Flags().Changed("prompt") {
			return cmd.Help()
		}
		question, _ := cmd.Flags().GetString("prompt")
		d, err := load()
		if err != nil {
			return err
		}
		return runResearch(cmd, d, question)
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
		fmt.Fprintf(out, "wrote %d file(s) to %q:\n", len(created), dir)
		for _, f := range created {
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

// keyURL is where a first-time user gets a free OpenRouter key.
const keyURL = "https://openrouter.ai/keys"

// pickAssistant builds the assistant. One that cannot be built is an error
// that says how to get a key, never a stub run that researched nothing.
func pickAssistant(d Deps) (agent.Assistant, error) {
	a, err := d.Assistant()
	if err != nil {
		return nil, fmt.Errorf("%w\n  Get a free key (no card): %s\n"+
			"  then: echo 'OPENAI_API_KEY=sk-or-...' >> .env && deep-research -p \"...\"", err, keyURL)
	}
	return a, nil
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

func runResearch(cmd *cobra.Command, d Deps, question string) error {
	// An empty question plans nothing, and the run would still spend its
	// model calls writing a report about nothing.
	if strings.TrimSpace(question) == "" {
		return errors.New("the question is empty")
	}
	// A misspelt tier used to fall through to standard without a word, so
	// "--mode deeep" ran a study the reader never asked for. Checked before
	// the assistant, so a typo is reported before a missing key.
	mode, _ := cmd.Flags().GetString("mode")
	if _, err := ui.ParseDepthMode(mode); err != nil {
		return err
	}
	assistant, err := pickAssistant(d)
	if err != nil {
		return err
	}

	silent, _ := cmd.Flags().GetBool("silent")
	jsonl, _ := cmd.Flags().GetBool("jsonl")
	noColor, _ := cmd.Flags().GetBool("no-color")
	detach, _ := cmd.Flags().GetBool("detach")
	outDir, _ := cmd.Flags().GetString("reports")

	// The agent's own progress lines are the fallback log for a plain pipe.
	// They are suppressed everywhere else: they would tear the live UI's
	// in-place repaint, duplicate the driver's events in --jsonl, and defeat
	// the point of --silent.
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
	})
	if err != nil {
		return fmt.Errorf("research failed: %w", err)
	}
	if res.Cancelled {
		fmt.Fprintln(cmd.ErrOrStderr(), "cancelled")
		return nil
	}
	result := res.Report
	if len(result.Findings) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: research produced no findings; the search agent returned nothing usable\n")
	}
	if err := saveRun(d, result); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save result: %v\n", err)
	}
	if err := printResult(cmd, result, silent || jsonl); err != nil {
		return err
	}
	// The partial run is saved and printed, but it did not finish: scripts
	// deciding on the exit status must not read it as a complete report.
	if result.Error != "" {
		return fmt.Errorf("research incomplete (the partial report was saved): %s", result.Error)
	}
	return nil
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
// plan, analyze, fact-check and summarize; LLM search adds up to two calls
// per sub-topic.
func callBudget(webSearch bool) string {
	parts := make([]string, len(ui.DepthModes))
	for i, m := range ui.DepthModes {
		n := 4
		if !webSearch {
			n += 2 * m.SubTopics
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
