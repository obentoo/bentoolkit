// MERGE FRAGMENT — story 034, sub-task 3.1 (`# BENTOO-DIVERGENCE:`).
//
// Target file: internal/overlay/divergence_test.go  (APPEND, package overlay)
// Append AFTER sub-task 3.2's fragment: this one reuses `writeVerifyEbuild` and
// adds only `parseDiv`-prefixed helpers, so the two merge without a collision.
// If 3.1 lands first it CREATES the file and 3.2 appends instead; either order
// works, but the import block must be merged rather than duplicated.
//
// PINNED CONTRACT (design.md "Components & Interfaces"):
//
//	type DeclaredDivergence struct{ Axis, Reason, DropWhen string; Expired bool }
//	func ParseDivergences(ebuildPath string) ([]DeclaredDivergence, error)
//
// # THE NAME IS PART OF THE CONTRACT
//
// `overlay.Divergence` already exists at compare.go:143 as the REGISTRY axis
// (Patched, Reason, Entry). It feeds CompareOptions.Divergence, it is built by
// buildDivergenceMap in cmd/bentoo, and this story leaves it untouched. Reusing
// the name is a redeclaration in the same package and does not compile — but the
// pressure to "unify the two divergence types" will outlive this story, and a
// future merge would silently make the ebuild tag and the registry entry answer
// the same question. TestParseDivergencesIsNotTheRegistryType below is a
// compile-time guard against exactly that.
//
// The axis word is compared case-insensitively. §7 of the protocol writes it
// upper-case (`INHERIT:`) and D4's axes are lower-case (`inherit`); the story
// never says which side normalises, so pinning one here would invent a decision.
package overlay

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	// The declaration shape §7 proposes and D5 adopts: one axis, one reason,
	// one optional continuation line carrying the exit condition.
	parseDivWithDropWhen = `EAPI=8
inherit meson

# BENTOO-DIVERGENCE: INHERIT: gstreamer-meson does not handle the qt6 option list
#   drop-when: gentoo-version >= 1.29

DESCRIPTION="Qt6 plugin for GStreamer"
`
	// A divergence with a reason and no end in sight. It is not malformed — most
	// real ones will look like this, and binutils is the exception rather than
	// the rule.
	parseDivNoDropWhen = `EAPI=8

# BENTOO-DIVERGENCE: OPTIONS: the option list is maintained by hand until upstream splits the eclass

DESCRIPTION="Qt6 plugin for GStreamer"
`
	parseDivNoTag = `EAPI=8
inherit meson

# An ordinary comment. It mentions a divergence, and it declares nothing.
DESCRIPTION="Qt6 plugin for GStreamer"
`
	// The tag appears twice and neither occurrence is a declaration: one is
	// inside a double-quoted string the shell will echo, the other inside a
	// heredoc. An ebuild is a bash script, and a line-oriented matcher that
	// ignores that will read a package's own documentation as policy.
	parseDivInStringAndHeredoc = `EAPI=8

src_install() {
	echo "# BENTOO-DIVERGENCE: INHERIT: this line is output, not a declaration"
	cat <<-EOF > "${D}/usr/share/doc/notes"
		# BENTOO-DIVERGENCE: OPTIONS: nor is this one
	EOF
}
`
	// A well-formed declaration sharing a file with a malformed one. The
	// precedent is buildDivergenceMap's, whose own comment says one bad key must
	// not blank the whole map: the report must learn about the broken tag AND
	// keep the good one.
	parseDivMalformedBesideValid = `EAPI=8

# BENTOO-DIVERGENCE:
# BENTOO-DIVERGENCE: INHERIT: gstreamer-meson does not handle the qt6 option list

DESCRIPTION="Qt6 plugin for GStreamer"
`
)

// parseDivEbuild writes one ebuild body and returns its path.
func parseDivEbuild(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	writeVerifyEbuild(t, root, "media-libs", "gst-plugins-qt6", "1.29.2", body)
	return root + "/media-libs/gst-plugins-qt6/gst-plugins-qt6-1.29.2.ebuild"
}

// TestParseDivergences covers the four shapes an ebuild can present.
//
// _Requirements: R3, R3.1, R3.4_
func TestParseDivergences(t *testing.T) {
	t.Run("a tag with drop-when parses into all three fields", func(t *testing.T) {
		got, err := ParseDivergences(parseDivEbuild(t, parseDivWithDropWhen))
		if err != nil {
			t.Fatalf("ParseDivergences returned %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("parsed %d declarations, want 1: %+v", len(got), got)
		}
		if !strings.EqualFold(got[0].Axis, "inherit") {
			t.Errorf("Axis is %q, want inherit (either case)", got[0].Axis)
		}
		if !strings.Contains(got[0].Reason, "gstreamer-meson") {
			t.Errorf("Reason is %q, want the maintainer's words verbatim", got[0].Reason)
		}
		if got[0].DropWhen != "gentoo-version >= 1.29" {
			t.Errorf("DropWhen is %q, want %q — the continuation line is where the exit condition lives (D5)", got[0].DropWhen, "gentoo-version >= 1.29")
		}
		if got[0].Expired {
			t.Error("Expired is true straight out of the parser; expiry is decided against the ::gentoo tree by task 3.2, not by reading the ebuild (R3.3)")
		}
	})

	t.Run("a tag without drop-when parses and declares no end", func(t *testing.T) {
		got, err := ParseDivergences(parseDivEbuild(t, parseDivNoDropWhen))
		if err != nil {
			t.Fatalf("ParseDivergences returned %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("parsed %d declarations, want 1: %+v", len(got), got)
		}
		if got[0].DropWhen != "" {
			t.Errorf("DropWhen is %q, want empty — this divergence states no condition, and inventing one would retire it", got[0].DropWhen)
		}
		if got[0].Reason == "" {
			t.Error("Reason is empty; a declaration without a reason is the thing R3 exists to abolish")
		}
	})

	t.Run("an ebuild with no tag yields none", func(t *testing.T) {
		got, err := ParseDivergences(parseDivEbuild(t, parseDivNoTag))
		if err != nil {
			t.Fatalf("ParseDivergences returned %v, want nil", err)
		}
		if len(got) != 0 {
			t.Errorf("parsed %d declarations from an ebuild that declares nothing: %+v", len(got), got)
		}
	})

	t.Run("a tag inside a string or a heredoc is not a declaration", func(t *testing.T) {
		got, err := ParseDivergences(parseDivEbuild(t, parseDivInStringAndHeredoc))
		if err != nil {
			t.Fatalf("ParseDivergences returned %v, want nil", err)
		}
		if len(got) != 0 {
			t.Errorf("parsed %d declarations out of quoted text: %+v\nan ebuild is a bash script; a line-oriented matcher reads the package's own output as policy, and every such phantom declaration silences a real divergence (R3.1)", len(got), got)
		}
	})
}

// TestParseDivergencesReportsAMalformedTag is the difference between a parser
// and a filter. A tag someone meant as a declaration and mistyped must not
// evaporate: silently dropped, it reads as "this divergence was never declared",
// and the maintainer is asked to declare it again next run — forever.
//
// _Requirements: R3, R3.1, R3.4_
func TestParseDivergencesReportsAMalformedTag(t *testing.T) {
	got, err := ParseDivergences(parseDivEbuild(t, parseDivMalformedBesideValid))

	if err == nil {
		t.Fatal("ParseDivergences returned nil error for `# BENTOO-DIVERGENCE:` with no axis and no reason; a malformed tag must be reported, not silently ignored")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "bentoo-divergence") {
		t.Errorf("the error is %q, want it to name the tag it could not read", err.Error())
	}
	// And the good declaration beside it survives. buildDivergenceMap's own
	// comment sets the precedent: one bad key must not blank the whole map.
	if len(got) != 1 {
		t.Fatalf("parsed %d declarations beside the malformed one, want 1 — a broken tag must not blank the file: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Reason, "gstreamer-meson") {
		t.Errorf("the surviving declaration is %+v, want the well-formed INHERIT one", got[0])
	}
}

// TestParseDivergencesIsNotTheRegistryType keeps two questions apart at compile
// time.
//
// `overlay.Divergence` (compare.go:143) answers "does the REGISTRY say we
// changed something in this package?" — one answer per package, feeding the
// `redundant` verdict. `DeclaredDivergence` answers "what does THIS EBUILD say
// about this particular change, and when does it stop applying?" — one answer
// per hunk, travelling with the file when it is copied to a new version, which
// D5 says is the whole reason it does not live in the registry.
//
// The two composite literals below cannot both compile against one merged type:
// Divergence has no Axis and no DropWhen, DeclaredDivergence has no Patched and
// no Entry. That is the guard. If a later change merges them, this file stops
// building and says why, instead of the merge quietly redefining what a
// declaration means.
//
// _Requirements: R3, R3.1_
func TestParseDivergencesIsNotTheRegistryType(t *testing.T) {
	declared := DeclaredDivergence{
		Axis:     "inherit",
		Reason:   "gstreamer-meson does not handle the qt6 option list",
		DropWhen: "gentoo-version >= 1.29",
	}
	registry := Divergence{
		Patched: true,
		Reason:  "declared in .autoupdate/packages.toml",
		Entry:   "media-libs/gst-plugins-qt6@stable",
	}

	if declared.Reason == "" || registry.Reason == "" {
		t.Fatal("both types carry a Reason and both are set here; this guard is about the fields they do NOT share")
	}

	// The parser's element type is the ebuild tag's type, asserted by
	// assignment rather than by reading the signature.
	var fromParser []DeclaredDivergence
	fromParser, err := ParseDivergences(parseDivEbuild(t, parseDivWithDropWhen))
	if err != nil {
		t.Fatalf("ParseDivergences returned %v, want nil", err)
	}
	if len(fromParser) != 1 {
		t.Fatalf("parsed %d declarations, want 1", len(fromParser))
	}
}

// MERGE FRAGMENT — story 034, sub-task 3.2 (`drop-when`, and refusing to guess).
//
// Target file: internal/overlay/divergence_test.go  (NEW file, package overlay)
// Sub-task 3.1 authors the same file for `ParseDivergences`. Whichever lands
// first CREATES the file; the second APPENDS its functions and merges the import
// block. Nothing here redeclares a helper 3.1 would own — every symbol below is
// prefixed `dropWhen`.
//
// PINNED CONTRACT
//
//	type DeclaredDivergence struct{ Axis, Reason, DropWhen string; Expired bool }
//	func EvaluateDropWhen(cond, gentooTree, atom string) (met, checkable bool)
//	func EvaluateDeclarations(decls []DeclaredDivergence, gentooTree, atom string) []DeclaredDivergence
//
// TWO DELIBERATE DEVIATIONS FROM design.md, BOTH FLAGGED RATHER THAN SMOOTHED:
//
//  1. design.md writes `EvaluateDropWhen(cond string, gentooTree string)`. That
//     signature cannot answer its own vocabulary. `gentoo-version >= 2.47` is a
//     question about A PACKAGE — binutils — and a two-argument evaluator was
//     never told which one; the same holds for `gentoo-inherits <eclass>`, which
//     asks what the BASELINE ebuild for this package inherits. So the atom the
//     declaration belongs to is a third parameter. If the implementer instead
//     embeds the atom in the condition text, this fragment must be renamed at
//     materialisation and the deviation recorded — but it must not be resolved
//     by dropping the atom, because then two of the three predicates are
//     unanswerable.
//
//  2. `Expired` is a decision, so production must own it. design.md lists the
//     field but no function that sets it, and a test that computes
//     `Expired = met && checkable` itself would be testing its own arithmetic.
//     `EvaluateDeclarations` is that missing function. Rename it freely; do not
//     delete the case.
//
// # WHY THE PROSE CASE CARRIES THE WEIGHT
//
// D6: a condition outside the vocabulary is reported verbatim, never evaluated,
// and never reported as unmet. Read as unmet, the divergence lives forever; read
// as met, it is retired with no cause. Both negatives are therefore asserted
// explicitly, and — the assertion that actually bites — prose must be
// DISTINGUISHABLE from a genuinely unmet condition. Both return met=false, so
// `met` alone cannot tell them apart; only `checkable` can, and a caller that
// ignored it would have collapsed the two states without any test noticing.

// dropWhenTree builds a fixture ::gentoo tree. `binutilsVersions` decides
// whether the binutils exit condition is met; the gst package is always present
// and always inherits gstreamer-meson, which is what the `gentoo-inherits`
// predicate reads.
//
// It is a directory of ebuilds and nothing else: no git, no network, no
// /var/db/repos. The real tree is a shallow clone whose history says nothing
// (M-A), so a fixture that only carries content is the same evidence the
// production path gets.
func dropWhenTree(t *testing.T, binutilsVersions ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, v := range binutilsVersions {
		writeVerifyEbuild(t, root, "sys-devel", "binutils", v,
			"EAPI=8\ninherit toolchain-binutils\nDESCRIPTION=\"GNU binary utilities\"\n")
	}
	writeVerifyEbuild(t, root, "media-libs", "gst-plugins-qt6", "1.26.11",
		"EAPI=8\ninherit gstreamer-meson\nDESCRIPTION=\"Qt6 plugin for GStreamer\"\n")
	return root
}

const (
	dropWhenBinutils = "sys-devel/binutils"
	dropWhenGst      = "media-libs/gst-plugins-qt6"
)

// TestDropWhenVocabulary evaluates each predicate the closed vocabulary admits,
// in both directions, against a tree on disk.
//
// _Requirements: R3, R3.2_
func TestDropWhenVocabulary(t *testing.T) {
	// The two trees differ in exactly one thing: whether ::gentoo ships 2.47.
	// That is the binutils case the story names, and having both lets the same
	// condition string be asserted met and unmet without editing the condition.
	without := dropWhenTree(t, "2.46")
	with := dropWhenTree(t, "2.46", "2.47")

	cases := []struct {
		name          string
		cond          string
		tree          string
		atom          string
		wantMet       bool
		wantCheckable bool
	}{
		{
			name:          "binutils 2.47 is unmet against a tree without it",
			cond:          "gentoo-version >= 2.47",
			tree:          without,
			atom:          dropWhenBinutils,
			wantMet:       false,
			wantCheckable: true,
		},
		{
			name:          "binutils 2.47 is met the day ::gentoo ships one",
			cond:          "gentoo-version >= 2.47",
			tree:          with,
			atom:          dropWhenBinutils,
			wantMet:       true,
			wantCheckable: true,
		},
		{
			// The version already in the tree satisfies `>=`. Asserted because
			// an implementation comparing strings rather than versions gets
			// "2.46" >= "2.47" wrong in one direction and "2.5" >= "2.47" wrong
			// in the other.
			name:          "an equal version satisfies >=",
			cond:          "gentoo-version >= 2.46",
			tree:          without,
			atom:          dropWhenBinutils,
			wantMet:       true,
			wantCheckable: true,
		},
		{
			name:          "gentoo-has-package is met for a package the tree carries",
			cond:          "gentoo-has-package",
			tree:          without,
			atom:          dropWhenBinutils,
			wantMet:       true,
			wantCheckable: true,
		},
		{
			name:          "gentoo-has-package is unmet for a package it does not",
			cond:          "gentoo-has-package",
			tree:          without,
			atom:          "app-editors/zed",
			wantMet:       false,
			wantCheckable: true,
		},
		{
			name:          "gentoo-inherits is met when the baseline inherits the eclass",
			cond:          "gentoo-inherits gstreamer-meson",
			tree:          without,
			atom:          dropWhenGst,
			wantMet:       true,
			wantCheckable: true,
		},
		{
			name:          "gentoo-inherits is unmet when it does not",
			cond:          "gentoo-inherits toolchain-binutils",
			tree:          without,
			atom:          dropWhenGst,
			wantMet:       false,
			wantCheckable: true,
		},
		{
			// D6's first half: outside the vocabulary, so not evaluated.
			name:          "free prose is not evaluated",
			cond:          "when upstream finally sorts out the qt6 option list",
			tree:          without,
			atom:          dropWhenGst,
			wantMet:       false,
			wantCheckable: false,
		},
		{
			// A declaration with no drop-when at all reaches here as "". It is
			// not checkable and it is certainly not met — a divergence with no
			// stated end has not ended.
			name:          "an empty condition is not evaluated",
			cond:          "",
			tree:          without,
			atom:          dropWhenGst,
			wantMet:       false,
			wantCheckable: false,
		},
		{
			// A predicate NAME from the vocabulary with a malformed argument is
			// prose, not a silent false: guessing what "≥ two point forty-seven"
			// meant is the guess this task exists to refuse.
			name:          "a vocabulary keyword with an unparseable argument is not evaluated",
			cond:          "gentoo-version >= two-point-forty-seven",
			tree:          without,
			atom:          dropWhenBinutils,
			wantMet:       false,
			wantCheckable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			met, checkable := EvaluateDropWhen(tc.cond, tc.tree, tc.atom)
			if met != tc.wantMet || checkable != tc.wantCheckable {
				t.Errorf("EvaluateDropWhen(%q, tree, %q) = (met=%v, checkable=%v), want (met=%v, checkable=%v)",
					tc.cond, tc.atom, met, checkable, tc.wantMet, tc.wantCheckable)
			}
		})
	}
}

// TestDropWhenExpiry is R3.3: a met condition retires the divergence, and
// nothing else does.
//
// _Requirements: R3, R3.2, R3.3_
func TestDropWhenExpiry(t *testing.T) {
	decl := DeclaredDivergence{
		Axis:     "inherit",
		Reason:   "gstreamer-meson does not handle the qt6 option list",
		DropWhen: "gentoo-version >= 2.47",
	}

	t.Run("a met condition marks the divergence expired", func(t *testing.T) {
		got := EvaluateDeclarations([]DeclaredDivergence{decl}, dropWhenTree(t, "2.46", "2.47"), dropWhenBinutils)
		if len(got) != 1 {
			t.Fatalf("EvaluateDeclarations returned %d declarations, want 1", len(got))
		}
		if !got[0].Expired {
			t.Error("Expired is false with the condition met — an expired divergence rejoins the model's queue (R3.3); left unexpired it stays quiet forever")
		}
	})

	t.Run("an unmet condition leaves it declared", func(t *testing.T) {
		got := EvaluateDeclarations([]DeclaredDivergence{decl}, dropWhenTree(t, "2.46"), dropWhenBinutils)
		if len(got) != 1 {
			t.Fatalf("EvaluateDeclarations returned %d declarations, want 1", len(got))
		}
		if got[0].Expired {
			t.Error("Expired is true while ::gentoo ships no 2.47 — the divergence still earns its place")
		}
	})

	t.Run("evaluation does not rewrite the declaration", func(t *testing.T) {
		// R3.6 in miniature: the pass reads the tree and reports. The reason and
		// the condition are the maintainer's words and come back verbatim, so
		// what the report prints is what the ebuild says.
		got := EvaluateDeclarations([]DeclaredDivergence{decl}, dropWhenTree(t, "2.46"), dropWhenBinutils)
		if got[0].Reason != decl.Reason || got[0].DropWhen != decl.DropWhen || got[0].Axis != decl.Axis {
			t.Errorf("EvaluateDeclarations altered the declaration: got %+v, want the fields of %+v unchanged", got[0], decl)
		}
	})
}

// TestDropWhenProseIsNeitherMetNorUnmet is D6 stated as the property the quality
// gate names. Both negatives are asserted, and then the one that a `met`-only
// caller would silently lose: prose and a genuinely unmet condition must not
// arrive at the same answer.
//
// _Requirements: R3, R3.2, R3.3_
func TestDropWhenProseIsNeitherMetNorUnmet(t *testing.T) {
	tree := dropWhenTree(t, "2.46")
	const prose = "remove once the qt6 option list stops changing every release"

	proseMet, proseCheckable := EvaluateDropWhen(prose, tree, dropWhenGst)

	// Negative one: never met. Retiring a divergence nobody proved was over is
	// how deliberate work gets reverted.
	if proseMet {
		t.Error("prose reported as met — a condition the tool cannot check must never retire a divergence")
	}
	// Negative two: never REPORTED as unmet. `checkable=false` is what carries
	// that; without it the report has no way to say "a human must check this".
	if proseCheckable {
		t.Error("prose reported as checkable — the report would then print it as unmet and the divergence would live forever")
	}

	// The asymmetry. Both states have met=false, so only `checkable` separates
	// them. An evaluator that returned (false, true) for prose would pass both
	// assertions above if they were written as `!met` alone.
	unmetMet, unmetCheckable := EvaluateDropWhen("gentoo-version >= 2.47", tree, dropWhenBinutils)
	if unmetMet {
		t.Fatalf("the control case is met against a tree without 2.47; the asymmetry below would prove nothing")
	}
	if proseCheckable == unmetCheckable {
		t.Errorf("prose and an unmet condition are indistinguishable (both checkable=%v) — D6 requires the report to tell them apart", proseCheckable)
	}

	// And the declaration carrying prose is not retired by it.
	decl := DeclaredDivergence{Axis: "options", Reason: "hardcoded option list", DropWhen: prose}
	got := EvaluateDeclarations([]DeclaredDivergence{decl}, tree, dropWhenGst)
	if len(got) != 1 {
		t.Fatalf("EvaluateDeclarations returned %d declarations, want 1", len(got))
	}
	if got[0].Expired {
		t.Error("a declaration whose drop-when is prose was marked expired — retired with no cause (D6)")
	}
	if got[0].DropWhen != prose {
		t.Errorf("DropWhen is %q, want the prose reported verbatim (%q)", got[0].DropWhen, prose)
	}
}

// TestDropWhenReadsNoGitHistory guards the constraint every Validation line in
// this story rests on: /var/db/repos/gentoo is a SHALLOW clone, so an evaluator
// that reached for `git log` would answer correctly here and wrongly on the real
// tree. The fixture has no .git at all, so any git-based implementation fails
// this rather than degrading silently on the host.
//
// _Requirements: R3, R3.2_
func TestDropWhenReadsNoGitHistory(t *testing.T) {
	tree := dropWhenTree(t, "2.46", "2.47")
	if _, err := filepath.Abs(tree); err != nil {
		t.Fatalf("resolving the fixture tree: %v", err)
	}
	met, checkable := EvaluateDropWhen("gentoo-version >= 2.47", tree, dropWhenBinutils)
	if !checkable || !met {
		t.Errorf("EvaluateDropWhen = (met=%v, checkable=%v) on a tree with no .git, want (true, true) — the answer is in the ebuild filenames, not in a history the real tree does not have", met, checkable)
	}
}
