package ui

// Regression tests that need a real terminal: a pseudo-terminal from
// openPTY, which is Linux-only (see term_unix_test.go).

import (
	"context"
	"io"
	"os"
	"os/exec"
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
		_, _ = Run(context.Background(), Options{
			Question:  "q",
			Assistant: &fakeAssistant{summary: "s"},
			Input:     tty,
			Stdout:    tty,
			Stderr:    tty,
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
