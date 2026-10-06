package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// This file is the whole of what `--depth` adds to `overlay compare`: the
// refusal for an invocation with nothing to prove, the plan, the one
// confirmation that covers the run, and the pass that puts each proposal up
// the validation ladder.
//
// It is separate from overlay_compare_realign.go because that file promises
// "NOTHING HERE WRITES A FILE" — the overlay auto-commits and pushes within
// minutes — while proving DOES write: a staged copy of the realigned ebuild, its
// eclasses and its profiles, under <configDir>/staging.
//
// Writing into the PUBLISHED overlay is realign.Promote's alone, reached only
// through offerRealignPublish (overlay_compare_promote.go): per package, only on
// a proof at least one gate read, and only on a maintainer's yes in a terminal.
// The confirmation below buys a BUILD; "yes, spend the machine time" is not
// "yes, publish it".
//
// All text goes to STDOUT through fmt and output/*, not the logger (stderr): the
// plan must come BEFORE the first build starts, and that order is only readable
// as one sequence on one stream.

// The prover is the realignProve field of deps (deps.go), so no build ever
// runs in a test. Its type IS realign.Prove's signature, not an adapter over
// it, so a change to either stops defaultDeps compiling instead of quietly
// changing what a realignment is proved by. Driving the real one from a test
// would need a real `ebuild … clean compile` — a network fetch, a writable
// DISTDIR and portage on the host.
//
// The y/N question is the confirmRealignPlan field of deps, defaulting to
// confirmAction, which reads os.Stdin and answers "no" on any read error, so an
// EOF is a decline rather than an accident.

// compareDepthPreflight reports why THIS INVOCATION cannot honour the `--depth`
// it was given, or nil when it can. It returns rather than logs, so a test can
// assert it; runCompare is the one place that logs it before failing the run.
//
// An empty depthFlag is the default and is accepted with or without `--realign`
// (`--realign` alone is report-only). It is compared EXACTLY, untrimmed: that is
// the only value cobra produces for an unpassed flag, and anything else was typed
// and is judged by validate.ParseDepth.
//
// --depth without --realign is refused rather than ignored: no baseline is read,
// so nothing is a candidate, and a command that quietly built nothing would read
// as one that built everything and found no problem.
//
// A depth below DepthPatches is refused too: RunBuildGates returns an empty list
// there and realign.Promote refuses to publish on an empty gate list, so the run
// could lead nowhere and the operator would pay for staging first. The check
// uses the ladder's ordering, which Depth documents as its contract.
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
		"  Each is ::gentoo's own ebuild at our version. Building proves it still works; publishing is a separate per-package question, asked in a terminal only after that package's gates have spoken — declining any of them publishes nothing.")
	return lines
}

// printRealignPlan emits the plan. It is the whole of the emission half, and it
// is this short on purpose — see realignPlanLines.
//
// Through output/* rather than logger, because logger writes to stderr: the plan
// and the build results below it are one sequence, and what matters is their
// ORDER.
func printRealignPlan(plan realignPlan) {
	for _, line := range realignPlanLines(plan) {
		output.Info.Println(line)
	}
}

// confirmRealignPlan takes ONE confirmation covering the whole run.
//
// The three gates are confirmSweep's, in the same order: --yes proceeds
// unattended because the operator asked for that in so many words; an
// interactive terminal is asked; anything else says how to proceed and proves
// nothing. deps.registryPromptIsInteractive requires BOTH a stdin and a stdout
// TTY, so `yes | bentoo overlay compare --realign --depth=compile` cannot answer
// for a human.
//
// ONE confirmation and not one per package: a prompt per package trains the
// operator to answer without reading.
//
// The refusal NAMES --yes, because CI, pipelines and cron jobs are exactly where
// this arm is reached, and a gate with no way through is a dead end. Declining is
// exit 0 (the caller's business): nothing failed, a decision was taken.
//
// The plan itself is printed by printRealignPlan, not here, so --yes skips the
// prompt and never the plan.
func confirmRealignPlan(plan realignPlan, d *deps) bool {
	if compareYes {
		output.Warning.Printf("  --yes given: proving %d realignment(s) without a prompt.\n", len(plan.Atoms))
		return true
	}
	if !d.registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing was built.")
		output.Info.Printf("  Re-run with --yes to prove these %d realignment(s) unattended. Nothing is published either way.\n", len(plan.Atoms))
		return false
	}

	fmt.Println()
	output.Warning.Printf("  Each package is staged outside the overlay and built to depth %s. This costs machine time; it publishes nothing.\n", plan.Depth)
	return d.confirmRealignPlan(fmt.Sprintf("Prove %d realignment(s) at depth %s?", len(plan.Atoms), plan.Depth))
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
// the baseline the review resolved and the content comparison's own finding,
// never what a model said, so `--no-review` proves the same set as a plain run.
//
//   - ::gentoo carries the package (Baseline.Found) and its baseline was
//     readable (no Unexamined, and a path to read): without one there is
//     nothing to realign towards.
//   - THE BASELINE IS AT OUR OWN VERSION (Distance == 0). Adopting another
//     version's ebuild changes which version we ship — a BUMP, not a
//     realignment — so those packages are reported and never proved.
//   - the two ebuilds differ and no registry entry says why. A declared
//     divergence is a decision already taken; re-proposing it every run is how
//     a declaration stops being read.
//
// The last condition restates internal/overlay's unexported
// isUndeclaredDivergence and must stay in step with it: the report warns about
// exactly this set, and a drifted rule would build a package the report never
// flagged.
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
// Read from Baseline.Path and passed through untouched. That makes publishing
// the exact bytes that were proved true by construction rather than by
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
func realignCandidates(report *overlay.CompareReport) (candidates []realignCandidate, dropped []string) {
	if report == nil {
		return nil, nil
	}

	for _, r := range report.Results {
		if !realignIsCandidate(r) {
			continue
		}

		name := fmt.Sprintf("%s/%s-%s", r.Category, r.Package, r.LocalVersion)

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

// proveRealignments is the `--depth` pass end to end: plan, ask once, and only
// then build.
//
// The ORDER IS THE SAFETY PROPERTY, exactly as runSweep states it for the sweep —
// plan, print, confirm, execute — and the prover is not entered at all when the
// confirmation is declined, so a declined run has built nothing and staged
// nothing.
//
// It never touches the exit code. Declining is not a failure, a FAILED gate
// is an answer rather than a crash, and even an unplaceable staging root leaves
// the review's own report — which is complete and useful without any of this —
// to speak for the run.
func proveRealignments(ctx context.Context, report *overlay.CompareReport, overlayPath string, d *deps) {
	// Unreachable: compareDepthPreflight parsed the very same string before the
	// run started and refused it there. Handled anyway, and handled by RETURNING,
	// because the alternative — assuming the zero value — is the one ParseDepth's
	// own comment warns about: DepthNone is not a fallback, and a caller that used
	// it would have switched the gates off in silence.
	depth, err := validate.ParseDepth(compareDepth)
	if err != nil {
		output.Error.Fprintf(os.Stderr, "\n  Nothing was proved — --depth: %v\n", err)
		return
	}

	// The SAME staging root `overlay autoupdate --apply` and `overlay validate
	// --depth` stage under, asked of the one function that spells it, so
	// a tree one command proves is a tree the others can find. It is deliberately
	// neither under the overlay — where `--clean` deletes any ebuild no registry
	// pin claims, staged trees included — nor under /tmp, where a failure could not
	// be inspected after the run.
	//
	// Resolved BEFORE the plan, so a root that cannot be placed refuses before
	// anybody is asked to agree to builds that could not happen.
	stagingRoot, err := autoupdateStagingRoot()
	if err != nil {
		output.Error.Fprintf(os.Stderr, "\n  Nothing was proved — --depth=%s builds, and a staged tree to build in could not be placed: %v\n", depth, err)
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

	// The policy and the log directory, resolved ONCE for the whole pass and the
	// same way `overlay validate --depth` resolves them: per candidate would
	// re-derive the same answers N times, and give two of them a place to differ.
	//
	// It sits BELOW the "nothing to prove" return, as in the sibling command: the
	// only thing either resolution can say out loud is that logs will not be
	// retained, which is noise to an operator about to be told there is nothing
	// to prove. Still above the loop — that is why there is a block here at all.
	//
	// requireIsolation is read from the SAME key `overlay autoupdate` reads
	// (autoupdate.validate.require_isolation), because the gates are the same. A
	// config that could not be loaded leaves it false — the shipped behaviour
	// with the key unset; the run is not refused over a missing config file. The
	// overlay path is NOT taken from here: this function was handed one, and a
	// second answer is how a run proves one overlay and reports on another.
	var requireIsolation bool
	// The config alone: no overlay selection runs here, so the run's "using the
	// overlay checkout at …" notice is not repeated for a path this ignores.
	if cfg, cerr := config.Load(); cerr == nil {
		if _, perr := cfg.GetOverlayPathNoValidation(); perr == nil {
			requireIsolation = cfg.Autoupdate.Validate.GetRequireIsolation()
		}
	}

	// A build gate that FAILED says so on its own, but the reason upstream broke
	// — the option `meson` refused — is only in `ebuild`'s log. The same
	// directory the apply path and `overlay validate` retain their logs in, so an
	// operator looking for "the log of the thing that just failed" has one place
	// to look regardless of which command ran the gates. A failure to place it is
	// NOT fatal: the gates still run and their reason says the log was not
	// retained, which is worth more than refusing to prove anything at all — and
	// it is said BEFORE the confirmation, so an operator agreeing to a build
	// knows in advance that a failure will leave no transcript.
	var logDir string
	if configDir, cerr := autoupdateConfigDir(); cerr == nil {
		logDir = filepath.Join(configDir, "logs")
	} else {
		output.Warning.Printf("\n  Build logs will not be retained: %v\n", cerr)
	}

	plan := realignPlan{Atoms: realignPlanAtoms(candidates), Depth: depth.String()}
	printRealignPlan(plan)
	if !confirmRealignPlan(plan, d) {
		return
	}

	for _, c := range candidates {
		proof, err := d.realignProve(ctx, c.proposal, realign.Options{
			Overlay:     overlayPath,
			StagingRoot: stagingRoot,
			Depth:       depth,
			// The composition that lives in cmd/bentoo and nowhere else:
			// validate accepts only what a caller supplies, autoupdate owns
			// Manifest GENERATION, and this is the one layer that imports both.
			// A realignment is a SAME-VERSION edit — it bumps nothing, so no
			// fetch has produced a new archive — which makes the published
			// Manifest the record describing the archive actually on disk. It is
			// the same source and the same function `overlay validate` reads,
			// for the same reason.
			StagedManifest:   publishedManifestBytes,
			RequireIsolation: requireIsolation,
			LogDir:           logDir,
			// Distdir is left EMPTY, and that is a decision rather than an
			// oversight. `overlay compare` registers no --distdir
			// flag, and the sibling command's answer comes from exactly that
			// flag and from nowhere else — validate.Options.Distdir states it
			// outright: there is no configured rung between the flag and the
			// host, "the command passes --distdir here or nothing". So there is
			// no resolved directory to carry here, and the two ways to pretend
			// otherwise are both worse than empty: a new flag is new CLI surface
			// this command deliberately lacks, and reading some other key
			// would make `overlay compare --depth` build against a directory
			// `overlay validate --depth` would not have used. Empty sets no
			// DISTDIR on the child, so the build reads the host's own
			// configuration — which is what every realign proof has always done.
			//
			// Deps is left at its zero value, which validate normalises into the
			// real process and host seams. There is nothing to substitute here: a
			// test never reaches this line, because realignProve is the seam.
		})
		printRealignProof(c.name, proof, err)

		// The publish question is put only for a proof that PASSED and that at
		// least one gate actually read: an errored prove was never examined, an
		// empty gate list is a proof of nothing (printRealignProof has already
		// said so), and a failed proof was refused by the gates themselves.
		if err != nil || len(proof.Gates) == 0 || !proof.Passed {
			continue
		}
		offerRealignPublish(c, proof, overlayPath, d)
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
// Passing gates are reported as a PERMISSION TO ASK and never as a result:
// publishing needs a maintainer's approval as well, and this run asks for none. The wording
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
