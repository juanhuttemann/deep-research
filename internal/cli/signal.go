package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// topLevel marks the context of the command the drsh process itself runs,
// as opposed to the copies serve executes in-process for each page launch.
type topLevel struct{}

// TopLevel is ctx marked as the process's own command: a headless run
// started under it ends on SIGINT or SIGTERM as a cancellation (done:
// cancelled, its checkpoint kept) instead of dying with no done line. main
// passes it to ExecuteContext; serve's in-process runs do not have it, so
// a signal still stops the server rather than the run it happens to be
// serving.
func TopLevel(ctx context.Context) context.Context {
	return context.WithValue(ctx, topLevel{}, true)
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
	if drawsUI || ctx.Value(topLevel{}) == nil {
		return ctx, func() {}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
