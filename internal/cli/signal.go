package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// topLevel is the context key under which the process's own command keeps
// its *process: serve's in-process runs shadow it with a nil one (see
// inProcess), since a context value passes to every context derived from it.
type topLevel struct{}

// process records the signal that cancelled a headless run, so main can end
// the process by it once the run has written its done line.
type process struct {
	mu     sync.Mutex
	caught os.Signal
}

func (p *process) set(s os.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.caught == nil {
		p.caught = s
	}
}

func (p *process) signal() os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caught
}

// TopLevel is ctx marked as the process's own command: a headless run
// started under it ends on SIGINT or SIGTERM as a cancellation (done:
// cancelled, its checkpoint kept) instead of dying with no done line. main
// passes it to ExecuteContext and then to ExitBySignal; serve's in-process
// runs are not marked (see inProcess), so a signal still stops the server
// rather than the run it happens to be serving.
func TopLevel(ctx context.Context) context.Context {
	return context.WithValue(ctx, topLevel{}, &process{})
}

// inProcess is ctx for a run serve executes in-process. serve's own context
// is the top-level one, and cobra hands it down to every run serve starts,
// so the mark has to be taken off rather than merely not added.
func inProcess(ctx context.Context) context.Context {
	return context.WithValue(ctx, topLevel{}, (*process)(nil))
}

// ExitBySignal ends the process by the signal that cancelled its run, if one
// did, and returns otherwise. The run has written its done line and
// checkpoint by then; ending by the signal keeps the exit status a shell or
// supervisor reads (130, 143) saying the run was stopped, where returning
// would exit 0 as if it had finished. On Windows a process cannot send
// itself os.Interrupt, so it exits 1.
func ExitBySignal(ctx context.Context) {
	p, _ := ctx.Value(topLevel{}).(*process)
	if p == nil || p.signal() == nil {
		return
	}
	s := p.signal()
	signal.Reset(s)
	if self, err := os.FindProcess(os.Getpid()); err == nil && self.Signal(s) == nil {
		// Delivery to the process is asynchronous; this only covers a
		// signal that somehow does not end it.
		time.Sleep(time.Second)
	}
	os.Exit(1)
}

// caught is the signals a headless run ends on. One the process started
// with ignored stays ignored: a shell starts a script's background jobs with
// SIGINT ignored, so that Ctrl-C at the script leaves them running, and
// Notify would take that back.
func caught() []os.Signal {
	var out []os.Signal
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !signal.Ignored(s) {
			out = append(out, s)
		}
	}
	return out
}

// stopOnSignal is the context a run takes: cancelled by SIGINT or SIGTERM
// for a headless run of the process's own command. A run that draws the
// terminal UI is left alone: its signal handling gives the terminal back
// and re-raises the signal (see ui.catchSignals), and a handler here would
// catch the re-raised one. After the first signal the handler is dropped,
// so a second one ends the process at once, as a reader pressing Ctrl-C
// twice means.
func stopOnSignal(cmd *cobra.Command, drawsUI bool) (context.Context, context.CancelFunc) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	p, _ := ctx.Value(topLevel{}).(*process)
	sigs := caught()
	// No signals left to catch is no handler: Notify with none listed
	// would take every signal, a terminal resize among them. Go keeps only
	// SIGHUP and SIGINT ignored from the start, so this takes an Ignore
	// call in-process; it is cheap to rule out.
	if drawsUI || p == nil || len(sigs) == 0 {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, sigs...)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		// Recorded before the cancel, so the run cannot end on it unrecorded.
		case s := <-sig:
			p.set(s)
		case <-ctx.Done():
		}
		signal.Stop(sig)
		// A signal that arrived as the run ended on its own still ends the
		// process by it: someone asked for that.
		select {
		case s := <-sig:
			p.set(s)
		default:
		}
		cancel()
	}()
	return ctx, func() {
		cancel()
		<-finished
	}
}
