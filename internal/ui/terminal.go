package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ANSI escape helpers for interactive terminals. All are no-ops on non-TTY
// output, which keeps headless modes clean.

const (
	// cursorHome moves the cursor to the top-left of the current viewport.
	cursorHome = "\x1b[H"
	// cursorHide toggles the cursor visibility.
	cursorHide = "\x1b[?25l"
	// cursorShow restores the cursor after a live repaint.
	cursorShow = "\x1b[?25h"
	// eraseToEnd erases from the cursor to the end of the screen.
	eraseToEnd = "\x1b[0J"
	// eraseLine erases from the cursor to the end of the current row.
	eraseLine = "\x1b[0K"
	// eraseScreen clears the whole viewport.
	eraseScreen = "\x1b[2J"
	// crlf ends a row. The carriage return is explicit because raw mode may
	// have output post-processing disabled, in which case a bare \n moves down
	// without returning to column 0 and the frame walks off to the right.
	crlf = "\r\n"
)

// termSize returns the rows x columns of the given file's terminal. When the
// file is not a TTY it returns (0, 0).
func termSize(f *os.File) (rows, cols int) {
	// The runtime package exposes term.GetSize on unix via golang.org/x/sys.
	return rawTermSize(f)
}

// IsTTYFile reports whether f is an interactive terminal.
func IsTTYFile(f *os.File) bool {
	_, cols := termSize(f)
	return cols > 0
}

// isTTYWriter reports whether an output writer targets a terminal.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && IsTTYFile(f)
}

// isTTYReader reports whether an input source is a terminal.
func isTTYReader(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && IsTTYFile(f)
}

// formatDuration renders a duration as H:MM:SS (or M:SS under an hour).
func formatDuration(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	h, rem := total/3600, total%3600
	m, s := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// statusSpinner animates msg on w until the returned stop func is called, so a
// slow step — the planning call, which is several seconds of nothing before
// the brief can be drawn — never looks like a hung process. The returned set
// func appends a live sub-status ("asking <model> for sub-topics", a retry
// notice) so the wait says what it is waiting on, not just how long it has
// been. On a non-terminal writer each distinct status is printed on its own
// line instead, which is what a log or a piped run wants.
func statusSpinner(w io.Writer, msg string) (set func(string), stop func()) {
	if !isTTYWriter(w) {
		_, _ = fmt.Fprintf(w, "%s\n", msg)
		var last string
		return func(s string) {
			if s = strings.TrimSpace(s); s != "" && s != last {
				last = s
				_, _ = fmt.Fprintf(w, "%s\n", s)
			}
		}, func() {}
	}
	return spin(w, msg)
}

// spin is statusSpinner's animated half, split out so it can be driven with a
// plain writer in a test.
func spin(w io.Writer, msg string) (set func(string), stop func()) {
	done := make(chan struct{})
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		status string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := time.Now()
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for i := 0; ; i++ {
			mu.Lock()
			line := msg
			if status != "" {
				line += " — " + status
			}
			mu.Unlock()
			// The row is erased before each repaint, so a status that shrinks
			// cannot leave the tail of a longer one behind it.
			_, _ = fmt.Fprintf(w, "\r%s%s %s (%s)", eraseLine,
				spinnerFrames[i%len(spinnerFrames)], line, formatDuration(time.Since(start)))
			select {
			case <-done:
				_, _ = fmt.Fprintf(w, "\r%s", eraseLine)
				return
			case <-t.C:
			}
		}
	}()
	return func(s string) {
			mu.Lock()
			status = strings.TrimSpace(s)
			mu.Unlock()
		}, func() {
			close(done)
			wg.Wait()
		}
}
