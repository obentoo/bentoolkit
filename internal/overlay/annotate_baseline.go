package overlay

import (
	"fmt"
	"path/filepath"
	"strings"

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
// Every way this can fail is a way of NOT KNOWING — no local tree behind the
// provider, an ebuild that will not read, an atom that names no package — and
// the zero value already says that, in the one way the report can print: by
// saying nothing. The run-level failure, "there is no ::gentoo tree at all", is
// not this function's to report either: it is ErrNoBaselineTree from
// LocateBaselineTree, decided by the caller before any comparison is run,
// because it is the one condition the command exits non-zero for (D9).
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
		// No local tree behind this provider, so nothing was examined. Every
		// field stays zero — including NoBaselineCount, which must not be set
		// here: "we could not look" is not "::gentoo carries none of them", and
		// the second is what a count of len(Results) would assert.
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
	// The third point is nil, and D3's first preference CANNOT supply one under
	// a nearest-version baseline. A hunk of ours matches a hunk of the move only
	// when the two diffs share the same BEFORE-text, which is the baseline; so a
	// usable move must end at the baseline, starting between it and us —
	// and ResolveBaseline already took the nearest version, so nothing lies
	// there. Every pair ::gentoo could offer subtracts exactly nothing, which
	// makes Reduced=false with Span=0 — "no third point was offered" — the
	// truthful answer rather than a gap. Proved by execution; the chooser is a
	// named follow-up, not something to improvise here.
	classified, _ := ReduceDiff(ourEbuild, baseline.Path, nil)
	r.Classified = classified
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
	return "", false
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
// reach is invisible is indistinguishable from a guess (R2.5). Reduced and Span
// are read as a pair and never separately: false with Span 0 means no third
// point existed, while false with a large Span means one existed and was refused
// for being too wide, and those are different facts about the same package.
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

// baselineReachProse says how far the classification reached, from the pair
// Classified reports it in.
func baselineReachProse(classified Classified) string {
	switch {
	case classified.Reduced && classified.Span == 0:
		// Our version and the baseline are the same one, so no version move
		// existed for anything to be attributed to.
		return "attributed without a third point: the baseline is our own version"
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
