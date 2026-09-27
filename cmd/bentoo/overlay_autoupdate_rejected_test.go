package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// TestApplyAllPackagesContinuesPastRejectedValue pins S050-R1.5 end to end
// through the command's batch function: the middle package of three carries the
// audit's quote-escape aux value. The batch must count exactly that package as a
// failure, still write the other two new ebuilds with their values, and leave
// the rejected package's directory byte-identical. Run through a one-worker pool
// and a three-worker pool so neither dispatch path can stop at the refusal.
func TestApplyAllPackagesContinuesPastRejectedValue(t *testing.T) {
	const hostile = `x"; touch /tmp/pwned; "`
	values := []string{"esr-bb24", hostile, "esr-bb25"}

	for _, concurrency := range []int{1, 3} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			tmp := t.TempDir()
			overlayDir := filepath.Join(tmp, "overlay")
			configDir := filepath.Join(tmp, "config")
			pending, err := autoupdate.NewPendingList(configDir)
			if err != nil {
				t.Fatalf("NewPendingList: %v", err)
			}
			cfg := &autoupdate.PackagesConfig{Packages: map[string]autoupdate.PackageConfig{}}
			updates := make([]autoupdate.PendingUpdate, 0, len(values))
			for i, v := range values {
				pkg := fmt.Sprintf("cat/pkg%d", i)
				dir := filepath.Join(overlayDir, "cat", fmt.Sprintf("pkg%d", i))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				body := minimalEbuildContent + "MY_BUILD=\"esr-bb23\"\n"
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("pkg%d-1.0.0.ebuild", i)), []byte(body), 0o644); err != nil {
					t.Fatalf("write ebuild: %v", err)
				}
				cfg.Packages[pkg] = autoupdate.PackageConfig{
					Parser:     "regex",
					URL:        "https://example.invalid/" + pkg,
					Pattern:    `v([0-9.]+)`,
					AuxVar:     "MY_BUILD",
					AuxPattern: `v[0-9.]+(esr-bb[0-9]+)`,
				}
				u := autoupdate.PendingUpdate{Package: pkg, CurrentVersion: "1.0.0", NewVersion: "2.0.0", AuxValue: v}
				if err := pending.Add(u); err != nil {
					t.Fatalf("pending.Add %s: %v", pkg, err)
				}
				updates = append(updates, u)
			}
			rejectedDir := filepath.Join(overlayDir, "cat", "pkg1")
			before := readDirBytes(t, rejectedDir)

			applier, err := autoupdate.NewApplier(overlayDir, configDir,
				autoupdate.WithApplierPendingList(pending),
				autoupdate.WithApplierPackagesConfig(cfg),
				autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return exec.CommandContext(ctx, "true")
				}),
				autoupdate.WithApplierDistdir(t.TempDir(), ""),
			)
			if err != nil {
				t.Fatalf("NewApplier: %v", err)
			}

			results, failures := applyAllPackages(applier, updates, false, concurrency)

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
			if after := readDirBytes(t, rejectedDir); !reflect.DeepEqual(before, after) {
				t.Errorf("the rejected package's directory changed\nbefore: %v\n after: %v", before, after)
			}
		})
	}
}

// readDirBytes maps each file name in dir to its content.
func readDirBytes(t *testing.T, dir string) map[string]string {
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
