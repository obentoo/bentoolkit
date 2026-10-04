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
	resolveGentooProviderFn = func(*config.Config) (provider.Provider, error) { return fake, nil }
	t.Cleanup(func() { resolveGentooProviderFn = orig })
}

// TestRunRevive_SkipPath drives runRevive's full post-guard path with an on-disk
// fake ::gentoo provider: a single target whose seeded ::gentoo version already
// equals upstream, so autoupdate.Reviver returns "skipped" and runRevive exits
// cleanly (no failures). This never reaches applier.Apply / `pkgdev manifest`.
//
// Its stdout is pinned byte for byte (U3): the per-target "Reviving <pkg>..."
// line runRevive prints before each Revive, then the summary, as the format
// strings stood before story 060 (1462803).
func TestRunRevive_SkipPath(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	auOpts.only, auOpts.compile, auOpts.clean = "", false, false

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

	var code int
	stdout := captureStdout(t, func() {
		code = exitCodeFor(testAutoupdateRun(auOpts).runRevive(context.Background(), overlay, configDir, "dev-test/foo", 0,
			&config.Config{}, config.LLMConfig{}))
	})
	if code != 0 {
		t.Fatalf("runRevive exit code = %d, want 0 (skip path)", code)
	}
	const want = "Reviving dev-test/foo...\n" +
		"\nRevive Summary\n\n" +
		"  - dev-test/foo: gentoo 1.2.3 already current with upstream 1.2.3\n" +
		"\n" +
		"  Revived: 0\n" +
		"  Skipped: 1\n"
	if stdout != want {
		t.Errorf("runRevive stdout differs (U3)\ngot:\n%q\nwant:\n%q", stdout, want)
	}
}

// TestRunReviveList_WithCandidate drives runReviveList's candidate path: a
// disabled package whose upstream (httptest) is strictly newer than the version
// the fake ::gentoo reports, so FindRevivableOrphans yields one candidate and
// displayReviveCandidates prints the populated table. No exit, no network for the
// gentoo lookup (the fake answers it).
func TestRunReviveList_WithCandidate(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	auOpts.only = ""

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

	code := exitCodeFor(testAutoupdateRun(auOpts).runReviveList(context.Background(), overlay, configDir, 0,
		&config.Config{}, config.LLMConfig{}))
	if code != 0 {
		t.Fatalf("runReviveList exit code = %d, want 0", code)
	}
}

// TestRunCheck_Revivable drives the --check --revivable add-on: a --check over a
// config whose only entry is a disabled+absent orphan (upstream newer than the
// fake ::gentoo) runs the normal check and then the revivable-orphan report in
// the same pass. The check has no active packages, so it exits 0; the report
// runs via the injected fake provider (no network for the gentoo lookup).
func TestRunCheck_Revivable(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	auOpts.only, auOpts.force, auOpts.revivable = "", true, true

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

	code := exitCodeFor(testAutoupdateRun(auOpts).runCheck(context.Background(), overlay, configDir, nil, 0,
		&config.Config{}, config.LLMConfig{}))
	if code != 0 {
		t.Fatalf("runCheck --revivable exit code = %d, want 0 (no active packages, report is read-only)", code)
	}
}
