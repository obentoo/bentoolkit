package autoupdate

// Pool, order and failure-tally assertions moved here from cmd/bentoo with
// (*Applier).ApplyAll (story 060, sub-task 1.1). They were
// TestApplyAllPackagesConcurrentSuccess, TestApplyAllPackagesCountsFailures
// (overlay_autoupdate_applyall_test.go) and
// TestApplyAllPackagesContinuesPastRejectedValue
// (overlay_autoupdate_rejected_test.go). The cancellation test of
// overlay_autoupdate_applyall_ctx_test.go is carried, assertion for assertion,
// by TestS060ApplyAllStopsDispatchingOnCancel in applier_all_s060_test.go.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestApplyAllConcurrentSuccess is the regression guard for the serial-apply
// bug: without compile, ApplyAll must dispatch the (network-bound) manifest
// step of each package across a worker pool, and it must still return results
// in input order despite out-of-order completion.
//
// The exec seam tracks how many goroutines are inside it at once; a serial
// implementation would never exceed 1. Each one is held at a barrier until
// concurrency of them are inside at once, so the overlap the peak assertion
// judges is certain rather than likely: a serial implementation never fills
// the barrier and fails on its deadline.
func TestApplyAllConcurrentSuccess(t *testing.T) {
	const (
		n           = 6
		concurrency = 4
	)

	var (
		mu           sync.Mutex
		active, peak int
	)
	barrier := newOverlapBarrier(t)
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
	updates := b.updates

	var (
		results  []*ApplyResult
		failures int
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		results, failures = b.applier.ApplyAll(t.Context(), updates, false, concurrency)
	}()
	barrier.openOnceArrived(t, "manifest steps", concurrency)
	waitReturned(t, "ApplyAll", done)

	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for i, u := range updates {
		if results[i] == nil {
			t.Errorf("results[%d] is nil (package %s not applied)", i, u.Package)
			continue
		}
		if results[i].Package != u.Package {
			t.Errorf("results[%d].Package = %q, want %q (input order not preserved)",
				i, results[i].Package, u.Package)
		}
		if !results[i].Success {
			t.Errorf("results[%d] (%s) Success = false, err = %v", i, u.Package, results[i].Error)
		}
	}

	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak < 2 {
		t.Errorf("peak concurrency = %d, want >= 2 (applies ran serially — the bug)", gotPeak)
	}
}

// TestApplyAllCountsFailures verifies the atomic failure tally on the
// concurrent path: when every manifest step fails, ApplyAll must report
// exactly n failures and mark every result unsuccessful (no lost/double counts
// under -race).
func TestApplyAllCountsFailures(t *testing.T) {
	const (
		n           = 5
		concurrency = 3
	)

	b := newS060Batch(t, s060BatchSetup{bumps: s060Bumps(n, "1.0.0", "2.0.0"), failChild: true})
	updates := b.updates

	results, failures := b.runApplyAll(t, t.Context(), false, concurrency)

	if failures != n {
		t.Errorf("failures = %d, want %d", failures, n)
	}
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for i, u := range updates {
		if results[i] == nil {
			t.Errorf("results[%d] is nil (package %s)", i, u.Package)
			continue
		}
		if results[i].Success {
			t.Errorf("results[%d] (%s) Success = true, want false", i, u.Package)
		}
	}
}

// applyAllRejectedEbuild is a minimal, parseable ebuild carrying the aux
// variable the rejected-value test rewrites.
const applyAllRejectedEbuild = "EAPI=8\nDESCRIPTION=\"test\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\n" +
	"MY_BUILD=\"esr-bb23\"\n"

// TestApplyAllContinuesPastRejectedValue pins S050-R1.5 through the batch
// function: the middle package of three carries the audit's quote-escape aux
// value. The batch must count exactly that package as a failure, still write
// the other two new ebuilds with their values, and leave the rejected
// package's directory byte-identical. Run through a one-worker pool and a
// three-worker pool so neither dispatch path can stop at the refusal.
func TestApplyAllContinuesPastRejectedValue(t *testing.T) {
	const hostile = `x"; touch /tmp/pwned; "`
	values := []string{"esr-bb24", hostile, "esr-bb25"}

	for _, concurrency := range []int{1, 3} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			tmp := t.TempDir()
			overlayDir := filepath.Join(tmp, "overlay")
			configDir := filepath.Join(tmp, "config")
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Fatalf("NewPendingList: %v", err)
			}
			cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{}}
			updates := make([]PendingUpdate, 0, len(values))
			for i, v := range values {
				pkg := fmt.Sprintf("cat/pkg%d", i)
				createTestEbuildFileWithContent(t, overlayDir, pkg, "1.0.0", applyAllRejectedEbuild)
				cfg.Packages[pkg] = registry.PackageConfig{
					Parser:     "regex",
					URL:        "https://example.invalid/" + pkg,
					Pattern:    `v([0-9.]+)`,
					AuxVar:     "MY_BUILD",
					AuxPattern: `v[0-9.]+(esr-bb[0-9]+)`,
				}
				u := PendingUpdate{Package: pkg, CurrentVersion: "1.0.0", NewVersion: "2.0.0", AuxValue: v}
				if err := pending.Add(u); err != nil {
					t.Fatalf("pending.Add %s: %v", pkg, err)
				}
				updates = append(updates, u)
			}
			rejectedDir := filepath.Join(overlayDir, "cat", "pkg1")
			before := applyAllReadDir(t, rejectedDir)

			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithApplierPackagesConfig(cfg),
				WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return exec.CommandContext(ctx, "true")
				}),
				WithApplierDistdir(t.TempDir(), ""),
			)
			if err != nil {
				t.Fatalf("NewApplier: %v", err)
			}

			results, failures := applier.ApplyAll(t.Context(), updates, false, concurrency)

			if failures != 1 {
				t.Errorf("failures = %d, want exactly 1 (the rejected package)", failures)
			}
			if len(results) != len(values) {
				t.Fatalf("len(results) = %d, want %d", len(results), len(values))
			}
			for i, v := range values {
				pkg := fmt.Sprintf("cat/pkg%d", i)
				newPath := filepath.Join(overlayDir, "cat", fmt.Sprintf("pkg%d", i), fmt.Sprintf("pkg%d-2.0.0.ebuild", i))
				got, readErr := os.ReadFile(newPath)
				if v == hostile {
					if results[i] == nil || results[i].Success {
						t.Errorf("%s: result = %+v, want a failure", pkg, results[i])
					}
					if readErr == nil {
						t.Errorf("%s: an ebuild was written for a refused value:\n%s", pkg, got)
					}
					continue
				}
				if results[i] == nil || !results[i].Success {
					t.Errorf("%s: result = %+v, want success after the rejected package", pkg, results[i])
				}
				if readErr != nil {
					t.Errorf("%s: new ebuild not written: %v", pkg, readErr)
					continue
				}
				if !strings.Contains(string(got), "MY_BUILD=\""+v+"\"\n") {
					t.Errorf("%s: new ebuild lacks MY_BUILD=%q:\n%s", pkg, v, got)
				}
			}
			if after := applyAllReadDir(t, rejectedDir); !reflect.DeepEqual(before, after) {
				t.Errorf("the rejected package's directory changed\nbefore: %v\n after: %v", before, after)
			}
		})
	}
}

// applyAllReadDir maps each file name in dir to its content.
func applyAllReadDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out
}
