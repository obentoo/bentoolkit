package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	pushDryRun bool
)

// newPushCmd builds `overlay push`.
func newPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "push",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Push committed changes to remote",
		Long:        `Push committed changes to the remote repository.`,
		RunE:        runPush,
	}
	cmd.Flags().BoolVarP(&pushDryRun, "dry-run", "n", false, "Show what would be pushed without pushing")
	return cmd
}

func runPush(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	ctx := commandContext(cmd)

	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	if pushDryRun {
		result, err := overlay.PushDryRun(ctx, appCtx.Config)
		if err != nil {
			log.Error("push dry-run: failed", "err", err)
			return exitWith(1)
		}
		uiInfo("Dry-run mode - would push:")
		uiInfo(result)
		return nil
	}

	result, err := overlay.Push(ctx, appCtx.Config)
	if err != nil {
		log.Error("pushing: failed", "err", err)
		return exitWith(1)
	}

	uiInfo(result.Message)
	return nil
}
