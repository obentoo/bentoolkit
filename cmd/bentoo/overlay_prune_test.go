package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// This file pins the command's plan-only half: `overlay prune` with no --apply
// prints what it would do and does nothing (R1.1, R1.3–R1.5, R2.6).
//
// "Removes nothing" is asserted twice on purpose, because the two assertions
// fail for different bugs. The overlay still holding its packages catches a
// removal that happened; the executor seam refusing to be called catches a
// removal that was ATTEMPTED — the difference between a command that is safe and
// one that is merely lucky about what the plan contained. The sweep's own design
// makes the same argument: the executor is unreachable without --apply, so R1.1
// holds by construction rather than by a well-placed if.
//
// The API-only case (R2.6) is where the command has to be most careful about
// tone. It is not an error: the operator asked a reasonable question with a
// provider that cannot answer it. So the command still runs, still prints a
// plan, and the plan is empty with a stated reason — which is also the second
// half of R1.5's distinction, because "nothing qualified" and "nothing was
// examined" send the operator to two completely different places.

// fakePruneGentoo is a ::gentoo provider whose tree is on disk — the shape
// `provider: local` and `--clone` both produce, and the one PruneVerification
// needs in order to compare anything at all.
type fakePruneGentoo struct {
	root     string
	versions map[string][]string // keyed by "category/pkg"
}

func (p *fakePruneGentoo) GetPackageVersions(category, pkg string) ([]string, error) {
	v, ok := p.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (p *fakePruneGentoo) GetName() string   { return "fake-gentoo" }
func (p *fakePruneGentoo) SupportsAPI() bool { return false }
func (p *fakePruneGentoo) Close() error      { return nil }

func (p *fakePruneGentoo) LocalPackagePath(category, pkg string) (string, error) {
	return filepath.Join(p.root, category, pkg), nil
}

// fakePruneAPIOnly is a provider with no tree on disk: it deliberately does NOT
// implement provider.PackageDirProvider, which is precisely the "API-only"
// signal R2.6 turns on.
type fakePruneAPIOnly struct {
	versions map[string][]string
}

func (p *fakePruneAPIOnly) GetPackageVersions(category, pkg string) ([]string, error) {
	v, ok := p.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (p *fakePruneAPIOnly) GetName() string   { return "fake-api" }
func (p *fakePruneAPIOnly) SupportsAPI() bool { return true }
func (p *fakePruneAPIOnly) Close() error      { return nil }

var (
	_ provider.Provider           = (*fakePruneGentoo)(nil)
	_ provider.PackageDirProvider = (*fakePruneGentoo)(nil)
	_ provider.Provider           = (*fakePruneAPIOnly)(nil)
)

const (
	prunedCat  = "app-editors"
	prunedPkg  = "zed"
	prunedAtom = prunedCat + "/" + prunedPkg
	prunedVer  = "1.0"

	pruneStockEbuild = "EAPI=8\nDESCRIPTION=\"zed editor\"\nSLOT=\"0\"\n"
	pruneOursEbuild  = "EAPI=8\nDESCRIPTION=\"zed editor\"\nSLOT=\"0\"\nPATCHES=( \"${FILESDIR}/w.patch\" )\n"
)

// writePruneCmdPackage lays down <root>/<category>/<pkg> with one ebuild plus the
// files a real package carries, and returns the directory.
func writePruneCmdPackage(t *testing.T, root, category, pkg, version, body string) string {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(pkg+"-"+version+".ebuild", body)
	write("Manifest", "DIST "+pkg+"-"+version+".tar.gz 1 BLAKE2B aa SHA512 aa\n")
	write("metadata.xml", "<pkgmetadata/>\n")
	write(filepath.Join("files", "w.patch"), "--- a/x\n+++ b/x\n")
	return dir
}

// prunePatchedReason is the declared reason every patched fixture entry carries,
// named once so a test can assert that the operator is shown the text the
// registry actually holds rather than a paraphrase of it.
const prunePatchedReason = "keeps our wayland patch"

// writePruneRegistryEntries writes a packages.toml holding one record per key.
// The keys are given whole so a test can carry several entries for one atom.
//
// `patched` is a STRING in the schema — the reason itself (config.go:116), not a
// bool with a separate reason field. It has to be written the way the real
// registry writes it: decoding is strict, so `patched = true` is a type error and
// an invented `patched_reason` key is an unknown-field error. Either one makes
// LoadPackagesConfig fail, which no longer degrades quietly — the command now
// gates on it — so a fixture with the wrong shape would silently test the
// unreadable-registry path while claiming to test a declared divergence.
func writePruneRegistryEntries(t *testing.T, overlayPath string, keys []string, patched bool) {
	t.Helper()
	dir := filepath.Join(overlayPath, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var b strings.Builder
	b.WriteString("# test registry\n\n")
	for _, key := range keys {
		b.WriteString("[\"" + key + "\"]\n")
		b.WriteString("url = \"https://example.invalid/releases\"\n")
		b.WriteString("parser = \"regex\"\n")
		b.WriteString("pattern = 'v([0-9.]+)'\n")
		if patched {
			b.WriteString("patched = \"" + prunePatchedReason + "\"\n")
		}
		b.WriteString("# END\n\n")
	}
	path := filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// overlayPruneFixture builds an overlay and a matching ::gentoo tree holding the same
// package at the same version, with the bodies given, plus a registry naming the
// keys. It returns the overlay root and a provider rooted at the gentoo tree.
func overlayPruneFixture(t *testing.T, ourBody, theirBody string, keys []string, patched bool) (overlayPath string, prov *fakePruneGentoo) {
	t.Helper()
	overlayPath = setupTestOverlay(t)
	writePruneCmdPackage(t, overlayPath, prunedCat, prunedPkg, prunedVer, ourBody)

	gentooRoot := t.TempDir()
	writePruneCmdPackage(t, gentooRoot, prunedCat, prunedPkg, prunedVer, theirBody)

	writePruneRegistryEntries(t, overlayPath, keys, patched)

	return overlayPath, &fakePruneGentoo{
		root:     gentooRoot,
		versions: map[string][]string{prunedAtom: {prunedVer}},
	}
}

// setOverlayPruneFlags pins the command's flag state for one test and restores it after.
func setOverlayPruneFlags(t *testing.T, apply, includePatched, keepRegistry, yes bool) {
	t.Helper()
	oApply, oPatched, oKeep, oYes := pruneApply, pruneIncludePatched, pruneKeepRegistry, pruneYes
	t.Cleanup(func() {
		pruneApply, pruneIncludePatched, pruneKeepRegistry, pruneYes = oApply, oPatched, oKeep, oYes
	})
	pruneApply, pruneIncludePatched, pruneKeepRegistry, pruneYes = apply, includePatched, keepRegistry, yes
}

// setOverlayPruneExecutor replaces the removal seam.
func setOverlayPruneExecutor(t *testing.T, fn func(overlay.PruneBatch, overlay.PruneOptions) []overlay.PruneResult) {
	t.Helper()
	orig := pruneExecutorFn
	t.Cleanup(func() { pruneExecutorFn = orig })
	pruneExecutorFn = fn
}

// setOverlayPruneConfirm replaces the confirmation seam.
func setOverlayPruneConfirm(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := confirmPruneFn
	t.Cleanup(func() { confirmPruneFn = orig })
	confirmPruneFn = fn
}

// forbidOverlayPruneRemoval wires both the executor and the confirmation to fail the
// test if they are reached at all.
func forbidOverlayPruneRemoval(t *testing.T) {
	t.Helper()
	setOverlayPruneExecutor(t, func(overlay.PruneBatch, overlay.PruneOptions) []overlay.PruneResult {
		t.Error("the executor ran on a plan-only invocation; R1.1 is supposed to hold because the executor is UNREACHABLE without --apply, not because the batch happened to be empty")
		return nil
	})
	setOverlayPruneConfirm(t, func(string) bool {
		t.Error("a confirmation was requested on a plan-only invocation")
		return false
	})
}

// TestPruneWithoutApplyRemovesNothing pins R1.1.
//
// The fixture is deliberately the case most at risk: identical content under a
// redundant verdict, i.e. a package the command WOULD remove under --apply. A
// plan-only run over a batch that was empty anyway proves nothing.
//
// _Requirements: R1.1_
func TestPruneWithoutApplyRemovesNothing(t *testing.T) {
	overlayPath, prov := overlayPruneFixture(t, pruneStockEbuild, pruneStockEbuild, []string{prunedAtom}, false)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, false, false, false, false)
	forbidOverlayPruneRemoval(t)

	pkgDir := filepath.Join(overlayPath, prunedCat, prunedPkg)
	registry := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	before, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	out := captureStdout(t, func() {
		_ = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, nil, &config.Config{})
		})
	})

	if _, err := os.Stat(pkgDir); err != nil {
		t.Errorf("%s was removed on a run without --apply: %v", pkgDir, err)
	}
	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry after: %v", err)
	}
	if string(after) != string(before) {
		t.Error("packages.toml changed on a run without --apply")
	}
	if !strings.Contains(out, prunedPkg) {
		t.Errorf("the plan does not name the package it would remove:\n%s", out)
	}
}

// TestPruneAPIOnlyProviderRefusesEverythingWithReason pins R2.6 and D6.
//
// An API-only provider has no tree, so no content exists to authorise anything —
// and the verdict alone must never be enough, which is the whole story. Getting
// the content over the API would cost one rate-limited request per package, ~300
// of them, which is why the answer is a refusal rather than a slower yes.
//
// It is NOT an error: the command still runs, still prints a plan, and exits 0.
// The reason has to name the way out, because "refused" without "re-run with
// --clone" is a dead end the operator has to guess their way out of.
//
// _Requirements: R2.6, R1.5_
func TestPruneAPIOnlyProviderRefusesEverythingWithReason(t *testing.T) {
	overlayPath := setupTestOverlay(t)
	pkgDir := writePruneCmdPackage(t, overlayPath, prunedCat, prunedPkg, prunedVer, pruneStockEbuild)
	writePruneRegistryEntries(t, overlayPath, []string{prunedAtom}, false)

	withFakeGentoo(t, &fakePruneAPIOnly{versions: map[string][]string{prunedAtom: {prunedVer}}})
	setOverlayPruneFlags(t, false, false, false, false)
	forbidOverlayPruneRemoval(t)

	var code int
	out := captureStdout(t, func() {
		code = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, nil, &config.Config{})
		})
	})

	if code > 0 {
		t.Errorf("exit code = %d, want 0 or no exit; an API-only run is a reasonable question the command cannot answer, not a failure", code)
	}
	if _, err := os.Stat(pkgDir); err != nil {
		t.Errorf("%s was removed: %v", pkgDir, err)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "--clone") && !strings.Contains(low, "local provider") {
		t.Errorf("the refusal does not name the way out; an operator told only 'refused' has to guess.\n%s", out)
	}
	if !strings.Contains(low, "tree") {
		t.Errorf("the refusal does not say that the missing thing is a local ::gentoo tree:\n%s", out)
	}
}

// TestPruneUnknownTargetFailsNamingIt pins R1.3.
//
// A typo in the target must fail loudly and name the argument. The failure mode
// it prevents is the quiet one: an unmatched restriction yields an empty plan,
// an empty plan prints "nothing to do", and the operator reads that as "the
// overlay is clean" when what actually happened is that they misspelled a
// category. The sweep learned the same lesson (its R1.4).
//
// _Requirements: R1.3_
func TestPruneUnknownTargetFailsNamingIt(t *testing.T) {
	const typo = "app-editrs/zed"

	overlayPath, prov := overlayPruneFixture(t, pruneStockEbuild, pruneStockEbuild, []string{prunedAtom}, false)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, false, false, false, false)
	forbidOverlayPruneRemoval(t)

	var code int
	out := captureStdout(t, func() {
		code = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, []string{typo}, &config.Config{})
		})
	})

	if code <= 0 {
		t.Errorf("exit code = %d, want non-zero; an unmatched target that exits 0 reads as 'nothing to clean'", code)
	}
	if !strings.Contains(out, typo) {
		t.Errorf("the error does not name the argument %q, so the operator cannot see the typo:\n%s", typo, out)
	}
}

// TestPrunePlanListsFilesAndRegistryKeys pins R1.4.
//
// Both halves of the removal are printed: the files that go from the overlay,
// and the registry entries that go with them. Manifest and metadata.xml appear
// even though the content comparison ignores them — they are not evidence about
// whether the copy is ours, but the removal certainly deletes them.
//
// Both registry keys must be listed. 90 of the registry's 321 atoms carry more
// than one entry, and a plan showing one of them understates what --apply would
// do to the file that publishes itself minutes later.
//
// _Requirements: R1.4, R5.2_
func TestPrunePlanListsFilesAndRegistryKeys(t *testing.T) {
	keys := []string{prunedAtom, prunedAtom + "@stable"}
	overlayPath, prov := overlayPruneFixture(t, pruneStockEbuild, pruneStockEbuild, keys, false)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, false, false, false, false)
	forbidOverlayPruneRemoval(t)

	out := captureStdout(t, func() {
		_ = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, nil, &config.Config{})
		})
	})

	for _, want := range []string{
		prunedPkg + "-" + prunedVer + ".ebuild",
		"Manifest",
		"metadata.xml",
		"w.patch",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not list %q among the files it would remove:\n%s", want, out)
		}
	}
	for _, want := range keys {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not list the registry entry %q that would be deleted with the package:\n%s", want, out)
		}
	}
}

// TestPruneUnreadableRegistryRefusesEverythingAndSaysSo pins both halves of what
// an unreadable registry must produce: total refusal, and a report that admits
// why.
//
// The refusal itself is structural and already holds. The divergence map is
// looked up with a comma-ok (compare.go:476) and a miss reaches deriveVerdict as
// known == false, which returns VerdictUnknown before any per-status rule runs
// (compare.go:185). A nil map misses on every atom, so every package is refused
// at the verdict gate and its content is never even compared (R2.5). The two
// assertions below that no package was planned or removed exist to keep it that
// way: a later edit that let an absent atom default to "known, not patched"
// would turn the whole overlay eligible in one line.
//
// Being right is not the same as being legible, and that is the rest of this
// test. Everything the operator sees on STDOUT is 'verdict is unknown, not
// redundant', once per package, with no cause — the one line naming the cause
// goes to stderr, so redirecting the report to a file loses it. Worse, the run
// then closes with "nothing qualified for removal", which is R1.5's OTHER half:
// it says the overlay is clean when what happened is that no verdict could be
// reached at all. An operator who reads that walks away from an overlay full of
// removable packages believing they were told something about it.
//
// The fixture is the dangerous case on purpose — identical content, a package a
// healthy run WOULD call redundant and remove — because a refusal proven over a
// batch that was empty anyway proves nothing.
//
// _Requirements: R1.5, R2.5_
func TestPruneUnreadableRegistryRefusesEverythingAndSaysSo(t *testing.T) {
	overlayPath := setupTestOverlay(t)
	pkgDir := writePruneCmdPackage(t, overlayPath, prunedCat, prunedPkg, prunedVer, pruneStockEbuild)

	gentooRoot := t.TempDir()
	writePruneCmdPackage(t, gentooRoot, prunedCat, prunedPkg, prunedVer, pruneStockEbuild)

	// No .autoupdate/packages.toml is written at all, so LoadPackagesConfig fails
	// with ErrPackagesConfigNotFound — the same failure a malformed file produces,
	// reached without depending on any particular parse error.
	withFakeGentoo(t, &fakePruneGentoo{
		root:     gentooRoot,
		versions: map[string][]string{prunedAtom: {prunedVer}},
	})
	setOverlayPruneFlags(t, false, false, false, false)
	forbidOverlayPruneRemoval(t)

	out := captureStdout(t, func() {
		_ = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, nil, &config.Config{})
		})
	})

	if strings.Contains(out, "would be removed") {
		t.Errorf("a package was planned for removal on a run that could not read the registry; without it no `patched` declaration can be honoured, so nothing may be authorised:\n%s", out)
	}
	if _, err := os.Stat(pkgDir); err != nil {
		t.Errorf("%s was removed: %v", pkgDir, err)
	}

	low := strings.ToLower(out)
	if !strings.Contains(low, "registry") {
		t.Errorf("the report does not name the REGISTRY as the cause on the stream the report itself is printed on; the operator sees only 'verdict is unknown' per package and cannot tell this apart from an overlay nobody has described yet:\n%s", out)
	}
	if strings.Contains(low, "qualified for removal") {
		t.Errorf("the run reports R1.5's 'nothing qualified' half, which claims every package WAS examined and found unremovable. No verdict was reached at all here, and an operator who reads 'nothing qualified' concludes the overlay is clean:\n%s", out)
	}
}

// TestPruneEmptyBatchDistinguishesNothingQualifiedFromNothingExamined pins R1.5.
//
// Two empty plans, two different situations, and conflating them wastes the
// operator's afternoon:
//
//   - Nothing QUALIFIED — every package was compared and none may be removed.
//     The overlay is doing its job; there is nothing to act on.
//   - Nothing was EXAMINED — no comparison happened at all. The overlay might be
//     full of removable packages and the run simply could not tell.
//
// The first says "you are done". The second says "run it again differently". A
// single "nothing to prune" says the first while meaning the second.
//
// "Nothing was examined" then has two shapes of its own, and they send the
// operator to two different places: the run COULD not look (no local ::gentoo
// tree — change the provider), or there was NOTHING to look at (the overlay
// holds no package — changing the provider would not help). Both are covered
// here, because a shared message that fits both fits neither.
//
// The exact wording is the implementer's; what is pinned is that the three runs
// do not print the same thing, and that each names its own situation.
//
// _Requirements: R1.5, R2.6_
func TestPruneEmptyBatchDistinguishesNothingQualifiedFromNothingExamined(t *testing.T) {
	run := func(t *testing.T, overlayPath string, prov provider.Provider) string {
		t.Helper()
		withFakeGentoo(t, prov)
		setOverlayPruneFlags(t, false, false, false, false)
		forbidOverlayPruneRemoval(t)
		return captureStdout(t, func() {
			_ = withExitIntercept(func() {
				runPrune(context.Background(), overlayPath, nil, &config.Config{})
			})
		})
	}

	var qualified, examined, empty string

	t.Run("nothing qualified", func(t *testing.T) {
		// The package is compared and found to carry a local change, so it is
		// refused. Something WAS examined; nothing came out of it.
		overlayPath, prov := overlayPruneFixture(t, pruneOursEbuild, pruneStockEbuild, []string{prunedAtom}, false)
		qualified = run(t, overlayPath, prov)

		if !strings.Contains(strings.ToLower(qualified), "qualif") {
			t.Errorf("the run does not say that nothing QUALIFIED, which is what actually happened:\n%s", qualified)
		}
	})

	t.Run("nothing was examined", func(t *testing.T) {
		overlayPath := setupTestOverlay(t)
		writePruneCmdPackage(t, overlayPath, prunedCat, prunedPkg, prunedVer, pruneStockEbuild)
		writePruneRegistryEntries(t, overlayPath, []string{prunedAtom}, false)
		examined = run(t, overlayPath, &fakePruneAPIOnly{versions: map[string][]string{prunedAtom: {prunedVer}}})

		if !strings.Contains(strings.ToLower(examined), "examin") {
			t.Errorf("the run does not say that nothing was EXAMINED; an operator reading 'nothing qualified' here would conclude the overlay is clean:\n%s", examined)
		}
	})

	t.Run("nothing was examined — the overlay holds no package", func(t *testing.T) {
		// The other shape of "nothing was examined": there was nothing to look
		// at. The run returns before the provider is ever resolved, so the
		// API-only arm's advice — point bentoo at an on-disk ::gentoo — would be
		// worse than silence here. Swapping the provider cannot conjure a package
		// that does not exist, and an operator who follows that instruction spends
		// the afternoon fixing something that was never broken.
		overlayPath := setupTestOverlay(t)

		withFakeGentoo(t, &fakePruneAPIOnly{})
		setOverlayPruneFlags(t, false, false, false, false)
		forbidOverlayPruneRemoval(t)

		var code int
		empty = captureStdout(t, func() {
			code = withExitIntercept(func() {
				runPrune(context.Background(), overlayPath, nil, &config.Config{})
			})
		})

		if code > 0 {
			t.Errorf("exit code = %d, want 0; an empty overlay is a correct answer to a reasonable question, not a failure", code)
		}
		low := strings.ToLower(empty)
		if !strings.Contains(low, "examin") {
			t.Errorf("an empty overlay does not say that nothing was EXAMINED:\n%s", empty)
		}
		if strings.Contains(low, "qualif") {
			t.Errorf("an empty overlay reports R1.5's 'nothing qualified' half, which claims packages WERE compared and found unremovable. There were none to compare:\n%s", empty)
		}
		if strings.Contains(low, "--clone") || strings.Contains(low, "provider") {
			t.Errorf("an empty overlay is sent to fix its provider. The provider is never resolved on this path, so that advice cannot change the outcome:\n%s", empty)
		}
	})

	if qualified != "" && examined != "" && qualified == examined {
		t.Errorf("both runs printed the same thing, so R1.5's distinction is not made:\n%s", qualified)
	}
	// The two "nothing was examined" runs must differ too: same half of R1.5,
	// different instruction to the operator.
	if examined != "" && empty != "" && examined == empty {
		t.Errorf("the no-local-tree run and the empty-overlay run printed the same thing, so the operator is given one instruction for two situations only one of it fits:\n%s", empty)
	}
}
