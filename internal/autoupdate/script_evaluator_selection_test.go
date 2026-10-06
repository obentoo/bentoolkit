//go:build !chromedp

// Story 069, sub-task 1.1 (R1.1, R1.2, R2.1, R2.2, R2.3, R3.1-R3.4, R6.1):
// the script parser's live evaluator is chosen by complementary build
// constraints, not by init() ordering, and a binary built without browser
// support says how to get it.
//
// This file runs only in the default build: its runtime assertions are about
// the stub evaluator, which a `-tags chromedp` build replaces by design. The
// source-level assertions read every .go file of the package regardless of its
// build constraint, so a tagged file the default build never compiles is still
// inspected.

package autoupdate

import (
	"context"
	"errors"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// selSourceFile is one .go file of this package, parsed with comments.
type selSourceFile struct {
	name       string
	file       *ast.File
	constraint constraint.Expr // nil when the file has no //go:build line
}

// selPackageSources parses every .go file in the package directory, whatever
// its build constraint. Test files are included only when includeTests is set.
func selPackageSources(t *testing.T, includeTests bool) []selSourceFile {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var out []selSourceFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		expr, err := selBuildConstraint(src[:fset.Position(f.Package).Offset])
		if err != nil {
			t.Fatalf("parsing the build constraint of %s: %v", name, err)
		}
		out = append(out, selSourceFile{name: name, file: f, constraint: expr})
	}
	if len(out) == 0 {
		t.Fatal("found no .go files in the package directory")
	}
	return out
}

// selBuildConstraint returns the //go:build expression in a file header, or
// nil when there is none.
func selBuildConstraint(header []byte) (constraint.Expr, error) {
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimSpace(line)
		if constraint.IsGoBuild(line) {
			return constraint.Parse(line)
		}
	}
	return nil, nil
}

// selMatches reports whether a file with constraint expr is compiled when
// exactly the tags in set are on.
func selMatches(expr constraint.Expr, set map[string]bool) bool {
	if expr == nil {
		return true
	}
	return expr.Eval(func(tag string) bool { return set[tag] })
}

// selTags collects every tag named by expr into dst.
func selTags(expr constraint.Expr, dst map[string]bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		dst[e.Tag] = true
	case *constraint.NotExpr:
		selTags(e.X, dst)
	case *constraint.AndExpr:
		selTags(e.X, dst)
		selTags(e.Y, dst)
	case *constraint.OrExpr:
		selTags(e.X, dst)
		selTags(e.Y, dst)
	}
}

// selDefines reports whether f declares a package-level func or var named
// name, and returns the node that defines it.
func selDefines(f *ast.File, name string) (ast.Node, bool) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == name {
				return d, true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name == name {
						if i < len(vs.Values) {
							return vs.Values[i], true
						}
						return vs, true
					}
				}
			}
		}
	}
	return nil, false
}

// selImports reports whether f imports path.
func selImports(f *ast.File, path string) bool {
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == path {
			return true
		}
	}
	return false
}

// selMentionsIdent reports whether node refers to the identifier name.
func selMentionsIdent(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// selAssignment renders a tag assignment for failure messages.
func selAssignment(set map[string]bool) string {
	var on []string
	for tag, v := range set {
		if v {
			on = append(on, tag)
		}
	}
	if len(on) == 0 {
		return "no tags"
	}
	sort.Strings(on)
	return "-tags " + strings.Join(on, ",")
}

// R2.1, R1.1, R1.2 - the factory behind newLiveEvaluator is defined in exactly
// one compiled file under every tag assignment: the stub when chromedp is off,
// the chromedp backend when it is on.
func TestSingleBackendFactoryDefinedOncePerTagSet(t *testing.T) {
	const factory = "defaultLiveEvaluator"
	var defs []selSourceFile
	for _, sf := range selPackageSources(t, false) {
		if _, ok := selDefines(sf.file, factory); ok {
			defs = append(defs, sf)
		}
	}
	if len(defs) == 0 {
		t.Fatalf("no non-test file defines %s; R2.1 wants newLiveEvaluator initialised from a factory "+
			"chosen by build constraints", factory)
	}

	tags := map[string]bool{"chromedp": true}
	for _, sf := range defs {
		selTags(sf.constraint, tags)
	}
	var names []string
	for tag := range tags {
		names = append(names, tag)
	}
	sort.Strings(names)
	if len(names) > 12 {
		t.Fatalf("the factory files name %d build tags (%v); too many to enumerate", len(names), names)
	}

	// Hostile halves first. Overlap: two factory files compiled together under
	// some assignment (a duplicate definition, or a constraint pair that is not
	// complementary). Gap: an assignment under which no file defines the
	// factory, so the build has no evaluator at all.
	for mask := 0; mask < 1<<len(names); mask++ {
		set := map[string]bool{}
		for i, tag := range names {
			set[tag] = mask&(1<<i) != 0
		}
		var matched []string
		for _, sf := range defs {
			if selMatches(sf.constraint, set) {
				matched = append(matched, sf.name)
			}
		}
		switch {
		case len(matched) > 1:
			t.Errorf("under %s, %d files define %s (%v): the build constraints overlap", selAssignment(set),
				len(matched), factory, matched)
		case len(matched) == 0:
			t.Errorf("under %s, no file defines %s: the build constraints leave a gap", selAssignment(set), factory)
		}
	}

	// A third definition, even one whose constraint never overlaps, is not the
	// two-file selection R2.1 describes.
	if len(defs) != 2 {
		var all []string
		for _, sf := range defs {
			all = append(all, sf.name)
		}
		t.Errorf("%s is defined in %d files (%v), want exactly 2 (the stub and the chromedp backend)",
			factory, len(defs), all)
	}

	// Benign: the stub carries the default build, the browser the chromedp one.
	for _, sf := range defs {
		node, _ := selDefines(sf.file, factory)
		switch {
		case selMatches(sf.constraint, map[string]bool{}):
			if selImports(sf.file, "github.com/chromedp/chromedp") {
				t.Errorf("%s is the default-build factory but imports chromedp", sf.name)
			}
			if !selMentionsIdent(node, "ErrScriptSupportNotBuilt") {
				t.Errorf("%s is the default-build factory but does not return ErrScriptSupportNotBuilt", sf.name)
			}
		case selMatches(sf.constraint, map[string]bool{"chromedp": true}):
			if !selImports(sf.file, "github.com/chromedp/chromedp") {
				t.Errorf("%s is the -tags chromedp factory but does not import github.com/chromedp/chromedp",
					sf.name)
			}
		default:
			t.Errorf("%s defines %s but is compiled neither by the default build nor by -tags chromedp",
				sf.name, factory)
		}
	}
}

// R2.1 - the seam is declared once, in every build, and initialised from the
// factory rather than from a literal some init() later overwrites.
func TestSingleBackendSeamInitialisedFromFactory(t *testing.T) {
	type decl struct {
		file string
		node ast.Node
		expr constraint.Expr
	}
	var decls []decl
	for _, sf := range selPackageSources(t, false) {
		if node, ok := selDefines(sf.file, "newLiveEvaluator"); ok {
			decls = append(decls, decl{sf.name, node, sf.constraint})
		}
	}
	if len(decls) != 1 {
		t.Fatalf("newLiveEvaluator is declared in %d non-test files, want exactly 1", len(decls))
	}
	d := decls[0]
	if d.expr != nil {
		t.Errorf("newLiveEvaluator is declared in %s behind //go:build %s; the seam must exist in every build",
			d.file, d.expr)
	}
	id, ok := d.node.(*ast.Ident)
	if !ok || id.Name != "defaultLiveEvaluator" {
		t.Errorf("newLiveEvaluator in %s is initialised from %T, want the identifier defaultLiveEvaluator",
			d.file, d.node)
	}
}

// R2.2 - no init() anywhere in the package assigns the seam. Every file is
// read, tagged ones included: a scan of the default build alone would miss the
// browser backend, the file this rule exists for.
func TestSingleBackendNoInitAssignsSeam(t *testing.T) {
	files := selPackageSources(t, true)
	sawChromedpFile := false
	for _, sf := range files {
		tags := map[string]bool{}
		selTags(sf.constraint, tags)
		if tags["chromedp"] {
			sawChromedpFile = true
		}
		for _, decl := range sf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "init" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "newLiveEvaluator" {
						t.Errorf("%s: init() assigns newLiveEvaluator; the evaluator must be chosen by build "+
							"constraints alone", sf.name)
					}
				}
				return true
			})
		}
	}
	if !sawChromedpFile {
		t.Error("the scan saw no file constrained on the chromedp tag, so it never inspected the browser backend")
	}
}

// selStubLive is a liveEvaluator that answers a fixed version.
type selStubLive struct{ calls int }

func (s *selStubLive) Evaluate(context.Context, string, string, map[string]string) (string, error) {
	s.calls++
	return "9.9.9", nil
}

// selScriptChecker builds a Checker over a one-package overlay whose only
// record uses parser = "script".
func selScriptChecker(t *testing.T) (*Checker, string) {
	t.Helper()
	const pkg = "app-misc/scripted"
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	createTestEbuild(t, overlay, pkg, "1.0.0")
	c, err := NewChecker(overlay,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			pkg: {URL: "https://example.invalid/releases", Parser: "script", Script: "latest()"},
		}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c, pkg
}

// R2.3 - a test that replaces newLiveEvaluator gets its replacement for that
// test, and the default comes back through t.Cleanup.
func TestSingleBackendSeamSwapIsRestoredByCleanup(t *testing.T) {
	stub := &selStubLive{}
	t.Run("swapped", func(t *testing.T) {
		orig := newLiveEvaluator
		newLiveEvaluator = func(time.Duration) (liveEvaluator, error) { return stub, nil }
		t.Cleanup(func() { newLiveEvaluator = orig })

		c, pkg := selScriptChecker(t)
		res, err := c.CheckPackage(t.Context(), pkg, true)
		if err != nil {
			t.Fatalf("check with the replacement evaluator failed: %v", err)
		}
		if res.UpstreamVersion != "9.9.9" || stub.calls != 1 {
			t.Errorf("the check did not use the replacement: upstream %q after %d evaluations",
				res.UpstreamVersion, stub.calls)
		}
	})

	if _, err := newLiveEvaluator(time.Second); !errors.Is(err, ErrScriptSupportNotBuilt) {
		t.Errorf("after the swapping test ended newLiveEvaluator returned %v, want the default build's "+
			"ErrScriptSupportNotBuilt", err)
	}
}

// R1.2, R3.1-R3.4 - a script package checked by the default build fails with
// the sentinel, and the text tells the user which tag and which browser.
func TestSingleBackendNotBuiltErrorThroughCheck(t *testing.T) {
	if ev, err := newLiveEvaluator(time.Second); ev != nil || !errors.Is(err, ErrScriptSupportNotBuilt) {
		t.Fatalf("the default build's evaluator = (%v, %v), want (nil, ErrScriptSupportNotBuilt)", ev, err)
	}

	c, pkg := selScriptChecker(t)
	_, err := c.CheckPackage(t.Context(), pkg, true)
	if err == nil {
		t.Fatal("checking a parser = \"script\" package without browser support succeeded")
	}
	if !errors.Is(err, ErrScriptSupportNotBuilt) {
		t.Errorf("errors.Is(err, ErrScriptSupportNotBuilt) = false for %v", err)
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	// Hostile half first: "chromedp" itself begins with "chrome", so a message
	// that names only the tag would pass a naive search for the browser. Strip
	// every occurrence of the tag before looking for Chrome or Chromium.
	browser := strings.ReplaceAll(lower, "chromedp", "")
	if !strings.Contains(browser, "chrome") && !strings.Contains(browser, "chromium") {
		t.Errorf("the error names no Chrome or Chromium executable (the chromedp tag does not count): %q", msg)
	}
	if !strings.Contains(lower, "requir") && !strings.Contains(lower, "need") {
		t.Errorf("the error does not say the browser is required at run time: %q", msg)
	}
	if !strings.Contains(msg, "-tags chromedp") {
		t.Errorf("the error does not name the -tags chromedp build flag: %q", msg)
	}
	if strings.Contains(lower, "playwright") {
		t.Errorf("the error still sends the user to playwright: %q", msg)
	}
}

// selCommentText joins the text of the given comment groups.
func selCommentText(groups []*ast.CommentGroup) string {
	var b strings.Builder
	for _, g := range groups {
		for _, c := range g.List {
			b.WriteString(c.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// R6.1 - the doc comments of script_parser.go, parseLive and the chromedp
// backend describe one backend, chromedp.
func TestSingleBackendDocCommentsNameOnlyChromedp(t *testing.T) {
	byName := map[string]selSourceFile{}
	for _, sf := range selPackageSources(t, false) {
		byName[sf.name] = sf
	}

	check := func(where, text string) {
		t.Helper()
		lower := strings.ToLower(text)
		if strings.Contains(lower, "playwright") {
			t.Errorf("%s still mentions playwright", where)
		}
		if !strings.Contains(lower, "chromedp") {
			t.Errorf("%s does not name chromedp as the script backend", where)
		}
	}

	for _, name := range []string{"script_parser.go", "script_evaluator_chromedp.go"} {
		sf, ok := byName[name]
		if !ok {
			t.Errorf("%s is missing from the package", name)
			continue
		}
		check(name+" comments", selCommentText(sf.file.Comments))
	}

	sf, ok := byName["checker.go"]
	if !ok {
		t.Fatal("checker.go is missing from the package")
	}
	var doc *ast.CommentGroup
	for _, decl := range sf.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.Name == "parseLive" {
			doc = fn.Doc
		}
	}
	if doc == nil {
		t.Fatal("checker.go has no documented (*Checker).parseLive")
	}
	check("the parseLive doc comment", selCommentText([]*ast.CommentGroup{doc}))
}
