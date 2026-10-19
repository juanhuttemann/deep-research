package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/catalog"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/setup"
)

// sections are the parts of a setup run.
type sections struct{ model, search, scraper bool }

// errSetupTerminal is setup without a terminal: each part has a scripted form.
var errSetupTerminal = errors.New("setup needs a terminal; without one, script each part:\n" +
	"  echo \"$KEY\" | drsh login --provider <id> --model <id> --key-stdin\n" +
	"  echo \"$KEY\" | drsh setup search --engine exa --key-stdin     (or --engine searxng --url URL, --engine off)\n" +
	"  drsh setup scraper --scraper firecrawl --url http://localhost:3002   (or --scraper none, exa, tavily)")

// setupCmd is the one command a new install is pointed at: the model, web
// search and page extraction, each explained, checked and then saved. login,
// model and init stay for the scripts and habits that use them.
func setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "set up the model, web search and page extraction (start here)",
		Long: "Choose the model, the web search and what reads the pages, one step at a time.\n" +
			"Nothing is saved until you confirm the review; then keys go to auth.json\n" +
			"(readable only by you), search settings to config.yaml and the model to\n" +
			"model.json, all in the config directory. Run one part again with\n" +
			"drsh setup model, drsh setup search or drsh setup scraper.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, sections{model: true, search: true, scraper: true})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "model",
		Short: "choose the model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, sections{model: true})
		},
	}, setupSearchCmd(), setupScraperCmd())
	return cmd
}

func setupSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "choose the web search engine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("engine") {
				return scriptedSearch(cmd)
			}
			return runSetup(cmd, sections{search: true})
		},
	}
	cmd.Flags().String("engine", "", "for a script: searxng, exa, tavily or off")
	cmd.Flags().String("url", "", "with --engine searxng: your instance(s), comma-separated (default: public instances)")
	scriptFlags(cmd)
	return cmd
}

func setupScraperCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scraper",
		Short: "choose what reads each result's page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("scraper") {
				return scriptedScraper(cmd)
			}
			return runSetup(cmd, sections{scraper: true})
		},
	}
	cmd.Flags().String("scraper", "", "for a script: none, firecrawl, exa or tavily")
	cmd.Flags().String("url", "", "with --scraper firecrawl: its address")
	scriptFlags(cmd)
	return cmd
}

func scriptFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("key-stdin", false, "read the service's key from stdin")
	cmd.Flags().Bool("no-check", false, "save without the check request (offline)")
}

// cmdContext is the command's context; a command run outside Execute has none.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// setupDraft is what a setup run has collected; nothing of it is saved until
// the review is confirmed.
type setupDraft struct {
	model    setup.Result
	services setup.Services
	asked    sections
}

func runSetup(cmd *cobra.Command, sec sections) error {
	if !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.OutOrStdout()) {
		return errSetupTerminal
	}
	ctx := cmdContext(cmd)
	out := cmd.OutOrStdout()
	st, err := loadLoginState(ctx)
	if err != nil {
		return err
	}
	if st.loadErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: the current configuration does not load (%v); setup starts from the defaults\n", st.loadErr)
	}
	if sec.scraper && !sec.search && currentServices(st).Engine == "off" {
		return errors.New("search is off, so nothing reads pages; turn search on first: drsh setup search")
	}
	if sec.model && sec.search {
		fmt.Fprintln(out, setup.Title("setup"))
		fmt.Fprintln(out, "Three steps: the model that does the research, where it searches the web, and")
		fmt.Fprintln(out, "what reads each result's page. Nothing is saved until you confirm the review.")
		fmt.Fprintln(out, setup.Dim("Saved in "+st.dir+"; run one part again with drsh setup model, search or scraper."))
		fmt.Fprintln(out)
	}
	return setupLoop(ctx, out, st, setupDraft{asked: sec})
}

// setupLoop collects, reviews and checks until the draft is saved or the
// reader cancels; edit goes round again from what was chosen.
func setupLoop(ctx context.Context, out io.Writer, st loginState, draft setupDraft) error {
	for {
		var ok bool
		var err error
		draft, ok, err = collect(ctx, st, draft)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		switch reviewAndCheck(ctx, out, st, draft) {
		case "edit":
			continue
		case "save":
			return saveSetup(out, st, draft)
		}
		break
	}
	fmt.Fprintln(out, "nothing saved")
	return nil
}

// collect runs the steps asked for, starting from what the draft already
// holds when the review sent the reader back to edit it.
func collect(ctx context.Context, st loginState, d setupDraft) (setupDraft, bool, error) {
	if d.asked.model {
		res, ok, err := setup.Run(ctx, pickerOptions(st, d.model))
		if err != nil || !ok {
			return d, false, err
		}
		d.model = res
	}
	if d.asked.search || d.asked.scraper {
		cur := currentServices(st)
		if d.services.SearchAsked || d.services.ScraperAsked {
			cur = d.services
		}
		s, ok, err := setup.RunServices(ctx, serviceOptions(st, cur, d.asked))
		if err != nil || !ok {
			return d, false, err
		}
		d.services = s
	}
	return d, true, nil
}

// pickerOptions is the model picker as setup shows it: login's, with a row
// that keeps the model runs use now.
func pickerOptions(st loginState, prev setup.Result) setup.Options {
	opts := setup.Options{Providers: st.providers, Current: st.current, Keep: keepLabel(st)}
	if prev.Selection.Model != "" {
		opts.Current, opts.Keep = prev.Selection, ""
	}
	opts.StoredEndpoint = func(id string) (string, bool) {
		c, ok := st.store.Get(id)
		return c.BaseURL, ok
	}
	opts.EnvEndpoint = func(transport string) (string, bool) {
		base, key := st.env.FallbackKey(transport)
		return base, key != ""
	}
	opts.Check = func(ctx context.Context, sel auth.Selection, key string) error {
		return checkSelection(ctx, st, sel, key)
	}
	opts.ListModels = agent.ListModels
	return opts
}

// keepLabel names the model runs use now, or "" when there is none worth
// keeping: no key, so a run would fail.
func keepLabel(st loginState) string {
	if st.loadErr != nil {
		return ""
	}
	if st.env.Provider != catalog.TransportClaude && st.env.OpenAIAPIKey == "" {
		return ""
	}
	d := st.env.Describe()
	return d.Provider + " · " + d.Model
}

// currentServices is the search a run uses now, as setup's steps start from.
func currentServices(st loginState) setup.Services {
	if st.loadErr != nil {
		return setup.Services{Engine: "searxng", SearXNGURL: "auto"}
	}
	s := st.env.Search()
	_, err := os.Stat(filepath.Join(st.dir, "docker-compose.yml"))
	return setup.Services{Engine: s.Engine, SearXNGURL: s.SearXNGURL, Scraper: s.Scraper, FirecrawlURL: s.FirecrawlURL,
		Docker: s.Engine == "searxng" && auth.SameEndpoint(s.SearXNGURL, config.LocalSearXNG) && err == nil}
}

func serviceOptions(st loginState, cur setup.Services, sec sections) setup.ServiceOptions {
	return setup.ServiceOptions{
		Current: cur, Search: sec.search, Scraper: sec.scraper,
		KeyMark:      func(service, base string) string { return keyMark(st, service, base) },
		LocalSearXNG: config.LocalSearXNG, ExaURL: agent.ExaURL, TavilyURL: agent.TavilyURL, FirecrawlURL: agent.FirecrawlURL,
	}
}

// keyMark says where a service's key for base comes from now. A stored key
// is not resolved for this: a "!command" would run just to draw a list.
func keyMark(st loginState, service, base string) string {
	if c, ok := st.store.Service(service); ok && auth.SameEndpoint(c.BaseURL, base) {
		return setup.MarkStored
	}
	if st.env.ServiceFallbackKey(service) != "" {
		return setup.MarkEnv
	}
	return ""
}

// settings is the draft's search in config.yaml's words: full, as a run
// would see it, and only what was asked, as setup writes it.
func settings(s setup.Services) (full, write config.SearchSettings) {
	full = config.SearchSettings{Engine: s.Engine, SearXNGURL: s.SearXNGURL, Scraper: s.Scraper, FirecrawlURL: s.FirecrawlURL}
	if s.SearchAsked {
		write.Engine = s.Engine
		if s.Engine == "searxng" {
			write.SearXNGURL = s.SearXNGURL
		}
	}
	if s.ScraperAsked {
		write.Scraper = s.Scraper
		if s.Scraper == config.ServiceFirecrawl {
			write.FirecrawlURL = s.FirecrawlURL
		}
	}
	return full, write
}

// reviewAndCheck shows what will be saved, then checks it: "save", "edit" or
// "" to cancel.
func reviewAndCheck(ctx context.Context, out io.Writer, st loginState, d setupDraft) string {
	pick, err := setup.Ask(ctx, review(st, d), []setup.Choice{
		{Key: "enter", Help: "enter check and save"}, {Key: "e", Help: "e edit"}, {Key: "esc", Help: "esc cancel"}})
	switch {
	case err != nil || pick == "esc" || pick == "":
		return ""
	case pick == "e":
		return "edit"
	}
	if !d.services.SearchAsked && !d.services.ScraperAsked {
		// The picker checked the model before it returned.
		return "save"
	}
	for {
		if checkServices(ctx, out, st, d) {
			return "save"
		}
		pick, err := setup.Ask(ctx, "The check failed. A setting that fails now usually fails in a run too.",
			[]setup.Choice{{Key: "r", Help: "r check again"}, {Key: "e", Help: "e edit"},
				{Key: "s", Help: "s save anyway"}, {Key: "q", Help: "q quit"}})
		switch {
		case err != nil || pick == "q" || pick == "":
			return ""
		case pick == "e":
			return "edit"
		case pick == "s":
			return "save"
		}
	}
}

// review is the screen before anything is saved: each choice, the file it
// goes to, and what in the environment would override it.
func review(st loginState, d setupDraft) string {
	var b strings.Builder
	b.WriteString(setup.Title("review") + "\n\n")
	if d.asked.model {
		fmt.Fprintf(&b, "  %-8s %s\n", "model", modelLine(st, d.model))
	}
	full, _ := settings(d.services)
	if d.services.SearchAsked {
		fmt.Fprintf(&b, "  %-8s %s  %s\n", "search", engineLine(d.services), setup.Dim("→ config.yaml"))
	}
	if d.services.ScraperAsked {
		fmt.Fprintf(&b, "  %-8s %s  %s\n", "pages", scraperLine(d.services), setup.Dim("→ config.yaml"))
	}
	for _, name := range slices.Sorted(maps.Keys(d.services.Keys)) {
		fmt.Fprintf(&b, "  %-8s %s key  %s\n", "", serviceTitle(name), setup.Dim("→ auth.json"))
	}
	if d.services.Docker && d.services.SearchAsked {
		fmt.Fprintf(&b, "  %-8s %s\n", "", setup.Dim("docker-compose.yml and searxng/settings.yml → "+st.dir+"; start it after saving"))
	}
	var overrides []config.Override
	if d.model.Selection.Model != "" {
		overrides = append(overrides, config.ModelOverrides(d.model.Selection)...)
	}
	if d.services.SearchAsked || d.services.ScraperAsked {
		overrides = append(overrides, config.SearchOverrides(full)...)
	}
	if len(overrides) > 0 {
		b.WriteString("\n  The environment overrides part of this, so a run will not use it as chosen:\n")
		for _, o := range overrides {
			fmt.Fprintf(&b, "    %s=%s, set in %s — %s\n", o.Var, o.Value, o.Source, overrideFix(o))
		}
	}
	if (d.services.SearchAsked || d.services.ScraperAsked) && full.Engine != "off" {
		b.WriteString("\n" + setup.Dim("  Saving checks it first: one search query and one page read (a hosted service may charge).") + "\n")
	}
	return b.String()
}

func overrideFix(o config.Override) string {
	switch o.Source {
	case "your shell":
		return "unset it (and remove it from your shell profile)"
	case "the environment":
		return "unset it"
	}
	return "remove that line"
}

func modelLine(st loginState, res setup.Result) string {
	if res.Keep {
		return keepLabel(st) + "  " + setup.Dim("(unchanged)")
	}
	line := res.Selection.Provider + " · " + res.Selection.Model + "  " + setup.Dim("→ model.json")
	if res.Key != "" {
		line += setup.Dim(", key → auth.json")
	}
	return line
}

func serviceTitle(name string) string {
	switch name {
	case config.ServiceExa:
		return "Exa"
	case config.ServiceTavily:
		return "Tavily"
	}
	return "Firecrawl"
}

func engineLine(s setup.Services) string {
	switch {
	case s.Engine == "off":
		return "no web search (model memory, unverified)"
	case s.Engine != "searxng":
		return serviceTitle(s.Engine)
	case s.Docker:
		return "local SearXNG in Docker at " + s.SearXNGURL
	case strings.EqualFold(s.SearXNGURL, "auto"):
		return "public SearXNG instances"
	}
	return "SearXNG at " + s.SearXNGURL
}

func scraperLine(s setup.Services) string {
	switch s.Scraper {
	case "", "none":
		if s.Engine == config.ServiceExa || s.Engine == config.ServiceTavily {
			return "nothing beyond " + serviceTitle(s.Engine) + "'s own page text"
		}
		return "none (search snippets only)"
	case config.ServiceFirecrawl:
		return "Firecrawl at " + s.FirecrawlURL
	}
	return serviceTitle(s.Scraper)
}

// checkServices runs doctor's search and scraper checks on the draft, as a
// run would resolve it, and prints them.
func checkServices(ctx context.Context, out io.Writer, st loginState, d setupDraft) bool {
	full, _ := settings(d.services)
	cfg, err := st.env.WithSearch(full, d.services.Keys, st.store)
	if err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return false
	}
	if !cfg.SearchEnabled() {
		// Nothing searches, so nothing reads pages: there is nothing to ask.
		return true
	}
	fmt.Fprintln(out, "checking: one search query and one page read…")
	ok := true
	for _, c := range []agent.Check{agent.CheckSearch(ctx, cfg.Config), agent.CheckScraper(ctx, cfg.Config)} {
		mark := "✓"
		switch {
		case !c.OK:
			mark, ok = "✗", false
		case c.Warn:
			mark = "!"
		}
		fmt.Fprintf(out, "%s %-8s %s\n", mark, c.Name, c.Detail)
		if c.Fix != "" && (!c.OK || c.Warn) {
			fmt.Fprintf(out, "  %-8s → %s\n", "", c.Fix)
		}
	}
	return ok
}

// saveSetup writes the draft: keys to auth.json, then config.yaml, then the
// Docker files, then model.json. config.yaml is built and checked before the
// first write, so a file that cannot take the settings stops the save before
// anything changed; a later failure says what was already written.
func saveSetup(out io.Writer, st loginState, d setupDraft) error {
	_, write := settings(d.services)
	var cfgFile *config.PendingFile
	if d.services.SearchAsked || d.services.ScraperAsked {
		p, err := config.SearchFile(st.dir, write)
		if err != nil {
			return fmt.Errorf("nothing saved: %w", err)
		}
		cfgFile = &p
	}
	wrote, err := writeSetup(st, d, cfgFile)
	for _, f := range wrote {
		fmt.Fprintf(out, "saved %s\n", f)
	}
	if err == nil && len(wrote) == 0 {
		fmt.Fprintln(out, "nothing changed")
	}
	if err != nil {
		if len(wrote) == 0 {
			return fmt.Errorf("nothing saved: %w", err)
		}
		return fmt.Errorf("%w (what is listed above was saved)", err)
	}
	setupDone(out, st, d)
	return nil
}

// writeSetup writes each file in turn and returns the ones written, up to
// the first that failed.
func writeSetup(st loginState, d setupDraft, cfgFile *config.PendingFile) (wrote []string, err error) {
	if err := saveKeys(st, d); err != nil {
		return wrote, fmt.Errorf("%s: %w", st.store.Path(), err)
	}
	if d.model.Key != "" || len(d.services.Keys) > 0 {
		wrote = append(wrote, st.store.Path())
	}
	if cfgFile != nil {
		if err := cfgFile.Write(); err != nil {
			return wrote, fmt.Errorf("%s: %w", cfgFile.Path, err)
		}
		wrote = append(wrote, cfgFile.Path)
	}
	if d.services.Docker && d.services.SearchAsked {
		created, err := config.InitDocker(st.dir)
		wrote = append(wrote, created...)
		if err != nil {
			return wrote, fmt.Errorf("docker files: %w", err)
		}
	}
	if sel := d.model.Selection; sel.Model != "" {
		p := filepath.Join(st.dir, auth.ModelFile)
		if err := auth.SaveSelection(st.dir, sel); err != nil {
			return wrote, fmt.Errorf("%s: %w", p, err)
		}
		wrote = append(wrote, p)
	}
	return wrote, nil
}

// saveKeys stores the keys typed in setup: the model's for its provider and
// endpoint, each service's for the address it goes to.
func saveKeys(st loginState, d setupDraft) error {
	if sel := d.model.Selection; d.model.Key != "" {
		if err := st.store.Set(sel.Provider, auth.Credential{Key: d.model.Key, BaseURL: sel.BaseURL}); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(d.services.Keys)) {
		base := map[string]string{config.ServiceExa: agent.ExaURL, config.ServiceTavily: agent.TavilyURL,
			config.ServiceFirecrawl: d.services.FirecrawlURL}[name]
		if err := st.store.SetService(name, d.services.Keys[name], base); err != nil {
			return err
		}
	}
	return nil
}

// setupDone says what a run now uses, read back the way a run reads it, and
// what is left to do.
func setupDone(out io.Writer, st loginState, d setupDraft) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(out, "\nsaved, but the configuration does not load: %v\n", err)
		return
	}
	s := cfg.Describe()
	fmt.Fprintln(out, "\nA run now uses:")
	fmt.Fprintf(out, "  %-8s %s · %s\n", "model", s.Provider, s.Model)
	fmt.Fprintf(out, "  %-8s %s\n", "search", s.Search)
	fmt.Fprintf(out, "  %-8s %s\n", "pages", s.Pages)
	if d.services.Docker && d.services.SearchAsked {
		fmt.Fprintln(out, "\nStart the local search; Docker restarts it from then on:")
		fmt.Fprintf(out, "  docker compose -f %s up -d\n", filepath.Join(st.dir, "docker-compose.yml"))
	}
	fmt.Fprintln(out, "\nNext: drsh -p \"your question\"   (drsh doctor checks the whole setup again)")
	fmt.Fprintln(out, setup.Dim("A drsh serve already running keeps its old setup until it is restarted."))
}

// scriptedSearch is `setup search --engine`: the same choice, checked and
// saved without a terminal. A failed check saves nothing unless --no-check.
func scriptedSearch(cmd *cobra.Command) error {
	st, err := loadLoginState(cmdContext(cmd))
	if err != nil {
		return err
	}
	engine, _ := cmd.Flags().GetString("engine")
	addr, _ := cmd.Flags().GetString("url")
	s := currentServices(st)
	s.SearchAsked, s.Docker, s.Engine = true, false, strings.ToLower(strings.TrimSpace(engine))
	switch s.Engine {
	case "searxng":
		s.SearXNGURL = cmp.Or(strings.TrimSpace(addr), "auto")
	case config.ServiceExa, config.ServiceTavily, "off":
	default:
		return fmt.Errorf("--engine %q: want searxng, exa, tavily or off", engine)
	}
	if s.Engine == config.ServiceExa || s.Engine == config.ServiceTavily {
		if s, err = scriptedServiceKey(cmd, st, s, s.Engine); err != nil {
			return err
		}
	}
	return scriptedSave(cmd, st, s)
}

// scriptedScraper is `setup scraper --scraper`.
func scriptedScraper(cmd *cobra.Command) error {
	st, err := loadLoginState(cmdContext(cmd))
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("scraper")
	addr, _ := cmd.Flags().GetString("url")
	s := currentServices(st)
	if s.Engine == "off" {
		return errors.New("search is off, so nothing reads pages; turn search on first: drsh setup search --engine <name>")
	}
	s.ScraperAsked, s.Scraper = true, strings.ToLower(strings.TrimSpace(name))
	switch s.Scraper {
	case "none":
		return scriptedSave(cmd, st, s)
	case config.ServiceFirecrawl:
		if s.FirecrawlURL = strings.TrimRight(strings.TrimSpace(addr), "/"); s.FirecrawlURL == "" {
			return errors.New("--scraper firecrawl needs --url, e.g. http://localhost:3002 or " + agent.FirecrawlURL)
		}
	case config.ServiceExa, config.ServiceTavily:
	default:
		return fmt.Errorf("--scraper %q: want none, firecrawl, exa or tavily", name)
	}
	if s, err = scriptedServiceKey(cmd, st, s, s.Scraper); err != nil {
		return err
	}
	return scriptedSave(cmd, st, s)
}

// scriptedServiceKey takes the service's key from stdin with --key-stdin. Without
// it the stored or environment key is kept, and a hosted service with none
// is an error before any request; a self-hosted Firecrawl needs none.
func scriptedServiceKey(cmd *cobra.Command, st loginState, s setup.Services, service string) (setup.Services, error) {
	s.Keys = map[string]string{}
	base := map[string]string{config.ServiceExa: agent.ExaURL, config.ServiceTavily: agent.TavilyURL}[service]
	if service == config.ServiceFirecrawl {
		base = s.FirecrawlURL
	}
	if fromStdin, _ := cmd.Flags().GetBool("key-stdin"); fromStdin {
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64<<10))
		if err != nil {
			return s, err
		}
		if s.Keys[service] = strings.TrimSpace(string(b)); s.Keys[service] == "" {
			return s, errors.New("no key on stdin")
		}
		return s, nil
	}
	hosted := service != config.ServiceFirecrawl || auth.SameEndpoint(base, agent.FirecrawlURL)
	if hosted && keyMark(st, service, base) == "" {
		return s, fmt.Errorf("%s needs a key: echo \"$KEY\" | drsh setup %s ... --key-stdin", serviceTitle(service), cmd.Name())
	}
	return s, nil
}

func scriptedSave(cmd *cobra.Command, st loginState, s setup.Services) error {
	d := setupDraft{services: s, asked: sections{search: s.SearchAsked, scraper: s.ScraperAsked}}
	if noCheck, _ := cmd.Flags().GetBool("no-check"); !noCheck && !checkServices(cmdContext(cmd), cmd.OutOrStdout(), st, d) {
		return errors.New("check failed, nothing saved (--no-check saves without it)")
	}
	return saveSetup(cmd.OutOrStdout(), st, d)
}
