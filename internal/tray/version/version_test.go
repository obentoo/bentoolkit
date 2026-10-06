package version_test

// The tray's version comes from one embedded file, VERSION, and Info renders
// the text bentoo-tray --version prints: the tray's version first, then the
// bentoolkit release it was built from, then the lines bentoo's own Info
// prints after its first one.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	release "github.com/obentoo/bentoolkit/internal/common/version"
	trayversion "github.com/obentoo/bentoolkit/internal/tray/version"
)

// semverCore is MAJOR.MINOR.PATCH with no leading zeros, no pre-release and no
// build metadata, the form semantic versioning gives a release.
var semverCore = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func TestVersion_IsMajorMinorPatch(t *testing.T) {
	v := trayversion.Version()
	if !semverCore.MatchString(v) {
		t.Fatalf("Version() = %q, want a MAJOR.MINOR.PATCH version such as 0.1.0", v)
	}
}

// The function returns the file's content with surrounding whitespace
// removed: a VERSION file ending in a newline must not leak it.
func TestVersion_IsTheTrimmedVersionFile(t *testing.T) {
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("reading the VERSION file beside the package: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("the VERSION file is empty")
	}
	got := trayversion.Version()
	if got != want {
		t.Errorf("Version() = %q, want the VERSION file's trimmed content %q", got, want)
	}
	if got != strings.TrimSpace(got) {
		t.Errorf("Version() = %q carries surrounding whitespace", got)
	}
}

// A plain `go build` must report the file's version, so the file is embedded
// with go:embed in the package source rather than injected with -ldflags.
func TestVersion_FileIsEmbeddedInThePackageSource(t *testing.T) {
	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: ParseDir is deprecated because it ignores build tags; this test only lists the package's files
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	embedded := false
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					fields := strings.Fields(strings.TrimPrefix(c.Text, "//go:embed"))
					if strings.HasPrefix(c.Text, "//go:embed ") && len(fields) == 1 && fields[0] == "VERSION" {
						embedded = true
					}
				}
			}
		}
	}
	if !embedded {
		t.Error("no non-test source file in the package carries a `//go:embed VERSION` directive")
	}
}

// moduleRoot walks up from the package directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// The Makefile keeps injecting bentoolkit's release into
// internal/common/version and injects nothing into the tray's version.
func TestMakefile_InjectsTheReleaseAndNotTheTrayVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(data)
	if !regexp.MustCompile(`(?m)^VERSION_PKG := \$\(MODULE\)/internal/common/version$`).MatchString(mk) {
		t.Error("the Makefile no longer sets VERSION_PKG to $(MODULE)/internal/common/version")
	}
	for _, v := range []string{"Version", "Commit", "BuildDate"} {
		if !strings.Contains(mk, "-X $(VERSION_PKG)."+v+"=") {
			t.Errorf("the Makefile no longer injects $(VERSION_PKG).%s", v)
		}
	}
	if strings.Contains(mk, "internal/tray/version") {
		t.Error("the Makefile names internal/tray/version; the tray's version must come from the embedded file alone")
	}
}

// setRelease replaces bentoolkit's build-time values for one test.
func setRelease(t *testing.T, v, commit, built string) {
	t.Helper()
	oldV, oldC, oldB := release.Version, release.Commit, release.BuildDate
	t.Cleanup(func() { release.Version, release.Commit, release.BuildDate = oldV, oldC, oldB })
	release.Version, release.Commit, release.BuildDate = v, commit, built
}

// Info must name the tray first and the release second. The release value is
// a sentinel that cannot be mistaken for the tray's version, so printing the
// two in swapped places, or printing only one of them, fails.
func TestInfo_NamesTheTrayThenTheRelease(t *testing.T) {
	setRelease(t, "9.8.7-release", "c0ffee1234", "2026-01-02T03:04:05Z")
	if trayversion.Version() == release.Version {
		t.Fatalf("fixture: tray version %q equals the release sentinel", trayversion.Version())
	}
	info := trayversion.Info()
	lines := strings.Split(info, "\n")
	if len(lines) < 2 {
		t.Fatalf("Info() has %d line(s), want at least 2:\n%s", len(lines), info)
	}
	if want := "bentoo-tray version " + trayversion.Version(); lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if want := "  bentoolkit: 9.8.7-release"; lines[1] != want {
		t.Errorf("second line = %q, want %q", lines[1], want)
	}
	// The rest is exactly what bentoo's Info prints after its first line:
	// commit, build date, Go version and os/arch.
	_, releaseTail, ok := strings.Cut(release.Info(), "\n")
	if !ok {
		t.Fatalf("bentoo's Info() has a single line: %q", release.Info())
	}
	if want := lines[0] + "\n" + lines[1] + "\n" + releaseTail; info != want {
		t.Errorf("Info() =\n%s\nwant\n%s", info, want)
	}
}

// The tray's text never presents itself as bentoo: no line reads
// "bentoo version ...", even when the release value would make one look
// plausible.
func TestInfo_HasNoBentooVersionLine(t *testing.T) {
	setRelease(t, "0.33.0", "c0ffee1234", "2026-01-02T03:04:05Z")
	info := trayversion.Info()
	if strings.Contains(info, "bentoo version") {
		t.Errorf("Info() names the program `bentoo version`:\n%s", info)
	}
	for _, l := range strings.Split(info, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "bentoo version") {
			t.Errorf("Info() line %q names bentoo rather than bentoo-tray", l)
		}
	}
}
