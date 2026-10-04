//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	winExe = `C:\bin\deep-research.exe`
	winNew = `C:\bin\deep-research.exe.new`
	winOld = `C:\bin\deep-research.exe.old-42`
)

func TestReplaceExecutableReplacesOrdinaryFile(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "deep-research.exe")
	candidate := filepath.Join(dir, "deep-research.exe.new")
	writeFile(t, current, "old", 0o755)
	writeFile(t, candidate, "new", 0o755)
	if _, err := replaceExecutable(current, candidate); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(current); string(b) != "new" {
		t.Errorf("executable is %q, want the new binary", b)
	}
	if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("candidate still there: %v", err)
	}
}

func TestReplaceExecutableWindows(t *testing.T) {
	var renames [][2]string
	var removed string
	ops := windowsReplaceOps{
		rename: func(from, to string) error {
			renames = append(renames, [2]string{from, to})
			return nil
		},
		remove: func(p string) error { removed = p; return nil },
		scheduleDelete: func(string) error {
			t.Fatal("scheduled deletion after a successful removal")
			return nil
		},
	}
	leftover, err := replaceExecutableWindows(winExe, winNew, "42", ops)
	if err != nil || leftover != "" {
		t.Fatalf("got %q, %v; want no leftover", leftover, err)
	}
	if want := [][2]string{{winExe, winOld}, {winNew, winExe}}; !slices.Equal(renames, want) {
		t.Errorf("renames %v, want %v", renames, want)
	}
	if removed != winOld {
		t.Errorf("removed %q, want %q", removed, winOld)
	}
}

func TestReplaceExecutableWindowsRollsBack(t *testing.T) {
	replaceErr := errors.New("replace failed")
	var renames [][2]string
	ops := windowsReplaceOps{
		rename: func(from, to string) error {
			renames = append(renames, [2]string{from, to})
			if from == winNew {
				return replaceErr
			}
			return nil
		},
		remove: func(string) error {
			t.Fatal("removed the backup after a failed replacement")
			return nil
		},
	}
	_, err := replaceExecutableWindows(winExe, winNew, "42", ops)
	if !errors.Is(err, replaceErr) {
		t.Fatalf("got %v, want the replace error", err)
	}
	if want := [][2]string{{winExe, winOld}, {winNew, winExe}, {winOld, winExe}}; !slices.Equal(renames, want) {
		t.Errorf("renames %v, want %v", renames, want)
	}
}

func TestReplaceExecutableWindowsReportsRollbackFailure(t *testing.T) {
	replaceErr := errors.New("replace failed")
	rollbackErr := errors.New("rollback failed")
	ops := windowsReplaceOps{
		rename: func(from, _ string) error {
			switch from {
			case winExe:
				return nil
			case winNew:
				return replaceErr
			default:
				return rollbackErr
			}
		},
	}
	_, err := replaceExecutableWindows(winExe, winNew, "42", ops)
	if !errors.Is(err, replaceErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("got %v, want both errors", err)
	}
}

// Windows locks a running executable, so the process that made the backup
// cannot delete it, and the reboot-time fallback needs administrator rights.
// Both failing is the normal case; the update itself worked.
func TestReplaceExecutableWindowsKeepsGoingWhenTheBackupCannotBeDeleted(t *testing.T) {
	ops := windowsReplaceOps{
		rename:         func(string, string) error { return nil },
		remove:         func(string) error { return errors.New("in use") },
		scheduleDelete: func(string) error { return errors.New("Access is denied") },
	}
	leftover, err := replaceExecutableWindows(winExe, winNew, "42", ops)
	if err != nil || leftover != winOld {
		t.Fatalf("got %q, %v; want leftover %q and no error", leftover, err, winOld)
	}
}

// A recycled pid means <exe>.old-<pid> may already exist, locked; renaming
// onto it fails. The retry with a fresh suffix keeps that from aborting.
func TestReplaceExecutableWindowsRetriesOnPidReuse(t *testing.T) {
	var renames [][2]string
	ops := windowsReplaceOps{
		rename: func(from, to string) error {
			renames = append(renames, [2]string{from, to})
			if from == winExe && to == winOld {
				return errors.New("Access is denied")
			}
			return nil
		},
		remove:         func(string) error { return errors.New("in use") },
		scheduleDelete: func(string) error { return nil },
	}
	leftover, err := replaceExecutableWindows(winExe, winNew, "42", ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(renames) != 3 || !strings.HasPrefix(renames[1][1], winOld+"-") || leftover != renames[1][1] {
		t.Errorf("renames %v, leftover %q; want a retry under a fresh suffix", renames, leftover)
	}
}

func TestSweepBackupsOnWindows(t *testing.T) {
	current := filepath.Join(t.TempDir(), "deep-research.exe")
	writeFile(t, current, "new", 0o755)
	stale := current + ".old-1"
	writeFile(t, stale, "stale", 0o644)
	sweepBackups(current)
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an earlier update's backup is still there: %v", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Errorf("the executable itself was swept: %v", err)
	}
}
