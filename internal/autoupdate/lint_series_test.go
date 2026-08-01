package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLintOverlay lays out an overlay with a packages.toml holding one record
// per entry and the given ebuild filenames per package directory.
func writeLintOverlay(t *testing.T, records string, ebuilds map[string][]string) string {
	t.Helper()
	root := t.TempDir()

	autoDir := filepath.Join(root, ".autoupdate")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "packages.toml"), []byte(records), 0o600); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}

	for pkg, names := range ebuilds {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for _, n := range names {
			body := "EAPI=8\nDESCRIPTION=\"t\"\nSLOT=\"0\"\nKEYWORDS=\"amd64\"\n"
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o600); err != nil {
				t.Fatalf("write ebuild: %v", err)
			}
		}
	}
	return root
}

// gstRecord is a minimal valid record for a GitLab tag-tracked package.
const gstRecord = `["media-libs/gstreamer"]
url = "https://gitlab.freedesktop.org/api/v4/projects/gstreamer%2Fgstreamer/repository/tags"
parser = "regex"
pattern = '"name":"([0-9]+\.[0-9]+\.[0-9]+)"'
comments = """
gstreamer — test record.
"""
# END
`

func findIssue(issues []LintIssue, rule string) *LintIssue {
	for i := range issues {
		if issues[i].Rule == rule {
			return &issues[i]
		}
	}
	return nil
}

func TestLintMissingSeries_TwoReleaseLines(t *testing.T) {
	// The shape this rule exists for: a stable line beside a pre-release one,
	// with nothing saying which the entry tracks. 1.28.5 can never be picked up
	// again because 1.28.5 < 1.29.2_pre.
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.28.5.ebuild", "gstreamer-1.29.2_pre.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}

	got := findIssue(issues, LintMissingSeries)
	if got == nil {
		t.Fatalf("expected a %s issue, got %v", LintMissingSeries, issues)
	}
	if got.Package != "media-libs/gstreamer" {
		t.Errorf("Package = %q, want media-libs/gstreamer", got.Package)
	}
	for _, want := range []string{"1.28.5", "1.29.2_pre", "series"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message %q should mention %q", got.Message, want)
		}
	}
}

func TestLintMissingSeries_SameLineIsQuiet(t *testing.T) {
	// Keeping the previous version of the SAME line around is ordinary overlay
	// hygiene, not a tracking mistake. Reporting it would bury the real finding.
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.28.4.ebuild", "gstreamer-1.28.5-r1.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("unexpected issue for two revisions of one line: %s", got)
	}
}

func TestLintMissingSeries_SnapshotSuffixesAreOneLine(t *testing.T) {
	// 1.29.2 and 1.29.2_pre are the same line; so are build-numbered snapshots.
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.29.2.ebuild", "gstreamer-1.29.2_pre20260731.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("unexpected issue for a version and its _pre snapshot: %s", got)
	}
}

func TestLintMissingSeries_SeriesSilencesIt(t *testing.T) {
	// Declaring the line is the fix, so the warning must disappear once it is.
	record := strings.Replace(gstRecord,
		`pattern = '"name":"([0-9]+\.[0-9]+\.[0-9]+)"'`,
		`pattern = '"name":"([0-9]+\.[0-9]+\.[0-9]+)"'`+"\n"+`series = '^1\.[0-9]*[02468]\.'`, 1)

	root := writeLintOverlay(t, record, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.28.5.ebuild", "gstreamer-1.29.2_pre.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("series is declared; unexpected issue: %s", got)
	}
}

func TestLintMissingSeries_SuccessiveVersionsAreQuiet(t *testing.T) {
	// bentoolkit's own directory: 0.15.3 beside 0.16.0 is one line mid-rotation,
	// not two maintained in parallel. Neither carries a pre-release suffix, so
	// there is no stable/unstable pair to declare.
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-0.15.3.ebuild", "gstreamer-0.16.0.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("successive versions of one line; unexpected issue: %s", got)
	}
}

func TestLintMissingSeries_TwoSnapshotLinesAreQuiet(t *testing.T) {
	// Two _p lines are two snapshot lines, not a stable/unstable pair: _p orders
	// AFTER its base and says nothing about stability.
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.4.357_p20260722.ebuild", "gstreamer-1.5.358_p20260731.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("two _p snapshot lines; unexpected issue: %s", got)
	}
}

func TestLintMissingSeries_DisabledEntryIsQuiet(t *testing.T) {
	record := strings.Replace(gstRecord, `["media-libs/gstreamer"]`,
		`["media-libs/gstreamer"]`+"\n"+`enabled = false`, 1)

	root := writeLintOverlay(t, record, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.28.5.ebuild", "gstreamer-1.29.2.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("disabled entry tracks nothing; unexpected issue: %s", got)
	}
}

func TestLintMissingSeries_SingleEbuildIsQuiet(t *testing.T) {
	root := writeLintOverlay(t, gstRecord, map[string][]string{
		"media-libs/gstreamer": {"gstreamer-1.29.2.ebuild"},
	})

	issues, err := LintPackagesConfig(root)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if got := findIssue(issues, LintMissingSeries); got != nil {
		t.Errorf("unexpected issue for a single ebuild: %s", got)
	}
}

func TestReleaseLineOf(t *testing.T) {
	tests := []struct{ version, want string }{
		{"1.28.5", "1.28"},
		{"1.28.5-r1", "1.28"},
		{"1.29.2_pre20260731", "1.29"},
		{"26.2.0_pre20260731", "26.2"},
		{"0_pre10202", "0"},
		{"0_pre10216", "0"},
		{"3.13.99_p20260729", "3.13"},
	}
	for _, tt := range tests {
		if got := releaseLineOf(tt.version); got != tt.want {
			t.Errorf("releaseLineOf(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}
