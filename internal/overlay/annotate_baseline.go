package overlay

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// AnnotateBaseline measures every compared package against the ::gentoo ebuild
// it should be compared with, and writes the answers onto the finished report:
// which baseline was used, how the two ebuilds differ structurally, what our
// ebuild declares about those differences, and what the three-way reduction made
// of the diff. It follows AnnotateAuthorship and AnnotateReviews.
//
// It runs ONLY when the review was requested: FormatReport never learns the
// flags, so every field written here renders nothing at its zero value, and
// nothing may call this from CompareWithProvider — that keeps `overlay compare`
// without `--realign` byte-identical. It runs after the sorted comparison and
// changes no Verdict, Status, count or ordering.
//
// It ends by rebuilding report.Findings with EstablishFindings; rebuilding
// rather than appending keeps a second call from doubling the list. A
// per-package failure is a way of NOT KNOWING and leaves the zero value; the
// run-level failure (no ::gentoo tree) is recorded by MarkBaselineSkipped, and
// the caller derives the exit code from it. It reads only files in the two
// trees: no command, no host, no git (the local ::gentoo is a shallow clone).
func AnnotateBaseline(report *CompareReport, prov provider.Provider, opts CompareOptions) {
	if report == nil {
		return
	}

	// The ::gentoo tree is reached THROUGH THE PROVIDER and never through a path
	// of its own. A review run is refused unless the provider is a
	// PackageDirProvider on ::gentoo, so the provider IS the tree, and a
	// second way to name it would be a second thing to disagree with the first.
	tree, ok := baselineTreeOf(prov, report.Results)
	if !ok {
		// No ::gentoo tree behind this provider at all, so NOTHING was examined —
		// the one outcome this review may not render as silence: omitting every
		// baseline field would report "we could not look" as "nothing differs".
		//
		// NoBaselineCount deliberately stays 0: "we could not look" is not
		// "::gentoo carries none of them", which a count of len(Results) would
		// assert. This state is the run's; Baseline.Unexamined is the package's.
		//
		// MarkBaselineSkipped records it as both a field and a FINDING, so the run
		// is not missing from every export and count that walks the findings. It
		// does both there rather than here so every caller gets it — `overlay
		// compare` marks the skip itself, before this pass is reached.
		MarkBaselineSkipped(report, baselineTreeCandidateOf(prov))
		return
	}

	for i := range report.Results {
		// Indexed rather than ranged over a copy: this pass exists to write
		// fields back onto the report the caller is holding.
		annotateOneBaseline(&report.Results[i], tree, opts)
	}

	// How many results ::gentoo carries no version of. It is a separate walk
	// over the finished results rather than a counter incremented above, so the
	// number and the rows can never disagree — a report claiming 3 while 4 rows
	// carry no baseline is the one failure a count like this has.
	report.NoBaselineCount = countNoBaseline(report.Results)

	// Everything the review just established, as values the caller receives.
	// It is LAST because it reads the fields written above: a
	// rebuild placed before the loop would produce the list this pass was called
	// to replace.
	EstablishFindings(report)
}

// annotateOneBaseline fills one result's baseline review.
//
// Each step is guarded by the one before it, and every guard leaves the fields
// it could not fill at their zero value rather than at a guess. That direction
// matters: an unfilled field costs the operator one manual diff, while a stated
// finding about an ebuild nobody opened is a claim the report cannot support.
func annotateOneBaseline(r *CompareResult, gentooTree string, opts CompareOptions) {
	atom := r.Category + "/" + r.Package

	baseline, err := ResolveBaseline(gentooTree, atom, r.LocalVersion)
	if err != nil {
		// A request that cannot be answered AS ASKED: a malformed atom, or a
		// result carrying no version of ours — which is what a failed comparison
		// leaves behind. Neither is a fact about ::gentoo, so nothing is
		// recorded and the zero Baseline says the review reached this package
		// with nothing to say.
		return
	}
	r.Baseline = baseline

	if !baseline.Found || baseline.Unexamined != "" || baseline.Path == "" {
		// Nothing readable to compare against. Baseline.Unexamined already
		// carries the reason where there is one, so the three content
		// answers below stay empty rather than restating it three times.
		return
	}

	ourEbuild := ourBaselineEbuild(*r, opts)
	if ourEbuild == "" {
		return
	}

	// The structural axes. CompareAxes errs only on a file that will not
	// read; the baseline side was read a moment ago by ResolveBaseline, so a
	// failure here is our own ebuild moving underneath the run — the same rare
	// case AnnotateReviews warns about, and worth the same one line, because it
	// is the difference between "the ebuilds agree on every axis" and "nobody
	// looked".
	axes, err := CompareAxes(ourEbuild, baseline.Path)
	if err != nil {
		opts.logger().Warn("overlay: the structural axes of a package could not be compared; its baseline review carries no findings, and the report is otherwise complete", "atom", atom, "err", err)
		return
	}
	r.Axes = axes

	// What the ebuild declares about those differences, with Expired decided
	// against the tree by production rather than by whoever reads it.
	//
	// The error and the value are BOTH used, which is ParseDivergences' own
	// contract: a malformed tag is reported while the well-formed declarations
	// beside it are still returned. Dropping them on the error would read as
	// "this divergence was never declared" and ask the maintainer to declare it
	// again on every run, forever.
	declared, err := ParseDivergences(ourEbuild)
	if err != nil {
		opts.logger().Warn("overlay: a package declares a divergence this reader could not parse; the well-formed declarations beside it are still reported", "atom", atom, "err", err)
	}
	if len(declared) > 0 {
		r.Declarations = EvaluateDeclarations(declared, gentooTree, atom)
	}

	// The three-way reduction. The HUNKS are discarded: the report prints how
	// many differences fell into each class and never their text, because
	// rendering the baseline's own lines is indistinguishable from offering them
	// as a replacement — the wholesale-realignment proposal this review exists to
	// prevent.
	//
	// The third point is CHOSEN here rather than left nil: a ::gentoo version
	// above the baseline where one exists, our own previous version otherwise (86
	// of the 158 packages whose version differs from ::gentoo's have one). Where
	// neither exists nil travels, and the report says no third point was
	// available. It costs at most two directory listings per package and reads
	// no extra ebuild — ReduceDiff reads the pair itself.
	//
	// The WIDTH of what is offered is not checked here and must not be:
	// reduceSpan owns that bound, and a chooser that withheld a wide pair would
	// report Span 0 — "no third point existed" — where one existed and was
	// refused for being too wide. The report distinguishes those two facts.
	move := baselineVersionMove(gentooTree, opts.OverlayPath, atom, r.LocalVersion, baseline)
	classified, _ := ReduceDiff(ourEbuild, baseline.Path, move)
	r.Classified = classified
}

// baselineVersionMove chooses the third point the reduction subtracts for one
// package — the pair of ebuilds whose difference IS a version move — or nil when
// this overlay has none: preference 1 is a ::gentoo version above the baseline,
// preference 2 our own previous version.
//
// It answers from the CONTENT of the two trees: one directory listing each,
// through the same carriedVersions the baseline is chosen with. It runs no
// command and consults no git — the local ::gentoo is a shallow clone without
// history. A directory that will not list yields no third point, not an error.
//
// It does not judge the WIDTH of what it offers: reduceSpan owns that bound, and
// a pair withheld here would reach the report as Span 0 rather than as the width
// that refused it. Under a nearest-version baseline preference 1 is refused by
// that bound every time today; it stays first because it is a capability.
// Whether a refused preference 1 falls back to preference 2 is deliberately not
// decided: a fallback chosen quietly here is a subtraction nobody agreed to.
func baselineVersionMove(gentooTree, overlayPath, atom, ourVersion string, baseline Baseline) *VersionMove {
	if ourVersion == "" || !baseline.Found || baseline.Version == "" || baseline.Path == "" {
		// No baseline to move away from, or no version of ours to move to. Both
		// preferences are pairs anchored on one of those two, so there is nothing
		// to choose between.
		return nil
	}
	category, pkg, err := splitBaselineAtom(atom)
	if err != nil {
		// Not one package, so there is no directory to list. Unreachable from the
		// annotation pass, which got this far only because ResolveBaseline accepted
		// the same atom, and refused here anyway: both halves are joined onto a
		// tree root below, and a ".." would read an ebuild from another package
		// entirely.
		return nil
	}

	if move := gentooVersionMoveAbove(gentooTree, category, pkg, baseline); move != nil {
		return move
	}
	return ourPreviousVersionMove(overlayPath, category, pkg, ourVersion)
}

// gentooVersionMoveAbove is the first preference: the move from the baseline up
// to the next version ::gentoo carries, or nil when it carries none above it.
//
// It is FIRST because the move is then written in ::gentoo's own hand — better
// provenance for the same subtraction than a move written in ours, since nobody
// here decided any of it.
//
// It must START at the baseline, and at baseline.Path itself rather than at a
// path rebuilt from baseline.Version. A hunk of ours matches a hunk of the move
// only when the two share their before-text, and ours is diffed from the
// baseline; and the file the reduction subtracts from is then the very one the
// report named as the baseline, rather than a second answer to where that ebuild
// is.
func gentooVersionMoveAbove(gentooTree, category, pkg string, baseline Baseline) *VersionMove {
	dir := filepath.Join(gentooTree, category, pkg)
	newer, ok := adjacentVersion(carriedIn(dir, category, pkg), baseline.Version, versionAbove)
	if !ok {
		return nil
	}
	return &VersionMove{From: baseline.Path, To: filepath.Join(dir, newer.filename)}
}

// ourPreviousVersionMove is the second preference: the move OUR OWN last bump
// made, from the version we carried before this one to the one we carry now, or
// nil when the overlay carries no previous version.
//
// This is the third point this overlay actually has — 86 of its 158
// different-version packages carry one, against 1 that has a ::gentoo version
// above the baseline — and running the shipped matching rule proved that it
// reads it correctly. What our bump changed on text that still agreed with the
// baseline renders as the same two-sided hunk in both diffs and is subtracted as
// version noise; a divergence that predates the bump is absent from the move
// entirely and stays ours, which is the deliberate work the reduction exists to
// leave behind.
func ourPreviousVersionMove(overlayPath, category, pkg, ourVersion string) *VersionMove {
	if overlayPath == "" {
		return nil
	}
	dir := filepath.Join(overlayPath, category, pkg)
	carried := carriedIn(dir, category, pkg)

	// Our own ebuild is taken from the listing by STRING equality, exactly as
	// pickBaseline takes a baseline at our version: the question is which file
	// carries our version, not which versions order equally. Two ebuild files are
	// identified by their version text, and PMS equality (1.0 and 1.0-r0, 1.010
	// and 1.01) is not file identity — ending the move at the wrong one would
	// offer a diff nobody's report describes.
	ours, ok := carriedAt(carried, ourVersion)
	if !ok {
		// Our version is not in the overlay's own listing for this package, though
		// the scan found it a moment ago. That is the tree moving underneath the
		// run, and no pair can be built without the end of it.
		return nil
	}
	previous, ok := adjacentVersion(carried, ourVersion, versionBelow)
	if !ok {
		// Nothing of ours below our own version — the answer for the 72 of the 158
		// this overlay carries no previous version of. The rule is
		// that there is then no third point, not a worse one.
		return nil
	}
	return &VersionMove{
		From: filepath.Join(dir, previous.filename),
		To:   filepath.Join(dir, ours.filename),
	}
}

// The two sides a neighbouring version can lie on, in ebuild.CompareVersions'
// own answers so the direction and the comparison cannot drift apart.
const (
	versionAbove = 1
	versionBelow = -1
)

// carriedIn lists the versions one repository carries of one package, and is the
// ONE listing both preferences above are chosen from.
//
// It is carriedVersions with a directory in front of it rather than a second
// walker: what an ebuild filename is, and which files belong to this package
// rather than to a neighbour like gst-plugins-qt6-doc, stays decided in exactly
// one place. dropWhenCarried opens the same two lines for the declaration
// evaluator and is deliberately not reused — it answers a third question this has
// no use for, and its tree parameter is named for ::gentoo while half the
// listings here are of our own overlay.
//
// A directory that will not list — absent, unreadable, or not a directory at all
// — carries no candidate as far as this is concerned. The distinction
// ResolveBaseline draws between the two, "::gentoo does not carry it" and "we
// could not look", is worth a field on the report; here both answers lead to the
// same place, which is that no third point can be built out of it.
func carriedIn(dir, category, pkg string) []carriedEbuild {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return carriedVersions(entries, category, pkg)
}

// carriedAt finds the entry carrying exactly this version string.
func carriedAt(carried []carriedEbuild, version string) (carriedEbuild, bool) {
	for _, candidate := range carried {
		if candidate.version == version {
			return candidate, true
		}
	}
	return carriedEbuild{}, false
}

// adjacentVersion is the carried version NEXT TO pivot on one side: the lowest
// above it, or the highest below it.
//
// The NEIGHBOUR is what both preferences want because of the width bound:
// reduceSpan refuses a third point spanning further than the baseline is from
// us, so the narrowest pair is the one most likely to be accepted. A wider pair
// carries more changes and could match more of our differences — that is the
// argument against it: more churn in the move is more room for a deliberate
// divergence of ours to be subtracted as version noise.
//
// The ordering is ebuild.CompareVersions, the same in both directions — never
// versionDistance, which measures a magnitude and cannot say which version is
// later.
//
// A candidate that orders EQUAL to the pivot (1.0-r0 beside 1.0) is on neither
// side and is skipped: the difference between them is not a version move. 1.0.0
// is not such a candidate — it is a real version above 1.0.
func adjacentVersion(carried []carriedEbuild, pivot string, direction int) (carriedEbuild, bool) {
	var nearest carriedEbuild
	found := false
	for _, candidate := range carried {
		if ebuild.CompareVersions(candidate.version, pivot) != direction {
			continue
		}
		// Nearer to the pivot means further from the direction of travel: going
		// up, the smaller candidate wins; going down, the larger one does.
		if !found || ebuild.CompareVersions(candidate.version, nearest.version) == -direction {
			nearest, found = candidate, true
		}
	}
	return nearest, found
}

// baselineTreeOf resolves the ::gentoo repository root behind a provider. ok is
// false when the provider has no local tree at all, which is the API-only case.
//
// It is derived from LocalPackagePath — the capability the comparison already
// type-asserts for — because the review is refused unless the provider is a
// PackageDirProvider on ::gentoo. Both implementations answer
// <root>/<category>/<package>, so the root is what remains once the two names
// are taken back off, and the names are CHECKED against the answer: a provider
// that resolved a package elsewhere would otherwise yield a root nobody named.
//
// It walks the results because the first package need not be one ::gentoo
// carries (84 packages answer ErrNotFound). When the walk answers for NONE of
// them — `--realign --only-outdated` with nothing outdated in ::gentoo — that is
// still a tree: the provider is asked where its tree is, and the answer is
// RECOGNISED through LocateBaselineTree's Portage marker rather than trusted.
// Reading it as "no tree" made the review a silent no-op at exit 0. The walk
// comes FIRST because for a `--clone` provider its LocalPackagePath calls are
// what bring the tree onto disk.
func baselineTreeOf(prov provider.Provider, results []CompareResult) (string, bool) {
	dirProv, ok := prov.(provider.PackageDirProvider)
	if !ok {
		return "", false
	}

	for _, r := range results {
		if r.Category == "" || r.Package == "" {
			continue
		}
		dir, err := dirProv.LocalPackagePath(r.Category, r.Package)
		if err != nil {
			continue
		}
		if filepath.Base(dir) != r.Package || filepath.Base(filepath.Dir(dir)) != r.Category {
			continue
		}
		root := filepath.Dir(filepath.Dir(dir))
		if root == "" || root == "." {
			continue
		}
		return root, true
	}

	// Nothing in view could answer. That is a fact about the SELECTION, not about
	// the repository, so the repository is asked directly before the review gives
	// up on it.
	if root, err := LocateBaselineTree(baselineTreeCandidateOf(prov)); err == nil {
		return root, true
	}
	return "", false
}

// baselineTreeCandidateOf names the directory a provider reads its repository
// from, or "" when it has none to name.
//
// It asks the ONE concrete type that carries a tree. *provider.GitCloneProvider
// is what both `provider: local` and `--clone` build, and it is the only
// production implementation of PackageDirProvider there is — the capability
// interface names a package DIRECTORY and has no member for the root the
// directory sits in. The command already resolves the same path the same way
// (realignBaselineTreeCandidate), so this is the existing answer asked from the
// other side rather than a second notion of where a tree lives.
//
// NOTHING IS TRUSTED ABOUT WHAT COMES BACK. Every caller puts it through
// LocateBaselineTree, which is what turns a path into a recognised repository —
// and which is why returning "" for a provider that has no tree costs nothing:
// LocateBaselineTree refuses an empty candidate by naming the marker it could not
// look for.
func baselineTreeCandidateOf(prov provider.Provider) string {
	clone, ok := prov.(*provider.GitCloneProvider)
	if !ok {
		return ""
	}
	return clone.LocalPath
}

// ourBaselineEbuild names the overlay-side ebuild at the compared version, or ""
// when it cannot be named.
//
// It is built here rather than through resolvePackagePaths because that resolver
// refuses two DIFFERENT versions on purpose — it exists to compare like with
// like — and a baseline is most interesting precisely when ::gentoo is at
// another version. The content is compared regardless of the version
// relationship, so this side is named from what the scan already found.
//
// Nothing here comes from registry input. Category and Package are the directory
// names the overlay scan produced, and the caller reached this line only because
// ResolveBaseline accepted "<Category>/<Package>" as an atom — which refuses an
// empty component, a "." or a ".." and anything carrying a separator. The
// version is the ebuild filename's own.
func ourBaselineEbuild(r CompareResult, opts CompareOptions) string {
	if opts.OverlayPath == "" || r.Category == "" || r.Package == "" || r.LocalVersion == "" {
		return ""
	}
	return filepath.Join(opts.OverlayPath, r.Category, r.Package, r.Package+"-"+r.LocalVersion+".ebuild")
}

// countNoBaseline is how many results ::gentoo carries no version of.
//
// It counts Baseline.Found and nothing else. Every other candidate definition —
// results with no Axes, results the comparison called not-in-remote — answers a
// different question and would drift from this one the first time a package
// matched one and not the other.
func countNoBaseline(results []CompareResult) int {
	missing := 0
	for _, r := range results {
		if !r.Baseline.Found {
			missing++
		}
	}
	return missing
}

// The per-package indents (baselineFindingLead, baselineClassLead) were removed
// with their renderer: the indent an operator sees is decided by the renderer in
// internal/common/report, over the notes cmd/bentoo builds from the findings.

// baselineRunFindings is what the baseline review established about the RUN
// rather than about any one package: today, exactly the outcome that may never
// render as silence.
//
// It reads report.BaselineSkipped rather than re-deciding anything, so the
// finding and the field cannot disagree, and produces nothing when the field is
// empty — every run that reached a ::gentoo tree, and every run that asked for
// no review at all.
//
// It has to be a finding because "we could not look" and "we looked and
// everything matched" produce the same per-package output; a consumer of the
// per-package findings alone would read the first as the second, and the field
// alone goes missing from every export and count that walks the findings.
//
// It carries NO ATOM, and Finding.Atom's own doc says why that is this one
// kind's property and nobody else's.
func baselineRunFindings(report *CompareReport) []Finding {
	if report == nil || report.BaselineSkipped == "" {
		return nil
	}
	return []Finding{{
		Kind: FindingBaselineSkipped,
		// MarkBaselineSkipped's sentence, verbatim. It already names the tree
		// that was looked for and the marker that was looked for in it, which
		// are the identifiers an operator needs to fix the configuration.
		Detail: report.BaselineSkipped,
	}}
}

// baselineResultsFindings is the baseline review of a slice of results, in the
// results' own order — the order the report prints them in, so a renderer that
// walks the list writes the report rather than inventing a second arrangement.
func baselineResultsFindings(results []CompareResult) []Finding {
	var findings []Finding
	for _, r := range results {
		findings = append(findings, baselineResultFindings(r)...)
	}
	return findings
}

// baselineResultFindings is one package's baseline review as VALUES, or nil when
// the review has nothing to say about it — which is every package of every run
// that requested no review, and is what carries the byte-identical promise
// through to the rendering.
//
// The ORDER is the order the report prints: what we measured against, how the
// two ebuilds differ, what our ebuild declares about those differences, what the
// reduction made of them, what a model said, and finally who else carries the
// package. Each answers the previous one, and a renderer walking the list in
// order writes the block unchanged.
//
// EVERY piece of text here — an axis detail read out of an ebuild, a
// maintainer's declared reason, a model's verdict — is passed as an ARGUMENT and
// never as a format string, exactly like every other piece of text this report
// prints. None of it reaches a shell, a command or a file. Each is collapsed
// onto one line as it is stated, because the report's structure IS its lines: a
// newline inside an ebuild's text would forge a finding about a package that
// need not exist.
func baselineResultFindings(r CompareResult) []Finding {
	atom := r.Category + "/" + r.Package

	findings := measuredAgainstFindings(atom, r)

	// One finding per axis that differs. The DETAIL is what makes it actionable
	// — "the inherit lines differ" would not have told anyone which eclass
	// ::gentoo delegates the option list to — so it is stated rather than
	// counted.
	//
	// The AXIS WORD stays inside the sentence rather than becoming a field of
	// its own. Finding's fields are the ones a consumer keys on, and the one it
	// keys on is the atom; a field nothing reads yet is a guess at what a later
	// redesign of the report's content will want.
	for _, axis := range r.Axes {
		findings = append(findings, Finding{
			Kind:     FindingAxisDivergence,
			Atom:     atom,
			Detail:   fmt.Sprintf("%s differs from ::%s — %s", axis.Axis, baselineRepo, oneLine(axis.Detail)),
			Version:  r.LocalVersion,
			Upstream: r.Baseline.Version,
		})
	}

	for _, declaration := range r.Declarations {
		findings = append(findings, baselineDeclarationFinding(atom, r, declaration))
	}

	if classification, classified := classificationFinding(r); classified {
		findings = append(findings, classification)
	}

	// The model's verdict, and it SAYS WHOSE WORDS FOLLOW, on the same argument
	// reviewReadingLead makes: everything else here is something the tool
	// established by reading two files, and an operator who cannot tell the two
	// apart will act on the wrong one.
	//
	// The words are ALSO held apart in Effect, with EffectReviewed on them, so
	// that a renderer which wants to label the guess rather than repeat this
	// sentence has the guess as a value. Detail keeps the whole sentence for the
	// reason Detail's doc gives: today's rendered line survives the move
	// unchanged, and a later rewrite has a before to diff against.
	if verdict := oneLine(r.RealignVerdict); verdict != "" {
		findings = append(findings, Finding{
			Kind:     FindingRealignVerdict,
			Atom:     atom,
			Detail:   "realignment verdict — a model's reading, not a finding of this report: " + verdict,
			Version:  r.LocalVersion,
			Upstream: r.Baseline.Version,
			Effect:   reviewed(verdict),
		})
	}

	for _, other := range r.Others {
		findings = append(findings, baselineOtherRepoFinding(atom, other))
	}

	return findings
}

// measuredAgainstFindings names the ebuild this package was measured against
// or says that the one that should have been read could not be.
//
// The zero Baseline yields NOTHING, and that is load-bearing rather than
// tidiness: a package ::gentoo does not carry has the zero value, and so does
// every package of every run that requested no review. The two are
// indistinguishable here by construction, which is why the first of them is
// reported once, as a count, by formatBaselineSummary.
//
// The two states it can report are separate FINDINGS rather than one with a
// hedge in it, because a consumer counting "how many packages could not be
// measured" must be able to do so without reading prose — which is the whole of
// why the findings are values.
func measuredAgainstFindings(atom string, r CompareResult) []Finding {
	baseline := r.Baseline
	if baseline == (Baseline{}) {
		return nil
	}

	// The repository is NAMED and never assumed, which is what the field is
	// for. baselineRepo stands in only for a value built by hand without one:
	// this package refuses a realignment run against any other repository long
	// before this line, so the fallback restates that invariant rather than
	// inventing a source for the evidence.
	repo := baseline.Repo
	if repo == "" {
		repo = baselineRepo
	}

	if !baseline.Found {
		// Reachable only with Unexamined set: ResolveBaseline reports a package
		// ::gentoo does not carry as the zero value, which returned above.
		return []Finding{{
			Kind:    FindingBaselineUnexamined,
			Atom:    atom,
			Detail:  fmt.Sprintf("no ::%s baseline was examined — %s", repo, oneLine(baseline.Unexamined)),
			Version: r.LocalVersion,
		}}
	}

	findings := []Finding{{
		Kind: FindingBaseline,
		Atom: atom,
		Detail: fmt.Sprintf("baseline ::%s %s (%s) — %s",
			repo, baseline.Version, baselineDistanceProse(baseline.Distance), baseline.Path),
		// The two versions the finding is about, as the pair Finding names them:
		// ours, and the one ::gentoo carried that we measured against. Held apart
		// rather than joined, so a consumer asking "did the two sides agree?"
		// compares two values instead of splitting a sentence.
		Version:  r.LocalVersion,
		Upstream: baseline.Version,
	}}
	if baseline.Unexamined != "" {
		findings = append(findings, Finding{
			Kind:     FindingBaselineUnexamined,
			Atom:     atom,
			Detail:   "that baseline could not be read — " + oneLine(baseline.Unexamined),
			Version:  r.LocalVersion,
			Upstream: baseline.Version,
		})
	}
	return findings
}

// baselineDistanceProse says how far the baseline is from our version, in the
// release steps versionDistance measures.
//
// It is printed because it BOUNDS what the comparison is worth: a baseline
// one patch release away and one three series away are not the same kind of
// evidence, and a difference measured against the second is a question rather
// than a finding. The number alone would not say that, so the zero case gets
// words instead — 0 is not "very close", it is the same ebuild version.
func baselineDistanceProse(distance int) string {
	switch distance {
	case 0:
		return "the same version"
	case 1:
		return "1 release step away"
	default:
		return fmt.Sprintf("%d release steps away", distance)
	}
}

// baselineDeclarationFinding states one `# BENTOO-DIVERGENCE:` declaration the
// ebuild carries.
//
// An EXPIRED declaration is the loud one and is its own KIND, not a word inside
// the sentence. Its condition was evaluated against the tree and is met, so the
// divergence it was protecting is back in front of the review — and a finding a
// renderer could not tell apart from every other declaration would leave it
// quiet forever. Said as a kind,
// the distinction survives into a count, a filter and the export; said only as
// the word "EXPIRED" inside prose, it survives into a terminal and nowhere else.
func baselineDeclarationFinding(atom string, r CompareResult, declaration DeclaredDivergence) Finding {
	// The axis and the reason are the maintainer's own words, verbatim: nothing
	// here normalises the spelling, because the tag format writes INHERIT, the
	// axes write inherit, and neither side is authoritative.
	detail := fmt.Sprintf("(%s) — %s", declaration.Axis, oneLine(declaration.Reason))
	if declaration.DropWhen != "" {
		detail += fmt.Sprintf(" · drop-when: %s", oneLine(declaration.DropWhen))
	}

	finding := Finding{
		Kind:     FindingEbuildDeclaration,
		Atom:     atom,
		Detail:   "declared divergence " + detail,
		Version:  r.LocalVersion,
		Upstream: r.Baseline.Version,
		// What the divergence DOES, in the maintainer's own words and labelled
		// as theirs. It is the ebuild's tag rather than the registry's `patched`
		// text, which is the same commitment written in the other file — see
		// EffectDeclared — so a renderer weighing this against a model's reading
		// weighs it the same way either way.
		Effect: declared(declaration.Reason),
	}
	if declaration.Expired {
		finding.Kind = FindingExpiredDeclaration
		finding.Detail = "EXPIRED declaration " + detail
	}
	return finding
}

// The classification rationale outlives the removed classificationLines
// renderer, because it is about the value and not its rendering: the three
// classes are three NUMBERS rather than one, so "how many differences are
// unclassified" can be read without arithmetic; the denominator is the
// arithmetic classifiedTotal states rather than a fourth number typed out, since
// a total computed twice eventually disagrees with itself; and the REACH is
// carried beside the counts, because a share whose reach is invisible is
// indistinguishable from a guess.

// classificationFinding is one package's classification as a VALUE, and false
// when nothing was classified for it.
//
// Every result of every run that requested no review is in exactly that state,
// which is what keeps the byte-identical promise mechanical here: no
// classification, no finding, no lines, nothing to join.
//
// An UNCLASSIFIED difference is counted in Unclassified and in NEITHER of the
// other two fields. Pushing it into Ours would invite a realignment of
// something nobody read, and pushing it into VersionMove would subtract
// deliberate work as noise. That rule used to be stated by the renderer that
// printed the three classes on three lines; it is a property of the value, so it
// is stated and asserted here instead.
//
// The COUNTS ride on the finding as numbers, in Classified, beside the sentence
// that reads them out. That is the point of this block: a consumer
// summing unclassified differences across a run, or exporting them as JSON,
// reads three integers instead of parsing digits back out of a sentence — and
// the sentence is still there, verbatim, for the report that prints it.
func classificationFinding(r CompareResult) (Finding, bool) {
	if r.Classified == (Classified{}) {
		return Finding{}, false
	}
	return Finding{
		Kind: FindingClassification,
		Atom: r.Category + "/" + r.Package,
		Detail: fmt.Sprintf("%d differences against the baseline, %s",
			classifiedTotal(r.Classified), baselineReachProse(r.Classified)),
		Version:    r.LocalVersion,
		Upstream:   r.Baseline.Version,
		Classified: r.Classified,
	}, true
}

// The per-class lines (the lead sentence, then the three per-class counts under
// it) are built by `func comparePkgFacts` in cmd/bentoo from classificationFinding's value,
// carried on ComparePkg.FurtherFindings, and rendered by internal/common/report.

// classifiedTotal is the denominator every one of the three counts is a share
// of: the number of differences the reduction actually looked at.
//
// It is one function rather than an expression written out at each site so the
// per-package block and the run-level one can never disagree about what the
// total is — and so that a class added to Classified later is added to the total
// in the one place that decides it.
func classifiedTotal(classified Classified) int {
	return classified.VersionMove + classified.Ours + classified.Unclassified
}

// baselineReachProse says how far the classification reached, from the three
// fields Classified reports it in: whether a third point was accepted, how wide
// it was, and how much of the diff it actually explained.
//
// The COUNT is read as well as the pair. `Reduced` says a third point was
// accepted, never that it attributed anything, and our own previous version can
// be accepted and explain nothing for a package whose every difference predates
// its bump. Written from the pair alone the sentence would read `reduced against
// a third point spanning N release steps` over a subtraction of zero — a
// confident sentence about work that did not happen.
//
// The count is read OFF THE VALUE rather than taken as a second parameter, so
// there is no second way of saying what was attributed. `Reduced` keeps its
// meaning: only what is SAID about an accepted third point that attributed
// nothing changes.
func baselineReachProse(classified Classified) string {
	switch {
	case classified.Reduced && classified.Span == 0:
		// Our version and the baseline are the same one, so no version move
		// existed for anything to be attributed to.
		return "attributed without a third point: the baseline is our own version"
	case classified.Reduced && classified.Span > 0 && classified.VersionMove == 0:
		// A third point was accepted, and it explained none of the differences.
		//
		// The SPAN half is what the arm above already implies in this order, and
		// it is written out anyway so the condition says what the row IS rather
		// than what its neighbour happens to have taken first. On the count alone
		// this arm swallows the same-version case the moment the two are
		// reordered — and that row's version-move count is zero because no
		// version move ever existed, not because a third point came up empty.
		// Reporting it here would invent a third point that was never offered.
		//
		// The WIDTH is still stated. A third point existed, and the span is the
		// only thing that tells this state apart from the one where none was
		// available.
		return fmt.Sprintf("a third point spanning %d release steps explained nothing", classified.Span)
	case classified.Reduced:
		return fmt.Sprintf("reduced against a third point spanning %d release steps", classified.Span)
	case classified.Span > 0:
		return fmt.Sprintf("NOT reduced: the third point offered spans %d release steps, wider than the baseline is from us", classified.Span)
	default:
		return "NOT reduced: no third point was available, so nothing was attributed"
	}
}

// baselineOtherRepoFinding states one repository other than ::gentoo.
//
// The NOT CHECKED case is the reason this has three arms instead of two.
// "Registered but not available locally" and "looked, and it does not carry it"
// are different answers, and stating the first as the second would report a
// repository nobody consulted as one that has nothing.
//
// Every arm says INFORMATIVE ONLY in one form or another, because that is the
// requirement: a repository outside ::gentoo has not been through the
// same review, and nothing here proposes a realignment from one.
//
// NEITHER Version NOR Upstream is filled, and that is not an omission. Both name
// a side of the ::gentoo comparison — Finding.Version is ours, Upstream is
// ::gentoo's — and the version this finding is about belongs to a third
// repository that is explicitly never a baseline. Putting it in Upstream would
// label another repository's ebuild as ::gentoo's, which is the one thing the
// informative-only rule prevents, so it stays inside the sentence until the
// report's content is redesigned with somewhere honest to put it.
func baselineOtherRepoFinding(atom string, other OtherRepo) Finding {
	finding := Finding{Kind: FindingOtherRepo, Atom: atom}
	switch {
	case !other.Checked:
		finding.Detail = fmt.Sprintf("::%s was NOT checked — it is registered but its contents are not available locally, which is not the same as it not carrying the package", other.Name)
	case other.Version == "":
		finding.Detail = fmt.Sprintf("::%s was checked and carries no version of it", other.Name)
	default:
		finding.Detail = fmt.Sprintf("also carried by ::%s at %s — informative only, never a baseline", other.Name, other.Version)
	}
	return finding
}
