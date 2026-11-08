package cooldown_test

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 1.1 —
// R1.6, R7.1, R7.2, R7.4, Q11, Q16.
//
// Written from the contract, not from the implementation: design.md
// "Block format" (two lines per entry, BEGIN/END markers, format=1, days=N,
// bare `=cat/pkg-PVR` atoms) and the Load / ValidateAtom signatures.
//
// This file uses only the symbols sub-task 1.1 introduces (Entry, Block, Load,
// ValidateAtom), so it compiles on its own before Add, Sweep or Save exist.
//
// Hostile halves first, per requirement:
//   - `cat/pkg` vs `cat/pkg-bin` with the SAME version must stay two entries
//     with two different Package values (wrong collapse), and a name holding a
//     version-looking segment (`libfoo-2d`) must not lose it to the version
//     (wrong split);
//   - a `-r1` revision belongs to the PVR, never to the name;
//   - line numbers are FILE line numbers, so the fixture puts hand-written
//     masks above the block: a block-relative count names the wrong line;
//   - `format=10` must not be read as format 1 by a prefix match.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

// cooldownPrefixLines are hand-written masks above the block. Their exact
// bytes must survive every rewrite (R1.6), and their count is what makes a
// block-relative line number wrong.
var cooldownPrefixLines = []string{
	"# Hand-written masks live above the managed block.",
	"",
	"# Lucas (2026-10-01)",
	"# Broken against SDL 3.4.20",
	"=media-libs/sdl3-3.4.20",
	"",
}

// cooldownSuffixLines follow the block; they too are outside it.
var cooldownSuffixLines = []string{
	"",
	"# A trailing hand mask after the block.",
	">=dev-lang/foo-2",
}

const cooldownHeaderLine = "# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand."

// cooldownValidBlockLines is design.md's own example plus three hostile
// entries: an `-r1` PVR, a name ending in a version-looking segment, and a
// manual origin without a group.
var cooldownValidBlockLines = []string{
	"# BEGIN bentoo-cooldown format=1 days=7",
	cooldownHeaderLine,
	"# cooldown since=2026-11-07T07:10:00Z origin=autoupdate group=sys-apps/ai-jail,sys-apps/ai-jail-bin",
	"=sys-apps/ai-jail-3.2.0",
	"# cooldown since=2026-11-07T07:10:00Z origin=autoupdate group=sys-apps/ai-jail,sys-apps/ai-jail-bin",
	"=sys-apps/ai-jail-bin-3.2.0",
	"# cooldown since=2026-11-02T08:00:00Z origin=autoupdate",
	"=media-libs/libfoo-2d-1.0",
	"# cooldown since=2026-11-01T00:00:00Z origin=manual",
	"=net-misc/freerdp-3.33.0-r1",
	"# END bentoo-cooldown",
}

func joinLines(parts ...[]string) string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return strings.Join(all, "\n") + "\n"
}

// writeOverlayMask lays out <root>/profiles/package.mask with content and an
// empty <root>/.autoupdate directory, and returns root.
func writeOverlayMask(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"profiles", ".autoupdate"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "package.mask"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing package.mask: %v", err)
	}
	return root
}

func readMask(t *testing.T, root string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "profiles", "package.mask"))
	if err != nil {
		t.Fatalf("reading package.mask: %v", err)
	}
	return b
}

func findEntry(b *cooldown.Block, pkg, version string) (cooldown.Entry, bool) {
	for _, e := range b.Entries {
		if e.Package == pkg && e.Version == version {
			return e, true
		}
	}
	return cooldown.Entry{}, false
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return v
}

// TestCooldownParse_ReadsDesignExampleEntryByEntry: the block of design.md
// "Block format" parses into exactly its entries, field by field.
func TestCooldownParse_ReadsDesignExampleEntryByEntry(t *testing.T) {
	root := writeOverlayMask(t, joinLines(cooldownPrefixLines, cooldownValidBlockLines, cooldownSuffixLines))

	b, err := cooldown.Load(root)
	if err != nil {
		t.Fatalf("Load of a valid format=1 block: %v", err)
	}
	if b == nil {
		t.Fatal("Load returned a nil block for a file that carries one; nil means cooldown is disabled")
	}
	if b.Days != 7 {
		t.Errorf("Days = %d, want 7 (the BEGIN line's days=7)", b.Days)
	}
	if len(b.Entries) != 4 {
		t.Fatalf("parsed %d entries, want 4: %+v", len(b.Entries), b.Entries)
	}

	cases := []struct {
		pkg, version, since, origin, group string
	}{
		// Hostile pair first: same version, names one a prefix of the other.
		{"sys-apps/ai-jail", "3.2.0", "2026-11-07T07:10:00Z", "autoupdate", "sys-apps/ai-jail,sys-apps/ai-jail-bin"},
		{"sys-apps/ai-jail-bin", "3.2.0", "2026-11-07T07:10:00Z", "autoupdate", "sys-apps/ai-jail,sys-apps/ai-jail-bin"},
		// The name keeps its `-2d` segment; the version is only `1.0`.
		{"media-libs/libfoo-2d", "1.0", "2026-11-02T08:00:00Z", "autoupdate", ""},
		// The revision is part of the PVR.
		{"net-misc/freerdp", "3.33.0-r1", "2026-11-01T00:00:00Z", "manual", ""},
	}
	for _, c := range cases {
		e, ok := findEntry(b, c.pkg, c.version)
		if !ok {
			t.Errorf("no entry Package=%q Version=%q; got %+v", c.pkg, c.version, b.Entries)
			continue
		}
		if want := mustTime(t, c.since); !e.Since.Equal(want) {
			t.Errorf("%s-%s: Since = %s, want %s", c.pkg, c.version, e.Since, want)
		}
		if e.Origin != c.origin {
			t.Errorf("%s-%s: Origin = %q, want %q", c.pkg, c.version, e.Origin, c.origin)
		}
		if e.Group != c.group {
			t.Errorf("%s-%s: Group = %q, want %q", c.pkg, c.version, e.Group, c.group)
		}
	}

	// The wrong splits, named so a failure says which one happened.
	for _, wrong := range []struct{ pkg, version string }{
		{"sys-apps/ai-jail", "bin-3.2.0"},
		{"media-libs/libfoo", "2d-1.0"},
		{"net-misc/freerdp-3.33.0", "r1"},
	} {
		if _, ok := findEntry(b, wrong.pkg, wrong.version); ok {
			t.Errorf("atom split wrongly into Package=%q Version=%q", wrong.pkg, wrong.version)
		}
	}
}

// TestCooldownParse_NoBlockMeansDisabledAndWritesNothing: R1.4 / Q11 at the
// reader. A file without a BEGIN line — including one that merely MENTIONS
// the marker name in a comment — is "cooldown off": nil, nil, and no file is
// created or touched.
//
// The converse comes first: the same hand masks plus an EMPTY block are
// "cooldown on" — a non-nil block with no entries. A reader that returned nil
// for everything would pass the disabled half alone.
func TestCooldownParse_NoBlockMeansDisabledAndWritesNothing(t *testing.T) {
	t.Run("an empty block enables cooldown", func(t *testing.T) {
		root := writeOverlayMask(t, joinLines(cooldownPrefixLines,
			[]string{"# BEGIN bentoo-cooldown format=1 days=7", cooldownHeaderLine, "# END bentoo-cooldown"},
			cooldownSuffixLines))
		b, err := cooldown.Load(root)
		if err != nil {
			t.Fatalf("Load of an empty block: %v", err)
		}
		if b == nil {
			t.Fatal("Load of an empty block returned nil; the BEGIN line's presence is what enables cooldown")
		}
		if b.Days != 7 || len(b.Entries) != 0 {
			t.Errorf("empty block = Days %d, %d entries; want Days 7, 0 entries", b.Days, len(b.Entries))
		}
	})

	cases := map[string]string{
		"hand masks only": joinLines(cooldownPrefixLines, cooldownSuffixLines),
		// Hostile: the marker's name in prose is not a marker.
		"comment mentioning the marker": joinLines(cooldownPrefixLines,
			[]string{"# bentoo-cooldown is documented in CLAUDE.md; this is not a BEGIN line"}, cooldownSuffixLines),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeOverlayMask(t, content)
			before := readMask(t, root)

			b, err := cooldown.Load(root)
			if err != nil {
				t.Fatalf("Load without a block must not fail, got %v", err)
			}
			if b != nil {
				t.Fatalf("Load without a block returned %+v, want nil (cooldown disabled)", b)
			}
			if after := readMask(t, root); !bytes.Equal(before, after) {
				t.Error("Load changed profiles/package.mask")
			}
			if _, err := os.Stat(filepath.Join(root, ".autoupdate", "cooldown.unmask")); err == nil {
				t.Error("Load created .autoupdate/cooldown.unmask although the overlay has no block")
			}
		})
	}

	t.Run("no package.mask at all", func(t *testing.T) {
		root := t.TempDir()
		b, err := cooldown.Load(root)
		if err != nil || b != nil {
			t.Fatalf("Load on an overlay without profiles/package.mask = (%+v, %v), want (nil, nil)", b, err)
		}
	})
}

// TestCooldownParse_MalformedBlockNamesFileAndLine: R7.1. Each malformed
// shape fails, the error names profiles/package.mask and the FILE line of the
// offending line, and the file is left byte-identical.
func TestCooldownParse_MalformedBlockNamesFileAndLine(t *testing.T) {
	begin := "# BEGIN bentoo-cooldown format=1 days=7"
	end := "# END bentoo-cooldown"
	okComment := "# cooldown since=2026-11-07T07:10:00Z origin=autoupdate"

	// badAt is the index, inside block, of the line the error must name; -1
	// means "any line" (a missing END has no single offending line).
	cases := []struct {
		name  string
		block []string
		badAt int
	}{
		{"garbage line", []string{begin, cooldownHeaderLine, "this is not an atom", end}, 2},
		{"atom without its comment", []string{begin, cooldownHeaderLine, "=sys-apps/foo-1.0", end}, 2},
		{"operator other than =", []string{begin, cooldownHeaderLine, okComment, ">=sys-apps/foo-1.0", end}, 3},
		{"atom without a version", []string{begin, cooldownHeaderLine, okComment, "=sys-apps/foo", end}, 3},
		{"version outside the PVR grammar", []string{begin, cooldownHeaderLine, okComment, "=sys-apps/foo-1.0;rm", end}, 3},
		{"unparseable since", []string{begin, cooldownHeaderLine, "# cooldown since=yesterday origin=autoupdate", "=sys-apps/foo-1.0", end}, 2},
		{"comment without its atom", []string{begin, cooldownHeaderLine, okComment, end}, -1},
		{"second BEGIN", []string{begin, cooldownHeaderLine, okComment, "=sys-apps/foo-1.0", begin, end}, 4},
		{"days below 1", []string{"# BEGIN bentoo-cooldown format=1 days=0", cooldownHeaderLine, end}, 0},
		{"missing END", []string{begin, cooldownHeaderLine, okComment, "=sys-apps/foo-1.0"}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeOverlayMask(t, joinLines(cooldownPrefixLines, c.block, cooldownSuffixLines))
			before := readMask(t, root)

			b, err := cooldown.Load(root)
			if err == nil {
				t.Fatalf("Load accepted a malformed block (%s): %+v", c.name, b)
			}
			if !strings.Contains(err.Error(), "package.mask") {
				t.Errorf("the error does not name profiles/package.mask: %v", err)
			}
			if c.badAt >= 0 {
				// 1-based FILE line number: the prefix lines come first.
				line := len(cooldownPrefixLines) + c.badAt + 1
				re := regexp.MustCompile(fmt.Sprintf(`package\.mask:%d\b`, line))
				if !re.MatchString(err.Error()) {
					t.Errorf("the error does not name package.mask:%d (the file line, not the block line): %v", line, err)
				}
			}
			if after := readMask(t, root); !bytes.Equal(before, after) {
				t.Error("a malformed block was rewritten; a hand edit must never be clobbered")
			}
		})
	}
}

// TestCooldownFormat_ReadsFormat1AndRefusesEveryOther: R7.2 / Q16. format=1
// is read; any other format fails naming it and leaves the file alone. The
// `format=10` row is the prefix trap.
func TestCooldownFormat_ReadsFormat1AndRefusesEveryOther(t *testing.T) {
	body := []string{
		cooldownHeaderLine,
		"# cooldown since=2026-11-07T07:10:00Z origin=autoupdate",
		"=sys-apps/foo-1.0",
		"# END bentoo-cooldown",
	}
	for _, c := range []struct {
		format string
		ok     bool
	}{
		{"2", false},
		{"10", false},
		{"0", false},
		{"1", true},
	} {
		t.Run("format="+c.format, func(t *testing.T) {
			lines := append([]string{"# BEGIN bentoo-cooldown format=" + c.format + " days=7"}, body...)
			root := writeOverlayMask(t, joinLines(cooldownPrefixLines, lines, cooldownSuffixLines))
			before := readMask(t, root)

			b, err := cooldown.Load(root)
			if c.ok {
				if err != nil || b == nil {
					t.Fatalf("Load(format=1) = (%+v, %v), want a block", b, err)
				}
				if _, found := findEntry(b, "sys-apps/foo", "1.0"); !found {
					t.Errorf("format=1 block lost its entry: %+v", b.Entries)
				}
				return
			}
			if err == nil {
				t.Fatalf("Load accepted format=%s: %+v", c.format, b)
			}
			if !strings.Contains(err.Error(), "format") || !strings.Contains(err.Error(), c.format) {
				t.Errorf("the error does not name the format %q: %v", c.format, err)
			}
			if after := readMask(t, root); !bytes.Equal(before, after) {
				t.Error("a block of an unknown format was rewritten")
			}
		})
	}
}

// TestCooldownParse_ValidateAtomRefusesWhatIsNotAPVR: R7.4. The applier
// builds atoms from the record's cat/pkg and the promoted version; anything
// that could inject a second line or another atom is refused.
func TestCooldownParse_ValidateAtomRefusesWhatIsNotAPVR(t *testing.T) {
	bad := []struct{ pkg, pvr string }{
		{"sys-apps/foo", "1.0\n=evil/pkg-1"},
		{"sys-apps/foo", "1.0;rm -rf /"},
		{"sys-apps/foo", "1.0 "},
		{"sys-apps/foo", ""},
		{"sys-apps/foo", "v1.0"},
		{"sys-apps/foo", "1.0-r"},
		{"sys-apps/foo", "1.0::bentoo"},
		{"sys-apps/foo", "1.0*"},
		{"sys-apps/foo", "../1.0"},
		{"foo", "1.0"},
		{"=sys-apps/foo", "1.0"},
		{"sys-apps/foo\n=evil/pkg", "1.0"},
	}
	for _, c := range bad {
		if err := cooldown.ValidateAtom(c.pkg, c.pvr); err == nil {
			t.Errorf("ValidateAtom(%q, %q) = nil, want an error", c.pkg, c.pvr)
		}
	}

	good := []struct{ pkg, pvr string }{
		{"sys-apps/foo", "1.0"},
		{"sys-apps/foo", "1.2.3-r1"},
		{"media-libs/vulkan-loader", "1.4.330_pre20261107"},
		{"dev-lang/foo", "2.0_rc1-r2"},
		{"sys-apps/ai-jail-bin", "3.2.0"},
		{"media-libs/libfoo-2d", "1.0"},
	}
	for _, c := range good {
		if err := cooldown.ValidateAtom(c.pkg, c.pvr); err != nil {
			t.Errorf("ValidateAtom(%q, %q) = %v, want nil", c.pkg, c.pvr, err)
		}
	}
}
