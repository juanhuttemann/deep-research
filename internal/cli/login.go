package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/catalog"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/setup"
)

// These commands load no config: they are how a machine with none, or with
// a key that no longer works, gets one.

func loginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "pick a provider and model, and store its API key",
		Long: "Pick a provider and model, paste the API key (shown as asterisks) and store it in\n" +
			"auth.json, readable only by you. The stored key wins over OPENAI_API_KEY and\n" +
			"ANTHROPIC_API_KEY, which remain the fallback.\n\n" +
			"Without a terminal: echo \"$KEY\" | drsh login --provider openrouter --model openrouter/free --key-stdin",
		Args: cobra.NoArgs,
		RunE: runLogin,
	}
	cmd.Flags().Bool("new-key", false, "ask for the key even when one is stored for the provider")
	cmd.Flags().String("provider", "", "provider ID, for a scripted login (with --key-stdin)")
	cmd.Flags().String("model", "", "model ID, for a scripted login")
	cmd.Flags().String("base-url", "", "endpoint of the custom provider, for a scripted login")
	cmd.Flags().Bool("key-stdin", false, "read the key from stdin instead of asking for it")
	return cmd
}

func modelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "model",
		Short: "switch to another model, of any provider",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return pickAndSave(cmd, setup.Options{})
		},
	}
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout [provider|exa|tavily|firecrawl]",
		Short: "remove a stored API key",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runLogout,
	}
}

func runLogin(cmd *cobra.Command, _ []string) error {
	if p, _ := cmd.Flags().GetString("provider"); p != "" {
		return scriptedLogin(cmd, p)
	}
	newKey, _ := cmd.Flags().GetBool("new-key")
	return pickAndSave(cmd, setup.Options{NewKey: newKey})
}

// loadCatalog is swapped out by tests, which must not reach Catwalk.
var loadCatalog = func(ctx context.Context) []catalog.Provider {
	return catalog.Load(ctx, http.DefaultClient, catalog.URL, catalog.CacheDir())
}

// loginState is what login reads before showing anything.
type loginState struct {
	dir       string
	store     *auth.Store
	current   auth.Selection
	providers []catalog.Provider
	// env is the run config without login's choice applied to keys: where
	// the environment's keys go (Config.FallbackKey).
	env config.Config
	// loadErr is why env did not load; env is then empty, and setup starts
	// from the defaults rather than refusing to repair the setup.
	loadErr error
}

func loadLoginState(ctx context.Context) (loginState, error) {
	dir, err := config.AuthDir()
	if err != nil {
		return loginState{}, err
	}
	// Load also reads .env, where most keys live. A config that does not
	// load only means no environment keys are offered. It warns about a
	// readable auth.json, so the store below is loaded without a second one.
	env, loadErr := config.Load()
	warn := io.Writer(nil)
	if loadErr != nil {
		warn = os.Stderr
	}
	store, err := auth.Load(dir, warn)
	if err != nil {
		return loginState{}, err
	}
	current, _, err := auth.LoadSelection(dir)
	if err != nil {
		return loginState{}, err
	}
	providers := loadCatalog(ctx)
	return loginState{dir: dir, store: store, current: current, providers: providers, env: env, loadErr: loadErr}, nil
}

// pickAndSave runs the picker and saves what it returns.
func pickAndSave(cmd *cobra.Command, opts setup.Options) error {
	if !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.OutOrStdout()) {
		return errors.New("the picker needs a terminal; script it with:" +
			" echo \"$KEY\" | drsh login --provider <id> --model <id> --key-stdin")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := loadLoginState(ctx)
	if err != nil {
		return err
	}
	opts.Providers, opts.Current = st.providers, st.current
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
	res, ok, err := setup.Run(ctx, opts)
	if err != nil || !ok {
		return err
	}
	return save(cmd.OutOrStdout(), cmd.ErrOrStderr(), st, res)
}

// isTerminal reports whether v is a terminal; a command's streams are
// buffers in tests, and pipes in scripts.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// checkSelection makes doctor's model check for sel: one free request with
// the new key, or the one a run would use — stored, else the environment's
// when it is for that endpoint.
func checkSelection(ctx context.Context, st loginState, sel auth.Selection, key string) error {
	var err error
	if key == "" {
		key, _, err = st.store.KeyFor(sel.BaseURL, sel.Provider)
		if base, envKey := st.env.FallbackKey(sel.Transport); key == "" && auth.SameEndpoint(base, sel.BaseURL) {
			key = envKey
		}
	} else {
		// A "!command" key is checked with what it prints, as a run uses
		// it; sent as typed it is a certain 401.
		key, err = auth.Resolve(key)
	}
	if err != nil {
		return err
	}
	c := agent.CheckModel(ctx, selectionConfig(sel, key))
	if !c.OK {
		return errors.New(c.Detail)
	}
	return nil
}

// selectionConfig is the agent config a run on sel would get.
func selectionConfig(sel auth.Selection, key string) agent.Config {
	cfg := agent.Config{Provider: sel.Transport}
	if sel.Transport == catalog.TransportClaude {
		cfg.ClaudeModel, cfg.AnthropicBaseURL, cfg.AnthropicAPIKey = sel.Model, sel.BaseURL, key
		cfg.ClaudeAPIOnly = sel.Provider != catalog.ClaudeSubscription
		return cfg
	}
	cfg.OpenAIModel, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey = sel.Model, sel.BaseURL, key
	return cfg
}

// save stores the key (when a new one was entered) and the selection, then
// says what a run will use, including anything in the environment that
// overrides the choice just made.
func save(out, errOut io.Writer, st loginState, res setup.Result) error {
	sel := res.Selection
	if res.Key != "" {
		if err := st.store.Set(sel.Provider, auth.Credential{Key: res.Key, BaseURL: sel.BaseURL}); err != nil {
			return fmt.Errorf("store key: %w", err)
		}
	}
	if err := auth.SaveSelection(st.dir, sel); err != nil {
		return fmt.Errorf("save selection: %w", err)
	}
	fmt.Fprintf(out, "using %s · %s\n", sel.Provider, sel.Model)
	if res.Key != "" {
		fmt.Fprintf(out, "key stored in %s (readable only by you)\n", st.store.Path())
	}
	warnOverrides(errOut, sel)
	return nil
}

// warnOverrides names the environment variables that will win over sel: a
// stale OPENAI_MODEL in .env would otherwise make login look broken.
func warnOverrides(w io.Writer, sel auth.Selection) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	model, base := cfg.OpenAIModel, cfg.OpenAIBaseURL
	if cfg.Provider == catalog.TransportClaude {
		model, base = cfg.ClaudeModel, cfg.AnthropicBaseURL
	}
	if cfg.Provider != sel.Transport || model != sel.Model || !auth.SameEndpoint(base, sel.BaseURL) {
		fmt.Fprintf(w, "note: the environment overrides this choice (a run uses provider %s, %s at %s);"+
			" check DEEP_RESEARCH_PROVIDER, OPENAI_* and ANTHROPIC_* in your shell and .env\n",
			cfg.Provider, model, shownURL(base))
	}
}

// scriptedLogin is login without the picker, for scripts and CI. The key
// comes from stdin so it never sits in the shell history or the process list.
func scriptedLogin(cmd *cobra.Command, providerID string) error {
	model, _ := cmd.Flags().GetString("model")
	fromStdin, _ := cmd.Flags().GetBool("key-stdin")
	if model == "" {
		return errors.New("login --provider needs --model")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := loadLoginState(ctx)
	if err != nil {
		return err
	}
	p, ok := catalog.Find(st.providers, providerID)
	if !ok {
		return fmt.Errorf("unknown provider %q; known: %s", providerID, providerIDs(st.providers))
	}
	if p.ID == catalog.Custom {
		if p.BaseURL, _ = cmd.Flags().GetString("base-url"); p.BaseURL == "" {
			return errors.New("login --provider custom needs --base-url")
		}
	}
	key, err := scriptedKey(cmd.InOrStdin(), fromStdin, p)
	if err != nil {
		return err
	}
	sel := auth.Selection{Provider: p.ID, Transport: p.Transport, BaseURL: strings.TrimRight(p.BaseURL, "/"), Model: model}
	if err := checkSelection(ctx, st, sel, key); err != nil {
		return fmt.Errorf("check failed, nothing saved: %w", err)
	}
	return save(cmd.OutOrStdout(), cmd.ErrOrStderr(), st, setup.Result{Selection: sel, Key: key})
}

// scriptedKey is the key on stdin, or with no --key-stdin none: the stored or
// environment key then applies, and the check says when there is neither.
func scriptedKey(in io.Reader, fromStdin bool, p catalog.Provider) (string, error) {
	if !fromStdin {
		return "", nil
	}
	b, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	switch {
	case key != "":
		return key, nil
	case p.ID == catalog.Custom:
		// A local server without a key, as the picker's empty answer.
		return setup.NoKey, nil
	}
	return "", errors.New("no key on stdin")
}

func providerIDs(ps []catalog.Provider) string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

func runLogout(cmd *cobra.Command, args []string) error {
	dir, err := config.AuthDir()
	if err != nil {
		return err
	}
	store, err := auth.Load(dir, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	providers, services := store.Providers(), store.Services()
	stored := slices.Sorted(slices.Values(append(slices.Clone(providers), services...)))
	var id string
	switch {
	case len(args) == 1:
		id = args[0]
	case len(stored) == 1:
		id = stored[0]
	case len(stored) == 0:
		fmt.Fprintln(cmd.OutOrStdout(), "no stored keys")
		return nil
	default:
		return fmt.Errorf("keys are stored for %s; name the one to remove: drsh logout <name>", strings.Join(stored, ", "))
	}
	// A service is named as setup shows it ("exa"); its entry is service:exa.
	entry := id
	if !slices.Contains(providers, id) && slices.Contains(services, id) {
		entry = auth.ServiceID(id)
	}
	found, err := store.Delete(entry)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no key stored for %q (stored: %s)", id, strings.Join(stored, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed the %s key from %s\n", id, store.Path())
	if entry != id {
		fmt.Fprintf(cmd.OutOrStdout(), "a run now uses %s_API_KEY from the environment or config.yaml, if one is set\n", strings.ToUpper(id))
	}
	if sel, ok, _ := auth.LoadSelection(dir); ok && sel.Provider == id {
		fmt.Fprintln(cmd.OutOrStdout(), "it was the selected provider: runs now use the key from the environment, if any")
	}
	return nil
}
