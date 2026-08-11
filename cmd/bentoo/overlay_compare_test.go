package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// TestCompareCmd_HasCloneFlag verifies that the compare command registers a --clone flag.
func TestCompareCmd_HasCloneFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("clone")
	if flag == nil {
		t.Fatal("compare command should have --clone flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--clone should be bool type, got %s", flag.Value.Type())
	}
}

// TestCompareCmd_HasTimeoutFlag verifies that the compare command registers --timeout with default 30.
func TestCompareCmd_HasTimeoutFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("timeout")
	if flag == nil {
		t.Fatal("compare command should have --timeout flag")
	}
	if flag.Value.Type() != "int" {
		t.Errorf("--timeout should be int type, got %s", flag.Value.Type())
	}
	if flag.DefValue != "30" {
		t.Errorf("--timeout default should be 30, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasNoCacheFlag verifies that the compare command registers a --no-cache flag.
func TestCompareCmd_HasNoCacheFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("no-cache")
	if flag == nil {
		t.Fatal("compare command should have --no-cache flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--no-cache should be bool type, got %s", flag.Value.Type())
	}
}

// TestCompareCmd_DefaultRepoIsGentoo verifies that compare Use string accepts an optional [repository] arg.
func TestCompareCmd_DefaultRepoIsGentoo(t *testing.T) {
	// The command accepts at most one positional arg (repository name)
	// When none given, it defaults to "gentoo". Verify the Use string documents this.
	if !strings.Contains(compareCmd.Use, "compare") {
		t.Errorf("compare command Use should contain 'compare', got %q", compareCmd.Use)
	}
	// The args validator allows zero or one positional arg
	if compareCmd.Args == nil {
		t.Error("compare command should have Args validator set (MaximumNArgs(1))")
	}
}

// TestCompareCmd_HasRunFunction verifies that the compare command has a Run or RunE function.
func TestCompareCmd_HasRunFunction(t *testing.T) {
	if compareCmd.Run == nil && compareCmd.RunE == nil {
		t.Error("compare command should have a Run or RunE function")
	}
}

// TestCompareCmd_HasOnlyOutdatedFlag verifies that the compare command registers --only-outdated.
func TestCompareCmd_HasOnlyOutdatedFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("only-outdated")
	if flag == nil {
		t.Fatal("compare command should have --only-outdated flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--only-outdated default should be false, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasSyncFlag verifies that the compare command registers a --sync flag with default false.
func TestCompareCmd_HasSyncFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("sync")
	if flag == nil {
		t.Fatal("compare command should have --sync flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--sync should be bool type, got %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--sync default should be false, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasTokenFlag verifies that the compare command registers a --token flag.
func TestCompareCmd_HasTokenFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("token")
	if flag == nil {
		t.Fatal("compare command should have --token flag")
	}
	if flag.Value.Type() != "string" {
		t.Errorf("--token should be string type, got %s", flag.Value.Type())
	}
}

// MERGE FRAGMENT — story 034, sub-task 6.1 (`--realign` on `overlay compare`).
//
// Target file: cmd/bentoo/overlay_compare_test.go  (APPEND, package main)
// The target file today imports only "strings" and "testing". Merge its import
// block with the one below rather than adding a second block.
//
// Borrowed from other test files in package main, never re-declared:
//
//	withExitIntercept (run_functions_test.go:50) · captureStdout (snapshot_test.go:41)
//	comparisonSummaryLines / verdictSummaryLines (overlay_compare_summary_test.go)
//
// PINNED CONTRACT
//
//	var compareRealign bool                                    // --realign
//	func realignPreflight(repoName string, prov provider.Provider) error
//
// `realignPreflight` is a pure function returning the refusal, not a logger call,
// for a reason overlay_compare_summary_test.go already records: logger binds its
// io.Writer at first use and exposes no setter (logger.go:44-52), so a message
// written there is unassertable without adding production API for a test. R7.4
// says the refusal must NAME the reason; a returned error is the only shape in
// which a test can read the name.
//
// The review locates the ::gentoo tree by its Portage marker, `profiles/repo_name`
// under the provider's local root. That is what R1.5's "name what it looked for"
// names, and it is what makes "the tree is not there" different from "the tree is
// there and carries nothing".
//
// # WHY THIS FILE IS THE HIGH-RISK ONE
//
// `overlay compare` is shipped and in daily use. Every assertion here exists
// because the regression is the risk, not the feature. In particular the
// byte-identical case does NOT eyeball fields: it rebuilds the expected report
// independently — same provider, same options the shipped command builds today,
// IncludeNotInRemote OFF — renders it, and compares the whole rendering to the
// bytes runCompare actually wrote. A new line rendered on the no-realign path
// fails it, and so does IncludeNotInRemote being switched on for everyone, which
// D1 names as the way the 84 Bentoo-only packages would silently gain rows.
//
// OFFLINE BY CONSTRUCTION: every repository in these fixtures is `provider: local`
// pointing at a t.TempDir(), PATH is emptied so no `claude` is reachable, and the
// API-only refusal is exercised through realignPreflight rather than end to end —
// constructing a GitHub provider would make runCompare attempt a rate-limit call,
// which is a network dependency this suite must not have.

const (
	realignOursEbuild = `EAPI=8
inherit meson python-any-r1

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml wayland"
src_configure() {
	local emesonargs=( -Dqt6=enabled -Dqml=enabled -Dwayland=enabled )
	meson_src_configure
}
`
	realignBaselineEbuild = `EAPI=8
inherit gstreamer-meson

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml"
src_configure() {
	local emesonargs=( -Dqt6=enabled )
	meson_src_configure
}
`
	realignZedEbuild = `EAPI=8
DESCRIPTION="Zed editor"
`
)

// realignFixture is one run's world: an overlay, a ::gentoo tree, and the HOME
// the command reads its config from.
type realignFixture struct {
	overlayPath string
	gentooPath  string
}

// realignWriteEbuild writes <root>/<category>/<pkg>/<pkg>-<version>.ebuild.
func realignWriteEbuild(t *testing.T, root, category, pkg, version, body string) {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, pkg+"-"+version+".ebuild")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// realignSetup builds the fixture and points HOME/XDG at a config naming both
// repositories as `provider: local`, so nothing in the run leaves the machine.
//
// baselineDiffers=false makes our ebuild byte-identical to ::gentoo's, which is
// the "no divergences at all" case R7.5 needs; carryBaseline=false leaves the
// ::gentoo tree without the profiles/repo_name marker, which is R1.5's "the tree
// cannot be located".
func realignSetup(t *testing.T, baselineDiffers, carryBaseline bool) realignFixture {
	t.Helper()
	home := t.TempDir()
	overlayPath := filepath.Join(home, "overlay")
	gentooPath := filepath.Join(home, "gentoo")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayPath, sub), 0o750); err != nil {
			t.Fatalf("mkdir overlay/%s: %v", sub, err)
		}
	}
	if err := os.MkdirAll(gentooPath, 0o750); err != nil {
		t.Fatalf("mkdir gentoo: %v", err)
	}

	// Ours: one package ::gentoo also carries, one it does not. The second is a
	// stand-in for the measured 84, and it is what makes the package-set half of
	// the byte-identical assertion able to fail.
	realignWriteEbuild(t, overlayPath, "media-libs", "gst-plugins-qt6", "1.29.2", realignOursEbuild)
	realignWriteEbuild(t, overlayPath, "app-editors", "zed", "1.0.0", realignZedEbuild)

	body := realignBaselineEbuild
	if !baselineDiffers {
		body = realignOursEbuild
	}
	realignWriteEbuild(t, gentooPath, "media-libs", "gst-plugins-qt6", "1.29.2", body)
	if carryBaseline {
		if err := os.MkdirAll(filepath.Join(gentooPath, "profiles"), 0o750); err != nil {
			t.Fatalf("mkdir gentoo/profiles: %v", err)
		}
		if err := os.WriteFile(filepath.Join(gentooPath, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
			t.Fatalf("write repo_name: %v", err)
		}
	}

	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfg := "overlay:\n  path: " + overlayPath + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		"repositories:\n" +
		"  gentoo:\n    provider: local\n    path: " + gentooPath + "\n" +
		"  guru:\n    provider: local\n    path: " + gentooPath + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// No `claude` on PATH: the model is unreachable in every case below, which
	// is the state `--no-review` and a machine without the CLI are both in.
	t.Setenv("PATH", t.TempDir())

	// The rendering is compared byte for byte, so colour must be off for BOTH
	// the captured run and the expectation built beside it.
	origNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = origNoColor })

	return realignFixture{overlayPath: overlayPath, gentooPath: gentooPath}
}

// realignFlags saves and restores every package-level flag a case touches, so
// the order tests run in cannot change what any of them asserts.
func realignFlags(t *testing.T, realign, noReview bool) {
	t.Helper()
	origRealign, origNoReview, origTimeout := compareRealign, compareNoReview, compareTimeout
	t.Cleanup(func() {
		compareRealign, compareNoReview, compareTimeout = origRealign, origNoReview, origTimeout
	})
	compareRealign, compareNoReview, compareTimeout = realign, noReview, 1
}

// realignRun executes the command and returns what it wrote to stdout together
// with its exit code. withExitIntercept reports -1 when osExit was never called,
// which IS exit 0 — the command returns normally on the success path — so it is
// normalised here rather than at each call site.
//
// The progress line ("\r  Checking: ...") and the clear sequence the command
// writes before the report are stripped: they are terminal control, not report
// bytes, and the byte-identical promise is about the report.
func realignRun(t *testing.T, args []string) (stdout string, code int) {
	t.Helper()
	out := captureStdout(t, func() {
		code = withExitIntercept(func() { runCompare(compareCmd, args) })
	})
	if code == -1 {
		code = 0
	}
	if i := strings.LastIndex(out, "\r"); i >= 0 {
		out = out[i+1:]
	}
	return out, code
}

// realignExpectedRendering rebuilds, from the same fixture and with the options
// the SHIPPED command builds today, the report bytes a run must produce. It is
// deliberately written without reference to anything this story adds: if the new
// code renders one extra character on the no-realign path, these bytes differ.
func realignExpectedRendering(t *testing.T, fx realignFixture) (string, *overlay.CompareReport) {
	t.Helper()
	prov, err := provider.NewProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "local", Path: fx.gentooPath}, false)
	if err != nil {
		t.Fatalf("building the local provider: %v", err)
	}
	t.Cleanup(func() { _ = prov.Close() })

	scan, err := overlay.ScanOverlay(fx.overlayPath)
	if err != nil {
		t.Fatalf("scanning the fixture overlay: %v", err)
	}

	opts := overlay.CompareOptions{
		IncludeSynced: true, // what runCompare sets when --only-outdated is off
		Concurrency:   compareConcurrency,
		Ctx:           context.Background(),
		OverlayPath:   fx.overlayPath,
		// IncludeNotInRemote is deliberately ABSENT. D1: switching it on
		// unconditionally would give the Bentoo-only packages rows they do not
		// have today and change `counted - len(report.Results)` under everyone.
	}
	report, err := overlay.CompareWithProvider(scan.Packages, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	// The two annotation passes the shipped command runs before rendering. The
	// reviewer is nil because PATH holds no `claude`, which is also the state
	// the captured run is in.
	overlay.AnnotateAuthorship(report, prov, opts)
	overlay.AnnotateReviews(report, nil, prov, opts)
	return overlay.FormatReport(report), report
}

// TestCompareCmdHasRealignFlag pins the surface R7.1 chose: the review is a flag
// on the existing command, not a fourth top-level verb.
//
// _Requirements: R7, R7.1_
func TestCompareCmdHasRealignFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("realign")
	if flag == nil {
		t.Fatal("compare has no --realign flag; R7.1 puts the baseline review on the existing comparison command")
	}
	if flag.DefValue != "false" {
		t.Errorf("--realign defaults to %q, want \"false\" — a review that ran by default would change the shipped behaviour of every invocation (R7.2)", flag.DefValue)
	}
}

// TestCompareWithoutRealignIsByteIdentical is the quality gate stated as a test.
//
// _Requirements: R7, R7.2_
func TestCompareWithoutRealignIsByteIdentical(t *testing.T) {
	t.Run("the rendered report is the shipped bytes", func(t *testing.T) {
		fx := realignSetup(t, true, true)
		realignFlags(t, false, false)

		want, report := realignExpectedRendering(t, fx)
		got, code := realignRun(t, nil)

		if got != want {
			t.Errorf("the no-realign rendering changed.\n got:\n%s\nwant:\n%s", got, want)
		}
		if code != 0 {
			t.Errorf("exit code is %d, want 0 — the shipped invocation exits 0 on a healthy overlay", code)
		}

		// The package set, asserted where a reader can see it: the Bentoo-only
		// package has no row today and must not gain one.
		if strings.Contains(got, "zed") {
			t.Error("app-editors/zed has a row without --realign — IncludeNotInRemote was switched on for everyone (D1)")
		}
		if len(report.Results) != 1 {
			t.Errorf("the shipped options produced %d rows, want 1; the fixture has one package ::gentoo carries", len(report.Results))
		}

		// The summary arithmetic, pinned line for line. `Found in both` is
		// derived from three counters, so a story that added a fourth without
		// noticing would move it.
		wantSummary := []string{
			"\nSummary:",
			"  Total packages scanned: 2",
			"  Found in both repos: 1",
			"  Only in Bentoo: 1",
		}
		gotSummary := comparisonSummaryLines(report)
		if len(gotSummary) != len(wantSummary) {
			t.Fatalf("summary has %d lines, want %d:\ngot  %q\nwant %q", len(gotSummary), len(wantSummary), gotSummary, wantSummary)
		}
		for i := range wantSummary {
			if gotSummary[i] != wantSummary[i] {
				t.Errorf("summary line %d is %q, want %q", i, gotSummary[i], wantSummary[i])
			}
		}
	})

	t.Run("a tree the review would refuse does not touch the shipped path", func(t *testing.T) {
		// carryBaseline=false is the state that makes --realign exit non-zero
		// (R1.5). Without the flag it is nobody's business: `compare` has never
		// needed a local ::gentoo and must not start now.
		fx := realignSetup(t, true, false)
		realignFlags(t, false, false)

		want, _ := realignExpectedRendering(t, fx)
		got, code := realignRun(t, nil)

		if code != 0 {
			t.Errorf("exit code is %d without --realign, want 0 — the review's failure mode leaked into the shipped path", code)
		}
		if got != want {
			t.Errorf("the no-realign rendering changed when the baseline tree was absent.\n got:\n%s\nwant:\n%s", got, want)
		}
	})
}

// TestCompareRealignExitCodes is D9 and R7.5: the exit code reports the review's
// own outcome and nothing about the overlay's shape.
//
// _Requirements: R7, R7.5, R1.5, R4.4_
func TestCompareRealignExitCodes(t *testing.T) {
	cases := []struct {
		name            string
		baselineDiffers bool
		carryBaseline   bool
		wantZero        bool
		why             string
	}{
		{
			name:            "divergences found is not a failure",
			baselineDiffers: true,
			carryBaseline:   true,
			wantZero:        true,
			why:             "an overlay of a derived distribution HAS divergences; an exit code counting them is non-zero forever and ignored within a week (D9)",
		},
		{
			name:            "no divergences at all is still 0",
			baselineDiffers: false,
			carryBaseline:   true,
			wantZero:        true,
			why:             "nothing diverged, nothing failed",
		},
		{
			name:            "no baseline tree is the one non-zero condition",
			baselineDiffers: true,
			carryBaseline:   false,
			wantZero:        false,
			why:             "the review could not do its job and must not report success (R1.5, D9)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			realignSetup(t, tc.baselineDiffers, tc.carryBaseline)
			realignFlags(t, true, false)

			_, code := realignRun(t, nil)

			if tc.wantZero && code != 0 {
				t.Errorf("exit code is %d, want 0 — %s", code, tc.why)
			}
			if !tc.wantZero && code == 0 {
				t.Errorf("exit code is 0, want non-zero — %s", tc.why)
			}
		})
	}
}

// TestCompareRealignUnreachableModelExitsZero is R4.4 with D9's reasoning: the
// deterministic half of the report — the baseline, the structural axes, the
// declarations, the reduction — is complete and useful, so the only thing the
// run owes the operator is to say the verdicts are missing.
//
// PATH is empty, so there is no `claude` to reach; this is the same path a
// machine without the CLI is permanently on.
//
// _Requirements: R4, R4.4_
func TestCompareRealignUnreachableModelExitsZero(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, true, false)

	got, code := realignRun(t, nil)

	if code != 0 {
		t.Errorf("exit code is %d with no model reachable, want 0 — AnnotateReviews already warns and exits 0, and the deterministic report is complete (D9)", code)
	}
	if !strings.Contains(strings.ToLower(got), "no verdict") {
		t.Errorf("the report does not say that no verdict was produced; R4.4 requires it to, because a divergence with no verdict and a divergence judged justified look identical otherwise.\nreport:\n%s", got)
	}
}

// TestCompareRealignStillAnswersUnderNoReview is the quality gate "the inherit
// axis finding is produced with --no-review, with no model wired". A signal this
// cheap has no business depending on a provider being configured.
//
// _Requirements: R2, R2.4, R7.2_
func TestCompareRealignStillAnswersUnderNoReview(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, true, true) // --realign --no-review

	got, code := realignRun(t, nil)

	if code != 0 {
		t.Errorf("exit code is %d, want 0", code)
	}
	if !strings.Contains(got, "gstreamer-meson") {
		t.Errorf("the report names no inherit finding under --no-review; this is the finding that catches issue #33 (M-D).\nreport:\n%s", got)
	}
}

// TestRealignPreflightRefusesWhatThisInvocationCannotDo is R7.4. Both refusals
// are usage errors rather than SKIPPED runs: nothing was examined, and the
// operator needs to know the request itself was impossible — which is a
// different sentence from "we looked and found nothing" (D9).
//
// The API-only case is asserted here rather than end to end on purpose:
// constructing a GitHub provider makes runCompare attempt a rate-limit call, and
// no test in this suite may need the network.
//
// _Requirements: R7, R7.4_
func TestRealignPreflightRefusesWhatThisInvocationCannotDo(t *testing.T) {
	local, err := provider.NewProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "local", Path: t.TempDir()}, false)
	if err != nil {
		t.Fatalf("building the local provider: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })

	api, err := provider.NewProvider(&provider.RepositoryInfo{
		Name: "gentoo", Provider: "github", URL: "https://github.com/gentoo/gentoo",
	}, false)
	if err != nil {
		t.Fatalf("building the API provider: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cases := []struct {
		name     string
		repo     string
		prov     provider.Provider
		wantErr  bool
		mustName []string
	}{
		{
			name:    "gentoo on a local tree is what the review is for",
			repo:    "gentoo",
			prov:    local,
			wantErr: false,
		},
		{
			name:     "another repository is refused",
			repo:     "guru",
			prov:     local,
			wantErr:  true,
			mustName: []string{"guru", "gentoo"},
		},
		{
			name:     "an API-only provider is refused",
			repo:     "gentoo",
			prov:     api,
			wantErr:  true,
			mustName: []string{"local"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := realignPreflight(tc.repo, tc.prov)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("realignPreflight(%q, local) = %v, want nil", tc.repo, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("realignPreflight(%q, %s) = nil, want a refusal — the comparison would read the baseline from ::gentoo while comparing against something else (R7.4)", tc.repo, tc.prov.GetName())
			}
			for _, want := range tc.mustName {
				if !strings.Contains(strings.ToLower(err.Error()), want) {
					t.Errorf("the refusal is %q, want it to name %q — R7.4 requires the reason, not just the refusal", err.Error(), want)
				}
			}
		})
	}
}

// TestCompareRealignAgainstAnotherRepositoryRefusesBeforeLooking is the same
// refusal end to end, and it asserts WHEN as well as WHAT: nothing may be
// compared, so nothing may be printed. A refusal that arrived after the scan
// would have spent the run before saying the request was impossible.
//
// _Requirements: R7, R7.4_
func TestCompareRealignAgainstAnotherRepositoryRefusesBeforeLooking(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, true, false)

	got, code := realignRun(t, []string{"guru"})

	if code == 0 {
		t.Error("`compare guru --realign` exited 0, want a usage error — the baseline would be read from ::gentoo while the comparison ran against GURU (R7.4)")
	}
	if strings.TrimSpace(got) != "" {
		t.Errorf("the refused run printed a report; it must refuse before comparing anything.\nstdout:\n%s", got)
	}
}
