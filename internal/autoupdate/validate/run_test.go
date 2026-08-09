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
