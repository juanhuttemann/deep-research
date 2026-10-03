package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/ui"
	"github.com/juanhuttemann/deep-research/internal/web"
)

// askCmd answers questions about a finished run from its .json: one with -p,
// otherwise one per line of input until an empty line or the input ends.
func askCmd(load func() (Deps, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask <run.json>",
		Short: "ask follow-up questions about a finished run, answered from its saved sources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := load()
			if err != nil {
				return err
			}
			chat, err := chatAbout(d, args[0])
			if err != nil {
				return err
			}
			if q, _ := cmd.Flags().GetString("prompt"); q != "" {
				return answer(cmd, chat, q)
			}
			return converse(cmd, chat, "")
		},
	}
	cmd.Flags().StringP("prompt", "p", "", "one question; without it, questions are read one per line")
	cmd.Flags().Bool("no-color", false, "disable ANSI colour")
	return cmd
}

// chatAbout starts a conversation about the run whose .json is at path.
func chatAbout(d Deps, path string) (*agent.Chat, error) {
	m, err := ui.LoadMeta(path)
	if err != nil {
		return nil, err
	}
	chat, err := agent.NewChat(d.Config.Config, ui.RunOverview(m), ui.SourceTools(m))
	if err != nil {
		return nil, keyHint(err)
	}
	return chat, nil
}

// askAfter offers a conversation about the run that just finished, in the
// terminal that watched it. ui.Run has closed its raw input by now, so what
// is typed here is a question, not a key for a run that is over.
func askAfter(cmd *cobra.Command, d Deps, path string, interactive bool) error {
	if !interactive || path == "" {
		return nil
	}
	chat, err := chatAbout(d, path)
	if err != nil {
		return err
	}
	return converse(cmd, chat, "Ask about this report (empty line to finish)")
}

// converse answers one question per line of input until an empty line or the
// input ends. A question that fails is reported and the conversation goes
// on: a provider that refused once may answer the next.
func converse(cmd *cobra.Command, chat *agent.Chat, header string) error {
	f, ok := cmd.InOrStdin().(*os.File)
	tty := ok && ui.IsTTYFile(f)
	if tty && header != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), header)
	}
	in := bufio.NewScanner(cmd.InOrStdin())
	for {
		if tty {
			fmt.Fprint(cmd.ErrOrStderr(), "› ")
		}
		if !in.Scan() {
			return in.Err()
		}
		q := strings.TrimSpace(in.Text())
		if q == "" {
			return nil
		}
		if err := answer(cmd, chat, q); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
		}
	}
}

// answer asks one question and prints the answer.
func answer(cmd *cobra.Command, chat *agent.Chat, q string) error {
	_, stop := ui.StatusSpinner(cmd.ErrOrStderr(), "Reading the run's sources")
	a, err := chat.Ask(cmd.Context(), q)
	stop()
	if err != nil {
		return err
	}
	noColor, _ := cmd.Flags().GetBool("no-color")
	fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", ui.FormatMarkdown(cmd.OutOrStdout(), a, noColor))
	return nil
}

// webAsk answers a question from the page about a run in the reports
// directory; web passes only a bare .json name there.
func webAsk(load func() (Deps, error), reports string) web.AskFunc {
	return func(ctx context.Context, run string, turns []web.Turn, q string) (string, error) {
		d, err := load()
		if err != nil {
			return "", err
		}
		chat, err := chatAbout(d, filepath.Join(reports, run))
		if err != nil {
			return "", err
		}
		for _, t := range turns {
			chat.Turns = append(chat.Turns, agent.Turn(t))
		}
		return chat.Ask(ctx, q)
	}
}
