package overlay

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"
)

func TestKeywordedFor(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"testing keyword", "KEYWORDS=\"~amd64 ~arm64\"\n", true},
		{"stable keyword", "KEYWORDS=\"amd64 x86\"\n", true},
		{"commented out (glibc -r5)", "\t#KEYWORDS=\"~amd64 ~x86\"\n", false},
		{"no assignment (snapshot)", "EAPI=8\nSLOT=0\n", false},
		{"other arch only", "KEYWORDS=\"~arm64\"\n", false},
		{"live branch then release branch", "if [[ ${PV} == 9999 ]]; then\n\tinherit git-r3\nelse\n\tKEYWORDS=\"~amd64\"\nfi\n", true},
		{"multi-line value", "KEYWORDS=\"~arm64\n\t~amd64\"\n", true},
		{"masked arch", "KEYWORDS=\"-amd64 ~arm64\"\n", false},
	}
	for _, c := range cases {
		if got := keywordedFor(c.body, "amd64"); got != c.want {
			t.Errorf("%s: keywordedFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMainSlot(t *testing.T) {
	cases := []struct {
		body, version string
		main, sub     string
		ok            bool
	}{
		{"SLOT=\"0\"\n", "2.28.5", "0", "", true},
		{"SLOT=\"2.36\"\n", "2.36.3", "2.36", "", true},
		{"SLOT=\"3/$(ver_cut 1-2)\"\n", "5.0.1", "3", "5.0", true},
		{"SLOT=\"3/$(ver_cut 1-2)\"\n", "3.4.1-r1", "3", "3.4", true},
		{"SLOT=\"0.16/4\"\n", "0.16.2-r1", "0.16", "4", true},
		{"SLOT=\"${PV}\"\n", "1.2-r3", "1.2", "", true},
		{"SLOT=\"${LLVM_MAJOR}/${LLVM_SOABI}\"\n", "23.1.5", "", "", false},
		{"EAPI=8\n", "16.2.1", "", "", false}, // gcc: SLOT comes from toolchain.eclass
	}
	for _, c := range cases {
		main, sub, ok := mainSlot(c.body, c.version)
		if main != c.main || sub != c.sub || ok != c.ok {
			t.Errorf("mainSlot(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				c.body, c.version, main, sub, ok, c.main, c.sub, c.ok)
		}
	}
}

func TestVerCut(t *testing.T) {
	cases := map[string]string{"1": "5", "1-2": "5.0", "2-": "0.1_p2", "1-9": "5.0.1_p2", "9": ""}
	for spec, want := range cases {
		if got := verCut("5.0.1_p2", spec); got != want {
			t.Errorf("verCut(5.0.1_p2, %q) = %q, want %q", spec, got, want)
		}
	}
}

func TestMaskMatching(t *testing.T) {
	const atom = "dev-cpp/eigen"
	cases := []struct {
		line, version, slot, sub string
		want                     bool
	}{
		{"dev-cpp/eigen:3/5.0", "5.0.1", "3", "5.0", true},
		{"dev-cpp/eigen:3/5.0", "3.4.1-r1", "3", "3.4", false},
		{"dev-cpp/eigen", "3.4.1", "3", "3.4", true},
		{">=dev-cpp/eigen-5", "5.0.1", "3", "5.0", true},
		{">=dev-cpp/eigen-5", "3.4.1", "3", "3.4", false},
		{"=dev-cpp/eigen-5.0*", "5.0.1", "3", "5.0", true},
		{"~dev-cpp/eigen-3.4.1", "3.4.1-r1", "3", "3.4", true},
		{"<dev-cpp/eigen-3.4::gentoo", "3.3.9", "3", "3.3", true},
	}
	for _, c := range cases {
		a, ok := parseMaskAtom(c.line, atom)
		if !ok {
			t.Fatalf("parseMaskAtom(%q) refused a valid line", c.line)
		}
		if got := maskedBy([]maskAtom{a}, c.version, c.slot, c.sub, true); got != c.want {
			t.Errorf("%q masks %s = %v, want %v", c.line, c.version, got, c.want)
		}
	}
	for _, line := range []string{"dev-cpp/eigen-extra", "=dev-cpp/eigen-extra-1.0", "dev-cpp/eigenx"} {
		if _, ok := parseMaskAtom(line, atom); ok {
			t.Errorf("parseMaskAtom(%q) accepted a line about another package", line)
		}
	}
}

// TestComparableRemoteVersions_Tree reproduces three of the 2026-11-05 false
// "needs rebase" rows on disk: an unkeyworded revision, another SLOT, a masked
// sub-slot — and checks the narrowing reaches Status through CompareWithProvider.
func TestComparableRemoteVersions_Tree(t *testing.T) {
	overlay, gentoo := t.TempDir(), t.TempDir()
	const kw = "KEYWORDS=\"~amd64\"\n"

	writeVerifyEbuild(t, overlay, "sys-libs", "glibc", "2.44-r4", "SLOT=\"2.2\"\n"+kw)
	writeVerifyEbuild(t, gentoo, "sys-libs", "glibc", "2.44-r4", "SLOT=\"2.2\"\n"+kw)
	writeVerifyEbuild(t, gentoo, "sys-libs", "glibc", "2.44-r5", "SLOT=\"2.2\"\n\t#"+kw)

	writeVerifyEbuild(t, overlay, "dev-cpp", "atkmm", "2.28.5", "SLOT=\"0\"\n"+kw)
	writeVerifyEbuild(t, gentoo, "dev-cpp", "atkmm", "2.28.4", "SLOT=\"0\"\n"+kw)
	writeVerifyEbuild(t, gentoo, "dev-cpp", "atkmm", "2.36.3", "SLOT=\"2.36\"\n"+kw)

	eigenSlot := "SLOT=\"3/$(ver_cut 1-2)\"\n"
	writeVerifyEbuild(t, overlay, "dev-cpp", "eigen", "3.4.1-r1", eigenSlot+kw)
	writeVerifyEbuild(t, gentoo, "dev-cpp", "eigen", "3.4.1-r1", eigenSlot+kw)
	writeVerifyEbuild(t, gentoo, "dev-cpp", "eigen", "5.0.1", eigenSlot+kw)
	if err := os.MkdirAll(filepath.Join(gentoo, "profiles"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gentoo, "profiles", "package.mask"),
		[]byte("# too many breakages\ndev-cpp/eigen:3/5.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A genuine bump in the same slot must still read as outdated.
	writeVerifyEbuild(t, overlay, "app-misc", "tmux", "3.7", "SLOT=\"0\"\n"+kw)
	writeVerifyEbuild(t, gentoo, "app-misc", "tmux", "3.8", "SLOT=\"0\"\n"+kw)

	prov := &localRootedFakeProvider{root: gentoo, versions: map[string][]string{
		"sys-libs/glibc": {"2.44-r4", "2.44-r5"},
		"dev-cpp/atkmm":  {"2.28.4", "2.36.3"},
		"dev-cpp/eigen":  {"3.4.1-r1", "5.0.1"},
		"app-misc/tmux":  {"3.8"},
	}}
	want := map[string]struct {
		status         CompareStatus
		remote, newest string
	}{
		"glibc": {StatusUpToDate, "2.44-r4", "2.44-r5"},
		"atkmm": {StatusNewer, "2.28.4", "2.36.3"},
		"eigen": {StatusUpToDate, "3.4.1-r1", "5.0.1"},
		"tmux":  {StatusOutdated, "3.8", ""},
	}
	pkgs := []repo.PackageInfo{
		{Category: "sys-libs", Package: "glibc", LatestVersion: "2.44-r4"},
		{Category: "dev-cpp", Package: "atkmm", LatestVersion: "2.28.5"},
		{Category: "dev-cpp", Package: "eigen", LatestVersion: "3.4.1-r1"},
		{Category: "app-misc", Package: "tmux", LatestVersion: "3.7"},
	}
	for _, pkg := range pkgs {
		got, _ := verifyRun(t, overlay, prov, pkg, nil)
		w := want[pkg.Package]
		if got.Status != w.status || got.RemoteVersion != w.remote || got.RemoteNewest != w.newest {
			t.Errorf("%s: (status %v, remote %q, newest %q), want (%v, %q, %q)",
				pkg.Package, got.Status, got.RemoteVersion, got.RemoteNewest, w.status, w.remote, w.newest)
		}
	}
}

// TestComparableRemoteVersions_NothingSurvives keeps the old answer when every
// version would be set aside, rather than inventing "not in remote".
func TestComparableRemoteVersions_NothingSurvives(t *testing.T) {
	overlay, gentoo := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlay, "cat", "pkg", "1.0", "SLOT=\"0\"\nKEYWORDS=\"~amd64\"\n")
	writeVerifyEbuild(t, gentoo, "cat", "pkg", "2.0", "SLOT=\"0\"\n")
	got := comparableRemoteVersions([]string{"2.0"}, filepath.Join(overlay, "cat", "pkg"), "pkg", "1.0",
		filepath.Join(gentoo, "cat", "pkg"))
	if !slices.Equal(got, []string{"2.0"}) {
		t.Errorf("comparableRemoteVersions = %v, want the input back", got)
	}
}

func TestComparedDetail_NamesSetAsideVersion(t *testing.T) {
	r := CompareResult{Status: StatusUpToDate, LocalVersion: "2.44-r4", RemoteVersion: "2.44-r4", RemoteNewest: "2.44-r5"}
	if got := comparedDetail(r); !strings.Contains(got, "2.44-r5") {
		t.Errorf("comparedDetail = %q, want it to name the set-aside 2.44-r5", got)
	}
	r.RemoteNewest = ""
	if got := comparedDetail(r); strings.Contains(got, "not comparable") {
		t.Errorf("comparedDetail = %q, want no note when nothing was set aside", got)
	}
}
