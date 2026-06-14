package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// withFakeGentoo overrides the resolveGentooProviderFn seam so the revive flows
// resolve the given fake instead of the real ::gentoo repository, restoring it
// when the test ends.
func withFakeGentoo(t *testing.T, fake provider.Provider) {
	t.Helper()
	orig := resolveGentooProviderFn
	resolveGentooProviderFn = func(*config.Config, string) (provider.Provider, error) { return fake, nil }
	t.Cleanup(func() { resolveGentooProviderFn = orig })
}

// TestRunRevive_SkipPath drives runRevive's full post-guard path with an on-disk
// fake ::gentoo provider: a single target whose seeded ::gentoo version already
// equals upstream, so reviveOne returns "skipped" and runRevive exits cleanly
// (no failures). This never reaches applier.Apply / `pkgdev manifest`.
func TestRunRevive_SkipPath(t *testing.T) {
	pinReviveConcurrency(t)
	origOnly, origCompile, origClean := autoupdateOnly, autoupdateCompile, autoupdateClean
	autoupdateOnly, autoupdateCompile, autoupdateClean = "", false, false
	t.Cleanup(func() {
		autoupdateOnly, autoupdateCompile, autoupdateClean = origOnly, origCompile, origClean
	})

	const ver = "1.2.3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version " + ver + "\n"))
	}))
	defer server.Close()

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL)

	srcDir := filepath.Join(t.TempDir(), "src")
	writeReviveSrcEbuild(t, srcDir, "foo", ver)

	fake := &fakeReviveProvider{
		versions: map[string][]string{"dev-test/foo": {ver}},
		dir:      srcDir,
	}
	withFakeGentoo(t, fake)

	code := withExitIntercept(func() {
		runRevive(context.Background(), overlay, configDir, "dev-test/foo", 0,
			&config.Config{}, config.LLMConfig{}, "")
	})
	if code != -1 {
		t.Fatalf("runRevive exit code = %d, want no exit (skip path)", code)
	}
}

// TestRunReviveList_WithCandidate drives runReviveList's candidate path: a
// disabled package whose upstream (httptest) is strictly newer than the version
// the fake ::gentoo reports, so FindRevivableOrphans yields one candidate and
// displayReviveCandidates prints the populated table. No exit, no network for the
// gentoo lookup (the fake answers it).
func TestRunReviveList_WithCandidate(t *testing.T) {
	pinReviveConcurrency(t)
	origOnly := autoupdateOnly
	autoupdateOnly = ""
	t.Cleanup(func() { autoupdateOnly = origOnly })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version 2.0.0\n"))
	}))
	defer server.Close()

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL) // disabled entry

	fake := &fakeReviveProvider{
		versions: map[string][]string{"dev-test/foo": {"1.0.0"}}, // gentoo older than upstream 2.0.0
	}
	withFakeGentoo(t, fake)

	code := withExitIntercept(func() {
		runReviveList(context.Background(), overlay, configDir, 0,
			&config.Config{}, config.LLMConfig{}, "")
	})
	if code != -1 {
		t.Fatalf("runReviveList exit code = %d, want no exit", code)
	}
}

// TestRunCheck_Revivable drives the --check --revivable add-on: a --check over a
// config whose only entry is a disabled+absent orphan (upstream newer than the
// fake ::gentoo) runs the normal check and then the revivable-orphan report in
// the same pass. The check has no active packages, so it exits 0; the report
// runs via the injected fake provider (no network for the gentoo lookup).
func TestRunCheck_Revivable(t *testing.T) {
	pinReviveConcurrency(t)
	origOnly, origForce, origRevivable := autoupdateOnly, autoupdateForce, autoupdateRevivable
	autoupdateOnly, autoupdateForce, autoupdateRevivable = "", true, true
	t.Cleanup(func() {
		autoupdateOnly, autoupdateForce, autoupdateRevivable = origOnly, origForce, origRevivable
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version 2.0.0\n"))
	}))
	defer server.Close()

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL) // disabled, no ebuild -> orphan

	fake := &fakeReviveProvider{
		versions: map[string][]string{"dev-test/foo": {"1.0.0"}}, // gentoo older than upstream
	}
	withFakeGentoo(t, fake)

	code := withExitIntercept(func() {
		runCheck(context.Background(), overlay, configDir, nil, 0,
			&config.Config{}, config.LLMConfig{}, "")
	})
	if code != 0 {
		t.Fatalf("runCheck --revivable exit code = %d, want 0 (no active packages, report is read-only)", code)
	}
}
