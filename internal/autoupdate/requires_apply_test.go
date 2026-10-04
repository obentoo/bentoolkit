package autoupdate

// Authored for story 079, sub-tasks 4.1 (R4.1, R4.2, R4.5, R4.6) and 4.2
// (R5.1, R5.2, R5.4, R5.5, R5.6, R5.7).
//
// Written from the contract, not from an implementation: design.md
// "Requirement gate" (ApplyResult.Waiting, refusal reported like Held — Success
// false, Error nil, pending kept), "Atom rewrite" (ErrRequirementPinNotFound,
// captured version re-checked before it reaches bash source) and "Pending
// entry" (PendingUpdate.Requires, atom -> captured version).
//
// The flutter/dart pairing is the story's motivating case: flutter's ebuild pins
// ~dev-lang/dart-<dart_sdk_version>, and the bump must point that pin at the
// version captured at check time.
//
// Hostile halves come first in every table (identity of "the required package"):
// a package whose NAME extends the required one (dev-lang/dart-sass) and a
// prerelease of the very version must not satisfy the requirement, while a
// revision of it, and a copy that lives only in ::gentoo, must.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const (
	requiresApplyPkg     = "dev-lang/flutter"
	requiresApplyAtom    = "dev-lang/dart"
	requiresApplyOld     = "3.47.0"
	requiresApplyNew     = "3.48.0"
	requiresApplyDartNew = "3.14.0"
	requiresApplyWaiting = "~dev-lang/dart-3.14.0"
)

// requiresApplyEbuild is flutter's ebuild as the overlay holds it before the
// bump: one pinned atom of the required package, one minimum bound of it, a
// package whose name extends it, and a comment that mentions the pin.
const requiresApplyEbuild = `# Copyright 2026 Bentoo
EAPI=8

DESCRIPTION="Flutter SDK"
HOMEPAGE="https://flutter.dev"
SRC_URI=""
LICENSE="BSD"
SLOT="0"
KEYWORDS="~amd64"

# flutter-3.47.0 bundled ~dev-lang/dart-3.13.5
RDEPEND="
	~dev-lang/dart-3.13.5
	>=dev-lang/dart-3.0
	~dev-lang/dart-sass-1.80.0
"
`

// execRecorder is the exec seam: every child succeeds ("true"), and every call
// is counted, so a refusal can be shown to have run nothing at all.
type execRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *execRecorder) command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	r.mu.Lock()
	r.calls = append(r.calls, name+" "+strings.Join(arg, " "))
	r.mu.Unlock()
	return exec.CommandContext(ctx, "true")
}

func (r *execRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// requiresApplyFixture is one flutter bump: the overlay, an empty ::gentoo
// tree, a pending list and the record. Everything lives under t.TempDir().
type requiresApplyFixture struct {
	tmp      string
	overlay  string
	gentoo   string
	config   string
	staging  string // "" unless the fixture was asked to stage
	pending  *PendingList
	exec     *execRecorder
	cfg      PackageConfig
	ebuild   string
	oldBytes []byte
}

type requiresApplyOptions struct {
	ebuild   string            // flutter's current ebuild; requiresApplyEbuild when ""
	requires map[string]string // PendingUpdate.Requires
	noSpec   bool              // the record declares no requires at all
	staged   bool              // configure a staging root
}

func newRequiresApplyFixture(t *testing.T, opt requiresApplyOptions) *requiresApplyFixture {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	f := &requiresApplyFixture{
		tmp:     tmp,
		overlay: filepath.Join(tmp, "overlay"),
		gentoo:  filepath.Join(tmp, "gentoo"),
		config:  filepath.Join(tmp, "config"),
		exec:    &execRecorder{},
		ebuild:  opt.ebuild,
	}
	if f.ebuild == "" {
		f.ebuild = requiresApplyEbuild
	}
	if opt.staged {
		f.staging = filepath.Join(tmp, "staging")
	}
	if err := os.MkdirAll(f.gentoo, 0o755); err != nil {
		t.Fatalf("mkdir gentoo: %v", err)
	}
	createTestEbuildFileWithContent(t, f.overlay, requiresApplyPkg, requiresApplyOld, f.ebuild)
	f.oldBytes = []byte(f.ebuild)

	f.cfg = PackageConfig{URL: "https://example.com/releases_linux.json", Parser: "json", Path: "version"}
	if !opt.noSpec {
		f.cfg.Requires = map[string]RequireSpec{
			requiresApplyAtom: {Pattern: `"version": "{version}",\s+"dart_sdk_version": "([^"]+)"`, Pin: "~"},
		}
	}

	pending, err := NewPendingList(f.config)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package:        requiresApplyPkg,
		CurrentVersion: requiresApplyOld,
		NewVersion:     requiresApplyNew,
		Status:         StatusPending,
		Requires:       opt.requires,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}
	f.pending = pending
	return f
}

// place writes an ebuild of pkg at version into root (the overlay or ::gentoo).
func (f *requiresApplyFixture) place(t *testing.T, root, pkg, version string) {
	t.Helper()
	createTestEbuildFile(t, root, pkg, version)
}

func (f *requiresApplyFixture) applier(t *testing.T, extra ...ApplierOption) *Applier {
	t.Helper()
	opts := []ApplierOption{
		WithApplierPendingList(f.pending),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{requiresApplyPkg: f.cfg}}),
		WithApplierGentooPath(f.gentoo),
		WithExecCommand(f.exec.command),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	}
	if f.staging != "" {
		opts = append(opts, WithApplierStagingRoot(f.staging), WithApplierDepth(validate.DepthOptions))
	}
	a, err := NewApplier(f.overlay, f.config, append(opts, extra...)...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a
}

func (f *requiresApplyFixture) newEbuildPath() string {
	return filepath.Join(f.overlay, "dev-lang", "flutter", "flutter-"+requiresApplyNew+".ebuild")
}

// findFiles returns every regular file named name anywhere under root.
func requiresFindFiles(t *testing.T, root, name string) []string {
	t.Helper()
	var found []string
	if root == "" {
		return nil
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() && info.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// assertNothingWritten is R4.1's "write no file": no candidate in the overlay,
// none in a staged tree, the current ebuild byte-identical, and no child run.
func (f *requiresApplyFixture) assertNothingWritten(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.newEbuildPath()); err == nil {
		t.Errorf("a refused bump left %s in the overlay", f.newEbuildPath())
	}
	if staged := requiresFindFiles(t, f.staging, "flutter-"+requiresApplyNew+".ebuild"); len(staged) > 0 {
		t.Errorf("a refused bump staged a candidate: %v", staged)
	}
	got, err := os.ReadFile(filepath.Join(f.overlay, "dev-lang", "flutter", "flutter-"+requiresApplyOld+".ebuild"))
	if err != nil {
		t.Fatalf("reading the current ebuild: %v", err)
	}
	if string(got) != string(f.oldBytes) {
		t.Errorf("a refused bump changed the current ebuild\nbefore:\n%s\nafter:\n%s", f.oldBytes, got)
	}
	if calls := f.exec.snapshot(); len(calls) > 0 {
		t.Errorf("a refused bump ran %d child process(es): %v", len(calls), calls)
	}
}

// assertPendingKept: the entry survives a refusal with its status and its
// captured requirements unchanged, so the next run can apply it.
func (f *requiresApplyFixture) assertPendingKept(t *testing.T, wantRequires map[string]string) {
	t.Helper()
	got, ok := f.pending.Get(requiresApplyPkg)
	if !ok {
		t.Fatalf("the pending entry of %s was removed by a refusal; it must be kept", requiresApplyPkg)
	}
	if got.Status != StatusPending {
		t.Errorf("pending status = %q after a refusal, want %q (waiting is not a failure)", got.Status, StatusPending)
	}
	if got.NewVersion != requiresApplyNew {
		t.Errorf("pending NewVersion = %q, want %q", got.NewVersion, requiresApplyNew)
	}
	if fmt.Sprint(got.Requires) != fmt.Sprint(wantRequires) {
		t.Errorf("pending Requires = %v, want %v kept", got.Requires, wantRequires)
	}
}

// assertWaiting is the refusal shape the design fixes: Success false, Error
// nil, nothing returned as an error, Waiting naming what is missing.
func assertWaiting(t *testing.T, result *ApplyResult, err error, wantSubstring string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Apply returned error %v; an unmet requirement is a refusal, not a failure", err)
	}
	if result == nil {
		t.Fatal("Apply returned a nil result")
	}
	if result.Success {
		t.Errorf("Success = true for a bump whose requirement is unmet")
	}
	if result.Error != nil {
		t.Errorf("result.Error = %v, want nil (waiting is reported like Held)", result.Error)
	}
	if result.Held || result.Obsolete {
		t.Errorf("Held=%v Obsolete=%v; a waiting bump is neither", result.Held, result.Obsolete)
	}
	for _, w := range result.Waiting {
		if strings.Contains(w, wantSubstring) {
			return
		}
	}
	t.Errorf("Waiting = %q, want an entry containing %q", result.Waiting, wantSubstring)
}

// TestRequiresApplyWaitsWhileRequirementUnmet — R4.1, R4.2.
//
// The hostile half first: each fixture holds something that LOOKS like
// dev-lang/dart-3.14.0 and is not it. A gate that matched by name prefix, by
// category-less name, or by version prefix would publish a flutter whose pin
// cannot be satisfied — the exact bug the story exists to stop.
func TestRequiresApplyWaitsWhileRequirementUnmet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *requiresApplyFixture)
	}{
		{"name extends the atom (dev-lang/dart-sass-3.14.0)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.overlay, "dev-lang/dart-sass", "3.14.0")
			f.place(t, f.overlay, requiresApplyAtom, "3.13.5")
		}},
		{"same name in another category (dev-util/dart-3.14.0)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.gentoo, "dev-util/dart", "3.14.0")
			f.place(t, f.overlay, requiresApplyAtom, "3.13.5")
		}},
		{"prerelease of the version (dart-3.14.0_rc1)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.overlay, requiresApplyAtom, "3.14.0_rc1")
		}},
		{"longer version (dart-3.14.0.1)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.gentoo, requiresApplyAtom, "3.14.0.1")
		}},
		{"only the old version anywhere", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.overlay, requiresApplyAtom, "3.13.5")
			f.place(t, f.gentoo, requiresApplyAtom, "3.13.5")
		}},
		{"no dart at all", func(*testing.T, *requiresApplyFixture) {}},
	}
	for _, staged := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("staged=%v/%s", staged, tc.name), func(t *testing.T) {
				requires := map[string]string{requiresApplyAtom: requiresApplyDartNew}
				f := newRequiresApplyFixture(t, requiresApplyOptions{requires: requires, staged: staged})
				tc.setup(t, f)

				result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)

				assertWaiting(t, result, err, requiresApplyWaiting)
				f.assertNothingWritten(t)
				f.assertPendingKept(t, requires)
			})
		}
	}
}

// TestRequiresApplyProceedsWhenRequirementMet — R4.2 and the ::gentoo fallback.
//
// The converse of the test above: inputs shaped differently from the plain
// overlay ebuild that still ARE dev-lang/dart-3.14.0 under a ~ pin. A gate that
// read only the overlay, or compared the full PVR, would refuse these forever.
func TestRequiresApplyProceedsWhenRequirementMet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *requiresApplyFixture)
	}{
		{"only ::gentoo holds it", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.gentoo, requiresApplyAtom, "3.14.0")
		}},
		{"a revision of it in ::gentoo (dart-3.14.0-r1)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.gentoo, requiresApplyAtom, "3.14.0-r1")
		}},
		{"a revision of it in the overlay (dart-3.14.0-r2)", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.overlay, requiresApplyAtom, "3.14.0-r2")
		}},
		{"the overlay holds it", func(t *testing.T, f *requiresApplyFixture) {
			f.place(t, f.overlay, requiresApplyAtom, "3.14.0")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequiresApplyFixture(t, requiresApplyOptions{requires: map[string]string{requiresApplyAtom: requiresApplyDartNew}})
			tc.setup(t, f)

			result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(result.Waiting) != 0 {
				t.Fatalf("Waiting = %q for a requirement that is met", result.Waiting)
			}
			if !result.Success {
				t.Fatalf("Success = false (Error: %v) for a bump whose requirement is met", result.Error)
			}
			if _, err := os.Stat(f.newEbuildPath()); err != nil {
				t.Errorf("the met bump wrote no %s: %v", f.newEbuildPath(), err)
			}
			if f.pending.Has(requiresApplyPkg) {
				t.Errorf("the pending entry survived a successful apply")
			}
		})
	}
}

// TestRequiresApplyWaitsWhenRequirementsNotCaptured — R4.5.
//
// The record declares requires; the pending entry does not carry the capture
// (written by the previous release, or captured for another atom). Absence is
// never "no requirement": the bump waits and tells the operator to re-run
// --check. Even a dart that WOULD satisfy any version is present, so a gate
// that guessed "met" from the overlay alone would wrongly let it through.
func TestRequiresApplyWaitsWhenRequirementsNotCaptured(t *testing.T) {
	cases := []struct {
		name     string
		requires map[string]string
	}{
		{"captured for another atom only", map[string]string{"dev-lang/dart-sass": "1.81.0"}},
		{"empty map", map[string]string{}},
		{"entry from the previous release (no requires)", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequiresApplyFixture(t, requiresApplyOptions{requires: tc.requires})
			f.place(t, f.overlay, requiresApplyAtom, "3.14.0")

			result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)

			assertWaiting(t, result, err, "--check")
			f.assertNothingWritten(t)
			got, ok := f.pending.Get(requiresApplyPkg)
			if !ok {
				t.Fatal("the pending entry was removed; it must be kept until --check captures the requirement")
			}
			if got.Status != StatusPending {
				t.Errorf("pending status = %q, want %q", got.Status, StatusPending)
			}
		})
	}
}

// TestRequiresApplyRecordWithoutRequiresIsNotGated — R4 converse.
//
// A record that declares no requires must apply exactly as before, whatever the
// pending entry carries: the gate keys on the RECORD, not on the entry.
func TestRequiresApplyRecordWithoutRequiresIsNotGated(t *testing.T) {
	f := newRequiresApplyFixture(t, requiresApplyOptions{noSpec: true, ebuild: strings.Replace(requiresApplyEbuild, "\t~dev-lang/dart-3.13.5\n", "", 1)})

	result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Waiting) != 0 || !result.Success {
		t.Fatalf("a record without requires was gated: Success=%v Waiting=%q Error=%v", result.Success, result.Waiting, result.Error)
	}
}

// TestRequiresApplyValidateReportsWaiting — R4.6.
//
// --check's Validate is the second writer of a candidate. With the requirement
// unmet it must report the bump as waiting — naming the unmet atom — instead of
// staging it and running gates over an ebuild that cannot be published.
func TestRequiresApplyValidateReportsWaiting(t *testing.T) {
	requires := map[string]string{requiresApplyAtom: requiresApplyDartNew}
	f := newRequiresApplyFixture(t, requiresApplyOptions{requires: requires, staged: true})
	f.place(t, f.overlay, "dev-lang/dart-sass", "3.14.0")
	f.place(t, f.overlay, requiresApplyAtom, "3.13.5")

	result := f.applier(t).Validate(t.Context(), requiresApplyPkg, validate.DepthNone)

	if !strings.Contains(fmt.Sprintf("%+v", result), requiresApplyWaiting) {
		t.Errorf("Validate's result does not name the unmet %s:\n%+v", requiresApplyWaiting, result)
	}
	for _, g := range result.Gates {
		if g.Outcome != validate.OutcomeSkipped {
			t.Errorf("gate %s reported %s for a bump that must not be validated (waiting on %s)", g.Gate, g.Outcome, requiresApplyWaiting)
		}
	}
	f.assertNothingWritten(t)
	f.assertPendingKept(t, requires)
}

// TestRequiresApplyRewritesPinnedAtom — R5.1, R5.2, R5.4, R5.7 on both writers
// of an applied bump (the overlay path and the staged path).
//
// The expected ebuild is the old one with exactly one line changed. Asserting
// the whole file is what makes the hostile neighbours count: the minimum bound
// (another operator), dart-sass (a third atom whose name begins with the
// required one) and the comment that quotes the old pin must all survive.
func TestRequiresApplyRewritesPinnedAtom(t *testing.T) {
	want := strings.Replace(requiresApplyEbuild, "\t~dev-lang/dart-3.13.5\n", "\t~dev-lang/dart-3.14.0\n", 1)
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%v", staged), func(t *testing.T) {
			f := newRequiresApplyFixture(t, requiresApplyOptions{
				requires: map[string]string{requiresApplyAtom: requiresApplyDartNew},
				staged:   staged,
			})
			f.place(t, f.overlay, requiresApplyAtom, "3.14.0")

			result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !result.Success {
				t.Fatalf("Success = false: Error=%v Waiting=%q", result.Error, result.Waiting)
			}
			got, err := os.ReadFile(f.newEbuildPath())
			if err != nil {
				t.Fatalf("reading the new ebuild: %v", err)
			}
			assertRequiresEbuild(t, string(got), want)
		})
	}
}

// TestRequiresApplyValidateStagesRewrittenPin — R5.7: the tree --check stages
// carries the pin already rewritten, so the gates prove the ebuild that would
// be published.
func TestRequiresApplyValidateStagesRewrittenPin(t *testing.T) {
	want := strings.Replace(requiresApplyEbuild, "\t~dev-lang/dart-3.13.5\n", "\t~dev-lang/dart-3.14.0\n", 1)
	f := newRequiresApplyFixture(t, requiresApplyOptions{
		requires: map[string]string{requiresApplyAtom: requiresApplyDartNew},
		staged:   true,
	})
	f.place(t, f.gentoo, requiresApplyAtom, "3.14.0")

	result := f.applier(t).Validate(t.Context(), requiresApplyPkg, validate.DepthNone)

	staged := requiresFindFiles(t, f.staging, "flutter-"+requiresApplyNew+".ebuild")
	if len(staged) != 1 {
		t.Fatalf("staged candidates = %v, want exactly one (result: %+v)", staged, result)
	}
	got, err := os.ReadFile(staged[0])
	if err != nil {
		t.Fatalf("reading the staged ebuild: %v", err)
	}
	assertRequiresEbuild(t, string(got), want)
	if _, err := os.Stat(f.newEbuildPath()); err == nil {
		t.Errorf("Validate wrote %s into the published overlay", f.newEbuildPath())
	}
}

func assertRequiresEbuild(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	for _, line := range []struct{ text, why string }{
		{"\t~dev-lang/dart-3.14.0\n", "the pinned atom must point at the captured version (R5.1)"},
		{"\t>=dev-lang/dart-3.0\n", "an atom with another operator must stay unchanged (R5.2)"},
		{"\t~dev-lang/dart-sass-1.80.0\n", "a package whose name extends the atom is not the atom"},
		{"# flutter-3.47.0 bundled ~dev-lang/dart-3.13.5\n", "a commented line must stay unchanged (R5.4)"},
	} {
		if !strings.Contains(got, line.text) {
			t.Errorf("missing line %q: %s", line.text, line.why)
		}
	}
	if strings.Contains(got, "\t~dev-lang/dart-3.13.5\n") {
		t.Errorf("the old pin ~dev-lang/dart-3.13.5 is still in the dependency list")
	}
	t.Errorf("new ebuild differs from the expected rewrite\n--- got ---\n%s\n--- want ---\n%s", got, want)
}

// TestRequiresApplyFailsWhenPinNotFound — R5.5.
//
// The record pins ~dev-lang/dart but the ebuild holds no such atom: the record
// and the ebuild disagree, so the bump FAILS (it does not wait, and it does not
// silently publish an unpinned ebuild). Hostile first: ebuilds that contain the
// right characters in the wrong place.
func TestRequiresApplyFailsWhenPinNotFound(t *testing.T) {
	const head = "EAPI=8\nDESCRIPTION=\"Flutter SDK\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	cases := []struct {
		name   string
		ebuild string
	}{
		{"the ~ atom only in a comment", head + "# was ~dev-lang/dart-3.13.5\nRDEPEND=\">=dev-lang/dart-3.0\"\n"},
		{"~ only on a package whose name extends the atom", head + "RDEPEND=\"~dev-lang/dart-sass-1.80.0 >=dev-lang/dart-3.0\"\n"},
		{"the required package with another operator only", head + "RDEPEND=\">=dev-lang/dart-3.0\"\n"},
	}
	for _, staged := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("staged=%v/%s", staged, tc.name), func(t *testing.T) {
				f := newRequiresApplyFixture(t, requiresApplyOptions{
					ebuild:   tc.ebuild,
					requires: map[string]string{requiresApplyAtom: requiresApplyDartNew},
					staged:   staged,
				})
				f.place(t, f.overlay, requiresApplyAtom, "3.14.0")

				result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)

				if result == nil {
					t.Fatalf("Apply returned a nil result (err %v)", err)
				}
				failure := err
				if failure == nil {
					failure = result.Error
				}
				if !errors.Is(failure, ErrRequirementPinNotFound) {
					t.Fatalf("Apply error = %v (Success=%v Waiting=%q), want it to wrap ErrRequirementPinNotFound",
						failure, result.Success, result.Waiting)
				}
				if msg := failure.Error(); !strings.Contains(msg, "~") || !strings.Contains(msg, requiresApplyAtom) {
					t.Errorf("error %q does not name the operator ~ and the atom %s", msg, requiresApplyAtom)
				}
				if result.Success {
					t.Errorf("Success = true for a bump whose pin was not found")
				}
				if _, err := os.Stat(f.newEbuildPath()); err == nil {
					t.Errorf("a failed bump left %s in the overlay", f.newEbuildPath())
				}
				if got, ok := f.pending.Get(requiresApplyPkg); !ok || got.Status != StatusFailed {
					t.Errorf("pending entry after a failed bump = %+v (found %v), want it kept with status %q", got, ok, StatusFailed)
				}
			})
		}
	}
}

// TestRequiresApplyRefusesInvalidCapturedVersion — R5.6.
//
// pending.json is a file anyone with the account can edit, and the captured
// version is written into bash source. A value that is not a Gentoo version
// fails the bump before ANY file is written or any child is run — and it must
// fail, not merely wait: waiting would keep an injected value queued forever.
func TestRequiresApplyRefusesInvalidCapturedVersion(t *testing.T) {
	for _, value := range []string{
		"3.14;rm -rf /",
		"3.14.0\nrm -rf /",
		"$(id)",
		"3.14.0 ",
	} {
		for _, staged := range []bool{false, true} {
			t.Run(fmt.Sprintf("staged=%v/%q", staged, value), func(t *testing.T) {
				requires := map[string]string{requiresApplyAtom: value}
				f := newRequiresApplyFixture(t, requiresApplyOptions{requires: requires, staged: staged})
				f.place(t, f.overlay, requiresApplyAtom, "3.14.0")

				result, err := f.applier(t).Apply(t.Context(), requiresApplyPkg, false)

				if result == nil {
					t.Fatalf("Apply returned a nil result (err %v)", err)
				}
				if err == nil && result.Error == nil {
					t.Fatalf("Apply accepted captured version %q: Success=%v Waiting=%q", value, result.Success, result.Waiting)
				}
				if result.Success {
					t.Errorf("Success = true for captured version %q", value)
				}
				f.assertNothingWritten(t)
			})
		}
	}
}

// TestRequiresApplyValidateRefusesInvalidCapturedVersion — R5.6 under
// Validate (`--check`), the second writer of the candidate ebuild.
//
// The requirement is ALSO unmet here (no dart anywhere), which is the hostile
// half: a gate that ran before checkUpstreamValues would report the bump as
// waiting on dart and keep the injected value queued. The design orders the
// gate AFTER checkUpstreamValues on both paths, so Validate must report the
// refusal of the value itself — every gate SKIPPED, the reason naming the value
// quoted as checkUpstreamValues quotes it (%q) — and never the word "waiting".
func TestRequiresApplyValidateRefusesInvalidCapturedVersion(t *testing.T) {
	const value = "3.14;rm -rf /"
	requires := map[string]string{requiresApplyAtom: value}
	f := newRequiresApplyFixture(t, requiresApplyOptions{requires: requires, staged: true})
	// Look-alike only: dart-sass at the "version" must not count as dart.
	f.place(t, f.overlay, "dev-lang/dart-sass", "3.14.0")

	result := f.applier(t).Validate(t.Context(), requiresApplyPkg, validate.DepthNone)

	if len(result.Gates) == 0 {
		t.Fatalf("Validate returned no gate at all for an invalid captured version: %+v", result)
	}
	quoted := fmt.Sprintf("%q", value)
	named := strings.Contains(result.DepthReason, quoted)
	for _, g := range result.Gates {
		if g.Outcome != validate.OutcomeSkipped {
			t.Errorf("gate %s reported %s for a bump whose captured version %s is invalid; want SKIPPED", g.Gate, g.Outcome, quoted)
		}
		if strings.Contains(g.Reason, quoted) {
			named = true
		}
		if strings.Contains(strings.ToLower(g.Reason), "waiting") {
			t.Errorf("gate %s reason reports a wait for an invalid captured version: %q", g.Gate, g.Reason)
		}
	}
	if !named {
		t.Errorf("no gate reason names the refused value %s:\n%+v", quoted, result)
	}
	if strings.Contains(strings.ToLower(fmt.Sprintf("%+v", result)), "waiting") {
		t.Errorf("Validate's result reports a wait for an invalid captured version:\n%+v", result)
	}
	f.assertNothingWritten(t)
	f.assertPendingKept(t, requires)
}
