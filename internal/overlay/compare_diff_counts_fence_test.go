package overlay

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// This file is a FENCE, not a behaviour test: it asserts on this package's own
// source rather than on anything the package does at run time.
//
// R1.3 forbids the line counts from ever becoming a decision — no verdict, no
// recommendation, no removal eligibility may be derived from their size. A
// prohibition has no runtime surface to exercise. It is a statement about code
// that must never be written, and the only way to test code that does not exist
// is to read the source and prove it still does not.
//
// The pressure it holds back is named, not hypothetical. Story 025 deferred
// D-G5 — verification compares raw bytes, so a bumped copyright year reads as a
// divergence — and ruled that the fix, when it comes, is "a normalisation rule
// for R4.3, *not* a tolerance threshold: 'ignore small differences' would
// eventually ignore a real one". DiffAdded and DiffRemoved are exactly the input
// such a threshold wants, and they did not exist when that was written. What
// stands between the veto and the temptation is otherwise a convention someone
// has to remember; this file makes it mechanical.

// diffCountReaders is the whole allow-list. It has two entries by construction
// rather than by policy: one function FILLS the counts and one PRINTS them, and
// anything between those two ends is a computation on the size of a difference —
// which is the thing R1.3 forbids.
//
// The fill site's use is an ASSIGNMENT to the fields, not a read of them. The
// walk below sees a selector either way, and that is intended: a function that
// can write the counts is a function that decides what they say, so it belongs
// on the list on the same terms as the one that prints them.
var diffCountReaders = []string{
	// compare.go: result.DiffAdded, result.DiffRemoved = check.added, check.removed
	"comparePackageWithProvider",
	// compare.go: the "(+%d/-%d)" on the undeclared-divergence finding.
	"formatVerificationFindings",
}

// diffCountFields are the two names the fence watches.
//
// contentCheck's own lowercase added/removed are deliberately absent. They are
// one content check's local finding, they reach the report only through the fill
// site above, and matching them would fence off verifyAgainstLocalContent and
// diffLineCounts — the code that computes the counts in the first place.
var diffCountFields = []string{"DiffAdded", "DiffRemoved"}

// packageLevelOwner names a read that sits outside every function — a var
// initialiser, say. No allowed reader is spelled like this, so such a read fails
// the allow-list by construction instead of escaping a walk that only knows how
// to look inside function bodies.
const packageLevelOwner = "<package level>"

// diffCountRead is one place the package's source names a counted field: which
// declaration encloses it, which field, and where.
type diffCountRead struct {
	owner string
	field string
	pos   token.Position
}

// collectDiffCountReads walks one parsed file and returns every mention of a
// counted field, attributed to the declaration that encloses it.
//
// It walks per DECLARATION rather than per file so the enclosing function is
// known by construction instead of being recovered from a position afterwards.
// That also gives a package-level initialiser an owner, rather than letting it
// slip past a walk that only ever descends into function bodies.
func collectDiffCountReads(file *ast.File, fset *token.FileSet) []diffCountRead {
	var reads []diffCountRead
	for _, decl := range file.Decls {
		owner := packageLevelOwner
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name != nil {
			owner = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, isSelector := n.(*ast.SelectorExpr)
			if !isSelector || !slices.Contains(diffCountFields, sel.Sel.Name) {
				return true
			}
			reads = append(reads, diffCountRead{
				owner: owner,
				field: sel.Sel.Name,
				pos:   fset.Position(sel.Sel.Pos()),
			})
			return true
		})
	}
	return reads
}

// forbiddenDiffCountReads keeps the reads the allow-list does not permit.
//
// This is the fence's entire judgement, factored out so the scan of the real
// package and the scan of the probe source below are judged by the same code. A
// probe that exercised a second, parallel copy of this would prove something
// about that copy and nothing about the fence guarding the package.
func forbiddenDiffCountReads(reads []diffCountRead) []diffCountRead {
	var forbidden []diffCountRead
	for _, r := range reads {
		if !slices.Contains(diffCountReaders, r.owner) {
			forbidden = append(forbidden, r)
		}
	}
	return forbidden
}

// readsBy returns the reads attributed to one owner.
func readsBy(reads []diffCountRead, owner string) []diffCountRead {
	var found []diffCountRead
	for _, r := range reads {
		if r.owner == owner {
			found = append(found, r)
		}
	}
	return found
}

// diffCountReadsInPackage parses this package's non-test sources and returns
// every read of a counted field found in them.
//
// _test.go files are EXCLUDED on purpose. A test that asserts on the counts is a
// legitimate reader — compare_divergence_magnitude_test.go is one, and so is the
// probe below — and fencing tests off would make the counts unassertable, which
// is the opposite of what R1.3 protects.
func diffCountReadsInPackage(t *testing.T) []diffCountRead {
	t.Helper()

	// The package directory comes from this file's own path rather than from the
	// process working directory: a fence that reads whatever directory it happens
	// to be started in is a fence that can be moved off its subject.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	pkgDir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package directory %q: %v", pkgDir, err)
	}

	var reads []diffCountRead
	sources := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources++
		// A parse failure FAILS the test rather than skipping the file. An
		// unreadable source scanned as if it were empty yields exactly the clean
		// result a compliant package yields, so the fence would report the
		// absence of a violation it never actually looked for.
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		reads = append(reads, collectDiffCountReads(file, fset)...)
	}
	// Same argument one level up: a scan that found no files to read is not a
	// package with nothing to hide.
	if sources == 0 {
		t.Fatalf("no non-test Go source found in %q; the scan would pass vacuously", pkgDir)
	}
	return reads
}

// diffCountReadsInSource runs the same collector over one in-memory source, so
// the detector can be proved to detect before it is trusted to prove an absence.
func diffCountReadsInSource(t *testing.T, name, src string) []diffCountRead {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse probe source: %v", err)
	}
	return collectDiffCountReads(file, fset)
}

// TestDiffCountsFence is R1.3 held mechanically: in this package, outside the
// fill site and the render site, the line counts are not readable at all.
func TestDiffCountsFence(t *testing.T) {
	reads := diffCountReadsInPackage(t)

	for _, r := range forbiddenDiffCountReads(reads) {
		t.Errorf("%s reads %s at %s.\n"+
			"R1.3: the counts describe a difference and never decide anything about it. Only %s (which fills them) "+
			"and %s (which prints them) may name them — a third reader computes on the SIZE of a divergence, which is "+
			"the tolerance threshold story 025 ruled out by name: \"ignore small differences\" would eventually ignore a real one.",
			r.owner, r.field, r.pos, diffCountReaders[0], diffCountReaders[1])
	}

	// A fence that matched nothing also reports a clean scan, and would go on
	// reporting one after the fields were renamed out from under it — so the two
	// permitted readers are asserted present rather than assumed. The log lines
	// are the evidence that the scan reached the code it claims to be guarding.
	for _, reader := range diffCountReaders {
		found := readsBy(reads, reader)
		if len(found) == 0 {
			t.Errorf("%s names neither DiffAdded nor DiffRemoved: the scan matched nothing there, so a clean result above "+
				"proves nothing. Were the fields renamed, or the function moved or renamed?", reader)
			continue
		}
		where := make([]string, len(found))
		for i, r := range found {
			where[i] = fmt.Sprintf("%s at %s", r.field, r.pos)
		}
		t.Logf("allowed reader found — %s: %s", reader, strings.Join(where, "; "))
	}
}

// diffCountsFenceProbe is the source the fence is tested against.
//
// It is a STRING and never a file in this package, because a real fourth reader
// would fail the scan above — that scan is the assertion, not somewhere to put a
// fixture. Nothing here has to compile or type-check: the fence reads syntax.
const diffCountsFenceProbe = `package overlay

// The exact shape R1.3 forbids, and the one story 025 ruled out: a size
// threshold turned into removal eligibility. A fence that cannot see this sees
// nothing.
func pruneEligibility(r CompareResult) bool {
	return r.DiffAdded+r.DiffRemoved < 3
}

// contentCheck's own lowercase fields are a different pair of numbers. Matching
// them would fence off verifyAgainstLocalContent, where the counts are computed.
func summariseCheck(c contentCheck) int {
	return c.added + c.removed
}

// No enclosing function, so no owner a walk over function bodies would report.
// It is a reader all the same.
var divergenceTolerance = CompareResult{}.DiffRemoved

// The render site, spelled as compare.go spells it, so the allow-list is proved
// to be a filter rather than a blanket.
func formatVerificationFindings(r CompareResult) string {
	return fmt.Sprintf("(+%d/-%d)", r.DiffAdded, r.DiffRemoved)
}
`

// TestDiffCountsFenceDetectsAViolation proves the fence bites.
//
// The scan above passes today because the package is compliant, and it would
// pass identically if the walk were broken — a detector that finds nothing and a
// package that hides nothing produce the same green. This runs the same
// collector and the same allow-list over a source that IS in violation, so the
// green next door means something.
func TestDiffCountsFenceDetectsAViolation(t *testing.T) {
	reads := diffCountReadsInSource(t, "probe.go", diffCountsFenceProbe)
	forbidden := forbiddenDiffCountReads(reads)

	t.Run("a function outside the allow-list is reported by name and position", func(t *testing.T) {
		found := readsBy(forbidden, "pruneEligibility")
		if len(found) != 2 {
			t.Fatalf("the fence reports %d reads in pruneEligibility, want 2 (DiffAdded and DiffRemoved); "+
				"a removal threshold computed from the counts would land unnoticed", len(found))
		}
		for _, r := range found {
			if r.pos.Filename != "probe.go" || r.pos.Line == 0 {
				t.Errorf("the read of %s carries position %q; a violation the report cannot locate is one nobody can fix", r.field, r.pos)
			}
		}
	})

	t.Run("a read outside every function is reported too", func(t *testing.T) {
		if n := len(readsBy(forbidden, packageLevelOwner)); n != 1 {
			t.Errorf("the fence reports %d package-level reads, want 1; a walk that only descends into function bodies "+
				"would let a var declaration derive whatever it liked from the counts", n)
		}
	})

	t.Run("an allowed reader is seen and permitted", func(t *testing.T) {
		if n := len(readsBy(reads, "formatVerificationFindings")); n != 2 {
			t.Fatalf("the collector found %d reads in the probe's render site, want 2; the allow-list is never exercised", n)
		}
		if n := len(readsBy(forbidden, "formatVerificationFindings")); n != 0 {
			t.Errorf("the fence reports %d violations in formatVerificationFindings, which is on the allow-list; "+
				"a fence that flags its own permitted readers cannot stay in the build", n)
		}
	})

	t.Run("contentCheck's lowercase fields are not the counts", func(t *testing.T) {
		if n := len(readsBy(reads, "summariseCheck")); n != 0 {
			t.Errorf("the collector matched %d of contentCheck's own added/removed fields; over-matching would fence off "+
				"verifyAgainstLocalContent and diffLineCounts, which is where the counts come from", n)
		}
	})
}
