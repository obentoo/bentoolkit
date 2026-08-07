package validate

// Authored for story 031, sub-task 3.2 — R2, R2.4.
//
// Written from the contract, and deliberately exercised through the PUBLIC
// entry point `OptionsFromEbuild` rather than through the built-in table
// itself: R2.4 is about how a passed name is CLASSIFIED, and the table is an
// implementation of that, free to be a map, a switch or a set of rules.
//
// What this file protects is worth stating plainly, because it is the failure
// mode that gets gates switched off: Meson's built-in options are never
// declared in an option file, so an unclassified built-in becomes a
// passed-but-undeclared FALSE error. Measured against the live overlay on
// 2026-08-07, exactly two ebuilds pass one — `-Db_ndebug=`.
//
// Red is DEFERRED to Run mode: the package does not exist yet.
//
// writeEbuild and hasProjectOption come from ebuild_test.go in this package.

import "testing"

// isBuiltIn reports whether name was classified as a Meson built-in.
func isBuiltIn(p Passed, name string) bool {
	for _, o := range p.BuiltIn {
		if o.Name == name {
			return true
		}
	}
	return false
}

// TestOptionsFromEbuild_BuiltInFamilies walks one representative of every rule
// the built-in classifier has to cover. Each row names why it exists, because
// a future reader deleting a "redundant" row is how the table rots.
func TestOptionsFromEbuild_BuiltInFamilies(t *testing.T) {
	tests := []struct {
		name   string
		option string
		why    string
	}{
		{"base option family", "b_ndebug", "the `b_` prefix; the only built-in family the live overlay actually passes"},
		{"core option", "default_library", "a fixed name from the core table"},
		{"directory option", "libdir", "a fixed name from the directory table"},
		{"buildtype", "buildtype", "a fixed name eclasses and ebuilds both set"},
		{"compiler args", "c_args", "the `<lang>_args` suffix rule"},
		{"compiler std", "cpp_std", "the `<lang>_std` suffix rule"},
		{"module option", "python.bytecompile", "a module option, carrying a dot before any colon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeEbuild(t, "emesonargs=(\n\t-D"+tt.option+"=false\n)\n")

			got, err := OptionsFromEbuild(path)
			if err != nil {
				t.Fatalf("OptionsFromEbuild: %v", err)
			}

			if !isBuiltIn(got, tt.option) {
				t.Errorf("%q was not classified as a Meson built-in (%s); left unclassified it becomes a FALSE error", tt.option, tt.why)
			}
			if hasProjectOption(got, "", tt.option) {
				t.Errorf("%q was also counted as a project option; a built-in must never reach the comparison", tt.option)
			}
		})
	}
}

// TestOptionsFromEbuild_ProjectOptionIsNotBuiltIn guards the other direction.
// A classifier wide enough to swallow real project options would make the gate
// silently useless — `aalib` is exactly the name issue #33 turns on.
func TestOptionsFromEbuild_ProjectOptionIsNotBuiltIn(t *testing.T) {
	path := writeEbuild(t, "emesonargs=(\n\t-Daalib=disabled\n\t-Dlibcaca=disabled\n)\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	for _, name := range []string{"aalib", "libcaca"} {
		if isBuiltIn(got, name) {
			t.Errorf("%q was classified as a built-in; the classifier is too wide and the gate would miss issue #33", name)
		}
		if !hasProjectOption(got, "", name) {
			t.Errorf("%q was not reported as a project option", name)
		}
	}
}

// TestOptionsFromEbuild_SubprojectBuiltInKeepsItsNamespace pins that a
// built-in addressed at a subproject is still a built-in. Meson allows several
// built-ins to be set per subproject, so `-Dsub:b_ndebug=` is legitimate and
// must not become a finding against `sub`'s declarations.
func TestOptionsFromEbuild_SubprojectBuiltInKeepsItsNamespace(t *testing.T) {
	path := writeEbuild(t, "emesonargs=(\n\t-Dsub:b_ndebug=true\n)\n")

	got, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatalf("OptionsFromEbuild: %v", err)
	}

	if hasProjectOption(got, "sub", "b_ndebug") {
		t.Error("a subproject-addressed built-in reached the project options; it would be compared against declarations that never list it")
	}
	if !isBuiltIn(got, "b_ndebug") {
		t.Errorf("subproject-addressed built-in was not classified as built-in; got %+v", got)
	}
}
