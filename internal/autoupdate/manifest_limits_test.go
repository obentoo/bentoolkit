//go:build unix

package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The manifest step's limits: a bounded number of runs download at once, and a
// run is stopped when it stops progressing or reaches its ceiling — never by a
// fixed wall clock a saturated link makes every large download miss. The fake
// pkgdev is a shell script; nothing here touches the network.

// scriptSeam returns an exec seam whose "pkgdev" runs script under sh, with the
// --distdir argument as $1.
func scriptSeam(script string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
		distdir := ""
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				distdir = arg[i+1]
			}
		}
		return exec.CommandContext(ctx, "sh", "-c", script, "pkgdev", distdir)
	}
}

// stagedPkgDir writes a one-ebuild staged package directory.
func stagedPkgDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staged", "test-cat", "test-pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test-pkg-2.0.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runStagedWith runs one staged manifest against a supplied distdir under the
// given limits and returns its error and duration.
func runStagedWith(t *testing.T, script string, l ManifestLimits, slots chan struct{}) (time.Duration, error) {
	t.Helper()
	s := newSweeper(filepath.Join(t.TempDir(), "overlay"),
		withSweeperExec(scriptSeam(script)),
		withSweeperManifestLimits(slots, l))
	start := time.Now()
	_, err := s.runStagedManifestIn(context.Background(), t.TempDir(), stagedPkgDir(t), "test-cat/test-pkg", "2.0.0")
	return time.Since(start), err
}

func TestManifestSlotsBoundConcurrentRuns(t *testing.T) {
	markers, counts := t.TempDir(), t.TempDir()
	// Each run drops a marker, records how many markers are present, holds for
	// a while and leaves: the largest record is the most runs ever inside.
	script := `m="` + markers + `/$$"; : > "$m"; ls "` + markers + `" | wc -l > "` + counts + `/$$"; sleep 0.3; rm -f "$m"`
	slots := make(chan struct{}, 2)

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runStagedWith(t, script, ManifestLimits{}, slots)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("manifest run failed: %v", err)
		}
	}

	files, err := os.ReadDir(counts)
	if err != nil || len(files) != 6 {
		t.Fatalf("want 6 runs recorded, got %d (%v)", len(files), err)
	}
	peak := 0
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(counts, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("count %q: %v", raw, err)
		}
		peak = max(peak, n)
	}
	if peak > 2 {
		t.Errorf("%d manifest runs were inside at once, want at most 2", peak)
	}
	if peak < 2 {
		t.Errorf("never more than %d run inside at once: the slots serialised what they should only bound", peak)
	}
}

func TestManifestOutputProgressKeepsTheRunAlive(t *testing.T) {
	// Silent for less than the stall timeout at a time, but running for four
	// times it in total: a slow download that keeps printing.
	script := `i=0; while [ $i -lt 16 ]; do echo "downloading $i"; sleep 0.05; i=$((i+1)); done`
	took, err := runStagedWith(t, script, ManifestLimits{StallTimeout: 200 * time.Millisecond, Timeout: time.Minute}, nil)
	if err != nil {
		t.Fatalf("a run that kept printing was stopped after %v: %v", took, err)
	}
}

func TestManifestDistdirGrowthKeepsTheRunAlive(t *testing.T) {
	// No output at all; only the distdir grows, the way a quiet fetcher writes.
	script := `i=0; while [ $i -lt 16 ]; do printf 'xxxx' >> "$1/archive.tar.xz"; sleep 0.05; i=$((i+1)); done`
	took, err := runStagedWith(t, script, ManifestLimits{StallTimeout: 200 * time.Millisecond, Timeout: time.Minute}, nil)
	if err != nil {
		t.Fatalf("a run whose distdir kept growing was stopped after %v: %v", took, err)
	}
}

func TestManifestStallStopsTheRun(t *testing.T) {
	took, err := runStagedWith(t, `exec sleep 30`, ManifestLimits{StallTimeout: 200 * time.Millisecond, Timeout: time.Hour}, nil)
	if err == nil {
		t.Fatal("a run with no progress finished without an error")
	}
	if took > 6*time.Second {
		t.Errorf("a stalled run returned after %v, want within the stall timeout plus the grace period", took)
	}
	if !errors.Is(err, ErrManifestTimedOut) || !errors.Is(err, ErrManifestFailed) {
		t.Errorf("error is not a manifest timeout: %v", err)
	}
	for _, want := range []string{"no download progress for 200ms", "autoupdate.manifest.stall_timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if !errors.Is(environmentVerdict(err), ErrManifestEnvironment) {
		t.Errorf("a stalled run is classified as the ebuild's fault, want the environment: %v", err)
	}
}

func TestManifestCeilingStillApplies(t *testing.T) {
	// Progressing steadily, forever.
	script := `while :; do echo tick; sleep 0.05; done`
	took, err := runStagedWith(t, script, ManifestLimits{StallTimeout: time.Minute, Timeout: 400 * time.Millisecond}, nil)
	if err == nil {
		t.Fatal("a run that never ends finished without an error")
	}
	if took > 6*time.Second {
		t.Errorf("the ceiling stopped the run after %v, want within the ceiling plus the grace period", took)
	}
	if !errors.Is(err, ErrManifestTimedOut) {
		t.Errorf("error is not a manifest timeout: %v", err)
	}
	for _, want := range []string{"400ms ceiling", "autoupdate.manifest.timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

func TestManifestPublishedPathNamesTheStall(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	s := manifestSweeper(t, overlayDir, t.TempDir(), "",
		withSweeperExec(scriptSeam(`exec sleep 30`)),
		withSweeperManifestLimits(make(chan struct{}, 1), ManifestLimits{StallTimeout: 200 * time.Millisecond, Timeout: time.Hour}))
	err := s.runManifest(context.Background(), pkg, "2.0.0")
	if !errors.Is(err, ErrManifestTimedOut) || !strings.Contains(err.Error(), "autoupdate.manifest.stall_timeout") {
		t.Fatalf("published manifest stall = %v, want a named stall timeout", err)
	}
	if !errors.Is(environmentVerdict(err), ErrManifestEnvironment) {
		t.Errorf("a stalled published run is classified as the ebuild's fault: %v", err)
	}
}

func TestManifestSlotWaitGivesUpWithTheContext(t *testing.T) {
	s := newSweeper(t.TempDir(), withSweeperManifestLimits(make(chan struct{}, 1), ManifestLimits{}))
	release, err := s.takeManifestSlot(context.Background(), "a/b")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.takeManifestSlot(ctx, "c/d"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting on a full slot with a done context = %v, want the context's error", err)
	}
}

func TestManifestLimitsDefaults(t *testing.T) {
	got := ManifestLimits{Concurrency: -1}.withDefaults()
	want := ManifestLimits{Concurrency: DefaultManifestConcurrency, StallTimeout: DefaultManifestStallTimeout, Timeout: DefaultManifestTimeout}
	if got != want {
		t.Errorf("withDefaults = %+v, want %+v", got, want)
	}
}

func TestVersionFreeDistNames(t *testing.T) {
	names := []string{"zed-1.29.3.tar.gz", "serde-1.0.200.crate", "zed-v1.29.3-assets.tar.xz", "fonts-data.tar.xz"}
	got := versionFreeDistNames(names, []string{"1.29.3"})
	want := []string{"serde-1.0.200.crate", "fonts-data.tar.xz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("versionFreeDistNames = %v, want %v", got, want)
	}
	if got := versionFreeDistNames(names, nil); got != nil {
		t.Errorf("with no old version = %v, want nothing", got)
	}
}

func TestStagedExpectedDistfilesIncludesVersionFreeNames(t *testing.T) {
	overlay := t.TempDir()
	pkgDir := filepath.Join(overlay, "app-editors", "zed")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "zed-1.29.3.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "DIST serde-1.0.200.crate 10 BLAKE2B aa SHA512 bb\n" +
		"DIST zed-1.29.3.tar.gz 20 BLAKE2B cc SHA512 dd\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSweeper(overlay)
	got := s.stagedExpectedDistfiles("app-editors/zed", "1.30.2")
	want := []string{"serde-1.0.200.crate", "zed-1.30.2.tar.gz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stagedExpectedDistfiles = %v, want %v", got, want)
	}
}
