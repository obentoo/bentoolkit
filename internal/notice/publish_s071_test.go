package notice

// Story 071, sub-task 5.1: Publish checks every destination before writing,
// creates the news item and the site file without clobbering anything, and
// removes the first when the second fails (R1.7, R1.8, R3.1, R3.7, R4.2 to
// R4.5).
//
// Result is assumed to carry Paths (every file written), YAML (the site
// document, printed by the command when no site_path is set) and Warnings
// (the renderer's).
//
// Hostile halves first, for both "an item with this ID already exists" rules:
//   - wrongly collapse: IDs that merely share a prefix or a name with the new
//     one (`<date>-foo`, `<date>-foo-cve2`, `<other date>-foo-cve`) are
//     different notices and must not block it;
//   - wrongly split: an item directory for the SAME ID that holds only another
//     language's file is still that ID, and must block it.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func publishNoticeS071() Notice {
	pub := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return Notice{
		ID: "2026-10-02-foo-cve", Type: "security", Severity: "critical",
		Title: "foo 1.2 heap overflow", Summary: "Summary.", Author: "Jane Doe <jane@example.org>",
		Body:      "Upgrade now.",
		Affects:   []Affects{{CP: "dev-libs/foo", Slot: "1", Ranges: []Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.2.3"}}}},
		Published: pub, Updated: pub, Revision: 1,
	}
}

func publishTreesS071(t *testing.T) (overlay, site string) {
	t.Helper()
	overlay = filepath.Join(t.TempDir(), "overlay")
	site = filepath.Join(t.TempDir(), "site")
	for _, d := range []string{filepath.Join(overlay, "metadata"), filepath.Join(overlay, "profiles"), filepath.Join(site, "src", "content", "notices")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return overlay, site
}

func newsPathS071(overlay, id string) string {
	return filepath.Join(overlay, "metadata", "news", id, id+".en.txt")
}

func sitePathS071(site, id string) string {
	return filepath.Join(site, "src", "content", "notices", id+".yaml")
}

func mustWriteS071(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertAbsentS071(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (%v): %s", path, err, why)
	}
}

func assertContentS071(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("reading %s: %v", path, err)
		return
	}
	if string(b) != want {
		t.Errorf("%s was modified: got %q, want %q", path, b, want)
	}
}

func TestPublish_WritesBothFilesUnderOneID(t *testing.T) {
	overlay, site := publishTreesS071(t)
	n := publishNoticeS071()

	res, err := Publish(context.Background(), n, overlay, site)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	newsPath, yamlPath := newsPathS071(overlay, n.ID), sitePathS071(site, n.ID)

	wantNews, wantWarnings := RenderNews(n)
	assertContentS071(t, newsPath, wantNews)
	wantYAML, err := RenderSiteYAML(n)
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	assertContentS071(t, yamlPath, string(wantYAML))

	if fi, err := os.Stat(newsPath); err == nil && fi.Mode().Perm() != 0o644 {
		t.Errorf("news item mode = %v, want 0644", fi.Mode().Perm())
	}
	if fi, err := os.Stat(filepath.Dir(newsPath)); err == nil && fi.Mode().Perm() != 0o755 {
		t.Errorf("news item directory mode = %v, want 0755", fi.Mode().Perm())
	}
	if fi, err := os.Stat(yamlPath); err == nil && fi.Mode().Perm() != 0o644 {
		t.Errorf("site file mode = %v, want 0644", fi.Mode().Perm())
	}

	if !slices.Contains(res.Paths, newsPath) || !slices.Contains(res.Paths, yamlPath) || len(res.Paths) != 2 {
		t.Errorf("Result.Paths = %q, want exactly %s and %s", res.Paths, newsPath, yamlPath)
	}
	if len(res.Warnings) != len(wantWarnings) || len(res.Warnings) == 0 {
		t.Errorf("Result.Warnings = %q, want the renderer's %q", res.Warnings, wantWarnings)
	}
}

func TestPublish_NeighbouringIDsDoNotBlock(t *testing.T) {
	overlay, site := publishTreesS071(t)
	n := publishNoticeS071()
	for _, other := range []string{"2026-10-02-foo", "2026-10-02-foo-cve2", "2026-10-01-foo-cve"} {
		mustWriteS071(t, newsPathS071(overlay, other), "Title: other\n")
		mustWriteS071(t, sitePathS071(site, other), "id: "+other+"\n")
	}

	if _, err := Publish(context.Background(), n, overlay, site); err != nil {
		t.Fatalf("a notice whose ID merely resembles existing ones was refused: %v", err)
	}
	for _, other := range []string{"2026-10-02-foo", "2026-10-02-foo-cve2", "2026-10-01-foo-cve"} {
		assertContentS071(t, newsPathS071(overlay, other), "Title: other\n")
	}
}

func TestPublish_RefusesAnIDTheOverlayAlreadyHas(t *testing.T) {
	cases := map[string]string{
		// wrongly split: same ID, only another language's file
		"another language only": "2026-10-02-foo-cve.de.txt",
		// benign
		"the English item": "2026-10-02-foo-cve.en.txt",
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			overlay, site := publishTreesS071(t)
			n := publishNoticeS071()
			existing := filepath.Join(overlay, "metadata", "news", n.ID, file)
			mustWriteS071(t, existing, "Title: already here\n")

			_, err := Publish(context.Background(), n, overlay, site)
			if !errors.Is(err, ErrNewsExists) {
				t.Fatalf("err = %v, want ErrNewsExists", err)
			}
			if !strings.Contains(err.Error(), filepath.Join(overlay, "metadata", "news", n.ID)) {
				t.Errorf("the error %q does not name the existing item", err)
			}
			assertContentS071(t, existing, "Title: already here\n")
			assertAbsentS071(t, sitePathS071(site, n.ID), "no file may be written when the ID is taken (R1.8)")
		})
	}
}

func TestPublish_SiteDirectoryMustExist(t *testing.T) {
	overlay, _ := publishTreesS071(t)
	site := filepath.Join(t.TempDir(), "not-a-site")
	if err := os.MkdirAll(filepath.Join(site, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := publishNoticeS071()

	_, err := Publish(context.Background(), n, overlay, site)
	if !errors.Is(err, ErrSiteDirMissing) {
		t.Fatalf("err = %v, want ErrSiteDirMissing", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(site, "src", "content", "notices")) {
		t.Errorf("the error %q does not name the missing directory", err)
	}
	assertAbsentS071(t, filepath.Join(overlay, "metadata", "news", n.ID), "nothing may be written before the pre-checks pass")
	assertAbsentS071(t, filepath.Join(site, "src", "content"), "the site tree must not be created")
}

func TestPublish_RefusesAnExistingSiteFile(t *testing.T) {
	overlay, site := publishTreesS071(t)
	n := publishNoticeS071()
	mustWriteS071(t, sitePathS071(site, n.ID), "id: hand-written\n")

	_, err := Publish(context.Background(), n, overlay, site)
	if !errors.Is(err, ErrSiteExists) {
		t.Fatalf("err = %v, want ErrSiteExists", err)
	}
	if !strings.Contains(err.Error(), sitePathS071(site, n.ID)) {
		t.Errorf("the error %q does not name the site file", err)
	}
	assertContentS071(t, sitePathS071(site, n.ID), "id: hand-written\n")
	assertAbsentS071(t, filepath.Join(overlay, "metadata", "news", n.ID), "the news item must not be written when the site file is taken")
}

func TestPublish_WithoutASitePathWritesOnlyTheItem(t *testing.T) {
	overlay, _ := publishTreesS071(t)
	n := publishNoticeS071()
	cwd := t.TempDir()
	t.Chdir(cwd)

	res, err := Publish(context.Background(), n, overlay, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(res.Paths) != 1 || res.Paths[0] != newsPathS071(overlay, n.ID) {
		t.Errorf("Result.Paths = %q, want only the news item", res.Paths)
	}
	want, err := RenderSiteYAML(n)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.YAML) != string(want) {
		t.Errorf("Result.YAML = %q, want the rendered site document for printing", res.YAML)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("Publish wrote into the working directory: %v", entries)
	}
}

// R4.5: the second write fails after the first succeeded. The first is
// removed, both are reported, and — the observable proof that the rollback is
// complete — the same publish succeeds once the cause is fixed.
func TestPublish_RollsBackTheItemWhenTheSiteWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	overlay, site := publishTreesS071(t)
	n := publishNoticeS071()
	notices := filepath.Join(site, "src", "content", "notices")
	if err := os.Chmod(notices, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(notices, 0o755) })

	_, err := Publish(context.Background(), n, overlay, site)
	if err == nil {
		t.Fatal("Publish succeeded although the site directory is read-only")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the write error is not wrapped: %v", err)
	}
	for _, p := range []string{sitePathS071(site, n.ID), newsPathS071(overlay, n.ID)} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("the error %q does not name %s (write error and rollback both reported)", err, p)
		}
	}
	assertAbsentS071(t, newsPathS071(overlay, n.ID), "the first file must be rolled back")

	if err := os.Chmod(notices, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), n, overlay, site); err != nil {
		t.Errorf("after the rollback, retrying the same notice failed: %v", err)
	}
}

func TestPublish_RefusesAnIDThatLeavesTheTree(t *testing.T) {
	overlay, site := publishTreesS071(t)
	n := publishNoticeS071()
	n.ID = "../../escaped"

	if _, err := Publish(context.Background(), n, overlay, site); err == nil {
		t.Error("Publish accepted an ID that points outside metadata/news")
	}
	assertAbsentS071(t, filepath.Join(overlay, "escaped"), "a file escaped the news tree")
	assertAbsentS071(t, filepath.Join(filepath.Dir(overlay), "escaped"), "a file escaped the overlay")
	assertAbsentS071(t, filepath.Join(site, "src", "escaped.yaml"), "a file escaped the notices directory")
}
