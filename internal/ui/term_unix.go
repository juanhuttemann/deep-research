//go:build unix

package ui

import (
	"os"

	"golang.org/x/sys/unix"
)

// rawTermSize reports the size of f's controlling terminal. It returns (0, 0)
// when f is not a terminal.
//
// The request must be TIOCGWINSZ. IoctlGetWinsize passes the kernel a pointer
// to an 8-byte unix.Winsize; TCGETS is the termios request, so the kernel
// writes a ~60-byte struct termios through that pointer and smashes whatever
// follows it. The corruption is invisible on a pipe, where TCGETS fails with
// ENOTTY before writing anything, and fatal on a real terminal.
func rawTermSize(f *os.File) (rows, cols int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws == nil {
		return 0, 0
	}
	return int(ws.Row), int(ws.Col)
}
