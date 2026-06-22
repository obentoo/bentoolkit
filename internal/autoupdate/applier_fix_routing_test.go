package autoupdate

// Story 011 — Phase-3 authored (RED-first) tests for the applier-side surface:
//   1.1 isFetchFailure marker detector
//   4.1 rewriteSrcURI mechanical rewrite
//   6.2 runManifestWithFix routing (integration)
//
// These tests target internal/autoupdate/applier_fix_test.go (per each sub-task's
// Covered-by). They are authored to FAIL until the executor adds isFetchFailure,
// ErrFetchUnrecoverable, WithApplierFetchClassifier, FetchClassifier/FetchVerdict,
// rewriteSrcURI, and the routing branch in runManifestWithFix. In Go a reference
// to a not-yet-defined symbol is a compile error ("undefined: X"), which is valid
// RED ("the code under test does not yet exist").
//
// Behavior-level: assertions pin observable outcomes (verdict-driven routing,
// errors.Is sentinels, fixer-invocation counts, fixer Mode, rewritten bytes,
// reporter stages) rather than exact internal signatures, so the executor's
// audited surface exception absorbs signature drift.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// 1.1 — isFetchFailure marker detector (Unit) — R1.1..R1.4
// =============================================================================

// TestIsFetchFailure_Markers covers the three pkgcore markers as tolerant
// substrings on the raw (untruncated) error, and the negative cases.
func TestIsFetchFailure_Markers(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "marker: failed fetching required distfiles",
			raw:  "command failed: exit status 1\nOutput: pkgdev manifest: error: failed fetching required distfiles",
			want: true,
		},
		{
			name: "marker: failed fetching files for package",
			raw:  " * failed fetching files for package media-gfx/imagemagick::bentoo",
			want: true,
		},
		{
			name: "marker: failed fetching file:",
			raw:  " * failed fetching file: big-1.4.8.tar.xz",
			want: true,
		},
		{
			name: "non-fetch: digest mismatch",
			raw:  "command failed: exit status 1\nOutput: !!! Digest verification failed: size mismatch",
			want: false,
		},
		{
			name: "non-fetch: EAPI/QA error",
			raw:  "command failed: exit status 1\nOutput: ebuild.badheader: EAPI line must be first",
			want: false,
		},
		{
			name: "empty",
			raw:  "",
			want: false,
		},
		{
			name: "garbage",
			raw:  "lorem ipsum dolor sit amet, nothing to see",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFetchFailure(tc.raw); got != tc.want {
				t.Errorf("isFetchFailure(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestIsFetchFailure_MarkerInMultiKiBOutput covers R1.3: the marker must be found
// on the raw, untruncated string even when buried in a multi-KiB Output: dump
// (where middle-elision truncation would have lost it).
func TestIsFetchFailure_MarkerInMultiKiBOutput(t *testing.T) {
	head := "command failed: exit status 1\nOutput: fetching https://example.com/big-1.4.8.tar.xz\n"
	noise := strings.Repeat("137750K .......... .......... 99% 58.7M 0s\n", 100000) // ~ multi-MiB
	tail := "\npkgdev manifest: error: failed fetching required distfiles"
	raw := head + noise + tail

	if !isFetchFailure(raw) {
		t.Error("isFetchFailure must detect a marker buried in a large raw Output: dump (R1.3)")
	}
}

// =============================================================================
// 4.1 — rewriteSrcURI (Unit) — R4.1, R4.3
// =============================================================================

// newRewriteApplier builds a minimal Applier usable for rewriteSrcURI unit tests.
func newRewriteApplier(t *testing.T) *Applier {
	t.Helper()
	tmp := t.TempDir()
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"))
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a
}

func writeEbuild(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pkg-1.0.ebuild")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write ebuild: %v", err)
	}
	return p
}

func TestRewriteSrcURI_SingleLine(t *testing.T) {
	a := newRewriteApplier(t)
	old := "https://github.com/o/r/releases/download/v1.0/old-asset.tar.gz"
	neu := "https://github.com/o/r/releases/download/v1.0/new-asset.tar.gz"
	body := `EAPI=8
SRC_URI="` + old + `"
SLOT="0"
`
	p := writeEbuild(t, body)

	if err := a.rewriteSrcURI(p, old, neu); err != nil {
		t.Fatalf("rewriteSrcURI: unexpected error: %v", err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), old) {
		t.Error("old URL still present after rewrite")
	}
	if !strings.Contains(string(got), neu) {
		t.Error("new URL not present after rewrite")
	}
	// Bytes outside the URL untouched.
	for _, want := range []string{"EAPI=8", `SLOT="0"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("rewrite disturbed unrelated bytes: missing %q", want)
		}
	}
}

func TestRewriteSrcURI_MultiLineRewritesOnlyTarget(t *testing.T) {
	a := newRewriteApplier(t)
	target := "https://github.com/o/r/releases/download/v1.0/moved.tar.gz"
	other := "https://example.com/keep-me.patch"
	neu := "https://github.com/o/r/releases/download/v1.0/found.tar.gz"
	body := `EAPI=8
SRC_URI="
	` + target + `
	` + other + `
"
SLOT="0"
`
	p := writeEbuild(t, body)

	if err := a.rewriteSrcURI(p, target, neu); err != nil {
		t.Fatalf("rewriteSrcURI: unexpected error: %v", err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), target) {
		t.Error("target URL still present after rewrite")
	}
	if !strings.Contains(string(got), neu) {
		t.Error("discovered URL not written")
	}
	if !strings.Contains(string(got), other) {
		t.Error("rewrite must touch only the target URL, not the other SRC_URI entry")
	}
}

func TestRewriteSrcURI_AbsentURLErrors(t *testing.T) {
	a := newRewriteApplier(t)
	body := `EAPI=8
SRC_URI="https://github.com/o/r/releases/download/v1.0/present.tar.gz"
`
	p := writeEbuild(t, body)

	// The URL to replace is not present → must error (drives the LLM fallback, R4.3).
	err := a.rewriteSrcURI(p, "https://github.com/o/r/releases/download/v1.0/absent.tar.gz",
		"https://github.com/o/r/releases/download/v1.0/new.tar.gz")
	if err == nil {
		t.Fatal("rewriteSrcURI must error when the old URL is absent (no change)")
	}
}

func TestRewriteSrcURI_RegexSpecialCharsHandled(t *testing.T) {
	a := newRewriteApplier(t)
	// A URL containing regex metacharacters: must be matched literally (QuoteMeta).
	old := "https://dl.example.com/get?file=foo+bar(1.0).tar.gz&v=1.0"
	neu := "https://dl.example.com/get?file=foo+bar(2.0).tar.gz&v=2.0"
	body := `EAPI=8
SRC_URI="` + old + `"
`
	p := writeEbuild(t, body)

	if err := a.rewriteSrcURI(p, old, neu); err != nil {
		t.Fatalf("rewriteSrcURI with regex-special chars: %v", err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), old) || !strings.Contains(string(got), neu) {
		t.Errorf("regex-special URL not rewritten literally; got:\n%s", got)
	}
}

// =============================================================================
// 6.2 — runManifestWithFix routing (Integration) — R3.x, R4.2/R4.3, R6.1/R6.2, UB1/2/3
// =============================================================================

// stubClassifier is a FetchClassifier test double returning a canned verdict and
// recording whether Classify ran.
type stubClassifier struct {
	verdict FetchVerdict
	called  int
}

func (s *stubClassifier) Classify(_ context.Context, _ FetchClassifyRequest) FetchVerdict {
	s.called++
	return s.verdict
}

var _ FetchClassifier = (*stubClassifier)(nil)

// fetchFailSeam scripts pkgdev to PRINT the real pkgcore fetch markers to stdout
// then exit non-zero, so the manifest error carries a fetch marker (isFetchFailure
// → true). Non-pkgdev commands succeed. With onlyFirst=true, the FIRST pkgdev call
// fails-with-marker and subsequent pkgdev calls succeed (used to model a passing
// re-check after a mechanical rewrite).
func fetchFailSeam(onlyFirst bool) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	const marker = "pkgdev manifest: error: failed fetching required distfiles"
	calls := 0
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			calls++
			if !onlyFirst || calls == 1 {
				// Print the marker to stdout, then fail.
				return exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' '"+marker+"'; exit 1")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
}

// setupRoutingApply wires an applier with a fetch-marker pkgdev seam, a stub
// classifier, a fakeFixer, and a recording reporter. It returns the applier plus
// the doubles so the test can assert routing outcomes. pkg is dev-games/godot.
func setupRoutingApply(t *testing.T, seam func(context.Context, string, ...string) *exec.Cmd,
	classifier FetchClassifier, fixer *fakeFixer, rec *recordingReporter) (*Applier, *PendingList) {
	t.Helper()
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	cfg := filepath.Join(tmp, "config")
	pkg := "dev-games/godot"

	// Give the ebuild a real SRC_URI so a recoverable rewrite has a target.
	createTestEbuildFileWithContent(t, overlay, pkg, "4.7_rc3",
		`EAPI=8
DESCRIPTION="t"
SRC_URI="https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz"
SLOT="0"
KEYWORDS="~amd64"
`)

	pending, _ := NewPendingList(cfg)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "4.7_rc3", NewVersion: "4.7", Status: StatusPending})

	opts := []ApplierOption{
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFetchClassifier(classifier),
	}
	if fixer != nil {
		opts = append(opts, WithApplierFixer(fixer))
	}
	if rec != nil {
		opts = append(opts, WithApplierReporter(rec))
	}
	a, err := NewApplier(overlay, cfg, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a, pending
}

// TestRouting_NilClassifier_GenericFixer covers UB2: with a nil classifier the
// fetch-marker failure still takes the generic fixer path (FixModeGeneric).
func TestRouting_NilClassifier_GenericFixer(t *testing.T) {
	fixer := &fakeFixer{summary: "generic"}
	// Nil classifier → generic path; fixer "fixes" then re-check passes.
	a, _ := setupRoutingApply(t, fetchFailSeam(true), nil, fixer, nil)

	res, err := a.Apply("dev-games/godot", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success on generic recover path: %v", res.Error)
	}
	if fixer.called != 1 {
		t.Errorf("generic fixer called %d times, want 1 (UB2)", fixer.called)
	}
	if fixer.lastReq.Mode != FixModeGeneric {
		t.Errorf("nil-classifier path must use FixModeGeneric, got %v", fixer.lastReq.Mode)
	}
}

// TestRouting_NonFetchFailure_GenericFixer covers UB2: a non-fetch manifest
// failure (no marker) takes the generic path even when a classifier is wired.
func TestRouting_NonFetchFailure_GenericFixer(t *testing.T) {
	// pkgdev fails WITHOUT a fetch marker, then succeeds → generic recover.
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			calls++
			if calls == 1 {
				return exec.CommandContext(ctx, "sh", "-c", "printf 'Digest verification failed\\n'; exit 1")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	classifier := &stubClassifier{verdict: FetchVerdict{Kind: FetchIrreparable, Reason: "should not be consulted"}}
	fixer := &fakeFixer{summary: "generic"}
	a, _ := setupRoutingApply(t, seam, classifier, fixer, nil)

	res, err := a.Apply("dev-games/godot", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected generic recover, got failure: %v", res.Error)
	}
	if classifier.called != 0 {
		t.Errorf("classifier must NOT run for a non-fetch failure (UB2), called=%d", classifier.called)
	}
	if fixer.called != 1 || fixer.lastReq.Mode != FixModeGeneric {
		t.Errorf("non-fetch failure must use the generic fixer once; called=%d mode=%v", fixer.called, fixer.lastReq.Mode)
	}
}

// TestRouting_Irreparable_SkipsLLM covers R3.1/R6.2: an Irreparable verdict skips
// the fixer and fails with ErrFetchUnrecoverable (and NOT ErrLLMRequestFailed).
func TestRouting_Irreparable_SkipsLLM(t *testing.T) {
	const distfile = "imagemagick-7.1.2.26.tar.xz"
	classifier := &stubClassifier{verdict: FetchVerdict{
		Kind:   FetchIrreparable,
		Reason: "no obtainable distfile: " + distfile,
	}}
	fixer := &fakeFixer{summary: "must not run"}
	a, _ := setupRoutingApply(t, fetchFailSeam(false), classifier, fixer, nil)

	res, applyErr := a.Apply("dev-games/godot", false)
	if applyErr == nil || res.Success {
		t.Fatal("expected the irreparable skip to fail the apply")
	}
	if fixer.called != 0 {
		t.Errorf("fixer must NOT be invoked on the irreparable route, called=%d", fixer.called)
	}
	if !errors.Is(applyErr, ErrFetchUnrecoverable) {
		t.Errorf("error %v must wrap ErrFetchUnrecoverable (R6.2)", applyErr)
	}
	if errors.Is(applyErr, ErrLLMRequestFailed) {
		t.Error("irreparable skip must NOT wrap ErrLLMRequestFailed (distinct from attempted-and-failed)")
	}
	if !strings.Contains(applyErr.Error(), distfile) {
		t.Errorf("error %v should name the unobtainable distfile %q", applyErr, distfile)
	}

	// UB (orphan rollback): the deferred cleanup in Apply must still fire on the
	// new irreparable-skip path, so the half-applied new-version ebuild copy is
	// removed and the overlay is never left half-applied.
	orphan := a.EbuildPath("dev-games/godot", "4.7")
	if _, statErr := os.Stat(orphan); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("irreparable skip must roll back the orphan ebuild %s (stat err = %v, want os.ErrNotExist)", orphan, statErr)
	}
}

// TestRouting_Recoverable_MechanicalSuccess covers R4.2: a Recoverable verdict is
// repaired mechanically (rewrite + passing re-check) with NO fixer invocation,
// and the result records a mechanical fix.
func TestRouting_Recoverable_MechanicalSuccess(t *testing.T) {
	cur := "https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz"
	found := "https://github.com/godotengine/godot/releases/download/4.7/godot-4.7.tar.gz"
	classifier := &stubClassifier{verdict: FetchVerdict{
		Kind:          FetchRecoverable,
		CurrentURL:    cur,
		DiscoveredURL: found,
		Reason:        "renamed asset",
	}}
	fixer := &fakeFixer{summary: "must not run"}
	// First pkgdev fails-with-marker; the re-check (after rewrite) passes.
	a, _ := setupRoutingApply(t, fetchFailSeam(true), classifier, fixer, nil)

	res, err := a.Apply("dev-games/godot", false)
	if err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected mechanical success, got failure: %v", res.Error)
	}
	if fixer.called != 0 {
		t.Errorf("mechanical success must NOT invoke the fixer, called=%d", fixer.called)
	}
	// The ebuild must now carry the discovered URL.
	got, _ := os.ReadFile(a.EbuildPath("dev-games/godot", "4.7"))
	if !strings.Contains(string(got), found) {
		t.Errorf("ebuild was not mechanically rewritten to the discovered URL; got:\n%s", got)
	}
	// Result records it as a mechanical (non-LLM) fix.
	if !res.Fixed {
		t.Error("expected result.Fixed = true on a mechanical recover")
	}
}

// TestRouting_Recoverable_RewriteOrRecheckFails_FallsBackToFetchLLM covers R4.3:
// when the mechanical re-check still fails, routing falls back to the focused LLM
// fixer invoked with FixModeFetch.
func TestRouting_Recoverable_FallbackToFetchLLM(t *testing.T) {
	cur := "https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz"
	found := "https://github.com/godotengine/godot/releases/download/4.7/godot-4.7.tar.gz"
	classifier := &stubClassifier{verdict: FetchVerdict{
		Kind: FetchRecoverable, CurrentURL: cur, DiscoveredURL: found, Reason: "renamed",
	}}
	// pkgdev ALWAYS fails-with-marker → mechanical re-check fails → fall back to LLM.
	// fakeFixer.onCall edits the ebuild but the manifest still fails (every pkgdev
	// fails), so the LLM path is exercised and ultimately fails; we assert the fixer
	// WAS invoked in fetch mode and the error wraps ErrLLMRequestFailed.
	fixer := &fakeFixer{err: ErrLLMRequestFailed}
	a, _ := setupRoutingApply(t, fetchFailSeam(false), classifier, fixer, nil)

	_, applyErr := a.Apply("dev-games/godot", false)
	if applyErr == nil {
		t.Fatal("expected failure when the recoverable re-check and the LLM both fail")
	}
	if fixer.called != 1 {
		t.Errorf("focused fixer should be invoked once on fallback, called=%d", fixer.called)
	}
	if fixer.lastReq.Mode != FixModeFetch {
		t.Errorf("fallback fixer must use FixModeFetch, got %v", fixer.lastReq.Mode)
	}
	if !errors.Is(applyErr, ErrLLMRequestFailed) {
		t.Errorf("every LLM route must wrap ErrLLMRequestFailed (R6.1/UB3), got %v", applyErr)
	}
}

// TestRouting_Inconclusive_FocusedLLM covers R3.3: an Inconclusive verdict routes
// to the focused LLM fixer (fail-open), invoked with FixModeFetch.
func TestRouting_Inconclusive_FocusedLLM(t *testing.T) {
	classifier := &stubClassifier{verdict: FetchVerdict{Kind: FetchInconclusive, Reason: "probe timed out"}}
	// Fixer "fixes" and the re-check passes (onlyFirst).
	fixer := &fakeFixer{summary: "focused fix"}
	a, _ := setupRoutingApply(t, fetchFailSeam(true), classifier, fixer, nil)

	res, err := a.Apply("dev-games/godot", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected fail-open recover, got failure: %v", res.Error)
	}
	if fixer.called != 1 || fixer.lastReq.Mode != FixModeFetch {
		t.Errorf("inconclusive must invoke the focused fixer once in FixModeFetch; called=%d mode=%v",
			fixer.called, fixer.lastReq.Mode)
	}
}

// TestRouting_ReporterStages covers the reporter contract: the fetch route emits
// "fetch-classify"; the recoverable route additionally emits "mech-fix".
func TestRouting_ReporterStages(t *testing.T) {
	cur := "https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz"
	found := "https://github.com/godotengine/godot/releases/download/4.7/godot-4.7.tar.gz"
	classifier := &stubClassifier{verdict: FetchVerdict{
		Kind: FetchRecoverable, CurrentURL: cur, DiscoveredURL: found, Reason: "renamed",
	}}
	rec := &recordingReporter{}
	a, _ := setupRoutingApply(t, fetchFailSeam(true), classifier, nil, rec)

	if _, err := a.Apply("dev-games/godot", false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	events := rec.snapshot()
	assertOrder(t, events, "TaskStage:dev-games/godot:fetch-classify", "TaskStage:dev-games/godot:mech-fix")
}
