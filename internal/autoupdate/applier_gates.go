package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// This file is the validation pipeline Apply runs between the manifest step and
// promotion. Classification, the depth policy, staging, the static gates, the
// reviewer, the dependency pre-check and the build gates are inert until they
// sit HERE, on the path the operator's own `--apply` takes.
//
// # The whole pipeline is on the STAGED path and only there
//
// Every function below answers "nothing to do" for a candidate that was not
// staged. That is the contract WithApplierStagingRoot states: a caller with no
// staging root keeps the old behaviour — the candidate written straight into the
// overlay and rolled back on failure — and gates bolted onto that path would
// validate a file that is ALREADY published, the very inversion staging removes.
//
// # Why the reasons are so wordy
//
// Every SKIPPED outcome carries a sentence naming what stopped it and what the
// operator can do about it. A skip nobody can read is a pass (validate's own
// rule), and the failure mode to avoid is a green that claims more than it
// measured.

// gateDepths maps a gate back to the rung of the ladder it PROVES. It is the
// inverse of validate's own depth→gate table and exists for one question:
// "how far did validation actually get", which is answered by the
// deepest rung whose own gate passed.
//
// qa and review are absent, and their absence is the rule rather than an
// oversight: neither decides anything, so neither can be evidence
// that a rung was reached.
var gateDepths = map[string]validate.Depth{
	validate.GateOptions:   validate.DepthOptions,
	validate.GatePatches:   validate.DepthPatches,
	validate.GateConfigure: validate.DepthConfigure,
	validate.GateCompile:   validate.DepthCompile,
	validate.GateInstall:   validate.DepthInstall,
}

// RequiresSerialApply reports whether a run validating at depth d must apply one
// package at a time.
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

// SerialApplyRequired asks RequiresSerialApply of a whole batch: does any of
// these pending updates resolve to a depth that starts a build.
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

// depthFor resolves how deep one bump is validated, and why.
//
// The classification goes through validate.ClassifyForDepth rather than
// Classify, so a version this package cannot read cannot be dropped on the floor
// by a reflexive `if err != nil { return }`: it answers ClassMajor for exactly
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
	if ebuilds.DetectBinaryPackage(content) {
		return "bin"
	}
	return "source"
}

// runStaticGates runs the option gate and the advisory QA scan over the STAGED
// candidate, reusing validate's gate rather than reimplementing it.
//
// validate.Run gets the staged tree — a single-package repository holding
// exactly the candidate — as its overlay; a second copy of the distfile rules
// here would be the one that goes stale. A run that could not scan is ONE
// skipped option gate, not an error: a gate that cannot run is an outcome.
//
// # WHOSE archive this is
//
// validate answers "which tarball is this ebuild's" from the package Manifest.
// Without one the gate reports SKIPPED, and SKIPPED promotes — a bump published
// on a gate that read nothing, which is how obentoo/bentoo#33 reached the tree.
// So the names are decided HERE, per bump: a staged Manifest (the manifest child
// really ran) is parsed directly, since the published one describes the release
// being replaced; otherwise the PUBLISHED names travel through the seam as
// values, and nothing is written into the staged tree. The value rides the
// per-CALL Options, never an Applier field: ApplyAll runs applies concurrently,
// and a stored seam would hand package A's archive names to package B.
func (a *Applier) runStaticGates(ctx context.Context, cand candidatePaths, pkg, version string) []validate.GateResult {
	if !cand.staged {
		return nil
	}

	opts := validate.Options{
		Overlay:  cand.repoRoot,
		Distdir:  a.staticGateDistdir(cand),
		Selector: pkg,
		Logger:   a.logger(),
	}
	if !stagedTreeNamesArchive(cand) {
		opts.DistNames = publishedDistNames(a.overlayPath, pkg, version)
	}

	report, err := validate.Run(ctx, opts)
	if err != nil {
		// Unstamped, and the cause is genuinely not one thing.
		// validate.Run returns an error for a run the operator INTERRUPTED — the
		// route refuseOnInterrupt exists for, and it says so at its own guard —
		// and for a staged tree ScanOverlay could not walk, which is this
		// machine's filesystem at least as often as it is the tree's contents.
		// All this site holds is the error; telling those apart would mean
		// classifying it by its text, which is never evidence.
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
	// Unstamped. The scan ran and reported no result for this
	// version, which is either the staged tree really not holding the candidate —
	// the candidate's — or this applier and the scan spelling the version
	// differently, say over a revision suffix, which is a fault in bentoo and not
	// in the ebuild. The two arrive here identically, and blaming a candidate for
	// the tool's own disagreement is the error DeclineCandidate is least able to
	// afford: it refuses a bump. Where the tree's failure to appear IS the
	// candidate's, the site that staged it already says so.
	return []validate.GateResult{{
		Gate:    validate.GateOptions,
		Outcome: validate.OutcomeSkipped,
		Reason: fmt.Sprintf("the staged tree of %s holds no ebuild for version %s, so the option gate had nothing to read",
			pkg, version),
	}}
}

// staticGateDistdir names the directory the option gate reads archives from.
//
// The run's OWN fetch comes first: on a host that had not already fetched this
// release, the private directory the manifest step downloaded into holds the
// ONLY copy. Reading the shared distdir instead handed the gate the PREVIOUS
// release's archive, which it rightly declined — and the resulting SKIPPED
// promoted a bump nothing had read.
//
// The fall-back keeps this command's precedence: --distdir, then
// autoupdate.distdir, then the host's DISTDIR via validate.Run's own
// distfiles.Locate. Nothing is created or written on any branch — the gate only
// opens archives already on disk, hence Locate rather than Resolve, and the host
// DISTDIR is still only ever read.
//
// An EMPTY private directory means "nothing was fetched", not "nothing is
// there": it is created before the manifest step, so it exists even when the
// step brought nothing back (nothing to download, or stubbed out). Preferring
// it would hand the gate an empty room with the shared distdir, which does hold
// the archive, unread — so the fall-back triggers on it being empty.
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
// step really wrote a Manifest there.
//
// It is the ONE place that rule is written, and it is deliberately "names an
// archive" rather than "a file called Manifest exists": a Manifest holding only
// AUX or EBUILD records names no tarball, so treating its presence as an answer
// would leave the gate with nothing to look for and no seam to ask instead.
func stagedTreeNamesArchive(cand candidatePaths) bool {
	return len(distfiles.ParseManifestDistFilenames(filepath.Join(cand.pkgDir, "Manifest"))) > 0
}

// publishedDistNames is the per-bump seam value the option gate reads its
// candidate archive names from when the staged tree names none of its own. It
// answers with BASENAMES — never Manifest lines.
//
// The published Manifest is a legitimate source of NAMES, never of hashes: the
// gate decides on the archive's CONTENTS, and selectDistfile still requires
// exactly one present name carrying THIS version before it reads a byte. It is
// the same record presentArchive and the manifest step's prefetch already use.
// A DIST line ("<name> <size> BLAKE2B … SHA512 …") handed over verbatim would
// miss every os.Stat and report a silent SKIPPED, so the shared parser answers.
//
// An unreadable Manifest is an ERROR here, on purpose. A non-nil seam returning
// no names is AUTHORITATIVE — "this package publishes no archive" — and the gate
// answers on it without falling back, so swallowing a read error into an empty
// slice would report a claim nothing measured. The error travels instead, and
// validate turns it into a SKIPPED carrying namingFailure's words verbatim.
func publishedDistNames(overlayPath, pkg, version string) func(string) ([]string, error) {
	// The pkgDir the gate asks about is ignored: this value is built for ONE bump
	// and rides the Options of the one Selector-scoped run that validates it, so
	// the only directory it can ever be asked about is that candidate's.
	// Answering some other directory out of this package's Manifest is
	// exactly what the per-package func signature exists to prevent.
	return func(string) ([]string, error) {
		published, err := publishedCandidate(overlayPath, pkg, version)
		if err != nil {
			return nil, namingFailure(pkg, version,
				fmt.Errorf("naming the published package directory of %s: %w", pkg, err))
		}
		manifestPath := filepath.Join(published.pkgDir, "Manifest")
		// The parser asked here is the one that can SAY the file was unreadable.
		// ParseManifestDistFilenames answers a missing or unreadable Manifest with
		// an empty slice, which through this seam would be read as an answer rather
		// than as the failure to produce one it is; recovering the distinction used
		// to mean reading the file once to prove it readable and then parsing it
		// again, here and in cmd/bentoo's publishedManifestDistNames alike.
		// ReadManifestDistFilenames reports it from a single read, and the
		// sentence below is unchanged to the byte.
		names, err := distfiles.ReadManifestDistFilenames(manifestPath)
		if err != nil {
			return nil, namingFailure(pkg, version,
				fmt.Errorf("reading the published Manifest of %s: %w", pkg, err))
		}
		return names, nil
	}
}

// namingFailure is the sentence a producer failure reaches the operator in.
//
// It is preserved to the byte from the retired lend's own SKIPPED reason: the
// mechanism changed, the condition did not, and a
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
// gate would leave them to go and diff two tarballs by hand — the very work the
// gates exist to replace.
//
// Error findings only, and deduplicated: a warning or an info did not decide
// anything, and repeating one detail per gate that carried it would bury the
// option name under the repetition.
func refusalWithFindings(reason string, gates []validate.GateResult) error {
	var details []string
	seen := map[string]bool{}
	for _, gate := range gates {
		// The QA gate never decides, so its findings cannot be part of why
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
// declarations and, where it sees a risk, to ask for MORE validation.
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
// Its report is advisory: a skip becomes a SKIPPED review gate
// carrying the reviewer's own sentence, and the depth is untouched. Even the
// error return — reserved by the interface for a programming fault — is recorded
// as a skipped gate rather than surfaced as the apply's failure, because an
// advisory capability that broke must not stop a bump the deterministic gates
// would have passed.
func (a *Applier) reviewBump(ctx context.Context, cand candidatePaths, pkg, oldVersion, newVersion string, floor validate.DepthDecision, gates *[]validate.GateResult) validate.DepthDecision {
	if a.reviewer == nil || !cand.staged {
		return floor
	}

	a.reporter.TaskStage(pkg, "review")
	oldArchive, newArchive := a.reviewArchives(ctx, cand, pkg, oldVersion, newVersion)
	report, err := a.reviewer.ReviewBump(ctx, fixer.BumpReviewRequest{
		Package:    pkg,
		OldVersion: oldVersion,
		NewVersion: newVersion,
		OldArchive: oldArchive,
		NewArchive: newArchive,
	})
	if err != nil {
		// Unstamped: the reviewer is an optional capability outside
		// this process, so a failure to ask it is a missing credential, a provider
		// that answered nothing, a transport that broke — none of them a fact
		// about the ebuild, and none of them "this machine cannot build it"
		// either. DeclineCause names two causes, the host's and the candidate's,
		// and this belongs to neither; inventing a third for an advisory gate is
		// not worth its cost. What it must NOT be is the candidate's:
		// PromotionDecision excludes only the QA gate, so a candidate stamp here
		// would let an unreachable reviewer refuse a bump the deterministic gates
		// never got to judge — authority an advisory reviewer must never have.
		reason := fmt.Sprintf("the bump reviewer could not be asked about %s-%s: %v", pkg, newVersion, err)
		a.logger().Debug("the bump reviewer could not be asked", "package", pkg, "version", newVersion, "err", err)
		*gates = append(*gates, validate.GateResult{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: reason})
		return floor
	}

	if report.Skipped {
		// Unstamped for the same reason and one of its own: this skip
		// is the REVIEWER's own answer, and the only account of why it declined is
		// the prose it chose for SkipReason. Deriving a cause from that string is
		// precisely what putting the cause on the producer exists to avoid — and
		// the producer here is behind an interface, so this file cannot know what
		// its implementations will decline for.
		*gates = append(*gates, validate.GateResult{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: report.SkipReason})
		return floor
	}

	// A review that ran is a PASS whatever it found: its findings are clamped to
	// info or warning, so the gate itself never decides — it reports.
	*gates = append(*gates, validate.GateResult{
		Gate:     validate.GateReview,
		Outcome:  validate.OutcomePass,
		Reason:   fmt.Sprintf("the bump reviewer read the build-declaration difference of %s-%s", pkg, newVersion),
		Findings: report.Risks,
	})

	if report.ProposedDepth == nil {
		// Proposing nothing and proposing `none` are different facts, which
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
func (a *Applier) reviewArchives(ctx context.Context, cand candidatePaths, pkg, oldVersion, newVersion string) (oldArchive, newArchive string) {
	distdir, ok := distfiles.Locate(ctx, a.distdir, a.configuredDistdir)
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

// hostDeclinedGates is validate.SkippedGates with the cause stamped on every
// gate: THIS HOST could not answer.
//
// The cause is data rather than prose because validate.PromotionDecision now
// REFUSES a bump whose every deciding gate declined over the CANDIDATE, and
// these two skips are the other side of that rule — the machine is missing
// something, the ebuild is not. Untagged they would read as "cause unrecorded",
// which keeps today's answer today; stating it is what keeps the applier right
// if the unrecorded default is ever tightened.
//
// It stamps the field rather than calling a validate-side constructor because
// SkippedGates' exported signature is fixed: outside callers hold it, and which
// gates a depth owes an outcome for must keep having exactly one definition.
//
// candidateDeclinedGates just below is its mirror for the other cause. The two
// are deliberately separate functions rather than one that takes the cause: see
// its own note for why merging them would put "stop publishing on every
// workstation that cannot build" one word away from a caller.
func hostDeclinedGates(depth validate.Depth, reason string) []validate.GateResult {
	gates := validate.SkippedGates(depth, reason)
	for i := range gates {
		gates[i].Declined = validate.DeclineHost
	}
	return gates
}

// candidateDeclinedGates is the other half of that pair: validate.SkippedGates
// with the cause stamped as THIS CANDIDATE's.
//
// It answers for a staged tree that could not be prepared and a manifest step
// that failed — faults OF THE BUMP: no gate ever opened the ebuild, which is the
// vacuity validate.PromotionDecision refuses. Unstamped, they would look like a
// host merely lacking a build dependency, which must keep promoting. validate's
// own core (run.go) already answers these two conditions with DeclineCandidate;
// a cause that depended on the route a bump took would be no cause at all.
//
// It refuses nothing TODAY (Validate serves only `--check`, the apply path
// returns through failApply first, and a StageRecord drops Declined). It is
// written anyway because the cause is known at the producer and nowhere else;
// downstream it could only be guessed from a Reason string.
//
// It is a second function, not one taking the cause, because the wrong argument
// is catastrophic one way: `candidate` where the host was meant stops `--apply`
// on every workstation lacking a bump's build dependencies. Here that mistake is
// a whole function away, not one word.
func candidateDeclinedGates(depth validate.Depth, reason string) []validate.GateResult {
	gates := validate.SkippedGates(depth, reason)
	for i := range gates {
		gates[i].Declined = validate.DeclineCandidate
	}
	return gates
}

// runBuildGates runs the gates that need the sources unpacked — patches,
// configure, compile — at the selected depth, and reports one outcome per gate
// the depth covers.
//
// Portage is asked BEFORE anything is built. `ebuild … configure` never installs
// dependencies, so a bump whose dependencies are absent on this host would fail
// configure for a reason unrelated to the bump — a confident FAILED nobody can
// trust across a whole-registry sweep. The pretend resolve separates "the bump
// is broken" from "this host cannot build it": unsatisfied names the atoms to
// install, undetermined names the probe that could not answer.
//
// The error return means the APPLY failed, never that a gate reported FAILED (a
// FAILED gate is data PromotionDecision refuses on). A non-nil error is the
// build CHILD failing before any covered phase began; that must fail the apply
// rather than promote on a list of skips, as the shipped compile gate already
// does — a generalised gate must not be more permissive than the one it
// generalises.
func (a *Applier) runBuildGates(ctx context.Context, cand candidatePaths, pkg, version string, depth validate.Depth, result *ApplyResult) ([]validate.GateResult, error) {
	if !cand.staged || depth <= validate.DepthOptions {
		// Below DepthPatches nothing is built, so there is no build gate to
		// report — an empty list, not a hollow pass. It is the same threshold
		// RequiresSerialApply reads, and deliberately so: the depths that start a
		// build are exactly the depths that must not run concurrently.
		return nil, nil
	}

	// The SECOND host pre-check, and it sits AHEAD of the dependency
	// probe because it is both the cheaper refusal and the more certain one: the
	// probe spawns `emerge -p` to learn something about this host, while this
	// reads back something this host already wrote about itself. Ordering them
	// the other way would pay for a resolve whose answer cannot change the
	// outcome.
	//
	// It sits BELOW the early return for the mirror reason: at the depths above
	// it nothing is built, so there is no build to decline — an empty list is
	// the honest answer there, and a skip would be one more outcome the
	// promotion report has to explain about a gate that was never going to run.
	//
	// hostDeclinedGates, not candidateDeclinedGates: an unreadable key is this
	// MACHINE's, and the stamp is what makes the bump read as a package this
	// host could not measure rather than as an errored one, without the tally
	// logic learning a new case.
	if path, unmet := a.unmetPrecondition(pkg); unmet {
		return hostDeclinedGates(depth, fmt.Sprintf(
			"%s is not readable by the build user, so no build phase was run for %s-%s: the last build failed on it and this run declines rather than buy the same failure again; make it readable by the %s group and the gate runs on the next run, with no flag to set and nothing to wait for",
			path, pkg, version, portageGroupName)), nil
	}

	deps := a.buildDeps(nil)
	satisfied, missing, err := validate.DependenciesSatisfied(ctx, cand.repoRoot, pkg, version, deps)
	switch {
	case err != nil:
		// UNDETERMINED. The caller still skips, but must NOT name a missing
		// dependency, because it does not know of one.
		//
		// Declined = host. Without it PromotionDecision would read this
		// all-SKIPPED list as a candidate nothing measured and REFUSE the bump —
		// conflating "this host cannot tell" with "the bump is unproven". The probe failing to
		// answer is this machine's problem, not the ebuild's.
		return hostDeclinedGates(depth, fmt.Sprintf(
			"whether this host holds the build dependencies of %s-%s could not be determined, so no build phase was run: %v",
			pkg, version, err)), nil
	case !satisfied:
		// Determined and unsatisfied. The atoms are named because
		// they are the operator's next action, and the promotion report says the
		// depth this bump therefore did not reach.
		//
		// Declined = host, the plainest host case: the operator's next
		// action is `emerge` on THIS BOX. Refusing here would make
		// `overlay autoupdate --apply` inert on any workstation that does not
		// already hold the bump's build dependencies, which is most of them.
		return hostDeclinedGates(depth, fmt.Sprintf(
			"this host does not hold the build dependencies of %s-%s, so no build phase was run; install %s to validate it here",
			pkg, version, strings.Join(missing, ", "))), nil
	}

	// The child's own failure, captured through the runner seam. RunBuildGates
	// deliberately reports a failing build as gates plus a nil error, so the only
	// place the exit status is observable is the runner this applier supplies.
	var attempt buildAttempt
	deps.RunAttached = a.recordingRunner(ctx, &attempt)

	req := validate.BuildRequest{
		StagedRoot:       cand.repoRoot,
		Key:              pkg,
		Version:          version,
		Depth:            depth,
		RequireIsolation: a.requireIsolation,
		LogDir:           a.logsDir,
		// The SAME directory the static gate reads, resolved by the same helper.
		// The question staticGateDistdir answers is "which distdir
		// holds this apply's archive" — the run's own fetch first, the shared one
		// when that fetch brought nothing back — and the build has exactly that
		// question: the manifest step downloaded the candidate's tarball into the
		// private directory, so a build pointed anywhere else would fetch it a
		// second time, or fail with the archive already on disk a directory away.
		// Two resolvers here would be the same decision made twice, and the first
		// run where they disagreed would gate a tarball the option gate never
		// read.
		Distdir: a.staticGateDistdir(cand),
	}
	gates, err := validate.RunBuildGates(ctx, req, deps)
	if err != nil {
		// The REQUEST could not be attempted — a malformed atom, no version, no
		// staged tree. Reporting it as a failed bump would blame the ebuild for a
		// caller's bug, so it is wrapped and surfaced as this apply's own failure.
		return nil, fmt.Errorf("running the build gates for %s-%s: %w", pkg, version, err)
	}
	if attempt.err == nil {
		return gates, nil
	}

	fixed, fixErr := a.repairBuildGatesAndRerun(ctx, cand, pkg, version, req, deps, attempt, result)
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
func (a *Applier) recordingRunner(ctx context.Context, into *buildAttempt) func(cmd *exec.Cmd) ([]byte, error) {
	return func(cmd *exec.Cmd) ([]byte, error) {
		output, err := a.runAttached(cmd)
		// The build runs in procgroup's group mode, whose WaitDelay also runs
		// after a NORMAL exit: a build that exited 0 while a helper it left behind
		// still held the output pipe comes back as exec.ErrWaitDelay. RunBuildGates
		// reads that as the success it is; recorded raw here, it would
		// send a passing build into repairBuildGatesAndRerun as a failure.
		err = procgroup.Result(cmd, err)
		into.transcript = string(output)
		if err != nil {
			// As compileOnce applies it: a build its context stopped
			// says nothing about the ebuild, so it is never labelled a compile
			// failure. RunBuildGates returns the interrupt as its own error before
			// anything reads this attempt; the label stays honest regardless.
			if ctxErr := ctx.Err(); ctxErr != nil {
				into.err = fmt.Errorf("the build was interrupted, so it says nothing about this ebuild: %w", ctxErr)
				return output, err
			}
			into.err = fmt.Errorf("%w: %w", ErrCompileFailed, err)
		}
		return output, err
	}
}

// repairBuildGatesAndRerun is what happens after the build gates' child has
// failed once: the failure is attributed, and only if it is the EBUILD's does an
// agent get to see it — after which the SAME gates run again and that re-run is
// the verdict.
//
// It is repairBuildAndRerun's twin for the depth-driven gates rather than a
// second policy: the attribution rungs, their order and the refusal are the same
// functions, so a machine fault is diagnosed identically whether the build was
// reached through `--compile` or through a configure-depth policy. What differs
// is only what is re-run — RunBuildGates rather than one privileged `ebuild
// compile` — and that difference is the point: the gate that decides is the
// gate that failed.
func (a *Applier) repairBuildGatesAndRerun(ctx context.Context, cand candidatePaths, pkg, version string, req validate.BuildRequest, deps validate.BuildDeps, first buildAttempt, result *ApplyResult) ([]validate.GateResult, error) {
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
		deps:        a.buildDependencyAnswer(ctx, cand, pkg, version),
		buildTmpdir: fixSandboxRoot(ctx),
	}
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, paid); machineErr != nil {
		return nil, machineErr
	}

	gate := gateForDepth(req.Depth)
	fixLine := fmt.Sprintf("the %s gate failed for %s-%s; invoking the LLM build fixer to repair the staged ebuild", gate, pkg, version)
	a.logger().Info("gate failed; invoking the LLM build fixer to repair the staged ebuild",
		"gate", gate, "package", pkg, "version", version)
	a.reporter.TaskStage(pkg, "llm-build-fix")
	a.reporter.Log("info", fixLine)

	fixRes, fixErr := a.buildFixer.FixBuild(ctx, fixer.BuildFixRequest{
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

	// The authoritative re-run: bentoo's own build of the same gates, never
	// the agent's account of what it did.
	a.reporter.TaskStage(pkg, "re-check")
	var second buildAttempt
	deps.RunAttached = a.recordingRunner(ctx, &second)
	gates, err := validate.RunBuildGates(ctx, req, deps)
	if err != nil {
		return nil, fmt.Errorf("re-running the build gates for %s-%s after the LLM fix: %w", pkg, version, err)
	}
	if second.err != nil {
		return nil, fmt.Errorf("%w (the build fixer edited the staged ebuild and the %s gate still failed on the re-run: %v)",
			first.err, gate, second.err) //nolint:errorlint // secondary error is context; wrapping it would let errors.Is match it
	}

	result.Fixed = true
	result.FixSummary = summary
	repaired := fmt.Sprintf("LLM build fixer repaired %s-%s using %s: %s", pkg, version, fixer.FormatModelUsed(fixRes.Model), summary)
	a.logger().Info("LLM build fixer repaired the staged ebuild",
		"package", pkg, "version", version, "model", fixer.FormatModelUsed(fixRes.Model), "summary", summary)
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
// refuseUnproved read the privileged gate exactly as they read the depth-driven
// ones.
//
// It is a SKIPPED and not a PASS when --require-isolation refused the build:
// runCompile answers ("", nil) there, and a caller cannot tell that from a pass —
// a silence that must not be reproduced here.
//
// The PASS carries the same fidelity statements its unprivileged sibling does:
// one word cannot describe two different amounts of evidence, so it states
// which DISTDIR it enforced and whether isolation was verified, in gateFor's
// order and BY CALLING gateFor's own note-producing functions — one copy of each
// sentence. It does NOT route through buildRun.gateFor itself: that derives an
// outcome from a transcript's phase markers, and this path already knows its
// exit status.
func (a *Applier) compileGateResult(cand candidatePaths, pkg, version string, result *ApplyResult) []validate.GateResult {
	if !cand.staged {
		return nil
	}
	if a.requireIsolation && !result.IsolationVerified {
		// Unstamped, though the honest answer is not that the cause is unknown.
		// It is the HOST's, by DeclineCause's own example: the probe answered
		// about this machine's ability to create a namespace, and "no privilege
		// to isolate" is the case DeclineHost names. It is left alone because
		// only the two CANDIDATE faults are stamped, and nothing reads DeclineHost
		// today — it is a claim held for a future tightening. Marking it host
		// would be correct and is a decision for whoever next rewrites this gate;
		// what is not in question is that it is not the candidate's.
		return []validate.GateResult{{
			Gate:    validate.GateCompile,
			Outcome: validate.OutcomeSkipped,
			Reason: fmt.Sprintf("isolation was required for %s-%s and this host could not provide it, so the compile gate did not run: %s",
				pkg, version, result.IsolationReason),
		}}
	}
	// The verdict's own sentence is untouched — this adds fidelity, it does not
	// reword what the gate concluded.
	reason := fmt.Sprintf("the compile phase completed for %s-%s; a compile pass does not cover src_install, which this ladder deliberately stops short of",
		pkg, version)

	// ORDER IS DELIBERATE, and it is gateFor's (validate/build.go): the isolation
	// note CLOSES by warning that an unisolated build could have reached past the
	// directory, so the sentence naming that directory has to be read first for
	// the caveat to have an antecedent. The isolation note is appended
	// unconditionally because it returns "" on a verified run, which is what keeps
	// the label a signal rather than decoration under every gate on every host.
	reason += compileDistdirNote(result)
	reason += validate.IsolationFidelityNote(result.IsolationVerified, result.IsolationReason)

	return []validate.GateResult{{
		Gate:    validate.GateCompile,
		Outcome: validate.OutcomePass,
		Reason:  reason,
	}}
}

// compileDistdirNote is the distdir half of the privileged PASS's fidelity, and
// the one place that path has a state its unprivileged sibling does not.
//
// Two states are shared and answered by validate's own sentence: no directory
// was resolved (Portage answers from its own configuration), or one was and the
// privilege tool carried it into the child.
//
// The third state is this path's own: a resolved directory that cannot reach the
// build. privilegedDistdirArgs carries it as `sudo DISTDIR=<dir> ebuild …`, and
// `doas` has no VAR=value form at all. Silence would imply a hermeticity this run
// never had, and the enforced sentence would claim an export that did not
// happen — so the note says the directory could not be enforced, and names it
// for the operator who wants the enforcement.
//
// IT IS STILL A PASS: the compile phase completed; what the privilege tool could
// not carry changes what the pass may CLAIM, not whether the ebuild built.
func compileDistdirNote(result *ApplyResult) string {
	switch {
	case result.CompileDistdir == "":
		return validate.DistdirEvidenceNote("")
	case result.CompileDistdirEnforced:
		return validate.DistdirEvidenceNote(result.CompileDistdir)
	default:
		return fmt.Sprintf("; this run resolved %s for the build's archives but the privilege tool it escalated through "+
			"has no argument form that carries an environment assignment, so the directory could not be enforced and "+
			"the privileged Portage answered from its own configuration instead", result.CompileDistdir)
	}
}

// recordDepthReached states, on the result, how far validation actually got and —
// when that is short of what was asked for — why.
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

// refuseUnproved enforces require_proof: where configuration requires proof at
// the selected depth, a bump whose build gates were SKIPPED is refused instead
// of published with the unreached depth named.
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
func (a *Applier) refuseOnInterrupt(ctx context.Context, pkg, version string) error {
	ctxErr := ctx.Err()
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
