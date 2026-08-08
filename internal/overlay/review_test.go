package overlay

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// fakeReviewer is the reviewer every test in this file drives instead of a
// model. It records what it was handed, so the request can be asserted on
// without a CLI, a network, or a prompt.
type fakeReviewer struct {
	note ReviewNote
	err  error
	seen []ReviewRequest
}

func (f *fakeReviewer) ReviewDivergence(_ context.Context, req ReviewRequest) (ReviewNote, error) {
	f.seen = append(f.seen, req)
	return f.note, f.err
}

// The seam is satisfiable by something that knows nothing about an LLM. This
// assertion is COMPILE-TIME on purpose: it holds whether or not any test below
// runs, and it is the form that fails at build time if the interface's shape
// drifts from what the package's own consumers implement.
var _ DivergenceReviewer = (*fakeReviewer)(nil)

// TestDivergenceReviewerSeam pins the shape design.md fixes for the seam: what a
// reviewer is handed, what it gives back, and what an unfilled answer means.
//
// _Requirements: R5.2, R5.3, R5.4_
func TestDivergenceReviewerSeam(t *testing.T) {
	t.Run("a reviewer receives the package, the version and both files", func(t *testing.T) {
		rev := &fakeReviewer{note: ReviewNote{
			Origin:      OriginOverlay,
			Summary:     "adds a PaX marking step ::gentoo does not carry",
			Declaration: "patched = true",
		}}

		// Called through the INTERFACE, not through the concrete type: it is the
		// interface this package consumes, so it is the interface that must carry
		// every field.
		var reviewer DivergenceReviewer = rev
		req := ReviewRequest{
			Category: "net-libs",
			Package:  "nodejs",
			Version:  "26.7.0",
			Ours:     []byte("ours\n"),
			Theirs:   []byte("theirs\n"),
		}
		note, err := reviewer.ReviewDivergence(context.Background(), req)
		if err != nil {
			t.Fatalf("ReviewDivergence returned %v, want nil", err)
		}

		if len(rev.seen) != 1 {
			t.Fatalf("the reviewer saw %d requests, want 1", len(rev.seen))
		}
		got := rev.seen[0]
		if got.Category != req.Category || got.Package != req.Package || got.Version != req.Version {
			t.Errorf("the reviewer saw %s/%s-%s, want %s/%s-%s",
				got.Category, got.Package, got.Version, req.Category, req.Package, req.Version)
		}
		if string(got.Ours) != "ours\n" || string(got.Theirs) != "theirs\n" {
			t.Errorf("the reviewer saw ours=%q theirs=%q; the two ebuilds' BYTES are what a review reads, and they must arrive unswapped",
				got.Ours, got.Theirs)
		}

		if note.Origin != OriginOverlay {
			t.Errorf("note.Origin = %v, want OriginOverlay", note.Origin)
		}
		if note.Summary != rev.note.Summary {
			t.Errorf("note.Summary = %q, want %q", note.Summary, rev.note.Summary)
		}
		if note.Declaration != rev.note.Declaration {
			t.Errorf("note.Declaration = %q, want %q", note.Declaration, rev.note.Declaration)
		}
	})

	t.Run("the zero note claims nothing about anybody", func(t *testing.T) {
		// The same argument AuthorshipUnproved already makes as the zero value: a
		// result nobody filled must read as "nothing was said", never as a finding.
		// If OriginOverlay were the zero value, every un-reviewed finding would
		// assert the divergence is OURS — and R5.4 attaches a proposed `patched`
		// declaration to exactly that classification.
		var note ReviewNote

		if note.Origin != OriginUnknown {
			t.Errorf("the zero ReviewNote carries Origin %v; the zero value must be OriginUnknown, or an answer nobody gave reads as a claim", note.Origin)
		}
		if OriginUnknown != ReviewOrigin(0) {
			t.Errorf("OriginUnknown is %d, not 0; only the zero constant can protect a note nobody filled", OriginUnknown)
		}
		if note.Summary != "" || note.Declaration != "" {
			t.Errorf("the zero ReviewNote carries Summary %q and Declaration %q, want both empty", note.Summary, note.Declaration)
		}
	})

	t.Run("each origin has its own word", func(t *testing.T) {
		// The words are the vocabulary two later sub-tasks share: 6.4 renders one
		// beside the finding and 6.2 stores one in the cache. Pinning them here
		// keeps a rename from silently changing what a persisted note means.
		for _, c := range []struct {
			origin ReviewOrigin
			want   string
		}{
			{OriginUnknown, "unknown"},
			{OriginOverlay, "overlay"},
			{OriginUpstream, "upstream"},
			{OriginBoth, "both"},
		} {
			if got := c.origin.String(); got != c.want {
				t.Errorf("ReviewOrigin(%d).String() = %q, want %q", c.origin, got, c.want)
			}
		}

		// Four distinct values, so no classification can be mistaken for another.
		words := map[string]bool{}
		for _, o := range []ReviewOrigin{OriginUnknown, OriginOverlay, OriginUpstream, OriginBoth} {
			if words[o.String()] {
				t.Errorf("two origins share the word %q", o.String())
			}
			words[o.String()] = true
		}

		// A fifth origin added without a word here reads as "unknown" rather than
		// as a bare integer — the same fallback Verdict.String() and
		// CompareStatus.String() take, and the only one that stays honest: a number
		// printed beside a finding claims a classification nobody defined.
		if got := ReviewOrigin(99).String(); got != "unknown" {
			t.Errorf("an origin with no word renders as %q, want \"unknown\"", got)
		}
	})
}

// ---------------------------------------------------------------------------
// The import fence.
//
// This half of the file is a FENCE, not a behaviour test: it asserts on this
// package's own source rather than on anything the package does at run time,
// exactly as compare_diff_counts_fence_test.go does for the diff counts. The two
// are separate scanners on purpose — one reads selector expressions, the other
// reads import specs — because they hold two independent prohibitions, and a
// shared walker would couple them.
//
// 025 R2.4 keeps internal/overlay and internal/autoupdate free of an import edge
// in BOTH directions, and review.go is where that could quietly break: it
// declares a seam whose only production implementation is an adapter over
// autoupdate.NewClaudeCodeClient. That adapter lives in cmd/, which already
// imports both halves. Nothing here may reach for it directly.
// ---------------------------------------------------------------------------

// autoupdateImportPath is the substring that identifies the forbidden edge. It
// is matched as a SUBSTRING so a future internal/autoupdate/... subpackage is
// caught by the same rule as the package itself.
const autoupdateImportPath = "internal/autoupdate"

// packageImport is one import this package's source declares: the path and where
// it is written, so a violation can be pointed at rather than merely announced.
type packageImport struct {
	path string
	pos  token.Position
}

// collectImports returns every import path one parsed file declares.
func collectImports(file *ast.File, fset *token.FileSet) []packageImport {
	found := make([]packageImport, 0, len(file.Imports))
	for _, spec := range file.Imports {
		// An import path is a quoted string literal. A path that will not unquote
		// is kept VERBATIM rather than dropped: a fence must not lose an import it
		// merely failed to tidy.
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			path = spec.Path.Value
		}
		found = append(found, packageImport{path: path, pos: fset.Position(spec.Path.Pos())})
	}
	return found
}

// forbiddenAutoupdateImports keeps the imports the fence refuses.
//
// This is the fence's whole judgement, factored out so the scan of the real
// package and the scan of the probe source below are judged by the same code. A
// probe that exercised a second, parallel copy would prove something about that
// copy and nothing about the fence guarding the package.
func forbiddenAutoupdateImports(imports []packageImport) []packageImport {
	var forbidden []packageImport
	for _, imp := range imports {
		if strings.Contains(imp.path, autoupdateImportPath) {
			forbidden = append(forbidden, imp)
		}
	}
	return forbidden
}

// packageImportsInPackage parses this package's non-test sources and returns
// every import they declare.
//
// _test.go files are excluded because the gate this mirrors excludes them:
// `go list -deps ./internal/overlay` reports the imports of the SHIPPED package,
// which is the edge 025 R2.4 is about. A test file is still not a licence to
// import autoupdate — it would put the edge in the test binary — but that is a
// review matter, not something this scan claims to cover.
func packageImportsInPackage(t *testing.T) []packageImport {
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

	var imports []packageImport
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
		// result a compliant package yields, so the fence would report the absence
		// of an edge it never actually looked for.
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.SkipObjectResolution|parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		imports = append(imports, collectImports(file, fset)...)
	}
	// Same argument one level up: a scan that found no files to read is not a
	// package with nothing to hide.
	if sources == 0 {
		t.Fatalf("no non-test Go source found in %q; the scan would pass vacuously", pkgDir)
	}
	return imports
}

// importsInSource runs the same collector over one in-memory source, so the
// detector can be proved to detect before it is trusted to prove an absence.
func importsInSource(t *testing.T, name, src string) []packageImport {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution|parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse probe source: %v", err)
	}
	return collectImports(file, fset)
}

// TestOverlayImportsNoAutoupdate is 025 R2.4 held mechanically on this side of
// the edge: internal/overlay names no internal/autoupdate symbol, so it cannot
// come to depend on one.
//
// _Requirements: R5.2, R5.3, R5.4 — the seam that makes the absence possible._
func TestOverlayImportsNoAutoupdate(t *testing.T) {
	imports := packageImportsInPackage(t)

	for _, imp := range forbiddenAutoupdateImports(imports) {
		t.Errorf("internal/overlay imports %q at %s.\n"+
			"025 R2.4 keeps this edge absent in BOTH directions. The reviewer reaches this package as the "+
			"DivergenceReviewer interface it declares itself; the adapter over autoupdate.NewClaudeCodeClient belongs in "+
			"cmd/bentoo/overlay_compare_review.go, which already imports both halves.", imp.path, imp.pos)
	}

	// A scan that matched nothing also reports a clean result. The package is
	// asserted to import SOMETHING, so a green above is evidence the scan reached
	// real source rather than evidence the walk is broken.
	if len(imports) == 0 {
		t.Fatal("the scan found no imports at all in internal/overlay's non-test sources; a clean result proves nothing")
	}
	t.Logf("scanned %d imports across internal/overlay's non-test sources", len(imports))
}

// autoupdateImportProbe is the source the fence is tested against.
//
// It is a STRING and never a file in this package, because a real import of
// autoupdate would fail the scan above — that scan is the assertion, not
// somewhere to put a fixture. Nothing here has to compile or type-check: the
// fence reads syntax.
const autoupdateImportProbe = `package overlay

import (
	"context"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// The exact shape 025 R2.4 forbids: the overlay package reaching for the CLI
// client directly instead of consuming the seam it declares itself.
func newReviewer(ctx context.Context) (autoupdate.LLMProvider, error) {
	_ = provider.ErrNotFound
	return autoupdate.NewClaudeCodeClient()
}
`

// TestOverlayImportFenceDetectsAViolation proves the fence bites.
//
// The scan above passes today because the package is compliant, and it would
// pass identically if the walk were broken — a detector that finds nothing and a
// package that imports nothing forbidden produce the same green. This runs the
// same collector and the same judgement over a source that IS in violation, so
// the green next door means something.
func TestOverlayImportFenceDetectsAViolation(t *testing.T) {
	imports := importsInSource(t, "probe.go", autoupdateImportProbe)
	forbidden := forbiddenAutoupdateImports(imports)

	if len(forbidden) != 1 {
		t.Fatalf("the fence reports %d forbidden imports in the probe, want 1; an import edge to internal/autoupdate would land unnoticed", len(forbidden))
	}
	if !strings.Contains(forbidden[0].path, autoupdateImportPath) {
		t.Errorf("the fence reported %q, which is not the forbidden path", forbidden[0].path)
	}
	if forbidden[0].pos.Filename != "probe.go" || forbidden[0].pos.Line == 0 {
		t.Errorf("the violation carries position %q; an edge the report cannot locate is one nobody can fix", forbidden[0].pos)
	}

	// The judgement is a FILTER, not a blanket: the probe's other two imports are
	// ordinary and must pass. A fence that flagged every import would also "pass"
	// the test above by flagging nothing in a package that imports nothing.
	if len(imports) != 3 {
		t.Fatalf("the collector found %d imports in the probe, want 3; it is not reading the whole import block", len(imports))
	}
}

// ---------------------------------------------------------------------------
// AnnotateReviews — the pass that attaches a model's reading to each undeclared
// divergence, AFTER the comparison has finished.
// ---------------------------------------------------------------------------

// AnnotateReviews returns NOTHING, and that is the contract rather than an
// oversight — the same one AnnotateAuthorship holds next door. Every failure it
// can meet is a way of having no reading: a reviewer that errored, one that ran
// out of time, one that answered with nothing usable. design.md's error table
// gives all of them the same answer — one warning, then the deterministic report
// (R5.5), which is already complete without the commentary. A signature with no
// error value is how that is held: there is nothing for a caller to drop, and an
// edit that added one would stop compiling here.
var _ func(*CompareReport, DivergenceReviewer, provider.Provider, CompareOptions) = AnnotateReviews

// annotateReviewer is the reviewer the AnnotateReviews cases drive.
//
// It is a SECOND double beside fakeReviewer above, for the reason
// authorshipProvider is a second double beside localRootedFakeProvider: two of
// its behaviours are ASSERTIONS rather than configuration.
//
//   - offLimits fails the test when the pass submits a package it must never
//     submit. A package that carries no undeclared divergence ends the run with
//     the zero note whether it was skipped or reviewed and discarded, so "never
//     submitted" has to be asserted where the question would be ASKED.
//   - seenCtx records the context each call arrives with, which is the only way
//     to see that the caller's cancellation spine reached the reviewer rather
//     than a fresh context.Background().
type annotateReviewer struct {
	t         *testing.T
	note      ReviewNote
	err       error
	answer    func(ReviewRequest) (ReviewNote, error)
	offLimits map[string]bool

	calls   []ReviewRequest
	seenCtx []context.Context
}

func (r *annotateReviewer) ReviewDivergence(ctx context.Context, req ReviewRequest) (ReviewNote, error) {
	atom := req.Category + "/" + req.Package
	if r.offLimits[atom] {
		r.t.Errorf("the review pass submitted %s, which carries no undeclared divergence. R5.1 submits the findings the "+
			"report already calls undeclared; anything else is a `claude` invocation — seconds of wall clock — bought for a "+
			"note nothing would print", atom)
	}
	r.calls = append(r.calls, req)
	r.seenCtx = append(r.seenCtx, ctx)
	if r.answer != nil {
		return r.answer(req)
	}
	return r.note, r.err
}

// atoms returns the packages submitted, in submission order. ORDER is asserted
// rather than membership: the pass runs outside CompareWithProvider's ten
// goroutines precisely so the sequence is the report's own and not whichever
// review finished first.
func (r *annotateReviewer) atoms() []string {
	seen := make([]string, len(r.calls))
	for i, req := range r.calls {
		seen[i] = req.Category + "/" + req.Package
	}
	return seen
}

var _ DivergenceReviewer = (*annotateReviewer)(nil)

// The five packages of the fixture below, as two ebuilds each. Every pair is
// distinct so each is its own cache entry; the identical-pair case builds its
// own tree, since sharing one note is exactly what it is about.
const (
	// The live shape of a divergence that is ours: a PATCHES= block on top of an
	// otherwise untouched ebuild (kde-plasma/spectacle-6.7.4).
	reviewSpectacleOurs   = "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n"
	reviewSpectacleTheirs = "EAPI=8\ninherit ecm\n"
	// The live shape of one that probably is not: a single line ::gentoo revised
	// in place, with no revbump (kde-plasma/kwin-6.7.4).
	reviewKwinOurs      = "EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n"
	reviewKwinTheirs    = "EAPI=8\nPYTHON_COMPAT=( python3_{12..15} )\ninherit ecm\n"
	reviewIdenticalBody = "EAPI=8\nDESCRIPTION=\"byte-identical on both sides\"\n"
	reviewOutdatedBody  = "EAPI=8\nDESCRIPTION=\"two versions apart, so never content-compared\"\n"
)

// reviewedAtoms is the whole set R5.1 submits, in the order the report holds
// them: the two packages whose ebuilds differ and whose divergence no entry
// declares.
var reviewedAtoms = []string{"kde-plasma/kwin", "kde-plasma/spectacle"}

// unreviewableAtoms are the three that must never reach a reviewer, one per
// reason a finding can fail to be an undeclared divergence.
var unreviewableAtoms = map[string]bool{
	"app-editors/zed":   true, // the ebuilds differ, but an entry declares why
	"dev-libs/libixion": true, // the two ebuilds are byte-identical
	"net-libs/nodejs":   true, // the versions differ, so no content was compared
}

// reviewFixtureIn compares a five-package tree and returns the finished report
// together with the provider and options it was produced from, with the note
// cache pointed at cacheDir for the duration of the test.
//
// Redirecting the cache is not optional plumbing. defaultReviewCacheDir names
// ~/.cache/bentoo/compare, so a suite that did not redirect it would read and
// write the developer's real cache — and one run's stored note would then answer
// the next run's assertion about how many times a reviewer was called.
//
// The report comes from the REAL comparison rather than being hand-built,
// because every case below turns on which results carry an undeclared
// divergence, and that is exactly what a hand-built fixture is free to get
// wrong. The shape it must have is asserted here, once, so a drift in the
// comparison fails as a fixture error instead of quietly emptying the set under
// test.
func reviewFixtureIn(t *testing.T, cacheDir string) (*CompareReport, provider.Provider, CompareOptions) {
	t.Helper()

	previous := reviewCacheDirFor
	reviewCacheDirFor = func() (string, error) { return cacheDir, nil }
	t.Cleanup(func() { reviewCacheDirFor = previous })

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4", reviewSpectacleOurs)
	writeVerifyEbuild(t, upstreamRoot, "kde-plasma", "spectacle", "6.7.4", reviewSpectacleTheirs)
	writeVerifyEbuild(t, overlayRoot, "kde-plasma", "kwin", "6.7.4", reviewKwinOurs)
	writeVerifyEbuild(t, upstreamRoot, "kde-plasma", "kwin", "6.7.4", reviewKwinTheirs)
	writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
	writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
	writeVerifyEbuild(t, overlayRoot, "dev-libs", "libixion", "1.0", reviewIdenticalBody)
	writeVerifyEbuild(t, upstreamRoot, "dev-libs", "libixion", "1.0", reviewIdenticalBody)
	writeVerifyEbuild(t, overlayRoot, "net-libs", "nodejs", "1.0", reviewOutdatedBody)
	writeVerifyEbuild(t, upstreamRoot, "net-libs", "nodejs", "2.0", reviewOutdatedBody)

	prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{
		"kde-plasma/spectacle": {"6.7.4"},
		"kde-plasma/kwin":      {"6.7.4"},
		"app-editors/zed":      {"1.0"},
		"dev-libs/libixion":    {"1.0"},
		"net-libs/nodejs":      {"2.0"},
	}}
	// ONE options value for the comparison and the pass, exactly as runCompare
	// passes it — which is what lets the pass resolve the same two files the
	// comparison read.
	opts := CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence: map[string]Divergence{
			"kde-plasma/spectacle": {},
			"kde-plasma/kwin":      {},
			"app-editors/zed":      {Patched: true, Reason: "keeps our wayland patch", Entry: zedPatchedEntry},
			"dev-libs/libixion":    {},
			"net-libs/nodejs":      {},
		},
	}

	report, err := CompareWithProvider([]PackageInfo{
		{Category: "kde-plasma", Package: "spectacle", LatestVersion: "6.7.4"},
		{Category: "kde-plasma", Package: "kwin", LatestVersion: "6.7.4"},
		{Category: "app-editors", Package: "zed", LatestVersion: "1.0"},
		{Category: "dev-libs", Package: "libixion", LatestVersion: "1.0"},
		{Category: "net-libs", Package: "nodejs", LatestVersion: "1.0"},
	}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != 5 {
		t.Fatalf("the fixture produced %d results, want 5", len(report.Results))
	}

	var undeclared []string
	for _, r := range report.Results {
		if r.Verified == VerifiedDiffers && !r.Patched {
			undeclared = append(undeclared, r.Category+"/"+r.Package)
		}
	}
	if !reflect.DeepEqual(undeclared, reviewedAtoms) {
		t.Fatalf("the fixture holds undeclared divergences for %v, want %v; every case below is about that set",
			undeclared, reviewedAtoms)
	}

	return report, prov, opts
}

func reviewFixture(t *testing.T) (*CompareReport, provider.Provider, CompareOptions) {
	t.Helper()
	return reviewFixtureIn(t, t.TempDir())
}

// resultFor returns the one result naming atom.
func resultFor(t *testing.T, report *CompareReport, atom string) CompareResult {
	t.Helper()
	for _, r := range report.Results {
		if r.Category+"/"+r.Package == atom {
			return r
		}
	}
	t.Fatalf("no result for %s in the report", atom)
	return CompareResult{}
}

// cloneReport copies a report deeply enough to compare it against itself later:
// Results is the only reference field, and CompareResult is all values.
func cloneReport(report *CompareReport) *CompareReport {
	clone := *report
	clone.Results = append([]CompareResult(nil), report.Results...)
	return &clone
}

// withoutReviews returns a copy with every commentary field cleared, so two runs
// can be compared on EVERYTHING ELSE — the grouping, every Verdict, every count
// (R5.8). Zeroing the one field the pass writes is what makes the comparison an
// assertion about the other twenty rather than a tautology.
func withoutReviews(report *CompareReport) *CompareReport {
	clone := cloneReport(report)
	for i := range clone.Results {
		clone.Results[i].Review = ReviewNote{}
	}
	return clone
}

// warningsNaming returns the captured warnings that name atom.
func warningsNaming(lines []string, atom string) []string {
	var found []string
	for _, line := range lines {
		if strings.Contains(line, atom) {
			found = append(found, line)
		}
	}
	return found
}

// noteFor is a distinguishable note per package, so an assertion cannot pass by
// finding SOME note where it wanted a particular one.
func noteFor(atom string) ReviewNote {
	return ReviewNote{
		Origin:      OriginOverlay,
		Summary:     "what the difference in " + atom + " does",
		Declaration: "patched = true # " + atom,
	}
}

// TestAnnotateReviews is 6.3: every undeclared divergence carries the model's
// reading, nothing else is submitted at all, and every way of not getting one
// costs a warning and nothing more.
//
// The load-bearing property is the one asserted last and again in 6.6: NOTHING a
// reviewer returns can change what the report decides (R5.8). Commentary is
// commentary — the grouping, the Verdicts and the counts are the same whether a
// model spoke or not.
//
// _Requirements: R5.1, R5.5, R5.8_
func TestAnnotateReviews(t *testing.T) {
	t.Run("every undeclared divergence carries the model's reading, and nothing else is asked", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		rev := &annotateReviewer{
			t:         t,
			offLimits: unreviewableAtoms,
			answer: func(req ReviewRequest) (ReviewNote, error) {
				return noteFor(req.Category + "/" + req.Package), nil
			},
		}

		AnnotateReviews(report, rev, prov, opts)

		// ORDER, not membership. The pass runs after CompareWithProvider returns
		// rather than inside its ten goroutines, so the sequence is the report's
		// own — sorted by category/package — and not whichever review finished
		// first. Ten concurrent `claude` invocations would also be ten processes.
		if !reflect.DeepEqual(rev.atoms(), reviewedAtoms) {
			t.Errorf("the pass submitted %v, want exactly %v in that order", rev.atoms(), reviewedAtoms)
		}

		for _, atom := range reviewedAtoms {
			got := resultFor(t, report, atom).Review
			if got != noteFor(atom) {
				t.Errorf("%s carries %+v, want %+v", atom, got, noteFor(atom))
			}
		}
		// The other three keep the zero note: nothing was said about them, which
		// is what OriginUnknown with two empty strings means.
		for atom := range unreviewableAtoms {
			if got := resultFor(t, report, atom).Review; got != (ReviewNote{}) {
				t.Errorf("%s carries the note %+v; a package no review reached must read as \"nothing was said\"", atom, got)
			}
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("an ordinary annotated run warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("the reviewer is handed the package and both ebuilds, ours first", func(t *testing.T) {
		captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}

		AnnotateReviews(report, rev, prov, opts)

		want := map[string][2]string{
			"kde-plasma/kwin":      {reviewKwinOurs, reviewKwinTheirs},
			"kde-plasma/spectacle": {reviewSpectacleOurs, reviewSpectacleTheirs},
		}
		for _, req := range rev.calls {
			atom := req.Category + "/" + req.Package
			pair, ok := want[atom]
			if !ok {
				t.Errorf("unexpected submission for %s", atom)
				continue
			}
			// The ORDER is load-bearing: ReviewNote.Origin names a SIDE, so a
			// request with the two swapped inverts the answer — and the cache
			// fingerprint does not commute either.
			if string(req.Ours) != pair[0] || string(req.Theirs) != pair[1] {
				t.Errorf("%s was submitted as ours=%q theirs=%q, want ours=%q theirs=%q",
					atom, req.Ours, req.Theirs, pair[0], pair[1])
			}
			if req.Version != resultFor(t, report, atom).LocalVersion {
				t.Errorf("%s was submitted at version %q, want %q", atom, req.Version, resultFor(t, report, atom).LocalVersion)
			}
		}
	})

	t.Run("a nil reviewer changes nothing at all", func(t *testing.T) {
		// The single no-op path `--no-review` (R5.6) and an absent `claude` CLI
		// (R5.5) both reach. "Nothing crashed" is not the assertion: the report is
		// compared field for field, and the cache directory is asserted untouched,
		// which is what proves the pass returns BEFORE it opens anything.
		warnings := captureReviewWarnings(t)
		cacheDir := t.TempDir()
		report, prov, opts := reviewFixtureIn(t, cacheDir)
		before := cloneReport(report)

		AnnotateReviews(report, nil, prov, opts)

		if !reflect.DeepEqual(report, before) {
			t.Errorf("a nil reviewer changed the report.\n got %+v\nwant %+v", report, before)
		}
		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			t.Fatalf("read the cache directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("a nil reviewer left %d files in the cache directory; the no-op path must return before it opens one", len(entries))
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("a nil reviewer warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("an erroring reviewer yields today's report plus one warning per package", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		reference := cloneReport(report)
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: errors.New("claude: exit status 1")}

		AnnotateReviews(report, rev, prov, opts)

		// R5.5: the report the operator asked for is already complete without the
		// commentary, so a failed review leaves it byte for byte as it was.
		if !reflect.DeepEqual(report, reference) {
			t.Errorf("an erroring reviewer changed the report.\n got %+v\nwant %+v", report, reference)
		}
		// One warning NAMING THE PACKAGE, per package. It is not once per run: two
		// packages that failed for two reasons are two things the operator may want
		// to look at, and warnLogf carries no once-guard of its own.
		if lines := warnings(); len(lines) != len(reviewedAtoms) {
			t.Errorf("two failed reviews produced %d warnings, want %d: %v", len(lines), len(reviewedAtoms), lines)
		}
		for _, atom := range reviewedAtoms {
			if n := len(warningsNaming(warnings(), atom)); n != 1 {
				t.Errorf("%s is named by %d warnings, want exactly 1: %v", atom, n, warnings())
			}
		}
	})

	t.Run("a timeout and an unusable answer fail exactly like an error", func(t *testing.T) {
		// R5.5 lists four failures and gives them one answer. They are asserted
		// together because they must be indistinguishable in the report: the
		// operator gets the deterministic report and one warning, whichever it was.
		//
		// "Unusable" is the shape a model reply takes when it parsed but said
		// nothing: no classification (R5.2 unanswered) or no summary (R5.3
		// unanswered). Attaching one would print a finding-shaped line stating
		// nothing, and — with no expiry on the cache — would keep printing it.
		for _, c := range []struct {
			name string
			note ReviewNote
			err  error
		}{
			{"the model ran out of time", ReviewNote{}, fmt.Errorf("claude: %w", context.DeadlineExceeded)},
			{"the model classified nothing", ReviewNote{Summary: "something happened"}, nil},
			{"the model summarised nothing", ReviewNote{Origin: OriginOverlay, Summary: "   "}, nil},
			{"the model returned an empty answer", ReviewNote{}, nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				warnings := captureReviewWarnings(t)
				cacheDir := t.TempDir()
				report, prov, opts := reviewFixtureIn(t, cacheDir)
				reference := cloneReport(report)
				rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: c.note, err: c.err}

				AnnotateReviews(report, rev, prov, opts)

				if !reflect.DeepEqual(report, reference) {
					t.Errorf("the report changed.\n got %+v\nwant %+v", report, reference)
				}
				if lines := warnings(); len(lines) != len(reviewedAtoms) {
					t.Errorf("produced %d warnings, want %d (one per package): %v", len(lines), len(reviewedAtoms), lines)
				}

				// AND IT IS NOT CACHED. A cache has no expiry — the key is the two
				// files' content — so a stored non-answer would suppress the question
				// for as long as neither ebuild changed. A second pass must ask again.
				second, prov2, opts2 := reviewFixtureIn(t, cacheDir)
				again := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: c.note, err: c.err}
				AnnotateReviews(second, again, prov2, opts2)
				if len(again.calls) != len(reviewedAtoms) {
					t.Errorf("the second run asked %d times, want %d; a failed review was cached and the question is now permanently suppressed",
						len(again.calls), len(reviewedAtoms))
				}
			})
		}
	})

	t.Run("a cache hit issues no second call", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		cacheDir := t.TempDir()

		report, prov, opts := reviewFixtureIn(t, cacheDir)
		first := &annotateReviewer{t: t, offLimits: unreviewableAtoms, answer: func(req ReviewRequest) (ReviewNote, error) {
			return noteFor(req.Category + "/" + req.Package), nil
		}}
		AnnotateReviews(report, first, prov, opts)
		if len(first.calls) != len(reviewedAtoms) {
			t.Fatalf("the first run asked %d times, want %d", len(first.calls), len(reviewedAtoms))
		}

		// A second run over the same two trees: same ebuilds, same content, same
		// question. It must be answered from the cache and reach no reviewer.
		second, prov2, opts2 := reviewFixtureIn(t, cacheDir)
		silent := &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: errors.New("a cached run must not ask")}
		AnnotateReviews(second, silent, prov2, opts2)

		if len(silent.calls) != 0 {
			t.Errorf("the second run submitted %v; a note cached on the two files' content answers it", silent.atoms())
		}
		for _, atom := range reviewedAtoms {
			if got := resultFor(t, second, atom).Review; got != noteFor(atom) {
				t.Errorf("%s came back from the cache as %+v, want %+v", atom, got, noteFor(atom))
			}
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("two clean runs warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("two packages with byte-identical ebuild pairs are reviewed once", func(t *testing.T) {
		// R5.7 keys a note on the two files' CONTENT and excludes the atom, so two
		// packages whose ebuilds happen to match share one answer. It cannot happen
		// on the live overlay, and it is asserted here because it is what the
		// commentary's wording has to survive: prose about the DIFFERENCE stays true
		// under both atoms, prose about the package does not.
		warnings := captureReviewWarnings(t)
		previous := reviewCacheDirFor
		cacheDir := t.TempDir()
		reviewCacheDirFor = func() (string, error) { return cacheDir, nil }
		t.Cleanup(func() { reviewCacheDirFor = previous })

		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		for _, pkg := range []string{"alpha", "beta"} {
			writeVerifyEbuild(t, overlayRoot, "dev-libs", pkg, "1.0", reviewSpectacleOurs)
			writeVerifyEbuild(t, upstreamRoot, "dev-libs", pkg, "1.0", reviewSpectacleTheirs)
		}
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{
			"dev-libs/alpha": {"1.0"},
			"dev-libs/beta":  {"1.0"},
		}}
		opts := CompareOptions{
			IncludeSynced: true,
			OverlayPath:   overlayRoot,
			Divergence:    map[string]Divergence{"dev-libs/alpha": {}, "dev-libs/beta": {}},
		}
		report, err := CompareWithProvider([]PackageInfo{
			{Category: "dev-libs", Package: "alpha", LatestVersion: "1.0"},
			{Category: "dev-libs", Package: "beta", LatestVersion: "1.0"},
		}, prov, opts)
		if err != nil {
			t.Fatalf("CompareWithProvider returned %v, want nil", err)
		}

		rev := &annotateReviewer{t: t, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		if len(rev.calls) != 1 {
			t.Errorf("the pass asked %d times for two identical differences, want 1", len(rev.calls))
		}
		for _, atom := range []string{"dev-libs/alpha", "dev-libs/beta"} {
			if got := resultFor(t, report, atom).Review; got != reviewFixtureNote() {
				t.Errorf("%s carries %+v, want the shared note %+v", atom, got, reviewFixtureNote())
			}
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("this subtest warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("the ebuilds are re-read, not remembered from the comparison", func(t *testing.T) {
		// The pass reads both files again through resolvePackagePaths rather than
		// carrying buffers on every CompareResult — eight findings on the measured
		// overlay, so the re-read costs nothing. Proved by CHANGING one file between
		// the comparison and the pass: a request built from remembered bytes would
		// carry the old text.
		captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		const edited = "EAPI=8\ninherit ecm\n# edited after the comparison\n"
		writeVerifyEbuild(t, opts.OverlayPath, "kde-plasma", "spectacle", "6.7.4", edited)

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		for _, req := range rev.calls {
			if req.Package != "spectacle" {
				continue
			}
			if string(req.Ours) != edited {
				t.Errorf("spectacle was submitted with %q, want the file as it is on disk now (%q); the pass is not re-reading it",
					req.Ours, edited)
			}
		}
	})

	t.Run("an ebuild that cannot be re-read is left un-annotated, with one warning", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		gone := filepath.Join(opts.OverlayPath, "kde-plasma", "spectacle", "spectacle-6.7.4.ebuild")
		if err := os.Remove(gone); err != nil {
			t.Fatalf("remove the overlay ebuild: %v", err)
		}

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		if atoms := rev.atoms(); !reflect.DeepEqual(atoms, []string{"kde-plasma/kwin"}) {
			t.Errorf("the pass submitted %v, want only kde-plasma/kwin; a package whose files cannot be read has no difference to submit", atoms)
		}
		if got := resultFor(t, report, "kde-plasma/spectacle").Review; got != (ReviewNote{}) {
			t.Errorf("spectacle carries %+v after its ebuild vanished, want the zero note", got)
		}
		if n := len(warningsNaming(warnings(), "kde-plasma/spectacle")); n != 1 {
			t.Errorf("the vanished ebuild produced %d warnings naming the package, want exactly 1: %v", n, warnings())
		}
		// The rest of the report is unaffected: one package's missing file is not
		// the run's problem.
		if got := resultFor(t, report, "kde-plasma/kwin").Review; got != reviewFixtureNote() {
			t.Errorf("kwin carries %+v, want the reviewer's note; one unreadable package stopped the pass", got)
		}
	})

	t.Run("the caller's context reaches the reviewer", func(t *testing.T) {
		// A review is a network round trip behind a CLI, so a cancelled compare must
		// abort it rather than hold the run open. The spine is CompareOptions.Ctx —
		// the same one the comparison itself carries — and a value planted on it is
		// how the test sees that spine arrive rather than a fresh Background.
		captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		type ctxKey struct{}
		opts.Ctx = context.WithValue(context.Background(), ctxKey{}, "the caller's")

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		if len(rev.seenCtx) != len(reviewedAtoms) {
			t.Fatalf("the pass made %d calls, want %d", len(rev.seenCtx), len(reviewedAtoms))
		}
		for i, ctx := range rev.seenCtx {
			if ctx == nil {
				t.Fatalf("call %d arrived with a nil context", i)
			}
			if ctx.Value(ctxKey{}) != "the caller's" {
				t.Errorf("call %d arrived with a context that is not the caller's; a cancelled compare could not abort it", i)
			}
		}
	})

	t.Run("a cancelled run stops asking and says so once", func(t *testing.T) {
		// Ctrl-C during a compare. Every remaining reviewer call would fail on the
		// same cancelled context, so eight identical warnings would bury the report
		// the operator is about to get: the pass stops and states it once.
		warnings := captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		opts.Ctx = ctx

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		if len(rev.calls) != 0 {
			t.Errorf("a cancelled run still submitted %v", rev.atoms())
		}
		if lines := warnings(); len(lines) != 1 {
			t.Errorf("a cancelled run warned %d times, want exactly 1: %v", len(lines), lines)
		}
		for _, atom := range reviewedAtoms {
			if got := resultFor(t, report, atom).Review; got != (ReviewNote{}) {
				t.Errorf("%s carries %+v after a cancelled run, want the zero note", atom, got)
			}
		}
	})

	t.Run("nothing the reviewer returns changes a Verdict or a count", func(t *testing.T) {
		// R5.8, asserted here on the STRUCTURE and again in 6.6 on the rendered
		// report. The reviewer answers with the most decisive-looking note it can —
		// every divergence is ours, every one has a declaration to write — and the
		// grouping, the Verdicts and the counts are the same as with no reviewer at
		// all.
		captureReviewWarnings(t)
		reviewed, prov, opts := reviewFixture(t)
		unreviewed := cloneReport(reviewed)

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: ReviewNote{
			Origin:      OriginOverlay,
			Summary:     "this package must be kept",
			Declaration: "patched = true",
		}}
		AnnotateReviews(reviewed, rev, prov, opts)
		AnnotateReviews(unreviewed, nil, prov, opts)

		if !reflect.DeepEqual(withoutReviews(reviewed), unreviewed) {
			t.Errorf("a review changed something other than the commentary.\nreviewed (commentary stripped): %+v\nunreviewed: %+v",
				withoutReviews(reviewed), unreviewed)
		}
		// Spelled out as well, because DeepEqual on a struct that grew a field
		// silently starts covering it: these four are the numbers R5.8 names.
		if reviewed.VerdictKeepCount != unreviewed.VerdictKeepCount ||
			reviewed.VerdictRedundantCount != unreviewed.VerdictRedundantCount ||
			reviewed.VerdictNeedsRebaseCount != unreviewed.VerdictNeedsRebaseCount ||
			reviewed.VerdictUnknownCount != unreviewed.VerdictUnknownCount {
			t.Errorf("the Verdict counts moved: reviewed %d/%d/%d/%d, unreviewed %d/%d/%d/%d",
				reviewed.VerdictKeepCount, reviewed.VerdictRedundantCount, reviewed.VerdictNeedsRebaseCount, reviewed.VerdictUnknownCount,
				unreviewed.VerdictKeepCount, unreviewed.VerdictRedundantCount, unreviewed.VerdictNeedsRebaseCount, unreviewed.VerdictUnknownCount)
		}
		// And the annotation really happened, so the equality above is evidence of
		// impotence rather than of a pass that did nothing.
		if got := resultFor(t, reviewed, "kde-plasma/kwin").Review.Summary; got != "this package must be kept" {
			t.Errorf("the commentary never landed (%q), so the comparison above proves nothing", got)
		}
	})

	t.Run("a report with no undeclared divergence asks nothing and opens nothing", func(t *testing.T) {
		// R5.1's precondition. It is not an optimisation: a run whose findings are
		// all declared or all identical must not build a cache, so it cannot warn
		// about one either.
		warnings := captureReviewWarnings(t)
		cacheDir := t.TempDir()
		report, prov, opts := reviewFixtureIn(t, cacheDir)
		report.Results = nil

		rev := &annotateReviewer{t: t, err: errors.New("nothing should be asked")}
		AnnotateReviews(report, rev, prov, opts)

		if len(rev.calls) != 0 {
			t.Errorf("the pass submitted %v from a report with no findings", rev.atoms())
		}
		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			t.Fatalf("read the cache directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("the pass wrote %d files for a report with nothing to review", len(entries))
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("a report with nothing to review warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("a cache with nowhere to live says why, and the run carries on", func(t *testing.T) {
		// newReviewCache("") is deliberately silent: the caller that could not name
		// a directory is the one holding the reason, so the reason is stated HERE or
		// not at all.
		warnings := captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		previous := reviewCacheDirFor
		reviewCacheDirFor = func() (string, error) { return "", errors.New("no home directory") }
		t.Cleanup(func() { reviewCacheDirFor = previous })

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewFixtureNote()}
		AnnotateReviews(report, rev, prov, opts)

		if len(rev.calls) != len(reviewedAtoms) {
			t.Errorf("the pass asked %d times, want %d; an uncached run still reviews", len(rev.calls), len(reviewedAtoms))
		}
		for _, atom := range reviewedAtoms {
			if got := resultFor(t, report, atom).Review; got != reviewFixtureNote() {
				t.Errorf("%s carries %+v, want the reviewer's note", atom, got)
			}
		}
		if lines := warnings(); len(lines) != 1 {
			t.Errorf("an unusable cache directory warned %d times, want exactly 1: %v", len(lines), lines)
		}
	})
}

// ---------------------------------------------------------------------------
// The commentary, as the operator reads it (6.4).
// ---------------------------------------------------------------------------

// reviewedFinding is an undeclared-divergence finding carrying a note, built on
// the same helper the magnitude tests use so the commentary is asserted beneath
// the REAL finding line rather than beneath a hand-drawn approximation of one.
func reviewedFinding(pkg string, note ReviewNote) CompareResult {
	r := divergenceFinding(pkg, 1, 1, AuthorshipUnproved, "")
	r.Review = note
	return r
}

// isCommentaryLine reports whether one rendered line carries a model's words.
//
// It is the ONE definition of "this line is commentary", shared by the helper
// below and by stripCommentary (TestReviewChangesNothing). Spelled twice they
// would be two things to keep in step, and R5.8's whole assertion is that the
// report MINUS these lines is unchanged — so a second, looser definition would
// strip a line the report owns and call the difference commentary.
//
// A line qualifies only by carrying one of the two openings the renderer prints,
// never by carrying a model's text: text is what a mutation would move onto a
// row, and a predicate that recognised it there would excuse exactly the failure
// this is here to catch.
func isCommentaryLine(line string) bool {
	return strings.Contains(line, reviewReadingLead) || strings.Contains(line, reviewProposalLead)
}

// commentaryLines returns the rendered lines that carry a model's words.
func commentaryLines(out string) []string {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if isCommentaryLine(line) {
			found = append(found, line)
		}
	}
	return found
}

// treeSnapshot records every file under root with its contents, so a later
// comparison can prove that nothing on disk moved.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // path comes from walking the test's own temp tree
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// TestReviewCommentary is 6.4: the classification, the summary and — only where
// R5.4 asks for one — the proposed declaration, printed beneath the finding they
// are about.
//
// What every case here is really protecting is the boundary between what this
// tool STANDS BEHIND and what a model merely said. The finding above the
// commentary is the report's own: two files compared byte for byte, a registry
// consulted. The two lines below it are a guess by a language model. An operator
// who cannot tell them apart will act on the wrong one, and the one that invites
// an action — "declare this patched" — is the guess.
//
// _Requirements: R5.2, R5.3, R5.4_
func TestReviewCommentary(t *testing.T) {
	t.Run("an overlay-origin note renders the classification, the summary and the proposal", func(t *testing.T) {
		note := ReviewNote{
			Origin:      OriginOverlay,
			Summary:     "adds a PaX marking step ::gentoo does not carry",
			Declaration: "patched = true",
		}
		out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

		lines := commentaryLines(out)
		if len(lines) != 2 {
			t.Fatalf("an overlay-origin note rendered %d commentary lines, want 2 (the reading and the proposal):\n%s", len(lines), out)
		}
		// R5.2: which side, in the report's prose rather than in the wire word.
		// "upstream" is what the cache stores and what the adapter decodes;
		// "::gentoo" is what the operator calls it.
		if !strings.Contains(lines[0], reviewOriginProse(OriginOverlay)) {
			t.Errorf("the reading does not say where the divergence came from:\n%s", lines[0])
		}
		// R5.3.
		if !strings.Contains(lines[0], note.Summary) {
			t.Errorf("the reading does not carry the summary:\n%s", lines[0])
		}
		// R5.4.
		if !strings.Contains(lines[1], note.Declaration) {
			t.Errorf("the proposal does not carry the declaration text:\n%s", lines[1])
		}
		// The provenance, on the line itself. Without it the commentary reads as a
		// second finding of the report's own.
		if !strings.Contains(lines[0], "model") {
			t.Errorf("the reading never says a model produced it, so it reads as a finding this report stands behind:\n%s", lines[0])
		}
	})

	t.Run("only an overlay-origin note proposes a declaration", func(t *testing.T) {
		// R5.4 names ONE classification. `both` is deliberately not it: a copy that
		// carries work of ours AND has fallen behind ::gentoo needs the rebase
		// first, and declaring `patched` on it would record the whole difference as
		// intentional and suppress its removal recommendation permanently — for the
		// half that is merely stale. The declaration is refused HERE as well as left
		// empty by the type (ReviewNote.Declaration), so a model that fills it in
		// anyway still cannot get it printed.
		for _, c := range []struct {
			origin  ReviewOrigin
			propose bool
		}{
			{OriginOverlay, true},
			{OriginUpstream, false},
			{OriginBoth, false},
		} {
			t.Run(c.origin.String(), func(t *testing.T) {
				note := ReviewNote{
					Origin:      c.origin,
					Summary:     "what the difference does",
					Declaration: "patched = true",
				}
				out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

				if !strings.Contains(out, reviewOriginProse(c.origin)) {
					t.Errorf("the reading does not state the %s classification:\n%s", c.origin, out)
				}
				proposed := strings.Contains(out, reviewProposalLead)
				if proposed != c.propose {
					t.Errorf("a %s-origin note proposes a declaration = %v, want %v:\n%s", c.origin, proposed, c.propose, out)
				}
				if !c.propose && strings.Contains(out, note.Declaration) {
					t.Errorf("a %s-origin note printed the declaration text anyway:\n%s", c.origin, out)
				}
			})
		}
	})

	t.Run("an un-reviewed finding renders no commentary", func(t *testing.T) {
		// The zero note is every run without a review: `--no-review`, no `claude`
		// on PATH, a reviewer that errored. It must be invisible, not a blank line
		// or a header with nothing under it.
		out := formatVerificationFindings([]CompareResult{reviewedFinding("kwin", ReviewNote{})})

		if lines := commentaryLines(out); len(lines) != 0 {
			t.Errorf("a finding nobody reviewed rendered %d commentary lines: %v", len(lines), lines)
		}
		// Today's finding, unchanged, is what an un-reviewed run still prints.
		if !strings.Contains(out, todaysUnprovedFinding) {
			t.Errorf("the finding itself changed.\nwant the line: %s\n--- section ---\n%s", todaysUnprovedFinding, out)
		}
	})

	t.Run("a half-filled note renders nothing", func(t *testing.T) {
		// The same rule the annotator applies (reviewNoteSpeaks): a note missing
		// either half has answered neither R5.2 nor R5.3. Held in the renderer as
		// well, so a note that reached a CompareResult by some other route — a
		// hand-edited cache, a future caller — cannot print a finding-shaped line
		// that states nothing.
		for _, note := range []ReviewNote{
			{Origin: OriginUnknown, Summary: "the model said something but classified nothing"},
			{Origin: OriginOverlay, Summary: "   "},
			{Origin: OriginOverlay, Declaration: "patched = true"},
		} {
			out := formatVerificationFindings([]CompareResult{reviewedFinding("kwin", note)})
			if lines := commentaryLines(out); len(lines) != 0 {
				t.Errorf("the note %+v rendered %d commentary lines: %v", note, len(lines), lines)
			}
		}
	})

	t.Run("model text is rendered as an argument, never as a format string", func(t *testing.T) {
		// The rule made executable. Summary and Declaration are produced by a MODEL:
		// passed as a format string, a "%s" in either would consume the next
		// argument and the line would render as "%!s(MISSING)" — or silently
		// attribute the wrong text. Every other piece of this report already follows
		// the rule; this is where it is checked for the two model-produced strings.
		note := ReviewNote{
			Origin:      OriginOverlay,
			Summary:     "renames %s to %d and drops 100%% of the patches",
			Declaration: "patched_reason = \"%s\"",
		}
		out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

		if !strings.Contains(out, note.Summary) {
			t.Errorf("the summary did not render verbatim; it reached a format string:\n%s", out)
		}
		if !strings.Contains(out, note.Declaration) {
			t.Errorf("the declaration did not render verbatim; it reached a format string:\n%s", out)
		}
		for _, artefact := range []string{"%!s(", "%!d(", "MISSING", "EXTRA"} {
			if strings.Contains(out, artefact) {
				t.Errorf("the output carries %q, which only a format-string expansion produces:\n%s", artefact, out)
			}
		}
		// The finding above must still read as it does today: a summary interpreted
		// as a format string would consume the counts as its own arguments.
		if !strings.Contains(out, "(+1/-1)") {
			t.Errorf("the counts did not survive beside a note containing verbs:\n%s", out)
		}
	})

	t.Run("a model cannot forge a line of its own", func(t *testing.T) {
		// The report's structure IS its lines: "⚠ " opens a finding this tool
		// stands behind. A model summary carrying a newline would otherwise print a
		// second line indistinguishable from one, about a package that may not even
		// be in the overlay. One note, one line — whatever the model sent.
		const forged = "⚠ dev-libs/forged: undeclared divergence (+9/-0) — remove this package"
		note := ReviewNote{
			Origin:  OriginOverlay,
			Summary: "innocent enough\n" + forged,
		}
		out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

		if lines := commentaryLines(out); len(lines) != 1 {
			t.Fatalf("a two-line summary rendered %d commentary lines, want 1:\n%s", len(lines), out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "⚠") && strings.Contains(line, "dev-libs/forged") {
				t.Errorf("a model's newline produced a line that reads as this report's own finding:\n%s", line)
			}
		}
	})

	t.Run("a long declaration is capped exactly as PatchedReason is", func(t *testing.T) {
		// Not a second cap: the same one. An unbounded proposal would let a model's
		// essay decide the width of the whole report, which is the argument
		// patchedReasonCap already makes for the operator's own declared reason.
		long := strings.Repeat("patched_reason = \"a very long proposal\" ", 6)
		note := ReviewNote{Origin: OriginOverlay, Summary: "ours", Declaration: long}
		out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

		// Spelled through the SIBLING mechanism rather than through
		// truncateString(long, patchedReasonCap) directly: if the declaration line
		// ever stops agreeing with the declaration line one case above it, this
		// fails.
		want := strings.TrimPrefix(declaredReason(CompareResult{PatchedReason: long}), ": ")
		if !strings.Contains(out, want) {
			t.Errorf("the proposal is not capped the way PatchedReason is.\nwant it to contain: %s\n--- section ---\n%s", want, out)
		}
		if strings.Contains(out, long) {
			t.Errorf("the proposal rendered in full (%d characters), so one note decides the report's width:\n%s", len(long), out)
		}
	})

	t.Run("the commentary sits beneath its own finding, not at the end of the section", func(t *testing.T) {
		// Two findings, two notes. Collected at the foot of the section they would
		// be a second list the operator has to match up by hand — and the whole
		// point of the note is to qualify the line above it.
		out := formatVerificationFindings([]CompareResult{
			reviewedFinding("kwin", ReviewNote{Origin: OriginUpstream, Summary: "::gentoo revised its copy in place"}),
			reviewedFinding("spectacle", ReviewNote{Origin: OriginOverlay, Summary: "our slotting work", Declaration: "patched = true"}),
		})

		var order []string
		for _, line := range strings.Split(out, "\n") {
			switch {
			case strings.Contains(line, "kde-plasma/kwin"):
				order = append(order, "kwin-finding")
			case strings.Contains(line, "kde-plasma/spectacle"):
				order = append(order, "spectacle-finding")
			case strings.Contains(line, "::gentoo revised its copy in place"):
				order = append(order, "kwin-reading")
			case strings.Contains(line, "our slotting work"):
				order = append(order, "spectacle-reading")
			case strings.Contains(line, reviewProposalLead):
				order = append(order, "spectacle-proposal")
			case strings.Contains(line, undeclaredDivergenceCaveat):
				order = append(order, "caveat")
			}
		}
		want := []string{"kwin-finding", "kwin-reading", "spectacle-finding", "spectacle-reading", "spectacle-proposal", "caveat"}
		if !reflect.DeepEqual(order, want) {
			t.Errorf("the section reads %v, want %v", order, want)
		}
	})

	t.Run("the commentary names no package", func(t *testing.T) {
		// Two reasons, and they agree. The cache indexes a note on the two files'
		// CONTENT and excludes the atom (R5.7), so one note can legitimately be
		// printed under two packages — prose about the DIFFERENCE stays true there,
		// prose about the package does not. And findingLine, the helper several
		// neighbouring tests locate a finding with, fails when two lines name one
		// atom.
		note := ReviewNote{Origin: OriginOverlay, Summary: "adds a patch", Declaration: "patched = true"}
		out := formatVerificationFindings([]CompareResult{reviewedFinding("spectacle", note)})

		for _, line := range commentaryLines(out) {
			if strings.Contains(line, "kde-plasma/spectacle") || strings.Contains(line, "spectacle") {
				t.Errorf("the commentary names the package, so the finding above it is no longer the only line that does:\n%s", line)
			}
		}
		// And the helper itself still resolves the finding.
		findingLine(t, out, "kde-plasma/spectacle")
	})

	t.Run("a note on a declared package renders nothing", func(t *testing.T) {
		// Commentary belongs to the undeclared-divergence finding and to no other.
		// AnnotateReviews never attaches one elsewhere, and the renderer refuses one
		// anyway: a declaration line that grew a model's opinion would put a guess
		// underneath the one statement the registry is authoritative about.
		declared := comparedResult("app-editors", "zed", "1.0", VerifiedDiffers)
		declared.Patched = true
		declared.PatchedBy = zedPatchedEntry
		declared.PatchedReason = "keeps our wayland patch"
		declared.Review = ReviewNote{Origin: OriginOverlay, Summary: "a model's opinion about a declared package"}

		out := formatVerificationFindings([]CompareResult{declared})

		if lines := commentaryLines(out); len(lines) != 0 {
			t.Errorf("a declared package rendered %d commentary lines: %v", len(lines), lines)
		}
	})

	t.Run("the review leaves every file on disk unchanged", func(t *testing.T) {
		// R5.4 PROPOSES declaration text; the operator applies it. The overlay
		// repository auto-commits and pushes within minutes, so a declaration this
		// program wrote would be published before anyone could read it — which is
		// why the proposal is text on a terminal and this is an assertion over a
		// real tree rather than a promise in a comment.
		captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		before := treeSnapshot(t, opts.OverlayPath)
		if len(before) == 0 {
			t.Fatal("the overlay tree is empty, so an unchanged tree proves nothing")
		}

		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: ReviewNote{
			Origin:      OriginOverlay,
			Summary:     "ours",
			Declaration: "patched = true\npatched_reason = \"apply me\"",
		}}
		AnnotateReviews(report, rev, prov, opts)
		out := FormatReport(report)

		if after := treeSnapshot(t, opts.OverlayPath); !reflect.DeepEqual(after, before) {
			t.Errorf("the overlay tree changed.\nbefore: %v\nafter:  %v", before, after)
		}
		// And the proposal really was produced, so the unchanged tree is evidence
		// that nothing applied it rather than evidence that nothing proposed one.
		if !strings.Contains(out, reviewProposalLead) {
			t.Errorf("no declaration was proposed, so the assertion above proves nothing:\n%s", out)
		}
	})

	t.Run("the commentary reaches the rendered report", func(t *testing.T) {
		// The cases above hand-build their results, which is what lets one section
		// carry several notes without several overlay trees. This one reaches the
		// same lines the way runCompare will: comparison, annotation, FormatReport.
		captureReviewWarnings(t)
		report, prov, opts := reviewFixture(t)
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, answer: func(req ReviewRequest) (ReviewNote, error) {
			return ReviewNote{
				Origin:      OriginOverlay,
				Summary:     "what " + req.Package + "'s difference does",
				Declaration: "patched = true",
			}, nil
		}}
		AnnotateReviews(report, rev, prov, opts)

		out := FormatReport(report)
		for _, pkg := range []string{"kwin", "spectacle"} {
			if !strings.Contains(out, "what "+pkg+"'s difference does") {
				t.Errorf("%s's reading never reached the report:\n%s", pkg, out)
			}
		}
		if n := len(commentaryLines(out)); n != 4 {
			t.Errorf("the report carries %d commentary lines, want 4 (a reading and a proposal for each of two findings):\n%s", n, out)
		}
	})
}

// ---------------------------------------------------------------------------
// The review is COMMENTARY (6.6): it changes nothing the operator acts on.
// ---------------------------------------------------------------------------

// stripCommentary returns the rendered report with every model line removed —
// the report a run with no reviewer at all would print, if R5.8 holds.
//
// Removing the lines rather than ignoring them is what makes the comparison an
// assertion about EVERYTHING ELSE: a commentary line that had displaced,
// reworded or reordered anything would leave the remainder no longer equal to
// the un-reviewed report, whatever the two lines themselves said.
func stripCommentary(out string) string {
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if isCommentaryLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// renderedSection is one table as the operator reads it: the heading and note
// above it, and the data rows inside it.
//
// It is extracted from the RENDERED text rather than read off the report,
// because that is where the three things R5.8 names actually reach the operator.
// The heading carries the removal recommendation (redundantRemovalAdvice), the
// rows carry the Verdict column, and which section a package's row sits in IS
// the grouping.
type renderedSection struct {
	heading []string
	rows    []string
}

// renderedSections extracts every table the report printed.
//
// The report's tables are drawn with box characters, so the structure is read
// from those: a table opens at "┌", its data rows are the lines between the
// "├" separator under the column headers and the closing "└". The heading is
// the run of non-empty lines directly above the opening, which is exactly what
// formatResultSection writes there — the title and, where there is one, the
// section note.
//
// Every malformed shape FAILS rather than being skipped. An extractor that
// silently found nothing would report two reports as equal for the same reason a
// broken scanner reports a clean package: both produce an empty result.
func renderedSections(t *testing.T, out string) []renderedSection {
	t.Helper()

	lines := strings.Split(out, "\n")
	var sections []renderedSection
	for i, line := range lines {
		if !strings.HasPrefix(line, "┌") {
			continue
		}

		var sec renderedSection
		for j := i - 1; j >= 0 && strings.TrimSpace(lines[j]) != ""; j-- {
			sec.heading = append([]string{lines[j]}, sec.heading...)
		}

		inside, closed := false, false
		for j := i + 1; j < len(lines) && !closed; j++ {
			switch {
			case strings.HasPrefix(lines[j], "┌"):
				t.Fatalf("a second table opened before the one at line %d closed; the extractor is not reading the report's structure:\n%s", i+1, out)
			case strings.HasPrefix(lines[j], "├"):
				inside = true
			case strings.HasPrefix(lines[j], "└"):
				closed = true
			case inside:
				sec.rows = append(sec.rows, lines[j])
			}
		}
		if !closed {
			t.Fatalf("the table opening at line %d never closes:\n%s", i+1, out)
		}
		if len(sec.heading) == 0 || len(sec.rows) == 0 {
			t.Fatalf("the table opening at line %d was read as %d heading lines and %d rows; a table with neither cannot be compared:\n%s",
				i+1, len(sec.heading), len(sec.rows), out)
		}
		sections = append(sections, sec)
	}
	return sections
}

// sectionsRecommendingRemoval returns the sections whose heading tells the
// operator to remove the packages beneath it.
//
// This is the report's one actionable instruction, and R5.8's "same removal
// recommendations" is about precisely which rows sit under it. It is matched on
// redundantRemovalAdvice — the constant the notes are BUILT from — so a reworded
// note cannot slip past by no longer matching a copied sentence.
func sectionsRecommendingRemoval(sections []renderedSection) []renderedSection {
	var found []renderedSection
	for _, sec := range sections {
		for _, line := range sec.heading {
			if strings.Contains(line, redundantRemovalAdvice) {
				found = append(found, sec)
				break
			}
		}
	}
	return found
}

// firstDifference describes where two rendered reports stop agreeing, so a
// failure names the line instead of printing two reports for a human to diff.
func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return fmt.Sprintf("line %d differs:\n  reviewed:   %q\n  unreviewed: %q", i+1, gotLines[i], wantLines[i])
		}
	}
	if len(gotLines) == len(wantLines) {
		return "no line differs"
	}
	return fmt.Sprintf("the reviewed report has %d lines and the un-reviewed one %d, and the shorter is a prefix of the longer",
		len(gotLines), len(wantLines))
}

// countsOf returns the report's summary counts — every scalar it carries, with
// the results themselves dropped.
//
// Whole-struct rather than four named fields, on purpose: a count added later is
// covered the day it is added, which a list of names is not. The four R5.8 names
// are ALSO asserted by name below, because DeepEqual over a struct reports "not
// equal" and this one has to say which number moved.
func countsOf(report *CompareReport) CompareReport {
	clone := *report
	clone.Results = nil
	return clone
}

// TestReviewChangesNothing is R5.8 as an executed check: the same fixture run
// twice, once with a reviewer and once with none, produces the same grouping,
// the same Verdicts and the same removal recommendations — the difference
// between the two reports is confined to the lines that announce a model.
//
// This is the property the whole feature rests on. A classification is a GUESS
// by a language model; the report around it is two files compared byte for byte
// and a registry consulted. The moment a guess can move a package between
// tables, change a Verdict or withdraw a removal recommendation, the operator is
// acting on the guess while reading the report — and `--no-review` (R5.6) stops
// being a way of declining commentary and becomes a way of getting different
// answers.
//
// It is asserted over the RENDERED report because that is what the operator
// reads. TestAnnotateReviews already holds the same property over the structure;
// a struct can be equal while the renderer, which also sees Review, prints
// something different.
//
// _Requirements: R5.8_
func TestReviewChangesNothing(t *testing.T) {
	warnings := captureReviewWarnings(t)

	// TWO independent runs of the same fixture, each with its own trees and its
	// own note cache — the reviewed one and the `--no-review` one, as they would
	// be on two invocations. Cloning one report and annotating the copy would
	// compare a report against itself and could not see a comparison that read
	// its own Review field.
	reviewedReport, prov, opts := reviewFixture(t)
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, answer: func(req ReviewRequest) (ReviewNote, error) {
		// The most decisive-looking answer a model can give: every divergence is
		// ours, and every one comes with a declaration to write. If anything a
		// reviewer says could move the report, this is the answer that would move
		// it — it is the one that argues a package must be KEPT while the report
		// beneath it recommends removal.
		return noteFor(req.Category + "/" + req.Package), nil
	}}
	AnnotateReviews(reviewedReport, rev, prov, opts)

	unreviewedReport, nilProv, nilOpts := reviewFixture(t)
	// nil IS `--no-review` (R5.6) and "no `claude` on PATH" (R5.5): one no-op
	// path, called here exactly as runCompare calls it.
	AnnotateReviews(unreviewedReport, nil, nilProv, nilOpts)

	reviewed, unreviewed := FormatReport(reviewedReport), FormatReport(unreviewedReport)

	// The fixture itself must be sound before any equality below means anything.
	// A run that warned is a run where something did not happen, and "nothing
	// changed" is then a statement about a review that never ran.
	if lines := warnings(); len(lines) != 0 {
		t.Fatalf("the fixture warned %d times, so neither run is the ordinary one this compares: %v", len(lines), lines)
	}
	if !reflect.DeepEqual(rev.atoms(), reviewedAtoms) {
		t.Fatalf("the reviewed run submitted %v, want %v; every equality below would otherwise be about a review that did not happen",
			rev.atoms(), reviewedAtoms)
	}

	t.Run("the two reports differ only in the lines that announce a model", func(t *testing.T) {
		// The guard first: a reviewed run with no commentary and an un-reviewed one
		// are trivially equal, and would pass everything below while proving
		// nothing. Four lines — a reading and a proposal for each of the two
		// undeclared divergences.
		if n := len(commentaryLines(reviewed)); n != 4 {
			t.Fatalf("the reviewed report carries %d commentary lines, want 4; the comparison below would be between two identical un-reviewed reports:\n%s", n, reviewed)
		}
		if n := len(commentaryLines(unreviewed)); n != 0 {
			t.Fatalf("the un-reviewed report carries %d commentary lines: %v", n, commentaryLines(unreviewed))
		}

		// The assertion. Byte for byte, with the model's own lines taken out.
		if got := stripCommentary(reviewed); got != unreviewed {
			t.Errorf("a review changed the report beyond its own lines.\n%s\n--- reviewed, commentary stripped ---\n%s\n--- un-reviewed ---\n%s",
				firstDifference(got, unreviewed), got, unreviewed)
		}

		// And no model text anywhere else. The equality above already forbids it;
		// this states it directly, because "a model's words appear only on the two
		// lines that say whose words they are" is the boundary the operator relies
		// on to tell a finding from a guess.
		for _, line := range strings.Split(reviewed, "\n") {
			if isCommentaryLine(line) {
				continue
			}
			for _, atom := range reviewedAtoms {
				note := noteFor(atom)
				if strings.Contains(line, note.Summary) || strings.Contains(line, note.Declaration) {
					t.Errorf("a model's words reached a line the report speaks in its own voice on:\n%s", line)
				}
			}
		}
	})

	t.Run("every table is identical: the same headings, the same notes, the same rows", func(t *testing.T) {
		// The GROUPING, as the operator sees it. Which table a package's row sits
		// in is what the report recommends about it, and the row carries the
		// Verdict column that says so in words.
		reviewedSections := renderedSections(t, reviewed)
		unreviewedSections := renderedSections(t, unreviewed)

		// The extractor is asserted to have read the whole report, so an equality
		// between two empty results cannot pass for one between two reports.
		rows := 0
		for _, sec := range reviewedSections {
			rows += len(sec.rows)
		}
		if len(reviewedSections) < 2 || rows != len(reviewedReport.Results) {
			t.Fatalf("the reviewed report was read as %d tables holding %d rows, want at least 2 tables holding all %d results:\n%s",
				len(reviewedSections), rows, len(reviewedReport.Results), reviewed)
		}

		if !reflect.DeepEqual(reviewedSections, unreviewedSections) {
			t.Errorf("the tables differ.\nreviewed:   %+v\nunreviewed: %+v", reviewedSections, unreviewedSections)
		}
	})

	t.Run("the removal recommendation covers exactly the same packages", func(t *testing.T) {
		// The one instruction this report gives. R5.8 is at its sharpest here: a
		// model that classified a divergence as ours must not be able to withdraw a
		// removal recommendation — nor to extend one — because the recommendation
		// rests on a byte comparison and a registry, neither of which it read.
		reviewedAdvice := sectionsRecommendingRemoval(renderedSections(t, reviewed))
		unreviewedAdvice := sectionsRecommendingRemoval(renderedSections(t, unreviewed))

		if len(reviewedAdvice) != 1 || len(unreviewedAdvice) != 1 {
			t.Fatalf("the reviewed report recommends removal in %d sections and the un-reviewed one in %d, want exactly 1 each:\n%s",
				len(reviewedAdvice), len(unreviewedAdvice), reviewed)
		}
		if !reflect.DeepEqual(reviewedAdvice[0], unreviewedAdvice[0]) {
			t.Errorf("the removal recommendation changed.\nreviewed:   %+v\nunreviewed: %+v", reviewedAdvice[0], unreviewedAdvice[0])
		}
	})

	t.Run("every Verdict and every summary count is identical", func(t *testing.T) {
		// The counts are asserted on the STRUCTURE because that is the only place
		// they exist: FormatReport prints the per-Status line, and the per-Verdict
		// counts are rendered by cmd/'s summary from these fields. A pass that
		// moved one would leave the text above untouched and the operator's summary
		// wrong.
		if len(reviewedReport.Results) != len(unreviewedReport.Results) {
			t.Fatalf("the two runs produced %d and %d results", len(reviewedReport.Results), len(unreviewedReport.Results))
		}
		for i, got := range reviewedReport.Results {
			want := unreviewedReport.Results[i]
			gotAtom, wantAtom := got.Category+"/"+got.Package, want.Category+"/"+want.Package
			if gotAtom != wantAtom {
				t.Errorf("result %d is %s in the reviewed run and %s in the un-reviewed one; the ordering the tables inherit has moved", i, gotAtom, wantAtom)
				continue
			}
			if got.Verdict != want.Verdict {
				t.Errorf("%s is %s with a review and %s without one", gotAtom, got.Verdict, want.Verdict)
			}
		}

		// Whole-struct, so a count added later is covered the day it is added.
		if !reflect.DeepEqual(countsOf(reviewedReport), countsOf(unreviewedReport)) {
			t.Errorf("a summary count moved.\nreviewed:   %+v\nunreviewed: %+v", countsOf(reviewedReport), countsOf(unreviewedReport))
		}
		// And by name, so the failure says WHICH number moved rather than that two
		// structs are unequal.
		for _, c := range []struct {
			name            string
			reviewed, plain int
		}{
			{"keep", reviewedReport.VerdictKeepCount, unreviewedReport.VerdictKeepCount},
			{"redundant", reviewedReport.VerdictRedundantCount, unreviewedReport.VerdictRedundantCount},
			{"needs-rebase", reviewedReport.VerdictNeedsRebaseCount, unreviewedReport.VerdictNeedsRebaseCount},
			{"unknown", reviewedReport.VerdictUnknownCount, unreviewedReport.VerdictUnknownCount},
		} {
			if c.reviewed != c.plain {
				t.Errorf("the %s count is %d with a review and %d without one", c.name, c.reviewed, c.plain)
			}
		}
	})
}
