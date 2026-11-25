package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// This file evaluates a pending update through the gates at its resolved depth,
// with one hard constraint: publish NOTHING on any path.
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
// record beside the staged tree. A check that recorded its proof differently
// from an apply would be a check whose hours the apply cannot spend, which is
// the whole point of reusing a check's proof defeated by a second code path.

// Validate runs one pending update through the gates at its resolved depth and
// reports what each gate said, without publishing anything.
//
// ceiling is the depth the operator confirmed for the whole run. A reviewer's
// escalation is held there rather than spent unasked: a plan that resolved
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
func (a *Applier) Validate(ctx context.Context, pkg string, ceiling validate.Depth) validate.EbuildResult {
	// An adopted tree is the operator's repair, not an agent's input: no fixer
	// or reviewer runs on it, exactly as on the apply path.
	adopted := a.adoptedRoot != ""
	if adopted {
		a = a.withoutAgents()
	}
	update, found := a.pending.Get(pkg)
	if !found {
		return checkSkipped(pkg, "", fmt.Sprintf("%s is no longer in the pending list, so there was nothing to validate", pkg))
	}

	newVersion := parse.StripVersionPrefix(strings.TrimSpace(update.NewVersion))
	if !ebuild.IsValidVersion(newVersion) {
		return checkSkipped(pkg, update.NewVersion, fmt.Sprintf(
			"%q is not a version an ebuild filename can carry (from %q), so no gate could be run for %s", newVersion, update.NewVersion, pkg))
	}
	// The same allow-list Apply enforces, and for the same reason: Validate is a
	// second writer of the candidate ebuild, and every gate below it (manifest,
	// patches, configure) sources that file as bash. Refused before anything is
	// staged, and reported SKIPPED like the version gate above — failing the
	// package is Apply's job, not the check's.
	if err := checkUpstreamValues(pkg, update); err != nil {
		return checkSkipped(pkg, newVersion, fmt.Sprintf("%v, so no gate was run for %s", err, pkg))
	}
	// Requirements after the value check, as in Apply: an invalid replayed
	// version is refused above and never reported as a wait.
	if waiting, err := a.unmetRequirements(pkg, update); err != nil {
		return checkSkipped(pkg, newVersion, fmt.Sprintf("%v, so no gate was run for %s", err, pkg))
	} else if len(waiting) > 0 {
		// Another package has to land first: nothing about this candidate is
		// wrong, so the bump waits.
		res := checkSkipped(pkg, newVersion, fmt.Sprintf("waiting for %s, so no gate was run for %s", strings.Join(waiting, ", "), pkg))
		res.Gates[0].Declined = validate.DeclineHost
		return res
	}
	newVersion = ebuilds.ApplyRevision(newVersion, a.configs[pkg].Revision)

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
	// A binary record at depth none still has one thing to prove: that its
	// archives fetch and digest. Every other depth-none answer (an operator's
	// --depth=none on a source package) runs no gate at all, as before.
	fetchOnly := depth.Depth == validate.DepthNone && a.resolvedPackageType(pkg, currentVersion) == "bin"
	if depth.Depth == validate.DepthNone && !fetchOnly {
		return checkSkipped(pkg, newVersion, depth.Reason)
	}
	// The refusal Apply makes, before anything under the staging root is
	// removed: a repair in progress is not restaged over by a check either.
	// An adopted tree IS that repair, so it is not refused.
	if !adopted {
		if _, _, err := a.repairPending(pkg, newVersion); err != nil {
			return checkSkipped(pkg, newVersion, err.Error())
		}
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
	// gatesPassed is set only at the end, once the gates would allow
	// promotion; only then does the record name the bytes the gates read, so a
	// later apply may promote them.
	var gatesPassed bool
	a.reporter.TaskStart(pkg, pkg)
	defer func() {
		// ✓ means what the report means by it, READY: a bump the maintainer
		// or the environment still has to act on is not a success, whether or
		// not a gate FAILED (a manifest failure leaves every gate SKIPPED). The
		// row names the state. The bundle defer below runs first, so a HUMAN
		// bump's row can name --repair, as a failed apply's row did.
		state := proofState(gates, depth.Depth)
		hint := ""
		if local.RepairBundle != "" && a.outcomeHint != nil {
			hint = a.outcomeHint(local)
		}
		summary := state
		if local.DepthReached != "" {
			summary += ", reached " + local.DepthReached
		}
		tui.TaskDoneWithHint(a.reporter, pkg, state == string(report.Ready), summary, hint, "")
	}()

	var cand candidatePaths
	if adopted {
		cand, depth, err = a.adoptForCheck(pkg, currentVersion, newVersion, update, depth, local)
		local.DepthRequested, local.DepthReason = depth.Depth.String(), depth.Reason
	} else {
		cand, err = a.prepareInStagingTree(pkg, currentVersion, newVersion, update, local)
	}
	if err != nil {
		// A tree that could not be prepared withdraws the bump. Here that
		// is one SKIPPED gate per build gate the depth covers, which is the same
		// discharge validate.SkippedGates gives the apply path — the set of gates
		// that owe an outcome must not depend on which command asked.
		//
		// BOTH halves carry DeclineCandidate, not just the build-gate
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

	// On the same terms as an apply: whatever the gates below say, it is
	// recorded beside the tree they said it about — which is precisely what makes
	// a later `--apply` able to promote this work instead of repeating it.
	stagedRoot := local.StagedPath
	// A check never publishes, so a tarball its vendor step generated is
	// recorded as unpublished.
	defer func() {
		validatedEbuild := ""
		if gatesPassed {
			validatedEbuild = cand.ebuildPath
		}
		a.recordStagedProof(ctx, stagedRoot, pkg, newVersion, inputs, gates, depth.Depth, validatedEbuild,
			unpublishedVendor(local.Vendor, false))
	}()
	// The bundle --repair works from, for a bump this check leaves to the
	// maintainer. Registered after the record, so it runs first.
	manifestFailed := false
	defer func() { a.writeCheckBundle(pkg, local, cand, gates, depth.Depth, manifestFailed) }()

	a.reporter.TaskStage(pkg, "manifest")
	fetchedDistdir, err := a.runManifestWithFix(ctx, cand, pkg, newVersion, local)
	// The same lifetime and the same hand-off the apply path gets, on the runner
	// that publishes nothing. --check's failure mode is quieter, not
	// smaller: a plan that reports "proved" for a bump nothing read.
	defer removeStagedDistdir(a.logger(), fetchedDistdir)
	cand.fetchedDistdir = fetchedDistdir
	if err != nil {
		// DeclineCandidate on both halves, for the reason the staging
		// fault above states and one more that is specific to this step: Portage
		// refuses an ebuild whose Manifest does not describe its archive, so a
		// candidate that never got one is a candidate no gate could have read.
		// validate's own core already answers this exact condition with the
		// candidate's cause (run.go, prepareStagedManifest); reaching it through
		// the applier instead must not change whose fault it is.
		//
		// Unless the failure was classified as the environment's (a timeout, an
		// upstream 5xx, a full or unwritable distdir): then the candidate was
		// never at fault, and the bump waits rather than asking the maintainer.
		manifestFailed = true
		cause, declined := validate.DeclineCandidate, candidateDeclinedGates
		if errors.Is(err, ErrManifestEnvironment) {
			cause, declined = validate.DeclineHost, hostDeclinedGates
		}
		gates = append(gates, validate.GateResult{
			Gate:     validate.GateOptions,
			Outcome:  validate.OutcomeSkipped,
			Reason:   fmt.Sprintf("the manifest step failed for %s-%s, so the candidate has no digested distfile for any gate to read: %v", pkg, newVersion, err),
			Declined: cause,
		})
		gates = append(gates, declined(depth.Depth, fmt.Sprintf("the manifest step failed for %s-%s: %v", pkg, newVersion, err))...)
		return checkResult(pkg, newVersion, local, depth, gates)
	}

	if fetchOnly {
		gates = a.fetchGates(ctx, cand, pkg, newVersion)
		gatesPassed, _ = validate.PromotionDecision(gates, nil)
		return checkResult(pkg, newVersion, local, depth, gates)
	}

	gates = a.runStaticGates(ctx, cand, pkg, newVersion)

	depth = a.reviewBump(ctx, cand, pkg, currentVersion, newVersion, depth, &gates)
	depth = holdAtConfirmedDepth(depth, ceiling)
	local.DepthRequested = depth.Depth.String()
	local.DepthReason = depth.Reason

	a.reporter.TaskStage(pkg, "build gates")
	buildGates, buildErr := a.runBuildGates(ctx, cand, pkg, newVersion, depth.Depth, local)
	gates = appendBuildGates(gates, buildGates, buildErr, pkg, newVersion, depth.Depth)

	a.recordDepthReached(local, gates, depth.Depth)
	gatesPassed, _ = validate.PromotionDecision(gates, nil)
	return checkResult(pkg, newVersion, local, depth, gates)
}

// appendBuildGates adds what the build gates reported to gates, turning a build
// error into the gate it belongs to: declined over the host when the
// attribution put it on the machine, FAILED otherwise.
func appendBuildGates(gates, buildGates []validate.GateResult, buildErr error, pkg, newVersion string, depth validate.Depth) []validate.GateResult {
	reason := fmt.Sprintf("the build of %s-%s did not complete: %v", pkg, newVersion, buildErr)
	switch {
	case errors.Is(buildErr, ErrBuildEnvironment):
		// Attributed to this machine (a missing precondition, a full disk, a
		// death before src_prepare): the first run's gates are REPLACED, since a
		// FAILED one among them would still read as the candidate's, and the
		// build gates are declined over the host, the way the next run declines
		// them on the recorded precondition. Reported as a failure it carried no
		// cause and read HUMAN (edk2 202611 on 2026-11-23, a signing key the
		// portage user could not read). Only the check does this: the apply
		// keeps failing the bump, because a host-declined build gate would let
		// PromotionDecision publish it.
		return append(gates, hostDeclinedGates(depth, reason)...)
	case buildErr != nil:
		// The build CHILD failed in a way no gate could attribute. On the apply
		// path that fails the bump; here there is no bump to fail, so it is
		// reported as the failing gate it belongs to — the operator asked what
		// would survive an apply, and "this would not have" is the answer.
		gates = append(gates, buildGates...)
		return append(gates, validate.GateResult{
			Gate:     gateForDepth(depth),
			Outcome:  validate.OutcomeFailed,
			Reason:   reason,
			Findings: []validate.Finding{{Gate: gateForDepth(depth), Severity: validate.SeverityError, Detail: buildErr.Error()}},
		})
	default:
		return append(gates, buildGates...)
	}
}

// holdAtConfirmedDepth applies the confirmed-depth ceiling to a depth a reviewer
// may have raised.
//
// HOLDING IS A CHOICE, not the only valid answer. It is the one that keeps
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
		Reason: fmt.Sprintf("%s; held at %s, the deepest depth this run's plan confirmed, rather than spent unasked",
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
	res := validate.EbuildResult{
		Package:        pkg,
		Version:        version,
		Depth:          reached,
		DepthRequested: depth.Depth.String(),
		DepthReason:    local.DepthReason,
		Gates:          gates,
	}
	if local.Vendor.Generated {
		res.Notes = append(res.Notes, VendorGeneratedNote)
	}
	return res
}

// VendorGeneratedNote marks a check whose gates ran on a vendor tarball this
// run generated: a check never uploads one.
const VendorGeneratedNote = "vendor tarball generated locally, not uploaded"

// checkSkipped is the answer for a package no gate could be run for at all.
//
// It reports ONE skipped gate rather than none, because a result carrying no gate
// at all is indistinguishable from a package that was never planned — and the
// reason is required, since a skip nobody can read is a pass.
//
// # Its cause is left unrecorded, because five callers do not share one
//
// It answers for a stale pending entry, a NewVersion no ebuild filename can
// carry, a current version the overlay would not resolve, a run with no staging
// root, and a policy that resolved to depth none. Only the second is about the
// candidate; the fourth is this run's configuration, and the fifth is no fault
// at all — stamping the candidate's cause there would turn "there was nothing
// to prove" into "nothing was measured". One stamp would make one claim for all
// five, so DeclineUnrecorded keeps today's answer for each of them.
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

// fetchGates is what a check proves about a binary record: the manifest step
// above already fetched and digested its archives, so the fetch gate asks only
// whether that Manifest covers every SRC_URI file, and the required-use gate
// (when the overlay declares a target policy) reads the candidate's text as it
// does for any package. The option gate is left out on purpose: it reads build
// declarations from a source archive, and a .deb or an AppImage has none, so it
// could only ever report SKIPPED — a non-answer that would make the bump read
// as unmeasured.
func (a *Applier) fetchGates(ctx context.Context, cand candidatePaths, pkg, version string) []validate.GateResult {
	fetch := validate.GateResult{
		Gate:    validate.GateFetch,
		Outcome: validate.OutcomePass,
		Reason:  fmt.Sprintf("binary record: the manifest step fetched and digested every archive of %s-%s, and the coverage check found no SRC_URI file without a DIST line", pkg, version),
	}
	if err := a.checkManifestCoverage(ctx, cand.pkgDir, pkg, version); err != nil {
		fetch.Outcome = validate.OutcomeFailed
		fetch.Reason = fmt.Sprintf("the Manifest of %s-%s does not cover its SRC_URI: %v", pkg, version, err)
		fetch.Findings = []validate.Finding{{Gate: validate.GateFetch, Severity: validate.SeverityError, Detail: err.Error()}}
	}
	gates := []validate.GateResult{fetch}
	if res, ok := a.requiredUseGate(cand, pkg, version); ok {
		gates = append(gates, res)
	}
	return gates
}
