package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-tasks 3.1..3.4 —
// every spawner uses the scoped permissions (S051-R2.1..R2.9, R3.1..R3.8,
// R4.1, R4.2).
//
// Everything is read from the argv and the *exec.Cmd each spawner handed to its
// own exec seam, so no test here depends on HOW a spawner builds its argv —
// only on what reaches the CLI. The allow sets are asserted EXACTLY: a rule the
// requirement does not name is as much a failure as a missing one.

import (
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestManifestFix_UpstreamHostsFromConfigAndError is R3.5 end to end through
// the Applier: the hosts of THIS package's registry URL and FallbackURL, plus
// the http(s) hosts pkgdev printed, reach the agent's WebFetch rules. Hostile
// halves: another package's config hosts and an ftp:// host in the error text
// do not.
func TestManifestFix_UpstreamHostsFromConfigAndError(t *testing.T) {
	isolateSecretsPaths(t)
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg, oldVersion, newVersion := "dev-games/godot", "4.7_rc3", "4.7"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldVersion, NewVersion: newVersion, Status: StatusPending})

	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	fixer := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, fixer.WithFixerExecCommand(seam))

	pkgdevOut := "SRC_URI is unreachable: 404 Not Found: https://dist.example.com/godot-4.7.tar.xz\n" +
		"also tried ftp://ftp.example.edu/pub/godot-4.7.tar.xz"
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFailsPrinting(pkgdevOut)),
		WithApplierFixer(fixer),
		WithApplierPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			pkg:            {URL: "https://upstream.example.org/releases.json", Parser: "json", Path: "tag", FallbackURL: "https://fallback.example.net/tags"},
			"dev-libs/foo": {URL: "https://other-package.example.org/x", Parser: "json", Path: "v"},
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	_, _ = applier.Apply(t.Context(), pkg, false) // pkgdev keeps failing; only the agent's argv matters here

	if spy.spawns() != 1 {
		t.Fatalf("the manifest fixer spawned %d agents, want 1", spy.spawns())
	}
	allow := ruleList(t, "manifest fixer", spy.args, "allow")
	for _, h := range []string{"upstream.example.org", "fallback.example.net", "dist.example.com", "github.com", "codeload.github.com", "objects.githubusercontent.com"} {
		if countOf(allow, "WebFetch(domain:"+h+")") != 1 {
			t.Errorf("WebFetch(domain:%s) missing or repeated (R3.3, R3.5); allow = %q", h, allow)
		}
	}
	for _, h := range []string{"other-package.example.org", "ftp.example.edu"} {
		if containsRule(allow, "WebFetch(domain:"+h+")") {
			t.Errorf("WebFetch(domain:%s) was granted; it is not this package's http(s) upstream (R3.5)", h)
		}
	}
}
