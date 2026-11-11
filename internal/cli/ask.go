package cli

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
			return converse(cmd, chat)
		},
	}
	cmd.Flags().StringP("prompt", "p", "", "one question; without it, questions are read one per line")
	cmd.Flags().Bool("no-color", false, "disable ANSI colour")
	return cmd
}

// talk is a conversation about one run, with the edits of its report it
// makes: the terminal session, ask and the page each hold one per run.
type talk struct {
	d    Deps
	m    *ui.Meta
	ed   *ui.Editor
	chat *agent.Chat
}

// chatAbout starts a conversation about the run whose .json is at path.
func chatAbout(d Deps, path string) (*talk, error) {
	m, err := ui.LoadMeta(path)
	if err != nil {
		return nil, err
	}
	return chatWith(d, path, m, nil)
}

// chatWith is chatAbout for a run already read, under the conversation so
// far.
func chatWith(d Deps, path string, m *ui.Meta, turns []agent.Turn) (*talk, error) {
	ed, err := ui.NewEditor(path, m)
	if err != nil {
		return nil, err
	}
	t := &talk{d: d, m: m, ed: ed}
	if err := t.connect(turns); err != nil {
		return nil, err
	}
	return t, nil
}

// connect starts the conversation's model on the report in effect. It is
// done again whenever that changes: the report is in the model's
// instructions, and a model still holding the one an edit replaced answered
// about, and edited, the text before it.
func (t *talk) connect(turns []agent.Turn) error {
	chat, err := agent.NewChat(t.d.Config.Config, t.ed.Overview(), append(ui.SourceTools(t.m), t.ed.Tools()...))
	if err != nil {
		return keyHint(t.d, err)
	}
	chat.Turns, chat.State = turns, t.ed.State
	t.chat = chat
	return nil
}

// ask answers q and says what the turn did to the report. The outcome is
// the editor's, so it is given even when the answer failed: a save made
// before the call failed is still saved.
func (t *talk) ask(ctx context.Context, q string) (string, ui.Outcome, error) {
	if t.ed.Begin() {
		if err := t.connect(t.chat.Turns); err != nil {
			return "", t.ed.End(), err
		}
	}
	a, err := t.chat.Ask(ctx, q)
	o := t.ed.End()
	if err == nil {
		t.chat.Turns[len(t.chat.Turns)-1].Edit = o.Note
	}
	if o.Changed {
		if cerr := t.connect(t.chat.Turns); cerr != nil && err == nil {
			err = cerr
		}
	}
	return a, o, err
}

// lastTurn is the turn just asked, for the chat log.
func (t *talk) lastTurn() agent.Turn { return t.chat.Turns[len(t.chat.Turns)-1] }

// askAfter offers a conversation about the -p run that just finished, in
// the terminal that watched it: a session with no research box, which an
// empty line ends. ui.Run has closed its raw input by now, so what is typed
// here is a question, not a key for a run that is over.
func askAfter(cmd *cobra.Command, d Deps, path string, interactive bool) error {
	if !interactive || path == "" {
		return nil
	}
	// The run is delivered by now; a chat that cannot start costs the offer,
	// not the run's exit status.
	m, err := ui.LoadMeta(path)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Questions about this report are unavailable: %v\n", err)
		return nil
	}
	s := newSession(cmd, d, false)
	if !s.open(path, m, nil) {
		return nil
	}
	return s.loop()
}

// converse answers one question per line of input until an empty line or the
// input ends. A question that fails is reported and the conversation goes
// on: a provider that refused once may answer the next.
func converse(cmd *cobra.Command, chat *talk) error {
	f, ok := cmd.InOrStdin().(*os.File)
	tty := ok && ui.IsTTYFile(f)
	in := bufio.NewScanner(cmd.InOrStdin())
	for {
		if tty {
			noColor, _ := cmd.Flags().GetBool("no-color")
			fmt.Fprint(cmd.ErrOrStderr(), ui.AskPrompt(noColor))
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

// answer asks one question and prints the answer, and what it did to the
// report: the diff of the edit proposed or saved, and the note.
func answer(cmd *cobra.Command, chat *talk, q string) error {
	_, stop := ui.StatusSpinner(cmd.ErrOrStderr(), "Reading the run's sources")
	a, o, err := chat.ask(cmd.Context(), q)
	stop()
	noColor, _ := cmd.Flags().GetBool("no-color")
	out := cmd.OutOrStdout()
	// The diff comes first and the reply under it, which says what was
	// changed and what to know about it; under the diff, a reply read as
	// pointing up to it.
	ui.PrintOutcome(out, o, noColor)
	if err == nil {
		fmt.Fprintf(out, "\n%s\n\n", ui.FormatMarkdown(out, a, noColor))
	}
	ui.PrintOutcomeNote(out, o, noColor)
	return err
}

// readInRoot reads the run named run within the reports directory, as the
// page's report and download routes do: through an os.Root, which refuses a
// symlink out of it. Read by its joined path, a .json symlink in reports
// sent a file from anywhere the process could read to the model as the
// run's.
func readInRoot(reports, run string) (*ui.Meta, error) {
	root, err := os.OpenRoot(reports)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := fs.ReadFile(root.FS(), run)
	if err != nil {
		return nil, err
	}
	return ui.DecodeMeta(data, run)
}

// webTalks are the page's conversations' edits, one editor per run: the
// page keeps the conversation and hands it back with each question, but the
// proposal it is shown must still be there when it says "save it" or
// presses Apply, so the server keeps that much.
type webTalks struct {
	load    func() (Deps, error)
	reports string

	mu   sync.Mutex
	runs map[string]*webTalk
}

// webTalk is one run's editor, and the .json's modification time it was
// read at: a rerun of the question writes a new run over it, and the edit
// of the old report must not be saved onto the new.
type webTalk struct {
	mu  sync.Mutex // one question or button at a time per run
	m   *ui.Meta
	ed  *ui.Editor
	mod time.Time
}

func newWebTalks(load func() (Deps, error), reports string) *webTalks {
	return &webTalks{load: load, reports: reports, runs: map[string]*webTalk{}}
}

// talk is the run's editor, read again when the run was written over. It is
// returned locked.
func (w *webTalks) talk(run string) (*webTalk, error) {
	path := filepath.Join(w.reports, run)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	t := w.runs[run]
	if t == nil {
		t = &webTalk{}
		w.runs[run] = t
	}
	w.mu.Unlock()
	t.mu.Lock()
	if t.ed == nil || !fi.ModTime().Equal(t.mod) {
		m, err := readInRoot(w.reports, run)
		if err == nil {
			t.ed, err = ui.NewEditor(path, m)
		}
		if err != nil {
			t.ed = nil
			t.mu.Unlock()
			return nil, err
		}
		t.m, t.mod = m, fi.ModTime()
	}
	return t, nil
}

// ask answers a question from the page about a run in the reports
// directory; web passes only a bare .json name there.
func (w *webTalks) ask(ctx context.Context, run string, turns []web.Turn, q string) (string, any, error) {
	d, err := w.load()
	if err != nil {
		return "", nil, err
	}
	t, err := w.talk(run)
	if err != nil {
		return "", nil, err
	}
	defer t.mu.Unlock()
	var ts []agent.Turn
	for _, turn := range turns {
		ts = append(ts, agent.Turn(turn))
	}
	chat := &talk{d: d, m: t.m, ed: t.ed}
	if err := chat.connect(ts); err != nil {
		return "", nil, err
	}
	a, o, err := chat.ask(ctx, q)
	return a, outcomeView(o), err
}

// edit is the page's Apply, Discard and Undo: the same actions the model's
// tools take, pressed instead of asked for.
func (w *webTalks) edit(run, action, proposal string) (any, error) {
	t, err := w.talk(run)
	if err != nil {
		return nil, err
	}
	defer t.mu.Unlock()
	t.ed.Begin()
	switch {
	case action != "undo" && !t.ed.Holds(proposal):
		err = ui.ErrNotShown
	case action == "save":
		_, err = t.ed.Save()
	case action == "discard":
		err = t.ed.Discard()
	case action == "undo":
		_, err = t.ed.Undo()
	default:
		err = fmt.Errorf("unknown edit action %q", action)
	}
	o := t.ed.End()
	if err != nil {
		return nil, err
	}
	return outcomeView(o), nil
}

// active is the report in effect for the page: its Markdown, and the
// revision and its downloads.
func (w *webTalks) active(run string) (string, any, error) {
	t, err := w.talk(run)
	if err != nil {
		return "", nil, err
	}
	defer t.mu.Unlock()
	t.ed.Begin()
	r := t.ed.Active()
	t.ed.End()
	if r == nil {
		return t.m.Report, revisionView{}, nil
	}
	return r.Report, revisionView{Revision: r.N, Saved: r.Time, MD: r.MD, PDF: r.PDF, Report: r.Report}, nil
}

// revisionView is the revision in effect as the page shows it: none is
// the original, whose downloads are the run's own.
type revisionView struct {
	Revision int       `json:"revision"`
	Saved    time.Time `json:"saved,omitzero"`
	MD       string    `json:"md,omitempty"`
	PDF      string    `json:"pdf,omitempty"`
	// Report is the revision's text, which Copy falls back to when its .md
	// could not be written or read.
	Report string `json:"report,omitempty"`
}

// outcomePage is an outcome as the page shows it.
type outcomePage struct {
	ui.Outcome
	Blocks []diffBlock `json:"blocks,omitempty"`
}

// diffBlock is one of ui.DiffBlocks, an added one with its HTML.
type diffBlock struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
	HTML string `json:"html,omitempty"`
}

// outcomeView is an outcome for the page, nil when the turn left the report
// alone. Its blocks are the diff as the terminal shows it, the added text
// rendered as the report is: raw Markdown in red and green read as a patch.
func outcomeView(o ui.Outcome) any {
	if o.Note == "" && !o.Pending && len(o.Diff) == 0 {
		return nil
	}
	v := outcomePage{Outcome: o}
	for _, b := range ui.DiffBlocks(o.Diff) {
		if b.Op != "+" {
			v.Blocks = append(v.Blocks, diffBlock{Op: b.Op, Text: b.Text})
			continue
		}
		html, err := web.Markdown(b.Text)
		if err != nil {
			html = ""
		}
		v.Blocks = append(v.Blocks, diffBlock{Op: b.Op, Text: b.Text, HTML: html})
	}
	return v
}
