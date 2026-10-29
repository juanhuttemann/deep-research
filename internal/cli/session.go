package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/auth"
	"github.com/juanhuttemann/deep-research/internal/config"
	"github.com/juanhuttemann/deep-research/internal/setup"
	"github.com/juanhuttemann/deep-research/internal/ui"
)

// session is a bare drsh, or the questions after a -p run, in the terminal
// that watched it: the research box, a run, then questions about its report
// until an empty line, and /resume and /model at either prompt.
type session struct {
	cmd *cobra.Command
	d   Deps
	// home is whether there is a research box to go back to: a -p run has
	// none, and an empty line ends it.
	home bool
	run  string // the .json asked about; "" at the research box
	chat *agent.Chat
	note string // for the start screen: why the last step ended
	in   *bufio.Scanner
}

// reloadDeps loads the configuration again after /model saved a new one;
// main's load keeps the first for the life of the process.
var reloadDeps = LoadDeps

func newSession(cmd *cobra.Command, d Deps, home bool) *session {
	return &session{cmd: cmd, d: d, home: home, in: bufio.NewScanner(cmd.InOrStdin())}
}

// interactive is a bare drsh on a terminal: the browser page's start
// screen, in the terminal. Each question runs as `drsh -p` with the same
// flags would, brief and live frame included, then takes questions about
// its report; an empty line there brings the screen back for the next, and
// Esc or Ctrl-C on the screen ends it. A run that fails is reported on the
// screen, as the page does.
func interactive(cmd *cobra.Command, load func() (Deps, error)) error {
	d, err := load()
	if err != nil {
		return err
	}
	return newSession(cmd, d, true).loop()
}

func (s *session) loop() error {
	for {
		line, ok := s.read()
		switch {
		case !ok:
			return nil
		case line == "":
			s.run, s.chat = "", nil
		case line == "/resume":
			s.resume()
		case line == "/model":
			s.switchModel()
		case s.isSetting(line):
			s.chooseSetting(line)
		case strings.HasPrefix(line, "/"):
			s.say("unknown command " + line + "; the commands are " + strings.Join(ui.CommandNames(), ", "))
		case s.run != "":
			s.ask(line)
		default:
			_ = s.cmd.Flags().Set("prompt", line)
			res, err := runResearch(s.cmd, s.d, line)
			s.after(res, err)
		}
	}
}

// read is the next line at the research box or, with a report open, at its
// ask prompt; ok is false when the reader is done. On a terminal the ask
// prompt is read a key at a time, as the research box is, so its commands
// complete; elsewhere it is read a line at a time.
func (s *session) read() (string, bool) {
	if s.run == "" {
		if !s.home {
			return "", false
		}
		q := ui.AskQuestion(s.cmd.InOrStdin(), s.cmd.OutOrStdout(), s.noColor(), s.d.Config.Describe(),
			ui.Settings(runSettings(s.cmd, s.d)), s.note)
		s.note = ""
		return q, q != ""
	}
	back := "finish"
	if s.home {
		back = "new research"
	}
	errOut := s.cmd.ErrOrStderr()
	if f, ok := s.cmd.InOrStdin().(*os.File); ok && ui.IsTTYFile(f) {
		return ui.Ask(f, errOut, s.noColor(), back)
	}
	ui.AskBox(errOut, s.noColor(), back)
	ok := s.in.Scan()
	if !ok {
		// Input ended on the label's line: what follows starts below it.
		fmt.Fprintln(errOut)
	}
	ui.EndAsk(errOut)
	if !ok {
		return "", false
	}
	// Esc, the start screen's way back, arrives here as a character.
	return strings.TrimSpace(strings.ReplaceAll(s.in.Text(), "\x1b", "")), true
}

func (s *session) noColor() bool {
	noColor, _ := s.cmd.Flags().GetBool("no-color")
	return noColor || os.Getenv("NO_COLOR") != ""
}

// say tells the reader something: on the start screen when that is where
// they are going, beside the box they will type in next, where it is read.
func (s *session) say(msg string) {
	if s.run == "" && s.home {
		s.note = msg
		return
	}
	fmt.Fprintln(s.cmd.ErrOrStderr(), msg)
}

// after opens the report of a run that finished for questions.
func (s *session) after(res ui.RunResult, err error) {
	if err != nil {
		s.say("error: " + err.Error())
		return
	}
	if res.Cancelled {
		// Said on the start screen when the run goes back there, where it is
		// read; printed, it sat between the cleared brief and that screen.
		s.say("research cancelled")
		return
	}
	if res.Detached || res.JSONPath == "" {
		return
	}
	if m, err := ui.LoadMeta(res.JSONPath); err != nil {
		s.say(err.Error())
	} else {
		s.open(res.JSONPath, m, nil)
	}
}

// open takes questions about the run at path, under the conversation so
// far. A chat that cannot start costs the questions, not the run.
func (s *session) open(path string, m *ui.Meta, turns []agent.Turn) bool {
	chat, err := agent.NewChat(s.d.Config.Config, ui.RunOverview(m), ui.SourceTools(m))
	if err != nil {
		s.say(fmt.Sprintf("Questions about this report are unavailable: %v", keyHint(s.d, err)))
		return false
	}
	chat.Turns = turns
	s.run, s.chat = path, chat
	fmt.Fprintln(s.cmd.ErrOrStderr(), ui.AskHeading(s.noColor()))
	out := s.cmd.OutOrStdout()
	for _, t := range turns {
		fmt.Fprintf(out, "%s%s\n%s\n", ui.AskPrompt(s.noColor()), t.Question, ui.FormatMarkdown(out, t.Answer, s.noColor()))
	}
	return true
}

// ask answers q and keeps the turn in the run's chat log.
func (s *session) ask(q string) {
	if err := answer(s.cmd, s.chat, q); err != nil {
		fmt.Fprintf(s.cmd.ErrOrStderr(), "error: %v\n", err)
		return
	}
	if err := ui.AppendTurn(s.run, s.chat.Turns[len(s.chat.Turns)-1]); err != nil {
		fmt.Fprintf(s.cmd.ErrOrStderr(), "warning: could not save the question: %v\n", err)
	}
}

// isSetting reports whether line is a start setting's /command, with or
// without its value ("/mode" or "/mode deep").
func (s *session) isSetting(line string) bool {
	name, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	set, ok := ui.SettingFor(ui.Settings(ui.RunSettings{}), name)
	return strings.HasPrefix(line, "/") && ok && set.Start
}

// chooseSetting is a setting's /command: the value typed after it, or one
// picked from its choices, set as its flag for every run the session starts
// after, as the serve page's controls set it for the run they launch.
func (s *session) chooseSetting(line string) {
	name, value, typed := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	set, _ := ui.SettingFor(ui.Settings(runSettings(s.cmd, s.d)), name)
	if value = strings.TrimSpace(value); !typed || value == "" {
		rows := make([]ui.Choice, len(set.Opts))
		for i, o := range set.Opts {
			rows[i] = ui.Choice{Title: o.Label, Note: o.Note}
			if o.Value == set.Value {
				rows[i].Note = strings.TrimSpace(o.Note + " · now")
			}
		}
		i := ui.Pick(s.cmd.InOrStdin(), s.cmd.OutOrStdout(), s.noColor(), strings.ToUpper(set.Label), set.Label, rows)
		if i < 0 {
			return
		}
		value = set.Opts[i].Value
	} else if o, ok := optionByLabel(set, value); ok {
		value = o.Value
	}
	if err := setSetting(s.cmd, name, value); err != nil {
		s.say("error: " + err.Error())
	}
}

// optionByLabel is the option a typed value names, by its label ("auto")
// or its value ("0").
func optionByLabel(set ui.Setting, typed string) (ui.Option, bool) {
	for _, o := range set.Opts {
		if strings.EqualFold(o.Label, typed) || o.Value == typed {
			return o, true
		}
	}
	return ui.Option{}, false
}

// resume is /resume: the runs in the reports directory, filtered by what is
// typed. A finished one is reopened with its conversation; an interrupted
// one is run from its checkpoint, as --resume does.
func (s *session) resume() {
	reports, _ := s.cmd.Flags().GetString("reports")
	all, err := ui.Sessions(reports)
	if err != nil || len(all) == 0 {
		s.say(fmt.Sprintf("no past runs in %s", reports))
		return
	}
	rows := make([]ui.Choice, len(all))
	for i, r := range all {
		rows[i] = ui.Choice{Title: r.Title, Note: r.Modified.Format("Jan 2 15:04")}
		if r.Interrupted {
			rows[i].Note += " · stopped"
		}
	}
	i := ui.Pick(s.cmd.InOrStdin(), s.cmd.OutOrStdout(), s.noColor(), "RESUME", "resume", rows)
	switch {
	case i < 0:
	case all[i].Interrupted:
		s.resumeRun(all[i].Path)
	default:
		s.reopen(all[i].Path)
	}
}

// resumeRun runs an interrupted run from its checkpoint. runStart sets the
// checkpoint's tier and budget on flags no one gave, and the session's next
// run must not inherit them, so the flags are put back as they were.
func (s *session) resumeRun(path string) {
	var undo []func()
	for _, name := range []string{"resume", "mode", "sources", "topics", "no-follow-ups"} {
		f := s.cmd.Flags().Lookup(name)
		v, changed := f.Value.String(), f.Changed
		undo = append(undo, func() { _ = f.Value.Set(v); f.Changed = changed })
		// A setting chosen on the start screen is for new runs: the
		// checkpoint carries the tier and budget it was made with.
		if name != "resume" {
			f.Changed = false
		}
	}
	defer func() {
		for _, u := range undo {
			u()
		}
	}()
	_ = s.cmd.Flags().Set("resume", path)
	res, err := runResearch(s.cmd, s.d, "")
	s.after(res, err)
}

// reopen prints a finished run's report and the questions asked about it,
// and takes the next.
func (s *session) reopen(path string) {
	m, err := ui.LoadMeta(path)
	if err != nil {
		s.say(err.Error())
		return
	}
	turns, err := ui.LoadTurns(path)
	if err != nil {
		fmt.Fprintf(s.cmd.ErrOrStderr(), "warning: the earlier questions could not be read: %v\n", err)
	}
	out := s.cmd.OutOrStdout()
	fmt.Fprintf(out, "\n%s\n", ui.FormatMarkdown(out, "# "+m.Question+"\n\n"+m.Report, s.noColor()))
	s.open(path, m, turns)
}

// switchModel is /model. The next run, and the questions about the open
// report, use what it saves.
func (s *session) switchModel() {
	if err := chooseModel(s.cmd, s.noColor()); err != nil {
		s.say("error: " + err.Error())
		return
	}
	d, err := reloadDeps()
	if err != nil {
		s.say("error: " + err.Error())
		return
	}
	s.d = d
	if s.chat == nil {
		return
	}
	path, turns := s.run, s.chat.Turns
	s.run, s.chat = "", nil
	if m, err := ui.LoadMeta(path); err != nil {
		s.say(err.Error())
	} else {
		s.open(path, m, turns)
	}
}

// chooseModel offers the model runs use now and the ones picked before it,
// then every provider's in the full picker; a pick is saved as `drsh model`
// saves it.
func chooseModel(cmd *cobra.Command, noColor bool) error {
	dir, err := config.AuthDir()
	if err != nil {
		return err
	}
	cur, ok, err := auth.LoadSelection(dir)
	if err != nil {
		return err
	}
	sels, err := auth.Recent(dir)
	if err != nil {
		return err
	}
	if ok {
		sels = append([]auth.Selection{cur}, sels...)
	}
	rows := make([]ui.Choice, 0, len(sels)+1)
	for i, sel := range sels {
		rows = append(rows, ui.Choice{Title: sel.Model, Note: sel.Provider})
		if i == 0 && ok {
			rows[i].Note += " · current"
		}
	}
	rows = append(rows, ui.Choice{Title: "other model…", Note: "every provider's models"})
	i := ui.Pick(cmd.InOrStdin(), cmd.OutOrStdout(), noColor, "MODEL", "model", rows)
	switch {
	case i < 0 || (i == 0 && ok):
		return nil
	case i == len(sels):
		return pickAndSave(cmd, setup.Options{})
	}
	if err := auth.SaveSelection(dir, sels[i]); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "using %s · %s\n", sels[i].Provider, sels[i].Model)
	warnOverrides(cmd.ErrOrStderr(), sels[i])
	return nil
}
