// Package config holds the embedded default configuration files.
package config

import "embed"

// ConfigFS holds the embedded default config files.
//
//go:embed config.yaml agent.yaml
var ConfigFS embed.FS
