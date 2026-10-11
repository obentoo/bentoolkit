package autoupdate

// Authored for story 060, sub-task 1.1 — R1.1, R1.2, R1.3, R1.4, R1.5, R1.6, R1.7.
//
// Written from the contract (design C1):
//
//	func (a *Applier) ApplyAll(ctx context.Context, updates []PendingUpdate,
//	    compile bool, concurrency int) ([]*ApplyResult, int)
//
// The batch policy lives in this package now, so every assertion here drives
// the real Applier with no cmd/bentoo code involved (R1.6):
//
//   - compile requested: the pool still runs, but the privileged compile step
//     (prompt and sudo) is taken one package at a time (R1.1);
//   - a bump at a depth that starts a build keeps the pool, and the privileged
//     compile step is NOT acquired (R1.2);
//   - a pool of at most `concurrency`, clamped to [1, len] (R1.3);
//   - one non-nil result per update, in input order, and a failure count that
//     counts only an Apply that returned an error (R1.4);
//   - the story 059 cancellation contract (R1.5, R1.7).
//
// Overlap is observed at the exec seam: the first child spawned lingers until a
// second Apply reaches it (or 300 ms pass), so a pool cannot avoid overlapping
// by luck.
//
// Red on arrival: (*Applier).ApplyAll does not exist.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// s060Overlap counts how many callers are inside the exec seam at once. The
// first caller lingers until a second one arrives, or 300 ms pass, so an
// implementation that runs two Applies at once is observed doing so.
type s060Overlap struct {
	mu       sync.Mutex
	active   int
	peak     int
	lingered sync.Once
}

func (o *s060Overlap) enter() {
	o.mu.Lock()
	o.active++
	if o.active > o.peak {
		o.peak = o.active
	}
	o.mu.Unlock()

	o.lingered.Do(func() {
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			o.mu.Lock()
			n := o.active
			o.mu.Unlock()
			if n >= 2 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	o.mu.Lock()
	o.active--
	o.mu.Unlock()
}

func (o *s060Overlap) maxSeen() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.peak
}

// s060Gauge counts callers inside a section at once and keeps the peak.
type s060Gauge struct {
	mu           sync.Mutex
	active, peak int
}

// enter holds the section for d, so callers that overlap are observed doing so.
func (g *s060Gauge) enter(d time.Duration) {
	g.mu.Lock()
	g.active++
	g.peak = max(g.peak, g.active)
	g.mu.Unlock()
	time.Sleep(d)
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

func (g *s060Gauge) maxSeen() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// s060Batch is one ApplyAll fixture: an overlay holding the packages, a
// pending list, and an applier whose exec seam, reporter and confirm prompt are
// all observed.
type s060Batch struct {
	applier  *Applier
	pending  *PendingList
	updates  []PendingUpdate
	reporter *recordingReporter
	overlay  *s060Overlap
	// prompting counts compile prompts in flight at once.
	prompting s060Gauge

	mu      sync.Mutex
	prompts []string
	spawned []string
}

func (b *s060Batch) confirmPrompts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.prompts...)
}

func (b *s060Batch) spawnNames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.spawned...)
}

// s060BatchSetup describes the packages and the applier options.
type s060BatchSetup struct {
	// bumps lists from→to per package; the package names are cat/pkg0..N-1.
	bumps [][2]string
	// staged adds a staging root and the gate policy, so depth decides serial.
	staged bool
	// onSpawn runs inside the exec seam before the child is returned.
	onSpawn func(ctx context.Context)
	// onSpawnNamed runs inside the exec seam with the command's name.
	onSpawnNamed func(name string)
	// failChild makes every spawned child exit non-zero.
	failChild bool
	opts      []ApplierOption
}

func newS060Batch(t *testing.T, setup s060BatchSetup) *s060Batch {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}

	b := &s060Batch{pending: pending, reporter: &recordingReporter{}, overlay: &s060Overlap{}}
	for i, bump := range setup.bumps {
		pkg := fmt.Sprintf("cat/pkg%d", i)
		createTestEbuildFile(t, overlayDir, pkg, bump[0])
		u := PendingUpdate{Package: pkg, CurrentVersion: bump[0], NewVersion: bump[1], Status: StatusPending}
		if err := pending.Add(u); err != nil {
			t.Fatalf("pending.Add %s: %v", pkg, err)
		}
		b.updates = append(b.updates, u)
	}

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		b.mu.Lock()
		b.spawned = append(b.spawned, name)
		b.mu.Unlock()
		b.overlay.enter()
		if setup.onSpawn != nil {
			setup.onSpawn(ctx)
		}
		if setup.onSpawnNamed != nil {
			setup.onSpawnNamed(name)
		}
		if name == "emerge" {
			// The dependency pre-check: answer that only the package itself
			// would be merged, whichever package asked.
			var lines strings.Builder
			for _, a := range arg {
				if strings.HasPrefix(a, "=cat/") {
					lines.WriteString("[ebuild  N    ] " + strings.TrimPrefix(a, "=") + "\n")
				}
			}
			return exec.CommandContext(ctx, "printf", "%s", lines.String())
		}
		if setup.failChild {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}

	opts := []ApplierOption{
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierReporter(b.reporter),
		// Declining is what a privileged compile request looks like from here:
		// the prompt is asked (so the request is observable) and nothing is run
		// with elevated privileges.
		WithConfirmFunc(func(prompt string) bool {
			b.mu.Lock()
			b.prompts = append(b.prompts, prompt)
			b.mu.Unlock()
			// Held briefly so two prompts asked at once are observed overlapping.
			b.prompting.enter(20 * time.Millisecond)
			return false
		}),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierDistdir(t.TempDir(), ""),
	}
	if setup.staged {
		opts = append(opts,
			WithApplierStagingRoot(filepath.Join(tmp, "staging")),
			WithApplierValidatePolicy(gatesPolicy()),
		)
	}
	opts = append(opts, setup.opts...)

	applier, err := NewApplier(overlayDir, configDir, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	b.applier = applier
	return b
}

func s060Bumps(n int, from, to string) [][2]string {
	out := make([][2]string, n)
	for i := range out {
		out[i] = [2]string{from, to}
	}
	return out
}

// runApplyAll calls ApplyAll on its own goroutine so a pool that never
// finishes fails the test instead of hanging it.
func (b *s060Batch) runApplyAll(t *testing.T, ctx context.Context, compile bool, concurrency int) ([]*ApplyResult, int) {
	t.Helper()
	var (
		results  []*ApplyResult
		failures int
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		results, failures = b.applier.ApplyAll(ctx, b.updates, compile, concurrency)
	}()
	waitReturned(t, "ApplyAll", done)
	return results, failures
}

// assertOneResultPerUpdate is R1.4's shape: one non-nil result per update, at
// the update's own index.
func assertOneResultPerUpdate(t *testing.T, results []*ApplyResult, updates []PendingUpdate) {
	t.Helper()
	if len(results) != len(updates) {
		t.Fatalf("ApplyAll returned %d results for %d updates; want exactly one per update (R1.4)", len(results), len(updates))
	}
	for i, u := range updates {
		if results[i] == nil {
			t.Errorf("results[%d] is nil; want a result for %s (R1.4)", i, u.Package)
			continue
		}
		if results[i].Package != u.Package {
			t.Errorf("results[%d].Package = %q; want %q — results must be in input order (R1.4)", i, results[i].Package, u.Package)
		}
	}
}

// TestS060ApplyAllCompileRunsThePoolWithOnePromptAtATime is R1.1. compile
// no longer narrows the batch: the applies overlap, and only the privileged
// compile step — its prompt and its sudo invocation — is taken one at a time.
func TestS060ApplyAllCompileRunsThePoolWithOnePromptAtATime(t *testing.T) {
	b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(4, "1.0.0", "2.0.0")})

	results, failures := b.runApplyAll(t, t.Context(), true, 4)

	assertOneResultPerUpdate(t, results, b.updates)
	if peak := b.overlay.maxSeen(); peak < 2 {
		t.Errorf("peak concurrency = %d; --compile must not serialise the whole batch", peak)
	}
	if peak := b.prompting.maxSeen(); peak != 1 {
		t.Errorf("%d compile prompts were asked at once; the privileged step takes one package at a time", peak)
	}

	// compile reached every Apply: each one asked to run the compile test.
	prompts := b.confirmPrompts()
	if len(prompts) != len(b.updates) {
		t.Fatalf("the compile prompt was asked %d times, want %d — compile must reach every apply (R1.1)", len(prompts), len(b.updates))
	}
	for _, u := range b.updates {
		if !slices.ContainsFunc(prompts, func(p string) bool { return strings.Contains(p, u.Package+"-") }) {
			t.Errorf("no compile prompt names %s: %q", u.Package, prompts)
		}
	}
	// Every compile was declined, so every apply returned an error.
	if failures != len(b.updates) {
		t.Errorf("failures = %d, want %d (each declined compile is an Apply error)", failures, len(b.updates))
	}
}

// TestS060ApplyAllKeepsThePoolWhenADepthStartsABuild is R1.2. 1.28.6 → 1.29.2
// is a series crossing, which the gate policy sends to the configure gate: a
// depth that starts a build. The batch still runs on the pool, and because
// compile was NOT requested, the privileged compile step is never acquired.
func TestS060ApplyAllKeepsThePoolWhenADepthStartsABuild(t *testing.T) {
	b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(3, "1.28.6", "1.29.2"), staged: true})

	results, _ := b.runApplyAll(t, t.Context(), false, 3)

	assertOneResultPerUpdate(t, results, b.updates)
	if peak := b.overlay.maxSeen(); peak < 2 {
		t.Errorf("peak concurrency = %d; a build depth must not serialise the whole batch", peak)
	}
	if !slices.Contains(b.spawnNames(), "ebuild") {
		t.Fatalf("fixture defect: no build was spawned for a series crossing: %v", b.spawnNames())
	}
	if prompts := b.confirmPrompts(); len(prompts) != 0 {
		t.Errorf("a build-depth batch asked for the privileged compile %d time(s): %q — compile was never requested (R1.2)", len(prompts), prompts)
	}
	for _, name := range b.spawnNames() {
		if name == "sudo" || name == "doas" {
			t.Errorf("a build-depth batch spawned %q — compile was never requested (R1.2)", name)
		}
	}
}

// TestS060ApplyAllKeepsThePoolBelowABuildDepth is the converse of R1.2: the
// same staged applier and policy, but patch bumps only (depth options, no
// build) (R1.3).
func TestS060ApplyAllKeepsThePoolBelowABuildDepth(t *testing.T) {
	b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(3, "1.28.5", "1.28.6"), staged: true})

	results, _ := b.runApplyAll(t, t.Context(), false, 3)

	assertOneResultPerUpdate(t, results, b.updates)
	if peak := b.overlay.maxSeen(); peak < 2 {
		t.Errorf("peak concurrency = %d; patch bumps start no build, so the pool must run (R1.3)", peak)
	}
	if slices.Contains(b.spawnNames(), "ebuild") {
		t.Errorf("fixture defect: a patch bump spawned a build: %v", b.spawnNames())
	}
	if prompts := b.confirmPrompts(); len(prompts) != 0 {
		t.Errorf("the pool asked for the privileged compile: %q", prompts)
	}
}

// TestApplyAllBoundsBuildsBySlots: with every bump at a build depth, the
// builds in flight never exceed the build limit, while the batch as a whole
// still overlaps.
func TestApplyAllBoundsBuildsBySlots(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			var builds s060Gauge
			b := newS060Batch(t, s060BatchSetup{
				bumps:  s060Bumps(4, "1.28.6", "1.29.2"),
				staged: true,
				opts:   []ApplierOption{WithApplierBuildConcurrency(limit)},
				onSpawnNamed: func(name string) {
					if name == "ebuild" {
						builds.enter(30 * time.Millisecond)
					}
				},
			})

			results, _ := b.runApplyAll(t, t.Context(), false, 4)

			assertOneResultPerUpdate(t, results, b.updates)
			if peak := builds.maxSeen(); peak > limit {
				t.Errorf("%d builds ran at once, want at most %d", peak, limit)
			}
			if peak := builds.maxSeen(); peak < limit {
				t.Errorf("%d builds ran at once, want %d — the build slots went unused", peak, limit)
			}
			if peak := b.overlay.maxSeen(); peak < 2 {
				t.Errorf("peak concurrency = %d; the batch must overlap outside the builds", peak)
			}
		})
	}
}

// s060Reviewer is a BumpReviewer that only counts how many reviews run at once.
type s060Reviewer struct{ gauge s060Gauge }

func (r *s060Reviewer) ReviewBump(context.Context, fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	r.gauge.enter(30 * time.Millisecond)
	return fixer.BumpReviewReport{}, nil
}

// TestApplyAllBoundsLLMAgentsBySlots: every staged bump with both archives on
// disk asks the reviewer, and the reviews in flight never exceed the LLM limit
// even on a wider pool. The fake pkgdev writes the staged Manifest and the
// candidate archive the way the real one would; the previous archive and its
// published Manifest sit in --distdir. Both are Meson archives, because the
// reviewer is only asked about a project whose options it can read.
func TestApplyAllBoundsLLMAgentsBySlots(t *testing.T) {
	const limit = 2
	reviewer := &s060Reviewer{}
	// Real Meson archives give the options gate a verdict, which starts the
	// host's pkgcheck QA scan; a PATH holding only the fake pkgdev's tools and the
	// archive readers makes that scan skip here exactly as it does on a runner
	// without Portage.
	bin := t.TempDir()
	for _, tool := range []string{"sh", "cp", "true", "basename", "tar", "gzip"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("fixture needs %s: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	distdir := t.TempDir()
	candidate := filepath.Join(t.TempDir(), "candidate.tar.gz")
	s089aMesonArchive(t, candidate, "1.28.6")
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		fetched := distdir
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				fetched = arg[i+1]
			}
		}
		script := `n=$(basename "$PWD") && printf 'DIST %s-1.28.6.tar.gz 1 BLAKE2B a SHA512 b\n' "$n" > Manifest && cp "$2" "$1/$n-1.28.6.tar.gz"`
		return exec.CommandContext(ctx, "sh", "-c", script, "sh", fetched, candidate)
	}
	b := newS060Batch(t, s060BatchSetup{
		bumps:  s060Bumps(5, "1.28.5", "1.28.6"),
		staged: true,
		opts: []ApplierOption{
			WithApplierBumpReviewer(reviewer),
			WithApplierLLMConcurrency(limit),
			WithExecCommand(seam),
			WithApplierDistdir(distdir, ""),
		},
	})
	for _, u := range b.updates {
		name := strings.TrimPrefix(u.Package, "cat/")
		archive := name + "-1.28.5.tar.gz"
		manifest := "DIST " + archive + " 1 BLAKE2B a SHA512 b\n"
		if err := os.WriteFile(filepath.Join(b.applier.overlayPath, u.Package, "Manifest"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		s089aMesonArchive(t, filepath.Join(distdir, archive), "1.28.5")
	}

	results, _ := b.runApplyAll(t, t.Context(), false, 5)

	assertOneResultPerUpdate(t, results, b.updates)
	if peak := reviewer.gauge.maxSeen(); peak > limit {
		t.Errorf("%d LLM reviews ran at once, want at most %d", peak, limit)
	}
	if peak := reviewer.gauge.maxSeen(); peak < limit {
		t.Errorf("%d LLM reviews ran at once, want %d — the LLM slots went unused", peak, limit)
	}
}

// TestS060ApplyAllPoolIsBoundedByConcurrency is R1.3's upper bound. Workers
// are held in the seam; once `concurrency` of them are in, a grace period
// gives a third the chance to arrive, which a bounded pool never lets happen.
func TestS060ApplyAllPoolIsBoundedByConcurrency(t *testing.T) {
	const (
		n           = 6
		concurrency = 2
	)
	barrier := newOverlapBarrier(t)
	var (
		mu           sync.Mutex
		active, peak int
	)
	b := newS060Batch(t, s060BatchSetup{
		bumps: s060Bumps(n, "1.0.0", "2.0.0"),
		onSpawn: func(context.Context) {
			mu.Lock()
			active++
			if active > peak {
				peak = active
			}
			mu.Unlock()
			barrier.arrive()
			mu.Lock()
			active--
			mu.Unlock()
		},
	})

	var (
		results  []*ApplyResult
		failures int
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		results, failures = b.applier.ApplyAll(t.Context(), b.updates, false, concurrency)
	}()
	barrier.openWhen(t, fmt.Sprintf("%d applies in flight, and no more after a grace period", concurrency), func() (bool, string) {
		if barrier.arrived.Load() < concurrency {
			return false, fmt.Sprintf("%d arrived", barrier.arrived.Load())
		}
		time.Sleep(150 * time.Millisecond)
		return true, ""
	})
	waitReturned(t, "ApplyAll", done)

	assertOneResultPerUpdate(t, results, b.updates)
	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	for i, r := range results {
		if r != nil && !r.Success {
			t.Errorf("results[%d] (%s) failed: %v", i, r.Package, r.Error)
		}
	}
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak > concurrency {
		t.Errorf("peak concurrency = %d, want at most %d (R1.3)", gotPeak, concurrency)
	}
	if gotPeak < concurrency {
		t.Errorf("peak concurrency = %d, want %d — the pool did not use the workers it was given", gotPeak, concurrency)
	}
}

// TestS060ApplyAllClampsConcurrency is R1.3's clamp. A concurrency below 1
// still applies everything, one at a time; one above the batch size applies
// everything without inventing work.
func TestS060ApplyAllClampsConcurrency(t *testing.T) {
	for _, tt := range []struct {
		concurrency int
		wantPeakMax int
	}{
		{concurrency: 0, wantPeakMax: 1},
		{concurrency: -3, wantPeakMax: 1},
		{concurrency: 99, wantPeakMax: 3},
	} {
		t.Run(fmt.Sprintf("concurrency %d", tt.concurrency), func(t *testing.T) {
			b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(3, "1.0.0", "2.0.0")})

			results, failures := b.runApplyAll(t, t.Context(), false, tt.concurrency)

			assertOneResultPerUpdate(t, results, b.updates)
			if failures != 0 {
				t.Errorf("failures = %d, want 0", failures)
			}
			for i, r := range results {
				if r != nil && !r.Success {
					t.Errorf("results[%d] (%s) failed: %v", i, r.Package, r.Error)
				}
			}
			if peak := b.overlay.maxSeen(); peak > tt.wantPeakMax {
				t.Errorf("peak concurrency = %d, want at most %d (clamped to [1, len(updates)])", peak, tt.wantPeakMax)
			}
		})
	}
}

// TestS060ApplyAllCountsOnlyApplyErrorsAsFailures is R1.4. Four updates: two
// that apply, one not in the pending list (Apply returns an error), and one
// held (no error, but not a success either). The failure count is the number
// of Apply errors — 1 — not the number of results that are not a success.
func TestS060ApplyAllCountsOnlyApplyErrorsAsFailures(t *testing.T) {
	for _, concurrency := range []int{1, 3} {
		t.Run(fmt.Sprintf("concurrency %d", concurrency), func(t *testing.T) {
			held := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
				"cat/pkg2": {Parser: "json", URL: "https://example.invalid/pkg2", Path: "version", Hold: true},
			}}
			b := newS060Batch(t, s060BatchSetup{
				bumps: s060Bumps(4, "1.0.0", "2.0.0"),
				opts:  []ApplierOption{WithApplierPackagesConfig(held)},
			})
			// cat/pkg1 is in the batch but not in the pending list.
			if err := b.pending.Delete("cat/pkg1"); err != nil {
				t.Fatalf("pending.Delete: %v", err)
			}

			results, failures := b.runApplyAll(t, t.Context(), false, concurrency)

			assertOneResultPerUpdate(t, results, b.updates)
			if failures != 1 {
				t.Errorf("failures = %d, want 1 — only an Apply that returned an error counts; a held package is not a failure (R1.4)", failures)
			}
			if len(results) != 4 {
				return
			}
			if r := results[1]; r != nil && !errors.Is(r.Error, ErrPackageNotInPending) {
				t.Errorf("results[1].Error = %v, want ErrPackageNotInPending", r.Error)
			}
			if r := results[2]; r != nil && !r.Held {
				t.Errorf("results[2] = %+v, want the held result", r)
			}
			for _, i := range []int{0, 3} {
				if r := results[i]; r != nil && !r.Success {
					t.Errorf("results[%d] (%s) failed: %v", i, r.Package, r.Error)
				}
			}
		})
	}
}

// TestS060ApplyAllStopsDispatchingOnCancel is the story 059 contract, moved
// with the pool (R1.5, R1.7). The context ends at the first child the batch
// spawns, which is the earliest point at least one Apply has begun and at most
// `concurrency` can have. Every package at index >= concurrency never began:
// it gets a failed result wrapping context.Canceled, counts as a failure, and
// its overlay directory and pending entry are untouched. The compile row
// covers the serial path the same way.
func TestS060ApplyAllStopsDispatchingOnCancel(t *testing.T) {
	const n = 6
	for _, tt := range []struct {
		name        string
		compile     bool
		concurrency int
		begun       int
	}{
		{name: "pool of 1", concurrency: 1, begun: 1},
		{name: "pool of 3", concurrency: 3, begun: 3},
		{name: "serial for compile", compile: true, concurrency: 3, begun: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var interrupt sync.Once
			b := newS060Batch(t, s060BatchSetup{
				bumps:   s060Bumps(n, "1.0.0", "2.0.0"),
				onSpawn: func(context.Context) { interrupt.Do(cancel) },
			})

			results, failures := b.runApplyAll(t, ctx, tt.compile, tt.concurrency)

			assertOneResultPerUpdate(t, results, b.updates)
			if len(results) != n {
				return
			}
			notApplied := 0
			for _, r := range results {
				if r != nil && !r.Success {
					notApplied++
				}
			}
			if failures != notApplied {
				t.Errorf("failure total = %d; want %d, one per package that was not applied (R1.7)", failures, notApplied)
			}
			for i := tt.begun; i < n; i++ {
				pkg := b.updates[i].Package
				r := results[i]
				if r == nil {
					continue
				}
				if r.Success {
					t.Errorf("%s was applied after the batch's context ended (R1.5)", pkg)
				}
				if !errors.Is(r.Error, context.Canceled) {
					t.Errorf("%s: Error = %v; want it to wrap context.Canceled (R1.7)", pkg, r.Error)
				}
				if _, err := os.Stat(b.applier.EbuildPath(pkg, "2.0.0")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s: a 2.0.0 ebuild exists although the batch was cancelled before it began (stat err = %v) (R1.5)", pkg, err)
				}
				if !b.applier.Pending().Has(pkg) {
					t.Errorf("%s: its pending entry was removed although it never started", pkg)
				}
			}
		})
	}
}

// TestS060ApplyAllOnADoneContextStartsNothing is R1.5 and R1.7 at the edge: a
// context already cancelled before the call. No package starts — the reporter
// sees no TaskStart and no child is spawned — and every update still gets its
// own failed result wrapping the context error.
func TestS060ApplyAllOnADoneContextStartsNothing(t *testing.T) {
	for _, tt := range []struct {
		name        string
		compile     bool
		concurrency int
	}{
		{name: "pool", concurrency: 3},
		{name: "serial", compile: true, concurrency: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(4, "1.0.0", "2.0.0")})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			results, failures := b.runApplyAll(t, ctx, tt.compile, tt.concurrency)

			assertOneResultPerUpdate(t, results, b.updates)
			if failures != len(b.updates) {
				t.Errorf("failures = %d, want %d — every update not begun is a failure (R1.7)", failures, len(b.updates))
			}
			for i, r := range results {
				if r != nil && !errors.Is(r.Error, context.Canceled) {
					t.Errorf("results[%d].Error = %v; want it to wrap context.Canceled (R1.7)", i, r.Error)
				}
			}
			for _, e := range b.reporter.snapshot() {
				if strings.HasPrefix(e, "TaskStart:") {
					t.Errorf("a package started on a done context: %s (R1.5)", e)
				}
			}
			if spawned := b.spawnNames(); len(spawned) != 0 {
				t.Errorf("children were spawned on a done context: %v (R1.5)", spawned)
			}
		})
	}
}
