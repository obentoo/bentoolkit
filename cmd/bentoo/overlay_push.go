package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
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
	ctx := commandContext(cmd)

	appCtx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		return exitWith(1)
	}

	if pushDryRun {
		result, err := overlay.PushDryRun(ctx, appCtx.Config)
		if err != nil {
			logger.Error("%v", err)
			return exitWith(1)
		}
		logger.Info("Dry-run mode - would push:")
		logger.Info("%s", result)
		return nil
	}

	result, err := overlay.Push(ctx, appCtx.Config)
	if err != nil {
		logger.Error("%v", err)
		return exitWith(1)
	}

	logger.Info("%s", result.Message)
	return nil
}
