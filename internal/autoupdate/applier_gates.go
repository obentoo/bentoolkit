package autoupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// This file is the validation pipeline Apply runs between the manifest step and
// promotion (S033-12.1). Everything blocks A and B of story 033 built —
// classification, the depth policy, staging, the static gates, the reviewer, the
// dependency pre-check and the build gates — is inert until it sits HERE, on the
// path the operator's own `--apply` takes.
//
// # The whole pipeline is on the STAGED path and only there
//
// Every function below answers "nothing to do" for a candidate that was not
// staged. That is not caution, it is the contract WithApplierStagingRoot states:
// a caller that supplies no staging root keeps the behaviour it has always had —
// the candidate written straight into the overlay and rolled back on failure —
// and gates bolted onto that path would validate a file that is ALREADY
// published, which is the inversion this whole story exists to remove.
//
// # Why the reasons are so wordy
//
// Every SKIPPED outcome carries a sentence naming what stopped it and what the
// operator can do about it. A skip nobody can read is a pass (validate's own
// rule), and the failure mode this story is about is a green that claims more
// than it measured.

// gateDepths maps a gate back to the rung of the ladder it PROVES. It is the
// inverse of validate's own depth→gate table and exists for one question:
// "how far did validation actually get" (R3.12), which is answered by the
// deepest rung whose own gate passed.
//
// qa and review are absent, and their absence is the rule rather than an
// oversight: neither decides anything (D8, R7.6), so neither can be evidence
// that a rung was reached.
var gateDepths = map[string]validate.Depth{
	validate.GateOptions:   validate.DepthOptions,
	validate.GatePatches:   validate.DepthPatches,
	validate.GateConfigure: validate.DepthConfigure,
	validate.GateCompile:   validate.DepthCompile,
}

// RequiresSerialApply reports whether a run validating at depth d must apply one
// package at a time (S033-D14).
//
// `--compile` already serialises applies, for the prompt and the sudo
// invocation. The rule extends to every depth that STARTS A BUILD, for a reason
// that has nothing to do with prompts: concurrent builds contend for CPU and for
// space under PORTAGE_TMPDIR, measured at 60 MB for one gst configure, and a
// worker pool multiplies that by the pool size on a machine that was never asked.
//
// Depths none and options keep the existing worker pool: both only read files
// that are already on disk, so they are the network-bound manifest step this
// pool exists to overlap and nothing more.
func RequiresSerialApply(d validate.Depth) bool {
	return d > validate.DepthOptions
}

// SerialApplyRequired answers D14 for a whole batch: does any of these pending
// updates resolve to a depth that starts a build.
//
// It resolves each bump through the SAME decision Apply will make, rather than
// guessing from the flags, so the concurrency choice and the gates cannot come to
// disagree about a package. The resolution is pure computation plus one ebuild
// read per package, which is negligible beside the build it may be deciding to
// serialise.
//
// A run with no staging root always answers false: the build gates only exist on
// the staged path, so serialising there would buy nothing and would take away the
// concurrency `--apply all` has always had.
func (a *Applier) SerialApplyRequired(updates []PendingUpdate) bool {
	if a.stagingRoot == "" {
		return false
	}
	for _, update := range updates {
		if RequiresSerialApply(a.depthFor(update.Package, update.CurrentVersion, update.NewVersion).Depth) {
			return true
		}
	}
	return false
}

// depthFor resolves how deep one bump is validated, and why (R2, R2.2, R2.3,
// R2.6, R2.7).
//
// The classification goes through validate.ClassifyForDepth rather than
// Classify, so a version this package cannot read cannot be dropped on the floor
// by a reflexive `if err != nil { return }`: R1.5 answers ClassMajor for exactly
// that case, and losing the class would give the bump NO validation instead of
// the deepest — the precise inversion of what the fallback is for. The note
// travels into the reason so the report says why the deepest rung was chosen.
func (a *Applier) depthFor(pkg, currentVersion, newVersion string) validate.DepthDecision {
	class, note := validate.ClassifyForDepth(currentVersion, newVersion)

	decision := validate.ResolveDepth(validate.DepthRequest{
		Package: pkg,
		Class:   class,
		// The RESOLVED type, never PackageConfig.Type verbatim: that field is
		// empty for most records and the tier is auto-detected from the ebuild,
		// so a resolver reading the raw field would see "" for almost every
		// binary package in the registry and schedule a compile for a prebuilt
		// blob (validate.DepthRequest.ResolvedType).
		ResolvedType: a.resolvedPackageType(pkg, currentVersion),
		Policy:       a.validatePolicy,
		FlagDepth:    a.flagDepth,
	})

	if note != "" {
		decision.Reason = note + "; " + decision.Reason
	}
	return decision
}

// resolvedPackageType classifies pkg as "bin" or "source", mirroring
// Checker.resolveType so that the tier a check reported and the tier an apply
// validates at cannot disagree about one package: an explicit config type wins,
// otherwise the current ebuild is read and auto-detected.
//
// An unreadable ebuild answers "source", which is the checker's own default and
// the safe one here too — a record whose type could not be read keeps every gate
// its class earns rather than being waved through as a prebuilt blob.
func (a *Applier) resolvedPackageType(pkg, currentVersion string) string {
	if declared := a.configs[pkg].Type; declared != "" {
		return declared
	}
	path := a.EbuildPath(pkg, currentVersion)
	if path == "" {
		return "source"
	}
	content, err := os.ReadFile(path) //nolint:gosec // the path is derived from the overlay this applier was constructed for
	if err != nil {
		return "source"
	}
	if detectBinaryPackage(content) {
		return "bin"
	}
	return "source"
}

// runStaticGates runs the option gate and the advisory QA scan over the STAGED
// candidate (R3, story 031's gate reused rather than reimplemented).
//
// validate.Run is given the staged tree as its overlay, which is what makes this
// a reuse and not a copy: that tree is a single-package repository holding
// exactly the candidate, so the run it performs over "the whole overlay" is this
// one ebuild. Re-deriving the option comparison here instead would put the same
// distfile-selection rules in two places, and the copy would be the one that goes
// stale — the defect R12 had to fix once already.
//
// A run that could not scan at all is reported as ONE skipped option gate rather
// than as an error: a gate that cannot run is an outcome with a reason, and
// aborting the apply for it would fail a bump for something that is not about the
// bump.
func (a *Applier) runStaticGates(cand candidatePaths, pkg, version string) []validate.GateResult {
	if !cand.staged {
		return nil
	}

	report, err := validate.Run(a.ctx, validate.Options{
		Overlay:  cand.repoRoot,
		Distdir:  a.staticGateDistdir(),
		Selector: pkg,
	})
	if err != nil {
		return []validate.GateResult{{
			Gate:    validate.GateOptions,
			Outcome: validate.OutcomeSkipped,
			Reason: fmt.Sprintf("the staged tree of %s-%s could not be scanned, so the static gates could not read it: %v",
				pkg, version, err),
		}}
	}

	for _, res := range report.Results {
		if res.Version == version {
			return res.Gates
		}
	}
	return []validate.GateResult{{
		Gate:    validate.GateOptions,
		Outcome: validate.OutcomeSkipped,
		Reason: fmt.Sprintf("the staged tree of %s holds no ebuild for version %s, so the option gate had nothing to read",
			pkg, version),
	}}
}

// staticGateDistdir names the directory the option gate reads archives from,
// with the precedence the rest of this command already uses: the --distdir flag,
// then autoupdate.distdir, then — left to validate.Run's own distfiles.Locate —
// the host's DISTDIR.
//
// Nothing is created and nothing is written: the gate opens archives that the
// manifest step already put on disk, which is why Locate and not Resolve is the
// accessor on the last rung (distfiles D2).
func (a *Applier) staticGateDistdir() string {
	if a.distdir != "" {
		return a.distdir
	}
	return a.configuredDistdir
}

// reviewBump asks the optional LLM reviewer to read the two versions' build
// declarations and, where it sees a risk, to ask for MORE validation (R7, R7.3,
// R7.5).
//
// # It can only ever raise the depth
//
// validate.Escalate applies the rule, and the rule is max(floor, proposed). A
// reviewer reads a diff; it does not carry the authority an operator's flag
// carries, so it may buy scrutiny and may never sell any. There is deliberately
// no path here by which a proposal below the floor takes effect.
//
// # A reviewer that could not run never fails a bump
//
// Its report is advisory (R7.6/R7.7): a skip becomes a SKIPPED review gate
// carrying the reviewer's own sentence, and the depth is untouched. Even the
// error return — reserved by the interface for a programming fault — is recorded
// as a skipped gate rather than surfaced as the apply's failure, because an
// advisory capability that broke must not stop a bump the deterministic gates
// would have passed.
func (a *Applier) reviewBump(cand candidatePaths, pkg, oldVersion, newVersion string, floor validate.DepthDecision, gates *[]validate.GateResult) validate.DepthDecision {
	if a.reviewer == nil || !cand.staged {
		return floor
	}

	a.reporter.TaskStage(pkg, "review")
	oldArchive, newArchive := a.reviewArchives(cand, pkg, oldVersion, newVersion)
	report, err := a.reviewer.ReviewBump(a.ctx, BumpReviewRequest{
		Package:    pkg,
		OldVersion: oldVersion,
		NewVersion: newVersion,
		OldArchive: oldArchive,
		NewArchive: newArchive,
	})
	if err != nil {
		reason := fmt.Sprintf("the bump reviewer could not be asked about %s-%s: %v", pkg, newVersion, err)
		logger.Debug("%s", reason)
		*gates = append(*gates, validate.GateResult{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: reason})
		return floor
	}

	if report.Skipped {
		*gates = append(*gates, validate.GateResult{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: report.SkipReason})
		return floor
	}

	// A review that ran is a PASS whatever it found: its findings are clamped to
	// info or warning (R7.6), so the gate itself never decides — it reports.
	*gates = append(*gates, validate.GateResult{
		Gate:     validate.GateReview,
		Outcome:  validate.OutcomePass,
		Reason:   fmt.Sprintf("the bump reviewer read the build-declaration difference of %s-%s", pkg, newVersion),
		Findings: report.Risks,
	})

	if report.ProposedDepth == nil {
		// R7.7: proposing nothing and proposing `none` are different facts, which
		// is why ProposedDepth is a pointer. Nothing proposed leaves the policy
		// depth exactly as it was, uncommented — crediting a reviewer that decided
		// nothing would turn "escalated by review" into a count of agreements.
		return floor
	}

	depth, reason := validate.Escalate(floor.Depth, *report.ProposedDepth, report.Reason)
	return validate.DepthDecision{Depth: depth, Reason: reason, SkippedByPolicy: floor.SkippedByPolicy && depth <= floor.Depth}
}

// reviewArchives names the two release archives the reviewer compares, when both
// are already on disk. Either may come back empty, and empty is a legitimate
// answer the reviewer renders as a skip that NAMES the archive it wanted — which
// is more use than an anonymous failure.
//
// The two sides are read from different Manifests on purpose: the previous
// version's from the PUBLISHED package directory, which is the only place that
// still describes it, and the candidate's from the staged tree, which is the only
// place that describes it yet.
func (a *Applier) reviewArchives(cand candidatePaths, pkg, oldVersion, newVersion string) (oldArchive, newArchive string) {
	distdir, ok := distfiles.Locate(a.distdir, a.configuredDistdir)
	if !ok {
		return "", ""
	}

	publishedDir := filepath.Dir(a.EbuildPath(pkg, oldVersion))
	return presentArchive(distdir, filepath.Join(publishedDir, "Manifest"), oldVersion),
		presentArchive(distdir, filepath.Join(cand.pkgDir, "Manifest"), newVersion)
}

// presentArchive returns the path of the distfile a Manifest names for exactly
// this version and that distdir actually holds, or "" when there is no single
// unambiguous answer.
//
// Ambiguity answers "" rather than guessing. Handing the reviewer the wrong
// version's tarball would produce a confident opinion about a difference nobody
// asked about, which is worse than no opinion at all.
func presentArchive(distdir, manifestPath, version string) string {
	var found string
	for _, name := range distfiles.ParseManifestDistFilenames(manifestPath) {
		if !strings.Contains(name, version) {
			continue
		}
		if found != "" {
			return ""
		}
		path := filepath.Join(distdir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			found = path
		}
	}
	return found
}

// runBuildGates runs the gates that need the sources unpacked — patches,
// configure, compile — at the selected depth, and reports one outcome per gate
// the depth covers (R3, R5, R6).
//
// # Portage is asked BEFORE anything is built
//
// `ebuild … configure` never installs dependencies: it runs the phase in front of
// it and fails if a header or a build tool is not already here. So a bump whose
// dependencies are simply absent on this host would fail configure for a reason
// that has nothing to do with the bump — a confident FAILED about an ebuild that
// is fine, which over a whole-registry sweep is a report nobody trusts. The
// pretend resolve (design M-D) separates "the bump is broken" from "this host
// cannot build it", and the two answers get different gates: unsatisfied names
// the atoms to install, undetermined names the probe that could not answer.
//
// # The error return means the APPLY failed, never that a gate reported FAILED
//
// A gate that reports FAILED is data, and PromotionDecision refuses the bump on
// it. A non-nil error here is the build CHILD having failed in a way no gate
// could attribute — the run died before any covered phase began — and that must
// fail the apply rather than promote on a list of skips. It is also exactly what
// the shipped compile gate already does with a failing child, and a generalised
// gate must not be more permissive than the gate it generalises.
func (a *Applier) runBuildGates(cand candidatePaths, pkg, version string, depth validate.Depth, result *ApplyResult) ([]validate.GateResult, error) {
	if !cand.staged || depth <= validate.DepthOptions {
		// Below DepthPatches nothing is built, so there is no build gate to
		// report — an empty list, not a hollow pass. It is the same threshold
		// RequiresSerialApply reads, and deliberately so: the depths that start a
		// build are exactly the depths that must not run concurrently (D14).
		return nil, nil
	}

	deps := a.buildDeps(nil)
	satisfied, missing, err := validate.DependenciesSatisfied(a.ctx, cand.repoRoot, pkg, version, deps)
	switch {
	case err != nil:
		// UNDETERMINED. The caller still skips, but must NOT name a missing
		// dependency, because it does not know of one (R6.2).
		return validate.SkippedGates(depth, fmt.Sprintf(
			"whether this host holds the build dependencies of %s-%s could not be determined, so no build phase was run: %v",
			pkg, version, err)), nil
	case !satisfied:
		// R5.3/R3.12: determined and unsatisfied. The atoms are named because
		// they are the operator's next action, and the promotion report says the
		// depth this bump therefore did not reach.
		return validate.SkippedGates(depth, fmt.Sprintf(
			"this host does not hold the build dependencies of %s-%s, so no build phase was run; install %s to validate it here",
			pkg, version, strings.Join(missing, ", "))), nil
	}

	// The child's own failure, captured through the runner seam. RunBuildGates
	// deliberately reports a failing build as gates plus a nil error, so the only
	// place the exit status is observable is the runner this applier supplies.
	var attempt buildAttempt
	deps.RunAttached = a.recordingRunner(&attempt)

	req := validate.BuildRequest{
		StagedRoot:       cand.repoRoot,
		Atom:             pkg,
		Version:          version,
		Depth:            depth,
		RequireIsolation: a.requireIsolation,
		LogDir:           a.logsDir,
	}
	gates, err := validate.RunBuildGates(a.ctx, req, deps)
	if err != nil {
		// The REQUEST could not be attempted — a malformed atom, no version, no
		// staged tree. Reporting it as a failed bump would blame the ebuild for a
		// caller's bug, so it is wrapped and surfaced as this apply's own failure.
		return nil, fmt.Errorf("running the build gates for %s-%s: %w", pkg, version, err)
	}
	if attempt.err == nil {
		return gates, nil
	}

	fixed, fixErr := a.repairBuildGatesAndRerun(cand, pkg, version, req, deps, attempt, result)
	if fixErr != nil {
		return gates, fixErr
	}
	return fixed, nil
}

// recordingRunner wraps this applier's runAttached so the build child's exit
// status and transcript survive RunBuildGates, which by design returns neither.
//
// It is a seam and not a field because the capture is scoped to ONE call: the
// build runs at most once per invocation, and a shared field would make the
// attribution of a concurrent apply depend on which package finished last.
func (a *Applier) recordingRunner(into *buildAttempt) func(cmd *exec.Cmd) ([]byte, error) {
	return func(cmd *exec.Cmd) ([]byte, error) {
		output, err := a.runAttached(cmd)
		into.transcript = string(output)
		if err != nil {
			into.err = fmt.Errorf("%w: %v", ErrCompileFailed, err)
		}
		return output, err
	}
}

// repairBuildGatesAndRerun is what happens after the build gates' child has
// failed once: the failure is attributed, and only if it is the EBUILD's does an
// agent get to see it — after which the SAME gates run again and that re-run is
// the verdict (S033-R8.1, S033-R8.2, S033-R8.5).
//
// It is repairBuildAndRerun's twin for the depth-driven gates rather than a
// second policy: the attribution rungs, their order and the refusal are the same
// functions, so a machine fault is diagnosed identically whether the build was
// reached through `--compile` or through a configure-depth policy. What differs
// is only what is re-run — RunBuildGates rather than one privileged `ebuild
// compile` — and that difference is the point: R8.2 says the gate that decides is
// the gate that failed.
func (a *Applier) repairBuildGatesAndRerun(cand candidatePaths, pkg, version string, req validate.BuildRequest, deps validate.BuildDeps, first buildAttempt, result *ApplyResult) ([]validate.GateResult, error) {
	// The free rung: the transcript this run already holds. Reported to every
	// operator, LLM or not, because the verdict is a fact about the failure and
	// not about the configuration.
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, buildFaultEvidence{transcript: first.transcript}); machineErr != nil {
		return nil, machineErr
	}
	if a.buildFixer == nil {
		return nil, first.err
	}

	// The paid rungs, now that the alternative is a full agent invocation.
	paid := buildFaultEvidence{
		transcript:  first.transcript,
		deps:        a.buildDependencyAnswer(cand, pkg, version),
		buildTmpdir: fixSandboxRoot(),
	}
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, paid); machineErr != nil {
		return nil, machineErr
	}

	gate := gateForDepth(req.Depth)
	fixLine := fmt.Sprintf("the %s gate failed for %s-%s; invoking the LLM build fixer to repair the staged ebuild", gate, pkg, version)
	logger.Info("%s", fixLine)
	a.reporter.TaskStage(pkg, "llm-build-fix")
	a.reporter.Log("info", fixLine)

	fixRes, fixErr := a.buildFixer.FixBuild(a.ctx, BuildFixRequest{
		Package:    pkg,
		Version:    version,
		Gate:       gate,
		StagedDir:  cand.repoRoot,
		EbuildPath: cand.ebuildPath,
		BuildLog:   first.transcript,
		Attempt:    1,
	})
	if fixErr != nil {
		return nil, fmt.Errorf("%w (the LLM build fix attempt failed: %w)", first.err, fixErr)
	}

	// An empty summary is the agent reporting NO CHANGE, and a re-run of an
	// untouched tree can only reproduce the failure it already produced — at the
	// price of a whole second build.
	summary := strings.TrimSpace(fixRes.Summary)
	if summary == "" {
		return nil, fmt.Errorf("%w (the build fixer reported no change, so the %s gate was not re-run)", first.err, gate)
	}

	// R8.2. The authoritative re-run: bentoo's own build of the same gates, never
	// the agent's account of what it did.
	a.reporter.TaskStage(pkg, "re-check")
	var second buildAttempt
	deps.RunAttached = a.recordingRunner(&second)
	gates, err := validate.RunBuildGates(a.ctx, req, deps)
	if err != nil {
		return nil, fmt.Errorf("re-running the build gates for %s-%s after the LLM fix: %w", pkg, version, err)
	}
	if second.err != nil {
		return nil, fmt.Errorf("%w (the build fixer edited the staged ebuild and the %s gate still failed on the re-run: %v)",
			first.err, gate, second.err)
	}

	result.Fixed = true
	result.FixSummary = summary
	repaired := fmt.Sprintf("LLM build fixer repaired %s-%s using %s: %s", pkg, version, FormatModelUsed(fixRes.Model), summary)
	logger.Info("%s", repaired)
	a.reporter.Log("info", repaired)
	return gates, nil
}

// gateForDepth names the deepest gate a depth runs, which is the gate a repair is
// asked to fix. It is spelled through the same table the reach calculation reads,
// so the gate NAMED to the fixer and the gate the authoritative re-run decides on
// cannot drift apart — a fixer told to fix configure followed by a re-run of a
// shallower phase would produce a green that proves nothing.
func gateForDepth(d validate.Depth) string {
	// The zero value is the gate of the shallowest rung that builds anything: a
	// depth below it reaches no build and therefore no repair, so this fallback
	// is unreachable from runBuildGates rather than a guess it relies on.
	gate, best := validate.GatePatches, validate.DepthNone
	for name, rung := range gateDepths {
		if rung <= d && rung > best {
			gate, best = name, rung
		}
	}
	return gate
}

// buildDeps assembles the seams the validate package's build gates run through,
// so no gate reaches exec.CommandContext or the real PATH on its own.
//
// runAttached is taken by the caller rather than set here: the build gates need
// the child's exit status, which RunBuildGates does not return, so the runner is
// the one seam each call wraps for itself (see recordingRunner).
func (a *Applier) buildDeps(runAttached func(cmd *exec.Cmd) ([]byte, error)) validate.BuildDeps {
	return validate.BuildDeps{
		ExecCommand:    a.execCommand,
		RunAttached:    runAttached,
		LookPath:       a.lookPath,
		IsolationProbe: a.isolationProbe,
	}
}

// compileGateResult turns a `--compile` run that got past runCompile into the
// GateResult the rest of the pipeline reasons about, so the ladder's reach and
// R3.13's refusal read the privileged gate exactly as they read the depth-driven
// ones.
//
// It is a SKIPPED and not a PASS when --require-isolation refused the build:
// runCompile answers ("", nil) there, and a caller cannot tell that from a pass —
// which is precisely the silence this story removes rather than reproduces.
func (a *Applier) compileGateResult(cand candidatePaths, pkg, version string, result *ApplyResult) []validate.GateResult {
	if !cand.staged {
		return nil
	}
	if a.requireIsolation && !result.IsolationVerified {
		return []validate.GateResult{{
			Gate:    validate.GateCompile,
			Outcome: validate.OutcomeSkipped,
			Reason: fmt.Sprintf("isolation was required for %s-%s and this host could not provide it, so the compile gate did not run: %s",
				pkg, version, result.IsolationReason),
		}}
	}
	return []validate.GateResult{{
		Gate:    validate.GateCompile,
		Outcome: validate.OutcomePass,
		Reason: fmt.Sprintf("the compile phase completed for %s-%s; a compile pass does not cover src_install, which this ladder deliberately stops short of",
			pkg, version),
	}}
}

// recordDepthReached states, on the result, how far validation actually got and —
// when that is short of what was asked for — why (R3.12).
//
// The "why" is the SKIPPED build gates' own reasons, deduplicated: one skip
// produces one sentence however many gates the cumulative ladder made it cover,
// and printing it three times would bury the atom the operator has to install.
func (a *Applier) recordDepthReached(result *ApplyResult, gates []validate.GateResult, requested validate.Depth) {
	reached := validate.DepthNone
	for _, gate := range gates {
		if gate.Outcome != validate.OutcomePass {
			continue
		}
		if rung, ok := gateDepths[gate.Gate]; ok && rung > reached && rung <= requested {
			reached = rung
		}
	}
	result.DepthReached = reached.String()

	if reasons := skippedBuildReasons(gates); reached < requested && len(reasons) > 0 {
		result.DepthReason += fmt.Sprintf("; %s was NOT reached: %s", requested, strings.Join(reasons, "; "))
	}
}

// refuseUnproved is R3.13: where configuration requires proof at the selected
// depth, a bump whose build gates were SKIPPED is refused instead of published
// with the unreached depth named.
//
// It names what stopped the gate, because without that the operator cannot make
// the bump publishable — "refused, proof required" alone would leave them with a
// sweep that stops and no way to unstick it.
func (a *Applier) refuseUnproved(gates []validate.GateResult, pkg, version string, depth validate.Depth) error {
	if !a.requireProof {
		return nil
	}
	reasons := skippedBuildReasons(gates)
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("not promoted: proof at depth %s is required and the build gates of %s-%s did not run: %s",
		depth, pkg, version, strings.Join(reasons, "; "))
}

// skippedBuildReasons collects the reason of every SKIPPED BUILD gate, in gate
// order and without repeats.
//
// It is restricted to the build gates on purpose. The option gate skips whenever
// the archive is not on disk and the QA gate whenever pkgcheck is absent, and
// neither says anything about whether this bump was BUILT — folding them in would
// make `require_proof` refuse every bump on a host without pkgcheck.
func skippedBuildReasons(gates []validate.GateResult) []string {
	var reasons []string
	seen := map[string]bool{}
	for _, gate := range gates {
		if gate.Outcome != validate.OutcomeSkipped {
			continue
		}
		if rung, ok := gateDepths[gate.Gate]; !ok || rung < validate.DepthPatches {
			continue
		}
		if reason := strings.TrimSpace(gate.Reason); reason != "" && !seen[reason] {
			seen[reason] = true
			reasons = append(reasons, reason)
		}
	}
	return reasons
}
