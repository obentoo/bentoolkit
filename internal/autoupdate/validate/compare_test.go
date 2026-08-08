package validate

// Authored for story 031, sub-task 4.1 — R3, R3.1, R3.2, R3.3.
//
// Written from the contract: design.md fixes
// `Compare(d Declared, p Passed, pkg, version string) []Finding` as a pure
// function, which is why every case below is a table row and not a fixture.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"strings"
	"testing"
)

// findingsAt returns the findings carrying the given severity.
func findingsAt(fs []Finding, sev Severity) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

// declaring builds a Declared whose root project declares exactly names.
func declaring(names ...string) Declared {
	d := Declared{Sources: []string{"gst-plugins-good-1.29.2/meson.options"}}
	for _, n := range names {
		d.Root = append(d.Root, Option{Name: n, Source: "gst-plugins-good-1.29.2/meson.options"})
	}
	return d
}

// passing builds a Passed whose ebuild passes exactly names as root project
// options.
func passing(names ...string) Passed {
	var p Passed
	for _, n := range names {
		p.Project = append(p.Project, Option{Name: n, Source: "gst-plugins-qt6-1.29.2.ebuild:42"})
	}
	return p
}

// TestCompare_PassedButUndeclaredIsAnError is issue #33 in one assertion.
func TestCompare_PassedButUndeclaredIsAnError(t *testing.T) {
	got := Compare(declaring("qt6", "qt-wayland"), passing("qt6", "aalib", "libcaca"), "media-plugins/gst-plugins-qt6", "1.29.2")

	errs := findingsAt(got, "error")
	if len(errs) != 2 {
		t.Fatalf("error findings: got %d, want 2 (aalib, libcaca); got %+v", len(errs), got)
	}
	joined := strings.Join([]string{errs[0].Detail, errs[1].Detail}, " | ")
	for _, want := range []string{"aalib", "libcaca", "1.29.2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error details %q do not name %q; a finding that does not name the option is not actionable", joined, want)
		}
	}
}

// TestCompare_DeclaredButUnpassedIsInfo pins the quieter half of the parity
// check: upstream gained an option we never set. Useful, not a failure.
func TestCompare_DeclaredButUnpassedIsInfo(t *testing.T) {
	got := Compare(declaring("qt6", "vulkan"), passing("qt6"), "media-plugins/gst-plugins-qt6", "1.29.2")

	infos := findingsAt(got, "info")
	if len(infos) != 1 {
		t.Fatalf("info findings: got %d, want 1 (vulkan); got %+v", len(infos), got)
	}
	if !strings.Contains(infos[0].Detail, "vulkan") {
		t.Errorf("info detail %q does not name vulkan", infos[0].Detail)
	}
	if len(findingsAt(got, "error")) != 0 {
		t.Error("a declared-but-unpassed option must not be an error; it blocks nothing")
	}
}

// TestCompare_UnresolvedIsAWarningWithItsLine is R3.3. The line number is the
// difference between a warning someone acts on and one they ignore.
func TestCompare_UnresolvedIsAWarningWithItsLine(t *testing.T) {
	p := passing("qt6")
	p.Unresolved = []Unresolved{{Text: "-D${plugin}=", Line: 57}}

	got := Compare(declaring("qt6"), p, "media-plugins/gst-plugins-qt6", "1.29.2")

	warns := findingsAt(got, "warning")
	if len(warns) != 1 {
		t.Fatalf("warning findings: got %d, want 1; got %+v", len(warns), got)
	}
	if !strings.Contains(warns[0].Detail, "-D${plugin}=") {
		t.Errorf("warning detail %q does not quote the text as written", warns[0].Detail)
	}
	if !strings.Contains(warns[0].Detail, "57") {
		t.Errorf("warning detail %q does not carry the line number", warns[0].Detail)
	}
}

// TestCompare_SubprojectMatchesItsOwnNamespace is the false-positive guard the
// design calls out: `sub:gl` checked against the ROOT's declarations would be
// reported undeclared even though the subproject declares it.
func TestCompare_SubprojectMatchesItsOwnNamespace(t *testing.T) {
	d := declaring("qt6")
	d.Subproject = map[string][]Option{
		"gst-plugins-base": {{Name: "gl", Subproject: "gst-plugins-base"}},
	}
	p := passing("qt6")
	p.Project = append(p.Project, Option{Name: "gl", Subproject: "gst-plugins-base"})

	got := Compare(d, p, "media-plugins/gst-plugins-qt6", "1.29.2")

	if errs := findingsAt(got, "error"); len(errs) != 0 {
		t.Errorf("a subproject option declared by its own subproject must not be an error; got %+v", errs)
	}
}

// TestCompare_SubprojectUndeclaredIsStillAnError keeps the namespacing from
// becoming a blanket amnesty.
func TestCompare_SubprojectUndeclaredIsStillAnError(t *testing.T) {
	d := declaring("qt6")
	d.Subproject = map[string][]Option{"sub": {{Name: "known", Subproject: "sub"}}}
	p := passing("qt6")
	p.Project = append(p.Project, Option{Name: "gone", Subproject: "sub"})

	got := Compare(d, p, "media-plugins/gst-plugins-qt6", "1.29.2")

	errs := findingsAt(got, "error")
	if len(errs) != 1 {
		t.Fatalf("error findings: got %d, want 1 for sub:gone; got %+v", len(errs), got)
	}
}

// TestCompare_BuiltInsAreNeverCompared pins that a classified built-in makes no
// finding at all, in either direction.
func TestCompare_BuiltInsAreNeverCompared(t *testing.T) {
	p := passing("qt6")
	p.BuiltIn = []Option{{Name: "b_ndebug"}, {Name: "default_library"}}

	got := Compare(declaring("qt6"), p, "media-plugins/gst-plugins-qt6", "1.29.2")

	for _, f := range got {
		if strings.Contains(f.Detail, "b_ndebug") || strings.Contains(f.Detail, "default_library") {
			t.Errorf("a built-in produced a finding: %q", f.Detail)
		}
	}
}

// TestCompare_EveryFindingNamesItsSource pins the traceability the review added
// to the plan: a finding that cannot be traced to the line that caused it makes
// the operator grep for it.
func TestCompare_EveryFindingNamesItsSource(t *testing.T) {
	got := Compare(declaring("qt6", "vulkan"), passing("qt6", "aalib"), "media-plugins/gst-plugins-qt6", "1.29.2")

	if len(got) == 0 {
		t.Fatal("expected findings to assert on")
	}
	for _, f := range got {
		if f.Gate != "options" {
			t.Errorf("finding %q carries gate %q, want \"options\" — the exit code selects on this field", f.Detail, f.Gate)
		}
		if !strings.Contains(f.Detail, ".ebuild") && !strings.Contains(f.Detail, "meson") {
			t.Errorf("finding %q names neither an ebuild line nor an archive member as its source", f.Detail)
		}
	}
}

// TestCompare_CleanPairProducesNoErrors is the 1.28.6 half of the golden pair:
// an ebuild passing only declared options must produce nothing blocking.
func TestCompare_CleanPairProducesNoErrors(t *testing.T) {
	got := Compare(declaring("qt6", "aalib", "libcaca"), passing("qt6", "aalib", "libcaca"), "media-plugins/gst-plugins-qt6", "1.28.6")

	if errs := findingsAt(got, "error"); len(errs) != 0 {
		t.Errorf("a matching pair produced %d error findings: %+v", len(errs), errs)
	}
}
