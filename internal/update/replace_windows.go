//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// windowsReplaceOps are the file operations replaceExecutableWindows makes,
// injectable so its rollback paths are testable without a locked file.
type windowsReplaceOps struct {
	rename         func(string, string) error
	remove         func(string) error
	scheduleDelete func(string) error
}

// replaceExecutable moves the running binary aside first: Windows will not
// overwrite or delete a running executable, but it will rename one.
func replaceExecutable(current, candidate string) (string, error) {
	ops := windowsReplaceOps{
		rename: os.Rename,
		remove: os.Remove,
		scheduleDelete: func(path string) error {
			p, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return fmt.Errorf("encode backup path: %w", err)
			}
			if err := windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
				return fmt.Errorf("schedule backup deletion: %w", err)
			}
			return nil
		},
	}
	return replaceExecutableWindows(current, candidate, backupSuffix(), ops)
}

// backupSuffix names this run's backup. The nanosecond tail keeps a recycled
// pid from colliding with a locked <exe>.old-<pid> left by an earlier run.
func backupSuffix() string {
	return strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// sweepBackups deletes what earlier updates could not. The process that made
// a backup is the one process that cannot delete it; by the next update it
// has exited. Anything still locked is tried again next time.
func sweepBackups(current string) {
	dir := filepath.Dir(current)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Base(current) + ".old-"
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func replaceExecutableWindows(current, candidate, suffix string, ops windowsReplaceOps) (leftover string, err error) {
	backup := current + ".old-" + suffix
	if err := ops.rename(current, backup); err != nil {
		// A name taken by a locked leftover: retry once with a fresh tail
		// instead of aborting.
		backup = current + ".old-" + suffix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		if retryErr := ops.rename(current, backup); retryErr != nil {
			return "", fmt.Errorf("back up current executable: %w", errors.Join(err, retryErr))
		}
	}
	if err := ops.rename(candidate, current); err != nil {
		replaceErr := fmt.Errorf("replace current executable: %w", err)
		if rollbackErr := ops.rename(backup, current); rollbackErr != nil {
			return "", errors.Join(replaceErr, fmt.Errorf("restore current executable: %w", rollbackErr))
		}
		return "", replaceErr
	}
	// A backup that cannot be deleted is not a failed update: the new binary
	// is already in place. The running process is exactly the one that
	// cannot remove its own backup, and the reboot-time fallback needs
	// administrator rights an ordinary install lacks, so both failing is the
	// normal case. The path is returned so the caller can say what was left.
	if err := ops.remove(backup); err != nil {
		_ = ops.scheduleDelete(backup) // best effort; needs elevation
		return backup, nil
	}
	return "", nil
}

// syncDir is a no-op: Windows cannot open a directory to fsync it.
func syncDir(string) error { return nil }
