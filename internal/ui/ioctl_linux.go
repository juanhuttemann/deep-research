//go:build linux

package ui

import "golang.org/x/sys/unix"

// The termios ioctl request numbers differ between Linux and the BSDs, and
// x/sys/unix only defines each family's names on that family. Naming them
// here keeps input_unix.go — which is `//go:build unix`, so it also compiles
// for darwin — free of Linux-only constants. Building for macOS used to fail
// outright on TCGETS/TCSETSF being undefined there.
const (
	tcGet = unix.TCGETS
	// TCSETS applies raw mode without discarding typeahead, so what was typed
	// before can be counted (see newTTYInput).
	tcSet = unix.TCSETS
	// TCSETSF, not TCSAFLUSH: see setTermios in input_unix.go.
	tcSetFlush = unix.TCSETSF
)
