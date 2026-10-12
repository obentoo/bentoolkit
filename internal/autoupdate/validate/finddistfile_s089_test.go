package validate

import (
	"path/filepath"
	"strings"
	"testing"
)

// Story 089, R2.3 and R2.4: a version is matched against a distfile name as a
// whole version token, never as a substring. The token rule, revision stripped:
// not preceded by a digit or by `.`, not followed by a digit nor by `.` or `_`
// and a digit. With no whole-token match the selection declines with the same
// "does not belong to version" refusal the options gate already gives.
//
// The hostile fixtures come first: names that CONTAIN the version's characters
// but denote another release (they must stay apart), then names that decorate
// the version with a suffix or a revision (they must still come together), and
// only then the plain case.

// s089vFixture builds a package whose Manifest names manifestNames, and a
// distdir in which only the names in present exist.
func s089vFixture(t *testing.T, manifestNames, present []string) (pkgDir, distdir string) {
	t.Helper()
	overlay := overlayWith(t, "dev-libs/m", "1.0", goldenEbuild, manifestNames[0])
	pkgDir = filepath.Join(overlay, "dev-libs", "m")
	manifestNaming(t, pkgDir, manifestNames...)

	distdir = t.TempDir()
	for _, name := range present {
		archive := buildTarGz(t, map[string]string{"m/meson.build": "project('m')\n"})
		linkInto(t, archive, filepath.Join(distdir, name))
	}
	return pkgDir, distdir
}

// s089vDeclined fails the test unless findDistfile refused, naming the distfile
// it declined and stating that it does not belong to the version (R2.4).
func s089vDeclined(t *testing.T, pkgDir, distdir, version, declined string) {
	t.Helper()
	got, err := findDistfile(pkgDir, distdir, version)
	if err == nil {
		t.Fatalf("findDistfile accepted %q as the archive of version %s; the version appears in that name only "+
			"as a substring of another release's version, not as a whole version token", filepath.Base(got), version)
	}
	for _, want := range []string{declined, "does not belong to version " + version, distdir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not contain %q; R2.4 wants the same refusal the options gate gives "+
				"when no distfile belongs to the version", err, want)
		}
	}
}

// s089vChosen fails the test unless findDistfile chose want.
func s089vChosen(t *testing.T, pkgDir, distdir, version, want string) {
	t.Helper()
	got, err := findDistfile(pkgDir, distdir, version)
	if err != nil {
		t.Fatalf("findDistfile for version %s: %v — %q carries the version as a whole token", version, err, want)
	}
	if filepath.Base(got) != want {
		t.Errorf("findDistfile for version %s read %q, want %q", version, filepath.Base(got), want)
	}
}

// TestFindDistfile_S089ALongerVersionIsNotThisVersion: the one present archive
// is 1.0.1's, whose name contains "1.0" followed by `.` and a digit.
func TestFindDistfile_S089ALongerVersionIsNotThisVersion(t *testing.T) {
	pkgDir, distdir := s089vFixture(t,
		[]string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"},
		[]string{"m-1.0.1.tar.gz"})
	s089vDeclined(t, pkgDir, distdir, "1.0", "m-1.0.1.tar.gz")
}

// TestFindDistfile_S089AVersionInsideAnotherIsNotThisVersion: "1.0" preceded
// by a digit (11.0) or by `.` (2.1.0) is part of another version.
func TestFindDistfile_S089AVersionInsideAnotherIsNotThisVersion(t *testing.T) {
	t.Run("preceded by a digit", func(t *testing.T) {
		pkgDir, distdir := s089vFixture(t,
			[]string{"m-1.0.tar.gz", "m-11.0.tar.gz"},
			[]string{"m-11.0.tar.gz"})
		s089vDeclined(t, pkgDir, distdir, "1.0", "m-11.0.tar.gz")
	})
	t.Run("preceded by a dot", func(t *testing.T) {
		pkgDir, distdir := s089vFixture(t,
			[]string{"m-1.0.tar.gz", "m-2.1.0.tar.gz"},
			[]string{"m-2.1.0.tar.gz"})
		s089vDeclined(t, pkgDir, distdir, "1.0", "m-2.1.0.tar.gz")
	})
	t.Run("followed by an underscore and a digit", func(t *testing.T) {
		pkgDir, distdir := s089vFixture(t,
			[]string{"m-1.0.tar.gz", "m-1.0_1.tar.gz"},
			[]string{"m-1.0_1.tar.gz"})
		s089vDeclined(t, pkgDir, distdir, "1.0", "m-1.0_1.tar.gz")
	})
	t.Run("a shorter version followed by a dot and a digit", func(t *testing.T) {
		pkgDir, distdir := s089vFixture(t,
			[]string{"m-1.tar.gz", "m-1.0.tar.gz"},
			[]string{"m-1.0.tar.gz"})
		s089vDeclined(t, pkgDir, distdir, "1", "m-1.0.tar.gz")
	})
}

// TestFindDistfile_S089AmongSeveralTheWholeTokenWins: with 1.0's and 1.0.1's
// archives both present, only one carries "1.0" as a whole token. A substring
// match finds two and declines as ambiguous.
func TestFindDistfile_S089AmongSeveralTheWholeTokenWins(t *testing.T) {
	t.Run("two present", func(t *testing.T) {
		pkgDir, distdir := s089vFixture(t,
			[]string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"},
			[]string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"})
		s089vChosen(t, pkgDir, distdir, "1.0", "m-1.0.tar.gz")
	})
	// A third like-named release must not reopen the ambiguity.
	t.Run("three present", func(t *testing.T) {
		names := []string{"m-1.0.tar.gz", "m-1.0.1.tar.gz", "m-11.0.tar.gz"}
		pkgDir, distdir := s089vFixture(t, names, names)
		s089vChosen(t, pkgDir, distdir, "1.0", "m-1.0.tar.gz")
	})
}

// TestFindDistfile_S089SeveralPresentNoneThisVersionIsDeclined is a guard,
// green today: several present, none carrying 1.0 as a whole token, must still
// be a refusal — never a pick.
func TestFindDistfile_S089SeveralPresentNoneThisVersionIsDeclined(t *testing.T) {
	pkgDir, distdir := s089vFixture(t,
		[]string{"m-1.0.tar.gz", "m-1.0.1.tar.gz", "m-11.0.tar.gz"},
		[]string{"m-1.0.1.tar.gz", "m-11.0.tar.gz"})
	got, err := findDistfile(pkgDir, distdir, "1.0")
	if err == nil {
		t.Fatalf("findDistfile picked %q for version 1.0 while no present name carries 1.0 as a whole token",
			filepath.Base(got))
	}
	if !strings.Contains(err.Error(), "1.0") || !strings.Contains(err.Error(), distdir) {
		t.Errorf("the refusal %q names neither the version nor the directory searched", err)
	}
}

// TestFindDistfile_S089DecoratedNamesStillCarryTheVersion is the converse,
// green today and after the fix: a suffix, a different separator or a Gentoo
// revision must not split a name from its version.
func TestFindDistfile_S089DecoratedNamesStillCarryTheVersion(t *testing.T) {
	cases := []struct {
		desc, version, name string
	}{
		{"a revision is stripped before matching", "1.0-r1", "m-1.0.tar.gz"},
		{"a -src suffix", "1.0", "m-1.0-src.tar.gz"},
		{"an underscore separator and .orig", "1.0", "m_1.0.orig.tar.gz"},
		{"an underscore followed by a letter", "1.0", "m-1.0_rc.tar.gz"},
		{"a v prefix", "1.0", "m-v1.0.tar.gz"},
		{"the plain name", "1.0", "m-1.0.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			pkgDir, distdir := s089vFixture(t,
				[]string{c.name, "m-2.0.tar.gz"},
				[]string{c.name})
			s089vChosen(t, pkgDir, distdir, c.version, c.name)
		})
	}
}

// TestFindDistfile_S089VersionlessNamesKeepTheShortcut: with no version token
// in any name the single present file is still the only candidate.
func TestFindDistfile_S089VersionlessNamesKeepTheShortcut(t *testing.T) {
	const snapshot = "deadbeefcafe1234.tar.gz"
	pkgDir, distdir := s089vFixture(t, []string{snapshot}, []string{snapshot})
	s089vChosen(t, pkgDir, distdir, "1.0", snapshot)
}
