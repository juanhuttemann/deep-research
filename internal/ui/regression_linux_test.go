package ui

// Regression tests that need a real terminal: a pseudo-terminal from
// openPTY, which is Linux-only (see term_unix_test.go).

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// rawMode reports whether the terminal f addresses has echo and line
// editing off, as the live UI leaves it while it runs.
func rawMode(t *testing.T, f *os.File) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), tcGet)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Lflag&(unix.ECHO|unix.ICANON) == 0
}

// The key reader's goroutine parked in read(2) on stdin and stayed there
// after the run: Close restored the terminal but nothing woke the read,
// which then waited for a whole line in canonical mode. It ends with Close.
func TestKeyReaderEndsWhenTheInputCloses(t *testing.T) {
	master, slave := openPTY(t)
	in, err := newTTYInput(slave)
	if err != nil {
		t.Skipf("raw mode: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := in.Next(); done <- err }()
	time.Sleep(50 * time.Millisecond) // the reader is waiting for a key
	in.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("reader ended with %v, want io.EOF", err)
		}
	case <-time.After(time.Second):
		_, _ = master.Write([]byte("x\n")) // let the stuck reader go
		t.Error("the key reader still waits for a key after Close")
	}
	if rawMode(t, slave) {
		t.Error("Close left the terminal raw")
	}
}

// A run killed from outside (kill, timeout(1), a closed terminal window)
// died with the terminal raw: no echo, no line editing, no cursor, because
// a signal's default action runs no defers. The terminal is given back
// before the signal ends the process.
func TestKilledRunGivesTheTerminalBack(t *testing.T) {
	if os.Getenv("UI_KILLED_RUN_HELPER") != "" {
		tty := os.NewFile(3, "tty")
		// A run that finished before the signal wrote its report to the
		// default reports/ beside the package, and one was committed.
		_, _ = Run(context.Background(), Options{
			Question:  "q",
			Assistant: &fakeAssistant{summary: "s"},
			Input:     tty,
			Stdout:    tty,
			Stderr:    tty,
			OutDir:    t.TempDir(),
		})
		return
	}
	master, slave := openPTY(t)
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 100}); err != nil {
		t.Skipf("TIOCSWINSZ: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, master) }() // the frame must not fill the pty
	cmd := exec.Command(os.Args[0], "-test.run=^TestKilledRunGivesTheTerminalBack$")
	cmd.Env = append(os.Environ(), "UI_KILLED_RUN_HELPER=1")
	cmd.ExtraFiles = []*os.File{slave}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); !rawMode(t, slave); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the run never put the terminal in raw mode")
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err := cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("the run did not end by SIGTERM: %v", err)
	}
	if rawMode(t, slave) {
		t.Error("a run killed by SIGTERM left the terminal raw")
	}
}

// The questions were drawn on the run's own renderer, and the guard that
// gives the terminal back after them closed it: the brief that followed was
// never drawn, and the run sat waiting on a key for a screen nobody saw.
func TestBriefIsDrawnAfterTheQuestions(t *testing.T) {
	master, slave := openPTY(t)
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 100}); err != nil {
		t.Skipf("TIOCSWINSZ: %v", err)
	}
	var mu sync.Mutex
	var screen bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			screen.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	// Esc at the first question, then q at the brief, each once its screen
	// is up.
	go func() {
		for _, step := range []struct{ shown, key string }{{"A FEW QUESTIONS FIRST", "\x1b"}, {"RESEARCH BRIEF", "q"}} {
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				mu.Lock()
				shown := bytes.Contains(screen.Bytes(), []byte(step.shown))
				mu.Unlock()
				if shown {
					time.Sleep(3 * keyPoll)
					_, _ = master.Write([]byte(step.key))
					break
				}
			}
		}
	}()
	done := make(chan RunResult, 1)
	go func() {
		res, err := Run(context.Background(), Options{
			Question: "q", Assistant: &fakeAssistant{questions: []Question{{Text: "Which?", Options: []string{"a", "b"}}}},
			OutDir: t.TempDir(), Input: slave, Stdout: slave, Stderr: slave, Clarify: true,
		})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if !res.Cancelled {
			t.Errorf("the run was not cancelled at the brief: %+v", res)
		}
	case <-time.After(15 * time.Second):
		_, _ = master.Write([]byte("q"))
		t.Fatal("no brief after the questions")
	}
}

// Leaving a report's ask ▸ box drew the start screen in place, under the
// report, which still filled the screen. A terminal is cleared first; a
// pipe or a file gets no escape sequence.
func TestClearScreenClearsOnlyATerminal(t *testing.T) {
	master, slave := openPTY(t)
	// A pseudo-terminal with no size is not taken for a terminal.
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Skipf("TIOCSWINSZ: %v", err)
	}
	ClearScreen(slave)
	buf := make([]byte, 64)
	n, err := master.Read(buf)
	if err != nil || string(buf[:n]) != cursorHome+eraseToEnd {
		t.Errorf("terminal got %q (%v), want the screen cleared", buf[:n], err)
	}
	var out bytes.Buffer
	ClearScreen(&out)
	if out.Len() > 0 {
		t.Errorf("a non-terminal got %q", out.String())
	}
}
