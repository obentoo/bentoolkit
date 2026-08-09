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
