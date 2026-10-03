package autoupdate

import (
	"strconv"
	"strings"
	"testing"
)

// rewritePinnedAtoms points every dependency atom of the required package that
// carries the declared operator at the captured version, and nothing else.

// requiresRewriteSource is an ebuild fragment in which every line but the
// marked ones names something the ~dev-lang/dart pin must NOT touch: other
// operators, a blocker, a package whose name extends "dart", the same name in
// another category, and commented-out lines. The marked lines carry the pin
// with a slot, with a USE dependency, inside a group, and inside quotes at the
// end of a line.
const requiresRewriteSource = `EAPI=8
# Pinned to the Dart SDK this release bundles: ~dev-lang/dart-3.13.5
	# ~dev-lang/dart-3.13.5 (indented comment)
RDEPEND="~dev-lang/dart-3.13.5"
DEPEND="
	~dev-lang/dart-3.13.5:0=
	~dev-lang/dart-3.13.5[analyzer]
	|| ( ~dev-lang/dart-3.13.5 dev-lang/dart-bin )
	>=dev-lang/dart-3.0
	<dev-lang/dart-4
	<=dev-lang/dart-4.1
	=dev-lang/dart-3.13*
	!~dev-lang/dart-2.0
	~dev-lang/dart-sdk-3.13.5
	~dev-lang/dart2-3.13.5
	~dev-util/dart-3.13.5
	~app-dev-lang/dart-3.13.5
"
BDEPEND="~dev-lang/dart-3.13.5"`

// requiresRewriteWant is the source with exactly the five pinned atoms moved.
const requiresRewriteWant = `EAPI=8
# Pinned to the Dart SDK this release bundles: ~dev-lang/dart-3.13.5
	# ~dev-lang/dart-3.13.5 (indented comment)
RDEPEND="~dev-lang/dart-3.14.0"
DEPEND="
	~dev-lang/dart-3.14.0:0=
	~dev-lang/dart-3.14.0[analyzer]
	|| ( ~dev-lang/dart-3.14.0 dev-lang/dart-bin )
	>=dev-lang/dart-3.0
	<dev-lang/dart-4
	<=dev-lang/dart-4.1
	=dev-lang/dart-3.13*
	!~dev-lang/dart-2.0
	~dev-lang/dart-sdk-3.13.5
	~dev-lang/dart2-3.13.5
	~dev-util/dart-3.13.5
	~app-dev-lang/dart-3.13.5
"
BDEPEND="~dev-lang/dart-3.14.0"`

// requiresRewriteDiff reports the first differing line, for a readable failure.
func requiresRewriteDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "line " + strconv.Itoa(i+1) + ":\n want " + wl + "\n  got " + gl
		}
	}
	return "(no difference)"
}

// TestRewritePinnedAtomsTildeOnly: the ~ pin rewrites the five ~dev-lang/dart
// atoms, keeps slot, USE dependency, group and quote around the new version,
// and leaves every other operator, the blocker, look-alike packages and
// commented lines byte-identical.
func TestRewritePinnedAtomsTildeOnly(t *testing.T) {
	got, n, err := rewritePinnedAtoms([]byte(requiresRewriteSource), "dev-lang/dart", "~", "3.14.0")
	if err != nil {
		t.Fatalf("rewritePinnedAtoms: %v", err)
	}
	if string(got) != requiresRewriteWant {
		t.Errorf("rewritten ebuild differs: %s\n--- got ---\n%s", requiresRewriteDiff(requiresRewriteWant, string(got)), got)
	}
	if n != 5 {
		t.Errorf("count = %d, want 5", n)
	}
}

// TestRewritePinnedAtomsOtherOperators: the = pin keeps a trailing * and does
// not match the tail of >= or <=; the >= pin does not match a ~ or = atom.
func TestRewritePinnedAtomsOtherOperators(t *testing.T) {
	for _, tc := range []struct {
		name, pin, src, want string
		count                int
	}{
		{
			name:  "= keeps the wildcard",
			pin:   "=",
			src:   "DEPEND=\"=dev-lang/dart-3.13*\"\n",
			want:  "DEPEND=\"=dev-lang/dart-3.14.0*\"\n",
			count: 1,
		},
		{
			name:  "= is not the tail of >= or <=",
			pin:   "=",
			src:   "DEPEND=\">=dev-lang/dart-3.0 <=dev-lang/dart-4.1 =dev-lang/dart-3.13.5\"\n",
			want:  "DEPEND=\">=dev-lang/dart-3.0 <=dev-lang/dart-4.1 =dev-lang/dart-3.14.0\"\n",
			count: 1,
		},
		{
			name:  ">= leaves ~ and = atoms",
			pin:   ">=",
			src:   "DEPEND=\"~dev-lang/dart-3.13.5 =dev-lang/dart-3.13.5 >=dev-lang/dart-3.0:=\"\n",
			want:  "DEPEND=\"~dev-lang/dart-3.13.5 =dev-lang/dart-3.13.5 >=dev-lang/dart-3.14.0:=\"\n",
			count: 1,
		},
		{
			name:  "revision in an = atom is replaced whole",
			pin:   "=",
			src:   "RDEPEND=\"=dev-lang/dart-3.13.5-r2\"\n",
			want:  "RDEPEND=\"=dev-lang/dart-3.14.0\"\n",
			count: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := rewritePinnedAtoms([]byte(tc.src), "dev-lang/dart", tc.pin, "3.14.0")
			if err != nil {
				t.Fatalf("rewritePinnedAtoms: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("rewritten:\n got %q\nwant %q", got, tc.want)
			}
			if n != tc.count {
				t.Errorf("count = %d, want %d", n, tc.count)
			}
		})
	}
}

// TestRewritePinnedAtomsNoMatch: an ebuild holding atoms of the package only
// with other operators, or only in comments, yields a zero count and is left
// unchanged — the caller turns that into a failed bump.
func TestRewritePinnedAtomsNoMatch(t *testing.T) {
	src := "# ~dev-lang/dart-3.13.5\nRDEPEND=\">=dev-lang/dart-3.0 ~dev-lang/dart-sdk-3.13.5 !~dev-lang/dart-2.0\"\n"
	got, n, err := rewritePinnedAtoms([]byte(src), "dev-lang/dart", "~", "3.14.0")
	if err != nil {
		t.Fatalf("rewritePinnedAtoms: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if string(got) != src {
		t.Errorf("an ebuild with no pinned atom changed:\n got %q\nwant %q", got, src)
	}
}

// TestRewritePinnedAtomsRefusesInvalidVersion: the version reaches bash
// source, so a value that is not a Gentoo version is refused — the source
// comes back unchanged and no count is claimed. A replacement-template
// reference must not expand either.
func TestRewritePinnedAtomsRefusesInvalidVersion(t *testing.T) {
	src := "RDEPEND=\"~dev-lang/dart-3.13.5\"\n"
	for _, version := range []string{"", "3.14.0\"; id; \"", "3.14.0 x", "${1}", "$(id)", "3.14.0\n", "v3.14.0"} {
		t.Run(version, func(t *testing.T) {
			got, n, err := rewritePinnedAtoms([]byte(src), "dev-lang/dart", "~", version)
			if err == nil {
				t.Fatalf("rewritePinnedAtoms accepted version %q: %q", version, got)
			}
			if n != 0 {
				t.Errorf("count = %d on a refusal, want 0", n)
			}
			if got != nil && string(got) != src {
				t.Errorf("a refused version changed the source: %q", got)
			}
		})
	}
}
