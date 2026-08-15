// MERGE FRAGMENT — story 034, sub-task 2.1 (`CompareAxes`).
//
// Target file: internal/overlay/axes_test.go  (NEW file, package overlay)
// Materialise by creating that file with this content verbatim. Nothing here
// redeclares a helper the package's existing test files already own; the only
// borrowed symbol is `writeVerifyEbuild` from compare_verification_test.go, and
// it is borrowed rather than copied on purpose — a second copy of the ebuild
// writer is a second thing to drift.
//
// PINNED CONTRACT (design.md "Components & Interfaces"):
//
//	type AxisFinding struct{ Axis, Detail string }   // inherit | options | iuse | deps
//	func CompareAxes(ourPath, baselinePath string) ([]AxisFinding, error)
//
// `ourPath` and `baselinePath` are paths to the two EBUILD FILES, not to their
// package directories: the axes are properties of an ebuild's text, and a
// directory would force the callee to guess which version it was asked about.
//
// # WHY THESE ASSERTIONS AND NOT OTHERS
//
// D4 and measurement M-D say the whole of issue #33 sits in two lines: ::gentoo's
// gst-plugins-qt6-1.26.11 is `inherit gstreamer-meson` and passes 2 `-D` options,
// ours passes 85 and inherits `meson` directly. That is a text comparison and a
// count, so it must answer with NO model, NO provider and NO network — which is
// also what makes `--no-review` useful on its own. Every case below therefore
// runs with PATH pointed at an empty directory: a model call needs the `claude`
// binary, and with PATH empty there is none to find. A finding that vanished
// under that condition would be a finding that quietly depended on a provider.
//
// The four axes are asserted separately rather than through one "the fixture
// produces four findings" count, because a count passes for the wrong reason the
// moment two axes are collapsed into one finding.
package overlay

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// axisNoModelOnPath empties PATH for the duration of the test, so nothing this
// package could shell out to — `claude` above all — is reachable. It is the
// mechanical form of "every case runs with no model wired": the assertion is not
// that we chose not to call a model, but that a call would have failed and the
// findings arrived anyway.
func axisNoModelOnPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// axisGstBaseline is ::gentoo's gst-plugins-qt6-1.26.11, reduced to the two
// lines M-D measured: it delegates the option list to an eclass and passes two
// options of its own.
const axisGstBaseline = `EAPI=8
inherit gstreamer-meson

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml"
RDEPEND=">=dev-qt/qtbase-6.7.0:6"

src_configure() {
	local emesonargs=(
		-Dqt6=enabled
		-Dexamples=disabled
	)
	meson_src_configure
}
`

// axisGstOurs is our 1.29.2: the eclass is gone, three eclasses replace it, and
// the option list is hardcoded. The 85 is generated rather than typed so the
// count in the assertion and the count in the fixture cannot drift apart.
func axisGstOurs() string {
	var opts strings.Builder
	for i := 1; i <= 85; i++ {
		fmt.Fprintf(&opts, "\t\t-Dopt%02d=enabled\n", i)
	}
	return `EAPI=8
inherit meson python-any-r1 xdg-utils

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml"
RDEPEND=">=dev-qt/qtbase-6.7.0:6"

src_configure() {
	local emesonargs=(
` + opts.String() + `	)
	meson_src_configure
}
`
}

// axisWriteEbuild drops one ebuild body in its own directory under root and
// returns its path. Two ebuilds never share a directory here: CompareAxes is
// given two file paths, and a shared directory would let a bug that reads the
// wrong file still pass.
func axisWriteEbuild(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	writeVerifyEbuild(t, dir, "media-libs", "gst-plugins-qt6", "1.0.0", body)
	return filepath.Join(dir, "media-libs", "gst-plugins-qt6", "gst-plugins-qt6-1.0.0.ebuild")
}

// axisFindingsFor returns the findings on one axis. Selecting by axis rather
// than by index keeps the assertions independent of the order findings are
// emitted in, which nothing in the story fixes.
func axisFindingsFor(findings []AxisFinding, axis string) []AxisFinding {
	var out []AxisFinding
	for _, f := range findings {
		if f.Axis == axis {
			out = append(out, f)
		}
	}
	return out
}

// TestCompareAxes covers the four axes of D4 on the two shapes that matter: the
// measured gst pair, and the single-axis edits that must each produce exactly
// one finding.
//
// _Requirements: R2, R2.4_
func TestCompareAxes(t *testing.T) {
	t.Run("the gst pair names the eclass and the option counts", func(t *testing.T) {
		axisNoModelOnPath(t)
		root := t.TempDir()
		ours := axisWriteEbuild(t, root, "ours", axisGstOurs())
		baseline := axisWriteEbuild(t, root, "baseline", axisGstBaseline)

		findings, err := CompareAxes(ours, baseline)
		if err != nil {
			t.Fatalf("CompareAxes returned %v, want nil — the axes read two files and nothing else", err)
		}

		inherit := axisFindingsFor(findings, "inherit")
		if len(inherit) != 1 {
			t.Fatalf("inherit findings: %d, want 1 — this is the finding that catches issue #33: %+v", len(inherit), findings)
		}
		// The eclass ::gentoo delegates to and we dropped. Naming it is the
		// whole value of the finding: "the inherit lines differ" would not have
		// told anyone which 85 options they were now maintaining by hand.
		if !strings.Contains(inherit[0].Detail, "gstreamer-meson") {
			t.Errorf("inherit finding detail is %q, want it to name gstreamer-meson", inherit[0].Detail)
		}

		options := axisFindingsFor(findings, "options")
		if len(options) != 1 {
			t.Fatalf("options findings: %d, want 1: %+v", len(options), findings)
		}
		for _, want := range []string{"85", "2"} {
			if !strings.Contains(options[0].Detail, want) {
				t.Errorf("options finding detail is %q, want it to name %s — 85 versus 2 is the finding", options[0].Detail, want)
			}
		}
	})

	t.Run("identical ebuilds yield no finding", func(t *testing.T) {
		axisNoModelOnPath(t)
		root := t.TempDir()
		ours := axisWriteEbuild(t, root, "ours", axisGstBaseline)
		baseline := axisWriteEbuild(t, root, "baseline", axisGstBaseline)

		findings, err := CompareAxes(ours, baseline)
		if err != nil {
			t.Fatalf("CompareAxes returned %v, want nil", err)
		}
		if len(findings) != 0 {
			t.Errorf("identical ebuilds produced %d findings, want 0: %+v", len(findings), findings)
		}
	})

	// One edit per case, so a finding can only come from the axis under test.
	// Table-driven because the four axes differ in the line they touch and in
	// nothing else.
	singleEdit := []struct {
		name     string
		ourBody  string
		axis     string
		mustName string
	}{
		{
			name:     "an IUSE flag we added",
			ourBody:  strings.Replace(axisGstBaseline, `IUSE="qml"`, `IUSE="qml wayland"`, 1),
			axis:     "iuse",
			mustName: "wayland",
		},
		{
			// Ours is LOWER than the baseline's, which D4 records as usually
			// meaning we fell behind — the direction is the finding, not the
			// bare fact that the atoms differ.
			name:     "a dependency atom lower than the baseline's",
			ourBody:  strings.Replace(axisGstBaseline, ">=dev-qt/qtbase-6.7.0:6", ">=dev-qt/qtbase-6.5.0:6", 1),
			axis:     "deps",
			mustName: "dev-qt/qtbase",
		},
	}

	for _, tc := range singleEdit {
		t.Run(tc.name, func(t *testing.T) {
			axisNoModelOnPath(t)
			root := t.TempDir()
			ours := axisWriteEbuild(t, root, "ours", tc.ourBody)
			baseline := axisWriteEbuild(t, root, "baseline", axisGstBaseline)

			findings, err := CompareAxes(ours, baseline)
			if err != nil {
				t.Fatalf("CompareAxes returned %v, want nil", err)
			}
			got := axisFindingsFor(findings, tc.axis)
			if len(got) != 1 {
				t.Fatalf("%s findings: %d, want exactly 1: %+v", tc.axis, len(got), findings)
			}
			if !strings.Contains(got[0].Detail, tc.mustName) {
				t.Errorf("%s finding detail is %q, want it to name %s", tc.axis, got[0].Detail, tc.mustName)
			}
			// The edit touched one line; anything else reporting is a finding
			// invented out of a difference nobody made.
			if len(findings) != 1 {
				t.Errorf("a single-axis edit produced %d findings, want 1: %+v", len(findings), findings)
			}
		})
	}
}

// TestCompareAxesSurvivesNoReview is the standing-rule assertion stated on its
// own, because it is the one the quality gate names: the inherit finding is
// produced with no model wired.
//
// It is not a duplicate of the gst case above. That case asserts WHAT the
// finding says; this one asserts that the finding exists on the path where the
// operator has deliberately turned every model off — the path `--no-review`
// selects, and the path a machine with no `claude` installed is always on.
//
// _Requirements: R2, R2.4_
func TestCompareAxesSurvivesNoReview(t *testing.T) {
	axisNoModelOnPath(t)
	// A provider is not merely absent, it is not a parameter: CompareAxes takes
	// two paths. If this signature ever grows a provider or a reviewer, this
	// test stops compiling, which is the intended alarm.
	root := t.TempDir()
	ours := axisWriteEbuild(t, root, "ours", axisGstOurs())
	baseline := axisWriteEbuild(t, root, "baseline", axisGstBaseline)

	findings, err := CompareAxes(ours, baseline)
	if err != nil {
		t.Fatalf("CompareAxes returned %v, want nil with no model reachable", err)
	}
	if len(axisFindingsFor(findings, "inherit")) == 0 {
		t.Fatal("no inherit finding with PATH empty — the signal that catches issue #33 must not depend on a provider being configured")
	}
}
