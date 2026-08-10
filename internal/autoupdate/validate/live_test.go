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

// liveStagedGate stages one version and runs the build gates at configure depth
// against the real host, returning the gates it reported.
func liveStagedGate(t *testing.T, version string) []GateResult {
	t.Helper()
	liveEbuildTool(t)
	overlay := liveOverlay(t)

	// The distfile has to be on disk: `ebuild … clean configure` unpacks it, and
	// nothing here is allowed to fetch. The skip names the file so the operator
	// can go and get it.
	_ = liveArchive(t, "gst-plugins-good-"+version+".tar.xz")

	staged, err := Stage(StageRequest{
		Overlay:     overlay,
		StagingRoot: filepath.Join(t.TempDir(), "staging"),
		Atom:        "media-plugins/gst-plugins-qt6",
		Version:     version,
		EbuildBytes: liveEbuildSource(t, overlay, version),
	})
	if err != nil {
		t.Fatalf("staging %s: %v", version, err)
	}

	gates, err := RunBuildGates(context.Background(), BuildRequest{
		StagedRoot: staged,
		Atom:       "media-plugins/gst-plugins-qt6",
		Version:    version,
		Depth:      DepthConfigure,
		LogDir:     t.TempDir(),
	}, BuildDeps{})
	if err != nil {
		t.Fatalf("RunBuildGates(%s): %v — a failing build is a reported outcome, not an aborted run", version, err)
	}
	return gates
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
