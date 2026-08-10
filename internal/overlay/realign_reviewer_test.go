// MERGE FRAGMENT — story 034, sub-task 5.1 (the reviewing model).
//
// Target file: internal/overlay/realign_reviewer_test.go  (NEW file, package overlay)
// Borrowed, never re-declared: `localRootedFakeProvider` and `writeVerifyEbuild`
// (compare_verification_test.go), and `packageLevelOwner`
// (compare_diff_counts_fence_test.go) — the fence below is a second instance of
// that file's pattern, not a second copy of its machinery.
//
// PINNED CONTRACT
//
//	type RealignRequest struct{ Category, Package, Version string; Ours, Baseline []byte }
//	type RealignNote struct{ Justified bool; Why, BaselineText string }
//	type RealignReviewer interface {
//	    ReviewRealignment(ctx context.Context, req RealignRequest) (RealignNote, error)
//	}
//	func realignAllowedTools() []string
//	func needsRealignVerdict(r CompareResult) bool
//	func AnnotateRealignVerdicts(report *CompareReport, rev RealignReviewer, prov provider.Provider, opts CompareOptions)
//
// D7 says the verdict RIDES the AnnotateReviews pass — one model call per package
// rather than two. If the implementer folds this into AnnotateReviews rather than
// adding a pass beside it, rename the call sites below; every assertion still
// holds, because none of them is about which function does the walking. What is
// NOT negotiable is the count: two full passes over 237 packages, sequentially,
// at a 120-second per-call timeout, is the cost D7 exists to refuse.
//
// AnnotateRealignVerdicts returns NOTHING, and that is R4.4's "exit 0" expressed
// where this package can hold it: an unreachable model is a warning and a report
// that still prints, exactly as AnnotateReviews already behaves. A pass that
// returned an error would hand the caller something to exit on.
package overlay

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// realignFakeReviewer answers every request the same way and counts the calls.
// The count is the assertion in the cache case: "no model call" is not
// observable from the report, only from the reviewer.
type realignFakeReviewer struct {
	calls int
	seen  []string // "category/package" per call, in order
	note  RealignNote
	err   error
}

func (r *realignFakeReviewer) ReviewRealignment(_ context.Context, req RealignRequest) (RealignNote, error) {
	r.calls++
	r.seen = append(r.seen, req.Category+"/"+req.Package)
	return r.note, r.err
}

var _ RealignReviewer = (*realignFakeReviewer)(nil)

const (
	realignOurs     = "EAPI=8\ninherit meson python-any-r1\nIUSE=\"qml wayland\"\nDESCRIPTION=\"Qt6 plugin\"\n"
	realignBaseline = "EAPI=8\ninherit gstreamer-meson\nIUSE=\"qml\"\nDESCRIPTION=\"Qt6 plugin\"\n"
)

// realignFixture builds one overlay package with a ::gentoo counterpart that
// differs, and returns a compared report ready for the verdict pass.
func realignFixture(t *testing.T) (*CompareReport, *localRootedFakeProvider, CompareOptions) {
	t.Helper()
	// The review cache is on disk; every case gets its own so the order tests
	// run in cannot make a call disappear.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	overlayRoot, gentooRoot := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlayRoot, "media-libs", "gst-plugins-qt6", "1.29.2", realignOurs)
	writeVerifyEbuild(t, gentooRoot, "media-libs", "gst-plugins-qt6", "1.29.2", realignBaseline)

	prov := &localRootedFakeProvider{
		root:     gentooRoot,
		versions: map[string][]string{"media-libs/gst-plugins-qt6": {"1.29.2"}},
	}
	opts := CompareOptions{IncludeSynced: true, IncludeNotInRemote: true, OverlayPath: overlayRoot}
	report, err := CompareWithProvider([]PackageInfo{{
		Category:      "media-libs",
		Package:       "gst-plugins-qt6",
		Versions:      []string{"1.29.2"},
		LatestVersion: "1.29.2",
	}}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	return report, prov, opts
}

// TestRealignReviewerAllowListHoldsNothingThatWrites is R4.5, and it is the one
// assertion here that is about capability rather than about behaviour.
//
// The staged tree is the only boundary between a bad proposal and an overlay
// that publishes itself — the overlay auto-commits, so a model that could write
// a file could publish an unreviewed ebuild. Story 033's D7 and
// registry_fixer.go:44-48 set the precedent: narrow the list, and write the
// reason beside it.
//
// _Requirements: R4, R4.5_
func TestRealignReviewerAllowListHoldsNothingThatWrites(t *testing.T) {
	tools := realignAllowedTools()

	if len(tools) == 0 {
		t.Fatal("the allow-list is empty, which makes every assertion below vacuous; if the reviewer genuinely needs no tools, delete this test rather than let it pass for the wrong reason")
	}

	// Bash is on this list for the same reason as Write: `bash -c 'echo > x'`
	// writes, and an allow-list that admits a shell has not narrowed anything.
	forbidden := []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash"}
	for _, tool := range tools {
		for _, bad := range forbidden {
			if strings.EqualFold(strings.TrimSpace(tool), bad) {
				t.Errorf("the allow-list holds %q; the reviewing model must hold no capability that can modify a file (R4.5) — the overlay auto-commits, so a write is a publish", tool)
			}
		}
	}
}

// TestNeedsRealignVerdict is D7's filter: 237 packages of full ebuild diff is
// not a prompt anyone should pay for, and the declaration is what decides who
// pays.
//
// _Requirements: R3.1, R3.3, R4, R4.1, R1.3_
func TestNeedsRealignVerdict(t *testing.T) {
	withBaseline := Baseline{Repo: "gentoo", Version: "1.29.2", Path: "/x.ebuild", Found: true}

	cases := []struct {
		name string
		res  CompareResult
		want bool
		why  string
	}{
		{
			name: "an undeclared divergence is asked about",
			res:  CompareResult{Baseline: withBaseline},
			want: true,
			why:  "nothing declares why it differs, which on today's overlay is every package (R3.4)",
		},
		{
			name: "a declared, unexpired divergence is not",
			res: CompareResult{Baseline: withBaseline, Declarations: []DeclaredDivergence{
				{Axis: "inherit", Reason: "gstreamer-meson does not handle the qt6 option list", DropWhen: "gentoo-version >= 1.29"},
			}},
			want: false,
			why:  "a decision with a reason is not re-litigated every run (R3.1), and asking anyway is what makes the first run's bill permanent",
		},
		{
			name: "an expired declaration rejoins the queue",
			res: CompareResult{Baseline: withBaseline, Declarations: []DeclaredDivergence{
				{Axis: "inherit", Reason: "until ::gentoo ships 1.29", DropWhen: "gentoo-version >= 1.29", Expired: true},
			}},
			want: true,
			why:  "the reason has run out, so the divergence is treated as undeclared from then on (R3.3)",
		},
		{
			name: "a package with no baseline is never asked about",
			res:  CompareResult{Baseline: Baseline{Found: false}},
			want: false,
			why:  "R1.3 proposes no realignment where there is nothing to realign towards; 84 of 321 packages are here",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsRealignVerdict(tc.res); got != tc.want {
				t.Errorf("needsRealignVerdict = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestRealignVerdictAnnotatesWhatItIsGiven checks the two halves of R4.1 and
// R4.2 arrive on the result: the opinion, and — when the answer is "not
// justified" — the baseline text that would replace the divergence.
//
// _Requirements: R4, R4.1, R4.2_
func TestRealignVerdictAnnotatesWhatItIsGiven(t *testing.T) {
	report, prov, opts := realignFixture(t)
	rev := &realignFakeReviewer{note: RealignNote{
		Justified:    false,
		Why:          "::gentoo's eclass now carries the option list our inherit replaced",
		BaselineText: realignBaseline,
	}}

	AnnotateRealignVerdicts(report, rev, prov, opts)

	if rev.calls != 1 {
		t.Fatalf("the reviewer was called %d times, want 1 (one call per package, D7): %v", rev.calls, rev.seen)
	}
	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	got := report.Results[0]
	if got.RealignVerdict == "" {
		t.Fatal("RealignVerdict is empty although the model answered; the verdict reached no report (R4.1)")
	}
	if !strings.Contains(got.RealignVerdict, "eclass") {
		t.Errorf("RealignVerdict is %q, want the model's own reason — 'not justified' with no why is an instruction, not an input", got.RealignVerdict)
	}
	rendered := FormatReport(report)
	if !strings.Contains(rendered, "gstreamer-meson") {
		t.Errorf("the report does not show the baseline text that would replace the divergence (R4.2):\n%s", rendered)
	}
}

// TestRealignVerdictUnreachableModelSaysSo is R4.4 with D9's reasoning. The
// deterministic half of the report — the baseline, the structural axes, the
// declarations, the reduction — is complete and useful; only the verdicts are
// missing, and the one thing the run owes the operator is to say so. A missing
// verdict and a verdict of "justified" must never look alike.
//
// _Requirements: R4, R4.4_
func TestRealignVerdictUnreachableModelSaysSo(t *testing.T) {
	report, prov, opts := realignFixture(t)
	rev := &realignFakeReviewer{err: errors.New("claude: executable file not found in $PATH")}

	// No error return by construction: an unreachable model is exit 0, and a
	// pass that handed the caller an error would invite an exit code (D9).
	AnnotateRealignVerdicts(report, rev, prov, opts)

	for _, r := range report.Results {
		if r.RealignVerdict != "" {
			t.Errorf("%s/%s carries RealignVerdict %q although the model could not be reached; a verdict invented on a failed call is worse than none", r.Category, r.Package, r.RealignVerdict)
		}
	}
	rendered := FormatReport(report)
	if !strings.Contains(strings.ToLower(rendered), "no verdict") {
		t.Errorf("the report does not say that no verdict was produced (R4.4); silence here reads as 'every divergence was judged and none objected':\n%s", rendered)
	}
}

// TestRealignVerdictSecondRunMakesNoCall is D7's third guard: reviewCache is
// keyed on the content of the two files, so an unchanged pair is never re-asked.
// Without it the second run costs exactly what the first one did, and the
// argument that "the second run is cheap" is untrue.
//
// _Requirements: R4, R4.1_
func TestRealignVerdictSecondRunMakesNoCall(t *testing.T) {
	report, prov, opts := realignFixture(t)
	rev := &realignFakeReviewer{note: RealignNote{Justified: true, Why: "the qt6 option list is still ours to carry"}}

	AnnotateRealignVerdicts(report, rev, prov, opts)
	if rev.calls != 1 {
		t.Fatalf("the first pass made %d calls, want 1", rev.calls)
	}

	// A second report over the SAME files: same content, same key, no question.
	second, err := CompareWithProvider([]PackageInfo{{
		Category:      "media-libs",
		Package:       "gst-plugins-qt6",
		Versions:      []string{"1.29.2"},
		LatestVersion: "1.29.2",
	}}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	AnnotateRealignVerdicts(second, rev, prov, opts)

	if rev.calls != 1 {
		t.Errorf("the reviewer was called %d times over two runs of unchanged content, want 1 — the cache keys on the two files' bytes (D7)", rev.calls)
	}
	if second.Results[0].RealignVerdict == "" {
		t.Error("the cached run produced no verdict; a cache that saves the call by dropping the answer has saved nothing")
	}
}

// ---------------------------------------------------------------------------
// The fence.
//
// R4.3 says the realignment verdict is written where no code that decides an
// exit code or a Verdict reads it. That is a statement about code which must
// never be written, and the only way to test code that does not exist is to read
// the source and prove it still does not — the argument
// compare_diff_counts_fence_test.go makes at length for DiffAdded/DiffRemoved.
// This is a second instance of that pattern, on the same terms.
//
// It deliberately does NOT invent a severity: internal/overlay has no Finding,
// no Severity and no ExitCode, and compare's exit code comes from osExit calls in
// runCompare, one package away. What IS checkable here is the intersection: no
// function that decides a Verdict may read RealignVerdict. The set of deciders is
// DERIVED from the source (anything that assigns to a .Verdict field or returns a
// Verdict) rather than listed, so a new decider is caught the day it is written
// instead of the day someone remembers to update a list.
// ---------------------------------------------------------------------------

// realignFenceScan parses this package's non-test sources and returns, per
// enclosing declaration, whether it reads RealignVerdict and whether it decides
// a Verdict.
func realignFenceScan(t *testing.T) (readers, deciders map[string]bool) {
	t.Helper()
	readers, deciders = map[string]bool{}, map[string]bool{}

	// The package directory comes from this file's own path, never from the
	// process working directory: a fence that reads whatever directory it was
	// started in is a fence that can be moved off its subject.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	pkgDir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package directory %q: %v", pkgDir, err)
	}

	sources := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources++
		// A parse failure FAILS rather than skips: an unreadable source scanned
		// as if it were empty yields exactly the clean result a compliant
		// package yields, so the fence would report the absence of a violation
		// it never looked for.
		file, parseErr := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			owner := packageLevelOwner
			if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name != nil {
				owner = fn.Name.Name
				// A function whose signature returns a Verdict decides one.
				if fn.Type.Results != nil {
					for _, res := range fn.Type.Results.List {
						if id, isIdent := res.Type.(*ast.Ident); isIdent && id.Name == "Verdict" {
							deciders[owner] = true
						}
					}
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if node.Sel.Name == "RealignVerdict" {
						readers[owner] = true
					}
				case *ast.AssignStmt:
					// An assignment to some .Verdict field is a decision about
					// a Verdict wherever it happens.
					for _, lhs := range node.Lhs {
						if sel, isSel := lhs.(*ast.SelectorExpr); isSel && sel.Sel.Name == "Verdict" {
							deciders[owner] = true
						}
					}
				}
				return true
			})
		}
	}
	// Same argument one level up: a scan that found no files to read is not a
	// package with nothing to hide.
	if sources == 0 {
		t.Fatalf("no non-test Go source found in %q; the scan would pass vacuously", pkgDir)
	}
	return readers, deciders
}

// TestRealignVerdictDecidesNothing is R4.3.
//
// _Requirements: R4, R4.3_
func TestRealignVerdictDecidesNothing(t *testing.T) {
	readers, deciders := realignFenceScan(t)

	if len(readers) == 0 {
		t.Fatal("no non-test source mentions RealignVerdict; the fence would pass vacuously, and a verdict nothing writes or renders is not a verdict")
	}
	if len(deciders) == 0 {
		t.Fatal("no function in this package decides a Verdict, which cannot be true while deriveVerdict exists; the scan is looking at the wrong source")
	}

	for owner := range readers {
		if deciders[owner] {
			t.Errorf("%s both reads RealignVerdict and decides a Verdict; R4.3 keeps the model's opinion out of every mechanism that decides — one mechanism decides, the other comments, and a disagreement between them stays legible only while that holds", owner)
		}
	}
	if readers[packageLevelOwner] {
		t.Error("RealignVerdict is read outside every function (a package-level initialiser); such a read has no owner an allow-list can name and no reviewer can see it in a diff")
	}
}
