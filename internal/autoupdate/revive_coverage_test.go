package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// errProvider is a provider.Provider whose GetPackageVersions always returns a
// fixed non-ErrNotFound error, exercising FindRevivableOrphans's "gentoo lookup
// failed" soft-error branch. It is a distinct type from fakeProvider (not a
// redefinition): fakeProvider can only signal ErrNotFound (absent key) or a
// success, never an arbitrary provider error.
type errProvider struct {
	err error
}

func (e *errProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	return nil, e.err
}

func (e *errProvider) GetName() string   { return "errprov" }
func (e *errProvider) SupportsAPI() bool { return true }
func (e *errProvider) Close() error      { return nil }

// TestFindRevivableOrphans_InvalidNameSoftError covers the branch where a
// disabled entry's key has no category/pkg slash: it is recorded as a soft
// error note, the returned error is non-nil, and the entry yields no candidate.
func TestFindRevivableOrphans_InvalidNameSoftError(t *testing.T) {
	const badKey = "badname" // no slash -> invalid split

	checker := newReviveChecker(t, map[string]PackageConfig{
		badKey: {Parser: "json", Path: "version", URL: "http://127.0.0.1:0", Enabled: boolPtr(false)},
	})
	prov := &fakeProvider{versions: map[string][]string{}}

	got, err := checker.FindRevivableOrphans(prov)
	if err == nil {
		t.Fatal("expected soft error for invalid package name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid package name format") {
		t.Errorf("error = %v, want it to mention invalid package name format", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_UpstreamFetchSoftError covers the branch where a
// disabled entry's upstream fetch fails: the server responds 200 with a body
// that does NOT carry the configured JSON path, so the parse fails (a live
// server keeps this fast — no connection-refused retry/backoff). The failure is
// recorded as a soft error note, the returned error is non-nil, and the entry
// yields no candidate. The provider is never consulted for it.
func TestFindRevivableOrphans_UpstreamFetchSoftError(t *testing.T) {
	const pkg = "app-editors/brokenfetch"

	// 200 OK, but the JSON has no "version" field -> the json parser errors,
	// so fetchUpstreamVersion fails without any network retry.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unexpected":"shape"}`))
	}))
	t.Cleanup(srv.Close)

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	// Provider would report a gentoo version, but the upstream fetch fails first
	// so the package is dropped before the provider is consulted.
	prov := &fakeProvider{versions: map[string][]string{pkg: {"1.0.0"}}}

	got, err := checker.FindRevivableOrphans(prov)
	if err == nil {
		t.Fatal("expected soft error for failed upstream fetch, got nil")
	}
	if !strings.Contains(err.Error(), "upstream fetch failed") {
		t.Errorf("error = %v, want it to mention upstream fetch failed", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_GentooLookupSoftError covers the branch where the
// provider returns a NON-ErrNotFound error for a disabled entry: it is recorded
// as a "gentoo lookup failed" soft note, the returned error is non-nil, and the
// entry yields no candidate.
func TestFindRevivableOrphans_GentooLookupSoftError(t *testing.T) {
	const pkg = "app-editors/lookupfail"
	srv := jsonVersionServer(t, "3.0.0")

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	sentinel := errors.New("boom: gentoo backend unavailable")
	prov := &errProvider{err: sentinel}

	got, err := checker.FindRevivableOrphans(prov)
	if err == nil {
		t.Fatal("expected soft error for gentoo lookup failure, got nil")
	}
	if !strings.Contains(err.Error(), "gentoo lookup failed") {
		t.Errorf("error = %v, want it to mention gentoo lookup failed", err)
	}
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("error = %v, want it to wrap the sentinel %v", err, sentinel)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_GentooMaxEmptySkipped covers the branch where the
// provider returns only unparseable versions, so maxGentooVersion is "" and the
// entry is skipped silently (no candidate, no error).
func TestFindRevivableOrphans_GentooMaxEmptySkipped(t *testing.T) {
	const pkg = "app-editors/junkversions"
	srv := jsonVersionServer(t, "5.0.0")

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	// All entries are unparseable -> maxGentooVersion returns "".
	prov := &fakeProvider{versions: map[string][]string{
		pkg: {"not-a-version", "also-junk", ""},
	}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}

// TestMaxGentooVersion is a table test for maxGentooVersion, covering a mix of
// valid and unparseable entries (the highest valid one wins, junk is skipped)
// and an all-invalid slice (returns "").
func TestMaxGentooVersion(t *testing.T) {
	cases := []struct {
		name     string
		versions []string
		want     string
	}{
		{"empty slice", nil, ""},
		{"single valid", []string{"1.2.3"}, "1.2.3"},
		{"valid mixed with junk", []string{"1.0.0", "not-a-version", "1.5.0", ""}, "1.5.0"},
		{"junk before highest", []string{"garbage", "2.0.0", "9..bad", "2.1.0"}, "2.1.0"},
		{"all invalid", []string{"junk", "x.y.z?", "", "  "}, ""},
		{"whitespace trimmed", []string{"  3.3.3  "}, "3.3.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxGentooVersion(tc.versions); got != tc.want {
				t.Errorf("maxGentooVersion(%v) = %q, want %q", tc.versions, got, tc.want)
			}
		})
	}
}

// TestSeedFromGentooNestedFilesAndMetadata covers SeedFromGentoo's success path
// with a files/ subdir containing a NESTED file plus a present metadata.xml:
// both are copied (preserving the nested relative layout) and no Manifest is
// created in the overlay. This exercises copyTree's directory-recursion branch
// and copyFileContents end-to-end.
func TestSeedFromGentooNestedFilesAndMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const (
		pkg           = "app-misc/nested"
		pkgName       = "nested"
		gentooVersion = "2.4.6"
	)

	srcPkgDir := filepath.Join(tmpDir, "gentoo", "app-misc", pkgName)
	ebuildContent := "EAPI=8\nDESCRIPTION=\"nested seed\"\n"
	metaContent := "<?xml version=\"1.0\"?>\n<pkgmetadata/>\n"
	nestedPatch := "--- a/x\n+++ b/x\n@@ nested @@\n"
	topInit := "#!/sbin/openrc-run\n"

	seedWriteFile(t, filepath.Join(srcPkgDir, pkgName+"-"+gentooVersion+".ebuild"), ebuildContent)
	seedWriteFile(t, filepath.Join(srcPkgDir, "metadata.xml"), metaContent)
	seedWriteFile(t, filepath.Join(srcPkgDir, "files", "init.d"), topInit)
	// A nested subdirectory under files/ forces copyTree to recurse.
	seedWriteFile(t, filepath.Join(srcPkgDir, "files", "patches", "fix.patch"), nestedPatch)

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if err := applier.SeedFromGentoo(pkg, srcPkgDir, gentooVersion); err != nil {
		t.Fatalf("SeedFromGentoo: %v", err)
	}

	dst := filepath.Join(overlayDir, "app-misc", pkgName)
	if got := seedReadFile(t, filepath.Join(dst, pkgName+"-"+gentooVersion+".ebuild")); got != ebuildContent {
		t.Errorf("ebuild content mismatch: got %q want %q", got, ebuildContent)
	}
	if got := seedReadFile(t, filepath.Join(dst, "metadata.xml")); got != metaContent {
		t.Errorf("metadata.xml content mismatch: got %q want %q", got, metaContent)
	}
	if got := seedReadFile(t, filepath.Join(dst, "files", "init.d")); got != topInit {
		t.Errorf("files/init.d content mismatch: got %q want %q", got, topInit)
	}
	if got := seedReadFile(t, filepath.Join(dst, "files", "patches", "fix.patch")); got != nestedPatch {
		t.Errorf("nested files/patches/fix.patch content mismatch: got %q want %q", got, nestedPatch)
	}
	// Manifest is deliberately never created by SeedFromGentoo.
	if _, err := os.Stat(filepath.Join(dst, "Manifest")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected Manifest to be absent in overlay, stat err = %v", err)
	}
}

// TestCopyTreeNonexistentSource covers copyTree's error branch: pointing it at a
// source directory that does not exist makes filepath.Walk invoke the callback
// with a non-nil err, which copyTree wraps and returns.
func TestCopyTreeNonexistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "does-not-exist")
	dst := filepath.Join(tmpDir, "dst")

	if err := copyTree(src, dst); err == nil {
		t.Fatal("expected error copying a non-existent tree, got nil")
	}
}

// TestCopyFileContentsNonexistentSource covers copyFileContents's open-error
// branch: a source path that does not exist fails at os.Open.
func TestCopyFileContentsNonexistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "missing.txt")
	dst := filepath.Join(tmpDir, "out.txt")

	err := copyFileContents(src, dst)
	if err == nil {
		t.Fatal("expected error copying a non-existent file, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open source file") {
		t.Errorf("error = %v, want it to mention failed to open source file", err)
	}
}

// TestCopyFileContentsCreateError covers copyFileContents's create-error branch:
// a valid source but a destination whose parent directory does not exist fails
// at os.Create.
func TestCopyFileContentsCreateError(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "in.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Destination parent directory does not exist -> os.Create fails.
	dst := filepath.Join(tmpDir, "no-such-dir", "out.txt")

	err := copyFileContents(src, dst)
	if err == nil {
		t.Fatal("expected error creating destination in a missing dir, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create destination file") {
		t.Errorf("error = %v, want it to mention failed to create destination file", err)
	}
}

// TestSeedFromGentooFilesIsRegularFile covers the SeedFromGentoo branch where a
// "files" entry exists in the source dir but is a REGULAR FILE rather than a
// directory: it is silently skipped (info.IsDir() is false), the ebuild is still
// seeded, and no "files" entry is created in the overlay.
func TestSeedFromGentooFilesIsRegularFile(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const (
		pkg     = "app-misc/regfiles"
		pkgName = "regfiles"
		version = "1.1.1"
	)
	srcPkgDir := filepath.Join(tmpDir, "gentoo", "app-misc", pkgName)
	ebuildContent := "EAPI=8\nDESCRIPTION=\"reg files\"\n"
	seedWriteFile(t, filepath.Join(srcPkgDir, pkgName+"-"+version+".ebuild"), ebuildContent)
	// "files" exists but as a regular file, not a directory -> the IsDir branch
	// is false and copyTree is never called.
	seedWriteFile(t, filepath.Join(srcPkgDir, "files"), "not a directory\n")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if err := applier.SeedFromGentoo(pkg, srcPkgDir, version); err != nil {
		t.Fatalf("SeedFromGentoo: %v", err)
	}

	dst := filepath.Join(overlayDir, "app-misc", pkgName)
	if got := seedReadFile(t, filepath.Join(dst, pkgName+"-"+version+".ebuild")); got != ebuildContent {
		t.Errorf("ebuild content mismatch: got %q want %q", got, ebuildContent)
	}
	// The regular "files" entry must NOT have been copied into the overlay.
	if _, err := os.Stat(filepath.Join(dst, "files")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected no files entry in overlay, stat err = %v", err)
	}
}

// TestCopyTreeFileCopyError covers copyTree's copyFileContents-error branch: a
// source tree containing a regular file whose destination path already exists as
// a DIRECTORY makes os.Create fail ("is a directory"), so copyTree wraps and
// returns the error. This works regardless of process privileges (no perm bits).
func TestCopyTreeFileCopyError(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	// Source: a single regular file "clash".
	seedWriteFile(t, filepath.Join(src, "clash"), "payload")
	// Destination already has "clash" as a DIRECTORY, so copying the file into it
	// fails at os.Create.
	if err := os.MkdirAll(filepath.Join(dst, "clash"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := copyTree(src, dst); err == nil {
		t.Fatal("expected copyTree error when a file target is an existing dir, got nil")
	}
}

// verify the local errProvider actually satisfies provider.Provider at compile
// time (a guard rather than a runtime test).
var _ provider.Provider = (*errProvider)(nil)
