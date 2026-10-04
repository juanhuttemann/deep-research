package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/juanhuttemann/deep-research/internal/update"
)

// selfUpdate is swapped out by tests, which must not replace the test binary.
var selfUpdate = update.Run

func updateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "install the latest release over this binary",
		// No config load: a broken config or a missing key is no reason to
		// refuse the release that may fix it.
		RunE: runUpdate,
	}
	cmd.Flags().Bool("check", false, "only report whether a newer release exists; change nothing")
	return cmd
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	version := cmd.Root().Version
	if !update.Released(version) {
		return fmt.Errorf("update: %q is a source build, not a release; rebuild from source or reinstall with the install script", version)
	}
	check, _ := cmd.Flags().GetBool("check")
	result, err := selfUpdate(cmd.Context(), version, check)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	out := cmd.OutOrStdout()
	switch {
	case result.Updated:
		fmt.Fprintf(out, "updated %s -> %s\n", result.Current, result.Latest)
		if result.LeftoverBackup != "" {
			// Windows locks the running executable, so the update cannot
			// delete its own backup. Saying so beats failing an update that
			// worked; the next update removes the file.
			fmt.Fprintf(out, "the previous version is still running and was left at %s; the next update removes it\n", result.LeftoverBackup)
		}
		if result.Warning != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", result.Warning)
		}
	case result.State == update.UpgradeAvailable:
		fmt.Fprintf(out, "update available: %s (current: %s); run `deep-research update` to install it\n", result.Latest, result.Current)
	case result.State == update.NewerInstalled:
		fmt.Fprintf(out, "%s is newer than the latest release %s\n", result.Current, result.Latest)
	default:
		fmt.Fprintf(out, "up to date (%s)\n", result.Latest)
	}
	return nil
}
