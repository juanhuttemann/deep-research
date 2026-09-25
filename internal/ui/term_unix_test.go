//go:build unix

package ui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY returns the master and slave ends of a new pseudo-terminal.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("TIOCSPTLCK: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("TIOCGPTN: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open slave: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

// TestRawTermSizeOnPTY exercises rawTermSize against a real terminal.
//
// This is the case a pipe cannot reach: the previous implementation asked for
// TCGETS, so the kernel wrote a ~60-byte struct termios through a pointer to
// an 8-byte unix.Winsize and smashed the stack. On a pipe TCGETS fails with
// ENOTTY before writing, which is why every piped test passed.
func TestRawTermSizeOnPTY(t *testing.T) {
	master, slave := openPTY(t)

	want := unix.Winsize{Row: 24, Col: 80}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &want); err != nil {
		t.Skipf("TIOCSWINSZ: %v", err)
	}

	rows, cols := rawTermSize(slave)
	if rows != 24 || cols != 80 {
		t.Fatalf("rawTermSize = %dx%d, want 24x80", rows, cols)
	}
	if !IsTTYFile(slave) {
		t.Error("IsTTYFile said a PTY is not a terminal")
	}
}

// TestRawTermSizeOnPipe keeps the non-terminal path honest.
func TestRawTermSizeOnPipe(t *testing.T) {
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()

	if rows, cols := rawTermSize(wr); rows != 0 || cols != 0 {
		t.Errorf("rawTermSize on a pipe = %dx%d, want 0x0", rows, cols)
	}
	if IsTTYFile(wr) {
		t.Error("IsTTYFile said a pipe is a terminal")
	}
}

func TestMarkdownOnTerminal(t *testing.T) {
	master, slave := openPTY(t)
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	body := "# Report\n\n**Finding**\n"
	for _, tc := range []struct {
		name    string
		noColor bool
		env     string
		term    string
		styled  bool
	}{
		{name: "terminal", term: "xterm-256color", styled: true},
		{name: "flag", term: "xterm-256color", noColor: true},
		{name: "environment", term: "xterm-256color", env: "1"},
		{name: "dumb", term: "dumb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.env)
			t.Setenv("TERM", tc.term)
			t.Setenv("GLAMOUR_STYLE", "dark")
			got := FormatMarkdown(slave, body, tc.noColor)
			if tc.styled {
				if !strings.Contains(got, "\x1b[") || strings.Contains(got, "**Finding**") {
					t.Errorf("expected rendered Markdown: %q", got)
				}
			} else if got != body {
				t.Errorf("expected unchanged Markdown: %q", got)
			}
		})
	}
}

func TestSpinPaintsAFrameAndErasesOnStop(t *testing.T) {
	var buf bytes.Buffer
	_, stop := spin(&buf, "Planning research")
	stop() // must not block, and must clean the row up behind it
	out := buf.String()
	if !strings.Contains(out, "Planning research") {
		t.Errorf("spinner never painted its message: %q", out)
	}
	if !strings.HasSuffix(out, "\r"+eraseLine) {
		t.Errorf("spinner left its row on screen: %q", out)
	}
}
