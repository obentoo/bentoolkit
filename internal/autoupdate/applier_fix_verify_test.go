package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
)

// manifestFixFixture lays out dev-games/godot 4.7_rc3 with a pending bump to 4.7.
func manifestFixFixture(t *testing.T) (overlayDir, configDir string, pending *PendingList) {
	t.Helper()
	tmpDir := t.TempDir()
	overlayDir = filepath.Join(tmpDir, "overlay")
	configDir = filepath.Join(tmpDir, "config")
	createTestEbuildFile(t, overlayDir, "dev-games/godot", "4.7_rc3")
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	pending.Add(PendingUpdate{Package: "dev-games/godot", CurrentVersion: "4.7_rc3", NewVersion: "4.7", Status: StatusPending})
	return overlayDir, configDir, pending
}

// A re-check that passes over an ebuild the fixer left byte-identical is a
// transient first failure, not a repair: Success without Fixed.
func TestApply_ManifestFix_NoEditIsNotReportedAsRepaired(t *testing.T) {
	overlayDir, configDir, pending := manifestFixFixture(t)
	fx := &fakeFixer{summary: "the failure was a timeout; nothing to change", noEdit: true}
	rec := &s062Recorder{}
	a, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFlakySeam()),
		WithApplierFixer(fx),
		WithApplierLogger(rec.logger()),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Apply(t.Context(), "dev-games/godot")
	if err != nil || !result.Success {
		t.Fatalf("Apply: err %v, result %+v", err, result)
	}
	if fx.called != 1 {
		t.Fatalf("fixer called %d times, want 1", fx.called)
	}
	if result.Fixed || result.FixSummary != "" {
		t.Errorf("Fixed = %v, FixSummary = %q: an untouched ebuild was reported as repaired", result.Fixed, result.FixSummary)
	}
	for _, r := range rec.records(t) {
		if r["msg"] == "LLM fixer repaired the ebuild" {
			t.Errorf("logged a repair for an untouched ebuild: %v", r)
		}
	}
}

// A copy that contradicts the published Manifest never reaches the fixer or
// the `--force` re-check, on the staged path the incident took: the first
// attempt leaves a truncated published archive in its distdir (carried over),
// or the fixer brings one. Either way the re-check must not find it.
func TestApply_ManifestFix_StagedDropsCopiesContradictingPublishedManifest(t *testing.T) {
	name := s082Dist(0, s082From) // published with size 100
	for _, tc := range []struct {
		label, firstAttempt string
		fixerWrites         bool
	}{
		{"truncated by the first attempt", `printf 'trunc' > "$1/` + name + `"`, false},
		{"truncated by the fixer", `:`, true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s081SandboxRoot(t)
			tmp := t.TempDir()
			overlayDir, configDir := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config")
			s082PublishedPackage(t, overlayDir)
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Fatal(err)
			}
			pending.Add(PendingUpdate{Package: s082Pkg, CurrentVersion: s082From, NewVersion: s082To, Status: StatusPending})

			var atFixer, atRecheck bool
			fx := &fakeFixer{summary: "rewrote SRC_URI", onCall: func(req fixer.ManifestFixRequest) {
				_, err := os.Stat(filepath.Join(req.DistDir, name))
				atFixer = err == nil
				if tc.fixerWrites {
					if err := os.WriteFile(filepath.Join(req.DistDir, name), []byte("trunc"), 0o600); err != nil {
						t.Error(err)
					}
				}
			}}
			first := s082FirstAttempt(tc.firstAttempt)
			calls := 0
			seam := func(ctx context.Context, cmd string, arg ...string) *exec.Cmd {
				if cmd == "pkgdev" {
					calls++
					if calls == 2 {
						for i, v := range arg {
							if v == "--distdir" && i+1 < len(arg) {
								_, err := os.Stat(filepath.Join(arg[i+1], name))
								atRecheck = err == nil
							}
						}
					}
				}
				return first(ctx, cmd, arg...)
			}
			a, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(seam),
				WithApplierFixer(fx),
				WithApplierStagingRoot(filepath.Join(tmp, "staging")),
				WithApplierDistdir(filepath.Join(tmp, "host-distdir"), ""),
				WithApplierDistfilesCache(filepath.Join(tmp, "cache")),
				WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = a.Apply(t.Context(), s082Pkg)
			if fx.called != 1 || calls < 2 {
				t.Fatalf("fixer ran %d time(s), pkgdev %d time(s); the re-check never ran", fx.called, calls)
			}
			if !tc.fixerWrites && atFixer {
				t.Errorf("the truncated %s was carried over to the fixer", name)
			}
			if atRecheck {
				t.Errorf("the truncated %s was still in the distdir when the --force re-check ran", name)
			}
		})
	}
}
