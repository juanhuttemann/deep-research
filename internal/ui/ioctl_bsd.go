//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package ui

import "golang.org/x/sys/unix"

// The BSD spellings of the requests documented in ioctl_linux.go. TIOCGETA
// and TIOCSETAF are what darwin's tcgetattr/tcsetattr(TCSAFLUSH) issue.
const (
	tcGet      = unix.TIOCGETA
	tcSetFlush = unix.TIOCSETAF
)
