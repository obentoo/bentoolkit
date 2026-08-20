package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

// This file is `bentoo overlay staged clean`: the caller the staged-tree
// sweeper shipped without. The sweeper landed in 0.25.0 with no production
// caller at all, so every staged tree an `overlay validate --depth` or an
// `overlay autoupdate --apply` run left behind stayed there permanently.
//
// # Why a subcommand of its own and not a flag on the command that stages
//
// A sweeper hosted inside another operation inherits that operation's blind
// spots. Story 027 had to UNDO exactly that shape one level up: the overlay
// sweep once lived inside an apply's success path, so a package already at its
// upstream version could never be swept — no check produces a pending entry for
// it, so no apply happens, so no sweep happens. The residue this command
// removes is left precisely by runs that end without applying anything, so
// hanging it off an apply would reproduce that hole on purpose.
//
// The second reason is the dispatch rule at overlay_autoupdate.go:535-543. A
// case added to that switch above the two `--apply` cases converts every
// existing `--apply … --clean` invocation into a whole-root sweep. This command
// is not in that switch at all, so there is no ordering here to get wrong, and
// no existing invocation changes meaning (R6.2).
//
// # Why the word is `staged` and not `sweep`
//
// `sweep` and `--clean` already mean, on this overlay, A DELETION FROM A
// PUBLISHED TREE that auto-commits and pushes within minutes. What goes here is
// a scratch copy under the autoupdate config directory — the inverse risk
// profile — and a `--clean-staging` flag would have sat one keystroke from the
// flag that publishes deletions. `staging` was taken too: `overlay add` uses it
// in the git sense. `staged` names what is actually removed.
//
// # Where the operator-facing text goes
//
// STDOUT, through fmt and output/*, never through logger — the rule
// overlay_prune.go states, for the reason it states: logger binds os.Stderr
// once at first use, so a report split across two streams loses its ordering
// the moment either is redirected. Here the plan and the reason beside each
// entry are what an operator approves, so they have to arrive together.

// confirmStagedCleanFn is the confirmation seam the CLI tests drive, defaulting
// to the real prompt so a caller that supplies nothing gets production
// behaviour. Same shape as overlay_autoupdate_sweep.go:19-23 and for the same
// reason: a test has to be able to prove the executor was NOT REACHED, and that
// is only observable if reaching it goes through a replaceable name.
//
// It is not confirmSweepFn. That seam belongs to the overlay sweep, whose
// prompt covers a published deletion; one seam shared by two commands would let
// a test pinning either one answer for the other.
//
// The planner and the executor deliberately get NO seam. The tests drive the
// real ones over a real temporary staging root, because the fidelity that
// matters is that this command's idea of "a tree that may be removed" is the
// validate package's idea of it — and an inline fake plan would assert the
// opposite of that.
var confirmStagedCleanFn = confirmAction

// stagedCleanYes is this subcommand's own --yes, and it is deliberately not
// autoupdateYes.
//
// That variable is `overlay autoupdate --yes`, whose consent covers deletions
// from the published overlay. Sharing it would let one operator's --yes on one
// command stand in for consent on the other, and would leave a test driving it
// setting a flag the code under test never reads — passing while asserting the
// opposite of what it claims.
var stagedCleanYes bool

// newStagedCmd builds `overlay staged` and the `clean` subcommand under it.
//
// It is a constructor rather than a package-level var, following newValidateCmd
// (overlay_validate.go:60-63): a fresh command per call means one test's flag
// state can never survive into the next.
func newStagedCmd() *cobra.Command {
	staged := &cobra.Command{
		Use:   "staged",
		Short: "Work with the staged trees the build gates prepare outside the overlay",
		Long: `Inspect and clean up the staged trees that 'bentoo overlay autoupdate --apply'
and 'bentoo overlay validate --depth' prepare under
~/.config/bentoo/autoupdate/staging.

A staged tree is a scratch copy of the overlay, built outside it so that a gate
can compile a candidate without ever writing to the published tree. Nothing
under this command reads, writes or removes anything in the overlay itself.`,
	}
	staged.AddCommand(newStagedCleanCmd())
	return staged
}

// newStagedCleanCmd builds `overlay staged clean`.
func newStagedCleanCmd() *cobra.Command {
	clean := &cobra.Command{
		Use:   "clean",
		Short: "Remove the staged trees no longer worth keeping, and say what stays",
		Long: `Remove the staged copies left under ~/.config/bentoo/autoupdate/staging by
'bentoo overlay autoupdate --apply' and 'bentoo overlay validate --depth', and
report every tree left behind with the reason it stays.

The published overlay is never read, written or removed from. A staging root
that sits inside the overlay is refused before a single entry is listed, by the
same check that refuses to stage there in the first place.

A tree is removed only when this command RECOGNISES it as one of its own — it
carries the marker every staged tree is built with — and the record beside it
shows no deciding gate FAILED. Two kinds of tree are therefore kept and named
in the plan: the one whose gate FAILED, which is the artifact you still need
next to the retained build log, and the one with no readable record, whose
outcome is unknown. Keeping an unknown tree costs disk; removing it costs the
artifact somebody was about to open.

Nothing is removed before the whole plan has been printed and you have agreed
to it. Every removal and every keep is listed — never a sample.

There is no lock on the staging root, and that absence is deliberate: the
retention rule is expressed by the PATH, and a central index of proofs would put
back the one file every worker writes through. So a sweep running beside another
run can REMOVE a tree that run is using — an 'overlay autoupdate --apply' or an
'overlay validate --depth' happening at the same time. Nothing detects it. The
cost is bounded to that one run, which fails on a working directory that no
longer exists and is classified as an environment failure, so nothing wrong is
published; but it is a run you have to start again. Sweep when nothing else is
building.

Exit codes:
  0  the staging root was read and the plan reported
  1  the run could not be honoured: the config, the overlay path or the
     staging root could not be resolved, or the staging root could not be read

Examples:
  bentoo overlay staged clean         # print the plan, then ask
  bentoo overlay staged clean --yes   # remove the planned trees unattended`,
		Args: cobra.NoArgs,
		Run:  runStagedCleanCmd,
	}
	// The one thing a fresh command per call does NOT make fresh: --yes is bound
	// to a package variable, because the tests set consent directly rather than
	// through cobra's parser. Building a second command therefore rebinds the
	// same variable and resets it to false — so a test that pins stagedCleanYes
	// must do it AFTER the command it drives was built, which is what
	// setStagedCleanYes does by never building one.
	clean.Flags().BoolVar(&stagedCleanYes, "yes", false,
		"Remove the planned trees without asking (default: print the plan and ask)")
	return clean
}

func init() {
	overlayCmd.AddCommand(newStagedCmd())
}

// runStagedCleanCmd is the cobra half: signals, config, the staging root.
// Everything decidable without them lives in runStagedClean, which takes the
// overlay path and the staging root as parameters so the whole flow is drivable
// from a test — the same split runPrune and runSweep use.
//
// # Both resolutions refuse rather than fall back (R6.1)
//
// The staging root comes from autoupdateStagingRoot(), which is the ONE
// spelling of that path: the producers join it too, so a second derivation here
// would go stale in silence and report "nothing to sweep" over a staging root
// that is full. There is no default to fall back to when it cannot be resolved,
// and inventing one would name a directory nothing writes to.
//
// The overlay path is refused for a sharper reason. It is not a search path
// here — it is the SAFETY BOUNDARY the planner refuses against, and an empty
// one does not disable that check, it moves it: filepath.Abs("") resolves to
// the working directory, so a run that shrugged off a config failure would be
// asking "is the staging root inside whatever directory I was launched from".
// Refusing is the only answer that keeps the check meaning what it says.
func runStagedCleanCmd(cmd *cobra.Command, _ []string) {
	// SIGINT/SIGTERM reach the sweep through the context, so an interrupted run
	// stops between trees instead of finishing the batch first.
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	appCtx, err := loadAppContext()
	if err != nil {
		output.Error.Printf("  loading config: %v\n", err)
		osExit(1)
		return
	}

	stagingRoot, err := autoupdateStagingRoot()
	if err != nil {
		output.Error.Printf("  the staged trees could not be located: %v\n", err)
		osExit(1)
		return
	}

	runStagedClean(ctx, appCtx.OverlayPath, stagingRoot)
}

// runStagedClean plans what may leave the staging root, prints it, and — once
// consent is given — carries it out.
//
// # The order is the safety property
//
// Plan, print, confirm, execute, with the executor unreachable until a gate has
// passed. Producing the plan removes nothing at all (R1.1): PlanStagedSweep
// walks the root and classifies, and ExecuteStagedSweep is the only thing that
// acts. That is the same argument overlay_prune.go and
// overlay_autoupdate_sweep.go make, transposed one directory over.
//
// ctx is carried because the executor consults it before each removal, so an
// interrupted sweep leaves the tree it had not reached whole and reports it as
// a keep naming the interruption (R3.1).
//
// A standalone run passes an EMPTY InScope, and that is deliberate rather than
// an omission: InScope names what the CURRENT run still needs, and there is no
// current run here. What protects an operator is not a scope list but the three
// things the planner and this command already do — the tree must carry the
// recognition marker, a tree whose deciding gate FAILED is kept, and the whole
// plan is printed and agreed to before anything is removed.
func runStagedClean(ctx context.Context, overlayPath, stagingRoot string) {
	plan, err := validate.PlanStagedSweep(validate.SweepRequest{
		Overlay:     overlayPath,
		StagingRoot: stagingRoot,
	})
	if err != nil {
		// Reported verbatim: the refusal an operator reads has to be the one the
		// planner reached — "the staging root is inside the overlay" and "the
		// root could not be walked" are different problems with different fixes.
		output.Error.Printf("  the staged trees could not be planned: %v\n", err)
		osExit(1)
		return
	}

	printStagedCleanPlan(plan)

	// R1.3: there is nothing to confirm, so nothing is asked. "Remove 0 tree(s)?"
	// trains an operator to answer yes without reading, and the next prompt they
	// meet is one that removes something.
	//
	// The reason matters as much as the fact, because the two emptinesses are
	// different facts about the machine. "Nothing is staged here" is only true
	// when the walk found nothing at all; saying it after finding trees and
	// declining to touch them — because a deciding gate FAILED, or because no
	// record says what happened — contradicts the very list printed above it, and
	// leaves the operator concluding the staging root is empty when it is full of
	// artifacts they still need. S027-R7.1 made exactly this correction for the
	// overlay sweep; this is the same rule at a new command.
	//
	// Which of the two it is, is read from the plan's own partition rather than
	// from a second walk: Remove and Kept together cover everything the walk saw,
	// so an empty Kept beside an empty Remove means the walk saw nothing.
	if len(plan.Remove) == 0 {
		if len(plan.Kept) > 0 {
			output.Info.Println("  Nothing to remove: everything found under this root is protected — each entry above carries the reason it stays.")
		} else {
			output.Info.Println("  Nothing to remove: nothing is staged under this root.")
		}
		return
	}

	// THE ORDER IS THE GUARANTEE, and this early return is where it is spent.
	//
	// A declined run leaves here, and there is no path from this point to
	// ExecuteStagedSweep — so R2.1's "byte-identical" holds because the removal
	// path was never entered, not because the remover behaved well once inside
	// it. Handing consent to the executor as a parameter would move the guarantee
	// into the loop that deletes, where one wrong branch removes a tree; kept out
	// here, the worst a wrong branch can do is fail to remove one.
	if !confirmStagedClean(plan) {
		return
	}

	// The plan, unmodified, and this run's own context. ExecuteStagedSweep reads
	// plan.Remove and nothing else, so what is removed is what was displayed and
	// agreed to rather than a second verdict reached on a root that has moved
	// since (R1.4) — a tree staged between the printing and the answer is
	// removable by every rule in the policy and appears in no list the operator
	// saw. It consults ctx between trees, so a SIGINT lands with the next tree
	// still whole and reported as a keep naming the interruption.
	report := validate.ExecuteStagedSweep(ctx, plan)
	displayStagedCleanReport(report, len(plan.Remove))
}

// confirmStagedClean takes ONE confirmation covering the whole plan, and is the
// only thing between the plan printed above and the executor below.
//
// # The three gates, in order of how much they trust the caller
//
// --yes removes unattended, because the operator asked for exactly that in so
// many words — and it says so before acting. An unattended run has nobody
// watching, but it still leaves output somebody reads afterwards, and "why is
// this directory gone" has to be answerable from it (R2.3).
//
// An interactive terminal is asked. Anything else is REFUSED OUT LOUD, naming
// the flag that would have authorized it: a silent refusal is indistinguishable
// from a staging root with nothing in it, and the operator walks away believing
// their machine is clean when it is full of trees (R2.2). That is the same
// defect S027-R7.1 corrected one directory over, arriving by a different route.
//
// registryPromptIsInteractive is reused rather than re-derived, and it requires
// BOTH stdin and stdout to be terminals. Either half alone leaves a hole:
// confirmAction reads os.Stdin, so `yes | bentoo overlay staged clean` would
// answer for a human who is not there, and with stdout redirected the plan this
// consent is about reached nobody's eyes.
//
// ONE prompt for the whole plan, never one per tree. A question asked per tree is
// answered by reflex from the third one on, which turns a careful operator into a
// rubber stamp — and the line they stop reading is the one that was wrong.
func confirmStagedClean(plan validate.StagedSweepPlan) bool {
	if stagedCleanYes {
		output.Warning.Printf("  --yes given: removing %d staged tree(s) without a prompt.\n", len(plan.Remove))
		return true
	}
	if !registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing removed.")
		output.Info.Printf("  Re-run with --yes to remove these %d staged tree(s) unattended.\n", len(plan.Remove))
		return false
	}

	fmt.Println()
	// What is at stake, stated where the answer is given rather than left to the
	// help text. These are scratch copies outside the overlay, so this is not the
	// published deletion `overlay autoupdate --clean` asks about — but a tree
	// somebody was about to open is just as gone.
	output.Warning.Println("  These directories are deleted from disk. Nothing in the overlay is read, written or removed.")
	return confirmStagedCleanFn(fmt.Sprintf(
		"Remove %d staged tree(s) from %s?", len(plan.Remove), plan.StagingRoot))
}

// displayStagedCleanReport prints what actually happened, which is not the same
// document as the plan printed before the prompt.
//
// The plan is what was intended and agreed to; the report is what the staging
// root now holds. The two diverge in precisely the cases worth knowing about — a
// tree the sweep was interrupted before reaching, and one whose removal failed —
// and both arrive here as keeps whose reason names the cause. A summary alone
// would leave those facts to be inferred from a count that came out smaller,
// which is the inference an operator makes last and wrongly.
//
// planned is carried separately because the report cannot state it: an
// interrupted sweep's report and a completed sweep over a shorter plan are the
// same object, and only the number the operator approved tells them apart.
func displayStagedCleanReport(report validate.SweepReport, planned int) {
	fmt.Println()
	output.Header.Println("Staged Tree Clean Result")
	fmt.Println()

	if len(report.Removed) > 0 {
		fmt.Printf("  Removed (%d):\n", len(report.Removed))
		for _, path := range report.Removed {
			fmt.Printf("    %s\n", path)
		}
		fmt.Println()
	}

	if len(report.Kept) > 0 {
		fmt.Printf("  Kept — still on disk (%d):\n", len(report.Kept))
		for _, entry := range report.Kept {
			fmt.Printf("    %s\n", entry.Path)
			output.Info.Printf("      why: %s\n", stagedKeepReason(entry))
		}
		fmt.Println()
	}

	// The shortfall is never left as a bare count. A plan that did not complete
	// did so because the sweep was interrupted or because one removal failed, and
	// the reason beside each keep above is what says which — so the summary points
	// at them rather than restating a number the operator would have to subtract.
	if short := planned - len(report.Removed); short > 0 {
		output.Warning.Printf("  Removed %d of the %d tree(s) the plan named — the %d still there are listed above, "+
			"each with the reason it stayed.\n", len(report.Removed), planned, short)
		return
	}
	output.Success.Printf("  Removed %d of the %d tree(s) the plan named.\n", len(report.Removed), planned)
}

// printStagedCleanPlan prints the whole plan before anything is removed (R1.2).
//
// The full list, never a sample: the operator approves one batch on one
// keystroke, and a truncated list hides exactly the line that is wrong — the
// same argument displaySweepPlan makes one directory over, where the entries are
// ebuilds in a published tree instead of scratch copies outside it.
//
// Both lists are printed, not only the removals. A plan naming only what it will
// delete leaves no way to tell "this tree is protected" from "the sweep never
// saw it", and the second is the failure this whole story exists to correct: the
// sweeper shipped with no caller, so a staging root nothing had ever swept
// looked exactly like one that had just been cleaned.
//
// Neither list is capped, sorted again or grouped. The plan already sorts both,
// so two runs over one staging root print something an operator can diff.
func printStagedCleanPlan(plan validate.StagedSweepPlan) {
	fmt.Println()
	output.Header.Println("Staged Tree Clean")
	fmt.Println()
	output.Info.Printf("  Staging root: %s\n", plan.StagingRoot)
	fmt.Println()

	if len(plan.Remove) > 0 {
		// The heading states the removal criterion once instead of repeating it on
		// every line, and states all of it: a tree reaches this list only by being
		// recognised as this package's own AND carrying a record that parsed AND
		// showing no deciding gate FAILED. "No gate failed" alone would read as if an
		// unreadable record counted as a pass, which is the one reading the retention
		// policy refuses.
		fmt.Printf("  To remove (%d) — recognised as this tool's own, with a record showing no deciding gate FAILED:\n",
			len(plan.Remove))
		for _, path := range plan.Remove {
			fmt.Printf("    %s\n", path)
		}
		fmt.Println()
	}

	if len(plan.Kept) > 0 {
		fmt.Printf("  Kept — nothing below is touched (%d):\n", len(plan.Kept))
		for _, entry := range plan.Kept {
			fmt.Printf("    %s\n", entry.Path)
			output.Info.Printf("      why: %s\n", stagedKeepReason(entry))
		}
		fmt.Println()
	}
}

// stagedKeepReason is the reason printed beside one kept entry, and never a
// blank line.
//
// Every branch of the retention policy records a reason (R6.4), so the fallback
// is unreachable through today's planner. It is here because a path printed with
// nothing under it reads as "no reason was needed", which is the one thing an
// unexplained keep does not mean — the same choice keptLines makes for a version
// kept by no registry entry.
func stagedKeepReason(entry validate.SweptEntry) string {
	if reason := strings.TrimSpace(entry.Reason); reason != "" {
		return reason
	}
	return "no reason was recorded, which is itself a defect: treat this tree as unexplained rather than as safe to delete"
}
