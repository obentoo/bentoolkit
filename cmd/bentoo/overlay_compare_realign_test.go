package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests close gaps the sub-task's own fragment leaves open, and they live
// in a file of their own so that fragment stays exactly as it was authored. Both
// were found by MUTATION rather than by reading: each was written after breaking
// production in a specific way and watching the whole suite stay green.
//
// They borrow realignWriteEbuild / realignFlags / realignRun from
// overlay_compare_test.go rather than re-declaring them.

// realignThreeWayFixture builds the one shape that tells R6.4's two candidate
// denominators apart: a package that is COMPARED and then left out of the view.
//
// Three packages, one per role. gst-plugins-qt6 is carried at our exact version,
// so it is up-to-date and `--only-outdated` excludes it from the rows while the
// comparison still counts it. brotli is carried at a NEWER version, so it is
// outdated and stays — which also keeps a package ::gentoo carries inside the
// result set, the only way AnnotateBaseline can find the tree at all
// (baselineTreeOf walks the results and asks the provider about each). zed is
// carried by nobody, and is the package the coverage line counts.
//
// It returns the overlay root. Everything is `provider: local` on a t.TempDir()
// and PATH is emptied, so nothing in the run leaves the machine.
func realignThreeWayFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	overlayPath := filepath.Join(home, "overlay")
	gentooPath := filepath.Join(home, "gentoo")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayPath, sub), 0o750); err != nil {
			t.Fatalf("mkdir overlay/%s: %v", sub, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(gentooPath, "profiles"), 0o750); err != nil {
		t.Fatalf("mkdir gentoo/profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gentooPath, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}

	const body = "EAPI=8\nDESCRIPTION=\"fixture\"\n"
	// Compared and EXCLUDED from the view by --only-outdated.
	realignWriteEbuild(t, overlayPath, "media-libs", "gst-plugins-qt6", "1.29.2", body)
	realignWriteEbuild(t, gentooPath, "media-libs", "gst-plugins-qt6", "1.29.2", body)
	// Compared, outdated, and KEPT — which is also what lets the review locate
	// the ::gentoo tree through the provider.
	realignWriteEbuild(t, overlayPath, "app-arch", "brotli", "1.0.0", body)
	realignWriteEbuild(t, gentooPath, "app-arch", "brotli", "1.1.0", body)
	// Carried by nobody: the package the coverage line counts.
	realignWriteEbuild(t, overlayPath, "app-editors", "zed", "1.0.0", body)

	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfg := "overlay:\n  path: " + overlayPath + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		"repositories:\n" +
		"  gentoo:\n    provider: local\n    path: " + gentooPath + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", t.TempDir())
	return overlayPath
}

// TestRealignBaselineShareCountsPackagesComparedNotRows pins the DENOMINATOR of
// R6.4's coverage line: it is the number of packages COMPARED, never the number
// of rows the report happens to print.
//
// The two are equal on a default review run — every compared package has a row —
// so any code that confused them would pass unnoticed. `--only-outdated` is what
// separates them: it selects on Status inside the comparison, so the up-to-date
// package is compared and then left out of the view, and the run reaches this
// line having compared three packages and printed two rows. A denominator that
// followed the view would read "1 of the 2": a report claiming it examined
// everything it was able to, which is exactly the sentence R6.4 exists to make
// checkable.
//
// It is asserted through the rendered bytes rather than through the report,
// because a run never hands its report back: the three summary lines go to
// stderr through logger, which binds its writer at first use, and this line is
// the only place the count reaches stdout where a test can read it.
//
// _Requirements: R6, R6.4_
func TestRealignBaselineShareCountsPackagesComparedNotRows(t *testing.T) {
	realignThreeWayFixture(t)
	realignFlags(t, true, false)

	// Saved and restored here rather than in realignFlags: that helper belongs to
	// the frozen fragment, and this is the only case that touches this flag.
	origOnlyOutdated := compareOnlyOutdated
	t.Cleanup(func() { compareOnlyOutdated = origOnlyOutdated })
	compareOnlyOutdated = true

	got, code := realignRun(t, nil)

	if code != 0 {
		t.Errorf("exit code is %d, want 0 — narrowing the view is not a failure", code)
	}
	const want = "1 of the 3 packages compared"
	if !strings.Contains(got, want) {
		t.Errorf("the coverage line does not read %q; R6.4 asks for the count as a share of the packages EXAMINED, and a denominator taken from the rows on screen would shrink with every filter.\nreport:\n%s", want, got)
	}
}

// TestRealignOtherRepositoriesOnlyWhereNoBaselineExists pins the gate on the
// informative rows: they are produced WHERE no ::gentoo baseline exists (R6.1)
// and nowhere else.
//
// Both halves are asserted, and the second is the one that can rot. A row saying
// another repository also carries a package we already measured against ::gentoo
// answers a question nobody asked, and it would print directly beneath that
// package's own baseline line contradicting its "never a baseline" caveat. The
// fragment's fixture is built for exactly this: ::gentoo carries gst-plugins-qt6
// and does not carry zed, while the configured ::guru tree is the same directory
// — so a gate keyed on anything but the baseline lights up the wrong row.
//
// _Requirements: R6, R6.1, R6.2_
func TestRealignOtherRepositoriesOnlyWhereNoBaselineExists(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, true, false)

	got, code := realignRun(t, nil)

	if code != 0 {
		t.Fatalf("exit code is %d, want 0", code)
	}
	if !strings.Contains(got, "app-editors/zed: ::guru") {
		t.Errorf("no other-repository row for app-editors/zed, which ::gentoo does not carry; R6.1 is what those 84 packages get instead of a baseline.\nreport:\n%s", got)
	}
	if strings.Contains(got, "gst-plugins-qt6: ::guru") {
		t.Errorf("media-libs/gst-plugins-qt6 has an other-repository row although ::gentoo carries it; R6.1 scopes those rows to packages with NO baseline, and one printed here sits under the baseline line contradicting it.\nreport:\n%s", got)
	}
}
