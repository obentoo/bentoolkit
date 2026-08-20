package autoupdate

import (
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// This file is R9.1 with R9.2 as its hard constraint: evaluate a pending update
// through the gates at its resolved depth, and publish NOTHING on any path.
//
// # Why it is not Apply with a flag
//
// A boolean parameter that switches promotion off would put the one line that
// writes into the published overlay inside a function whose other caller wants it
// to run. `--check` is documented read-only and the overlay it would write to
// auto-commits and pushes; the guarantee has to be readable, and "this function
// contains no call that writes into the overlay" is readable in a way "this
// function contains such a call, guarded by a bool" is not.
//
// # What it shares with Apply, and why that is the point
//
// Everything BUT promotion: the same depth resolution, the same staging, the same
// manifest step, the same gates, the same reviewer, and — decisively — the same
// record beside the staged tree (R10.4). A check that recorded its proof
// differently from an apply would be a check whose hours the apply cannot spend,
// which is the whole of R10 defeated by a second code path.

// Validate runs one pending update through the gates at its resolved depth and
// reports what each gate said, without publishing anything (R9.1, R9.2).
//
// ceiling is the depth the operator confirmed for the whole run. A reviewer's
// escalation is held there rather than spent unasked (R9.6): a plan that resolved
// entirely to `options` asked the operator about nothing, so a bump raised to
// `compile` afterwards would spend hours they never saw priced. DepthNone means
// "no ceiling", which is what a caller that did not ask anybody passes.
//
// IT NEVER RETURNS AN ERROR, and that is deliberate. A check evaluates a BATCH:
// one package whose tree could not be staged, or whose pending entry went stale
// between the check and here, must not stop the other three hundred. Every such
// case comes back as a SKIPPED gate carrying the reason, which is what
// EbuildResult.WorstOutcome reads as "nothing was said about this bump" — never
// as a pass.
func (a *Applier) Validate(pkg string, ceiling validate.Depth) validate.EbuildResult {
	update, found := a.pending.Get(pkg)
	if !found {
		return checkSkipped(pkg, "", fmt.Sprintf("%s is no longer in the pending list, so there was nothing to validate", pkg))
	}

	newVersion := stripVersionPrefix(strings.TrimSpace(update.NewVersion))
	if !ebuild.IsValidVersion(newVersion) {
		return checkSkipped(pkg, update.NewVersion, fmt.Sprintf(
			"%q is not a version an ebuild filename can carry (from %q), so no gate could be run for %s", newVersion, update.NewVersion, pkg))
	}
	newVersion = applyRevision(newVersion, a.configs[pkg].Revision)

	currentVersion, err := a.resolveCurrentVersion(pkg)
	if err != nil {
		return checkSkipped(pkg, newVersion, fmt.Sprintf("the current version of %s could not be resolved against the overlay: %v", pkg, err))
	}

	depth := a.depthFor(pkg, currentVersion, newVersion)
	if a.stagingRoot == "" {
		// Not a defensive branch: every gate above `none` reads a staged tree, so
		// an applier built without a staging root can answer nothing at all — and
		// saying so is better than reporting a clean run of zero gates.
		return checkSkipped(pkg, newVersion, "this run has no staging root, so there is nowhere to validate the candidate without writing it into the published overlay")
	}
	if depth.Depth == validate.DepthNone {
		return checkSkipped(pkg, newVersion, depth.Reason)
	}

	inputs, err := a.stagedInputsFor(pkg, currentVersion, update)
	if err != nil {
		return checkSkipped(pkg, newVersion, fmt.Sprintf("the inputs of %s-%s could not be digested: %v", pkg, newVersion, err))
	}

	// A private ApplyResult, never returned and never promoted from. The gate
	// helpers below record their side-effects on one (the staged path, the fix
	// summary, the depth reached), and giving them a local one keeps this function
	// from having to reimplement any of them.
	local := &ApplyResult{Package: pkg, OldVersion: currentVersion, NewVersion: newVersion, DepthRequested: depth.Depth.String(), DepthReason: depth.Reason}

	// gates is declared before the reporter's defer so the lifecycle this check
	// emits reports the VERDICT rather than merely that the function returned.
	// It is also what the record below is written from, however this returns.
	var gates []validate.GateResult
	a.reporter.TaskStart(pkg, pkg)
	defer func() {
		worst := validate.EbuildResult{Gates: gates}.WorstOutcome()
		a.reporter.TaskDone(pkg, worst != validate.OutcomeFailed, local.DepthReached, "")
	}()

	cand, err := a.prepareInStagingTree(pkg, currentVersion, newVersion, update, local)
	if err != nil {
		// R3.10: a tree that could not be prepared withdraws the bump. Here that
		// is one SKIPPED gate per build gate the depth covers, which is the same
		// discharge validate.SkippedGates gives the apply path — the set of gates
		// that owe an outcome must not depend on which command asked.
		//
		// BOTH halves carry DeclineCandidate (S040-R1.1), not just the build-gate
		// list. The option gate is absent from SkippedGates' ladder — it reads the
		// ebuild's own text and never wanted a staged tree — so stamping only the
		// ladder would leave the one gate that ALWAYS appears here reading as a
		// cause nobody recorded; and at a depth that covers no build gate the
		// ladder is empty and that gate is the entire list. It is the same fault
		// either way: nothing read this candidate.
		return validate.EbuildResult{
			Package:        pkg,
			Version:        newVersion,
			Depth:          validate.DepthNone.String(),
			DepthRequested: depth.Depth.String(),
			DepthReason:    depth.Reason,
			Gates: append([]validate.GateResult{{
				Gate:     validate.GateOptions,
				Outcome:  validate.OutcomeSkipped,
				Reason:   fmt.Sprintf("the staged tree of %s-%s could not be prepared, so no gate ever read this candidate: %v", pkg, newVersion, err),
				Declined: validate.DeclineCandidate,
			}}, candidateDeclinedGates(depth.Depth, fmt.Sprintf("the staged tree of %s-%s could not be prepared: %v", pkg, newVersion, err))...),
		}
	}

	// R10.4, on the same terms as an apply: whatever the gates below say, it is
	// recorded beside the tree they said it about — which is precisely what makes
	// a later `--apply` able to promote this work instead of repeating it.
	stagedRoot := local.StagedPath
	defer func() { a.recordStagedProof(stagedRoot, pkg, newVersion, inputs, gates, depth.Depth) }()

	a.reporter.TaskStage(pkg, "manifest")
	fetchedDistdir, err := a.runManifestWithFix(cand, pkg, newVersion, local)
	// The same lifetime and the same hand-off the apply path gets, on the runner
	// that publishes nothing (R3.1). --check's failure mode is quieter, not
	// smaller: a plan that reports "proved" for a bump nothing read.
	defer removeStagedDistdir(fetchedDistdir)
	cand.fetchedDistdir = fetchedDistdir
	if err != nil {
		// DeclineCandidate on both halves (S040-R1.2), for the reason the staging
		// fault above states and one more that is specific to this step: Portage
		// refuses an ebuild whose Manifest does not describe its archive, so a
		// candidate that never got one is a candidate no gate could have read.
		// validate's own core already answers this exact condition with the
		// candidate's cause (run.go, prepareStagedManifest); reaching it through
		// the applier instead must not change whose fault it is.
		gates = append(gates, validate.GateResult{
			Gate:     validate.GateOptions,
			Outcome:  validate.OutcomeSkipped,
			Reason:   fmt.Sprintf("the manifest step failed for %s-%s, so the candidate has no digested distfile for any gate to read: %v", pkg, newVersion, err),
			Declined: validate.DeclineCandidate,
		})
		gates = append(gates, candidateDeclinedGates(depth.Depth, fmt.Sprintf("the manifest step failed for %s-%s: %v", pkg, newVersion, err))...)
		return checkResult(pkg, newVersion, local, depth, gates)
	}

	gates = a.runStaticGates(cand, pkg, newVersion)

	depth = a.reviewBump(cand, pkg, currentVersion, newVersion, depth, &gates)
	depth = holdAtConfirmedDepth(depth, ceiling)
	local.DepthRequested = depth.Depth.String()
	local.DepthReason = depth.Reason

	a.reporter.TaskStage(pkg, "build gates")
	buildGates, buildErr := a.runBuildGates(cand, pkg, newVersion, depth.Depth, local)
	gates = append(gates, buildGates...)
	if buildErr != nil {
		// The build CHILD failed in a way no gate could attribute. On the apply
		// path that fails the bump; here there is no bump to fail, so it is
		// reported as the failing gate it belongs to — the operator asked what
		// would survive an apply, and "this would not have" is the answer.
		gates = append(gates, validate.GateResult{
			Gate:     gateForDepth(depth.Depth),
			Outcome:  validate.OutcomeFailed,
			Reason:   fmt.Sprintf("the build of %s-%s did not complete: %v", pkg, newVersion, buildErr),
			Findings: []validate.Finding{{Gate: gateForDepth(depth.Depth), Severity: validate.SeverityError, Detail: buildErr.Error()}},
		})
	}

	a.recordDepthReached(local, gates, depth.Depth)
	return checkResult(pkg, newVersion, local, depth, gates)
}

// holdAtConfirmedDepth applies R9.6's ceiling to a depth a reviewer may have
// raised.
//
// HOLDING IS A CHOICE, and R9.6 allows either answer. It is the one that keeps
// the promise the plan made: the operator was shown a cost and approved it, and a
// raise past what they saw is hours they were never asked about. The hold is
// never silent — it goes into the reason, which is what the check prints beside
// the package.
//
// A ceiling of none means nobody was asked, so nothing is held: that is the
// caller who never showed a plan, and inventing a ceiling for them would silently
// cap a run the operator drove directly.
func holdAtConfirmedDepth(decision validate.DepthDecision, ceiling validate.Depth) validate.DepthDecision {
	if ceiling == validate.DepthNone || decision.Depth <= ceiling {
		return decision
	}
	return validate.DepthDecision{
		Depth: ceiling,
		Reason: fmt.Sprintf("%s; held at %s, the deepest depth this run's plan confirmed, rather than spent unasked (R9.6)",
			decision.Reason, ceiling),
		SkippedByPolicy: decision.SkippedByPolicy,
	}
}

// checkResult assembles the one value a check reports per package.
func checkResult(pkg, version string, local *ApplyResult, depth validate.DepthDecision, gates []validate.GateResult) validate.EbuildResult {
	reached := local.DepthReached
	if reached == "" {
		reached = validate.DepthNone.String()
	}
	return validate.EbuildResult{
		Package:        pkg,
		Version:        version,
		Depth:          reached,
		DepthRequested: depth.Depth.String(),
		DepthReason:    local.DepthReason,
		Gates:          gates,
	}
}

// checkSkipped is the answer for a package no gate could be run for at all.
//
// It reports ONE skipped gate rather than none, because a result carrying no gate
// at all is indistinguishable from a package that was never planned — and the
// reason is required, since a skip nobody can read is a pass.
//
// # Its cause is left unrecorded, because five callers do not share one (S040-R1.5)
//
// This helper answers for a pending entry that went stale, a NewVersion no
// ebuild filename can carry, a current version the overlay would not resolve, a
// run built with no staging root, and a policy that resolved to depth none.
// Only the second is about the candidate: the fourth is this run's own
// configuration, and the fifth is not a fault at all — the policy decided this
// bump needs no gate, and stamping the candidate's cause on that answer would
// turn "there was nothing to prove" into "nothing was measured".
//
// So the cause here is genuinely mixed rather than merely unexamined, and one
// stamp would make one claim on behalf of all five. Splitting it per caller is a
// change to what `--check` reports about four conditions R1.1 and R1.2 do not
// name; DeclineUnrecorded keeps today's answer for all of them, which is what
// that default is for.
func checkSkipped(pkg, version, reason string) validate.EbuildResult {
	return validate.EbuildResult{
		Package:        pkg,
		Version:        version,
		Depth:          validate.DepthNone.String(),
		DepthRequested: validate.DepthNone.String(),
		DepthReason:    reason,
		Gates: []validate.GateResult{{
			Gate:    validate.GateOptions,
			Outcome: validate.OutcomeSkipped,
			Reason:  reason,
		}},
	}
}
