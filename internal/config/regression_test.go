package config

// Regression suite. Every test here pins behaviour that a shipped bug once
// got wrong and names the failure it prevents, so a reader can tell settled
// ground from work in progress. The file was called pending_test.go, which
// read as unfinished work.

import (
	"path/filepath"
	"testing"
)

// chdirTemp points config discovery at fresh directories and returns the
// config dir, so a test never reads or writes the developer's real config.
func chdirTemp(t *testing.T) string {
	t.Helper()
	cfgDir := t.TempDir()
	t.Setenv("DEEP_RESEARCH_CONFIG_DIR", cfgDir)
	t.Chdir(t.TempDir())
	return cfgDir
}

// `init` listed the files it wrote by ranging a map, so two identical runs
// reported them in different orders — output that cannot be diffed, scripted
// against, or trusted to mean the same thing twice.
func TestInitListsCreatedFilesInAFixedOrder(t *testing.T) {
	want := []string{"config.yaml", "agent.yaml", ".env"}
	for range 20 {
		chdirTemp(t)
		_, created, err := Init()
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		if len(created) != len(want) {
			t.Fatalf("created %v, want %d files", created, len(want))
		}
		for i, name := range want {
			if filepath.Base(created[i]) != name {
				t.Fatalf("created = %v, want the order %v", created, want)
			}
		}
	}
}

// Sub-agent concurrency is a budget like every other one, and on a rate-limited
// provider it is the one that has to come down. It used to be a literal 3 in
// the UI with no way to reach it.
func TestParallelismIsConfigurable(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		env  string
		want int
	}{
		{name: "default when unset", yaml: "offline: true\n", want: 3},
		{name: "from the config file", yaml: "parallelism: 6\n", want: 6},
		{name: "environment overrides the file", yaml: "parallelism: 6\n", env: "2", want: 2},
		// 0 or a negative number would otherwise mean "no sub-agent runs".
		{name: "zero falls back", yaml: "parallelism: 0\n", want: 3},
		{name: "negative falls back", yaml: "parallelism: -4\n", want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := chdirTemp(t)
			writeFile(t, filepath.Join(cfgDir, "config.yaml"), tc.yaml)
			if tc.env != "" {
				t.Setenv("DEEP_RESEARCH_PARALLELISM", tc.env)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Parallelism != tc.want {
				t.Errorf("Parallelism = %d, want %d", cfg.Parallelism, tc.want)
			}
		})
	}
}

// Every config.yaml key can be overridden as DEEP_RESEARCH_<KEY>. That is a
// documented channel now (README + config.yaml), so it is also a contract:
// these keys must keep resolving from the environment, and the environment
// must keep winning over the file.
func TestDocumentedEnvOverridesApply(t *testing.T) {
	cfgDir := chdirTemp(t)
	writeFile(t, filepath.Join(cfgDir, "config.yaml"),
		"offline: false\nsources_per_topic: 2\nrun_timeout: 30m\n")
	t.Setenv("DEEP_RESEARCH_OFFLINE", "true")
	t.Setenv("DEEP_RESEARCH_SOURCES_PER_TOPIC", "9")
	t.Setenv("DEEP_RESEARCH_RUN_TIMEOUT", "90s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Offline {
		t.Error("DEEP_RESEARCH_OFFLINE did not override offline: false")
	}
	if cfg.SourcesPerTopic != 9 {
		t.Errorf("SourcesPerTopic = %d, want 9", cfg.SourcesPerTopic)
	}
	if cfg.RunTimeout.String() != "1m30s" {
		t.Errorf("RunTimeout = %s, want 1m30s", cfg.RunTimeout)
	}
}

// The override prefix is what the README and config.yaml tell people to type.
// Renaming it would break every documented DEEP_RESEARCH_* variable at once.
func TestEnvPrefixMatchesTheDocumentedName(t *testing.T) {
	if EnvPrefix != "DEEP_RESEARCH" {
		t.Errorf("EnvPrefix = %q, but the docs promise DEEP_RESEARCH_<KEY>", EnvPrefix)
	}
}
