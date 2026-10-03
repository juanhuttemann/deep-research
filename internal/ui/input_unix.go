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
	// ignored counts keys typed before raw mode began — during planning,
	// before the brief existed. They are discarded, not applied: nobody can
	// confirm or cancel a plan they have not seen. Counting them is what lets
	// the brief say an Enter was ignored rather than appear to hang.
	ignored int
	// mu is held from a read's poll to its read(2), and by Close while it
	// marks done. A restore that slipped between the two (canonical mode, the
	// pending input flushed) left the read waiting for a whole line, and the
	// key reader parked in it long after the run had ended.
	mu   sync.Mutex
	done bool
}

// ownsTerminal marks the one Input that changed the terminal's mode.
func (t *ttyReader) ownsTerminal() {}

// keyPoll is how long one wait for a key lasts before the reader checks
// whether it was closed: the most a Close waits for a read in progress.
const keyPoll = 100 * time.Millisecond

// Ignored reports how many keys typed before the brief were discarded.
func (t *ttyReader) Ignored() int { return t.ignored }

// setTermios applies t to fd with an ioctl request: tcSet, or tcSetFlush
// (TCSETSF on Linux, TIOCSETAF on the BSDs) to also discard pending input.
// It must be one of those request numbers — unix.TCSAFLUSH is 0x2 — a tcsetattr(3) optional_actions value, not an
// ioctl request number — and passing it here does not fail: the kernel accepts request 0x2 on a terminal and returns
// success without touching the termios at all. The terminal then stays in
// canonical mode with echo on, so single keypresses are never delivered and
// every key the UI offers silently does nothing.
func setTermios(fd int, req uint, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, req, t)
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
	if err := setTermios(fd, tcSet, &raw); err != nil {
		return nil, err
	}
	// Confirm the mode actually took. A termios request the kernel accepts
	// but ignores is not hypothetical — it is what this function used to do —
	// and a silent no-op here disables every key in the UI.
	if now, err := unix.IoctlGetTermios(fd, tcGet); err != nil ||
		now.Lflag&unix.ICANON != 0 || now.Lflag&unix.ECHO != 0 {
		_ = setTermios(fd, tcSetFlush, old)
		return nil, errors.New("raw mode not applied")
	}
	t := &ttyReader{f: f, old: old}
	// Drain what was typed before now, counting it. Flushing it in the
	// ioctl, as this used to, lost an Enter pressed during planning without
	// a trace. Draining after the switch also catches a half-typed line the
	// canonical mode was still holding.
	for {
		if _, err := t.NextWithin(0); err != nil {
			break
		}
		t.ignored++
	}
	return t, nil
}

// NextWithin returns the next byte if one arrives within d. It polls the
// descriptor first so it never blocks past the deadline, which is what lets a
// lone Esc be told apart from the ESC that opens an arrow key's sequence.
// It returns io.EOF once the reader is closed.
func (t *ttyReader) NextWithin(d time.Duration) (byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return 0, io.EOF
	}
	fd := int(t.f.Fd())
	var set unix.FdSet
	// Set, not hand-rolled bit arithmetic: the word size of an FdSet is 64
	// bits on linux/amd64 but 32 on darwin and 32-bit unix.
	set.Set(fd)
	tv := unix.NsecToTimeval(int64(d))
	n, err := unix.Select(fd+1, &set, nil, nil, &tv)
	switch {
	case errors.Is(err, unix.EINTR):
		// A signal (a window resize) interrupted the wait: no key came.
		return 0, errNoKeyPending
	case err != nil:
		return 0, err
	case n <= 0:
		return 0, errNoKeyPending
	}
	return t.read()
}

// errNoKeyPending reports that nothing arrived inside the escape window.
var errNoKeyPending = errors.New("no key pending")

// Next waits for a key, in keyPoll steps so that a Close ends the wait: a
// blocking read(2) on stdin outlived the run it served.
func (t *ttyReader) Next() (byte, error) {
	for {
		b, err := t.NextWithin(keyPoll)
		if !errors.Is(err, errNoKeyPending) {
			return b, err
		}
	}
}

// read takes one byte that select reported waiting.
func (t *ttyReader) read() (byte, error) {
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
		t.mu.Lock()
		t.done = true
		t.mu.Unlock()
		if t.old != nil {
			// Flushed: keys pressed during the run are not the shell's.
			_ = setTermios(int(t.f.Fd()), tcSetFlush, t.old)
		}
	})
}
