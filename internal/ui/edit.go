package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"

	"github.com/juanhuttemann/deep-research/internal/tools"
)

// Editor is a conversation's edits of one run's report: the proposal it has
// made and not saved, and the saves and undos it asks for. The terminal and
// the serve page drive the same Editor, through the model's tools or the
// page's buttons, so an edit behaves the same in both.
//
// What it saves is the application's to write (SaveRevision): the model
// asks through a tool, and the file names, the history and the stale check
// are decided here.
type Editor struct {
	path string
	meta *Meta

	mu       sync.Mutex
	edits    *Edits
	proposal *Proposal
	// did and diff are what the turn under way did, in the application's
	// words, and the diff to show for it.
	did  []string
	diff []DiffLine
	// changed is whether the turn changed the revision in effect, so the
	// conversation is given the report it now reads.
	changed bool
}

// Proposal is an edit of the report not yet saved: the text it would save,
// and the head and revision it was made from.
type Proposal struct {
	Head   int    `json:"head"`
	Base   int    `json:"base"`
	Report string `json:"report"`
	Note   string `json:"note,omitempty"`
}

// Outcome is what a turn did to the report, for the reader: the note, the
// diff (of the proposal pending, or of the save made), and the revision in
// effect after it.
type Outcome struct {
	Note    string     `json:"note,omitempty"`
	Diff    []DiffLine `json:"diff,omitempty"`
	Pending bool       `json:"pending,omitempty"`
	// Proposal names the edit pending, which a button that keeps or drops
	// it hands back: another tab, or a later amendment, may have replaced
	// the proposal, and Apply saved text its reader never saw.
	Proposal string `json:"proposal,omitempty"`
	Revision int    `json:"revision"`
	// Changed says the revision in effect changed: the report shown and the
	// one the conversation reads must be replaced.
	Changed bool `json:"-"`
}

// NewEditor starts the edits of the run whose .json, already read as m, is
// at path.
func NewEditor(path string, m *Meta) (*Editor, error) {
	e, err := LoadEdits(path)
	if err != nil {
		return nil, err
	}
	return &Editor{path: path, meta: m, edits: e}, nil
}

// Begin starts a turn and reports whether another session changed the
// report since the last: the edits are read again, and a proposal made
// before that change is dropped, as a save of it would be refused.
func (e *Editor) Begin() (changed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.did, e.diff, e.changed = nil, nil, false
	if ed, err := LoadEdits(e.path); err == nil {
		changed = ed.Head != e.edits.Head
		e.edits = ed
	}
	if e.proposal != nil && e.proposal.Head != e.edits.Head {
		e.proposal = nil
		e.did = append(e.did, "dropped the proposed edit: another session changed the report")
	}
	return changed
}

// End finishes the turn and says what it did.
func (e *Editor) End() Outcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := Outcome{Note: strings.Join(slices.Compact(e.did), "; "), Diff: e.diff, Pending: e.proposal != nil,
		Proposal: e.proposalIDLocked(), Revision: e.activeLocked(), Changed: e.changed}
	e.did, e.diff, e.changed = nil, nil, false
	return o
}

func (e *Editor) proposalIDLocked() string {
	if e.proposal == nil {
		return ""
	}
	return proposalDigest(e.proposal.Head, e.proposal.Report)
}

// ErrNotShown is a button pressed on an edit that is no longer the one
// pending.
var ErrNotShown = errors.New("this edit was replaced by a newer one: review the latest diff")

// Holds reports whether id names the edit pending, for a button that keeps
// or drops the edit it shows.
func (e *Editor) Holds(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return id != "" && id == e.proposalIDLocked()
}

func (e *Editor) activeLocked() int {
	if r := e.edits.Active(); r != nil {
		return r.N
	}
	return 0
}

// Active is the revision in effect, nil for the original.
func (e *Editor) Active() *Revision {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.edits.Active(); r != nil {
		c := *r
		return &c
	}
	return nil
}

// Overview is RunOverview with the report in effect in place of the
// original, said to be an edit.
func (e *Editor) Overview() string {
	e.mu.Lock()
	m := *e.meta
	r := e.edits.Active()
	e.mu.Unlock()
	if r == nil {
		return RunOverview(&m)
	}
	m.Report = fmt.Sprintf("(Revision %d, edited in conversation after the research; the claims' statuses below are the original's fact-check.)\n\n%s", r.N, r.Report)
	return RunOverview(&m)
}

// State is said ahead of each question: the edit pending, which no earlier
// answer may describe as it now stands.
func (e *Editor) State() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proposal == nil {
		return ""
	}
	return "A proposed edit of the report is pending, not saved. Its diff against the report in effect:\n" +
		DiffText(Diff(e.edits.ActiveReport(e.meta), e.proposal.Report))
}

// Replace is one edit of the report's text: find, which must occur in it
// exactly once, replaced by with.
type Replace struct {
	Find string `json:"find" jsonschema:"text of the report to replace, copied exactly; it must occur once"`
	With string `json:"with,omitempty" jsonschema:"the text to put in its place; empty or left out removes it"`
}

// Propose applies edits to the proposal pending or, with none or fresh set,
// to the report in effect, and returns the proposal's diff.
func (e *Editor) Propose(edits []Replace, note string, fresh bool) ([]DiffLine, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(edits) == 0 {
		return nil, errors.New("no edits given")
	}
	saved := e.edits.ActiveReport(e.meta)
	text, amend := saved, e.proposal != nil && !fresh
	if amend {
		text = e.proposal.Report
	}
	for _, r := range edits {
		switch n := strings.Count(text, r.Find); {
		case r.Find == "":
			return nil, errors.New("an edit's find is empty: copy the text it replaces from the report")
		case n == 0:
			return nil, fmt.Errorf("%q is not in the report: copy it exactly", clipText(r.Find))
		case n > 1:
			return nil, fmt.Errorf("%q occurs %d times: give more of the text around it", clipText(r.Find), n)
		}
		text = strings.Replace(text, r.Find, r.With, 1)
	}
	if err := e.checkLinks(saved, text); err != nil {
		return nil, err
	}
	if text == saved {
		return nil, errors.New("the edits leave the report as it is")
	}
	e.proposal = &Proposal{Head: e.edits.Head, Base: e.activeLocked(), Report: text, Note: note}
	e.diff = Diff(saved, text)
	if amend {
		e.did = append(e.did, "revised the proposed edit")
	} else {
		e.did = append(e.did, "proposed an edit")
	}
	return e.diff, nil
}

// editLink is a Markdown link's target.
var editLink = regexp.MustCompile(`\]\(\s*<?(https?://[^)\s>]+)`)

// checkLinks refuses a link the edit adds to a page this run did not read:
// what the report cites is the run's evidence, and a URL from memory is
// none. A link the report already had is left alone.
func (e *Editor) checkLinks(before, after string) error {
	had := map[string]bool{}
	for _, m := range editLink.FindAllStringSubmatch(before, -1) {
		had[tools.CanonicalURL(m[1])] = true
	}
	known := map[string]bool{}
	for _, c := range e.meta.Citations {
		known[tools.CanonicalURL(c.URL)] = true
	}
	for u := range e.meta.Pages {
		known[tools.CanonicalURL(u)] = true
	}
	for _, m := range editLink.FindAllStringSubmatch(after, -1) {
		if c := tools.CanonicalURL(m[1]); !had[c] && !known[c] {
			return fmt.Errorf("%s is not one of this run's sources: cite only the pages it read, or say the run did not find support", m[1])
		}
	}
	return nil
}

func clipText(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

// Discard drops the proposal pending.
func (e *Editor) Discard() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proposal == nil {
		return errors.New("no edit is pending")
	}
	e.proposal = nil
	e.diff = nil
	e.did = append(e.did, "discarded the proposed edit")
	return nil
}

// Save saves the proposal pending as the report's next revision.
func (e *Editor) Save() (Revision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.proposal
	if p == nil {
		return Revision{}, errors.New("no edit is pending: propose one first")
	}
	before := e.edits.ActiveReport(e.meta)
	r, err := SaveRevision(e.path, e.meta, p.Head, p.Report, p.Note)
	if ed, lerr := LoadEdits(e.path); lerr == nil {
		e.edits = ed
	}
	if errors.Is(err, ErrStale) {
		e.proposal = nil
		e.did = append(e.did, "dropped the proposed edit: another session changed the report")
		e.changed = true
	}
	if err != nil {
		return Revision{}, err
	}
	e.proposal = nil
	// "Save it" saves the diff the user was shown last turn; shown again,
	// it pushed the answer off the screen. One proposed and saved in this
	// turn has not been seen, and is shown.
	if e.diff != nil {
		e.diff = Diff(before, r.Report)
	}
	e.did = append(e.did, fmt.Sprintf("saved revision %d", r.N))
	e.changed = true
	return r, nil
}

// Undo takes back the last save. A proposal pending was made from the
// report the undo replaces, so it goes too.
func (e *Editor) Undo() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	before := e.edits.ActiveReport(e.meta)
	undone, now, err := UndoRevision(e.path, e.edits.Head)
	if ed, lerr := LoadEdits(e.path); lerr == nil {
		e.edits = ed
	}
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("undid revision %d; the original report is in effect", undone)
	// The model's instructions still hold the report it undid until the
	// next turn: told only "the original", it described the original as the
	// revision it had just taken back.
	tell := msg + ": the report as the research wrote it, with no edits; the report you were given is no longer in effect"
	if now != nil {
		msg = fmt.Sprintf("undid revision %d; revision %d is in effect", undone, now.N)
		tell = msg + ": the report you were given is no longer in effect"
	}
	if e.proposal != nil {
		e.proposal = nil
		msg += "; the proposed edit was dropped"
		tell += "; the proposed edit was dropped"
	}
	e.diff = Diff(before, e.edits.ActiveReport(e.meta))
	e.did = append(e.did, msg)
	e.changed = true
	return tell, nil
}

// Tools are the tools the conversation edits the report with, beside the
// ones that read its sources.
func (e *Editor) Tools() []tool.Tool {
	type editArgs struct {
		Edits []Replace `json:"edits" jsonschema:"the replacements, applied in order"`
		Note  string    `json:"note,omitempty" jsonschema:"what the edit does, in a few words"`
		Fresh bool      `json:"fresh,omitempty" jsonschema:"start from the saved report instead of amending the pending edit"`
	}
	edit := functool.MustNew(functool.Config{Name: "edit_report",
		Description: "Propose an edit of the report, or amend the pending one: each find is replaced by its with. " +
			"The user is shown the diff. Nothing is saved."},
		func(_ context.Context, in editArgs) (string, error) {
			d, err := e.Propose(in.Edits, in.Note, in.Fresh)
			if err != nil {
				return "Not applied: " + err.Error(), nil
			}
			return "Proposed, not saved. The user is shown this diff:\n" + DiffText(d), nil
		})
	type none struct{}
	discard := functool.MustNew(functool.Config{Name: "discard_edit",
		Description: "Drop the pending edit, when the user says to discard it."},
		func(_ context.Context, _ none) (string, error) {
			if err := e.Discard(); err != nil {
				return err.Error(), nil
			}
			return "Discarded; the report is as it was.", nil
		})
	save := functool.MustNew(functool.Config{Name: "save_edit",
		Description: "Save the pending edit as the report's new revision. Call it only when the user's latest message " +
			"asks to save or apply it (\"save it\", \"apply\", \"...and save it\"), never on your own judgement. " +
			"An edit made in this same turn may be saved only when this message asked for that change itself."},
		func(_ context.Context, _ none) (string, error) {
			r, err := e.Save()
			if err != nil {
				return "Not saved: " + err.Error(), nil
			}
			return fmt.Sprintf("Saved as revision %d; it is the report now.", r.N), nil
		})
	undo := functool.MustNew(functool.Config{Name: "undo_save",
		Description: "Take back the last saved edit, when the user asks to undo it."},
		func(_ context.Context, _ none) (string, error) {
			msg, err := e.Undo()
			if err != nil {
				return "Not undone: " + err.Error(), nil
			}
			return msg + ".", nil
		})
	return []tool.Tool{edit, discard, save, undo}
}
