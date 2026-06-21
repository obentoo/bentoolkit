package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/tui"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

// ManifestFlags holds command-line flags for the manifest regeneration command.
type ManifestFlags struct {
	Keep           bool   // --keep: do not remove existing Manifest before pkgdev runs
	DryRun         bool   // --dry-run: list packages without invoking pkgdev
	Distdir        string // --distdir: pkgdev distfiles directory (persistent when set)
	Jobs           int    // --jobs: maximum number of parallel pkgdev workers
	DistfilesCache string // --distfiles-cache: read-only cache consulted before downloads ("" disables)
}

var manifestFlags ManifestFlags

var manifestCmd = &cobra.Command{
	Use:   "manifest [<category> | <category>/<package>]",
	Short: "Regenerate Manifest files for overlay packages",
	Long: `Regenerate Manifest files for one or more packages in the overlay.

By default, the existing Manifest is moved aside before pkgdev runs so a
fresh file is produced (clean regeneration). The backup is restored
automatically if pkgdev fails. Use --keep to skip the clean step and let
pkgdev reconcile the existing Manifest.

Workers are dispatched in parallel up to --jobs (default 10), so larger
overlays regenerate much faster. When stdout is a terminal, a live block
shows one slot per active worker plus a global progress bar; finished
packages scroll above as ✓/✗ history. Outside a TTY (CI, pipes), output
falls back to plain start/finish log lines.

Scope is selected by the optional argument:
  (no argument)            All packages in the overlay
  <category>               All packages in the given category
  <category>/<package>     Only the given package

The command runs as the current user — by default pkgdev is invoked with a
temporary distdir that is discarded after the run, so no sudo is required.
Pass --distdir to use a persistent path instead (created if missing); this
acts as a download cache reused across runs.

Before each package is processed, distfiles already present in
--distfiles-cache (default /var/cache/distfiles) are symlinked into the
working distdir so pkgdev can validate them locally instead of downloading
again. The cache is opened read-only — nothing is ever written back. Pass
--distfiles-cache "" to disable, or point to another directory.

Examples:
  # Regenerate every Manifest in the overlay (10 in parallel)
  bentoo overlay manifest

  # Regenerate every package in app-editors
  bentoo overlay manifest app-editors

  # Regenerate a single package
  bentoo overlay manifest app-editors/zed

  # Limit parallelism (e.g. for low-bandwidth links)
  bentoo overlay manifest --jobs 2

  # Preview without running pkgdev
  bentoo overlay manifest --dry-run app-editors

  # Skip the clean-regen step (keep existing Manifest in place)
  bentoo overlay manifest --keep app-editors/zed

  # Cache distfiles in a persistent directory
  bentoo overlay manifest --distdir ~/.cache/bentoo/distfiles

  # Disable the system distfiles cache lookup
  bentoo overlay manifest --distfiles-cache ""`,
	Args: cobra.MaximumNArgs(1),
	Run:  runManifest,
}

func init() {
	manifestCmd.Flags().BoolVar(&manifestFlags.Keep, "keep", false, "Keep existing Manifest in place (skip clean regen)")
	manifestCmd.Flags().BoolVarP(&manifestFlags.DryRun, "dry-run", "n", false, "Show what would be processed without running pkgdev")
	manifestCmd.Flags().StringVar(&manifestFlags.Distdir, "distdir", "", "Distfiles directory used by pkgdev (default: temporary directory removed after run)")
	manifestCmd.Flags().IntVarP(&manifestFlags.Jobs, "jobs", "j", overlay.DefaultManifestJobs, "Maximum parallel pkgdev workers")
	manifestCmd.Flags().StringVar(&manifestFlags.DistfilesCache, "distfiles-cache", overlay.DefaultDistfilesCache, "Read-only distfiles cache consulted before download (\"\" disables)")
	overlayCmd.AddCommand(manifestCmd)
}

func runManifest(cmd *cobra.Command, args []string) {
	arg := ""
	if len(args) == 1 {
		arg = args[0]
	}

	scope, err := overlay.ParseManifestScope(arg)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	targets, err := overlay.ResolveManifestTargets(ctx.OverlayPath, scope)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	// Wire SIGINT/SIGTERM to a context so an in-flight run cancels cleanly:
	// pkgdev sub-processes inherit the cancellation through exec.CommandContext.
	runCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Emit the lead-in line BEFORE building the reporter: once the live TUI
	// program is running it owns the terminal, so direct logger writes would
	// race with its rendering.
	logger.Info("Regenerating Manifest for %d package(s)", len(targets))

	reporter, finishUI := chooseManifestReporter(manifestFlags.DryRun, runCtx, cancel)

	opts := &overlay.ManifestOptions{
		Keep:           manifestFlags.Keep,
		DryRun:         manifestFlags.DryRun,
		Distdir:        manifestFlags.Distdir,
		Jobs:           manifestFlags.Jobs,
		DistfilesCache: manifestFlags.DistfilesCache,
		Reporter:       reporter,
		Ctx:            runCtx,
	}

	updates := overlay.RegenerateManifests(ctx.OverlayPath, targets, opts)
	result := &overlay.ManifestResult{Updates: updates}

	// Tear the UI down (stop the program, restore the terminal) before any
	// further logging or exit so the summary is not swallowed by the TUI.
	finishUI()

	logger.Info("%s", overlay.FormatManifestResult(result, opts.DryRun))

	if opts.DryRun {
		return
	}
	for _, u := range updates {
		if !u.Success {
			osExit(1)
			return
		}
	}
}

// chooseManifestReporter picks the reporter for the current run: the live TUI
// when interactive (AD7 gate via tui.Enabled), a plain ANSI-free reporter
// otherwise. Dry-run skips the reporter entirely since there are no pkgdev
// invocations to track. The returned func tears the UI down and must be called
// before any post-run logging or exit; for the non-TUI paths it is a no-op.
func chooseManifestReporter(dryRun bool, ctx context.Context, cancel context.CancelFunc) (tui.Reporter, func()) {
	if dryRun {
		return tui.Noop(), func() {}
	}
	if tui.Enabled(tui.Options{}) {
		prog, r := tui.New(ctx, cancel, os.Stdout, os.Stdin)
		prog.Start()
		return r, func() { prog.Stop(); _ = prog.Wait() }
	}
	return tui.NewPlainReporter(os.Stderr, time.Second), func() {}
}
