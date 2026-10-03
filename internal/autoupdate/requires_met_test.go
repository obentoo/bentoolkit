package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// The presence rule: is a requirement (pin + version of a package) satisfied by
// an ebuild the overlay or ::gentoo already holds? The overlay is searched
// first, then ::gentoo, because a required package may live in the master
// repository. Both trees are temp dirs; /var/db/repos/gentoo is never read.

// requiresMetTree lays out a repository holding the named ebuild files
// ("category/package/file") and returns its root.
func requiresMetTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte("EAPI=8\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root
}

type requiresMetCase struct {
	name    string
	ebuilds []string // dev-lang/dart ebuild versions in the overlay
	pin     string
	version string
	want    bool
}

func requiresMetRun(t *testing.T, cases []requiresMetCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]string, 0, len(tc.ebuilds)+1)
			for _, v := range tc.ebuilds {
				files = append(files, "dev-lang/dart/dart-"+v+".ebuild")
			}
			files = append(files, "dev-lang/dart/metadata.xml")
			overlay := requiresMetTree(t, files...)
			got, err := requirementMet(overlay, "", "dev-lang/dart", tc.pin, tc.version)
			if err != nil {
				t.Fatalf("requirementMet: %v", err)
			}
			if got != tc.want {
				t.Errorf("requirementMet(%v, %s%s) = %v, want %v", tc.ebuilds, tc.pin, tc.version, got, tc.want)
			}
		})
	}
}

// TestRequirementMetTilde: ~ is met by an ebuild whose version without its
// revision equals the captured version. Hostile first — versions that share a
// prefix, a suffix or a numeric value-in-disguise with 3.14.0 and are NOT it —
// then the converse, revisions that ARE it.
func TestRequirementMetTilde(t *testing.T) {
	requiresMetRun(t, []requiresMetCase{
		{"longer version sharing the prefix", []string{"3.14.0.1"}, "~", "3.14.0", false},
		{"shorter version", []string{"3.14"}, "~", "3.14.0", false},
		{"version ending with the captured one", []string{"13.14.0"}, "~", "3.14.0", false},
		{"pre-release of the captured one", []string{"3.14.0_rc1"}, "~", "3.14.0", false},
		{"patch release after it", []string{"3.14.0_p1"}, "~", "3.14.0", false},
		{"newer version", []string{"3.15.0"}, "~", "3.14.0", false},
		{"live ebuild only", []string{"9999"}, "~", "3.14.0", false},
		{"captured version is shorter than the ebuild", []string{"3.14.0"}, "~", "3.14", false},
		{"revision of it", []string{"3.14.0-r1"}, "~", "3.14.0", true},
		{"high revision of it", []string{"3.14.0-r12"}, "~", "3.14.0", true},
		{"exact", []string{"3.14.0"}, "~", "3.14.0", true},
		{"among others", []string{"3.13.5", "3.14.0-r2", "3.15.0"}, "~", "3.14.0", true},
	})
}

// TestRequirementMetEquals: = is met only by an ebuild whose full version,
// revision included, equals the captured version.
func TestRequirementMetEquals(t *testing.T) {
	requiresMetRun(t, []requiresMetCase{
		{"revision is not the bare version", []string{"3.14.0-r1"}, "=", "3.14.0", false},
		{"bare version is not the revision", []string{"3.14.0"}, "=", "3.14.0-r1", false},
		{"longer version sharing the prefix", []string{"3.14.0.1"}, "=", "3.14.0", false},
		{"newer version", []string{"3.15.0"}, "=", "3.14.0", false},
		{"exact", []string{"3.14.0"}, "=", "3.14.0", true},
		{"exact revision", []string{"3.14.0", "3.14.0-r1"}, "=", "3.14.0-r1", true},
	})
}

// TestRequirementMetAtLeast: >= is met by any ebuild whose version compares
// greater than or equal. The hostile cases order differently as strings and as
// versions: 3.9.0 sorts after 3.14.0 as text and is older; 3.14.0_rc1 starts
// with the captured version and is older.
func TestRequirementMetAtLeast(t *testing.T) {
	requiresMetRun(t, []requiresMetCase{
		{"textually greater, numerically older", []string{"3.9.0"}, ">=", "3.14.0", false},
		{"pre-release of it", []string{"3.14.0_rc1"}, ">=", "3.14.0", false},
		{"older", []string{"3.13.5"}, ">=", "3.14.0", false},
		{"textually smaller, numerically newer", []string{"3.100.0"}, ">=", "3.14.0", true},
		{"equal", []string{"3.14.0"}, ">=", "3.14.0", true},
		{"revision of it", []string{"3.14.0-r1"}, ">=", "3.14.0", true},
		{"newer", []string{"3.13.5", "4.0.0"}, ">=", "3.14.0", true},
	})
}

// TestRequirementMetOtherPackagesDoNotCount: only ebuilds of the required
// package itself count. A sibling whose name extends the atom's name, the same
// name in another category, and a file that is not an ebuild must all be
// ignored, however exactly their version matches.
func TestRequirementMetOtherPackagesDoNotCount(t *testing.T) {
	overlay := requiresMetTree(t,
		"dev-lang/dart-sdk/dart-sdk-3.14.0.ebuild",
		"dev-util/dart/dart-3.14.0.ebuild",
		"dev-lang/dart/dart-3.13.5.ebuild",
		"dev-lang/dart/dart-3.14.0.ebuild.orig",
		"dev-lang/dart/files/dart-3.14.0.ebuild",
		"dev-lang/dart/Manifest",
	)
	for _, pin := range []string{"~", "=", ">="} {
		got, err := requirementMet(overlay, "", "dev-lang/dart", pin, "3.14.0")
		if err != nil {
			t.Fatalf("requirementMet(%s): %v", pin, err)
		}
		if got {
			t.Errorf("requirementMet(%sdev-lang/dart-3.14.0) = true, satisfied by another package or a non-ebuild file", pin)
		}
	}
}

// TestRequirementMetGentooFallback: ::gentoo is consulted when the overlay
// does not satisfy the requirement; an unset ::gentoo path and a package
// absent from both trees are "not met", never an error.
func TestRequirementMetGentooFallback(t *testing.T) {
	overlayOld := requiresMetTree(t, "dev-lang/dart/dart-3.13.5.ebuild")
	overlayNone := requiresMetTree(t, "dev-lang/other/other-1.0.ebuild")
	gentoo := requiresMetTree(t, "dev-lang/dart/dart-3.14.0-r1.ebuild")
	gentooOld := requiresMetTree(t, "dev-lang/dart/dart-3.13.0.ebuild")

	for _, tc := range []struct {
		name            string
		overlay, gentoo string
		want            bool
	}{
		{"no gentoo path, overlay too old", overlayOld, "", false},
		{"gentoo also too old", overlayOld, gentooOld, false},
		{"absent from both trees", overlayNone, gentooOld, false},
		{"gentoo directory missing", overlayOld, filepath.Join(t.TempDir(), "nonexistent"), false},
		{"overlay too old, gentoo has it", overlayOld, gentoo, true},
		{"overlay lacks the package, gentoo has it", overlayNone, gentoo, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requirementMet(tc.overlay, tc.gentoo, "dev-lang/dart", "~", "3.14.0")
			if err != nil {
				t.Fatalf("requirementMet: %v", err)
			}
			if got != tc.want {
				t.Errorf("requirementMet = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestVersionSatisfies — the presence rule as one exported function
// (design.md "Requirement gate", Presence rule): `~` — have with its revision
// dropped equals want; `=` — have == want; `>=` — version comparison. The
// presence scan, the report's pending state and the wave planner all call it,
// so this table is the one place the rule itself is pinned.
//
// Hostile halves first. Wrongly collapse: versions that share a prefix, a
// suffix or a string ordering with the wanted one and are NOT it (a string
// compare would say 3.9.0 >= 3.14.0). Wrongly split: a revision of the very
// version, and a version that is larger numerically but smaller as a string.
// An operator outside the three is never satisfied.
func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		pin, have, want string
		ok              bool
	}{
		// Wrongly collapse: must NOT satisfy.
		{"~", "3.14.0.1", "3.14.0", false},
		{"~", "3.14.0_rc1", "3.14.0", false},
		{"~", "3.14.0_p1", "3.14.0", false},
		{"~", "13.14.0", "3.14.0", false},
		{"~", "3.14", "3.14.0", false},
		{"=", "3.14.0-r1", "3.14.0", false},
		{"=", "3.14.0", "3.14.0-r1", false},
		{">=", "3.9.0", "3.14.0", false},
		{">=", "3.14.0_rc1", "3.14.0", false},
		// Wrongly split: MUST satisfy.
		{"~", "3.14.0-r1", "3.14.0", true},
		{"~", "3.14.0-r12", "3.14.0", true},
		{">=", "3.100.0", "3.14.0", true},
		{">=", "3.14.0-r1", "3.14.0", true},
		{"=", "3.14.0-r1", "3.14.0-r1", true},
		// Benign.
		{"~", "3.14.0", "3.14.0", true},
		{"=", "3.14.0", "3.14.0", true},
		{">=", "3.14.0", "3.14.0", true},
		// An operator outside ~, = and >= is never satisfied — not even by a
		// version it would accept if it were implemented.
		{"<", "3.13.0", "3.14.0", false},
		{"<=", "3.14.0", "3.14.0", false},
		{"", "3.14.0", "3.14.0", false},
	}
	for _, tc := range cases {
		if got := VersionSatisfies(tc.pin, tc.have, tc.want); got != tc.ok {
			t.Errorf("VersionSatisfies(%q, %q, %q) = %v, want %v", tc.pin, tc.have, tc.want, got, tc.ok)
		}
	}
}
