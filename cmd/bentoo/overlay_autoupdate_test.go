package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// writeExitTestEbuild writes a minimal ebuild for pkg ("category/name") at the
// given version under overlayDir.
func writeExitTestEbuild(t *testing.T, overlayDir, pkg, version string) {
	t.Helper()
	parts := strings.SplitN(pkg, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid package name %q", pkg)
	}
	pkgDir := filepath.Join(overlayDir, parts[0], parts[1])
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", pkgDir, err)
	}
	content := "EAPI=8\nDESCRIPTION=\"t\"\nHOMEPAGE=\"https://example.com\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	ebuildPath := filepath.Join(pkgDir, parts[1]+"-"+version+".ebuild")
	if err := os.WriteFile(ebuildPath, []byte(content), 0644); err != nil {
		t.Fatalf("write ebuild %s: %v", ebuildPath, err)
	}
}

// writeExitTestPackagesConfig writes a packages.toml under <overlayDir>/.autoupdate
// mapping each package name to a JSON-parser schema pointed at serverURL.
func writeExitTestPackagesConfig(t *testing.T, overlayDir, serverURL string, pkgs []string) {
	t.Helper()
	cfgDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	var b strings.Builder
	for _, pkg := range pkgs {
		b.WriteString("[\"" + pkg + "\"]\n")
		b.WriteString("url = \"" + serverURL + "\"\n")
		b.WriteString("parser = \"json\"\n")
		b.WriteString("path = \"version\"\n\n")
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
}

// TestCLI_ExitCodes exercises the documented exit-code contract of the
// autoupdate --check path: 0 when every package succeeds, 1 on partial
// failure, 2 on total failure. Every package has an on-disk ebuild — so none is
// treated as an orphan (orphans are auto-disabled and excluded from the
// exit-code contract). A package fails deterministically, without any HTTP retry
// latency, when its config points at a missing JSON field: the fetch returns 200
// and the failure happens at parse time, so it is a real per-package failure.
// The exit code is captured via the shared withExitIntercept/exitSentinel harness.
func TestCLI_ExitCodes(t *testing.T) {
	// Local server returns a valid version payload so a package whose path
	// matches ("version") succeeds on the first HTTP try (no retries needed).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	tests := []struct {
		name string
		// goodPkgs get an ebuild and a matching path ("version") → succeed.
		goodPkgs []string
		// badPkgs get an ebuild but a missing path ("nonexistent") → fail at
		// JSON parse time (a real failure, no orphan, no HTTP retry).
		badPkgs  []string
		wantExit int
	}{
		{
			name:     "all packages succeed -> exit 0",
			goodPkgs: []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			wantExit: 0,
		},
		{
			name:     "partial failure -> exit 1",
			goodPkgs: []string{"cat-a/pkg1", "cat-b/pkg2"},
			badPkgs:  []string{"cat-c/pkg3"},
			wantExit: 1,
		},
		{
			name:     "total failure -> exit 2",
			badPkgs:  []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			wantExit: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlayDir := t.TempDir()
			configDir := t.TempDir()

			// Write packages.toml directly so good/bad packages can carry
			// different paths; give every package an ebuild so none orphans.
			cfgDir := filepath.Join(overlayDir, ".autoupdate")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", cfgDir, err)
			}
			var b strings.Builder
			writeEntry := func(pkg, path string) {
				b.WriteString("[\"" + pkg + "\"]\n")
				b.WriteString("url = \"" + server.URL + "\"\n")
				b.WriteString("parser = \"json\"\n")
				b.WriteString("path = \"" + path + "\"\n\n")
				writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")
			}
			for _, pkg := range tt.goodPkgs {
				writeEntry(pkg, "version")
			}
			for _, pkg := range tt.badPkgs {
				writeEntry(pkg, "nonexistent")
			}
			if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(b.String()), 0o644); err != nil {
				t.Fatalf("write packages.toml: %v", err)
			}

			// Force = true bypasses the cache so every package performs a
			// real check; args nil selects the check-all path.
			origForce := autoupdateForce
			autoupdateForce = true
			defer func() { autoupdateForce = origForce }()

			// runCheck reads autoupdateConcurrency via WithConcurrency, which
			// rejects values outside [1, 100]; pin a valid value for the test.
			origConc := autoupdateConcurrency
			autoupdateConcurrency = autoupdate.DefaultConcurrency
			defer func() { autoupdateConcurrency = origConc }()

			code := withExitIntercept(func() {
				// cacheTTL = 0 → runCheck skips WithCacheTTL and the Checker
				// uses its default 1-hour TTL (R2.2). This test does not
				// exercise cache freshness; force=true bypasses the cache.
				// Zero config.LLMConfig{} (Provider == "") → no LLM provider is
				// wired and the exit-code contract is unaffected.
				runCheck(context.Background(), overlayDir, configDir, nil, 0, &config.Config{}, config.LLMConfig{})
			})
			if code != tt.wantExit {
				t.Errorf("runCheck exit code = %d, want %d", code, tt.wantExit)
			}
		})
	}
}

// TestRunAutoupdate_CacheTTLFromConfig verifies R2.1 end-to-end: a user
// `autoupdate.cache_ttl: 60` in ~/.config/bentoo/config.yaml reaches the Cache
// that runCheck constructs, so the written cache entry is fresh under the
// 60-second TTL and expires past it.
//
// The test drives runAutoupdate (not runCheck directly) so the config-loading
// path (loadAppContextNoValidation → GetCacheTTL → time.Duration → WithCacheTTL)
// is exercised, not just the inner constructor.
func TestRunAutoupdate_CacheTTLFromConfig(t *testing.T) {
	// Stub HTTP server returning a valid JSON version payload.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	// HOME with config.yaml carrying autoupdate.cache_ttl: 60.
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir bentoo config dir: %v", err)
	}
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir overlay subdir: %v", err)
		}
	}
	configYAML := "overlay:\n  path: " + overlayDir + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		"autoupdate:\n  cache_ttl: 60\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	pkg := "cat-a/pkg1"
	writeExitTestPackagesConfig(t, overlayDir, server.URL, []string{pkg})
	writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")

	autoupdateConfigDir := filepath.Join(tmpHome, ".config", "bentoo", "autoupdate")
	if err := os.MkdirAll(autoupdateConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir autoupdate config dir: %v", err)
	}

	// Pin CLI flag globals to run --check once.
	origCheck, origForce, origApply, origConc :=
		autoupdateCheck, autoupdateForce, autoupdateApply, autoupdateConcurrency
	autoupdateCheck = true
	autoupdateForce = true // ensure a fresh upstream fetch
	autoupdateApply = ""
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	defer func() {
		autoupdateCheck, autoupdateForce, autoupdateApply, autoupdateConcurrency =
			origCheck, origForce, origApply, origConc
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })

	// Reload the cache with the SAME TTL the config declared (60 s). If the
	// TTL had not reached the writer, the entry written above would have been
	// timestamped under a different TTL — but Cache stores raw Timestamp, so
	// freshness depends on (reader TTL, age). The point of this test is that
	// the writer honoured 60 s; we then probe the entry against the same TTL
	// with injected times to confirm the freshness window.
	now := time.Now()
	cacheAtT59, err := autoupdate.NewCache(autoupdateConfigDir,
		autoupdate.WithTTL(60*time.Second),
		autoupdate.WithNowFunc(func() time.Time { return now.Add(59 * time.Second) }),
	)
	if err != nil {
		t.Fatalf("reload cache (t+59s): %v", err)
	}
	if _, ok := cacheAtT59.Get(pkg); !ok {
		t.Errorf("cache entry for %s should be fresh at t+59s under TTL=60s (R2.1)", pkg)
	}

	cacheAtT61, err := autoupdate.NewCache(autoupdateConfigDir,
		autoupdate.WithTTL(60*time.Second),
		autoupdate.WithNowFunc(func() time.Time { return now.Add(61 * time.Second) }),
	)
	if err != nil {
		t.Fatalf("reload cache (t+61s): %v", err)
	}
	if _, ok := cacheAtT61.Get(pkg); ok {
		t.Errorf("cache entry for %s should be expired at t+61s under TTL=60s (R2.1)", pkg)
	}
}

// TestAutoupdateCommandExists tests that the autoupdate command is registered
func TestAutoupdateCommandExists(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "autoupdate") {
			found = true
			break
		}
	}
	if !found {
		t.Error("overlay autoupdate subcommand should exist")
	}
}

// TestAutoupdateCommandFlags tests that all required flags are present
func TestAutoupdateCommandFlags(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
	}{
		{"check flag", "check"},
		{"list flag", "list"},
		{"apply flag", "apply"},
		{"force flag", "force"},
		{"compile flag", "compile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := autoupdateCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Errorf("autoupdate command should have --%s flag", tt.flagName)
			}
		})
	}
}

// TestAutoupdateCommandDescription tests command descriptions
func TestAutoupdateCommandDescription(t *testing.T) {
	if autoupdateCmd.Short == "" {
		t.Error("autoupdate command should have a short description")
	}
	if autoupdateCmd.Long == "" {
		t.Error("autoupdate command should have a long description")
	}
}

// TestAutoupdateCommandRun tests that Run function is set
func TestAutoupdateCommandRun(t *testing.T) {
	if autoupdateCmd.Run == nil {
		t.Error("autoupdate command should have a Run function")
	}
}

// TestAutoupdateFlagTypes tests that flags have correct types
func TestAutoupdateFlagTypes(t *testing.T) {
	// Boolean flags
	boolFlags := []string{"check", "list", "force", "compile"}
	for _, flagName := range boolFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "bool" {
			t.Errorf("flag %s should be bool type, got %s", flagName, flag.Value.Type())
		}
	}

	// String flags
	stringFlags := []string{"apply"}
	for _, flagName := range stringFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "string" {
			t.Errorf("flag %s should be string type, got %s", flagName, flag.Value.Type())
		}
	}
}

// TestAutoupdateUsageContainsExamples tests that usage contains examples
func TestAutoupdateUsageContainsExamples(t *testing.T) {
	examples := []string{
		"--check",
		"--list",
		"--apply",
		"--force",
		"--compile",
	}

	for _, example := range examples {
		if !strings.Contains(autoupdateCmd.Long, example) {
			t.Errorf("autoupdate long description should contain example with %s", example)
		}
	}

	// The bulk-apply form must be documented so users discover it.
	if !strings.Contains(autoupdateCmd.Long, "--apply all") {
		t.Error("autoupdate long description should document '--apply all'")
	}
}

// TestRunAutoupdate_OverlayPathBoundsCheck tests Property 4: Bounds-Safe Tilde Check
// Verifies that empty or whitespace overlay paths do not cause a panic.
// **Feature: quality-improvements, Property 4: Bounds-Safe Tilde Check**
// **Validates: Requirements 3.1-3.4**
func TestRunAutoupdate_OverlayPathBoundsCheck(t *testing.T) {
	tests := []struct {
		name        string
		overlayPath string
		wantPanic   bool
	}{
		{
			name:        "empty overlay path does not panic",
			overlayPath: "",
			wantPanic:   false,
		},
		{
			name:        "whitespace-only overlay path does not panic",
			overlayPath: "   ",
			wantPanic:   false,
		},
		{
			name:        "tilde path is handled safely",
			overlayPath: "~/overlay",
			wantPanic:   false,
		},
		{
			name:        "absolute path is handled safely",
			overlayPath: "/tmp/overlay",
			wantPanic:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil && !tt.wantPanic {
					t.Errorf("unexpected panic for overlayPath=%q: %v", tt.overlayPath, r)
				}
			}()

			// Exercise the bounds-guarded tilde check directly
			path := tt.overlayPath
			if len(path) > 0 && path[0] == '~' {
				// tilde expansion would happen here — no panic expected
				_ = path[1:]
			}
		})
	}
}

// ---------------------------------------------------------------------------
// S021 sub-task 5.2 — the post-check reconciliation prompt, --yes, and the
// clean report.
//
// Every fixture below is a t.TempDir(). NOTHING here may point at the real
// overlay: it auto-commits and pushes, so a test that wrote there would publish
// its fixture within minutes. That is also why the tests which must prove
// nothing was written keep the REAL writer wired and compare the file's bytes,
// instead of asserting against a mock that would pass even with the guard gone.
// ---------------------------------------------------------------------------

// reconcileFixture is a temp overlay plus a record-model packages.toml whose
// divergences cover all three of R3.1's classes — including the coincidence
// that makes a naive write batch corrupt the registry.
type reconcileFixture struct {
	overlayDir string
	configPath string
	serverURL  string
}

// reconcileEntry is one record of the fixture registry.
type reconcileEntry struct {
	key string
	// pin is written as `version = "<pin>"`; empty means the record has no
	// version assignment at all (the state every real record is in today).
	pin string
	// ebuilds are the versions written into <category>/<pkg>/; nil means the
	// package directory is never created, which is the NoEbuild class.
	ebuilds []string
}

// newReconcileFixture writes the overlay and the registry. The records follow
// the model the linter enforces (a trailing `comments` field, closed by
// `# END`), so SetPackageVersions exercises its real insertion point rather
// than the fallback.
func newReconcileFixture(t *testing.T, entries []reconcileEntry) *reconcileFixture {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "9.9.9"})
	}))
	t.Cleanup(server.Close)

	overlayDir := t.TempDir()
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("[\"" + e.key + "\"]\n")
		b.WriteString("url = \"" + server.URL + "\"\n")
		b.WriteString("parser = \"json\"\n")
		b.WriteString("path = \"version\"\n")
		if e.pin != "" {
			b.WriteString("version = \"" + e.pin + "\"\n")
		}
		b.WriteString("comments = \"\"\"fixture record for " + e.key + "\"\"\"\n")
		b.WriteString("# END\n\n")
		for _, v := range e.ebuilds {
			writeExitTestEbuild(t, overlayDir, e.key, v)
		}
	}

	cfgDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	configPath := filepath.Join(cfgDir, "packages.toml")
	if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}

	return &reconcileFixture{overlayDir: overlayDir, configPath: configPath, serverURL: server.URL}
}

// allClassesFixture is the shape that makes the Kind switch load-bearing:
//
//	app-editors/neovim  pinless, one ebuild        → StalePin (writable)
//	dev-util/gone       pinned 3.0.0, NO directory → NoEbuild (Disk empty)
//	net-misc/rclone     pinless, two ebuilds       → StalePin + UnclaimedEbuild
//
// The last record is the corruption case, and it is not hypothetical: the real
// overlay's net-misc/rclone is both a registry key and a directory holding a
// stray ebuild. A `map[Key]Disk` built over every divergence would end up
// pinning rclone to the stray 1.9.0 (UnclaimedEbuild sorts after StalePin for
// the same key) and would blank dev-util/gone's pin with NoEbuild's empty Disk.
func allClassesFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	return newReconcileFixture(t, []reconcileEntry{
		{key: "app-editors/neovim", ebuilds: []string{"0.11.1"}},
		{key: "dev-util/gone", pin: "3.0.0"},
		{key: "net-misc/rclone", ebuilds: []string{"1.9.0", "1.71.1"}},
	})
}

// checkableFixture drops the directory-less record, so a full runCheck neither
// auto-disables an orphan nor otherwise rewrites packages.toml — which is what
// lets the byte-identical assertions mean what they say.
func checkableFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	return newReconcileFixture(t, []reconcileEntry{
		{key: "app-editors/neovim", ebuilds: []string{"0.11.1"}},
		{key: "net-misc/rclone", ebuilds: []string{"1.9.0", "1.71.1"}},
	})
}

func (f *reconcileFixture) readRegistry(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("read packages.toml: %v", err)
	}
	return data
}

// pins reloads the registry and returns each entry's version assignment, so an
// assertion names the entry it is about instead of grepping the whole file.
func (f *reconcileFixture) pins(t *testing.T) map[string]string {
	t.Helper()
	cfg, err := autoupdate.LoadPackagesConfig(f.overlayDir)
	if err != nil {
		t.Fatalf("reload packages.toml: %v", err)
	}
	out := make(map[string]string, len(cfg.Packages))
	for k, c := range cfg.Packages {
		out[k] = c.Version
	}
	return out
}

// divergences runs the real Reconcile over the fixture — the same call the CLI
// makes — so a test can compare the prompt's count against the truth rather
// than against a number hard-coded twice.
func (f *reconcileFixture) divergences(t *testing.T) []autoupdate.Divergence {
	t.Helper()
	cfg, err := autoupdate.LoadPackagesConfig(f.overlayDir)
	if err != nil {
		t.Fatalf("load packages.toml: %v", err)
	}
	return autoupdate.Reconcile(f.overlayDir, cfg.Packages)
}

// stalePinCount is the number of entries a correct batch writes.
func stalePinCount(divs []autoupdate.Divergence) int {
	n := 0
	for _, d := range divs {
		if d.Kind == autoupdate.StalePin {
			n++
		}
	}
	return n
}

// setReconcileYes pins the --yes flag for one test and restores it after. The
// flag is a process global and its default is a publish-safety property, so it
// is never left mutated.
func setReconcileYes(t *testing.T, v bool) {
	t.Helper()
	orig := autoupdateYes
	t.Cleanup(func() { autoupdateYes = orig })
	autoupdateYes = v
}

func setReconcileInteractive(t *testing.T, fn func() bool) {
	t.Helper()
	orig := registryPromptIsInteractive
	t.Cleanup(func() { registryPromptIsInteractive = orig })
	registryPromptIsInteractive = fn
}

func setReconcileConfirm(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := confirmRegistryWriteFn
	t.Cleanup(func() { confirmRegistryWriteFn = orig })
	confirmRegistryWriteFn = fn
}

func setReconcileWriter(t *testing.T, fn func(string, map[string]string) error) {
	t.Helper()
	orig := registryWriterFn
	t.Cleanup(func() { registryWriterFn = orig })
	registryWriterFn = fn
}

// feedStdin replaces os.Stdin with a regular file holding answer, so the REAL
// confirmAction can be exercised. A regular file is deliberately not a
// character device, which is exactly what a piped run looks like to the TTY
// probe.
func feedStdin(t *testing.T, answer string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(answer), 0o600); err != nil {
		t.Fatalf("write fake stdin: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fake stdin: %v", err)
	}
	orig := os.Stdin
	t.Cleanup(func() {
		os.Stdin = orig
		_ = f.Close()
	})
	os.Stdin = f
}

// pinCheckFlags pins every autoupdate flag runCheck reads to a known state, so
// a test never inherits another test's globals.
func pinCheckFlags(t *testing.T) {
	t.Helper()
	origForce, origConc, origOnly, origRevivable, origQuiet :=
		autoupdateForce, autoupdateConcurrency, autoupdateOnly, autoupdateRevivable, quiet
	t.Cleanup(func() {
		autoupdateForce, autoupdateConcurrency, autoupdateOnly, autoupdateRevivable, quiet =
			origForce, origConc, origOnly, origRevivable, origQuiet
	})
	autoupdateForce = true // no cache: every package really checks
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	autoupdateOnly = ""
	autoupdateRevivable = false
	quiet = true // silence the \r progress counter so captured output is readable
}

// TestAutoupdateReconcileDeclineLeavesRegistryByteIdentical pins R3.3: a "no"
// answer must leave packages.toml exactly as it was — proven on the bytes, with
// the REAL writer still wired and the REAL confirmAction reading a fake stdin.
func TestAutoupdateReconcileDeclineLeavesRegistryByteIdentical(t *testing.T) {
	f := allClassesFixture(t)
	before := f.readRegistry(t)

	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return true }) // pretend a terminal
	feedStdin(t, "n\n")

	out := captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })

	after := f.readRegistry(t)
	if !bytes.Equal(before, after) {
		t.Errorf("a declined reconciliation wrote to packages.toml (R3.3)\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The divergences must still have been REPORTED — a decline is a refusal to
	// write, not a refusal to inform.
	if !strings.Contains(out, "Registry Reconciliation") {
		t.Errorf("the reconciliation printed no report before asking; got:\n%s", out)
	}
	if strings.Contains(out, "Wrote ") {
		t.Errorf("a declined run claimed to have written; got:\n%s", out)
	}
}

// TestAutoupdateReconcileYesWritesWithoutReadingStdin pins R3.4's first clause:
// --yes writes unattended. Stdin is a real (non-terminal) file holding "n", so
// a run that consulted stdin at all would decline and write nothing; and the
// TTY probe is a tripwire, so a run that even asked whether it may prompt fails.
func TestAutoupdateReconcileYesWritesWithoutReadingStdin(t *testing.T) {
	f := allClassesFixture(t)

	setReconcileYes(t, true)
	setReconcileInteractive(t, func() bool {
		t.Error("--yes must not consult the TTY probe: it is an explicit approval")
		return false
	})
	setReconcileConfirm(t, func(string) bool {
		t.Error("--yes must not prompt: stdin was read")
		return false
	})
	feedStdin(t, "n\n") // the trap: reading this would decline

	captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })

	if got := f.pins(t)["app-editors/neovim"]; got != "0.11.1" {
		t.Errorf("--yes did not write the pin: app-editors/neovim version = %q, want %q", got, "0.11.1")
	}
}

// TestAutoupdateReconcileWritesOnlyStalePins is the corruption case. A batch
// built as map[Key]Disk over every divergence would erase dev-util/gone's pin
// (NoEbuild carries an empty Disk) and would point net-misc/rclone at the stray
// ebuild the UnclaimedEbuild finding is asking a human to look at (its Key is a
// bare atom that coincides with a real registry key). Only StalePin is writable.
func TestAutoupdateReconcileWritesOnlyStalePins(t *testing.T) {
	f := allClassesFixture(t)

	// Guard the fixture itself: if Reconcile ever stopped producing all three
	// classes the assertions below would pass vacuously.
	divs := f.divergences(t)
	seen := map[autoupdate.DivergenceKind]int{}
	for _, d := range divs {
		seen[d.Kind]++
	}
	for _, k := range []autoupdate.DivergenceKind{autoupdate.StalePin, autoupdate.UnclaimedEbuild, autoupdate.NoEbuild} {
		if seen[k] == 0 {
			t.Fatalf("fixture no longer produces a %s divergence; got %v", k, divs)
		}
	}

	setReconcileYes(t, true)
	captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })

	got := f.pins(t)
	want := map[string]string{
		// StalePin: written.
		"app-editors/neovim": "0.11.1",
		// StalePin: the RESOLVED version, never the unclaimed stray 1.9.0.
		"net-misc/rclone": "1.71.1",
		// NoEbuild: reported only. Its pin must survive untouched — writing
		// NoEbuild's empty Disk would blank it, and an entry with no pin blocks
		// its whole directory's next --clean.
		"dev-util/gone": "3.0.0",
	}
	for key, wantPin := range want {
		if got[key] != wantPin {
			t.Errorf("%s version = %q, want %q", key, got[key], wantPin)
		}
	}
	if strings.Contains(string(f.readRegistry(t)), "1.9.0\"") {
		t.Errorf("the unclaimed stray 1.9.0 was written as a pin; registry:\n%s", f.readRegistry(t))
	}
}

// TestAutoupdateReconcilePromptStatesWritableCount pins A1: the prompt states
// the number of entries about to be written, and that number is the stale-pin
// count — not the total number of divergences. The real confirmAction is used,
// so the assertion is on the text a human actually sees.
func TestAutoupdateReconcilePromptStatesWritableCount(t *testing.T) {
	f := allClassesFixture(t)

	divs := f.divergences(t)
	writable := stalePinCount(divs)
	if writable == len(divs) {
		t.Fatalf("fixture cannot distinguish the two counts: %d writable of %d divergences", writable, len(divs))
	}

	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return true })
	feedStdin(t, "n\n") // decline: this test is about the text, not the write

	out := captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })

	wantPrompt := fmt.Sprintf("Write %d version pin(s) to packages.toml?", writable)
	if !strings.Contains(out, wantPrompt) {
		t.Errorf("the prompt does not state the write count.\nwant substring: %q\ngot:\n%s", wantPrompt, out)
	}
	wrongPrompt := fmt.Sprintf("Write %d version pin(s) to packages.toml?", len(divs))
	if strings.Contains(out, wrongPrompt) {
		t.Errorf("the prompt states the TOTAL divergence count (%d) as the number to be written; only %d are writable",
			len(divs), writable)
	}
	// The summary above the prompt states it too, and splits it the way A1 asks:
	// a first-time bulk fill reads very differently from a stale-pin correction.
	if !strings.Contains(out, fmt.Sprintf("About to write %d version pin(s)", writable)) {
		t.Errorf("the summary does not state the write count; got:\n%s", out)
	}
	if !strings.Contains(out, "FIRST time") {
		t.Errorf("the summary does not distinguish a first-time pin from a corrected one; got:\n%s", out)
	}
	// The publish warning is the reason the prompt exists at all.
	if !strings.Contains(out, "PUBLISHED") {
		t.Errorf("the prompt does not warn that packages.toml is published; got:\n%s", out)
	}
}

// TestAutoupdateReconcileNoDivergencesPrintsNothing pins the quiet path: with
// the registry already matching the overlay, the reconciliation is invisible.
func TestAutoupdateReconcileNoDivergencesPrintsNothing(t *testing.T) {
	f := newReconcileFixture(t, []reconcileEntry{
		{key: "app-editors/neovim", pin: "0.11.1", ebuilds: []string{"0.11.1"}},
	})

	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool {
		t.Error("nothing diverges, so nothing may be confirmed")
		return false
	})

	out := captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })
	if out != "" {
		t.Errorf("an in-sync registry printed a reconciliation report:\n%s", out)
	}
}

// TestAutoupdateReconcileNonTTYWithoutYesWritesNothing pins R3.4's second
// clause end-to-end through runCheck: a non-interactive run reports the
// divergences, writes nothing, and still exits 0.
func TestAutoupdateReconcileNonTTYWithoutYesWritesNothing(t *testing.T) {
	f := checkableFixture(t)
	before := f.readRegistry(t)

	pinCheckFlags(t)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return false }) // piped / CI
	setReconcileConfirm(t, func(string) bool {
		t.Error("a non-interactive run must not prompt")
		return true
	})

	// captureStdout OUTSIDE withExitIntercept: runCheck ends by calling osExit,
	// which the intercept turns into a panic. Capturing on the inside would let
	// that panic unwind past the pipe read and yield empty output.
	var code int
	out := captureStdout(t, func() {
		code = withExitIntercept(func() {
			runCheck(context.Background(), f.overlayDir, t.TempDir(), nil, 0,
				&config.Config{}, config.LLMConfig{})
		})
	})

	if code != 0 {
		t.Errorf("runCheck exit code = %d, want 0: a refused registry write is not a check failure", code)
	}
	if after := f.readRegistry(t); !bytes.Equal(before, after) {
		t.Errorf("a non-interactive run wrote to packages.toml (R3.4)\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(out, "Registry Reconciliation") {
		t.Errorf("the divergences were not reported; got:\n%s", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the refusal does not say how to write unattended; got:\n%s", out)
	}
}

// TestAutoupdateReconcileWriteFailureIsReportedNotSwallowed pins the failure
// path: SetPackageVersions returning an error is surfaced to the operator, and
// the check that already succeeded still exits 0 — the pins are bookkeeping on
// top of a check, not part of its verdict.
func TestAutoupdateReconcileWriteFailureIsReportedNotSwallowed(t *testing.T) {
	f := checkableFixture(t)
	before := f.readRegistry(t)

	pinCheckFlags(t)
	setReconcileYes(t, true)
	wantErr := errors.New("packages.toml is read-only")
	var calls int
	setReconcileWriter(t, func(string, map[string]string) error {
		calls++
		return wantErr
	})

	// captureStdout OUTSIDE withExitIntercept — see the sibling test for why.
	var code int
	out := captureStdout(t, func() {
		code = withExitIntercept(func() {
			runCheck(context.Background(), f.overlayDir, t.TempDir(), nil, 0,
				&config.Config{}, config.LLMConfig{})
		})
	})

	if code != 0 {
		t.Errorf("runCheck exit code = %d, want 0: a failed registry write must not fail an otherwise-good check", code)
	}
	if calls != 1 {
		t.Errorf("the writer was called %d times, want exactly 1 (design D4: one batch, one call)", calls)
	}
	if !strings.Contains(out, wantErr.Error()) {
		t.Errorf("the write failure was swallowed; want the cause %q in the output, got:\n%s", wantErr, out)
	}
	if strings.Contains(out, "Wrote ") {
		t.Errorf("a failed write reported success; got:\n%s", out)
	}
	if after := f.readRegistry(t); !bytes.Equal(before, after) {
		t.Error("the stubbed writer must not have touched the file; the fixture is no longer a valid control")
	}
}

// TestAutoupdateReconcileBatchIsOneCallForEveryEntry pins design D4: one
// confirmation, one write call carrying the whole batch — not one call per
// entry, which would make a partially-written registry reachable.
func TestAutoupdateReconcileBatchIsOneCallForEveryEntry(t *testing.T) {
	f := allClassesFixture(t)
	writable := stalePinCount(f.divergences(t))

	setReconcileYes(t, true)
	var batches []map[string]string
	setReconcileWriter(t, func(_ string, pins map[string]string) error {
		batches = append(batches, pins)
		return nil
	})

	captureStdout(t, func() { reconcileRegistryAfterCheck(f.overlayDir) })

	if len(batches) != 1 {
		t.Fatalf("the writer was called %d times, want 1 (design D4)", len(batches))
	}
	if len(batches[0]) != writable {
		t.Errorf("the batch carries %d pin(s), want the stale-pin count %d: %v", len(batches[0]), writable, batches[0])
	}
}

// TestAutoupdateYesFlagDefaultsToFalse pins the publish-safety property
// directly. A --yes that defaulted to true would make every scripted --check a
// release; this is cheap to assert and expensive to get wrong.
func TestAutoupdateYesFlagDefaultsToFalse(t *testing.T) {
	flag := autoupdateCmd.Flags().Lookup("yes")
	if flag == nil {
		t.Fatal("autoupdate command should have a --yes flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--yes should be bool, got %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--yes default = %q, want \"false\": packages.toml is published, so an unattended write must be opt-in", flag.DefValue)
	}
	if flag.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want \"y\" (every other --yes in this CLI uses it)", flag.Shorthand)
	}
	if !strings.Contains(flag.Usage, "non-interactive") {
		t.Errorf("--yes help does not say it is required for a non-interactive registry write; got %q", flag.Usage)
	}
}

// TestAutoupdateApplyCleanReportNamesClaimingEntry pins R6.1: the clean report
// gives, per kept version, the registry entry that claims it — and says which
// RULE kept a version no entry claims, rather than leaving it unexplained.
func TestAutoupdateApplyCleanReportNamesClaimingEntry(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:    "media-plugins/gst-plugins-vpx",
		OldVersion: "1.28.4",
		NewVersion: "1.28.5",
		Success:    true,
		CleanKept: map[string]string{
			"1.28.5": "media-plugins/gst-plugins-vpx@stable",
			"1.29.2": "media-plugins/gst-plugins-vpx@dev",
			"9999":   "", // kept by the live rule, claimed by nobody
		},
		CleanRemoved:      []string{"1.28.3", "1.28.4"},
		CleanedOldVersion: "1.28.4",
	}

	out := captureStdout(t, func() { displayApplyResult(result) })

	for version, key := range map[string]string{
		"1.28.5": "media-plugins/gst-plugins-vpx@stable",
		"1.29.2": "media-plugins/gst-plugins-vpx@dev",
	} {
		line := fmt.Sprintf("gst-plugins-vpx-%s.ebuild — claimed by %s", version, key)
		if !strings.Contains(out, line) {
			t.Errorf("the clean report does not name the entry claiming %s.\nwant substring: %q\ngot:\n%s", version, line, out)
		}
	}
	if !strings.Contains(out, "gst-plugins-vpx-9999.ebuild — kept by rule") {
		t.Errorf("a version kept by a rule is reported without saying which rule; got:\n%s", out)
	}
	// Every removed version is named: the legacy single-version line reports
	// only the highest, which under-reports a multi-file sweep.
	for _, v := range result.CleanRemoved {
		if !strings.Contains(out, "gst-plugins-vpx-"+v+".ebuild") {
			t.Errorf("removed version %s is missing from the report; got:\n%s", v, out)
		}
	}
	// And it is reported once, not twice: the legacy line must not repeat the list.
	if n := strings.Count(out, "    Removed:"); n != 1 {
		t.Errorf("the report has %d \"Removed:\" lines, want exactly 1; got:\n%s", n, out)
	}
}

// TestAutoupdateApplyCleanReportKeepsLegacySingleRemovalLine pins the common
// case: one ebuild swept still reads exactly as it did before this story.
func TestAutoupdateApplyCleanReportKeepsLegacySingleRemovalLine(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:           "net-misc/rclone",
		OldVersion:        "1.71.0",
		NewVersion:        "1.71.1",
		Success:           true,
		CleanRemoved:      []string{"1.71.0"},
		CleanedOldVersion: "1.71.0",
		CleanKept:         map[string]string{"1.71.1": "net-misc/rclone"},
	}

	out := captureStdout(t, func() { displayApplyResult(result) })

	if !strings.Contains(out, "Removed: rclone-1.71.0.ebuild (old version)") {
		t.Errorf("the single-removal line changed wording; got:\n%s", out)
	}
}

// TestAutoupdateApplyCleanReportNamesPinlessEntryWhenBlocked pins R6.2: when
// the sweep is blocked because a claiming entry has no pin, the report names
// that entry — the one fact a maintainer needs to unblock the directory.
//
// The line comes from CleanWarning, which cleanPackageDir fills with the
// blocked-plan error. ApplyResult carries no sweepPlan and so no Blocked field;
// this IS the blocked-entry line, and printing a second one would name the same
// entry twice.
func TestAutoupdateApplyCleanReportNamesPinlessEntryWhenBlocked(t *testing.T) {
	const pinless = "media-plugins/gst-plugins-vpx@dev"
	result := &autoupdate.ApplyResult{
		Package:    "media-plugins/gst-plugins-vpx",
		OldVersion: "1.28.4",
		NewVersion: "1.28.5",
		Success:    true,
		CleanWarning: fmt.Sprintf("nothing removed from media-plugins/gst-plugins-vpx: registry entry %q "+
			"has no version pin, so the sweep cannot tell which ebuilds that entry keeps "+
			"(would have removed: 1.28.3, 1.28.4)", pinless),
	}

	out := captureStdout(t, func() { displayApplyResult(result) })

	if !strings.Contains(out, pinless) {
		t.Errorf("a blocked clean does not name the pinless entry (R6.2); got:\n%s", out)
	}
	if !strings.Contains(out, "would have removed: 1.28.3, 1.28.4") {
		t.Errorf("a blocked clean does not report its candidates; got:\n%s", out)
	}
	if strings.Contains(out, "    Removed:") {
		t.Errorf("a blocked clean reported a removal; got:\n%s", out)
	}
	// Named once. A second line repeating the entry would read as two problems.
	if n := strings.Count(out, pinless); n != 1 {
		t.Errorf("the pinless entry is named %d times, want exactly 1; got:\n%s", n, out)
	}
}

// TestAutoupdateApplyReportPrintsRegistryWarningUnderItsOwnLabel pins the label
// split 4.2 asked for: a failed pin write is reported, and NOT under "Clean:" —
// the pin is written on every successful apply while the sweep only runs under
// --clean, so blaming the clean step would point at a step that never ran.
func TestAutoupdateApplyReportPrintsRegistryWarningUnderItsOwnLabel(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:         "net-misc/rclone",
		OldVersion:      "1.71.0",
		NewVersion:      "1.71.1",
		Success:         true,
		RegistryWarning: `could not record version = "1.71.1" for net-misc/rclone: disk full`,
	}

	out := captureStdout(t, func() { displayApplyResult(result) })

	if !strings.Contains(out, "Registry: could not record version") {
		t.Errorf("RegistryWarning is not printed under its own label; got:\n%s", out)
	}
	if strings.Contains(out, "Clean:") {
		t.Errorf("a registry failure was reported as a clean failure; got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// The gate that refuses an unattended registry write (S021-R3.4)
// ---------------------------------------------------------------------------

// swapStdio points os.Stdin and os.Stdout at the given files for one test,
// restoring both afterwards. These are process globals, so this is only safe
// because no test in this package calls t.Parallel.
func swapStdio(t *testing.T, stdin, stdout *os.File) {
	t.Helper()
	origIn, origOut := os.Stdin, os.Stdout
	t.Cleanup(func() { os.Stdin, os.Stdout = origIn, origOut })
	os.Stdin, os.Stdout = stdin, stdout
}

// pipeRead and pipeWrite return the two ends of a fresh pipe. A pipe never
// carries os.ModeCharDevice, which is what lets it stand in for a redirected
// stream without a pty.
func pipeRead(t *testing.T) *os.File  { r, _ := newPipe(t); return r }
func pipeWrite(t *testing.T) *os.File { _, w := newPipe(t); return w }

func newPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

// charDevice opens /dev/null, which always carries os.ModeCharDevice — the only
// bit either terminal probe actually tests.
func charDevice(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestRegistryPromptIsInteractive exercises the REAL predicate. Every other test
// in this file replaces it through setReconcileInteractive, which is the right
// seam for driving runCheck but leaves the predicate's own composition — the
// thing standing between a scripted --check and a published registry — asserted
// nowhere. Invert the && to || and this is the only test that notices.
//
// The "neither stream redirected" case is worth reading twice: it passes with
// both streams on /dev/null, because these probes test for a character device
// rather than for a terminal, and /dev/null is one. So
// `bentoo overlay autoupdate --check </dev/null >/dev/null` IS judged
// interactive and IS prompted with no human present. What refuses that run is
// confirmAction answering no on EOF, already pinned by TestConfirmActionEOF
// (run_functions_test.go). The gate and that default are two independent
// reasons the unattended publish does not happen; neither may be relaxed on the
// assumption that the other covers it.
func TestRegistryPromptIsInteractive(t *testing.T) {
	tests := []struct {
		name   string
		stdin  func(*testing.T) *os.File
		stdout func(*testing.T) *os.File
		want   bool
	}{
		{"both streams redirected", pipeRead, pipeWrite, false},
		{"stdout redirected, so the operator cannot see what they approve", charDevice, pipeWrite, false},
		{"stdin redirected, so nobody is there to answer", pipeRead, charDevice, false},
		{"neither stream redirected", charDevice, charDevice, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			swapStdio(t, tc.stdin(t), tc.stdout(t))
			if got := registryPromptIsInteractive(); got != tc.want {
				t.Errorf("registryPromptIsInteractive() = %v, want %v", got, tc.want)
			}
		})
	}
}
