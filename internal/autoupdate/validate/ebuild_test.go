package validate

// Authored for story 031, sub-task 3.1 — R2, R2.1, R2.2, R2.3.
//
// Written from the contract: design.md fixes
// `OptionsFromEbuild(path string) (Passed, error)` and the Passed/Unresolved
// shapes. The behaviours asserted here are story requirements, not design
// choices — R2.1 literal names, R2.2 subproject namespacing, R2.3 unresolved
// names reported rather than dropped.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEbuild puts content in a temporary .ebuild and returns its path. Shared
// with builtin_test.go, which exercises the same entry point.
func writeEbuild(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg-1.0.ebuild")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test ebuild: %v", err)
	}
	return path
}

// hasProjectOption reports whether name was classified as a project option
// under the given subproject ("" for the root project).
func hasProjectOption(p Passed, subproject, name string) bool {
	for _, o := range p.Project {
		if o.Subproject == subproject && o.Name == name {
			return true
		}
	}
	return false
}

// TestOptionsFromEbuild_LiteralName is R2.1, and it is the case the whole gate
// rests on: `-Daalib=` is what gst-plugins-qt6 passes and upstream no longer
// declares.
func TestOptionsFromEbuild_LiteralName(t *testing.T) {
	path := writeEbuild(t, "emesonargs=(\n\t-Daalib=disabled\n\t-Dlibcaca=disabled\n)\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	for _, name := range []string{"aalib", "libcaca"} {
		if !hasProjectOption(got, "", name) {
			t.Errorf("option %q was not reported as a passed project option; got %+v", name, got.Project)
		}
	}
}

// TestOptionsFromEbuild_SubprojectQualified is R2.2. A subproject option
// compared against the root's declarations would be a false error, so the
// namespace has to survive extraction.
func TestOptionsFromEbuild_SubprojectQualified(t *testing.T) {
	path := writeEbuild(t, "emesonargs=(\n\t-Dgst-plugins-base:gl=enabled\n)\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	if !hasProjectOption(got, "gst-plugins-base", "gl") {
		t.Errorf("subproject option not namespaced; got %+v", got.Project)
	}
	if hasProjectOption(got, "", "gst-plugins-base:gl") {
		t.Error("subproject option was kept as a single root-level name; the prefix must be split off")
	}
}

// TestOptionsFromEbuild_ExpansionInNameIsUnresolved is R2.3, and it is the
// anti-false-clean rule: a name built at runtime is REPORTED, never dropped.
func TestOptionsFromEbuild_ExpansionInNameIsUnresolved(t *testing.T) {
	path := writeEbuild(t, "src_configure() {\n\tlocal plugin=foo\n\temesonargs+=( -D${plugin}=enabled )\n}\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	if len(got.Unresolved) != 1 {
		t.Fatalf("Unresolved: got %d entries, want 1 — a runtime-built name must never be silently skipped; got %+v", len(got.Unresolved), got)
	}
	if got.Unresolved[0].Line != 3 {
		t.Errorf("Unresolved[0].Line: got %d, want 3 — a finding the operator cannot locate is a finding they will grep for", got.Unresolved[0].Line)
	}
	if got.Unresolved[0].Text == "" {
		t.Error("Unresolved[0].Text is empty; the text as written is what makes the warning actionable")
	}
	if len(got.Project) != 0 {
		t.Errorf("an unresolvable name must not also be counted as a project option; got %+v", got.Project)
	}
}

// TestOptionsFromEbuild_ExpansionInValueIsResolved is the precision half of
// R2.3. `$(usex ...)` on the VALUE side is ordinary and must not trip the
// unresolved rule — a gate that warns on every normal ebuild gets switched off.
func TestOptionsFromEbuild_ExpansionInValueIsResolved(t *testing.T) {
	path := writeEbuild(t, "emesonargs=(\n\t$(meson_use qt6)\n\t-Dexamples=$(usex test enabled disabled)\n)\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	if !hasProjectOption(got, "", "examples") {
		t.Errorf("`-Dexamples=$(usex ...)` must resolve: the expansion is on the value side; got %+v", got.Project)
	}
	for _, u := range got.Unresolved {
		if u.Line == 3 {
			t.Errorf("line 3 was reported unresolved (%q); only the NAME side counts", u.Text)
		}
	}
}

// TestOptionsFromEbuild_MissingFileIsAnError pins that an unreadable ebuild
// surfaces as an error naming the path, rather than as an empty clean result.
func TestOptionsFromEbuild_MissingFileIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-1.0.ebuild")

	_, err := OptionsFromEbuild(missing)

	if err == nil {
		t.Fatal("OptionsFromEbuild on a missing file: got nil error, want one — an unreadable ebuild is never a clean ebuild")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the path %q", err, missing)
	}
}
