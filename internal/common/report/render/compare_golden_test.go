package render

// Story 047, sub-task 5.1 — S047-R8.3: one golden per render mode, so a mode
// that silently stops rendering the comparison report is caught by a diff
// instead of by a maintainer noticing months later.
//
// # Why this file exists
//
// The compare report had no golden at all. Its expectations lived inline in
// eighteen files, each pinning one sentence, so nothing pinned the SHAPE: the
// six sections in order, the tables, the notes, the detail lines under a row.
// A section that stopped being emitted would have left every inline assertion
// green.
//
// Because there was no golden, there was also no baseline to regenerate FROM.
// These files were authored from the fixture and reviewed by reading, against
// .draft/target-output.txt — the rendering agreed with the maintainer over the
// real 279-package overlay. Regenerate with:
//
//	go test ./internal/common/report/render/ -run 'TestCompareGolden|TestCompareRowsGolden' -update
//
// and READ the diff. A golden accepted unread is a screenshot of a bug.
//
// # What the populated fixture exercises, and why each piece is there
//
//   - A GROUP: three packages sharing the version pair 1.29.2/1.26.11 with no
//     finding, which GroupKeep collapses into one row.
//   - The SINGLE-MEMBER case: app-admin/ansible holds its version pair alone,
//     so it renders as an ordinary row and never as a group of one
//     (S047-R2.5).
//   - A FINDING THAT REFUSES TO COLLAPSE: media-plugins/gst-plugins-ugly
//     carries the same version pair as the group above but has a Reason, so it
//     stays its own row and keeps its detail line (S047-R2.2).
//   - ALL FOUR READING STATES, all four inside the redundant list, which is the
//     only section whose prose is derived from them: failed, not comparable,
//     read, not requested.
//
// # Three things the goldens deliberately do NOT contain
//
//  1. The group labels are DERIVED STEMS, not names. target-output.txt shows
//     "gstreamer stack" and "rust toolchain"; nothing in this repository can
//     derive those, and a name-to-label map would put Gentoo domain knowledge
//     inside a package whose guards exist to keep domain out. compareGroupLabel
//     takes the longest shared stem of the members' names, cut to a token
//     boundary, so the goldens expect "gst (3 packages)" and "rust (2
//     packages)". The members were chosen to make that stem predictable: the
//     three names share "gst-" and diverge after it, which cuts to "gst".
//
//  2. No row says "unreadable". It is one of the four words the Diff cell
//     documents (S047-R3.2), and it has no producer: the content check collapses
//     its four failure causes into one zero value, so no consumer can recover
//     which applied. A golden containing it would pin a string the product
//     cannot emit.
//
//  3. No count is invented. Scanned 20 = keep 7 + redundant 4 + needs rebase 2
//     + unknown 7, and unknown 7 is the two listed plus the five that exist
//     only here. Unread 12 is the twelve listed rows whose Reading is not
//     "read". Every number in the goldens can be re-derived from the fixture by
//     hand, which is what makes reading them a review rather than a glance.
//
// # The two redundant-advice arms
//
// The removal advice is derived from the readings, and it has three arms. Two
// are pinned here, by two fixtures:
//
//   - PARTLY READ (comparePopulatedRun): one of four carries a reading, so the
//     recommendation covers that one "and none of the rest".
//   - NONE READ (compareUnreadRun): no reading at all and two distinct causes,
//     so "No removal advice follows from either" — the sentence
//     target-output.txt ends its redundant lead with.
//
// The third arm, all read, is left to the inline tests; pinning it would need a
// third fixture whose only difference is a field this file already proves is
// load-bearing.

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// compareSeededRun is the payload an ADAPTER hands a renderer: every list
// non-nil, whether or not it has members.
//
// `func buildCompareReport` in cmd/bentoo/overlay_compare_report.go seeds each
// of them before it fills it, because render/json.go normalises nothing — a nil
// reaches the wire as null and says "the producer established nothing" where
// the run established an empty list (S046-R4.2). The fixtures below go through
// this rather than repeat an empty literal on eighteen entries, which would
// bury the two entries that DO carry a further finding — and those are what
// these goldens were regenerated to show.
func compareSeededRun(r report.CompareRun) report.CompareRun {
	seed := func(pkgs []report.ComparePkg) []report.ComparePkg {
		for i := range pkgs {
			if pkgs[i].FurtherFindings == nil {
				pkgs[i].FurtherFindings = []string{}
			}
		}
		return pkgs
	}
	r.Redundant, r.NeedsRebase = seed(r.Redundant), seed(r.NeedsRebase)
	r.Keep, r.Unknown = seed(r.Keep), seed(r.Unknown)
	if r.Notes == nil {
		r.Notes = []string{}
	}
	return r
}

// comparePopulatedRun is a finished comparison over 20 packages, 15 of which
// exist in both repositories and are listed.
//
// KeepGroups is built by calling report.GroupKeep rather than being written out
// by hand, so the goldens pin the GROUPING DECISION — which packages collapse
// and which refuse to — and not merely a table this fixture typed in.
func comparePopulatedRun() report.Run {
	keep := []report.ComparePkg{
		{Package: "media-plugins/gst-plugins-base", Local: "1.29.2", Remote: "1.26.11", Status: "newer", Reading: "not requested", Diff: "not compared"},
		{Package: "media-plugins/gst-plugins-good", Local: "1.29.2", Remote: "1.26.11", Status: "newer", Reading: "not requested", Diff: "not compared"},
		{Package: "media-libs/gst-libav", Local: "1.29.2", Remote: "1.26.11", Status: "newer", Reading: "not requested", Diff: "not compared"},
		{
			Package: "media-plugins/gst-plugins-ugly", Local: "1.29.2", Remote: "1.26.11", Status: "newer", Reading: "read",
			// A read package whose content check found a difference: the producer
			// emits "+N/-M" here, never the empty string. compareDiffCell has a
			// default arm and returns one of three words on every path, so a golden
			// showing "" would pin a value no run can produce (S047-R3.2).
			Diff:   "+12/-3",
			Reason: "patched: applies files/gst-ugly-x264.patch, which ::gentoo does not ship",
			// The row keeps the FIRST finding and this is the second: the shape
			// that produced issue #33, where a package had more established
			// about it than a one-line cell could hold.
			FurtherFindings: []string{"inherit differs from ::gentoo — ours adds gstreamer-meson"},
		},
		{Package: "dev-lang/rust", Local: "1.98.0", Remote: "1.97.1", Status: "newer", Reading: "not requested", Diff: "not compared"},
		{Package: "dev-lang/rust-bin", Local: "1.98.0", Remote: "1.97.1", Status: "newer", Reading: "not requested", Diff: "not compared"},
		{Package: "app-admin/ansible", Local: "14.3.1", Remote: "14.1.0", Status: "newer", Reading: "not requested", Diff: "not compared"},
	}

	return report.Run{
		Schema:   report.SchemaVersion,
		Kind:     report.KindOverlayCompare,
		Title:    "overlay comparison",
		Complete: true,
		Payload: compareSeededRun(report.CompareRun{
			Repository: "gentoo",
			// What the run has to say about ITSELF, under the tally that closes
			// the report. These reached the terminal and no export at all until
			// they were carried in the payload (S047-R1.3, S047-R6.3).
			Notes: []string{
				"3 of the 15 packages compared were found to have no ::gentoo counterpart — those are the overlay's own work rather than a divergence from anyone's, and no realignment is proposed for them.",
				"Classification: 9 of the 12 differences examined across 7 packages are version moves, 2 are ours, 1 could not be classified.",
				"Some packages are recommended for removal. Run `bentoo overlay prune` to act on them.",
			},
			Scanned:   20,
			InBoth:    15,
			OnlyLocal: 5,
			Unread:    12,
			// The two failed readings below, by cause (S057-R4.5). The
			// compareUnreadRun fixture keeps a failed row with NO cause, so the
			// bare marker stays pinned too.
			ReadingFailures: []report.CauseCount{{Cause: "timed out", Count: 1}, {Cause: "could not start", Count: 1}},
			Redundant: []report.ComparePkg{
				{
					Package: "dev-lang/go", Local: "1.27.0", Remote: "1.27.0", Status: "up-to-date",
					Reading: "failed", Diff: "+24/-0",
					Reason: "differs, and no entry declares why",
					Cause:  "timed out",
					Error:  "the divergence review failed: LLM API request failed: claude CLI ran out of time: its 2m0s budget elapsed before it answered",
					// A second block with a further finding, so the goldens
					// show that each lands under the table holding its own row
					// rather than all of them under one.
					FurtherFindings: []string{"KEYWORDS differs from ::gentoo — ours drops ~arm64"},
				},
				{
					Package: "kde-plasma/breeze-gtk", Local: "6.7.4", Remote: "6.7.4-r1", Status: "outdated",
					Reading: "not comparable", Diff: "not compared",
				},
				{
					Package: "net-libs/nodejs", Local: "26.8.1", Remote: "26.8.1", Status: "up-to-date",
					Reading: "read", Diff: "identical",
					Reason: "identical to the ::gentoo copy, line for line",
				},
				{
					Package: "sys-apps/fwupd", Local: "2.1.7", Remote: "2.1.7", Status: "up-to-date",
					Reading: "not requested", Diff: "+8/-29",
				},
			},
			NeedsRebase: []report.ComparePkg{
				{
					Package: "kde-plasma/kwin", Local: "6.7.4", Remote: "6.7.4-r2", Status: "outdated",
					Reading: "read", Diff: "+3/-1",
					Reason: "ours: adds files/kwin-wayland-crash.patch on top of a version ::gentoo has moved past",
				},
				{
					Package: "x11-base/xwayland", Local: "24.1.6", Remote: "24.1.8", Status: "outdated",
					Reading: "failed", Diff: "+11/-4",
					Cause: "could not start", Error: "the divergence review failed: LLM API request failed: claude CLI could not start: exec: \"claude\": executable file not found in $PATH",
				},
			},
			Keep:       keep,
			KeepGroups: report.GroupKeep(keep),
			Unknown: []report.ComparePkg{
				{Package: "sys-kernel/gentoo-kernel", Local: "7.2.2", Remote: "7.1.12", Status: "newer", Reading: "not requested", Diff: "not compared"},
				{Package: "virtual/dist-kernel", Local: "7.2.2", Remote: "7.1.12", Status: "newer", Reading: "not requested", Diff: "not compared"},
			},
			Verdicts: report.VerdictTally{Keep: 7, Redundant: 4, NeedsRebase: 2, Unknown: 7},
		}),
	}
}

// compareUnreadRun is the run in which nothing was read: two packages the
// content check refused to diff and one whose review died.
//
// It pins two things the populated fixture cannot. The first is the none-read
// arm of the removal advice. The second is every EMPTY section's wording —
// keep, needs rebase, and the unknown section's count-without-a-list branch,
// where a verdict was tallied but no row reached the report. Those branches are
// prose with no table to make them conspicuous, which is exactly the kind of
// output that can stop being emitted unnoticed (S047-R8.3).
func compareUnreadRun() report.Run {
	return report.Run{
		Schema:   report.SchemaVersion,
		Kind:     report.KindOverlayCompare,
		Title:    "overlay comparison",
		Complete: true,
		Payload: compareSeededRun(report.CompareRun{
			Repository: "gentoo",
			Scanned:    4,
			InBoth:     3,
			OnlyLocal:  1,
			Unread:     3,
			Redundant: []report.ComparePkg{
				{
					Package: "kde-plasma/discover", Local: "6.7.4", Remote: "6.7.4-r1", Status: "outdated",
					Reading: "not comparable", Diff: "not compared",
				},
				{
					Package: "kde-plasma/drkonqi", Local: "6.7.4", Remote: "6.7.4-r1", Status: "outdated",
					Reading: "not comparable", Diff: "not compared",
				},
				{
					Package: "sys-apps/fwupd", Local: "2.1.7", Remote: "2.1.7", Status: "up-to-date",
					Reading: "failed", Diff: "+8/-29",
					Reason: "differs, and no entry declares why",
				},
			},
			Verdicts: report.VerdictTally{Redundant: 3, Unknown: 1},
		}),
	}
}

// TestCompareGoldenPlain pins the default terminal rendering: six sections, the
// two keep groups collapsed, and the packages that refused to collapse listed
// beneath them.
func TestCompareGoldenPlain(t *testing.T) {
	var buf bytes.Buffer
	if err := Plain(&buf, comparePopulatedRun().Sections(report.SectionOptions{}), Options{Width: 100}); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}
	golden(t, "TestCompareGoldenPlain", buf.Bytes())
}

// TestCompareGoldenMarkdown pins the export rendering of the same run.
//
// It is rendered with ShowAll, as the other markdown goldens are: an export
// that quietly held back rows would be a worse defect than a terminal that did,
// because nothing downstream can ask for the rest.
func TestCompareGoldenMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, comparePopulatedRun().Sections(report.SectionOptions{ShowAll: true})); err != nil {
		t.Fatalf("Markdown returned an error: %v", err)
	}
	golden(t, "TestCompareGoldenMarkdown", buf.Bytes())
}

// TestCompareGoldenMarkdownGrouped pins the ONE thing the markdown golden above
// cannot: a group row, rendered as markdown.
//
// # Why a fifth golden earns its review cost
//
// Every other markdown golden in this package renders with ShowAll, and this
// payload's ShowAll listing has no group row in it at all — that is the whole
// point of the flag. So the two goldens above leave the compression this story
// exists to produce (S047-R2.1) pinned in plain text and in no other mode,
// while markdown is the shape the EXPORT carries. A group row that stopped
// rendering, or started rendering its member count wrong, would reach an
// exported file with nothing to catch it.
//
// S047-R8.3 asks for one golden per render mode so a mode that silently stops
// rendering is caught. This is the narrower companion claim: a mode that keeps
// rendering while dropping one KIND of row is caught too.
func TestCompareGoldenMarkdownGrouped(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, comparePopulatedRun().Sections(report.SectionOptions{})); err != nil {
		t.Fatalf("Markdown returned an error: %v", err)
	}
	golden(t, "TestCompareGoldenMarkdownGrouped", buf.Bytes())
}

// TestCompareRowsGoldenPlainShowAll pins the same run with --all: every group
// member listed as its own row, no group row at all, and the note that says how
// many members would have collapsed.
//
// Diffing this golden against TestCompareGoldenPlain is the review S047-R8.3
// asks for: the counts in the leads and the summary must be identical in both,
// because only the LISTING changed. The day a number moves because a listing
// did, the two files stop agreeing.
func TestCompareRowsGoldenPlainShowAll(t *testing.T) {
	var buf bytes.Buffer
	if err := Plain(&buf, comparePopulatedRun().Sections(report.SectionOptions{ShowAll: true}), Options{Width: 100}); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}
	golden(t, "TestCompareRowsGoldenPlainShowAll", buf.Bytes())
}

// TestCompareGoldenPlainNoneRead pins the run nobody read: the none-read arm of
// the removal advice, and the wording of every section that has no rows.
func TestCompareGoldenPlainNoneRead(t *testing.T) {
	var buf bytes.Buffer
	if err := Plain(&buf, compareUnreadRun().Sections(report.SectionOptions{}), Options{Width: 100}); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}
	golden(t, "TestCompareGoldenPlainNoneRead", buf.Bytes())
}

// TestCompareGoldenFullscreen is the mode S047-R8.3 asks for that this file did
// not have.
//
// Plain and markdown were pinned above; fullscreen was not, and it is the mode
// most exposed to a layout change — it is the only one that measures a viewport,
// paginates, and draws a frame. TestFullscreenSectionsGoldenFrame in
// fullscreen_sections_test.go pins the frame over a GENERIC fixture, and
// TestFullscreenAgreesWithPlainOnContent proves the two modes agree over that
// same fixture. Neither has ever seen a comparison payload, so a section shape
// only this report produces — a group row, a detail line under a row, a
// four-state redundant lead — could stop rendering here with every existing
// golden still green. That is exactly the silence S047-R8.3 names.
//
// # The viewport is deliberately taller than the report
//
// Pagination is a property of the SCREEN and TestFullscreenStatesWhatItCouldNotShow
// already covers it. A height that truncated would pin whichever rows happened
// to fit, so the next person to add a section would regenerate a golden that
// silently dropped an older one off the bottom.
//
// The view is stripped of ANSI before it is pinned, for the reason the frame
// golden strips it: a colour change is not a content change, and a golden that
// failed on one would be regenerated unread.
//
// Regenerate with:
//
//	go test ./internal/common/report/render/ -run TestCompareGoldenFullscreen -update
//
// and READ the diff.
func TestCompareGoldenFullscreen(t *testing.T) {
	model := newModel(comparePopulatedRun().Sections(report.SectionOptions{}), Options{Width: 100})
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 200})

	golden(t, "TestCompareGoldenFullscreen", []byte(ansi.Strip(sized.View())))
}
