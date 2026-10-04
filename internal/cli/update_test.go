package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/update"
)

// runUpdateWith executes `update args...` on a binary stamped version, with
// selfUpdate replaced by fake. It never loads config.
func runUpdateWith(t *testing.T, version string, fake func(context.Context, string, bool) (update.Result, error), args ...string) (string, error) {
	t.Helper()
	orig := selfUpdate
	selfUpdate = fake
	t.Cleanup(func() { selfUpdate = orig })
	cmd := New(func() (Deps, error) {
		t.Error("update loaded the config")
		return Deps{}, nil
	})
	cmd.Version = version
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"update"}, args...))
	err := cmd.Execute()
	return buf.String(), err
}

func TestUpdateRefusesASourceBuild(t *testing.T) {
	for _, v := range []string{"dev", "v0.5.0-3-gc8b3b08-dirty"} {
		_, err := runUpdateWith(t, v, func(context.Context, string, bool) (update.Result, error) {
			t.Errorf("%s: a source build reached the network", v)
			return update.Result{}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "source build") {
			t.Errorf("%s: got %v, want a source-build refusal", v, err)
		}
	}
}

func TestUpdatePassesCheckAndVersionThrough(t *testing.T) {
	var gotVersion string
	var gotCheck bool
	out, err := runUpdateWith(t, "0.5.0", func(_ context.Context, v string, check bool) (update.Result, error) {
		gotVersion, gotCheck = v, check
		return update.Result{Current: "v0.5.0", Latest: "v0.6.0", State: update.UpgradeAvailable}, nil
	}, "--check")
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != "0.5.0" || !gotCheck {
		t.Errorf("selfUpdate(%q, %v), want (0.5.0, true)", gotVersion, gotCheck)
	}
	if !strings.Contains(out, "update available: v0.6.0") {
		t.Errorf("output %q does not name the new release", out)
	}
}

func TestUpdateReportsEachOutcome(t *testing.T) {
	for _, tc := range []struct {
		result update.Result
		want   string
	}{
		{update.Result{Current: "v0.5.0", Latest: "v0.6.0", State: update.UpgradeAvailable, Updated: true}, "updated v0.5.0 -> v0.6.0"},
		{update.Result{Current: "v0.6.0", Latest: "v0.6.0", State: update.UpToDate}, "up to date (v0.6.0)"},
		{update.Result{Current: "v0.7.0", Latest: "v0.6.0", State: update.NewerInstalled}, "newer than the latest release v0.6.0"},
		{update.Result{Current: "v0.5.0", Latest: "v0.6.0", State: update.UpgradeAvailable, Updated: true,
			LeftoverBackup: `C:\bin\deep-research.exe.old-1`}, `left at C:\bin\deep-research.exe.old-1`},
	} {
		out, err := runUpdateWith(t, "0.5.0", func(context.Context, string, bool) (update.Result, error) {
			return tc.result, nil
		})
		if err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("got %q, %v; want it to say %q", out, err, tc.want)
		}
	}
}

// update's verifyVersion accepts a downloaded binary only if the last word of
// its --version output is the release tag; a changed version template would
// make every update fail verification.
func TestVersionOutputEndsWithTheVersion(t *testing.T) {
	cmd := New(func() (Deps, error) { return Deps{}, nil })
	cmd.Version = "0.6.0"
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if fields := strings.Fields(buf.String()); len(fields) == 0 || fields[len(fields)-1] != "0.6.0" {
		t.Errorf("--version printed %q; its last word must be the version", buf.String())
	}
}

func TestUpdateWarnsWhenTheSwapWasNotSynced(t *testing.T) {
	out, err := runUpdateWith(t, "0.5.0", func(context.Context, string, bool) (update.Result, error) {
		return update.Result{Current: "v0.5.0", Latest: "v0.6.0", State: update.UpgradeAvailable,
			Updated: true, Warning: "directory could not be synced"}, nil
	})
	if err != nil || !strings.Contains(out, "updated v0.5.0 -> v0.6.0") || !strings.Contains(out, "warning: directory could not be synced") {
		t.Errorf("got %q, %v; want the update and the warning", out, err)
	}
}
