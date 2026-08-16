package autoupdate

import (
	"errors"
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
//
// # The one input validate.Run cannot derive for itself: WHOSE archive this is
//
// The staged tree is a fresh single-package repository, and validate answers
// "which tarball is this ebuild's" out of the package directory's Manifest. Until
// the manifest step has written one there, the directory names no archive, the
// option gate reports SKIPPED, and R3.3 promotes on SKIPPED — a bump published on
// the strength of a gate that read nothing, which is how obentoo/bentoo#33 reached
// the tree.
//
// So the names are decided HERE, per bump, from cand (S037-R3.1, S037-R3.2):
//
//   - the staged tree already carries a Manifest — the manifest child really ran —
//     so the seam stays nil and the gate parses that file directly. It describes
//     THIS candidate; the published one describes the release being replaced, and
//     answering a bump with it is the defect R12 already had to fix once.
//   - it carries none, so the PUBLISHED names travel through the seam as values.
//     Nothing is written into the staged tree to say them, which is the whole of
//     S037-R3.2 and the reason the lend that used to write a temporary Manifest
//     here no longer exists (design D4).
//
// The value rides the per-CALL Options built from cand and is never a field on
// Applier (S037-D7): applyAllPackages runs applies concurrently, and a seam stored
// once would hand package A's archive names to package B — story 035's D2, with
// names in place of a directory.
func (a *Applier) runStaticGates(cand candidatePaths, pkg, version string) []validate.GateResult {
	if !cand.staged {
		return nil
	}

	opts := validate.Options{
		Overlay:  cand.repoRoot,
		Distdir:  a.staticGateDistdir(cand),
		Selector: pkg,
	}
	if !stagedTreeNamesArchive(cand) {
		opts.DistNames = publishedDistNames(a.overlayPath, pkg, version)
	}

	report, err := validate.Run(a.ctx, opts)
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

// staticGateDistdir names the directory the option gate reads archives from.
//
// The run's OWN fetch comes first (S035-R1.1, R1.2). The manifest step of this
// same apply downloaded the candidate's archive into a private directory, and on
// any host that had not already fetched this release that copy is the ONLY one
// on disk. Reading the shared distdir instead is how the gate came to be handed
// an empty room: the sole archive present was the PREVIOUS release's, sub-task
// 8.1 declined it — correctly, because answering about the wrong tarball is
// worse than not answering — the gate reported SKIPPED, and R3.3's "PASS or
// SKIPPED" promoted a bump nothing had read.
//
// The fall-back keeps the precedence the rest of this command already uses: the
// --distdir flag, then autoupdate.distdir, then — left to validate.Run's own
// distfiles.Locate — the host's DISTDIR. It is what answers for a candidate with
// no manifest step of its own, and for the unstaged path, which never had a
// private directory because it wrote into the shared one all along.
//
// Nothing is created and nothing is written on either branch: the gate opens
// archives that the manifest step already put on disk, which is why Locate and
// not Resolve is the accessor on the last rung (distfiles D2). D3 is untouched —
// the host DISTDIR is still only ever read, and reading a private directory
// instead reads it even less.
//
// # An EMPTY private directory is "nothing was fetched", not "nothing is there"
//
// The preference is conditional on the directory holding something, and that is
// not defensive coding. The private distdir is created before the manifest step
// runs, so it exists on paths where the step brought nothing back: a manifest
// child that had nothing to download, or one stubbed out entirely. Preferring an
// empty directory would hand the gate the same empty room this story exists to
// stop handing it — with the shared distdir, which does hold the archive, right
// there unread. So the fall-back triggers on the directory being empty, which is
// exactly the ToDo's "falling back to the shared one when nothing was fetched".
func (a *Applier) staticGateDistdir(cand candidatePaths) string {
	if holdsAnyFile(cand.fetchedDistdir) {
		return cand.fetchedDistdir
	}
	if a.distdir != "" {
		return a.distdir
	}
	return a.configuredDistdir
}

// holdsAnyFile answers whether a directory has anything in it at all.
//
// An unreadable or absent directory answers false rather than propagating an
// error: the caller is choosing between two directories, and "I could not look"
// and "there was nothing to see" lead to the same choice — read the other one.
func holdsAnyFile(dir string) bool {
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// stagedTreeNamesArchive answers whether the staged package directory already
// names an upstream archive of its own — which is to say whether the manifest
// step really wrote a Manifest there (S037-R3.1).
//
// It is the ONE place that rule is written, and it is deliberately "names an
// archive" rather than "a file called Manifest exists": a Manifest holding only
// AUX or EBUILD records names no tarball, so treating its presence as an answer
// would leave the gate with nothing to look for and no seam to ask instead.
func stagedTreeNamesArchive(cand candidatePaths) bool {
	return len(distfiles.ParseManifestDistFilenames(filepath.Join(cand.pkgDir, "Manifest"))) > 0
}

// publishedDistNames is the per-bump seam value the option gate reads its
// candidate archive names from when the staged tree names none of its own
// (S037-R3.2, design D4). It answers with BASENAMES — never Manifest lines.
//
// # Why the published Manifest is a legitimate source of the NAMES
//
// It is a name lookup and never a hash check. The gate opens the archive and
// reads meson.options out of it, so the answer is decided by the archive's
// CONTENTS; a wrong name yields a wrong answer, which is precisely why
// selectDistfile still has to find exactly one present name carrying THIS version
// before it reads a byte, and reports what it declined otherwise (R12). The
// published Manifest is the same record presentArchive already consults for the
// reviewer and the same one the manifest step derives its prefetch names from, so
// this adds no new notion of which file belongs to a package.
//
// # Names, not the Manifest's own lines
//
// A DIST record is "<name> <size> BLAKE2B … SHA512 …", and the gate looks each
// name it is given up with os.Stat in the directory searched. Handing the lines
// over verbatim would miss on every one of them and report SKIPPED — the silent
// non-answer this story exists to remove, reintroduced through the seam meant to
// remove it. So the shared parser answers, and the applier holds no second copy
// of the DIST-line grammar.
//
// # An unreadable Manifest is an ERROR here, and that is the point
//
// A non-nil seam returning no names is AUTHORITATIVE — "I looked, this package
// publishes no archive" — and the gate answers on it without falling back
// (S037-D2). A package whose published Manifest could not be read has said no
// such thing, so swallowing the read error to an empty slice would put a claim
// in the operator's report that nothing ever measured. The error travels instead,
// and validate turns it into a SKIPPED carrying these words verbatim (S037-R3.5)
// — the same sentence the retired lend produced for the same condition, kept to
// the byte so the outcome an operator has already learned to read did not change
// along with the mechanism underneath it.
func publishedDistNames(overlayPath, pkg, version string) func(string) ([]string, error) {
	// The pkgDir the gate asks about is ignored: this value is built for ONE bump
	// and rides the Options of the one Selector-scoped run that validates it, so
	// the only directory it can ever be asked about is that candidate's
	// (S037-D7). Answering some other directory out of this package's Manifest is
	// exactly what the per-package func signature exists to prevent.
	return func(string) ([]string, error) {
		published, err := publishedCandidate(overlayPath, pkg, version)
		if err != nil {
			return nil, namingFailure(pkg, version,
				fmt.Errorf("naming the published package directory of %s: %w", pkg, err))
		}
		manifestPath := filepath.Join(published.pkgDir, "Manifest")
		// Read once to prove the file is READABLE, because the parser below cannot
		// say so: ParseManifestDistFilenames answers a missing or unreadable
		// Manifest with an empty slice, which through this seam would be read as an
		// answer rather than as the failure to produce one it is.
		if _, err := os.ReadFile(manifestPath); err != nil { //nolint:gosec // the path is derived from the overlay this applier was constructed for
			return nil, namingFailure(pkg, version,
				fmt.Errorf("reading the published Manifest of %s: %w", pkg, err))
		}
		return distfiles.ParseManifestDistFilenames(manifestPath), nil
	}
}

// namingFailure is the sentence a producer failure reaches the operator in.
//
// It is preserved to the byte from the retired lend's own SKIPPED reason
// (S037-R3.5, design D4): the mechanism changed, the condition did not, and a
// report whose wording moves with a refactor costs a reader the ability to
// recognise a case they have seen before.
func namingFailure(pkg, version string, cause error) error {
	return fmt.Errorf("the staged tree of %s-%s names no upstream archive and none could be named for it, "+
		"so the option gate had nothing to read: %w", pkg, version, cause)
}

// refusalWithFindings turns PromotionDecision's verdict into the sentence the
// operator actually reads, by appending WHAT the failing gates found.
//
// PromotionDecision is pure and names only the gate — "not promoted: the options
// gate reported FAILED" — and that is right for a decision function: the findings
// are data on the report, and the standalone command renders them from there.
// An apply has no such report to render. Its whole outcome reaches the operator
// through one error string and one summary line, so a refusal that named only the
// gate would leave them to go and diff two tarballs by hand — which is the work
// this story exists to replace.
//
// Error findings only, and deduplicated: a warning or an info did not decide
// anything, and repeating one detail per gate that carried it would bury the
// option name under the repetition.
func refusalWithFindings(reason string, gates []validate.GateResult) error {
	var details []string
	seen := map[string]bool{}
	for _, gate := range gates {
		// The QA gate never decides (D8), so its findings cannot be part of why
		// this bump was refused — PromotionDecision skipped it for the same reason.
		if gate.Gate == validate.GateQA || gate.Outcome != validate.OutcomeFailed {
			continue
		}
		for _, finding := range gate.Findings {
			detail := strings.TrimSpace(finding.Detail)
			if finding.Severity != validate.SeverityError || detail == "" || seen[detail] {
				continue
			}
			seen[detail] = true
			details = append(details, detail)
		}
	}
	if len(details) == 0 {
		// A gate can fail without a finding — a build gate reports its cause in
		// its Reason. The verdict alone is then the whole truth, and inventing a
		// colon with nothing after it would only look like a lost message.
		return errors.New(reason)
	}
	return fmt.Errorf("%s: %s", reason, strings.Join(details, "; "))
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

// refuseOnInterrupt is the one invariant that dominates every published write:
// nothing derived from a cancelled context may be promoted.
//
// It exists because guarding the VERDICTS is what kept failing. Twice now an
// interrupt has reached promotion by a route the previous fix did not cover:
// through runStaticGates, which turns any validate.Run error — ctx.Err()
// included — into one SKIPPED option gate, and through DependenciesSatisfied,
// which turns a cancelled probe into SkippedGates with a nil error.
// PromotionDecision promotes on PASS-or-SKIPPED and cannot tell either list
// from a real one, because a gate list is the wrong shape for "you stopped me"
// — build.go states exactly that at its own guard.
//
// So the rule is enforced where the overlay is WRITTEN rather than where the
// verdict is reached. A future route that manufactures a promotable gate list
// out of a cancellation is then merely wrong, not publishing.
func (a *Applier) refuseOnInterrupt(pkg, version string) error {
	ctxErr := a.ctx.Err()
	if ctxErr == nil {
		return nil
	}
	return fmt.Errorf("not promoted: the run was interrupted before %s-%s reached the published overlay, "+
		"so nothing recorded here is a verdict on this candidate: %w", pkg, version, ctxErr)
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
