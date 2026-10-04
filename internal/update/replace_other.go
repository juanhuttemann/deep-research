//go:build !unix && !windows

package update

import (
	"fmt"
	"runtime"
)

// replaceExecutable refuses: whether a running binary can be renamed over is
// unknown here. Nothing reaches it today, since AssetName already refuses
// every platform this file builds for; the file keeps the package compiling.
func replaceExecutable(string, string) (string, error) {
	return "", fmt.Errorf("self-update is not supported on %s", runtime.GOOS)
}

func sweepBackups(string) {}

func syncDir(string) error { return nil }
