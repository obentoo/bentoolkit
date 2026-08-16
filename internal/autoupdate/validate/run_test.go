package validate

// Authored for story 031, sub-task 6.2 — R4, R4.1, R4.2, R4.3, R4.4, R5.4.
//
// Written from the contract: design.md fixes `Run(ctx, opts Options) (Report, error)`
// and the Error Handling table that says what every stopping condition becomes.
// The governing rule, asserted over and over below: a condition that stops the
// gate becomes a REPORTED OUTCOME WITH A REASON, never a silent pass and never
// an aborted run.
//
// This file pins the `Options` field names, which design.md left open:
// Overlay, Distdir, Selector.
//
// buildTarGz comes from archive_test.go; writeEbuild from ebuild_test.go.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// overlayWith lays out a minimal overlay holding one ebuild and returns its
// root. The Manifest names the distfile so the run can look for it.
func overlayWith(t *testing.T, atom, version, ebuild, distfile string) string {
	t.Helper()
	root := t.TempDir()
	parts := strings.SplitN(atom, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("atom %q is not category/package", atom)
	}
	dir := filepath.Join(root, parts[0], parts[1])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("laying out overlay: %v", err)
	}
	name := parts[1] + "-" + version + ".ebuild"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(ebuild), 0o644); err != nil {
		t.Fatalf("writing ebuild: %v", err)
	}
	manifest := "DIST " + distfile + " 100 BLAKE2B deadbeef SHA512 deadbeef\n"
	if err := os.WriteFile(filepath.Join(dir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}
	return root
}

func onlyResult(t *testing.T, r Report) EbuildResult {
	t.Helper()
	if len(r.Results) != 1 {
		t.Fatalf("Results: got %d, want 1: %+v", len(r.Results), r.Results)
	}
	return r.Results[0]
}

// gateOf returns the named gate's result, failing when the run never reported
// it. Absence is a failure and not a zero value on purpose: a gate that produced
// no GateResult at all has said nothing, and reading that as an empty outcome is
// the "clean report that means we did not look" this story removes.
func gateOf(t *testing.T, res EbuildResult, name string) GateResult {
	t.Helper()
	for _, gate := range res.Gates {
		if gate.Gate == name {
			return gate
		}
	}
	t.Fatalf("%s-%s reports no %q gate: %+v", res.Package, res.Version, name, res.Gates)
	return GateResult{}
}

// TestRun_AbsentDistdirSkipsWithAReason is R4.1 at the coarsest level: with no
// distdir at all, nothing can be read, and every ebuild says so.
func TestRun_AbsentDistdirSkipsWithAReason(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.29.2",
		"emesonargs=(\n\t-Daalib=disabled\n)\n", "gst-plugins-good-1.29.2.tar.xz")
	missing := filepath.Join(t.TempDir(), "no-distdir")

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: missing})
	if err != nil {
		t.Fatalf("Run: got error %v; a missing distdir is a reported outcome, not a failed run", err)
	}

	res := onlyResult(t, got)
	options := gateOf(t, res, GateOptions)
	if options.Outcome != OutcomeSkipped {
		t.Errorf("options gate outcome: got %q, want SKIPPED", options.Outcome)
	}
	if options.Reason == "" {
		t.Error("SKIPPED with no reason — the operator cannot tell this from a pass")
	}
}

// TestRun_AbsentDistfileNamesIt is R4.1 at the per-ebuild level.
func TestRun_AbsentDistfileNamesIt(t *testing.T) {
	const distfile = "gst-plugins-good-1.29.2.tar.xz"
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.29.2",
		"emesonargs=(\n\t-Daalib=disabled\n)\n", distfile)

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	res := onlyResult(t, got)
	options := gateOf(t, res, GateOptions)
	if options.Outcome != OutcomeSkipped {
		t.Fatalf("options gate outcome: got %q, want SKIPPED", options.Outcome)
	}
	if !strings.Contains(options.Reason, distfile) {
		t.Errorf("reason %q does not name the absent distfile %q", options.Reason, distfile)
	}
}

// TestRun_NonMesonBuildSystemNamesIt is R4.2. Out of scope is not the same as
// clean, and the report has to say which one it means.
func TestRun_NonMesonBuildSystemNamesIt(t *testing.T) {
	const distfile = "cmakeproj-1.0.tar.gz"
	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{"cmakeproj-1.0/CMakeLists.txt": "project(cmakeproj)\n"})
	linkInto(t, archive, filepath.Join(distdir, distfile))

	overlay := overlayWith(t, "dev-libs/cmakeproj", "1.0", "inherit cmake\n", distfile)

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	res := onlyResult(t, got)
	options := gateOf(t, res, GateOptions)
	if options.Outcome != OutcomeSkipped {
		t.Fatalf("options gate outcome: got %q, want SKIPPED", options.Outcome)
	}
	if options.Reason == "" {
		t.Fatal("SKIPPED with no reason")
	}
}

// TestRun_UnreadableEbuildSkipsWithTheReadError keeps a broken file from
// reading as a clean one.
func TestRun_UnreadableEbuildSkipsWithTheReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: every file is readable, so this assertion would pass for the wrong reason")
	}
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.29.2", "emesonargs=()\n", "x-1.tar.xz")
	ebuild := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if err := os.Chmod(ebuild, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ebuild, 0o644) })

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v; an unreadable ebuild is a reported outcome, not an aborted run", err)
	}

	res := onlyResult(t, got)
	options := gateOf(t, res, GateOptions)
	if options.Outcome != OutcomeSkipped || options.Reason == "" {
		t.Errorf("got outcome %q reason %q, want SKIPPED with a reason", options.Outcome, options.Reason)
	}
}

// TestRun_ReportsOnePerVersion is R5.4, and it is what lets the golden test
// show 1.28.6 and 1.29.2 side by side in one run.
func TestRun_ReportsOnePerVersion(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", "emesonargs=()\n", "a-1.28.6.tar.xz")
	dir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	if err := os.WriteFile(filepath.Join(dir, "gst-plugins-qt6-1.29.2.ebuild"), []byte("emesonargs=()\n"), 0o644); err != nil {
		t.Fatalf("writing second ebuild: %v", err)
	}

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(got.Results) != 2 {
		t.Fatalf("Results: got %d, want one per ebuild version: %+v", len(got.Results), got.Results)
	}
	seen := map[string]bool{}
	for _, r := range got.Results {
		seen[r.Version] = true
	}
	for _, want := range []string{"1.28.6", "1.29.2"} {
		if !seen[want] {
			t.Errorf("version %s missing from the report", want)
		}
	}
}

// TestRun_UnmatchedSelectorIsReported is R5.7 seen from the runner's side.
func TestRun_UnmatchedSelectorIsReported(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.29.2", "emesonargs=()\n", "a-1.tar.xz")

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: t.TempDir(), Selector: "dev-libs/nothing-here"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.UnmatchedSelector == "" {
		t.Fatal("a selector matching nothing left UnmatchedSelector empty; the command cannot then exit 2")
	}
	if got.ExitCode() != 2 {
		t.Errorf("ExitCode: got %d, want 2", got.ExitCode())
	}
}

// TestValidatePackage_ReachesNoNetwork is R4.4, and it is asserted structurally
// because behaviour cannot prove a negative: the package's own sources must not
// import anything that can open a socket. Without this, R4.4 is a requirement
// nothing checks — precisely the unverified green this whole story exists to
// remove.
func TestValidatePackage_ReachesNoNetwork(t *testing.T) {
	banned := map[string]bool{
		`"net"`:          true,
		`"net/http"`:     true,
		`"net/url"`:      false, // parsing a URL opens nothing
		`"crypto/tls"`:   true,
		`"net/rpc"`:      true,
		`"database/sql"`: true,
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing package sources: %v", err)
	}

	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				if banned[imp.Path.Value] {
					t.Errorf("%s imports %s; the option gate reads every input from the local filesystem (R4.4)", name, imp.Path.Value)
				}
			}
		}
	}
}

// linkInto puts src at dst, so a fixture archive can stand in for a distfile.
func linkInto(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %q: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("writing %q: %v", dst, err)
	}
}

// manifestNaming rewrites the package's Manifest so a case can control exactly
// which names it declares. It is a local detail of these four cases rather than
// a shared helper, because every other test in the package wants overlayWith's
// single-distfile Manifest.
func manifestNaming(t *testing.T, pkgDir string, names ...string) {
	t.Helper()
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("DIST " + n + " 100 BLAKE2B ab SHA512 cd\n")
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}
}

// TestRun_OnlyTheNewerDistfilePresentSkipsTheOlderEbuild is the observed bug,
// end to end through Run: the golden overlay holds both versions, the distdir
// holds only 1.29.2's archive, and 1.28.6 must decline to answer.
//
// SKIPPED and not FAILED is the whole point. FAILED here is a lie about an
// ebuild that is correct, and it is the kind of lie that gets a gate switched
// off.
func TestRun_OnlyTheNewerDistfilePresentSkipsTheOlderEbuild(t *testing.T) {
	overlay, distdir := goldenOverlay(t)
	older := filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz")
	if err := os.Remove(older); err != nil {
		t.Fatalf("removing the 1.28.6 distfile: %v", err)
	}

	got, err := Run(context.Background(), Options{Overlay: overlay, Distdir: distdir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Surface adjusted for sub-task 5.1's Gates model: this fragment was
	// authored against EbuildResult.Options/.Reason, which 5.1 replaced with
	// one GateResult per gate. Every assertion below is the one that was
	// authored; only the accessor moved.
	res := gateOf(t, resultFor(t, got, "1.28.6"), GateOptions)
	if res.Outcome == OutcomeFailed {
		t.Fatalf("1.28.6 reported FAILED with only the 1.29.2 archive on disk (reason %q); "+
			"the gate answered about the wrong tarball and blamed an ebuild that is correct", res.Reason)
	}
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("1.28.6 outcome: got %q, want SKIPPED — the archive belonging to this version is not present", res.Outcome)
	}
	if !strings.Contains(res.Reason, "gst-plugins-good-1.29.2.tar.gz") {
		t.Errorf("reason %q does not name the distfile that was declined; the operator cannot tell which archive was refused", res.Reason)
	}

	// The other half: 1.29.2 still has its own archive and must keep failing for
	// the real reason. A fix that turns everything into SKIPPED is not a fix.
	newer := gateOf(t, resultFor(t, got, "1.29.2"), GateOptions)
	if newer.Outcome != OutcomeFailed {
		t.Errorf("1.29.2 outcome: got %q, want FAILED — its own archive is present and declares neither aalib nor libcaca", newer.Outcome)
	}
}

// TestFindDistfile_SinglePresentCarryingTheVersionIsRead is R12.1: the
// shortcut is safe when the one file present is this ebuild's.
func TestFindDistfile_SinglePresentCarryingTheVersionIsRead(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild, "gst-plugins-good-1.28.6.tar.gz")
	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	manifestNaming(t, pkgDir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz")

	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{"gst-plugins-good-1.28.6/meson.build": "project('x')\n"})
	linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))

	got, err := findDistfile(pkgDir, distdir, "1.28.6")
	if err != nil {
		t.Fatalf("findDistfile: %v — the one distfile present carries this ebuild's version", err)
	}
	if filepath.Base(got) != "gst-plugins-good-1.28.6.tar.gz" {
		t.Errorf("read %q, want the 1.28.6 archive", got)
	}
}

// TestFindDistfile_SinglePresentOfAnotherVersionIsDeclined is R12.3, the unit
// view of the bug above.
func TestFindDistfile_SinglePresentOfAnotherVersionIsDeclined(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild, "gst-plugins-good-1.28.6.tar.gz")
	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	manifestNaming(t, pkgDir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz")

	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{"gst-plugins-good-1.29.2/meson.build": "project('x')\n"})
	linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.gz"))

	got, err := findDistfile(pkgDir, distdir, "1.28.6")

	if err == nil {
		t.Fatalf("findDistfile returned %q for version 1.28.6 with only the 1.29.2 archive present; "+
			"a versioned sibling in the Manifest means the single-present shortcut is a guess", got)
	}
	if !strings.Contains(err.Error(), "gst-plugins-good-1.29.2.tar.gz") {
		t.Errorf("the refusal %q does not name the distfile it declined to read", err)
	}
}

// TestFindDistfile_ASkipNamesTheDirectoryItSearched is story 035's R4.1.
//
// # Why this is worth a requirement of its own
//
// Story 035's defect read as a wrong-version refusal for as long as it did
// because no message said WHERE the search happened. "No archive here" and "I
// looked in the wrong place" produce the same SKIP, and only the directory name
// separates them. Naming it is what makes the next occurrence diagnosable from
// the log alone, without reproducing the run.
func TestFindDistfile_ASkipNamesTheDirectoryItSearched(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild, "gst-plugins-good-1.28.6.tar.gz")
	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")

	t.Run("nothing the Manifest names is present", func(t *testing.T) {
		manifestNaming(t, pkgDir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz")
		distdir := t.TempDir()

		_, err := findDistfile(pkgDir, distdir, "1.28.6")
		if err == nil {
			t.Fatal("an empty distdir produced no error")
		}
		if !strings.Contains(err.Error(), distdir) {
			t.Errorf("the skip %q does not name the directory it searched (%s); the operator cannot tell a host "+
				"that never fetched the release from a fetch that went somewhere else", err, distdir)
		}
	})

	t.Run("the Manifest names nothing at all", func(t *testing.T) {
		manifestNaming(t, pkgDir)
		distdir := t.TempDir()

		_, err := findDistfile(pkgDir, distdir, "1.28.6")
		if err == nil {
			t.Fatal("a Manifest naming no distfile produced no error")
		}
		if !strings.Contains(err.Error(), distdir) {
			t.Errorf("the skip %q does not name the directory (%s); with no directory in the message, "+
				"\"the Manifest is empty\" and \"I searched the wrong place\" are indistinguishable", err, distdir)
		}
	})

	// The wrong-version message already named the directory and must keep doing
	// so: it is the reason string story 035's reproduction was read FROM, and a
	// change to it would invalidate that transcript.
	t.Run("the wrong-version refusal keeps naming both", func(t *testing.T) {
		manifestNaming(t, pkgDir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz")
		distdir := t.TempDir()
		archive := buildTarGz(t, map[string]string{"gst-plugins-good-1.29.2/meson.build": "project('x')\n"})
		linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.gz"))

		_, err := findDistfile(pkgDir, distdir, "1.28.6")
		if err == nil {
			t.Fatal("the wrong-version archive was accepted")
		}
		for _, want := range []string{distdir, "gst-plugins-good-1.29.2.tar.gz", "does not belong to version 1.28.6"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q no longer contains %q", err, want)
			}
		}
	})
}

// TestFindDistfile_UnversionedManifestNamesKeepTheShortcut is R12.2, and it is
// the case that stops the fix from breaking snapshot packages: when NO distfile
// the Manifest names carries any version, there is nothing to compare against
// and the single present file is the only candidate there is.
func TestFindDistfile_UnversionedManifestNamesKeepTheShortcut(t *testing.T) {
	const snapshot = "deadbeefcafe1234.tar.gz"
	overlay := overlayWith(t, "dev-libs/snapshotpkg", "0_p20260809", "inherit meson\n", snapshot)
	pkgDir := filepath.Join(overlay, "dev-libs", "snapshotpkg")

	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{"snapshotpkg/meson.build": "project('x')\n"})
	linkInto(t, archive, filepath.Join(distdir, snapshot))

	got, err := findDistfile(pkgDir, distdir, "0_p20260809")
	if err != nil {
		t.Fatalf("findDistfile: %v — a commit-hash distfile carries no version, so there is nothing to disagree with", err)
	}
	if filepath.Base(got) != snapshot {
		t.Errorf("read %q, want %q", got, snapshot)
	}
}

// TestFindDistfile_SeveralPresentKeepTheirBehaviour is the Unchanged Behaviour
// guard: the two multi-present branches are not what this sub-task changes.
func TestFindDistfile_SeveralPresentKeepTheirBehaviour(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild, "gst-plugins-good-1.28.6.tar.gz")
	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	manifestNaming(t, pkgDir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz")

	distdir := t.TempDir()
	for _, name := range []string{"gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz"} {
		archive := buildTarGz(t, map[string]string{"x/meson.build": "project('x')\n"})
		linkInto(t, archive, filepath.Join(distdir, name))
	}

	got, err := findDistfile(pkgDir, distdir, "1.29.2")
	if err != nil {
		t.Fatalf("findDistfile with both present: %v — exactly one name carries 1.29.2", err)
	}
	if filepath.Base(got) != "gst-plugins-good-1.29.2.tar.gz" {
		t.Errorf("read %q, want the 1.29.2 archive", got)
	}

	// And none present is still a named refusal, not a silent pick.
	empty := t.TempDir()
	if _, err := findDistfile(pkgDir, empty, "1.29.2"); err == nil {
		t.Error("findDistfile answered from an empty distdir")
	}
}

// MERGE FRAGMENT — story 037, sub-task 1.1 (Options.DistNames + selection core).
//
// Target file: internal/autoupdate/validate/run_test.go (APPEND at the end).
// Do NOT repeat the `package validate` clause.
//
// IMPORTS: none added — context, os, path/filepath, strings and testing are
// already in the target's block.
//
// # Symbols
//
// Added: TestRun_CallerSuppliedNamesSelectTheArchiveUnderTheSameRules,
// TestRun_ASuppliedNameRefusalNamesTheDirectoryAndTheSource, and the helper
// seamOverlayWithoutManifest — prefixed so nothing collides with run_test.go's
// own helpers.
//
// Borrowed, never re-declared: overlayWith and gateOf (run_test.go),
// buildTarGz (archive_test.go), linkInto (run_test.go), goldenEbuild and
// resultFor (golden_test.go), onlyResult (run_test.go).
//
// # PINNED CONTRACT (design D2, D5, D6 — S037-R1.1, R1.4, R1.6)
//
//	Options.DistNames func(pkgDir string) ([]string, error)
//
// nil parses pkgDir/Manifest exactly as today — that half is already pinned by
// the TestFindDistfile_* suite and golden_test.go and is NOT re-asserted here.
// What is asserted is only the NEW behaviour: names supplied through the seam
// are selected under the SAME R12 rules the Manifest-parsed names get, a name
// carrying a path separator is refused by name without escaping the distdir,
// and every refusal names the directory searched AND the source the names came
// from ("supplied", D6's wording for the caller-side source).
//
// These tests go through Run, not through the extracted selection core: the
// core's name is not part of the contract, and pinning it would turn a
// refactoring seam into a public surface.

// seamOverlayWithoutManifest is overlayWith with the Manifest removed — the
// shape of a staged tree, which is the whole reason the seam exists: a package
// directory that has archives to answer for and no Manifest file to name them.
func seamOverlayWithoutManifest(t *testing.T, atom, version, ebuild string) string {
	t.Helper()
	root := overlayWith(t, atom, version, ebuild, "placeholder-0.tar.gz")
	parts := strings.SplitN(atom, "/", 2)
	if err := os.Remove(filepath.Join(root, parts[0], parts[1], "Manifest")); err != nil {
		t.Fatalf("removing the Manifest to shape the tree like a staged one: %v", err)
	}
	return root
}

// TestRun_CallerSuppliedNamesSelectTheArchiveUnderTheSameRules is R1.1: with
// no Manifest file anywhere, the seam's names feed the option gate, and the
// R12 selection rules still decide WHICH archive is read.
//
// The proof that selection happened is in the archives' contents: the 1.28.6
// archive declares everything the ebuild passes and the 1.29.2 one does not,
// so a PASS is only reachable by reading exactly the 1.28.6 archive. An
// implementation that took the first present name, or any present name, fails
// this with a confident FAILED — the false-FAILED findDistfile's own notes
// call the way a gate gets switched off.
func TestRun_CallerSuppliedNamesSelectTheArchiveUnderTheSameRules(t *testing.T) {
	overlay := seamOverlayWithoutManifest(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild)

	distdir := t.TempDir()
	older := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.28.6/meson.build":   "project('x')\n",
		"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
	})
	linkInto(t, older, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))
	newer := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.29.2/meson.build":   "project('x')\n",
		"gst-plugins-good-1.29.2/meson.options": "option('qt6')\n",
	})
	linkInto(t, newer, filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.gz"))

	supplied := []string{"gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz"}
	got, err := Run(context.Background(), Options{
		Overlay: overlay,
		Distdir: distdir,
		DistNames: func(string) ([]string, error) {
			return supplied, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	options := gateOf(t, onlyResult(t, got), GateOptions)
	if options.Outcome == OutcomeSkipped {
		t.Fatalf("the option gate SKIPPED (reason %q) although the caller supplied the names; "+
			"a staged tree without a Manifest is exactly what the seam exists to validate (R1.1)", options.Reason)
	}
	if options.Outcome != OutcomePass {
		t.Errorf("options gate: got %q (reason %q), want PASS — only the 1.28.6 archive declares what this "+
			"ebuild passes, so any other outcome means the selection rules were not applied to the supplied names",
			options.Outcome, options.Reason)
	}
}

// TestRun_ASuppliedNameRefusalNamesTheDirectoryAndTheSource is R1.4 and R1.6
// together, because they are one sentence in the report: a refusal that names
// what was declined, where the search happened, and whose names these were.
func TestRun_ASuppliedNameRefusalNamesTheDirectoryAndTheSource(t *testing.T) {
	overlay := seamOverlayWithoutManifest(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild)

	t.Run("only another version's archive is present", func(t *testing.T) {
		distdir := t.TempDir()
		newer := buildTarGz(t, map[string]string{"gst-plugins-good-1.29.2/meson.build": "project('x')\n"})
		linkInto(t, newer, filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.gz"))

		got, err := Run(context.Background(), Options{
			Overlay: overlay,
			Distdir: distdir,
			DistNames: func(string) ([]string, error) {
				return []string{"gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.29.2.tar.gz"}, nil
			},
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		options := gateOf(t, onlyResult(t, got), GateOptions)
		if options.Outcome != OutcomeSkipped {
			t.Fatalf("options gate: got %q, want SKIPPED — the only present archive belongs to another release, "+
				"and R12.3 applies to supplied names exactly as it applies to parsed ones", options.Outcome)
		}
		for _, want := range []string{distdir, "gst-plugins-good-1.29.2.tar.gz", "supplied"} {
			if !strings.Contains(options.Reason, want) {
				t.Errorf("the refusal %q does not contain %q; R1.6 requires the directory searched, the file "+
					"declined and the source of the names, all in the sentence the operator reads", options.Reason, want)
			}
		}
	})

	t.Run("a name carrying a path separator is refused without leaving the distdir", func(t *testing.T) {
		parent := t.TempDir()
		distdir := filepath.Join(parent, "dist")
		if err := os.Mkdir(distdir, 0o755); err != nil {
			t.Fatalf("creating the distdir: %v", err)
		}
		// A perfectly readable archive OUTSIDE the distdir, declaring everything
		// the ebuild passes. If the gate follows the traversal it reaches a PASS
		// — which is precisely how this assertion detects the escape (D5).
		escape := buildTarGz(t, map[string]string{
			"gst-plugins-good-1.28.6/meson.build":   "project('x')\n",
			"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
		})
		linkInto(t, escape, filepath.Join(parent, "evil-1.28.6.tar.gz"))

		const hostile = "../evil-1.28.6.tar.gz"
		got, err := Run(context.Background(), Options{
			Overlay: overlay,
			Distdir: distdir,
			DistNames: func(string) ([]string, error) {
				return []string{hostile}, nil
			},
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		options := gateOf(t, onlyResult(t, got), GateOptions)
		if options.Outcome != OutcomeSkipped {
			t.Fatalf("options gate: got %q (reason %q), want SKIPPED — a name carrying a path separator was "+
				"followed outside the distdir, which is the escape D5 exists to close (R1.4)", options.Outcome, options.Reason)
		}
		if !strings.Contains(options.Reason, hostile) {
			t.Errorf("the refusal %q does not name the hostile name %q it declined (R1.4)", options.Reason, hostile)
		}
		if !strings.Contains(options.Reason, distdir) {
			t.Errorf("the refusal %q does not name the directory searched, %s (R1.6)", options.Reason, distdir)
		}
	})
}

// MERGE FRAGMENT — story 037, sub-task 1.2 (empty-authoritative and
// producer-failure outcomes).
//
// Target file: internal/autoupdate/validate/run_test.go (APPEND immediately
// AFTER sub-task 1.1's fragment). Do NOT repeat the `package validate` clause.
//
// IMPORTS: "errors" joins the target's existing block (context, go/parser,
// go/token, os, path/filepath, strings, testing). One block, not two, and not
// left unused — the producer-failure case below is its only user.
//
// # Symbols
//
// Added: TestRun_EmptySuppliedNamesAreAuthoritativeOverTheManifest,
// TestRun_ANamesProducerFailureIsAReportedSkip. Borrowed: overlayWith, gateOf,
// onlyResult (run_test.go), buildTarGz (archive_test.go), linkInto
// (run_test.go), goldenEbuild (golden_test.go).
//
// # PINNED CONTRACT (design D6 — S037-R1.3, R1.5, R1.6)
//
// An empty-but-present seam result is AUTHORITATIVE: the option gate reports
// SKIPPED naming the source and does NOT fall back to parsing the package's
// Manifest file. A producer error becomes a SKIPPED naming what was attempted.
// Both refusals keep naming the directory searched (035's discipline).
//
// The fixture deliberately makes the silent-fallback failure mode VISIBLE: the
// Manifest file is present, names an archive that is on disk, and that archive
// declares everything the ebuild passes. Any fallback therefore produces a
// PASS — so a PASS here is the defect, not a near-miss.

// TestRun_EmptySuppliedNamesAreAuthoritativeOverTheManifest is R1.3. nil seam
// and empty result are different facts: nil means "parse the Manifest as
// today", empty means "the caller answered, and the answer is nothing".
func TestRun_EmptySuppliedNamesAreAuthoritativeOverTheManifest(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild,
		"gst-plugins-good-1.28.6.tar.gz")

	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.28.6/meson.build":   "project('x')\n",
		"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
	})
	linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))

	got, err := Run(context.Background(), Options{
		Overlay: overlay,
		Distdir: distdir,
		DistNames: func(string) ([]string, error) {
			return []string{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	options := gateOf(t, onlyResult(t, got), GateOptions)
	if options.Outcome == OutcomePass || options.Outcome == OutcomeFailed {
		t.Fatalf("the option gate answered %q although the caller supplied an EMPTY names list; the only way "+
			"to that answer is a silent fallback to the Manifest file, and D6 says empty is authoritative (R1.3)",
			options.Outcome)
	}
	if options.Outcome != OutcomeSkipped {
		t.Fatalf("options gate: got %q, want SKIPPED", options.Outcome)
	}
	for _, want := range []string{distdir, "supplied"} {
		if !strings.Contains(options.Reason, want) {
			t.Errorf("the skip %q does not contain %q; the operator has to be able to tell WHOSE empty answer "+
				"this was and where nothing was looked for (R1.3, R1.6)", options.Reason, want)
		}
	}
}

// TestRun_ANamesProducerFailureIsAReportedSkip is R1.5: the producer failing is
// a stopping condition like any other, and the governing rule applies — a
// reported outcome with a reason naming what was attempted, never a silent
// pass and never an aborted run.
func TestRun_ANamesProducerFailureIsAReportedSkip(t *testing.T) {
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild,
		"gst-plugins-good-1.28.6.tar.gz")

	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.28.6/meson.build":   "project('x')\n",
		"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
	})
	linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))

	got, err := Run(context.Background(), Options{
		Overlay: overlay,
		Distdir: distdir,
		DistNames: func(string) ([]string, error) {
			return nil, errors.New("deriving the bump's archive names: the registry entry is unreadable")
		},
	})
	if err != nil {
		t.Fatalf("Run: %v — a failing producer is one ebuild's reported outcome, not a failed run", err)
	}

	options := gateOf(t, onlyResult(t, got), GateOptions)
	if options.Outcome != OutcomeSkipped {
		t.Fatalf("options gate: got %q (reason %q), want SKIPPED — the producer failed, so nothing was read, "+
			"and anything but a skip claims a measurement that never happened (R1.5)", options.Outcome, options.Reason)
	}
	if !strings.Contains(options.Reason, "the registry entry is unreadable") {
		t.Errorf("the skip %q does not carry the producer's own error; without it the operator cannot tell "+
			"WHAT was attempted (R1.5)", options.Reason)
	}
	if !strings.Contains(options.Reason, distdir) {
		t.Errorf("the skip %q does not name the directory, %s; R1.6 covers every option-gate refusal, "+
			"this one included", options.Reason, distdir)
	}
}

// MERGE FRAGMENT — story 037, sub-task 2.1 (Options.StagedManifest +
// materialization).
//
// Target file: internal/autoupdate/validate/run_test.go (APPEND immediately
// AFTER sub-task 1.2's fragment). Do NOT repeat the `package validate` clause.
//
// IMPORTS: none added beyond what 1.2 already merged — context, os,
// path/filepath, strings, testing are in the target's block.
//
// # Symbols
//
// Added: seamStagedManifests, seamStagedCandidateDir (shared with sub-task
// 2.2's fragment — materialise 2.1 BEFORE 2.2), and the two tests. Borrowed:
// overlayWith, gateOf, onlyResult (run_test.go), buildTarGz (archive_test.go),
// linkInto (run_test.go), goldenEbuild, hashTree (golden_test.go).
//
// # PINNED CONTRACT (design D3 — S037-R2.1, R2.4, R2.5, R2.6)
//
//	Options.StagedManifest func(pkgDir string) ([]byte, error)
//
// nil → nothing travels (today, already pinned elsewhere). Non-nil, staged
// tree lacking a Manifest → the returned bytes are materialised at
// <staged>/Manifest, mode 0o600, inside the staged tree ONLY, before the build
// gates run; a write failure is a reported SKIP naming the staged path; a
// PRODUCER failure — the content could not be made at all, distinct from a
// write that failed (R2.6 vs R2.5) — is a reported SKIP carrying the
// producer's own words; the published overlay stays byte-identical
// throughout. The R2.6 case uses the "errors" import sub-task 1.2's fragment
// merges into this file — materialise 1.2 before 2.1, which the ordering
// above already requires.
//
// # Why PATH is stripped
//
// These cases assert the MATERIALISATION, not the build. With no `ebuild` on
// PATH the build gates report their own skip and spawn nothing, so the case is
// hermetic on every host — including a Gentoo host, where a real `ebuild`
// against a synthetic tree would otherwise be invoked. The contract still
// requires staging and materialisation to have happened by then: the gates'
// skip must be the GATES' answer, produced after the tree they would have
// built in was prepared.

// seamStagedManifests walks a staging root and returns every file named
// Manifest under it. A walk rather than a joined path, so the assertion cannot
// silently miss a layout change and pass by looking in the wrong place.
func seamStagedManifests(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == "Manifest" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// seamStagedCandidateDir returns the directory under root holding the named
// candidate ebuild, or "" when no tree was staged at all.
func seamStagedCandidateDir(root, ebuildName string) string {
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == ebuildName {
			found = filepath.Dir(path)
		}
		return nil
	})
	return found
}

// seamDepthFixture lays out the one-package overlay whose option gate PASSES —
// so the run genuinely reaches the build-depth path — and a distdir holding
// the matching archive. Returned: overlay root, distdir.
func seamDepthFixture(t *testing.T) (string, string) {
	t.Helper()
	overlay := overlayWith(t, "media-plugins/gst-plugins-qt6", "1.28.6", goldenEbuild,
		"gst-plugins-good-1.28.6.tar.gz")
	distdir := t.TempDir()
	archive := buildTarGz(t, map[string]string{
		"gst-plugins-good-1.28.6/meson.build":   "project('x')\n",
		"gst-plugins-good-1.28.6/meson.options": "option('qt6')\noption('aalib')\noption('libcaca')\n",
	})
	linkInto(t, archive, filepath.Join(distdir, "gst-plugins-good-1.28.6.tar.gz"))
	return overlay, distdir
}

// TestRun_SuppliedManifestIsMaterializedInsideTheStagedTree is R2.1 and R2.4:
// the caller's bytes land at <staged>/Manifest, 0o600, and NOTHING lands in
// the published overlay.
func TestRun_SuppliedManifestIsMaterializedInsideTheStagedTree(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	staging := t.TempDir()
	supplied := []byte("DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n")
	before := hashTree(t, overlay)

	_, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Distdir:     distdir,
		Depth:       "configure",
		StagingRoot: staging,
		StagedManifest: func(string) ([]byte, error) {
			return append([]byte(nil), supplied...), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifests := seamStagedManifests(t, staging)
	if len(manifests) != 1 {
		t.Fatalf("found %d Manifest files under the staging root, want exactly 1 — the supplied bytes were "+
			"not materialised into the staged tree (R2.1): %v", len(manifests), manifests)
	}
	body, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatalf("reading the materialised Manifest: %v", err)
	}
	if string(body) != string(supplied) {
		t.Errorf("the materialised Manifest is not the supplied bytes:\ngot  %q\nwant %q", body, supplied)
	}
	info, err := os.Stat(manifests[0])
	if err != nil {
		t.Fatalf("stat %s: %v", manifests[0], err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("materialised Manifest mode: got %04o, want 0600 — the staged tree's own stance is that "+
			"nothing outside this run reads it (stagedFileMode)", info.Mode().Perm())
	}

	if after := hashTree(t, overlay); after != before {
		t.Errorf("the run changed the published overlay: %s -> %s; materialisation writes only inside the "+
			"staged tree (R2.4)", before, after)
	}
}

// TestRun_AManifestWriteFailureIsAReportedSkipNamingTheStagedPath is R2.5.
//
// The failure is injected through the seam's own call: by the time the
// producer is asked, the staged tree exists (the contract consults the seam
// about "a staged tree lacking a Manifest", so staging comes first), and the
// producer seals the staged package directory read-only before answering. The
// write that follows must fail, and the build gates must say so — naming the
// staged path — rather than run against a tree whose Manifest silently never
// arrived.
func TestRun_AManifestWriteFailureIsAReportedSkipNamingTheStagedPath(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	staging := t.TempDir()
	var sealed []string
	t.Cleanup(func() {
		for _, dir := range sealed {
			_ = os.Chmod(dir, 0o755)
		}
	})

	got, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Distdir:     distdir,
		Depth:       "configure",
		StagingRoot: staging,
		StagedManifest: func(string) ([]byte, error) {
			dir := seamStagedCandidateDir(staging, "gst-plugins-qt6-1.28.6.ebuild")
			if dir == "" {
				t.Error("the Manifest producer was asked before any tree was staged; the seam answers about " +
					"a staged tree that lacks a Manifest, so staging must come first (D3)")
				return []byte("DIST x 1 BLAKE2B ab SHA512 cd\n"), nil
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Errorf("sealing the staged package directory: %v", err)
			} else {
				sealed = append(sealed, dir)
			}
			return []byte("DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n"), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v — a write failure is a reported outcome, not a failed run", err)
	}
	if len(sealed) == 0 {
		t.Fatal("the staged package directory was never sealed: either nothing was staged or the Manifest " +
			"producer was never consulted, and this case then proves nothing about a failing write (R2.5)")
	}

	configure := gateOf(t, onlyResult(t, got), GateConfigure)
	if configure.Outcome != OutcomeSkipped {
		t.Fatalf("configure gate: got %q (reason %q), want SKIPPED — the Manifest could not be written, so no "+
			"build phase had anything trustworthy to verify against (R2.5)", configure.Outcome, configure.Reason)
	}
	if !strings.Contains(configure.Reason, staging) {
		t.Errorf("the skip %q does not name the staged path it could not write (under %s); without it the "+
			"operator cannot tell a full disk from a sealed directory (R2.5)", configure.Reason, staging)
	}
}

// TestRun_AManifestProducerFailureIsAReportedSkipNamingTheAttempt is R2.6,
// the producer-side twin of the write failure above: the content could not be
// PRODUCED, so nothing may be materialised and the build gates must say what
// was attempted — in the producer's own words, because those words carry the
// identifiers the operator needs to reproduce the failure. Distinct from
// R2.5 on purpose: "the bytes could not be made" and "the bytes could not be
// written" are different next actions.
func TestRun_AManifestProducerFailureIsAReportedSkipNamingTheAttempt(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	staging := t.TempDir()
	before := hashTree(t, overlay)

	got, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Distdir:     distdir,
		Depth:       "configure",
		StagingRoot: staging,
		StagedManifest: func(string) ([]byte, error) {
			return nil, errors.New("generating the candidate's Manifest: pkgdev is not installed on this host")
		},
	})
	if err != nil {
		t.Fatalf("Run: %v — a failing producer is a reported outcome, not a failed run", err)
	}

	configure := gateOf(t, onlyResult(t, got), GateConfigure)
	if configure.Outcome != OutcomeSkipped {
		t.Fatalf("configure gate: got %q (reason %q), want SKIPPED — no Manifest content exists, so a build "+
			"phase could only die in setup and report a confident failure about nothing (R2.6)",
			configure.Outcome, configure.Reason)
	}
	if !strings.Contains(configure.Reason, "pkgdev is not installed on this host") {
		t.Errorf("the skip %q does not carry the producer's own error; without it the operator cannot tell "+
			"WHAT was attempted (R2.6)", configure.Reason)
	}

	if manifests := seamStagedManifests(t, staging); len(manifests) != 0 {
		t.Errorf("a Manifest was materialised although the producer failed: %v — there were no bytes to "+
			"write, so whatever this file holds is invented (R2.6)", manifests)
	}
	if after := hashTree(t, overlay); after != before {
		t.Errorf("the failing producer's run changed the published overlay: %s -> %s (R2.4)", before, after)
	}
}

// MERGE FRAGMENT — story 037, sub-task 2.2 (Run's depth path executes instead
// of skipping).
//
// Target file: internal/autoupdate/validate/run_test.go (APPEND immediately
// AFTER sub-task 2.1's fragment — this fragment BORROWS 2.1's helpers
// seamDepthFixture, seamStagedCandidateDir; materialise 2.1 first).
// Do NOT repeat the `package validate` clause.
//
// IMPORTS: none added.
//
// # PINNED CONTRACT (S037-R2.2, R2.3, R4.4)
//
// With a Manifest source supplied, Run at a depth above `options` goes
// Stage → materialise → RunBuildGates: the build gates' outcomes come from the
// gates themselves, not from noteBuildDepth's unconditional deferral to the
// apply path. With NO Manifest source (seam nil, staged tree without a file),
// the residual skip survives and names the condition.
//
// # What separates the new path from the old one, observably
//
// Today every build gate of a standalone run carries buildDepthNotRunReason's
// sentence, which ends by directing the operator to `bentoo overlay autoupdate
// --apply`. After this sub-task that sentence is gone from the seam-fed path:
// the gates RUN, and whatever stops them (here: no `ebuild` on a stripped
// PATH) is the gates' own reported reason. The staged tree's existence is the
// second observable: today the standalone runner stages nothing.

// TestRun_DepthAboveOptionsExecutesTheBuildGates is R2.2. PATH is stripped so
// the case is hermetic (see 2.1's fragment header): the gates stop at their
// own "no ebuild on this host" answer — but they are REACHED, over a tree that
// was really staged, which is exactly what today's run never does.
func TestRun_DepthAboveOptionsExecutesTheBuildGates(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)

	// A PATH holding `emerge` AND NOT `ebuild`, and the pair is the whole point
	// of this fixture.
	//
	// STRIPPING PATH ENTIRELY — what this test did first — does not reach the
	// build gates at all. unbuildableHereReason runs the dependency pre-check in
	// FRONT of RunBuildGates, that pre-check needs `emerge`, and without it every
	// gate comes back SKIPPED carrying "could not be determined". The old
	// assertions passed on that list: it does not contain the deferral sentence
	// either, so the test went green over a run that never executed a build gate
	// — the exact half of R2.2 it was written to pin.
	//
	// So `emerge` answers (exit 0, nothing listed, therefore nothing
	// unsatisfied), the run gets past the pre-check, and `ebuild`'s absence is
	// then RunBuildGates' OWN answer. That sentence is produced at exactly one
	// place in this package, which is what makes it usable as proof of arrival.
	t.Setenv("PATH", fakeBinDir(t, map[string]string{"emerge": "#!/bin/sh\nexit 0\n"}))

	staging := t.TempDir()
	got, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Distdir:     distdir,
		Depth:       "configure",
		StagingRoot: staging,
		StagedManifest: func(string) ([]byte, error) {
			return []byte("DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n"), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if dir := seamStagedCandidateDir(staging, "gst-plugins-qt6-1.28.6.ebuild"); dir == "" {
		t.Error("no staged tree exists under the staging root after a configure-depth run with a Manifest " +
			"source; the depth path must Stage before it can build (R2.2)")
	}

	res := onlyResult(t, got)
	if res.DepthRequested != "configure" {
		t.Errorf("DepthRequested = %q, want %q", res.DepthRequested, "configure")
	}
	for _, gate := range []string{GatePatches, GateConfigure} {
		g := gateOf(t, res, gate)
		if strings.Contains(g.Reason, "overlay autoupdate --apply") {
			t.Errorf("the %s gate still carries the old deferral sentence (%q); with a Manifest source "+
				"supplied, the depth path executes the gates instead of sending the operator to the apply "+
				"command (R2.2)", gate, g.Reason)
		}
		// The negative and the positive are BOTH asserted, because the negative
		// alone is what let this test pass without executing anything.
		if strings.Contains(g.Reason, "could not be determined") {
			t.Errorf("the %s gate carries the dependency PRE-CHECK's answer (%q); that check runs in front of "+
				"RunBuildGates, so a run stopping there executed no build gate at all (R2.2)", gate, g.Reason)
		}
		if !strings.Contains(g.Reason, "ebuild was not found on PATH") {
			t.Errorf("the %s gate does not carry RunBuildGates' own answer (%q); this fixture puts `emerge` on "+
				"PATH and leaves `ebuild` off it precisely so that arriving inside RunBuildGates is the only "+
				"way to produce that sentence (R2.2)", gate, g.Reason)
		}
	}
}

// fakeBinDir writes one executable per entry into a fresh directory and returns
// it, to be used as the WHOLE of PATH. It is how a hermetic test decides which
// external binaries a run can find — the lookups themselves are seams inside the
// package, but a test driving Run only reaches them through the environment.
func fakeBinDir(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // a stub that must be executable is the point
			t.Fatalf("writing the fake %s: %v", name, err)
		}
	}
	return dir
}

// TestRun_NoManifestSourceIsAReportedSkipNamingTheCondition is R2.3/R4.4, the
// residual skip: seam nil, staged tree without a Manifest — the build gates
// are SKIPPED with a reason naming the missing source.
//
// EXPECT THIS GREEN ON ARRIVAL at the outcome level: today's run also reports
// these gates SKIPPED with a Manifest-mentioning sentence, so this test is a
// REGRESSION PIN on the residual branch surviving the rewrite, not a Red. In
// Run mode, prove it by mutation: remove the residual branch (make the nil-seam
// depth path stage and run the gates anyway) and confirm this fails.
func TestRun_NoManifestSourceIsAReportedSkipNamingTheCondition(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	got, err := Run(context.Background(), Options{
		Overlay:     overlay,
		Distdir:     distdir,
		Depth:       "configure",
		StagingRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	configure := gateOf(t, onlyResult(t, got), GateConfigure)
	if configure.Outcome != OutcomeSkipped {
		t.Fatalf("configure gate: got %q (reason %q), want SKIPPED — with no Manifest source anywhere, a build "+
			"would die in Portage's setup phase and report a confident failure about a bump that may be fine (R2.3)",
			configure.Outcome, configure.Reason)
	}
	if configure.Reason == "" || !strings.Contains(configure.Reason, "Manifest") {
		t.Errorf("the skip %q does not name the missing Manifest source; \"skipped\" without the condition is "+
			"the silence this package exists to remove (R2.3, R4.4)", configure.Reason)
	}
}

// ---------------------------------------------------------------------------
// Story 037, review round 2 (2026-08-16). Both tests below pin a behaviour the
// seams CREATED rather than one they were designed to change: before the build
// gates could run at all, neither condition was reachable.
// ---------------------------------------------------------------------------

// TestUnbuildableHereReason_SeparatesTheHostFromTheEbuild is the divergence the
// review found: `ebuild` does no dependency resolution, so a host missing a
// DEPEND atom makes the phase start and die, and derive reads that as FAILED —
// a confident verdict about a candidate that may be perfectly fine, with exit 1
// behind it. The applier has asked this question since story 031; this entry
// point did not, so the same host answered differently depending on which
// command was run.
//
// The three cases are the three answers, and the difference between the last
// two is the operator's next action: unsatisfied names atoms to install,
// undetermined names no atom because none is known.
func TestUnbuildableHereReason_SeparatesTheHostFromTheEbuild(t *testing.T) {
	target := ebuildTarget{atom: "media-plugins/gst-plugins-qt6", version: "1.29.2"}

	t.Run("satisfied lets the gates run", func(t *testing.T) {
		reason := unbuildableHereReason(context.Background(), t.TempDir(), target,
			newDepsSeam(&depsSeam{}, emergeOnlyTheBump, false))
		if reason != "" {
			t.Errorf("a host that holds every dependency stopped the build gates: %q", reason)
		}
	})

	t.Run("unsatisfied names the atoms to install", func(t *testing.T) {
		reason := unbuildableHereReason(context.Background(), t.TempDir(), target,
			newDepsSeam(&depsSeam{}, emergeWithMissingDeps, false))
		if reason == "" {
			t.Fatal("a host missing two dependencies let the build gates run; the phase would die and the " +
				"ebuild would be reported FAILED for something only this machine is missing")
		}
		for _, want := range []string{"media-libs/gst-plugins-base", "dev-qt/qtbase", "install "} {
			if !strings.Contains(reason, want) {
				t.Errorf("the skip %q does not carry %q; without the atoms it is not an instruction", reason, want)
			}
		}
	})

	t.Run("undetermined names no atom", func(t *testing.T) {
		reason := unbuildableHereReason(context.Background(), t.TempDir(), target,
			newDepsSeam(&depsSeam{}, "", true))
		if reason == "" {
			t.Fatal("a probe that could not answer let the build gates run")
		}
		if !strings.Contains(reason, "could not be determined") {
			t.Errorf("the skip %q does not say the question went unanswered", reason)
		}
		if strings.Contains(reason, "install ") {
			t.Errorf("the skip %q instructs the operator to install something, but nothing is known to be "+
				"missing — the probe failed (mirrors R6.2)", reason)
		}
	})
}

// TestRun_ACancelledRunIsAnErrorAndNotAQuietPass covers what the Report shape
// cannot express on its own.
//
// ExitCode reads error-severity findings, and an interrupted gate has none to
// give. So a sweep reported purely as SKIPPED lines exits 0 — at the shell, a
// SIGTERM'd `--depth=compile` is indistinguishable from a clean run that found
// nothing wrong, and anything scripting this reads a killed run as a pass. The
// remaining packages are still listed, so the partial report says WHICH ones
// went unexamined; the error is what stops it being read as a verdict.
func TestRun_ACancelledRunIsAnErrorAndNotAQuietPass(t *testing.T) {
	overlay, distdir := seamDepthFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := Run(ctx, Options{
		Overlay:        overlay,
		Distdir:        distdir,
		Depth:          "configure",
		StagingRoot:    t.TempDir(),
		StagedManifest: func(string) ([]byte, error) { return []byte("DIST x 1 BLAKE2B ab SHA512 cd\n"), nil },
	})

	if err == nil {
		t.Fatalf("an interrupted run returned no error; ExitCode()=%d, and an interrupted gate carries no error "+
			"finding, so this renders as a clean pass at the shell", got.ExitCode())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error does not wrap the context cause: %v", err)
	}

	// The package is still listed and still says why, so the partial report
	// names what went unexamined instead of silently being short.
	res := onlyResult(t, got)
	configure := gateOf(t, res, GateConfigure)
	if configure.Outcome != OutcomeSkipped {
		t.Fatalf("configure gate: got %q, want SKIPPED", configure.Outcome)
	}
	if !strings.Contains(configure.Reason, "interrupted") {
		t.Errorf("the gate reason %q does not say the run was stopped", configure.Reason)
	}
	if res.DepthReason == "" {
		t.Error("an interrupted package reports no depth_reason, so a reader of the JSON who never opens the " +
			"gate list cannot tell an interruption from a package that was examined")
	}
}
