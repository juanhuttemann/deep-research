package main

import (
	"os"
	"sync"

	"github.com/juanhuttemann/deep-research/internal/agent"
	"github.com/juanhuttemann/deep-research/internal/cli"
	"github.com/juanhuttemann/deep-research/internal/config"
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
	load := sync.OnceValues(func() (cli.Deps, error) {
		cfg, err := config.Load()
		if err != nil {
			return cli.Deps{}, err
		}
		return cli.Deps{
			Assistant: sync.OnceValues(func() (agent.Assistant, error) {
				return agent.New(cfg.Config)
			}),
			Config: cfg,
		}, nil
	})
	root := cli.New(load)
	root.Version = version
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
