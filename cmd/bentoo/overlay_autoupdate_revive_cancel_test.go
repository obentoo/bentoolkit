package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// TestReviveOneStopsOnCancel pins R7.4 end to end: the ::gentoo version lookup
// reviveOne makes must carry the revive command's context, so cancelling that
// context ends an in-flight lookup within 1 s. The version provider is the real
// GitHub provider pointed at a host that never answers; the package-dir side is
// the binary-free fake, so nothing past the lookup is reached.
//
// The outcome must be a failure whose detail carries the cancellation cause —
// never "revived" or "skipped", and never a seeded ebuild.
func TestReviveOneStopsOnCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // NewGitHubProvider creates a cache dir under $HOME
	t.Setenv("GITHUB_TOKEN", "")

	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	gentoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(gentoo.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO)

	prov, err := provider.NewGitHubProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	prov.BaseURL = gentoo.URL
	prov.CacheDir = ""

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "src")
	writeReviveSrcEbuild(t, srcDir, "foo", "1.2.3")
	pdp := &fakeReviveProvider{dir: srcDir}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	applier := newReviveApplier(t, overlay, configDir, pending)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan reviveOutcome, 1)
	go func() {
		done <- reviveOne(ctx, "dev-test/foo", overlay, configDir, 0, 0,
			config.LLMConfig{}, prov, pdp, applier, pending)
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the ::gentoo version lookup never reached the provider")
	}
	cancel()

	var out reviveOutcome
	select {
	case out = <-done:
	case <-time.After(time.Second):
		t.Fatal("reviveOne still running 1s after the revive context was cancelled: the ::gentoo lookup does not carry ctx")
	}
	if out.status != "failed" {
		t.Errorf("status = %q (detail: %s), want \"failed\"", out.status, out.detail)
	}
	if !strings.Contains(out.detail, context.Canceled.Error()) {
		t.Errorf("detail = %q, want it to carry the cancellation cause %q", out.detail, context.Canceled.Error())
	}
	seeded, err := filepath.Glob(filepath.Join(overlay, "dev-test", "foo", "*.ebuild"))
	if err != nil {
		t.Fatalf("glob seeded ebuilds: %v", err)
	}
	if len(seeded) != 0 {
		t.Errorf("a cancelled revive seeded %v into the overlay", seeded)
	}
}
