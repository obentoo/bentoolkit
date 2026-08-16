package validate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// Options is what a run needs, and a run is fully described by it: nothing here
// is discovered.
//
// The plain values come from the command line or the config. The last two do
// not — they are SEAMS, functions the caller supplies so that a package
// directory which cannot answer for itself can still be validated: DistNames
// names the upstream archives the option gate may look for, StagedManifest
// supplies the Manifest a staged tree must carry before Portage will build in
// it. Their zero value is nil, and nil is exactly the behaviour every field of
// this struct had before they existed (S037-R1.2, S037-R2), which is why they
// are listed last: a caller written against the older struct is still
// describing the same run.
type Options struct {
	// Overlay is the tree to validate.
	Overlay string
	// Distdir names the directory to read archives from. Empty falls through to
	// the host's own `portageq distdir` — see distfiles.Locate, which never
	// creates the directory and never proves it writable.
	//
	// There is no configured rung between the two, because this story creates
	// no configuration key; the command passes --distdir here or nothing.
	Distdir string
	// Selector is "", "<category>" or "<category>/<package>". Empty validates
	// every ebuild in the overlay.
	Selector string
	// Depth is how far up the ladder this run goes, spelled the way `--depth`
	// and the config key spell it: none, options, patches, configure or
	// compile, each including every rung before it (R2, R11.1).
	//
	// EMPTY MEANS "options", AND THAT IS THE WHOLE COMPATIBILITY PROMISE
	// (R11.3). Every caller written before the ladder existed leaves this field
	// zero, and each of them asked for exactly the static option gate. ParseDepth
	// refuses "" rather than answering DepthNone, precisely so a mistyped key
	// cannot switch validation off in silence — so the empty-to-options mapping
	// is made HERE, once, instead of being left to each caller to remember.
	//
	// It is a string and not a Depth so the word the operator typed survives into
	// the run and can be quoted back at them when it does not parse.
	Depth string
	// StagingRoot is the directory a run above DepthOptions prepares its staged
	// trees under, <StagingRoot>/<category>/<package>/<version> (design D1).
	//
	// It is unused at or below DepthOptions, which is why a --depth-less run
	// leaves it empty: the static gate reads files that are already on disk and
	// writes nothing, and a scratch directory nothing uses is a directory that
	// should not be created.
	//
	// IT MAY NOT RESOLVE INSIDE THE OVERLAY, and Stage refuses one that does
	// rather than trusting its callers: ScanOverlay walks the overlay
	// category/package deep and Reconcile turns every ebuild no registry pin
	// claims into a deletion candidate, so a staged tree parked under the overlay
	// root would be reported as a defect by this very command and deleted by
	// `overlay autoupdate --clean`.
	StagingRoot string

	// DistNames answers, for ONE package directory, which upstream archives the
	// option gate may look for. It is the seam a caller uses when the directory
	// on disk cannot name them itself — a staged tree is exactly that shape: a
	// fresh single-package repository with no Manifest in it until a manifest
	// step has run, which is a gate reading nothing and reporting SKIPPED
	// (S037-R1.1).
	//
	// # NIL IS NOT AN EMPTY SLICE
	//
	// Nil means NOBODY SUPPLIED ANYTHING: the gate parses pkgDir/Manifest exactly
	// as it did before this field existed, and the report it produces is
	// byte-identical to that one (S037-R1.2). A non-nil function returning no
	// names is an ANSWER — the caller looked, and there is nothing to read — and
	// it is answered on its own authority, never by quietly falling back to a
	// Manifest the caller has already spoken for. Two different facts, so two
	// different values.
	//
	// # Why a func of pkgDir, and never a bare []string
	//
	// One Run walks MANY packages. A flat list would apply one package's archives
	// to the whole overlay, and each ebuild would be answered with whichever name
	// happened to match — a confident verdict about the wrong tarball, which is
	// the failure findDistfile's own notes exist to prevent (S037-D2).
	//
	// # Why the value is per-CALL, and never a field on an applier
	//
	// It is set on the Options of the one run that needs it and goes away with
	// it. An applier that stored the seam once would hand package A's archive
	// names to package B — the shared-mutable-state failure story 035 kept the
	// distdir off the Applier to avoid (S035-D2), reproduced here with names
	// instead of a directory. Concurrent applies make that a race; a sequential
	// one merely makes it wrong later.
	DistNames func(pkgDir string) ([]string, error)

	// StagedManifest answers, for ONE package directory, the Manifest content
	// that package's STAGED tree must carry before a build gate can run in it
	// (S037-R2.1, design D3).
	//
	// # Why the caller owns this and this package cannot
	//
	// RunBuildGates drives `ebuild <staged candidate> clean <phase>`, and Portage
	// refuses an ebuild whose Manifest does not describe its archive. Stage
	// deliberately does not carry the published Manifest across — it describes
	// the versions already published, not the candidate — and the step that
	// GENERATES one, `pkgdev manifest` with its fetch, its timeout and its own
	// repair path, lives on the apply side in package autoupdate. That side
	// cannot be reached from here: applier.go already imports this package, so
	// the import back would be a cycle. The content therefore arrives as a value,
	// from whoever already has it.
	//
	// # NIL MEANS NOTHING TRAVELS
	//
	// Nil is not "supply an empty Manifest": it is the whole build-depth path
	// behaving exactly as it did before this field existed — nothing staged,
	// nothing written, every build gate reported SKIPPED naming what stopped it
	// (S037-R2). A non-nil function is a caller taking responsibility for the
	// bytes, and what it returns is written into the staged tree verbatim,
	// because Portage VERIFIES the digests in them: re-encoding or filtering
	// them here would hand the gates a Manifest they fail on.
	//
	// # Two callers, two different right answers
	//
	// A same-version caller — the standalone `overlay validate --depth` — feeds
	// the PUBLISHED Manifest's bytes, which describe exactly the archive on
	// disk. A bump caller feeds the GENERATED one, because the published digests
	// belong to the release being replaced; feeding them for a bump would answer
	// about a different release, the defect R12 already had to fix once for
	// distfile names.
	//
	// It is a func of pkgDir, and per-CALL rather than a field on an applier, for
	// the two reasons DistNames states above: one Run walks many packages, and a
	// seam stored once hands package A's Manifest to package B (S035-D2).
	StagedManifest func(pkgDir string) ([]byte, error)

	// LogDir is where a FAILED build gate's whole transcript is retained.
	//
	// # What it buys, stated precisely
	//
	// The gate's findings quote only a SUMMARY of the failure. The transcript
	// holds everything the phase printed, and it is often the difference between
	// "configure failed" and a diagnosis — the compiler invocation, the preceding
	// warnings, the exact line of a generated file. A Run caller with no LogDir
	// gets the summary and nothing to fall back on.
	//
	// It does NOT decide whether the findings name the cause: that is
	// failureExcerpt's selection, and it reads the captured transcript in memory,
	// never a file on disk. A gate names the option upstream removed with or
	// without a LogDir. (This field was first added on the opposite belief; the
	// belief was measured and refuted — see failureExcerpt's own note.)
	//
	// # Empty is today's behaviour, and it is still honest
	//
	// Empty retains nothing and the gate's reason says so, so nothing silently
	// degrades — the operator is told the log was not kept rather than left to
	// wonder where it went. Every caller written before this field existed leaves
	// it zero and keeps exactly the reports it had.
	//
	// It is a plain directory and not a func of pkgDir, unlike the two seams
	// above: a log directory is one place for the whole run, not one answer per
	// package, and RunBuildGates already names each log after the atom and
	// version it belongs to.
	LogDir string
}

// depth resolves Options.Depth to a rung of the ladder, mapping the empty
// string to DepthOptions — see the field's own note for why that mapping lives
// here and not in ParseDepth.
func (o Options) depth() (Depth, error) {
	if o.Depth == "" {
		return DepthOptions, nil
	}
	d, err := ParseDepth(o.Depth)
	if err != nil {
		return DepthNone, fmt.Errorf("reading the requested validation depth: %w", err)
	}
	return d, nil
}

// distNameLookup answers, for one package directory, which upstream archives may
// be looked for and in whose words a refusal about them is written.
//
// The source travels WITH the names rather than beside them because the two
// cannot come apart without producing a wrong diagnostic: "the package's
// Manifest names no distfile", said about a directory that has no Manifest,
// sends an operator to fix a file that was never part of the question
// (S037-R1.6).
type distNameLookup func(pkgDir string) ([]string, distNameSource, error)

// distNames is the name source a call must use: the caller's seam when there is
// one, otherwise this package's own Manifest parse.
//
// It is BuildDeps.commandFactory's idiom (deps.go:109) applied to a field on
// Options — normalised at ONE place, so a nil seam can never reach a call site
// and the two sources cannot drift into two selection rules (S037-D2).
func (o Options) distNames() distNameLookup {
	if o.DistNames != nil {
		return func(pkgDir string) ([]string, distNameSource, error) {
			names, err := o.DistNames(pkgDir)
			return names, suppliedSource, err
		}
	}
	return func(pkgDir string) ([]string, distNameSource, error) {
		return manifestDistNames(pkgDir), manifestSource(pkgDir), nil
	}
}

// manifestDistNames is the nil seam's answer: the archives the package
// directory's own Manifest names, parsed exactly as they were parsed before
// Options.DistNames existed.
//
// IT REPORTS NO ERROR, and that is the byte-for-byte promise rather than an
// omission (S037-R1.2). ParseManifestDistFilenames answers a missing or
// unreadable Manifest with an empty slice, and an empty slice is already an
// answer here — selectDistfile's named refusal, in the words story 031 shipped.
// Inventing an error return would replace that reported outcome with a different
// sentence for every package directory that has no Manifest, which is every
// staged tree and precisely the case this story is about.
func manifestDistNames(pkgDir string) []string {
	return distfiles.ParseManifestDistFilenames(filepath.Join(pkgDir, "Manifest"))
}

// stagedManifestLookup answers, for one package directory, the Manifest content
// its staged tree must carry before a build gate can run in it.
type stagedManifestLookup func(pkgDir string) ([]byte, error)

// stagedManifest is the Manifest source a run must use, AND whether it has one
// at all.
//
// It is distNames' normalising idiom (BuildDeps.commandFactory, deps.go:109)
// with one addition, and the addition is the whole difference between the two
// seams. A nil DistNames has an answer of its own — parse the Manifest on disk —
// so normalising it produces a function and the call site never learns there was
// a nil. A nil StagedManifest has NO answer: nothing travels, so nothing is
// staged and nothing is built, and the build-depth path has to be able to take
// that branch WITHOUT calling anything (S037-R2).
//
// The bool carries that fact, and the returned function stays callable on both
// paths so no branch can reach a nil and panic. Asking it on the nil path is
// answered rather than forbidden: no content, no error.
func (o Options) stagedManifest() (stagedManifestLookup, bool) {
	if o.StagedManifest != nil {
		return o.StagedManifest, true
	}
	return func(string) ([]byte, error) { return nil, nil }, false
}

// ebuildTarget is one ebuild the run has to answer for.
type ebuildTarget struct {
	atom    string // category/package
	version string
	dir     string // the package directory, for pkgcheck's cwd
	path    string // the ebuild file
}

// qaResult caches one package's pkgcheck outcome for the length of a run.
type qaResult struct {
	findings []Finding
	outcome  Outcome
	reason   string
}

// Run validates every ebuild the selector names and returns one outcome per
// ebuild version.
//
// # The governing rule
//
// A condition that stops the gate becomes a REPORTED OUTCOME WITH A REASON —
// never a silent pass, and never an aborted run. A missing distfile, a build
// system that is not Meson, an unreadable ebuild: each is one ebuild reporting
// SKIPPED and saying why, while the rest of the run carries on. The only error
// returned is one that makes the whole run meaningless, and there is exactly
// one of those — the overlay itself could not be scanned, so there is nothing
// to report about.
//
// Nothing here reaches the network (R4.4). That is asserted structurally, by a
// test over this package's own imports, because behaviour cannot prove a
// negative.
//
// # The depth is answered, never assumed
//
// Options.Depth selects a rung of the ladder and every rung gets an answer. At
// or below DepthOptions that answer is this file's own static gate. Above it,
// the build gates run against a staged tree when the caller supplied the
// Manifest that tree needs (Options.StagedManifest), and are otherwise reported
// as SKIPPED NAMING WHAT STOPPED THEM rather than left out: the governing rule
// above applies to the ladder itself, and a report that simply omitted the
// configure gate the operator asked for would be the silence this whole package
// exists to remove.
func Run(ctx context.Context, opts Options) (Report, error) {
	// Before the tree is walked: a depth that does not parse makes the whole run
	// meaningless, and answering it costs nothing.
	depth, err := opts.depth()
	if err != nil {
		return Report{}, err
	}

	scan, err := overlay.ScanOverlay(opts.Overlay)
	if err != nil {
		return Report{}, fmt.Errorf("scanning overlay %q: %w", opts.Overlay, err)
	}

	report := Report{Overlay: scan.OverlayPath}

	targets := selectTargets(scan, opts.Selector)
	if len(targets) == 0 && opts.Selector != "" {
		report.UnmatchedSelector = opts.Selector
		return report, nil
	}

	// DepthNone runs no gate at all, so it does not get as far as locating a
	// distdir — but it still reports one result per ebuild, saying that nothing
	// was measured. An empty report would be indistinguishable from a clean one.
	if depth == DepthNone {
		for _, target := range targets {
			report.Results = append(report.Results, unvalidatedResult(target))
		}
		return report, nil
	}

	// The distdir is one answer for the whole run, so it is resolved once. It
	// is only ever READ from: Locate creates nothing and proves nothing
	// writable, which is what lets a validate run work against the portage-owned
	// DISTDIR the invoking user cannot write (design D2).
	//
	//nolint:contextcheck // Locate mirrors Resolve's context-free signature by
	// design (D2) — they are siblings and diverging on this would be worse than
	// the gap. Its only child process is a portageq query bounded by the
	// distfiles package's own timeout, on the last rung of the precedence, and
	// it is reached at most once per run.
	distdir, haveDistdir := distfiles.Locate(opts.Distdir, "")

	// Resolved once, for the whole run, so that every package is answered by the
	// same source and no branch below has to remember that the seam may be nil.
	distNames := opts.distNames()

	qa := map[string]qaResult{}
	for i, target := range targets {
		// Interruption is checked HERE, before anything is staged or spawned.
		//
		// While the build gates could not run this loop was cheap and a late
		// cancellation cost nothing. Now each iteration can stage a tree and spawn
		// `ebuild`, so a whole-overlay `--depth=compile` that the operator stopped
		// would otherwise keep building the rest of the overlay — and, worse,
		// report every remaining package as SKIPPED, a word that reads as "this
		// was considered and found not to apply" rather than "you stopped me".
		//
		// Every remaining package is still REPORTED, because Run's governing rule
		// is that a package in view never goes unmentioned. What changes is that
		// it is mentioned as interrupted.
		if err := ctx.Err(); err != nil {
			// The remaining packages are still LISTED, so a reader of the partial
			// report sees which ones went unexamined rather than having to infer
			// it from a short list. But the run still ends as an error: see the
			// note at the bottom of this loop for why a Report cannot carry this.
			for _, remaining := range targets[i:] {
				report.Results = append(report.Results, interruptedResult(remaining, depth, err))
			}
			return report, fmt.Errorf("the validation run was interrupted before %s-%s, so %d of %d ebuilds "+
				"went unexamined and this report says nothing about them: %w",
				target.atom, target.version, len(targets)-i, len(targets), err)
		}

		res := validateOptions(ctx, target, distdir, haveDistdir, distNames)
		// Before attachQA, so the gates read in ladder order — options, then the
		// build gates, then the advisory QA scan that decides nothing.
		noteBuildDepth(ctx, &res, target, depth, opts)
		attachQA(ctx, &res, target, qa)
		report.Results = append(report.Results, res)

		// Cancelled DURING this package, rather than before it. The check above
		// catches packages the sweep never started; this one catches the package
		// it was in the middle of, whose gates were killed rather than answered.
		//
		// It is an ERROR and not a report, because a Report cannot express this.
		// ExitCode reads error-severity findings, and an interrupted gate has
		// none to give — so a SIGTERM'd `--depth=compile` would render as SKIPPED
		// lines and exit 0, indistinguishable at the shell from a clean sweep
		// that found nothing wrong. An operator scripting this would read a
		// killed run as a pass.
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("the validation run was interrupted after %d of %d ebuilds, so this "+
				"report is partial and says nothing about the rest: %w", len(report.Results), len(targets), err)
		}
	}
	return report, nil
}

// unvalidatedResult is what DepthNone reports for one ebuild: a SKIPPED option
// gate saying that no validation was asked for.
//
// It states the depth on both fields because the request and the reach really
// are the same here — nothing was asked for and nothing ran — which is the one
// case where "as far as it got" needs no explanation.
func unvalidatedResult(target ebuildTarget) EbuildResult {
	res := skippedResult(target.atom, target.version,
		"depth none was requested, so no gate ran and this report says nothing about this ebuild")
	res.Depth = DepthNone.String()
	res.DepthRequested = DepthNone.String()
	return res
}

// interruptedResult is what a package still in the queue reports when the run
// was cancelled before it was reached: nothing ran, and the reason says the run
// was stopped rather than that this package was found wanting.
//
// The depth fields follow noteBuildDepth's own rule rather than a second one:
// they are populated only above DepthOptions, because a --depth-less run must
// still produce the bytes story 031 shipped (R11.3), and an interrupted run is
// not a licence to add keys to that document.
func interruptedResult(target ebuildTarget, depth Depth, err error) EbuildResult {
	reason := fmt.Sprintf(
		"the run was interrupted before %s-%s was validated, so no gate ran and this report says nothing about this ebuild: %v",
		target.atom, target.version, err)

	res := skippedResult(target.atom, target.version, reason)
	if depth > DepthOptions {
		// Every gate the requested depth covers is still listed, for the same
		// reason skippedBuildGates lists them: an unreported gate is
		// indistinguishable from one that passed.
		res.Gates = append(res.Gates, SkippedGates(depth, reason)...)
		res.Depth = DepthNone.String()
		res.DepthRequested = depth.String()
		res.DepthReason = reason
	}
	return res
}

// noteBuildDepth records, on a result the static gate has just produced, that a
// depth above `options` was asked for — and either drives the build gates or
// says why they did not run (R4.4, R6.4, R11.2, S037-R2.1).
//
// # A staged tree needs a Manifest, and this package cannot make one
//
// RunBuildGates drives `ebuild <staged candidate> clean <phase>`, and Portage
// refuses an ebuild whose Manifest does not describe its archive. Stage
// deliberately does not carry the published Manifest across — it describes the
// versions already published, not the candidate — and the step that GENERATES
// one (`pkgdev manifest`, with its fetch, its timeout and its own repair path)
// lives on the apply side, in package autoupdate, which cannot be reached from
// here: applier.go already imports this package, so the import back would be a
// cycle.
//
// So the content is the CALLER'S to supply, through Options.StagedManifest
// (S037-D3). With that seam the tree is staged, the Manifest is materialised
// inside it, and the gates run. Without it nothing travels: nothing is staged,
// nothing is built, and the depth is reported unreached with the reason and the
// command that does reach it — which is what every run did before the seam
// existed.
//
// # Why the gates are never simply left out
//
// Building against a tree Portage refuses would produce a confident FAILED for
// an ebuild that is fine — the false-FAILED failure mode findDistfile's own
// notes describe, and the one that gets a gate switched off. Omitting the gates
// instead would be worse still, because an unreported gate is indistinguishable
// from one that passed. Every branch below therefore reports every gate the
// requested depth covers.
//
// # Why the reach is PASS-only
//
// It mirrors the applier's recordDepthReached exactly: a rung is "reached" when
// its own gate PASSED. Two entry points answering "how far did this get" by
// different rules would make the same bump report two depths.
//
// Below DepthPatches this is a no-op, including for the default rung — a
// --depth-less run must produce the bytes story 031 shipped, and populating a
// field that report leaves empty would change the JSON document (R11.3).
func noteBuildDepth(ctx context.Context, res *EbuildResult, target ebuildTarget, depth Depth, opts Options) {
	if depth <= DepthOptions {
		return
	}

	gates, reason := buildDepthGates(ctx, target, depth, opts)
	res.Gates = append(res.Gates, gates...)

	// Read out of the gate list AFTER the build gates joined it: with the gates
	// able to run, "how far did this get" is no longer a question the option gate
	// alone can answer.
	res.Depth = deepestPassedRung(res.Gates, depth).String()
	res.DepthRequested = depth.String()
	res.DepthReason = reason
}

// buildDepthGates prepares what the build gates need and reports them, plus the
// run-level reason the depth went unreached — empty when the gates themselves
// answered, because each then carries its own.
//
// # The order is the contract, not an accident
//
// Stage, then the Manifest, then the gates. The seam is asked about a staged
// tree that LACKS a Manifest, so the tree has to exist before it is asked; and
// the Manifest has to be in place before `ebuild` reads the tree, or Portage
// refuses the candidate over a file this run was about to write. An
// implementation that probed for `ebuild` first and skipped early would invert
// both, and its skip would be an answer about a tree nobody prepared.
//
// # Every stopping condition is a reported SKIP
//
// Run's governing rule, applied to the four things that can stop a build gate
// before it starts: the candidate could not be read, the tree could not be
// staged, the Manifest could not be PRODUCED (S037-R2.6) or could not be WRITTEN
// (S037-R2.5). None of them is an error out of Run and none of them is silence —
// the rest of the overlay is still validated, and this ebuild says what stopped
// it.
func buildDepthGates(ctx context.Context, target ebuildTarget, depth Depth, opts Options) ([]GateResult, string) {
	manifest, supplied := opts.stagedManifest()
	if !supplied {
		// Nothing travels, so nothing is staged and nothing is built: exactly
		// the bytes every run produced before the seam existed (S037-R2).
		return skippedBuildGates(depth, buildDepthNotRunReason(depth, opts.StagingRoot))
	}

	stagedRoot, err := stageCandidate(target, opts)
	if err != nil {
		// Stage's own sentence already opens with "the staged tree could not be
		// prepared", so this one says what that COST rather than repeating it.
		return skippedBuildGates(depth, fmt.Sprintf(
			"the build gates for %s-%s had no staged tree to run in, so none of them read this candidate: %v",
			target.atom, target.version, err))
	}

	if err := materializeStagedManifest(stagedRoot, target, manifest); err != nil {
		return skippedBuildGates(depth, err.Error())
	}

	// A host that lacks a build dependency is not an ebuild that fails to build,
	// and the two must not arrive at the same verdict. `ebuild` does no
	// dependency resolution at all: it starts the phase, the phase dies on the
	// missing header, and derive reads that as FAILED — blaming the candidate for
	// something only this machine is missing, and exiting 1 on it.
	//
	// The applier has answered this since story 031 (runBuildGates, package
	// autoupdate) and this entry point did not, so the same host could get
	// opposite verdicts for the same package depending on which command asked.
	// The sentences below are the applier's, deliberately word-for-word: two
	// entry points explaining the same condition differently is the divergence
	// one shared helper exists to prevent (S037-R2.1).
	//
	// It runs AFTER the Manifest is in place: the probe resolves the candidate
	// through Portage, which refuses an ebuild whose Manifest does not describe
	// its archive — the very condition this story exists to remove.
	if reason := unbuildableHereReason(ctx, stagedRoot, target, BuildDeps{}); reason != "" {
		return skippedBuildGates(depth, reason)
	}

	gates, err := RunBuildGates(ctx, BuildRequest{
		StagedRoot: stagedRoot,
		Atom:       target.atom,
		Version:    target.version,
		Depth:      depth,
		// The whole transcript, kept for whoever has to go past the summary.
		// Empty is still accepted and the gate's reason still says so.
		LogDir: opts.LogDir,
	}, BuildDeps{})
	if err != nil {
		// RunBuildGates errors about the REQUEST and never about the build, so
		// this is a caller's bug rather than a verdict on the ebuild — and it is
		// still one ebuild reporting why while the run carries on.
		return skippedBuildGates(depth, fmt.Sprintf("the build gates for %s-%s could not be started: %v",
			target.atom, target.version, err))
	}
	return gates, ""
}

// unbuildableHereReason answers whether THIS HOST can build the candidate at
// all, and says why not — or "" when the build gates may proceed.
//
// It is the same question, asked with the same helper and answered in the same
// words, as the applier's runBuildGates (package autoupdate). The two callers
// stay word-for-word aligned on purpose: the answer is about the machine, not
// about the bump, so an operator who sees it from `overlay validate` and from
// `overlay autoupdate` must not have to work out whether they are being told the
// same thing.
//
// Both non-nil answers are a SKIP rather than a FAILED, and the difference
// between them is the operator's next action: unsatisfied names the atoms to
// install, undetermined names the probe that could not answer and names no atom,
// because none is known (mirrors R6.2).
//
// deps is a parameter rather than the BuildDeps{} literal the only production
// caller passes, so that both branches below stay reachable from a hermetic
// test on a host that does have Portage — the same reasoning BuildDeps.LookPath
// is separate from ExecCommand for.
func unbuildableHereReason(ctx context.Context, stagedRoot string, target ebuildTarget, deps BuildDeps) string {
	satisfied, missing, err := DependenciesSatisfied(ctx, stagedRoot, target.atom, target.version, deps)
	switch {
	case err != nil:
		return fmt.Sprintf(
			"whether this host holds the build dependencies of %s-%s could not be determined, so no build phase was run: %v",
			target.atom, target.version, err)
	case !satisfied:
		return fmt.Sprintf(
			"this host does not hold the build dependencies of %s-%s, so no build phase was run; install %s to validate it here",
			target.atom, target.version, strings.Join(missing, ", "))
	}
	return ""
}

// skippedBuildGates renders one stopping condition as buildDepthGates' two
// answers: every build gate the depth covers reporting SKIPPED with the reason,
// and the same sentence on DepthReason so a reader who never opens the gate list
// still learns why the depth went unreached.
func skippedBuildGates(depth Depth, reason string) ([]GateResult, string) {
	return SkippedGates(depth, reason), reason
}

// stageCandidate builds the single-package repository the build gates run in,
// out of the ebuild the overlay actually holds.
//
// The bytes are READ FROM DISK rather than regenerated, for the reason
// StageRequest.EbuildBytes gives: a gate result has to describe a file that
// exists somewhere other than in this process.
func stageCandidate(target ebuildTarget, opts Options) (string, error) {
	body, err := os.ReadFile(target.path) //nolint:gosec // the path comes from scanning the overlay under validation, not from input
	if err != nil {
		return "", fmt.Errorf("reading the candidate ebuild %s: %w", target.path, err)
	}

	return Stage(StageRequest{
		Overlay:     opts.Overlay,
		StagingRoot: opts.StagingRoot,
		Atom:        target.atom,
		Version:     target.version,
		EbuildBytes: body,
	})
}

// materializeStagedManifest puts the Manifest the caller supplied where Portage
// reads one from — the staged tree's own package directory (S037-R2.1, D3).
//
// # It writes inside the staged tree and nowhere else (S037-R2.4)
//
// The path is built from the root Stage returned, and Stage refuses a staging
// root that resolves inside the published overlay (ensureOutsideOverlay). That
// refusal is what makes "never the overlay" a property of the code rather than a
// promise kept by hand: there is no path from here to a published package
// directory to get wrong.
//
// # A staged tree that already carries one is left alone
//
// The caller's bytes answer for a tree that LACKS a Manifest. A tree that has one
// has it because a manifest step wrote it — the apply path's `pkgdev manifest` —
// and overwriting a generated Manifest with the published release's digests
// would replace a measurement with a guess.
//
// The mode is stagedFileMode, 0600: the same stance every file staging writes
// takes, because the tree holds a candidate nobody has reviewed yet.
func materializeStagedManifest(stagedRoot string, target ebuildTarget, manifest stagedManifestLookup) error {
	category, pkg, err := splitStagedAtom(target.atom)
	if err != nil {
		// Unreachable after a successful Stage, which split the same atom through
		// this same function — checked anyway, because "unreachable" is a property
		// of today's call order rather than of this function.
		return fmt.Errorf("naming the staged Manifest of %s-%s: %v", target.atom, target.version, err)
	}
	path := filepath.Join(stagedRoot, category, pkg, "Manifest")

	if _, err := os.Stat(path); err == nil {
		return nil
	}

	// Asked only now, with the tree on disk: the seam answers about a staged tree
	// that lacks a Manifest, and there is no such thing to answer about until
	// Stage has run.
	body, err := manifest(target.dir)
	if err != nil {
		// S037-R2.6, and a DIFFERENT fault from the write failure below: the bytes
		// were never made. The producer's own words travel verbatim because it is
		// the only party that knows what it was attempting, and "could not be
		// produced" on its own sends an operator nowhere.
		return fmt.Errorf("the Manifest content for %s-%s could not be produced, so there was nothing to write "+
			"into the staged tree at %s and no build phase could run against it: %v",
			target.atom, target.version, path, err)
	}
	if len(body) == 0 {
		// A producer that answered with no content has ANSWERED — the same
		// nil-is-not-empty rule DistNames states — and the answer is that this
		// tree cannot be built in. Writing an empty Manifest instead would send
		// `ebuild` at a candidate Portage refuses, and the gate would report a
		// confident FAILED about a bump that may be perfectly fine.
		return fmt.Errorf("the Manifest content supplied for %s-%s is empty, so the staged tree at %s would "+
			"describe no archive and Portage would refuse the candidate before any phase ran",
			target.atom, target.version, path)
	}

	if err := os.WriteFile(path, body, stagedFileMode); err != nil {
		// S037-R2.5. The staged path is named because it is the fact the operator
		// acts on: a full disk, a sealed directory and a tree that was swept away
		// under the run all read alike without it.
		return fmt.Errorf("the Manifest supplied for %s-%s could not be written to %s, so the staged tree "+
			"carries none and no build phase could run against it: %v",
			target.atom, target.version, path, err)
	}
	return nil
}

// gateRungs maps a gate back to the rung of the ladder its PASS proves. It is
// the inverse of buildGates plus the option rung, and it answers exactly one
// question: how far did this run actually get.
//
// qa and review are absent, and their absence is the rule rather than an
// oversight: neither decides anything (D8), so neither can be evidence that a
// rung was reached.
//
// The applier keeps the same table for the same question (gateDepths,
// applier_gates.go:46). Two entry points, one rule — see deepestPassedRung.
var gateRungs = map[string]Depth{
	GateOptions:   DepthOptions,
	GatePatches:   DepthPatches,
	GateConfigure: DepthConfigure,
	GateCompile:   DepthCompile,
}

// deepestPassedRung answers "how far did this get": the deepest rung whose own
// gate PASSED, never deeper than the rung that was asked for.
//
// PASS-ONLY, exactly as the applier's recordDepthReached answers it
// (applier_gates.go:779). A SKIPPED gate measured nothing and a FAILED one
// measured a failure; reading either as reach would let a report claim a depth
// no gate ever proved.
func deepestPassedRung(gates []GateResult, requested Depth) Depth {
	reached := DepthNone
	for _, gate := range gates {
		if gate.Outcome != OutcomePass {
			continue
		}
		if rung, isRung := gateRungs[gate.Gate]; isRung && rung > reached && rung <= requested {
			reached = rung
		}
	}
	return reached
}

// buildDepthNotRunReason is the sentence every skipped build gate of a
// standalone run carries: what stopped it, where its tree would have gone, and
// the command that does run it.
//
// The staging root is named because its absence and its presence are different
// facts. A run given one has a scratch directory ready and is short only the
// manifest step; a run given none was not even plumbed for the depth it was
// asked for, and only the caller can fix that.
func buildDepthNotRunReason(depth Depth, stagingRoot string) string {
	where := "no staging root was given, so there is nowhere to prepare one either"
	if root := strings.TrimSpace(stagingRoot); root != "" {
		where = "its staged tree would be prepared under " + root + ", never in the published overlay"
	}
	return fmt.Sprintf("depth %s was requested and only the static gates ran: a build gate needs a staged tree whose "+
		"Manifest describes the candidate archive, and the manifest step that writes one runs on the apply path (%s); "+
		"run `bentoo overlay autoupdate --apply <package> --depth=%s` to drive the build gates",
		depth, where, depth)
}

// selectTargets resolves the selector against the overlay, returning one target
// per ebuild version (R5.1, R5.2, R5.3, R5.4).
//
// A selector that matches nothing returns no targets. The caller reports that
// on the Report rather than as an error: the run DID produce an answer, and the
// command turns it into exit 2 naming the selector (R5.7).
func selectTargets(scan *overlay.ScanResult, selector string) []ebuildTarget {
	var targets []ebuildTarget

	for _, pkg := range scan.Packages {
		atom := pkg.Category + "/" + pkg.Package
		if !matchesSelector(atom, pkg.Category, selector) {
			continue
		}
		dir := filepath.Join(scan.OverlayPath, pkg.Category, pkg.Package)
		for _, version := range pkg.Versions {
			targets = append(targets, ebuildTarget{
				atom:    atom,
				version: version,
				dir:     dir,
				path:    filepath.Join(dir, pkg.Package+"-"+version+".ebuild"),
			})
		}
	}
	return targets
}

// matchesSelector implements the three selector forms. An empty selector takes
// everything; one without a slash is a category; one with a slash is an atom.
func matchesSelector(atom, category, selector string) bool {
	switch selector {
	case "":
		return true
	case category, atom:
		return true
	default:
		return false
	}
}

// validateOptions runs the option gate over one ebuild.
//
// Every branch that cannot continue returns a SKIPPED naming what stopped it.
// The order — ebuild, then the candidate names, then the distfile, then the
// archive — is cheapest-first, and it also produces the most specific
// diagnostic: an ebuild that cannot be read is reported as exactly that, rather
// than as whichever later step happened to fail second.
//
// distNames arrives already normalised (Options.distNames), so this function has
// no nil seam to defend against and no notion of WHERE the names came from
// beyond the words it is handed to refuse in.
func validateOptions(ctx context.Context, target ebuildTarget, distdir string, haveDistdir bool,
	distNames distNameLookup) EbuildResult {
	passed, err := OptionsFromEbuild(target.path)
	if err != nil {
		return skippedResult(target.atom, target.version, fmt.Sprintf("the ebuild could not be read: %v", err))
	}

	if !haveDistdir {
		return skippedResult(target.atom, target.version,
			"no distdir could be located, so there is no archive to read the upstream options from")
	}

	names, source, err := distNames(target.dir)
	if err != nil {
		// A producer that failed has NOT told us there are no archives — it has
		// told us it could not say. Both stop the gate, and the difference is the
		// sentence the operator reads, so the producer's own words are carried
		// through verbatim (S037-R1.5) and the directory is still named (R1.6).
		return skippedResult(target.atom, target.version,
			fmt.Sprintf("the distfile names for %s-%s could not be produced, so there was no archive to look for in %s: %v%s",
				target.atom, target.version, distdir, err, source.attributed))
	}

	archive, err := selectDistfile(names, source, distdir, target.version)
	if err != nil {
		return skippedResult(target.atom, target.version, err.Error())
	}

	declared, err := OptionsFromArchive(ctx, archive)
	if err != nil {
		if errors.Is(err, ErrBuildSystemUndetermined) {
			return skippedResult(target.atom, target.version, err.Error())
		}
		return skippedResult(target.atom, target.version,
			fmt.Sprintf("the upstream archive could not be read: %v", err))
	}

	return comparedResult(target.atom, target.version, declared, passed)
}

// distNameSource says where one package's candidate distfile names came from,
// and carries the words every refusal about them is written in (S037-R1.6,
// design D6).
//
// # Why the wording is a value and not a constant
//
// The two sources have nothing in common to say. One can point at a FILE and
// quote its path; the other has no file at all, and "the package's Manifest
// names no distfile" said about a staged tree is a wrong answer dressed as a
// diagnostic — it names a fix that does not exist. Passing the words in, instead
// of deciding them at each refusal, is what lets ONE set of selection rules
// serve both sources (S037-R1.1) while each still explains itself.
type distNameSource struct {
	// origin names the source as the SUBJECT of "<origin> names no distfile".
	origin string

	// listed attributes a list of names inside "no distfile <listed> is present
	// in the directory searched". It is its own phrase rather than something
	// derived from origin because the Manifest wording predates this story and is
	// reproduced to the byte (S037-R1.2).
	listed string

	// attributed is the clause appended to the two refusals story 031 wrote with
	// no source in them at all.
	//
	// IT IS EMPTY FOR THE MANIFEST, and that is the byte-for-byte promise rather
	// than an oversight: those two sentences are in reports that have already
	// shipped, and a run that supplies no names must produce today's bytes
	// (S037-R1.2). A run that DOES supply names is new, so its wording is free to
	// say so — which is where R1.6's "source its names came from" is met on the
	// only path that could ever be confused about it.
	attributed string
}

// manifestSource is the nil seam's source: the package directory's own Manifest,
// in the words story 031 wrote and story 035 pinned.
func manifestSource(pkgDir string) distNameSource {
	return distNameSource{
		origin: "the package's Manifest (" + filepath.Join(pkgDir, "Manifest") + ")",
		listed: "named by the Manifest",
	}
}

// suppliedSource is the seam's source. Every sentence it produces carries the
// word "supplied", which is what tells an operator reading a SKIP that this
// package did not choose the names — the caller did, and the caller is where a
// wrong list has to be fixed (S037-R1.6).
//
// # It says where THIS package got them, and stops there
//
// It deliberately does not say what the names are NOT. An earlier wording
// asserted "not read from a Manifest", which was true of this package and false
// of the operator's world: `overlay validate` supplies names it read out of the
// published Manifest, so the sentence denied the existence of the very file that
// had just been read and sent whoever read it somewhere else. What validate can
// honestly say is that the list arrived from outside, and that is all this says.
var suppliedSource = distNameSource{
	origin:     "the distfile list the caller supplied",
	listed:     "the caller supplied",
	attributed: "; the names searched for were supplied by the caller rather than read here",
}

// findDistfile returns the path of the distfile belonging to THIS ebuild
// version, among those the package's Manifest names and distdir actually holds.
//
// It is the nil-seam half of the answer, and one line of it: the Manifest is
// parsed here, and selectDistfile — which serves caller-supplied names under
// exactly the same rules (S037-R1.1) — does the choosing. The signature is
// unchanged because four TestFindDistfile_* cases call it directly, and they are
// the measurement that the Manifest path still behaves as it did.
func findDistfile(pkgDir, distdir, version string) (string, error) {
	return selectDistfile(manifestDistNames(pkgDir), manifestSource(pkgDir), distdir, version)
}

// selectDistfile is the frozen selection core: given the candidate names, where
// they came from, the directory to search and the version to answer for, it
// returns the one archive belonging to this ebuild version — or a refusal naming
// what it declined, where it looked, and whose names those were.
//
// Story 037 changed none of the rules below. What it changed is that they now
// serve caller-supplied names as well as Manifest-parsed ones, from ONE body of
// code rather than two copies that could drift into two answers.
//
// # Why the names are re-validated here
//
// ParseManifestDistFilenames drops any name carrying a path separator
// (distfiles.go:537-541), so on the Manifest path the first loop can never fire.
// Names arriving through Options.DistNames never passed that filter, and
// filepath.Join(distdir, "../elsewhere.tar.gz") resolves happily OUTSIDE the
// directory searched — an archive nobody chose, read as though the gate had
// chosen it (S037-R1.4, design D5).
//
// ONE BAD NAME DECLINES THE WHOLE LIST. Selecting from the rest would answer a
// compromised supplier with a PASS built out of its other names; refusing
// everything costs a gate that could have run, and this package has preferred
// that trade since R12 — a false SKIP is investigated, a false PASS is not.
//
// # Why the version has to be part of the question
//
// One name list serves the whole package directory — one Manifest, or one
// caller's answer for that directory — so a directory holding 1.28.6 and 1.29.2
// names both tarballs. Taking the first present one, which this did until the
// golden test caught it, validated the 1.29.2 ebuild against the 1.28.6
// archive, where aalib and libcaca are still declared, and reported a PASS for
// exactly the bump this gate exists to reject. A wrong archive is worse than no
// archive: it produces a confident answer to a question nobody asked.
//
// # The single-present shortcut had the same defect, in reverse
//
// "Exactly one distfile present, so it must be mine" is only true when nothing
// distinguishes the versions. With a directory holding 1.28.6 and 1.29.2 and a
// distdir holding only 1.29.2's archive, the shortcut handed 1.29.2's tarball to
// the 1.28.6 ebuild and reported FAILED — naming aalib and libcaca, options
// 1.28.6 really does declare. A false FAILED is worse than a false PASS here: it
// blames an ebuild that is correct, and that is how a gate gets switched off.
//
// # The cases, and why two of them refuse to guess
//
//   - One present, its name carrying this ebuild's version: it is the one
//     (R12.1).
//   - One present and no candidate name carrying any version at all: it is the
//     one (R12.2). This is the snapshot and commit-hash naming scheme — with no
//     version anywhere in the names there is no other release for the file to
//     belong to, and this is the case the shortcut exists to serve.
//   - One present, but the candidate names DO distinguish versions and this one
//     is not this ebuild's: SKIPPED naming the file declined (R12.3).
//   - Several present and exactly one carrying the version string: it is the
//     one, and this is the ordinary multi-version package directory.
//   - Anything else — several carrying the version, or none — is reported as
//     SKIPPED naming the candidates. Picking the shortest, or the first, would
//     be a guess, and a guess here is indistinguishable from a measurement in
//     the report it produces.
func selectDistfile(names []string, source distNameSource, distdir, version string) (string, error) {
	// Before anything is joined to distdir, and over the WHOLE list before any of
	// it is used: the same test ParseManifestDistFilenames applies, applied again
	// because these names may never have been through it (S037-R1.4).
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "/\\") {
			return "", fmt.Errorf("the distfile name %q is not a plain file name, so it names nothing in the "+
				"directory searched, %s; the whole list was declined rather than the remaining names read%s",
				name, distdir, source.attributed)
		}
	}

	if len(names) == 0 {
		// The directory is named even though nothing was looked for in it (R4.1).
		// Reading this line, an operator has to be able to tell "the Manifest is
		// empty" from "I searched the wrong place", and a message that mentions no
		// directory at all leaves the second possibility invisible.
		return "", fmt.Errorf("%s names no distfile, so there was no archive to look for in %s", source.origin, distdir)
	}

	var present []string
	for _, name := range names {
		path := filepath.Join(distdir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			present = append(present, name)
		}
	}

	switch len(present) {
	case 0:
		// R4.1. Naming the directory is what separates "this host has never
		// fetched this release" from "the fetch went somewhere else" — and story
		// 035 is the second one, which read as the first for as long as it did
		// precisely because no message said where the search happened.
		return "", fmt.Errorf("no distfile %s is present in the directory searched, %s: %s",
			source.listed, distdir, strings.Join(names, ", "))
	case 1:
		// Safe when this file is this ebuild's (R12.1), or when no candidate name
		// tells releases apart at all (R12.2). Otherwise the shortcut is a guess,
		// and it declines by name (R12.3).
		only := present[0]
		if carriesVersion(only, version) || !anyNameCarriesAVersion(names) {
			return filepath.Join(distdir, only), nil
		}
		return "", fmt.Errorf("the only distfile present in %s is %s, which does not belong to version %s; "+
			"reading it would answer about a different release%s", distdir, only, version, source.attributed)
	}

	// Exact rather than carriesVersion: with several archives present a revision
	// suffix can be the ONLY thing telling two names apart, and this branch is
	// not what R12 changes.
	var matching []string
	for _, name := range present {
		if strings.Contains(name, version) {
			matching = append(matching, name)
		}
	}
	if len(matching) == 1 {
		return filepath.Join(distdir, matching[0]), nil
	}
	return "", fmt.Errorf("cannot tell which of %d distfiles in %s belongs to version %s: %s%s",
		len(present), distdir, version, strings.Join(present, ", "), source.attributed)
}

var (
	// revisionSuffix is Gentoo's -rN package revision.
	revisionSuffix = regexp.MustCompile(`-r[0-9]+$`)

	// versionInDistfileName matches a version-looking token in a distfile name:
	// a numeric run, optionally dotted and optionally v-prefixed, bounded by a
	// separator or the name's edge on BOTH sides.
	//
	// Both boundaries earn their place, and a commit-hash snapshot shows why.
	// Without the left one, `deadbeefcafe1234.tar.gz` reads as versioned because
	// the hash ends in digits followed by a dot. Without the right one,
	// `pkg-1a2b3c4d.tar.gz` reads as versioned or not depending on whether the
	// hash happens to start with a digit — an answer no operator could predict.
	versionInDistfileName = regexp.MustCompile(`(^|[-_])v?[0-9]+(\.[0-9]+)*([._-]|$)`)
)

// carriesVersion reports whether a distfile's name carries this ebuild's
// version (R12.1).
//
// The revision is stripped first. `-rN` counts Gentoo-side rebuilds of the SAME
// upstream tarball, so it never appears in the distfile's name; requiring it
// would make every revbumped ebuild in the overlay decline to validate. That is
// a false SKIP, and a gate that skips silently is as useless as one that lies.
func carriesVersion(name, version string) bool {
	return strings.Contains(name, revisionSuffix.ReplaceAllString(version, ""))
}

// anyNameCarriesAVersion reports whether the candidate names distinguish
// releases at all (R12.2) — whoever supplied them. When they do not, one present
// distfile is the only candidate there could be and the shortcut is safe; when
// they do, taking a name that is not this ebuild's is a guess.
func anyNameCarriesAVersion(names []string) bool {
	for _, name := range names {
		if versionInDistfileName.MatchString(name) {
			return true
		}
	}
	return false
}

// attachQA adds the package's pkgcheck findings to a result.
//
// # Once per package, not once per version
//
// pkgcheck scans a package, not an ebuild file, so every version of one package
// would get an identical answer. Measured at roughly three seconds per
// invocation, a package with five versions would spend twelve of them
// recomputing the same thing — and a whole-overlay run multiplies that by the
// tree. The cache is per run and lives no longer.
//
// # Only where the option gate produced a verdict
//
// An ebuild the option gate skipped has already reported why; adding a QA
// section to it would spend the scan without changing what the operator has to
// do next.
//
// # The QA gate now explains itself
//
// This used to write its reason into the result's single shared Reason field,
// overwriting whatever the option gate had put there — so two gates skipping for
// different causes rendered as one, and the cause the operator read was decided
// by write order. The reason now rides on the QA gate itself, beside the outcome
// it explains, which is what makes both readable at once (R4.4).
func attachQA(ctx context.Context, res *EbuildResult, target ebuildTarget, cache map[string]qaResult) {
	for _, gate := range res.Gates {
		if gate.Gate == GateOptions && gate.Outcome == OutcomeSkipped {
			return
		}
	}

	got, seen := cache[target.dir]
	if !seen {
		findings, outcome, reason := PkgcheckFindings(ctx, target.dir, target.atom)
		got = qaResult{findings: findings, outcome: outcome, reason: reason}
		cache[target.dir] = got
	}

	res.Gates = append(res.Gates, GateResult{
		Gate:     GateQA,
		Outcome:  got.outcome,
		Reason:   got.reason,
		Findings: got.findings,
	})
}
