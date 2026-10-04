// Story 069, sub-task 1.2 (R1.3, R1.4, R4.1, R4.2): the playwright backend is
// gone, its module is gone, and a build that still asks for the removed tag
// fails with a pointer to chromedp instead of silently shipping a binary
// without browser support.
//
// This is the test of the legacy-tag guard file, so it is one of the .go files
// that may name the removed backend.

package autoupdate

import (
	"bufio"
	"context"
	"errors"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const legacyModulePath = "github.com/mxschmitt/playwright-go"

// legacyModuleRoot walks up from the package directory to the directory
// holding this module's go.mod.
func legacyModuleRoot(t *testing.T) string {
	t.Helper()
	dir := legacyPackageDir(t)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			if !regexp.MustCompile(`(?m)^module\s+github\.com/obentoo/bentoolkit\s*$`).Match(data) {
				t.Fatalf("%s/go.mod is not the bentoolkit module", dir)
			}
			return dir
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reading %s/go.mod: %v", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// legacyPackageDir is the package directory, which `go test` makes the
// working directory.
func legacyPackageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// legacyConstraint returns the //go:build expression of a .go file in the
// package directory, or nil when it has none.
func legacyConstraint(t *testing.T, name string) constraint.Expr {
	t.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	for _, line := range strings.Split(string(src[:fset.Position(f.Package).Offset]), "\n") {
		line = strings.TrimSpace(line)
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				t.Fatalf("parsing the build constraint of %s: %v", name, err)
			}
			return expr
		}
	}
	return nil
}

// legacyGuardOnly reports whether a file with constraint expr is compiled only
// when the removed tag is on: never by the default build, never by -tags
// chromedp, always by -tags playwright and -tags "chromedp playwright".
func legacyGuardOnly(expr constraint.Expr) bool {
	if expr == nil {
		return false
	}
	builtWith := func(tags ...string) bool {
		return expr.Eval(func(tag string) bool {
			for _, on := range tags {
				if tag == on {
					return true
				}
			}
			return false
		})
	}
	return !builtWith() && !builtWith("chromedp") && builtWith("playwright") && builtWith("chromedp", "playwright")
}

// legacyGoFiles lists the .go files in the package directory.
func legacyGoFiles(t *testing.T, includeTests bool) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// legacyGoBuild runs `go build [-tags tags]` on this package from the module
// root and returns its combined output and whether it succeeded. A failure to
// run the toolchain at all, or a timeout, fails the test: neither is evidence
// about the build constraints.
func legacyGoBuild(t *testing.T, tags string) (string, bool) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is not on PATH: %v", err)
	}
	root := legacyModuleRoot(t)
	rel, err := filepath.Rel(root, legacyPackageDir(t))
	if err != nil {
		t.Fatalf("locating the package below %s: %v", root, err)
	}

	budget := 5 * time.Minute
	if dl, ok := t.Deadline(); ok {
		if left := time.Until(dl) - 10*time.Second; left < budget {
			budget = left
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	args := []string{"build"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./"+filepath.ToSlash(rel))
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("go %s did not finish within %v: %v\n%s", strings.Join(args, " "), budget, ctx.Err(), out)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running go %s: %v", strings.Join(args, " "), err)
		}
		return string(out), false
	}
	return string(out), true
}

// legacyDiagnostic matches one compiler diagnostic: "path.go:line[:col]: msg".
var legacyDiagnostic = regexp.MustCompile(`^(\S+\.go):\d+(?::\d+)?: (.+)$`)

// R4.1, R4.2 - the removed tag, alone or beside chromedp, fails compilation,
// and the failure is the guard's own diagnostic naming chromedp.
func TestSingleBackendLegacyTagFailsNamingChromedp(t *testing.T) {
	root := legacyModuleRoot(t)
	pkgDir := legacyPackageDir(t)
	for _, tags := range []string{"playwright", "chromedp playwright"} {
		t.Run(strings.ReplaceAll(tags, " ", "+"), func(t *testing.T) {
			out, ok := legacyGoBuild(t, tags)
			if ok {
				t.Fatalf("go build -tags %q succeeded; a build asking for the removed backend must fail\n%s",
					tags, out)
			}
			// Hostile half: the output can contain "chromedp" for the wrong
			// reason, e.g. a diagnostic located in script_evaluator_chromedp.go,
			// or a build broken by something else entirely. Only the message of a
			// diagnostic raised from a file of this package compiled solely for
			// the removed tag counts.
			found := false
			sc := bufio.NewScanner(strings.NewReader(out))
			for sc.Scan() {
				m := legacyDiagnostic.FindStringSubmatch(strings.TrimSpace(sc.Text()))
				if m == nil {
					continue
				}
				path := m[1]
				if !filepath.IsAbs(path) {
					path = filepath.Join(root, path)
				}
				if filepath.Dir(path) != pkgDir {
					continue
				}
				if legacyGuardOnly(legacyConstraint(t, filepath.Base(path))) && strings.Contains(m[2], "chromedp") {
					found = true
				}
			}
			if !found {
				t.Errorf("go build -tags %q failed, but no diagnostic from a file built only for the removed tag "+
					"names chromedp\n%s", tags, out)
			}
		})
	}
}

// R4 converse - the guard fires on the removed tag only. A guard that also
// trips on chromedp would break the one backend that remains.
func TestSingleBackendChromedpTagStillBuilds(t *testing.T) {
	if out, ok := legacyGoBuild(t, "chromedp"); !ok {
		t.Errorf("go build -tags chromedp failed; the legacy-tag guard must not fire on the surviving tag\n%s", out)
	}
}

// R1.3 - no source file of the package, tagged or not, test or not, imports
// playwright-go.
func TestSingleBackendNoPlaywrightGoSource(t *testing.T) {
	sawTagged := false
	for _, name := range legacyGoFiles(t, true) {
		if legacyConstraint(t, name) != nil {
			sawTagged = true
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", name, imp.Path.Value, err)
			}
			if p == legacyModulePath || strings.HasPrefix(p, legacyModulePath+"/") {
				t.Errorf("%s imports %s; the playwright-go backend must be deleted", name, p)
			}
		}
	}
	if !sawTagged {
		t.Error("the scan saw no file with a build constraint, so it never inspected a tagged backend")
	}
}

// R1.4 - go.mod requires nothing from playwright-go and go.sum holds no line
// for it. A comment naming the module is not a requirement and does not count.
func TestSingleBackendGoModDropsPlaywrightGo(t *testing.T) {
	root := legacyModuleRoot(t)
	isModule := func(field string) bool {
		return field == legacyModulePath || strings.HasPrefix(field, legacyModulePath+"/")
	}

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for i, line := range strings.Split(string(gomod), "\n") {
		if j := strings.Index(line, "//"); j >= 0 {
			line = line[:j]
		}
		for _, field := range strings.Fields(line) {
			if isModule(field) {
				t.Errorf("go.mod:%d still names %s", i+1, legacyModulePath)
			}
		}
	}

	gosum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("reading go.sum: %v", err)
	}
	for i, line := range strings.Split(string(gosum), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && isModule(fields[0]) {
			t.Errorf("go.sum:%d still carries %s", i+1, fields[0])
		}
	}
}

// R1.3, R6.1 - outside the tests, the only file of the package that still
// names the removed backend is the guard compiled solely for its tag.
func TestSingleBackendOnlyGuardMentionsPlaywright(t *testing.T) {
	for _, name := range legacyGoFiles(t, false) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if !strings.Contains(strings.ToLower(string(src)), "playwright") {
			continue
		}
		if !legacyGuardOnly(legacyConstraint(t, name)) {
			t.Errorf("%s names playwright but is not the legacy-tag guard (a file built only for that tag)", name)
		}
	}
}
