//go:build live

package validate

// Authored for story 031, sub-task 8.2 — R1, R1.1, R4.1.
//
// The opt-in half of the golden pair. golden_test.go proves the gate against a
// synthetic archive built in-process; this file proves it against the REAL
// 5.7 MB gst-plugins-good tarball, which is never committed. Run it with:
//
//	go test -tags live ./internal/autoupdate/validate/...
//
// The build tag keeps the default suite offline and hermetic. When the tarball
// is not on the host, the test SKIPS WITH A REASON NAMING THE FILE — it never
// passes silently, because a silent pass here would be the same unverified
// green the whole story exists to remove.
//
// Red is DEFERRED to Run mode, and doubly so: this file is not even compiled
// without the tag.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// liveArchive locates the real tarball in the host's distdir, or skips.
func liveArchive(t *testing.T, name string) string {
	t.Helper()
	dir, found := distfiles.Locate("", "")
	if !found {
		t.Skipf("no distdir on this host; %s cannot be read", name)
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not in %s: %v — fetch it, or run without -tags live", name, dir, err)
	}
	return path
}

// TestLive_UpstreamDroppedAalibAndLibcacaAt1292 is the measurement the source
// report made by hand, turned into an assertion: 1.29.2 declares neither
// option, while 1.28.6 declares both.
func TestLive_UpstreamDroppedAalibAndLibcacaAt1292(t *testing.T) {
	newer := liveArchive(t, "gst-plugins-good-1.29.2.tar.xz")

	got, err := OptionsFromArchive(context.Background(), newer)
	if err != nil {
		t.Fatalf("OptionsFromArchive(%s): %v", newer, err)
	}

	declared := map[string]bool{}
	for _, o := range got.Root {
		declared[o.Name] = true
	}
	for _, gone := range []string{"aalib", "libcaca"} {
		if declared[gone] {
			t.Errorf("1.29.2 still declares %q; the premise of issue #33 no longer holds and this story's golden case needs revisiting", gone)
		}
	}
	if len(declared) == 0 {
		t.Fatal("read zero options from the real archive; the extraction is not finding the option file")
	}
	if !declared["qt6"] {
		t.Error("1.29.2 does not declare qt6, which the ebuild passes — the extraction is reading the wrong member")
	}
}

func TestLive_ThePreviousVersionStillDeclaresBoth(t *testing.T) {
	older := liveArchive(t, "gst-plugins-good-1.28.6.tar.xz")

	got, err := OptionsFromArchive(context.Background(), older)
	if err != nil {
		t.Fatalf("OptionsFromArchive(%s): %v", older, err)
	}

	declared := map[string]bool{}
	for _, o := range got.Root {
		declared[o.Name] = true
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !declared[want] {
			t.Errorf("1.28.6 does not declare %q; the two versions would then differ for some other reason than the one measured", want)
		}
	}
}

// TestLive_ReadsTheRealMesonOptionsFilename pins the naming detail that made
// this package worth checking at all: gst-plugins-good ships `meson.options`,
// the post-Meson-1.1 name, not `meson_options.txt`.
func TestLive_ReadsTheRealMesonOptionsFilename(t *testing.T) {
	newer := liveArchive(t, "gst-plugins-good-1.29.2.tar.xz")

	got, err := OptionsFromArchive(context.Background(), newer)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	var read string
	for _, s := range got.Sources {
		if strings.HasSuffix(s, "meson.options") || strings.HasSuffix(s, "meson_options.txt") {
			read = s
			break
		}
	}
	if read == "" {
		t.Fatalf("Sources %v records no option file; the gate cannot show its evidence", got.Sources)
	}
	t.Logf("read upstream declarations from %s", read)
}

// liveOverlay resolves the overlay whose eclasses and profiles the staged tree
// copies. It is taken from the environment rather than discovered, because a
// live test that guessed which overlay to read could validate against a tree the
// operator did not mean.
func liveOverlay(t *testing.T) string {
	t.Helper()
	const envVar = "BENTOO_LIVE_OVERLAY"
	root := os.Getenv(envVar)
	if root == "" {
		t.Skipf("%s is not set; point it at the overlay whose eclass/ and profiles/ the staged tree should copy, "+
			"or run without -tags live", envVar)
	}
	for _, rel := range []string{"eclass", "profiles", "metadata/layout.conf"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Skipf("%s=%s does not look like an overlay: %s is missing (%v)", envVar, root, rel, err)
		}
	}
	return root
}

// liveEbuildTool checks that the thing under test exists at all.
func liveEbuildTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ebuild"); err != nil {
		t.Skipf("`ebuild` is not on PATH: %v — this test drives a real Portage invocation and there is nothing to drive", err)
	}
}

// liveEbuildSource reads the real ebuild for one version out of the overlay, so
// the staged candidate is the file the overlay actually publishes rather than a
// synthetic stand-in. A synthetic one would prove the harness, not the bump.
func liveEbuildSource(t *testing.T, overlay, version string) []byte {
	t.Helper()
	path := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-"+version+".ebuild")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s is not in the overlay: %v — the golden pair needs both versions' ebuilds present", path, err)
	}
	return body
}

// MERGE FRAGMENT — story 037, sub-task 5.1 (the live golden pair through the
// seams). E2E RUNG — tool: go-test-live. DO NOT run in Phase 3; Red is
// confirmed in Run mode on kekkai, where the standing measurement (story 033,
// 2026-08-10) is 4 PASS / 2 FAIL and this story's acceptance is 6/6.
//
// Target file: internal/autoupdate/validate/live_test.go (//go:build live).
//
// THIS FRAGMENT REPLACES A FUNCTION RATHER THAN ONLY APPENDING. The existing
// helper `liveStagedGate` (the one that calls Stage and RunBuildGates
// directly, currently between liveEbuildSource and
// TestLiveStaged_TheBumpThatBrokeFailsTheConfigureGate) must be DELETED and
// the version below put in its place — Go refuses two functions of one name,
// and the whole point of 5.1 is that the harness stops hand-building what the
// seams now carry. Every EXISTING test and its assertions stay untouched:
// TestLiveStaged_TheBumpThatBrokeFailsTheConfigureGate still demands configure
// FAILED naming `aalib` (`meson.build:1:0: ERROR: Unknown option: "aalib"`,
// measured 2026-08-09), TestLiveStaged_ThePreviousVersionConfiguresCleanly
// still demands PASS (`>>> Source configured.`), and
// TestLiveStaged_ThePublishedOverlayIsUntouchedByARealBuild still hashes the
// real overlay around the failing half.
//
// IMPORTS: none added — context, os, path/filepath, testing and the distfiles
// import are already in the target's block.
//
// # Why the harness routes through Run and not through Stage+RunBuildGates
//
// R5.3's claim is "opposite verdicts through the REAL staged path". The old
// harness staged a tree with no Manifest and drove RunBuildGates directly, so
// both configure runs died in Portage's setup phase — the 2 FAIL half of
// 033's measurement, and precisely the setup-failure-read-as-outcome this
// story removes. Driving validate.Run with the seams populated the way the
// standalone command populates them (published bytes — the same-version case,
// where the published digests are the right ones) makes the live pair prove
// the WHOLE path: Stage → materialise 0o600 → RunBuildGates, with nothing
// hand-fed behind the seam's back.
//
// # Cost, stated honestly
//
// Run walks every version the selector matches, so each call configures BOTH
// versions and the pair costs four configures instead of two. That is the
// price of proving the real path end to end; if kekkai minutes matter, Run
// mode may memoise ONE report across the two tests — but must then keep the
// per-version skip behaviour of liveArchive intact.

// liveStagedGate validates one version of the golden package at configure
// depth THROUGH THE SEAMS — validate.Run's own depth path, fed the published
// Manifest exactly as `overlay validate --depth=configure` feeds it — and
// returns the gates it reported for that version.
func liveStagedGate(t *testing.T, version string) []GateResult {
	t.Helper()
	liveEbuildTool(t)
	overlay := liveOverlay(t)

	// The distfile has to be on disk: `ebuild … clean configure` unpacks it, and
	// nothing here is allowed to fetch. The skip names the file so the operator
	// can go and get it.
	_ = liveArchive(t, "gst-plugins-good-"+version+".tar.xz")

	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	manifestPath := filepath.Join(pkgDir, "Manifest")
	published, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Skipf("the published Manifest is not readable at %s: %v — the same-version seam feeds published "+
			"bytes, and without them there is nothing to feed", manifestPath, err)
	}

	report, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Selector:    "media-plugins/gst-plugins-qt6",
		Depth:       DepthConfigure.String(),
		StagingRoot: filepath.Join(t.TempDir(), "staging"),
		// The helper this fragment replaced passed a LogDir to its own
		// RunBuildGates call, so the live run keeps retaining its transcript.
		// It is NOT what makes the configure gate name `aalib` — that was a
		// diagnosis made and refuted in Run mode; the cause was failureExcerpt
		// quoting die's epilogue instead of the error before it. No assertion is
		// touched either way.
		LogDir: t.TempDir(),
		// S037-R5: the seams, populated the way the standalone command populates
		// them — a realignment-shaped, same-version consumer. The names come from
		// the same published Manifest whose bytes travel, so the option gate and
		// the build gates answer about the same archives.
		DistNames: func(string) ([]string, error) {
			return distfiles.ParseManifestDistFilenames(manifestPath), nil
		},
		StagedManifest: func(string) ([]byte, error) {
			return append([]byte(nil), published...), nil
		},
	})
	if err != nil {
		t.Fatalf("Run at configure depth for %s: %v — a failing build is a reported outcome, not an aborted run",
			version, err)
	}

	for _, res := range report.Results {
		if res.Version == version {
			return res.Gates
		}
	}
	t.Fatalf("the run reported no result for version %s: %+v", version, report.Results)
	return nil
}

// TestLiveStaged_TheBumpThatBrokeFailsTheConfigureGate is design M-A turned into
// an assertion. This is the measurement that proved the configure gate reaches
// the defect the static gate already catches — two independent gates, one bug.
func TestLiveStaged_TheBumpThatBrokeFailsTheConfigureGate(t *testing.T) {
	gates := liveStagedGate(t, "1.29.2")

	patches := gateNamed(t, gates, GatePatches)
	if patches.Outcome != OutcomePass {
		t.Errorf("patches gate: got %q (reason %q), want PASS — the failure is at configure, not before it",
			patches.Outcome, patches.Reason)
	}

	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome != OutcomeFailed {
		t.Fatalf("configure gate: got %q (reason %q), want FAILED.\n"+
			"Measured on 2026-08-09: `ebuild … clean configure` exits 1 with "+
			"`meson.build:1:0: ERROR: Unknown option: \"aalib\".`\n"+
			"If this now passes, upstream or the ebuild changed and the story's golden case needs revisiting.",
			cfg.Outcome, cfg.Reason)
	}

	var details []string
	for _, f := range cfg.Findings {
		details = append(details, f.Detail)
	}
	joined := strings.Join(details, " | ") + " " + cfg.Reason
	if !strings.Contains(joined, "aalib") {
		t.Errorf("the configure failure does not name the option upstream removed: %q", joined)
	}
	// D13: Portage stamps its error with the STAGING repo's name, and the
	// operator asked about the real atom.
	if strings.Contains(joined, "bentoo-staging") {
		t.Errorf("the staging repo name leaked into the report: %q", joined)
	}
	if !strings.Contains(joined, "media-plugins/gst-plugins-qt6") {
		t.Errorf("the report does not name the real atom: %q", joined)
	}
}

// TestLiveStaged_ThePreviousVersionConfiguresCleanly is the other measured half:
// EXIT=0 and `>>> Source configured.`. Without it, a gate that failed everything
// would look like success here.
func TestLiveStaged_ThePreviousVersionConfiguresCleanly(t *testing.T) {
	gates := liveStagedGate(t, "1.28.6")

	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome != OutcomePass {
		t.Fatalf("configure gate for 1.28.6: got %q (reason %q), want PASS.\n"+
			"Measured on 2026-08-09: the same command exits 0 with `>>> Source configured.`", cfg.Outcome, cfg.Reason)
	}
	// R5.2: even a real pass names its own reach.
	if !strings.Contains(strings.ToLower(cfg.Reason), "compil") {
		t.Errorf("the configure PASS reason %q does not state that a configure pass does not cover compilation", cfg.Reason)
	}
	for _, g := range gates {
		for _, f := range g.Findings {
			if f.Severity == SeverityError {
				t.Errorf("1.28.6 produced an error finding on the %s gate: %q", g.Gate, f.Detail)
			}
		}
	}
}

// TestLiveStaged_ThePublishedOverlayIsUntouchedByARealBuild is R3.2 against a
// real Portage run rather than a stubbed one. Every hermetic assertion about
// byte-identity is only as good as the assumption that a real `ebuild` behaves
// like the seam — and this is the one place that assumption is tested.
func TestLiveStaged_ThePublishedOverlayIsUntouchedByARealBuild(t *testing.T) {
	liveEbuildTool(t)
	overlay := liveOverlay(t)

	before := hashTree(t, overlay)
	_ = liveStagedGate(t, "1.29.2") // the failing half: the worst case for stray writes

	if after := hashTree(t, overlay); after != before {
		t.Errorf("a real ebuild invocation modified the published overlay: %s -> %s\n"+
			"the staged tree masters onto gentoo and carries its own copies of eclass/ and profiles/ precisely so this cannot happen (D2)",
			before, after)
	}
}
