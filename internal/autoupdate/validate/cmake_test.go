package validate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cmakeArchive is a CMake release with options in the root, in a
// subdirectory and in an included module, a cache variable, a variable only
// read, and an option a release commented out.
func cmakeArchive(t *testing.T) string {
	t.Helper()
	return buildTarGz(t, map[string]string{
		"proj-2.0/CMakeLists.txt": `cmake_minimum_required(VERSION 3.20)
project(proj)
option(ENABLE_GUI "Build the GUI" ON)
OPTION(WITH_DOCS "Docs" OFF)
include(cmake/Features.cmake)
add_compile_options(-Wall)
set(PROJ_PLUGIN_DIR "lib/proj" CACHE PATH "where plugins go")
if(USE_SYSTEM_ZLIB)
endif()
option(python-bindings "Python" OFF)
foreach(p foo bar)
  option(ENABLE_PLUGIN_${p} "plugin" ON)
endforeach()
# option(ENABLE_OLDTHING "removed in 2.0" ON)
message(STATUS "a # inside quotes is not a comment")
add_subdirectory(src)
`,
		"proj-2.0/src/CMakeLists.txt":   "cmake_dependent_option(ENABLE_GL \"GL\" ON \"ENABLE_GUI\" OFF)\n",
		"proj-2.0/cmake/Features.cmake": "option(ENABLE_TRAY \"Tray\" ON)\n",
		"proj-2.0/README":               "option(NOT_CMAKE \"\" ON)\n",
	})
}

func TestOptionsFromArchive_ReadsCMake(t *testing.T) {
	got, err := OptionsFromArchive(context.Background(), cmakeArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.System != systemCMake {
		t.Fatalf("system = %q, want cmake", got.System)
	}
	names := strings.Join(declaredNames(got.Root), " ")
	for _, want := range []string{"ENABLE_GUI", "WITH_DOCS", "ENABLE_GL", "ENABLE_TRAY"} {
		if !strings.Contains(names, want) {
			t.Errorf("declared options %q lack %s", names, want)
		}
	}
	for _, unwanted := range []string{"ENABLE_OLDTHING", "NOT_CMAKE", "Wall"} {
		if strings.Contains(names, unwanted) {
			t.Errorf("declared options %q include %s, which no live option() declares", names, unwanted)
		}
	}
	for _, known := range []string{"PROJ_PLUGIN_DIR", "USE_SYSTEM_ZLIB", "ENABLE_GUI"} {
		if !got.Known[known] {
			t.Errorf("%s is mentioned by the CMake sources but not known", known)
		}
	}
	if got.Known["ENABLE_OLDTHING"] {
		t.Error("a commented-out option counts as known")
	}
}

// TestCompare_CMake: a warning only for a name the release mentions nowhere
// (an option upstream removed; CMake ignores it, so it is not the error it is
// for Meson); CMake's and find_package's own variables, typed names and names
// built at configure time are never reported; an option the ebuild does not
// pass is info.
func TestCompare_CMake(t *testing.T) {
	d, err := OptionsFromArchive(context.Background(), cmakeArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	passed := Passed{Project: []Option{
		{Name: "ENABLE_GUI", Source: "proj-2.0.ebuild:10"},
		{Name: "PROJ_PLUGIN_DIR", Source: "proj-2.0.ebuild:11"},
		{Name: "USE_SYSTEM_ZLIB", Source: "proj-2.0.ebuild:12"},
		{Name: "CMAKE_DISABLE_FIND_PACKAGE_Foo", Source: "proj-2.0.ebuild:13"},
		{Name: "Foo_DIR", Source: "proj-2.0.ebuild:14"},
		{Name: "KDE_INSTALL_USE_QT_SYS_PATHS", Source: "proj-2.0.ebuild:15"},
		{Name: "BUILD_TESTING", Source: "proj-2.0.ebuild:16"},
		{Name: "ENABLE_OLDTHING", Source: "proj-2.0.ebuild:17"},
		{Name: "BOOL", Subproject: "USE_SYSTEM_ZLIB", Source: "proj-2.0.ebuild:18"},
		{Name: "ENABLE_PLUGIN_FOO", Source: "proj-2.0.ebuild:19"},
		{Name: "python-bindings", Source: "proj-2.0.ebuild:20"},
	}}

	var errs, warns, infos []string
	for _, f := range Compare(d, passed, "dev-libs/proj", "2.0") {
		switch f.Severity {
		case SeverityError:
			errs = append(errs, f.Detail)
		case SeverityWarning:
			warns = append(warns, f.Detail)
		case SeverityInfo:
			infos = append(infos, f.Detail)
		}
	}
	if len(errs) != 0 {
		t.Errorf("errors = %q; a CMake -D never fails the gate", errs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "-DENABLE_OLDTHING=") || !strings.Contains(warns[0], "CMake ignores it") {
		t.Errorf("warnings = %q; want exactly the removed ENABLE_OLDTHING", warns)
	}
	if joined := strings.Join(infos, "\n"); !strings.Contains(joined, "WITH_DOCS") || strings.Contains(joined, "ENABLE_GUI") {
		t.Errorf("infos = %q; want WITH_DOCS (not passed) and not ENABLE_GUI (passed)", infos)
	}
}

func TestStripCMakeComments(t *testing.T) {
	got := string(stripCMakeComments([]byte("option(A \"x # y\" ON) # trailing\n# whole\nset(B 1)")))
	if !strings.Contains(got, `option(A "x # y" ON)`) || strings.Contains(got, "trailing") || strings.Contains(got, "whole") || !strings.Contains(got, "set(B 1)") {
		t.Errorf("stripped = %q", got)
	}
}

// TestLearnCMakePatches: an option a Gentoo patch adds is the ebuild's to pass.
func TestLearnCMakePatches(t *testing.T) {
	pkgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pkgDir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	patch := "+option(ENABLE_SLOTTED_VERSION \"slot\" OFF)\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "files", "slot.patch"), []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Declared{System: systemCMake, Known: map[string]bool{}, BuiltPrefixes: map[string]bool{}}
	d.LearnCMakePatches(pkgDir)
	if !d.cmakeKnows("ENABLE_SLOTTED_VERSION") {
		t.Error("an option the package's patch adds is not known")
	}
	meson := Declared{}
	meson.LearnCMakePatches(pkgDir)
	if meson.Known != nil {
		t.Error("a Meson declaration learned from CMake patches")
	}
}

// TestOptionsFromEbuild_CompilerDefinesAreNotOptions: a -D in a compiler flag
// line is a preprocessor define, not a build system option.
func TestOptionsFromEbuild_CompilerDefinesAreNotOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-1.ebuild")
	body := "src_configure() {\n\tappend-cppflags -DSKCMS_HAS_MUSTTAIL=0\n\tlocal mycmakeargs=( -DENABLE_GUI=ON )\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := OptionsFromEbuild(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Project) != 1 || p.Project[0].Name != "ENABLE_GUI" {
		t.Errorf("project options = %+v; want only ENABLE_GUI", p.Project)
	}
}
