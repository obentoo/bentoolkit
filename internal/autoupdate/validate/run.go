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
	"github.com/obentoo/bentoolkit/internal/common/logger"
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
	// and the config key spell it: none, options, patches, configure, compile
	// or install, each including every rung before it (R2, R11.1).
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

	// RequireIsolation refuses to run a build this host cannot isolate, exactly
	// as BuildRequest.RequireIsolation does for the applier — it IS that field,
	// carried to the one caller that had no way to set it.
	//
	// IT EXISTS BECAUSE ITS ABSENCE WAS A POLICY BYPASS, not a missing feature.
	// `autoupdate.validate.require_isolation` is honoured by the identical gates
	// under `overlay autoupdate`, and until this story those gates were
	// unreachable from `overlay validate`: every one of them SKIPPED, so nothing
	// this command did could be unisolated. Wiring the seams made them run, and
	// a build path that ignores the setting is not a command missing an option —
	// it is the operator's decision that builds must be isolated, silently not
	// applying to one of the two commands that build.
	//
	// The zero value is false, which is the shipped behaviour of both commands
	// when the key is unset (R11.3): an unisolated build still runs and its pass
	// is labelled "unverified isolation" rather than refused, because creating
	// the namespace needs privilege an ordinary user does not have.
	RequireIsolation bool

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
//
// # Staying out of the unification is a REQUIREMENT, not an oversight (S039-R5.4)
//
// Story 039 sub-task 5.1 unified the other two readers of "which archives does
// this Manifest name" — cmd/bentoo's publishedManifestDistNames and
// internal/autoupdate's publishedDistNames — onto the error-returning sibling
// distfiles.ReadManifestDistFilenames, because each was recovering the
// missing-versus-empty distinction with a second read of its own. This is the
// THIRD reader and it was deliberately left where it is.
//
// Those two answer about a PUBLISHED package directory, where a Manifest that
// cannot be read is a fault. This one answers about a STAGED tree, where "no
// Manifest" is the normal case: it is what a staged tree looks like, and it
// already reaches the operator as an outcome of its own — selectDistfile's named
// refusal, in the words story 031 shipped. Folding it in would exchange that
// sentence for a different one on EVERY staged tree, which is every package this
// seam is ever asked about, and would break S037-R1.2's byte-for-byte promise
// along the way.
//
// So a later reader who "finishes" the merge is breaking a promise, not tidying
// up. TestManifestDistNames_StaysOutOfTheUnification pins the signature at
// compile time so the drift is caught where it happens, rather than in whatever
// report starts reading differently.
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

		// After the result is complete, and reading it rather than re-deriving
		// it: what the operator was shown and what the tree carries have to be
		// the same account of the same package (R4.1).
		//
		// The context travels with it because this record is the one thing here
		// that OUTLIVES the run, so a run that was stopped has to be able to
		// refuse to leave one behind (R4.2). The cancellation check below cannot
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
// this run's gates said about it and how deep the run asked them to go (R4.1).
//
// Its two inputs answer two different questions and neither is read for the
// other's: target IDENTIFIES the package — it holds the very key and version
// Stage was handed, so the tree is named from the same values it was created
// from — while res carries what was REPORTED about it. Taking the identity out
// of the reported result instead would be a second source for a path, which is
// the class of drift StagedTreePath exists to prevent.
//
// # Why a command that changes nothing writes anything at all
//
// A staged tree carrying no record is KEPT, because "its outcome is unknown"
// (recordKeepsIt, sweep.go). Until this existed, WriteStageRecord had exactly one
// production caller — two defers inside the applier — and this package only ever
// READ records. So every tree an `overlay validate --depth` run left behind was
// permanently unremovable: a sweep over that staging root would report every one
// of them kept and take nothing away, which is precisely the accumulation the
// sweep was written to stop (R4.4). Nothing else can classify these trees,
// because nothing else knows what was measured about them.
//
// # It is EVIDENCE and not a licence, which is why the producer is named
//
// ProducedByValidate, never the applier's constant. The reuse path publishes a
// bump on the strength of a record (R10.1), and this run staged nothing it means
// to publish — it MEASURED a tree, by contract. The refusal that keeps the two
// apart lives in stagedReuse and keys on exactly this value (S041-R5.1), so
// naming the wrong producer here would quietly turn a read-only command into a
// producer of publication evidence.
//
// # An interrupted run records nothing, and that invariant is INHERITED (R4.2)
//
// The applier already refuses to write under a cancelled context, in so many
// words: "Writing this record would turn 'Ctrl-C does not publish' into 'Ctrl-C
// publishes one run later'" (recordStagedProof, promote_reuse.go). The refusal
// is repeated here for the same reason, one reader further along.
//
// Gates that were STOPPED report SKIPPED, and so do gates that were asked and
// had nothing to do — the two are indistinguishable in a gate list. A record
// written under cancellation is therefore an account of a run nobody
// interrupted, and it is read as one: the sweep's retention rule classifies the
// tree by it (recordKeepsIt, sweep.go) and can take away the very artifact the
// stopped run left behind, while the reuse path reads the same list through it.
//
// Nothing is lost by refusing, and that is what makes the refusal cheap. A tree
// carrying no record is KEPT, with the unknown outcome an unrecorded tree has
// always had — one tree that stays, never a tree removed on evidence no gate
// produced.
//
// # A record that cannot be written never fails a run
//
// The applier's rule (recordStagedProof, promote_reuse.go) for the applier's
// reason: the cost of a missing record is one tree that stays, reported by the
// sweep as an unknown outcome — which is what every release before this one did
// with every tree this command left.
//
// It is still SAID OUT LOUD (R4.3). The failure changes what a later sweep does
// with this tree, and an operator who is never told cannot tell a tree nothing
// measured from a tree whose measurement could not be filed.
//
// # Why this warns instead of reporting a gate
//
// Everything else in this file answers a stopping condition with a reported gate
// and a reason, and this deliberately does not. A record is bookkeeping ABOUT a
// tree rather than a statement about the ebuild, and the report's bytes are
// pinned (R1.6, R11.3): a new gate — or a new field — would change the JSON
// document every `overlay validate --json` consumer already parses, to say
// something that is not a verdict on the candidate.
func recordStagedGates(ctx context.Context, res EbuildResult, target ebuildTarget, depth Depth, opts Options) {
	// THE CONDITION IS "A TREE WAS STAGED", NOT "THE DEPTH WAS HIGH ENOUGH", and
	// the two branches here are only its necessary half.
	//
	// A run that named no staging root staged nothing anywhere, and a depth at or
	// below DepthOptions prepares nothing at all — runPreparedBuildGates returns
	// before Stage is reached. Under either condition a tree standing at this path
	// belongs to some OTHER run, and its record is not this one's to overwrite:
	// the applier retains the tree of a bump that FAILED as the artifact an
	// operator still needs, and the retention rule that keeps it reads the record
	// beside it.
	//
	// Neither is sufficient on its own, which is what the stat below is for: a
	// package above DepthOptions can still decline staging — its ebuild could not
	// be read, or Stage refused — and WriteStageRecord errors on a directory that
	// is not there. A depth-only condition would therefore produce one failure per
	// declining package and bury a real write failure in that noise.
	//
	// The root is TRIMMED because Stage trims it before naming the tree, so an
	// untrimmed value here would name a different directory than the one that was
	// actually staged.
	stagingRoot := strings.TrimSpace(opts.StagingRoot)
	if stagingRoot == "" || depth <= DepthOptions {
		return
	}

	// The layout is asked of the one function that spells it, which is the one
	// Stage itself goes through: a second spelling of
	// <staging>/<category>/<package>/<version> would be a second spelling that can
	// go stale, and it would fail in silence — the record lands where nothing
	// reads it, and every tree this command leaves stays unremovable exactly as it
	// did before. It is not a hypothetical shape either: splitStagedAtom KEEPS a
	// key's ":slot" or "@label" where splitContentAtom strips it, so a path
	// rebuilt from the parts of a suffixed key names a directory Stage never
	// wrote.
	//
	// A path this cannot name is not a path this may write to. The failure is a
	// malformed atom or version — a registry key is a file a maintainer edits by
	// hand — rather than a fact about the disk, so it is said out loud and names
	// the package it is about.
	staged, err := StagedTreePath(stagingRoot, target.atom, target.version)
	if err != nil {
		logger.Warn("what the gates reported about %s-%s is NOT recorded, because the staged tree it would be "+
			"recorded beside cannot be named: %v (the run's own report is unaffected; a tree left under the "+
			"staging root keeps the unknown outcome an unrecorded tree has always had)",
			target.atom, target.version, err)
		return
	}

	// The tree ITSELF is what says a record is owed here, and its absence is the
	// ordinary answer rather than a fault: this run declined to stage this
	// package.
	//
	// Silence is right, and it adds no silence. Every reason there is no readable
	// directory at this path is a reason the run ALREADY rendered as a reported
	// gate carrying its own sentence — the candidate could not be read, the tree
	// could not be prepared, the caller wired no Manifest seam — so an operator is
	// told what stopped this package by the report they asked for. Repeating it
	// here would be a second, differently worded account of one fact.
	//
	// WHAT THIS CANNOT DISTINGUISH, stated rather than left to be discovered. A
	// directory standing here says a tree was staged for this package and version;
	// it does not say THIS run staged it. The one case where the two differ is a
	// package that reached the build depths and then declined staging while an
	// earlier producer's tree still stood at the same path, and the record written
	// then describes gates that never read it. Two things bound it: Stage
	// REPLACES, so every package that does stage is describing its own tree
	// (R3.7); and the applier only retains a tree at a version this command
	// validates once that version is published, which is to say once it PASSED —
	// so the record being replaced is not the failure artifact an operator kept
	// the tree for, and the cost is one revalidation. Closing it exactly would
	// mean carrying Stage's own answer back out through buildDepthGates, whose
	// two-value shape is asserted elsewhere; it is a known gap, not an oversight.
	info, err := os.Stat(staged)
	if err != nil || !info.IsDir() {
		return
	}

	// THE INTERRUPT INVARIANT, INHERITED FROM THE APPLIER — see the note above
	// for why a record of stopped gates is worse than no record at all.
	//
	// It is asked HERE — after the tree has been found, immediately before the
	// write — where the applier asks it first thing. What differs is only the
	// point at which each function knows a record is OWED: the applier's
	// `root == ""` return settles that on its own, while here it takes the staging
	// root, the depth and the stat above together. Refusing any EARLIER would
	// announce a missing record for a package that was never going to have one —
	// a run below the build depths, or a package that declined staging — which is
	// noise in the one report an operator reads after stopping a sweep. Refusing
	// any LATER is not refusing: the record is already on disk.
	//
	// It withholds the WRITE and nothing else. The tree itself stays on disk as
	// the stopped run's evidence, and stays kept.
	if err := ctx.Err(); err != nil {
		logger.Warn("the run was interrupted, so what the gates of %s-%s reported is NOT recorded beside %s: "+
			"they were stopped rather than answered, and recording them would hand the next reader an account "+
			"of a run nobody interrupted. The tree stays under the staging root, keeping the unknown outcome "+
			"an unrecorded tree has always had (%v)",
			target.atom, target.version, staged, err)
		return
	}

	// The gates as REPORTED, outcome for outcome, and never a second reading of
	// the same run: the retention rule reads this list (recordKeepsIt), so a gate
	// that FAILED and was recorded as anything else takes away the artifact an
	// operator is keeping the tree for.
	//
	// The depth is the one the run SELECTED — the depth the gates were asked to
	// cover — because that is what StageRecord.Depth means and what both of its
	// readers compare against. res.Depth is the depth REACHED, which is a
	// different question with a different answer whenever a gate stopped short,
	// and recording it would understate every tree this command leaves.
	//
	// The three digests are left EMPTY, which is a decision and not an omission.
	// They exist for the reuse path's question, "is this evidence about the very
	// bytes I am about to publish" — and that path refuses a record this command
	// wrote on its provenance alone, before it looks at a digest. Computing them
	// would mean re-reading the candidate and the Manifest to produce values whose
	// only consumer has already declined to read them.
	//
	// A failure never fails the run, so the error is deliberately not returned to
	// the loop: this is bookkeeping beside a tree, and the run's own outcome — the
	// report the operator asked for — is complete either way. It is REPORTED and
	// not discarded (R4.3), because the two silences are different: a tree with no
	// record is one an operator may reasonably read as never measured, and only
	// this sentence distinguishes it from one whose measurement could not be filed.
	if err := WriteStageRecord(staged, StageRecord{
		Package:    target.atom,
		Version:    target.version,
		ProducedBy: ProducedByValidate,
		Depth:      depth,
		Gates:      res.Gates,
	}); err != nil {
		logger.Warn("could not record what the gates said about %s-%s beside %s: %v (the run's own report is "+
			"unaffected; the tree stays under the staging root, keeping the unknown outcome an unrecorded "+
			"tree has always had)", target.atom, target.version, staged, err)
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

// buildDepthGates chooses what a prepared build runs against, hands it to the
// core, and reports what the core answered — unchanged.
//
// The selecting is all that is left here: the Manifest seam Options carries, the
// two roots, the two policy fields, and a reader for the candidate's bytes.
// Everything past that — the order, the stopping conditions, the gates
// themselves — belongs to runPreparedBuildGates, so that another entry point
// arriving with a candidate it selected differently is still answered in the
// same words (R1.5).
//
// It does not post-process the core's answer. A depth path that adjusted the
// gates or the reason on the way out would be the second implementation of the
// ladder again, one return statement further down.
//
// # It reads two of the result's four fields and IGNORES the other two on purpose
//
// PreparedBuild reports the staging fault and the gate-ladder fault separately
// from the gates it renders them into, because realign.Prove owes its operator a
// distinction this path must not draw: for Run, EVERY stopping condition IS the
// reported skip (the governing rule above), the rest of the overlay is still
// validated, and this ebuild says what stopped it. Returning StageErr here would
// abort a sweep over an unwritable directory and leave every later package
// unmentioned — and it would change the bytes `overlay validate --depth` has
// always printed, which R1.6 pins.
func buildDepthGates(ctx context.Context, target ebuildTarget, depth Depth, opts Options) ([]GateResult, string) {
	manifest, supplied := opts.stagedManifest()

	r := preparedBuildGates(ctx, preparedBuild{
		target:      target,
		depth:       depth,
		overlay:     opts.Overlay,
		stagingRoot: opts.StagingRoot,
		ebuild: func() ([]byte, error) {
			return os.ReadFile(target.path) //nolint:gosec // the path comes from scanning the overlay under validation, not from input
		},
		manifest:         manifest,
		manifestSupplied: supplied,
		requireIsolation: opts.RequireIsolation,
		logDir:           opts.LogDir,
		// The run's own --distdir, unresolved: Options.Distdir is what the
		// operator typed, and empty means the run named no directory — which the
		// build gate answers by setting no DISTDIR at all and leaving Portage its
		// own configuration, the same fall-through distfiles.Locate applies to
		// the static gate (S039-R3.1, R3.2).
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
	// overlay" a property of the code (S037-R2.4).
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
	// and panic (S037-R2).
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
	// BuildRequest.Distdir and set on the child as DISTDIR (S039-R3.1).
	//
	// It is CARRIED and never resolved here, like every other field of this
	// struct: the two entry points answer it differently — Run has the run's own
	// --distdir, realign has whatever the command layer resolved for it — and a
	// core that picked one would be selecting, which is the half its callers
	// legitimately do for themselves. Empty stays empty all the way down and sets
	// nothing (R3.2).
	distdir string

	// deps is the command seam RunBuildGates and the host probe run through.
	// BuildDeps{} — the zero value, meaning the real commands — is what every
	// production caller passes; a test passes its own so both branches of the
	// probe stay reachable on a host that does have Portage.
	deps BuildDeps
}

// runPreparedBuildGates reports the build gates for an already-chosen candidate,
// plus the run-level reason the depth went unreached — empty when the gates
// themselves answered, because each then carries its own.
//
// ONE class breaks that habit on purpose. A candidate that needs no distfile
// reaches the gates AND carries a reason (R4.4): "the gates ran" is not the
// whole answer there, because which class the candidate was placed in — and why
// its staged tree carries an empty Manifest — is the half an operator acts on.
// See prepareStagedManifest.
//
// # Why this is a function of its own (R1.5)
//
// Building a candidate is ONE operation with two halves, and only the lower half
// was ever exposed. Stage and RunBuildGates are exported, so a caller holding
// those two reaches the build gates in two calls — and gets none of the upper
// half: no Manifest seam, no Manifest written into the staged tree, no host
// probe, and a BuildRequest missing the isolation and log-directory fields.
// Gates that run under those conditions report on a tree nobody prepared, and a
// caller assembling the upper half for itself is the second implementation of
// this ladder even when the two copies agree. This function is that upper half,
// in one place, for the two entry points that ask validate to prove a candidate:
// Run's build depths, and internal/realign's Prove.
//
// # The applier keeps its own copy, and that is deliberate
//
// "Every caller in the repository" would be false, and saying it would send the
// next reader to finish a job nobody wants finished. package autoupdate's
// applier still assembles an upper half of its own — Applier.prepareInStagingTree
// calls Stage directly, and Applier.runBuildGates holds its own dependency probe
// (whose two sentences this file's unbuildableHereReason matches word for word,
// on purpose) and builds its own BuildRequest.
//
// It is not folded in here because story 039's R1.6 pins `overlay autoupdate` to
// the bytes it produces today, and the applier's staging is not the same
// operation under a different name: a fixer may rewrite the staged ebuild
// between staging and gating, which is why its promote reads the file back out
// of the staged tree while realign's publishes the proposal's own slice. Two
// copies is the defect R1.5 names, so this is a KNOWN one held open by a pin —
// not an oversight, and not an invitation.
//
// # The order is the contract, not an accident
//
// Stage, then the Manifest, then the gates. The seam is asked about a staged tree
// that LACKS a Manifest, so the tree has to exist before it is asked; and the
// Manifest has to be in place before `ebuild` reads the tree, or Portage refuses
// the candidate over a file this run was about to write. An implementation that
// probed for `ebuild` first and skipped early would invert both, and its skip
// would be an answer about a tree nobody prepared.
//
// # Every stopping condition is a reported SKIP
//
// Run's governing rule, applied to the four things that can stop a build gate
// before it starts: the candidate could not be read, the tree could not be
// staged, the Manifest could not be PRODUCED (S037-R2.6) or could not be WRITTEN
// (S037-R2.5). None of them is an error out of Run and none of them is silence —
// the rest of the overlay is still validated, and this ebuild says what stopped
// it.
//
// # …and the two faults travel UNRENDERED as well
//
// The rendering above is Run's rule, not the shared one. realign.Prove's rule is
// the opposite and is right for the opposite reason: a realignment reported as
// `Passed=false` because a directory was unwritable is a change abandoned on a
// fact about the disk, so "the gates examined this and said no" and "nothing was
// ever examined" have to stay two answers. A core that returned only the skip
// could not serve it, and a caller reconstructing the difference by matching the
// reason's words would be pattern-matching prose this file is free to reword.
//
// So StageErr and GatesErr carry the fault itself, chained, next to — never
// instead of — the skip that renders it. Each caller then reads the halves its
// own contract needs, and neither of them re-derives the other's.
func runPreparedBuildGates(ctx context.Context, req preparedBuild) PreparedBuild {
	if req.depth <= DepthOptions {
		// A depth below DepthPatches builds nothing, so it PREPARES nothing
		// either — and preparing is not free. Everything below this line stages
		// a real tree into the shared staging root, writes a Manifest into it
		// and starts `emerge --pretend` as a child process, all so that
		// RunBuildGates can reach its own `!runs` branch and answer with an
		// empty list. The work is not merely wasted: a shallow run would be
		// writing into a directory the other two commands stage under, for a
		// question no gate was ever going to be asked.
		//
		// The empty result is the contract and not a shortcut. No gates, no
		// reason and no faults is exactly what a caller at these depths got
		// before the two halves were joined, so PromotionDecision is still
		// reached with the same nil list it was reached with then. Inventing a
		// reason here would change the outcome of a `depth=none` run, which
		// R2.5 pins.
		//
		// It lives in the CORE rather than in either entry point for the reason
		// the core exists at all (R1.5): a rule kept by the callers is a rule
		// the next caller does not inherit. `overlay validate --depth` is
		// unaffected — noteBuildDepth is buildDepthGates' only caller and
		// returns at this same comparison, so that route never reached here
		// with such a depth.
		return PreparedBuild{}
	}

	if !req.manifestSupplied {
		// Nothing travels, so nothing is staged and nothing is built: exactly
		// the bytes every run produced before the seam existed (S037-R2).
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
		// DeclineCandidate (S039-R2.1): the ebuild could not be read, or the tree
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
	// candidate that legitimately needs no distfile (R4.1). It is settled BEFORE
	// materializeStagedManifest is reached rather than by softening it (R4.2),
	// and classReason is how the result says which class was chosen (R4.4).
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
	if reason := unbuildableHereReason(ctx, stagedRoot, req.target, req.deps); reason != "" {
		// R1.2 for both entry points at once: the reason is reported and no
		// verdict is recorded against the ebuild, because the missing thing is
		// on this machine rather than in the candidate.
		// DeclineHost, which is S033-R3.12 made structural: the missing thing is
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
		// AN INTERRUPTION IS NOT A REQUEST THAT COULD NOT BE STARTED, and this
		// branch used to say it was. The sentence below was written when
		// RunBuildGates errored about the REQUEST and never about the build, so
		// every error here really was a caller's bug. The interrupt guard inside
		// RunBuildGates now returns the cancellation as an error too, and
		// answering a build that ran for minutes and was KILLED with "could not
		// be started" is simply false — worse, it disagreed with the wording
		// every LATER package in the same sweep got from interruptedResult.
		//
		// The reason stays a SKIP because the run-level ctx.Err() check below
		// turns the sweep itself into an error; these gates only have to describe
		// the package honestly in the report that check hands back.
		// A caller's bug rather than a verdict on the ebuild — and still one
		// ebuild reporting why while the run carries on.
		reason := fmt.Sprintf("the build gates for %s-%s could not be started: %v",
			req.target.atom, req.target.version, err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = fmt.Sprintf(
				"the run was interrupted while %s-%s was building, so no phase reached a verdict and this "+
					"report says nothing about this ebuild: %v", req.target.atom, req.target.version, err)
		}
		// One construction point for both, so the fault cannot travel rendered
		// on one branch and unrendered on the other. It is CHAINED rather than
		// restated, so a caller asks errors.Is about the cancellation instead of
		// reading the sentence above for the word.
		// UNRECORDED on both branches. A cancellation is a fact about the RUN,
		// and a malformed request is a fact about the CALLER; neither is a
		// statement about the candidate, and neither is this host lacking
		// something. The interrupt has its own guard at the write
		// (Applier.refuseOnInterrupt) precisely because a gate list is the wrong
		// shape for "you stopped me".
		out := skippedPreparedBuild(stagedRoot, req.depth, reason, DeclineUnrecorded)
		out.GatesErr = err
		return out
	}
	// classReason is empty for every candidate but the no-distfile class, so this
	// is the same empty Reason every gates-answered run has always carried. The
	// two branches above return BEFORE it: a host that cannot build and a request
	// that could not be started are more specific answers than the class, and
	// overwriting either with "no Manifest was required" would bury the fact the
	// operator has to act on.
	// THE CLASS OWNS ITS OWN LOSING BET (S039 post-audit, R2.1).
	//
	// The no-distfile class writes an EMPTY Manifest and lets Portage arbitrate,
	// which is what makes it safe: a candidate that does declare an archive is
	// refused at "VERIFY FAILED! Insufficient data for checksum verification",
	// before any fetch is attempted. But Portage refuses before any PHASE MARKER
	// too, so derive reads the run as "died before this gate began" and leaves
	// the skip's cause unrecorded — and an unrecorded cause PROMOTES. The class
	// this story added was therefore reporting its own misclassification as a
	// pass, measured against the real ladder and confirmed before this fix.
	//
	// Only this class, and only the unrecorded ones. For an ordinary candidate a
	// build that died before its first phase may have died of a flaky mirror,
	// and blaming the ebuild there withdraws a bump over a fact about the
	// network — that reasoning still holds and is left alone. Here the empty
	// Manifest is a bet THIS PACKAGE placed, so the loss is the candidate's. The
	// costs are asymmetric in the same direction: a wrong refusal proves the
	// candidate again, a wrong promotion publishes an ebuild nothing measured.
	//
	// DeclineHost survives untouched: a missing `ebuild` and a refused isolation
	// are this machine's faults whichever class the candidate is in.
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
// it is here for what R1.5 asks that behaviour cannot show: a second copy of the
// ladder is a defect "even when both copies agree", and two agreeing copies
// produce equal bytes. Reaching the core is therefore asserted structurally —
// re-inline the sequence into a caller and the observation simply never happens.
var preparedBuildGates = runPreparedBuildGates

// PreparedBuildRequest is one already-chosen candidate plus everything the
// prepared build needs — the UPPER half of the two-half operation whose lower
// half (Stage, RunBuildGates) has been exposed as a seam since story 033.
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
// the overlay, realign holds the one candidate it just built (R1.5).
type PreparedBuildRequest struct {
	// Overlay is the published tree Stage copies the eclasses and profiles out
	// of; StagingRoot is where the single-package repository is built. Stage
	// refuses a staging root that resolves inside the overlay, which is what
	// keeps "never the overlay" a property of the code (S037-R2.4).
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
	// through rather than restated here (S037-R2).
	StagedManifest func(pkgDir string) ([]byte, error)

	// RequireIsolation is carried, not defaulted. The refusal inside
	// RunBuildGates fires only when the request carries it, and a policy that
	// applies to one of the two commands that build is not a policy (R1.3).
	RequireIsolation bool

	// LogDir is where the whole transcript is kept for whoever has to go past
	// the summary — the run that needs one is exactly the run that FAILED
	// (R1.4). Empty is still accepted and the gate's reason still says so.
	LogDir string

	// Distdir is the directory the build reads its archives from, set on the
	// child as DISTDIR (S039-R3.1). It is the caller's answer and never one
	// derived here — see BuildRequest.Distdir for why it travels as a field
	// instead of through the environment.
	//
	// Empty sets nothing and invents nothing (S039-R3.2), which is the same
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
// caller cannot acquire a rule of its own about what gets gated (R1.5).
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
//
// cause is what the skip DECLINED over (S039-R2.1) and it is a required argument
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
	// splitContentAtom, not splitStagedAtom: the Manifest must land in the
	// package directory Stage actually wrote, which is the suffix-stripped one
	// (design D4, role B). Joined from a key's ":slot" or "@label" spelling, the
	// file would sit in a directory Portage never reads, beside nothing.
	category, pkg, err := splitContentAtom(target.atom)
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

// prepareStagedManifest gives the staged tree the Manifest a build gate needs,
// and reports which of THREE answers the seam gave — because a candidate that
// needs no distfile is not a candidate whose Manifest failed to arrive (R4.1).
//
// It returns the reason naming that class when the third answer is taken, empty
// otherwise, and an error for the two faults — which stay materializeStagedManifest's
// own sentences, produced by calling it rather than by restating them here.
//
// # Why Portage decides the class, and no heuristic here does
//
// MEASURED on a host before this branch existed, and recorded because both
// obvious alternatives look reasonable until they are run:
//
//   - Portage answers NOTHING about an ebuild in a thin-manifest tree that has
//     no Manifest FILE. `portageq metadata / ebuild <cpv> SRC_URI` and
//     `ebuild <path> depend` both stop at "Manifest not found". So the class
//     cannot be asked of Portage BEFORE a Manifest exists: reading "this needs
//     no distfile" out of the ebuild's metadata is not a probe that can run.
//   - With an EMPTY Manifest present, Portage answers, and answers correctly. An
//     ebuild with no SRC_URI passes the fetch and verify checks and its phases
//     run; an ebuild WITH a SRC_URI dies at "VERIFY FAILED! Reason: Insufficient
//     data for checksum verification", exit 1.
//   - That refusal happens BEFORE any fetch is attempted. Measured with
//     SRC_URI="http://127.0.0.1:9/..." — a port where nothing listens — and no
//     connection error appeared and the distdir stayed empty. This path CANNOT
//     reach the network on a candidate whose digests are unknown, and it is
//     Portage's own ordering that guarantees it, not a rule this package keeps.
//
// So the empty file is not a guess standing in for digests: it is the smallest
// input that makes Portage answer the question at all, and Portage then
// discriminates. Nothing here scans SRC_URI, and nothing here reads the inherit
// list.
//
// # The seam is asked at most once, and only when the tree lacks a Manifest
//
// Both rules are materializeStagedManifest's, and both are kept by ORDER rather
// than by a second copy: the stat below is its own guard's predicate, and the
// two branches that take it hand the ORIGINAL lookup straight to it. When the
// seam is asked, its answer is memoised into a lookup of its own, so a producer
// that costs a subprocess is not paid for twice — and a producer that could
// answer differently the second time cannot classify one way and write another.
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
// deliberate and narrow: materializeStagedManifest is frozen by R4.2 — a test
// asserts both of its errors still fire — so it keeps its inline copy rather
// than being edited to call this.
func stagedManifestPath(stagedRoot string, target ebuildTarget) (string, error) {
	// splitContentAtom, in lockstep with materializeStagedManifest's inline
	// copy of this join: both must name the suffix-stripped directory Stage
	// wrote (design D4, role B), or the stat guarding the no-distfile class and
	// the write it guards would disagree about where the Manifest lives.
	category, pkg, err := splitContentAtom(target.atom)
	if err != nil {
		return "", err
	}
	return filepath.Join(stagedRoot, category, pkg, "Manifest"), nil
}

// emptyStagedManifest writes the third answer's Manifest and states the class
// (R4.4).
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
// the other question gateRungs answers, read in the other direction, so the
// mapping stays in one table.
//
// IT IS EXPORTED FOR THE CHECK PATH (S043-R4.1). `overlay autoupdate --check`
// decides whether the depth the POLICY SELECTED was actually measured, which
// takes the gate that stands for that rung; a table of its own in cmd/bentoo
// would be a fourth spelling of this mapping, and the bug that produces is a
// command reporting a rung the runner never proved.
//
// ok IS FALSE WHEN NO GATE PROVES d, which today means DepthNone and nothing
// else. The applier asks a DIFFERENT question with a similar name — which gate a
// repair should be aimed at (gateForDepth, applier_gates.go:863) — and falls
// back to the patch gate there, because a repair always has a target. This one
// must not: a depth-none bump ran no gate, and a fallback here would hand the
// caller a gate whose pass would read as proof of a rung nobody climbed.
//
// THE LOOP RETURNS THE DEEPEST GATE AT OR BELOW d, NOT A GATE PINNED TO d. The
// two coincide only while gateRungs covers every depth a plan can select, which
// it does today (options, patches, configure, compile, install). Give a depth no
// gate of its own — or hand `review` a rung — and a bump planned at that depth
// would read as proved on a SHALLOWER gate's pass, which is the over-report
// S043-R4.1 exists to remove, arrived at from the other side. Add the depth and
// its gate to gateRungs together, or make this exact. deepestPassedRung above is
// the exact form, for reference.
//
// FOLLOW-UP, DELIBERATELY NOT DONE HERE (S043): the depth↔gate mapping is spelt
// by three tables — gateDepths (applier_gates.go:46), gateRungs above, and
// buildGates (stage.go:697) — read by two functions with near-identical names.
// Collapsing them was rejected as out of scope because it would change the
// applier's repair-target fallback, which is behaviour, not bookkeeping.
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
