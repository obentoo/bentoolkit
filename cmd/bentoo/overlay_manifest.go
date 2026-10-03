package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
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

// newManifestCmd builds `overlay manifest`.
func newManifestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "manifest [<category> | <category>/<package>]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Regenerate Manifest files for overlay packages",
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
		RunE: runManifest,
	}
	cmd.Flags().BoolVar(&manifestFlags.Keep, "keep", false, "Keep existing Manifest in place (skip clean regen)")
	cmd.Flags().BoolVarP(&manifestFlags.DryRun, "dry-run", "n", false, "Show what would be processed without running pkgdev")
	cmd.Flags().StringVar(&manifestFlags.Distdir, "distdir", "", "Distfiles directory used by pkgdev (default: temporary directory removed after run)")
	cmd.Flags().IntVarP(&manifestFlags.Jobs, "jobs", "j", overlay.DefaultManifestJobs, "Maximum parallel pkgdev workers")
	cmd.Flags().StringVar(&manifestFlags.DistfilesCache, "distfiles-cache", overlay.DefaultDistfilesCache, "Read-only distfiles cache consulted before download (\"\" disables)")
	return cmd
}

func runManifest(cmd *cobra.Command, args []string) error {
	arg := ""
	if len(args) == 1 {
		arg = args[0]
	}

	scope, err := overlay.ParseManifestScope(arg)
	if err != nil {
		logger.Error("%v", err)
		return exitWith(1)
	}

	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		return exitWith(1)
	}

	targets, err := overlay.ResolveManifestTargets(ctx.OverlayPath, scope)
	if err != nil {
		logger.Error("%v", err)
		return exitWith(1)
	}

	// The process-wide context (func commandContext): overlay manifest is
	// annotated cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels
	// the run. pkgdev runs in its own process group and receives no terminal
	// signal, so this context, handed to exec.CommandContext by
	// overlay.RegenerateManifests, is what stops it (story 054, R4.4). The
	// child context exists only for its cancel, which func
	// chooseManifestReporter hands to the live TUI: the TUI reads its own
	// interrupt key from a raw terminal, where no SIGINT is raised.
	runCtx, cancel := context.WithCancel(commandContext(cmd))
	defer cancel()

	// Emit the lead-in line BEFORE building the reporter: once the live TUI
	// program is running it owns the terminal, so direct logger writes would
	// race with its rendering.
	logger.Info("Regenerating Manifest for %d package(s)", len(targets))

	reporter, finishUI := chooseManifestReporter(ctx.Config, manifestFlags.DryRun, runCtx, cancel)

	opts := &overlay.ManifestOptions{
		Keep:           manifestFlags.Keep,
		DryRun:         manifestFlags.DryRun,
		Distdir:        manifestFlags.Distdir,
		Jobs:           manifestFlags.Jobs,
		DistfilesCache: manifestFlags.DistfilesCache,
		Reporter:       reporter,
		Summary:        manifestLiveSummary,
	}

	result := overlay.RegenerateManifests(runCtx, ctx.OverlayPath, targets, opts)

	// Tear the UI down (stop the program, restore the terminal) before any
	// further logging or exit so the summary is not swallowed by the TUI.
	finishUI()

	// The run ends in a report, and it ends in exactly one (S046-R1.1).
	//
	// What stood here was logger.Info over overlay.FormatManifestResult — the
	// same facts, formatted by the library, on stderr, in one mode, exportable
	// by nothing. That sentence is what story 046 replaces: the counts are
	// values now (ManifestResult.Ok/Failed), the report is assembled whole
	// before any of it is displayed (R1.3), and the same value is rendered in
	// the mode the run resolved and written to --export if one was named.
	//
	// It is not printed AS WELL. Two statements of one run's outcome, in two
	// voices on two streams, is the defect rather than a safety net: an
	// operator would read the list of targets twice and have no way to tell
	// which of the two was authoritative the day they disagreed.
	//
	// AFTER finishUI() and not one line before it. The live region owns the
	// terminal until the program is stopped, and the report writes to that same
	// stdout — rendering first would draw it into a frame the TUI then redraws
	// over. This is the point in the run where the terminal has been handed
	// back, so it is the first point the report may be drawn.
	presentManifestReport(ctx.Config, buildManifestReport(&result, opts.DryRun))

	if opts.DryRun {
		return nil
	}

	// An interrupted run does not exit 0, and saying so explicitly is what KEEPS
	// today's behaviour rather than changing it. Until this sub-task, a
	// cancelled run drained its queue against a dead context and came back with
	// every remaining target marked failed, so the loop below always found one
	// and the status was 1. Those fabricated failures are gone — that is the
	// point of R1.4 — and without this line their disappearance would silently
	// turn a ctrl+c into a success for any script reading the status.
	//
	// It is checked BEFORE the rows, not instead of them: the two answer
	// different questions, and the first one to say "not a clean run" wins.
	if result.Interrupted {
		return exitWith(1)
	}
	for _, u := range result.Updates {
		if !u.Success {
			return exitWith(1)
		}
	}
	return nil
}

// chooseManifestReporter picks the reporter for the current run: the live TUI
// when the resolved UI mode draws one, a plain ANSI-free reporter otherwise.
//
// The gate is manifestUsesTUI (S044-R3.8), NOT tui.Enabled. This command used
// to decide for itself; it now inherits the same ui.mode / BENTOO_UI answer
// `autoupdate` resolves, so one setting governs both commands. For an operator
// who configured no ui.mode the answer is unchanged — inline on a terminal,
// plain off one, and plain under any of the three opt-outs, which is exactly
// what tui.Enabled returned here before (R3.7).
//
// Dry-run skips the reporter entirely since there are no pkgdev invocations to
// track. The returned func tears the UI down and must be called before any
// post-run logging or exit; for the non-TUI paths it is a no-op.
func chooseManifestReporter(cfg *config.Config, dryRun bool, ctx context.Context, cancel context.CancelFunc) (tui.Reporter, func()) {
	if dryRun {
		return tui.Noop(), func() {}
	}
	if manifestUsesTUI(cfg) {
		prog, r := tui.New(ctx, cancel, os.Stdout, os.Stdin)
		prog.Start()
		return r, func() { prog.Stop(); _ = prog.Wait() }
	}
	return tui.NewPlainReporter(os.Stderr, time.Second), func() {}
}

// manifestLiveSummary is the sentence the live region ends on: the two counts a
// regeneration run established, in the words THIS layer chose (S046-R5.2,
// design.md D5).
//
// # It is here because choosing words is the command's job
//
// overlay.RegenerateManifests used to hold this format string, which made the
// end of a run a report squeezed through a progress channel: text, composed
// inside a package that has no business composing any, that nothing downstream
// could count, export, shorten or draw a second time in another mode. The
// producer returns its facts now and this function turns them into a sentence,
// so a run's numbers exist as values first and as wording second.
//
// # It reads the SAME value the report is built from
//
// Its argument is the ManifestResult that reaches buildManifestReport a few
// lines later, and Ok and Failed are derived from that value's own rows. The
// live region's last line and the report drawn under it therefore cannot
// disagree about how the run went — which was the whole of the argument the
// library made for keeping the sentence, kept intact and carried across the
// boundary rather than lost with it.
//
// # From the result, not from the built payload
//
// D5 words the destination as the payload, and the counts are identical either
// way: report.ManifestRun's Ok and Failed are set from these very two calls.
// Building a payload to read them would copy one row per target, which is a
// whole-overlay target list allocated to produce two integers and then thrown
// away. The preview case settles it — a --dry-run returns before the run opens
// a batch, so this is never called for one, and the DryRun flag that only the
// payload carries has nothing to answer for here.
func manifestLiveSummary(result overlay.ManifestResult) string {
	return fmt.Sprintf("%d ok, %d failed", result.Ok(), result.Failed())
}
