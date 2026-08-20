package main

// Authored for story 031, sub-task 6.1 — R5, R5.1, R5.2, R5.3, R5.4, R5.7.
//
// Written from the contract: design.md's CLI table fixes the three selector
// forms and the three exit codes.
//
// This file pins the seam `validateRunnerFn`, in the shape overlay_prune.go
// already uses — and for the same reason that file states: a test has to be
// able to prove the runner was NOT REACHED, which is only observable if
// reaching it goes through a replaceable name.
//
// captureExit comes from snapshot_test.go in this package.
//
// Red is DEFERRED to Run mode: the command does not exist yet.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// There is deliberately NO overlay fixture in this file. The runner is stubbed
// in every test below, so nothing ever scans a tree — what is under test here
// is SELECTION: which options the command hands the runner, and whether it
// reaches the runner at all. The runner's own behaviour over a real tree is
// run_test.go's and golden_test.go's job, in the validate package.

// stubValidateRunner captures the options the command hands the runner and
// answers with a clean report, so the assertions are about SELECTION only.
func stubValidateRunner(t *testing.T, report validate.Report) *validate.Options {
	t.Helper()
	seen := &validate.Options{}
	orig := validateRunnerFn
	validateRunnerFn = func(_ context.Context, opts validate.Options) (validate.Report, error) {
		*seen = opts
		return report, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })
	return seen
}

func TestOverlayValidate_NoSelectorTakesTheWholeOverlay(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{})
	})

	if exited && code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if seen.Selector != "" {
		t.Errorf("Selector: got %q, want empty for a whole-overlay run", seen.Selector)
	}
}

func TestOverlayValidate_CategorySelectorNarrows(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins"})
	})

	if seen.Selector != "media-plugins" {
		t.Errorf("Selector: got %q, want %q", seen.Selector, "media-plugins")
	}
}

func TestOverlayValidate_PackageSelectorNarrows(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if seen.Selector != "media-plugins/gst-plugins-qt6" {
		t.Errorf("Selector: got %q, want %q", seen.Selector, "media-plugins/gst-plugins-qt6")
	}
}

// TestOverlayValidate_UnknownSelectorExitsTwo is R5.7. Exit 2 is the usage
// code: the operator asked about something that is not there, which is neither
// a clean run nor a finding.
func TestOverlayValidate_UnknownSelectorExitsTwo(t *testing.T) {
	stubValidateRunner(t, validate.Report{UnmatchedSelector: "media-plugins/does-not-exist"})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/does-not-exist"})
	})

	if !exited {
		t.Fatal("the command did not exit")
	}
	if code != 2 {
		t.Errorf("exit code: got %d, want 2", code)
	}
}

// TestOverlayValidate_ErrorFindingExitsOne pins the difference between a gate
// that found something and a command that was used wrongly.
func TestOverlayValidate_ErrorFindingExitsOne(t *testing.T) {
	stubValidateRunner(t, validate.Report{
		Results: []validate.EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Gates: []validate.GateResult{{
				Gate:    validate.GateOptions,
				Outcome: validate.OutcomeFailed,
				Findings: []validate.Finding{
					{Gate: validate.GateOptions, Severity: validate.SeverityError, Detail: "-Daalib= is undeclared upstream"},
				},
			}},
		}},
	})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if !exited || code != 1 {
		t.Errorf("exit code: got %d (exited=%v), want 1", code, exited)
	}
}

// TestOverlayValidate_UnknownSelectorNeverReachesTheRunner is the assertion the
// seam exists for: a usage error must be answered without doing any work.
func TestOverlayValidate_UnknownSelectorNeverReachesTheRunner(t *testing.T) {
	var reached bool
	orig := validateRunnerFn
	validateRunnerFn = func(context.Context, validate.Options) (validate.Report, error) {
		reached = true
		return validate.Report{}, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"not-a-category/nor-a-package/too-many-parts"})
	})

	if reached {
		t.Error("a malformed selector reached the runner; it should be rejected before any work")
	}
}

// TestOverlayValidate_NoDepthKeepsTheShippedReadOnlyContract is R11.3. Depth
// `options` is the default, no staged tree is prepared, and the command behaves
// exactly as it does today.
func TestOverlayValidate_NoDepthKeepsTheShippedReadOnlyContract(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if exited && code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if seen.Depth != "" && seen.Depth != "options" {
		t.Errorf("Depth = %q for an invocation with no --depth; the default is the static gate and nothing else (R11.3)", seen.Depth)
	}
	if seen.StagingRoot != "" {
		t.Errorf("StagingRoot = %q with no --depth; the shipped command stages nothing, builds nothing and needs no scratch directory",
			seen.StagingRoot)
	}
}

// TestOverlayValidate_DepthIsHandedToTheRunner is R11.1, across the whole
// ladder. Each case builds its own command, which is also what proves the flag
// is not bound to a package variable.
func TestOverlayValidate_DepthIsHandedToTheRunner(t *testing.T) {
	for _, depth := range []string{"none", "options", "patches", "configure", "compile"} {
		t.Run(depth, func(t *testing.T) {
			seen := stubValidateRunner(t, validate.Report{})

			captureExit(t, func() {
				runValidate(newValidateCmd(), []string{"--depth=" + depth, "media-plugins/gst-plugins-qt6"})
			})

			if seen.Depth != depth {
				t.Errorf("Depth = %q, want %q", seen.Depth, depth)
			}
			if seen.Selector != "media-plugins/gst-plugins-qt6" {
				t.Errorf("Selector = %q; the positional argument must survive alongside the flag", seen.Selector)
			}
		})
	}
}

// TestOverlayValidate_DepthDoesNotLeakBetweenCommands is the convention at
// overlay_validate.go:43-47, asserted rather than trusted. A package-level flag
// var would make the second invocation inherit the first's compile depth.
func TestOverlayValidate_DepthDoesNotLeakBetweenCommands(t *testing.T) {
	first := stubValidateRunner(t, validate.Report{})
	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=compile", "media-plugins/gst-plugins-qt6"})
	})
	if first.Depth != "compile" {
		t.Fatalf("Depth = %q on the first invocation, want compile", first.Depth)
	}

	second := stubValidateRunner(t, validate.Report{})
	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if second.Depth == "compile" {
		t.Error("the second invocation inherited --depth=compile from the first; the flag is read off the command, " +
			"never bound to a package var (overlay_validate.go:43-47)")
	}
}

// TestOverlayValidate_BuildDepthStagesAndNamesWhere is R11.2's first half: above
// `options` the gates need a tree, and it is not the published one.
func TestOverlayValidate_BuildDepthStagesAndNamesWhere(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=configure", "media-plugins/gst-plugins-qt6"})
	})

	if seen.StagingRoot == "" {
		t.Fatal("--depth=configure passed no StagingRoot; the build gates would then run against the published overlay, " +
			"which is the one thing this story exists to prevent (R11.2)")
	}
	if seen.Overlay != "" && strings.HasPrefix(seen.StagingRoot, seen.Overlay) {
		t.Errorf("StagingRoot %q is inside the overlay %q; ScanOverlay would see an unclaimed ebuild and --clean would delete it",
			seen.StagingRoot, seen.Overlay)
	}
}

// TestOverlayValidate_BuildDepthLeavesTheOverlayByteIdentical is R11.2's second
// half, asserted where the command can see it: the runner is stubbed, so what is
// pinned here is that the COMMAND writes nothing of its own around the call —
// the runner's own byte-identity is asserted in the validate package
// (golden_test.go), where the tree really exists.
func TestOverlayValidate_BuildDepthLeavesTheOverlayByteIdentical(t *testing.T) {
	var reached bool
	orig := validateRunnerFn
	validateRunnerFn = func(_ context.Context, opts validate.Options) (validate.Report, error) {
		reached = true
		if opts.Overlay == "" {
			return validate.Report{}, nil
		}
		return validate.Report{Overlay: opts.Overlay}, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=compile", "media-plugins/gst-plugins-qt6"})
	})

	if !reached {
		t.Fatal("the runner was never reached for a build-depth invocation")
	}
}

// TestOverlayValidate_UnknownDepthExitsOneNamingTheValidSet keeps a typo cheap.
//
// EXIT 1, NOT 2, AND THE DISTINCTION IS THE SHIPPED CONTRACT. Exit 2 is reserved
// for "the selector names something the overlay does not hold"
// (overlay_validate.go:70-73). A --depth value that does not parse is not a
// selector miss: the overlay was never consulted, and a CI script that branches
// on 2 to mean "unknown package" would silently mis-handle a typo in a flag.
// The message lists the five valid names so the operator does not have to go and
// read the source.
//
// An earlier draft of this file asserted 2 on the mistaken premise that 2 is a
// general usage code. It is not; it names one specific condition.
func TestOverlayValidate_UnknownDepthExitsOneNamingTheValidSet(t *testing.T) {
	var reached bool
	orig := validateRunnerFn
	validateRunnerFn = func(_ context.Context, _ validate.Options) (validate.Report, error) {
		reached = true
		return validate.Report{}, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })

	// captureStdout OUTSIDE captureExit, the order captureStdoutExit
	// (snapshot_apply_test.go:110) already uses for a verb that prints and then
	// exits. Nested the other way, osExit's stub panics, the panic unwinds
	// through captureStdout before captureExit recovers it, and the assignment
	// never runs — out would be "" whatever was printed. Surface only; every
	// assertion below is the one that was authored.
	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runValidate(newValidateCmd(), []string{"--depth=shallow", "media-plugins/gst-plugins-qt6"})
		})
	})

	if !exited {
		t.Fatal("an unknown depth did not exit")
	}
	if code == 2 {
		t.Fatalf("exit code 2 for an unparseable --depth; 2 is reserved for a selector the overlay does not hold, " +
			"and nothing about a bad flag value says anything about the overlay's contents")
	}
	if code != 1 {
		t.Errorf("exit code: got %d, want 1 for a --depth value that does not parse", code)
	}
	if reached {
		t.Error("an unknown depth reached the runner; a usage error is answered before any work is done")
	}
	for _, name := range []string{"none", "options", "patches", "configure", "compile"} {
		if !strings.Contains(out, name) {
			t.Errorf("the diagnostic does not list the valid depth %q:\n%s", name, out)
		}
	}
}

// TestOverlayValidate_CompileDepthStillExitsOneOnAnErrorFinding pins that the
// three exit codes survive the new flag. A build-depth run that found a real
// problem must fail the same way a static one does, or a CI script that already
// keys on the exit status quietly stops noticing.
func TestOverlayValidate_CompileDepthStillExitsOneOnAnErrorFinding(t *testing.T) {
	stubValidateRunner(t, validate.Report{
		Results: []validate.EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Depth:   "configure",
			Gates: []validate.GateResult{{
				Gate:    validate.GateConfigure,
				Outcome: validate.OutcomeFailed,
				Findings: []validate.Finding{{
					Gate: validate.GateConfigure, Severity: validate.SeverityError,
					Detail: `meson.build:1:0: ERROR: Unknown option: "aalib".`,
				}},
			}},
		}},
	})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=configure", "media-plugins/gst-plugins-qt6"})
	})

	if !exited || code != 1 {
		t.Errorf("exit code: got %d (exited=%v), want 1 for an error finding from the configure gate", code, exited)
	}
}

// MERGE FRAGMENT — story 037, sub-task 4.1 (overlay_validate.go populates the
// seams).
//
// Target file: cmd/bentoo/overlay_validate_test.go (APPEND at the end).
// Do NOT repeat the `package main` clause.
//
// IMPORTS MERGE, necessary and sufficient: "os" and "path/filepath" join the
// target's existing block (context, strings, testing, and the validate
// import). One block, not two.
//
// # Symbols
//
// Added: the two tests. Borrowed, never re-declared: stubValidateRunner and
// captureExit (this file / snapshot_test.go).
//
// # PINNED CONTRACT (design D1, D3 — S037-R4, R4.1, R4.2, R4.3)
//
//	Options.DistNames      func(pkgDir string) ([]string, error)
//	Options.StagedManifest func(pkgDir string) ([]byte, error)
//
// Above depth `options` the command populates BOTH seams from the published
// `pkgDir/Manifest` — the same-version case, where the published digests are
// the right ones (Clarify decision). Without `--depth` the Options are
// constructed exactly as today: both seams nil, so
// TestOverlayValidate_DepthIsHandedToTheRunner's field-by-field capture stays
// honest and the shipped read-only contract survives (R4.3, R11.3).
//
// The producers are asserted BEHAVIOURALLY, by calling the captured funcs
// against a package directory this test lays out: they are keyed by the pkgDir
// they are handed (design D2 — Run walks many packages), and what must travel
// is the published Manifest's DIST record, digests included, because Portage
// verifies them (R4.2). Whether the command sends the file verbatim or its
// DIST lines only is deliberately NOT pinned — both satisfy R4.2, and the
// staged tree holds none of the files an AUX/EBUILD record would describe.

// TestOverlayValidate_BuildDepthPopulatesTheManifestSeams is R4.1/R4.2: the
// runner is handed producers that answer from the published Manifest.
func TestOverlayValidate_BuildDepthPopulatesTheManifestSeams(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=configure", "media-plugins/gst-plugins-qt6"})
	})

	if seen.DistNames == nil {
		t.Fatal("--depth=configure handed the runner no DistNames producer; the option gate over a staged " +
			"tree then has no source of names and reports SKIPPED — the exact skip R11.2's execution half removes (R4.1)")
	}
	if seen.StagedManifest == nil {
		t.Fatal("--depth=configure handed the runner no StagedManifest producer; the build gates then run " +
			"against a staged tree Portage refuses, and the depth is a deeper class of skip (R4.1)")
	}

	// The producers' behaviour, against a package directory of this test's own.
	pkgDir := t.TempDir()
	const distLine = "DIST gst-plugins-good-1.29.2.tar.gz 5872164 BLAKE2B feedface SHA512 cafebabe"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(distLine+"\n"), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}

	body, err := seen.StagedManifest(pkgDir)
	if err != nil {
		t.Fatalf("StagedManifest(%s): %v — the directory holds a readable Manifest", pkgDir, err)
	}
	if !strings.Contains(string(body), distLine) {
		t.Errorf("the supplied Manifest content does not carry the published DIST record with its digests:\n"+
			"got  %q\nwant it to contain %q\nPortage VERIFIES these digests, so anything less leaves the build "+
			"gates a Manifest they fail on (R4.2)", body, distLine)
	}

	names, err := seen.DistNames(pkgDir)
	if err != nil {
		t.Fatalf("DistNames(%s): %v", pkgDir, err)
	}
	found := false
	for _, name := range names {
		if name == "gst-plugins-good-1.29.2.tar.gz" {
			found = true
		}
	}
	if !found {
		t.Errorf("DistNames = %v; the published Manifest's distfile name did not travel, so the option gate "+
			"has nothing to select from (R4.1)", names)
	}
}

// TestOverlayValidate_NoDepthLeavesTheSeamsNil is R4.3: a depth-less run
// constructs Options exactly as today. nil and populated are different
// contracts inside the runner — nil parses the package's own Manifest, and a
// depth-less run must keep doing exactly that, byte for byte.
func TestOverlayValidate_NoDepthLeavesTheSeamsNil(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if seen.DistNames != nil {
		t.Error("a depth-less run populated DistNames; the shipped read-only contract is Options exactly as " +
			"today, and a seam nobody asked for is a behaviour change waiting to be noticed (R4.3)")
	}
	if seen.StagedManifest != nil {
		t.Error("a depth-less run populated StagedManifest; nothing travels on the shipped path (R4.3)")
	}
}

// TestPublishedManifestBytes_KeepsDistRecordsAndDropsTheRest is the regression
// the seam introduced by retiring manifestDistLines along with its caller.
//
// A Manifest is DIST-only just when the repository sets `thin-manifests = true`.
// Portage's default is thin=false, and then the file also names EBUILD, AUX and
// MISC records for files in the package directory. Stage copies the candidate
// ebuild and files/ — not metadata.xml and not the sibling ebuilds — so handing
// the full Manifest to the staged tree describes files that are not there,
// digestcheck raises FileNotFound, and `ebuild` dies before the first phase
// marker. Every build gate then reports SKIPPED for a package that would have
// built, which is the exact silence this story exists to remove.
func TestPublishedManifestBytes_KeepsDistRecordsAndDropsTheRest(t *testing.T) {
	pkgDir := t.TempDir()
	full := "AUX gst-plugins-qt6-1.29.2-qt6-detection.patch 512 BLAKE2B aa SHA512 bb\n" +
		"DIST gst-plugins-good-1.29.2.tar.xz 2048 BLAKE2B cc SHA512 dd\n" +
		"EBUILD gst-plugins-qt6-1.28.6.ebuild 900 BLAKE2B ee SHA512 ff\n" +
		"EBUILD gst-plugins-qt6-1.29.2.ebuild 910 BLAKE2B 11 SHA512 22\n" +
		"MISC metadata.xml 400 BLAKE2B 33 SHA512 44\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(full), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}

	got, err := publishedManifestBytes(pkgDir)
	if err != nil {
		t.Fatalf("publishedManifestBytes: %v", err)
	}

	want := "DIST gst-plugins-good-1.29.2.tar.xz 2048 BLAKE2B cc SHA512 dd\n"
	if string(got) != want {
		t.Errorf("the staged Manifest is not DIST-only:\n  got:  %q\n  want: %q", got, want)
	}
	for _, dropped := range []string{"EBUILD ", "AUX ", "MISC ", "metadata.xml", "1.28.6"} {
		if strings.Contains(string(got), dropped) {
			t.Errorf("%q survived into the staged Manifest; it names a file Stage never copies, so digestcheck "+
				"fails and the gate reports SKIPPED for an ebuild that is fine", dropped)
		}
	}
}

// TestPublishedManifestBytes_ADistLineTravelsByteForByte is the other half, and
// the reason this filters lines instead of parsing records: Portage verifies
// these digests against the archive on disk, so a line that survived a
// round-trip through some intermediate form is a line the build gates fail on.
func TestPublishedManifestBytes_ADistLineTravelsByteForByte(t *testing.T) {
	pkgDir := t.TempDir()
	dist := "DIST some-1.0.tar.gz 12345 BLAKE2B 0f1e2d SHA512 9a8b7c"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(dist+"\n"), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}

	got, err := publishedManifestBytes(pkgDir)
	if err != nil {
		t.Fatalf("publishedManifestBytes: %v", err)
	}
	if string(got) != dist+"\n" {
		t.Errorf("a DIST line did not survive unchanged:\n  got:  %q\n  want: %q", got, dist+"\n")
	}
}

// Authored for the S037 review, round 6 — the interrupted run's report.
//
// Run does not merely return an error when a sweep is stopped: it fills the
// report with one interruptedResult per package it never reached, because its
// governing rule is that a package in view is never left unmentioned. The
// command discarded that report and answered 2, which is Report.ExitCode's
// "the selector matched nothing" — so a stopped run and a mistyped selector
// were the same event at the shell, and `--json` emitted no document at all.

// interruptedValidateRunner answers with a partial report AND a cancellation,
// which is exactly the pair validate.Run returns when a sweep is stopped.
func interruptedValidateRunner(t *testing.T) {
	t.Helper()
	orig := validateRunnerFn
	validateRunnerFn = func(context.Context, validate.Options) (validate.Report, error) {
		return validate.Report{
			Results: []validate.EbuildResult{{
				Package: "media-plugins/gst-plugins-qt6",
				Version: "1.28.6",
				Gates: []validate.GateResult{{
					Gate:    validate.GateOptions,
					Outcome: validate.OutcomeSkipped,
					Reason:  "the run was interrupted before this ebuild was validated",
				}},
			}},
		}, fmt.Errorf("the validation run was interrupted: %w", context.Canceled)
	}
	t.Cleanup(func() { validateRunnerFn = orig })
}

func TestOverlayValidate_AnInterruptedRunRendersItsPartialReport(t *testing.T) {
	interruptedValidateRunner(t)

	cmd := newValidateCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("setting --json: %v", err)
	}

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runValidate(cmd, []string{}) })
	})

	if !exited {
		t.Fatal("an interrupted run did not exit at all")
	}
	if code == 2 {
		t.Errorf("an interrupted run exits 2, the code Report.ExitCode documents for a selector that matched " +
			"nothing; a script cannot tell a stopped run from a mistyped package name")
	}
	if code != 130 {
		t.Errorf("exit code %d, want 130 (128 + SIGINT)", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("--json emitted no document for an interrupted run; the partial report Run assembles " +
			"names the packages that went unexamined and it was thrown away")
	}
	if !strings.Contains(out, "gst-plugins-qt6") {
		t.Errorf("the emitted document does not name the package the run was stopped over: %s", out)
	}
}

func TestOverlayValidate_AnInterruptedTextRunAlsoExits130(t *testing.T) {
	interruptedValidateRunner(t)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runValidate(newValidateCmd(), []string{}) })
	})

	if !exited {
		t.Fatal("an interrupted run did not exit at all")
	}
	if code != 130 {
		t.Errorf("exit code %d, want 130; the text and --json paths must agree on what an interruption is", code)
	}
}

// TestOverlayValidate_HelpNoLongerPromisesAReadOnlyRunAtEveryDepth pins the
// help text against the change this story made to the command's behaviour.
//
// "Nothing is built, downloaded or changed" was ACCURATE on main: every build
// gate reachable from this entry point reported SKIPPED, so whatever --depth
// said, nothing was ever built. Wiring the seams made the gates real, and the
// promise became a command that compiles upstream code and fetches distfiles
// across every package the selector matches while its own help says it does
// neither. That is how someone starts a whole-overlay build by accident.
func TestOverlayValidate_HelpNoLongerPromisesAReadOnlyRunAtEveryDepth(t *testing.T) {
	long := newValidateCmd().Long

	if strings.Contains(long, "Nothing is built, downloaded or") {
		t.Error("the help still promises unconditionally that nothing is built or downloaded; " +
			"--depth above `options` does both")
	}
	if !strings.Contains(strings.ToLower(long), "at the default depth nothing is built") {
		t.Error("the help does not qualify the read-only promise by depth")
	}
	if !strings.Contains(long, "--depth") {
		t.Error("the help never names --depth, the flag that turns this into a command that builds")
	}
}

// TestOverlayValidate_RequireIsolationReachesTheRunner is the command half of
// the same policy bypass: the runner honours Options.RequireIsolation, but only
// if this command reads the key and hands it over.
//
// The config is written under a private XDG_CONFIG_HOME rather than read from
// the host, keeping the environment coupling this file's header calls out.
func TestOverlayValidate_RequireIsolationReachesTheRunner(t *testing.T) {
	xdg := t.TempDir()
	dir := filepath.Join(xdg, "bentoo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("laying out the config dir: %v", err)
	}
	body := "overlay:\n  path: " + t.TempDir() + "\nautoupdate:\n  validate:\n    require_isolation: true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	seen := stubValidateRunner(t, validate.Report{})
	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"--depth=configure", "media-plugins/gst-plugins-qt6"})
	})

	if !seen.RequireIsolation {
		t.Error("autoupdate.validate.require_isolation is set and the runner was handed RequireIsolation=false; " +
			"the identical gates honour that key under `overlay autoupdate`, so a build path that drops it " +
			"is the operator's policy silently not applying to one of the two commands that build")
	}
}

// TestPublishedManifestBytes_AMissingManifestIsTheNoDistfileClass is story 039's
// R4.1 reaching the package class it was written for.
//
// Under thin-manifests a Manifest holds DIST lines and nothing else, so a
// package with no distfile has NO MANIFEST FILE AT ALL — not an empty one. That
// is not a rare shape: acct-group/*, acct-user/*, virtual/* and, in ::bentoo,
// net-dns/bind-tools, app-eselect/eselect-nodejs and sys-kernel/linux-firmware
// all look like this today.
//
// This seam answered a missing file with an ERROR, which the prepared build
// reads as "a Manifest was expected and its production failed" and turns into a
// reported skip. So every one of those packages stayed permanently unmeasured —
// the exact class R4.1 exists to bring under a build gate — and no amount of
// work inside validate could reach them, because the fault was decided here.
//
// The distinction this restores is the same one story 039's D6 is about: "I
// could not look" and "I looked, there is nothing there" are different answers,
// and collapsing them into one is how a report comes to deny what it also proves.
func TestPublishedManifestBytes_AMissingManifestIsTheNoDistfileClass(t *testing.T) {
	pkgDir := t.TempDir() // a package directory with no Manifest in it

	got, err := publishedManifestBytes(pkgDir)

	if err != nil {
		t.Fatalf("publishedManifestBytes reported an error for a package that simply has no Manifest (%v); "+
			"under thin-manifests that is what a candidate needing no distfile LOOKS like, and reporting it "+
			"as a fault keeps the whole class out of every build gate (R4.1)", err)
	}
	if len(got) != 0 {
		t.Errorf("the seam produced %q for a package with no Manifest; there is nothing to produce, and "+
			"inventing content would be a guessed digest", got)
	}
}

// TestPublishedManifestBytes_AnUnreadableManifestIsStillAFault is the other half,
// and it is what stops the fix above from becoming "any read problem is fine".
//
// A Manifest that EXISTS and cannot be read is a fault about this host or this
// checkout, and it must keep travelling as an error: taking it for "no distfile
// required" would stage an empty Manifest over a package that has digests, and
// the gate would then report on a candidate nobody could describe.
func TestPublishedManifestBytes_AnUnreadableManifestIsStillAFault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable, so this case cannot be observed here")
	}
	pkgDir := t.TempDir()
	path := filepath.Join(pkgDir, "Manifest")
	if err := os.WriteFile(path, []byte("DIST x-1.0.tar.gz 1 BLAKE2B ab SHA512 cd\n"), 0o600); err != nil {
		t.Fatalf("writing the Manifest: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("sealing the Manifest: %v", err)
	}

	if _, err := publishedManifestBytes(pkgDir); err == nil {
		t.Error("a Manifest that exists and could not be read was reported as no Manifest at all; that " +
			"stages an empty Manifest over a package that has digests, and the gate then reports on a " +
			"candidate nobody could describe")
	}
}
