//go:build unix

package ui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ttyReader reads single keys from a terminal placed in raw mode.
type ttyReader struct {
	f   *os.File
	old *unix.Termios
	// closed guards the restore. Detaching gives the terminal back from the
	// key-reader goroutine while ui.Run still closes the same Input when the
	// run ends, so the restore has to be both idempotent and race-free.
	closed sync.Once
}

// setTermios applies t to fd.
//
// The request must be tcSetFlush (TCSETSF on Linux, TIOCSETAF on the BSDs).
// unix.TCSAFLUSH is 0x2 — a tcsetattr(3) optional_actions value, not an
// ioctl request number — and passing it here does not fail: the kernel accepts request 0x2 on a terminal and returns
// success without touching the termios at all. The terminal then stays in
// canonical mode with echo on, so single keypresses are never delivered and
// every key the UI offers silently does nothing.
func setTermios(fd int, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, tcSetFlush, t)
}

// newTTYInput puts f in raw mode so single keys can be read, returning an
// error (handled by the dispatcher) when f is not a terminal.
func newTTYInput(f *os.File) (Input, error) {
	if f == nil {
		return nil, errors.New("no input file")
	}
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, tcGet)
	if err != nil {
		return nil, err
	}
	// Only the input and local flags are touched. stdin and stdout address
	// the same terminal device, so clearing OPOST here would disable output
	// post-processing for the live UI as well: every "\n" would become a bare
	// line feed, each frame row would start where the last one ended, and the
	// display would staircase to the right instead of redrawing in place.
	// CREAD is likewise left alone — clearing it switches the receiver off.
	raw := *old
	raw.Iflag &^= unix.ICRNL | unix.IXON
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := setTermios(fd, &raw); err != nil {
		return nil, err
	}
	// Confirm the mode actually took. A termios request the kernel accepts
	// but ignores is not hypothetical — it is what this function used to do —
	// and a silent no-op here disables every key in the UI.
	if now, err := unix.IoctlGetTermios(fd, tcGet); err != nil ||
		now.Lflag&unix.ICANON != 0 || now.Lflag&unix.ECHO != 0 {
		_ = setTermios(fd, old)
		return nil, errors.New("raw mode not applied")
	}
	return &ttyReader{f: f, old: old}, nil
}

// NextWithin returns the next byte if one arrives within d. It polls the
// descriptor first so it never blocks past the deadline, which is what lets a
// lone Esc be told apart from the ESC that opens an arrow key's sequence.
func (t *ttyReader) NextWithin(d time.Duration) (byte, error) {
	fd := int(t.f.Fd())
	var set unix.FdSet
	set.Bits[fd/64] |= 1 << (uint(fd) % 64)
	tv := unix.NsecToTimeval(int64(d))
	n, err := unix.Select(fd+1, &set, nil, nil, &tv)
	if err != nil || n <= 0 {
		if err == nil {
			err = errNoKeyPending
		}
		return 0, err
	}
	return t.Next()
}

// errNoKeyPending reports that nothing arrived inside the escape window.
var errNoKeyPending = errors.New("no key pending")

func (t *ttyReader) Next() (byte, error) {
	var b [1]byte
	n, err := t.f.Read(b[:])
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return b[0], nil
}

func (t *ttyReader) NextLine() (string, error) {
	var buf bytes.Buffer
	for {
		var b [1]byte
		n, err := t.f.Read(b[:])
		if err != nil && n == 0 {
			return buf.String(), err
		}
		c := b[0]
		if c == '\n' || c == '\r' {
			break
		}
		buf.WriteByte(c)
	}
	return buf.String(), nil
}

func (t *ttyReader) Raw() bool { return true }

func (t *ttyReader) Close() {
	t.closed.Do(func() {
		if t.old != nil {
			_ = setTermios(int(t.f.Fd()), t.old)
		}
	})
}
