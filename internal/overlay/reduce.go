package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// The three classes one difference can carry, spelled once here so a consumer
// selects on these rather than on a string it typed a second time.
//
// They are the report's own vocabulary rather than an internal detail: a run
// states how many of its differences fell into each of them (R2.4, R2.5).
const (
	// hunkVersionMove is a difference a version move already contains — the same
	// change, in the same hand, between two versions nobody here decided
	// anything about. It is noise from the bump, not a packaging decision.
	hunkVersionMove = "version-move"
	// hunkOurs is a difference the reduction had the evidence to attribute to a
	// version move and could not: something we did on purpose, or at least
	// something the version move does not account for.
	hunkOurs = "ours"
	// hunkUnclassified is a difference NOBODY attributed. It is deliberately not
	// "ours by elimination" (R2.3): with the version move genuinely mixed in and
	// no evidence separating it, calling the residue ours is the tempting answer
	// and the one that would propose realigning 492 lines of deliberate slotting
	// work as though they were noise.
	hunkUnclassified = "unclassified"
)

// The two marks a hunk's text carries, one per side, in `diff`'s own spelling
// and orientation: the baseline is what was there, ours is what is there now.
//
// Both sides are rendered into the SAME string on purpose. A hunk that carried
// only the baseline's lines would lose every line we added and nothing else,
// and a hunk that carried only ours would lose every line we dropped — either
// way the reduction would be matching half a difference against half another,
// and a line that exists on one side only is exactly the kind that carries a
// decision.
const (
	hunkBaselineMark = "-"
	hunkOursMark     = "+"
)

// Hunk is one contiguous difference between the baseline ebuild and ours,
// together with what the reduction concluded caused it.
//
// The line range is measured in the BASELINE, which is the side both the range
// and the diff are oriented from: this file compares baseline -> ours, the same
// direction diffLineCounts already reports DiffAdded/DiffRemoved in. Start and
// End are 1-based and the range is HALF-OPEN, [Start, End), so a pure insertion
// — a hunk that removes nothing — is the empty range Start == End at the line it
// was inserted before, exactly as a unified diff's `@@ -5,0 +6,2 @@` says it.
//
// _Requirements: R2.2, R2.3, R2.4_
type Hunk struct {
	// Class is version-move, ours or unclassified, and is never empty on a hunk
	// this package returns: every difference lands in exactly one class, which
	// is what makes the three counts add up to the number of differences (R2.4).
	Class string
	// Start and End are the half-open line range the hunk replaces in the
	// baseline, 1-based.
	Start, End int
	// Text is the hunk in both hands: every baseline line prefixed "-", then
	// every line of ours prefixed "+". It carries NO line numbers, and that is
	// what makes it comparable across two different diffs — a version move
	// shifts every line below it, so a hunk that matched by position would stop
	// matching as soon as anything above it changed size.
	Text string
}

// Classified is what the reduction concluded about one package's differences:
// how many fell into each class, and how much evidence it had to work with.
//
// The counts are per DIFFERENCE (per hunk), not per line. The size of the
// difference in lines is already reported beside them, by CompareResult's
// DiffAdded and DiffRemoved, and a second line count here would be a second
// answer to a question that already has one.
//
// Reduced and Span are reported together because neither reads correctly alone.
// Reduced=false with Span=0 is "no third point existed"; Reduced=false with a
// large Span is "a third point existed and was REFUSED for being too wide", and
// those are different facts about the same package. A classification whose reach
// is invisible is indistinguishable from a guess (R2.5).
//
// It renders nothing at its zero value, which is what lets it ride on an
// existing result without changing a byte of what a run that asked for no
// baseline review prints.
//
// _Requirements: R2.2, R2.3, R2.4, R2.5_
type Classified struct {
	// VersionMove, Ours and Unclassified are the per-class counts. They always
	// sum to the number of hunks returned beside them.
	VersionMove, Ours, Unclassified int
	// Reduced reports that the classification is an attributed one rather than a
	// shrug: either a usable third point was accepted (D3's first two
	// preferences), or the baseline is our own version and no version noise was
	// possible in the first place.
	Reduced bool
	// Span is the version distance the third point covers, in the release steps
	// versionDistance measures — the same unit as Baseline.Distance, because the
	// bound compares the two. It is 0 when no third point was offered, and it is
	// reported even when the third point was refused: the refusal is only legible
	// if the width that caused it is printed beside it.
	Span int
}

// VersionMove is the pair of ebuilds whose difference IS a version move: the
// third point the reduction subtracts.
//
// It carries the already-chosen pair rather than a bare third path because D3's
// two preferences diff different pairs — `diff(baseline, gentoo_newer)` when
// ::gentoo carries two versions spanning ours, `diff(our_previous, ours)` when
// we carry the previous version — and a callee handed one lone path could not
// tell which of the two roles it was being given. Choosing is the caller's job;
// measuring and subtracting is this file's.
//
// Both fields are PATHS to ebuild files, like ReduceDiff's own two, because the
// span is a distance between two VERSIONS and the version is in the filename.
//
// _Requirements: R2.2_
type VersionMove struct{ From, To string }

// ReduceDiff compares our ebuild against its baseline and says, difference by
// difference, what caused each one — so that the model is asked only what
// deterministic work could not answer.
//
// our and baseline are PATHS to ebuild files, as in CompareAxes. They are paths
// and not contents because the versions are in the filenames and the reduction
// needs all four of them: Span is a version distance and the bound compares it
// against the baseline distance, so a contents-taking signature would have
// needed two more parameters carrying what the paths already say.
//
// # What it does, in D3's order
//
//  1. It diffs the FULL CONTENT of the two ebuilds, whether or not the versions
//     match (R2.1). Everything below only decides how the differences are
//     labelled; none of it decides whether they are looked for.
//  2. Same version: no version move exists between the two files, so no
//     difference between them can be attributed to one. Every difference is ours
//     by construction and no model is needed to say so — this is the nodejs
//     case, 492 differing lines at one version, all of them ours.
//  3. Different versions, with a usable third point: the third point's own diff
//     is a version move written in somebody's hand, and any hunk of ours that
//     matches a hunk of it is that move showing through. What survives is ours.
//  4. Different versions with no usable third point: nothing is attributed. The
//     whole diff is unclassified and the report says the classification was
//     unreduced (R2.3).
//
// # The third point is bounded
//
// An unbounded one recreates the nodejs failure mode INSIDE the reduction. A
// move spanning several series carries so much churn that it subsumes real
// divergences and classifies deliberate work as version noise — the exact error
// this exists to prevent, arriving through the subtraction instead of through
// the model, where nobody is looking for it. So a third point whose span is
// wider than the distance from the baseline to us subtracts NOTHING and says it
// reduced nothing; subtracting less and reporting Reduced=true would be the same
// failure with better manners.
//
// # Matching is by text, and it errs one way
//
// A hunk of ours matches a hunk of the move when their texts are equal: the same
// lines removed and the same lines added, in the same order, with no line numbers
// involved. Where the two diffs group changed lines differently — one edit of
// ours spanning what the move split in two — nothing matches and the hunk stays
// ours. That is the conservative direction and it is chosen on purpose: an
// under-subtraction leaves a version-move hunk to be judged as ours, which costs
// a question; an over-subtraction deletes a decision from the report, which is
// what the story exists to prevent.
//
// # No error return
//
// There is nothing here a caller could do differently. An ebuild that will not
// read has no differences to classify, and the zero Classified with no hunks
// says exactly that while rendering nothing — the same answer as two identical
// ebuilds, which is also the truthful one: this pass ran and found nothing to
// attribute. Whether the baseline ebuild could be read at all is already reported
// upstream, by Baseline.Unexamined, and repeating it here as an error the
// annotation pass would have to swallow would put the same fact in two places.
//
// _Requirements: R2, R2.1, R2.2, R2.3, R2.4, R2.5_
func ReduceDiff(our, baseline string, move *VersionMove) (Classified, []Hunk) {
	ourText, err := reduceReadEbuild(our)
	if err != nil {
		return Classified{}, nil
	}
	baselineText, err := reduceReadEbuild(baseline)
	if err != nil {
		return Classified{}, nil
	}

	// R2.1: the content comparison happens before any version is read, so no
	// version relationship can decide whether the ebuilds get compared at all.
	hunks := reduceHunks(baselineText, ourText)
	if len(hunks) == 0 {
		// Two identical ebuilds. Nothing to classify, and a zero Classified so
		// the report stays silent about a package that has nothing to say.
		return Classified{}, nil
	}

	ourVersion, baselineVersion := reduceVersionOf(our), reduceVersionOf(baseline)

	// The same version, by string equality: ebuild.CompareVersions pads missing
	// components with zeros and reads 1.0 and 1.0.0 as equal, and those are two
	// different ebuilds. versionDistance is 0 on exactly the same pairs, and is
	// used below for everything that is a magnitude rather than an identity.
	if ourVersion != "" && ourVersion == baselineVersion {
		// No third point is consulted even if one was offered: with both sides at
		// one version there is no version noise for it to explain, so Span stays
		// 0 — nothing covered a distance here — while the classification is
		// complete and Reduced says so.
		return reduceTally(reduceMarkAll(hunks, hunkOurs), true, 0)
	}

	span, usable := reduceSpan(move, ourVersion, baselineVersion)
	if !usable {
		return reduceTally(reduceMarkAll(hunks, hunkUnclassified), false, span)
	}

	moved, err := reduceMovedText(move)
	if err != nil {
		// A third point that will not read is a third point that subtracts
		// nothing. The span is still reported: it is a property of the pair the
		// caller offered, not of what could be done with it.
		return reduceTally(reduceMarkAll(hunks, hunkUnclassified), false, span)
	}

	for i := range hunks {
		if _, ok := moved[hunks[i].Text]; ok {
			hunks[i].Class = hunkVersionMove
			continue
		}
		hunks[i].Class = hunkOurs
	}
	return reduceTally(hunks, true, span)
}

// reduceSpan measures the third point and says whether it may be subtracted.
//
// The span is measured in versionDistance's release steps, the same unit
// Baseline.Distance is in, because the bound below compares the two and two
// numbers in different units are not comparable at all. There is deliberately no
// second metric in this file.
//
// It refuses, in each case returning the span it did manage to measure so the
// refusal is legible rather than a silent shrug:
//
//   - No third point at all — D3's third preference.
//   - A version it could not read, on any of the four ebuilds. An unreadable
//     version is a bound nobody could check, and an unchecked bound is precisely
//     the failure this function exists to prevent.
//   - A pair that is not a move: From and To at the same version have no version
//     move between them, so whatever their diff contains, it is not one.
//   - A span WIDER than the distance from the baseline to us. This is the bound.
func reduceSpan(move *VersionMove, ourVersion, baselineVersion string) (int, bool) {
	if move == nil {
		return 0, false
	}
	fromVersion, toVersion := reduceVersionOf(move.From), reduceVersionOf(move.To)
	if fromVersion == "" || toVersion == "" || ourVersion == "" || baselineVersion == "" {
		return 0, false
	}

	span := versionDistance(fromVersion, toVersion)
	if span == 0 {
		return 0, false
	}
	if span > versionDistance(ourVersion, baselineVersion) {
		return span, false
	}
	return span, true
}

// reduceMovedText is the set of hunk texts the version move contains, which is
// what a hunk of ours is matched against.
//
// A SET, not a one-to-one consumption: a move hunk that our diff repeats twice
// explains both occurrences, because the same change appearing twice is the same
// version noise twice, and making the second occurrence depend on whether the
// first used the match up would make the answer depend on the order the file
// happens to be written in.
func reduceMovedText(move *VersionMove) (map[string]struct{}, error) {
	fromText, err := reduceReadEbuild(move.From)
	if err != nil {
		return nil, fmt.Errorf("reading the version move's earlier ebuild: %w", err)
	}
	toText, err := reduceReadEbuild(move.To)
	if err != nil {
		return nil, fmt.Errorf("reading the version move's later ebuild: %w", err)
	}

	hunks := reduceHunks(fromText, toText)
	moved := make(map[string]struct{}, len(hunks))
	for _, hunk := range hunks {
		moved[hunk.Text] = struct{}{}
	}
	return moved, nil
}

// reduceHunks is the ONE differ in this file, used for the diff being classified
// and for the version move it is compared against alike.
//
// That reuse is what makes "a hunk of ours matches a hunk of the move" a
// comparison between two things produced the same way, rather than between two
// shapes that merely look alike. Two differs, however similar, would group
// changed lines differently often enough that the match rate would be a property
// of the differs rather than of the ebuilds.
//
// The orientation is the package's existing one — the baseline is `before` and
// ours is `after`, as in diffLineCounts — so a "+" line is ours throughout the
// report.
func reduceHunks(baselineText, ourText string) []Hunk {
	edits := udiff.Lines(baselineText, ourText)

	hunks := make([]Hunk, 0, len(edits))
	for _, edit := range edits {
		if edit.Start < 0 || edit.Start > edit.End || edit.End > len(baselineText) {
			// Unreachable with the differ's own output, and cheaper than the
			// alternative: an out-of-range offset would slice out of bounds, and
			// this package may not panic.
			continue
		}
		removed := baselineText[edit.Start:edit.End]
		if removed == "" && edit.New == "" {
			// An edit that neither removes nor adds anything is not a difference.
			continue
		}
		hunks = append(hunks, Hunk{
			Start: reduceLineAt(baselineText, edit.Start),
			End:   reduceLineAt(baselineText, edit.End),
			Text:  reduceHunkText(removed, edit.New),
		})
	}
	return hunks
}

// reduceHunkText renders one hunk as the two sides of a diff, baseline first.
func reduceHunkText(removed, added string) string {
	var text strings.Builder
	reduceWriteSide(&text, removed, hunkBaselineMark)
	reduceWriteSide(&text, added, hunkOursMark)
	return text.String()
}

// reduceWriteSide writes one side of a hunk, one marked line at a time.
//
// A final line with no newline of its own is still a line and is written with
// one, so two hunks that differ only in whether the file they came from ended in
// a newline read as the same difference — which they are.
func reduceWriteSide(text *strings.Builder, side, mark string) {
	for _, line := range strings.SplitAfter(side, "\n") {
		if line == "" {
			// The empty tail SplitAfter leaves after a final newline. It is not a
			// line, and an empty line in the middle is "\n" rather than "".
			continue
		}
		text.WriteString(mark)
		text.WriteString(strings.TrimSuffix(line, "\n"))
		text.WriteString("\n")
	}
}

// reduceLineAt is the 1-based line the byte offset falls on. An offset at the
// very end of the text is the line after the last one, which is what makes the
// half-open range of a hunk that runs to the end of the file come out right.
func reduceLineAt(text string, offset int) int {
	if offset < 0 {
		return 1
	}
	if offset > len(text) {
		offset = len(text)
	}
	return 1 + strings.Count(text[:offset], "\n")
}

// reduceMarkAll gives every hunk the same class, for the three answers that are
// about the whole diff rather than about one difference in it.
func reduceMarkAll(hunks []Hunk, class string) []Hunk {
	for i := range hunks {
		hunks[i].Class = class
	}
	return hunks
}

// reduceTally counts the classified hunks, and is the ONLY way this file builds
// a Classified.
//
// The counts are READ back off the hunks rather than accumulated beside them, so
// the summary cannot drift from what it describes: every hunk is counted exactly
// once and the three counts sum to the number of differences on every path
// through ReduceDiff, by construction rather than by four separate authors
// remembering to keep them level (R2.4, R2.5).
//
// A class this file never writes would still be counted — as unclassified, and
// normalised to it — because a difference that fell out of the arithmetic
// entirely is the one failure the arithmetic exists to make visible.
func reduceTally(hunks []Hunk, reduced bool, span int) (Classified, []Hunk) {
	summary := Classified{Reduced: reduced, Span: span}
	for i := range hunks {
		switch hunks[i].Class {
		case hunkVersionMove:
			summary.VersionMove++
		case hunkOurs:
			summary.Ours++
		default:
			hunks[i].Class = hunkUnclassified
			summary.Unclassified++
		}
	}
	return summary, hunks
}

// reduceReadEbuild reads one ebuild's text.
//
// It is a reader of ebuild TEXT and never a shell: nothing here is expanded,
// evaluated or executed, so an ebuild that would delete the tree when sourced is
// just a file with some lines in it.
func reduceReadEbuild(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the ebuilds to compare are the caller's whole request; each path is a baseline or overlay ebuild resolved from a directory listing, never from registry input
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return string(data), nil
}

// reduceVersionOf reads the version out of an ebuild path, or "" when the path
// does not name one.
//
// The version comes from the repository's own ebuild path parser, which wants
// the three-element category/package/file shape, so an absolute path is trimmed
// to its last three elements first. Nothing here is a second notion of what an
// ebuild filename is.
//
// A path that parser cannot read a version out of is reported as no version at
// all rather than as an error, because every caller here treats an unreadable
// version the same way: as a measurement it may not make, and therefore as a
// bound it may not skip.
func reduceVersionOf(path string) string {
	elements := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if len(elements) < 3 {
		return ""
	}

	parsed, err := ebuild.ParsePath(strings.Join(elements[len(elements)-3:], "/"))
	if err != nil {
		return ""
	}
	return parsed.Version
}
