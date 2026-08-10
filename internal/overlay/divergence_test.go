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
