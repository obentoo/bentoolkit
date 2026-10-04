package autoupdate

import (
	"strings"
	"testing"
)

// Regression guards for the tech review of story 079, group 4.

// TestRequiresReviewDigestCoversRequirements: a staged tree may be promoted
// as it stands only when its substitutions are unchanged, and the requirement
// pins are substitutions. Declaring requires, changing the pin or capturing
// another version must each change the digest.
func TestRequiresReviewDigestCoversRequirements(t *testing.T) {
	spec := func(pin string) map[string]RequireSpec {
		return map[string]RequireSpec{"dev-lang/dart": {Pattern: `x([0-9.]+)`, Pin: pin}}
	}
	upd := func(v string) *PendingUpdate {
		return &PendingUpdate{Requires: map[string]string{"dev-lang/dart": v}}
	}
	base := substitutionDigest(PackageConfig{Requires: spec("~")}, upd("3.14.0"))
	if base == "" {
		t.Fatal("a bump with a requirement digests to nothing")
	}
	for name, got := range map[string]string{
		"requires not declared":  substitutionDigest(PackageConfig{}, &PendingUpdate{}),
		"pin changed":            substitutionDigest(PackageConfig{Requires: spec(">=")}, upd("3.14.0")),
		"version changed":        substitutionDigest(PackageConfig{Requires: spec("~")}, upd("3.15.0")),
		"declared, not captured": substitutionDigest(PackageConfig{Requires: spec("~")}, &PendingUpdate{}),
	} {
		if got == base {
			t.Errorf("%s: digest unchanged (%q)", name, got)
		}
	}
	if again := substitutionDigest(PackageConfig{Requires: spec("~")}, upd("3.14.0")); again != base {
		t.Errorf("the same requirement digests differently: %q vs %q", again, base)
	}
}

// TestRequiresReviewRewriteSkipsTrailingComments: a "# …" after code is a
// comment and its atom is not rewritten; a "#" inside a quoted, multi-line
// string is text, so the atom after it is.
func TestRequiresReviewRewriteSkipsTrailingComments(t *testing.T) {
	src := "RDEPEND=\"~dev-lang/dart-3.13.5\" # was ~dev-lang/dart-3.0\n" +
		"DEPEND=\"\n\tfoo#bar ~dev-lang/dart-3.13.5\n\t# ~dev-lang/dart-3.13.5\n\"\n"
	want := "RDEPEND=\"~dev-lang/dart-3.14.0\" # was ~dev-lang/dart-3.0\n" +
		"DEPEND=\"\n\tfoo#bar ~dev-lang/dart-3.14.0\n\t# ~dev-lang/dart-3.14.0\n\"\n"
	got, n, err := rewritePinnedAtoms([]byte(src), "dev-lang/dart", "~", "3.14.0")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want || n != 3 {
		t.Errorf("rewrite = %q (count %d)\nwant      %q (count 3)", got, n, want)
	}
}

// TestRequiresReviewCaptureRefusesRevisionForTilde: a ~ pin can never match a
// version carrying -rN, so capturing one would leave the bump waiting forever;
// it is held at check time instead.
func TestRequiresReviewCaptureRefusesRevisionForTilde(t *testing.T) {
	page, _ := requiresCaptureServer(t, `{"current_release": {"stable": "3.48.0"},
  "releases": [{"version": "3.48.0", "dart_sdk_version": "3.14.0-r1"}]}`)
	c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
		map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))
	result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
	requiresCaptureAssertHeld(t, c, result, err, requiresCapturePkg, requiresCaptureAtom, "3.14.0-r1")
}

// TestRequiresReviewObsoleteBeatsWaiting: an entry the overlay already passed
// is pruned as obsolete even when its requirement is unmet, rather than left
// waiting forever.
func TestRequiresReviewObsoleteBeatsWaiting(t *testing.T) {
	f := newRequiresApplyFixture(t, requiresApplyOptions{requires: map[string]string{requiresApplyAtom: requiresApplyDartNew}})
	f.place(t, f.overlay, requiresApplyPkg, requiresApplyNew)
	result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Obsolete || len(result.Waiting) != 0 {
		t.Errorf("Obsolete=%v Waiting=%q; want an obsolete prune, not a wait", result.Obsolete, result.Waiting)
	}
	if !strings.Contains(result.ObsoleteReason, requiresApplyNew) {
		t.Errorf("ObsoleteReason %q does not name the target", result.ObsoleteReason)
	}
}
