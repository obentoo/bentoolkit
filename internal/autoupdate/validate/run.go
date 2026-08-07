package validate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// Options is what a run needs. All three come from the command line or the
// config; none is discovered, so a run is fully described by this struct.
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
func Run(ctx context.Context, opts Options) (Report, error) {
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

	// The distdir is one answer for the whole run, so it is resolved once. It
	// is only ever READ from: Locate creates nothing and proves nothing
	// writable, which is what lets a validate run work against the portage-owned
	// DISTDIR the invoking user cannot write (design D2).
	distdir, haveDistdir := distfiles.Locate(opts.Distdir, "")

	qa := map[string]qaResult{}
	for _, target := range targets {
		res := validateOptions(ctx, target, distdir, haveDistdir)
		attachQA(ctx, &res, target, qa)
		report.Results = append(report.Results, res)
	}
	return report, nil
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
// The order — ebuild, then distfile, then archive — is cheapest-first, and it
// also produces the most specific diagnostic: an ebuild that cannot be read is
// reported as exactly that, rather than as whichever later step happened to
// fail second.
func validateOptions(ctx context.Context, target ebuildTarget, distdir string, haveDistdir bool) EbuildResult {
	passed, err := OptionsFromEbuild(target.path)
	if err != nil {
		return skippedResult(target.atom, target.version, fmt.Sprintf("the ebuild could not be read: %v", err))
	}

	if !haveDistdir {
		return skippedResult(target.atom, target.version,
			"no distdir could be located, so there is no archive to read the upstream options from")
	}

	archive, err := findDistfile(target.dir, distdir)
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

// findDistfile returns the path of the first distfile the package's Manifest
// names that is actually present in distdir.
//
// A Manifest naming several distfiles is ordinary — a package with patches or
// bundled subprojects has one line per file — and the FIRST present one is the
// source archive in every layout measured. The error names every candidate, so
// a skip says which files were looked for rather than only that none was found.
func findDistfile(pkgDir, distdir string) (string, error) {
	names := distfiles.ParseManifestDistFilenames(filepath.Join(pkgDir, "Manifest"))
	if len(names) == 0 {
		return "", errors.New("the package's Manifest names no distfile, so there is no archive to read")
	}
	for _, name := range names {
		path := filepath.Join(distdir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("no distfile named by the Manifest is present in %s: %s",
		distdir, strings.Join(names, ", "))
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
// do next. This also keeps a skipped result's single Reason field unambiguous —
// it holds the reason the OPTION gate skipped, never two reasons spliced.
func attachQA(ctx context.Context, res *EbuildResult, target ebuildTarget, cache map[string]qaResult) {
	if res.Options == OutcomeSkipped {
		return
	}

	got, seen := cache[target.dir]
	if !seen {
		findings, outcome, reason := PkgcheckFindings(ctx, target.dir, target.atom)
		got = qaResult{findings: findings, outcome: outcome, reason: reason}
		cache[target.dir] = got
	}

	res.QA = got.outcome
	res.Findings = append(res.Findings, got.findings...)
	if got.outcome == OutcomeSkipped {
		res.Reason = got.reason
	}
}
