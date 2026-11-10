package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Edits is the saved history of a run's report edits, kept beside its .json
// as <stem>.edits. The run's own files are never rewritten: the original
// report, its audit and its exports stay as the research left them, and each
// saved revision has exports of its own.
type Edits struct {
	// Head counts the saves and undos made. A proposal remembers the head it
	// was made at, and a save at any other is refused as stale: comparing
	// the revision in effect alone let a proposal through after another
	// session saved and undid in between.
	Head      int        `json:"head"`
	Revisions []Revision `json:"revisions"`
	// Stack is the revisions in effect, the latest last; empty is the
	// original. An undo pops it, so it goes back to what was in effect
	// before the save it undoes.
	Stack []int `json:"stack"`
}

// Revision is one saved edit of the report.
type Revision struct {
	N      int       `json:"n"`
	Time   time.Time `json:"time"`
	Note   string    `json:"note,omitempty"`
	Report string    `json:"report"`
	// MD and PDF are the revision's exports, file names in the reports
	// directory; PDF is empty when it could not be written, and then no
	// PDF is offered for it rather than the original's.
	MD  string `json:"md,omitempty"`
	PDF string `json:"pdf,omitempty"`
	// Digest identifies the proposal saved, so saving it twice saves it once.
	Digest string `json:"digest"`
}

// ErrStale is a save or undo refused because the report changed since the
// edit was proposed, in another session or by this one.
var ErrStale = errors.New("the report was changed in another session since this edit was proposed")

// EditsPath is where the edits of the run whose .json is at path are kept.
func EditsPath(path string) string { return strings.TrimSuffix(path, ".json") + ".edits" }

// LoadEdits reads a run's edits; a run never edited has none.
func LoadEdits(path string) (*Edits, error) {
	data, err := os.ReadFile(EditsPath(path))
	if errors.Is(err, fs.ErrNotExist) {
		return &Edits{}, nil
	}
	if err != nil {
		return nil, err
	}
	var e Edits
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", EditsPath(path), err)
	}
	return &e, nil
}

// Active is the revision in effect, nil for the original.
func (e *Edits) Active() *Revision {
	if len(e.Stack) == 0 {
		return nil
	}
	return e.revision(e.Stack[len(e.Stack)-1])
}

func (e *Edits) revision(n int) *Revision {
	for i := range e.Revisions {
		if e.Revisions[i].N == n {
			return &e.Revisions[i]
		}
	}
	return nil
}

// ActiveReport is the report in effect: the revision's, or the run's own.
func (e *Edits) ActiveReport(m *Meta) string {
	if r := e.Active(); r != nil {
		return r.Report
	}
	return m.Report
}

// proposalDigest names a proposal by what it would save and where: two
// different texts never share one, so a duplicate save is recognised and
// a changed proposal is not mistaken for one already saved.
func proposalDigest(head int, report string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s", head, report))
	return hex.EncodeToString(sum[:12])
}

// SaveRevision saves report as the run's next revision, made at head, and
// writes its exports. It returns the revision saved; a proposal already
// saved returns the revision it became.
func SaveRevision(path string, m *Meta, head int, report, note string) (Revision, error) {
	var saved Revision
	err := withEditsLock(path, func(e *Edits) (bool, error) {
		digest := proposalDigest(head, report)
		if i := slices.IndexFunc(e.Revisions, func(r Revision) bool { return r.Digest == digest }); i >= 0 {
			saved = e.Revisions[i]
			return false, nil
		}
		if e.Head != head {
			return false, ErrStale
		}
		n := 1
		for _, r := range e.Revisions {
			n = max(n, r.N+1)
		}
		saved = Revision{N: n, Time: time.Now(), Note: note, Report: report, Digest: digest}
		// The exports are written first, under names no other revision
		// has, so a revision is never recorded with files that are not its.
		saved.MD, saved.PDF = writeRevisionExports(path, m, saved)
		e.Revisions = append(e.Revisions, saved)
		e.Stack = append(e.Stack, n)
		e.Head++
		return true, nil
	})
	return saved, err
}

// UndoRevision takes back the last save, made at head, and returns the
// revision now in effect, nil for the original. The undone revision is kept
// in the history, as its exports are.
func UndoRevision(path string, head int) (undone int, now *Revision, err error) {
	err = withEditsLock(path, func(e *Edits) (bool, error) {
		if e.Head != head {
			return false, ErrStale
		}
		if len(e.Stack) == 0 {
			return false, errors.New("no saved edit to undo: the original report is in effect")
		}
		undone = e.Stack[len(e.Stack)-1]
		e.Stack = e.Stack[:len(e.Stack)-1]
		e.Head++
		if r := e.Active(); r != nil {
			c := *r
			now = &c
		}
		return true, nil
	})
	return undone, now, err
}

// editsLockWait bounds waiting for another session's save, and an older
// lock is one a crash left behind.
const (
	editsLockWait  = 10 * time.Second
	editsLockStale = time.Minute
)

// withEditsLock runs fn on the run's edits under a lock file, which the
// terminal and the serve page, separate processes on one reports directory,
// both take; the edits are written back when fn says they changed.
func withEditsLock(path string, fn func(*Edits) (bool, error)) error {
	lock := EditsPath(path) + ".lock"
	deadline := time.Now().Add(editsLockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > editsLockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("another session is saving an edit to this report; try again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lock) }()
	e, err := LoadEdits(path)
	if err != nil {
		return err
	}
	changed, err := fn(e)
	if err != nil || !changed {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(EditsPath(path), data)
}

// writeRevisionExports writes a revision's Markdown and PDF beside the run's
// and returns their names, empty for one that could not be written.
func writeRevisionExports(path string, m *Meta, r Revision) (md, pdf string) {
	stem := strings.TrimSuffix(path, ".json") + fmt.Sprintf(".r%d", r.N)
	doc := RevisedMarkdown(m, r)
	if writeAtomic(stem+".md", []byte(doc)) == nil {
		md = filepath.Base(stem + ".md")
	}
	if writeAtomic(stem+".pdf", BuildPDF(doc)) == nil {
		pdf = filepath.Base(stem + ".pdf")
	}
	return md, pdf
}

// RevisedMarkdown is a revision as a Markdown document: the original's
// header, the revised report, and the original's fact-check and sources,
// with a note saying which is which. The fact-check checked the original;
// read under the revised text unlabelled, it vouched for prose it never saw.
func RevisedMarkdown(m *Meta, r Revision) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", OneLine(m.Question))
	fmt.Fprintf(&sb, "_Revision %d, edited in conversation on %s after the research. The confidence, the fact-check and the sources below are the original run's; the edits were not fact-checked._\n\n",
		r.N, r.Time.Format("2006-01-02"))
	fmt.Fprintf(&sb, "**Confidence (original):** %s\n\n", m.Confidence)
	if m.Model != "" {
		fmt.Fprintf(&sb, "**Model:** %s\n\n", m.Model)
	}
	if len(m.Domains) > 0 {
		fmt.Fprintf(&sb, "**Sources limited to:** %s\n\n", strings.Join(m.Domains, ", "))
	}
	sb.WriteString(ReadingMarkdown(m.Clarifications))
	sb.WriteString(strings.TrimSpace(r.Report))
	sb.WriteString("\n\n")
	if m.Analysis != nil {
		writeOpenQuestions(&sb, m.Analysis)
	}
	writeFactCheck(&sb, m.FactCheck)
	cits := slices.Clone(m.Citations)
	for i := range cits {
		cits[i].Cited = cits[i].URL != "" && strings.Contains(r.Report, cits[i].URL)
	}
	cited, retrieved := splitCited(cits)
	if len(cited) == 0 {
		cited, retrieved = cits, nil
	}
	writeCitationList(&sb, "Citations", cited)
	writeCitationList(&sb, "Other Sources Retrieved", retrieved)
	return sb.String()
}

// isRevisionExport reports whether name is one of the export names
// writeRevisionExports gives the run at path: "<stem>.r<N>.md" or ".pdf".
func isRevisionExport(path, name string) bool {
	rest, ok := strings.CutPrefix(name, filepath.Base(strings.TrimSuffix(path, ".json"))+".r")
	if !ok || filepath.Base(name) != name {
		return false
	}
	n, ext, _ := strings.Cut(rest, ".")
	_, err := strconv.Atoi(n)
	return err == nil && (ext == "md" || ext == "pdf")
}

// removeEdits deletes a run's edits and their exports: the same question
// asked again writes a new run over this one, and its edits were of the
// report it replaces.
func removeEdits(path string) error {
	e, err := LoadEdits(path)
	if err != nil {
		return os.Remove(EditsPath(path))
	}
	dir := filepath.Dir(path)
	var errs []error
	for _, r := range e.Revisions {
		for _, name := range []string{r.MD, r.PDF} {
			// Only the files a revision is written as: the names come from a
			// file anyone can edit, and "../x" there removed x.
			if name == "" || !isRevisionExport(path, name) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	if err := os.Remove(EditsPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
