package main

import (
	"fmt"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// This file is the command half of `--lint --fix`. internal/autoupdate/lintfix.go
// computes the repair and proves it inert; this shows it to a human and decides
// whether it may land.
//
// The split exists for one reason. packages.toml lives in an overlay that
// auto-commits and pushes, so a repair written unattended is a repair PUBLISHED
// unattended — a wrong rewrite is not a local mistake to undo before anyone
// notices, it is a released one. Hence R7.3: print the diff, gate the write, and
// only then let RepairResult.Write re-run the inertness gate and rename the file.
//
// The gates are story 021's, not a second set: the same --yes flag, the same
// registryPromptIsInteractive probe (stdin AND stdout must be terminals) and the
// same confirmRegistryWriteFn seam that guard the post-check version pins. Two
// idioms for "may I publish?" in one command is one too many.

// runLintFix is the --fix half of runLint. issues is what the lint above
// reported, already printed; it is carried here so the paths that write nothing
// can state what is still outstanding without re-reading a file they did not
// touch.
//
// It owns the exit code from here on, and the question that code answers is
// always the same: is anything still reported? 0 only when nothing is — so a
// declined repair, a refused unattended write and a repair that left the
// unrepairable findings behind all exit 1, and the pre-commit hook that runs
// --lint next agrees with what this run just said.
func runLintFix(overlayPath string, issues []autoupdate.LintIssue) {
	result, err := autoupdate.RepairPackagesConfig(overlayPath)
	if err != nil {
		// RepairPackagesConfig has written nothing: it returns an error only when
		// the repair ABORTED — the file could not be read or parsed, or the
		// rewrite failed the inertness gate. Both mean report it and change
		// nothing; neither is a condition to retry around.
		logger.Error("failed to repair packages.toml: %v", err)
		output.Error.Println("  packages.toml was NOT modified.")
		osExit(1)
		return
	}

	if !result.Changed {
		// A repair with nothing to write must not ask. "Confirm writing 0
		// changes?" is exactly the question that teaches an operator to answer
		// yes without reading, and the one prompt here that has to survive that
		// habit is the one that publishes.
		//
		// The findings are NOT reprinted here: runLint listed every one of them
		// moments ago and nothing has been written since, so a second copy would
		// be the same lines twice in one screen. One sentence ties the two facts
		// together instead — nothing was repairable, and what remains is why.
		summarizeUnrepaired(issues)
		return
	}

	diff, err := repairDiff(result)
	if err != nil {
		// No diff means no review, and no review means no write: this gate exists
		// to stop a change nobody has seen from being published.
		logger.Error("failed to render the repair as a diff: %v", err)
		output.Error.Println("  packages.toml was NOT modified.")
		osExit(1)
		return
	}

	// The diff comes BEFORE the question, always. A confirmation for a change
	// nobody has seen does not gather consent, it manufactures it.
	fmt.Println()
	output.Header.Println("Registry Repair")
	fmt.Println()
	printRepairDiff(diff)
	fmt.Println()
	printRepairSummary(result)

	if !confirmLintRepair(result) {
		// Return WITHOUT calling Write: the file is never opened, so it stays
		// byte-identical by construction rather than by care. Every finding the
		// lint reported is still there, so the exit code still says so.
		output.Warning.Println("  packages.toml is unchanged.")
		osExit(1)
		return
	}

	if err := result.Write(); err != nil {
		// Write re-runs the inertness gate before the rename, so this covers both
		// "the filesystem refused" and "the rewrite stopped being provably inert
		// between the diff and now". Either way the registry is untouched.
		logger.Error("failed to write the repaired packages.toml: %v", err)
		output.Error.Printf("  The registry was NOT repaired: %v\n", err)
		osExit(1)
		return
	}

	output.Success.Printf("  Repaired packages.toml: %d change(s) written.\n", totalRepairs(result))
	output.Info.Println("  Review it before it is published: 'bentoo overlay diff'")

	// Re-lint rather than subtract the repaired rules from the list above: only
	// the file on disk can say whether the repair did what it claimed, and this
	// is the run's own proof that `--lint --fix` followed by `--lint` is silent
	// except for the findings no repair offers.
	remaining, err := autoupdate.LintPackagesConfig(overlayPath)
	if err != nil {
		// Unreachable for a repair that passed the gate — it parses the rewrite
		// before allowing it — so if it fires, the write is the suspect.
		logger.Error("packages.toml was repaired but no longer lints: %v", err)
		osExit(1)
		return
	}
	reportUnrepaired(remaining)
}

// reportUnrepaired states what the registry says NOW and sets the exit code from
// it. It is the AFTER-A-WRITE report: `remaining` comes from a fresh lint of the
// rewritten file, so it can differ from anything printed earlier and is listed in
// full.
//
// The distinction it draws is the point: `--fix` deliberately declines to guess
// at some findings — an entry tracking commits with no base source cannot be
// repaired without knowing where upstream versions itself (R6.1) — so a run that
// ended on "repaired!" while those remain would imply a clean registry that the
// next --lint will contradict.
func reportUnrepaired(remaining []autoupdate.LintIssue) {
	if len(remaining) == 0 {
		output.Success.Println("packages.toml: record model OK")
		return
	}

	output.Warning.Printf("  %d finding(s) still need a human — --fix does not guess at them:\n", len(remaining))
	for _, issue := range remaining {
		output.Warning.Println("    " + issue.String())
	}
	logger.Error("packages.toml: %d issue(s) remain", len(remaining))
	osExit(1)
}

// summarizeUnrepaired is the NOTHING-WAS-WRITTEN report: the findings are the
// ones runLint just listed, so it states the verdict in one line instead of
// printing them again.
//
// The two facts belong in one sentence. "Nothing to repair" followed by "N
// issues remain" reads as a contradiction to anyone who does not already know
// that a rule may carry no repair; said together, the second explains the first.
func summarizeUnrepaired(remaining []autoupdate.LintIssue) {
	if len(remaining) == 0 {
		output.Success.Println("packages.toml: record model OK")
		return
	}

	output.Warning.Printf(
		"  Nothing to repair: the %d finding(s) above have no mechanical fix — --fix does not guess at them.\n",
		len(remaining))
	logger.Error("packages.toml: %d issue(s) remain", len(remaining))
	osExit(1)
}

// confirmLintRepair is the write gate (R7.3): three gates, in order of how much
// they trust the caller. --yes writes unattended because the operator asked for
// that in so many words; an interactive terminal is asked; anything else has
// printed the diff and writes nothing.
//
// It reports whether the repair may be written, and prints WHY whenever the
// answer is no — a run that silently declines to write is indistinguishable from
// one that wrote and failed to say so.
func confirmLintRepair(result *autoupdate.RepairResult) bool {
	repairs := totalRepairs(result)

	if autoupdateYes {
		// An explicit, in-so-many-words approval. Stdin is never read on this
		// path, so it works from a pipe, a cron job or a CI step.
		output.Warning.Printf("  --yes given: writing %d repair(s) without a prompt.\n", repairs)
		return true
	}
	if !registryPromptIsInteractive() {
		// The diff above IS the report; this run writes nothing. Prompting here
		// would ask a pipe for consent — `yes | bentoo …` would publish.
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing written.")
		output.Info.Printf("  Re-run with --yes to write these %d repair(s) unattended.\n", repairs)
		return false
	}

	// ONE question covering the whole diff, not one per record: the operator is
	// approving the rewrite they just read.
	fmt.Println()
	return confirmRegistryWriteFn(fmt.Sprintf(
		"Write %d repair(s) to packages.toml? (the diff above is the whole change)", repairs))
}

// printRepairSummary states what the repair does in the same vocabulary the lint
// report uses — the Fix* identifiers LintIssue.Fix carries — so the tally the
// linter printed and the tally about to be written are read side by side. It
// closes with the reason the confirmation below is not ceremony.
func printRepairSummary(result *autoupdate.RepairResult) {
	actions := make([]string, 0, len(result.Actions))
	for name := range result.Actions {
		actions = append(actions, name)
	}
	sort.Strings(actions)

	output.Warning.Printf("  About to rewrite packages.toml with %d repair(s):\n", totalRepairs(result))
	for _, name := range actions {
		fmt.Printf("    %-26s %d\n", name, result.Actions[name])
	}
	output.Warning.Println("  packages.toml is PUBLISHED: this overlay auto-commits and pushes, so this write reaches origin.")
}

// totalRepairs is how many individual repairs the rewrite applies, summed over
// the actions.
//
// It counts ACTIONS, not records, and the two genuinely differ: one record can
// carry both a dropped `binary` line and a reorder. Reporting a record count
// derived from this sum would overstate the blast radius; the per-action
// breakdown above says which is which.
func totalRepairs(result *autoupdate.RepairResult) int {
	n := 0
	for _, count := range result.Actions {
		n += count
	}
	return n
}

// repairDiff renders the repair as a unified diff — the format every reviewer
// already reads, and the one `patch` and `git apply` consume.
//
// It goes through udiff.ToUnified rather than the one-call udiff.Unified because
// that helper answers an inconsistent edit list with log.Fatalf: a diff that
// could not be rendered would kill the process from inside a library, on the one
// path whose whole job is to show a human what is about to be published. Here it
// is an error, and an error means write nothing.
func repairDiff(result *autoupdate.RepairResult) (string, error) {
	edits := udiff.Lines(result.Original, result.Repaired)
	diff, err := udiff.ToUnified("a/packages.toml", "b/packages.toml", result.Original, edits, udiff.DefaultContextLines)
	if err != nil {
		return "", fmt.Errorf("rendering the repair of %s as a unified diff: %w", result.Path, err)
	}
	return diff, nil
}

// printRepairDiff writes the diff with its +/- lines coloured the way every diff
// a maintainer reads is coloured. Only the colour is added: the text is
// untouched, so the output is still a valid patch when colour is off (a pipe,
// NO_COLOR, a redirected stdout).
func printRepairDiff(diff string) {
	if diff == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		// The file headers are checked before the +/- lines they start with.
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			output.Dim.Println(line)
		case strings.HasPrefix(line, "@@"):
			output.Info.Println(line)
		case strings.HasPrefix(line, "+"):
			output.Added.Println(line)
		case strings.HasPrefix(line, "-"):
			output.Deleted.Println(line)
		default:
			fmt.Println(line)
		}
	}
}
