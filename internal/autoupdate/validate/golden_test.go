package validate

// Authored for story 031, sub-task 8.1 — R1, R2, R3, R3.1, R5.9.
//
// THE GOLDEN TEST. A gate that does not reject a bug we already know about is
// not a gate, so this file reproduces obentoo/bentoo#33 end to end: upstream
// removed the `aalib` and `libcaca` build options at 1.29, the ebuild kept
// passing `-Daalib=` and `-Dlibcaca=`, and every check in the toolkit stayed
// green. The two versions sit in one package directory exactly as they do in
// the real overlay, and one run has to separate them.
//
// The upstream sides are built here as `.tar.gz` rather than committed, so
// there is nothing to drift; the committed `.tar.xz` fixture is exercised
// separately in archive_test.go, which is where the format matters.
//
// overlayWith, linkInto and buildTarGz come from run_test.go and archive_test.go.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// the option list the ebuild passes, in both versions — unchanged across the
// bump, which is the entire defect.
const goldenEbuild = "emesonargs=(\n\t-Dqt6=enabled\n\t-Daalib=disabled\n\t-Dlibcaca=disabled\n)\n"

// goldenOverlay lays out both versions of gst-plugins-qt6 and a distdir holding
// the matching upstream archives: 1.28.6 still declares aalib and libcaca,
// 1.29.2 no longer does.
func goldenOverlay(t *testing.T) (overlay, distdir string) {
	t.Helper()
	overlay = overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild, "gst-plugins-good-1.28.6.tar.gz")
	dir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")

	if err := os.WriteFile(filepath.Join(dir, "gst-plugins-qt6-1.29.2.ebuild"), []byte(goldenEbuild), 0o644); err != nil {
		t.Fatalf("writing the 1.29.2 ebuild: %v", err)
	}
	manifest := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(dir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}

	distdir = t.TempDir()
	old := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.28.6/meson.build":   "project('gst-plugins-good')\n",
		"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
	})
	linkInto(t, old, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))

	// 1.29.2: aalib and libcaca are gone from upstream. Nothing else changed.
	fresh := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.29.2/meson.build":   "project('gst-plugins-good')\n",
		"gst-plugins-good-1.29.2/meson.options": "option('qt6')\n",
	})
	linkInto(t, fresh, filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.gz"))

	return overlay, distdir
}

func resultFor(t *testing.T, r Report, version string) EbuildResult {
	t.Helper()
	for _, res := range r.Results {
		if res.Version == version {
			return res
		}
	}
	t.Fatalf("no result for version %s in %+v", version, r.Results)
	return EbuildResult{}
}

// TestGolden_TheBumpThatBrokeIsRejected is the acceptance criterion of the
// whole story.
func TestGolden_TheBumpThatBrokeIsRejected(t *testing.T) {
	overlay, distdir := goldenOverlay(t)

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	bad := resultFor(t, got, "1.29.2")
	options := gateOf(t, bad, GateOptions)
	if options.Outcome != OutcomeFailed {
		t.Fatalf("1.29.2 outcome: got %q, want FAILED — this is issue #33 and the gate exists to catch it", options.Outcome)
	}

	var details []string
	for _, f := range options.Findings {
		if f.Gate == GateOptions && f.Severity == SeverityError {
			details = append(details, f.Detail)
		}
	}
	joined := strings.Join(details, " | ")
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the failure does not name %q; %q", want, joined)
		}
	}

	if got.ExitCode() != 1 {
		t.Errorf("ExitCode: got %d, want 1", got.ExitCode())
	}
}

// TestGolden_ThePreviousVersionStillPasses is the other half, and the one that
// stops the gate being a rubber stamp in reverse: a gate that fails everything
// is as useless as one that fails nothing.
func TestGolden_ThePreviousVersionStillPasses(t *testing.T) {
	overlay, distdir := goldenOverlay(t)

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	good := resultFor(t, got, "1.28.6")
	options := gateOf(t, good, GateOptions)
	if options.Outcome != OutcomePass {
		t.Errorf("1.28.6 outcome: got %q (reason %q), want PASS — upstream still declares every option it passes",
			options.Outcome, options.Reason)
	}
	for _, f := range options.Findings {
		if f.Severity == SeverityError {
			t.Errorf("1.28.6 produced an error finding: %q", f.Detail)
		}
	}
}

// TestGolden_RunsWithAnUnwritableDistdir pins design decision D2 end to end.
// Resolve would fail here; the read-only locator must not.
func TestGolden_RunsWithAnUnwritableDistdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: every directory is writable, so this would pass for the wrong reason")
	}
	overlay, distdir := goldenOverlay(t)
	if err := os.Chmod(distdir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(distdir, 0o700) })

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir})
	if err != nil {
		t.Fatalf("Run against a read-only distdir: %v", err)
	}

	if gateOf(t, resultFor(t, got, "1.29.2"), GateOptions).Outcome != OutcomeFailed {
		t.Error("the gate lost its verdict when the distdir was not writable; reading a distfile needs no write permission")
	}
}

// TestGolden_LeavesTheOverlayByteIdentical is R5.9. The command is read-only,
// and "read-only" is a claim worth hashing rather than asserting.
func TestGolden_LeavesTheOverlayByteIdentical(t *testing.T) {
	overlay, distdir := goldenOverlay(t)
	before := hashTree(t, overlay)

	if _, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if after := hashTree(t, overlay); after != before {
		t.Errorf("the overlay changed during a read-only run: %s -> %s", before, after)
	}
}

// hashTree returns one digest over every path and file body under root, so any
// creation, deletion or edit moves it.
func hashTree(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %q: %v", root, err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %q: %v", p, err)
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
