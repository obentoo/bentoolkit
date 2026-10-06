package validate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/logging"
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
// this struct had before they existed, which is why they are listed last: a
// caller written against the older struct is still describing the same run.
type Options struct {
	// Overlay is the tree to validate.
	Overlay string
	// Distdir names the directory to read archives from. Empty falls through to
	// the host's own `portageq distdir` — see distfiles.Locate, which never
	// creates the directory and never proves it writable.
	//
	// There is no configured rung between the two, because no configuration key
	// exists for it; the command passes --distdir here or nothing.
	Distdir string
	// Selector is "", "<category>" or "<category>/<package>". Empty validates
	// every ebuild in the overlay.
	Selector string
	// Depth is how far up the ladder this run goes, spelled the way `--depth`
	// and the config key spell it: none, options, patches, configure, compile
	// or install, each including every rung before it.
	//
	// EMPTY MEANS "options", AND THAT IS THE WHOLE COMPATIBILITY PROMISE.
	// Every caller written before the ladder existed leaves this field
	// zero, and each of them asked for exactly the static option gate. ParseDepth
	// refuses "" rather than answering DepthNone, precisely so a mistyped key
	// cannot switch validation off in silence — so the empty-to-options mapping
	// is made HERE, once, instead of being left to each caller to remember.
	//
	// It is a string and not a Depth so the word the operator typed survives into
	// the run and can be quoted back at them when it does not parse.
	Depth string
	// StagingRoot is the directory a run above DepthOptions prepares its staged
	// trees under, <StagingRoot>/<category>/<package>/<version>.
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

	// RequireIsolation refuses to run a build this host cannot isolate, exactly
	// as BuildRequest.RequireIsolation does for the applier — it IS that field,
	// carried to the one caller that had no way to set it.
	//
	// IT EXISTS BECAUSE ITS ABSENCE WAS A POLICY BYPASS, not a missing feature.
	// `autoupdate.validate.require_isolation` is honoured by the identical gates
	// under `overlay autoupdate`, and those gates were once unreachable from
	// `overlay validate`: every one of them SKIPPED, so nothing
	// this command did could be unisolated. Wiring the seams made them run, and
	// a build path that ignores the setting is not a command missing an option —
	// it is the operator's decision that builds must be isolated, silently not
	// applying to one of the two commands that build.
	//
	// The zero value is false, which is the shipped behaviour of both commands
	// when the key is unset: an unisolated build still runs and its pass
	// is labelled "unverified isolation" rather than refused, because creating
	// the namespace needs privilege an ordinary user does not have.
	RequireIsolation bool

	// DistNames answers, for ONE package directory, which upstream archives the
	// option gate may look for. It is the seam for a directory that cannot name
	// them itself — a staged tree has no Manifest until a manifest step runs, so
	// without it the gate would read nothing and report SKIPPED.
	//
	// NIL IS NOT AN EMPTY SLICE. Nil means nobody supplied anything: the gate
	// parses pkgDir/Manifest and the report is byte-identical to the one before
	// this field existed. A non-nil function returning no names is an ANSWER —
	// the caller looked and there is nothing to read — and is never quietly
	// replaced by a Manifest the caller has already spoken for.
	//
	// It is a func of pkgDir, never a bare []string, because one Run walks MANY
	// packages: a flat list would answer each ebuild with whichever name matched,
	// a confident verdict about the wrong tarball. It is per-CALL, never a field
	// on an applier, because a seam stored once would hand package A's names to
	// package B — a race under concurrent applies, wrong later under sequential
	// ones.
	DistNames func(pkgDir string) ([]string, error)

	// Logger receives the run's diagnostics. Nil discards them.
	Logger *slog.Logger

	// StagedManifest answers, for ONE package directory, the Manifest content
	// that package's STAGED tree must carry before a build gate can run in it.
	//
	// The caller owns it because this package cannot: Portage refuses an ebuild
	// whose Manifest does not describe its archive, Stage deliberately does not
	// copy the published Manifest (it describes the published versions), and
	// the step that GENERATES one, `pkgdev manifest`, lives in package
	// autoupdate, which imports this one — importing back would be a cycle.
	//
	// NIL MEANS NOTHING TRAVELS: nothing staged, nothing written, every build
	// gate SKIPPED naming what stopped it. A non-nil function's bytes are written
	// verbatim, because Portage VERIFIES their digests.
	//
	// A same-version caller (`overlay validate --depth`) feeds the PUBLISHED
	// Manifest, which describes the archive on disk; a bump caller feeds the
	// GENERATED one, since the published digests belong to the release being
	// replaced. It is a per-CALL func of pkgDir for the reasons DistNames gives.
	StagedManifest func(pkgDir string) ([]byte, error)

	// LogDir is where a FAILED build gate's whole transcript is retained.
	//
	// The gate's findings quote only a SUMMARY; the transcript holds everything
	// the phase printed — the compiler invocation, preceding warnings, the exact
	// line of a generated file — and is often what turns "configure failed" into
	// a diagnosis. It does NOT decide whether the findings name the cause: that
	// is failureExcerpt's selection, over the in-memory transcript, so a gate
	// names a removed upstream option with or without a LogDir.
	//
	// Empty retains nothing and the gate's reason says so, so the operator is
	// told the log was not kept rather than left to wonder. It is a plain
	// directory, not a func of pkgDir: one place for the whole run, and
	// RunBuildGates already names each log after its atom and version.
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
// sends an operator to fix a file that was never part of the question.
type distNameLookup func(pkgDir string) ([]string, distNameSource, error)

// distNames is the name source a call must use: the caller's seam when there is
// one, otherwise this package's own Manifest parse.
//
// It is BuildDeps.commandFactory's idiom (deps.go:109) applied to a field on
// Options — normalised at ONE place, so a nil seam can never reach a call site
// and the two sources cannot drift into two selection rules.
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
// omission. ParseManifestDistFilenames answers a missing or unreadable Manifest
// with an empty slice, which already reaches the operator as selectDistfile's
// named refusal. An error return would replace that sentence on every
// directory with no Manifest — which is every staged tree.
//
// It deliberately stays out of the unification onto the error-returning
// distfiles.ReadManifestDistFilenames that cmd/bentoo's
// publishedManifestDistNames and internal/autoupdate's publishedDistNames
// share. Those read a PUBLISHED directory, where an unreadable Manifest is a
// fault; this reads a STAGED tree, where "no Manifest" is normal. "Finishing"
// that merge breaks a promise; TestManifestDistNames_StaysOutOfTheUnification
// pins the signature at compile time.
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
// that branch WITHOUT calling anything.
//
// The bool carries that fact, and the returned function stays callable on both
// paths so no branch can reach a nil and panic. Asking it on the nil path is
// answered rather than forbidden: no content, no error.
func (o Options) stagedManifest() (stagedManifestLookup, bool) {
	return normaliseStagedManifest(o.StagedManifest)
}

// normaliseStagedManifest is the rule above, held apart from Options so that the
// entry point which arrives WITHOUT an Options — RunPreparedBuild, whose caller
// holds one already-chosen candidate rather than a run — reads it from here
// instead of restating it.
//
// A restatement is what the rule cannot survive. "nil means nothing travels" and
// "nil means parse the Manifest on disk" are one keystroke apart and neither is
// wrong in the abstract; two copies is the arrangement in which one of them
// quietly becomes the other, and the branch that would change is the one that
// builds nothing at all.
func normaliseStagedManifest(fn stagedManifestLookup) (stagedManifestLookup, bool) {
	if fn != nil {
		return fn, true
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
// The governing rule: a condition that stops a gate becomes a REPORTED OUTCOME
// WITH A REASON — never a silent pass, never an aborted run. A missing
// distfile, a non-Meson build system, an unreadable ebuild: each is one ebuild
// reporting SKIPPED and why, while the run carries on. The only error returned
// is an overlay that could not be scanned, which leaves nothing to report.
//
// Nothing here reaches the network; a test over this package's imports asserts
// it, because behaviour cannot prove a negative.
//
// Every rung of Options.Depth gets an answer. At or below DepthOptions it is
// the static gate. Above it the build gates run against a staged tree when the
// caller supplied its Manifest (Options.StagedManifest), and are otherwise
// reported SKIPPED NAMING WHAT STOPPED THEM: omitting a gate the operator asked
// for would be exactly the silence the governing rule forbids.
func Run(ctx context.Context, opts Options) (Report, error) {
	// Before the tree is walked: a depth that does not parse makes the whole run
	// meaningless, and answering it costs nothing.
	depth, err := opts.depth()
	if err != nil {
		return Report{}, err
	}

	scan, err := repo.ScanOverlay(opts.Overlay)
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
	// DISTDIR the invoking user cannot write.
	distdir, haveDistdir := distfiles.Locate(ctx, opts.Distdir, "")

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

		// After the result is complete, and reading it rather than re-deriving
		// it: what the operator was shown and what the tree carries have to be
		// the same account of the same package.
		//
		// The context travels with it because this record is the one thing here
		// that OUTLIVES the run, so a run that was stopped has to be able to
		// refuse to leave one behind. The cancellation check below cannot
		// stand in for it: that one answers whether the RUN ends as an error, and
		// it is reached only once the record would already be on disk.
		recordStagedGates(ctx, res, target, depth, opts)

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
			examined := len(report.Results)
			// The packages BELOW this one are listed here too, exactly as the
			// pre-package branch lists them — and without this they were not.
			// That branch only ever fires for a run cancelled before its FIRST
			// package, because this check returns first on every real mid-sweep
			// Ctrl-C. So the rule it states in its own comment ("every remaining
			// package is still REPORTED") held for a case that barely happens,
			// while the case that does happen silently dropped every unexamined
			// package from the report the caller goes on to print.
			for _, remaining := range targets[i+1:] {
				report.Results = append(report.Results, interruptedResult(remaining, depth, err))
			}
			return report, fmt.Errorf("the validation run was interrupted after %d of %d ebuilds, so this "+
				"report is partial and says nothing about the rest: %w", examined, len(targets), err)
		}
	}
	return report, nil
}

// recordStagedGates writes, beside the tree one package was staged into, what
// this run's gates said about it and how deep the run asked them to go. target
// IDENTIFIES the package (the very key and version Stage was handed); res
// carries what was REPORTED (a second source for the path would drift).
//
// A read-only command writes because a staged tree with no record is KEPT
// (recordKeepsIt, sweep.go): without this, every tree `overlay validate
// --depth` left behind was permanently unremovable by the sweep. The producer
// is ProducedByValidate — EVIDENCE, not a licence: stagedReuse refuses to
// publish on exactly this value, so naming the applier here would turn a
// read-only command into a producer of publication evidence.
//
// An interrupted run records nothing, as in the applier's recordStagedProof:
// stopped gates report SKIPPED like gates with nothing to do, so the record
// would read as a run nobody interrupted, and the sweep could remove the tree
// on it. Refusing is cheap: an unrecorded tree is kept. A write failure never
// fails the run but is logged, so a tree whose measurement could not be filed
// is distinguishable from one never measured; it is a warning, not a gate,
// because the report's JSON bytes are pinned and this is no verdict.
func recordStagedGates(ctx context.Context, res EbuildResult, target ebuildTarget, depth Depth, opts Options) {
	log := logging.OrDiscard(opts.Logger)
	// THE CONDITION IS "A TREE WAS STAGED", NOT "THE DEPTH WAS HIGH ENOUGH"; the
	// two branches here are only its necessary half. With no staging root, or at
	// or below DepthOptions, nothing was staged, so a tree at this path belongs
	// to some OTHER run — possibly a FAILED bump the applier retained, whose
	// record must not be overwritten. The stat below is the other half: a
	// package above DepthOptions can still decline staging, and writing anyway
	// would bury a real failure in one error per declining package.
	//
	// The root is TRIMMED because Stage trims it before naming the tree.
	stagingRoot := strings.TrimSpace(opts.StagingRoot)
	if stagingRoot == "" || depth <= DepthOptions {
		return
	}

	// The layout is asked of StagedTreePath, the spelling Stage itself uses. A
	// second spelling would fail in silence: splitStagedAtom KEEPS a key's
	// ":slot" or "@label" where splitContentAtom strips it, so a rebuilt path
	// would name a directory Stage never wrote. A path this cannot name (a
	// malformed hand-edited registry key) is not written to, and is said out
	// loud.
	staged, err := StagedTreePath(stagingRoot, target.atom, target.version)
	if err != nil {
		log.Warn("what the gates reported is NOT recorded, because the staged tree it would be "+
			"recorded beside cannot be named (the run's own report is unaffected; a tree left under the "+
			"staging root keeps the unknown outcome an unrecorded tree has always had)",
			"package", target.atom, "version", target.version, "err", err)
		return
	}

	// The tree ITSELF says a record is owed; its absence means this run declined
	// to stage the package, and the report already carries a gate saying why, so
	// silence here adds no silence.
	//
	// KNOWN GAP: a directory here says a tree was staged for this version, not
	// that THIS run staged it. A package that reached the build depths, then
	// declined staging while an older tree stood at the path, gets a record
	// describing gates that never read it. It is bounded: Stage REPLACES, and
	// the applier retains a tree at a version this command validates only once
	// it PASSED, so the cost is one revalidation. Closing it would mean carrying
	// Stage's answer out through buildDepthGates, whose shape is asserted
	// elsewhere.
	info, err := os.Stat(staged)
	if err != nil || !info.IsDir() {
		return
	}

	// THE INTERRUPT INVARIANT, INHERITED FROM THE APPLIER (see the doc comment).
	// It is asked HERE, after the tree was found and right before the write:
	// any EARLIER would announce a missing record for a package never owed one
	// (noise in the report read after a stop), and any LATER is not refusing.
	// It withholds the WRITE only; the tree stays on disk, and stays kept.
	if err := ctx.Err(); err != nil {
		log.Warn("the run was interrupted, so what the gates reported is NOT recorded beside the staged tree: "+
			"they were stopped rather than answered, and recording them would hand the next reader an account "+
			"of a run nobody interrupted. The tree stays under the staging root, keeping the unknown outcome "+
			"an unrecorded tree has always had",
			"package", target.atom, "version", target.version, "path", staged, "err", err)
		return
	}

	// The gates as REPORTED, never a second reading: the retention rule reads
	// this list (recordKeepsIt), so a FAILED gate recorded as anything else would
	// remove the artifact the operator keeps the tree for. The depth is the one
	// SELECTED, which is what StageRecord.Depth means; res.Depth is the depth
	// REACHED and would understate every tree.
	//
	// The three digests stay EMPTY on purpose: they answer the reuse path's
	// question, and that path refuses a validate-produced record on provenance
	// alone, before reading a digest.
	//
	// A failure is logged, never returned: this is bookkeeping, and the report
	// the operator asked for is complete either way.
	if err := WriteStageRecord(staged, StageRecord{
		Package:    target.atom,
		Version:    target.version,
		ProducedBy: ProducedByValidate,
		Depth:      depth,
		Gates:      res.Gates,
	}); err != nil {
		log.Warn("could not record what the gates said beside the staged tree (the run's own report is "+
			"unaffected; the tree stays under the staging root, keeping the unknown outcome an unrecorded "+
			"tree has always had)", "package", target.atom, "version", target.version, "path", staged, "err", err)
	}
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
// still produce the bytes it always produced, and an interrupted run is not a
// licence to add keys to that document.
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
// says why they did not run.
//
// A staged tree needs a Manifest this package cannot make (see
// Options.StagedManifest), so the content is the CALLER'S to supply. With the
// seam the tree is staged, the Manifest materialised inside it, and the gates
// run. Without it nothing is staged or built, and the depth is reported
// unreached with the reason and the command that does reach it.
//
// The gates are never left out: building against a tree Portage refuses would
// give a false FAILED for a fine ebuild, and an unreported gate is
// indistinguishable from one that passed. So every branch reports every gate
// the requested depth covers. The reach is PASS-only, as in the applier's
// recordDepthReached, so both entry points report the same depth for a bump.
//
// Below DepthPatches this is a no-op, default rung included: a --depth-less run
// must keep producing the same JSON bytes.
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

// buildDepthGates chooses what a prepared build runs against, hands it to the
// core, and reports what the core answered — unchanged.
//
// The selecting is all that is left here: the Manifest seam, the two roots, the
// two policy fields, and a reader for the candidate's bytes. The order, the
// stopping conditions and the gates belong to runPreparedBuildGates, so another
// entry point that selected its candidate differently is answered in the same
// words. Adjusting the answer on the way out would be a second ladder.
//
// It IGNORES PreparedBuild's StageErr and gate-ladder fault on purpose. They
// exist for realign.Prove; for Run, EVERY stopping condition IS the reported
// skip and the rest of the overlay is still validated. Returning StageErr here
// would abort a sweep over an unwritable directory, leave later packages
// unmentioned, and change the pinned bytes of `overlay validate --depth`.
func buildDepthGates(ctx context.Context, target ebuildTarget, depth Depth, opts Options) ([]GateResult, string) {
	manifest, supplied := opts.stagedManifest()

	r := preparedBuildGates(ctx, preparedBuild{
		target:      target,
		depth:       depth,
		overlay:     opts.Overlay,
		stagingRoot: opts.StagingRoot,
		ebuild: func() ([]byte, error) {
			return os.ReadFile(target.path)
		},
		manifest:         manifest,
		manifestSupplied: supplied,
		requireIsolation: opts.RequireIsolation,
		logDir:           opts.LogDir,
		// The run's own --distdir, unresolved: Options.Distdir is what the
		// operator typed, and empty means the run named no directory — which the
		// build gate answers by setting no DISTDIR at all and leaving Portage its
		// own configuration, the same fall-through distfiles.Locate applies to
		// the static gate.
		distdir: opts.Distdir,
		deps:    BuildDeps{},
	})

	return r.Gates, r.Reason
}

// preparedBuild is one ALREADY-CHOSEN candidate plus the run-level settings a
// build gate needs before it can run against it.
//
// Every field is an answer its caller already holds. The core re-derives none of
// them, because re-deriving the target would be selecting, and selecting is the
// half its callers legitimately do differently: Run walks the overlay, realign
// holds the one candidate it just built.
type preparedBuild struct {
	// target is the ebuild to build and dir is where its published package
	// directory is — the Manifest seam is asked about that directory, not about
	// the staged copy.
	target ebuildTarget
	depth  Depth

	// overlay is the published tree Stage copies the package out of; stagingRoot
	// is where the single-package repository is built. Stage refuses a staging
	// root that resolves inside the overlay, which is what keeps "never the
	// overlay" a property of the code.
	overlay     string
	stagingRoot string

	// ebuild answers with the bytes Stage writes into the staged tree.
	//
	// The bytes are READ FROM DISK rather than regenerated, for the reason
	// StageRequest.EbuildBytes gives: a gate result has to describe a file that
	// exists somewhere other than in this process.
	//
	// It is a function rather than a []byte so that the read stays LAZY. The
	// Manifest seam is answered first and returns without ever touching the
	// candidate; a caller that read the file eagerly would report an unreadable
	// ebuild for a run whose real answer is that it had no Manifest source at all.
	ebuild func() ([]byte, error)

	// manifest and manifestSupplied are Options.stagedManifest's two answers, and
	// both travel because they say different things. manifestSupplied is whether
	// the run has a Manifest source AT ALL — the branch that builds nothing —
	// while manifest stays callable on both paths so no branch can reach a nil
	// and panic.
	manifest         stagedManifestLookup
	manifestSupplied bool

	// requireIsolation is carried, not defaulted. Leaving it zero was the whole of
	// the bypass: the same gates honour it under `overlay autoupdate`, and a
	// policy that applies to one of the two commands that build is not a policy.
	requireIsolation bool

	// logDir is the whole transcript, kept for whoever has to go past the summary.
	// Empty is still accepted and the gate's reason still says so.
	logDir string

	// distdir is the directory the build reads its archives from, carried into
	// BuildRequest.Distdir and set on the child as DISTDIR.
	//
	// It is CARRIED and never resolved here, like every other field of this
	// struct: the two entry points answer it differently — Run has the run's own
	// --distdir, realign has whatever the command layer resolved for it — and a
	// core that picked one would be selecting, which is the half its callers
	// legitimately do for themselves. Empty stays empty all the way down and sets
	// nothing.
	distdir string

	// deps is the command seam RunBuildGates and the host probe run through.
	// BuildDeps{} — the zero value, meaning the real commands — is what every
	// production caller passes; a test passes its own so both branches of the
	// probe stay reachable on a host that does have Portage.
	deps BuildDeps
}

// runPreparedBuildGates reports the build gates for an already-chosen candidate,
// plus the run-level reason the depth went unreached — empty when the gates
// answered, since each carries its own. The one exception is a candidate that
// needs no distfile: it reaches the gates AND carries a reason naming its class
// (see prepareStagedManifest), because that is the half an operator acts on.
//
// It is the UPPER half of building a candidate, in one place, for Run's build
// depths and realign.Prove. Stage and RunBuildGates alone give no Manifest
// seam, no Manifest in the tree, no host probe, and a BuildRequest missing the
// isolation and log fields. The applier keeps its own copy (its bytes are
// pinned, and its fixer may rewrite the staged ebuild before gating): a KNOWN
// duplicate held open by a pin, not an invitation.
//
// The order is the contract: Stage, then the Manifest, then the gates. Every
// stopping condition — unreadable candidate, unstageable tree, Manifest not
// produced or not written — is a reported SKIP, never an error out of Run.
// StageErr and GatesErr still carry the fault, chained, beside the skip:
// realign.Prove must tell "the gates said no" from "nothing was examined"
// without matching the reason's prose.
func runPreparedBuildGates(ctx context.Context, req preparedBuild) PreparedBuild {
	if req.depth <= DepthOptions {
		// A depth below DepthPatches builds nothing, so it PREPARES nothing
		// either: preparing stages a real tree into the shared staging root,
		// writes a Manifest and runs `emerge --pretend`, all for a question no
		// gate will be asked. The empty result is the contract — what a caller
		// at these depths always got, so PromotionDecision sees the same nil
		// list, and a `depth=none` run keeps its outcome. It lives in the CORE
		// so the next caller inherits it; noteBuildDepth already returns at the
		// same comparison, so `overlay validate --depth` is unaffected.
		return PreparedBuild{}
	}

	if !req.manifestSupplied {
		// Nothing travels, so nothing is staged and nothing is built: exactly
		// the bytes every run produced before the seam existed.
		// UNRECORDED, and that is the right answer rather than a gap: nothing
		// went wrong here. The caller did not wire the Manifest seam, so no tree
		// was ever asked for — the same shape a depth the caller never meant to
		// build produces, and not a statement about the candidate OR the host.
		return skippedPreparedBuild("", req.depth, buildDepthNotRunReason(req.depth, req.stagingRoot), DeclineUnrecorded)
	}

	body, err := req.ebuild()
	var stagedRoot string
	if err != nil {
		// Which file could not be read is the operator's next action, so it is
		// named here; the sentence below says what the failure cost.
		err = fmt.Errorf("reading the candidate ebuild %s: %w", req.target.path, err)
	} else {
		stagedRoot, err = Stage(StageRequest{
			Overlay:     req.overlay,
			StagingRoot: req.stagingRoot,
			Key:         req.target.atom,
			Version:     req.target.version,
			EbuildBytes: body,
		})
	}
	if err != nil {
		// Stage's own sentence already opens with "the staged tree could not be
		// prepared", so this one says what that COST rather than repeating it.
		// DeclineCandidate: the ebuild could not be read, or the tree
		// holding it could not be built. Either way nothing read THIS CANDIDATE,
		// and the published overlay auto-commits — so a promotion here is an
		// unmeasured ebuild pushed within minutes, not a host saying "not me".
		out := skippedPreparedBuild("", req.depth, fmt.Sprintf(
			"the build gates for %s-%s had no staged tree to run in, so none of them read this candidate: %v",
			req.target.atom, req.target.version, err), DeclineCandidate)
		// Unrendered, and CHAINED rather than restated: ErrStageUnpreparable is
		// the sentinel Stage promises on every one of its failure paths, and a
		// caller reacts to staging having failed without enumerating the ways it
		// can. Sprintf'ing it into the reason above is what loses it.
		out.StageErr = err
		return out
	}

	// The seam has THREE answers and only two of them are faults; the third is a
	// candidate that legitimately needs no distfile. It is settled BEFORE
	// materializeStagedManifest is reached rather than by softening it, and
	// classReason is how the result says which class was chosen.
	//
	// It sits at exactly the point the real Manifest was written at, because the
	// order above is the contract: stage, then the Manifest, then the gates.
	classReason, err := prepareStagedManifest(stagedRoot, req.target, req.manifest)
	if err != nil {
		// A tree that was staged and then could not be given its Manifest is NOT
		// a tree nobody prepared: it exists, it is where anyone diagnosing this
		// looks, and it stays on the result. The fault is a reported skip and
		// nothing more — StageErr means "no tree was ever prepared", and saying
		// so here would tell realign.Prove to abandon a realignment whose staged
		// tree is sitting on the disk.
		// DeclineCandidate: Portage refuses an ebuild whose Manifest does not
		// describe its archive, so a tree that never got one is a tree in which
		// no gate could read the candidate. The missing thing is this bump's own
		// digest, not something an operator installs on the box.
		return skippedPreparedBuild(stagedRoot, req.depth, err.Error(), DeclineCandidate)
	}

	// A host that lacks a build dependency is not an ebuild that fails to build,
	// and the two must not arrive at the same verdict. `ebuild` does no
	// dependency resolution at all: it starts the phase, the phase dies on the
	// missing header, and derive reads that as FAILED — blaming the candidate for
	// something only this machine is missing, and exiting 1 on it.
	//
	// The applier answers this too (runBuildGates, package autoupdate), and
	// when this entry point did not, the same host could get
	// opposite verdicts for the same package depending on which command asked.
	// The sentences below are the applier's, deliberately word-for-word: two
	// entry points explaining the same condition differently is the divergence
	// one shared helper exists to prevent.
	//
	// It runs AFTER the Manifest is in place: the probe resolves the candidate
	// through Portage, which refuses an ebuild whose Manifest does not describe
	// its archive.
	if reason := unbuildableHereReason(ctx, stagedRoot, req.target, req.deps); reason != "" {
		// For both entry points at once: the reason is reported and no
		// verdict is recorded against the ebuild, because the missing thing is
		// on this machine rather than in the candidate.
		// DeclineHost makes that structural: the missing thing is
		// on this machine, so the bump is still promoted with the depth it did
		// not reach named. Refusing these instead would make the feature inert
		// on every workstation that does not hold the bump's build deps.
		return skippedPreparedBuild(stagedRoot, req.depth, reason, DeclineHost)
	}

	gates, err := RunBuildGates(ctx, BuildRequest{
		StagedRoot:       stagedRoot,
		Key:              req.target.atom,
		Version:          req.target.version,
		Depth:            req.depth,
		RequireIsolation: req.requireIsolation,
		LogDir:           req.logDir,
		Distdir:          req.distdir,
	}, req.deps)
	if err != nil {
		// AN INTERRUPTION IS NOT A REQUEST THAT COULD NOT BE STARTED. Most errors
		// here are a caller's bug, but RunBuildGates also returns a cancellation,
		// and "could not be started" for a build KILLED after minutes is false
		// and contradicts what interruptedResult tells every later package.
		// Either way it stays a SKIP: the ctx.Err() check in Run turns the sweep
		// itself into an error.
		reason := fmt.Sprintf("the build gates for %s-%s could not be started: %v",
			req.target.atom, req.target.version, err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = fmt.Sprintf(
				"the run was interrupted while %s-%s was building, so no phase reached a verdict and this "+
					"report says nothing about this ebuild: %v", req.target.atom, req.target.version, err)
		}
		// One construction point for both, so the fault cannot travel rendered
		// on one branch and unrendered on the other; CHAINED so a caller asks
		// errors.Is about the cancellation instead of reading the sentence.
		// UNRECORDED on both: a cancellation is about the RUN, a malformed
		// request about the CALLER — neither about the candidate or the host.
		// The interrupt has its own guard at the write (Applier.refuseOnInterrupt).
		out := skippedPreparedBuild(stagedRoot, req.depth, reason, DeclineUnrecorded)
		out.GatesErr = err
		return out
	}
	// classReason is empty except for the no-distfile class, so every other
	// run keeps its empty Reason. The two branches above return first: "cannot
	// build here" and "could not start" are more specific than the class.
	//
	// THE CLASS OWNS ITS OWN LOSING BET. It writes an EMPTY Manifest and lets
	// Portage arbitrate: a candidate that does declare an archive is refused at
	// "VERIFY FAILED! Insufficient data for checksum verification" before any
	// fetch — but also before any PHASE MARKER, so derive leaves the skip's
	// cause unrecorded, and an unrecorded cause PROMOTES. Measured against the
	// real ladder. Only this class is retagged: for an ordinary candidate a
	// pre-phase death may be a flaky mirror, but here the empty Manifest is a
	// bet THIS PACKAGE placed. A wrong refusal costs a re-proof; a wrong
	// promotion publishes an unmeasured ebuild. DeclineHost is left untouched.
	if classReason != "" {
		for i := range gates {
			if gates[i].Outcome == OutcomeSkipped && gates[i].Declined == DeclineUnrecorded {
				gates[i].Declined = DeclineCandidate
			}
		}
	}
	return PreparedBuild{StagedRoot: stagedRoot, Gates: gates, Reason: classReason}
}

// preparedBuildGates is runPreparedBuildGates, held by a package-level variable
// so that a test can OBSERVE a caller going through it.
//
// It is the idiom internal/realign already holds Stage and RunBuildGates by, and
// it is here for what behaviour cannot show: a second copy of the ladder is a
// defect "even when both copies agree", and two agreeing copies
// produce equal bytes. Reaching the core is therefore asserted structurally —
// re-inline the sequence into a caller and the observation simply never happens.
var preparedBuildGates = runPreparedBuildGates

// PreparedBuildRequest is one already-chosen candidate plus everything the
// prepared build needs — the UPPER half of the two-half operation whose lower
// half (Stage, RunBuildGates) is exposed as a seam of its own.
//
// Exporting only the lower half is what let a second entry point reach the build
// gates having prepared none of the upper one: no Manifest seam, no Manifest
// written into the staged tree, no host probe, and a BuildRequest whose two
// policy fields were left at their zero value. This type is the shape of what
// that caller was missing, so that asking for a build and asking for HALF a
// build stop being the same call.
//
// Every field is an answer the caller already holds. Nothing here is re-derived
// by the core, because re-deriving the candidate would be SELECTING it, and
// selecting is the half the two callers legitimately do differently: Run walks
// the overlay, realign holds the one candidate it just built.
type PreparedBuildRequest struct {
	// Overlay is the published tree Stage copies the eclasses and profiles out
	// of; StagingRoot is where the single-package repository is built. Stage
	// refuses a staging root that resolves inside the overlay, which is what
	// keeps "never the overlay" a property of the code.
	Overlay     string
	StagingRoot string

	// Key is the registry key — category/package, possibly carrying ":slot" or
	// "@label" — and Version is the version being built. They name the candidate
	// to Portage; nothing here parses them back out of a filename.
	Key     string // registry key: category/package, possibly :slot or @label
	Version string

	// PackageDir is the PUBLISHED package directory the Manifest seam is asked
	// about — not the staged copy. The staged tree is the thing that LACKS a
	// Manifest, which is the whole reason the seam is being asked.
	PackageDir string

	// Ebuild is the candidate's bytes, written into the staged tree as they are.
	//
	// A caller that holds the bytes passes them; there is no path here for a
	// caller that holds only a file, because the one entry point that reads from
	// disk (buildDepthGates) reaches the core directly and keeps its read lazy.
	Ebuild []byte

	// Depth is how far up the ladder to go, passed through unchanged.
	Depth Depth

	// StagedManifest answers, for PackageDir, the Manifest content the staged
	// tree must carry before a build gate can run in it. Nil means NOTHING
	// TRAVELS — nothing is staged and nothing is built — which is exactly the
	// rule Options.StagedManifest states, read from the one helper both go
	// through rather than restated here.
	StagedManifest func(pkgDir string) ([]byte, error)

	// RequireIsolation is carried, not defaulted. The refusal inside
	// RunBuildGates fires only when the request carries it, and a policy that
	// applies to one of the two commands that build is not a policy.
	RequireIsolation bool

	// LogDir is where the whole transcript is kept for whoever has to go past
	// the summary — the run that needs one is exactly the run that FAILED.
	// Empty is still accepted and the gate's reason still says so.
	LogDir string

	// Distdir is the directory the build reads its archives from, set on the
	// child as DISTDIR. It is the caller's answer and never one
	// derived here — see BuildRequest.Distdir for why it travels as a field
	// instead of through the environment.
	//
	// Empty sets nothing and invents nothing, which is the same
	// answer a caller that never knew about this field gets.
	Distdir string

	// Deps is the command seam RunBuildGates and the host probe run through. Its
	// zero value means the real commands, which is what every production caller
	// passes.
	Deps BuildDeps
}

// PreparedBuild is what running one prepared build produced.
//
// Gates and Reason are the report: the ladder's results in order, and the
// run-level reason the depth went unreached — empty when the gates themselves
// answered, because each then carries its own.
//
// StageErr and GatesErr are the same two faults UNRENDERED, and they are here
// because the two callers owe their operators opposite things. Run turns every
// stopping condition into a reported skip and carries on with the rest of the
// overlay; realign.Prove must not, because a realignment reported as "does not
// build" when nothing was ever built is a change discarded over a fact about the
// disk. Both fields are non-nil ALONGSIDE the rendered skip, never instead of
// it, so a caller reading only Gates and Reason sees exactly what it saw before
// these fields existed.
type PreparedBuild struct {
	// StagedRoot is the tree Stage produced, and it is empty only when no tree
	// was ever made. It travels even on the failing paths: the tree exists, and
	// it is where anyone diagnosing this looks first.
	StagedRoot string

	Gates  []GateResult
	Reason string

	// StageErr is non-nil when NO TREE WAS EVER PREPARED — the candidate's bytes
	// could not be read, or Stage refused. It wraps ErrStageUnpreparable on
	// Stage's own paths, which is why it is chained rather than restated: a
	// caller reacts to staging having failed without enumerating the ways it
	// can fail.
	StageErr error

	// GatesErr is non-nil when the gate ladder could not be started or was
	// interrupted. Neither is a verdict on the ebuild — the first is a caller's
	// bug about the REQUEST, the second is a build that ran and was killed — so
	// a caller that records either as "it does not build" is recording something
	// no gate said.
	GatesErr error
}

// RunPreparedBuild is the prepared build, for a caller that selected its own
// candidate.
//
// It is the SAME function buildDepthGates goes through — it calls the seam
// variable, not runPreparedBuildGates directly — and that is the whole point of
// it existing: one copy of the ladder, reached by both entry points, so a second
// caller cannot acquire a rule of its own about what gets gated.
//
// It selects nothing and normalises nothing beyond the one rule it MUST NOT
// restate: the nil Manifest seam, read from normaliseStagedManifest so that both
// entry points take the same branch for the same reason.
func RunPreparedBuild(ctx context.Context, req PreparedBuildRequest) PreparedBuild {
	manifest, supplied := normaliseStagedManifest(req.StagedManifest)

	return preparedBuildGates(ctx, preparedBuild{
		target: ebuildTarget{
			atom:    req.Key,
			version: req.Version,
			dir:     req.PackageDir,
			// path stays empty, and deliberately: this caller holds the
			// candidate's BYTES rather than a file to read them from, so there
			// is no path to name. The core touches it on exactly one line — the
			// sentence naming which file could not be read — and that line is
			// unreachable from here, because the reader below cannot fail.
		},
		depth:       req.Depth,
		overlay:     req.Overlay,
		stagingRoot: req.StagingRoot,
		ebuild: func() ([]byte, error) {
			return req.Ebuild, nil
		},
		manifest:         manifest,
		manifestSupplied: supplied,
		requireIsolation: req.RequireIsolation,
		logDir:           req.LogDir,
		distdir:          req.Distdir,
		deps:             req.Deps,
	})
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
// because none is known.
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
//
// cause is what the skip DECLINED over, and it is a required argument
// for the same reason the reason itself is: a stopping condition whose cause
// nobody stated is a stopping condition PromotionDecision cannot judge, and the
// place that knows the cause is the branch that detected the condition — never
// a later reader of the sentence. DeclineUnrecorded is a legitimate value where
// the cause genuinely is not one or the other; guessing is not.
func skippedBuildGates(depth Depth, reason string, cause DeclineCause) ([]GateResult, string) {
	return declinedGates(depth, reason, cause), reason
}

// skippedPreparedBuild is that same rendering on the core's result, so the two
// answers stay produced in ONE place and a stopping condition cannot end up
// listing gates one way and wording DepthReason another.
//
// stagedRoot is a parameter because half the stopping conditions happen with a
// tree on disk and half without one, and the difference is what a maintainer
// needs: an empty root says there is nothing to go and look at.
func skippedPreparedBuild(stagedRoot string, depth Depth, reason string, cause DeclineCause) PreparedBuild {
	gates, rendered := skippedBuildGates(depth, reason, cause)
	return PreparedBuild{StagedRoot: stagedRoot, Gates: gates, Reason: rendered}
}

// materializeStagedManifest puts the Manifest the caller supplied where Portage
// reads one from — the staged tree's own package directory.
//
// It writes inside the staged tree and nowhere else. The path is built from the
// root Stage returned, and Stage refuses a staging root inside the published
// overlay (ensureOutsideOverlay), so "never the overlay" is a property of the
// code: no path leads from here to a published package directory.
//
// A staged tree that already carries a Manifest is left alone: a manifest step
// (the apply path's `pkgdev manifest`) wrote it, and overwriting a generated
// Manifest with the published release's digests would replace a measurement
// with a guess.
//
// The mode is stagedFileMode, 0600, like every file staging writes, because the
// tree holds a candidate nobody has reviewed yet.
func materializeStagedManifest(stagedRoot string, target ebuildTarget, manifest stagedManifestLookup) error {
	// splitContentAtom, not splitStagedAtom: the Manifest must land in the
	// package directory Stage actually wrote, which is the suffix-stripped one
	// Joined from a key's ":slot" or "@label" spelling, the
	// file would sit in a directory Portage never reads, beside nothing.
	category, pkg, err := splitContentAtom(target.atom)
	if err != nil {
		// Unreachable after a successful Stage, which split the same atom through
		// this same function — checked anyway, because "unreachable" is a property
		// of today's call order rather than of this function.
		return fmt.Errorf("naming the staged Manifest of %s-%s: %w", target.atom, target.version, err)
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
		// A DIFFERENT fault from the write failure below: the bytes
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
		// The staged path is named because it is the fact the operator
		// acts on: a full disk, a sealed directory and a tree that was swept away
		// under the run all read alike without it.
		return fmt.Errorf("the Manifest supplied for %s-%s could not be written to %s, so the staged tree "+
			"carries none and no build phase could run against it: %v",
			target.atom, target.version, path, err)
	}
	return nil
}

// prepareStagedManifest gives the staged tree the Manifest a build gate needs,
// and reports which of THREE answers the seam gave — a candidate that needs no
// distfile is not one whose Manifest failed to arrive. It returns the reason
// naming that class when the third answer is taken, and for the two faults
// returns materializeStagedManifest's own errors by calling it.
//
// Portage decides the class, no heuristic here does. Measured: Portage answers
// NOTHING about an ebuild in a thin-manifest tree with no Manifest FILE
// ("Manifest not found"), so the class cannot be asked first. With an EMPTY
// Manifest it answers correctly: no SRC_URI runs its phases; a SRC_URI dies at
// "VERIFY FAILED! ... Insufficient data for checksum verification" BEFORE any
// fetch (tested against a port where nothing listens: no connection, empty
// distdir). So the empty file is the smallest input that makes Portage answer,
// and Portage's own ordering keeps this path off the network. Nothing here
// scans SRC_URI or the inherit list.
//
// The seam is asked at most once, and only when the tree lacks a Manifest. The
// answer is memoised, so a costly producer is not paid twice and a producer
// that would answer differently cannot classify one way and write another.
func prepareStagedManifest(stagedRoot string, target ebuildTarget, manifest stagedManifestLookup) (string, error) {
	path, err := stagedManifestPath(stagedRoot, target)
	if err != nil {
		// Unreachable after a successful Stage, which split the same atom. Handed
		// on rather than answered here, so a malformed atom keeps producing the
		// one sentence it has always produced.
		return "", materializeStagedManifest(stagedRoot, target, manifest)
	}
	if _, err := os.Stat(path); err == nil {
		// A staged tree that already carries a Manifest is left alone AND the
		// seam is not asked at all — the apply path's `pkgdev manifest` wrote it,
		// and classifying a candidate whose Manifest is already on disk would
		// turn a run that succeeds today into a skip the moment the published
		// tree has no Manifest to read.
		return "", materializeStagedManifest(stagedRoot, target, manifest)
	}

	body, prodErr := manifest(target.dir)
	if prodErr == nil && len(body) == 0 {
		return emptyStagedManifest(path, target)
	}
	// Either of the two faults, or real content: all three are materializeStagedManifest's
	// to answer, and it is handed the answer ALREADY IN HAND rather than the
	// producer, so the seam is asked exactly once per staged tree.
	answered := func(string) ([]byte, error) { return body, prodErr }
	return "", materializeStagedManifest(stagedRoot, target, answered)
}

// stagedManifestPath names the file Portage reads a package's digests from
// inside a staged tree.
//
// It is materializeStagedManifest's own join, held here for the ONE caller that
// needs the path without writing through that function. The duplication is
// deliberate and narrow: materializeStagedManifest is frozen — a test asserts
// both of its errors still fire — so it keeps its inline copy rather
// than being edited to call this.
func stagedManifestPath(stagedRoot string, target ebuildTarget) (string, error) {
	// splitContentAtom, in lockstep with materializeStagedManifest's inline
	// copy of this join: both must name the suffix-stripped directory Stage
	// wrote, or the stat guarding the no-distfile class and
	// the write it guards would disagree about where the Manifest lives.
	category, pkg, err := splitContentAtom(target.atom)
	if err != nil {
		return "", err
	}
	return filepath.Join(stagedRoot, category, pkg, "Manifest"), nil
}

// emptyStagedManifest writes the third answer's Manifest and states the class.
//
// The file is EMPTY and 0600 — the same path and the same stagedFileMode the
// real one would have had, because the difference between this class and a
// digested one belongs in the file's contents, not in where it lands.
//
// The reason deliberately does NOT reuse the expected-Manifest fault's sentence.
// Telling an operator that a metapackage "would describe no archive and Portage
// would refuse the candidate before any phase ran" sends them to regenerate a
// Manifest that was never supposed to exist.
func emptyStagedManifest(path string, target ebuildTarget) (string, error) {
	if err := os.WriteFile(path, nil, stagedFileMode); err != nil {
		return "", fmt.Errorf("the empty Manifest placing %s-%s in the no-distfile class could not be written "+
			"to %s, so the staged tree carries no Manifest file and Portage answers nothing about the ebuild: %v",
			target.atom, target.version, path, err)
	}
	return fmt.Sprintf("no Manifest content was supplied for %s-%s, so it is taken as a candidate that requires "+
		"no distfile: the staged tree at %s carries an empty Manifest, which is what lets Portage answer about "+
		"the ebuild at all, and a candidate that does require an archive after all is refused at digest "+
		"verification — before any fetch is attempted", target.atom, target.version, path), nil
}

// gateRungs maps a gate back to the rung of the ladder its PASS proves. It is
// the inverse of buildGates plus the option rung, and it answers exactly one
// question: how far did this run actually get.
//
// qa and review are absent, and their absence is the rule rather than an
// oversight: neither decides anything, so neither can be evidence that a
// rung was reached.
//
// The applier keeps the same table for the same question (gateDepths,
// applier_gates.go:46). Two entry points, one rule — see deepestPassedRung.
var gateRungs = map[string]Depth{
	GateOptions:   DepthOptions,
	GatePatches:   DepthPatches,
	GateConfigure: DepthConfigure,
	GateCompile:   DepthCompile,
	GateInstall:   DepthInstall,
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

// GateForDepth names the gate whose OWN pass proves a run reached rung d. It is
// gateRungs read in the other direction, so the mapping stays in one table.
//
// It is exported for `overlay autoupdate --check`, which must know whether the
// depth the POLICY SELECTED was measured; a table of its own in cmd/bentoo
// would be another spelling of this mapping, and could report a rung the
// runner never proved.
//
// ok IS FALSE WHEN NO GATE PROVES d — today only DepthNone. The applier's
// similarly named gateForDepth (a repair target) falls back to the patch gate;
// this must not, or a depth-none bump would read as proved by a gate it never
// ran.
//
// The loop returns the deepest gate AT OR BELOW d, which equals the gate for d
// only while gateRungs covers every selectable depth. Add a depth and its gate
// together, or a bump at that depth reads as proved on a SHALLOWER gate's pass.
// The three depth-gate tables (gateDepths, gateRungs, buildGates) stay
// separate on purpose: merging them would change the applier's repair-target
// fallback, which is behaviour.
func GateForDepth(d Depth) (string, bool) {
	gate, best := "", DepthNone
	for name, rung := range gateRungs {
		if rung <= d && rung > best {
			gate, best = name, rung
		}
	}
	return gate, gate != ""
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
// per ebuild version.
//
// A selector that matches nothing returns no targets. The caller reports that
// on the Report rather than as an error: the run DID produce an answer, and the
// command turns it into exit 2 naming the selector.
func selectTargets(scan *repo.ScanResult, selector string) []ebuildTarget {
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
		// through verbatim and the directory is still named.
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
// and carries the words every refusal about them is written in.
//
// # Why the wording is a value and not a constant
//
// The two sources have nothing in common to say. One can point at a FILE and
// quote its path; the other has no file at all, and "the package's Manifest
// names no distfile" said about a staged tree is a wrong answer dressed as a
// diagnostic — it names a fix that does not exist. Passing the words in, instead
// of deciding them at each refusal, is what lets ONE set of selection rules
// serve both sources while each still explains itself.
type distNameSource struct {
	// origin names the source as the SUBJECT of "<origin> names no distfile".
	origin string

	// listed attributes a list of names inside "no distfile <listed> is present
	// in the directory searched". It is its own phrase rather than something
	// derived from origin because the Manifest wording predates the seam and is
	// reproduced to the byte.
	listed string

	// attributed is the clause appended to the two refusals originally written
	// with no source in them at all.
	//
	// IT IS EMPTY FOR THE MANIFEST, and that is the byte-for-byte promise rather
	// than an oversight: those two sentences are in reports that have already
	// shipped, and a run that supplies no names must produce today's bytes. A
	// run that DOES supply names is new, so its wording is free to name the
	// source its names came from — on the only path that could ever be confused
	// about it.
	attributed string
}

// manifestSource is the nil seam's source: the package directory's own Manifest,
// in the words its refusals have always used, which tests pin.
func manifestSource(pkgDir string) distNameSource {
	return distNameSource{
		origin: "the package's Manifest (" + filepath.Join(pkgDir, "Manifest") + ")",
		listed: "named by the Manifest",
	}
}

// suppliedSource is the seam's source. Every sentence it produces carries the
// word "supplied", which is what tells an operator reading a SKIP that this
// package did not choose the names — the caller did, and the caller is where a
// wrong list has to be fixed.
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
// exactly the same rules — does the choosing. The signature is
// unchanged because four TestFindDistfile_* cases call it directly, and they are
// the measurement that the Manifest path still behaves as it did.
func findDistfile(pkgDir, distdir, version string) (string, error) {
	return selectDistfile(manifestDistNames(pkgDir), manifestSource(pkgDir), distdir, version)
}

// selectDistfile is the frozen selection core: given the candidate names, their
// source, the directory to search and the version, it returns the one archive
// of this ebuild version — or a refusal naming what it declined, where it
// looked, and whose names those were. ONE body serves Manifest-parsed and
// caller-supplied names, so they cannot drift.
//
// Supplied names never passed ParseManifestDistFilenames' path-separator
// filter ("../x.tar.gz" would resolve OUTSIDE the directory), so they are
// re-checked, and ONE BAD NAME DECLINES THE WHOLE LIST: a false SKIP gets
// investigated, a false PASS does not.
//
// One list serves the whole package directory, so the version must decide.
// Taking the first present name once PASSED 1.29.2 against 1.28.6's archive;
// the single-present shortcut once FAILED a correct 1.28.6 against 1.29.2's.
// Chosen: the one present name carrying this version, or the one present name
// when no name carries any version (snapshot or commit-hash names), or the one
// of several carrying the version. Anything else is SKIPPED by name — a guess
// is indistinguishable from a measurement in the report.
func selectDistfile(names []string, source distNameSource, distdir, version string) (string, error) {
	// Before anything is joined to distdir, and over the WHOLE list before any of
	// it is used: the same test ParseManifestDistFilenames applies, applied again
	// because these names may never have been through it.
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "/\\") {
			return "", fmt.Errorf("the distfile name %q is not a plain file name, so it names nothing in the "+
				"directory searched, %s; the whole list was declined rather than the remaining names read%s",
				name, distdir, source.attributed)
		}
	}

	if len(names) == 0 {
		// The directory is named even though nothing was looked for in it.
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
		// Naming the directory is what separates "this host has never fetched
		// this release" from "the fetch went somewhere else" — a misdirected
		// fetch once read as the first for as long as no message said where the
		// search happened.
		return "", fmt.Errorf("no distfile %s is present in the directory searched, %s: %s",
			source.listed, distdir, strings.Join(names, ", "))
	case 1:
		// Safe when this file is this ebuild's, or when no candidate name tells
		// releases apart at all. Otherwise the shortcut is a guess, and it
		// declines by name.
		only := present[0]
		if carriesVersion(only, version) || !anyNameCarriesAVersion(names) {
			return filepath.Join(distdir, only), nil
		}
		return "", fmt.Errorf("the only distfile present in %s is %s, which does not belong to version %s; "+
			"reading it would answer about a different release%s", distdir, only, version, source.attributed)
	}

	// Exact rather than carriesVersion: with several archives present a revision
	// suffix can be the ONLY thing telling two names apart.
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
// version.
//
// The revision is stripped first. `-rN` counts Gentoo-side rebuilds of the SAME
// upstream tarball, so it never appears in the distfile's name; requiring it
// would make every revbumped ebuild in the overlay decline to validate. That is
// a false SKIP, and a gate that skips silently is as useless as one that lies.
func carriesVersion(name, version string) bool {
	return strings.Contains(name, revisionSuffix.ReplaceAllString(version, ""))
}

// anyNameCarriesAVersion reports whether the candidate names distinguish
// releases at all — whoever supplied them. When they do not, one present
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
// Once per package, not per version: pkgcheck scans a package, so every
// version would get the same answer, at roughly three seconds per invocation,
// multiplied across a whole-overlay run. The cache is per run and lives no
// longer.
//
// Only where the option gate produced a verdict: an ebuild that gate skipped
// has already reported why, and a QA section would spend the scan without
// changing what the operator does next.
//
// The reason rides on the QA gate itself, beside the outcome it explains, so
// it never overwrites the option gate's own reason.
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
