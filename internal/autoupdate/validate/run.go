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
// The plain values come from the command line or the config. DistNames does not
// — it is a SEAM, a function the caller supplies so that a package directory
// which cannot name its own upstream archives can still be validated. Its zero
// value is nil, and nil is exactly the behaviour every field of this struct had
// before it existed (S037-R1.2), which is why it is listed last: a caller
// written against the older struct is still describing the same run.
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
// the build gates are reported as SKIPPED NAMING WHAT STOPPED THEM rather than
// left out: the governing rule above applies to the ladder itself, and a report
// that simply omitted the configure gate the operator asked for would be the
// silence this whole package exists to remove.
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
	for _, target := range targets {
		res := validateOptions(ctx, target, distdir, haveDistdir, distNames)
		// Before attachQA, so the gates read in ladder order — options, then the
		// build gates, then the advisory QA scan that decides nothing.
		noteBuildDepth(&res, depth, opts.StagingRoot)
		attachQA(ctx, &res, target, qa)
		report.Results = append(report.Results, res)
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

// noteBuildDepth records, on a result the static gate has just produced, that a
// depth above `options` was asked for and how far this entry point got (R4.4,
// R6.4, R11.2).
//
// # Why the build gates SKIP here instead of running
//
// RunBuildGates drives `ebuild <staged candidate> clean <phase>`, and Portage
// refuses an ebuild whose Manifest does not describe its archive. Stage
// deliberately does not carry the published Manifest across — it describes the
// versions already published, not the candidate — so the staged tree needs a
// manifest step, and that step (`pkgdev manifest`, with its fetch, its timeout
// and its own repair path) lives on the apply side, in package autoupdate. It
// cannot be reached from here: applier.go already imports this package, so the
// import back would be a cycle.
//
// Running the gates anyway would produce a confident FAILED for an ebuild that
// is fine — the false-FAILED failure mode findDistfile's own notes describe, and
// the one that gets a gate switched off. So the depth is reported unreached,
// with the reason and the command that does reach it.
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
func noteBuildDepth(res *EbuildResult, depth Depth, stagingRoot string) {
	if depth <= DepthOptions {
		return
	}

	reached := DepthNone
	for _, gate := range res.Gates {
		if gate.Gate == GateOptions && gate.Outcome == OutcomePass {
			reached = DepthOptions
		}
	}

	reason := buildDepthNotRunReason(depth, stagingRoot)
	res.Depth = reached.String()
	res.DepthRequested = depth.String()
	res.DepthReason = reason
	res.Gates = append(res.Gates, SkippedGates(depth, reason)...)
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
// word "supplied", which is what tells an operator reading a SKIP that no
// Manifest decided this — so going to look for one would be looking in the wrong
// place, and the caller that handed the names over is what has to be fixed
// (S037-R1.6).
var suppliedSource = distNameSource{
	origin:     "the distfile list the caller supplied",
	listed:     "the caller supplied",
	attributed: "; the names searched for were supplied by the caller, not read from a Manifest",
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
