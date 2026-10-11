package main

import (
	"fmt"

	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/spf13/cobra"
)

// newRootCmd builds a complete, freshly wired `bentoo` command tree.
//
// Every command in the tree is produced by a constructor, so two calls share no
// command object and no flag state. That is the whole point: cobra
// keeps a flag's parsed value and its Changed bit ON the command, so a suite
// that drove one shared tree would let one case's --ui or --distdir survive into
// the next, and prove nothing about either.
//
// Registration follows one rule throughout the package: a parent's constructor
// registers its own children. newRootCmd registers overlay, snapshot, version
// and completion; newOverlayCmd and newSnapshotCmd register theirs.
//
// The three persistent flags are bound to LOCALS rather than to the package
// variables of the same name, because binding them to the package variables is
// exactly the sharing this function exists to remove — pflag writes through the
// pointer it is given, so both trees would read one bool. The values are copied
// into the package variables in PersistentPreRun instead, which runs before any
// Run and so is invisible to their readers (overlay_compare.go, which reads
// verbose, and overlay_autoupdate.go, which reads quiet).
func newRootCmd() *cobra.Command {
	return newRootCmdWith(defaultDeps())
}

// newRootCmdWith builds the same tree as newRootCmd with d as its dependencies.
// d reaches every constructor that owns a seam, so a test that substitutes a
// field of its own d changes this tree alone.
func newRootCmdWith(d *deps) *cobra.Command {
	var (
		verboseFlag bool
		quietFlag   bool
		noColorFlag bool
		// closeLog closes this invocation's log file. PersistentPreRunE sets
		// it; PersistentPostRunE calls it on a successful return.
		closeLog func()
	)

	root := &cobra.Command{
		Use:   "bentoo",
		Short: "Bentoo Linux tools",
		Long:  `A collection of tools for managing Bentoo Linux overlay and packages.`,
		// Set on the root and so covering all 30 commands, unlike the
		// SilenceUsage below: cobra consults the ROOT's copy of both fields
		// whichever command ran. This one stops cobra printing the error it
		// is about to return from Execute(), which func execute already prints —
		// with both printers live every refusal is stated twice, and it
		// must be stated exactly once. func execute is left the single owner.
		//
		// It is not the same decision as the one below, because the two
		// fields silence different things. SilenceUsage governs the usage
		// block, which an unknown flag still deserves; SilenceErrors governs
		// only the duplicated sentence. Setting THIS one on the root costs
		// the unknown-flag error nothing, which is why it can go where the
		// other one could not.
		SilenceErrors: true,
		// This is the only PersistentPreRun(E) in the tree, and the two
		// statements that open it depend on that: cobra runs the nearest hook
		// only, so a second one lower down would take both away from the
		// commands under it.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// cobra has validated flags and arguments by the time this runs,
			// so an unknown flag or a wrong argument count still prints the
			// usage block, while a bad --ui below and every handler failure
			// print none — the diagnostic alone, as when handlers ended the
			// process themselves.
			cmd.SilenceUsage = true
			// The command is selected now: from here on the first signal
			// cancels its context only if it declared itself cancellable. A
			// tree executed without func runMain carries no policy.
			applySignalPolicy(cmd)

			// Publish this tree's flag values to the package variables the run
			// functions read, before any of them runs.
			verbose, quiet, noColor = verboseFlag, quietFlag, noColorFlag
			// An unusable --ui stops ANY command before it does work,
			// naming the set it accepts. Here, once per invocation, rather than
			// inside each command that happens to render — a rejection that
			// only some commands perform is a rejection the operator has to
			// know the command list to predict.
			//
			// Only the FLAG is checked here, and deliberately so. ResolveMode
			// is called with nothing else populated, so it validates the value
			// and nothing about the environment or the config: a full
			// resolution at the root would make `bentoo version` fail on a host
			// whose config cannot be loaded, which is a regression for a
			// command that renders nothing. The full precedence chain still
			// runs where the report is produced, from ResolveMode UNCHANGED.
			if _, _, err := report.ResolveMode(report.ModeInputs{Flag: autoupdateUI}); err != nil {
				// Returned, not printed: cobra stops before RunE only if the
				// error travels back to it. Printing here and continuing would
				// deliver the message and lose the half that
				// matters — that NOTHING runs.
				return err
			}

			// The invocation's one logger, its level from
			// the flags and BENTOO_LOG_LEVEL, its JSON file opened here. The
			// file is closed by PersistentPostRunE on a successful return. cobra
			// skips the post-run hooks when RunE fails, and func execute still
			// logs a failWith cause after ExecuteContext returns, so on that
			// path execute closes it (func closeInvocationLog), and the
			// exitProcess cleanup covers a process that ends before either.
			closeLog = setUpInvocationLogger(cmd, d.resolvedSecrets, verboseFlag, quietFlag)

			if noColor {
				output.NoColor()
			}

			return nil
		},
		// The only PersistentPostRun(E) in the tree, for the same reason as the
		// pre-run hook above: cobra runs the nearest one only. It runs after a
		// successful RunE alone; a normal return never passes through
		// exitProcess, so this is where the log file is closed then.
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if closeLog != nil {
				closeLog()
			}
			return nil
		},
	}

	// Global flags
	root.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Enable verbose output: diagnostics at the debug level and above. Without --verbose or --quiet the level comes from the BENTOO_LOG_LEVEL environment variable, which accepts debug, info, warn or error in any letter case and defaults to info; --verbose and --quiet both outrank it, and --quiet outranks --verbose")
	root.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "Suppress non-error output")
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false, "Disable colored output")
	root.PersistentFlags().String(overlayFlagName, "", "Overlay to work on for this run, instead of overlay.path. Without it, a run started inside another checkout of the configured overlay (a worktree, a second clone) uses that checkout and says so")

	// These three bind straight to their package variables, unlike --verbose,
	// --quiet and --no-color above. Two reasons: pflag writes the default
	// through the pointer at construction, so a fresh tree resets them anyway
	// (the harness builds one per run); and a publish line naming autoupdateAll
	// would put that identifier outside a renderer Options, which
	// TestAllDoesNotChangeActions forbids — a display flag must not appear
	// where the run's actions are wired.
	//
	// The three report flags are declared ONCE, here, and are
	// honoured wherever a report exists. They were on `overlay autoupdate` alone,
	// which made every other report-producing command a command the operator had
	// to guess about. The help texts are carried across verbatim — they document
	// the precedence chain and are the user-facing contract.
	//
	// --no-tui deliberately stays on autoupdate. It is deprecated, it outranks
	// --ui, and giving every command a second opt-out competing with --ui would
	// be the opposite of retiring it (Unchanged Behavior 2).
	root.PersistentFlags().StringVar(&autoupdateUI, "ui", "", "Renderer for this run: auto, plain, inline or fullscreen. auto is the default and picks inline on a terminal and plain off one; it never picks fullscreen, because taking over somebody's screen is not something configuring nothing should do. plain contains no escape sequence at all and is what a pipe, a log file and a CI job want. This flag outranks the BENTOO_UI environment variable, which outranks the ui.mode configuration key. On the overlay autoupdate command alone, the deprecated opt-out flag documented there outranks all three; no other command has one. A value outside that set is rejected and NOTHING runs")
	root.PersistentFlags().BoolVar(&autoupdateAll, "all", false, "List every package found up to date in the version-check section instead of reporting them as a count alone. It changes WHAT IS SHOWN and nothing else — the same packages are scanned, validated and acted upon either way. The count is the default because the up-to-date packages are the bulk of a 269-package overlay, and listing them is most of the reason a check that found four updates used to print 348 lines")
	root.PersistentFlags().StringVar(&autoupdateExport, "export", "", "Also write the report to this path. The format follows the extension: .md is Markdown, .json is JSON, anything else is plain text. The file always carries the COMPLETE report — every package, every reason in full, nothing shortened — whatever --all and --ui asked of the terminal, because a report is saved precisely for when the terminal is gone. A path that cannot be written is reported, and the run still renders to the terminal and still exits with the status it would have had")

	root.AddCommand(newDistfileCmd())
	root.AddCommand(newNoticeCmd())
	root.AddCommand(newOverlayCmd(d))
	root.AddCommand(newSnapshotCmd(d))
	root.AddCommand(newVersionCmd())
	root.AddCommand(newCompletionCmd())

	return root
}

// newOverlayCmd builds `overlay` and registers every command under it, handing
// d to each child that reaches a seam.
func newOverlayCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "overlay",
		Short: "Manage the Bentoo overlay repository",
		Long:  `Commands for managing the Bentoo overlay repository including adding files, checking status, committing changes, and pushing to remote.`,
	}

	cmd.AddCommand(
		newAddCmd(),
		newAnalyzeCmd(),
		newAutoupdateCmd(d),
		newCommitCmd(),
		newCompareCmd(d),
		newDiffCmd(),
		newInitCmd(),
		newLogCmd(),
		newManifestCmd(d),
		newPruneCmd(d),
		newPullCmd(),
		newPushCmd(),
		newRenameCmd(),
		newStagedCmd(d),
		newStatusCmd(),
		newValidateCmd(d),
	)

	return cmd
}

// subCommand returns the direct sub-command of parent whose name is name.
//
// It PANICS when there is none. The tree is assembled at process start from
// constructors this package owns, so a miss can only mean one of them was left
// out of newRootCmd — a programming error, and one whose single useful moment
// to surface is immediately, loudly, in every environment. Returning nil would
// let production carry on holding a nil command and would reach a test as a nil
// dereference far from the cause.
func subCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, cmd := range parent.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	panic(fmt.Sprintf("bentoo: command %q has no sub-command %q — a constructor is missing from newRootCmd", parent.CommandPath(), name))
}

// The command variables below name the commands of the ONE production tree, the
// tree rootCmd holds. They are not separate commands: each is a lookup into
// rootCmd, so `overlayCmd.Commands()` still contains `addCmd` exactly as it did
// when every file registered its own global in an init().
//
// They exist so that the constructor conversion did not have to rewrite the
// package's existing tests in the same change. Nothing in production reads them
// — production goes through rootCmd — and a later change can delete this block
// once the tests it serves are moved onto the harness newRootCmd now makes
// possible. `overlay prune` has no variable here because nothing references one,
// and `overlay autoupdate` has none because its tests moved onto
// testAutoupdateCmd.
//
// Go orders these by dependency, not by line: rootCmd is built first, then the
// four below it, then their children.
var (
	distfileCmd   = subCommand(rootCmd, "distfile")
	overlayCmd    = subCommand(rootCmd, "overlay")
	snapshotCmd   = subCommand(rootCmd, "snapshot")
	versionCmd    = subCommand(rootCmd, "version")
	completionCmd = subCommand(rootCmd, "completion")

	addCmd      = subCommand(overlayCmd, "add")
	analyzeCmd  = subCommand(overlayCmd, "analyze")
	commitCmd   = subCommand(overlayCmd, "commit")
	compareCmd  = subCommand(overlayCmd, "compare")
	diffCmd     = subCommand(overlayCmd, "diff")
	initCmd     = subCommand(overlayCmd, "init")
	logCmd      = subCommand(overlayCmd, "log")
	manifestCmd = subCommand(overlayCmd, "manifest")
	pullCmd     = subCommand(overlayCmd, "pull")
	pushCmd     = subCommand(overlayCmd, "push")
	renameCmd   = subCommand(overlayCmd, "rename")
	statusCmd   = subCommand(overlayCmd, "status")

	distfileFetchCmd = subCommand(distfileCmd, "fetch")

	snapshotApplyCmd    = subCommand(snapshotCmd, "apply")
	snapshotHookCmd     = subCommand(snapshotCmd, "hook")
	snapshotListCmd     = subCommand(snapshotCmd, "list")
	snapshotPruneCmd    = subCommand(snapshotCmd, "prune")
	snapshotRestoreCmd  = subCommand(snapshotCmd, "restore")
	snapshotRollbackCmd = subCommand(snapshotCmd, "rollback")
	snapshotRunCmd      = subCommand(snapshotCmd, "run")
	snapshotStatusCmd   = subCommand(snapshotCmd, "status")
)
