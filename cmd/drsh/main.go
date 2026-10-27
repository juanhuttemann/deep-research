package main

import (
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
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
