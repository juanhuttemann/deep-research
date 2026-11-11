package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editRun writes a finished run and returns its .json path and record.
func editRun(t *testing.T) (string, *Meta) {
	t.Helper()
	m := Meta{Question: "Is Acme worth it?", Confidence: "high",
		Report:    "## Background\n\nAcme was founded in 1990.\n\n## Price\n\nAcme costs 12 euros ([docs](https://a.example/acme)).",
		Citations: []MetaCitation{{Title: "Acme docs", URL: "https://a.example/acme", Status: "ok"}},
		Pages:     map[string]string{"https://a.example/acme": "Acme costs 12 euros and ships worldwide."}}
	path, err := WriteMetadata(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path, &m
}

func newTestEditor(t *testing.T, path string, m *Meta) *Editor {
	t.Helper()
	e, err := NewEditor(path, m)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// An edit is proposed, amended, saved and undone; the original's files are
// never touched, and each step says what it did.
func TestEditLifecycle(t *testing.T) {
	path, m := editRun(t)
	e := newTestEditor(t, path, m)
	proposeAndAmend(t, e)
	saveRevisionOne(t, e, path, m)
	undoToOriginal(t, e)
}

func proposeAndAmend(t *testing.T, e *Editor) {
	t.Helper()
	e.Begin()
	d, err := e.Propose([]Replace{{Find: "## Background\n\nAcme was founded in 1990.\n\n", With: ""}}, "cut the background", false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLine(d, "-", "## Background") || !hasLine(d, "-", "Acme was founded in 1990.") {
		t.Errorf("diff %+v does not remove the background", d)
	}
	// "Keep the price heading" amends the pending edit, not the saved report.
	if _, err := e.Propose([]Replace{{Find: "## Price", With: "## Cost"}}, "", false); err != nil {
		t.Fatal(err)
	}
	o := e.End()
	if !o.Pending || o.Note != "proposed an edit; revised the proposed edit" || o.Revision != 0 {
		t.Errorf("outcome = %+v", o)
	}
	if s := e.State(); !strings.Contains(s, "pending") || !strings.Contains(s, "## Cost") {
		t.Errorf("state = %q, want the pending edit", s)
	}
}

func saveRevisionOne(t *testing.T, e *Editor, path string, m *Meta) {
	t.Helper()
	e.Begin()
	r, err := e.Save()
	if err != nil {
		t.Fatal(err)
	}
	o := e.End()
	if r.N != 1 || o.Pending || !o.Changed || o.Revision != 1 || o.Note != "saved revision 1" {
		t.Errorf("save: rev %d, outcome %+v", r.N, o)
	}
	if strings.Contains(r.Report, "Background") || !strings.Contains(r.Report, "## Cost") {
		t.Errorf("saved report = %q", r.Report)
	}
	if !strings.Contains(e.Overview(), "Revision 1") || strings.Contains(e.Overview(), "founded in 1990") {
		t.Errorf("the overview is not the revision in effect:\n%s", e.Overview())
	}
	checkRevisionFiles(t, path, m, r)
}

func checkRevisionFiles(t *testing.T, path string, m *Meta, r Revision) {
	t.Helper()
	md, err := os.ReadFile(filepath.Join(filepath.Dir(path), r.MD))
	if err != nil || !strings.Contains(string(md), "_Revision 1, edited in conversation") || strings.Contains(string(md), "founded") {
		t.Errorf("revision export = %q, %v", md, err)
	}
	if r.PDF == "" {
		t.Error("no PDF was written for the revision")
	}
	if orig, _ := LoadMeta(path); orig.Report != m.Report {
		t.Error("the run's own .json was rewritten")
	}
}

func undoToOriginal(t *testing.T, e *Editor) {
	t.Helper()
	e.Begin()
	if _, err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	o := e.End()
	if o.Revision != 0 || !o.Changed || !strings.Contains(o.Note, "undid revision 1") {
		t.Errorf("undo outcome = %+v", o)
	}
	if _, err := e.Undo(); err == nil {
		t.Error("an undo with nothing saved succeeded")
	}
}

func hasLine(d []DiffLine, op, text string) bool {
	for _, l := range d {
		if l.Op == op && l.Text == text {
			return true
		}
	}
	return false
}

// A proposal made before another session saved is refused, and dropped when
// the next turn begins: saved, it would undo the other session's edit.
// Comparing the revision in effect alone let it through after a save and an
// undo in between.
func TestStaleProposalIsRefused(t *testing.T) {
	path, m := editRun(t)
	a, b := newTestEditor(t, path, m), newTestEditor(t, path, m)
	if _, err := a.Propose([]Replace{{Find: "1990", With: "1991"}}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Propose([]Replace{{Find: "12 euros", With: "13 euros"}}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Save(); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale save = %v, want ErrStale", err)
	}
	if a.proposal != nil {
		t.Error("the stale proposal is still pending")
	}

	// Begin drops it too, before the model is asked to save it.
	if _, err := a.Propose([]Replace{{Find: "1990", With: "1991"}}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Propose([]Replace{{Find: "12 euros", With: "14 euros"}}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if !a.Begin() || a.proposal != nil {
		t.Error("Begin kept a proposal another session's save made stale")
	}
	if o := a.End(); !strings.Contains(o.Note, "another session") {
		t.Errorf("outcome = %+v, want the drop said", o)
	}
}

// The same proposal saved twice (a retried click) is saved once.
func TestDuplicateSaveIsOne(t *testing.T) {
	path, m := editRun(t)
	e := newTestEditor(t, path, m)
	if _, err := e.Propose([]Replace{{Find: "1990", With: "1991"}}, "", false); err != nil {
		t.Fatal(err)
	}
	p := e.proposal
	r1, err := SaveRevision(path, m, p.Head, p.Report, "")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := SaveRevision(path, m, p.Head, p.Report, "")
	if err != nil || r2.N != r1.N {
		t.Fatalf("second save = %d, %v; want revision %d again", r2.N, err, r1.N)
	}
	if ed, _ := LoadEdits(path); len(ed.Revisions) != 1 || ed.Head != 1 {
		t.Errorf("edits = %+v, want one revision", ed)
	}
}

// An edit may cite only pages the run read: a URL from the model's memory
// is no evidence. A link the report already had stays.
func TestEditRefusesForeignLinks(t *testing.T) {
	path, m := editRun(t)
	e := newTestEditor(t, path, m)
	_, err := e.Propose([]Replace{{Find: "Acme was founded in 1990.", With: "Acme was founded in 1990 ([wiki](https://elsewhere.example/acme))."}}, "", false)
	if err == nil || !strings.Contains(err.Error(), "not one of this run's sources") {
		t.Errorf("a foreign link = %v, want refused", err)
	}
	if _, err := e.Propose([]Replace{{Find: "Acme was founded in 1990.", With: "Acme ships worldwide ([docs](https://a.example/acme))."}}, "", false); err != nil {
		t.Errorf("a link to the run's own page was refused: %v", err)
	}
	for _, bad := range []Replace{{Find: "", With: "x"}, {Find: "not there", With: "x"}, {Find: "Acme", With: "x"}} {
		if _, err := e.Propose([]Replace{bad}, "", true); err == nil {
			t.Errorf("edit %+v was applied", bad)
		}
	}
}

// The same question run again replaces the run, and its edits went with it:
// left, they were edits of a report no longer there.
func TestRerunRemovesEdits(t *testing.T) {
	path, m := editRun(t)
	e := newTestEditor(t, path, m)
	if _, err := e.Propose([]Replace{{Find: "1990", With: "1991"}}, "", false); err != nil {
		t.Fatal(err)
	}
	r, err := e.Save()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMetadata(*m, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{EditsPath(path), filepath.Join(filepath.Dir(path), r.MD), filepath.Join(filepath.Dir(path), r.PDF)} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the rerun: %v", f, err)
		}
	}
}

// The diff shows what changed and a line either side, with the rest left
// out.
func TestDiffKeepsContext(t *testing.T) {
	a := "one\n\ntwo\n\nthree\n\nfour\n\nfive"
	b := "one\n\ntwo\n\nTHREE\n\nfour\n\nfive"
	got := DiffText(Diff(a, b))
	if want := "  two\n- three\n+ THREE\n  four\n"; got != want {
		t.Errorf("diff =\n%s\nwant\n%s", got, want)
	}
}

// A rerun removes only the files a revision is written as: the names come
// from the .edits file, and one reading "../x" removed x.
func TestRerunRemovesOnlyRevisionExports(t *testing.T) {
	path, m := editRun(t)
	dir := filepath.Dir(path)
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(dir, outside)
	if err := os.WriteFile(EditsPath(path), []byte(`{"revisions":[{"n":1,"md":"`+rel+`","pdf":"`+filepath.Base(path)+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMetadata(*m, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a file outside the reports directory was removed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the run's own .json was removed: %v", err)
	}
}

// On a terminal an added paragraph wraps under its marker and a removed one
// is cut to a row: unwrapped, a replaced section ran off the screen and
// buried the text replacing it.
func TestDiffFitsTheTerminal(t *testing.T) {
	long := strings.Repeat("word ", 30)
	del := diffRows(DiffLine{"-", long}, 40)
	if len(del) != 1 || dispWidth(del[0]) > 40 || !strings.HasSuffix(del[0], "…") {
		t.Errorf("removed line = %q, want one clipped row", del)
	}
	add := diffRows(DiffLine{"+", long}, 40)
	if len(add) < 2 || !strings.HasPrefix(add[0], "  + ") || !strings.HasPrefix(add[1], "    word") {
		t.Errorf("added line = %q, want wrapped under its marker", add)
	}
	for _, r := range add {
		if dispWidth(r) > 40 {
			t.Errorf("row %q is wider than the terminal", r)
		}
	}
	if got := diffRows(DiffLine{"+", long}, 0); len(got) != 1 || !strings.Contains(got[0], strings.TrimSpace(long)) {
		t.Errorf("piped line = %q, want every word on one line", got)
	}
}

// Under a pending edit the note says only how to keep or drop it, not
// "proposed an edit" again.
func TestOutcomeNoteUnderAPendingEdit(t *testing.T) {
	var sb strings.Builder
	PrintOutcomeNote(&sb, Outcome{Note: "proposed an edit; revised the proposed edit", Pending: true}, true)
	if got := sb.String(); strings.Contains(got, "roposed an edit") || !strings.Contains(got, `"save it" keeps it`) {
		t.Errorf("note = %q", got)
	}
}

// In colour the added text is rendered as the report is, its links as
// their text, under a bar, not as raw Markdown after a "+"; a removed
// line is its words on one row.
func TestStyledDiffReadsAsTheReport(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	if got := PlainMarkdown("_Instant answer, not fact-checked._"); got != "Instant answer, not fact-checked." {
		t.Errorf("italic line = %q", got)
	}
	rows := styledDiff([]DiffLine{
		{"-", "**Limits.** Nothing addresses cost ([Colossal](https://colossal.com/mammoth/))."},
		{"+", "## Summary"},
		{"+", "Good news first ([Loeb](https://example.org/loeb.pdf))."},
	}, 80)
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "Limits. Nothing addresses cost (Colossal).") {
		t.Errorf("removed row = %q, want its words", plain)
	}
	if strings.Contains(plain, "+ ") || strings.Contains(plain, "**") || strings.Contains(plain, "](") {
		t.Errorf("the diff shows raw Markdown:\n%s", plain)
	}
	for _, r := range rows {
		if !strings.HasPrefix(stripANSI(r), "▌") {
			t.Errorf("row %q has no change bar", stripANSI(r))
		}
		if dispWidth(stripANSI(r)) > 80 {
			t.Errorf("row %q is wider than the terminal", stripANSI(r))
		}
	}
}
