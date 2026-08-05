package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// This file pins R4: a DECLARATION is verified against CONTENT whenever the
// content happens to be at hand, and the verification only ever warns.
//
// Two seams it exercises, both from the design:
//
//   - provider.PackageDirProvider, the optional capability a Provider has when
//     the compared repository is already on disk (`provider: local` and
//     `--clone` are the same concrete type). A provider that does not satisfy it
//     simply skips
//     verification — the API providers cannot supply content without ~300 extra
//     rate-limited requests, which the story puts out of scope.
//   - CompareOptions.OverlayPath, the root of the overlay being compared. The
//     comparison needs BOTH ebuilds to compare two of them, and only the
//     upstream side arrives through the provider; PackageInfo carries no path.
//     (The design's Integration Points table names only the Divergence field, so
//     this second field is a gap the test closes rather than a choice it copies.)
//
// The load-bearing assertion is not the finding but its impotence: a finding
// NEVER changes the Verdict (R4.5). One mechanism decides, the other checks, and
// a disagreement between them stays legible precisely because only one of them
// can speak.

// localRootedFakeProvider is a Provider whose compared repository is a directory
// on disk, i.e. the shape `provider: local` and `--clone` both produce.
type localRootedFakeProvider struct {
	root     string
	versions map[string][]string // keyed by "category/pkg"
}

func (p *localRootedFakeProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	v, ok := p.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (p *localRootedFakeProvider) GetName() string   { return "fake-local" }
func (p *localRootedFakeProvider) SupportsAPI() bool { return false }
func (p *localRootedFakeProvider) Close() error      { return nil }

// LocalPackagePath mirrors *GitCloneProvider's: the package directory under the
// repository root. The real one also stats and returns ErrNotFound; this double
// deliberately does not, so the "missing upstream ebuild" case below still
// exercises the READ failing rather than the lookup failing.
func (p *localRootedFakeProvider) LocalPackagePath(category, pkg string) (string, error) {
	return filepath.Join(p.root, category, pkg), nil
}

// The double must satisfy both the Provider contract and the optional capability
// verification type-asserts for; if it stopped satisfying either, every "not
// verified" expectation below would pass for the wrong reason.
var (
	_ provider.Provider           = (*localRootedFakeProvider)(nil)
	_ provider.PackageDirProvider = (*localRootedFakeProvider)(nil)
)

// writeVerifyEbuild writes <root>/<category>/<pkg>/<pkg>-<version>.ebuild, the
// path shape the git-clone provider already resolves for both of its modes.
func writeVerifyEbuild(t *testing.T, root, category, pkg, version, body string) {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, pkg+"-"+version+".ebuild")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// verifyRun compares one package and returns its result together with the
// rendered report, so a finding can be asserted where the operator reads it.
func verifyRun(t *testing.T, overlayRoot string, prov provider.Provider, pkg PackageInfo, div map[string]Divergence) (CompareResult, string) {
	t.Helper()
	report, err := CompareWithProvider([]PackageInfo{pkg}, prov, CompareOptions{
		IncludeSynced:      true,
		IncludeNotInRemote: true,
		OverlayPath:        overlayRoot,
		Divergence:         div,
	})
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	return report.Results[0], FormatReport(report)
}

const (
	zedEbuildOurs   = "EAPI=8\nDESCRIPTION=\"zed\"\nPATCHES=( \"${FILESDIR}/wayland.patch\" )\n"
	zedEbuildStock  = "EAPI=8\nDESCRIPTION=\"zed\"\n"
	zedPatchedEntry = "app-editors/zed@stable"
)

// TestVerifyAgainstLocalContent covers the four-row finding table plus the two
// ways verification declines to run.
//
// _Requirements: R4.1, R4.2, R4.3, R4.4, R4.5_
func TestVerifyAgainstLocalContent(t *testing.T) {
	pkg := PackageInfo{Category: "app-editors", Package: "zed", LatestVersion: "1.0"}

	declared := map[string]Divergence{
		"app-editors/zed": {Patched: true, Reason: "keeps our wayland patch", Entry: zedPatchedEntry},
	}
	silent := map[string]Divergence{
		"app-editors/zed": {}, // a registry entry exists and declares nothing
	}

	t.Run("identical content with a declaration is a stale declaration (R4.2)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, declared)

		if got.Verified != VerifiedIdentical {
			t.Errorf("Verified = %v, want VerifiedIdentical", got.Verified)
		}
		// The declaration still decides: a patched package level with ::gentoo is
		// Keep, and the finding does not turn it into a removal candidate.
		if got.Verdict != VerdictKeep {
			t.Errorf("Verdict = %v, want VerdictKeep; a finding must not change the verdict (R4.5)", got.Verdict)
		}
		low := strings.ToLower(out)
		if !strings.Contains(low, "stale") {
			t.Errorf("report reports no stale declaration; the divergence it describes no longer exists.\n--- report ---\n%s", out)
		}
		if !strings.Contains(out, zedPatchedEntry) {
			t.Errorf("report does not name the declaring entry %q.\n--- report ---\n%s", zedPatchedEntry, out)
		}
	})

	t.Run("differing content with no declaration is an undeclared divergence (R4.3)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, silent)

		if got.Verified != VerifiedDiffers {
			t.Errorf("Verified = %v, want VerifiedDiffers", got.Verified)
		}
		// Still Redundant: the declaration decides, and this package declares
		// nothing. The finding is exactly the warning that the recommendation
		// about to be printed should not be followed.
		if got.Verdict != VerdictRedundant {
			t.Errorf("Verdict = %v, want VerdictRedundant; a finding must not change the verdict (R4.5)", got.Verdict)
		}
		if low := strings.ToLower(out); !strings.Contains(low, "undeclared") {
			t.Errorf("report reports no undeclared divergence, yet the package is about to be recommended for removal.\n--- report ---\n%s", out)
		}
	})

	t.Run("identical content with no declaration yields no finding", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, silent)

		if got.Verified != VerifiedIdentical {
			t.Errorf("Verified = %v, want VerifiedIdentical", got.Verified)
		}
		if got.Verdict != VerdictRedundant {
			t.Errorf("Verdict = %v, want VerdictRedundant (now verified rather than merely derived)", got.Verdict)
		}
		assertNoFinding(t, out)
	})

	t.Run("differing content with a declaration yields no finding", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, declared)

		if got.Verified != VerifiedDiffers {
			t.Errorf("Verified = %v, want VerifiedDiffers", got.Verified)
		}
		if got.Verdict != VerdictKeep {
			t.Errorf("Verdict = %v, want VerdictKeep", got.Verdict)
		}
		assertNoFinding(t, out)
	})

	t.Run("a missing upstream ebuild leaves the declaration standing (R4.4)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
		// The version list says 1.0 exists upstream; the file system says
		// otherwise. Disagreeing with the provider is not this feature's job.
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, declared)

		if got.Verified != NotVerified {
			t.Errorf("Verified = %v, want NotVerified; absence of evidence is not evidence", got.Verified)
		}
		if got.Verdict != VerdictKeep {
			t.Errorf("Verdict = %v, want VerdictKeep; the declaration stands", got.Verdict)
		}
		assertNoFinding(t, out)
	})

	t.Run("different versions are never content-compared (R4.1)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "2.0", zedEbuildStock)
		prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"2.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, declared)

		if got.Status != StatusOutdated {
			t.Fatalf("Status = %v, want StatusOutdated (fixture check)", got.Status)
		}
		if got.Verified != NotVerified {
			t.Errorf("Verified = %v, want NotVerified; two different versions differ for reasons that say nothing about a patch", got.Verified)
		}
		if got.Verdict != VerdictNeedsRebase {
			t.Errorf("Verdict = %v, want VerdictNeedsRebase", got.Verdict)
		}
		assertNoFinding(t, out)
	})

	t.Run("a provider with no local root skips verification (R4.4)", func(t *testing.T) {
		overlayRoot := t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
		// fakeProvider is the API-shaped double: no LocalRoot, so no content.
		prov := &fakeProvider{versions: map[string][]string{"app-editors/zed": {"1.0"}}}

		got, out := verifyRun(t, overlayRoot, prov, pkg, declared)

		if got.Verified != NotVerified {
			t.Errorf("Verified = %v, want NotVerified; an API provider cannot supply content", got.Verified)
		}
		if got.Verdict != VerdictKeep {
			t.Errorf("Verdict = %v, want VerdictKeep", got.Verdict)
		}
		assertNoFinding(t, out)
	})
}

// assertNoFinding fails when the report speaks of either verification finding.
func assertNoFinding(t *testing.T, out string) {
	t.Helper()
	low := strings.ToLower(out)
	for _, word := range []string{"stale", "undeclared"} {
		if strings.Contains(low, word) {
			t.Errorf("report mentions %q but this case has no finding.\n--- report ---\n%s", word, out)
		}
	}
}
