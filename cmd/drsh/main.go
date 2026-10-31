package main

import (
	"context"
	"os"
	"sync"

	"github.com/juanhuttemann/deep-research/internal/cli"
)

// version is stamped at link time by the build:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// It stays "dev" for a plain `go build`, which is honest: that binary has no
// release identity to report.
var version = "dev"

func main() {
	// Deferred: `init` writes the config files, so it has to run before
	// there is a config to load.
	load := sync.OnceValues(cli.LoadDeps)
	root := cli.New(load)
	root.Version = version
	// Marked as the process's own command: a headless run it starts ends on
	// SIGINT or SIGTERM with a done line (see cli.TopLevel).
	if err := root.ExecuteContext(cli.TopLevel(context.Background())); err != nil {
		os.Exit(1)
	}
}
