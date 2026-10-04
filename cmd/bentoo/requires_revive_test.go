package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// Story 079, sub-task 8.1 (R4.8, R4.9): the revive path honours requirements.
// Regression guards: the behaviour landed during Task 4's tech review.

const revivePkg = "dev-test/foo"

// reviveFixture is one orphaned record revived from a fake ::gentoo at 1.2.3
// while upstream publishes 1.3.0, optionally declaring a requirement on
// dev-lang/dart that the same upstream page answers with 3.14.0.
type reviveFixture struct {
	overlay, configDir, gentoo string
	fake                       *fakeReviveProvider
	pending                    *autoupdate.PendingList
	applier                    *autoupdate.Applier
	run                        *autoupdateRun
}

func newReviveFixture(t *testing.T, withRequires bool) *reviveFixture {
	t.Helper()
	run := testAutoupdateRun(testAutoupdateOptions())
	pinReviveConcurrency(t, run.opts)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version 1.3.0 dart 3.14.0\n"))
	}))
	t.Cleanup(server.Close)

	f := &reviveFixture{overlay: setupTestOverlay(t), configDir: t.TempDir(), gentoo: t.TempDir(), run: run}
	t.Setenv("BENTOO_GENTOO_REPO", f.gentoo)

	record := "[\"" + revivePkg + "\"]\nenabled = false\nurl = \"" + server.URL + "\"\nparser = \"regex\"\npattern = 'version ([0-9.]+)'\n"
	if withRequires {
		record += "requires = { \"dev-lang/dart\" = { pattern = 'version {version} dart ([0-9.]+)', pin = \"~\" } }\n"
	}
	if err := os.MkdirAll(filepath.Join(f.overlay, ".autoupdate"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.overlay, ".autoupdate", "packages.toml"), []byte(record), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}

	srcDir := filepath.Join(t.TempDir(), "src")
	writeReviveSrcEbuild(t, srcDir, "foo", "1.2.3")
	if withRequires {
		ebuild := "EAPI=8\nDESCRIPTION=\"t\"\nHOMEPAGE=\"https://example.com\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nRDEPEND=\"~dev-lang/dart-3.13.5\"\n"
		if err := os.WriteFile(filepath.Join(srcDir, "foo-1.2.3.ebuild"), []byte(ebuild), 0o644); err != nil {
			t.Fatalf("write src ebuild: %v", err)
		}
	}
	f.fake = &fakeReviveProvider{versions: map[string][]string{revivePkg: {"1.2.3"}}, dir: srcDir}

	pending, err := autoupdate.NewPendingList(f.configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	f.pending = pending
	opts := append(run.reviveApplierOptions(f.overlay, f.configDir, pending),
		autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}))
	applier, err := autoupdate.NewApplier(f.overlay, f.configDir, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	f.applier = applier
	return f
}

// revive drives one target through the Reviver the way runRevive wires it: a
// fresh Checker per target that shares the applier's pending list.
func (f *reviveFixture) revive(t *testing.T) autoupdate.ReviveOutcome {
	t.Helper()
	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(f.overlay,
			append(f.run.reviveCheckerOptions(f.configDir, 0, 0, config.LLMConfig{}), autoupdate.WithPendingList(f.pending))...)
	}
	reviver, err := autoupdate.NewReviver(f.overlay, f.applier, f.fake, newChecker,
		autoupdate.WithReviveCompile(f.run.opts.compile))
	if err != nil {
		t.Fatalf("NewReviver: %v", err)
	}
	return reviver.Revive(t.Context(), revivePkg)
}

// TestRequiresReviveWaitsOnUnmetRequirement — R4.8. The first case is the
// converse guard: a revive with no requirement still revives.
func TestRequiresReviveWaitsOnUnmetRequirement(t *testing.T) {
	t.Run("no requirement revives", func(t *testing.T) {
		f := newReviveFixture(t, false)
		out := f.revive(t)
		if out.Status != autoupdate.ReviveRevived {
			t.Fatalf("status = %q (%s), want revived", out.Status, out.Detail)
		}
		// "revived" must mean the bump landed: the record was disabled when the
		// Applier loaded it, and a refusal reported as success is the bug.
		if _, err := os.Stat(filepath.Join(f.overlay, "dev-test", "foo", "foo-1.3.0.ebuild")); err != nil {
			t.Errorf("revive reported success but wrote no foo-1.3.0.ebuild: %v", err)
		}
	})

	t.Run("unmet requirement waits", func(t *testing.T) {
		f := newReviveFixture(t, true)
		out := f.revive(t)
		if out.Status != autoupdate.ReviveWaiting || !strings.Contains(out.Detail, "~dev-lang/dart-3.14.0") {
			t.Fatalf("status = %q (%s), want waiting naming ~dev-lang/dart-3.14.0", out.Status, out.Detail)
		}
		if _, ok := f.pending.Get(revivePkg); !ok {
			t.Error("the waiting bump's pending entry was not kept")
		}
		if failed := displayReviveSummary([]autoupdate.ReviveOutcome{out}); failed != 0 {
			t.Errorf("a waiting revive counted as %d failure(s)", failed)
		}
	})
}

// TestRequiresReviveSeesGentoo — R4.9: a requirement met only by ::gentoo lets
// the revive apply, with the pin rewritten.
func TestRequiresReviveSeesGentoo(t *testing.T) {
	f := newReviveFixture(t, true)
	dir := filepath.Join(f.gentoo, "dev-lang", "dart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dart-3.14.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatalf("write gentoo dart: %v", err)
	}
	out := f.revive(t)
	if out.Status != autoupdate.ReviveRevived {
		t.Fatalf("status = %q (%s), want revived", out.Status, out.Detail)
	}
	got, err := os.ReadFile(filepath.Join(f.overlay, "dev-test", "foo", "foo-1.3.0.ebuild"))
	if err != nil {
		t.Fatalf("reading the revived ebuild: %v", err)
	}
	if !strings.Contains(string(got), "~dev-lang/dart-3.14.0") {
		t.Errorf("revived ebuild does not pin ~dev-lang/dart-3.14.0:\n%s", got)
	}
}
