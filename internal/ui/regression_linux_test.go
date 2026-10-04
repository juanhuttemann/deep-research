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

// The key reader runs until the input closes, through the report and the
// exports, so a b pressed there detaches the run too. The run sampled its
// detach before them and said it had not, and the CLI then opened the
// question prompt on a terminal it had just given back, where what is typed
// next is meant for the shell.
func TestDetachWhileTheReportIsWrittenIsReported(t *testing.T) {
	// A watched run must still come back undetached once its reader stops,
	// or every run would lose the prompt the reader never asked to skip.
	for name, key := range map[string]string{"pressed b": "b", "watched": ""} {
		t.Run(name, func(t *testing.T) {
			if got := runPressingAfterReport(t, key); got != (key == "b") {
				t.Errorf("Detached = %v after pressing %q while the report was written", got, key)
			}
		})
	}
}

// runPressingAfterReport runs in a pseudo-terminal, launches at the brief,
// types key once the report is rendered and its files written, and returns
// whether the run reported it detached.
func runPressingAfterReport(t *testing.T, key string) bool {
	t.Helper()
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
	// Keys typed before the brief are discarded, so Enter waits for it.
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			mu.Lock()
			shown := bytes.Contains(screen.Bytes(), []byte("[enter] launch"))
			mu.Unlock()
			if shown {
				_, _ = master.Write([]byte("\r"))
				return
			}
		}
	}()
	res, err := Run(context.Background(), Options{
		Question: "q", Assistant: &fakeAssistant{summary: "s"}, OutDir: t.TempDir(),
		Input: slave, Stdout: slave, Stderr: slave,
		// The bell rings once the report is rendered and its files written:
		// the key is typed there, and given time to be read.
		Bell: func(string) {
			_, _ = master.Write([]byte(key))
			time.Sleep(3 * keyPoll)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res.Detached
}
