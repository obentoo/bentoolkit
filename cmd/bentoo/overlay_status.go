package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

// newStatusCmd builds `overlay status`.
func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "status",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Show the status of changes in the overlay",
		Long:        `Display the current status of changes in the overlay repository, grouped by category/package.`,
		RunE:        runStatus,
	}
	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	ctx := commandContext(cmd)

	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	statuses, err := overlay.Status(ctx, appCtx.Config)
	if err != nil {
		log.Error("reading the overlay status: failed", "err", err)
		return exitWith(1)
	}

	// The library composes the facts; THIS is where they are shown, and the
	// presentation chosen here is deliberately minimal.
	//
	// overlay.FormatStatus used to return the lines already coloured, so the
	// package that read git had decided how a terminal would look — text that
	// could then go to a terminal and nowhere else. It returns plain text now.
	// Nothing re-applies the colour here: the report envelope renders every mode
	// from one place, and a second styled renderer outside it would be a second
	// thing to migrate and a second place for the wording to drift.
	//
	// The text is unchanged, byte for byte, against what this printed off a TTY —
	// which is every pipe, log and CI run.
	uiInfo(overlay.FormatStatus(statuses))
	return nil
}
