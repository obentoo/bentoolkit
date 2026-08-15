package main

import (
	"context"
	"fmt"
	"os"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// This file is the whole of what `--depth` adds to `overlay compare`: the
// refusal for an invocation with nothing to prove, the plan, the one
// confirmation that covers the run, and the pass that puts each proposal up
// story 033's ladder (R7.3).
//
// # Why it is separate from overlay_compare_realign.go
//
// That file opens with "NOTHING HERE WRITES A FILE", and it means it — the
// candidate declarations and the baseline text a model proposes are printed for
// a maintainer to paste, never written, because the overlay auto-commits and
// pushes within minutes. Proving DOES write: a staged copy of the realigned
// ebuild, its eclasses and its profiles, under <configDir>/staging. Folding this
// into that file would have left its first sentence false for whoever read it
// next, which is the sort of drift a promise like that does not survive.
//
// Nothing here writes into the PUBLISHED overlay. Publishing is realign.Promote,
// it takes a maintainer's approval as a parameter, and this pass does not call
// it: the confirmation below buys a BUILD, and "yes, spend the machine time" is
// not "yes, publish it" (design D8 — the model proposes, the gates prove, the
// maintainer approves, and no two of the three are collapsed).
//
// # Where the text goes, and why not through logger
//
// STDOUT, through fmt and output/*, exactly as overlay_validate.go states the
// rule: logger binds os.Stderr once at first use and exposes no setter, so a
// plan on one stream and a build result on the other lose their ordering the
// moment either is redirected — and the ordering is the requirement here. R7.3
// is "the plan BEFORE the first build starts", which is only readable as one
// sequence in one stream.
//
// _Requirements: R7, R7.3_

// realignProve is the prover, held as a package-level variable so no build ever
// runs in a test.
//
// It IS realign.Prove and not an adapter over it, on the discipline
// internal/realign already applies to story 033's two entry points: the var's
// type is the function's signature, so a change to either stops this file
// compiling instead of quietly changing what a realignment is proved by. Driving
// the real one from a test would mean a real `ebuild … clean compile` — a network
// fetch, a writable DISTDIR and portage on the host — which this story's
// constraints forbid a test to need.
//
// _Requirements: R7, R7.3_
var realignProve = realign.Prove

// confirmRealignPlanFn is the y/N seam, defaulting to the single confirmation
// helper this package already uses. confirmAction reads os.Stdin and answers
// "no" on any read error, so an EOF is a decline rather than an accident.
var confirmRealignPlanFn = confirmAction

// compareDepthPreflight reports why THIS INVOCATION cannot honour the `--depth`
// it was given, or nil when it can (R7.3).
//
// It is a returned value rather than a log line for the reason realignPreflight
// states beside it: logger binds its io.Writer at first use, so a refusal written
// there cannot be read by anything but a human watching a terminal, and a
// returned error is the only shape a test — or a caller that knows where its
// output goes — can read the reason in.
//
// # No --depth at all is not a refusal, it is the shipped run
//
// The empty string is the default and is accepted with or without `--realign`:
// `--realign` alone is report-only, which is what Stage 1 shipped and what every
// group-6 test asserts. The empty string is compared EXACTLY, with no trimming,
// because that is the only value cobra produces for a flag nobody passed —
// anything else was typed, and validate.ParseDepth (which trims and folds
// nothing, on purpose) is the right judge of it.
//
// # --depth without --realign is refused rather than ignored
//
// There is nothing to prove: without `--realign` no baseline is read, so no
// realignment is proposed, so no ebuild is a candidate for building. A command
// that quietly did nothing with `--depth=compile` would be indistinguishable
// from one that built everything and found no problem — which is the worse of
// the two silences, since it reads as evidence.
//
// # A depth that builds nothing is refused too, and this is the honest one
//
// validate.RunBuildGates returns "an empty list, not a hollow pass" for every
// depth below DepthPatches, so `--depth=none` and `--depth=options` would stage
// a tree for every candidate, gate none of them, and report a proof of nothing.
// realign.Promote already refuses to publish on an empty gate list — approval
// would otherwise be the only authority that ever spoke — so such a run cannot
// lead anywhere, and letting the operator pay for it first would be charging for
// a receipt. The comparison is against the LADDER's ordering, which Depth
// documents as its contract, rather than against a list of names that could fall
// out of step with it.
//
// _Requirements: R7, R7.3_
func compareDepthPreflight(depthFlag string, realign bool) error {
	if depthFlag == "" {
		return nil
	}

	if !realign {
		return fmt.Errorf(
			"--depth=%s was given without --realign, and there is nothing to prove: without --realign no ::gentoo baseline is read, so no realignment is proposed and no ebuild is built. Add --realign to prove what the review proposes, or drop --depth to compare exactly as before",
			depthFlag)
	}

	depth, err := validate.ParseDepth(depthFlag)
	if err != nil {
		// ParseDepth's own message names the offender and lists every valid rung,
		// so the operator is not sent to the source for five short words. It is
		// wrapped rather than restated so ErrUnknownDepth survives.
		return fmt.Errorf("--depth: %w", err)
	}

	if depth < validate.DepthPatches {
		return fmt.Errorf(
			"--depth=%s builds nothing, so nothing would be proved: below \"%s\" the ladder runs no build gate at all and returns an empty list, and a realignment no gate ever read cannot be published on any evidence. Ask for %s or deeper, or drop --depth to review without building",
			depth, validate.DepthPatches, validate.DepthPatches)
	}
	return nil
}

// realignPlan is what one run would build: which packages, and how deep.
//
// Atoms carries one entry per package, ALREADY SPELLED the way the plan should
// name it — this pass fills it with "<category>/<package>-<version>", since a
// realignment rewrites how one published version is built and a plan that named
// only the package would not say which file changes.
//
// Depth is the rung's NAME rather than the validate.Depth value, because that is
// what the plan prints and what `--depth` accepts: Depth.String and ParseDepth
// are exact inverses, so a plan naming a rung is naming one the operator can ask
// for again.
//
// _Requirements: R7, R7.3_
type realignPlan struct {
	Atoms []string
	Depth string
}

// realignPlanLines builds the plan, one line at a time, and prints nothing.
//
// It is a PURE BUILDER and confirmRealignPlan is the decision, split for the
// reason overlay_compare_summary_test.go records for the summary: logger binds
// its writer at first use and exposes no setter, so splitting the decision from
// the emission is cheaper than capturing a stream, and it leaves the emission
// trivial enough to read at a glance.
//
// EVERY ATOM IS NAMED, and that is the requirement rather than a formatting
// choice. "Three packages will be built" is not a plan anyone can decline in
// part: the operator's only real question is whether the list contains something
// that has no business being rebuilt, and a count cannot be checked against
// anything. The depth is named for the same reason — the depth IS the cost, and
// a plan that omits it asks for consent to an unstated amount of machine time.
//
// _Requirements: R7, R7.3_
func realignPlanLines(plan realignPlan) []string {
	if len(plan.Atoms) == 0 {
		return nil
	}

	lines := make([]string, 0, len(plan.Atoms)+3)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf(
		"Realignment plan: %d package(s) to be built to depth %s, each in a staged tree outside the overlay.",
		len(plan.Atoms), plan.Depth))
	for _, atom := range plan.Atoms {
		lines = append(lines, "  "+atom)
	}
	lines = append(lines,
		"  Each is ::gentoo's own ebuild at our version. Building proves it still works; publishing it is a separate decision, and this run takes none.")
	return lines
}

// printRealignPlan emits the plan. It is the whole of the emission half, and it
// is this short on purpose — see realignPlanLines.
//
// Through output/* rather than logger, because logger writes to stderr: the plan
// and the build results below it are one sequence, and R7.3 is a statement about
// their ORDER.
func printRealignPlan(plan realignPlan) {
	for _, line := range realignPlanLines(plan) {
		output.Info.Println(line)
	}
}

// confirmRealignPlan takes ONE confirmation covering the whole run.
//
// The three gates are confirmSweep's, in the same order and for the same reason:
// --yes proceeds unattended because the operator asked for that in so many words;
// an interactive terminal is asked; anything else says how to proceed and proves
// nothing. registryPromptIsInteractive requires BOTH a stdin and a stdout TTY, so
// `yes | bentoo overlay compare --realign --depth=compile` cannot answer for a
// human — a readable stdin full of consent nobody typed is exactly what a check
// on stdin alone accepts.
//
// ONE confirmation and not one per package: that is the difference between one
// decision and 237 of them, and a prompt per package trains the operator to
// answer without reading.
//
// The refusal NAMES --yes. A gate that cannot be passed is a dead end, and a run
// in CI, a pipeline or a cron job is exactly where this arm is reached — the
// operator has to be told there is a way through, or the feature is unusable
// anywhere it would be most useful. It is the caller's business that declining is
// exit 0: nothing failed, a decision was taken (D9).
//
// The plan itself is NOT printed here; printRealignPlan has already done it, on
// the same split displaySweepPlan and confirmSweep already use. So --yes buys
// past the prompt and never past the plan.
//
// _Requirements: R7, R7.3_
func confirmRealignPlan(plan realignPlan) bool {
	if compareYes {
		output.Warning.Printf("  --yes given: proving %d realignment(s) without a prompt.\n", len(plan.Atoms))
		return true
	}
	if !registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing was built.")
		output.Info.Printf("  Re-run with --yes to prove these %d realignment(s) unattended. Nothing is published either way.\n", len(plan.Atoms))
		return false
	}

	fmt.Println()
	output.Warning.Printf("  Each package is staged outside the overlay and built to depth %s. This costs machine time; it publishes nothing.\n", plan.Depth)
	return confirmRealignPlanFn(fmt.Sprintf("Prove %d realignment(s) at depth %s?", len(plan.Atoms), plan.Depth))
}

// realignCandidate is one package this run would prove, with the proposal that
// would be put up the ladder for it.
type realignCandidate struct {
	// name is the package as the plan and the results name it,
	// "<category>/<package>-<version>".
	name     string
	proposal realign.Proposal
}

// realignIsCandidate is the rule, and it is deliberately DETERMINISTIC: it reads
// the baseline the review resolved and the content comparison's own finding, and
// nothing a model said. A candidate set that depended on a model having been
// reachable would differ between two runs of the same command on the same
// overlay, and `--no-review` would silently prove a different set from a plain
// run.
//
// Three conditions, each carrying its own reason:
//
//   - ::gentoo carries the package (Baseline.Found) and its baseline was
//     readable (no Unexamined, and a path to read). A package ::gentoo does not
//     carry has nothing to realign towards — 84 of the overlay's 321.
//   - THE BASELINE IS AT OUR OWN VERSION (Distance == 0). A baseline at a
//     different version is never proposed, and this is the load-bearing half of
//     the rule: adopting another version's ebuild changes which version we ship,
//     which is a BUMP and not a realignment. Those packages are reported and
//     never proved.
//   - the two ebuilds differ and no registry entry says why. A declared
//     divergence is a decision somebody already took, with a reason written down;
//     re-proposing it every run is how a declaration stops being read.
//
// The last condition is spelled here rather than borrowed because
// internal/overlay's own isUndeclaredDivergence is unexported. It is the same two
// fields in the same order, and it must stay that way: the report warns about
// exactly this set, and a rule that drifted would propose a build for a package
// the report never flagged.
//
// _Requirements: R7, R7.3_
func realignIsCandidate(r overlay.CompareResult) bool {
	if !r.Baseline.Found || r.Baseline.Distance != 0 {
		return false
	}
	if r.Baseline.Unexamined != "" || r.Baseline.Path == "" {
		return false
	}
	return r.Verified == overlay.VerifiedDiffers && !r.Patched
}

// realignCandidates collects what this run would prove, and returns beside it
// the packages that qualified and had to be dropped anyway.
//
// # The proposed ebuild is ::gentoo's own bytes, verbatim
//
// Read from Baseline.Path and passed through untouched. That makes R5.5 —
// publish the exact bytes that were proved — true by construction rather than by
// resemblance, and it makes the word "realign" mean what it says: the proposal is
// not a merge, a patch or a model's rewrite, it is the baseline file. Anything
// derived would be a file no gate had read and no maintainer had compared.
//
// # Why the drops come back as VALUES
//
// Reading a baseline that the review already read successfully fails only in a
// race — the tree resynced, the file moved — and it is rare enough that logging
// it would be the easy choice. It is returned instead so the caller prints it in
// sequence with the plan, on the same stream: a package silently missing from a
// plan is a package the operator believes was proved.
//
// _Requirements: R7, R7.3_
func realignCandidates(report *overlay.CompareReport) (candidates []realignCandidate, dropped []string) {
	if report == nil {
		return nil, nil
	}

	for _, r := range report.Results {
		if !realignIsCandidate(r) {
			continue
		}

		name := fmt.Sprintf("%s/%s-%s", r.Category, r.Package, r.LocalVersion)

		//nolint:gosec // G304: the path is ResolveBaseline's own join of the located ::gentoo tree, a validated atom and a filename read from that directory's listing — never registry input.
		body, err := os.ReadFile(r.Baseline.Path)
		if err != nil {
			dropped = append(dropped, fmt.Sprintf(
				"  %s: not in the plan — its ::gentoo baseline %s could not be read now, although the review read it: %v",
				name, r.Baseline.Path, err))
			continue
		}

		candidates = append(candidates, realignCandidate{
			name: name,
			proposal: realign.Proposal{
				Category: r.Category,
				Package:  r.Package,
				// Our version, which Distance == 0 makes the baseline's version
				// too. A realignment is not a bump: the version does not move, only
				// the way it is built.
				Version: r.LocalVersion,
				Ebuild:  body,
			},
		})
	}
	return candidates, dropped
}

// proveRealignments is R7.3 end to end: plan, ask once, and only then build.
//
// The ORDER IS THE SAFETY PROPERTY, exactly as runSweep states it for the sweep —
// plan, print, confirm, execute — and the prover is not entered at all when the
// confirmation is declined, so a declined run has built nothing and staged
// nothing.
//
// It never touches the exit code (D9). Declining is not a failure, a FAILED gate
// is an answer rather than a crash, and even an unplaceable staging root leaves
// the review's own report — which is complete and useful without any of this —
// to speak for the run.
//
// _Requirements: R7, R7.3_
func proveRealignments(ctx context.Context, report *overlay.CompareReport, overlayPath string) {
	// Unreachable: compareDepthPreflight parsed the very same string before the
	// run started and refused it there. Handled anyway, and handled by RETURNING,
	// because the alternative — assuming the zero value — is the one ParseDepth's
	// own comment warns about: DepthNone is not a fallback, and a caller that used
	// it would have switched the gates off in silence.
	depth, err := validate.ParseDepth(compareDepth)
	if err != nil {
		output.Error.Printf("\n  Nothing was proved — --depth: %v\n", err)
		return
	}

	// The SAME staging root `overlay autoupdate --apply` and `overlay validate
	// --depth` stage under (S033-D1), asked of the one function that spells it, so
	// a tree one command proves is a tree the others can find. It is deliberately
	// neither under the overlay — where `--clean` deletes any ebuild no registry
	// pin claims, staged trees included — nor under /tmp, where a failure could not
	// be inspected after the run.
	//
	// Resolved BEFORE the plan, so a root that cannot be placed refuses before
	// anybody is asked to agree to builds that could not happen.
	stagingRoot, err := autoupdateStagingRoot()
	if err != nil {
		output.Error.Printf("\n  Nothing was proved — --depth=%s builds, and a staged tree to build in could not be placed: %v\n", depth, err)
		return
	}

	candidates, dropped := realignCandidates(report)
	for _, line := range dropped {
		output.Warning.Println(line)
	}
	if len(candidates) == 0 {
		// Said in terms of the RULE and not as "nothing to do", because the two
		// have opposite meanings for an operator who just asked for a build: an
		// overlay whose every divergence is declared, and one whose baselines are
		// all at other versions, are both healthy answers, and neither is a failure
		// to look.
		output.Info.Println("\n  Nothing to prove: no package has an undeclared divergence from a ::gentoo baseline at our own version. A baseline at another version is never proposed — adopting it would change which version we ship, which is a bump and not a realignment.")
		return
	}

	plan := realignPlan{Atoms: realignPlanAtoms(candidates), Depth: depth.String()}
	printRealignPlan(plan)
	if !confirmRealignPlan(plan) {
		return
	}

	for _, c := range candidates {
		proof, err := realignProve(ctx, c.proposal, realign.Options{
			Overlay:     overlayPath,
			StagingRoot: stagingRoot,
			Depth:       depth,
			// Deps is left at its zero value, which validate normalises into the
			// real process and host seams. There is nothing to substitute here: a
			// test never reaches this line, because realignProve is the seam.
		})
		printRealignProof(c.name, proof, err)
	}
}

// realignPlanAtoms is the plan's list, in the order the report produced — which
// is sorted, so two runs over one overlay print the same plan and the operator
// can diff them.
func realignPlanAtoms(candidates []realignCandidate) []string {
	atoms := make([]string, 0, len(candidates))
	for _, c := range candidates {
		atoms = append(atoms, c.name)
	}
	return atoms
}

// printRealignProof says what the ladder found for one package.
//
// It distinguishes the three outcomes that must never be read as one another,
// which is Prove's own split carried through to the operator: an ERROR means
// nothing was examined (the tree could not be staged, the request was malformed)
// and is our bug rather than the realignment's; an EMPTY gate list means nothing
// read it, which "every gate passed" would satisfy only vacuously; and a gate
// that spoke is the run working, whatever it said.
//
// Passing gates are reported as a PERMISSION TO ASK and never as a result: R5.3
// needs a maintainer's approval as well, and this run asks for none. The wording
// says so, because "proved" left alone reads as "done".
func printRealignProof(name string, proof realign.Proof, err error) {
	if err != nil {
		output.Error.Printf("  %s: not proved — %v\n", name, err)
		return
	}

	if len(proof.Gates) == 0 {
		output.Warning.Printf("  %s: staged in %s, and no gate read it — there is nothing to publish it on.\n", name, proof.StagedRoot)
		return
	}

	output.Info.Printf("  %s: staged in %s\n", name, proof.StagedRoot)
	for _, gate := range proof.Gates {
		line := fmt.Sprintf("    %-10s %s", gate.Gate, gate.Outcome)
		if gate.Reason != "" {
			line += " — " + gate.Reason
		}
		switch gate.Outcome {
		case validate.OutcomeFailed:
			output.Error.Println(line)
		case validate.OutcomeSkipped:
			output.Warning.Println(line)
		default:
			output.Success.Println(line)
		}
	}

	if proof.Passed {
		output.Success.Printf("    every gate passed or skipped — that is permission to ASK, not the answer; publishing still needs a maintainer.\n")
		return
	}
	output.Warning.Printf("    the realignment does not pass as it stands, and nothing was published.\n")
}
