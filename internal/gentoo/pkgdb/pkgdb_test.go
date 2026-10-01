package pkgdb_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
)

// vdbEntry describes one installed package directory: root/<cat>/<pf>/ with
// optional SLOT and repository files (nil means the file is absent).
type vdbEntry struct {
	cat, pf    string
	slot, repo *string
}

func str(s string) *string { return &s }

// writeVDB builds a /var/db/pkg-shaped tree under a temp dir and returns its
// root.
func writeVDB(t *testing.T, entries []vdbEntry) string {
	t.Helper()
	root := t.TempDir()
	for _, e := range entries {
		dir := filepath.Join(root, e.cat, e.pf)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if e.slot != nil {
			writeFile(t, filepath.Join(dir, "SLOT"), *e.slot)
		}
		if e.repo != nil {
			writeFile(t, filepath.Join(dir, "repository"), *e.repo)
		}
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byCP(pkgs []pkgdb.Package) map[string]pkgdb.Package {
	m := make(map[string]pkgdb.Package, len(pkgs))
	for _, p := range pkgs {
		m[p.Category+"/"+p.Name] = p
	}
	return m
}

// TestRead_SplitsPFAndReadsSlotAndRepository covers the ordinary layout plus
// every PF shape that a naive split gets wrong: a revision, a _p suffix, a
// live 9999 version, and package names that themselves contain -<digit>
// segments (the GOTCHA: split at the right-most "-" whose remainder is a
// version, never the first "-" followed by a digit).
func TestRead_SplitsPFAndReadsSlotAndRepository(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"dev-libs", "foo-1.2.3", str("0\n"), str("bentoo\n")},
		{"app-misc", "bar-2.0-r1", str("2/2.0\n"), str("gentoo\n")},
		{"media-fonts", "font-arial-1-2.0", str("0\n"), str("bentoo\n")},
		{"dev-util", "foo-2d-1.0_p20260101", str("0\n"), str("bentoo\n")},
		{"sys-apps", "baz-9999", str("0\n"), str("bentoo\n")},
		{"x11-libs", "gtk+-3.24.43-r2", str("3\n"), str("gentoo\n")},
		{"app-text", "bar-r1-1.0", str("0\n"), str("bentoo\n")},
	})

	pkgs, skipped, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d for a fully readable tree, want 0", skipped)
	}
	got := byCP(pkgs)
	want := []pkgdb.Package{
		{Category: "dev-libs", Name: "foo", Version: "1.2.3", Slot: "0", Repo: "bentoo"},
		{Category: "app-misc", Name: "bar", Version: "2.0-r1", Slot: "2", Repo: "gentoo"},
		{Category: "media-fonts", Name: "font-arial-1", Version: "2.0", Slot: "0", Repo: "bentoo"},
		{Category: "dev-util", Name: "foo-2d", Version: "1.0_p20260101", Slot: "0", Repo: "bentoo"},
		{Category: "sys-apps", Name: "baz", Version: "9999", Slot: "0", Repo: "bentoo"},
		{Category: "x11-libs", Name: "gtk+", Version: "3.24.43-r2", Slot: "3", Repo: "gentoo"},
		{Category: "app-text", Name: "bar-r1", Version: "1.0", Slot: "0", Repo: "bentoo"},
	}
	if len(pkgs) != len(want) {
		t.Errorf("Read returned %d packages, want %d: %+v", len(pkgs), len(want), pkgs)
	}
	for _, w := range want {
		g, ok := got[w.Category+"/"+w.Name]
		if !ok {
			t.Errorf("missing %s/%s (PF split wrongly?); got %+v", w.Category, w.Name, pkgs)
			continue
		}
		if g != w {
			t.Errorf("%s/%s = %+v, want %+v", w.Category, w.Name, g, w)
		}
	}
}

// TestRead_SlotKeepsOnlyThePartBeforeTheSubslot: SLOT holds "slot/subslot";
// only the slot is compared against a notice's slot.
func TestRead_SlotKeepsOnlyThePartBeforeTheSubslot(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"dev-lang", "rust-1.90.0", str("1.90.0/1.90\n"), str("bentoo\n")},
		{"dev-libs", "openssl-3.5.0", str("0/3\nignored second line\n"), str("bentoo\n")},
	})
	pkgs, _, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := byCP(pkgs)
	if s := got["dev-lang/rust"].Slot; s != "1.90.0" {
		t.Errorf("rust slot = %q, want 1.90.0", s)
	}
	if s := got["dev-libs/openssl"].Slot; s != "0" {
		t.Errorf("openssl slot = %q, want 0", s)
	}
}

// TestRead_MissingRepositoryRecordIsEmptyRepo is R5.6: a package without a
// repository file is reported with an empty Repo, never as "bentoo", and the
// rest of the tree still loads.
func TestRead_MissingRepositoryRecordIsEmptyRepo(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"app-portage", "bentoolkit-0.31.1", str("0\n"), nil},
		{"dev-libs", "foo-1.0", str("0\n"), str("bentoo\n")},
	})
	pkgs, _, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := byCP(pkgs)
	p, ok := got["app-portage/bentoolkit"]
	if !ok {
		t.Fatalf("a package without a repository file was dropped; got %+v", pkgs)
	}
	if p.Repo != "" {
		t.Errorf("Repo = %q for a missing repository record, want empty", p.Repo)
	}
	if got["dev-libs/foo"].Repo != "bentoo" {
		t.Errorf("the neighbour lost its repository: %+v", got["dev-libs/foo"])
	}
}

// TestRead_RepositoryIsTrimmed: the files end with a newline; "bentoo\n" is
// the repository "bentoo", and a second line is not part of it.
func TestRead_RepositoryIsTrimmed(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"dev-libs", "foo-1.0", str("0"), str("bentoo\nsomething else\n")},
	})
	pkgs, _, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Repo != "bentoo" || pkgs[0].Slot != "0" {
		t.Errorf("Read = %+v, want one package with Repo bentoo and Slot 0", pkgs)
	}
}

// TestRead_SkipsEntriesThatAreNotPackages: stray files and directories whose
// name has no version part are skipped, never fatal and never reported as a
// package.
func TestRead_SkipsEntriesThatAreNotPackages(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"dev-libs", "foo-1.0", str("0\n"), str("bentoo\n")},
		{"dev-libs", "notaversion", str("0\n"), str("bentoo\n")},
		{"dev-libs", "trailing-", str("0\n"), str("bentoo\n")},
	})
	writeFile(t, filepath.Join(root, "stray-file"), "x")
	writeFile(t, filepath.Join(root, "dev-libs", "stray-file-1.0"), "x")

	pkgs, _, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "foo" {
		t.Errorf("Read = %+v, want only dev-libs/foo", pkgs)
	}
}

// TestRead_UnreadableEntryIsSkipped: one unreadable package directory must not
// hide every other installed package.
func TestRead_UnreadableEntryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory; the unreadable case cannot be built")
	}
	root := writeVDB(t, []vdbEntry{
		{"dev-libs", "foo-1.0", str("0\n"), str("bentoo\n")},
		{"dev-libs", "locked-2.0", str("0\n"), str("bentoo\n")},
		{"net-misc", "curl-8.0", str("0\n"), str("gentoo\n")},
	})
	locked := filepath.Join(root, "dev-libs", "locked-2.0")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	pkgs, skipped, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatalf("Read failed on one unreadable entry: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the unreadable entry is counted so the caller can WARN)", skipped)
	}
	got := byCP(pkgs)
	if _, ok := got["dev-libs/foo"]; !ok {
		t.Errorf("dev-libs/foo missing; got %+v", pkgs)
	}
	if _, ok := got["net-misc/curl"]; !ok {
		t.Errorf("net-misc/curl missing; got %+v", pkgs)
	}
	if p, ok := got["dev-libs/locked"]; ok && p.Repo == "bentoo" {
		t.Errorf("an unreadable entry was reported as installed from bentoo: %+v", p)
	}
}

// TestRead_UnreadableRootIsAnErrorNamingIt is R5.5's trigger: when the
// database itself cannot be read the caller must know, and the error names the
// path so the WARN can.
func TestRead_UnreadableRootIsAnErrorNamingIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-vdb")
	pkgs, _, err := pkgdb.Read(context.Background(), missing)
	if err == nil {
		t.Fatalf("Read of a missing root returned %+v and no error", pkgs)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the root %q", err, missing)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v does not wrap the cause (want errors.Is os.ErrNotExist)", err)
	}
}

// TestRead_ReaderInstalledDelegates: pkgdb.Reader{Root} is the App's
// InstalledReader and answers exactly what Read answers.
func TestRead_ReaderInstalledDelegates(t *testing.T) {
	root := writeVDB(t, []vdbEntry{
		{"dev-libs", "foo-1.0", str("0\n"), str("bentoo\n")},
		{"net-misc", "curl-8.0", str("0\n"), str("gentoo\n")},
	})
	want, wantSkipped, err := pkgdb.Read(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got, gotSkipped, err := pkgdb.Reader{Root: root}.Installed(context.Background())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if !slices.Equal(got, want) || gotSkipped != wantSkipped || len(got) != 2 {
		t.Errorf("Installed = %+v (%d skipped), want Read's %+v (%d skipped)", got, gotSkipped, want, wantSkipped)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, _, err := (pkgdb.Reader{Root: missing}).Installed(context.Background()); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("Installed on a missing root: err = %v, want one naming %s", err, missing)
	}
}

// TestRead_HonoursCancellation: a cancelled context stops the walk with the
// context's error rather than returning a partial list as if complete.
func TestRead_HonoursCancellation(t *testing.T) {
	root := writeVDB(t, []vdbEntry{{"dev-libs", "foo-1.0", str("0\n"), str("bentoo\n")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := pkgdb.Read(ctx, root); !errors.Is(err, context.Canceled) {
		t.Errorf("Read with a cancelled context: err = %v, want context.Canceled", err)
	}
}

// FuzzSplitPF drives the PF split through Read with arbitrary directory names.
// Whatever Read reports must reassemble to the directory name it came from,
// carry a valid version, and never panic.
func FuzzSplitPF(f *testing.F) {
	for _, seed := range []string{
		"foo-1.2.3", "bar-2.0-r1", "font-arial-1-2.0", "foo-2d-1.0_p20260101",
		"baz-9999", "gtk+-3.24.43-r2", "bar-r1-1.0", "notaversion", "trailing-",
		"-1.0", "a-1-r", "x-1.0_alpha_beta2_rc3_p4-r5", "a--1", "1-1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pf string) {
		if pf == "" || pf == "." || pf == ".." || len(pf) > 200 ||
			strings.ContainsAny(pf, "/\x00") || !isPrintableASCII(pf) {
			t.Skip()
		}
		root := t.TempDir()
		dir := filepath.Join(root, "cat", pf)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Skip()
		}
		writeFile(t, filepath.Join(dir, "SLOT"), "0\n")
		writeFile(t, filepath.Join(dir, "repository"), "bentoo\n")

		pkgs, _, err := pkgdb.Read(context.Background(), root)
		if err != nil {
			t.Fatalf("Read(%q): %v", pf, err)
		}
		if len(pkgs) > 1 {
			t.Fatalf("one directory %q produced %d packages: %+v", pf, len(pkgs), pkgs)
		}
		for _, p := range pkgs {
			if p.Category != "cat" {
				t.Errorf("category = %q, want cat", p.Category)
			}
			if p.Name == "" {
				t.Errorf("PF %q split into an empty name: %+v", pf, p)
			}
			if p.Name+"-"+p.Version != pf {
				t.Errorf("PF %q split into name %q + version %q, which does not reassemble", pf, p.Name, p.Version)
			}
			if !ebuild.IsValidVersion(p.Version) {
				t.Errorf("PF %q yielded invalid version %q", pf, p.Version)
			}
		}
		if slices.ContainsFunc(pkgs, func(p pkgdb.Package) bool { return p.Repo != "bentoo" }) {
			t.Errorf("repository lost for %q: %+v", pf, pkgs)
		}
	})
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
