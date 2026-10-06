package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

// newAddCmd builds `overlay add`.
func newAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "add [paths...]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Add files to the staging area",
		Long: `Add files to the Git staging area in the overlay repository.
If no paths are specified, adds all changes (equivalent to "git add .").`,
		RunE: runAdd,
	}
	return cmd
}

func runAdd(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	ctx := commandContext(cmd)

	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	result, err := overlay.AddFiles(ctx, appCtx.Config, args...)
	if err != nil {
		log.Error("staging files: failed", "err", err)
		return exitWith(1)
	}

	// Display errors for individual files
	for _, e := range result.Errors {
		log.Error("staging a file: failed", "err", e)
	}

	// Display success message if any files were added.
	// Show only what is staged in the index — not the whole working tree — so
	// "overlay add <pkg>" reports just the package(s) the user staged.
	if len(result.Added) > 0 {
		statuses, err := overlay.StagedStatus(ctx, appCtx.Config)
		if err != nil {
			log.Error("getting status: failed", "err", err)
			return exitWith(1)
		}
		// Plain text, by the same call runStatus documents at length: the library
		// composes what was staged, this command shows it, and nothing here
		// re-applies the colour overlay.FormatStatus used to decide for it.
		uiInfo(overlay.FormatStatus(statuses))
	}

	// Exit with error if there were any failures
	if result.HasErrors() {
		return exitWith(1)
	}
	return nil
}
