package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestReviveAppliesAfterReenable guards the revive path against the stale
// enabled = false the shared Applier loaded before the Reviver re-enabled the
// entry: "revived" must mean the new ebuild was written.
func TestReviveAppliesAfterReenable(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version 1.3.0\n"))
	}))
	t.Cleanup(server.Close)

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL)
	srcDir := filepath.Join(t.TempDir(), "src")
	writeReviveSrcEbuild(t, srcDir, "foo", "1.2.3")
	fake := &fakeReviveProvider{versions: map[string][]string{"dev-test/foo": {"1.2.3"}}, dir: srcDir}

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	// Loaded while the entry is still disabled, as runRevive does.
	applier, err := autoupdate.NewApplier(overlay, configDir,
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlay)),
		autoupdate.WithApplierPendingList(pending),
		autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	// Wired as runRevive wires it: a fresh Checker per target, sharing the
	// applier's pending list.
	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(overlay,
			append(testAutoupdateRun(auOpts).reviveCheckerOptions(configDir, 0, 0, config.LLMConfig{}),
				autoupdate.WithPendingList(pending))...)
	}
	reviver, err := autoupdate.NewReviver(overlay, applier, fake, newChecker)
	if err != nil {
		t.Fatalf("NewReviver: %v", err)
	}

	out := reviver.Revive(t.Context(), "dev-test/foo")
	if out.Status != autoupdate.ReviveRevived {
		t.Fatalf("status = %q (%s), want revived", out.Status, out.Detail)
	}
	if _, err := os.Stat(filepath.Join(overlay, "dev-test", "foo", "foo-1.3.0.ebuild")); err != nil {
		t.Errorf("revive reported success but wrote no foo-1.3.0.ebuild: %v", err)
	}
}
