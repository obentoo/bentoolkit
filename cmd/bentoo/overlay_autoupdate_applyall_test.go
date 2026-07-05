package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

const minimalEbuildContent = "EAPI=8\nDESCRIPTION=\"test\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\n"

// writeApplyAllEbuild drops a minimal, parseable ebuild for pkg at version into
// overlayDir so the applier's resolveCurrentVersion/copyEbuild steps have a real
// source file to work from.
func writeApplyAllEbuild(t *testing.T, overlayDir, pkg, version string) {
	t.Helper()
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		t.Fatalf("invalid package name: %s", pkg)
	}
	pkgDir := filepath.Join(overlayDir, parts[0], parts[1])
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pkgDir, err)
	}
	p := filepath.Join(pkgDir, parts[1]+"-"+version+".ebuild")
	if err := os.WriteFile(p, []byte(minimalEbuildContent), 0o644); err != nil {
		t.Fatalf("write ebuild %s: %v", p, err)
	}
}

// setupApplyAllTest builds an Applier over a temp overlay holding n packages
// (cat/pkg0..pkgN-1 at 1.0.0, each pending a bump to 2.0.0) whose manifest step
// is driven by the injected exec factory. It returns the applier plus the
// updates slice in a deterministic order so callers can assert result ordering.
func setupApplyAllTest(t *testing.T, n int, factory func(context.Context, string, ...string) *exec.Cmd) (*autoupdate.Applier, []autoupdate.PendingUpdate) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}

	updates := make([]autoupdate.PendingUpdate, 0, n)
	for i := 0; i < n; i++ {
		pkg := fmt.Sprintf("cat/pkg%d", i)
		writeApplyAllEbuild(t, overlayDir, pkg, "1.0.0")
		u := autoupdate.PendingUpdate{Package: pkg, CurrentVersion: "1.0.0", NewVersion: "2.0.0"}
		if err := pending.Add(u); err != nil {
			t.Fatalf("pending.Add %s: %v", pkg, err)
		}
		updates = append(updates, u)
	}

	applier, err := autoupdate.NewApplier(overlayDir, configDir,
		autoupdate.WithApplierPendingList(pending),
		autoupdate.WithExecCommand(factory),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier, updates
}

// TestApplyAllPackagesConcurrentSuccess is the regression guard for the
// serial-apply bug: without --compile, applyAllPackages must dispatch the
// (network-bound) manifest step of each package across a worker pool, and it
// must still return results in input order despite out-of-order completion.
//
// The exec factory tracks how many goroutines are inside it at once; a serial
// implementation would never exceed 1. A short sleep widens the overlap window
// so the assertion is robust rather than timing-fragile.
func TestApplyAllPackagesConcurrentSuccess(t *testing.T) {
	const (
		n           = 6
		concurrency = 4
	)

	var (
		mu           sync.Mutex
		active, peak int
	)
	factory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond) // overlap window

		mu.Lock()
		active--
		mu.Unlock()
		return exec.CommandContext(ctx, "true")
	}

	applier, updates := setupApplyAllTest(t, n, factory)

	results, failures := applyAllPackages(applier, updates, false, concurrency)

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

// TestApplyAllPackagesCountsFailures verifies the atomic failure tally on the
// concurrent path: when every manifest step fails, applyAllPackages must report
// exactly n failures and mark every result unsuccessful (no lost/double counts
// under -race).
func TestApplyAllPackagesCountsFailures(t *testing.T) {
	const (
		n           = 5
		concurrency = 3
	)

	failFactory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	applier, updates := setupApplyAllTest(t, n, failFactory)

	results, failures := applyAllPackages(applier, updates, false, concurrency)

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
