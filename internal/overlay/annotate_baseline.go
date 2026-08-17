package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// AnnotateBaseline measures every compared package against the ::gentoo ebuild
// it should be compared with, and writes the answers onto the finished report:
// which baseline was used (R1.1), how the two ebuilds differ structurally
// (R2.4), what our ebuild declares about those differences (R3.1), and what the
// three-way reduction made of the diff (R2.4, R2.5).
//
// It follows AnnotateAuthorship and AnnotateReviews exactly, which is the point
// of it existing at all. Each of those producers already answers its own
// question; without one pass writing the answers onto CompareResult, every one
// of them returns a value into the air and no renderer can read it.
//
// # It runs ONLY when the review was requested
//
// That is the whole mechanism behind the promise that `overlay compare` without
// `--realign` is byte-identical to the shipped command (R7.2). FormatReport
// takes a *CompareReport and never learns which flags were passed, so the gate
// cannot live in the renderer: instead every field this pass writes renders
// nothing at its zero value, and the pass that fills them is called by the
// caller that asked for the review. Nothing calls it from inside
// CompareWithProvider, and nothing may — a plain comparison must leave every one
// of these fields untouched.
//
// # It runs AFTER the comparison, and changes nothing the comparison decided
//
// Like both passes it mirrors, it runs once the concurrent comparison has
// returned and the results are sorted, so nothing here is concurrent. It writes
// five fields onto CompareResult plus one counter on the report, and reads
// everything else. No Verdict, no Status, no count and no ordering can change
// (R4.3, R5.8): zero those six fields again and the rendering is byte-identical
// to the one the comparison produced.
//
// # It returns nothing, deliberately
//
// Every PER-PACKAGE way this can fail is a way of NOT KNOWING — an ebuild that
// will not read, an atom that names no package — and the zero value already says
// that, in the one way the report can print: by saying nothing.
//
// The RUN-LEVEL failure, "there is no ::gentoo tree at all", is the one thing
// that may not render as silence, so it is recorded rather than returned:
// MarkBaselineSkipped writes it onto the report, and the caller derives the exit
// code from that field (D9). The command still asks LocateBaselineTree for itself
// before any comparison is run — this pass does not replace that check, it stops
// the same state from being unreported when the pass is reached without one,
// which AnnotateRealignVerdicts does whenever a caller has not annotated yet.
//
// It reads only files, inside the two trees it was given. It runs no command,
// resolves no host and consults no git — the local ::gentoo is a shallow clone
// whose history is absent by construction (R1.4).
//
// _Requirements: R1, R1.1, R2, R2.4, R6, R6.1, R6.4_
func AnnotateBaseline(report *CompareReport, prov provider.Provider, opts CompareOptions) {
	if report == nil {
		return
	}

	// The ::gentoo tree is reached THROUGH THE PROVIDER and never through a path
	// of its own. A review run is refused unless the provider is a
	// PackageDirProvider on ::gentoo (R7.4), so the provider IS the tree, and a
	// second way to name it would be a second thing to disagree with the first.
	tree, ok := baselineTreeOf(prov, report.Results)
	if !ok {
		// No ::gentoo tree behind this provider at all, so NOTHING was examined —
		// and that is the one outcome this review may not render as silence
		// (R1.5). Omitting every baseline field would leave the run
		// indistinguishable from one whose packages all matched ::gentoo exactly,
		// which is "we could not look" reported as "nothing differs".
		//
		// NoBaselineCount deliberately stays 0 beside it. "We could not look" is
		// not "::gentoo carries none of them", and the second is precisely what a
		// count of len(Results) would assert. The two states travel in different
		// channels: this one is the run's, and Baseline.Unexamined is the
		// package's.
		MarkBaselineSkipped(report, baselineTreeCandidateOf(prov))
		return
	}

	for i := range report.Results {
		// Indexed rather than ranged over a copy: this pass exists to write
		// fields back onto the report the caller is holding.
		annotateOneBaseline(&report.Results[i], tree, opts)
	}

	// R6.4, computed as the very sentence the requirement states: how many
	// results ::gentoo carries no version of. It is deliberately a separate walk
	// over the finished results rather than a counter incremented above, so the
	// number and the rows can never disagree — a report claiming 3 while 4 rows
	// carry no baseline is the one failure a count like this has.
	report.NoBaselineCount = countNoBaseline(report.Results)
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
		// carries the reason where there is one (D2), so the three content
		// answers below stay empty rather than restating it three times.
		return
	}

	ourEbuild := ourBaselineEbuild(*r, opts)
	if ourEbuild == "" {
		return
	}

	// The structural axes (R2.4). CompareAxes errs only on a file that will not
	// read; the baseline side was read a moment ago by ResolveBaseline, so a
	// failure here is our own ebuild moving underneath the run — the same rare
	// case AnnotateReviews warns about, and worth the same one line, because it
	// is the difference between "the ebuilds agree on every axis" and "nobody
	// looked".
	axes, err := CompareAxes(ourEbuild, baseline.Path)
	if err != nil {
		warnLogf("overlay: the structural axes of %s could not be compared (%v); its baseline review carries no findings, and the report is otherwise complete", atom, err)
		return
	}
	r.Axes = axes

	// What the ebuild declares about those differences (R3.1), with Expired
	// decided against the tree by production rather than by whoever reads it
	// (R3.3).
	//
	// The error and the value are BOTH used, which is ParseDivergences' own
	// contract: a malformed tag is reported while the well-formed declarations
	// beside it are still returned. Dropping them on the error would read as
	// "this divergence was never declared" and ask the maintainer to declare it
	// again on every run, forever.
	declared, err := ParseDivergences(ourEbuild)
	if err != nil {
		warnLogf("overlay: %s declares a divergence this reader could not parse (%v); the well-formed declarations beside it are still reported", atom, err)
	}
	if len(declared) > 0 {
		r.Declarations = EvaluateDeclarations(declared, gentooTree, atom)
	}

	// The three-way reduction (R2.4, R2.5). The HUNKS are discarded: the report
	// prints how many differences fell into each class and never their text,
	// because rendering the baseline's own lines is indistinguishable from
	// offering them as a replacement — which is the wholesale-realignment
	// proposal this story exists to prevent.
	//
	// The third point is CHOSEN here rather than left nil (R3). The chooser
	// offers a ::gentoo version above the baseline where one exists, and our own
	// previous version otherwise — the third point this overlay actually has, in
	// 86 of the 158 packages whose version differs from ::gentoo's. Where it has
	// neither, nil still travels, and the report says no third point was
	// available rather than pretending one was subtracted (R3.3).
	//
	// What it costs is ONE MORE DIRECTORY LISTING per package, at most two: the
	// ::gentoo package directory ResolveBaseline listed a moment ago is listed
	// again for the versions above the baseline, and our own is listed only when
	// that found nothing. No extra ebuild is read here — ReduceDiff reads the
	// pair itself, and refuses it unread when the span is too wide — and no
	// command is run, on the same terms as everything else on this path (R1.4).
	//
	// The WIDTH of what is offered is not checked here and must not be:
	// reduceSpan owns that bound (R3.4), and a chooser that withheld a wide pair
	// itself would report Span 0 — "no third point existed" — for a package where
	// one existed and was refused for being too wide. Those are different facts
	// about the same package, and the report distinguishes them (R2.5).
	move := baselineVersionMove(gentooTree, opts.OverlayPath, atom, r.LocalVersion, baseline)
	classified, _ := ReduceDiff(ourEbuild, baseline.Path, move)
	r.Classified = classified
}

// baselineVersionMove chooses the third point the reduction subtracts for one
// package — the pair of ebuilds whose difference IS a version move — or nil when
// this overlay has none to offer (R3, R3.3).
//
// It answers from the CONTENT of the two trees and nothing else: one directory
// listing each, read through the same carriedVersions the baseline itself is
// chosen with, so nothing here is a second notion of what an ebuild filename is.
// It runs no command and consults no git, for the reason ResolveBaseline does not
// either — the local ::gentoo is a shallow clone whose history is absent by
// construction, so a chooser that read a log would work on a fixture and answer
// nothing on the host (R1.4).
//
// # It states D3's order and nothing else
//
// Preference 1 is a ::gentoo version above the baseline, preference 2 is our own
// previous version, and where neither exists the answer is nil (R3.3). Each
// preference decides for itself, below, what it will offer and why; this
// function's whole content is which of them is asked first.
//
// # It does not judge the WIDTH of what it offers
//
// reduceSpan owns that bound (R3.4), and this story leaves reduce.go untouched. A
// pair withheld here for being wide would reach the report as no pair at all —
// Span 0 — while a pair offered and refused reports the width that refused it,
// and those are different facts about the same package (R2.5). Under a corrected
// nearest-version baseline preference 1 is refused by that bound every time
// today (D4); it stays first and stays implemented because R3.2 makes it a
// capability. Whether a REFUSED preference 1 should fall back to preference 2 is
// deliberately not decided here: nothing in the overlay pins it, and a fallback
// chosen quietly inside a chooser is a subtraction nobody agreed to.
//
// A directory that will not list yields NO THIRD POINT rather than an error, as
// everything on this path already degrades: this is a report, and "we could not
// look for a third point" renders as "there was none" — the same sentence, and
// the same exit code.
//
// _Requirements: R2, R2.1, R3, R3.1, R3.2, R3.3, R3.4_
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

// gentooVersionMoveAbove is D3's first preference: the move from the baseline up
// to the next version ::gentoo carries, or nil when it carries none above it
// (R3.2).
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
//
// _Requirements: R3, R3.2_
func gentooVersionMoveAbove(gentooTree, category, pkg string, baseline Baseline) *VersionMove {
	dir := filepath.Join(gentooTree, category, pkg)
	newer, ok := adjacentVersion(carriedIn(dir, category, pkg), baseline.Version, versionAbove)
	if !ok {
		return nil
	}
	return &VersionMove{From: baseline.Path, To: filepath.Join(dir, newer.filename)}
}

// ourPreviousVersionMove is D3's second preference: the move OUR OWN last bump
// made, from the version we carried before this one to the one we carry now, or
// nil when the overlay carries no previous version (R3.1, R3.3).
//
// This is the third point this overlay actually has — 86 of its 158
// different-version packages carry one, against 1 that has a ::gentoo version
// above the baseline — and D3 proved by execution that the shipped matching rule
// reads it correctly. What our bump changed on text that still agreed with the
// baseline renders as the same two-sided hunk in both diffs and is subtracted as
// version noise; a divergence that predates the bump is absent from the move
// entirely and stays ours, which is the deliberate work the reduction exists to
// leave behind.
//
// _Requirements: R3, R3.1, R3.3_
func ourPreviousVersionMove(overlayPath, category, pkg, ourVersion string) *VersionMove {
	if overlayPath == "" {
		return nil
	}
	dir := filepath.Join(overlayPath, category, pkg)
	carried := carriedIn(dir, category, pkg)

	// Our own ebuild is taken from the listing by STRING equality, exactly as
	// pickBaseline takes a baseline at our version: the question is which file
	// carries our version, not which versions order equally. ebuild.CompareVersions
	// reads 1.0 and 1.0.0 as one version and those are two different ebuilds —
	// ending the move at the wrong one would offer a diff nobody's report
	// describes.
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
		// this overlay carries no previous version of. D3's third preference is
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
// The NEIGHBOUR is what both preferences want, and that is the width bound's
// doing rather than a preference for small numbers. reduceSpan refuses a third
// point spanning further than the baseline is from us (R3.4), so the narrowest
// pair is the one most likely to be accepted at all — offering ::gentoo's newest
// version, or our oldest, offers a pair that is refused more often and subtracts
// nothing when it is. A wider pair does carry more changes and could match more
// of our differences; that is the argument AGAINST it, not for it, on the same
// reasoning the bound itself rests on: more churn in the move is more room for a
// deliberate divergence of ours to be subtracted as version noise.
//
// Both sides are answered by one function because they are one question asked in
// two directions, and because the ordering behind them must be the same in both.
// That ordering is ebuild.CompareVersions — the repository's own, never a second
// opinion assembled here, and never versionDistance, which measures a magnitude
// and cannot say which of two versions is later.
//
// A candidate that orders EQUAL to the pivot is on neither side and is skipped.
// That is what keeps 1.0.0 from being offered as the version above 1.0: the two
// are different ebuilds, but the difference between them is not a version move,
// and a pair built from them would subtract one file's text from the other's
// under a name nothing supports.
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
// It is derived from LocalPackagePath — the one capability the comparison itself
// already type-asserts for (resolvePackagePaths) — rather than from a path
// parameter, because the review is refused outright unless the provider is a
// PackageDirProvider on ::gentoo (R7.4). Both implementations answer
// <root>/<category>/<package>, so the root is what remains once the two names
// the caller supplied are taken back off, and the two names are CHECKED against
// the answer rather than assumed: a provider that resolved a package somewhere
// else entirely would otherwise have its parent's parent read as a repository
// root, and every baseline in the report would come from a directory nobody
// named.
//
// It walks the results rather than asking about one, because the first package
// need not be one ::gentoo carries: a real provider answers ErrNotFound for the
// 84 packages that have no counterpart, and taking that for "no tree" would
// disable the review on an overlay whose first category happens to be
// Bentoo's own.
//
// # A result set that answers for NONE of them is still a tree
//
// The walk needs one package ::gentoo carries to be IN VIEW, and a run can
// legitimately have none: `--realign --only-outdated` where nothing outdated has
// a counterpart, or any selection that lands entirely on the overlay's own work.
// Read as "no tree", that made the whole review a silent no-op at exit 0 over a
// repository that was synced and perfectly readable — the same failure R1.5
// exists to prevent, arriving through which packages the operator asked to see.
//
// So when the walk answers for nobody, the provider is asked where its own tree
// is, and the answer is RECOGNISED rather than trusted: LocateBaselineTree wants
// Portage's own marker, so a directory standing where a repository should be is
// refused here exactly as it is refused to the command. That check stats two
// paths and reads nothing.
//
// The walk comes FIRST and keeps every answer it used to give, which is not only
// conservatism: for a `--clone` provider the walk's own LocalPackagePath calls are
// what bring the tree onto disk, and a marker looked for before that would be
// looked for in a directory nothing had cloned into yet.
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
// another version. R2.1 says the content is compared regardless of the version
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

// countNoBaseline is R6.4's number: how many results ::gentoo carries no version
// of (R1.3).
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

// The two indents the baseline review prints under.
//
// baselineFindingLead is the same "  ↳ " the verification findings and the model
// commentary already use, so a baseline line reads as one more thing said about
// the row above rather than as a second report. baselineClassLead sits one level
// under it, for the per-class counts that belong to the line introducing them.
//
// They are constants so a test can name them without copying the wording, on the
// same argument that made undeclaredDivergenceCaveat one.
const (
	baselineFindingLead = "  ↳ "
	baselineClassLead   = "      "
)

// baselineSummaryLead opens the run-level coverage line.
const baselineSummaryLead = "Baseline coverage: "

// classificationSummaryLead opens the run-level classification block: how much
// of the whole run's diff the reduction and the model actually explained.
//
// It is a constant for the reason baselineSummaryLead is one — a test can name
// the line without copying its wording — and it is a SEPARATE line from
// realignSummaryLead on purpose. "How many divergences went unjudged" and "how
// many differences nobody attributed" are different numbers over different
// denominators, and one line carrying both would be read as one claim.
const classificationSummaryLead = "Difference classification: "

// formatBaselineFindings renders the baseline review for a section's results,
// beneath its table and beneath the verification findings.
//
// IT RENDERS NOTHING FOR A RESULT WHOSE FIELDS ARE ALL ZERO, which is every
// result of every run that requested no review — and that is the entire
// mechanism behind R7.2's byte-identical promise. A report the pass never
// touched appends the empty string here and prints what it printed yesterday.
//
// The other half of that promise is that these fields ARE read: a field written
// by the review and rendered by nobody would keep the promise true and make it
// worthless, since the whole story could then ship as dead struct fields.
func formatBaselineFindings(results []CompareResult) string {
	var sb strings.Builder
	for _, r := range results {
		sb.WriteString(baselineResultLines(r))
	}
	return sb.String()
}

// baselineResultLines renders one package's baseline review, or "" when it has
// nothing to say.
//
// EVERY piece of text on these lines — an axis detail read out of an ebuild, a
// maintainer's declared reason, a model's verdict — is passed as an ARGUMENT and
// never as a format string, exactly like every other piece of text this report
// prints. None of it reaches a shell, a command or a file.
func baselineResultLines(r CompareResult) string {
	atom := r.Category + "/" + r.Package

	var sb strings.Builder
	sb.WriteString(baselineLine(atom, r.Baseline))

	// One line per axis that differs. The DETAIL is what makes the finding
	// actionable — "the inherit lines differ" would not have told anyone which
	// eclass ::gentoo delegates the option list to — so it is printed rather
	// than counted, collapsed onto one line so an ebuild cannot break the table
	// beneath it.
	for _, axis := range r.Axes {
		sb.WriteString(output.Sprintf(output.Warning, "%s%s: %s differs from ::%s — %s\n",
			baselineFindingLead, atom, axis.Axis, baselineRepo, oneLine(axis.Detail)))
	}

	for _, declared := range r.Declarations {
		sb.WriteString(baselineDeclarationLine(atom, declared))
	}

	sb.WriteString(joinReportLines(classificationLines(r)))

	// The model's verdict, and it SAYS WHOSE WORDS FOLLOW, on the same argument
	// reviewReadingLead makes: everything else on these lines is something the
	// tool established by reading two files, and an operator who cannot tell the
	// two apart will act on the wrong one.
	if verdict := oneLine(r.RealignVerdict); verdict != "" {
		sb.WriteString(output.Sprintf(output.Dim, "%s%s: realignment verdict — a model's reading, not a finding of this report: %s\n",
			baselineFindingLead, atom, verdict))
	}

	for _, other := range r.Others {
		sb.WriteString(baselineOtherRepoLine(atom, other))
	}

	return sb.String()
}

// baselineLine names the ebuild this package was measured against (R1.1), or
// says that the one that should have been read could not be.
//
// The zero Baseline renders NOTHING, and that is load-bearing rather than
// tidiness: a package ::gentoo does not carry has the zero value, and so does
// every package of every run that requested no review. The two are
// indistinguishable here by construction, which is why the first of them is
// reported once, as a count, by formatBaselineSummary.
func baselineLine(atom string, baseline Baseline) string {
	if baseline == (Baseline{}) {
		return ""
	}

	// The repository is NAMED and never assumed (R1), which is what the field is
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
		return output.Sprintf(output.Warning, "%s%s: no ::%s baseline was examined — %s\n",
			baselineFindingLead, atom, repo, oneLine(baseline.Unexamined))
	}

	var sb strings.Builder
	sb.WriteString(output.Sprintf(output.Info, "%s%s: baseline ::%s %s (%s) — %s\n",
		baselineFindingLead, atom, repo, baseline.Version, baselineDistanceProse(baseline.Distance), baseline.Path))
	if baseline.Unexamined != "" {
		sb.WriteString(output.Sprintf(output.Warning, "%s%s: that baseline could not be read — %s\n",
			baselineFindingLead, atom, oneLine(baseline.Unexamined)))
	}
	return sb.String()
}

// baselineDistanceProse says how far the baseline is from our version, in the
// release steps versionDistance measures.
//
// It is printed because it BOUNDS what the comparison is worth (D2): a baseline
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

// baselineDeclarationLine renders one `# BENTOO-DIVERGENCE:` declaration the
// ebuild carries (R3.1).
//
// An EXPIRED declaration is the loud one and says so first. Its condition was
// evaluated against the tree and is met, so the divergence it was protecting is
// back in front of the review — and a line that read like every other
// declaration would leave it quiet forever, which is the failure R3.3 exists to
// prevent.
func baselineDeclarationLine(atom string, declared DeclaredDivergence) string {
	// The axis and the reason are the maintainer's own words, verbatim: nothing
	// here normalises the spelling, because §7 writes INHERIT and D4 writes
	// inherit and neither side is authoritative.
	detail := fmt.Sprintf("(%s) — %s", declared.Axis, oneLine(declared.Reason))
	if declared.DropWhen != "" {
		detail += fmt.Sprintf(" · drop-when: %s", oneLine(declared.DropWhen))
	}

	if declared.Expired {
		return output.Sprintf(output.Warning, "%s%s: EXPIRED declaration %s\n", baselineFindingLead, atom, detail)
	}
	return output.Sprintf(output.Info, "%s%s: declared divergence %s\n", baselineFindingLead, atom, detail)
}

// classificationLines renders one package's per-class counts WITH the total they
// are a share of (R2.4, R2.5), or nothing for a package nothing was classified
// for.
//
// The three classes get a line each rather than one line with three numbers on
// it, so that "how many differences are unclassified" can be read without
// arithmetic — and so that the unclassified count cannot be mistaken for either
// of the other two, which is what R2.3 is about. An unclassified difference is
// counted in the third line and in NEITHER of the first two: pushing it into
// "ours" would invite a realignment of something nobody read, and pushing it
// into "version move" would subtract deliberate work as noise.
//
// The DENOMINATOR is on the line that introduces them, once, and it is the
// arithmetic classifiedTotal states rather than a fourth number typed out here —
// three counts whose total is invisible let a reader infer the missing one
// wrongly, and a total computed twice eventually disagrees with itself.
//
// The REACH of the classification is stated beside it too, because a share whose
// reach is invisible is indistinguishable from a guess (R2.5). Reduced, Span and
// the version-move count are read together and never separately: false with Span
// 0 means no third point existed, false with a large Span means one existed and
// was refused for being too wide, and true with a span but nothing attributed
// means one was accepted and explained nothing (D5). Those are different facts
// about the same package, and baselineReachProse is where they are told apart.
//
// It takes the whole result rather than the atom and the Classified so a
// renderer holding one result cannot pair one package's counts with another's
// name, and it returns LINES rather than a block so the run-level builder below
// can be assembled and asserted the same way (D-8.1).
//
// _Requirements: R2, R2.3, R2.4, R2.5, R7.2_
func classificationLines(r CompareResult) []string {
	if r.Classified == (Classified{}) {
		// Every result of every run that requested no review is in exactly this
		// state, which is the whole of what keeps R7.2's byte-identical promise
		// mechanical here: no classification, no lines, nothing to join.
		return nil
	}

	atom := r.Category + "/" + r.Package
	classified := r.Classified

	lead := output.Sprintf(output.Info, "%s%s: %d differences against the baseline, %s",
		baselineFindingLead, atom, classifiedTotal(classified), baselineReachProse(classified))
	return append([]string{lead}, classificationClassLines(classified)...)
}

// runClassificationLines renders the run-level classification block: how many of
// all the differences this run examined were attributed to the version move, how
// many to us, and how many to neither — with the number they are a share of
// (R2.3, R2.5).
//
// The denominator is the point, and it is the same point formatBaselineSummary's
// is. "122 differences were attributed to nobody" is unreadable alone, and "63%
// unclassified" is worse: 63% of twelve differences in one package and 63% of
// forty thousand across the overlay are different claims, and a classification
// whose reach is invisible is indistinguishable from a guess.
//
// It is derived ENTIRELY from the per-result Classified fields — no counter on
// the report, nothing accumulated during the pass. That is not tidiness: it is
// what makes the block and the per-package lines above it incapable of
// disagreeing, on the same argument countNoBaseline is a separate walk rather
// than a counter incremented as the results are annotated.
//
// It renders NOTHING when no result carries a classification, which is every run
// that requested no review (R7.2) — the same predicate, `== (Classified{})`, that
// silences the per-package lines, so the two cannot fall out of step.
//
// _Requirements: R2, R2.3, R2.5, R7.2_
func runClassificationLines(report *CompareReport) []string {
	if report == nil {
		return nil
	}

	var run Classified
	packages := 0
	for _, r := range report.Results {
		if r.Classified == (Classified{}) {
			// Never classified — a package with no readable baseline, or any
			// package at all on a run that asked for no review. Counting it as a
			// package with zero differences would inflate the reach of the
			// classification with rows nobody looked at.
			continue
		}
		packages++
		run.VersionMove += r.Classified.VersionMove
		run.Ours += r.Classified.Ours
		run.Unclassified += r.Classified.Unclassified
	}
	if packages == 0 {
		return nil
	}

	lead := output.Sprintf(output.Info, "%s%d differences examined across %d of the packages reviewed — %d of them were attributed to neither the version move nor to us",
		classificationSummaryLead, classifiedTotal(run), packages, run.Unclassified)
	return append([]string{lead}, classificationClassLines(run)...)
}

// classificationClassLines renders the three per-class counts, and is shared by
// the per-package block and the run-level one so that the second reads as the
// sum of the firsts rather than as a second way of saying the same thing.
//
// Sharing it is not only de-duplication. The two blocks sit in one report, and
// an operator reads the run-level one as the total of the rows above it — which
// they can only do if the labels, their order and their indent are the same
// three, decided once. A class renamed on one of them and not the other would
// read as two different classifications of the same diff.
//
// _Requirements: R2.3, R2.4_
func classificationClassLines(classified Classified) []string {
	return []string{
		output.Sprintf(output.Info, "%sversion move: %d", baselineClassLead, classified.VersionMove),
		output.Sprintf(output.Info, "%sours: %d", baselineClassLead, classified.Ours),
		// Third and separate, always. An unclassified difference is one the
		// reduction and the model both declined to attribute, and it is counted
		// here and in neither line above (R2.3).
		output.Sprintf(output.Info, "%sunclassified: %d", baselineClassLead, classified.Unclassified),
	}
}

// classifiedTotal is the denominator every one of the three counts is a share
// of: the number of differences the reduction actually looked at.
//
// It is one function rather than an expression written out at each site so the
// per-package block and the run-level one can never disagree about what the
// total is — and so that a class added to Classified later is added to the total
// in the one place that decides it.
//
// _Requirements: R2.4, R2.5_
func classifiedTotal(classified Classified) int {
	return classified.VersionMove + classified.Ours + classified.Unclassified
}

// joinReportLines turns a line builder's output into the block the report
// appends, one line each. It returns "" for no lines, which is what carries a
// builder's silence through to the rendering unchanged (R7.2).
func joinReportLines(lines []string) string {
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// baselineReachProse says how far the classification reached, from the three
// fields Classified reports it in: whether a third point was accepted, how wide
// it was, and how much of the diff it actually explained.
//
// The COUNT is read as well as the pair, and that is the whole of D5. `Reduced`
// says a third point was accepted, never that it attributed anything, and a third
// point can be accepted and explain nothing — which is exactly what our own
// previous version does for a package whose every difference predates its bump.
// Written from the pair alone the sentence then reads `reduced against a third
// point spanning N release steps` over a subtraction of zero: the claim R2.3
// forbids, and a confident sentence about work that did not happen is worse than
// the honest one it replaces.
//
// The count is read OFF THE VALUE rather than taken as a second parameter,
// because it is already in there. A caller that had to pass the same number
// alongside the struct carrying it would be a second way of saying what was
// attributed, and eventually a second answer.
//
// `Reduced` itself is unchanged, and deliberately: what moves is what is SAID
// about an accepted third point that attributed nothing, not whether one was
// accepted. The field keeps its meaning and the three counts keep theirs.
//
// _Requirements: R2, R2.2, R2.3, R2.5_
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
		// available (R2.5).
		return fmt.Sprintf("a third point spanning %d release steps explained nothing", classified.Span)
	case classified.Reduced:
		return fmt.Sprintf("reduced against a third point spanning %d release steps", classified.Span)
	case classified.Span > 0:
		return fmt.Sprintf("NOT reduced: the third point offered spans %d release steps, wider than the baseline is from us", classified.Span)
	default:
		return "NOT reduced: no third point was available, so nothing was attributed"
	}
}

// baselineOtherRepoLine renders one repository other than ::gentoo (R6.1).
//
// The NOT CHECKED case is the reason this has three arms instead of two.
// "Registered but not available locally" and "looked, and it does not carry it"
// are different answers, and printing the first as the second would report a
// repository nobody consulted as one that has nothing.
//
// Every arm says INFORMATIVE ONLY in one form or another, because that is the
// requirement (R6.2): a repository outside ::gentoo has not been through the
// same review, and nothing here proposes a realignment from one.
func baselineOtherRepoLine(atom string, other OtherRepo) string {
	switch {
	case !other.Checked:
		return output.Sprintf(output.Dim, "%s%s: ::%s was NOT checked — it is registered but its contents are not available locally, which is not the same as it not carrying the package\n",
			baselineFindingLead, atom, other.Name)
	case other.Version == "":
		return output.Sprintf(output.Dim, "%s%s: ::%s was checked and carries no version of it\n",
			baselineFindingLead, atom, other.Name)
	default:
		return output.Sprintf(output.Dim, "%s%s: also carried by ::%s at %s — informative only, never a baseline\n",
			baselineFindingLead, atom, other.Name, other.Version)
	}
}

// formatBaselineSummary renders the run-level coverage line: how many packages
// the review found no ::gentoo counterpart for, WITH the number it is a share of
// (R6.4).
//
// The denominator is the point. "84 packages have no baseline" is unreadable
// on its own — 84 out of 100 and 84 out of 40,000 are different claims — and the
// count exists to say how much of the overlay this review could not measure.
//
// It is ComparedPackages rather than len(Results) because Results is the VIEW: a
// --only-redundant run narrows it after the annotation pass has already run, and
// a denominator that shrank with the filter could print a share larger than one.
// The wording is careful for the mirror-image reason: with a status filter in
// play the review ranges over fewer packages than were compared, so the line
// says what was FOUND rather than asserting anything about the rows it never
// examined.
//
// It renders nothing at 0, which is every run that requested no review (R7.2).
func formatBaselineSummary(report *CompareReport) string {
	if report == nil || report.NoBaselineCount <= 0 {
		return ""
	}
	return output.Sprintf(output.Info,
		"\n%s%d of the %d packages compared were found to have no ::%s counterpart — those are the overlay's own work rather than a divergence from anyone's, and no realignment is proposed for them\n",
		baselineSummaryLead, report.NoBaselineCount, report.ComparedPackages, baselineRepo)
}
