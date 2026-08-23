package main

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// This file is the command half of `--mark-auto-disabled`, the one-shot
// migration story 043 R1.5 asks for. internal/autoupdate/disable_origin_migration.go
// decides what qualifies and writes it; this shows a human the decision and
// gates the write.
//
// The split is the one `--lint --fix` already uses, and for the same reason.
// packages.toml lives in an overlay that auto-commits and pushes, so a write
// made unattended is a write PUBLISHED unattended — a wrong migration is not a
// local mistake to undo before anyone notices, it is a released one. Hence:
// print the whole plan, gate the write, and only then stamp.
//
// The gates are story 021's, not a third set: the same --yes flag, the same
// registryPromptIsInteractive probe (stdin AND stdout must be terminals) and the
// same confirmRegistryWriteFn seam that guard the post-check version pins and
// the lint repair. Three idioms for "may I publish?" in one command would be two
// too many.
//
// WHY THIS RUNS AT ALL. R1.3 made an absent `disabled_by` mean "a human decided,
// leave it alone" — the fail-safe direction, and the one that protects
// dev-libs/icu-compat and media-libs/libjxl-compat the instant the code lands.
// Its price is that the ~90 entries the checker auto-disabled BEFORE the field
// existed look identical on disk to those two pins, so they stop reconciling
// too. This command pays that price once and then has nothing left to do.

// runMarkAutoDisabled handles --mark-auto-disabled: it plans the migration over
// the parsed registry, prints the whole plan, and stamps `disabled_by = "auto"`
// onto the qualifying entries behind one confirmation.
//
// It owns the exit code, and that code answers one question: is the registry now
// in the state the operator asked for? 0 only when it is — so a declined
// migration, a refused unattended write, an exclusion list with a typo in it and
// a plan that did not fully land all exit 1.
func runMarkAutoDisabled(overlayPath string) {
	plan, err := autoupdate.PlanAutoDisableMigration(overlayPath, autoupdateExcept)
	if err != nil {
		// Nothing has been written: the plan only reads. A registry that does not
		// load cannot be migrated either, and failing here is the cheapest place
		// to say so.
		logger.Error("failed to plan the disable-origin migration: %v", err)
		output.Error.Println("  packages.toml was NOT modified.")
		osExit(1)
		return
	}

	fmt.Println()
	output.Header.Println("Registry Migration — record the automatic disable origin")
	fmt.Println()
	printAutoDisableMigrationPlan(plan)

	// The typo check comes BEFORE anything else can happen, because it is the
	// one error whose consequence is silent. An --except entry naming no record
	// protected nothing, and the entry it was meant to protect is sitting in the
	// list above waiting to be stamped — after which the reconciliation is free
	// to re-enable and bump the very pin that exists to prevent that.
	if len(plan.UnmatchedExcept) > 0 {
		output.Error.Println("  --except names entries that are not in packages.toml, so they protect nothing:")
		for _, pkg := range plan.UnmatchedExcept {
			output.Error.Println("    " + pkg)
		}
		logger.Error("refusing to migrate: %d --except entry(ies) match no record — fix the spelling and re-run", len(plan.UnmatchedExcept))
		output.Error.Println("  packages.toml was NOT modified.")
		osExit(1)
		return
	}

	if len(plan.Mark) == 0 {
		// A migration with nothing to write must not ask. "Confirm writing 0
		// changes?" is exactly the question that teaches an operator to answer
		// yes without reading, and the one prompt here that has to survive that
		// habit is the one that publishes.
		output.Success.Println("  Nothing to migrate: every disabled entry already states its origin, is held, or was excluded.")
		return
	}

	if !confirmAutoDisableMigration(plan) {
		// Return WITHOUT calling MarkAutoDisabled: the file is never opened, so
		// it stays byte-identical by construction rather than by care. The
		// entries above are still frozen, so the exit code still says so.
		output.Warning.Println("  packages.toml is unchanged.")
		osExit(1)
		return
	}

	marked, err := autoupdate.MarkAutoDisabled(overlayPath, autoupdateExcept)
	if err != nil {
		// The write is atomic, so this means the registry is exactly as it was.
		logger.Error("failed to record the automatic origin: %v", err)
		output.Error.Printf("  packages.toml was NOT modified: %v\n", err)
		osExit(1)
		return
	}

	output.Success.Printf("  Stamped %d entry(ies) with disabled_by = \"auto\":\n", len(marked))
	for _, pkg := range marked {
		fmt.Println("    " + pkg)
	}
	output.Info.Println("  Review it before it is published: 'bentoo overlay diff'")

	// The plan and the write are separate reads of the same file, and only the
	// second one touched it. A shortfall means a record the plan selected has an
	// `enabled` assignment the surgical editor could not reach — a shape worth a
	// human's eye, and one a bare success line would hide behind a smaller count
	// nobody was going to compare.
	if len(marked) != len(plan.Mark) {
		output.Warning.Printf("  %d of the %d planned entry(ies) were NOT stamped: their enabled assignment was not where the editor could rewrite it.\n",
			len(plan.Mark)-len(marked), len(plan.Mark))
		logger.Error("the migration is incomplete: %d entry(ies) remain without an origin", len(plan.Mark)-len(marked))
		osExit(1)
		return
	}
}

// printAutoDisableMigrationPlan lists the whole decision, group by group, in
// full.
//
// Nothing is elided. This command runs ONCE against a 411-record registry and
// its output IS the audit record of what was done to a published file, so the
// groups that change NOTHING carry as much of the argument as the one that does:
// they are the evidence that a deliberate pin, a held package and an
// already-migrated entry were each recognised as such rather than missed. A
// summary reading "N stamped" cannot be checked against anything.
//
// Enabled records are deliberately absent — some 320 of them state no origin
// because they have no disable to explain, and listing them would bury the five
// lines that matter.
func printAutoDisableMigrationPlan(plan *autoupdate.AutoDisableMigration) {
	printAutoDisableGroup(output.Warning, fmt.Sprintf("%d entry(ies) will be stamped disabled_by = \"auto\" — the checker's own bookkeeping, now free to reconcile:", len(plan.Mark)), plan.Mark)
	printAutoDisableGroup(output.Info, fmt.Sprintf("%d excluded by --except — deliberate pins, left stating no origin so no scan may revoke them:", len(plan.Excluded)), plan.Excluded)
	printAutoDisableGroup(output.Info, fmt.Sprintf("%d already stamped — left as they are, which is what makes a re-run a no-op:", len(plan.AlreadyStamped)), plan.AlreadyStamped)
	printAutoDisableGroup(output.Info, fmt.Sprintf("%d held — hold already excludes them, and stamping one would claim a disable the checker never wrote:", len(plan.Held)), plan.Held)
	fmt.Println()
}

// printAutoDisableGroup prints one group of the plan, or nothing at all when the
// group is empty: a heading followed by no entries states a count of zero twice
// and pushes the groups that matter off the screen.
func printAutoDisableGroup(style *color.Color, heading string, pkgs []string) {
	if len(pkgs) == 0 {
		return
	}
	style.Println("  " + heading)
	for _, pkg := range pkgs {
		fmt.Println("    " + pkg)
	}
}

// confirmAutoDisableMigration is the write gate: three gates, in order of how
// much they trust the caller. --yes writes unattended because the operator asked
// for that in so many words; an interactive terminal is asked; anything else has
// printed the plan and writes nothing.
//
// It reports whether the migration may be written, and prints WHY whenever the
// answer is no — a run that silently declines to write is indistinguishable from
// one that wrote and failed to say so.
func confirmAutoDisableMigration(plan *autoupdate.AutoDisableMigration) bool {
	output.Warning.Println("  packages.toml is PUBLISHED: this overlay auto-commits and pushes, so this write reaches origin.")

	if autoupdateYes {
		// An explicit, in-so-many-words approval. Stdin is never read on this
		// path, so it works from a pipe, a cron job or a CI step.
		output.Warning.Printf("  --yes given: stamping %d entry(ies) without a prompt.\n", len(plan.Mark))
		return true
	}
	if !registryPromptIsInteractive() {
		// The plan above IS the report; this run writes nothing. Prompting here
		// would ask a pipe for consent — `yes | bentoo …` would publish.
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing written.")
		output.Info.Printf("  Re-run with --yes to stamp these %d entry(ies) unattended.\n", len(plan.Mark))
		return false
	}

	// ONE question covering the whole plan, not one per record: the operator is
	// approving the migration they just read.
	fmt.Println()
	return confirmRegistryWriteFn(fmt.Sprintf(
		"Stamp %d entry(ies) with disabled_by = \"auto\"? (%d excluded, the plan above is the whole change)",
		len(plan.Mark), len(plan.Excluded)))
}
