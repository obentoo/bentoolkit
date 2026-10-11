package repair

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// stageBundle creates <stagingRoot>/<pkg>/<version>/ and writes a bundle there
// with the given creation time; it returns the staged root.
func stageBundle(t *testing.T, stagingRoot, pkg, version string, created time.Time) string {
	t.Helper()
	root := filepath.Join(stagingRoot, filepath.FromSlash(pkg), version)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cat, name, _ := strings.Cut(pkg, "/")
	b := Bundle{
		Schema:         SchemaVersion,
		Package:        pkg,
		CurrentVersion: "0.1",
		Candidate:      version,
		Stage:          "manifest",
		Failure:        "failed " + pkg + " " + version,
		StagedRoot:     root,
		EbuildPath:     filepath.Join(root, cat, name, name+"-"+version+".ebuild"),
		Created:        created,
	}
	if err := WriteBundle(b); err != nil {
		t.Fatalf("WriteBundle(%s %s): %v", pkg, version, err)
	}
	return root
}

var t0 = time.Date(2026, 10, 11, 7, 0, 0, 0, time.UTC)

// TestFindBundleNearIdenticalPackagesStayApart: a package whose name is a
// prefix of another, or the same name in another category, never answers for
// it (hostile half: wrong collapse).
func TestFindBundleNearIdenticalPackagesStayApart(t *testing.T) {
	staging := t.TempDir()
	stageBundle(t, staging, "dev-go/govulncheck", "1.9.0", t0.Add(time.Hour))
	stageBundle(t, staging, "app-misc/govuln", "2.0", t0.Add(2*time.Hour))

	for _, pkg := range []string{"dev-go/govuln", "dev-go/govulnchec", "app-misc/govulncheck"} {
		_, _, err := FindBundle(staging, pkg)
		if !errors.Is(err, ErrNoBundle) {
			t.Errorf("FindBundle(%q) err = %v, want ErrNoBundle (another package's bundle answered)", pkg, err)
		}
	}
	b, path, err := FindBundle(staging, "app-misc/govuln")
	if err != nil {
		t.Fatalf("FindBundle(app-misc/govuln): %v", err)
	}
	if b.Package != "app-misc/govuln" || !strings.HasPrefix(path, filepath.Join(staging, "app-misc", "govuln")+string(filepath.Separator)) {
		t.Fatalf("FindBundle(app-misc/govuln) = %q at %s", b.Package, path)
	}
}

// TestFindBundleTwoVersionsOfOnePackageYieldTheNewest: two staged versions of
// the same package are one answer, chosen by Created and not by directory
// order (hostile half: wrong split / wrong pick).
func TestFindBundleTwoVersionsOfOnePackageYieldTheNewest(t *testing.T) {
	cases := []struct {
		name        string
		olderVer    string
		newerVer    string
		wantVersion string
	}{
		{name: "lexically smaller dir is newer", olderVer: "2.0", newerVer: "10.0", wantVersion: "10.0"},
		{name: "lexically larger dir is newer", olderVer: "10.0", newerVer: "2.0", wantVersion: "2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			staging := t.TempDir()
			stageBundle(t, staging, "dev-go/tool", tc.olderVer, t0)
			newRoot := stageBundle(t, staging, "dev-go/tool", tc.newerVer, t0.Add(time.Minute))
			b, path, err := FindBundle(staging, "dev-go/tool")
			if err != nil {
				t.Fatalf("FindBundle: %v", err)
			}
			if b.Candidate != tc.wantVersion || b.StagedRoot != newRoot {
				t.Fatalf("FindBundle picked candidate %q root %q, want %q root %q", b.Candidate, b.StagedRoot, tc.wantVersion, newRoot)
			}
			if path != filepath.Join(newRoot, BundleFile) {
				t.Fatalf("path = %q, want %q", path, filepath.Join(newRoot, BundleFile))
			}
		})
	}
}

// TestFindBundleOneBundle is the benign case.
func TestFindBundleOneBundle(t *testing.T) {
	staging := t.TempDir()
	root := stageBundle(t, staging, "dev-go/govulncheck", "1.9.0", t0)
	b, path, err := FindBundle(staging, "dev-go/govulncheck")
	if err != nil {
		t.Fatalf("FindBundle: %v", err)
	}
	if b.Package != "dev-go/govulncheck" || b.Candidate != "1.9.0" || b.StagedRoot != root {
		t.Fatalf("FindBundle = %+v", b)
	}
	if path != filepath.Join(root, BundleFile) {
		t.Fatalf("path = %q", path)
	}
}

// TestFindBundleNoneListsThePackagesThatHaveOne covers R2.4.
func TestFindBundleNoneListsThePackagesThatHaveOne(t *testing.T) {
	staging := t.TempDir()
	stageBundle(t, staging, "dev-go/alpha", "1.0", t0)
	stageBundle(t, staging, "app-misc/beta", "2.0", t0)
	_, _, err := FindBundle(staging, "dev-go/missing")
	if !errors.Is(err, ErrNoBundle) {
		t.Fatalf("err = %v, want ErrNoBundle", err)
	}
	for _, p := range []string{"dev-go/alpha", "app-misc/beta"} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error does not list %s: %v", p, err)
		}
	}

	empty := t.TempDir()
	_, _, err = FindBundle(empty, "dev-go/missing")
	if !errors.Is(err, ErrNoBundle) {
		t.Fatalf("empty staging: err = %v, want ErrNoBundle", err)
	}
	if !regexp.MustCompile(`(?i)\bno(ne)?\b`).MatchString(err.Error()) {
		t.Fatalf("empty staging: error must say no package has a bundle: %v", err)
	}
}

// TestFindBundleUnreadableBundleNamesItsPath covers R2.5 through the lookup.
func TestFindBundleUnreadableBundleNamesItsPath(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantSchema bool
	}{
		{name: "unknown schema", content: `{"schema":7,"package":"dev-go/x"}`, wantSchema: true},
		{name: "garbage", content: "{{{"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			staging := t.TempDir()
			root := filepath.Join(staging, "dev-go", "x", "1.0")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, BundleFile)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := FindBundle(staging, "dev-go/x")
			if err == nil {
				t.Fatalf("FindBundle accepted an unreadable bundle")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error does not name %s: %v", path, err)
			}
			if tc.wantSchema && !errors.Is(err, ErrUnknownSchema) {
				t.Fatalf("err = %v, want ErrUnknownSchema", err)
			}
		})
	}
}

// TestFindBundleStagedTreeGone covers R2.6: the bundle names a staged root that
// no longer exists.
func TestFindBundleStagedTreeGone(t *testing.T) {
	staging := t.TempDir()
	root := filepath.Join(staging, "dev-go", "x", "1.0")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "moved-away", "1.0")
	b := Bundle{Schema: SchemaVersion, Package: "dev-go/x", Candidate: "1.0", StagedRoot: root, Created: t0}
	if err := WriteBundle(b); err != nil {
		t.Fatal(err)
	}
	// Rewrite the recorded root to a path that does not exist.
	data, err := os.ReadFile(filepath.Join(root, BundleFile))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), root, gone, 1))
	if err := os.WriteFile(filepath.Join(root, BundleFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = FindBundle(staging, "dev-go/x")
	if !errors.Is(err, ErrStagedTreeGone) {
		t.Fatalf("err = %v, want ErrStagedTreeGone", err)
	}
	if !strings.Contains(err.Error(), gone) {
		t.Fatalf("error does not name the missing path %s: %v", gone, err)
	}
}

// TestListBundlesOnePerPackage: two versions of one package are one entry,
// two packages sharing a name across categories are two (both hostile halves).
func TestListBundlesOnePerPackage(t *testing.T) {
	staging := t.TempDir()
	stageBundle(t, staging, "dev-go/tool", "1.0", t0)
	stageBundle(t, staging, "dev-go/tool", "2.0", t0.Add(time.Minute))
	stageBundle(t, staging, "app-misc/tool", "1.0", t0)
	stageBundle(t, staging, "dev-go/toolkit", "3.0", t0)
	// A staged tree without a bundle is not listed.
	if err := os.MkdirAll(filepath.Join(staging, "dev-go", "nobundle", "1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ListBundles(staging)
	if err != nil {
		t.Fatalf("ListBundles: %v", err)
	}
	slices.Sort(got)
	want := []string{"app-misc/tool", "dev-go/tool", "dev-go/toolkit"}
	if !slices.Equal(got, want) {
		t.Fatalf("ListBundles = %v, want %v", got, want)
	}

	none, err := ListBundles(t.TempDir())
	if err != nil {
		t.Fatalf("ListBundles(empty): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListBundles(empty) = %v, want none", none)
	}
}

// TestRemoveBundleRemovesOnlyTheBundle covers R5.3: the bundle goes, the
// staged tree stays, and removing an absent bundle is not an error.
func TestRemoveBundleRemovesOnlyTheBundle(t *testing.T) {
	staging := t.TempDir()
	root := stageBundle(t, staging, "dev-go/x", "1.0", t0)
	ebuild := filepath.Join(root, "dev-go", "x", "x-1.0.ebuild")
	if err := os.MkdirAll(filepath.Dir(ebuild), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ebuild, []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBundle(root); err != nil {
		t.Fatalf("RemoveBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, BundleFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle still present after RemoveBundle (stat err %v)", err)
	}
	if _, err := os.Stat(ebuild); err != nil {
		t.Fatalf("RemoveBundle touched the staged tree: %v", err)
	}
	if err := RemoveBundle(root); err != nil {
		t.Fatalf("RemoveBundle on an absent bundle: %v", err)
	}
}
