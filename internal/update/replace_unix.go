//go:build unix

package update

import (
	"errors"
	"fmt"
	"os"
)

// replaceExecutable renames over the running binary. Unix lets a running
// executable be replaced: the process keeps its open inode, so the rename is
// atomic and leaves nothing behind.
func replaceExecutable(current, candidate string) (string, error) {
	if err := os.Rename(candidate, current); err != nil {
		return "", fmt.Errorf("replace executable: %w", err)
	}
	return "", nil
}

// sweepBackups does nothing here: only Windows updates leave <exe>.old-*
// files, and a file by that name beside a Unix binary is the user's own
// rollback copy.
func sweepBackups(string) {}

// syncDir makes the rename durable: without it a crash right after the
// update can bring the old directory entry back.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
